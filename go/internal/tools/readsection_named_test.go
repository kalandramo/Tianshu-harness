package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

// newStoreWithSections 构造带**命名片段**的 artifact。
//
// 手工构造（而非走生产端）：Go 侧 summarize 尚未移植，此处先验证**消费端**
// 的行为——这正是「消费端先行」的意义：测试先跑通，生产端才知道要产什么名字。
func newStoreWithSections(t *testing.T, content string, sections []artifact.ArtifactSection) (*artifact.Store, string) {
	t.Helper()
	s := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "sec" },
	})
	id, err := s.Save(artifact.SaveInput{
		Tool: "read_file", Target: "src/a.ts", RawContent: content,
		Summary: "ts file, 10 lines.", Sections: sections,
	})
	if err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	return s, id
}

// sampleContent 是 6 行内容，行号可辨识。
const sampleContent = "L1-imports\nL2-blank\nL3-export-foo\nL4-body\nL5-more\nL6-tail"

var sampleSections = []artifact.ArtifactSection{
	{Name: "imports", LineStart: 1, LineEnd: 2, CharCount: 20},
	{Name: "export:foo", LineStart: 3, LineEnd: 5, CharCount: 30},
}

// TestReadSectionNamedSection —— **核心**：按命名片段取回。
//
// 这是让 `Artifact.Sections` 从「写进 JSON 的死数据」变成「活功能」的那一环。
func TestReadSectionNamedSection(t *testing.T) {
	store, id := newStoreWithSections(t, sampleContent, sampleSections)
	tool := ReadSection("", nil)

	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "imports"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("命名片段应可召回，实得错误：%s", res.Content)
	}
	if res.Content != "L1-imports\nL2-blank" {
		t.Errorf("imports 片段内容不符：%q", res.Content)
	}
}

// TestReadSectionNamedSectionSecond —— 第二个片段（带冒号的名字）。
func TestReadSectionNamedSectionSecond(t *testing.T) {
	store, id := newStoreWithSections(t, sampleContent, sampleSections)
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "export:foo"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if res.Content != "L3-export-foo\nL4-body\nL5-more" {
		t.Errorf("export:foo 片段内容不符：%q", res.Content)
	}
}

// TestReadSectionNamedSectionUnknownListsAvailable —— **可发现性**：未命中时列出可用片段。
//
// 模型看不到 `Sections`——不在错误里列出来，它只能反复试位置格式。
func TestReadSectionNamedSectionUnknownListsAvailable(t *testing.T) {
	store, id := newStoreWithSections(t, sampleContent, sampleSections)
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "nonexistent"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if !res.IsError {
		t.Fatal("未知片段应报错")
	}
	if !strings.Contains(res.Content, "没有命名片段") {
		t.Errorf("应说明片段不存在，实得 %q", res.Content)
	}
	// 必须列出可用的片段名（可发现性）。
	if !strings.Contains(res.Content, "imports") || !strings.Contains(res.Content, "export:foo") {
		t.Errorf("应列出可用片段，实得 %q", res.Content)
	}
}

// TestReadSectionNoSectionsGivesGuidance —— 无片段的 artifact 给出替代指引。
func TestReadSectionNoSectionsGivesGuidance(t *testing.T) {
	store, id := newStoreWithSections(t, "plain content", nil)
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "imports"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if !res.IsError {
		t.Fatal("无片段时应报错")
	}
	if !strings.Contains(res.Content, "无命名片段") {
		t.Errorf("应说明该 artifact 无片段，实得 %q", res.Content)
	}
	// 应指引位置格式。
	if !strings.Contains(res.Content, "L100-L200") {
		t.Errorf("应指引位置格式，实得 %q", res.Content)
	}
}

// TestReadSectionPositionStillWorks —— **不回归**：位置格式仍正常。
//
// 命名分支的插入不得破坏位置解析。
func TestReadSectionPositionStillWorks(t *testing.T) {
	store, id := newStoreWithSections(t, sampleContent, sampleSections)
	tool := ReadSection("", nil)

	// 行范围。
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L3-L4"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	if res.IsError || res.Content != "L3-export-foo\nL4-body" {
		t.Errorf("行范围应正常，实得 %q (err=%v)", res.Content, res.IsError)
	}

	// 字符范围。
	res2, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "c0-c11"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	if res2.IsError || res2.Content != "L1-imports\n" {
		t.Errorf("字符范围应正常，实得 %q (err=%v)", res2.Content, res2.IsError)
	}
}

// TestReadSectionNamedVsPositionPrecedence —— 命名与位置不冲突（各走各的）。
//
// 若某 artifact 恰有一个叫 `L1-L2` 的片段（异常数据），位置格式仍应优先
// 走解析链——命名匹配只对**不在位置格式内**的串生效。
func TestReadSectionNamedVsPositionPrecedence(t *testing.T) {
	// 构造一个名字像位置格式的片段（异常数据）。
	weird := []artifact.ArtifactSection{{Name: "L1-L2", LineStart: 5, LineEnd: 6, CharCount: 10}}
	store, id := newStoreWithSections(t, sampleContent, weird)
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L2"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	// 位置格式优先 → 返回行 1-2，而非片段指的行 5-6。
	if res.Content != "L1-imports\nL2-blank" {
		t.Errorf("位置格式应优先，实得 %q", res.Content)
	}
}

// ── 单元：isPlausibleSectionName ──

func TestIsPlausibleSectionName(t *testing.T) {
	cases := map[string]bool{
		"imports":       true,
		"export:foo":    true,
		"function:bar":  true,
		"heading:Title": true,
		"key:name":      true,
		"msg0 turn1":    true,
		"@@@":           false, // 非法字符
		"L100-L200":     false, // 位置格式（但由 parseLineRange 先处理）
		"L100-":         false, // 位置格式残骸
		"":              false,
		"中文名":           false, // 非 ASCII（片段名是 ASCII 的）
	}
	for in, want := range cases {
		if got := isPlausibleSectionName(in); got != want {
			t.Errorf("isPlausibleSectionName(%q)：期望 %v，实得 %v", in, want, got)
		}
	}
}

// ── 单元：extractNamedSection ──

func TestExtractNamedSectionExactMatch(t *testing.T) {
	got := extractNamedSection(sampleContent, sampleSections, "imports")
	if got == nil || *got != "L1-imports\nL2-blank" {
		t.Errorf("精确匹配失败：%v", got)
	}
	// 未命中 → nil（调用方回退位置解析）。
	if extractNamedSection(sampleContent, sampleSections, "nope") != nil {
		t.Error("未命中应返回 nil")
	}
}

func TestExtractNamedSectionClampsOutOfRange(t *testing.T) {
	// 片段行范围越界 → 截到实际行数（不 panic）。
	big := []artifact.ArtifactSection{{Name: "huge", LineStart: 2, LineEnd: 9999}}
	got := extractNamedSection(sampleContent, big, "huge")
	if got == nil {
		t.Fatal("应返回内容")
	}
	if !strings.HasPrefix(*got, "L2-blank") {
		t.Errorf("应从行 2 开始，实得 %q", *got)
	}
	// 起点越界 → 空串。
	beyond := []artifact.ArtifactSection{{Name: "beyond", LineStart: 100, LineEnd: 200}}
	got2 := extractNamedSection(sampleContent, beyond, "beyond")
	if got2 == nil || *got2 != "" {
		t.Errorf("起点越界应返回空串，实得 %v", got2)
	}
}

// ── 单元：availableSectionNames ──

func TestAvailableSectionNames(t *testing.T) {
	if got := availableSectionNames(nil); !strings.Contains(got, "无命名片段") {
		t.Errorf("空片段应给指引，实得 %q", got)
	}
	got := availableSectionNames(sampleSections)
	if !strings.Contains(got, "imports") || !strings.Contains(got, "export:foo") {
		t.Errorf("应列出名字，实得 %q", got)
	}
	// 超 12 个时截断并标注。
	many := make([]artifact.ArtifactSection, 20)
	for i := range many {
		many[i] = artifact.ArtifactSection{Name: "s" + itoaT(i)}
	}
	if got := availableSectionNames(many); !strings.Contains(got, "(+8)") {
		t.Errorf("超 12 个应标注剩余数，实得 %q", got)
	}
}

// TestReadSectionNamedSectionSurvivesReload —— **跨实例读回**（真实场景）。
//
// 上面的测试都用同一 Store 实例（内存命中）。真实运行时 artifact 会
// 跨进程/跨会话从 `_index.jsonl` 读回——若 `Sections` 没持久化，
// 命名片段在真实场景**永远查不到**，而同实例测试会假绿。
func TestReadSectionNamedSectionSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	opts := artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "reload" },
	}
	// 实例 1：写入。
	s1 := artifact.NewStore(dir, "sess1", opts)
	id, err := s1.Save(artifact.SaveInput{
		Tool: "read_file", Target: "src/a.ts", RawContent: sampleContent,
		Summary: "ts file.", Sections: sampleSections,
	})
	if err != nil {
		t.Fatalf("Save 失败：%v", err)
	}

	// 实例 2：从磁盘重新加载（模拟重启/跨进程）。
	s2 := artifact.NewStore(dir, "sess1", opts)
	reloaded := s2.Get(id)
	if reloaded == nil {
		t.Fatalf("跨实例 Get 失败——id=%s", id)
	}
	if len(reloaded.Sections) != len(sampleSections) {
		t.Fatalf("Sections 未持久化：期望 %d 个，实得 %d 个",
			len(sampleSections), len(reloaded.Sections))
	}

	// 用重新加载的 store 走 read_section 命名分支。
	tool := ReadSection("", nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "imports"},
		ArtifactStore: s2, ContextWindow: 1_000_000,
	})
	if res.IsError {
		t.Fatalf("跨实例命名召回失败：%s", res.Content)
	}
	if res.Content != "L1-imports\nL2-blank" {
		t.Errorf("跨实例内容不符：%q", res.Content)
	}
}
