package net

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// httpfetch_test.go —— 带防护的 HTTP 抓取（第八十七刀 · W3-2）。
//
// 对账 TS `src/tools/net/http-fetch.ts`（272 行）。
//
// # 防护面（每一层都有对应测试）
//
//  1. 协议白名单（仅 http/https）
//  2. **每跳** SSRF 预检（含重定向——攻击者可用 302 绕到私网）
//  3. DNS pin（防重绑定：连到预检过的地址，而非重新解析）
//  4. 超时（连接 + body 读取）
//  5. 响应大小上限
//  6. 重定向次数上限
//
// # 测试策略
//
// 走**注入**路径（对账 TS 的 `deps.fetch` / `deps.lookup`）——这样能：
//   - 用 `httptest` 本地服务器（其地址是 127.0.0.1，会被 SSRF 拦）
//   - 注入一个返回公共地址的 lookup
//   - 不打真实外网（测试不得依赖网络）

// TestHTTPFetchConstants —— 常量逐字对账 TS。
func TestHTTPFetchConstants(t *testing.T) {
	if defaultTimeoutMs != 15_000 {
		t.Errorf("默认超时应 15000ms，实得 %d", defaultTimeoutMs)
	}
	if defaultMaxBytes != 10_485_760 {
		t.Errorf("默认大小上限应 10485760，实得 %d", defaultMaxBytes)
	}
	if defaultMaxRedirects != 5 {
		t.Errorf("默认重定向上限应 5，实得 %d", defaultMaxRedirects)
	}
	want := "Tianshu/1.0 (terminal coding agent)"
	if defaultUserAgent != want {
		t.Errorf("UA 应逐字对账 %q，实得 %q", want, defaultUserAgent)
	}
}

// TestHTTPFetchPinningFlag —— `RIVET_FETCH_PIN` 语义（对账 TS）。
func TestHTTPFetchPinningFlag(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"yes", true},
		{"0", false}, {"false", false},
	}
	for _, c := range cases {
		t.Setenv("RIVET_FETCH_PIN", c.val)
		if got := IsConnectionPinningEnabled(); got != c.want {
			t.Errorf("RIVET_FETCH_PIN=%q 应得 %v，实得 %v", c.val, c.want, got)
		}
	}
}

// TestHTTPFetchProtocolWhitelist —— 仅 http/https。
func TestHTTPFetchProtocolWhitelist(t *testing.T) {
	for _, u := range []string{
		"file:///etc/passwd", "ftp://x/y", "javascript:alert(1)", "data:text/html,x",
	} {
		_, err := HTTPFetchGuarded(context.Background(), u, Deps{Lookup: publicLookup}, Options{})
		if err == nil {
			t.Errorf("%s 应被拒（协议不在白名单）", u)
			continue
		}
		if !strings.Contains(err.Error(), "Unsupported protocol") {
			t.Errorf("%s 的错误应含 Unsupported protocol，实得 %v", u, err)
		}
	}
}

// TestHTTPFetchInvalidURL —— 非法 URL 报错。
func TestHTTPFetchInvalidURL(t *testing.T) {
	_, err := HTTPFetchGuarded(context.Background(), "http://[::1", Deps{Lookup: publicLookup}, Options{})
	if err == nil || !strings.Contains(err.Error(), "Invalid URL") {
		t.Errorf("非法 URL 应报错，实得 %v", err)
	}
}

// TestHTTPFetchSSRFPrecheck —— 解析到私有地址时拒绝。
//
// 这是与 SSRF 层的接缝——证明 precheck **真的被调用**。
func TestHTTPFetchSSRFPrecheck(t *testing.T) {
	privateLookup := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "169.254.169.254", Family: 4}, nil
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://metadata.example/x", Deps{Lookup: privateLookup}, Options{})
	if err == nil {
		t.Fatal("解析到私网地址应拒绝")
	}
	var ssrfErr *SSRFError
	if !errors.As(err, &ssrfErr) {
		t.Fatalf("应为 *SSRFError，实得 %T: %v", err, err)
	}
}

// TestHTTPFetchSuccess —— 正常抓取（注入 Doer）。
func TestHTTPFetchSuccess(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/html; charset=utf-8", "hello body"),
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/page", deps, Options{})
	if err != nil {
		t.Fatalf("应成功：%v", err)
	}
	if res.Status != 200 {
		t.Errorf("status 应 200，实得 %d", res.Status)
	}
	if string(res.Bytes) != "hello body" {
		t.Errorf("body 不符，实得 %q", res.Bytes)
	}
	if res.ContentType != "text/html; charset=utf-8" {
		t.Errorf("contentType 不符，实得 %q", res.ContentType)
	}
	if res.FinalURL != "http://example.com/page" {
		t.Errorf("finalUrl 不符，实得 %q", res.FinalURL)
	}
}

// TestHTTPFetchUserAgent —— UA 头被设置（可覆盖）。
func TestHTTPFetchUserAgent(t *testing.T) {
	var sawUA string
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			sawUA = req.Header.Get("User-Agent")
			return cannedResponse(200, "", "ok"), nil
		},
	}
	_, _ = HTTPFetchGuarded(context.Background(), "http://example.com/", deps, Options{})
	if sawUA != defaultUserAgent {
		t.Errorf("默认 UA 应 %q，实得 %q", defaultUserAgent, sawUA)
	}

	_, _ = HTTPFetchGuarded(context.Background(), "http://example.com/", deps, Options{UserAgent: "Custom/2.0"})
	if sawUA != "Custom/2.0" {
		t.Errorf("自定义 UA 应生效，实得 %q", sawUA)
	}
}

// TestHTTPFetchFollowsRedirect —— 跟随重定向（manual 模式下逐跳处理）。
func TestHTTPFetchFollowsRedirect(t *testing.T) {
	var hits []string
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			hits = append(hits, req.URL.String())
			switch req.URL.Path {
			case "/start":
				return redirectResponse(302, "http://example.com/mid"), nil
			case "/mid":
				return redirectResponse(301, "http://example.com/end"), nil
			default:
				return cannedResponse(200, "text/plain", "final"), nil
			}
		},
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/start", deps, Options{})
	if err != nil {
		t.Fatalf("应成功：%v", err)
	}
	if len(hits) != 3 {
		t.Errorf("应请求 3 跳，实得 %d：%v", len(hits), hits)
	}
	if res.FinalURL != "http://example.com/end" {
		t.Errorf("finalUrl 应为终点，实得 %q", res.FinalURL)
	}
	if string(res.Bytes) != "final" {
		t.Errorf("应返回终点内容，实得 %q", res.Bytes)
	}
}

// TestHTTPFetchSSRFOnRedirect —— **重定向也不能绕 SSRF**。
//
// 这是关键的绕过路径：攻击者用 302 把请求引到私网。
func TestHTTPFetchSSRFOnRedirect(t *testing.T) {
	// 第一次解析给公共地址，重定向目标解析给私网
	var calls int
	lookup := func(host string) (ResolvedAddress, error) {
		calls++
		if host == "evil.example" {
			return ResolvedAddress{Address: "169.254.169.254", Family: 4}, nil
		}
		return ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
	}
	deps := Deps{
		Lookup: lookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return redirectResponse(302, "http://evil.example/steal"), nil
		},
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://good.example/", deps, Options{})
	if err == nil {
		t.Fatal("重定向到私网应被拒")
	}
	var ssrfErr *SSRFError
	if !errors.As(err, &ssrfErr) {
		t.Fatalf("应为 *SSRFError，实得 %T: %v", err, err)
	}
	if ssrfErr.Hostname != "evil.example" {
		t.Errorf("错误应指向 evil.example，实得 %q", ssrfErr.Hostname)
	}
	if calls < 2 {
		t.Errorf("应对每跳各解析一次（实得 %d 次）", calls)
	}
}

// TestHTTPFetchTooManyRedirects —— 超重定向上限。
func TestHTTPFetchTooManyRedirects(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return redirectResponse(302, "http://example.com/loop"), nil
		},
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/loop", deps, Options{MaxRedirects: 3})
	if err == nil || !strings.Contains(err.Error(), "Too many redirects") {
		t.Errorf("应报 Too many redirects，实得 %v", err)
	}
}

// TestHTTPFetchRedirectWithoutLocation —— 3xx 无 Location 头 → 报错。
func TestHTTPFetchRedirectWithoutLocation(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return redirectResponse(302, ""), nil
		},
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/", deps, Options{})
	if err == nil || !strings.Contains(err.Error(), "no Location header") {
		t.Errorf("应报无 Location，实得 %v", err)
	}
}

// TestHTTPFetchRedirectProtocolDowngrade —— 重定向到非 http(s) 协议 → 拒绝。
func TestHTTPFetchRedirectProtocolDowngrade(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			return redirectResponse(302, "file:///etc/passwd"), nil
		},
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/", deps, Options{})
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol") {
		t.Errorf("应拒绝非 http(s) 重定向，实得 %v", err)
	}
}

// TestHTTPFetchMaxBytes —— 响应超上限 → 报错（**不静默截断**）。
//
// 对账 TS：`Response body exceeds maximum allowed size`。
func TestHTTPFetchMaxBytes(t *testing.T) {
	big := strings.Repeat("x", 2000)
	deps := Deps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/plain", big),
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/big", deps, Options{MaxResponseBytes: 1000})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Errorf("超限应报错，实得 %v", err)
	}
}

// TestHTTPFetchMaxBytesBoundary —— 恰好等于上限**不**报错。
func TestHTTPFetchMaxBytesBoundary(t *testing.T) {
	exact := strings.Repeat("y", 1000)
	deps := Deps{
		Lookup: publicLookup,
		Doer:   cannedDoer(200, "text/plain", exact),
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/exact", deps, Options{MaxResponseBytes: 1000})
	if err != nil {
		t.Fatalf("恰好等于上限不应报错：%v", err)
	}
	if len(res.Bytes) != 1000 {
		t.Errorf("应读到 1000 字节，实得 %d", len(res.Bytes))
	}
}

// TestHTTPFetchTimeout —— 超时报错。
func TestHTTPFetchTimeout(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(5 * time.Second):
				return cannedResponse(200, "", "late"), nil
			}
		},
	}
	start := time.Now()
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/slow", deps, Options{TimeoutMs: 100})
	if err == nil {
		t.Fatal("应超时报错")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("应在超时值附近返回，实得耗时 %v", elapsed)
	}
}

// TestHTTPFetchEmptyBody —— 空 body 不报错。
func TestHTTPFetchEmptyBody(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer:   cannedDoer(204, "", ""),
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/none", deps, Options{})
	if err != nil {
		t.Fatalf("空 body 不应报错：%v", err)
	}
	if len(res.Bytes) != 0 {
		t.Errorf("body 应为空，实得 %q", res.Bytes)
	}
}

// TestHTTPFetchLookupError —— DNS 解析失败时错误透传。
func TestHTTPFetchLookupError(t *testing.T) {
	failing := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{}, fmt.Errorf("no such host")
	}
	_, err := HTTPFetchGuarded(context.Background(), "http://nope.invalid/", Deps{Lookup: failing}, Options{})
	if err == nil || !strings.Contains(err.Error(), "no such host") {
		t.Errorf("应透传解析错误，实得 %v", err)
	}
}

// TestHTTPFetchContextCancel —— 外部 context 取消能中断。
func TestHTTPFetchContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			if err := req.Context().Err(); err != nil {
				return nil, err
			}
			return cannedResponse(200, "", "should not reach"), nil
		},
	}
	_, err := HTTPFetchGuarded(ctx, "http://example.com/", deps, Options{})
	if err == nil {
		t.Error("已取消的 context 应导致错误")
	}
}

// ── 测试辅助 ────────────────────────────────────────────────────────────

// publicLookup 返回一个公共地址（让 SSRF 预检通过）。
func publicLookup(host string) (ResolvedAddress, error) {
	return ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
}

// cannedDoer 返回固定响应的 Doer。
func cannedDoer(status int, contentType, body string) Doer {
	return func(req *http.Request) (*http.Response, error) {
		return cannedResponse(status, contentType, body), nil
	}
}

// cannedResponse 构造一个 http.Response。
func cannedResponse(status int, contentType, body string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// redirectResponse 构造一个 3xx 响应。
func redirectResponse(status int, location string) *http.Response {
	h := http.Header{}
	if location != "" {
		h.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString("")),
	}
}

// TestHTTPFetchRedirectLimitPrecise —— **精确验证重定向上限**。
//
// # 为什么补这条（变异反证暴露的测试弱点）
//
// M4 变异（`hop <= maxRedirects` 改成 `hop <= maxRedirects+100`）**没让测试变红**——
// 因为原测试用的 doer 是**无限重定向**，上限 3 与 103 都最终报
// "Too many redirects"。测试无法区分「上限被遵守」与「上限形同虚设」。
//
// 本测试让 doer **在第 N 次返回 200**，从而精确锁定边界。
//
// 对账 TS 语义：`for (let hop = 0; hop <= maxRedirects; hop++)`——
// maxRedirects=2 时允许 **3 次请求**（hop 0/1/2），第 4 次前退出。
func TestHTTPFetchRedirectLimitPrecise(t *testing.T) {
	newDoer := func(finalAt int) (Deps, *int) {
		count := 0
		return Deps{
			Lookup: publicLookup,
			Doer: func(req *http.Request) (*http.Response, error) {
				count++
				if count >= finalAt {
					return cannedResponse(200, "text/plain", "done"), nil
				}
				return redirectResponse(302, "http://example.com/next"), nil
			},
		}, &count
	}

	// ① 恰好在上限内到达终点（maxRedirects=2 → 允许 3 次请求）
	deps, count := newDoer(3)
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/a", deps, Options{MaxRedirects: 2})
	if err != nil {
		t.Fatalf("第 3 次请求成功应通过（上限内）：%v", err)
	}
	if *count != 3 {
		t.Errorf("应恰好请求 3 次，实得 %d", *count)
	}
	if string(res.Bytes) != "done" {
		t.Errorf("应返回终点内容，实得 %q", res.Bytes)
	}

	// ② 超上限（maxRedirects=2，终点在第 4 次）→ 应报错
	deps, count = newDoer(4)
	_, err = HTTPFetchGuarded(context.Background(), "http://example.com/a", deps, Options{MaxRedirects: 2})
	if err == nil {
		t.Fatal("超上限应报错")
	}
	if !strings.Contains(err.Error(), "Too many redirects (>2)") {
		t.Errorf("错误应含上限值 2，实得 %v", err)
	}
	// **关键**：请求次数应被上限钳住（3 次 = hop 0/1/2），而非无限
	if *count != 3 {
		t.Errorf("请求次数应被上限钳为 3，实得 %d", *count)
	}
}
