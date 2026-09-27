package search

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// api.go —— 三个 API 型后端（第九十七刀 · W4）。
//
// 对账 TS `brave.ts`（53 行）+ `tavily.ts`（50 行）+ `bocha.ts`（86 行）。
//
// # 共同契约
//
// 三者都需要 API key：未配置时 `IsAvailable()` 返回 false，链**静默跳过**。
// 三者都在非 2xx 时返回 `HTTPStatusError`（链记录后落空）。
//
// # 与抓取型后端的差异
//
// 结果是**结构化 JSON**（不是 HTML 解析）——故不需要实体解码/标签剥离，
// 只需字段映射与截断到 count。

// ── Brave ───────────────────────────────────────────────────────────────

// braveEndpoint 对账 TS：`https://api.search.brave.com/res/v1/web/search`
const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

type braveResponse struct {
	Web *struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

// BraveBackend 是 Brave Search API 后端（需订阅 token）。
//
// 对账 TS `BraveBackend`。仅当 key 从配置解析到时可用。
type BraveBackend struct {
	fetch  Fetch
	apiKey string
	region string
}

// NewBraveBackend 创建 Brave 后端。region 为空时不传 `country` 参数。
func NewBraveBackend(fetch Fetch, apiKey, region string) *BraveBackend {
	return &BraveBackend{fetch: fetch, apiKey: apiKey, region: region}
}

func (b *BraveBackend) Name() string      { return "brave" }
func (b *BraveBackend) IsAvailable() bool { return b.apiKey != "" }

func (b *BraveBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("count", strconv.Itoa(count))
	if b.region != "" {
		params.Set("country", b.region)
	}
	req := &Request{
		URL: braveEndpoint + "?" + params.Encode(),
		Headers: map[string]string{
			"Accept":               "application/json",
			"Accept-Encoding":      "gzip",
			"X-Subscription-Token": b.apiKey,
		},
	}
	res, err := b.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &HTTPStatusError{Status: res.Status}
	}
	var data braveResponse
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	results := []Result{}
	if data.Web == nil {
		return results, nil
	}
	for _, r := range data.Web.Results {
		// 对账 TS：url 或 title 缺任一跳过
		if r.URL == "" || r.Title == "" {
			continue
		}
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Description})
		if len(results) >= count {
			break
		}
	}
	return results, nil
}

// ── Tavily ──────────────────────────────────────────────────────────────

// tavilyEndpoint 对账 TS：`https://api.tavily.com/search`
const tavilyEndpoint = "https://api.tavily.com/search"

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// TavilyBackend 是 Tavily Search API 后端（需 API key）。
type TavilyBackend struct {
	fetch  Fetch
	apiKey string
}

// NewTavilyBackend 创建 Tavily 后端。
func NewTavilyBackend(fetch Fetch, apiKey string) *TavilyBackend {
	return &TavilyBackend{fetch: fetch, apiKey: apiKey}
}

func (b *TavilyBackend) Name() string      { return "tavily" }
func (b *TavilyBackend) IsAvailable() bool { return b.apiKey != "" }

func (b *TavilyBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "max_results": count})
	if err != nil {
		return nil, err
	}
	req := &Request{
		Method: "POST",
		URL:    tavilyEndpoint,
		Headers: map[string]string{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + b.apiKey,
		},
		Body: payload,
	}
	res, err := b.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &HTTPStatusError{Status: res.Status}
	}
	var data tavilyResponse
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	results := []Result{}
	for _, r := range data.Results {
		if r.URL == "" || r.Title == "" {
			continue
		}
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
		if len(results) >= count {
			break
		}
	}
	return results, nil
}

// ── 博查（Bocha）────────────────────────────────────────────────────────

// bochaEndpoint 对账 TS：`https://api.bochaai.com/v1/web-search`
const bochaEndpoint = "https://api.bochaai.com/v1/web-search"

// bochaResponse 对账 TS `BochaResponse`。
//
// 字段结构来自 InternLM/lagent 的 BochaBrowser 实现（经 MindSearch 实测）。
type bochaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		WebPages *struct {
			Value []struct {
				Name          string `json:"name"`
				URL           string `json:"url"`
				Snippet       string `json:"snippet"`
				Summary       string `json:"summary"`
				SiteName      string `json:"siteName"`
				DatePublished string `json:"datePublished"`
			} `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

// BochaBackend 是博查（Bocha）Web Search API 后端——国内直连的 AI 搜索。
//
// 对账 TS `BochaBackend`：`api.bochaai.com` 国内直连可达，是 Tavily 在国内
// AI agent 圈的标准替代。需 API key（有免费额度），未配置时链自动跳过。
//
// `summary: true` 请求博查为每条结果生成 AI 摘要——比裸 snippet 信息密度更高。
// 响应 `summary` 字段**优先于** `snippet`。
type BochaBackend struct {
	fetch  Fetch
	apiKey string
}

// NewBochaBackend 创建博查后端。
func NewBochaBackend(fetch Fetch, apiKey string) *BochaBackend {
	return &BochaBackend{fetch: fetch, apiKey: apiKey}
}

func (b *BochaBackend) Name() string      { return "bocha" }
func (b *BochaBackend) IsAvailable() bool { return b.apiKey != "" }

func (b *BochaBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{
		"query": query, "count": count, "summary": true,
	})
	if err != nil {
		return nil, err
	}
	req := &Request{
		Method: "POST",
		URL:    bochaEndpoint,
		Headers: map[string]string{
			"Accept":        "application/json",
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + b.apiKey,
		},
		Body: payload,
	}
	res, err := b.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &HTTPStatusError{Status: res.Status}
	}
	var data bochaResponse
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	// **博查业务错误**（如 key 无效）HTTP 可能仍 200，靠 code/msg 兜底判败。
	if data.Code != 0 && data.Code != 200 {
		return nil, &BochaError{Code: data.Code, Msg: data.Msg}
	}
	results := []Result{}
	if data.Data == nil || data.Data.WebPages == nil {
		return results, nil
	}
	for _, r := range data.Data.WebPages.Value {
		if r.URL == "" || r.Name == "" {
			continue
		}
		// summary（AI 摘要）优先，缺省回退 snippet
		snippet := r.Summary
		if snippet == "" {
			snippet = r.Snippet
		}
		result := Result{Title: r.Name, URL: r.URL, Snippet: snippet}
		// siteName / datePublished 让模型判断来源可信度与时效（博查独有，可选）
		result.SiteName = r.SiteName
		result.PublishedAt = r.DatePublished
		results = append(results, result)
		if len(results) >= count {
			break
		}
	}
	return results, nil
}

// BochaError 表示博查的业务层错误（HTTP 200 但 code 非 0/200）。
//
// 文案对账 TS：“ `bocha ${code}: ${msg ?? 'unknown error'}` “。
type BochaError struct {
	Code int
	Msg  string
}

func (e *BochaError) Error() string {
	msg := e.Msg
	if msg == "" {
		msg = "unknown error"
	}
	return "bocha " + itoa(e.Code) + ": " + msg
}
