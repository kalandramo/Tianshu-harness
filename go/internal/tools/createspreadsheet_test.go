package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createspreadsheet_test.go —— `create_spreadsheet` 工具（第八十三刀 · W1-2）。
//
// 对账 TS `src/tools/create-spreadsheet.ts`（约 160 行）。
//
// 依赖 `exportFileRun` + 纯字符串（CSV/TSV 转义、HTML 表格）——零第三方库。
// **注意**：`.xls` 是「以 .xls 扩展名保存的 HTML 表格」（TS 注释明说），
// 不是真二进制 Excel——故与 `html` 走同一渲染器。

// ── 格式推断（对账 TS inferSpreadsheetFormat）────────────────────────────

// TestCreateSpreadsheetInferFormat —— 扩展名推断；显式优先。
func TestCreateSpreadsheetInferFormat(t *testing.T) {
	cases := []struct{ path, explicit, want string }{
		{"/tmp/a.csv", "html", "html"}, // 显式优先
		{"/tmp/a.tsv", "", "tsv"},
		{"/tmp/a.html", "", "html"},
		{"/tmp/a.htm", "", "html"},
		{"/tmp/a.xls", "", "xls"},
		{"/tmp/a.csv", "", "csv"},
		{"/tmp/a.unknown", "", "csv"}, // 兜底 csv（对账 TS 默认）
		{"/tmp/noext", "", "csv"},
		{"/tmp/A.TSV", "", "tsv"}, // 大小写不敏感
	}
	for _, c := range cases {
		if got := inferSpreadsheetFormat(c.path, c.explicit); got != c.want {
			t.Errorf("inferSpreadsheetFormat(%q, %q) = %q，期望 %q", c.path, c.explicit, got, c.want)
		}
	}
}

// ── 单元格规范化与转义 ──────────────────────────────────────────────────

// TestCreateSpreadsheetNormalizeCell —— null → 空串；其余 String()。
func TestCreateSpreadsheetNormalizeCell(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"文本", "文本"},
		{float64(10), "10"},
		{true, "true"},
		{false, "false"},
	}
	for _, c := range cases {
		if got := normalizeCell(c.in); got != c.want {
			t.Errorf("normalizeCell(%#v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestCreateSpreadsheetEscapeDelimitedCell —— CSV/TSV 引号规则。
//
// 对账 TS `escapeDelimitedCell`：
//   - CSV（`,`）：含 `"` `,` CR LF 时加引号
//   - TSV（`\t`）：含 `\t` CR LF 时加引号
//   - 引号内 `"` 翻倍
func TestCreateSpreadsheetEscapeDelimitedCell(t *testing.T) {
	// CSV：普通值不加引号
	if got := escapeDelimitedCell("abc", ','); got != "abc" {
		t.Errorf("普通值不应加引号，实得 %q", got)
	}
	// CSV：含逗号 → 加引号
	if got := escapeDelimitedCell("a,b", ','); got != `"a,b"` {
		t.Errorf("含逗号应加引号，实得 %q", got)
	}
	// CSV：含引号 → 加引号且引号翻倍
	if got := escapeDelimitedCell(`say "hi"`, ','); got != `"say ""hi"""` {
		t.Errorf("含引号应翻倍，实得 %q", got)
	}
	// CSV：含换行 → 加引号
	if got := escapeDelimitedCell("a\nb", ','); got != "\"a\nb\"" {
		t.Errorf("含换行应加引号，实得 %q", got)
	}
	// TSV：逗号**不**触发引号（分隔符不同）
	if got := escapeDelimitedCell("a,b", '\t'); got != "a,b" {
		t.Errorf("TSV 下逗号不应加引号，实得 %q", got)
	}
	// TSV：含制表符 → 加引号
	if got := escapeDelimitedCell("a\tb", '\t'); got != "\"a\tb\"" {
		t.Errorf("TSV 含制表符应加引号，实得 %q", got)
	}
}

// ── 渲染纯函数 ──────────────────────────────────────────────────────────

// TestCreateSpreadsheetRenderDelimited —— CSV/TSV：headers 行 + 数据行 + 末尾换行。
//
// 对账 TS `renderDelimited`：`lines.join('\n') + '\n'`——**末尾必有一个 \n**。
func TestCreateSpreadsheetRenderDelimited(t *testing.T) {
	got := renderSpreadsheet(
		[]any{"姓名", "分数"},
		[][]any{{"A", float64(10)}, {"B", float64(20)}},
		"csv",
	)
	want := "姓名,分数\nA,10\nB,20\n"
	if got != want {
		t.Errorf("CSV 渲染应为 %q，实得 %q", want, got)
	}

	// 无 headers → 只有数据行
	got = renderSpreadsheet(nil, [][]any{{"x", "y"}}, "csv")
	if got != "x,y\n" {
		t.Errorf("无 headers 应只有数据行，实得 %q", got)
	}

	// TSV 用制表符
	got = renderSpreadsheet([]any{"a"}, [][]any{{"b"}}, "tsv")
	if got != "a\nb\n" {
		t.Errorf("单列 TSV 应为 %q，实得 %q", "a\nb\n", got)
	}
	got = renderSpreadsheet([]any{"a", "b"}, [][]any{{"c", "d"}}, "tsv")
	if got != "a\tb\nc\td\n" {
		t.Errorf("TSV 应用制表符，实得 %q", got)
	}

	// 空 rows（无 headers）→ 只有末尾换行
	if got := renderSpreadsheet(nil, nil, "csv"); got != "\n" {
		t.Errorf("空表应为单个换行，实得 %q", got)
	}
}

// TestCreateSpreadsheetRenderHtml —— html/xls 走同一渲染器（表格 + 样式）。
func TestCreateSpreadsheetRenderHtml(t *testing.T) {
	got := renderSpreadsheet([]any{"名"}, [][]any{{"值"}}, "html")

	if !strings.Contains(got, "<table>") || !strings.Contains(got, "</table>") {
		t.Errorf("应含 table，实得 %q", got)
	}
	if !strings.Contains(got, "<th>名</th>") {
		t.Errorf("表头应在 thead 的 th 中，实得 %q", got)
	}
	if !strings.Contains(got, "<td>值</td>") {
		t.Errorf("数据应在 td 中，实得 %q", got)
	}
	// 无 title → caption 用默认 'Spreadsheet'
	if !strings.Contains(got, "<title>Spreadsheet</title>") {
		t.Errorf("无 title 应用默认 Spreadsheet，实得 %q", got)
	}
	// xls 与 html 同渲染器
	if xls := renderSpreadsheet([]any{"名"}, [][]any{{"值"}}, "xls"); xls != got {
		t.Error("xls 应与 html 用同一渲染器")
	}
}

// TestCreateSpreadsheetHtmlEscapes —— HTML 表格转义（防注入）。
func TestCreateSpreadsheetHtmlEscapes(t *testing.T) {
	got := renderSpreadsheet([]any{"<b>h</b>"}, [][]any{{`<script>&"`}}, "html")
	if strings.Contains(got, "<b>h</b>") || strings.Contains(got, "<script>") {
		t.Errorf("HTML 应被转义，实得 %q", got)
	}
	if !strings.Contains(got, "&lt;b&gt;h&lt;/b&gt;") {
		t.Errorf("表头应转义，实得 %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;&amp;&quot;") {
		t.Errorf("单元格应转义（含 & 与引号），实得 %q", got)
	}
}

// ── 必填校验 ────────────────────────────────────────────────────────────

// TestCreateSpreadsheetRequiresDestination —— destination_path 必填。
func TestCreateSpreadsheetRequiresDestination(t *testing.T) {
	res := runCreateSpreadsheet(t, map[string]any{"rows": []any{}})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("缺 destination_path 应报错，实得 %q", res.Content)
	}
}

// TestCreateSpreadsheetRequiresRows —— rows 必填且须为数组。
//
// 对账 TS：`if (!Array.isArray(input.rows)) throw new Error('rows 为必填项')`。
func TestCreateSpreadsheetRequiresRows(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "a.csv")

	// 缺 rows
	res := runCreateSpreadsheet(t, map[string]any{"destination_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "rows 为必填项") {
		t.Errorf("缺 rows 应报错，实得 %q", res.Content)
	}
	// rows 非数组
	res = runCreateSpreadsheet(t, map[string]any{"destination_path": dst, "rows": "notarray"})
	if !res.IsError || !strings.Contains(res.Content, "rows 为必填项") {
		t.Errorf("rows 非数组应报错，实得 %q", res.Content)
	}
	// 空数组是合法的
	res = runCreateSpreadsheet(t, map[string]any{"destination_path": dst, "rows": []any{}})
	if res.IsError {
		t.Errorf("空数组 rows 应合法，实得 %q", res.Content)
	}
}

// TestCreateSpreadsheetHeadersMustBeArray —— headers 若给须为数组。
func TestCreateSpreadsheetHeadersMustBeArray(t *testing.T) {
	base := t.TempDir()
	res := runCreateSpreadsheet(t, map[string]any{
		"destination_path": filepath.Join(base, "a.csv"),
		"rows":             []any{},
		"headers":          "notarray",
	})
	if !res.IsError || !strings.Contains(res.Content, "headers 必须为数组") {
		t.Errorf("headers 非数组应报错，实得 %q", res.Content)
	}
}

// ── 端到端 ──────────────────────────────────────────────────────────────

// TestCreateSpreadsheetWritesFile —— 走真实 exportFile 落盘。
func TestCreateSpreadsheetWritesFile(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "out", "report.csv")

	res := runCreateSpreadsheet(t, map[string]any{
		"destination_path": dst,
		"headers":          []any{"姓名", "分数"},
		"rows":             []any{[]any{"A", float64(10)}},
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应落盘：%v", err)
	}
	if string(got) != "姓名,分数\nA,10\n" {
		t.Errorf("内容不符，实得 %q", got)
	}
	// 文案：`已创建 ${format} 电子表格（${bytes} 字节）：${path}`
	if !strings.Contains(res.Content, "已创建 csv 电子表格（") || !strings.Contains(res.Content, dst) {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
}

// ── definition 对账 ─────────────────────────────────────────────────────

// TestCreateSpreadsheetDefinitionParity —— definition 逐字对账 TS（含嵌套数组键序）。
func TestCreateSpreadsheetDefinitionParity(t *testing.T) {
	def := CreateSpreadsheet(t.TempDir()).Definition()

	if def.Name != "create_spreadsheet" {
		t.Errorf("name 应为 create_spreadsheet，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "在外部或项目路径创建基础电子表格/数据表。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	wantOrder := []string{"destination_path", "title", "headers", "rows", "format"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 2 ||
		def.InputSchema.Required[0] != "destination_path" || def.InputSchema.Required[1] != "rows" {
		t.Errorf("required 应为 [destination_path rows]，实得 %#v", def.InputSchema.Required)
	}
	// headers 是「元素为联合类型的数组」——键序 type→items→description
	hp, ok := def.InputSchema.Properties["headers"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("headers 应为有序结构，实得 %T", def.InputSchema.Properties["headers"])
	}
	if raw := hp.Marshal(); !strings.HasPrefix(raw, `{"type":"array","items":`) {
		t.Errorf("headers 键序应 type→items→description，实得 %s", raw)
	}
	// rows 是「元素为数组的数组」
	rp, ok := def.InputSchema.Properties["rows"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("rows 应为有序结构，实得 %T", def.InputSchema.Properties["rows"])
	}
	if raw := rp.Marshal(); !strings.HasPrefix(raw, `{"type":"array","items":{"type":"array"`) {
		t.Errorf("rows 应为嵌套数组 schema，实得 %s", raw)
	}
	// format 的 enum
	fp, ok := def.InputSchema.Properties["format"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("format 应为有序结构，实得 %T", def.InputSchema.Properties["format"])
	}
	want := `{"type":"string","enum":["csv","tsv","html","xls"],"description":`
	if !strings.HasPrefix(fp.Marshal(), want) {
		t.Errorf("format 键序不符：\n实得 %s\n期望前缀 %s", fp.Marshal(), want)
	}
}

// TestCreateSpreadsheetApprovalSemantics —— 恒需审批 + 非并发安全。
func TestCreateSpreadsheetApprovalSemantics(t *testing.T) {
	tool := CreateSpreadsheet(t.TempDir())
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

func runCreateSpreadsheet(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := CreateSpreadsheet(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
