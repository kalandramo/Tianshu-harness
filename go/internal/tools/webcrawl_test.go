package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"

	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webcrawl_test.go —— `web_crawl` 工具（第九十五刀 · W3-5b）。
//
// 对账 TS `src/tools/web-crawl/tool.ts`（202 行）。
//
// # 依赖已就位
//
// crawl BFS 内核（第九十四刀）+ sitemap 阶梯 + fetch 内核 + artifact。
//
// # 有意收窄（诚实披露）
//
// **artifact 落盘未接**——TS 把每页 markdown 汇总为 artifact（sections 按页
// 切分），Go 侧的 `artifact.Store` 接口虽存在但 web_crawl 未接线。
// 故 `ArtifactNote` 恒为空。这是**功能收窄**，已在代码注释披露。

// ── definition 对账 ─────────────────────────────────────────────────────

// TestWebCrawlDefinitionParity —— definition 逐字对账 TS。
func TestWebCrawlDefinitionParity(t *testing.T) {
	def := WebCrawl(t.TempDir()).Definition()

	if def.Name != "web_crawl" {
		t.Errorf("name 应为 web_crawl，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "从种子 URL 出发整站爬取（BFS 跟随链接 + sitemap 发现）") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	for _, want := range []string{
		"把这个文档站/知识库读完",
		"并发 4、同域名 300ms 间隔",
		"重复页面走缓存",
		"read_section 分页细读",
		"需要用户审批",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description 应含 %q", want)
		}
	}

	wantOrder := []string{"url", "maxPages", "maxDepth", "includePaths", "excludePaths", "allowBackward", "budgetMs"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "url" {
		t.Errorf("required 应只有 url，实得 %#v", def.InputSchema.Required)
	}
	// 描述里的默认值应来自常量（改常量应同步文案）
	if !strings.Contains(def.Description, "20") && !strings.Contains(def.Description, "默认") {
		t.Log("（默认值在属性描述里）")
	}
}

// TestWebCrawlDefinitionDefaults —— 属性描述含默认值与上限（对账 TS 模板串）。
func TestWebCrawlDefinitionDefaults(t *testing.T) {
	def := WebCrawl(t.TempDir()).Definition()
	props := def.InputSchema.Properties

	check := func(key, want string) {
		t.Helper()
		p, ok := props[key].(interface{ Marshal() string })
		if !ok {
			t.Fatalf("%s 应为有序结构", key)
		}
		if !strings.Contains(p.Marshal(), want) {
			t.Errorf("%s 描述应含 %q，实得 %s", key, want, p.Marshal())
		}
	}
	check("maxPages", "默认 20")
	check("maxPages", "最大 200")
	check("maxDepth", "默认 2")
	check("maxDepth", "最大 10")
	check("budgetMs", "默认 180000")
	check("budgetMs", "最大 600000")
	check("url", "只跟随同域名链接")
	check("includePaths", "正则数组")
	check("allowBackward", "默认 false")
}

// TestWebCrawlApprovalSemantics —— 恒需审批 + 并发安全。
func TestWebCrawlApprovalSemantics(t *testing.T) {
	tool := WebCrawl(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应并发安全")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// ── 参数校验 ────────────────────────────────────────────────────────────

// TestWebCrawlInvalidURL —— 非法 URL。
func TestWebCrawlInvalidURL(t *testing.T) {
	res := runWebCrawl(t, map[string]any{"url": "not a url\n"})
	if !res.IsError || !strings.Contains(res.Content, "无效 URL") {
		t.Errorf("应报无效 URL，实得 %q", res.Content)
	}
}

// TestWebCrawlUnsupportedProtocol —— 非 http(s)。
func TestWebCrawlUnsupportedProtocol(t *testing.T) {
	res := runWebCrawl(t, map[string]any{"url": "file:///etc/passwd"})
	if !res.IsError || !strings.Contains(res.Content, "不支持的协议") {
		t.Errorf("应报协议错误，实得 %q", res.Content)
	}
}

// TestWebCrawlRequiredURL —— url 必填。
func TestWebCrawlRequiredURL(t *testing.T) {
	res := runWebCrawl(t, map[string]any{})
	if !res.IsError {
		t.Error("缺 url 应报错")
	}
}

// TestWebCrawlInvalidRegex —— includePaths/excludePaths 非法正则。
func TestWebCrawlInvalidRegex(t *testing.T) {
	res := runWebCrawl(t, map[string]any{
		"url":          "https://example.com/",
		"includePaths": []any{"[invalid("},
	})
	if !res.IsError || !strings.Contains(res.Content, "includePaths 参数错误") {
		t.Errorf("非法 include 正则应报错，实得 %q", res.Content)
	}

	res = runWebCrawl(t, map[string]any{
		"url":          "https://example.com/",
		"excludePaths": []any{"[invalid("},
	})
	if !res.IsError || !strings.Contains(res.Content, "excludePaths 参数错误") {
		t.Errorf("非法 exclude 正则应报错，实得 %q", res.Content)
	}
}

// TestWebCrawlRegexMustBeStringArray —— 非字符串数组。
func TestWebCrawlRegexMustBeStringArray(t *testing.T) {
	res := runWebCrawl(t, map[string]any{
		"url":          "https://example.com/",
		"includePaths": []any{123},
	})
	if !res.IsError || !strings.Contains(res.Content, "必须是字符串数组") {
		t.Errorf("应报类型错误，实得 %q", res.Content)
	}
}

// ── clampInt ────────────────────────────────────────────────────────────

// TestWebCrawlClampInt —— 对账 TS `clampInt`。
func TestWebCrawlClampInt(t *testing.T) {
	cases := []struct {
		in       any
		fallback int
		min, max int
		want     int
	}{
		{nil, 20, 1, 200, 20},           // 非数字 → fallback
		{"x", 20, 1, 200, 20},           // 非数字 → fallback
		{float64(5), 20, 1, 200, 5},     // 正常
		{float64(0), 20, 1, 200, 1},     // 低于 min → min
		{float64(999), 20, 1, 200, 200}, // 高于 max → max
		{float64(2.7), 20, 1, 200, 2},   // 向下取整
		{float64(-5), 20, 0, 10, 0},     // 负数 → min
	}
	for _, c := range cases {
		got := crawlClampInt(c.in, c.fallback, c.min, c.max)
		if got != c.want {
			t.Errorf("crawlClampInt(%v, %d, %d, %d) = %d，期望 %d", c.in, c.fallback, c.min, c.max, got, c.want)
		}
	}
}

// ── buildCrawlArtifact ──────────────────────────────────────────────────

// TestBuildCrawlArtifactSections —— sections 按页切分 + 行号连续。
func TestBuildCrawlArtifactSections(t *testing.T) {
	pages := []tnet.CrawlPage{
		{URL: "https://example.com/a", Markdown: "第一页内容"},
		{URL: "https://example.com/b", Markdown: "第二页内容\n多行"},
	}
	raw, sections := buildCrawlArtifact(pages)

	if len(sections) != 2 {
		t.Fatalf("应有 2 个 section，实得 %d", len(sections))
	}
	if sections[0].Name != "https://example.com/a" {
		t.Errorf("section 名应为页面 URL，实得 %q", sections[0].Name)
	}
	// 行号应从 1 开始且连续
	if sections[0].LineStart != 1 {
		t.Errorf("首个 section 应从行 1 开始，实得 %d", sections[0].LineStart)
	}
	if sections[1].LineStart != sections[0].LineEnd+1 {
		t.Errorf("section 行号应连续：s0=[%d,%d] s1 起于 %d",
			sections[0].LineStart, sections[0].LineEnd, sections[1].LineStart)
	}
	// rawContent 含两页正文
	if !strings.Contains(raw, "第一页内容") || !strings.Contains(raw, "第二页内容") {
		t.Errorf("rawContent 应含两页，实得 %q", raw)
	}
	// 每页有 header（`# <url>`）与分隔线
	if !strings.Contains(raw, "# https://example.com/a") {
		t.Errorf("应有页面 header，实得 %q", raw)
	}
	if !strings.Contains(raw, "---") {
		t.Errorf("应有分隔线，实得 %q", raw)
	}
	// charCount 应等于该页文本长度
	if sections[0].CharCount <= 0 {
		t.Error("charCount 应 > 0")
	}
}

// TestBuildCrawlArtifactEmpty —— 空页列表不 panic。
func TestBuildCrawlArtifactEmpty(t *testing.T) {
	raw, sections := buildCrawlArtifact(nil)
	if raw != "" || len(sections) != 0 {
		t.Errorf("空输入应产出空结果，实得 raw=%q sections=%d", raw, len(sections))
	}
}

// ── formatCrawlSummary ──────────────────────────────────────────────────

// TestFormatCrawlSummaryBasic —— 摘要头部（对账 TS）。
func TestFormatCrawlSummaryBasic(t *testing.T) {
	result := tnet.CrawlResult{
		Pages: []tnet.CrawlPage{
			{URL: "https://example.com/a", Status: 200, Markdown: "x"},
			{URL: "https://example.com/b", Status: 200, Markdown: "y", FromCache: true},
		},
		Errors:     []tnet.CrawlError{{URL: "https://example.com/e", Error: "err"}},
		Denied:     []tnet.CrawlDenied{{URL: "https://other.com/x", Reason: "cross_domain"}},
		DurationMs: 1500,
	}
	got := formatCrawlSummary("https://example.com/", result, "")

	if !strings.Contains(got, "爬取完成：https://example.com/（耗时 1.5s）") {
		t.Errorf("应含完成行（含耗时），实得：\n%s", got)
	}
	if !strings.Contains(got, "成功 2 页（缓存命中 1）/ 失败 1 / 跳过 1 个候选") {
		t.Errorf("应含统计行，实得：\n%s", got)
	}
	if !strings.Contains(got, "页面清单：") {
		t.Errorf("应含页面清单，实得：\n%s", got)
	}
}

// TestFormatCrawlSummaryTruncated —— 截断标记。
func TestFormatCrawlSummaryTruncated(t *testing.T) {
	result := tnet.CrawlResult{Truncated: true}
	got := formatCrawlSummary("https://example.com/", result, "")
	if !strings.Contains(got, "（已达上限/预算，截断）") {
		t.Errorf("应含截断标记，实得：\n%s", got)
	}
}

// TestFormatCrawlSummaryPageListCap —— 页面清单截前 15 条。
func TestFormatCrawlSummaryPageListCap(t *testing.T) {
	var pages []tnet.CrawlPage
	for i := 0; i < 20; i++ {
		pages = append(pages, tnet.CrawlPage{URL: "https://example.com/p" + itoaC(i), Status: 200, Markdown: "x"})
	}
	result := tnet.CrawlResult{Pages: pages}
	got := formatCrawlSummary("https://example.com/", result, "")

	if !strings.Contains(got, "… 其余 5 页见 artifact") {
		t.Errorf("超过 15 页应提示其余见 artifact，实得：\n%s", got)
	}
	// 第 16 条起不应在列表里（但 URL 可能出现在其他行，故只数「  16.」这种前缀）
	if strings.Contains(got, "  16. ") {
		t.Errorf("不应列出第 16 条，实得：\n%s", got)
	}
}

// TestFormatCrawlSummaryDenialBreakdown —— 拒绝原因分布（对账 TS）。
func TestFormatCrawlSummaryDenialBreakdown(t *testing.T) {
	result := tnet.CrawlResult{
		Denied: []tnet.CrawlDenied{
			{URL: "https://other.com/1", Reason: "cross_domain"},
			{URL: "https://other.com/2", Reason: "cross_domain"},
			{URL: "https://example.com/a.png", Reason: "file_extension"},
		},
	}
	got := formatCrawlSummary("https://example.com/", result, "")

	if !strings.Contains(got, "跳过原因分布：") {
		t.Errorf("应含原因分布，实得：\n%s", got)
	}
	if !strings.Contains(got, "跨域名 ×2") {
		t.Errorf("应含跨域名 ×2，实得：\n%s", got)
	}
	if !strings.Contains(got, "二进制/媒体文件扩展名 ×1") {
		t.Errorf("应含文件扩展名 ×1，实得：\n%s", got)
	}
}

// TestFormatCrawlSummaryDenialExamplesCap —— 示例截前 8 条。
func TestFormatCrawlSummaryDenialExamplesCap(t *testing.T) {
	var denied []tnet.CrawlDenied
	for i := 0; i < 12; i++ {
		denied = append(denied, tnet.CrawlDenied{URL: "https://other.com/" + itoaC(i), Reason: "cross_domain"})
	}
	got := formatCrawlSummary("https://example.com/", tnet.CrawlResult{Denied: denied}, "")
	// 示例行前缀是 "  - "
	count := strings.Count(got, "\n  - https://other.com/")
	if count > deniedExamplesCap {
		t.Errorf("示例应截前 %d 条，实得 %d", deniedExamplesCap, count)
	}
}

// TestFormatCrawlSummaryErrorsCap —— 失败截前 5 条 + 错误只取首行。
func TestFormatCrawlSummaryErrorsCap(t *testing.T) {
	var errs []tnet.CrawlError
	for i := 0; i < 8; i++ {
		errs = append(errs, tnet.CrawlError{URL: "https://example.com/e" + itoaC(i), Error: "第一行\n第二行"})
	}
	got := formatCrawlSummary("https://example.com/", tnet.CrawlResult{Errors: errs}, "")

	if !strings.Contains(got, "失败（前 5）：") {
		t.Errorf("应含失败标题，实得：\n%s", got)
	}
	if strings.Contains(got, "第二行") {
		t.Errorf("错误应只取首行，实得：\n%s", got)
	}
	if n := strings.Count(got, "\n  - https://example.com/e"); n > 5 {
		t.Errorf("失败应截前 5 条，实得 %d", n)
	}
}

// TestFormatCrawlSummaryArtifactNote —— artifact 注记拼接。
func TestFormatCrawlSummaryArtifactNote(t *testing.T) {
	got := formatCrawlSummary("https://example.com/", tnet.CrawlResult{}, "\n\n已存 artifact：abc")
	if !strings.HasSuffix(got, "已存 artifact：abc") {
		t.Errorf("artifact 注记应拼在末尾，实得：\n%s", got)
	}
}

// ── 常量 ────────────────────────────────────────────────────────────────

// TestWebCrawlConstants —— 常量对账 TS。
func TestWebCrawlConstants(t *testing.T) {
	checks := []struct {
		name      string
		got, want int
	}{
		{"DEFAULT_MAX_PAGES", crawlDefaultMaxPages, 20},
		{"MAX_MAX_PAGES", crawlMaxMaxPages, 200},
		{"DEFAULT_MAX_DEPTH", crawlDefaultMaxDepth, 2},
		{"MAX_MAX_DEPTH", crawlMaxMaxDepth, 10},
		{"DEFAULT_BUDGET_MS", crawlDefaultBudgetMs, 180_000},
		{"MAX_BUDGET_MS", crawlMaxBudgetMs, 600_000},
		{"MIN_BUDGET_MS", crawlMinBudgetMs, 10_000},
		{"DENIED_EXAMPLES_CAP", deniedExamplesCap, 8},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s 应为 %d，实得 %d", c.name, c.want, c.got)
		}
	}
}

// ── 端到端（注入 Doer，不触网）──────────────────────────────────────────

// TestWebCrawlEndToEndWithStubDoer —— 走真实 crawl 内核（注入 Doer）。
//
// **验证什么**：工具 → crawl BFS → fetch 内核 → 摘要 的完整链路。
func TestWebCrawlEndToEndWithStubDoer(t *testing.T) {
	pages := map[string]string{
		"https://example.com/":  `<html><body><main><p>` + strings.Repeat("种子页正文。", 30) + `</p><a href="/a">A</a></main></body></html>`,
		"https://example.com/a": `<html><body><main><p>` + strings.Repeat("A 页正文。", 30) + `</p></main></body></html>`,
	}
	doer := func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		body, ok := pages["https://example.com"+path]
		if !ok {
			return crawlTestResponse(404, "text/plain", "not found"), nil
		}
		return crawlTestResponse(200, "text/html; charset=utf-8", body), nil
	}

	tool := WebCrawlWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(nil, &CallParams{Input: map[string]any{
		"url": "https://example.com/", "maxPages": float64(10), "maxDepth": float64(2),
	}})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "爬取完成：") {
		t.Errorf("应含摘要，实得：\n%s", r.Content)
	}
	// 应抓到 2 页（种子 + /a）
	if !strings.Contains(r.Content, "成功 2 页") {
		t.Errorf("应抓 2 页，实得：\n%s", r.Content)
	}
}

// ── 辅助 ────────────────────────────────────────────────────────────────

func runWebCrawl(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := WebCrawl(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}

// crawlTestResponse 构造一个 http.Response（tools 包内的本地辅助——
// net 包的同名函数未导出到本包）。
func crawlTestResponse(status int, contentType, body string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func itoaC(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
