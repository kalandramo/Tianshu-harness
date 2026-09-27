package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// createpdf.go —— `create_pdf` 工具（第八十三刀 · W1-4）。
//
// 对账 TS `src/tools/create-pdf.ts`（约 160 行）。
//
// # 它不生成真 PDF
//
// TS 描述明说：「创建**打印就绪的 HTML 文档**，含专业排版和 @page 规则。
// 在任何浏览器中打开并打印为 PDF（Ctrl+P / Cmd+P → 另存为 PDF）。」
// 故 format 恒 `html`，输出是带 `@page` CSS 的 HTML。
//
// # 与 create_document 的关键差异
//
// `content` 是**原始 HTML**（不转义）——工具描述说「内容为原始 HTML——使用标准
// HTML 标签组织结构」。只有 `title` 转义。且 `content` 的校验**看 trim**
// （与 create_document 不同）。

// CreatePdf 创建 `create_pdf` 工具。
func CreatePdf(cwd string) Tool { return &createPdfTool{cwd: cwd} }

type createPdfTool struct{ cwd string }

func (t *createPdfTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "create_pdf",
		Description: "创建打印就绪的 HTML 文档，含专业排版和 @page 规则。在任何浏览器中打开并打印为 PDF（Ctrl+P / Cmd+P → 另存为 PDF）。\n\n" +
			"支持 A4 和 Letter 纸张大小、纵向和横向。内容为原始 HTML——使用标准 HTML 标签组织结构。\n\n" +
			"用 open_path 在默认浏览器中打开结果，然后打印为 PDF。\n\n" +
			"示例：\n" +
			"Good: create_pdf(destination_path=\"~/Desktop/report.html\", title=\"Q4 报告\", content=\"<h1>Q4 报告</h1><p>营收增长 15%...</p>\")\n" +
			"Good: create_pdf(destination_path=\"~/Desktop/invoice.html\", title=\"发票 #42\", content=\"<table>...</table>\", pageSize=\"Letter\")",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "title", "content", "orientation", "pageSize"},
			map[string]any{
				"destination_path": strProp("目标文件路径。应以 .html 结尾。可在项目之外。在浏览器中打开后打印为 PDF。"),
				"title":            strProp("文档标题（显示在浏览器标题栏）。"),
				"content":          strProp("HTML 正文内容。使用标准 HTML 标签（h1-h3、p、ul、ol、table、pre、code、div.page-break）。"),
				"orientation": enumPropOrdered(
					"页面方向。默认：portrait。",
					[]string{"portrait", "landscape"},
				),
				"pageSize": enumPropOrdered(
					"@page CSS 的纸张大小。默认：A4。",
					[]string{"A4", "Letter"},
				),
			}, "destination_path", "content"),
	}
}

func (t *createPdfTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	dest, _ := p.Input["destination_path"].(string)
	if strings.TrimSpace(dest) == "" {
		return contract.Result{Content: "错误：destination_path 为必填项", IsError: true}, nil
	}
	// **看 trim**（对账 TS `input.content.trim().length === 0`）——与 create_document 不同。
	content, _ := p.Input["content"].(string)
	if strings.TrimSpace(content) == "" {
		return contract.Result{Content: "错误：content 为必填项", IsError: true}, nil
	}

	title, _ := p.Input["title"].(string)
	orientation, _ := p.Input["orientation"].(string)
	pageSize, _ := p.Input["pageSize"].(string)
	rendered := renderPdf(title, content, orientation, pageSize)

	res, err := exportFileRun(map[string]any{
		"destination_path": dest,
		"content":          rendered,
	})
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{
		Content: fmt.Sprintf("已创建可打印的 html 文档（%d 字节）：%s\n\n在浏览器中打开并打印为 PDF（Cmd+P → 存储为 PDF）。",
			res.bytes, res.path),
	}, nil
}

func (t *createPdfTool) RequiresApproval(_ *CallParams) bool { return true }
func (t *createPdfTool) ConcurrencySafe() bool               { return false }
func (t *createPdfTool) Enabled() bool                       { return true }
func (t *createPdfTool) Timeout(_ *CallParams) time.Duration { return 0 }

// pdfPageSizes 对账 TS `PAGE_SIZE_CSS`。
var pdfPageSizes = map[string]string{
	"A4":     "210mm 297mm",
	"Letter": "8.5in 11in",
}

// renderPdf 对账 TS `renderPdfHtml`。
//
// 默认值：orientation→portrait、pageSize→A4、title→Document。
// **content 原样插入**（不转义）；title 转义。
func renderPdf(title, content, orientation, pageSize string) string {
	if orientation == "" {
		orientation = "portrait"
	}
	if pageSize == "" {
		pageSize = "A4"
	}
	sizeValue, ok := pdfPageSizes[pageSize]
	if !ok {
		sizeValue = pdfPageSizes["A4"]
	}
	safeTitle := escapeHTML(defaultStr(title, "Document"))

	maxWidth := "170mm"
	if orientation == "landscape" {
		maxWidth = "240mm"
	}

	return "<!doctype html>\n" +
		"<html>\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>" + safeTitle + "</title>\n" +
		"<style>\n" +
		"* { margin:0; padding:0; box-sizing:border-box; }\n" +
		"\n" +
		"@page {\n" +
		"  size: " + sizeValue + " " + orientation + ";\n" +
		"  margin: 20mm 18mm;\n" +
		"}\n" +
		"\n" +
		"body {\n" +
		"  font-family: system-ui, -apple-system, Segoe UI, sans-serif;\n" +
		"  font-size: 12pt;\n" +
		"  line-height: 1.7;\n" +
		"  color: #1e293b;\n" +
		"  max-width: " + maxWidth + ";\n" +
		"  margin: 0 auto;\n" +
		"  padding: 20mm 0;\n" +
		"}\n" +
		"\n" +
		"h1 { font-size: 1.75rem; margin: 0 0 0.5em; color: #0f172a; page-break-after: avoid; }\n" +
		"h2 { font-size: 1.35rem; margin: 1.2em 0 0.4em; color: #334155; page-break-after: avoid; }\n" +
		"h3 { font-size: 1.15rem; margin: 1em 0 0.3em; color: #475569; page-break-after: avoid; }\n" +
		"p { margin: 0 0 0.75em; orphans:3; widows:3; }\n" +
		"ul, ol { margin: 0 0 0.75em; padding-left: 1.5em; }\n" +
		"li { margin-bottom: 0.25em; }\n" +
		"pre {\n" +
		"  background: #f1f5f9; border:1px solid #e2e8f0; border-radius:6px;\n" +
		"  padding:12px 16px; font-size:0.85rem; overflow-x:auto;\n" +
		"  page-break-inside:avoid;\n" +
		"}\n" +
		"code { font-family: ui-monospace, 'Cascadia Code', 'Source Code Pro', monospace; font-size:0.9em; }\n" +
		"table { border-collapse:collapse; width:100%; margin:0.75em 0; }\n" +
		"th, td { border:1px solid #cbd5e1; padding:6px 10px; text-align:left; }\n" +
		"th { background:#f8fafc; font-weight:600; }\n" +
		".page-break { page-break-after:always; break-after:page; }\n" +
		"\n" +
		"@media print {\n" +
		"  body { padding:0; margin:0; }\n" +
		"}\n" +
		"</style>\n" +
		"</head>\n" +
		"<body>\n" +
		content + "\n" +
		"</body>\n" +
		"</html>\n"
}
