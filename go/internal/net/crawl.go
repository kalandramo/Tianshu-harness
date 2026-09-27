package net

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// crawl.go —— 进程内隐式 BFS 引擎（第九十四刀 · W3-5a）。
//
// 对账 TS `src/tools/web-crawl/crawl.ts`（199 行）+ `filter-links.ts`（85 行）。
//
// # firecrawl 的等价物
//
// firecrawl 的 crawl 是 **job 自我增殖构成的图**（每个 job 完成后「提链接 →
// 原子去重 → 派新 job」），收敛靠 `jobs == jobs_done` 集合基数判定。
// 进程内化后的等价物：**frontier 数组 + visited Set + inFlight 计数**，
// **收敛 = frontier 空 && inFlight == 0**。
//
// # 并发模型（Go 版）
//
// TS 用 Promise + 显式 pump 循环。Go 版用 goroutine + WaitGroup + 互斥锁——
// 语义等价：并发度受 `concurrency` 钳制，收敛条件不变。

// ── 过滤链（对账 TS `filter-links.ts`）────────────────────────────────

// denialReasonText 对账 TS `DENIAL_REASON_TEXT`。
var denialReasonText = map[string]string{
	"non_http":          "非 http(s) 协议",
	"cross_domain":      "跨域名",
	"max_depth":         "超过路径深度上限",
	"excluded_path":     "命中 excludePaths",
	"not_included_path": "不在 includePaths 内",
	"backward_path":     "位于种子路径之外",
	"file_extension":    "二进制/媒体文件扩展名",
}

// blockedExtensions 对账 TS `BLOCKED_EXTENSIONS`。
//
// crawl 无文本价值的扩展名黑名单——当前管线消费不了 pdf/office/媒体/压缩包
// （web_fetch 对它们返回二进制提示），跟随它们只会浪费一次抓取。
var blockedExtensions = []string{
	"png", "jpg", "jpeg", "gif", "webp", "svg", "ico", "bmp", "avif",
	"mp4", "mp3", "avi", "mov", "webm", "wav", "flac", "ogg",
	"zip", "gz", "tar", "rar", "7z", "bz2", "xz",
	"woff", "woff2", "ttf", "otf", "eot",
	"exe", "dmg", "msi", "apk", "deb", "rpm",
	"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "rtf",
	"csv", "parquet", "db", "sqlite",
	"wasm", "so", "dll", "dylib", "jar", "class",
	"css", "map",
}

// blockedExtRe 对账 TS `new RegExp('\\.(exts)$', 'i')`——**只匹配结尾**。
var blockedExtRe = regexp.MustCompile(`(?i)\.(` + strings.Join(blockedExtensions, "|") + `)$`)

// getURLDepth 对账 TS `getUrlDepth`——路径段数。
func getURLDepth(pathname string) int {
	n := 0
	for _, seg := range strings.Split(pathname, "/") {
		if seg != "" {
			n++
		}
	}
	return n
}

// LinkFilterOptions 对账 TS `LinkFilterOptions`。
type LinkFilterOptions struct {
	SeedHost      string
	SeedPath      string
	SeedDepth     int
	MaxDepth      int
	IncludePaths  []*regexp.Regexp
	ExcludePaths  []*regexp.Regexp
	AllowBackward bool
}

// filterLink 对账 TS `filterLink`——返回空串表示通过。
//
// **顺序以代码为准**（TS 注释漏了 `cross_domain`）：
// 协议 → 跨域 → 扩展名 → 深度 → exclude → include → backward。
func filterLink(u *url.URL, opts LinkFilterOptions) string {
	if u.Scheme != "http" && u.Scheme != "https" {
		return "non_http"
	}
	if strings.ToLower(u.Hostname()) != opts.SeedHost {
		return "cross_domain"
	}
	if blockedExtRe.MatchString(u.Path) {
		return "file_extension"
	}
	if getURLDepth(u.Path) > opts.SeedDepth+opts.MaxDepth {
		return "max_depth"
	}
	// **include/exclude 匹配 path + query**（对账 TS `url.pathname + url.search`）。
	pathQuery := u.Path
	if u.RawQuery != "" {
		pathQuery += "?" + u.RawQuery
	}
	for _, re := range opts.ExcludePaths {
		if re.MatchString(pathQuery) {
			return "excluded_path"
		}
	}
	if len(opts.IncludePaths) > 0 {
		matched := false
		for _, re := range opts.IncludePaths {
			if re.MatchString(pathQuery) {
				matched = true
				break
			}
		}
		if !matched {
			return "not_included_path"
		}
	}
	if !opts.AllowBackward {
		// 须位于种子路径之下：种子视为目录（`/docs/guide` → 允许 `/docs/guide/…`）。
		seedDir := opts.SeedPath
		if !strings.HasSuffix(seedDir, "/") {
			seedDir += "/"
		}
		if seedDir != "/" && !strings.HasPrefix(u.Path, seedDir) {
			return "backward_path"
		}
	}
	return ""
}

// ── BFS 引擎（对账 TS `crawl.ts`）──────────────────────────────────────

// CrawlOptions 对账 TS `CrawlOptions`。
type CrawlOptions struct {
	MaxPages             int
	MaxDepth             int
	IncludePaths         []*regexp.Regexp
	ExcludePaths         []*regexp.Regexp
	AllowBackward        bool
	BudgetMs             int
	Concurrency          int
	MinIntervalPerHostMs int
	SitemapURLs          []string
}

// CrawlPage 对账 TS `CrawlPage`。
type CrawlPage struct {
	URL       string
	Depth     int
	Status    int
	Markdown  string
	Via       string
	FromCache bool
}

// CrawlDenied 对账 TS `CrawlDenied`。
type CrawlDenied struct {
	URL    string
	Reason string
}

// CrawlError 对账 TS `CrawlError`。
type CrawlError struct {
	URL   string
	Error string
}

// CrawlResult 对账 TS `CrawlResult`。
type CrawlResult struct {
	Pages  []CrawlPage
	Denied []CrawlDenied
	Errors []CrawlError
	// Truncated：因页数/预算截断（frontier 还有未抓的候选）。
	Truncated  bool
	DurationMs int64
}

// CrawlFetcher 对账 TS `CrawlFetcher`。
type CrawlFetcher func(url string) FetchMarkdownOutcome

// wwwVariant 对账 TS `wwwVariant`。
//
// 轻量相似 URL 去重——firecrawl `generateURLPermutations` 的最小子集。
func wwwVariant(normalizedURL string) string {
	if strings.Contains(normalizedURL, "://www.") {
		return strings.Replace(normalizedURL, "://www.", "://", 1)
	}
	return strings.Replace(normalizedURL, "://", "://www.", 1)
}

// Crawl 是 BFS 引擎的公开入口（对账 TS `crawl`）。
//
// 供 `internal/tools` 的 web_crawl / web_map 消费。
func Crawl(seedURL string, fetcher CrawlFetcher, opts CrawlOptions) CrawlResult {
	return crawlWith(seedURL, fetcher, opts)
}

// DenialReasonText 返回拒绝原因的中文文案（对账 TS `DENIAL_REASON_TEXT`）。
//
// 未知原因返回原值（不 panic——上游可能给出未来新增的原因）。
func DenialReasonText(reason string) string {
	if t, ok := denialReasonText[reason]; ok {
		return t
	}
	return reason
}

// crawlWith 是 BFS 引擎的内部实现（对账 TS `crawl`）。
func crawlWith(seedURL string, fetcher CrawlFetcher, opts CrawlOptions) CrawlResult {
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = 20
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 2
	}
	budgetMs := opts.BudgetMs
	if budgetMs <= 0 {
		budgetMs = 180_000
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	minInterval := opts.MinIntervalPerHostMs
	if minInterval <= 0 {
		minInterval = 300
	}

	seed, err := url.Parse(seedURL)
	if err != nil {
		return CrawlResult{Errors: []CrawlError{{URL: seedURL, Error: "无效种子 URL"}}}
	}
	filterOpts := LinkFilterOptions{
		SeedHost:      strings.ToLower(seed.Hostname()),
		SeedPath:      seed.Path,
		SeedDepth:     getURLDepth(seed.Path),
		MaxDepth:      maxDepth,
		IncludePaths:  opts.IncludePaths,
		ExcludePaths:  opts.ExcludePaths,
		AllowBackward: opts.AllowBackward,
	}

	var mu sync.Mutex
	// throttleMu 保护 hostLastFetch——throttle 含 sleep，不能持有 mu。
	var throttleMu sync.Mutex
	visited := map[string]bool{}
	var frontier []crawlItem
	var pages []CrawlPage
	var denied []CrawlDenied
	var errs []CrawlError
	hostLastFetch := map[string]int64{}
	startedAt := time.Now()

	pushFrontier := func(rawLink string, depth int) {
		link, err := url.Parse(rawLink)
		if err != nil {
			denied = append(denied, CrawlDenied{URL: rawLink, Reason: "non_http"})
			return
		}
		key := normalizeCacheURL(rawLink)
		if visited[key] || visited[wwwVariant(key)] {
			return
		}
		visited[key] = true
		if reason := filterLink(link, filterOpts); reason != "" {
			denied = append(denied, CrawlDenied{URL: rawLink, Reason: reason})
			return
		}
		frontier = append(frontier, crawlItem{url: rawLink, depth: depth})
	}

	// 种子本身**不入过滤链**（backward 会挡住自己）。
	visited[normalizeCacheURL(seedURL)] = true
	frontier = append(frontier, crawlItem{url: seedURL, depth: 0})

	// sitemap 预收集 URL 作为 depth-1 候选（仍过过滤链）。
	for _, u := range opts.SitemapURLs {
		pushFrontier(u, 1)
	}

	throttle := func(host string) {
		now := time.Now().UnixMilli()
		last := hostLastFetch[host]
		slot := now
		if last+int64(minInterval) > slot {
			slot = last + int64(minInterval)
		}
		hostLastFetch[host] = slot // 立即占位（读-改-写在同一锁内）
		if wait := slot - now; wait > 0 {
			time.Sleep(time.Duration(wait) * time.Millisecond)
		}
	}

	// **收敛循环**（对账 TS 的 pump + 收敛判据）。
	//
	// 关键：不能「frontier 空就退出」——在途任务完成后可能**压入新候选**。
	// TS 的判据是 `inFlight === 0 && (frontier.length === 0 || 截断)`。
	// Go 版用 `wg` 跟踪在途 + 每轮 `wg.Wait()` 后重查 frontier。
	//
	// **首版 bug（本刀修复）**：用 `for { if len(frontier)==0 { break } }` +
	// 协程并发 → 主循环在种子任务完成前就看到 frontier 空而退出，
	// 只抓了 1 页。探针实测：fetcher 返回 2 个链接，但 pages=1、denied=[]。
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	// inFlight 是「已派发未完成」的计数——页数上限必须用
	// `len(pages) + inFlight < maxPages`（对账 TS 的双重条件），
	// 否则并发的在途任务各自完成时都会 push，导致超发。
	inFlight := 0

	for {
		mu.Lock()
		if len(frontier) == 0 {
			mu.Unlock()
			// 无候选——**等在途任务收尾**后重查（它们可能压入新候选）。
			wg.Wait()
			mu.Lock()
			empty := len(frontier) == 0
			mu.Unlock()
			if empty {
				break
			}
			continue
		}
		overBudget := time.Since(startedAt).Milliseconds() >= int64(budgetMs)
		if overBudget || len(pages) >= maxPages {
			mu.Unlock()
			break
		}
		// **双重条件**（对账 TS）：已产出的页 + 在途数 不能超上限。
		if len(pages)+inFlight >= maxPages {
			mu.Unlock()
			break
		}
		item := frontier[0]
		frontier = frontier[1:]
		inFlight++
		mu.Unlock()

		wg.Add(1)
		sem <- struct{}{}
		go func(it crawlItem) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				mu.Lock()
				inFlight--
				mu.Unlock()
			}()

			host := ""
			if u, err := url.Parse(it.url); err == nil {
				host = strings.ToLower(u.Hostname())
			}

			// throttle 需要独立的锁——它含 sleep，不能持有 mu（会阻塞收敛检查）。
			throttleMu.Lock()
			throttle(host)
			throttleMu.Unlock()

			outcome := fetcher(it.url)

			mu.Lock()
			defer mu.Unlock()
			if !outcome.OK {
				errs = append(errs, CrawlError{URL: it.url, Error: outcome.Error})
				return
			}
			pages = append(pages, CrawlPage{
				URL: it.url, Depth: it.depth, Status: outcome.Status,
				Markdown: outcome.Markdown, Via: outcome.Via, FromCache: outcome.FromCache,
			})
			if it.depth < maxDepth {
				for _, link := range outcome.Links {
					pushFrontier(link, it.depth+1)
				}
			}
		}(item)
	}
	wg.Wait()

	// 收敛后 frontier 可能仍有残留（因预算/页数停调）→ 标记截断。
	mu.Lock()
	truncated := len(frontier) > 0
	mu.Unlock()

	return CrawlResult{
		Pages:      pages,
		Denied:     denied,
		Errors:     errs,
		Truncated:  truncated,
		DurationMs: time.Since(startedAt).Milliseconds(),
	}
}

// crawlItem 是 frontier 的元素。
type crawlItem struct {
	url   string
	depth int
}
