package net

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// fetchcore.go —— web_fetch 抓取主链路（第九十二刀 · W3-4c）。
//
// 对账 TS `src/tools/web-fetch/fetch-core.ts`（249 行）。
//
// # 管线（对账 TS）
//
//	URL 校验 → maxAge 缓存命中直返 → HTTPFetchGuarded 直连 →
//	坏状态码+实质内容视为成功 → HTMLToMarkdownSmart 转换 →
//	质量判败时 Jina 兜底 → 成功写缓存
//
// web_fetch 工具与 web_crawl 复用同一管线——crawl 因此自动获得缓存、
// 渲染降级与链接提取能力。
//
// # 有意收窄（诚实标注）
//
// **Playwright 渲染层不做**。TS 里它是可选降级层（`enablePlaywright`，
// 默认关，需 chromium 可用）。Go 侧无对应物——故质量判败时**直接落到
// Jina 兜底**（跳过本地渲染那一跳）。这是**功能收窄**，不是等价移植：
// 在「有 chromium 且 Jina 不可达」的场景下 TS 能出内容、Go 不能。
// 消费方若需要该能力，须先移植 render-fetch.ts（381 行）。

// 常量（对账 TS `fetch-core.ts` 的 DEFAULT_*）。
const (
	fetchCoreTimeoutMs    = 15_000
	fetchCoreMaxBytes     = 10_485_760
	fetchCoreMaxRedirects = 5
	fetchCoreUserAgent    = "Tianshu/1.0 (terminal coding agent)"
)

// binaryContentTypePrefixes 对账 TS `BINARY_CONTENT_TYPE_PREFIXES`。
//
// 这些类型**不以文本返回**——TS 提示用户改用 import_resource 下载。
var binaryContentTypePrefixes = []string{
	"image/",
	"application/pdf",
	"application/octet-stream",
	"video/",
	"audio/",
	"font/",
}

// FetchCoreDeps 对账 TS `FetchCoreDeps`。
type FetchCoreDeps struct {
	// Deps 内嵌 HTTP 抓取依赖（Lookup / Doer）。
	Deps
	// Cache：可注入缓存（缺省用 <cwd>/.rivet/cache/web-fetch）。
	Cache *FetchCache
	// JinaBaseURL：Jina Reader 基础地址（默认 https://r.jina.ai）。
	JinaBaseURL string
	// DisableJina：禁用 Jina 兜底（测试用——避免真实外网请求）。
	DisableJina bool
}

// FetchMarkdownOptions 对账 TS `FetchMarkdownOptions`。
type FetchMarkdownOptions struct {
	// Cwd：缓存目录推导基准。
	Cwd string
	// ExtractMainContent：主内容提取开关（默认 true）。**用指针**区分未设置与显式 false。
	ExtractMainContent *bool
	// CacheMaxAgeMs：缓存读取有效期（默认 2 天；0 = 禁读仍写）。
	CacheMaxAgeMs *int64
	HTTPTimeoutMs *int
	MaxBytes      *int
	MaxRedirects  *int
	UserAgent     string
}

// FetchMarkdownOutcome 对账 TS `FetchMarkdownOutcome`（Ok / Error 两个变体合一）。
type FetchMarkdownOutcome struct {
	OK       bool
	Status   int
	Markdown string
	// Via：（经 Playwright 渲染）/（经 Jina Reader）/ ''。
	Via string
	// Links：转换前原始 HTML（或缓存/Jina markdown）提取的绝对链接——crawl 发现源。
	Links    []string
	RawBytes int
	// FromCache：缓存命中标记。
	FromCache bool
	// FetchedAt：仅缓存命中时存在。
	FetchedAt int64

	// Error：失败信息（OK=false 时）。
	Error string
	// ErrorKind：'api_error'（429/5xx）——对账 TS 的结构字段先行。
	ErrorKind string
}

// FetchMarkdown 是抓取主链路（对账 TS `fetchMarkdown`）。
func FetchMarkdown(ctx context.Context, rawURL string, deps FetchCoreDeps, opts FetchMarkdownOptions) FetchMarkdownOutcome {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return FetchMarkdownOutcome{Error: "无效 URL：" + rawURL}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return FetchMarkdownOutcome{
			Error: fmt.Sprintf("不支持的协议：%s。仅允许 http 和 https。", parsed.Scheme),
		}
	}

	extractMain := true
	if opts.ExtractMainContent != nil {
		extractMain = *opts.ExtractMainContent
	}

	// maxAge 缓存：命中在降级链最前端直接返回，不发起任何请求。
	cache := deps.Cache
	if cache == nil {
		cache = NewFetchCache(FetchCacheDir(opts.Cwd), FetchCacheOptions{MaxAgeMs: opts.CacheMaxAgeMs})
	}
	variant := "e0"
	if extractMain {
		variant = "e1"
	}
	if cached, _ := cache.Read(rawURL, variant); cached != nil {
		return FetchMarkdownOutcome{
			OK:        true,
			Status:    cached.Status,
			Markdown:  cached.Markdown,
			Via:       cached.Via,
			Links:     ExtractLinksFromMarkdown(cached.Markdown),
			RawBytes:  len(cached.Markdown),
			FromCache: true,
			FetchedAt: cached.FetchedAt,
		}
	}

	writeCache := func(status int, markdown, via string) {
		if len(strings.TrimSpace(markdown)) < minSubstantialLength {
			return // 只写实质内容（宁旧勿错）
		}
		_ = cache.Write(rawURL, variant, FetchCacheEntry{
			URL: rawURL, Markdown: markdown, Via: via, Status: status,
		})
	}

	httpOpts := Options{
		TimeoutMs:        opts.HTTPTimeoutMs,
		MaxResponseBytes: opts.MaxBytes,
		MaxRedirects:     opts.MaxRedirects,
		UserAgent:        opts.UserAgent,
	}
	if httpOpts.UserAgent == "" {
		httpOpts.UserAgent = fetchCoreUserAgent
	}

	res, err := HTTPFetchGuarded(ctx, rawURL, deps.Deps, httpOpts)
	if err != nil {
		var ssrfErr *SSRFError
		if errors.As(err, &ssrfErr) {
			return FetchMarkdownOutcome{Error: ssrfErr.Error()}
		}
		return FetchMarkdownOutcome{Error: fmt.Sprintf("抓取失败 %s：%v", rawURL, err)}
	}

	contentTypeLower := strings.ToLower(res.ContentType)

	if res.Status >= 400 {
		// **坏状态码但有实质内容 → 视为成功**（部分站点 403/404 页仍渲染真实内容）。
		if strings.Contains(contentTypeLower, "text/html") {
			body := DecodeBody(res.Bytes, res.ContentType)
			md, err := HTMLToMarkdownSmart(body, SmartConvertOptions{
				HTMLToMarkdownOptions: HTMLToMarkdownOptions{PageURL: rawURL},
				OnlyMainContent:       BoolPtr(extractMain),
			})
			if err == nil && len(strings.TrimSpace(md)) >= minSubstantialLength {
				writeCache(res.Status, md, "")
				return FetchMarkdownOutcome{
					OK: true, Status: res.Status, Markdown: md,
					Links: ExtractLinks(body, rawURL), RawBytes: len(res.Bytes),
				}
			}
		}
		return FetchMarkdownOutcome{
			Error:     fmt.Sprintf("HTTP %d：%s", res.Status, rawURL),
			ErrorKind: httpAPIErrorKind(res.Status),
		}
	}

	for _, prefix := range binaryContentTypePrefixes {
		if strings.Contains(contentTypeLower, prefix) {
			return FetchMarkdownOutcome{
				Error: fmt.Sprintf("二进制内容（%s）不会以文本返回。请使用 import_resource 下载此 URL。", res.ContentType),
			}
		}
	}

	body := DecodeBody(res.Bytes, res.ContentType)

	content := body
	via := ""
	var links []string
	if strings.Contains(contentTypeLower, "text/html") {
		// crawl 发现源：**转换前**从原始 HTML 提链接（sidebar/menu 目录链接
		// 会被黑名单清洗剔除，markdown 层再提就丢了）。
		links = ExtractLinks(body, rawURL)
		md, err := HTMLToMarkdownSmart(body, SmartConvertOptions{
			HTMLToMarkdownOptions: HTMLToMarkdownOptions{PageURL: rawURL},
			OnlyMainContent:       BoolPtr(extractMain),
		})
		if err == nil {
			content = md
		}
		// 质量判败 → Jina 兜底（**跳过本地 Playwright 渲染那一跳**，见文件头）。
		if isJinaQualityHeuristic(content) {
			jinaOpts := FetchMarkdownOptions{
				Cwd: opts.Cwd, HTTPTimeoutMs: opts.HTTPTimeoutMs,
				MaxBytes: opts.MaxBytes, MaxRedirects: opts.MaxRedirects,
				UserAgent: opts.UserAgent,
			}
			jinaBase := deps.JinaBaseURL
			if jinaResult, ok := fetchViaJina(ctx, rawURL, deps.Deps, jinaOpts, jinaBase, deps.DisableJina); ok {
				content = jinaResult
				via = "（经 Jina Reader）"
				links = ExtractLinksFromMarkdown(content)
			}
		}
	}

	writeCache(res.Status, content, via)
	return FetchMarkdownOutcome{
		OK: true, Status: res.Status, Markdown: content, Via: via,
		Links: links, RawBytes: len(res.Bytes),
	}
}

// httpAPIErrorKind 对账 TS `httpApiErrorKind`。
//
// HTTP 状态码会留在文案里并命中 classifyFailure 的 api_error 正则——结构字段先行。
func httpAPIErrorKind(status int) string {
	switch status {
	case 429, 500, 502, 503:
		return "api_error"
	}
	return ""
}

// ── Jina Reader 兜底（对账 TS `jina-fetch.ts`，93 行）──────────────────

// defaultJinaBase 对账 TS `DEFAULT_JINA_BASE`。
const defaultJinaBase = "https://r.jina.ai"

// jinaQualitySignals 对账 TS `isJinaQualityHeuristic` 的 JS 页面信号表。
//
// 这些信号指示服务端返回了壳/错误/挑战页而非真实内容——触发 Jina 重抓，
// 而不是把垃圾交给用户。**只放不会出现在正常文章内容里的短语**。
var jinaQualitySignals = []string{
	"Please enable JavaScript",
	"Enable JavaScript",
	"This page requires JavaScript",
	"noscript",
	"Checking your browser",
	"Just a moment",
	"DDOS protection",
	`id="challenge-form"`,
	"error while loading",
	"please reload this page",
	"uh oh",
}

// isJinaQualityHeuristic 对账 TS `isJinaQualityHeuristic`。
//
// 本地提取很可能失败的判据：markdown 过小，或含 JS 渲染页信号。
// 阈值**有意保守**——宁可 Jina 重抓，也不静默返回垃圾。
func isJinaQualityHeuristic(md string) bool {
	trimmed := strings.TrimSpace(md)
	if len(trimmed) < 200 {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, sig := range jinaQualitySignals {
		if strings.Contains(lower, strings.ToLower(sig)) {
			return true
		}
	}
	return false
}

// fetchViaJina 通过 Jina Reader 抓取（对账 TS `fetchViaJina`）。
//
// **复用同一抓取管线**（对账 TS 注释）：代理解析、SSRF pin、超时、重定向
// 上限、body 大小上限——Jina 只是另一个 HTTP 目标，无需特殊处理。
//
// 返回 (markdown, ok)。
func fetchViaJina(ctx context.Context, rawURL string, deps Deps, opts FetchMarkdownOptions, baseOverride string, disabled bool) (string, bool) {
	if disabled {
		return "", false
	}
	base := baseOverride
	if base == "" {
		base = defaultJinaBase
	}
	jinaURL := strings.TrimRight(base, "/") + "/" + rawURL

	res, err := HTTPFetchGuarded(ctx, jinaURL, deps, Options{
		TimeoutMs:        opts.HTTPTimeoutMs,
		MaxResponseBytes: opts.MaxBytes,
		MaxRedirects:     opts.MaxRedirects,
		UserAgent:        opts.UserAgent,
	})
	if err != nil {
		return "", false
	}
	// 对账 TS：`ok = status >= 200 && status < 400 && body.trim().length > 0`。
	if res.Status < 200 || res.Status >= 400 {
		return "", false
	}
	body := DecodeBody(res.Bytes, res.ContentType)
	if strings.TrimSpace(body) == "" {
		return "", false
	}
	return body, true
}
