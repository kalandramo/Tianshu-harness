package tools

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webcrawl.go —— `web_crawl` 工具（第九十五刀 · W3-5b）。
//
// 对账 TS `src/tools/web-crawl/tool.ts`（202 行）。
//
// # 依赖已就位
//
// crawl BFS 内核（第九十四刀）+ sitemap 阶梯 + fetch 内核。
//
// # artifact 接线（第九十八刀）
//
// 本工具落地时（第九十五刀）**有意收窄**——每页 markdown 汇总为 artifact
// 的能力未接，摘要里恒无 artifact 注记，内容超过 15 页时只能重新爬取。
//
// 第九十八刀已接线：`Execute` 走 `p.ArtifactStore.Save(...)`，
// 语义与消费方同 TS（`tool.ts:177-192`）——`ArtifactStore` 非 nil 且
// `pages > 0` 时落盘；失败时**明示降级**为「（artifact 持久化失败，仅返回摘要）」
// 而不是静默省略。

// 常量（对账 TS）。
const (
	crawlDefaultMaxPages int = 20
	crawlMaxMaxPages     int = 200
	crawlDefaultMaxDepth int = 2
	crawlMaxMaxDepth     int = 10
	crawlDefaultBudgetMs int = 180_000
	crawlMaxBudgetMs     int = 600_000
	crawlMinBudgetMs     int = 10_000
	deniedExamplesCap    int = 8
	crawlSummaryPageCap  int = 15
	crawlSummaryErrorCap int = 5
)

// WebCrawl 创建 `web_crawl` 工具。
func WebCrawl(cwd string) Tool { return &webCrawlTool{cwd: cwd} }

// WebCrawlWithDeps 创建带**注入依赖**的 web_crawl（测试与未来配置层用）。
func WebCrawlWithDeps(cwd string, deps tnet.FetchCoreDeps, opts tnet.FetchMarkdownOptions) Tool {
	opts.Cwd = cwd
	return &webCrawlTool{cwd: cwd, deps: deps, opts: opts}
}

type webCrawlTool struct {
	cwd  string
	deps tnet.FetchCoreDeps
	opts tnet.FetchMarkdownOptions
}

func (t *webCrawlTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "web_crawl",
		Description: "从种子 URL 出发整站爬取（BFS 跟随链接 + sitemap 发现），批量获取各页正文。\n" +
			"适合「把这个文档站/知识库读完」。礼貌抓取：并发 4、同域名 300ms 间隔、页数/深度/预算受限；重复页面走缓存。\n" +
			"结果汇总为 artifact（可用 read_section 分页细读）。因发起大量网络请求，需要用户审批。",
		InputSchema: objSchemaOrdered(
			[]string{"url", "maxPages", "maxDepth", "includePaths", "excludePaths", "allowBackward", "budgetMs"},
			map[string]any{
				"url": strProp("种子 URL（只跟随同域名链接）"),
				"maxPages": numProp(fmt.Sprintf("页数上限（默认 %d，最大 %d）",
					crawlDefaultMaxPages, crawlMaxMaxPages)),
				"maxDepth": numProp(fmt.Sprintf("距种子跳数上限（默认 %d，最大 %d）",
					crawlDefaultMaxDepth, crawlMaxMaxDepth)),
				"includePaths": wireArrItemsFirst(
					"正则数组：只抓 pathname+query 命中的页面",
					wire.NewOrderedMap().Set("type", "string"),
				),
				"excludePaths": wireArrItemsFirst(
					"正则数组：跳过 pathname+query 命中的页面",
					wire.NewOrderedMap().Set("type", "string"),
				),
				"allowBackward": boolProp("允许跳出种子路径之外（默认 false，只抓种子之下）"),
				"budgetMs": numProp(fmt.Sprintf("总预算 ms（默认 %d，最大 %d）",
					crawlDefaultBudgetMs, crawlMaxBudgetMs)),
			},
			"url",
		),
	}
}

func (t *webCrawlTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawURL, _ := p.Input["url"].(string)
	if strings.TrimSpace(rawURL) == "" {
		return contract.Result{Content: "url 为必填项", IsError: true}, nil
	}
	seed, err := url.Parse(rawURL)
	if err != nil {
		return contract.Result{Content: "无效 URL：" + rawURL, IsError: true}, nil
	}
	if seed.Scheme != "http" && seed.Scheme != "https" {
		return contract.Result{
			Content: fmt.Sprintf("不支持的协议：%s。仅允许 http 和 https。", seed.Scheme),
			IsError: true,
		}, nil
	}

	include, errMsg := compileRegexList(p.Input["includePaths"])
	if errMsg != "" {
		return contract.Result{Content: "includePaths 参数错误：" + errMsg, IsError: true}, nil
	}
	exclude, errMsg := compileRegexList(p.Input["excludePaths"])
	if errMsg != "" {
		return contract.Result{Content: "excludePaths 参数错误：" + errMsg, IsError: true}, nil
	}

	maxPages := crawlClampInt(p.Input["maxPages"], crawlDefaultMaxPages, 1, crawlMaxMaxPages)
	maxDepth := crawlClampInt(p.Input["maxDepth"], crawlDefaultMaxDepth, 0, crawlMaxMaxDepth)
	budgetMs := crawlClampInt(p.Input["budgetMs"], crawlDefaultBudgetMs, crawlMinBudgetMs, crawlMaxBudgetMs)
	allowBackward, _ := p.Input["allowBackward"].(bool)

	// sitemap 阶梯预收集（**失败不阻塞 crawl**，对账 TS）。
	var sitemapURLs []string
	httpOpts := t.httpOptions()
	if sm := tnet.CollectSitemapURLs(seed, t.deps.Deps, httpOpts); len(sm.URLs) > 0 {
		sitemapURLs = sm.URLs
	}

	fetcher := func(u string) tnet.FetchMarkdownOutcome {
		opts := t.opts
		opts.Cwd = t.cwd
		return tnet.FetchMarkdown(ctx, u, t.deps, opts)
	}

	result := tnet.Crawl(rawURL, fetcher, tnet.CrawlOptions{
		MaxPages:      maxPages,
		MaxDepth:      maxDepth,
		IncludePaths:  include,
		ExcludePaths:  exclude,
		AllowBackward: allowBackward,
		BudgetMs:      budgetMs,
		Concurrency:   4,
		SitemapURLs:   sitemapURLs,
	})

	return contract.Result{Content: formatCrawlSummary(rawURL, result, t.saveCrawlArtifact(p, rawURL, result))}, nil
}

// RequiresApproval 恒 true（对账 TS `() => true`）——发起大量网络请求。
func (t *webCrawlTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 true（对账 TS `() => true`）。
func (t *webCrawlTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *webCrawlTool) Enabled() bool { return true }

// Timeout 对账 TS `timeoutMs: (params) => parseBudgetMs(params?.input ?? {}) + 30_000`。
//
// **诚实标注**：`registry.go` 声明了 `Tool.Timeout`，但 `internal/agent`
// **无统一读取点**（第八十八刀审查指出）。故此返回值当前**写而无人读**——
// 预算的实际约束靠 crawl 内部的 budgetMs，而非这里。
func (t *webCrawlTool) Timeout(p *CallParams) time.Duration {
	input := map[string]any{}
	if p != nil {
		input = p.Input
	}
	budget := crawlClampInt(input["budgetMs"], crawlDefaultBudgetMs, crawlMinBudgetMs, crawlMaxBudgetMs)
	return time.Duration(budget+30_000) * time.Millisecond
}

// httpOptions 构造 sitemap 抓取用的 HTTP 选项（对账 TS 的 `options`）。
func (t *webCrawlTool) httpOptions() tnet.Options {
	return tnet.Options{
		TimeoutMs:        t.opts.HTTPTimeoutMs,
		MaxResponseBytes: t.opts.MaxBytes,
		MaxRedirects:     t.opts.MaxRedirects,
		UserAgent:        t.opts.UserAgent,
	}
}

// ── 参数辅助（对账 TS `clampInt` / `compileRegexList`）──────────────────

// crawlClampInt 对账 TS `clampInt`。
func crawlClampInt(v any, fallback, min, max int) int {
	n := fallback
	if f, ok := v.(float64); ok {
		n = int(f) // 向下取整（对账 TS `Math.floor`）
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

// compileRegexList 对账 TS `compileRegexList`——返回 (regexes, errMsg)。
func compileRegexList(raw any) ([]*regexp.Regexp, string) {
	if raw == nil {
		return nil, ""
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, "必须是字符串数组"
	}
	var out []*regexp.Regexp
	for _, item := range arr {
		pattern, ok := item.(string)
		if !ok {
			return nil, "必须是字符串数组"
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, "无效正则：" + pattern
		}
		out = append(out, re)
	}
	return out, ""
}

// ── artifact 构建（对账 TS `buildCrawlArtifact`）────────────────────────

// buildCrawlArtifact 对账 TS `buildCrawlArtifact`——sections 按页切分。
func buildCrawlArtifact(pages []tnet.CrawlPage) (string, []artifact.ArtifactSection) {
	var chunks []string
	var sections []artifact.ArtifactSection
	lineCursor := 1

	for _, page := range pages {
		header := "# " + page.URL + "\n\n"
		body := page.Markdown + "\n\n---\n\n"
		text := header + body
		lines := strings.Count(text, "\n")
		sections = append(sections, artifact.ArtifactSection{
			Name:      page.URL,
			LineStart: lineCursor,
			LineEnd:   lineCursor + lines - 1,
			CharCount: len(text),
		})
		lineCursor += lines
		chunks = append(chunks, text)
	}
	return strings.Join(chunks, ""), sections
}

// saveCrawlArtifact 把各页正文汇总落 artifact，返回摘要尾部的注记。
//
// 对账 TS `tool.ts:177-192`：
//
//	let artifactNote = ''
//	if (params.artifactStore && result.pages.length > 0) {
//	  try { … artifactNote = `\n\n完整内容已存 artifact：${id}（可用 read_section 分页细读）` }
//	  catch { artifactNote = '\n\n（artifact 持久化失败，仅返回摘要）' }
//	}
//
// **失败是明示降级**（不是静默省略）——对账 TS 的 `catch` 分支，
// 逐字保留其文案。**没有 ArtifactStore 时注记为空**（对账 TS 的
// `params.artifactStore &&` 短路）——那是「调用方没配」而非「写失败了」，
// 两者必须可区分。
func (t *webCrawlTool) saveCrawlArtifact(p *CallParams, rawURL string, result tnet.CrawlResult) string {
	if p == nil || p.ArtifactStore == nil || len(result.Pages) == 0 {
		return ""
	}
	rawContent, sections := buildCrawlArtifact(result.Pages)
	id, err := p.ArtifactStore.Save(artifact.SaveInput{
		Tool:       "web_crawl",
		Target:     rawURL,
		RawContent: rawContent,
		Summary:    fmt.Sprintf("crawl %s：%d 页正文", rawURL, len(result.Pages)),
		Sections:   sections,
	})
	if err != nil {
		return "\n\n（artifact 持久化失败，仅返回摘要）"
	}
	return "\n\n完整内容已存 artifact：" + id + "（可用 read_section 分页细读）"
}

// ── 摘要格式化（对账 TS `formatCrawlSummary`）───────────────────────────

// formatCrawlSummary 对账 TS `formatCrawlSummary`。
func formatCrawlSummary(seedURL string, result tnet.CrawlResult, artifactNote string) string {
	cached := 0
	for _, p := range result.Pages {
		if p.FromCache {
			cached++
		}
	}

	var lines []string
	truncNote := ""
	if result.Truncated {
		truncNote = "（已达上限/预算，截断）"
	}
	lines = append(lines,
		fmt.Sprintf("爬取完成：%s（耗时 %.1fs）", seedURL, float64(result.DurationMs)/1000),
		fmt.Sprintf("成功 %d 页（缓存命中 %d）/ 失败 %d / 跳过 %d 个候选%s",
			len(result.Pages), cached, len(result.Errors), len(result.Denied), truncNote),
	)

	if len(result.Pages) > 0 {
		lines = append(lines, "", "页面清单：")
		for i, p := range result.Pages {
			if i >= crawlSummaryPageCap {
				break
			}
			lines = append(lines, fmt.Sprintf("  %d. %s（%d%s，%d 字符）",
				i+1, p.URL, p.Status, p.Via, len([]rune(p.Markdown))))
		}
		if len(result.Pages) > crawlSummaryPageCap {
			lines = append(lines, fmt.Sprintf("  … 其余 %d 页见 artifact",
				len(result.Pages)-crawlSummaryPageCap))
		}
	}

	if len(result.Denied) > 0 {
		// 拒绝原因分布（对账 TS 的 Map 聚合）
		counts := map[string]int{}
		var order []string
		for _, d := range result.Denied {
			if _, seen := counts[d.Reason]; !seen {
				order = append(order, d.Reason)
			}
			counts[d.Reason]++
		}
		var parts []string
		for _, r := range order {
			label := tnet.DenialReasonText(r)
			parts = append(parts, fmt.Sprintf("%s ×%d", label, counts[r]))
		}
		lines = append(lines, "", "跳过原因分布："+strings.Join(parts, "、"))

		for i, d := range result.Denied {
			if i >= deniedExamplesCap {
				break
			}
			lines = append(lines, fmt.Sprintf("  - %s（%s）", d.URL, tnet.DenialReasonText(d.Reason)))
		}
	}

	if len(result.Errors) > 0 {
		lines = append(lines, "", fmt.Sprintf("失败（前 %d）：", crawlSummaryErrorCap))
		for i, e := range result.Errors {
			if i >= crawlSummaryErrorCap {
				break
			}
			firstLine := e.Error
			if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
				firstLine = firstLine[:idx]
			}
			lines = append(lines, "  - "+e.URL+" — "+firstLine)
		}
	}

	return strings.Join(lines, "\n") + artifactNote
}
