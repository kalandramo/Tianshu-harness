// Package search —— web_search 的后端抽象与后端链（第九十七刀 · W3）。
//
// 对账 TS `src/tools/web-search/*.ts`（1085 行 / 12 文件）。
//
// # 分层
//
//   - `types.go`     —— `SearchBackend` 接口 + `SearchResult`
//   - `relevance.go` —— 跑题/降级守卫（词覆盖判据）
//   - `chain.go`     —— 有序后端链（首个可用且非空者胜出）
//   - `fetch.go`     —— body 大小上限包装
//
// # 设计要点（对账 TS 注释）
//
// **后端链的语义**：按配置顺序试，首个 `IsAvailable()` 且返回**可用非空**结果的
// 胜出并短路。不可用（缺 key）**静默跳过**，不算错误；空结果、跑题结果、抛错
// 都记入 errors 后继续走下一个。
//
// **「跑题」是静默出错的那一类**：HTTP 200、结构完好的一大块解析结果、但没有
// 一条与查询相关。它必须像空结果一样**继续走链**——否则链会把无关内容当作答案
// 交给模型。链序第一块跑题批次记在 `OffTopicFallback`，供调用方在全部后端都
// 无相关结果时**降级标注**（而非直接丢弃）。
package search

import "context"

// Result 对账 TS `SearchResult`。
type Result struct {
	Title   string
	URL     string
	Snippet string
	// SiteName 是来源站点名（如「知乎」「GitHub」），便于模型判断来源可信度。
	// **可选**——仅博查等返回 siteName 的后端填充；bing/DDG 抓取不提供。
	SiteName string
	// PublishedAt 是发布时间（ISO 字符串），便于时效性判断。**可选**。
	PublishedAt string
}

// Fetch 是可注入的 HTTP 抓取（生产用真实 client；测试传桩）。
//
// 对账 TS `SearchFetch`。Go 侧用 `context.Context` 承载超时/取消
// （TS 用 `AbortSignal`——两者语义对应）。
type Fetch func(ctx context.Context, req *Request) (*Response, error)

// Request 是一次搜索请求的形态。
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	// NoRedirect 对账 TS 的 `redirect: 'manual'`——3xx 不自动跟随，
	// 避免被弹到未校验的主机（对账 DDG/Bing 后端的注释）。
	NoRedirect bool
}

// Response 是搜索响应（已读入内存的 body + 状态）。
type Response struct {
	Status int
	Body   []byte
}

// OK 报告 HTTP 2xx（对账 TS `response.ok`）。
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Backend 是一个具体的搜索提供方（DDG 抓取 / Brave API / …）。
//
// 对账 TS `SearchBackend`。
type Backend interface {
	// Name 是稳定标识——用于配置的 backends 列表与结果归属标注。
	Name() string
	// IsAvailable 在缺少必需凭据时为 false——链会静默跳过。
	IsAvailable() bool
	// Search 执行一次搜索。**网络/HTTP 失败应返回 error**（链记录并落空）。
	// 查询确实无结果时返回空切片。ctx 承载链传入的**单后端超时**。
	Search(ctx context.Context, query string, count int) ([]Result, error)
}

// OffTopicError 是链用来标记「结果跑题/降级」的软失败文案。
//
// 对账 TS `OFF_TOPIC_ERROR`——上层据此把这类软失败与硬错误区分开
// （软失败不该报成「搜索失败」）。
const OffTopicError = "off-topic results"

// NoResultsError 是「该后端确实没结果」的标记文案。
//
// 对账 TS 字面量 `'no results'`。
const NoResultsError = "no results"
