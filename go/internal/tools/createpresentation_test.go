package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createpresentation_test.go —— `create_presentation` 工具（第八十三刀 · W1-3）。
//
// 对账 TS `src/tools/create-presentation.ts`（约 180 行）。
//
// `.ppt` 是「以 .ppt 扩展名保存的 HTML 文件」（TS 注释明说）——**纯字符串拼接**。
// `slides` 是**嵌套 object 数组**（每张幻灯片 {title, content}）——本刀是
// Go 侧首个用 `objPropMapOrdered` 的嵌套 schema（前置探针已验证键序稳定）。

// TestCreatePresentationRequiresSlides —— slides 须为非空数组。
//
// 对账 TS `renderPresentation`：`if (!Array.isArray(input.slides) || input.slides.length === 0)
// throw new Error('slides 必须为非空数组')`。
func TestCreatePresentationRequiresSlides(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "a.ppt")

	// 缺 slides
	res := runCreatePresentation(t, map[string]any{"destination_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "slides 必须为非空数组") {
		t.Errorf("缺 slides 应报错，实得 %q", res.Content)
	}
	// 空数组
	res = runCreatePresentation(t, map[string]any{"destination_path": dst, "slides": []any{}})
	if !res.IsError || !strings.Contains(res.Content, "slides 必须为非空数组") {
		t.Errorf("空 slides 应报错，实得 %q", res.Content)
	}
	// 非数组
	res = runCreatePresentation(t, map[string]any{"destination_path": dst, "slides": "x"})
	if !res.IsError || !strings.Contains(res.Content, "slides 必须为非空数组") {
		t.Errorf("slides 非数组应报错，实得 %q", res.Content)
	}
}

// TestCreatePresentationRequiresDestination —— destination_path 必填。
func TestCreatePresentationRequiresDestination(t *testing.T) {
	res := runCreatePresentation(t, map[string]any{
		"slides": []any{map[string]any{"title": "T"}},
	})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("缺 destination_path 应报错，实得 %q", res.Content)
	}
}

// TestCreatePresentationRender —— 每张幻灯片渲染为 section，含页码。
func TestCreatePresentationRender(t *testing.T) {
	got := renderPresentation("我的演示", []any{
		map[string]any{"title": "第一页", "content": "内容一"},
		map[string]any{"title": "第二页", "content": "内容二"},
	}, "light")

	// 每张幻灯片一个 section
	if n := strings.Count(got, "<section"); n != 2 {
		t.Errorf("应有 2 个 section，实得 %d", n)
	}
	if !strings.Contains(got, "第一页") || !strings.Contains(got, "第二页") {
		t.Errorf("应含两张幻灯片标题，实得 %q", got)
	}
	// 页码从 1 开始
	if !strings.Contains(got, ">1</div>") || !strings.Contains(got, ">2</div>") {
		t.Errorf("应有页码 1 与 2，实得 %q", got)
	}
	// 标题进 <title>
	if !strings.Contains(got, "<title>我的演示</title>") {
		t.Errorf("title 应进 <title>，实得 %q", got)
	}
}

// TestCreatePresentationTheme —— light/dark 主题切换背景色。
func TestCreatePresentationTheme(t *testing.T) {
	light := renderPresentation("T", []any{map[string]any{"title": "a"}}, "light")
	dark := renderPresentation("T", []any{map[string]any{"title": "a"}}, "dark")

	if light == dark {
		t.Error("light 与 dark 应产出不同内容")
	}
	// dark 用 #0f172a 背景（对账 TS）
	if !strings.Contains(dark, "#0f172a") {
		t.Errorf("dark 应含深色背景，实得 %q", dark)
	}
	if !strings.Contains(light, "#f8fafc") {
		t.Errorf("light 应含浅色背景，实得 %q", light)
	}
	// 默认 light（对账 TS `theme ?? 'light'`）
	if def := renderPresentation("T", []any{map[string]any{"title": "a"}}, ""); def != light {
		t.Error("空 theme 应默认 light")
	}
}

// TestCreatePresentationContentParagraphs —— content 按空行分段，单换行变 <br>。
//
// 对账 TS：`content.split(/\r?\n\r?\n/).map(p => ... p.replace(/\r?\n/g, '<br>'))`。
func TestCreatePresentationContentParagraphs(t *testing.T) {
	got := renderPresentation("T", []any{
		map[string]any{"title": "a", "content": "段一\n续行\n\n段二"},
	}, "light")

	if !strings.Contains(got, "段一<br>续行") {
		t.Errorf("单换行应变 <br>，实得 %q", got)
	}
	if n := strings.Count(got, "<p style="); n != 2 {
		t.Errorf("应分 2 段，实得 %d", n)
	}
}

// TestCreatePresentationEscapes —— HTML 转义。
func TestCreatePresentationEscapes(t *testing.T) {
	got := renderPresentation("T", []any{
		map[string]any{"title": "<script>x</script>", "content": `a & b "c"`},
	}, "light")

	if strings.Contains(got, "<script>x</script>") {
		t.Errorf("标题应转义，实得 %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;x&lt;/script&gt;") {
		t.Errorf("应含转义后的标题，实得 %q", got)
	}
	if !strings.Contains(got, "&amp;") || !strings.Contains(got, "&quot;") {
		t.Errorf("content 应转义 & 与引号，实得 %q", got)
	}
}

// TestCreatePresentationWritesFile —— 端到端落盘。
func TestCreatePresentationWritesFile(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "deck", "x.ppt")

	res := runCreatePresentation(t, map[string]any{
		"destination_path": dst,
		"title":            "演示",
		"slides":           []any{map[string]any{"title": "第一页", "content": "内容"}},
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应落盘：%v", err)
	}
	if !strings.Contains(string(got), "第一页") {
		t.Errorf("文件内容应含幻灯片标题，实得 %q", got)
	}
	// 文案：`已创建 ${format} 演示文稿（含幻灯片，${bytes} 字节）：${path}`
	if !strings.Contains(res.Content, "已创建 ppt 演示文稿（含幻灯片，") {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
}

// TestCreatePresentationDefinitionParity —— definition 逐字对账（含嵌套 slides 键序）。
func TestCreatePresentationDefinitionParity(t *testing.T) {
	def := CreatePresentation(t.TempDir()).Definition()

	if def.Name != "create_presentation" {
		t.Errorf("name 应为 create_presentation，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "创建演示文稿（幻灯片），以 .ppt 扩展名保存的 HTML 文件。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	wantOrder := []string{"destination_path", "title", "slides", "theme"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 2 ||
		def.InputSchema.Required[0] != "destination_path" || def.InputSchema.Required[1] != "slides" {
		t.Errorf("required 应为 [destination_path slides]，实得 %#v", def.InputSchema.Required)
	}
	// **关键**：slides 是嵌套 object 数组——items.properties 的键序应为 title→content
	sp, ok := def.InputSchema.Properties["slides"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("slides 应为有序结构，实得 %T", def.InputSchema.Properties["slides"])
	}
	raw := sp.Marshal()
	// TS 源 `create-presentation.ts` 的 slides.items.properties.content.description
	// 逐字是「幻灯片正文。用 \n 换行，双 \n\n 分段。」（含反斜杠 n 字面量）。
	want := `{"type":"array","items":{"type":"object","properties":{"title":{"type":"string","description":"幻灯片标题。"},"content":{"type":"string","description":"幻灯片正文。用 \\n 换行，双 \\n\\n 分段。"}}},"description":"按顺序排列的幻灯片。每张有标题和内容。"}`
	if raw != want {
		t.Errorf("slides 嵌套 schema 应逐字对账：\n实得 %s\n期望 %s", raw, want)
	}
	// theme 的 enum
	tp, _ := def.InputSchema.Properties["theme"].(interface{ Marshal() string })
	if !strings.HasPrefix(tp.Marshal(), `{"type":"string","enum":["light","dark"],"description":`) {
		t.Errorf("theme 键序应 type→enum→description，实得 %s", tp.Marshal())
	}
}

// TestCreatePresentationApprovalSemantics —— 恒需审批 + 非并发安全。
func TestCreatePresentationApprovalSemantics(t *testing.T) {
	tool := CreatePresentation(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批")
	}
	if tool.ConcurrencySafe() {
		t.Error("不应并发安全")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

func runCreatePresentation(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := CreatePresentation(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
