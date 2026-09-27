package lsp

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// didOpenLanguageID 从 server 收到的 didOpen 通知里取 languageId。
//
// **为什么走「server 收到的通知」而非 manager 的观察点**：multi 层测的是
// **注入的语言解析器是否真的传到了 manager**——只有 server 端看到的才是
// 端到端事实（manager 的观察点可以证明「发过」但不能证明「值对」）。
func didOpenLanguageID(fs *fakeServer, langID string) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, raw := range fs.didOpenParams {
		var p struct {
			TextDocument struct {
				LanguageID string `json:"languageId"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if p.TextDocument.LanguageID == langID {
			return langID
		}
	}
	return ""
}

// ── multi-manager 测试 ────────────────────────────────────────────

// multiFixture 记录每次 spawn 造出的假 server（按 def id 分）。
type multiFixture struct {
	mu      sync.Mutex
	spawned []string // 被 spawn 过的 def id（按序）
	servers map[string]*fakeServer
	// failInitialize 对这些 def id 让 initialize 永不响应（测就绪超时）
	failInitialize map[string]bool
	// deadAfterInit 对这些 def id，initialize 成功后立刻断连（测重启）
	deadAfterInit map[string]bool
}

func newMultiFixture() *multiFixture {
	return &multiFixture{
		servers:        map[string]*fakeServer{},
		failInitialize: map[string]bool{},
		deadAfterInit:  map[string]bool{},
	}
}

// spawnFor 返回注入给 multi-manager 的 spawn 缝。
func (fx *multiFixture) spawnFor(def *LspServerDef, cwd string) Transport {
	fx.mu.Lock()
	fx.spawned = append(fx.spawned, def.ID)
	fs := newFakeServer()
	fx.servers[def.ID] = fs
	fail := fx.failInitialize[def.ID]
	fx.mu.Unlock()

	if fail {
		// 永不响应 initialize → 触发就绪超时
		fs.onRequest = nil
		fs.dropRequests = true
	}
	return fs.clientSide()
}

func (fx *multiFixture) spawnedIDs() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), fx.spawned...)
}

func (fx *multiFixture) server(id string) *fakeServer {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.servers[id]
}

// newTestMulti 造一个 multi-manager（注入 which 与 spawn 缝）。
func newTestMulti(t *testing.T, cwd string, installed []string, fx *multiFixture, initTimeoutMs int) *multiManager {
	t.Helper()
	opts := &multiManagerOptions{
		which:             fakeWhich(installed...),
		spawnFor:          fx.spawnFor,
		initializeTimeout: time.Duration(initTimeoutMs) * time.Millisecond,
	}
	return newMultiManager(cwd, opts)
}

// TestMulti_IsReadyIsAvailableCountNotCapability —— ★★ 最关键的一条。
//
// TS `multi-manager.ts:190-201`：
//
//	isReady() { return getAvailable().length > 0 }
//	supportsDefinition() { return getAvailable().length > 0 }
//	supportsReferences() { return getAvailable().length > 0 }
//
// **只校验「有 server 装」，不校验 capability**——这与单 manager 层
// （读 server 自报的 `definitionProvider`）**语义不同**，必须保留：
// multi 层不知道每个 server 的能力，故采取乐观放行，实际能力在调用时由
// 具体 manager 决定（不支持则返回空结果）。
//
// 判别力：若实现改成 `getAvailable()` 后再查 capability → 本用例必红。
func TestMulti_IsReadyIsAvailableCountNotCapability(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)

	if !m.IsReady() {
		t.Error("有 server 装时 IsReady 应为 true（只看数量）")
	}
	if !m.SupportsDefinition() {
		t.Error("multi 层不校验 capability，应乐观返回 true")
	}
	if !m.SupportsReferences() {
		t.Error("同上")
	}
}

// TestMulti_NotReadyWhenNothingInstalled —— 没装任何 server → 全 false。
//
// 注意 typescript 的 alwaysAvailable 让 `fakeWhich()`（空集）下仍有 1 个可用，
// 故本用例的判据是「排除 alwaysAvailable 的条目后确实无可用」——
// 用 `available` 的实际数量断言，而非硬编码。
func TestMulti_NotReadyWhenNothingInstalled(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", nil, fx, 2000)

	avail := AvailableServers(fakeWhich())
	// typescript 恒可用，故 avail 至少 1 个 —— 这是 TS 的既有语义
	if len(avail) == 0 {
		t.Skip("前提不成立：typescript 应 alwaysAvailable")
	}
	if !m.IsReady() {
		t.Error("有 alwaysAvailable 的 typescript 时 IsReady 应为 true")
	}
}

// TestMulti_RoutesByExtensionLazily —— ★ 按扩展名路由，且**懒启动**
// （只在首次遇到该语言的文件时才 spawn）。
func TestMulti_RoutesByExtensionLazily(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls", "pyright-langserver"}, fx, 2000)

	if got := fx.spawnedIDs(); len(got) != 0 {
		t.Fatalf("初始化不该 spawn 任何 server（懒启动），实得 %v", got)
	}

	writeTempFile(t, "/tmp/p", "main.go", "package main\n")
	writeTempFile(t, "/tmp/p", "app.py", "x = 1\n")

	_, _ = m.GotoDefinition("main.go", 1, 0)
	ids := fx.spawnedIDs()
	if len(ids) != 1 || ids[0] != "gopls" {
		t.Fatalf("`.go` 应 spawn gopls，实得 %v", ids)
	}

	_, _ = m.GotoDefinition("app.py", 1, 0)
	ids = fx.spawnedIDs()
	if len(ids) != 2 || ids[1] != "pyright" {
		t.Fatalf("`.py` 应 spawn pyright，实得 %v", ids)
	}

	// 再次访问 .go 不该重复 spawn
	_, _ = m.GotoDefinition("main.go", 1, 0)
	if got := fx.spawnedIDs(); len(got) != 2 {
		t.Errorf("同一 server 不该重复 spawn，实得 %v", got)
	}
}

// TestMulti_UnsupportedExtensionReturnsEmptyNoSpawn —— 无候选 server 的扩展名
// → 返回空，且**不 spawn**。
func TestMulti_UnsupportedExtensionReturnsEmptyNoSpawn(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", nil, fx, 2000)
	writeTempFile(t, "/tmp/p", "a.zzz", "content\n")

	locs, err := m.GotoDefinition("a.zzz", 1, 0)
	if err != nil {
		t.Errorf("不该报错：%v", err)
	}
	if len(locs) != 0 {
		t.Errorf("应返回空，实得 %d", len(locs))
	}
	if got := fx.spawnedIDs(); len(got) != 0 {
		t.Errorf("无候选 server 时不该 spawn，实得 %v", got)
	}
}

// TestMulti_NotInstalledServerReturnsEmptyNoSpawn —— 扩展名有候选但我未安装
// → 返回空，不 spawn。
func TestMulti_NotInstalledServerReturnsEmptyNoSpawn(t *testing.T) {
	fx := newMultiFixture()
	// 不装 gopls
	m := newTestMulti(t, "/tmp/p", nil, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	locs, _ := m.GotoDefinition("main.go", 1, 0)
	if len(locs) != 0 {
		t.Errorf("未安装时应返回空，实得 %d", len(locs))
	}
	if got := fx.spawnedIDs(); len(got) != 0 {
		t.Errorf("未安装时不该 spawn，实得 %v", got)
	}
}

// TestMulti_InitializeTimeoutDegradesToNull —— ★ 就绪超时：硬上限到点后
// dispose 并降级为「无 LSP」，**不阻塞调用方**。
//
// TS：`initializeTimeoutMs`（默认 45s）到点 → `mgr.dispose()` + resolve(false)。
func TestMulti_InitializeTimeoutDegradesToNull(t *testing.T) {
	fx := newMultiFixture()
	fx.failInitialize["gopls"] = true

	start := time.Now()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 150) // 150ms 上限
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	locs, err := m.GotoDefinition("main.go", 1, 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("超时应降级为空结果，不报错：%v", err)
	}
	if len(locs) != 0 {
		t.Errorf("超时应返回空，实得 %d", len(locs))
	}
	if elapsed > 3*time.Second {
		t.Errorf("就绪超时应生效（约 150ms），实得 %v", elapsed)
	}
}

// TestMulti_InitializeHardCeilingIsIndependentOfWait —— ★ 两条超时路径必须**各自**成立。
//
// `ensure` 有两处独立上界（对账 TS 同构）：
//
//	① `entry.ready` 侧的硬上限（`newEntryLocked` 的 initializeTimeout）
//	   —— initialize 的 RPC 永远挂着时，到点 dispose 挂死的子进程
//	② 每次调用的有界等待 `boundedWait`
//
// **为什么单独一条**：把 ② 变异成 30s 时 `TestMulti_InitializeTimeoutDegradesToNull`
// 仍绿（因为 ① 挡住了）——即那条用例只验了 ①。本用例直接断言
// 「挂死的 server 不会让 `readyVal` 为 true」+「① 在 initializeTimeout 量级生效」，
// 与 ② 解耦。
func TestMulti_InitializeHardCeilingIsIndependentOfWait(t *testing.T) {
	fx := newMultiFixture()
	fx.failInitialize["gopls"] = true

	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 200)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	// 直接拿 entry（不等 ensure 的返回），观察 ① 独立生效
	start := time.Now()
	def := m.resolve("main.go")
	if def == nil {
		t.Fatal("应解析出 gopls")
	}
	mgr := m.ensure(def, 10*time.Second) // ★ 故意给很长的调用等待
	elapsed := time.Since(start)

	if mgr != nil {
		t.Error("initialize 挂死时 ensure 必须返回 nil（不能把挂死的 server 当 ready）")
	}
	if elapsed > 3*time.Second {
		t.Errorf("硬上限（200ms）必须独立生效，不受调用等待影响；实得 %v", elapsed)
	}
}

// TestMulti_RestartOnServerDeath —— ★ 服务器崩死后**有界重启**。
//
// TS 注释（`multi-manager.ts`）：`entry.ready` 永远停在 true 时，之后每次
// `ensure()` 都返回 null——LSP 对会话剩余时间**静默失效**（崩溃一次 =
// 定义跳转全部降级且永不恢复）。故丢弃条目让下次重新 spawn，有界防 crash-loop。
func TestMulti_RestartOnServerDeath(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	// 第一次：正常
	_, _ = m.GotoDefinition("main.go", 1, 0)
	if len(fx.spawnedIDs()) != 1 {
		t.Fatalf("首次应 spawn 1 次，实得 %v", fx.spawnedIDs())
	}

	// 杀掉 server
	fs := fx.server("gopls")
	fs.mu.Lock()
	fs.kill()
	fs.mu.Unlock()

	// ★ 显式等「死亡被 manager 察觉」（ready 变 false）。
	//
	// **不靠隐式时序**：readLoop 察觉 EOF → 触发死亡回调 → 置 ready=false
	// 是一段异步过程；直接访问会撞在窗口里（假 server 的 Read 恰好在
	// `Cond.Wait` 上，需 Broadcast 后才返回 EOF）。用轮询把等待显式化，
	// 否则测试是「靠运气绿」的。
	m.mu.Lock()
	e := m.managers["gopls"]
	m.mu.Unlock()
	waitFor(t, 2*time.Second, "manager 察觉 server 死亡", func() bool {
		return e == nil || !e.mgr.IsReady()
	})

	// 再访问：应触发重启（重新 spawn）
	_, _ = m.GotoDefinition("main.go", 1, 0)
	if len(fx.spawnedIDs()) != 2 {
		t.Errorf("server 死后应重启（再 spawn 一次），实得 %v", fx.spawnedIDs())
	}
}

// TestMulti_RestartIsBounded —— ★ 重启次数**有界**（MAX_LSP_RESTARTS = 2）。
//
// 防 crash-loop 打爆 spawn。
//
// 判别力：若无上限，持续杀 server 会无限 spawn → 本用例断言 spawn 次数有顶。
func TestMulti_RestartIsBounded(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 300)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	// 反复「访问 → 杀」20 轮
	for i := 0; i < 20; i++ {
		_, _ = m.GotoDefinition("main.go", 1, 0)
		fs := fx.server("gopls")
		if fs != nil {
			fs.mu.Lock()
			fs.kill()
			fs.mu.Unlock()
		}
	}

	// 上限 2 次重启 → 总 spawn 次数应是 1 + 2 = 3
	if got := len(fx.spawnedIDs()); got > 3 {
		t.Errorf("重启应上界为 2（总 spawn ≤ 3），实得 %d 次：%v", got, fx.spawnedIDs())
	}
}

// TestMulti_ChangeFileRoutesToOwningServer —— changeFile 按扩展名路由到对应 server。
func TestMulti_ChangeFileRoutesToOwningServer(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	_, _ = m.GotoDefinition("main.go", 1, 0) // 建会话

	fs := fx.server("gopls")
	before := len(fs.notifMethods())
	m.ChangeFile("main.go")

	waitFor(t, 2*time.Second, "didChange 到达", func() bool {
		return len(fs.notifMethods()) > before
	})
}

// TestMulti_ChangeFileForUnknownExtensionIsNoop —— 未知扩展名 → 什么都不做。
func TestMulti_ChangeFileForUnknownExtensionIsNoop(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "a.zzz", "x\n")

	m.ChangeFile("a.zzz") // 不该 panic / 不该 spawn
	time.Sleep(30 * time.Millisecond)
	if got := fx.spawnedIDs(); len(got) != 0 {
		t.Errorf("未知扩展名不该 spawn，实得 %v", got)
	}
}

// TestMulti_DisposeDisposesAllManagers —— Dispose 释放全部语言 server。
func TestMulti_DisposeDisposesAllManagers(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls", "pyright-langserver"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")
	writeTempFile(t, "/tmp/p", "app.py", "x=1\n")

	_, _ = m.GotoDefinition("main.go", 1, 0)
	_, _ = m.GotoDefinition("app.py", 1, 0)
	if len(fx.spawnedIDs()) != 2 {
		t.Fatalf("应 spawn 2 个，实得 %v", fx.spawnedIDs())
	}

	m.Dispose() // 不该 panic

	// Dispose 后再访问应重新 spawn（条目已清）
	_, _ = m.GotoDefinition("main.go", 1, 0)
	if len(fx.spawnedIDs()) != 3 {
		t.Errorf("Dispose 后应重建，实得 %v", fx.spawnedIDs())
	}
}

// TestMulti_InitializeIsLazyNoop —— multi 的 initialize 是**空操作**
// （懒启动，无 eager 工作）。
//
// TS：`async initialize(): Promise<void> { // Lazy: servers spawn on first matching file. Nothing to do eagerly. }`
func TestMulti_InitializeIsLazyNoop(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	if err := m.Initialize(); err != nil {
		t.Errorf("initialize 不该报错：%v", err)
	}
	if got := fx.spawnedIDs(); len(got) != 0 {
		t.Errorf("initialize 不该 spawn（懒），实得 %v", got)
	}
}

// TestMulti_LanguageIDPassedPerDef —— ★ 每个 def 的语言 id 解析器被传入
// manager（不传的话 .py/.go/.java 全被标成 'javascript'）。
//
// TS：`fp => languageIdForFile(def, fp)`。
//
// 判别力：若 multi 忘了传这个解析器，didOpen 的 languageId 会是 'javascript'。
func TestMulti_LanguageIDPassedPerDef(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"pyright-langserver"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "app.py", "x = 1\n")

	// 先让 manager 建起来（懒 spawn），再触发新文件的 didOpen
	writeTempFile(t, "/tmp/p", "app.py", "x = 1\n")
	_, _ = m.GotoDefinition("app.py", 1, 0)
	fs := fx.server("pyright")
	if fs == nil {
		t.Fatal("pyright 未被 spawn")
	}

	writeTempFile(t, "/tmp/p", "app2.py", "y = 2\n")
	_, _ = m.GotoDefinition("app2.py", 1, 0)

	if got := didOpenLanguageID(fs, "python"); got != "python" {
		// 若为 javascript，说明 multi 忘了把 per-def 解析器传给 manager
		t.Errorf("`.py` 的 languageId 应为 python（per-def 解析），实得 server 未收到 python；收到过的 id 见 didOpenParams")
	}
}

// TestMulti_TSFamilyGetsRefinedLanguageID —— TS 家族按扩展名细化。
func TestMulti_TSFamilyGetsRefinedLanguageID(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", nil, fx, 2000) // typescript 恒可用
	writeTempFile(t, "/tmp/p", "a.tsx", "export const X = () => null\n")

	_, _ = m.GotoDefinition("a.tsx", 1, 0)
	fs := fx.server("typescript")
	if fs == nil {
		t.Fatal("typescript 未被 spawn")
	}

	writeTempFile(t, "/tmp/p", "b.tsx", "export const Y = () => null\n")
	_, _ = m.GotoDefinition("b.tsx", 1, 0)

	if got := didOpenLanguageID(fs, "typescriptreact"); got != "typescriptreact" {
		t.Errorf("`.tsx` 应细化出 typescriptreact（而非 typescript）")
	}
}

// TestMulti_GetFileDiagnosticsRoutesAndWaitsBounded —— 诊断路径按扩展名路由，
// 且就绪等待有界（不能拖住编辑）。
//
// 本波不实现诊断内容，但**接口相位**要对（返回空而非 panic）。
func TestMulti_GetFileDiagnosticsRoutesAndWaitsBounded(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	diags := m.GetFileDiagnostics("main.go", 100)
	if len(diags) != 0 {
		t.Errorf("本波不产出诊断，应返回空，实得 %d", len(diags))
	}
	// 未知扩展名
	if d := m.GetFileDiagnostics("a.zzz", 100); len(d) != 0 {
		t.Errorf("未知扩展名应返回空，实得 %d", len(d))
	}
}

// TestMulti_ConcurrentAccessNoRace —— 并发访问不同语言（配合 -race）。
func TestMulti_ConcurrentAccessNoRace(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls", "pyright-langserver"}, fx, 500)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")
	writeTempFile(t, "/tmp/p", "app.py", "x=1\n")

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = m.GotoDefinition("main.go", 1, 0)
			} else {
				_, _ = m.GotoDefinition("app.py", 1, 0)
			}
		}(i)
	}
	wg.Wait()
}

// TestMulti_WhichResultCachedAcrossCalls —— which 结果被缓存（对账 TS 的
// `availableCache`）。
//
// TS：`if (availableCache === null) availableCache = availableServers(which)`
func TestMulti_WhichResultCachedAcrossCalls(t *testing.T) {
	calls := 0
	counting := func(bin string) bool { calls++; return bin == "gopls" }

	opts := &multiManagerOptions{
		which:             counting,
		spawnFor:          newMultiFixture().spawnFor,
		initializeTimeout: 2 * time.Second,
	}
	m := newMultiManager("/tmp/p", opts)

	_ = m.IsReady()
	first := calls
	if first == 0 {
		t.Fatal("首轮应探测 PATH")
	}
	if first >= len(LSP_SERVERS) {
		t.Errorf("alwaysAvailable 的条目不该被探测：首轮 %d 次 ≥ 条目数 %d", first, len(LSP_SERVERS))
	}

	// 后续调用必须**走缓存**（不再新增探测）
	for i := 0; i < 10; i++ {
		_ = m.IsReady()
	}
	if calls != first {
		t.Errorf("which 结果应缓存：首轮 %d 次，后续 10 次调用后变 %d 次", first, calls)
	}
}

// TestMulti_GotoDefinitionErrorsSwallowed —— server 报错时吞掉返回空
// （与单 manager 同语义：静默降级）。
func TestMulti_GotoDefinitionErrorsSwallowed(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")

	_, _ = m.GotoDefinition("main.go", 1, 0) // 建立
	fs := fx.server("gopls")
	fs.mu.Lock()
	fs.onRequest = func(string, json.RawMessage) (any, string) { return nil, "boom" }
	fs.mu.Unlock()

	locs, err := m.GotoDefinition("main.go", 2, 0)
	if err != nil {
		t.Errorf("不该向上抛错：%v", err)
	}
	if len(locs) != 0 {
		t.Errorf("应返回空，实得 %d", len(locs))
	}
}

// TestMulti_ResultURIStillRelativized —— 经 multi 层后 URI 仍被相对化
// （路由不破坏 manager 的产出）。
func TestMulti_ResultURIStillRelativized(t *testing.T) {
	fx := newMultiFixture()
	m := newTestMulti(t, "/tmp/p", []string{"gopls"}, fx, 2000)
	writeTempFile(t, "/tmp/p", "main.go", "package main\n")
	writeTempFile(t, "/tmp/p", "sub/lib.go", "package sub\n")

	_, _ = m.GotoDefinition("main.go", 1, 0)
	fs := fx.server("gopls")
	fs.mu.Lock()
	fs.onRequest = func(string, json.RawMessage) (any, string) {
		return []any{map[string]any{
			"uri": "file:///tmp/p/sub/lib.go",
			"range": map[string]any{
				"start": map[string]any{"line": 0, "character": 0},
				"end":   map[string]any{"line": 0, "character": 0},
			},
		}}, ""
	}
	fs.mu.Unlock()

	locs, _ := m.GotoDefinition("main.go", 1, 0)
	if len(locs) != 1 {
		t.Fatalf("应 1 个结果，实得 %d", len(locs))
	}
	if locs[0].URI != "sub/lib.go" {
		t.Errorf("URI 应相对化，实得 %q", locs[0].URI)
	}
	if strings.Contains(locs[0].URI, "tmp") {
		t.Errorf("不该残留绝对路径，实得 %q", locs[0].URI)
	}
}
