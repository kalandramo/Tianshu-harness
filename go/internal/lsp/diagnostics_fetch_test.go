package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// feedDiagnostics 让假 server 推送一次 publishDiagnostics。
//
// 直接复用既有的 `fakeServer.push`（不用真 gopls——本机未安装，
// 且诊断链路的验证点是**协议时序**而非 server 语义）。
func feedDiagnostics(t *testing.T, fs *fakeServer, uri string, diags []LspDiagnostic) {
	t.Helper()
	params := map[string]any{"uri": uri, "diagnostics": diags}
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/publishDiagnostics",
		"params":  params,
	})
	fs.push(encodeFrame(t, msg))
}

// encodeFrame 把 JSON 正文封成 LSP 帧（Content-Length 头）。
func encodeFrame(t *testing.T, body []byte) []byte {
	t.Helper()
	return append([]byte("Content-Length: "+itoa(len(body))+"\r\n\r\n"), body...)
}

// TestGetFileDiagnostics_ReceivesPush —— ★ 端到端：didChange 触发 → 推送到达 → 读回。
//
// # 这条测试证明什么
//
// Go 侧此前**从未注册** `publishDiagnostics` 处理器（`grep` 只命中测试桩），
// 且 `GetFileDiagnostics` 是返回空切片的桩。本用例覆盖三件套：
// ① 注册生效（推送被缓存）② 触发发送（didChange 真的发出）
// ③ 等待循环正确退出（`has` 判据，非 `len > 0`）。
func TestGetFileDiagnostics_ReceivesPush(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := newFakeServer()
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	t.Cleanup(func() {
		m.Dispose()
		// ⚠️ kill 要求持 f.mu（见其文档注释）
		fs.mu.Lock()
		fs.kill()
		fs.mu.Unlock()
	})
	if err := m.Initialize(); err != nil {
		t.Fatalf("initialize 失败：%v", err)
	}

	uri := uriForPath(file)

	// 假 server 收到 didChange 后推送诊断（模拟真实 server 行为）。
	// ★ 必须在**另一个 goroutine** 里推——`getFileDiagnostics` 会阻塞等待。
	// ★ 首次调用走 **didOpen** 路径（文档未打开 → ensureDocument → 直接等推送）。
	//
	// **为什么不是 didChange**：didOpen 已把当前内容告知 server，
	// 再叠同内容 didChange 对真实 server（gopls）是空操作——
	// 故新逻辑在 didOpen 路径**不发** didChange，直接等 didOpen 触发的推送。
	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{
			{Range: Range{Start: Position{Line: 4}}, Severity: 1, Message: "undefined: foo"},
		})
	}()

	got := m.getFileDiagnostics(file, 1500)
	if len(got) != 1 {
		t.Fatalf("应收到 1 条诊断，实得 %d 条：%+v", len(got), got)
	}
	if got[0].Message != "undefined: foo" || got[0].Severity != 1 {
		t.Errorf("诊断内容不符：%+v", got[0])
	}
}

// TestGetFileDiagnostics_EmptyPushCountsAsAnswer —— ★ 空推送 = 确切答案（不是超时）。
//
// LSP server 对**无问题的文件**推送空数组。若等待循环用 `len > 0` 判断，
// 干净文件会一直空转到超时（每次编辑白等 2 秒）。本用例锁住 `has` 判据：
// 收到空推送后应**立即**返回（而非等满超时）。
func TestGetFileDiagnostics_EmptyPushCountsAsAnswer(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "clean.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newFakeServer()
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	t.Cleanup(func() {
		m.Dispose()
		fs.mu.Lock()
		fs.kill()
		fs.mu.Unlock()
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	uri := uriForPath(file)

	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, nil) // 空数组
	}()

	start := time.Now()
	got := m.getFileDiagnostics(file, 3000) // 超时给 3s
	elapsed := time.Since(start)

	if len(got) != 0 {
		t.Errorf("空推送应得空诊断，实得 %+v", got)
	}
	// ★ 关键：不该等满 3s。给宽松上界（500ms）避免 flaky
	if elapsed > 900*time.Millisecond {
		t.Errorf("收到空推送应立即返回（用了 %v）——"+
			"等待循环可能用错了判据（应 has 而非 len>0）", elapsed)
	}
}

// TestGetFileDiagnostics_TimeoutReturnsNil —— 无推送 → 超时返回 nil（best-effort）。
func TestGetFileDiagnostics_TimeoutReturnsNil(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newFakeServer()
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	t.Cleanup(func() {
		m.Dispose()
		fs.mu.Lock()
		fs.kill()
		fs.mu.Unlock()
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	// 不推送 → 应超时返回空
	got := m.getFileDiagnostics(file, 200)
	if len(got) != 0 {
		t.Errorf("超时应返回空，实得 %+v", got)
	}
}

// TestGetFileDiagnostics_NotReadyReturnsNil —— 未初始化 → nil（不 panic）。
func TestGetFileDiagnostics_NotReadyReturnsNil(t *testing.T) {
	m := newManager(func() Transport { return nil }, "/tmp/proj", nil)
	if got := m.getFileDiagnostics("/tmp/proj/a.go", 100); got != nil {
		t.Errorf("未就绪应返回 nil，实得 %+v", got)
	}
}

// TestDiagCache_DistinguishesEmptyFromMissing —— ★ 缓存语义核心。
//
// 「收到空列表」与「从未收到」必须可区分——前者让等待循环立即退出，
// 后者让它继续等。合并二者会让干净文件的诊断等待**每次白等满超时**。
func TestDiagCache_DistinguishesEmptyFromMissing(t *testing.T) {
	c := newDiagCache()
	const uri = "file:///x.go"

	if c.has(uri) {
		t.Error("未存过不应 has")
	}
	c.set(uri, nil) // 空列表也是「已收到」
	if !c.has(uri) {
		t.Error("★ 存过空列表后 has 应为 true（空 != 未收到）")
	}
	if got := c.get(uri); got != nil {
		t.Errorf("空列表读回应为 nil/空，实得 %+v", got)
	}

	// delete 应回到「未收到」
	c.delete(uri)
	if c.has(uri) {
		t.Error("delete 后 has 应为 false（触发新一轮等待）")
	}
}

// TestGetFileDiagnostics_ContentChangeRetriggers —— ★ 内容变了必须重新触发。
//
// # 为什么这条必需（防我刚改的逻辑开新洞）
//
// 新逻辑是「内容未变 → 用缓存；内容变了 → 清缓存 + didChange」。
// 若判据写错（一律用缓存），第二次编辑新引入的错误**永远不会显示**——
// 那正是最初 undo 时序倒置那类「静默失效」。
//
// 本用例模拟「文件被改后再次取诊断」：必须看到 didChange 被发出。
func TestGetFileDiagnostics_ContentChangeRetriggers(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "a.go")
	if err := os.WriteFile(file, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newFakeServer()
	m := newManager(func() Transport { return fs.clientSide() }, cwd, nil)
	t.Cleanup(func() {
		m.Dispose()
		fs.mu.Lock()
		fs.kill()
		fs.mu.Unlock()
	})
	if err := m.Initialize(); err != nil {
		t.Fatal(err)
	}
	uri := uriForPath(file)

	// 第一次：didOpen 路径，推一条诊断
	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "first"}})
	}()
	first := m.getFileDiagnostics(file, 3000)
	if len(first) != 1 || first[0].Message != "first" {
		t.Fatalf("首次应得 first，实得 %+v", first)
	}

	// 改文件内容 → 第二次取诊断必须重新触发（didChange）
	if err := os.WriteFile(file, []byte("package a\n\nvar X = undefinedY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		// ★ 用**计数**判据（notifications 是累积的，存在判据会命中旧记录）
		fs.waitNotificationCount(t, "textDocument/didChange", 1)
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "second"}})
	}()
	second := m.getFileDiagnostics(file, 3000)
	if len(second) != 1 || second[0].Message != "second" {
		t.Fatalf("★ 内容变了应重新触发并拿到新诊断，实得 %+v（判据可能一律用缓存了）", second)
	}

	// 内容**未**变 → 第三次应直接复用缓存（不发 didChange，立刻返回）
	start := time.Now()
	third := m.getFileDiagnostics(file, 5000)
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("内容未变应直接用缓存（用了 %v），不该等推送", el)
	}
	if len(third) != 1 || third[0].Message != "second" {
		t.Errorf("内容未变应复用缓存，实得 %+v", third)
	}
}
