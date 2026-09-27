package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webmap.go —— `web_map` 工具（第九十六刀 · W3-5c）。
//
// 对账 TS `src/tools/web-crawl/map-tool.ts`（134 行）。
//
// # 三路来源
//
//  1. sitemap 阶梯（`CollectSitemapURLs`）
//  2. 种子页链接（`FetchMarkdown` 的 Links）
//  3. `site:host` 搜索（复用 web_search 后端链）
//
// 过滤：同域 → 可选子域 → 按路径前缀 → 去重。
// `search` 存在时**纯词频 cosine 重排**。
//
// # 有意收窄（诚实披露）
//
// **第三路来源（site: 搜索）当前不可用**——它依赖 `web_search` 的后端链
// （`web-search/*.ts` 1085 行），Go 侧未移植。
//
// 这**不是静默失败**：`search` 参数会触发「（无可用后端）」的如实报告，
// 且**不阻塞前两路**（对账 TS 的 `try/catch` 语义）。
// 待 `web_search` 落地后，只需给 `WebMapDeps.SearchBackends` 注入实现即可接通。

// 常量（对账 TS）。
const (
	mapDefaultLimit    int = 100
	mapMaxLimit        int = 5_000
	mapSearchTimeoutMs int = 15_000
)

// WebMapSearchBackend 是 `site:` 搜索的后端（对账 TS `SearchBackend` 的裁剪版）。
//
// **当前无实现**——`web_search` 移植后提供。接口先行，便于接线。
type WebMapSearchBackend interface {
	// Name 返回后端标识（用于摘要归属）。
	Name() string
	// Search 执行一次搜索。失败应返回 error（调用方记录并落空）。
	Search(query string, count int) ([]WebMapSearchResult, error)
}

// WebMapSearchResult 对账 TS `SearchResult`（裁剪版）。
type WebMapSearchResult struct {
	Title string
	URL   string
}

// WebMap 创建 `web_map` 工具。
func WebMap(cwd string) Tool { return &webMapTool{cwd: cwd} }

// WebMapWithDeps 创建带**注入依赖**的 web_map（测试与未来配置层用）。
func WebMapWithDeps(cwd string, deps tnet.FetchCoreDeps, opts tnet.FetchMarkdownOptions) Tool {
	opts.Cwd = cwd
	return &webMapTool{cwd: cwd, deps: deps, opts: opts}
}

// WebMapWithBackends 创建带**搜索后端**的 web_map。
//
// 待 `web_search` 移植后，配置层用它接通第三路来源。
func WebMapWithBackends(cwd string, deps tnet.FetchCoreDeps, opts tnet.FetchMarkdownOptions, backends []WebMapSearchBackend) Tool {
	opts.Cwd = cwd
	return &webMapTool{cwd: cwd, deps: deps, opts: opts, backends: backends}
}

type webMapTool struct {
	cwd      string
	deps     tnet.FetchCoreDeps
	opts     tnet.FetchMarkdownOptions
	backends []WebMapSearchBackend
}

func (t *webMapTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "web_map",
		Description: "发现站点内的 URL 清单（sitemap + 种子页链接 + site: 搜索三路汇合）。\n" +
			"适合「先看看这个站有哪些页面」或为 web_crawl 探路。轻量：不爬全站。\n" +
			"因发起网络请求，需要用户审批。",
		InputSchema: objSchemaOrdered(
			[]string{"url", "search", "limit", "includeSubdomains"},
			map[string]any{
				"url":    strProp("站点 URL（建议给 base domain 效果更好）"),
				"search": strProp("可选关键词：触发 site:host 搜索并按相关度重排结果"),
				"limit": numProp(fmt.Sprintf("返回条数上限（默认 %d，最大 %d）",
					mapDefaultLimit, mapMaxLimit)),
				"includeSubdomains": boolProp("包含子域名（默认 false，只同域名）"),
			},
			"url",
		),
	}
}

func (t *webMapTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
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

	search, _ := p.Input["search"].(string)
	search = strings.TrimSpace(search)
	limit := mapClampLimit(p.Input["limit"])
	includeSubdomains, _ := p.Input["includeSubdomains"].(bool)
	seedHost := strings.ToLower(seed.Hostname())

	collector := tnet.NewMapCollector()
	sourceStats := map[string]int{"sitemap": 0, "page": 0, "search": 0}

	// ── 来源 1：sitemap 阶梯（**失败静默**，对账 TS）──
	if sm := tnet.CollectSitemapURLs(seed, t.deps.Deps, t.httpOptions()); len(sm.URLs) > 0 {
		for _, u := range sm.URLs {
			collector.Add(u, "sitemap", "")
		}
	}

	// ── 来源 2：种子页链接 ──
	fetchOpts := t.opts
	fetchOpts.Cwd = t.cwd
	seedOutcome := tnet.FetchMarkdown(ctx, rawURL, t.deps, fetchOpts)
	if seedOutcome.OK {
		for _, link := range seedOutcome.Links {
			collector.Add(link, "page", "")
		}
	}

	// ── 来源 3：site: 搜索（仅给关键词且有后端时）──
	searchBackendName := ""
	if search != "" && len(t.backends) > 0 {
		query := search + " site:" + seedHost
		for _, b := range t.backends {
			results, err := b.Search(query, limit)
			if err != nil || len(results) == 0 {
				continue
			}
			searchBackendName = b.Name()
			for _, r := range results {
				collector.Add(r.URL, "search", r.Title)
			}
			break // 链式：首个有结果的后端胜出（对账 TS）
		}
	}

	// ── 过滤：同域/子域 → 路径前缀 ──
	var filtered []tnet.MapCandidate
	for _, c := range collector.List() {
		u, err := url.Parse(c.URL)
		if err != nil {
			continue
		}
		if !tnet.IsSameOrSubDomain(strings.ToLower(u.Hostname()), seedHost, includeSubdomains) {
			continue
		}
		if !tnet.FilterByPathPrefix(c.URL, seed.Path) {
			continue
		}
		filtered = append(filtered, c)
	}
	for _, c := range filtered {
		for src := range c.Sources {
			sourceStats[src]++
		}
	}

	// ── 重排 + 截断 ──
	ranked := filtered
	if search != "" {
		ranked = tnet.RerankByCosine(filtered, search)
	}
	results := ranked
	if len(results) > limit {
		results = results[:limit]
	}

	// ── 摘要组装（对账 TS）──
	var lines []string
	truncNote := ""
	if len(ranked) > limit {
		truncNote = fmt.Sprintf("，已按 limit=%d 截断", limit)
	}
	lines = append(lines,
		fmt.Sprintf("站点地图：%s（共 %d 个 URL%s）", rawURL, len(results), truncNote),
		fmt.Sprintf("来源分布：sitemap ×%d、种子页链接 ×%d%s",
			sourceStats["sitemap"], sourceStats["page"], t.searchSuffix(search, sourceStats["search"], searchBackendName)),
		"",
	)
	for _, r := range results {
		if r.Title != "" {
			lines = append(lines, r.URL+" — "+r.Title)
		} else {
			lines = append(lines, r.URL)
		}
	}
	if len(results) <= 1 && seed.Path != "/" && seed.Path != "" {
		lines = append(lines, "",
			fmt.Sprintf("⚠ 只找到 %d 个结果——建议改用 base domain（%s://%s）作为 url 再试，sitemap 与链接发现通常更全。",
				len(results), seed.Scheme, seed.Host))
	}
	return contract.Result{Content: strings.Join(lines, "\n")}, nil
}

// searchSuffix 组装来源分布里的搜索片段（对账 TS 的三元表达式）。
//
// **诚实报告**：search 非空但无后端时输出「（无可用后端）」。
func (t *webMapTool) searchSuffix(search string, searchCount int, backendName string) string {
	if search == "" {
		return ""
	}
	label := backendName
	if label == "" {
		label = "无可用后端"
	}
	return fmt.Sprintf("、搜索 ×%d（%s）", searchCount, label)
}

// RequiresApproval 恒 true（对账 TS `() => true`）。
func (t *webMapTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 true（对账 TS `() => true`）。
func (t *webMapTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *webMapTool) Enabled() bool { return true }

// Timeout 用默认（0）——轻量发现（不爬全站），各次抓取由内核自带 15s 超时。
//
// **诚实标注**：`registry.go` 声明了 `Tool.Timeout`，但 `internal/agent`
// **无统一读取点**（第八十八刀审查指出）。故此返回值当前写而无人读。
func (t *webMapTool) Timeout(_ *CallParams) time.Duration { return 0 }

// httpOptions 构造 sitemap 抓取用的 HTTP 选项。
func (t *webMapTool) httpOptions() tnet.Options {
	return tnet.Options{
		TimeoutMs:        t.opts.HTTPTimeoutMs,
		MaxResponseBytes: t.opts.MaxBytes,
		MaxRedirects:     t.opts.MaxRedirects,
		UserAgent:        t.opts.UserAgent,
	}
}

// mapClampLimit 对账 TS `clampLimit`。
func mapClampLimit(v any) int {
	n := mapDefaultLimit
	if f, ok := v.(float64); ok {
		n = int(f)
	}
	if n < 1 {
		n = 1
	}
	if n > mapMaxLimit {
		n = mapMaxLimit
	}
	return n
}
