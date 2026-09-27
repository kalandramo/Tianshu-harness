package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createimage_test.go —— `create_image` 工具（第八十三刀 · W1-5）。
//
// 对账 TS `src/tools/create-image.ts`（约 100 行）。
//
// 纯字符串（SVG 包裹 + markdown fence 剥离）——零第三方库。
// **注意两处细节**：
//   - `width`/`height` 是 `type: 'number'`（非 integer）
//   - 校验消息有**两个不同版本**：`createImage` 用「svg 为必填项」，
//     `renderImage` 用「svg 内容为必填项」

// TestCreateImageRequiresSvg —— svg 必填（对账 TS 的 `'svg 为必填项'`）。
func TestCreateImageRequiresSvg(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "a.svg")

	res := runCreateImage(t, map[string]any{"destination_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "svg 为必填项") {
		t.Errorf("缺 svg 应报错（文案「svg 为必填项」），实得 %q", res.Content)
	}
	// 空白也算缺失（对账 TS `input.svg.trim().length === 0`）
	res = runCreateImage(t, map[string]any{"destination_path": dst, "svg": "   "})
	if !res.IsError || !strings.Contains(res.Content, "svg 为必填项") {
		t.Errorf("空白 svg 应报错，实得 %q", res.Content)
	}
}

// TestCreateImageRequiresDestination —— destination_path 必填。
func TestCreateImageRequiresDestination(t *testing.T) {
	res := runCreateImage(t, map[string]any{"svg": "<rect/>"})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("缺 destination_path 应报错，实得 %q", res.Content)
	}
}

// TestCreateImageExtractSvgInner —— 三种输入形态的归一。
//
// 对账 TS `extractSvgInner`：
//  1. 已是完整 `<svg>` 文档 → 原样
//  2. markdown 代码围栏 → 剥离围栏
//  3. 原始内部元素 → 原样
func TestCreateImageExtractSvgInner(t *testing.T) {
	// ① 完整 svg → 原样
	full := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`
	if got := extractSvgInner(full); got != full {
		t.Errorf("完整 svg 应原样，实得 %q", got)
	}
	// ② markdown 围栏 → 剥离
	fenced := "```svg\n<rect x=\"1\"/>\n```"
	if got := extractSvgInner(fenced); got != `<rect x="1"/>` {
		t.Errorf("应剥离围栏，实得 %q", got)
	}
	// 无语言标注的围栏也可
	fenced2 := "```\n<rect x=\"2\"/>\n```"
	if got := extractSvgInner(fenced2); got != `<rect x="2"/>` {
		t.Errorf("无语言围栏也应剥离，实得 %q", got)
	}
	// ③ 原始元素 → 原样
	raw := `<rect x="10" y="20"/>`
	if got := extractSvgInner(raw); got != raw {
		t.Errorf("原始元素应原样，实得 %q", got)
	}
	// 前后空白被 trim
	if got := extractSvgInner("  <rect/>  "); got != "<rect/>" {
		t.Errorf("应 trim 空白，实得 %q", got)
	}
}

// mustRender 断言 renderImage 不报错并返回内容。
func mustRender(t *testing.T, svg string, w, h *float64) string {
	t.Helper()
	got, err := renderImage(svg, w, h)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	return got
}

// TestCreateImageWrapSvg —— 包裹 svg 元素 + 可选宽高。
//
// 对账 TS `wrapSvg`：`<svg ${attrs}>\n${inner}\n</svg>\n`。
// 无宽高时只有 xmlns；两者都给时加 viewBox。
func TestCreateImageWrapSvg(t *testing.T) {
	// 无宽高
	got := mustRender(t, "<rect/>", nil, nil)
	want := "<svg xmlns=\"http://www.w3.org/2000/svg\">\n<rect/>\n</svg>\n"
	if got != want {
		t.Errorf("无宽高应为 %q，实得 %q", want, got)
	}
	// 只给 width
	w := 200.0
	got = mustRender(t, "<rect/>", &w, nil)
	if !strings.Contains(got, `width="200"`) {
		t.Errorf("应含 width，实得 %q", got)
	}
	if strings.Contains(got, "viewBox") {
		t.Errorf("只给 width 不应有 viewBox，实得 %q", got)
	}
	// 两者都给 → 加 viewBox
	h := 120.0
	got = mustRender(t, "<rect/>", &w, &h)
	if !strings.Contains(got, `viewBox="0 0 200 120"`) {
		t.Errorf("两者都给应有 viewBox，实得 %q", got)
	}
}

// TestCreateImageAlreadySvgPassthrough —— 已是完整 svg 时不再包裹，只补换行。
//
// 对账 TS：`/^<svg\b/i.test(inner) ? inner + '\n' : wrapSvg(...)`。
func TestCreateImageAlreadySvgPassthrough(t *testing.T) {
	full := `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
	got := mustRender(t, full, nil, nil)
	if got != full+"\n" {
		t.Errorf("完整 svg 应只补换行，实得 %q", got)
	}
	// 大小写不敏感（对账 TS `/i`）
	upper := `<SVG xmlns="x"></SVG>`
	got = mustRender(t, upper, nil, nil)
	if got != upper+"\n" {
		t.Errorf("大写 SVG 也应识别，实得 %q", got)
	}
}

// TestCreateImageRenderRequiresSvg —— renderImage 自身的消息是「svg 内容为必填项」。
//
// **这是 TS 侧的两个不同消息**——必须都保留（不同调用路径给不同文案）。
func TestCreateImageRenderRequiresSvg(t *testing.T) {
	_, err := renderImage("   ", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "svg 内容为必填项") {
		t.Errorf("renderImage 内部消息应为「svg 内容为必填项」，实得 %v", err)
	}
}

// TestCreateImageWritesFile —— 端到端落盘。
func TestCreateImageWritesFile(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "img", "logo.svg")

	res := runCreateImage(t, map[string]any{
		"destination_path": dst,
		"svg":              `<circle cx="50" cy="50" r="40" fill="#38bdf8"/>`,
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应落盘：%v", err)
	}
	if !strings.HasPrefix(string(got), "<svg ") {
		t.Errorf("应以 <svg 开头，实得 %q", got)
	}
	if !strings.Contains(string(got), "<circle") {
		t.Errorf("应含原始元素，实得 %q", got)
	}
	// 文案：`已创建 ${format} 图片（${bytes} 字节）：${path}`
	if !strings.Contains(res.Content, "已创建 svg 图片（") {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
}

// TestCreateImageDefinitionParity —— definition 逐字对账（含 width/height 为 number）。
func TestCreateImageDefinitionParity(t *testing.T) {
	def := CreateImage(t.TempDir()).Definition()

	if def.Name != "create_image" {
		t.Errorf("name 应为 create_image，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "从 SVG 标记创建 SVG 图片文件。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	wantOrder := []string{"destination_path", "svg", "width", "height"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 2 ||
		def.InputSchema.Required[0] != "destination_path" || def.InputSchema.Required[1] != "svg" {
		t.Errorf("required 应为 [destination_path svg]，实得 %#v", def.InputSchema.Required)
	}
	// **关键**：width/height 是 `number`（非 integer）
	wp, _ := def.InputSchema.Properties["width"].(interface{ Marshal() string })
	if !strings.HasPrefix(wp.Marshal(), `{"type":"number","description":`) {
		t.Errorf("width 应为 number 类型，实得 %s", wp.Marshal())
	}
	hp, _ := def.InputSchema.Properties["height"].(interface{ Marshal() string })
	if !strings.HasPrefix(hp.Marshal(), `{"type":"number","description":`) {
		t.Errorf("height 应为 number 类型，实得 %s", hp.Marshal())
	}
}

// TestCreateImageApprovalSemantics —— 恒需审批 + 非并发安全。
func TestCreateImageApprovalSemantics(t *testing.T) {
	tool := CreateImage(t.TempDir())
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

func runCreateImage(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := CreateImage(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
