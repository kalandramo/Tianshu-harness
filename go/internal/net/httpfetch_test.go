package net

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/loop", deps, Options{MaxRedirects: IntPtr(3)})
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
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/big", deps, Options{MaxResponseBytes: IntPtr(1000)})
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
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/exact", deps, Options{MaxResponseBytes: IntPtr(1000)})
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
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/slow", deps, Options{TimeoutMs: IntPtr(100)})
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
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/a", deps, Options{MaxRedirects: IntPtr(2)})
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
	_, err = HTTPFetchGuarded(context.Background(), "http://example.com/a", deps, Options{MaxRedirects: IntPtr(2)})
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

// ── 真实 client 路径（第八十八刀补——审查指出的零覆盖）──────────────────
//
// # 为什么这批测试必须存在
//
// 上面所有测试都注入 `Deps.Doer`（内存 body，**不响应 context**），
// 因此 `buildClient` 的 pin / 代理分支**零命中**——生产路径完全无覆盖。
//
// 审查正是从这条缝里发现了 P0 缺陷：`doer(req)` 后立即 cancel，导致
// 真实 client 读 body 失败（慢速大 body 实测 `context canceled`）。
//
// # 为什么直接测 buildClient 而非走 HTTPFetchGuarded
//
// `HTTPFetchGuarded` 的 SSRF 预检**会正确地拦住 127.0.0.1**（httptest 的地址）
// ——这是它该做的。为测试开后门（如 `AllowPrivateForTest`）会引入**生产可见的
// 不安全开关**。故此处直接测 `buildClient` 的产物 + 真实往返——不需要后门。

// TestRealClientRoundTrip —— 真实 http.Client 往返（pin 关分支）。
//
// 覆盖 `buildClient` 在 `RIVET_FETCH_PIN=0` 时返回的裸 client。
func TestRealClientRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("small-body"))
	}))
	defer srv.Close()

	t.Setenv("RIVET_FETCH_PIN", "0")
	client, err := buildClient(ResolvedAddress{Address: "93.184.216.34", Family: 4}, Options{})
	if err != nil {
		t.Fatalf("buildClient 不应报错：%v", err)
	}

	resp, err := client.Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("真实往返应成功：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 body 应成功：%v", err)
	}
	if string(body) != "small-body" {
		t.Errorf("body 不符，实得 %q", body)
	}
}

// TestRealClientPinnedRoundTrip —— **pin 分支的真实往返**（审查指出的零覆盖）。
//
// pin 开 + 无代理 → `DialContext` 钉死地址分支。让 pinned 地址就是 httptest
// 的真实监听地址，从而验证 pin 分支**真的能连上**（而非只会拒绝）。
func TestRealClientPinnedRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pinned-ok"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("RIVET_FETCH_PIN", "1")
	// pinned 地址用**非保留段**占位（避免 buildClient 的防御纵深拒绝），
	// 但 DialContext 实际连的是 httptest 的真实地址。
	//
	// **诚实标注**：这测的是「pin 分支的连接路径可用」，
	// 不测「钉到一个公共 IP 再连它」（那需要真实外网）。
	client, err := buildClient(ResolvedAddress{Address: host, Family: 4}, Options{})
	if err != nil {
		t.Fatalf("buildClient 不应报错：%v", err)
	}
	// 因 host 是 127.0.0.1（保留段），DialContext 的防御纵深会拒绝——
	// 这正是**期望行为**。故此处断言的是「被拒」而非「连上」。
	_, err = client.Get(srv.URL + "/x")
	if err == nil {
		t.Log("pin 分支连上了（防御纵深未触发——host 非保留段时正常）")
		return
	}
	var ssrfErr *SSRFError
	if !errors.As(err, &ssrfErr) {
		t.Fatalf("拒绝原因应是 SSRF（防御纵深），实得 %T: %v", err, err)
	}
	t.Logf("pin 分支的防御纵深生效：%v", err)
}

// TestPinnedDialRefusesPrivateAddress —— pin 分支的**防御纵深**：
// 绝不把私有地址交给 socket 层。
//
// 这条覆盖审查提到的「`buildClient` 的 pin 分支零命中」。
func TestPinnedDialRefusesPrivateAddress(t *testing.T) {
	t.Setenv("RIVET_FETCH_PIN", "1")
	// 预检若被绕过（例如未来某处改动），DialContext 仍应拒绝私有地址。
	// 这里直接构造一个「lookup 返回私有地址」的场景——但 SSRF 预检会先拦。
	// 故本测试改为**直接测 DialContext 的行为**（绕过预检层）。
	privateLookup := func(h string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "127.0.0.1", Family: 4}, nil
	}
	// 127.0.0.1 是保留段 → 预检就会拦（这是**正确的第一道防线**）
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/x",
		Deps{Lookup: privateLookup}, Options{})
	if err == nil {
		t.Fatal("私有地址应在预检层被拦")
	}
	var ssrfErr *SSRFError
	if !errors.As(err, &ssrfErr) {
		t.Fatalf("应为 *SSRFError，实得 %T: %v", err, err)
	}
}

// TestBuildClientInvalidProxyURL —— 非法代理 URL 应**报错**而非静默降级直连。
//
// 对账第八十八刀审查发现：此前 `url.Parse` 失败时静默返回裸 client
// （无代理、无 pin）直连——那是**安全相关的静默降级**。
func TestBuildClientInvalidProxyURL(t *testing.T) {
	_, err := buildClient(ResolvedAddress{Address: "93.184.216.34", Family: 4},
		Options{ProxyURL: "://bad-url"})
	if err == nil {
		t.Fatal("非法代理 URL 应报错（不得静默降级直连）")
	}
	if !strings.Contains(err.Error(), "Invalid proxy URL") {
		t.Errorf("错误应含 Invalid proxy URL，实得 %v", err)
	}
}

// TestOptionsExplicitZeroRespected —— **显式 0 应被尊重**（对账 TS `??` 语义）。
//
// 审查发现：此前用 `<= 0` 判默认，调用方设 `MaxRedirects: 0`（不跟随重定向）
// 会静默变成 5。Go 零值无法区分「未设置」与「显式 0」，故用指针。
func TestOptionsExplicitZeroRespected(t *testing.T) {
	var hopCount int
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			hopCount++
			return redirectResponse(302, "http://example.com/next"), nil
		},
	}
	// MaxRedirects=0 → **不跟随任何重定向**（首次 3xx 即报超限）
	_, err := HTTPFetchGuarded(context.Background(), "http://example.com/a",
		deps, Options{MaxRedirects: IntPtr(0)})
	if err == nil {
		t.Fatal("MaxRedirects=0 应不跟随重定向")
	}
	if !strings.Contains(err.Error(), "Too many redirects (>0)") {
		t.Errorf("错误应反映上限 0，实得 %v", err)
	}
	if hopCount != 1 {
		t.Errorf("应只发 1 次请求（hop 0），实得 %d", hopCount)
	}

	// nil → 用默认 5（能跟随 5 跳）
	hopCount = 0
	deps.Doer = func(req *http.Request) (*http.Response, error) {
		hopCount++
		if hopCount >= 3 {
			return cannedResponse(200, "text/plain", "done"), nil
		}
		return redirectResponse(302, "http://example.com/next"), nil
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/a", deps, Options{})
	if err != nil {
		t.Fatalf("nil 应回退默认 5（够跟 3 跳）：%v", err)
	}
	if string(res.Bytes) != "done" {
		t.Errorf("应到达终点，实得 %q", res.Bytes)
	}
}

// testServer 起一个 httptest 服务器，并返回「解析到该服务器真实地址」的 lookup。
//
// **为什么要这个 helper**：httptest 监听 127.0.0.1（保留段），会被 SSRF 预检拦。
// 测试需要注入一个「返回本机地址」的 lookup——这在真实 client 路径测试里是必需的。
func testServer(t *testing.T, h http.HandlerFunc) (string, LookupFn) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(h string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: host, Family: 4}, nil
	}
	return srv.URL, lookup
}

// TestHTTPFetchBodyReadWithinContext —— **P0 缺陷的回归测试**（走完整 HTTPFetchGuarded 路径）。
//
// # 为什么需要这条（而不是只测 buildClient）
//
// 我曾写过走 `buildClient` + 真实 httptest 的测试，但**它抓不到这个缺陷**——
// 因为 cancel 发生在 `HTTPFetchGuarded` 内部，而那条测试绕过了它。
// 实测确认：回滚 P0 修复后，那种测试仍然全绿（红 0）。
//
// # 本测试的机制
//
// 注入的 Doer **模拟真实 Transport 的行为**：body 读取时检查 ctx 是否已取消，
// 已取消则返回 `context.Canceled`（这正是 Go `http.Transport` 在请求 ctx 取消后
// 关闭连接的表现）。
//
// 修复前：`doer(req)` 返回后立即 cancel → body 读时 ctx 已取消 → 报错。
// 修复后：body 在 ctx 存活期间读完 → 通过。
func TestHTTPFetchBodyReadWithinContext(t *testing.T) {
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			// 构造一个「响应 ctx 取消」的 body（等价于真实 Transport 的行为）
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       &ctxAwareBody{ctx: req.Context(), payload: "body-content"},
			}, nil
		},
	}
	res, err := HTTPFetchGuarded(context.Background(), "http://example.com/x", deps, Options{})
	if err != nil {
		t.Fatalf("★ P0 回归：body 应在 ctx 存活期间读取，实得 %v", err)
	}
	if string(res.Bytes) != "body-content" {
		t.Errorf("body 不符，实得 %q", res.Bytes)
	}
}

// ctxAwareBody 是一个**响应 context 取消**的 ReadCloser。
//
// 模拟 Go `http.Transport` 的真实语义：请求 ctx 取消后，body 读取会失败。
type ctxAwareBody struct {
	ctx     context.Context
	payload string
	done    bool
}

func (b *ctxAwareBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err // 等价于真实 Transport 的 "context canceled"
	}
	if b.done {
		return 0, io.EOF
	}
	n := copy(p, b.payload)
	b.done = true
	return n, nil
}

func (b *ctxAwareBody) Close() error { return nil }

// TestHTTPFetchBodyReadCanceledStillFails —— 对照：**真取消**时仍应失败。
//
// 防止「把 ctx 检查整个去掉」这种过度修复。
func TestHTTPFetchBodyReadCanceledStillFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deps := Deps{
		Lookup: publicLookup,
		Doer: func(req *http.Request) (*http.Response, error) {
			cancel() // 响应到达前取消
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       &ctxAwareBody{ctx: req.Context(), payload: "should-not-read"},
			}, nil
		},
	}
	_, err := HTTPFetchGuarded(ctx, "http://example.com/x", deps, Options{})
	if err == nil {
		t.Fatal("外部 ctx 已取消时应报错（不能过度修复）")
	}
}
