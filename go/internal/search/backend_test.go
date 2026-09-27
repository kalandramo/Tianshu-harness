package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// backend_test.go —— 五个后端（第九十七刀 · W4）。
//
// 对账 TS `duckduckgo.ts` / `bing.ts` / `brave.ts` / `tavily.ts` / `bocha.ts`。
//
// # 测试策略
//
// **不用真网络**——用桩 Fetch 喂**结构真实**的样本（从 TS 注释里的真实
// HTML 结构复刻），断言解析出的三元组。这样测试确定性且能打红解析错误。

// stubFetch 是记录请求并按 URL 返回预设响应的桩。
type stubFetch struct {
	got      []*Request
	response func(req *Request) (*Response, error)
}

func (s *stubFetch) fetch(_ context.Context, req *Request) (*Response, error) {
	s.got = append(s.got, req)
	if s.response != nil {
		return s.response(req)
	}
	return &Response{Status: 200}, nil
}

func ok(body string) func(*Request) (*Response, error) {
	return func(*Request) (*Response, error) {
		return &Response{Status: 200, Body: []byte(body)}, nil
	}
}

// ── HTML 实体解码 ───────────────────────────────────────────────────────

func TestDecodeHTMLEntities(t *testing.T) {
	cases := []struct{ in, want string }{
		{"&amp;", "&"},
		{"&lt;tag&gt;", "<tag>"},
		{"&quot;quoted&quot;", `"quoted"`},
		{"it&apos;s", "it's"},
		{"it&#39;s", "it's"},
		{"a&nbsp;b", "a\u00A0b"},
		{"&ensp;&emsp;&thinsp;", "\u2002\u2003\u2009"},
		{"&rsaquo;&lsaquo;", "\u203A\u2039"},
		{"&#92;", "\\"},
		{"&#x27;", "'"},
		{"&#x1F600;", "😀"},
	}
	for _, c := range cases {
		if got := DecodeHTMLEntities(c.in); got != c.want {
			t.Errorf("DecodeHTMLEntities(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestDecodeHTMLEntitiesNoDoubleDecode —— **单遍替换**（关键的 XSS 防护语义）。
//
// 对账 TS 注释：命中的实体只替换一次、不重新扫描，故 `&amp;#x27;` 解码为
// 字面量 `&#x27;`，**绝不二次解码成 `'`**。
func TestDecodeHTMLEntitiesNoDoubleDecode(t *testing.T) {
	if got := DecodeHTMLEntities("&amp;#x27;"); got != "&#x27;" {
		t.Errorf("不应二次解码，实得 %q（期望 &#x27;）", got)
	}
}

func TestDecodeHTMLEntitiesUnknownKeptLiteral(t *testing.T) {
	// 未知实体原样保留（对账 TS）
	if got := DecodeHTMLEntities("&unknown;"); got != "&unknown;" {
		t.Errorf("未知实体应原样保留，实得 %q", got)
	}
	// 码点 0 不合法 → 原样保留
	if got := DecodeHTMLEntities("&#0;"); got != "&#0;" {
		t.Errorf("非法码点应原样保留，实得 %q", got)
	}
}

func TestStripHTML(t *testing.T) {
	if got := StripHTML("<b>粗</b>体 <i>斜</i>"); got != "粗体 斜" {
		t.Errorf("实得 %q", got)
	}
}

// ── DuckDuckGo 解析 ─────────────────────────────────────────────────────

// ddgSample 复刻 TS 注释里的 DDG 结构。
const ddgSample = `<html><body>
<div class="result">
  <h2 class="result__title"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&amp;rut=abc">Go &amp; Docs</a></h2>
  <a class="result__snippet" href="x">官方文档 &lt;beta&gt;</a>
</div>
<div class="result">
  <h2 class="result__title"><a class="result__a" href="https://example.com/raw">Second</a></h2>
  <a class="result__snippet" href="y">第二个结果</a>
</div>
</body></html>`

func TestParseDuckDuckGoResults(t *testing.T) {
	got := ParseDuckDuckGoResults(ddgSample, 10)
	if len(got) != 2 {
		t.Fatalf("应解析 2 条，实得 %d（%#v）", len(got), got)
	}
	// 第一条：跳转包装被解开 + 实体解码 + 去标签
	if got[0].URL != "https://go.dev/doc" {
		t.Errorf("应解开 uddg 跳转，实得 %q", got[0].URL)
	}
	if got[0].Title != "Go & Docs" {
		t.Errorf("标题应实体解码，实得 %q", got[0].Title)
	}
	if got[0].Snippet != "官方文档 <beta>" {
		t.Errorf("摘要应去标签+解码，实得 %q", got[0].Snippet)
	}
	// 第二条：裸 URL 原样
	if got[1].URL != "https://example.com/raw" {
		t.Errorf("实得 %q", got[1].URL)
	}
}

func TestParseDuckDuckGoMaxCount(t *testing.T) {
	if got := ParseDuckDuckGoResults(ddgSample, 1); len(got) != 1 {
		t.Errorf("应截断到 1，实得 %d", len(got))
	}
}

func TestParseDuckDuckGoSkipsMalformed(t *testing.T) {
	html := `<h2 class="result__title"><a class="other" href="x">no class</a></h2>`
	if got := ParseDuckDuckGoResults(html, 10); len(got) != 0 {
		t.Errorf("结构不符应跳过，实得 %#v", got)
	}
}

// TestExtractDDGActualURLRejectsNonHTTP —— **只接受 http/https**。
func TestExtractDDGActualURLRejectsNonHTTP(t *testing.T) {
	// javascript: 伪协议不应被解出
	in := "//duckduckgo.com/l/?uddg=" + "javascript%3Aalert(1)"
	if got := extractDDGActualURL(in); strings.HasPrefix(got, "javascript") {
		t.Errorf("非 http(s) 应被拒，实得 %q", got)
	}
}

func TestDDGBackendAlwaysAvailable(t *testing.T) {
	if !NewDuckDuckGoBackend((&stubFetch{}).fetch).IsAvailable() {
		t.Error("DDG 零配置，应恒可用")
	}
}

// TestDDGBackendRequestShape —— 请求头/禁重定向开关。
func TestDDGBackendRequestShape(t *testing.T) {
	sf := &stubFetch{response: ok(ddgSample)}
	b := NewDuckDuckGoBackend(sf.fetch)
	if _, err := b.Search(context.Background(), "go lang", 10); err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	req := sf.got[0]
	if !strings.Contains(req.URL, "html.duckduckgo.com/html/?q=") {
		t.Errorf("实得 %q", req.URL)
	}
	// 查询应被转义（空格 → %20 或 +）
	if !strings.Contains(req.URL, "go") || strings.Contains(req.URL, "go lang") {
		t.Errorf("查询应被 URL 转义，实得 %q", req.URL)
	}
	if !req.NoRedirect {
		t.Error("对账 TS：应禁自动重定向（redirect: manual）")
	}
}

func TestDDGBackendNonOKErrors(t *testing.T) {
	sf := &stubFetch{response: func(*Request) (*Response, error) {
		return &Response{Status: 403}, nil
	}}
	_, err := NewDuckDuckGoBackend(sf.fetch).Search(context.Background(), "q", 10)
	if err == nil {
		t.Fatal("非 2xx 应报错")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误应含状态码，实得 %q", err.Error())
	}
}

// ── Bing 解析 ───────────────────────────────────────────────────────────

// bingLegacySample 复刻 TS 注释里的**旧结构**（有 `<h2>` 包裹）。
const bingLegacySample = `<ol>
<li class="b_algo" data-x="1">
  <div class="b_tpcn"><a class="tilk" href="https://cn.bing.com/fav.ico">icon</a></div>
  <h2 class=""><a href="https://go.dev/">Go 官网 <strong>官方</strong></a></h2>
  <div class="b_caption"><p class="b_lineclamp2">Go 语言官方网站</p></div>
</li>
<li class="b_algo" data-x="2">
  <h2 class=""><a href="https://rust-lang.org/">Rust 官网</a></h2>
  <div class="b_caption"><p class="b_lineclamp4">Rust 官方网站</p></div>
</li>
</ol>`

// bingRevampSample 复刻 TS 注释里的**当前结构**（无 `<h2>`，裸 `<a>`）。
const bingRevampSample = `<ol>
<li class="b_algo" data-x="1">
  <link rel="stylesheet" href="/rp/x.css">
  <div class="b_tpcn"><a class="tilk" href="https://cn.bing.com/fav.ico">icon</a></div>
  <a href="https://go.dev/">Go 官网</a>
  <p class="b_lineclamp2">Go 语言官方网站</p>
</li>
<li class="b_algo" data-x="2">
  <a href="https://cn.bing.com/nav">Bing 导航</a>
  <a href="https://go.dev/ref/spec">Go 语言规范</a>
</li>
</ol>`

func TestParseBingResultsLegacy(t *testing.T) {
	got := ParseBingResults(bingLegacySample, 10)
	if len(got) != 2 {
		t.Fatalf("应解析 2 条，实得 %d（%#v）", len(got), got)
	}
	if got[0].URL != "https://go.dev/" {
		t.Errorf("实得 %q", got[0].URL)
	}
	// 标题里的 <strong> 应被剥离
	if got[0].Title != "Go 官网 官方" {
		t.Errorf("标题应去标签，实得 %q", got[0].Title)
	}
	if got[0].Snippet != "Go 语言官方网站" {
		t.Errorf("实得 %q", got[0].Snippet)
	}
}

func TestParseBingResultsRevamp(t *testing.T) {
	got := ParseBingResults(bingRevampSample, 10)
	if len(got) != 2 {
		t.Fatalf("应解析 2 条，实得 %d（%#v）", len(got), got)
	}
	// 第一条应跳过 tilk favicon（同源 Bing），取裸 <a>
	if got[0].URL != "https://go.dev/" {
		t.Errorf("应跳过 favicon 取真实链接，实得 %q", got[0].URL)
	}
	// 第二条应跳过 Bing 导航链接
	if got[1].URL != "https://go.dev/ref/spec" {
		t.Errorf("应跳过同源导航，实得 %q", got[1].URL)
	}
}

// TestBingInternalURLSkipped —— Bing/微软基础设施 URL 被识别并跳过。
func TestBingInternalURLSkipped(t *testing.T) {
	for _, u := range []string{
		"https://cn.bing.com/x", "https://r.bing.com/y",
		"https://go.microsoft.com/z", "https://support.microsoft.com/a",
	} {
		if !isBingInternalURL(u) {
			t.Errorf("%s 应判为内部 URL", u)
		}
	}
	for _, u := range []string{"https://go.dev/", "https://example.com/"} {
		if isBingInternalURL(u) {
			t.Errorf("%s 不该判为内部 URL", u)
		}
	}
}

// TestDecodeBingCkAURL —— 国际版包装链接的 base64url 解码。
func TestDecodeBingCkAURL(t *testing.T) {
	// a1 + base64url("https://go.dev/")
	inner := "a1" + "aHR0cHM6Ly9nby5kZXYv"
	wrapped := "https://www.bing.com/ck/a?u=" + inner
	if got := decodeBingCkAURL(wrapped); got != "https://go.dev/" {
		t.Errorf("实得 %q", got)
	}
	// 非包装链接原样返回
	if got := decodeBingCkAURL("https://go.dev/"); got != "https://go.dev/" {
		t.Errorf("实得 %q", got)
	}
	// 解码结果非 http(s) → 原样返回
	bad := "https://www.bing.com/ck/a?u=a1" + "amF2YXNjcmlwdDphbGVydCgxKQ"
	if got := decodeBingCkAURL(bad); strings.HasPrefix(got, "javascript") {
		t.Errorf("非 http(s) 结果应被拒，实得 %q", got)
	}
}

// TestBingBackendSendsChineseLanguageNoSetlang —— **语言标识陷阱的回归钉子**。
//
// 对账 TS 注释：cn.bing.com 对携带**任何英文语言标识**的请求会静默错路由，
// 返回 HTTP 200 + 结构完好 + 内容无关的 SERP。故：
//   - **不得传 setlang**（尤其不得 setlang=en-US）
//   - Accept-Language 必须中文优先
//
// 这条测试是**防止未来有人「顺手加上 setlang=en」**。
func TestBingBackendSendsChineseLanguageNoSetlang(t *testing.T) {
	sf := &stubFetch{response: ok(bingLegacySample)}
	if _, err := NewBingBackend(sf.fetch).Search(context.Background(), "杭州西湖 门票", 10); err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	req := sf.got[0]

	if strings.Contains(strings.ToLower(req.URL), "setlang") {
		t.Errorf("**不得传 setlang**（英文标识会触发静默错路由），实得 URL %q", req.URL)
	}
	al := req.Headers["Accept-Language"]
	if al == "" {
		t.Fatal("必须设置 Accept-Language")
	}
	// 必须以中文优先（不得以 en 开头）
	if !strings.HasPrefix(al, "zh") {
		t.Errorf("Accept-Language 必须中文优先，实得 %q", al)
	}
}

// TestBingBackendUsesBrowserUA —— Bing 对非浏览器 UA 返回降级/空 SERP。
func TestBingBackendUsesBrowserUA(t *testing.T) {
	sf := &stubFetch{response: ok(bingLegacySample)}
	_, _ = NewBingBackend(sf.fetch).Search(context.Background(), "q", 10)
	ua := sf.got[0].Headers["User-Agent"]
	if !strings.Contains(ua, "Mozilla") {
		t.Errorf("必须用浏览器 UA（Bing 否则返回降级 SERP），实得 %q", ua)
	}
}

func TestBingBackendAlwaysAvailable(t *testing.T) {
	if !NewBingBackend((&stubFetch{}).fetch).IsAvailable() {
		t.Error("Bing 零配置，应恒可用")
	}
}

// ── Brave ───────────────────────────────────────────────────────────────

func TestBraveBackendAvailability(t *testing.T) {
	if NewBraveBackend((&stubFetch{}).fetch, "", "").IsAvailable() {
		t.Error("无 key 应不可用")
	}
	if !NewBraveBackend((&stubFetch{}).fetch, "k", "").IsAvailable() {
		t.Error("有 key 应可用")
	}
}

func TestBraveBackendParsesJSON(t *testing.T) {
	body := `{"web":{"results":[
	  {"title":"A","url":"https://a.com","description":"da"},
	  {"title":"","url":"https://skip.com","description":"no title"},
	  {"title":"B","url":"","description":"no url"},
	  {"title":"C","url":"https://c.com","description":"dc"}
	]}}`
	sf := &stubFetch{response: ok(body)}
	got, err := NewBraveBackend(sf.fetch, "key", "").Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	// 缺 title 或缺 url 的条目应被跳过
	if len(got) != 2 {
		t.Fatalf("应解析 2 条（跳过残缺条目），实得 %d（%#v）", len(got), got)
	}
	if got[0].Title != "A" || got[0].Snippet != "da" {
		t.Errorf("实得 %#v", got[0])
	}
	// 请求头
	if sf.got[0].Headers["X-Subscription-Token"] != "key" {
		t.Error("应带 X-Subscription-Token")
	}
}

func TestBraveBackendRegionParam(t *testing.T) {
	sf := &stubFetch{response: ok(`{"web":{"results":[]}}`)}
	_, _ = NewBraveBackend(sf.fetch, "k", "cn").Search(context.Background(), "q", 5)
	if !strings.Contains(sf.got[0].URL, "country=cn") {
		t.Errorf("应传 country 参数，实得 %q", sf.got[0].URL)
	}
	if !strings.Contains(sf.got[0].URL, "count=5") {
		t.Errorf("应传 count，实得 %q", sf.got[0].URL)
	}
}

// ── Tavily ──────────────────────────────────────────────────────────────

func TestTavilyBackendParsesJSON(t *testing.T) {
	body := `{"results":[
	  {"title":"T","url":"https://t.com","content":"ct"},
	  {"title":"X","url":"https://x.com","content":"cx"}
	]}`
	sf := &stubFetch{response: ok(body)}
	got, err := NewTavilyBackend(sf.fetch, "tk").Search(context.Background(), "my query", 1)
	if err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	// count=1 → 截断到 1
	if len(got) != 1 {
		t.Fatalf("应截断到 1，实得 %d", len(got))
	}
	if got[0].Snippet != "ct" {
		t.Errorf("实得 %#v", got[0])
	}
	// POST + JSON body
	req := sf.got[0]
	if req.Method != "POST" {
		t.Errorf("应为 POST，实得 %q", req.Method)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body 应是 JSON：%v", err)
	}
	if payload["query"] != "my query" {
		t.Errorf("实得 %#v", payload)
	}
	if payload["max_results"].(float64) != 1 {
		t.Errorf("实得 %#v", payload)
	}
	if req.Headers["Authorization"] != "Bearer tk" {
		t.Error("应带 Bearer 认证头")
	}
}

func TestTavilyAvailability(t *testing.T) {
	if NewTavilyBackend((&stubFetch{}).fetch, "").IsAvailable() {
		t.Error("无 key 应不可用")
	}
}

// ── 博查 ────────────────────────────────────────────────────────────────

func TestBochaBackendParsesJSON(t *testing.T) {
	body := `{"code":200,"data":{"webPages":{"value":[
	  {"name":"知乎回答","url":"https://zhihu.com/a","snippet":"裸摘要","summary":"AI 摘要","siteName":"知乎","datePublished":"2026-07-01"},
	  {"name":"无摘要条目","url":"https://x.com","snippet":"只有裸摘要","siteName":""}
	]}}}`
	sf := &stubFetch{response: ok(body)}
	got, err := NewBochaBackend(sf.fetch, "bk").Search(context.Background(), "q", 10)
	if err != nil {
		t.Fatalf("不应出错：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("实得 %d", len(got))
	}
	// summary 优先于 snippet
	if got[0].Snippet != "AI 摘要" {
		t.Errorf("应优先取 summary，实得 %q", got[0].Snippet)
	}
	if got[0].SiteName != "知乎" {
		t.Errorf("应带 siteName，实得 %q", got[0].SiteName)
	}
	if got[0].PublishedAt != "2026-07-01" {
		t.Errorf("应带 publishedAt，实得 %q", got[0].PublishedAt)
	}
	// 无 summary 时回退 snippet
	if got[1].Snippet != "只有裸摘要" {
		t.Errorf("应回退 snippet，实得 %q", got[1].Snippet)
	}
	// 请求应带 summary:true
	var payload map[string]any
	_ = json.Unmarshal(sf.got[0].Body, &payload)
	if payload["summary"] != true {
		t.Errorf("应请求 summary，实得 %#v", payload)
	}
}

// TestBochaBackendBusinessError —— **HTTP 200 但 code 非 200 的兜底判败**。
func TestBochaBackendBusinessError(t *testing.T) {
	sf := &stubFetch{response: func(*Request) (*Response, error) {
		return &Response{Status: 200, Body: []byte(`{"code":401,"msg":"invalid key"}`)}, nil
	}}
	_, err := NewBochaBackend(sf.fetch, "bad").Search(context.Background(), "q", 10)
	if err == nil {
		t.Fatal("业务错误应报错（HTTP 200 也要判败）")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("错误应含 code 与 msg，实得 %q", err.Error())
	}
}

func TestBochaBackendUnknownErrorMsg(t *testing.T) {
	sf := &stubFetch{response: func(*Request) (*Response, error) {
		return &Response{Status: 200, Body: []byte(`{"code":500}`)}, nil
	}}
	_, err := NewBochaBackend(sf.fetch, "bad").Search(context.Background(), "q", 10)
	if err == nil || !strings.Contains(err.Error(), "unknown error") {
		t.Errorf("缺 msg 时应用 unknown error，实得 %v", err)
	}
}

func TestBochaAvailability(t *testing.T) {
	if NewBochaBackend((&stubFetch{}).fetch, "").IsAvailable() {
		t.Error("无 key 应不可用")
	}
}

// ── 统一契约：非 2xx → HTTPStatusError ──────────────────────────────────

func TestAPIBackendsNonOKErrors(t *testing.T) {
	bad := func(*Request) (*Response, error) { return &Response{Status: 500}, nil }
	mkStub := func() Fetch { return (&stubFetch{response: bad}).fetch }
	for _, c := range []struct {
		name string
		b    Backend
	}{
		{"brave", NewBraveBackend(mkStub(), "k", "")},
		{"tavily", NewTavilyBackend(mkStub(), "k")},
		{"bocha", NewBochaBackend(mkStub(), "k")},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.b.Search(context.Background(), "q", 10)
			if err == nil || !strings.Contains(err.Error(), "500") {
				t.Errorf("非 2xx 应报 HTTP 500，实得 %v", err)
			}
		})
	}
}
