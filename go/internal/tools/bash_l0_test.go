package tools

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

func newBashStore(t *testing.T) *artifact.Store {
	t.Helper()
	return artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "b1" },
	})
}

// bashEchoCmd 造一个输出大量内容的跨平台命令。
//
// **为何用 `ls` 而非 `for` 循环**：Go 的 bash 工具在 Windows 上走 Git Bash /
// pwsh / cmd 探测（`platform.ResolveShellCommand`），`for` 语法不跨 shell 兼容。
func bashBigOutputCmd() string {
	// 输出约 3000 行（远超阈值 1600）。
	return "seq 1 3000"
}

// TestBashL0WrapsLargeOutput —— **核心**：大输出包成 artifact。
//
// bash 在 l0WrappedTools 里（L1 跳过），故大结果必须由工具自己落盘。
func TestBashL0WrapsLargeOutput(t *testing.T) {
	store := newBashStore(t)
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": bashBigOutputCmd()})
	p.ArtifactStore = store
	p.ContextWindow = 0 // 未知窗口 → 阈值 = 800 × 1.0(bash 乘数)

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令在本环境不可用（可能缺 seq）：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "[artifact:") {
		t.Fatalf("大输出应被包装，实得：%.300s", r.Content)
	}
	// 标记必须在末尾。
	if !artifactMarkerTailRe.MatchString(r.Content) {
		t.Errorf("标记必须在末尾：%.300s", r.Content)
	}
	// 文案对账 TS：含 "Use read_section" 指引。
	if !strings.Contains(r.Content, "Use read_section") {
		t.Errorf("应含 TS 的召回指引：%.300s", r.Content)
	}
}

// TestBashL0SuccessFold —— 成功且行数 > 20 时**保留末尾 20 行** + 截断 footer。
//
// **断言在第二十四刀修正**：第二十一刀我实现 successFold 时折叠成**单行提示**，
// 但 TS 的 `buildModelOutput`（`output-store.ts`）是 `SUCCESS_TAIL_LINES`=20
// → 保留末尾 20 行 + `[output truncated: last N of M lines shown]`。
// 单行提示让模型失去"最后发生了什么"的观测——TS 注释强调的正是要保留它。
func TestBashL0SuccessFold(t *testing.T) {
	store := newBashStore(t)
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": bashBigOutputCmd()})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	// header 对账 TS：`[cmd] exit=N time=Xs lines=M`。
	if !strings.Contains(r.Content, "exit=0") || !strings.Contains(r.Content, "lines=") {
		t.Errorf("应含 TS 形态的 header：%.300s", r.Content)
	}
	// 折叠 footer（对账 TS 文案）。
	if !strings.Contains(r.Content, "lines omitted") {
		t.Errorf("应含省略标记：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "last 20 of") {
		t.Errorf("应保留末尾 20 行并说明：%.300s", r.Content)
	}
	// **关键**：末尾内容确实在（不是单行提示）。
	if !strings.Contains(r.Content, "3000") {
		t.Errorf("折叠后应保留末行 3000（末尾 20 行）：%.300s", tailSnippet(r.Content, 200))
	}
}

// TestBashL0SkipsSmallOutput —— 小输出**不包**（零标记）。
func TestBashL0SkipsSmallOutput(t *testing.T) {
	store := newBashStore(t)
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": "echo hello"})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	if strings.Contains(r.Content, "[artifact:") {
		t.Errorf("小输出不应包 artifact：%q", r.Content)
	}
	if !strings.Contains(r.Content, "hello") {
		t.Errorf("小输出应原样返回：%q", r.Content)
	}
}

// TestBashL0StoresRaw —— 落盘的应是 bash 的输出（非包装后的）。
func TestBashL0StoresRaw(t *testing.T) {
	store := newBashStore(t)
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": bashBigOutputCmd()})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.300s", r.Content)
	}
	raw, err := store.ReadRaw(m[1])
	if err != nil {
		t.Fatalf("ReadRaw 失败：%v", err)
	}
	// 落盘原文应含命令的真实输出（seq 的输出含 "1" 与 "3000"）。
	if !strings.Contains(raw, "3000") {
		t.Errorf("落盘应是完整输出（含末行 3000），实得末尾：%.200s", tailSnippet(raw, 200))
	}
	// **落盘原文不应含 artifact 标记**（那是给模型看的包装）。
	if strings.Contains(raw, "[artifact:") {
		t.Error("落盘原文不应含 artifact 标记")
	}
}

// TestBashL0RecallByReadSection —— **端到端**：read_section 能取回完整输出。
func TestBashL0RecallByReadSection(t *testing.T) {
	store := newBashStore(t)
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": bashBigOutputCmd()})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.300s", r.Content)
	}

	rs := ReadSection("", nil)
	r2, _ := rs.Execute(t.Context(), &CallParams{
		Input:         map[string]any{"artifactId": m[1], "section": "L1-L20"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	if r2.IsError {
		t.Fatalf("read_section 取回失败：%s", r2.Content)
	}
	if !strings.Contains(r2.Content, "1") {
		t.Errorf("应取回输出内容：%.200s", r2.Content)
	}
}

// TestBashL0GracefulWhenSaveFails —— Save 失败时优雅降级。
func TestBashL0GracefulWhenSaveFails(t *testing.T) {
	bad := artifact.NewStore("bad\x00dir", "sess1", artifact.Options{})
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": bashBigOutputCmd()})
	p.ArtifactStore = bad
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	if strings.Contains(r.Content, "[artifact:") {
		t.Error("Save 失败时不应有 artifact 标记")
	}
}

// TestBashWithoutStoreUnchanged —— 无 store 时行为不变（无标记）。
func TestBashWithoutStoreUnchanged(t *testing.T) {
	tool := Bash(t.TempDir())
	p := call("", map[string]any{"command": "echo hello"})
	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Skipf("命令不可用：%.200s", r.Content)
	}
	if strings.Contains(r.Content, "[artifact:") {
		t.Error("无 store 不应有标记")
	}
}
