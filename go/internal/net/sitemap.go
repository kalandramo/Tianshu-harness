package net

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// sitemap.go —— crawl/map 的 sitemap 阶梯发现源（第九十五刀 · W3-5b）。
//
// 对账 TS `src/tools/web-crawl/sitemap.ts`（96 行）。
//
// # 阶梯顺序（对账 TS，firecrawl 同序）
//
//	robots.txt 声明的 `Sitemap:` → 种子目录 `sitemap.xml` → origin `sitemap.xml`
//
// sitemapindex 递归（上限 25 个 sitemap）；URL 上限 500。
//
// # 已知裁减（对账 TS 注释，诚实披露）
//
//   - 无 eTLD+1 库，「主域 sitemap」未实现（同 hostname 场景已覆盖绝大多数文档站）
//   - `.gz` sitemap **不解压**（跳过）

// maxSitemaps 对账 TS `MAX_SITEMAPS`。
const maxSitemaps = 25

// maxSitemapURLs 对账 TS `MAX_SITEMAP_URLS`。
const maxSitemapURLs = 500

// SitemapCollectResult 对账 TS `SitemapCollectResult`。
type SitemapCollectResult struct {
	URLs []string
	// SitemapsHit：实际尝试过的 sitemap（诊断用）。
	SitemapsHit []string
}

// locRe 对账 TS `/<loc>\s*([^<]+?)\s*<\/loc>/gi`。
var locRe = regexp.MustCompile(`(?is)<loc>\s*([^<]+?)\s*</loc>`)

// robotsSitemapRe 对账 TS `/^\s*sitemap:\s*(\S+)\s*$/i`。
var robotsSitemapRe = regexp.MustCompile(`(?im)^\s*sitemap:\s*(\S+)\s*$`)

// xmlSuffixRe 对账 TS `/\.xml(\.gz)?$/i`。
var xmlSuffixRe = regexp.MustCompile(`(?i)\.xml(\.gz)?$`)

// extractLocs 对账 TS `extractLocs`。
func extractLocs(xml string) []string {
	var out []string
	for _, m := range locRe.FindAllStringSubmatch(xml, -1) {
		if len(m) > 1 {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

// CollectSitemapURLs 按阶梯收集 sitemap 中的 URL。
//
// 对账 TS `collectSitemapUrls`。
//
// `deps` / `opts` 与 HTTP 抓取层共用——sitemap 抓取走同一 SSRF 与超时防护。
func CollectSitemapURLs(seed *url.URL, deps Deps, opts Options) SitemapCollectResult {
	origin := seed.Scheme + "://" + seed.Host

	fetchText := func(rawURL string) string {
		res, err := HTTPFetchGuarded(context.Background(), rawURL, deps, opts)
		if err != nil || res.Status >= 400 {
			return ""
		}
		// **不做内容形态门禁**（对账 TS 注释）：robots.txt 是纯文本、
		// soft-404 的 HTML 提不出 `<loc>`——无效内容在 loc 提取层自然落空。
		return DecodeBody(res.Bytes, res.ContentType)
	}

	// 阶梯候选：robots 声明 → 种子目录 → origin 根
	var candidates []string
	if robots := fetchText(origin + "/robots.txt"); robots != "" {
		for _, line := range strings.Split(robots, "\n") {
			if m := robotsSitemapRe.FindStringSubmatch(line); len(m) > 1 {
				candidates = append(candidates, m[1])
			}
		}
	}
	seedDir := seed.Path
	if !strings.HasSuffix(seedDir, "/") {
		if idx := strings.LastIndex(seedDir, "/"); idx >= 0 {
			seedDir = seedDir[:idx+1]
		} else {
			seedDir = "/"
		}
	}
	candidates = append(candidates, origin+seedDir+"sitemap.xml")
	candidates = append(candidates, origin+"/sitemap.xml")

	hit := map[string]bool{}
	var hitOrder []string
	var urls []string
	queue := dedupStrings(candidates)

	for len(queue) > 0 && len(hitOrder) < maxSitemaps && len(urls) < maxSitemapURLs {
		sitemapURL := queue[0]
		queue = queue[1:]
		if hit[sitemapURL] || strings.HasSuffix(sitemapURL, ".gz") {
			continue
		}
		hit[sitemapURL] = true
		hitOrder = append(hitOrder, sitemapURL)

		body := fetchText(sitemapURL)
		if body == "" {
			continue
		}
		for _, loc := range extractLocs(body) {
			locURL, err := url.Parse(loc)
			if err != nil {
				continue
			}
			if xmlSuffixRe.MatchString(locURL.Path) {
				queue = append(queue, loc) // sitemapindex → 递归
			} else if len(urls) < maxSitemapURLs {
				urls = append(urls, loc)
			}
		}
	}

	return SitemapCollectResult{URLs: urls, SitemapsHit: hitOrder}
}

// dedupStrings 保序去重（对账 TS `[...new Set(candidates)]`）。
func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
