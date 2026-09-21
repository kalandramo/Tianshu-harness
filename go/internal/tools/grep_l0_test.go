package tools

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

func newGrepStore(t *testing.T) *artifact.Store {
	t.Helper()
	return artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "g1" },
	})
}

// grepFixture 造一个含 n 个匹配行的目录，返回 root。
func grepFixture(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("export function targetFn() { return 1 }\n")
		b.WriteString("const other = 2\n")
	}
	mustWriteFile(t, rootDir(root)+"big.ts", b.String())
	return root
}

// TestGrepL0WrapsLargeResult —— **核心**：大结果包成 artifact。
//
// 与 read_file 同构：grep 在 l0WrappedTools 里（L1 跳过），故大结果必须
// 由工具自己落盘。
func TestGrepL0WrapsLargeResult(t *testing.T) {
	root := grepFixture(t, 800) // 800 匹配行 × ~40 字符 ≫ 阈值
	store := newGrepStore(t)
	tool := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	p.ArtifactStore = store
	p.ContextWindow = 0 // 阈值 = 800 × grep 乘数 0.67 ≈ 536

	r, _ := tool.Execute(t.Context(), p)
	if !strings.Contains(r.Content, "[artifact:") {
		t.Fatalf("大结果应被包装，实得：%.200s", r.Content)
	}
	// 标记必须在末尾。
	if !artifactMarkerTailRe.MatchString(r.Content) {
		t.Errorf("标记必须在末尾：%.200s", r.Content)
	}
	// 文案对账 TS：含「获取完整匹配列表」。
	if !strings.Contains(r.Content, "获取完整匹配列表") {
		t.Errorf("应含 TS 的使用指引文案：%.300s", r.Content)
	}
	// 应含 grep summary（对账 summarizeGrepResult 的格式）。
	if !strings.Contains(r.Content, `grep "targetFn"`) {
		t.Errorf("应含 grep summary：%.300s", r.Content)
	}
}

// TestGrepL0SkipsSmallResult —— 小结果**不包**（零标记）。
func TestGrepL0SkipsSmallResult(t *testing.T) {
	root := grepFixture(t, 3)
	store := newGrepStore(t)
	tool := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if strings.Contains(r.Content, "[artifact:") {
		t.Errorf("小结果不应包 artifact：%q", r.Content)
	}
	if !strings.Contains(r.Content, "targetFn") {
		t.Errorf("小结果应原样返回匹配：%.200s", r.Content)
	}
}

// TestGrepL0StoresRawNotTruncated —— 落盘的必须是**原文**（非截断版）。
//
// **构造关键**：内容必须**超 cap**（否则截断不发生，原文 ≡ 截断版，无法区分）。
// cap 在窗口=0 时是 8000；用长匹配行让 grep 输出远超它。
func TestGrepL0StoresRawNotTruncated(t *testing.T) {
	root := t.TempDir()
	// 100 个匹配行（grep 上限）× 每行 ~300 字符 ≈ 30000 字符 ≫ cap 8000。
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("export function targetFn() { return ")
		b.WriteString(strings.Repeat("x", 250))
		b.WriteString(" }\n")
	}
	mustWriteFile(t, rootDir(root)+"big.ts", b.String())

	store := newGrepStore(t)
	tool := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.200s", r.Content)
	}
	raw, err := store.ReadRaw(m[1])
	if err != nil {
		t.Fatalf("ReadRaw 失败：%v", err)
	}
	// 落盘原文应**远大于**模型可见的截断版（后者受 cap 8000 限制）。
	if len(raw) <= 8000 {
		t.Errorf("落盘应是完整原文（>cap 8000），实得 %d 字节", len(raw))
	}
	// 模型可见版含 artifact 标记；落盘原文不含（那是给模型看的包装）。
	if strings.Contains(raw, "[artifact:") {
		t.Error("落盘原文不应含 artifact 标记")
	}
	// 落盘应含上限提示（完整输出的末尾）。
	if !strings.Contains(raw, "已达上限") {
		t.Errorf("落盘应是完整输出，末尾：%.200s", tailSnippet(raw, 200))
	}
}

// TestGrepL0RecallByReadSection —— **端到端**：read_section 能取回匹配列表。
func TestGrepL0RecallByReadSection(t *testing.T) {
	root := grepFixture(t, 800)
	store := newGrepStore(t)
	g := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	p.ArtifactStore = store
	p.ContextWindow = 0
	r, _ := g.Execute(t.Context(), p)

	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.200s", r.Content)
	}

	rs := ReadSection("", nil)
	r2, _ := rs.Execute(t.Context(), &CallParams{
		Input:         map[string]any{"artifactId": m[1], "section": "L1-L20"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	if r2.IsError {
		t.Fatalf("read_section 取回失败：%s", r2.Content)
	}
	if !strings.Contains(r2.Content, "targetFn") {
		t.Errorf("取回内容应含匹配：%.200s", r2.Content)
	}
}

// TestGrepL0GracefulWhenSaveFails —— Save 失败时优雅降级。
func TestGrepL0GracefulWhenSaveFails(t *testing.T) {
	root := grepFixture(t, 800)
	bad := artifact.NewStore("bad\x00dir", "sess1", artifact.Options{})
	tool := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	p.ArtifactStore = bad
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Errorf("Save 失败不应让 grep 报错：%s", r.Content)
	}
	if strings.Contains(r.Content, "[artifact:") {
		t.Error("Save 失败时不应有 artifact 标记")
	}
	if !strings.Contains(r.Content, "targetFn") {
		t.Errorf("降级后应返回内容：%.200s", r.Content)
	}
}

// TestGrepWithoutStoreUnchanged —— 无 store 时行为不变（截断但无标记）。
func TestGrepWithoutStoreUnchanged(t *testing.T) {
	root := grepFixture(t, 5)
	tool := Grep(root)
	p := call(root, map[string]any{"pattern": "targetFn"})
	// 不设 ArtifactStore。

	r, _ := tool.Execute(t.Context(), p)
	if strings.Contains(r.Content, "[artifact:") {
		t.Error("无 store 不应有标记")
	}
	if !strings.Contains(r.Content, "targetFn") {
		t.Errorf("应返回匹配：%.200s", r.Content)
	}
}
