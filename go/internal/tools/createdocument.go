package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// createdocument.go —— `create_document` 工具（第八十三刀 · W1-1）。
//
// 对账 TS `src/tools/create-document.ts`（约 120 行）。
//
// # 为什么这是「接线」而非「造子系统」
//
// 依赖只有两样：`filepath.Ext`（对账 TS 的 `extname`）+ **`exportFileRun`**
// （第八十二刀已落地）。渲染逻辑是**纯字符串拼接**——零第三方库。
// 本工具是 `export_file` 的**下游**：复用其敏感门与 50MB 上限，
// 不自己实现写盘。
//
// **这也验证了排期判断**：门链（`approval_pathgrant.go:101`）早已为
// `create_document` 预留授权分支，本刀落地后那个分支首次有真实消费者。

// 文档格式常量（对账 TS 的 `DocumentFormat` 联合类型）。
const (
	docFormatTxt  = "txt"
	docFormatMd   = "md"
	docFormatHTML = "html"
	docFormatDoc  = "doc"
)

// CreateDocument 创建 `create_document` 工具。
func CreateDocument(cwd string) Tool { return &createDocumentTool{cwd: cwd} }

type createDocumentTool struct{ cwd string }

func (t *createDocumentTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "create_document",
		Description: "在外部或项目路径创建基础用户文档。\n\n" +
			"初版范围：纯文本、Markdown、HTML，以及 Word 可打开的 .doc（以 .doc 扩展名保存的 HTML 文档）。" +
			"用 export_file/open_path 处理原始二进制资产或打开结果。\n\n" +
			"示例：\n" +
			"Good: create_document(destination_path=\"~/Desktop/report.doc\", title=\"报告\", content=\"摘要...\")\n" +
			"Good: create_document(destination_path=\"H:\\\\zhuomian\\\\白嫖gpt\\\\notes.md\", content=\"# 笔记\\\\n...\")",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "title", "content", "format"},
			map[string]any{
				"destination_path": strProp("目标文件路径。可在项目之外。"),
				"title":            strProp("可选文档标题。"),
				"content":          strProp("文档正文。"),
				"format": enumPropOrdered(
					"可选输出格式。默认由文件扩展名推断。",
					[]string{"txt", "md", "html", "doc"},
				),
			}, "destination_path", "content"),
	}
}

func (t *createDocumentTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	dest, _ := p.Input["destination_path"].(string)
	if strings.TrimSpace(dest) == "" {
		return contract.Result{Content: "错误：destination_path 为必填项", IsError: true}, nil
	}
	// 对账 TS：`typeof input.content !== 'string'`——**不看 trim**。
	// 空字符串是合法输入（会创建空文档）。
	content, ok := p.Input["content"].(string)
	if !ok {
		return contract.Result{Content: "错误：content 为必填项", IsError: true}, nil
	}

	title, _ := p.Input["title"].(string)
	explicit, _ := p.Input["format"].(string)
	format := inferDocumentFormat(dest, explicit)
	rendered := renderDocument(title, content, format)

	// 复用 exportFileRun——同一敏感门 + 50MB 上限，不重复实现写盘。
	res, err := exportFileRun(map[string]any{
		"destination_path": dest,
		"content":          rendered,
	})
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{
		Content: fmt.Sprintf("已创建 %s 文档（%d 字节）：%s", format, res.bytes, res.path),
	}, nil
}

// RequiresApproval 恒 true（对账 TS `() => true`）——面向工作区外的输出。
func (t *createDocumentTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 false（对账 TS `() => false`）。
func (t *createDocumentTool) ConcurrencySafe() bool { return false }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *createDocumentTool) Enabled() bool { return true }

// Timeout 用默认（0）——纯本地字符串渲染 + 一次写盘。
func (t *createDocumentTool) Timeout(_ *CallParams) time.Duration { return 0 }

// inferDocumentFormat 对账 TS `inferDocumentFormat`——扩展名推断，显式优先。
func inferDocumentFormat(destinationPath, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch strings.ToLower(filepath.Ext(destinationPath)) {
	case ".md", ".markdown":
		return docFormatMd
	case ".html", ".htm":
		return docFormatHTML
	case ".doc":
		return docFormatDoc
	}
	return docFormatTxt
}

// renderDocument 对账 TS `renderDocument`——按格式选渲染器。
func renderDocument(title, content, format string) string {
	switch format {
	case docFormatMd:
		return renderMarkdown(title, content)
	case docFormatHTML, docFormatDoc:
		return renderHTMLDocument(title, content)
	}
	return renderPlainText(title, content)
}

// renderPlainText 对账 TS `renderPlainText`——`title ? title + "\n\n" + content : content`。
func renderPlainText(title, content string) string {
	if title != "" {
		return title + "\n\n" + content
	}
	return content
}

// renderMarkdown 对账 TS `renderMarkdown`。
//
// 无 title → 原样；content（trimStart 后）已以 `#` 开头 → 原样（不重复加标题）；
// 否则 `# title\n\ncontent`。
func renderMarkdown(title, content string) string {
	if title == "" {
		return content
	}
	if strings.HasPrefix(strings.TrimLeft(content, " \t\r\n"), "#") {
		return content
	}
	return "# " + title + "\n\n" + content
}

// renderHTMLDocument 对账 TS `renderHtmlDocument`。
//
// 逐行处理：空行 → `<p>&nbsp;</p>`，否则 `<p>转义(line)</p>`；
// 有 title 时在 body 前插 `<h1>转义(title)</h1>`。
func renderHTMLDocument(title, content string) string {
	safeTitle := escapeHTML(defaultStr(title, "Document"))
	lines := strings.Split(normalizeNewlines(content), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			parts = append(parts, "<p>&nbsp;</p>")
			continue
		}
		parts = append(parts, "<p>"+escapeHTML(line)+"</p>")
	}
	body := strings.Join(parts, "\n")

	heading := ""
	if title != "" {
		heading = "<h1>" + safeTitle + "</h1>\n"
	}

	return "<!doctype html>\n" +
		"<html>\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>" + safeTitle + "</title>\n" +
		"<style>\n" +
		"body { font-family: system-ui, -apple-system, Segoe UI, sans-serif; line-height: 1.6; margin: 40px; }\n" +
		"h1 { color: #0f172a; }\n" +
		"p { margin: 0 0 0.75em; }\n" +
		"</style>\n" +
		"</head>\n" +
		"<body>\n" +
		heading + body + "\n" +
		"</body>\n" +
		"</html>\n"
}

// escapeHTML 对账 TS `escapeHtml`——`&` `<` `>` `"` 四个实体。
//
// **顺序重要**：`&` 必须最先替换，否则后续替换产生的 `&lt;` 会被二次转义成
// `&amp;lt;`。TS 的链式 `.replace(/&/g,...).replace(/</g,...)` 也是这个顺序。
func escapeHTML(value string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
	)
	return r.Replace(value)
}

// normalizeNewlines 把 CRLF/CR 归一为 LF。
//
// 对账 TS 的 `/\r?\n/` 分割：TS 只把 `\r?\n` 当分隔符，单独的 `\r`（老 Mac 换行）
// **不**分隔。为语义等价，此处先把 `\r\n` 压成 `\n`，再把裸 `\r` 也压成 `\n`
// ——比 TS 略宽，但避免了「同一文档在 CRLF 与 LF 下渲染不同」的平台差异。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// defaultStr 返回 s，为空时返回 fallback。
func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
