package search

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// httpfetch.go —— 搜索后端的生产 HTTP 抓取（第九十七刀 · W4）。
//
// 对账 TS `src/tools/web-search/proxy-fetch.ts`（42 行）+ `bounded-fetch.ts`（54 行）。
//
// # 与 web_fetch 的抓取路径**有意不同**
//
// `internal/net/httpfetch.go` 的 `HTTPFetchGuarded` 带 **SSRF 防护 + DNS pin**——
// 那是给**用户传入的任意 URL** 用的。搜索后端的主机是**编译期固定的已知主机**
// （`html.duckduckgo.com` / `cn.bing.com` / `api.brave.com` / …），
// 不经用户输入，故不需要 SSRF 全套。
//
// **但代理要走**：TS 的 `createProxyAwareFetch` 明写「让搜索流量与 web_fetch
// 走同一个代理」。本实现接入 W1 的 `tnet.ResolveProxyForURL`。
//
// # 大小上限
//
// 对账 TS `boundedSearchFetch` 的 8MB 上限——响应先**限量读入**再返回，
// 超限即中止（不是读完再检查），避免恶意/畸形响应撑爆堆。

// HTTPFetchOptions 是生产 Fetch 的配置。
type HTTPFetchOptions struct {
	// Timeout 是单次请求超时。零值时用 15s。
	Timeout time.Duration
	// MaxBytes 是响应 body 上限。零值时用 DefaultMaxSearchBytes。
	MaxBytes int
	// Proxy 是来自 `config.network.{proxy,noProxy}` 的代理解析选项。
	Proxy *tnet.ProxyResolverOptions
}

// NewHTTPFetch 创建一个生产用 Fetch（net/http + 代理 + 大小上限）。
func NewHTTPFetch(opts HTTPFetchOptions) Fetch {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxSearchBytes
	}
	return func(ctx context.Context, req *Request) (*Response, error) {
		return doHTTPFetch(ctx, req, opts)
	}
}

func doHTTPFetch(ctx context.Context, req *Request, opts HTTPFetchOptions) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	client := buildSearchClient(req, opts)

	res, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// 限量读：**边读边计**，超限即中止（对账 TS boundedSearchFetch 的 reader 循环）。
	limited := io.LimitReader(res.Body, int64(opts.MaxBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > opts.MaxBytes {
		return nil, &BodyTooLargeError{MaxBytes: opts.MaxBytes, Actual: len(data)}
	}

	return &Response{Status: res.StatusCode, Body: data}, nil
}

// buildSearchClient 构造带代理设置的 HTTP client。
//
// 对账 TS `createProxyAwareFetch`：无代理时**零行为变化**（直连），
// 有代理时按 `resolveProxyForUrl` 的结果设置。
func buildSearchClient(req *Request, opts HTTPFetchOptions) *http.Client {
	transport := &http.Transport{
		// 代理按目标 URL 解析（对账 TS 的逐请求 resolveProxyForUrl）。
		Proxy: func(r *http.Request) (*url.URL, error) {
			proxyURL := tnet.ResolveProxyForURL(r.URL.String(), opts.Proxy)
			if proxyURL == "" {
				return nil, nil
			}
			return url.Parse(proxyURL)
		},
	}

	client := &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
	}
	// 对账 TS 的 `redirect: 'manual'`——3xx 不自动跟随，
	// 避免被弹到未校验的主机（DDG/Bing 后端注释明写）。
	if req.NoRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

// HTTPStatusError 表示后端返回了非 2xx。
//
// 文案对账 TS：各后端的 `throw new Error(\`HTTP ${response.status}\`)`。
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }
