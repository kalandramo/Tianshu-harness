package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/contract"
)

// createpresentation.go —— `create_presentation` 工具（第八十三刀 · W1-3）。
//
// 对账 TS `src/tools/create-presentation.ts`（约 180 行）。
//
// # `.ppt` 的真相
//
// TS 注释明说：「创建演示文稿（幻灯片），**以 .ppt 扩展名保存的 HTML 文件**。
// 可在任何浏览器中打开。」——纯字符串拼接，不是真 OOXML。
//
// # 嵌套 schema
//
// `slides` 是**嵌套 object 数组**（每张 {title, content}）——Go 侧首个用
// `objPropMapOrdered` 的嵌套 schema。前置探针已验证键序字节稳定。

// CreatePresentation 创建 `create_presentation` 工具。
func CreatePresentation(cwd string) Tool { return &createPresentationTool{cwd: cwd} }

type createPresentationTool struct{ cwd string }

func (t *createPresentationTool) Definition() contract.Definition {
	// slides items：object {title, content}（声明序对账 TS）。
	slideItems := objPropMapOrdered(
		[]string{"title", "content"},
		map[string]any{
			"title":   strProp("幻灯片标题。"),
			"content": strProp("幻灯片正文。用 \\n 换行，双 \\n\\n 分段。"),
		},
	)
	slides := wireArrItemsFirst("按顺序排列的幻灯片。每张有标题和内容。", slideItems)

	return contract.Definition{
		Name: "create_presentation",
		Description: "创建演示文稿（幻灯片），以 .ppt 扩展名保存的 HTML 文件。可在任何浏览器中打开。\n\n" +
			"每张幻灯片是一个全屏区域，含标题和内容。用于演示、幻灯片组和可视化摘要。\n\n" +
			"用 open_path 在默认浏览器中打开结果。\n\n" +
			"示例：\n" +
			"Good: create_presentation(destination_path=\"~/Desktop/deck.ppt\", title=\"Q4 回顾\", slides=[{title:\"概览\", content:\"关键结果...\"}, {title:\"下一步\", content:\"1. 发布\\\\n2. 迭代\"}])\n" +
			"Good: create_presentation(destination_path=\"~/Desktop/dark-deck.ppt\", title=\"路演\", slides=[{title:\"问题\", content:\"...\"}], theme=\"dark\")",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "title", "slides", "theme"},
			map[string]any{
				"destination_path": strProp("目标文件路径。应以 .ppt 或 .html 结尾。可在项目之外。"),
				"title":            strProp("可选演示文稿标题（显示在浏览器标题栏和 HTML title 中）。"),
				"slides":           slides,
				"theme": enumPropOrdered(
					"颜色主题。默认：light。",
					[]string{"light", "dark"},
				),
			}, "destination_path", "slides"),
	}
}

func (t *createPresentationTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	dest, _ := p.Input["destination_path"].(string)
	if strings.TrimSpace(dest) == "" {
		return contract.Result{Content: "错误：destination_path 为必填项", IsError: true}, nil
	}
	slidesRaw, ok := p.Input["slides"].([]any)
	if !ok || len(slidesRaw) == 0 {
		return contract.Result{Content: "错误：slides 必须为非空数组", IsError: true}, nil
	}

	title, _ := p.Input["title"].(string)
	theme, _ := p.Input["theme"].(string)
	rendered := renderPresentation(title, slidesRaw, theme)

	res, err := exportFileRun(map[string]any{
		"destination_path": dest,
		"content":          rendered,
	})
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{
		Content: fmt.Sprintf("已创建 ppt 演示文稿（含幻灯片，%d 字节）：%s", res.bytes, res.path),
	}, nil
}

func (t *createPresentationTool) RequiresApproval(_ *CallParams) bool { return true }
func (t *createPresentationTool) ConcurrencySafe() bool               { return false }
func (t *createPresentationTool) Enabled() bool                       { return true }
func (t *createPresentationTool) Timeout(_ *CallParams) time.Duration { return 0 }

// presentationTheme 是对账 TS 的主题配色表。
type presentationTheme struct {
	bg      string
	fg      string
	accent  string
	mutedFg string
}

// themeFor 对账 TS `renderSlideHtml` 里的三元表达式链。
func themeFor(name string) presentationTheme {
	if name == "dark" {
		return presentationTheme{bg: "#0f172a", fg: "#e2e8f0", accent: "#38bdf8", mutedFg: "#94a3b8"}
	}
	return presentationTheme{bg: "#f8fafc", fg: "#1e293b", accent: "#2563eb", mutedFg: "#64748b"}
}

// renderPresentation 对账 TS `renderPresentation` + `renderPresentationHtml`。
//
// 每张幻灯片一个 `<section>`；content 按空行分段、段内单换行变 `<br>`；
// 页码从 1 起，绝对定位右下角。
func renderPresentation(title string, slides []any, theme string) string {
	if theme == "" {
		theme = "light"
	}
	th := themeFor(theme)
	safeTitle := escapeHTML(defaultStr(title, "Presentation"))

	var sections []string
	for i, raw := range slides {
		m := anyMap(raw)
		sTitle, _ := m["title"].(string)
		sContent, _ := m["content"].(string)
		sections = append(sections, renderSlide(sTitle, sContent, i+1, th))
	}

	return "<!doctype html>\n" +
		"<html>\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>" + safeTitle + "</title>\n" +
		"<style>\n" +
		"* { margin:0; padding:0; box-sizing:border-box; }\n" +
		"body {\n" +
		"  font-family: system-ui, -apple-system, Segoe UI, sans-serif;\n" +
		"  background:" + th.bg + "; color:" + th.fg + ";\n" +
		"  display:flex; flex-direction:column; align-items:center;\n" +
		"}\n" +
		"@media print {\n" +
		"  body { background:white; }\n" +
		"  section { page-break-after:always; break-after:page; }\n" +
		"}\n" +
		"</style>\n" +
		"</head>\n" +
		"<body>\n" +
		strings.Join(sections, "\n") + "\n" +
		"</body>\n" +
		"</html>\n"
}

// renderSlide 对账 TS `renderSlideHtml`。
func renderSlide(title, content string, index int, th presentationTheme) string {
	titleHTML := ""
	if title != "" {
		titleHTML = "<h1 style=\"font-size:2.25rem;margin:0 0 0.5em;color:" + th.accent +
			";font-weight:700\">" + escapeHTML(title) + "</h1>"
	}

	contentHTML := ""
	if content != "" {
		// 按空行分段；段内单换行 → <br>。
		paras := splitParagraphs(content)
		var ps []string
		for _, para := range paras {
			if strings.TrimSpace(para) == "" {
				ps = append(ps, "")
				continue
			}
			withBr := strings.ReplaceAll(escapeHTML(para), "\n", "<br>")
			ps = append(ps, "<p style=\"margin:0 0 1em\">"+withBr+"</p>")
		}
		contentHTML = "<div style=\"font-size:1.25rem;line-height:1.8;color:" + th.fg + "\">" +
			strings.Join(ps, "\n") + "</div>"
	}

	pageNum := "<div style=\"position:absolute;bottom:24px;right:40px;font-size:0.875rem;color:" +
		th.mutedFg + "\">" + fmt.Sprintf("%d", index) + "</div>"

	return "<section style=\"\n" +
		"  position:relative;\n" +
		"  width:100vw;max-width:960px;min-height:100vh;\n" +
		"  display:flex;flex-direction:column;justify-content:center;\n" +
		"  padding:60px 80px;box-sizing:border-box;\n" +
		"  background:" + th.bg + ";color:" + th.fg + ";\n" +
		"  page-break-after:always;\n" +
		"  break-after:page;\n" +
		"\">\n" +
		titleHTML + "\n" +
		contentHTML + "\n" +
		pageNum + "\n" +
		"</section>"
}

// splitParagraphs 按空行分段（对账 TS `split(/\r?\n\r?\n/)`）。
func splitParagraphs(s string) []string {
	s = normalizeNewlines(s)
	return strings.Split(s, "\n\n")
}

// anyMap 把 `any` 转成 `map[string]any`（非 map 时返回空 map）。
func anyMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// 确保 wire 包被引用（slides 的 items 构造用了 *wire.OrderedMap）。
var _ = wire.NewOrderedMap

// 确保 filepath 被引用（保持与其他工具一致的导入形态）。
var _ = filepath.Ext
