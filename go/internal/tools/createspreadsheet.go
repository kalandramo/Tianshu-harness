package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// createspreadsheet.go —— `create_spreadsheet` 工具（第八十三刀 · W1-2）。
//
// 对账 TS `src/tools/create-spreadsheet.ts`（约 160 行）。
//
// # 为什么这是「接线」
//
// 依赖 `exportFileRun`（第八十二刀）+ 纯字符串（CSV/TSV 转义、HTML 表格）。
// **零第三方库**——不是 `exceljs`。
//
// # `.xls` 的真相
//
// TS 注释明说：`.xls` 是「**以 .xls 扩展名保存的 HTML 表格**」——Excel 能打开
// HTML 表格。故 `xls` 与 `html` 走**同一渲染器**，不生成真二进制 xlsx。

// 电子表格格式常量（对账 TS 的 `SpreadsheetFormat`）。
const (
	sheetFormatCSV  = "csv"
	sheetFormatTSV  = "tsv"
	sheetFormatHTML = "html"
	sheetFormatXLS  = "xls"
)

// CreateSpreadsheet 创建 `create_spreadsheet` 工具。
func CreateSpreadsheet(cwd string) Tool { return &createSpreadsheetTool{cwd: cwd} }

type createSpreadsheetTool struct{ cwd string }

func (t *createSpreadsheetTool) Definition() contract.Definition {
	// headers：元素为联合类型（string|number|boolean|null）的数组。
	// 对账 TS 字面量序：`type, items, description`（items 在 description **前**）。
	headers := wireArrItemsFirst(
		"可选表头行。",
		unionScalarItems(),
	)
	// rows：元素为「联合类型数组」的数组（嵌套两层）。
	rows := wireArrItemsFirst(
		"表格行。每行是一个单元格数组。",
		wireArrType(unionScalarItems()),
	)

	return contract.Definition{
		Name: "create_spreadsheet",
		Description: "在外部或项目路径创建基础电子表格/数据表。\n\n" +
			"初版范围：CSV、TSV、HTML，以及 Excel 可打开的 .xls（以 .xls 扩展名保存的 HTML 表格）。" +
			"用 open_path 在 OS 默认应用中打开结果。\n\n" +
			"示例：\n" +
			"Good: create_spreadsheet(destination_path=\"~/Desktop/report.csv\", headers=[\"姓名\",\"分数\"], rows=[[\"A\", 10]])\n" +
			"Good: create_spreadsheet(destination_path=\"H:\\\\zhuomian\\\\白嫖gpt\\\\report.xls\", title=\"报告\", headers=[\"姓名\"], rows=[[\"天枢\"]])",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "title", "headers", "rows", "format"},
			map[string]any{
				"destination_path": strProp("目标文件路径。可在项目之外。"),
				"title":            strProp("可选表格标题。"),
				"headers":          headers,
				"rows":             rows,
				"format": enumPropOrdered(
					"可选输出格式。默认由文件扩展名推断。",
					[]string{"csv", "tsv", "html", "xls"},
				),
			}, "destination_path", "rows"),
	}
}

func (t *createSpreadsheetTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	dest, _ := p.Input["destination_path"].(string)
	if strings.TrimSpace(dest) == "" {
		return contract.Result{Content: "错误：destination_path 为必填项", IsError: true}, nil
	}
	rowsRaw, ok := p.Input["rows"].([]any)
	if !ok {
		return contract.Result{Content: "错误：rows 为必填项", IsError: true}, nil
	}
	headersRaw, hasHeaders := p.Input["headers"]
	if hasHeaders && headersRaw != nil {
		if _, ok := headersRaw.([]any); !ok {
			return contract.Result{Content: "错误：headers 必须为数组", IsError: true}, nil
		}
	}

	title, _ := p.Input["title"].(string)
	explicit, _ := p.Input["format"].(string)
	format := inferSpreadsheetFormat(dest, explicit)

	headers := anySlice(headersRaw)
	rows := toRows(rowsRaw)
	rendered := renderSpreadsheetCells(title, headers, rows, format)

	res, err := exportFileRun(map[string]any{
		"destination_path": dest,
		"content":          rendered,
	})
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{
		Content: fmt.Sprintf("已创建 %s 电子表格（%d 字节）：%s", format, res.bytes, res.path),
	}, nil
}

func (t *createSpreadsheetTool) RequiresApproval(_ *CallParams) bool { return true }
func (t *createSpreadsheetTool) ConcurrencySafe() bool               { return false }
func (t *createSpreadsheetTool) Enabled() bool                       { return true }
func (t *createSpreadsheetTool) Timeout(_ *CallParams) time.Duration { return 0 }

// inferSpreadsheetFormat 对账 TS `inferSpreadsheetFormat`。
func inferSpreadsheetFormat(destinationPath, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch strings.ToLower(filepath.Ext(destinationPath)) {
	case ".tsv":
		return sheetFormatTSV
	case ".html", ".htm":
		return sheetFormatHTML
	case ".xls":
		return sheetFormatXLS
	}
	return sheetFormatCSV
}

// normalizeCell 对账 TS `normalizeCell`——null → 空串；其余转字符串。
func normalizeCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON 数字统一是 float64。整数不带小数点（对账 JS 的 String(n)）。
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	}
	return fmt.Sprintf("%v", v)
}

// escapeDelimitedCell 对账 TS `escapeDelimitedCell`。
//
// 分隔符决定哪些字符触发引号：CSV 是 `"` `,` CR LF；TSV 是 `\t` CR LF。
// 引号内的 `"` 翻倍。
func escapeDelimitedCell(value string, delimiter rune) string {
	var mustQuote bool
	if delimiter == ',' {
		mustQuote = strings.ContainsAny(value, "\",\r\n")
	} else {
		mustQuote = strings.ContainsAny(value, "\t\r\n")
	}
	if !mustQuote {
		return value
	}
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}

// renderDelimited 对账 TS `renderDelimited`——headers 行 + 数据行，末尾一个 `\n`。
func renderDelimited(headers []any, rows [][]any, delimiter rune) string {
	var lines []string
	if len(headers) > 0 {
		lines = append(lines, joinCells(headers, delimiter))
	}
	for _, row := range rows {
		lines = append(lines, joinCells(row, delimiter))
	}
	return strings.Join(lines, "\n") + "\n"
}

// joinCells 把一行单元格用分隔符连接（逐个转义）。
func joinCells(cells []any, delimiter rune) string {
	parts := make([]string, 0, len(cells))
	for _, c := range cells {
		parts = append(parts, escapeDelimitedCell(normalizeCell(c), delimiter))
	}
	return strings.Join(parts, string(delimiter))
}

// renderHTMLSpreadsheet 对账 TS `renderHtmlSpreadsheet`。
func renderHTMLSpreadsheet(title string, headers []any, rows [][]any) string {
	safeTitle := escapeHTML(defaultStr(title, "Spreadsheet"))

	thead := ""
	if len(headers) > 0 {
		ths := make([]string, 0, len(headers))
		for _, h := range headers {
			ths = append(ths, "<th>"+escapeHTML(normalizeCell(h))+"</th>")
		}
		thead = "<thead><tr>" + strings.Join(ths, "") + "</tr></thead>"
	}

	trs := make([]string, 0, len(rows))
	for _, row := range rows {
		tds := make([]string, 0)
		for _, c := range row {
			tds = append(tds, "<td>"+escapeHTML(normalizeCell(c))+"</td>")
		}
		trs = append(trs, "<tr>"+strings.Join(tds, "")+"</tr>")
	}
	tbody := "<tbody>" + strings.Join(trs, "\n") + "</tbody>"

	caption := ""
	if title != "" {
		caption = "<caption>" + safeTitle + "</caption>\n"
	}

	return "<!doctype html>\n" +
		"<html>\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>" + safeTitle + "</title>\n" +
		"<style>\n" +
		"body { font-family: system-ui, -apple-system, Segoe UI, sans-serif; margin: 32px; }\n" +
		"table { border-collapse: collapse; width: 100%; }\n" +
		"th, td { border: 1px solid #cbd5e1; padding: 6px 8px; text-align: left; }\n" +
		"th { background: #f1f5f9; }\n" +
		"caption { text-align: left; font-size: 1.25rem; font-weight: 700; margin-bottom: 12px; }\n" +
		"</style>\n" +
		"</head>\n" +
		"<body>\n" +
		"<table>\n" +
		caption + thead + "\n" + tbody + "\n" +
		"</table>\n" +
		"</body>\n" +
		"</html>\n"
}

// renderSpreadsheetCells 是 renderSpreadsheet 的内部实现（接收已归一化的切片）。
func renderSpreadsheetCells(title string, headers []any, rows [][]any, format string) string {
	switch format {
	case sheetFormatTSV:
		return renderDelimited(headers, rows, '\t')
	case sheetFormatHTML, sheetFormatXLS:
		return renderHTMLSpreadsheet(title, headers, rows)
	}
	return renderDelimited(headers, rows, ',')
}

// renderSpreadsheet 是对外的渲染入口（供测试直接调用）。
//
// 参数用 `any` 以贴近 TS 的宽松入参——测试里传 `[]any{...}`。
func renderSpreadsheet(headers []any, rows [][]any, format string) string {
	return renderSpreadsheetCells("", headers, rows, format)
}

// anySlice 把 `any` 转成 `[]any`（非切片时返回 nil）。
func anySlice(v any) []any {
	if v == nil {
		return nil
	}
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// toRows 把 `[]any`（每个元素应是 `[]any`）规整为 `[][]any`。
func toRows(raw []any) [][]any {
	out := make([][]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, anySlice(r))
	}
	return out
}
