package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件是第一百零四刀的 RED 测试——**必须先红**。
//
// # 要建立的不变量
//
//	对任一 uri，`lastSentText[uri]` 必须恒等于「server 缓冲中该文档的内容」。
//	任何改变 server 缓冲的路径都必须同步更新它。
//
// 上一刀（e5ddd49b）引入的判据「内容未变且有缓存 → 用缓存」**依赖**该不变量，
// 但只有两条路径维护它——第三条（`ChangeFile`）没有，故留下残余静默失效。

// TestChangeFile_UpdatesLastSentText —— ★ V1：ChangeFile 必须同步判据状态。
//
// # 为什么这条测试是必要的
//
// `ChangeFile` 会改 server 缓冲（发 didChange），却不更新 `lastSentText`。
// 于是在生产时序（`lspdiag.go:72` 先 ChangeFile，随后 getFileDiagnostics）下：
//
//	① ChangeFile 改了 server 缓冲 → `lastSentText` 还是**旧的**
//	② getFileDiagnostics 比对发现「内容变了」→ 清缓存 + 再发 didChange
//	③ 而 gopls 对**内容未变的重复 didChange 不重推**
//	   （第二次 didChange 的内容与第一次相同）→ 缓存清了填不回来
//	   → **拿不到诊断**
//
// 这正是上一刀宣称修好、实际仍有残余的那类静默失效。
//
// # 判别力
//
// 去掉 `ChangeFile` 里的 `recordSentText`（或改为不更新）→ 本例红。
func TestChangeFile_UpdatesLastSentText(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "a.go")
	content := "package a\n\nvar X = 1\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
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
	uri := uriForFile(file, cwd)

	// ① 首次：didOpen 路径，推一条诊断
	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "first"}})
	}()
	if got := m.getFileDiagnostics(file, 3000); len(got) != 1 {
		t.Fatalf("首次应得 1 条，实得 %+v", got)
	}

	m.mu.Lock()
	before := m.lastSentText[uri]
	m.mu.Unlock()
	if before != content {
		t.Fatalf("前置条件不成立：lastSentText 应等于 %q，实得 %q", content, before)
	}

	// ② ★ 复现**生产时序**：写工具已改盘 → ChangeFile 通知 → 取诊断。
	//
	// 上一版测试让磁盘内容**不变**，于是 `prev == text` 仍成立、走了缓存分支，
	// 掩盖了缺陷（测试 bug，非实现正确）。真实场景是：
	//   a) 写工具写盘（内容变了）
	//   b) `injectLspDiagnostics` 调 `ChangeFile` → 用**新**内容发 didChange
	//      （server 缓冲已更新）
	//   c) 紧接着调 `getFileDiagnostics` → 它比对 `lastSentText`（**旧的**）
	//      → 判「变了」→ 清掉刚推来的缓存
	// 故必须**改盘**再 ChangeFile，才复现真实窗口。
	newContent := "package a\n\nvar X = 2\n"
	if err := os.WriteFile(file, []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}
	m.ChangeFile(file)

	m.mu.Lock()
	after := m.lastSentText[uri]
	m.mu.Unlock()
	if after != newContent {
		t.Errorf("★ ChangeFile 后 lastSentText 应与 server 缓冲一致（%q），实得 %q",
			newContent, after)
	}

	// ③ 关键后果：ChangeFile 已把新内容发给 server，故取诊断时
	//    **不该再清缓存重触发**（应直接用 ChangeFile 触发的推送）。
	//
	// 当前实现下 `lastSentText` 是旧值 → 判「变了」→ `diags.delete(uri)`
	// → 把 ChangeFile 刚触发的有效推送清掉 → 与后续 didChange 竞态。
	if !m.diags.has(uri) {
		t.Errorf("★ ChangeFile 已通知 server 新内容，取诊断时不该清掉缓存" +
			"（当前实现因 lastSentText 未同步而误清）")
	}
}

// TestInitialize_ClearsAllState —— ★ V2：重启必须清空全部相关的状态。
//
// # 为什么
//
// `Initialize` 会 `Dispose` 旧 server 并 spawn 新的。新 server **没有任何
// 文档状态**，故 `openedDocs` 被清空（既有正确行为）。但若 `lastSentText`
// 与 `diags` 不清：
//
//   - `lastSentText` 仍认为「该内容已发给 server」→ `getFileDiagnostics`
//     判「未变」→ **跳过 didOpen**，而新 server 根本没这个文档 → 拿不到诊断
//   - `diags` 里留着**旧 server** 的诊断 → 返回陈旧结果
//
// 判别力：去掉 Initialize 里任一清理 → 本例红。
func TestInitialize_ClearsAllState(t *testing.T) {
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
	uri := uriForFile(file, cwd)

	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "from-old-server"}})
	}()
	if got := m.getFileDiagnostics(file, 3000); len(got) != 1 {
		t.Fatalf("前置条件：应得 1 条，实得 %+v", got)
	}

	// 前置条件确认真有状态残留
	m.mu.Lock()
	hasText := m.lastSentText[uri] != ""
	m.mu.Unlock()
	if !hasText || !m.diags.has(uri) {
		t.Fatal("前置条件不成立：应有 lastSentText 与 diags 状态")
	}

	// ★ 重启
	if err := m.Initialize(); err != nil {
		t.Fatalf("重启失败：%v", err)
	}

	m.mu.Lock()
	textAfter := m.lastSentText[uri]
	openedAfter := m.openedDocs[uri]
	m.mu.Unlock()

	if textAfter != "" {
		t.Errorf("★ 重启后 lastSentText 应被清空（新 server 无文档状态），实得 %q", textAfter)
	}
	if openedAfter {
		t.Errorf("★ 重启后 openedDocs 应被清空，实测仍为 true")
	}
	if m.diags.has(uri) {
		t.Errorf("★ 重启后 diags 应被清空（旧 server 的诊断无效），实测仍有 %+v",
			m.diags.get(uri))
	}
}

// TestChangeFile_NoInlineDidChange —— ★ V3：ChangeFile 不得绕过统一收口。
//
// # 为什么用源码级断言
//
// 本刀的不变量靠「所有改变 server 缓冲的路径都经 `notifyDidChangeWithText`」
// 保证。若将来有人在 `ChangeFile` 里再写一个内联 `rpc.Notify`，
// 不变量会**静默**破掉（编译通过、测试多半也过，因为只是少更新一个 map）。
// 源码级断言是确定性的，且失败信息直指问题。
func TestChangeFile_NoInlineDidChange(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// 取 ChangeFile 函数体（从其声明到下一个顶层 func）
	start := strings.Index(body, "func (m *manager) ChangeFile(")
	if start < 0 {
		t.Fatal("找不到 ChangeFile 定义")
	}
	rest := body[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}

	if strings.Contains(rest, `rpc.Notify("textDocument/didChange"`) {
		t.Errorf("★ ChangeFile 不得内联发送 didChange——必须经 " +
			"notifyDidChangeWithText（它统一维护 lastSentText）。\n" +
			"内联发送会绕过收口，静默破坏判据状态。")
	}
	if !strings.Contains(rest, "notifyDidChangeWithText") {
		t.Errorf("★ ChangeFile 应调用 notifyDidChangeWithText（统一收口点）")
	}
}
