package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestArtifactEndToEndInterceptThenRecall —— **端到端**：整条链路。
//
// 大工具结果 → L1 拦截落盘 → 消息里只剩引用 → read_section 按区段取回。
// 这是「artifact store 真的接了生产路径」的核心证据。
func TestArtifactEndToEndInterceptThenRecall(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "e2e" },
	})
	l := &Loop{Artifacts: store}
	l.Compact = NewCompactBoundary(1_000_000)

	// 造一个「大结果」（含可辨识的行）。
	var b strings.Builder
	for i := 1; i <= 2000; i++ {
		b.WriteString("LINE-" + itoaTest(i) + "\n")
	}
	big := b.String()
	if len(big) < 150_000 {
		// 阈值是 150K（1M 窗口的 delegate_task 口径），必须超过它。
		big += strings.Repeat("padding\n", 25000)
	}

	tc := toolCall{name: "delegate_task", input: map[string]any{"prompt": "do work"}}
	wrapped := l.interceptResultForArtifact(tc, contract.Result{Content: big})

	// 1) 被包装成引用。
	if !strings.HasPrefix(wrapped.Content, "[artifact:") {
		t.Fatalf("应被包装，实得前 80 字符：%q", wrapped.Content[:minInt(80, len(wrapped.Content))])
	}
	if len(wrapped.Content) > 1000 {
		t.Errorf("包装后应远小于原文，实得 %d 字符", len(wrapped.Content))
	}

	// 2) 从引用里取出 artifact id。
	id := extractArtifactID(wrapped.Content)
	if id == "" {
		t.Fatal("包装文案里应含 artifact id")
	}

	// 3) 用 read_section 取回（模拟模型的下一次调用）。
	tool := tools.ReadSection("", nil)
	res, err := tool.Execute(context.Background(), &tools.CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L3"},
		ArtifactStore: store,
	})
	if err != nil {
		t.Fatalf("read_section 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("read_section 报错：%s", res.Content)
	}
	if res.Content != "LINE-1\nLINE-2\nLINE-3" {
		t.Errorf("取回内容不符：%q", res.Content)
	}
}

// TestArtifactRecallSeesWrappedContentNotOriginal —— 包装后原文仍在 store 里。
//
// **关键不变量**：L1 拦截只改**返回给模型的内容**，落盘的原文必须完整
// ——否则 read_section 取回的是被截断的副本（TS 记录的 double-save 事故）。
func TestArtifactRecallSeesWrappedContentNotOriginal(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now: func() int64 { return 1 }, IDGenerator: func() string { return "x" },
	})
	l := &Loop{Artifacts: store}
	l.Compact = NewCompactBoundary(1_000_000)

	big := strings.Repeat("ORIGINAL-CONTENT\n", 20000)
	tc := toolCall{name: "delegate_task", input: map[string]any{}}
	wrapped := l.interceptResultForArtifact(tc, contract.Result{Content: big})

	id := extractArtifactID(wrapped.Content)
	raw, err := store.ReadRaw(id)
	if err != nil {
		t.Fatalf("ReadRaw 失败：%v", err)
	}
	if raw != big {
		t.Errorf("落盘原文应与输入逐字节相同（长度 %d vs %d）", len(raw), len(big))
	}
}

// extractArtifactID 从包装文案里取 artifact id。
func extractArtifactID(content string) string {
	const prefix = "[artifact:"
	i := strings.Index(content, prefix)
	if i < 0 {
		return ""
	}
	rest := content[i+len(prefix):]
	j := strings.Index(rest, "]")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
