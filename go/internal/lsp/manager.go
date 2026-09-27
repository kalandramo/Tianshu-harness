package lsp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLSPDocumentBytes 是 didOpen/didChange 文本载荷上限——超过此大小的文件
// 不把内容灌进 server。
//
// 对账 TS `MAX_LSP_DOCUMENT_BYTES = 512 * 1024`。
const maxLSPDocumentBytes = 512 * 1024

// didOpenSettleDelay 是 didOpen 之后等 server 处理通知的固定等待。
//
// 对账 TS：`await new Promise(r => setTimeout(r, 100))`。
const didOpenSettleDelay = 100 * time.Millisecond

// initializeSettleDelay 是握手后等 server 稳定的固定等待。
//
// 对账 TS：`await new Promise(r => setTimeout(r, 200))` 然后才置 ready。
const initializeSettleDelay = 200 * time.Millisecond

// Location 是 LSP 的位置（对账 TS 的 `Location`）。
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Range 是 LSP 的行列区间（**0-based**，与 TS 的 `Location.range` 一致）。
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position 是 0-based 的行列。
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LspDiagnostic 是文件级诊断（TS `LspDiagnostic`）。
//
// **本波不产出它**（诊断回流属 W4，独立于 goto/refs），但接口相位需要该类型
// ——与 TS 的 `getFileDiagnostics(): Promise<LspDiagnostic[]>` 对齐。
type LspDiagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"` // 1=Error 2=Warning 3=Info 4=Hint
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// ServerCapabilities 是 server 自报的能力（只取我们需要判定的两项）。
//
// 对账 TS `ServerCapabilities`（`manager.ts`）。
type ServerCapabilities struct {
	DefinitionProvider bool            `json:"definitionProvider"`
	ReferencesProvider bool            `json:"referencesProvider"`
	DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
}

// spawnFn 造一个 Transport（真实实现是 spawn 子进程并取 stdio）。
//
// 返回 nil 表示 spawn 失败（无管道）——对账 TS 的
// `if (!stdin || !stdout) throw new Error('LSP server spawn failed: no stdio pipes …')`。
type spawnFn func() Transport

// managerOptions 是 manager 的可注入项。
//
// **为什么要有测试观察点**（onDidOpen/onDidChange）：本层的行为核心是
// 「**发什么内容**给 server」——不发 vs 发空 vs 发真实内容，三者后果天差地别
// （TS 注释：发空文本是不可恢复的静默失效）。用真进程测这些要装 language
// server 且拿不到「发了什么」的确定视图；故按 TS 的注入式设计，开两个观察点。
//
// **这些不是测试专用后门**：对账 TS `createLspManager(spawnFn, cwd, rpcOptions,
// languageIdFor)` 本身就全是注入参数；Go 侧沿用同一模式，生产代码传 nil 即可。
type managerOptions struct {
	languageIDFor  func(filePath string) string
	requestTimeout time.Duration

	onDidOpen   func(params json.RawMessage)
	onDidChange func(params json.RawMessage)
}

// manager 是单语言服务器的客户端（对账 TS `createLspManager` 的返回对象）。
type manager struct {
	cwd string
	// spawn 是造 Transport 的缝（真实实现 spawn 子进程）。
	spawn spawnFn
	opts  managerOptions

	mu           sync.Mutex
	rpc          *RPC
	tr           Transport
	capabilities *ServerCapabilities
	ready        bool
	openedDocs   map[string]bool

	// 测试观察点（生产为 nil）
	onDidOpen   func(json.RawMessage)
	onDidChange func(json.RawMessage)
}

// newManager 创建 manager（spawn 缝可注入）。
func newManager(spawn spawnFn, cwd string, opts *managerOptions) *manager {
	o := managerOptions{
		requestTimeout: time.Duration(DefaultRequestTimeoutMS) * time.Millisecond,
	}
	if opts != nil {
		o = *opts
		if o.requestTimeout == 0 {
			o.requestTimeout = time.Duration(DefaultRequestTimeoutMS) * time.Millisecond
		}
	}
	return &manager{
		cwd:         cwd,
		opts:        o,
		openedDocs:  map[string]bool{},
		onDidOpen:   o.onDidOpen,
		onDidChange: o.onDidChange,
		spawn:       spawn,
	}
}

// spawn 保存 spawn 缝（字段而非参数，便于 Initialize 内使用）。
//
// 见 newManager 的赋值。

// Initialize 启动 server 并完成 LSP 握手。
//
// 对账 TS `initialize()`。**开头必须先清空 openedDocs**：
// 全新服务器进程对历史一无所知，不清空会让 didOpen 被旧缓存短路 →
// 新 server 永远收不到那些文档 → 诊断与定义**静默失真**。
func (m *manager) Initialize() error {
	// ★ 先释放上一轮的 RPC（若存在）。
	//
	// **TS 在此处有泄漏**（`rpc = createRpcClient(...)` 直接重新赋值，旧 proc
	// 不杀、旧 rpc 的 data 监听器仍挂在旧 stdout 上）。TS 生产路径绕过了它
	// ——`multi-manager` 的重启是 `mgr.dispose()` + **新建 manager**，不是
	// 复用同一实例。
	//
	// 但 TS 的注释明确把「崩溃后重新 initialize」当作支持的路径
	// （`manager.ts` 的 `openedDocs.clear()` 就是为它写的）。Go 侧若照搬这个
	// 泄漏，**同一 transport 上会有两个 readLoop 抢字节**——新握手的响应被
	// 旧循环偷走 → 请求 45s 超时（本层测试实测就是这个失败）。
	// 故 Go 侧显式释放：语义更正确，且这是**可观测的正确性差异**而非风格偏好。
	m.Dispose()

	m.mu.Lock()
	m.openedDocs = map[string]bool{}
	m.mu.Unlock()

	tr := m.spawn()
	if tr == nil {
		// 对账 TS：`throw new Error('LSP server spawn failed: no stdio pipes (check PATH / npx)')`
		return fmt.Errorf("LSP server spawn failed: no stdio pipes (check PATH / npx)")
	}

	m.mu.Lock()
	m.tr = tr
	m.rpc = NewRPC(tr,
		WithRequestTimeout(m.opts.requestTimeout),
		// 进程/连接死亡 → 置 ready=false。
		//
		// 对账 TS `manager.ts` 的 `proc.on('error')` / `on('exit')` 分支
		// （两者都 `ready = false` 并 abortAllPending）。**这是 multi-manager
		// 的重启判据**——不接的话崩溃的 server 会被永久当成 ready，
		// 定义跳转静默失效且永不恢复（TS 注释明写的事故形态）。
		WithDeathHandler(func(error) {
			m.mu.Lock()
			m.ready = false
			m.mu.Unlock()
		}))
	rpc := m.rpc
	m.mu.Unlock()

	raw, err := rpc.Request(nil, "initialize", map[string]any{
		"processId": os.Getpid(),
		"rootUri":   uriForPath(m.cwd),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"definition": map[string]any{"linkSupport": false},
				"references": map[string]any{},
			},
		},
	}, m.opts.requestTimeout)
	if err != nil {
		m.Dispose()
		return err
	}

	var initResult struct {
		Capabilities ServerCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &initResult); err != nil {
		m.Dispose()
		return err
	}

	m.mu.Lock()
	m.capabilities = &initResult.Capabilities
	m.mu.Unlock()

	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		m.Dispose()
		return err
	}

	// 对账 TS：`await new Promise(r => setTimeout(r, 200)); ready = true`
	time.Sleep(initializeSettleDelay)

	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()
	return nil
}

// IsReady 报告握手是否完成（对账 TS `isReady()`）。
func (m *manager) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

// SupportsDefinition 报告 server 是否支持定义跳转。
//
// 对账 TS：`capabilities?.definitionProvider === true`——**靠 server 自报**，
// 不是本地猜测。`=== true` 意味着 `false` / 缺席 / 非布尔都为「不支持」。
func (m *manager) SupportsDefinition() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capabilities != nil && m.capabilities.DefinitionProvider
}

// SupportsReferences 报告 server 是否支持引用查找。
func (m *manager) SupportsReferences() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capabilities != nil && m.capabilities.ReferencesProvider
}

// ensureDocument 确保文档已在 server 中打开。
//
// 对账 TS `ensureDocument`。**三条关键语义**：
//
//  1. 已 openedDocs 则短路（只发一次 didOpen）
//  2. 读不到或超上限（`readDocumentText` 返回 nil）→ **跳过通知**，
//     **且不 add(uri)**——否则「暂时读不到」被固化成「永久空文档」
//  3. 发的是**真实内容**（多数 server 按 didOpen 的 text 建缓冲，发空文本
//     等于告诉它文件是空的）
func (m *manager) ensureDocument(filePath string) {
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	opened := m.openedDocs[uriForFile(filePath, m.cwd)]
	m.mu.Unlock()

	if rpc == nil || !ready || opened {
		return
	}

	text, ok := readDocumentText(absFromCwd(filePath, m.cwd))
	if !ok {
		return // ★ 不发、不缓存
	}

	uri := uriForFile(filePath, m.cwd)
	m.mu.Lock()
	if m.openedDocs[uri] {
		m.mu.Unlock()
		return
	}
	m.openedDocs[uri] = true
	m.mu.Unlock()

	params := map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": m.languageIDFor(filePath),
			"version":    1,
			// 真实内容（0 字节文件是 ""，属真实内容而非读不到——判据用 ok 不用空判）
			"text": text,
		},
	}
	raw, _ := json.Marshal(params)
	if m.onDidOpen != nil {
		m.onDidOpen(raw)
	}
	_ = rpc.Notify("textDocument/didOpen", params)
	// 对账 TS：`await new Promise(r => setTimeout(r, 100))`
	time.Sleep(didOpenSettleDelay)
}

// languageIDFor 解析该文件的 LSP languageId。
func (m *manager) languageIDFor(filePath string) string {
	if m.opts.languageIDFor != nil {
		return m.opts.languageIDFor(filePath)
	}
	return defaultLanguageID(filePath)
}

// GotoDefinition 跳转到定义。
//
// 对账 TS `gotoDefinition`：
//
//	await ensureDocument(filePath)
//	const result = await rpc.request('textDocument/definition', {
//	  textDocument: { uri }, position: { line: line - 1, character },  // ← 行号减 1
//	})
//	const locations = (Array.isArray(result) ? result : [result])      // ← 非数组包裹
//	return locations.map(loc => ({ ...loc, uri: uriToRelPath(loc.uri) }))
//
// **整个方法体在 try/catch 里**——任何失败都返回空切片（静默降级）。
func (m *manager) GotoDefinition(filePath string, line, character int) ([]Location, error) {
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	m.mu.Unlock()
	if rpc == nil || !ready {
		return nil, nil
	}

	m.ensureDocument(filePath)

	raw, err := rpc.Request(nil, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uriForFile(filePath, m.cwd)},
		"position":     map[string]any{"line": line - 1, "character": character},
	}, m.opts.requestTimeout)
	if err != nil {
		return nil, nil // 对账 TS 的 catch：吞掉，返回空
	}
	return parseLocations(raw, m.cwd, true), nil
}

// FindReferences 查找引用。
//
// 对账 TS `findReferences`——与 GotoDefinition 的**两处不同**：
//
//  1. 带 `context: { includeDeclaration: false }`
//  2. 非数组结果返回**空**（`Array.isArray(result) ? result : []`），不包裹
func (m *manager) FindReferences(filePath string, line, character int) ([]Location, error) {
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	m.mu.Unlock()
	if rpc == nil || !ready {
		return nil, nil
	}

	m.ensureDocument(filePath)

	raw, err := rpc.Request(nil, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uriForFile(filePath, m.cwd)},
		"position":     map[string]any{"line": line - 1, "character": character},
		"context":      map[string]any{"includeDeclaration": false},
	}, m.opts.requestTimeout)
	if err != nil {
		return nil, nil
	}
	return parseLocations(raw, m.cwd, false), nil
}

// ChangeFile 通知 server 文件已在磁盘上被修改。
//
// 对账 TS `changeFile`：
//
//	if (!rpc || !ready) return
//	const uri = fileToUri(filePath)
//	if (!openedDocs.has(uri)) return        // 从未打开 → server 无缓存状态
//	const text = readDocumentText(...)
//	if (text === null) return               // 读不到 → 干脆不发（保持旧缓冲比清空安全）
//	rpc.notify('textDocument/didChange', { textDocument: { uri, version: Date.now() }, contentChanges: [{ text }] })
func (m *manager) ChangeFile(filePath string) {
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	uri := uriForFile(filePath, m.cwd)
	opened := m.openedDocs[uri]
	m.mu.Unlock()
	if rpc == nil || !ready || !opened {
		return
	}

	text, ok := readDocumentText(absFromCwd(filePath, m.cwd))
	if !ok {
		return // 保持 server 现有缓冲
	}

	params := map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": time.Now().UnixMilli()},
		"contentChanges": []any{map[string]any{"text": text}},
	}
	raw, _ := json.Marshal(params)
	if m.onDidChange != nil {
		m.onDidChange(raw)
	}
	_ = rpc.Notify("textDocument/didChange", params)
}

// Dispose 释放资源（对账 TS `dispose()`）。
func (m *manager) Dispose() {
	m.mu.Lock()
	m.ready = false
	rpc := m.rpc
	m.rpc = nil
	m.tr = nil
	m.mu.Unlock()

	if rpc != nil {
		rpc.Dispose()
	}
}

// ── 辅助（对账 TS 的 absFromCwd / fileToUri / uriToRelPath）────────

// absFromCwd 把可能相对的路径解析为绝对路径（对账 TS `absFromCwd`）。
func absFromCwd(filePath, cwd string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(cwd, filePath)
}

// uriForPath 把一个**绝对路径**直接转成 file:// URI（对账 TS 的
// `pathToFileURL(cwd).href`——用于 initialize 的 rootUri）。
func uriForPath(absPath string) string {
	p := filepath.ToSlash(absPath)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows 的 C:/x → /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// uriForFile 构造 file:// URI（对账 TS `fileToUri` 用 `pathToFileURL().href`）。
//
// **Windows 差异**：TS 用 `pathToFileURL` 保证得到 `file:///C:/...` 而非
// 非法的 `file://C:\...`。Go 的 `url.URL{Scheme:"file", Path: …}` 配合
// 正斜杠路径产出同形 URI。
func uriForFile(filePath, cwd string) string {
	abs := absFromCwd(filePath, cwd)
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs // Windows 的 C:/x → /C:/x
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String()
}

// uriToRelPath 把 LSP 的 file:// URI 转成 **cwd 相对、正斜杠** 路径。
//
// 对账 TS `uriToRelPath`：
//
//	const rel = relativePath(cwd, abs)
//	return (rel && !rel.startsWith('..') && !isAbsolute(rel) ? rel : abs).split('\\').join('/')
//
// ——cwd 外（以 `..` 开头）或仍是绝对路径时**保持绝对**。
func uriToRelPath(uri, cwd string) string {
	abs := uriToPath(uri)
	rel, err := filepath.Rel(cwd, abs)
	if err == nil && rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(abs)
}

// uriToPath 把 file:// URI 解回文件系统路径（对账 TS 的 `fileURLToPath`，
// 失败时回退到剥前缀的字符串）。
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return strings.TrimPrefix(uri, "file://")
	}
	p := u.Path
	// Windows：/C:/x → C:/x
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// readDocumentText 读磁盘内容作为 didOpen/didChange 的载荷。
//
// 返回 `(text, true)` 正常；`(_, false)` 表示读不到或超上限——调用方必须
// **跳过通知**。对账 TS `readDocumentText` 的 `null` 语义。
func readDocumentText(absPath string) (string, bool) {
	st, err := os.Stat(absPath)
	if err != nil || st.IsDir() || st.Size() > maxLSPDocumentBytes {
		return "", false
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// defaultLanguageID 是单 server 调用的默认 languageId 解析（TS/JS 家族）。
//
// 对账 TS `defaultLanguageId`。多语言路径由 multi-manager 注入 registry 的
// per-def 解析器覆盖。
func defaultLanguageID(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".tsx"):
		return "typescriptreact"
	case strings.HasSuffix(filePath, ".ts"),
		strings.HasSuffix(filePath, ".mts"),
		strings.HasSuffix(filePath, ".cts"):
		return "typescript"
	case strings.HasSuffix(filePath, ".jsx"):
		return "javascriptreact"
	}
	return "javascript"
}

// parseLocations 解析 server 返回的位置。
//
// `wrapSingle` 区分两种语义（对账 TS 的差异）：
//   - true（gotoDefinition）：非数组结果**包裹成一个元素**
//   - false（findReferences）：非数组结果返回**空**
func parseLocations(raw json.RawMessage, cwd string, wrapSingle bool) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	var arr []Location
	if err := json.Unmarshal(raw, &arr); err == nil {
		return relativize(arr, cwd)
	}

	if !wrapSingle {
		return nil
	}
	var one Location
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil
	}
	return relativize([]Location{one}, cwd)
}

func relativize(locs []Location, cwd string) []Location {
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		l.URI = uriToRelPath(l.URI, cwd)
		out = append(out, l)
	}
	return out
}
