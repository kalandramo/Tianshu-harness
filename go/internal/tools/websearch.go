package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/search"
)

// websearch.go —— `web_search` 工具（第九十七刀 · W5）。
//
// 对账 TS `src/tools/web-search/tool.ts`（151 行）。
//
// # 后端链来自配置
//
// `search.backends` 有序列表 + `resolveSearchKey` 四层回退。
// 默认 `[bing, duckduckgo]`——**零配置可用**（覆盖国内 cn.bing.com 与海外 DDG）。
//
// # 低置信兜底
//
// 全部后端都无相关结果但**有过跑题批次**时，降级返回该批次 + 显式低相关标注。
// 理由（对账 TS 注释）：单后端配置下，「后端降级返回泛结果」若变成
// 「未找到结果」，用户丢掉的是**唯一可得的信息**。标注把采信判断权交回使用者，
// 同时不让泛结果**冒充答案**。

// maxSearchResults 对账 TS `MAX_RESULTS`。
const maxSearchResults = 20

// defaultSearchTimeoutMs 对账 TS `DEFAULT_TIMEOUT_MS`。
const defaultSearchTimeoutMs = 15_000

// lowConfidenceNotice 是低置信兜底的用户可见标注。
//
// 对账 TS `LOW_CONFIDENCE_NOTICE`——**逐字对账**（用户可见文案）。
const lowConfidenceNotice = "⚠ 相关性提示：以下结果只覆盖了查询中的个别词，可能不是你要找的内容。" +
	"请勿直接采信为答案——建议换用更具体的查询词，或与其他来源交叉验证。"

// WebSearch 创建 `web_search` 工具（读全局配置）。
func WebSearch() Tool {
	cfg := search.LoadSearch()
	return WebSearchWithBackends(cfg, search.BuildBackends(cfg, search.BuildOptions{}), cfg.TimeoutMs)
}

// WebSearchWithBackends 创建带**注入后端链**的 web_search（测试与配置层用）。
func WebSearchWithBackends(cfg search.SearchConfig, backends []search.Backend, timeoutMs int) Tool {
	if timeoutMs <= 0 {
		timeoutMs = defaultSearchTimeoutMs
	}
	return &webSearchTool{backends: backends, timeoutMs: timeoutMs}
}

type webSearchTool struct {
	backends  []search.Backend
	timeoutMs int
}

func (t *webSearchTool) Definition() contract.Definition {
	return contract.Definition{
		Name:        "web_search",
		Description: webSearchDescription,
		InputSchema: objSchemaOrdered(
			[]string{"query", "count"},
			map[string]any{
				"query": strProp("搜索查询字符串"),
				"count": numProp("返回结果数量（默认：10，最大：20）。传 0 或负数按默认 10 处理。"),
			},
			"query",
		),
	}
}

// webSearchDescription 逐字对账 TS `tool.ts` 的 description。
const webSearchDescription = `搜索 Web 获取实时信息。结果包含标题、URL 和内容摘要。

### 何时搜索
- 当前/时效性事实（最新发布、breaking changes、今日状态）
- 你不认识或记不准的特定库/版本/API/报错
- 任何可能在你训练截止后发生变化的内容
- 不认识的首字母大写名称（产品、工具、包）：陌生名字更可能是训练之后才出现的真实事物，而不是可以臆测的对象——搜索，不要编造

### 何时不要搜索
- 稳定事实、语言语法，或你已掌握的成熟概念
- 本仓库已有的代码——改用 grep/read_file/semantic_search

### 使用结果（署名与版权）
- 用自己的话综合转述；非显而易见的论断要注明来源 URL
- 直接引用保持简短（约 15 词以内），每个来源最多引用一处
- 绝不要逐字复制文章段落、歌词或诗歌
- 摘要不够用时，用 web_fetch 读取完整页面`

func (t *webSearchTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawQuery, _ := p.Input["query"].(string)
	query := strings.TrimSpace(rawQuery)
	if query == "" {
		return contract.Result{Content: "错误：query 必须是非空字符串。", IsError: true}, nil
	}

	count := clampSearchCount(p.Input["count"])

	res := search.RunBackendChain(ctx, t.backends, query, count, t.timeoutMs)

	if len(res.Results) == 0 {
		return t.noResultsResult(query, res), nil
	}

	via := ""
	if res.Backend != "" {
		via = "（经 " + res.Backend + "）"
	}
	return contract.Result{
		Content: "「" + query + "」的网页搜索结果" + via + "：\n\n" + formatSearchResults(res.Results),
	}, nil
}

// noResultsResult 组装「无结果」分支。
//
// 对账 TS 的三分支：
//  1. 有**硬错误** → 报 `搜索失败（...）` isError=true
//  2. 无硬错误但**有跑题兜底** → 降级返回 + 低相关标注
//  3. 否则 → `未找到与「query」相关的搜索结果`（**不是**错误）
func (t *webSearchTool) noResultsResult(query string, res search.ChainResult) contract.Result {
	var hard []string
	for _, e := range res.Errors {
		// 对账 TS：`no results` 与 `off-topic results` 都不算硬错误——
		// 用户可见结果相同（都是没搜到可用的），报成硬错误会误导。
		if e.Message == search.NoResultsError || e.Message == search.OffTopicError {
			continue
		}
		hard = append(hard, e.Backend+": "+e.Message)
	}
	if len(hard) > 0 {
		return contract.Result{
			Content: "搜索失败（" + strings.Join(hard, "; ") + "）",
			IsError: true,
		}
	}

	if res.OffTopicFallback != nil {
		return contract.Result{
			Content: "「" + query + "」的网页搜索结果（经 " + res.OffTopicFallback.Backend + "，低相关）：\n\n" +
				lowConfidenceNotice + "\n\n" +
				formatSearchResults(res.OffTopicFallback.Results),
		}
	}

	return contract.Result{Content: "未找到与「" + query + "」相关的搜索结果"}
}

// formatSearchResults 渲染结果（正常路径与低置信兜底**共用同一份实现**，
// 避免双实现漂移——对账 TS 注释）。
func formatSearchResults(results []search.Result) string {
	var parts []string
	for i, r := range results {
		// 来源站点与发布时间（博查等后端提供）附在 snippet 后，
		// 帮助判断可信度与时效。
		var meta []string
		if r.SiteName != "" {
			meta = append(meta, r.SiteName)
		}
		if r.PublishedAt != "" {
			meta = append(meta, r.PublishedAt)
		}
		snippet := r.Snippet
		if len(meta) > 0 {
			snippet += "（" + strings.Join(meta, " · ") + "）"
		}
		parts = append(parts, fmt.Sprintf("%d. [%s](%s)\n   %s", i+1, r.Title, r.URL, snippet))
	}
	return strings.Join(parts, "\n\n")
}

// clampSearchCount 对账 TS：
//
//	Math.min(Math.max(1, lenientPositiveNumber(input.count) ?? 10), MAX_RESULTS)
//
// 即：缺省/非法 → 10；下界 1；上界 20。
func clampSearchCount(v any) int {
	n := 10
	if f, ok := v.(float64); ok && f > 0 {
		n = int(f)
	}
	if n < 1 {
		n = 1
	}
	if n > maxSearchResults {
		n = maxSearchResults
	}
	return n
}

// RequiresApproval 对账 TS `requiresApproval(): true`。
func (t *webSearchTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 对账 TS `isConcurrencySafe(): true`。
func (t *webSearchTool) ConcurrencySafe() bool { return true }

// Enabled 对账 TS `isEnabled(): true`。
func (t *webSearchTool) Enabled() bool { return true }

// Timeout 用配置的单后端超时（链内每个后端各套一次，故总时长可能为 N×该值）。
//
// **诚实标注**：`registry.go` 声明了 `Tool.Timeout`，但 `internal/agent`
// **无统一读取点**（第八十八刀审查指出）。故此返回值当前写而无人读。
func (t *webSearchTool) Timeout(_ *CallParams) time.Duration {
	return time.Duration(t.timeoutMs) * time.Millisecond
}
