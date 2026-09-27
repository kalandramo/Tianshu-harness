package lsp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件是第一百零五刀的 RED 测试。
//
// # 要修的回归
//
// 第一百零四刀 W1 让 `ChangeFile` 经 `recordSentText` 更新 `lastSentText`
// （表示「新内容已发给 server」），**但没有清 `diags`**。两者必须描述
// **同一份内容**，于是：
//
//	第二次编辑 → ChangeFile 更新 lastSentText = 新内容
//	           → 取诊断时 prev == text 成立 → 命中缓存
//	           → 而缓存里是**第一次编辑的诊断** → 返回陈旧值（确定性错误）
//
// 修 W1 之前该路径因 `lastSentText` 落后而走「清缓存 + 重触发」——
// racy 但方向正确。W1 把它变成了确定性滞后一轮。

// TestChangeFile_InvalidatesStaleDiags —— ★ V1。
//
// # 用户可见形态
//
// 连续两次编辑同一文件（第二次引入**不同**的错误）→ 第二次的诊断必须是
// **新**错误，而不是第一次的。
//
// # 判别力
//
// 去掉 `ChangeFile` 路径上的 `diags` 失效 → 本例红（返回 "first-error"）。
func TestChangeFile_InvalidatesStaleDiags(t *testing.T) {
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

	// ── 第一次编辑 + 取诊断 ──
	if err := os.WriteFile(file, []byte("package a\n\nvar A = undefined1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		fs.waitNotification(t, "textDocument/didOpen")
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "first-error"}})
	}()
	first := m.getFileDiagnostics(file, 3000)
	if len(first) != 1 || first[0].Message != "first-error" {
		t.Fatalf("前置条件：首次应得 first-error，实得 %+v", first)
	}

	// ── 第二次编辑：改盘 → ChangeFile 通知（生产时序）──
	if err := os.WriteFile(file, []byte("package a\n\nvar B = undefined2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.ChangeFile(file)

	// ★ 关键断言：ChangeFile 之后，旧诊断**必须已失效**。
	//
	// 理由：ChangeFile 已把新内容告知 server，旧诊断描述的是**旧内容**。
	// 若仍留在缓存里，下一次取诊断会把它当作「新内容的诊断」返回。
	if m.diags.has(uri) {
		t.Errorf("★ ChangeFile 后旧诊断必须失效（它描述的是上一版内容），"+
			"实测仍在缓存：%+v", m.diags.get(uri))
	}

	// ── 第二次取诊断：必须拿到**新**诊断 ──
	go func() {
		fs.waitNotificationCount(t, "textDocument/didChange", 1)
		time.Sleep(20 * time.Millisecond)
		feedDiagnostics(t, fs, uri, []LspDiagnostic{{Severity: 1, Message: "second-error"}})
	}()
	second := m.getFileDiagnostics(file, 3000)
	if len(second) != 1 || second[0].Message != "second-error" {
		t.Errorf("★ 第二次编辑后应得新诊断 second-error，实得 %+v"+
			"（若为 first-error 即滞后一轮的回归）", second)
	}
}

// TestEnsureDocument_InvalidatesStaleDiags —— didOpen 路径同理。
//
// didOpen 也是「把当前内容告知 server」，故旧诊断同样必须失效。
//
// ⚠️ **上一版这条测试是弱代理**（实测 PASS 但证明不了正确性）：它推了一条
// `fresh` 诊断，而推送**覆盖**缓存，于是无论失效逻辑在不在都会拿到 fresh。
// 改为**不推送新诊断**——若旧诊断未失效，就会原样返回 `stale`（暴露问题）。
func TestEnsureDocument_InvalidatesStaleDiags(t *testing.T) {
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

	// 预置「陈旧诊断」（模拟上一轮留下）
	m.diags.set(uri, []LspDiagnostic{{Severity: 1, Message: "stale"}})

	// ★ didOpen 后**故意不推送**——若旧诊断已正确失效，等待会超时并返回
	// 空（那是正确结果：我们不知道新内容有无问题）；若未失效，会返回 stale。
	m.getFileDiagnostics(file, 300) // 短超时，只为触发 didOpen 路径

	if m.diags.has(uri) {
		t.Errorf("★ didOpen 后旧诊断必须失效（它描述的是上一版内容），"+
			"实测仍在缓存：%+v", m.diags.get(uri))
	}
}
