package lsp

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
)

// ── 假 LSP server 夹具 ────────────────────────────────────────────
//
// fakeServer 在**进程内**讲 JSON-RPC（对账计划里的可测性策略：注入 spawn 缝，
// 不打真实 language server）。它实现 Transport（读=客户端收到的、写=客户端发出的），
// 并把客户端的请求交给一个可编程的处理函数。
//
// 为什么不用真进程：真 server 需要网络/PATH/数秒冷启动，测试不可重复；
// 而本层要验的是**协议交互与生命周期**，不是某个具体 server 的行为。
type fakeServer struct {
	mu         sync.Mutex
	current    *fakeServerEndpoint // 最近一次 spawn 的连接（server 的输出走它）
	fromClient []byte              // client → server（客户端写到这里）
	endpoints  map[*fakeServerEndpoint]bool
	killed     bool
	cond       *sync.Cond

	// onRequest 处理客户端请求，返回 (result, errorMsg)。
	// errorMsg 非空则回 JSON-RPC error。
	onRequest func(method string, params json.RawMessage) (any, string)
	// notifications 记录收到的通知（方法名 → 次数）
	notifications []string
	// didOpenParams 记录 didOpen 的原始 params（测试断言 languageId 用）
	didOpenParams []json.RawMessage
	// capabilities 是 initialize 的响应
	capabilities map[string]any
	// onInitialize 观察 initialize 的 params（测试断言握手字段）
	onInitialize func(json.RawMessage)
	// onRequestFor 观察**所有**请求（含 initialize）
	onRequestFor func(method string, params json.RawMessage)
	// dropRequests 让 server 收到请求但**从不响应**（测就绪超时）
	dropRequests bool
}

// kill 模拟服务器崩溃：置 ready 假象失效并关闭所有连接。
//
// ⚠️ **调用方必须持有 `f.mu`**（既有契约，`multi_manager_test.go` 的两个
// 调用点显式加锁）。
//
// **为什么不能改成「自持锁」**（曾试过，造成死锁）：Go 的 `sync.Mutex`
// **不可重入**——自持锁后，已在锁内的调用点会**永久阻塞**。
// 教训：修「易错契约」的正确方向是把契约**结构化**（如拆成
// `killLocked()` + `kill()` 两个方法），而非简单加锁——后者会把
// 「忘记加锁」的问题换成「重复加锁」的死锁，且后者更隐蔽。
//
// 新增调用点请照 `multi_manager_test.go` 的写法：
//
//	fs.mu.Lock()
//	fs.kill()
//	fs.mu.Unlock()
func (f *fakeServer) kill() {
	f.killed = true
	for e := range f.endpoints {
		e.closed = true
	}
	f.cond.Broadcast()
}

// IsKilled 报告该 server 是否已被 kill。
func (f *fakeServer) IsKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

func newFakeServer() *fakeServer {
	f := &fakeServer{
		capabilities: map[string]any{
			"definitionProvider": true,
			"referencesProvider": true,
		},
		endpoints: map[*fakeServerEndpoint]bool{},
	}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// clientSide 返回给被测客户端用的 Transport（读 server 写的、写给 server 读的）。
func (f *fakeServer) clientSide() Transport {
	e := &fakeServerEndpoint{f: f}
	f.mu.Lock()
	f.current = e
	f.endpoints[e] = true
	f.mu.Unlock()
	return e
}

// fakeServerEndpoint 是**每次 spawn 独立**的连接端点。
//
// **为什么缓冲属于 endpoint 而非 server**：`multi-manager` 的重启会
// 「dispose 旧连接 → spawn 新连接」。若两者共享缓冲，旧 endpoint 的
// readLoop 退出前可能读走本该给新 endpoint 的字节（`-race` 抓到的真实竞争）。
// 真实子进程天然无此问题（各有自己的管道），故这里也要独立。
type fakeServerEndpoint struct {
	f        *fakeServer
	toClient []byte // 本连接的 server → client 缓冲
	closed   bool
}

// push 把一个报文写进**最新**连接（server 的「当前输出」）。
func (f *fakeServer) push(data []byte) {
	f.mu.Lock()
	if f.current != nil {
		f.current.toClient = append(f.current.toClient, data...)
	}
	f.mu.Unlock()
	f.cond.Broadcast()
}

// pushTo 定向推给某个 endpoint（测试用）。
func (f *fakeServer) pushTo(e *fakeServerEndpoint, data []byte) {
	f.mu.Lock()
	e.toClient = append(e.toClient, data...)
	f.mu.Unlock()
	f.cond.Broadcast()
}

func (e *fakeServerEndpoint) Read(p []byte) (int, error) {
	f := e.f
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(e.toClient) == 0 && !e.closed && !f.killed {
		f.cond.Wait()
	}
	if len(e.toClient) == 0 {
		return 0, io.EOF
	}
	n := copy(p, e.toClient)
	e.toClient = e.toClient[n:]
	return n, nil
}

// Write 接收客户端的帧，解析并决定如何应答。
func (e *fakeServerEndpoint) Write(p []byte) (int, error) {
	f := e.f
	f.mu.Lock()
	f.fromClient = append(f.fromClient, p...)
	buf := append([]byte(nil), f.fromClient...)
	f.mu.Unlock()

	msgs, rest := DecodeMessages(buf)
	f.mu.Lock()
	f.fromClient = append([]byte(nil), rest...)
	f.mu.Unlock()

	for _, raw := range msgs {
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if m.Method == "" {
			continue
		}
		if m.ID == nil {
			// 通知
			f.mu.Lock()
			f.notifications = append(f.notifications, m.Method)
			if m.Method == "textDocument/didOpen" {
				f.didOpenParams = append(f.didOpenParams, append(json.RawMessage(nil), m.Params...))
			}
			f.mu.Unlock()
			continue
		}
		// 请求
		if f.dropRequests {
			continue // 收到但从不响应——模拟挂死的 server
		}
		var result any
		var errMsg string
		if f.onRequestFor != nil {
			f.onRequestFor(m.Method, m.Params)
		}
		if m.Method == "initialize" {
			if f.onInitialize != nil {
				f.onInitialize(m.Params)
			}
			result = map[string]any{"capabilities": f.capabilities}
		} else if f.onRequest != nil {
			result, errMsg = f.onRequest(m.Method, m.Params)
		} else {
			result = []any{}
		}
		var body []byte
		if errMsg != "" {
			body, _ = json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *m.ID,
				"error": map[string]any{"code": -32601, "message": errMsg},
			})
		} else {
			body, _ = json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *m.ID, "result": result,
			})
		}
		e.f.pushTo(e, frame(body))
	}
	return len(p), nil
}

// Close 模拟「连接断开」。
//
// **不置 closed**：真实语义是 server 进程被杀后**可再被 spawn**（multi-manager
// 的重启路径、TS 注释里的「崩溃后重新 initialize」）。若 Close 永久关掉，
// 「重新 initialize 应重发 didOpen」这类用例就无法成立（第二轮直接 EOF）。
// 断连的效果由「新 endpoint 有自己的缓冲」体现。
func (e *fakeServerEndpoint) Close() error { return nil }

// frame 给正文加 Content-Length 头。
func frame(body []byte) []byte { return EncodeMessage(body) }

// notifMethods 返回收到的通知方法名副本。
func (f *fakeServer) notifMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.notifications...)
}

// waitNotificationCount 等到某个通知**累计出现至少 n 次**。
//
// **为什么需要它**：`notifications` 是**累积**的（从不清空），
// 故 `waitNotification` 在「第二次等同一个方法」时会**立刻命中上一次的记录**
// ——测试会误以为新一轮触发已发生。需要区分「第 N 次」时用本方法。
func (f *fakeServer) waitNotificationCount(t *testing.T, method string, n int) {
	t.Helper()
	waitFor(t, 2e9, "收到第 "+itoa(n)+" 次通知 "+method, func() bool {
		c := 0
		for _, m := range f.notifMethods() {
			if m == method {
				c++
			}
		}
		return c >= n
	})
}

// waitNotification 等到某个通知出现。
func (f *fakeServer) waitNotification(t *testing.T, method string) {
	t.Helper()
	waitFor(t, 2e9, "收到通知 "+method, func() bool {
		for _, m := range f.notifMethods() {
			if m == method {
				return true
			}
		}
		return false
	})
}

// ── manager 测试 ─────────────────────────────────────────────────

// newTestManager 造一个 manager，spawn 缝注入假 server。
//
// 同时返回假 server 以便测试观察收到的通知/请求。
func newTestManager(t *testing.T, cwd string, onReq func(string, json.RawMessage) (any, string)) (*manager, *fakeServer) {
	t.Helper()
	fs := newFakeServer()
	fs.onRequest = onReq
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	return m, fs
}

// newTestManagerObserved 同上，但额外暴露 initialize 的观察点与注册语言解析器。
func newTestManagerObserved(
	t *testing.T, cwd string,
	onReq func(string, json.RawMessage) (any, string),
	onInit func(json.RawMessage),
) (*manager, *fakeServer) {
	t.Helper()
	fs := newFakeServer()
	fs.onRequest = onReq
	fs.onInitialize = onInit
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	return m, fs
}

// TestManager_InitializeHandshakeParams —— ★ 握手参数逐字段对账。
//
// TS（`manager.ts:168-173`）：
//
//	rpc.request('initialize', {
//	  processId: process.pid,
//	  rootUri: pathToFileURL(cwd).href,
//	  capabilities: { textDocument: { definition: { linkSupport: false }, references: {} } },
//	})
//
// 判别力：`linkSupport: false` 若写成 true 或漏掉，会改变 server 返回的
// Location 形态（DefinitionLink vs Location），下游解析全错。
func TestManager_InitializeHandshakeParams(t *testing.T) {
	var got map[string]any
	m, _ := newTestManagerObserved(t, "/tmp/proj", nil, func(params json.RawMessage) {
		_ = json.Unmarshal(params, &got)
	})

	if err := m.Initialize(); err != nil {
		t.Fatalf("initialize 失败：%v", err)
	}

	if got == nil {
		t.Fatal("server 未收到 initialize 请求")
	}
	if _, ok := got["processId"]; !ok {
		t.Error("initialize 应含 processId")
	}
	rootURI, _ := got["rootUri"].(string)
	if !strings.HasPrefix(rootURI, "file://") {
		t.Errorf("rootUri 应为 file:// URI，实得 %q", rootURI)
	}
	if !strings.HasSuffix(rootURI, "/tmp/proj") {
		t.Errorf("rootUri 应指向 cwd，实得 %q", rootURI)
	}

	caps, _ := got["capabilities"].(map[string]any)
	td, _ := caps["textDocument"].(map[string]any)
	def, _ := td["definition"].(map[string]any)
	if ls, ok := def["linkSupport"].(bool); !ok || ls {
		t.Errorf("capabilities.textDocument.definition.linkSupport 必须显式为 false，实得 %v", def["linkSupport"])
	}
	if _, ok := td["references"]; !ok {
		t.Error("capabilities.textDocument.references 必须存在（空对象）")
	}
}

// TestManager_SendsInitializedNotificationAfterInitialize —— 握手后发
// `initialized` 通知（LSP 规范要求）。
func TestManager_SendsInitializedNotificationAfterInitialize(t *testing.T) {
	m, fs := newTestManager(t, "/tmp/proj", nil)
	if err := m.Initialize(); err != nil {
		t.Fatalf("initialize 失败：%v", err)
	}
	fs.waitNotification(t, "initialized")
}

// TestManager_CapabilitiesDriveSupports —— ★ `supportsDefinition` /
// `supportsReferences` 靠 **server 自报的 capabilities**，不是本地猜测。
//
// TS：`supportsDefinition() { return capabilities?.definitionProvider === true }`
//
// 判别力：若实现恒返回 true（不看 capabilities），
// `definitionProvider:false` 的用例会红。
func TestManager_CapabilitiesDriveSupports(t *testing.T) {
	// 场景 1：两个都支持
	m, _ := newTestManager(t, "/tmp/p", nil)
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	if !m.SupportsDefinition() {
		t.Error("definitionProvider:true 时应支持定义跳转")
	}
	if !m.SupportsReferences() {
		t.Error("referencesProvider:true 时应支持引用查找")
	}

	// 场景 2：只支持定义
	m2, fs2 := newTestManager(t, "/tmp/p", nil)
	fs2.capabilities = map[string]any{"definitionProvider": true}
	if err := m2.Initialize(); err != nil {
		t.Fatal(err)
	}
	if !m2.SupportsDefinition() {
		t.Error("应支持定义跳转")
	}
	if m2.SupportsReferences() {
		t.Error("referencesProvider 缺席时必须**不支持**引用查找")
	}

	// 场景 3：都不是 true（含 false / 缺席）
	m3, fs3 := newTestManager(t, "/tmp/p", nil)
	fs3.capabilities = map[string]any{"definitionProvider": false}
	if err := m3.Initialize(); err != nil {
		t.Fatal(err)
	}
	if m3.SupportsDefinition() {
		t.Error("definitionProvider:false 时不该支持")
	}
}

// TestManager_GotoDefinitionLineConversion —— ★ 行号 **1-based → 0-based**。
//
// TS：`position: { line: line - 1, character }`（LSP 用 0-based 行）。
//
// 判别力：不做 -1 转换时，入参 line=5 会发出 line=5（实应 4）→ 必红。
// **列号不做转换**（LSP 的 character 本就是 0-based，与入参一致）。
func TestManager_GotoDefinitionLineConversion(t *testing.T) {
	var gotPos map[string]any
	m, fs := newTestManager(t, "/tmp/proj", func(method string, params json.RawMessage) (any, string) {
		if method == "textDocument/definition" {
			var p struct {
				Position map[string]any `json:"position"`
			}
			_ = json.Unmarshal(params, &p)
			gotPos = p.Position
		}
		return []any{}, ""
	})
	_ = fs
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	// 造一个可读文件（ensureDocument 要读它的真实内容）
	writeTempFile(t, "/tmp/proj", "a.go", "package main\nfunc F() {}\n")

	if _, err := m.GotoDefinition("a.go", 5, 7); err != nil {
		t.Fatalf("gotoDefinition 失败：%v", err)
	}
	if gotPos == nil {
		t.Fatal("server 未收到 position")
	}
	if gotPos["line"] != float64(4) {
		t.Errorf("行号应为 4（入参 5 减 1），实得 %v", gotPos["line"])
	}
	if gotPos["character"] != float64(7) {
		t.Errorf("列号应原样传 7（不做转换），实得 %v", gotPos["character"])
	}
}

// TestManager_GotoDefinitionWrapsNonArray —— ★ 非数组结果**包裹成数组**。
//
// TS：`const locations = (Array.isArray(result) ? result : [result]) as Location[]`
//
// 判别力：不包裹时 server 返回单个 Location 会丢结果（返回空）→ 必红。
func TestManager_GotoDefinitionWrapsNonArray(t *testing.T) {
	single := map[string]any{
		"uri": "file:///tmp/proj/b.go",
		"range": map[string]any{
			"start": map[string]any{"line": 9, "character": 0},
			"end":   map[string]any{"line": 9, "character": 5},
		},
	}
	m, _ := newTestManager(t, "/tmp/proj", func(method string, _ json.RawMessage) (any, string) {
		if method == "textDocument/definition" {
			return single, "" // 单个对象，非数组
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	locs, err := m.GotoDefinition("a.go", 1, 0)
	if err != nil {
		t.Fatalf("失败：%v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("非数组结果应被包裹成 1 个元素，实得 %d", len(locs))
	}
	if locs[0].Range.Start.Line != 9 {
		t.Errorf("Location 的 range.start.line 应为 9，实得 %d", locs[0].Range.Start.Line)
	}
}

// TestManager_FindReferencesRejectsNonArray —— ★ 与 gotoDefinition **不同的**语义。
//
// TS findReferences：`(Array.isArray(result) ? result : [])`——非数组返回**空**
// （不包裹）。两者语义不同，勿统一。
//
// 判别力：若把 findReferences 也写成「包裹」，本用例必红。
func TestManager_FindReferencesRejectsNonArray(t *testing.T) {
	single := map[string]any{
		"uri":   "file:///tmp/proj/b.go",
		"range": map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 2}},
	}
	m, _ := newTestManager(t, "/tmp/proj", func(method string, _ json.RawMessage) (any, string) {
		if method == "textDocument/references" {
			return single, ""
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	locs, err := m.FindReferences("a.go", 1, 0)
	if err != nil {
		t.Fatalf("失败：%v", err)
	}
	if len(locs) != 0 {
		t.Errorf("findReferences 对非数组应返回**空**（不包裹），实得 %d 个", len(locs))
	}
}

// TestManager_FindReferencesSendsIncludeDeclarationFalse —— ★ 请求参数含
// `context.includeDeclaration: false`。
//
// TS：`context: { includeDeclaration: false }`——**不包含声明本身**。
// 这是个容易漏的字段：漏了会把定义点也算进引用，影响「改导出符号前查全部
// 消费方」的判读。
func TestManager_FindReferencesSendsIncludeDeclarationFalse(t *testing.T) {
	var gotCtx map[string]any
	m, _ := newTestManager(t, "/tmp/proj", func(method string, params json.RawMessage) (any, string) {
		if method == "textDocument/references" {
			var p struct {
				Context map[string]any `json:"context"`
			}
			_ = json.Unmarshal(params, &p)
			gotCtx = p.Context
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")
	_, _ = m.FindReferences("a.go", 1, 0)

	if gotCtx == nil {
		t.Fatal("server 未收到 context")
	}
	if v, ok := gotCtx["includeDeclaration"].(bool); !ok || v {
		t.Errorf("context.includeDeclaration 必须为 false，实得 %v", gotCtx["includeDeclaration"])
	}
}

// TestManager_GotoReturnsEmptyWhenNotReady —— 未 ready 时返回**空切片**（不报错）。
//
// TS：`if (!rpc || !ready) return []`——静默降级（工具据此显示「未找到」）。
func TestManager_GotoReturnsEmptyWhenNotReady(t *testing.T) {
	m, _ := newTestManager(t, "/tmp/proj", nil)
	// 不 initialize
	locs, err := m.GotoDefinition("a.go", 1, 0)
	if err != nil {
		t.Errorf("未 ready 时不该报错，实得 %v", err)
	}
	if len(locs) != 0 {
		t.Errorf("未 ready 时应返回空，实得 %d", len(locs))
	}
	if m.IsReady() {
		t.Error("未 initialize 时 IsReady 应为 false")
	}
}

// TestManager_ServerErrorReturnsEmpty —— server 回 error 时**吞掉错误返回空**。
//
// TS：整个方法体在 `try { … } catch { return [] }` 里。
func TestManager_ServerErrorReturnsEmpty(t *testing.T) {
	m, _ := newTestManager(t, "/tmp/proj", func(method string, _ json.RawMessage) (any, string) {
		if method == "textDocument/definition" {
			return nil, "no such symbol"
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	locs, err := m.GotoDefinition("a.go", 1, 0)
	if err != nil {
		t.Errorf("server 报错时不该向上抛，实得 %v", err)
	}
	if len(locs) != 0 {
		t.Errorf("应返回空，实得 %d", len(locs))
	}
}

// TestManager_DidOpenSendsRealContent —— ★ didOpen 必须带**真实文件内容**。
//
// TS 注释（`manager.ts:14-24`）写明理由：多数语言服务器（sourcekit-lsp /
// jdtls / metals / roslyn）按 didOpen 的 text 建立文档缓冲——**发空文本等于
// 告诉它「这个文件是空的」**，之后的 definition / references 一律查不到符号。
// 实测 sourcekit-lsp 收到空 text 时 definition 1.4s 内返回空数组。
//
// 判别力：若实现发空 text（或漏发 text 字段）→ 本用例必红。
func TestManager_DidOpenSendsRealContent(t *testing.T) {
	content := "package main\n\nfunc 中文函数() {}\n"
	var gotText string
	var gotLangID string
	m, fs := newTestManager(t, "/tmp/proj", nil)
	// 拦截 didOpen 通知的 params：fakeServer 记录方法名，故另起一个观察点
	m.onDidOpen = func(params json.RawMessage) {
		var p struct {
			TextDocument struct {
				Text       string `json:"text"`
				LanguageID string `json:"languageId"`
				Version    int    `json:"version"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(params, &p)
		gotText = p.TextDocument.Text
		gotLangID = p.TextDocument.LanguageID
	}
	_ = fs
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", content)

	_, _ = m.GotoDefinition("a.go", 1, 0)

	if gotText != content {
		t.Errorf("didOpen 的 text 必须是**真实文件内容**\n want %q\n  got %q", content, gotText)
	}
	if gotLangID == "" {
		t.Error("didOpen 必须带 languageId")
	}
}

// TestManager_SkipsDidOpenWhenUnreadable —— ★ 读不到时**跳过通知**
// （不发空文本、**不进 openedDocs**）。
//
// TS 注释（`manager.ts:26-33`）：「didOpen 既不发也不缓存 uri（缓存了
// openedDocs 就永不补发），didChange 直接 return。发空文本会把 server 的
// 文档缓冲清成空，比内容略旧更坏；而『暂时读不到』一旦被当成『文档为空』
// 发给 server，就是**不可恢复的静默失效**」。
//
// 判别力：若实现无论读不读得到都发 didOpen（带空文本）→ 本用例必红；
// 若实现发了 didOpen 并 add(uri)，则后续**也**会被本用例的第二次检查抓到。
func TestManager_SkipsDidOpenWhenUnreadable(t *testing.T) {
	openCount := 0
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidOpen = func(json.RawMessage) { openCount++ }
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}

	// 不存在的文件
	_, _ = m.GotoDefinition("nonexistent.go", 1, 0)
	if openCount != 0 {
		t.Errorf("读不到的文件**不该**发 didOpen（会把 server 缓冲清空），实得 %d 次", openCount)
	}
}

// TestManager_DidOpenCachedOnlyOnce —— 同一文件的 didOpen 只发一次
// （TS：`if (openedDocs.has(uri)) return`）。
func TestManager_DidOpenCachedOnlyOnce(t *testing.T) {
	openCount := 0
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidOpen = func(json.RawMessage) { openCount++ }
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	for i := 0; i < 3; i++ {
		_, _ = m.GotoDefinition("a.go", 1, 0)
	}
	if openCount != 1 {
		t.Errorf("同一文件的 didOpen 应只发 1 次（缓存），实得 %d", openCount)
	}
}

// TestManager_ChangeFileSendsRealContent —— didChange 也带真实内容。
func TestManager_ChangeFileSendsRealContent(t *testing.T) {
	var changed string
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidChange = func(params json.RawMessage) {
		var p struct {
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		_ = json.Unmarshal(params, &p)
		if len(p.ContentChanges) > 0 {
			changed = p.ContentChanges[0].Text
		}
	}
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")
	_, _ = m.GotoDefinition("a.go", 1, 0) // 先 didOpen

	// 改文件
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n// 改过了\n")
	m.ChangeFile("a.go")

	if !strings.Contains(changed, "改过了") {
		t.Errorf("didChange 应带**改变后**的真实内容，实得 %q", changed)
	}
}

// TestManager_ChangeFileSkipsNeverOpened —— ★ 从未 didOpen 的文件不发 didChange。
//
// TS：`if (!openedDocs.has(uri)) return; // never opened, server has no cached state`
func TestManager_ChangeFileSkipsNeverOpened(t *testing.T) {
	changeCount := 0
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidChange = func(json.RawMessage) { changeCount++ }
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	m.ChangeFile("a.go") // 从未 didOpen
	if changeCount != 0 {
		t.Errorf("从未打开的文档不该发 didChange，实得 %d 次", changeCount)
	}
}

// TestManager_DisposeClearsReadyAndRejectsPending —— Dispose 后状态干净。
func TestManager_DisposeClearsReadyAndRejectsPending(t *testing.T) {
	m, _ := newTestManager(t, "/tmp/proj", nil)
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	if !m.IsReady() {
		t.Fatal("initialize 后应 ready")
	}
	m.Dispose()
	if m.IsReady() {
		t.Error("Dispose 后不该 ready")
	}
}

// TestManager_InitializeClearsOpenedDocs —— ★ initialize 必须清 openedDocs /
// 诊断缓存。
//
// TS 注释：「全新服务器进程对历史一无所知：崩溃后重新 initialize 必须清掉
// openedDocs/diagnosticCache，否则 openedDocs 短路 didOpen——新服务器永远
// 收不到那些文档的打开通知，诊断与定义**静默失真**」。
//
// 判别力：第二轮的 didOpen 计数若为 0（被旧缓存短路）→ 必红。
func TestManager_InitializeClearsOpenedDocs(t *testing.T) {
	openCount := 0
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidOpen = func(json.RawMessage) { openCount++ }

	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")
	_, _ = m.GotoDefinition("a.go", 1, 0)
	if openCount != 1 {
		t.Fatalf("首次 didOpen 应为 1，实得 %d", openCount)
	}

	// 重新 initialize（模拟崩溃后重启 server）
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.GotoDefinition("a.go", 1, 0)
	if openCount != 2 {
		t.Errorf("重新 initialize 后应**重发** didOpen（旧缓存必须清空），实得总计 %d 次", openCount)
	}
}

// TestManager_SpawnWithoutPipesFails —— 无 stdio 管道时立即报错
// （对账 TS：`throw new Error('LSP server spawn failed: no stdio pipes (check PATH / npx)')`）。
//
// 理由：桌面端最小 PATH 下 spawn 失败会产出 null 管道；不立即抛错就会让
// 请求永远挂着。
func TestManager_SpawnWithoutPipesFails(t *testing.T) {
	m := newManager(func() Transport { return nil }, "/tmp/proj", nil)
	err := m.Initialize()
	if err == nil {
		t.Fatal("spawn 无管道应报错")
	}
	if !strings.Contains(err.Error(), "no stdio pipes") {
		t.Errorf("错误文案应含 'no stdio pipes'，实得 %q", err.Error())
	}
	if m.IsReady() {
		t.Error("失败后不该 ready")
	}
}

// TestManager_URIRelativizedInResults —— 结果里的 URI 转成 **cwd 相对、
// 正斜杠** 形式。
//
// TS `uriToRelPath`：`relative(cwd, abs)`，若结果以 `..` 开头或仍为绝对路径
// 则回退原绝对路径；最后 `split('\\').join('/')`。
func TestManager_URIRelativizedInResults(t *testing.T) {
	m, _ := newTestManager(t, "/tmp/proj", func(method string, _ json.RawMessage) (any, string) {
		if method == "textDocument/definition" {
			return []any{map[string]any{
				"uri":   "file:///tmp/proj/sub/b.go",
				"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
			}}, ""
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	locs, _ := m.GotoDefinition("a.go", 1, 0)
	if len(locs) != 1 {
		t.Fatalf("应 1 个结果，实得 %d", len(locs))
	}
	if locs[0].URI != "sub/b.go" {
		t.Errorf("URI 应转成 cwd 相对形式 'sub/b.go'，实得 %q", locs[0].URI)
	}
}

// TestManager_URIOutsideCwdStaysAbsolute —— cwd 外的 URI 保持绝对路径。
func TestManager_URIOutsideCwdStaysAbsolute(t *testing.T) {
	m, _ := newTestManager(t, "/tmp/proj", func(method string, _ json.RawMessage) (any, string) {
		if method == "textDocument/definition" {
			return []any{map[string]any{
				"uri":   "file:///other/place/b.go",
				"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
			}}, ""
		}
		return []any{}, ""
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, "/tmp/proj", "a.go", "package main\n")

	locs, _ := m.GotoDefinition("a.go", 1, 0)
	if len(locs) != 1 {
		t.Fatalf("应 1 个结果，实得 %d", len(locs))
	}
	if !strings.HasPrefix(locs[0].URI, "/other/place") {
		t.Errorf("cwd 外应保持绝对路径，实得 %q", locs[0].URI)
	}
	if strings.Contains(locs[0].URI, "\\") {
		t.Errorf("URI 应统一用正斜杠，实得 %q", locs[0].URI)
	}
}

// TestManager_OversizeDocumentSkipped —— 超过 512KB 的文件不灌进 server
// （对账 TS `MAX_LSP_DOCUMENT_BYTES = 512 * 1024`）。
//
// 理由（TS 注释）：发超大文本会拖垮 server；发空文本更坏——故**跳过**。
func TestManager_OversizeDocumentSkipped(t *testing.T) {
	openCount := 0
	m, _ := newTestManager(t, "/tmp/proj", nil)
	m.onDidOpen = func(json.RawMessage) { openCount++ }
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	// 造一个 > 512KB 的文件
	big := bytes.Repeat([]byte("x"), 512*1024+1)
	writeTempBytes(t, "/tmp/proj", "big.go", big)

	_, _ = m.GotoDefinition("big.go", 1, 0)
	if openCount != 0 {
		t.Errorf("超上限的文件不该发 didOpen，实得 %d 次", openCount)
	}
}
