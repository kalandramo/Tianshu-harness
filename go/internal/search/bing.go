package search

import (
	"context"
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

// bing.go —— Bing（cn.bing.com）抓取后端（第九十七刀 · W4）。
//
// 对账 TS `bing.ts`（186 行）。
//
// # 为什么用 cn.bing.com
//
// 它是**国内可达**的 Bing 镜像，返回完整服务端渲染 HTML 与直连目标 URL
// （无 `/ck/a` 跳转包装、无 JS 挑战）。国际版 `www.bing.com` 的 SERP 后有
// Turnstile CAPTCHA，`www.baidu.com` 返回 JS 插页——故 cn.bing.com 是唯一
// 能用纯 fetch 抓取的零配置国内可用后端。
//
// # ★ 为什么用浏览器 UA（对账 TS 注释，**不可改动**）
//
// 与 DDG（接受 `terminal-coding-agent/1.0`）不同，**Bing 对非浏览器 UA 返回
// 降级/空 SERP**。下面的 UA 是通用 Chrome 120 串，能稳定拿到完整结果。
//
// # ★ 语言标识陷阱（对账 TS 注释，**不可重新引入**）
//
// cn.bing.com 对携带**任何英文语言标识**（`setlang=en-US` 或
// `Accept-Language: en`，**任一单独出现即触发**）的请求，在一部分中文查询上
// **静默错路由**：HTTP 200、结构完美的 SERP、内容与查询无关
// （`杭州西湖 门票预约` → 高校研究生院 / Nvidia 驱动问答）。
// 2026-09 对线上 cn.bing.com 实测：全中文标识 3/3 返回切题，任一英文标识
// 返回跑题。故此处**刻意不传 setlang**，`Accept-Language` 用中文优先。
// `relevance.go` 作为第二道防线兜底。
const bingEndpoint = "https://cn.bing.com/search"

// bingUA 对账 TS `BING_UA`。
const bingUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var (
	// bingBlockSplit 按 `b_algo` 切块（首块是前导片段，丢弃）。
	bingBlockSplit = `<li class="b_algo`
	// bingH2Re 匹配 `<h2>` 包裹（**旧结构**）。
	bingH2Re = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`)
	// bingAnchorRe 匹配任意 `<a href="...">...</a>`。
	bingAnchorRe = regexp.MustCompile(`(?is)<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	// bingSnippetRe 匹配 `b_lineclamp\d` 摘要（覆盖 b_lineclamp2 与 b_lineclamp4）。
	bingSnippetRe = regexp.MustCompile(`(?is)<p[^>]+class="b_lineclamp\d"[^>]*>(.*?)</p>`)
)

// ParseBingResults 解析 Bing（cn.bing.com）自然结果。
//
// 结构（对账 TS 注释，针对线上响应验证过）：
//
//   - **旧结构**（2026-07 及之前）：
//     `<li class="b_algo">` → `<div class="b_tpcn"><a class="tilk">`（**跳过**）
//     → `<h2><a href="URL">TITLE</a></h2>` → `<p class="b_lineclamp2">SNIPPET</p>`
//   - **当前结构**（2026-07 改版后）：`<h2>` 包装被去掉，标题链接变成
//     `b_algo` 内**裸 `<a>`**，前面还有内联 `<link rel="stylesheet">` 与
//     可选的 `tilk` favicon 链接。
//
// 解析策略：优先用 `<h2>` 锚点（旧结构，能干净跳过 favicon 链接）；
// 无 `<h2>` 时退化为「扫描块内首个 href 为**外部 http(s)** 的 `<a>`」——
// 这样能跳过 `tilk` favicon（同源）与 Bing 自有导航链接。
func ParseBingResults(html string, maxCount int) []Result {
	results := []Result{}
	blocks := strings.Split(html, bingBlockSplit)
	for i := 1; i < len(blocks) && len(results) < maxCount; i++ {
		block := blocks[i]

		link := extractBingTitleLink(block)
		if link == nil {
			continue
		}
		rawURL := DecodeHTMLEntities(link.url)
		finalURL := decodeBingCkAURL(rawURL)
		title := collapseSpaces(DecodeHTMLEntities(StripHTML(link.title)))
		if title == "" || finalURL == "" || !httpSchemeRe.MatchString(finalURL) {
			continue
		}

		snippet := ""
		if sm := bingSnippetRe.FindStringSubmatch(block); sm != nil {
			snippet = collapseSpaces(DecodeHTMLEntities(StripHTML(sm[1])))
		}

		results = append(results, Result{Title: title, URL: finalURL, Snippet: snippet})
	}
	return results
}

type bingLink struct{ url, title string }

// extractBingTitleLink 从 `b_algo` 块里取出标题链接的 (url, title-html)。
//
// 策略（对账 TS `extractTitleLink`）：
//  1. 旧结构：锚定 `<h2>`，取其中首个 `<a>`——干净跳过前置的 `tilk` favicon 链接
//  2. 当前结构（无 `<h2>`）：扫描块内所有 `<a href="...">`，取首个 href 为
//     **外部 http(s)** 者。跳过：`tilk` favicon（同源 Bing URL）、
//     Bing 导航/分页（bing.com / microsoft.com / go.microsoft）、非 http 锚点
//     （mailto / javascript / 页内 `#`）
//
// **扫描窗口截到块首 4KB**——长结果列表里不至于把远处无关链接拖进标题位。
func extractBingTitleLink(block string) *bingLink {
	// 1. 旧结构 <h2> 锚点
	if h2 := bingH2Re.FindStringSubmatch(block); h2 != nil {
		if m := bingAnchorRe.FindStringSubmatch(h2[1]); m != nil {
			return &bingLink{url: m[1], title: m[2]}
		}
	}
	// 2. 退化：块首 4KB 内首个外部 http(s) <a>
	head := block
	if len(head) > 4096 {
		head = head[:4096]
	}
	for _, m := range bingAnchorRe.FindAllStringSubmatch(head, -1) {
		href := m[1]
		if !httpSchemeRe.MatchString(href) {
			continue
		}
		if isBingInternalURL(href) {
			continue
		}
		return &bingLink{url: href, title: m[2]}
	}
	return nil
}

// isBingInternalURL 判断 URL 是否指向 Bing/微软基础设施（favicon、导航等）。
//
// 对账 TS `isBingInternalUrl`。
func isBingInternalURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.HasSuffix(host, ".bing.com") ||
		strings.HasSuffix(host, ".microsoft.com") ||
		host == "go.microsoft.com" ||
		host == "r.bing.com"
}

// decodeBingCkAURL 解码国际版 Bing 的 `/ck/a?...&u=a1aHR0cHM…` 跳转包装。
//
// `u` 参数是真实 URL 的 **base64url**，前置 2 字符标签（`a1`/`a3`）。
// cn.bing.com 直接给裸 URL，故此函数通常是 no-op；
// 但保留这条分支让解析器在 Bing 某天改用包装链接时仍可用（对账 TS）。
func decodeBingCkAURL(rawURL string) string {
	if !strings.Contains(rawURL, "/ck/a") {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u := parsed.Query().Get("u")
	if u == "" {
		return rawURL
	}
	// 去掉 2 字符 base64url 标签前缀，再解码
	if len(u) <= 2 {
		return rawURL
	}
	b64 := strings.NewReplacer("-", "+", "_", "/").Replace(u[2:])
	// base64url 无 padding——Go 的 RawURLEncoding 直接支持，但为兼容
	// 可能带 padding 的输入，先补齐再试标准解码。
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		return rawURL
	}
	if httpSchemeRe.MatchString(string(decoded)) {
		return string(decoded)
	}
	return rawURL
}

// BingBackend 是免费零配置后端。
//
// 对账 TS `BingBackend`：无 API key，`IsAvailable()` 恒 true。
// 作为**国内可达的主后端**；DuckDuckGo 在默认链里作为海外兜底。
type BingBackend struct{ fetch Fetch }

// NewBingBackend 创建 Bing 后端。
func NewBingBackend(fetch Fetch) *BingBackend { return &BingBackend{fetch: fetch} }

func (b *BingBackend) Name() string      { return "bing" }
func (b *BingBackend) IsAvailable() bool { return true }

func (b *BingBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	// **刻意不传 setlang**，Accept-Language 用中文优先——见文件头「语言标识陷阱」。
	req := &Request{
		URL: bingEndpoint + "?q=" + url.QueryEscape(query),
		Headers: map[string]string{
			"User-Agent":      bingUA,
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.5",
		},
		// 对账 TS：`redirect: 'manual'`——3xx 到未校验主机会显示为非 ok，链落空。
		NoRedirect: true,
	}
	res, err := b.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &HTTPStatusError{Status: res.Status}
	}
	return ParseBingResults(string(res.Body), count), nil
}
