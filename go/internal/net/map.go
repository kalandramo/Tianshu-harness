package net

import (
	"math"
	"net/url"
	"sort"
	"strings"
)

// map.go —— web_map 的 URL 发现逻辑（第九十六刀 · W3-5c）。
//
// 对账 TS `src/tools/web-crawl/map.ts`（85 行）。
//
// # 三路来源汇合
//
//  1. sitemap 阶梯（`CollectSitemapURLs`）
//  2. 种子页链接（`FetchMarkdown` 的 Links）
//  3. `site:host` 搜索（search 参数存在时，复用 web_search 后端链）
//
// 过滤：同域 → 可选子域 → 按路径前缀 → 去重。
// search 存在时**纯词频 cosine 重排**（无 embedding，firecrawl map-cosine 同款）。

// MapCandidate 对账 TS `MapCandidate`。
type MapCandidate struct {
	URL     string
	Title   string
	Sources map[string]bool // 'sitemap' | 'page' | 'search'
}

// MapCollector 对账 TS `MapCollector` 类。
type MapCollector struct {
	candidates map[string]*MapCandidate
	order      []string
}

// NewMapCollector 创建收集器。
func NewMapCollector() *MapCollector {
	return &MapCollector{candidates: map[string]*MapCandidate{}}
}

// Add 对账 TS `MapCollector.add`。
//
// **已有条目**：合并 sources 集合；title 仅在**原先为空**时补齐（不覆盖）。
func (c *MapCollector) Add(rawURL, source, title string) {
	if existing, ok := c.candidates[rawURL]; ok {
		existing.Sources[source] = true
		if title != "" && existing.Title == "" {
			existing.Title = title
		}
		return
	}
	c.candidates[rawURL] = &MapCandidate{
		URL:     rawURL,
		Title:   title,
		Sources: map[string]bool{source: true},
	}
	c.order = append(c.order, rawURL)
}

// List 对账 TS `MapCollector.list`——**保持插入序**（对账 Map 的迭代序）。
func (c *MapCollector) List() []MapCandidate {
	out := make([]MapCandidate, 0, len(c.order))
	for _, u := range c.order {
		out = append(out, *c.candidates[u])
	}
	return out
}

// IsSameOrSubDomain 对账 TS `isSameOrSubDomain`。
func IsSameOrSubDomain(hostname, seedHost string, includeSubdomains bool) bool {
	if hostname == seedHost {
		return true
	}
	return includeSubdomains && strings.HasSuffix(hostname, "."+seedHost)
}

// FilterByPathPrefix 对账 TS `filterByPathPrefix`。
//
// 种子含非根 path 时只保留该前缀下的 URL。
func FilterByPathPrefix(rawURL, seedPath string) bool {
	if seedPath == "" || seedPath == "/" {
		return true
	}
	dir := seedPath
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Path, dir)
}

// ── 词频 cosine（对账 TS `map-cosine` 同款：无 embedding，URL 字符串词频）──

// tokenize 对账 TS `tokenize`——小写切分，长度 < 2 的 token 丢弃。
//
// **切分字符集**：非 `[a-z0-9\u4e00-\u9fff]` 即分隔符（对账 TS 正则）。
func tokenize(text string) map[string]int {
	counts := map[string]int{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), isTokenSep) {
		if utf8Len(tok) < 2 {
			continue
		}
		counts[tok]++
	}
	return counts
}

// isTokenSep 判定分隔符（对账 TS `/[^a-z0-9\u4e00-\u9fff]+/i` 的补集判定）。
func isTokenSep(r rune) bool {
	if r >= 'a' && r <= 'z' {
		return false
	}
	if r >= '0' && r <= '9' {
		return false
	}
	// CJK 统一表意文字（对账 TS 的 `\u4e00-\u9fff`）
	if r >= 0x4e00 && r <= 0x9fff {
		return false
	}
	return true
}

// utf8Len 返回 rune 数（对账 JS 的 `token.length`——JS 按 UTF-16 unit，
// 但 token 长度阈值为 2，BMP 内字符两者一致）。
func utf8Len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// cosineSim 对账 TS `cosineSim`。
func cosineSim(a, b map[string]int) float64 {
	var dot, normA, normB float64
	for _, v := range a {
		normA += float64(v) * float64(v)
	}
	for _, v := range b {
		normB += float64(v) * float64(v)
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	for k, v := range a {
		if bv, ok := b[k]; ok {
			dot += float64(v) * float64(bv)
		}
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// RerankByCosine 对账 TS `rerankByCosine`——按词频 cosine 降序重排。
//
// **稳定排序**：零分项排后且**保持原相对顺序**（对账 TS 注释）。
func RerankByCosine(items []MapCandidate, search string) []MapCandidate {
	queryVec := tokenize(search)
	type scored struct {
		item  MapCandidate
		score float64
		idx   int
	}
	scoredItems := make([]scored, len(items))
	for i, it := range items {
		scoredItems[i] = scored{item: it, score: cosineSim(tokenize(it.URL), queryVec), idx: i}
	}
	sort.SliceStable(scoredItems, func(i, j int) bool {
		return scoredItems[i].score > scoredItems[j].score
	})
	out := make([]MapCandidate, len(scoredItems))
	for i, s := range scoredItems {
		out[i] = s.item
	}
	return out
}
