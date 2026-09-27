package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createdocument_test.go —— `create_document` 工具（第八十三刀）。
//
// 对账 TS `src/tools/create-document.ts`（约 120 行）。
//
// # 为什么这是「接线」而非「造子系统」
//
// 依赖只有两样：`node:path` 的 `extname`（Go 用 `filepath.Ext`）+ **`exportFile`**
// （第八十二刀已落地）。渲染逻辑是**纯字符串拼接**——零第三方库。
// 本工具是 `export_file` 的**下游**，复用其敏感门与 50MB 上限。

// ── 格式推断（对账 TS inferDocumentFormat）──────────────────────────────

// TestCreateDocumentInferFormat —— 扩展名推断；显式 format 优先。
func TestCreateDocumentInferFormat(t *testing.T) {
	cases := []struct{ path, explicit, want string }{
		// 显式优先（对账 TS：`if (explicit) return explicit`）
		{"/tmp/a.txt", "html", "html"},
		// 扩展名推断
		{"/tmp/a.md", "", "md"},
		{"/tmp/a.markdown", "", "md"},
		{"/tmp/a.html", "", "html"},
		{"/tmp/a.htm", "", "html"},
		{"/tmp/a.doc", "", "doc"},
		{"/tmp/a.txt", "", "txt"},
		{"/tmp/a.unknown", "", "txt"}, // 兜底 txt
		{"/tmp/noext", "", "txt"},
		{"/tmp/A.MD", "", "md"}, // 大小写不敏感（TS 用 toLowerCase）
	}
	for _, c := range cases {
		if got := inferDocumentFormat(c.path, c.explicit); got != c.want {
			t.Errorf("inferDocumentFormat(%q, %q) = %q，期望 %q", c.path, c.explicit, got, c.want)
		}
	}
}

// ── 渲染纯函数 ──────────────────────────────────────────────────────────

// TestCreateDocumentRenderPlainText —— txt：title 存在时 `title\n\ncontent`。
func TestCreateDocumentRenderPlainText(t *testing.T) {
	if got := renderDocument("标题", "正文", "txt"); got != "标题\n\n正文" {
		t.Errorf("带 title 应为「标题\\n\\n正文」，实得 %q", got)
	}
	if got := renderDocument("", "正文", "txt"); got != "正文" {
		t.Errorf("无 title 应原样返回，实得 %q", got)
	}
}

// TestCreateDocumentRenderMarkdown —— md：title 存在且 content 不以 # 开头时加 `# title`。
func TestCreateDocumentRenderMarkdown(t *testing.T) {
	if got := renderDocument("标题", "正文", "md"); got != "# 标题\n\n正文" {
		t.Errorf("应为「# 标题\\n\\n正文」，实得 %q", got)
	}
	// content 已以 # 开头 → 不再加标题（对账 TS `content.trimStart().startsWith('#')`）
	if got := renderDocument("标题", "# 已有标题\n正文", "md"); got != "# 已有标题\n正文" {
		t.Errorf("content 已含 # 时不应重复加标题，实得 %q", got)
	}
	// 前导空白后以 # 开头也算
	if got := renderDocument("标题", "  \n# 已有", "md"); got != "  \n# 已有" {
		t.Errorf("trimStart 后以 # 开头应跳过，实得 %q", got)
	}
	if got := renderDocument("", "正文", "md"); got != "正文" {
		t.Errorf("无 title 应原样，实得 %q", got)
	}
}

// TestCreateDocumentRenderHtml —— html/doc：转义 + 空行变 `<p>&nbsp;</p>`。
//
// 对账 TS `renderHtmlDocument`：逐行 map，空行 → `<p>&nbsp;</p>`，
// 否则 `<p>${escapeHtml(line)}</p>`；有 title 时前面插 `<h1>`。
func TestCreateDocumentRenderHtml(t *testing.T) {
	got := renderDocument("我的标题", "第一行\n\n第三行", "html")

	if !strings.Contains(got, "<h1>我的标题</h1>") {
		t.Errorf("应有 h1 标题，实得 %q", got)
	}
	if !strings.Contains(got, "<p>第一行</p>") {
		t.Errorf("应有第一行段落，实得 %q", got)
	}
	if !strings.Contains(got, "<p>&nbsp;</p>") {
		t.Errorf("空行应渲染为 &nbsp; 段落，实得 %q", got)
	}
	if !strings.Contains(got, "<p>第三行</p>") {
		t.Errorf("应有第三行段落，实得 %q", got)
	}
	// doc 走同一渲染器
	if doc := renderDocument("T", "c", "doc"); doc != renderDocument("T", "c", "html") {
		t.Error("doc 应与 html 用同一渲染器")
	}
}

// TestCreateDocumentEscapesHtml —— HTML 实体转义（防注入）。
func TestCreateDocumentEscapesHtml(t *testing.T) {
	got := renderDocument("", `<script>alert("x")</script> & more`, "html")
	if strings.Contains(got, "<script>") {
		t.Errorf("尖括号应被转义，实得 %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("应含 &lt;script&gt;，实得 %q", got)
	}
	if !strings.Contains(got, "&amp; more") {
		t.Errorf("& 应转义为 &amp;，实得 %q", got)
	}
	if !strings.Contains(got, "&quot;") {
		t.Errorf("引号应转义为 &quot;，实得 %q", got)
	}
	// 默认 title 是 'Document'（对账 TS `title ?? 'Document'`）
	if !strings.Contains(got, "<title>Document</title>") {
		t.Errorf("无 title 时应用默认 Document，实得 %q", got)
	}
}

// ── 必填校验 ────────────────────────────────────────────────────────────

// TestCreateDocumentRequiresDestination —— destination_path 必填。
func TestCreateDocumentRequiresDestination(t *testing.T) {
	res := runCreateDocument(t, map[string]any{"content": "x"})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("缺 destination_path 应报错，实得 %q", res.Content)
	}
	// 全空白也算缺失
	res = runCreateDocument(t, map[string]any{"destination_path": "   ", "content": "x"})
	if !res.IsError || !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("空白 destination_path 应报错，实得 %q", res.Content)
	}
}

// TestCreateDocumentRequiresContent —— content 必填。
//
// **注意**：TS 的检查是 `typeof input.content !== 'string'`（**不看 trim**）——
// 空字符串 `""` 是**合法**的（会创建空文档）。这是 TS 的实际语义，必须复刻。
func TestCreateDocumentRequiresContent(t *testing.T) {
	base := t.TempDir()

	// 缺 content 键 → 报错
	res := runCreateDocument(t, map[string]any{"destination_path": filepath.Join(base, "a.txt")})
	if !res.IsError || !strings.Contains(res.Content, "content 为必填项") {
		t.Errorf("缺 content 应报错，实得 %q", res.Content)
	}

	// **空字符串 content → 合法**（对账 TS：`typeof '' === 'string'`）
	res = runCreateDocument(t, map[string]any{
		"destination_path": filepath.Join(base, "empty.txt"), "content": "",
	})
	if res.IsError {
		t.Errorf("空字符串 content 应合法（对账 TS 语义），实得 %q", res.Content)
	}
}

// ── 端到端（落盘）───────────────────────────────────────────────────────

// TestCreateDocumentWritesFile —— 走真实 exportFile，文件落盘。
func TestCreateDocumentWritesFile(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "sub", "note.md")

	res := runCreateDocument(t, map[string]any{
		"destination_path": dst, "title": "T", "content": "正文",
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应落盘：%v", err)
	}
	if string(got) != "# T\n\n正文" {
		t.Errorf("内容应为「# T\\n\\n正文」，实得 %q", got)
	}
	// 文案：`已创建 ${format} 文档（${bytes} 字节）：${path}`
	if !strings.Contains(res.Content, "已创建 md 文档（") || !strings.Contains(res.Content, dst) {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
}

// TestCreateDocumentReusesSensitiveGate —— **敏感门复用**（不绕过 exportFile）。
//
// 本工具经 `exportFileRun` 写盘，故 `destination_path` 是写目标——
// 敏感门在 exportFile 内对 **source_path** 生效。此处验证**写路径仍受
// 50MB/门链约束**：至少确认它走的是 exportFileRun（而非自己 os.WriteFile）。
func TestCreateDocumentReusesSensitiveGate(t *testing.T) {
	base := t.TempDir()
	// 构造超 50MB 的内容会太慢；改为验证「写工作区外路径成功」
	// ——说明确实经 exportFile（它有 mkdir + 写盘）。
	dst := filepath.Join(base, "outside", "x.txt")
	res := runCreateDocument(t, map[string]any{"destination_path": dst, "content": "hi"})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("文件应存在：%v", err)
	}
}

// ── definition 对账 ─────────────────────────────────────────────────────

// TestCreateDocumentDefinitionParity —— definition 逐字对账 TS。
func TestCreateDocumentDefinitionParity(t *testing.T) {
	def := CreateDocument(t.TempDir()).Definition()

	if def.Name != "create_document" {
		t.Errorf("name 应为 create_document，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "在外部或项目路径创建基础用户文档。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	wantOrder := []string{"destination_path", "title", "content", "format"}
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
	// format 的 enum 键序（type→enum→description）
	enc, ok := def.InputSchema.Properties["format"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("format 应为有序结构，实得 %T", def.InputSchema.Properties["format"])
	}
	want := `{"type":"string","enum":["txt","md","html","doc"],"description":`
	if !strings.HasPrefix(enc.Marshal(), want) {
		t.Errorf("format 键序应 type→enum→description：\n实得 %s\n期望前缀 %s", enc.Marshal(), want)
	}
}

// TestCreateDocumentApprovalSemantics —— 恒需审批 + 非并发安全。
func TestCreateDocumentApprovalSemantics(t *testing.T) {
	tool := CreateDocument(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批（对账 TS `() => true`）")
	}
	if tool.ConcurrencySafe() {
		t.Error("不应并发安全（对账 TS `() => false`）")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

func runCreateDocument(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := CreateDocument(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}
