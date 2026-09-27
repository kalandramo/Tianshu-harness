package tools

import (
	"net/http"
	"strings"
	"testing"

	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webmap_test.go —— `web_map` 工具（第九十六刀 · W3-5c）。
//
// 对账 TS `src/tools/web-crawl/map-tool.ts`（134 行）。
//
// # 三路来源
//
//  1. sitemap 阶梯（已就位）
//  2. 种子页链接（已就位）
//  3. `site:host` 搜索（**依赖 web_search 后端链**——Go 侧未移植，
//     故 search 参数存在但无后端时，**只走前两路**并如实报告）
//
// # 有意收窄（诚实披露）
//
// `search` 参数依赖 `web_search` 的后端链（`web-search/*.ts` 1085 行）。
// Go 侧未移植，故**第三路来源当前恒不可用**——摘要里搜索计数恒为 0。
// 这**不是**静默失败：`search` 参数会触发「无可用后端」的如实报告。

// ── definition 对账 ─────────────────────────────────────────────────────

// TestWebMapDefinitionParity —— definition 逐字对账 TS。
func TestWebMapDefinitionParity(t *testing.T) {
	def := WebMap(t.TempDir()).Definition()

	if def.Name != "web_map" {
		t.Errorf("name 应为 web_map，实得 %q", def.Name)
	}
	if !strings.HasPrefix(def.Description, "发现站点内的 URL 清单（sitemap + 种子页链接 + site: 搜索三路汇合）") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	for _, want := range []string{
		"先看看这个站有哪些页面",
		"为 web_crawl 探路",
		"轻量：不爬全站",
		"需要用户审批",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description 应含 %q", want)
		}
	}

	wantOrder := []string{"url", "search", "limit", "includeSubdomains"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "url" {
		t.Errorf("required 应只有 url，实得 %#v", def.InputSchema.Required)
	}
	// 属性描述含默认值/上限（对账 TS 模板串）
	mp, _ := def.InputSchema.Properties["limit"].(interface{ Marshal() string })
	if !strings.Contains(mp.Marshal(), "默认 100") || !strings.Contains(mp.Marshal(), "最大 5000") {
		t.Errorf("limit 描述应含默认/上限，实得 %s", mp.Marshal())
	}
	sp, _ := def.InputSchema.Properties["includeSubdomains"].(interface{ Marshal() string })
	if !strings.Contains(sp.Marshal(), "默认 false") {
		t.Errorf("includeSubdomains 描述应含默认值，实得 %s", sp.Marshal())
	}
}

// TestWebMapApprovalSemantics —— 恒需审批 + 并发安全。
func TestWebMapApprovalSemantics(t *testing.T) {
	tool := WebMap(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应并发安全")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// ── 参数校验 ────────────────────────────────────────────────────────────

// TestWebMapInvalidURL —— 非法 URL。
func TestWebMapInvalidURL(t *testing.T) {
	res := runWebMap(t, map[string]any{"url": "not a url\n"})
	if !res.IsError || !strings.Contains(res.Content, "无效 URL") {
		t.Errorf("应报无效 URL，实得 %q", res.Content)
	}
}

// TestWebMapUnsupportedProtocol —— 非 http(s)。
func TestWebMapUnsupportedProtocol(t *testing.T) {
	res := runWebMap(t, map[string]any{"url": "file:///etc/passwd"})
	if !res.IsError || !strings.Contains(res.Content, "不支持的协议") {
		t.Errorf("应报协议错误，实得 %q", res.Content)
	}
}

// TestWebMapRequiredURL —— url 必填。
func TestWebMapRequiredURL(t *testing.T) {
	res := runWebMap(t, map[string]any{})
	if !res.IsError {
		t.Error("缺 url 应报错")
	}
}

// ── clampLimit ──────────────────────────────────────────────────────────

// TestWebMapClampLimit —— 对账 TS `clampLimit`。
func TestWebMapClampLimit(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{nil, 100},
		{"x", 100},
		{float64(50), 50},
		{float64(0), 1},        // 低于 min → 1
		{float64(99999), 5000}, // 高于 max → 5000
		{float64(2.9), 2},      // 向下取整
		{float64(-3), 1},
	}
	for _, c := range cases {
		if got := mapClampLimit(c.in); got != c.want {
			t.Errorf("mapClampLimit(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// ── 端到端（注入 Doer）──────────────────────────────────────────────────

// TestWebMapEndToEndTwoSources —— sitemap + 种子页链接两路汇合。
func TestWebMapEndToEndTwoSources(t *testing.T) {
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p>
<a href="/docs/a">A</a><a href="/docs/b">B</a></main></body></html>`
	sitemapXML := `<urlset>
<url><loc>https://example.com/docs/from-sitemap</loc></url>
<url><loc>https://example.com/docs/a</loc></url>
</urlset>`

	doer := func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "robots.txt"):
			return crawlTestResponse(404, "text/plain", ""), nil
		case strings.HasSuffix(req.URL.Path, "sitemap.xml"):
			return crawlTestResponse(200, "application/xml", sitemapXML), nil
		case req.URL.Path == "/":
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}

	tool := WebMapWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(nil, &CallParams{Input: map[string]any{"url": "https://example.com/"}})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "站点地图：") {
		t.Errorf("应含标题行，实得：\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "来源分布：") {
		t.Errorf("应含来源分布，实得：\n%s", r.Content)
	}
	// 三个 URL 都应被收集（/docs/a 来自两路 → 合并）
	for _, u := range []string{"/docs/from-sitemap", "/docs/a", "/docs/b"} {
		if !strings.Contains(r.Content, u) {
			t.Errorf("应含 %s，实得：\n%s", u, r.Content)
		}
	}
}

// TestWebMapSearchWithoutBackend —— **search 参数在无后端时如实报告**。
//
// 这是「诚实报错而非静默失败」的验证：搜索来源计数为 0 且注明原因。
func TestWebMapSearchWithoutBackend(t *testing.T) {
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p></main></body></html>`
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}
	tool := WebMapWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, _ := tool.Execute(nil, &CallParams{Input: map[string]any{
		"url": "https://example.com/", "search": "keyword",
	}})
	if r.IsError {
		t.Fatalf("应成功（搜索不可用不阻塞），实得 %q", r.Content)
	}
	// 应如实标注「无可用后端」
	if !strings.Contains(r.Content, "无可用后端") {
		t.Errorf("search 无后端时应如实报告，实得：\n%s", r.Content)
	}
}

// TestWebMapSingleResultHint —— 结果 <=1 且种子非根时给出建议。
func TestWebMapSingleResultHint(t *testing.T) {
	// 种子在深路径，页面无链接
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p></main></body></html>`
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/deep/page" {
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}
	tool := WebMapWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, _ := tool.Execute(nil, &CallParams{Input: map[string]any{"url": "https://example.com/deep/page"}})
	if !strings.Contains(r.Content, "建议改用 base domain") {
		t.Errorf("结果过少应给建议，实得：\n%s", r.Content)
	}
}

// TestWebMapIncludeSubdomains —— 子域开关生效。
func TestWebMapIncludeSubdomains(t *testing.T) {
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p>
<a href="https://sub.example.com/x">子域</a><a href="/same">同域</a></main></body></html>`
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}
	mk := func() Tool {
		return WebMapWithDeps(t.TempDir(),
			tnet.FetchCoreDeps{
				Lookup: func(host string) (tnet.ResolvedAddress, error) {
					return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
				},
				Doer: doer,
			},
			tnet.FetchMarkdownOptions{},
		)
	}

	// 默认不含子域
	r, _ := mk().Execute(nil, &CallParams{Input: map[string]any{"url": "https://example.com/"}})
	if strings.Contains(r.Content, "sub.example.com") {
		t.Errorf("默认不应含子域，实得：\n%s", r.Content)
	}
	// 开启后含
	r2, _ := mk().Execute(nil, &CallParams{Input: map[string]any{
		"url": "https://example.com/", "includeSubdomains": true,
	}})
	if !strings.Contains(r2.Content, "sub.example.com") {
		t.Errorf("开启后应含子域，实得：\n%s", r2.Content)
	}
}

// TestWebMapLimitTruncation —— limit 截断提示。
func TestWebMapLimitTruncation(t *testing.T) {
	var links strings.Builder
	for i := 0; i < 10; i++ {
		links.WriteString(`<a href="/p` + itoaC(i) + `">L</a>`)
	}
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p>` + links.String() + `</main></body></html>`
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}
	tool := WebMapWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)
	r, _ := tool.Execute(nil, &CallParams{Input: map[string]any{
		"url": "https://example.com/", "limit": float64(2),
	}})
	if !strings.Contains(r.Content, "已按 limit=2 截断") {
		t.Errorf("应含截断提示，实得：\n%s", r.Content)
	}
}

// ── 常量 ────────────────────────────────────────────────────────────────

// TestWebMapConstants —— 常量对账 TS。
func TestWebMapConstants(t *testing.T) {
	if mapDefaultLimit != 100 {
		t.Errorf("DEFAULT_LIMIT 应为 100，实得 %d", mapDefaultLimit)
	}
	if mapMaxLimit != 5000 {
		t.Errorf("MAX_LIMIT 应为 5000，实得 %d", mapMaxLimit)
	}
	if mapSearchTimeoutMs != 15_000 {
		t.Errorf("SEARCH_TIMEOUT_MS 应为 15000，实得 %d", mapSearchTimeoutMs)
	}
}

// ── 辅助 ────────────────────────────────────────────────────────────────

func runWebMap(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := WebMap(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}

// TestWebMapPathPrefixFilter —— **路径前缀过滤**（变异反证暴露的覆盖缺口）。
//
// # 为什么补这条
//
// M3' 变异（`FilterByPathPrefix` 判定改成恒 false）**没让测试变红**——
// 因为所有既有测试的种子都在**根路径**（`/`），而 `FilterByPathPrefix`
// 对根路径**恒返回 true**（短路），拒绝路径从未被触及。
//
// 本测试用**子目录种子** + 兄弟路径链接，强制走到拒绝分支。
func TestWebMapPathPrefixFilter(t *testing.T) {
	// 种子在 /docs/，页面含：/docs/ 下（应保留）+ /other/ 下（应被拒）
	seedHTML := `<html><body><main><p>` + strings.Repeat("正文。", 50) + `</p>
<a href="/docs/keep1">K1</a>
<a href="/docs/keep2">K2</a>
<a href="/other/drop1">D1</a>
<a href="/other/drop2">D2</a>
</main></body></html>`
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/docs/" {
			return crawlTestResponse(200, "text/html; charset=utf-8", seedHTML), nil
		}
		return crawlTestResponse(404, "text/plain", ""), nil
	}
	tool := WebMapWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(nil, &CallParams{Input: map[string]any{"url": "https://example.com/docs/"}})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	// /docs/ 下的应保留
	for _, u := range []string{"/docs/keep1", "/docs/keep2"} {
		if !strings.Contains(r.Content, u) {
			t.Errorf("应保留 %s，实得：\n%s", u, r.Content)
		}
	}
	// /other/ 下的应被拒
	for _, u := range []string{"/other/drop1", "/other/drop2"} {
		if strings.Contains(r.Content, u) {
			t.Errorf("应拒绝 %s（种子路径前缀过滤），实得：\n%s", u, r.Content)
		}
	}
	// 种子自身在 /docs/ → 应保留（前缀匹配自身）
	if !strings.Contains(r.Content, "https://example.com/docs/") {
		t.Errorf("应含种子自身，实得：\n%s", r.Content)
	}
}
