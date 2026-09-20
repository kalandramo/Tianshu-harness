package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
)

func newArtifactLoop(t *testing.T, contextWindow int) *Loop {
	t.Helper()
	store := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "a1" },
	})
	l := &Loop{Artifacts: store}
	l.Compact = NewCompactBoundary(contextWindow)
	return l
}

// TestInterceptSkipsL0WrappedTools —— **核心约束**：L0 工具不得被 L1 重复包装。
//
// 对账 TS 的 `L0_WRAPPED_TOOLS`（read_file/read_section/grep/bash）。
// 重复包装会导致无限嵌套（`[artifact:新ID] → read_section → ...`）——
// TS 记录了真实事故（tianshu v4 pro 2026-05-25）。
func TestInterceptSkipsL0WrappedTools(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	big := strings.Repeat("x", 500_000)

	for _, tool := range []string{"read_file", "read_section", "grep", "bash"} {
		tc := toolCall{name: tool, input: map[string]any{"file_path": "a.ts"}}
		got := l.interceptResultForArtifact(tc, contract.Result{Content: big})
		if got.Content != big {
			t.Errorf("%s 不应被 L1 包装（L0 工具）——内容被改了", tool)
		}
	}
}

// TestInterceptSkipsContentWithMarker —— 已含 artifact 标记不得重复包装。
//
// 对账 TS：早期只查 `startsWith` 漏掉尾部标记 → double-save bug。
// 现在查任意位置。
func TestInterceptSkipsContentWithMarker(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	// 尾部标记（L0 的形态）。
	content := strings.Repeat("x", 500_000) + "\n[artifact:abc] summary"
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	got := l.interceptResultForArtifact(tc, contract.Result{Content: content})
	if got.Content != content {
		t.Error("已含 artifact 标记的内容不应被重复包装（尾部标记也必须检出）")
	}
}

// TestInterceptWrapsLargeForeignTool —— 没有 L0 的工具的大结果应被包装。
func TestInterceptWrapsLargeForeignTool(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	big := strings.Repeat("x", 500_000)
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	got := l.interceptResultForArtifact(tc, contract.Result{Content: big})

	if !strings.HasPrefix(got.Content, "[artifact:") {
		t.Fatalf("大结果应被包装为 artifact 引用，实得前 50 字符：%q", got.Content[:min(50, len(got.Content))])
	}
	if !strings.Contains(got.Content, "Use read_section") {
		t.Error("包装文案应含 read_section 提示")
	}
	// store 里应真的存了原文。
	if len(l.Artifacts.List()) != 1 {
		t.Errorf("应落盘 1 条 artifact，实得 %d", len(l.Artifacts.List()))
	}
}

// TestInterceptSkipsSmallContent —— 小内容不包装。
func TestInterceptSkipsSmallContent(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	small := "tiny result"
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	got := l.interceptResultForArtifact(tc, contract.Result{Content: small})
	if got.Content != small {
		t.Error("小内容不应被包装")
	}
}

// TestInterceptThresholdIsWindowAware —— 成功结果的阈值随窗口放大。
//
// 对账 TS：1M 窗口的 read_file 阈值 300K——中等输出（如 40K）不该被包。
func TestInterceptThresholdIsWindowAware(t *testing.T) {
	// 40K 内容 + 1M 窗口：delegate_task 的阈值 = 基础 150K × 1.0 = 150K → 不包。
	l := newArtifactLoop(t, 1_000_000)
	content := strings.Repeat("x", 40_000)
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	if got := l.interceptResultForArtifact(tc, contract.Result{Content: content}); got.Content != content {
		t.Error("1M 窗口下 40K 内容（低于 150K 阈值）不应被包装")
	}

	// 小窗口（64K）：阈值 = 1200（<200K 的遗留值）→ 40K 应被包。
	l2 := newArtifactLoop(t, 64_000)
	if got := l2.interceptResultForArtifact(tc, contract.Result{Content: content}); !strings.HasPrefix(got.Content, "[artifact:") {
		t.Error("64K 窗口下 40K 内容（高于 1200 阈值）应被包装")
	}
}

// TestInterceptErrorUsesBaseThreshold —— 错误结果不受窗口 floor 放大。
//
// 对账 TS：~30K 以下的栈回溯行内更有用。
func TestInterceptErrorUsesBaseThreshold(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	// 3K 错误：高于兜底 2500，低于窗口 floor（150K）——应被包。
	content := strings.Repeat("x", 3000)
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	got := l.interceptResultForArtifact(tc, contract.Result{Content: content, IsError: true})
	if !strings.HasPrefix(got.Content, "[artifact:") {
		t.Error("错误结果用兜底阈值（2500）——3K 应被包装")
	}
}

// TestInterceptErrorIncludesHeadExcerpt —— 错误包装含诊断摘录。
func TestInterceptErrorIncludesHeadExcerpt(t *testing.T) {
	l := newArtifactLoop(t, 1_000_000)
	content := strings.Repeat("x", 3000) + "\nTypeError: cannot read property foo"
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	got := l.interceptResultForArtifact(tc, contract.Result{Content: content, IsError: true})
	if !strings.Contains(got.Content, "TypeError") {
		t.Errorf("错误包装应含 head 摘录，实得：%q", got.Content)
	}
}

// TestInterceptNilStoreSafe —— nil store 不得 panic。
func TestInterceptNilStoreSafe(t *testing.T) {
	l := &Loop{}
	got := l.interceptResultForArtifact(toolCall{name: "x"}, contract.Result{Content: strings.Repeat("x", 100000)})
	if !strings.HasPrefix(got.Content, "xxx") {
		t.Error("nil store 应原样返回")
	}
}

// TestExtractErrorHeadFallbackTail —— 无错误关键词时取末 8 行。
func TestExtractErrorHeadFallbackTail(t *testing.T) {
	content := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nSUMMARY LINE 1\nSUMMARY LINE 2"
	got := extractErrorHead(content)
	if !strings.Contains(got, "SUMMARY LINE 1") || !strings.Contains(got, "SUMMARY LINE 2") {
		t.Errorf("无错误关键词时应取末 8 行，实得：%q", got)
	}
}

// TestHasErrorKeywordWordBoundary —— errorHandler 不应命中 error。
func TestHasErrorKeywordWordBoundary(t *testing.T) {
	if hasErrorKeyword("const errorHandler = () => {}") {
		t.Error("errorHandler 不应命中 error（词边界）")
	}
	if !hasErrorKeyword("TypeError: boom") {
		t.Error("TypeError 应命中")
	}
	if !hasErrorKeyword("expect(x).toBe(1)") {
		t.Error("expect( 应命中")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
