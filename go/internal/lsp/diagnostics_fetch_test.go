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
	go func() {
		fs.waitNotification(t, "textDocument/didChange")
		// 稍等让清缓存动作发生在推送之前（对账 TS 的顺序要求）
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
		fs.waitNotification(t, "textDocument/didChange")
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
