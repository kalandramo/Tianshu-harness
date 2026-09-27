package tools

import (
	"context"
	"time"

	"github.com/kalandramo/tianshu/go/internal/search"
)

// webmap_search.go —— 把 `search.Backend` 接到 web_map 的第三路来源（第九十七刀 · W6）。
//
// # 背景：这是第九十六刀预留接口的兑现
//
// 第九十六刀实现 `web_map` 时，第三路来源（`site:host` 搜索）依赖尚未移植的
// `web_search` 后端链，故：
//
//   - 预留了 `WebMapSearchBackend` 接口与 `WebMapWithBackends` 构造器
//   - `WebMap(cwd)` 默认**不传后端**——`search` 参数会如实报告「（无可用后端）」
//
// 本文件把这两端接上：`search.Backend` → `WebMapSearchBackend` 适配器，
// 并让 `WebMap(cwd)` 默认从配置构造后端链。
//
// # 为什么用适配器而非直接复用
//
// `search.Backend.Search` 带 `context`（承载超时）且返回 `search.Result`
// （含 Snippet/SiteName/PublishedAt）；web_map 只需要 `Title`/`URL` 两项——
// 那是 sitemap 与页链接两路来源共用的最小形状（`MapCollector` 的入参）。

// searchBackendAdapter 把 `search.Backend` 适配成 `WebMapSearchBackend`。
type searchBackendAdapter struct {
	inner     search.Backend
	timeoutMs int
}

// Name 透传后端标识（用于 web_map 摘要里的归属标注）。
func (a *searchBackendAdapter) Name() string { return a.inner.Name() }

// Search 执行一次站点内搜索。
//
// **直接调单个后端**（不经 `RunBackendChain`）——理由：web_map 的调用方
// （`webMapTool.Execute`）已实现「链序首个返回非空结果者胜出」的逻辑
// （见 webmap.go 的 backends 循环），再套一层链会重复。
func (a *searchBackendAdapter) Search(query string, count int) ([]WebMapSearchResult, error) {
	ctx := context.Background()
	if a.timeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.timeoutMs)*time.Millisecond)
		defer cancel()
	}
	results, err := a.inner.Search(ctx, query, count)
	if err != nil {
		return nil, err
	}
	out := make([]WebMapSearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, WebMapSearchResult{Title: r.Title, URL: r.URL})
	}
	return out, nil
}

// buildMapSearchBackends 从配置构造 web_map 的搜索后端链。
//
// 返回的适配器**包含不可用的后端**（无 key 的 brave/tavily/bocha）——
// `webMapTool.Execute` 的循环会因它们返回错误/空结果而继续到下一个，
// 与 `search.RunBackendChain` 的「不可用静默跳过」语义等价。
func buildMapSearchBackends(cfg search.SearchConfig) []WebMapSearchBackend {
	inner := search.BuildBackends(cfg, search.BuildOptions{})
	out := make([]WebMapSearchBackend, 0, len(inner))
	for _, b := range inner {
		out = append(out, &searchBackendAdapter{inner: b, timeoutMs: cfg.TimeoutMs})
	}
	return out
}
