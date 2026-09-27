package net

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// fetchcore_test.go —— web_fetch 抓取主链路（第九十二刀 · W3-4c）。
//
// 对账 TS `src/tools/web-fetch/fetch-core.ts`（249 行）。
//
// # 管线（对账 TS）
//
//	URL 校验 → maxAge 缓存命中直返 → HTTPFetchGuarded 直连 →
//	坏状态码+实质内容视为成功 → HTMLToMarkdownSmart 转换 →
//	质量判败时 Jina 兜底 → 成功写缓存
//
// **Playwright 渲染层不做**（TS 默认关，需 chromium；Go 侧无对应物）——
// 这是**有意收窄**，已在 FetchCoreDeps 注释里披露。

// ── URL 校验 ────────────────────────────────────────────────────────────

// TestFetchMarkdownInvalidURL —— 非法 URL 报错。
func TestFetchMarkdownInvalidURL(t *testing.T) {
	out := FetchMarkdown(context.Background(), "not a url\n", FetchCoreDeps{}, FetchMarkdownOptions{Cwd: t.TempDir()})
	if out.OK {
		t.Fatal("非法 URL 应失败")
	}
	if !strings.Contains(out.Error, "无效 URL") {
		t.Errorf("文案应含「无效 URL」，实得 %q", out.Error)
	}
}

// TestFetchMarkdownUnsupportedProtocol —— 非 http(s) 协议报错。
func TestFetchMarkdownUnsupportedProtocol(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://x/y"} {
		out := FetchMarkdown(context.Background(), u, FetchCoreDeps{}, FetchMarkdownOptions{Cwd: t.TempDir()})
		if out.OK {
			t.Errorf("%s 应失败", u)
			continue
		}
		if !strings.Contains(out.Error, "不支持的协议") {
			t.Errorf("文案应含「不支持的协议」，实得 %q", out.Error)
		}
	}
}

// ── 缓存 ────────────────────────────────────────────────────────────────

// TestFetchMarkdownCacheHit —— 命中缓存直返，**不发起请求**。
func TestFetchMarkdownCacheHit(t *testing.T) {
	cwd := t.TempDir()
	cache := NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{})

	// 预置一条实质内容（>= 200 字符）
	md := strings.Repeat("缓存的正文内容。", 30)
	_ = cache.Write("https://example.com/a", "e1", FetchCacheEntry{
		URL: "https://example.com/a", Markdown: md, Status: 200, Via: "",
	})

	requestCount := 0
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			requestCount++
			return nil, errFetchCoreShouldNotCall{}
		},
		Cache: cache,
	}
	out := FetchMarkdown(context.Background(), "https://example.com/a", deps, FetchMarkdownOptions{Cwd: cwd})
	if !out.OK {
		t.Fatalf("应命中缓存，实得 %q", out.Error)
	}
	if !out.FromCache {
		t.Error("应标记 FromCache")
	}
	if requestCount != 0 {
		t.Errorf("命中缓存时不应发起请求（实得 %d 次）", requestCount)
	}
	if out.Markdown != md {
		t.Errorf("应返回缓存内容，实得长度 %d", len(out.Markdown))
	}
	if len(out.Links) == 0 {
		t.Log("（缓存路径的 links 从 markdown 提取——本条内容无链接，正常）")
	}
}

// TestFetchMarkdownCacheVariant —— variant 随 extractMainContent 变化。
func TestFetchMarkdownCacheVariant(t *testing.T) {
	cwd := t.TempDir()
	cache := NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{})
	md := strings.Repeat("提取版内容。", 40)
	// 只写 e1（extractMainContent=true）
	_ = cache.Write("https://example.com/a", "e1", FetchCacheEntry{Markdown: md, Status: 200})

	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return nil, errFetchCoreShouldNotCall{}
		},
		Cache: cache,
	}

	// extractMainContent 默认 true → 命中 e1
	out := FetchMarkdown(context.Background(), "https://example.com/a", deps, FetchMarkdownOptions{Cwd: cwd})
	if !out.OK || !out.FromCache {
		t.Errorf("默认应命中 e1 缓存，实得 %+v", out)
	}

	// 显式 false → variant e0 → miss（然后走网络，此处用 ShouldNotCall 探测）
	out2 := FetchMarkdown(context.Background(), "https://example.com/a", deps,
		FetchMarkdownOptions{Cwd: cwd, ExtractMainContent: BoolPtr(false)})
	if out2.OK {
		t.Error("e0 variant 应 miss（缓存隔离）")
	}
	if !strings.Contains(out2.Error, "should not call") {
		t.Errorf("miss 后应走网络（触发探测错误），实得 %q", out2.Error)
	}
}

// ── 成功路径 ────────────────────────────────────────────────────────────

// TestFetchMarkdownHTMLSuccess —— HTML 抓取 → markdown。
func TestFetchMarkdownHTMLSuccess(t *testing.T) {
	longBody := strings.Repeat("这是一段足够长的正文。", 30)
	html := `<html><body><main><h1>标题</h1><p>` + longBody + `</p></main></body></html>`

	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/html; charset=utf-8", html),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/a", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}
	if out.Status != 200 {
		t.Errorf("status 应 200，实得 %d", out.Status)
	}
	if !strings.Contains(out.Markdown, "# 标题") {
		t.Errorf("应转出 markdown 标题，实得：\n%s", out.Markdown)
	}
	if out.FromCache {
		t.Error("首次应 FromCache=false")
	}
	if out.RawBytes == 0 {
		t.Error("应记录原始字节数")
	}
}

// TestFetchMarkdownPlainText —— 非 HTML 内容原样返回。
func TestFetchMarkdownPlainText(t *testing.T) {
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/plain", "纯文本内容"),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/a.txt", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}
	if out.Markdown != "纯文本内容" {
		t.Errorf("纯文本应原样，实得 %q", out.Markdown)
	}
}

// TestFetchMarkdownWritesCache —— 成功后写缓存（内容够长时）。
func TestFetchMarkdownWritesCache(t *testing.T) {
	cwd := t.TempDir()
	longBody := strings.Repeat("足够的正文内容。", 40)
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/plain", longBody),
		Cache:  NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/cached", deps, FetchMarkdownOptions{Cwd: cwd})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}

	// 第二次应命中缓存
	deps2 := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return nil, errFetchCoreShouldNotCall{}
		},
		Cache: NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{}),
	}
	out2 := FetchMarkdown(context.Background(), "https://example.com/cached", deps2, FetchMarkdownOptions{Cwd: cwd})
	if !out2.OK || !out2.FromCache {
		t.Errorf("第二次应命中缓存，实得 %+v", out2)
	}
}

// TestFetchMarkdownShortContentNotCached —— **过短内容不写缓存**（宁旧勿错）。
//
// 对账 TS：`if (markdown.trim().length < MIN_SUBSTANTIAL_LENGTH) return`。
func TestFetchMarkdownShortContentNotCached(t *testing.T) {
	cwd := t.TempDir()
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/plain", "太短"),
		Cache:  NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{}),
	}
	_ = FetchMarkdown(context.Background(), "https://example.com/short", deps, FetchMarkdownOptions{Cwd: cwd})

	// 再抓一次——若上次写了缓存就会 miss 后命中，此处应是「又发请求」
	requestCount := 0
	deps2 := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			requestCount++
			return cannedResponse(200, "text/plain", "太短"), nil
		},
		Cache: NewFetchCache(FetchCacheDir(cwd), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/short", deps2, FetchMarkdownOptions{Cwd: cwd})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}
	if out.FromCache {
		t.Error("过短内容不应写缓存（第二次应走网络）")
	}
	if requestCount != 1 {
		t.Errorf("应发 1 次请求，实得 %d", requestCount)
	}
}

// ── 错误与降级 ──────────────────────────────────────────────────────────

// TestFetchMarkdownBinaryContent —— 二进制内容拒绝。
func TestFetchMarkdownBinaryContent(t *testing.T) {
	for _, ct := range []string{
		"image/png", "application/pdf", "application/octet-stream",
		"video/mp4", "audio/mpeg", "font/woff2",
	} {
		deps := FetchCoreDeps{
			Lookup: publicLookup,
			Doer:   cannedDoer(200, ct, "binary"),
			Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
		}
		out := FetchMarkdown(context.Background(), "https://example.com/x", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
		if out.OK {
			t.Errorf("%s 应被拒", ct)
			continue
		}
		if !strings.Contains(out.Error, "二进制内容") {
			t.Errorf("文案应含「二进制内容」，实得 %q", out.Error)
		}
		if !strings.Contains(out.Error, "import_resource") {
			t.Errorf("应提示用 import_resource，实得 %q", out.Error)
		}
	}
}

// TestFetchMarkdownHTTPError —— 4xx/5xx 无实质内容 → 报错。
func TestFetchMarkdownHTTPError(t *testing.T) {
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(404, "text/plain", "not found"),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/missing", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if out.OK {
		t.Fatal("404 应失败")
	}
	if !strings.Contains(out.Error, "HTTP 404") {
		t.Errorf("文案应含 HTTP 404，实得 %q", out.Error)
	}
}

// TestFetchMarkdownBadStatusWithSubstantialContent —— **坏状态码但有实质内容视为成功**。
//
// 对账 TS：「部分站点 403/404 页仍渲染真实内容」。
func TestFetchMarkdownBadStatusWithSubstantialContent(t *testing.T) {
	longBody := strings.Repeat("错误页里的真实内容。", 30)
	html := `<html><body><main><p>` + longBody + `</p></main></body></html>`
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(403, "text/html; charset=utf-8", html),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/forbidden", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("403 但有实质内容应视为成功，实得 %q", out.Error)
	}
	if out.Status != 403 {
		t.Errorf("status 应保留 403，实得 %d", out.Status)
	}
	if !strings.Contains(out.Markdown, "错误页里的真实内容") {
		t.Errorf("应转出内容，实得：\n%s", out.Markdown)
	}
}

// TestFetchMarkdownAPIErrorKind —— 429/5xx 带 errorKind。
func TestFetchMarkdownAPIErrorKind(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503} {
		deps := FetchCoreDeps{
			Lookup: publicLookup,
			Doer:   cannedDoer(status, "text/plain", "err"),
			Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
		}
		out := FetchMarkdown(context.Background(), "https://example.com/x", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
		if out.OK {
			t.Errorf("%d 应失败", status)
			continue
		}
		if out.ErrorKind != "api_error" {
			t.Errorf("%d 应带 api_error，实得 %q", status, out.ErrorKind)
		}
	}
	// 404 不带
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(404, "text/plain", "err"),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/x", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if out.ErrorKind != "" {
		t.Errorf("404 不应带 api_error，实得 %q", out.ErrorKind)
	}
}

// TestFetchMarkdownSSRFError —— SSRF 错误透传。
func TestFetchMarkdownSSRFError(t *testing.T) {
	privateLookup := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "169.254.169.254", Family: 4}, nil
	}
	deps := FetchCoreDeps{
		Lookup: privateLookup,
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "http://metadata.example/x", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if out.OK {
		t.Fatal("私网地址应失败")
	}
	if !strings.Contains(out.Error, "Access denied") {
		t.Errorf("应透传 SSRF 文案，实得 %q", out.Error)
	}
}

// TestFetchMarkdownNetworkError —— 网络错误包「抓取失败」前缀。
func TestFetchMarkdownNetworkError(t *testing.T) {
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		},
		Cache: NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/x", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if out.OK {
		t.Fatal("网络错误应失败")
	}
	if !strings.Contains(out.Error, "抓取失败") {
		t.Errorf("文案应含「抓取失败」，实得 %q", out.Error)
	}
	if !strings.Contains(out.Error, "connection refused") {
		t.Errorf("应含原始错误，实得 %q", out.Error)
	}
}

// ── 链接提取接入 ────────────────────────────────────────────────────────

// TestFetchMarkdownExtractsLinks —— HTML 路径提取绝对链接。
func TestFetchMarkdownExtractsLinks(t *testing.T) {
	longBody := strings.Repeat("正文内容。", 50)
	html := `<html><body><main><a href="/link1">L1</a><a href="/link2">L2</a><p>` + longBody + `</p></main></body></html>`
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/html; charset=utf-8", html),
		Cache:  NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/page", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}
	found := map[string]bool{}
	for _, l := range out.Links {
		found[l] = true
	}
	if !found["https://example.com/link1"] || !found["https://example.com/link2"] {
		t.Errorf("应提取两条绝对链接，实得 %v", out.Links)
	}
}

// ── 常量 ────────────────────────────────────────────────────────────────

// TestFetchCoreConstants —— 常量对账 TS。
func TestFetchCoreConstants(t *testing.T) {
	if fetchCoreTimeoutMs != 15_000 {
		t.Errorf("超时应 15000，实得 %d", fetchCoreTimeoutMs)
	}
	if fetchCoreMaxBytes != 10_485_760 {
		t.Errorf("大小上限应 10485760，实得 %d", fetchCoreMaxBytes)
	}
	if fetchCoreMaxRedirects != 5 {
		t.Errorf("重定向上限应 5，实得 %d", fetchCoreMaxRedirects)
	}
	if fetchCoreUserAgent != "Tianshu/1.0 (terminal coding agent)" {
		t.Errorf("UA 不符，实得 %q", fetchCoreUserAgent)
	}
}

// TestBinaryContentTypePrefixes —— 二进制前缀表对账 TS。
func TestBinaryContentTypePrefixes(t *testing.T) {
	want := []string{"image/", "application/pdf", "application/octet-stream", "video/", "audio/", "font/"}
	if len(binaryContentTypePrefixes) != len(want) {
		t.Fatalf("应有 %d 项，实得 %d", len(want), len(binaryContentTypePrefixes))
	}
	for i, w := range want {
		if binaryContentTypePrefixes[i] != w {
			t.Errorf("[%d] 应为 %q，实得 %q", i, w, binaryContentTypePrefixes[i])
		}
	}
}

// errFetchCoreShouldNotCall 是「不应被调用」的哨兵错误。
type errFetchCoreShouldNotCall struct{}

func (errFetchCoreShouldNotCall) Error() string { return "should not call" }

// ── isJinaQualityHeuristic（补测——变异反证暴露的覆盖缺口）─────────────

// TestIsJinaQualityHeuristic —— 质量判败的两种触发。
//
// # 为什么补这条
//
// M6 变异（把 `len < 200` 判断改成恒 false）**没让任何测试变红**——
// 因为该函数只在「有 Jina 兜底」的路径上被调用，而所有测试都禁用了 Jina。
// 这是**真覆盖缺口**，不是等价变异。
func TestIsJinaQualityHeuristic(t *testing.T) {
	// ① 过短 → 判败
	if !isJinaQualityHeuristic("太短") {
		t.Error("过短内容应判败")
	}
	if !isJinaQualityHeuristic(strings.Repeat("x", 199)) {
		t.Error("199 字符应判败（阈值 200）")
	}
	// ② 恰好 200 → 通过（且无 JS 信号）
	if isJinaQualityHeuristic(strings.Repeat("正", 200)) {
		t.Error("200 字符且无 JS 信号应通过")
	}

	// ③ JS 页面信号 → 判败（即便够长）
	longPrefix := strings.Repeat("正文内容填充。", 50)
	for _, sig := range []string{
		"Please enable JavaScript", "Enable JavaScript",
		"This page requires JavaScript", "noscript",
		"Checking your browser", "Just a moment", "DDOS protection",
		`id="challenge-form"`, "error while loading",
		"please reload this page", "uh oh",
	} {
		md := longPrefix + " " + sig
		if !isJinaQualityHeuristic(md) {
			t.Errorf("含信号 %q 应判败", sig)
		}
	}
	// ④ 大小写不敏感
	if !isJinaQualityHeuristic(longPrefix + " JUST A MOMENT") {
		t.Error("信号匹配应大小写不敏感")
	}
	// ⑤ 正常长文 → 通过
	if isJinaQualityHeuristic(longPrefix + "这是一篇正常的文章内容。") {
		t.Error("正常长文不应判败")
	}
}

// TestJinaQualitySignalsCoverage —— 信号表对账 TS（11 条）。
func TestJinaQualitySignalsCoverage(t *testing.T) {
	if len(jinaQualitySignals) != 11 {
		t.Errorf("信号表应有 11 条，实得 %d", len(jinaQualitySignals))
	}
}

// TestJinaFallbackDisabled —— DisableJina 时不发起 Jina 请求。
func TestJinaFallbackDisabled(t *testing.T) {
	// 构造一个质量判败的 HTML（正文很短）
	html := `<html><body><main>短</main></body></html>`
	jinaCalls := 0
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "jina") {
				jinaCalls++
			}
			return cannedResponse(200, "text/html; charset=utf-8", html), nil
		},
		Cache:       NewFetchCache(t.TempDir(), FetchCacheOptions{}),
		DisableJina: true,
	}
	out := FetchMarkdown(context.Background(), "https://example.com/short", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("应成功（只是不兜底）：%q", out.Error)
	}
	if jinaCalls != 0 {
		t.Errorf("DisableJina 时不应调 Jina，实得 %d 次", jinaCalls)
	}
	if out.Via != "" {
		t.Errorf("不应标记 Jina，实得 %q", out.Via)
	}
}

// TestJinaFallbackTriggered —— 质量判败且启用 Jina → 走兜底。
func TestJinaFallbackTriggered(t *testing.T) {
	shortHTML := `<html><body><main>短</main></body></html>`
	jinaMarkdown := strings.Repeat("Jina 返回的干净 markdown 内容。", 30)
	jinaCalls := 0
	deps := FetchCoreDeps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "jina") {
				jinaCalls++
				return cannedResponse(200, "text/plain", jinaMarkdown), nil
			}
			return cannedResponse(200, "text/html; charset=utf-8", shortHTML), nil
		},
		Cache: NewFetchCache(t.TempDir(), FetchCacheOptions{}),
	}
	out := FetchMarkdown(context.Background(), "https://example.com/short", deps, FetchMarkdownOptions{Cwd: t.TempDir()})
	if !out.OK {
		t.Fatalf("应成功：%q", out.Error)
	}
	if jinaCalls != 1 {
		t.Errorf("应调 1 次 Jina，实得 %d", jinaCalls)
	}
	if out.Via != "（经 Jina Reader）" {
		t.Errorf("应标记 Jina 路径，实得 %q", out.Via)
	}
	if !strings.Contains(out.Markdown, "Jina 返回的干净") {
		t.Errorf("应返回 Jina 内容，实得：\n%s", out.Markdown)
	}
}
