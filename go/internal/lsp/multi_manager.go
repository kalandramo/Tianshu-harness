package lsp

import (
	"sync"
	"time"
)

// DefaultInitializeTimeoutMS 是懒启动的单 server 初始化**硬上限**。
//
// 对账 TS `DEFAULT_LSP_INITIALIZE_TIMEOUT_MS = 45_000`。
// 超时后 dispose 挂死的子进程并降级为「无 LSP」，而不是**卡住整个回合**
// （TS 注释：`instead of wedging the turn`）。
const DefaultInitializeTimeoutMS = 45_000

// DiagnosticReadyWaitMS 是「编辑后诊断」对冷启动 server 的最短等待。
//
// 对账 TS `LSP_DIAGNOSTIC_READY_WAIT_MS = 2_000`——诊断是尽力而为，
// 不让慢冷启动阻塞工具结果（TS 的 2026-09-08 wedge 事故）。
const DiagnosticReadyWaitMS = 2_000

// maxLSPRestarts 是服务器崩死后允许的重启次数（有界，防 crash-loop 无限 spawn）。
//
// 对账 TS `MAX_LSP_RESTARTS = 2`。
const maxLSPRestarts = 2

// multiManagerOptions 是 multi-manager 的可注入项。
type multiManagerOptions struct {
	which             WhichFunc
	spawnFor          func(def *LspServerDef, cwd string) Transport
	initializeTimeout time.Duration
}

// lspEntry 是一个已 spawn 的语言服务器条目。
type lspEntry struct {
	mgr *manager
	// readyDone 在 initialize 完成（成功或失败）时**关闭**。
	//
	// **为什么不用 `chan bool`**：TS 的 `entry.ready` 是 Promise——
	// **可多次 await 且恒返回同一值**。Go 的 channel 是一次性消费：读走后
	// 再读只会阻塞到超时（本层测试实测：崩溃重启路径因此静默失效，
	// 表现为「server 死了却不重启」）。故用「关闭的 channel 表示已完成」+
	// 独立字段存值，语义与 Promise 等价。
	readyDone chan struct{}
	// readyVal 是 initialize 的终态（写于 close(readyDone) 之前，读时通道已关
	// 故无竞争）。
	readyVal bool
}

// multiManager 按文件扩展名把请求路由到对应语言服务器。
//
// 对账 TS `createMultiLspManager`。
//
// **存在理由**（TS 文件头）：多语言项目按扩展名路由到不同 server，
// 懒启动藏在**同一接口**后——这样 late-bound 的 `getLspManager()` getter
// 与所有调用点都不用改。
type multiManager struct {
	cwd  string
	opts multiManagerOptions

	mu             sync.Mutex
	managers       map[string]*lspEntry
	restartCounts  map[string]int
	availableCache []LspServerDef
	cacheFilled    bool
}

// newMultiManager 创建 multi-manager。
func newMultiManager(cwd string, opts *multiManagerOptions) *multiManager {
	o := multiManagerOptions{
		which:             DefaultWhich,
		initializeTimeout: time.Duration(DefaultInitializeTimeoutMS) * time.Millisecond,
	}
	if opts != nil {
		if opts.which != nil {
			o.which = opts.which
		}
		if opts.spawnFor != nil {
			o.spawnFor = opts.spawnFor
		}
		if opts.initializeTimeout > 0 {
			o.initializeTimeout = opts.initializeTimeout
		}
	}
	return &multiManager{
		cwd:           cwd,
		opts:          o,
		managers:      map[string]*lspEntry{},
		restartCounts: map[string]int{},
	}
}

// available 返回本机已装 server（结果缓存——对账 TS 的 `availableCache`）。
//
// TS 的注入点是 `opts.which`；`serverForFile` 路径由本层缓存兜住。
func (m *multiManager) available() []LspServerDef {
	m.mu.Lock()
	if m.cacheFilled {
		cached := m.availableCache
		m.mu.Unlock()
		return cached
	}
	m.mu.Unlock()

	fresh := AvailableServers(m.opts.which)

	m.mu.Lock()
	m.availableCache = fresh
	m.cacheFilled = true
	m.mu.Unlock()
	return fresh
}

// Initialize 是**空操作**——懒启动，无 eager 工作。
//
// 对账 TS：`async initialize(): Promise<void> { // Lazy: servers spawn on first matching file. Nothing to do eagerly. }`
func (m *multiManager) Initialize() error { return nil }

// IsReady 报告**是否有任何已装 server**。
//
// 对账 TS：`isReady() { return getAvailable().length > 0 }`
//
// ★ **不校验 capability**——与单 manager 层（读 server 自报的
// `definitionProvider`）语义**不同**。multi 层不知道每个 server 的能力，
// 故乐观放行；实际能力在调用时由具体 manager 决定（不支持则返回空结果）。
func (m *multiManager) IsReady() bool { return len(m.available()) > 0 }

// SupportsDefinition 同 IsReady（见其说明）。
func (m *multiManager) SupportsDefinition() bool { return len(m.available()) > 0 }

// SupportsReferences 同 IsReady（见其说明）。
func (m *multiManager) SupportsReferences() bool { return len(m.available()) > 0 }

// resolve 按扩展名选 server（对账 TS 的 `resolve`）。
//
// ★ **必须走缓存的 available()**，不能直接 `ServerForFile(…)`——后者每次都会
// 重新探测 PATH（TS 的 `whichCache` 只缓探测结果，而 `availableServers()` 的
// 过滤仍要遍历 26 条）。TS 侧 `serverForFile` 与 `availableServers` 各自带
// 缓存；Go 侧把「已装集合」缓在本层，`resolve` 从它里面筛——这样一次会话里
// PATH 只探一轮（本层测试实测：不缓存会探 25 次）。
func (m *multiManager) resolve(filePath string) *LspServerDef {
	ext := extOf(filePath)
	for _, def := range m.available() {
		for _, e := range def.Extensions {
			if e == ext {
				d := def
				return &d
			}
		}
	}
	return nil
}

// ensure 懒 spawn + 初始化一个 server，带**两个独立上界**：
//
//   - `ready` 在 `initializeTimeout` 处硬停 initialize()、dispose 挂死的子进程、
//     并 resolve false（LSP 降级为空）
//   - 每次调用可用更短的 `waitMs` 与 `ready` 竞速，慢 server 在后台继续初始化
//
// 对账 TS `ensure`。
func (m *multiManager) ensure(def *LspServerDef, waitMs time.Duration) *manager {
	m.mu.Lock()
	entry, exists := m.managers[def.ID]
	if !exists {
		entry = m.newEntryLocked(def)
		m.managers[def.ID] = entry
	}
	m.mu.Unlock()

	boundedWait := m.opts.initializeTimeout
	if waitMs > 0 {
		boundedWait = waitMs
	}

	// 等 readyDone 关闭（= 初始化结束）或本次的有界等待到点。
	// **关通道语义**：已关时立刻返回（并读到 readyVal），等价于 TS 的
	// `await entry.ready` 对已 resolve 的 Promise 立即返回——这正是不用
	// `chan bool` 的原因（那个会阻塞到超时）。
	ok := false
	select {
	case <-entry.readyDone:
		ok = entry.readyVal
	case <-time.After(boundedWait):
		ok = false
	}

	// ★ 服务器崩死后有界重启。
	//
	// TS 注释：`entry.ready` 永远停在 true 时，之后每次 `ensure()` 都返回 null
	// ——LSP 对会话剩余时间**静默失效**（崩溃一次 = 定义跳转全部降级且永不恢复）。
	// 故丢弃条目让下次重新 spawn；有界重试防 crash-loop 打爆 spawn。
	if ok && !entry.mgr.IsReady() {
		m.mu.Lock()
		restarts := m.restartCounts[def.ID] + 1
		m.restartCounts[def.ID] = restarts
		if restarts <= maxLSPRestarts {
			entry.mgr.Dispose()
			delete(m.managers, def.ID)
			m.mu.Unlock()
			return m.ensure(def, waitMs)
		}
		m.mu.Unlock()
	}

	if ok && entry.mgr.IsReady() {
		return entry.mgr
	}
	return nil
}

// newEntryLocked 造一个新条目并启动后台初始化（调用方须持有 m.mu）。
func (m *multiManager) newEntryLocked(def *LspServerDef) *lspEntry {
	// languageId 由 def 决定（并按扩展名细化）——不传时 manager 会回落到
	// TS/JS 家族解析，把 .py/.go/.java 全标成 'javascript'。
	opts := &managerOptions{
		languageIDFor: func(fp string) string { return LanguageIDForFile(def, fp) },
	}
	spawn := func() Transport {
		if m.opts.spawnFor == nil {
			return nil
		}
		return m.opts.spawnFor(def, m.cwd)
	}
	mgr := newManager(spawn, m.cwd, opts)

	e := &lspEntry{mgr: mgr, readyDone: make(chan struct{})}
	go func() {
		initDone := make(chan struct{})
		go func() {
			_ = mgr.Initialize()
			close(initDone)
		}()
		var ok bool
		select {
		case <-initDone:
			ok = mgr.IsReady()
		case <-time.After(m.opts.initializeTimeout):
			// 硬上限：initialize 的 RPC 可能永远挂着（spawn 卡死 / 进程已死）。
			// dispose 会把在飞请求 reject 掉并释放子进程。
			mgr.Dispose()
			ok = false
		}
		e.readyVal = ok
		close(e.readyDone) // close 即「已完成」，且**可被无限次读到**
	}()
	return e
}

// GotoDefinition 按扩展名路由到对应 server。
func (m *multiManager) GotoDefinition(filePath string, line, character int) ([]Location, error) {
	def := m.resolve(filePath)
	if def == nil {
		return nil, nil
	}
	mgr := m.ensure(def, m.opts.initializeTimeout)
	if mgr == nil {
		return nil, nil
	}
	return mgr.GotoDefinition(filePath, line, character)
}

// FindReferences 按扩展名路由到对应 server。
func (m *multiManager) FindReferences(filePath string, line, character int) ([]Location, error) {
	def := m.resolve(filePath)
	if def == nil {
		return nil, nil
	}
	mgr := m.ensure(def, m.opts.initializeTimeout)
	if mgr == nil {
		return nil, nil
	}
	return mgr.FindReferences(filePath, line, character)
}

// ChangeFile 通知所属 server 文件已改（尽力而为，异步）。
//
// 对账 TS：`void ensure(def).then(mgr => mgr?.changeFile(filePath)).catch(() => {})`
func (m *multiManager) ChangeFile(filePath string) {
	def := m.resolve(filePath)
	if def == nil {
		return
	}
	mgr := m.ensure(def, m.opts.initializeTimeout)
	if mgr != nil {
		mgr.ChangeFile(filePath)
	}
}

// GetFileDiagnostics 按扩展名路由；就绪等待**有界**（不拖住编辑）。
//
// 本波不实现诊断内容（属 W4，独立于 goto/refs），返回空切片。
// 保留方法是为了让接口相位与 TS 对齐（调用方无需按波次分支）。
func (m *multiManager) GetFileDiagnostics(filePath string, timeoutMS int) []LspDiagnostic {
	def := m.resolve(filePath)
	if def == nil {
		return nil
	}
	wait := time.Duration(DiagnosticReadyWaitMS) * time.Millisecond
	if timeoutMS > 0 {
		candidate := time.Duration(timeoutMS) * time.Millisecond
		if candidate < wait {
			wait = candidate
		}
	}
	// 对账 TS：诊断路径只用**有界**等待，慢冷启动不阻塞工具结果
	_ = m.ensure(def, wait)
	return nil
}

// Dispose 释放全部语言 server。
func (m *multiManager) Dispose() {
	m.mu.Lock()
	entries := make([]*lspEntry, 0, len(m.managers))
	for _, e := range m.managers {
		entries = append(entries, e)
	}
	m.managers = map[string]*lspEntry{}
	m.mu.Unlock()

	for _, e := range entries {
		e.mgr.Dispose()
	}
}
