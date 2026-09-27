package net

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// crawltest_links_test.go —— crawl 的链接过滤链（第九十四刀 · W3-5a）。
//
// 对账 TS `src/tools/web-crawl/filter-links.ts`（85 行）。
//
// # 过滤顺序（**以代码为准，非注释**）
//
// TS 的注释写「协议 → 扩展名 → 深度 → exclude → include → backward」，
// 但**代码里 `cross_domain` 是第二步**——注释漏了它。此处以代码为准。
//
// 每个被拒 URL 产出**结构化 denial reason**——crawl 摘要按原因聚合计数，
// agent 能向用户自述「为什么跳过」（firecrawl 的设计核心：过滤即数据）。

// TestGetURLDepth —— 路径段数。
func TestGetURLDepth(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{"/", 0},
		{"/a", 1},
		{"/a/b", 2},
		{"/a/b/c", 3},
		{"/a/b/", 2}, // 尾斜杠不产生空段
		{"/a//b", 2}, // 连续斜杠被过滤
		{"", 0},
		{"/docs/guide/x", 3},
	}
	for _, c := range cases {
		if got := getURLDepth(c.path); got != c.want {
			t.Errorf("getURLDepth(%q) = %d，期望 %d", c.path, got, c.want)
		}
	}
}

// TestBlockedExtensions —— 扩展名黑名单（对账 TS 清单的关键项）。
func TestBlockedExtensions(t *testing.T) {
	blocked := []string{
		"/a.png", "/a.jpg", "/a.svg", "/a.ico",
		"/a.mp4", "/a.mp3", "/a.wav",
		"/a.zip", "/a.tar", "/a.7z",
		"/a.woff2", "/a.ttf",
		"/a.exe", "/a.dmg",
		"/a.pdf", "/a.docx", "/a.xlsx", "/a.pptx",
		"/a.csv", "/a.db", "/a.sqlite",
		"/a.wasm", "/a.so", "/a.dll",
		"/a.css", "/a.map",
	}
	for _, p := range blocked {
		if !blockedExtRe.MatchString(p) {
			t.Errorf("%s 应被扩展名黑名单拦住", p)
		}
	}
	// 大写也应命中（对账 TS 的 `i` 标志）
	if !blockedExtRe.MatchString("/A.PNG") {
		t.Error("大写扩展名也应命中")
	}
	// 正常页面不该被拦
	for _, p := range []string{"/a.html", "/a.htm", "/doc", "/a.md", "/path/to/page"} {
		if blockedExtRe.MatchString(p) {
			t.Errorf("%s 不应被拦", p)
		}
	}
	// **注意**：只匹配结尾（对账 TS 的 `$`）——`/a.css/page` 不拦
	if blockedExtRe.MatchString("/a.css/page") {
		t.Error("扩展名只应匹配结尾")
	}
}

// TestFilterLinkProtocol —— 非 http(s) 拒绝。
func TestFilterLinkProtocol(t *testing.T) {
	opts := LinkFilterOptions{SeedHost: "example.com", SeedPath: "/", SeedDepth: 0, MaxDepth: 2}
	for _, raw := range []string{"ftp://example.com/a", "mailto:x@y.com", "javascript:void(0)"} {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if got := filterLink(u, opts); got != "non_http" {
			t.Errorf("%s 应判 non_http，实得 %q", raw, got)
		}
	}
}

// TestFilterLinkCrossDomain —— 跨域拒绝（**第二步，注释漏了这个**）。
func TestFilterLinkCrossDomain(t *testing.T) {
	opts := LinkFilterOptions{SeedHost: "example.com", SeedPath: "/", SeedDepth: 0, MaxDepth: 2}
	u, _ := url.Parse("https://other.com/a")
	if got := filterLink(u, opts); got != "cross_domain" {
		t.Errorf("应判 cross_domain，实得 %q", got)
	}
	// 子域也算跨域（对账 TS 是**精确 hostname 比较**）
	u2, _ := url.Parse("https://sub.example.com/a")
	if got := filterLink(u2, opts); got != "cross_domain" {
		t.Errorf("子域应判 cross_domain，实得 %q", got)
	}
	// 大小写不敏感
	u3, _ := url.Parse("https://EXAMPLE.COM/a")
	if got := filterLink(u3, opts); got != "" {
		t.Errorf("hostname 应大小写不敏感，实得 %q", got)
	}
}

// TestFilterLinkMaxDepth —— 绝对深度上限 = seedDepth + maxDepth。
func TestFilterLinkMaxDepth(t *testing.T) {
	// 种子 /docs（深度 1），maxDepth=2 → 上限 3
	opts := LinkFilterOptions{
		SeedHost: "example.com", SeedPath: "/docs", SeedDepth: 1, MaxDepth: 2,
	}
	// 深度 3 → 通过
	u, _ := url.Parse("https://example.com/docs/a/b")
	if got := filterLink(u, opts); got == "max_depth" {
		t.Errorf("深度 3 应通过（上限 3），实得 %q", got)
	}
	// 深度 4 → 拒绝
	u2, _ := url.Parse("https://example.com/docs/a/b/c")
	if got := filterLink(u2, opts); got != "max_depth" {
		t.Errorf("深度 4 应判 max_depth，实得 %q", got)
	}
}

// TestFilterLinkExcludeInclude —— excludePaths / includePaths。
func TestFilterLinkExcludeInclude(t *testing.T) {
	opts := LinkFilterOptions{
		SeedHost:     "example.com",
		SeedPath:     "/",
		SeedDepth:    0,
		MaxDepth:     3,
		ExcludePaths: []*regexp.Regexp{regexp.MustCompile(`/private/`)},
		IncludePaths: []*regexp.Regexp{regexp.MustCompile(`^/docs/`)},
	}
	// 命中 exclude
	u, _ := url.Parse("https://example.com/private/x")
	if got := filterLink(u, opts); got != "excluded_path" {
		t.Errorf("应判 excluded_path，实得 %q", got)
	}
	// 不在 include 内
	u2, _ := url.Parse("https://example.com/blog/x")
	if got := filterLink(u2, opts); got != "not_included_path" {
		t.Errorf("应判 not_included_path，实得 %q", got)
	}
	// 在 include 内
	u3, _ := url.Parse("https://example.com/docs/x")
	if got := filterLink(u3, opts); got != "" {
		t.Errorf("应通过，实得 %q", got)
	}
	// **exclude 优先于 include**（对账 TS 顺序）
	opts2 := LinkFilterOptions{
		SeedHost: "example.com", SeedPath: "/", SeedDepth: 0, MaxDepth: 3,
		ExcludePaths: []*regexp.Regexp{regexp.MustCompile(`/docs/private/`)},
		IncludePaths: []*regexp.Regexp{regexp.MustCompile(`^/docs/`)},
	}
	u4, _ := url.Parse("https://example.com/docs/private/x")
	if got := filterLink(u4, opts2); got != "excluded_path" {
		t.Errorf("exclude 应优先，实得 %q", got)
	}
}

// TestFilterLinkIncludeMatchesQuery —— **include 匹配 path+query**（对账 TS）。
func TestFilterLinkIncludeMatchesQuery(t *testing.T) {
	opts := LinkFilterOptions{
		SeedHost: "example.com", SeedPath: "/", SeedDepth: 0, MaxDepth: 3,
		IncludePaths: []*regexp.Regexp{regexp.MustCompile(`\?lang=zh`)},
	}
	u, _ := url.Parse("https://example.com/a?lang=zh")
	if got := filterLink(u, opts); got != "" {
		t.Errorf("query 应参与匹配，实得 %q", got)
	}
}

// TestFilterLinkBackward —— 须位于种子路径之下。
func TestFilterLinkBackward(t *testing.T) {
	// 种子 /docs/guide → 种子视为目录
	opts := LinkFilterOptions{
		SeedHost: "example.com", SeedPath: "/docs/guide", SeedDepth: 2, MaxDepth: 3,
	}
	// 在种子路径下 → 通过
	u, _ := url.Parse("https://example.com/docs/guide/x")
	if got := filterLink(u, opts); got != "" {
		t.Errorf("种子路径下应通过，实得 %q", got)
	}
	// **种子自身的边界**（已用 Node 实测 TS 的真实行为确认）：
	// `seedPath="/docs/guide"` → `seedDir="/docs/guide/"`，而种子自身路径
	// `/docs/guide` **不以** `/docs/guide/` 开头 → 判 `backward_path`。
	//
	// 这不是缺陷——TS 的设计是「**种子本身不入过滤链**」（见 crawl() 里
	// 种子直接 push frontier 而不调 filterLink），正是为了绕开这个。
	u2, _ := url.Parse("https://example.com/docs/guide")
	if got := filterLink(u2, opts); got != "backward_path" {
		t.Errorf("种子自身（无尾斜杠）应判 backward_path（TS 实测行为），实得 %q", got)
	}
	// 带尾斜杠的等价形式则通过
	u2b, _ := url.Parse("https://example.com/docs/guide/")
	if got := filterLink(u2b, opts); got != "" {
		t.Errorf("带尾斜杠应通过，实得 %q", got)
	}
	// 种子之外 → 拒绝
	u3, _ := url.Parse("https://example.com/other")
	if got := filterLink(u3, opts); got != "backward_path" {
		t.Errorf("应判 backward_path，实得 %q", got)
	}
	// allowBackward → 放行
	optsAllow := opts
	optsAllow.AllowBackward = true
	if got := filterLink(u3, optsAllow); got != "" {
		t.Errorf("allowBackward 时应通过，实得 %q", got)
	}
}

// TestFilterLinkSeedAtRoot —— 种子在根时 backward 不生效（对账 TS `seedDir !== '/'`）。
func TestFilterLinkSeedAtRoot(t *testing.T) {
	opts := LinkFilterOptions{SeedHost: "example.com", SeedPath: "/", SeedDepth: 0, MaxDepth: 3}
	u, _ := url.Parse("https://example.com/anywhere")
	if got := filterLink(u, opts); got != "" {
		t.Errorf("种子在根时不应判 backward，实得 %q", got)
	}
}

// TestDenialReasonText —— 拒绝原因文案（对账 TS `DENIAL_REASON_TEXT`）。
func TestDenialReasonText(t *testing.T) {
	want := map[string]string{
		"non_http":          "非 http(s) 协议",
		"cross_domain":      "跨域名",
		"max_depth":         "超过路径深度上限",
		"excluded_path":     "命中 excludePaths",
		"not_included_path": "不在 includePaths 内",
		"backward_path":     "位于种子路径之外",
		"file_extension":    "二进制/媒体文件扩展名",
	}
	if len(denialReasonText) != len(want) {
		t.Fatalf("应有 %d 条，实得 %d", len(want), len(denialReasonText))
	}
	for k, v := range want {
		if denialReasonText[k] != v {
			t.Errorf("%s 文案应为 %q，实得 %q", k, v, denialReasonText[k])
		}
	}
}

// ── crawl BFS 引擎 ─────────────────────────────────────────────────────

// TestCrawlBasicBFS —— 基本 BFS：种子 + 一层链接。
func TestCrawlBasicBFS(t *testing.T) {
	// 构造一个确定性的 fetcher：每个 URL 返回固定的链接集。
	graph := map[string][]string{
		"https://example.com/": {
			"https://example.com/a",
			"https://example.com/b",
		},
		"https://example.com/a":  {"https://example.com/a1"},
		"https://example.com/b":  {},
		"https://example.com/a1": {},
	}
	fetcher := func(u string) FetchMarkdownOutcome {
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: strings.Repeat("内容", 50),
			Links: graph[u],
		}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 10, MaxDepth: 2})

	if len(res.Pages) != 4 {
		t.Fatalf("应抓 4 页，实得 %d：%v", len(res.Pages), pageURLs(res.Pages))
	}
	if res.Truncated {
		t.Error("未超限不应截断")
	}
	// 种子深度 0（**按 URL 查找**——pages 的顺序取决于完成顺序）
	depthOf := map[string]int{}
	for _, p := range res.Pages {
		depthOf[p.URL] = p.Depth
	}
	if depthOf["https://example.com/"] != 0 {
		t.Errorf("种子深度应 0，实得 %d", depthOf["https://example.com/"])
	}
}

// TestCrawlMaxPages —— 页数上限生效且标记截断。
func TestCrawlMaxPages(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		// 每页都产出大量链接，保证 frontier 不空
		var links []string
		for i := 0; i < 5; i++ {
			links = append(links, "https://example.com/p"+itoa(len(u))+itoa(i))
		}
		return FetchMarkdownOutcome{OK: true, Status: 200, Markdown: "x", Links: links}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 3, MaxDepth: 5})
	if len(res.Pages) > 3 {
		t.Errorf("不应超过 3 页，实得 %d", len(res.Pages))
	}
	if !res.Truncated {
		t.Error("超页数上限应标记截断")
	}
}

// TestCrawlMaxDepthStops —— maxDepth 到达后不再入队。
func TestCrawlMaxDepthStops(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		// 每页链到下一层
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: "x",
			Links: []string{u + "x/"},
		}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 100, MaxDepth: 1})
	// maxDepth=1 → 种子(0) + 一层(1) = 2 页
	if len(res.Pages) > 2 {
		t.Errorf("maxDepth=1 应只 2 页，实得 %d：%v", len(res.Pages), pageURLs(res.Pages))
	}
}

// TestCrawlDedup —— visited 去重（含 www 变体）。
func TestCrawlDedup(t *testing.T) {
	callCount := 0
	fetcher := func(u string) FetchMarkdownOutcome {
		callCount++
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: "x",
			// 重复链接 + www 变体
			Links: []string{
				"https://example.com/a",
				"https://example.com/a",
				"https://www.example.com/a",
			},
		}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 20, MaxDepth: 3})
	// 种子 + /a（www 变体已 visited） = 2 次抓取
	if callCount != 2 {
		t.Errorf("应只抓 2 次（去重），实得 %d", callCount)
	}
	if len(res.Pages) != 2 {
		t.Errorf("应产出 2 页，实得 %d", len(res.Pages))
	}
}

// TestCrawlDenialReasons —— 被拒链接进 denied 且原因正确。
func TestCrawlDenialReasons(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: "x",
			Links: []string{
				"https://other.com/x",       // cross_domain
				"https://example.com/a.png", // file_extension
				"mailto:x@y.com",            // non_http
			},
		}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 10, MaxDepth: 3})
	if len(res.Denied) != 3 {
		t.Fatalf("应有 3 条 denied，实得 %d：%+v", len(res.Denied), res.Denied)
	}
	seen := map[string]bool{}
	for _, d := range res.Denied {
		seen[d.Reason] = true
	}
	for _, want := range []string{"cross_domain", "file_extension", "non_http"} {
		if !seen[want] {
			t.Errorf("应含拒绝原因 %q，实得 %v", want, seen)
		}
	}
}

// TestCrawlErrorsDontAbort —— 单页失败不中断整体。
func TestCrawlErrorsDontAbort(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		if strings.HasSuffix(u, "/bad") {
			return FetchMarkdownOutcome{Error: "抓取失败"}
		}
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: "x",
			Links: []string{"https://example.com/bad", "https://example.com/good"},
		}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{MaxPages: 10, MaxDepth: 2})
	if len(res.Errors) != 1 {
		t.Errorf("应有 1 条错误，实得 %d", len(res.Errors))
	}
	// good 仍应被抓
	found := false
	for _, p := range res.Pages {
		if strings.HasSuffix(p.URL, "/good") {
			found = true
		}
	}
	if !found {
		t.Errorf("失败不应中断其他页，实得 %v", pageURLs(res.Pages))
	}
}

// TestCrawlBudgetTruncation —— 预算到期停调新页。
func TestCrawlBudgetTruncation(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		time.Sleep(20 * time.Millisecond)
		return FetchMarkdownOutcome{
			OK: true, Status: 200, Markdown: "x",
			Links: []string{u + "child/"},
		}
	}
	start := time.Now()
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{
		MaxPages: 1000, MaxDepth: 100, BudgetMs: 50,
	})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("预算到期应尽快返回，实得耗时 %v", elapsed)
	}
	if !res.Truncated {
		t.Error("预算到期应标记截断")
	}
}

// TestCrawlSitemapUrls —— sitemap 预收集 URL 作为 depth-1 候选。
func TestCrawlSitemapUrls(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		return FetchMarkdownOutcome{OK: true, Status: 200, Markdown: "x"}
	}
	res := crawlWith("https://example.com/", fetcher, CrawlOptions{
		MaxPages: 10, MaxDepth: 3,
		SitemapURLs: []string{"https://example.com/s1", "https://example.com/s2"},
	})
	if len(res.Pages) != 3 {
		t.Fatalf("应有 3 页（种子+2 sitemap），实得 %d", len(res.Pages))
	}
	// **按深度分组校验**（`pages` 的追加顺序取决于 goroutine 完成顺序——
	// 不能按位置索引断言）。
	depthOf := map[string]int{}
	for _, p := range res.Pages {
		depthOf[p.URL] = p.Depth
	}
	if depthOf["https://example.com/"] != 0 {
		t.Errorf("种子深度应 0，实得 %d", depthOf["https://example.com/"])
	}
	for _, u := range []string{"https://example.com/s1", "https://example.com/s2"} {
		if depthOf[u] != 1 {
			t.Errorf("sitemap URL %s 深度应为 1，实得 %d", u, depthOf[u])
		}
	}
}

// TestCrawlSeedNotFiltered —— **种子本身不入过滤链**（backward 会挡住自己）。
func TestCrawlSeedNotFiltered(t *testing.T) {
	fetcher := func(u string) FetchMarkdownOutcome {
		return FetchMarkdownOutcome{OK: true, Status: 200, Markdown: "x"}
	}
	// 种子在 /docs/guide——若入过滤链，backward 会拒它自己
	res := crawlWith("https://example.com/docs/guide", fetcher, CrawlOptions{MaxPages: 5, MaxDepth: 1})
	if len(res.Pages) != 1 {
		t.Fatalf("种子应被抓（不入过滤链），实得 %d 页：%v", len(res.Pages), pageURLs(res.Pages))
	}
	if len(res.Denied) != 0 {
		t.Errorf("种子不应产生 denied，实得 %+v", res.Denied)
	}
}

// ── 辅助 ────────────────────────────────────────────────────────────────

func pageURLs(pages []CrawlPage) []string {
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.URL)
	}
	return out
}

func itoa(n int) string {
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
