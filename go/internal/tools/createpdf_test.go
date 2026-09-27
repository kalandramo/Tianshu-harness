package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createpdf_test.go —— `create_pdf` 工具（第八十三刀 · W1-4）。
//
// 对账 TS `src/tools/create-pdf.ts`（约 160 行）。
//
// **注意**：它不生成真 PDF——TS 注释明说「创建**打印就绪的 HTML 文档**，
// 含专业排版和 @page 规则。在任何浏览器中打开并打印为 PDF」。故 format 恒 `html`。

// TestCreatePdfRequiresContent —— content 必填（且**看 trim**）。
//
// 对账 TS：`if (typeof input.content !== 'string' || input.content.trim().length === 0)`。
// **与 create_document 不同**——这里空白 content 也算缺失。
func TestCreatePdfRequiresContent(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "a.html")

	res := runCreatePdf(t, map[string]any{"destination_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "content 为必填项") {
		t.Errorf("缺 content 应报错，实得 %q", res.Content)
	}
	// 空白也算缺失（对账 TS 的 trim 检查）
	res = runCreatePdf(t, map[string]any{"destination_path": dst, "content": "   \n  "})
	if !res.IsError || !strings.Contains(res.Content, "content 为必填项") {
		t.Errorf("空白 content 应报错，实得 %q", res.Content)
	}
}

// TestCreatePdfRequiresDestination —— destination_path 必填。
func TestCreatePdfRequiresDestination(t *testing.T) {
	res := runCreatePdf(t, map[string]any{"content": "<p>x</p>"})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("缺 destination_path 应报错，实得 %q", res.Content)
	}
}

// TestCreatePdfPageSize —— A4/Letter 映射到 CSS 尺寸。
//
// 对账 TS `PAGE_SIZE_CSS = { A4: '210mm 297mm', Letter: '8.5in 11in' }`。
func TestCreatePdfPageSize(t *testing.T) {
	a4 := renderPdf("T", "<p>x</p>", "portrait", "A4")
	if !strings.Contains(a4, "size: 210mm 297mm portrait") {
		t.Errorf("A4 应为 210mm 297mm，实得 %q", a4)
	}
	letter := renderPdf("T", "<p>x</p>", "portrait", "Letter")
	if !strings.Contains(letter, "size: 8.5in 11in portrait") {
		t.Errorf("Letter 应为 8.5in 11in，实得 %q", letter)
	}
	// 默认 A4 + portrait（对账 TS `?? 'A4'` / `?? 'portrait'`）
	def := renderPdf("T", "<p>x</p>", "", "")
	if !strings.Contains(def, "size: 210mm 297mm portrait") {
		t.Errorf("默认应 A4+portrait，实得 %q", def)
	}
	// landscape
	ls := renderPdf("T", "<p>x</p>", "landscape", "A4")
	if !strings.Contains(ls, "210mm 297mm landscape") {
		t.Errorf("横向应含 landscape，实得 %q", ls)
	}
}

// TestCreatePdfOrientationWidth —— 横向时 body max-width 更大。
//
// 对账 TS：`max-width: ${orientation === 'landscape' ? '240mm' : '170mm'}`。
func TestCreatePdfOrientationWidth(t *testing.T) {
	p := renderPdf("T", "<p>x</p>", "portrait", "A4")
	l := renderPdf("T", "<p>x</p>", "landscape", "A4")
	if !strings.Contains(p, "max-width: 170mm") {
		t.Errorf("纵向应 170mm，实得 %q", p)
	}
	if !strings.Contains(l, "max-width: 240mm") {
		t.Errorf("横向应 240mm，实得 %q", l)
	}
}

// TestCreatePdfContentIsRawHtml —— content 是**原始 HTML**（不转义）。
//
// 对账 TS：`${content}` 直接插入 body——与 create_document 的逐行转义不同。
// **这是有意设计**：工具描述说「内容为原始 HTML——使用标准 HTML 标签组织结构」。
func TestCreatePdfContentIsRawHtml(t *testing.T) {
	got := renderPdf("T", "<h1>标题</h1><p>正文</p>", "portrait", "A4")
	if !strings.Contains(got, "<h1>标题</h1>") {
		t.Errorf("content 应原样插入（不转义），实得 %q", got)
	}
	// 但 title 要转义（对账 TS `escapeHtml(title)`）
	esc := renderPdf("<script>x</script>", "<p>y</p>", "portrait", "A4")
	if strings.Contains(esc, "<title><script>") {
		t.Errorf("title 应转义，实得 %q", esc)
	}
	if !strings.Contains(esc, "<title>&lt;script&gt;x&lt;/script&gt;</title>") {
		t.Errorf("title 应转义为实体，实得 %q", esc)
	}
}

// TestCreatePdfDefaultTitle —— 无 title 时用 'Document'。
func TestCreatePdfDefaultTitle(t *testing.T) {
	got := renderPdf("", "<p>x</p>", "portrait", "A4")
	if !strings.Contains(got, "<title>Document</title>") {
		t.Errorf("无 title 应用 Document，实得 %q", got)
	}
}

// TestCreatePdfWritesFile —— 端到端落盘 + 文案（含打印提示）。
func TestCreatePdfWritesFile(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "out", "report.html")

	res := runCreatePdf(t, map[string]any{
		"destination_path": dst, "title": "报告", "content": "<h1>Q4</h1>",
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应落盘：%v", err)
	}
	if !strings.Contains(string(got), "<h1>Q4</h1>") {
		t.Errorf("文件应含原始 HTML，实得 %q", got)
	}
	// 文案：`已创建可打印的 ${format} 文档（${bytes} 字节）：${path}\n\n在浏览器中打开并打印为 PDF（Cmd+P → 存储为 PDF）。`
	if !strings.Contains(res.Content, "已创建可打印的 html 文档（") {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "在浏览器中打开并打印为 PDF") {
		t.Errorf("文案应含打印提示，实得 %q", res.Content)
	}
}

// TestCreatePdfDefinitionParity —— definition 逐字对账。
func TestCreatePdfDefinitionParity(t *testing.T) {
	def := CreatePdf(t.TempDir()).Definition()

	if def.Name != "create_pdf" {
		t.Errorf("name 应为 create_pdf，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "创建打印就绪的 HTML 文档，含专业排版和 @page 规则。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	wantOrder := []string{"destination_path", "title", "content", "orientation", "pageSize"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 2 ||
		def.InputSchema.Required[0] != "destination_path" || def.InputSchema.Required[1] != "content" {
		t.Errorf("required 应为 [destination_path content]，实得 %#v", def.InputSchema.Required)
	}
	// orientation enum
	op, _ := def.InputSchema.Properties["orientation"].(interface{ Marshal() string })
	if !strings.HasPrefix(op.Marshal(), `{"type":"string","enum":["portrait","landscape"],"description":`) {
		t.Errorf("orientation 键序不符，实得 %s", op.Marshal())
	}
	// pageSize enum（注意驼峰键名）
	pp, _ := def.InputSchema.Properties["pageSize"].(interface{ Marshal() string })
	if !strings.HasPrefix(pp.Marshal(), `{"type":"string","enum":["A4","Letter"],"description":`) {
		t.Errorf("pageSize 键序不符，实得 %s", pp.Marshal())
	}
}

// TestCreatePdfApprovalSemantics —— 恒需审批 + 非并发安全。
func TestCreatePdfApprovalSemantics(t *testing.T) {
	tool := CreatePdf(t.TempDir())
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

func runCreatePdf(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := CreatePdf(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
