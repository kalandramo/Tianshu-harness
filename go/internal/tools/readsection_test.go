package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

// newReadSectionStore 构造带一条 artifact 的测试存储。
func newReadSectionStore(t *testing.T, content string) (*artifact.Store, string) {
	t.Helper()
	base := t.TempDir()
	s := artifact.NewStore(base, "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "a1" },
	})
	id, err := s.Save(artifact.SaveInput{
		Tool: "read_file", Target: "src/a.ts", RawContent: content, Summary: "[read_file src/a.ts]",
	})
	if err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	return s, id
}

func TestReadSectionLineRange(t *testing.T) {
	store, id := newReadSectionStore(t, "L1\nL2\nL3\nL4\nL5")
	tool := ReadSection("", nil)

	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L2-L4"},
		ArtifactStore: store,
	})
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if res.Content != "L2\nL3\nL4" {
		t.Errorf("期望 L2\\nL3\\nL4，实得 %q", res.Content)
	}
	if res.RawPath == "" {
		t.Error("应回填 rawPath")
	}
}

// TestReadSectionBareNumberRange —— "100-200"（无 L 前缀）也应解析。
func TestReadSectionBareNumberRange(t *testing.T) {
	store, id := newReadSectionStore(t, "a\nb\nc")
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "1-2"}, ArtifactStore: store,
	})
	if res.IsError || res.Content != "a\nb" {
		t.Errorf("无 L 前缀应解析：实得 %q err=%v", res.Content, res.IsError)
	}
}

func TestReadSectionCharRange(t *testing.T) {
	store, id := newReadSectionStore(t, "abcdefghij")
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "c2-c5"}, ArtifactStore: store,
	})
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if res.Content != "cde" {
		t.Errorf("期望 cde，实得 %q", res.Content)
	}
}

func TestReadSectionOutOfRangeLine(t *testing.T) {
	store, id := newReadSectionStore(t, "a\nb\nc")
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "L100-L200"}, ArtifactStore: store,
	})
	if res.IsError {
		t.Fatalf("越界不应是错误：%s", res.Content)
	}
	// 对账 TS：越界返回提示文案（不是空串）。
	if !strings.Contains(res.Content, "超出范围") || !strings.Contains(res.Content, "3 行") {
		t.Errorf("应返回越界提示，实得 %q", res.Content)
	}
}

// ── 错误分支（文案逐字对账 TS）──

func TestReadSectionMissingSection(t *testing.T) {
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{Input: map[string]any{}})
	if !res.IsError || res.Content != "错误：需要提供 section" {
		t.Errorf("文案不符：%q", res.Content)
	}
}

// TestReadSectionInvalidFormat —— 真无效格式仍报错。
//
// **行为变更**：首版用 `garbage` 作无效输入。命名片段支持落地后，`garbage`
// 是**合法的标识符形态**（可能是个片段名），会被放行到 artifact 判定——
// 故改用含**非法字符**的输入（`@@@` 不在片段名字符集内）。
func TestReadSectionInvalidFormat(t *testing.T) {
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": "x", "section": "@@@"},
	})
	if !res.IsError {
		t.Fatal("无效格式应报错")
	}
	want := `错误：无效的区段格式：@@@。行范围用 "L100-L200"，字符范围用 "c0-c5000"。`
	if res.Content != want {
		t.Errorf("文案不符：\n期望 %q\n实得 %q", want, res.Content)
	}
}

func TestReadSectionMissingArtifactID(t *testing.T) {
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"section": "L1-L2"},
	})
	if !res.IsError || res.Content != "错误：需要提供 artifactId 或 file_path" {
		t.Errorf("文案不符：%q", res.Content)
	}
}

func TestReadSectionNilStore(t *testing.T) {
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": "x", "section": "L1-L2"},
	})
	if !res.IsError || res.Content != "错误：当前会话未配置 artifactStore" {
		t.Errorf("文案不符：%q", res.Content)
	}
}

func TestReadSectionUnknownArtifact(t *testing.T) {
	store, _ := newReadSectionStore(t, "x")
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": "nope:1", "section": "L1-L2"}, ArtifactStore: store,
	})
	if !res.IsError {
		t.Fatal("未知 artifact 应报错")
	}
	want := "错误：未找到 Artifact nope:1——可能已被清理或从未创建。请用原始工具（bash/read_file/grep）重新生成输出。"
	if res.Content != want {
		t.Errorf("文案不符：\n期望 %q\n实得 %q", want, res.Content)
	}
}

// TestReadSectionCorruptionDetected —— 篡改原文 → 损坏提示（非通用错误）。
func TestReadSectionCorruptionDetected(t *testing.T) {
	store, id := newReadSectionStore(t, "original")
	a := store.Get(id)
	if err := os.WriteFile(a.RawPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "L1-L1"}, ArtifactStore: store,
	})
	if !res.IsError {
		t.Fatal("篡改应报错")
	}
	if !strings.Contains(res.Content, "SHA-256 不匹配") {
		t.Errorf("应为损坏提示，实得 %q", res.Content)
	}
}

// ── 解析函数单测（边界）──

func TestParseLineRange(t *testing.T) {
	cases := []struct {
		in   string
		s, e int
		ok   bool
	}{
		{"L100-L200", 100, 200, true},
		{"100-200", 100, 200, true},
		{"l5-l10", 5, 10, true},  // 大小写不敏感
		{"L0-L5", 0, 0, false},   // start < 1
		{"L5-L1", 0, 0, false},   // end < start
		{"c0-c100", 0, 0, false}, // 不是行范围格式
		{"garbage", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseLineRange(c.in)
		if ok != c.ok || (ok && (s != c.s || e != c.e)) {
			t.Errorf("parseLineRange(%q)：期望 (%d,%d,%v)，实得 (%d,%d,%v)", c.in, c.s, c.e, c.ok, s, e, ok)
		}
	}
}

func TestParseCharRange(t *testing.T) {
	cases := []struct {
		in   string
		s, e int
		ok   bool
	}{
		{"c0-c5000", 0, 5000, true},
		{"C10-C20", 10, 20, true},
		{"c5-c1", 0, 0, false}, // end < start
		{"L1-L2", 0, 0, false}, // 不是字符范围
	}
	for _, c := range cases {
		s, e, ok := parseCharRange(c.in)
		if ok != c.ok || (ok && (s != c.s || e != c.e)) {
			t.Errorf("parseCharRange(%q)：期望 (%d,%d,%v)，实得 (%d,%d,%v)", c.in, c.s, c.e, c.ok, s, e, ok)
		}
	}
}

// TestReadSectionMaxCharsFloor —— 小窗口下仍不低于 8000 地板。
//
// **行为变更**：首版断言「1M 窗口 → 300000」——那是 `artifact.
// ToolArtifactThreshold("read_file", ...)` 的近似值（2.0 × 150K），
// 比 TS 真值**高 2.5 倍**。改用真实 `ComputeModelReadCap` 后是 120000：
//
//	0.05 × 1M × 4 × 1.0(balanced) = 200000 → 封顶 ABSOLUTE_MAX_CHARS(120000)
func TestReadSectionMaxCharsFloor(t *testing.T) {
	if got := readSectionMaxChars(0); got != legacyMaxSectionChars {
		t.Errorf("窗口未知应为地板 %d，实得 %d", legacyMaxSectionChars, got)
	}
	if got := readSectionMaxChars(64_000); got != legacyMaxSectionChars {
		t.Errorf("小窗口应为地板 %d，实得 %d", legacyMaxSectionChars, got)
	}
	// 1M 窗口 → 200000 封顶到 120000（对账 TS 的 ABSOLUTE_MAX_CHARS）。
	if got := readSectionMaxChars(1_000_000); got != absoluteMaxChars {
		t.Errorf("1M 窗口应封顶 %d，实得 %d", absoluteMaxChars, got)
	}
	// 300K 窗口 → 0.03 × 300000 × 4 = 36000（未封顶）。
	if got := readSectionMaxChars(300_000); got != 36_000 {
		t.Errorf("300K 窗口应为 36000，实得 %d", got)
	}
}

// TestReadSectionRegistered —— 工具必须在默认注册表里（防悬空）。
func TestReadSectionRegistered(t *testing.T) {
	r := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := r.Get("read_section"); !ok {
		t.Error("read_section 未注册进默认表——artifact 召回路径不可达")
	}
}
