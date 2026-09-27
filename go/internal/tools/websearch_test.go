package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/search"
)

// websearch_test.go —— `web_search` 工具（第九十七刀 · W5）。
//
// 对账 TS `src/tools/web-search/tool.ts`（151 行）。

// fakeBackend 是测试用后端。
type fakeBackend struct {
	name      string
	available bool
	results   []search.Result
	err       error
}

func (f *fakeBackend) Name() string      { return f.name }
func (f *fakeBackend) IsAvailable() bool { return f.available }
func (f *fakeBackend) Search(context.Context, string, int) ([]search.Result, error) {
	return f.results, f.err
}

func mkSearch(backends ...search.Backend) Tool {
	return WebSearchWithBackends(search.SearchConfig{}, backends, 1000)
}

// ── definition ──────────────────────────────────────────────────────────

func TestWebSearchDefinitionParity(t *testing.T) {
	def := mkSearch().Definition()

	if def.Name != "web_search" {
		t.Errorf("name 应为 web_search，实得 %q", def.Name)
	}
	// description 首行逐字对账
	if !strings.HasPrefix(def.Description, "搜索 Web 获取实时信息。结果包含标题、URL 和内容摘要。") {
		t.Errorf("description 首行不符：%q", def.Description)
	}
	for _, want := range []string{
		"### 何时搜索", "### 何时不要搜索", "### 使用结果（署名与版权）",
		"不认识的首字母大写名称", "不要编造", "semantic_search",
		"约 15 词以内", "绝不要逐字复制文章段落",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description 应含 %q", want)
		}
	}

	want := []string{"query", "count"}
	if len(def.InputSchema.PropOrder) != len(want) {
		t.Fatalf("PropOrder 实得 %#v", def.InputSchema.PropOrder)
	}
	for i, w := range want {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "query" {
		t.Errorf("required 应只有 query，实得 %#v", def.InputSchema.Required)
	}
	cp, _ := def.InputSchema.Properties["count"].(interface{ Marshal() string })
	if !strings.Contains(cp.Marshal(), "默认：10") || !strings.Contains(cp.Marshal(), "最大：20") {
		t.Errorf("count 描述应含默认/上限，实得 %s", cp.Marshal())
	}
}

func TestWebSearchApprovalSemantics(t *testing.T) {
	tool := mkSearch()
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

func TestWebSearchEmptyQuery(t *testing.T) {
	for _, in := range []map[string]any{{}, {"query": ""}, {"query": "   "}, {"query": 123}} {
		r, err := mkSearch().Execute(nil, &CallParams{Input: in})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if !r.IsError || !strings.Contains(r.Content, "query 必须是非空字符串") {
			t.Errorf("输入 %#v 应报 query 错误，实得 %q", in, r.Content)
		}
	}
}

// TestClampSearchCount —— 对账 TS 的 clamp 语义。
func TestClampSearchCount(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{nil, 10},
		{"x", 10},
		{float64(0), 10}, // 0 按默认处理（lenientPositiveNumber 对 0 返 undefined）
		{float64(-5), 10},
		{float64(5), 5},
		{float64(20), 20},
		{float64(999), 20}, // 上界
		{float64(2.9), 2},  // 向下取整
	}
	for _, c := range cases {
		if got := clampSearchCount(c.in); got != c.want {
			t.Errorf("clampSearchCount(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// ── 三分支（★ 本刀核心）────────────────────────────────────────────────

// TestWebSearchSuccessPath —— 正常路径含后端归属标注。
func TestWebSearchSuccessPath(t *testing.T) {
	b := &fakeBackend{name: "bing", available: true, results: []search.Result{
		{Title: "Go 官网", URL: "https://go.dev/", Snippet: "Go 语言"},
	}}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "go"}})

	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "「go」的网页搜索结果（经 bing）：") {
		t.Errorf("应含查询与后端标注，实得：\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "1. [Go 官网](https://go.dev/)") {
		t.Errorf("应含编号+标题+URL，实得：\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "   Go 语言") {
		t.Errorf("应含摘要缩进，实得：\n%s", r.Content)
	}
}

// TestWebSearchSiteNameAndPublishedAt —— 可选元信息附在摘要后。
func TestWebSearchSiteNameAndPublishedAt(t *testing.T) {
	b := &fakeBackend{name: "bocha", available: true, results: []search.Result{
		{Title: "知乎", URL: "https://zhihu.com/a", Snippet: "摘要",
			SiteName: "知乎", PublishedAt: "2026-07-01"},
	}}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "q"}})
	if !strings.Contains(r.Content, "摘要（知乎 · 2026-07-01）") {
		t.Errorf("应含元信息，实得：\n%s", r.Content)
	}
}

// TestWebSearchHardErrorBranch —— **分支 1**：有硬错误 → isError=true。
func TestWebSearchHardErrorBranch(t *testing.T) {
	b := &fakeBackend{name: "bing", available: true, err: errors.New("HTTP 503")}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "q"}})

	if !r.IsError {
		t.Fatalf("硬错误应 isError=true，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "搜索失败") || !strings.Contains(r.Content, "HTTP 503") {
		t.Errorf("应含失败文案与原因，实得：\n%s", r.Content)
	}
	// 应含后端名归属
	if !strings.Contains(r.Content, "bing:") {
		t.Errorf("应含后端名，实得：\n%s", r.Content)
	}
}

// TestWebSearchNoResultsBranchIsNotError —— **分支 3**：无结果**不是错误**。
//
// 对账 TS 注释：全部后端空结果 → 「用户可见结果相同」，
// 报成硬错误会**误导**。
func TestWebSearchNoResultsBranchIsNotError(t *testing.T) {
	b := &fakeBackend{name: "bing", available: true, results: nil}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "abc"}})

	if r.IsError {
		t.Errorf("空结果不该报错，实得 %q", r.Content)
	}
	if r.Content != "未找到与「abc」相关的搜索结果" {
		t.Errorf("实得 %q", r.Content)
	}
}

// TestWebSearchOffTopicNoticeIsNotError —— 「跑题」软失败也不报硬错误。
func TestWebSearchOffTopicSoftFailureNotHardError(t *testing.T) {
	// 结果与查询完全无关 → 链判跑题 → 无硬错误
	b := &fakeBackend{name: "bing", available: true, results: []search.Result{
		{Title: "完全无关的内容", URL: "https://x.com/", Snippet: "无关"},
	}}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "zzzz qqqq"}})

	if r.IsError {
		t.Errorf("跑题软失败不该报硬错误，实得 %q", r.Content)
	}
}

// TestWebSearchLowConfidenceFallback —— **分支 2**：跑题兜底降级返回 + 标注。
//
// 这是本工具最有认知价值的一条：单后端配置下，若把「后端降级返回泛结果」
// 变成「未找到结果」，用户丢掉的是**唯一可得的信息**。标注把采信判断权
// 交回使用者，同时不让泛结果**冒充答案**。
func TestWebSearchLowConfidenceFallback(t *testing.T) {
	// 多词查询，结果只覆盖一个泛词 → 判跑题 → 兜底
	b := &fakeBackend{name: "bing", available: true, results: []search.Result{
		{Title: "美国 - 维基百科", URL: "https://x.com/us", Snippet: "美国简介"},
		{Title: "美国历史", URL: "https://x.com/h", Snippet: "美国历史"},
	}}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "美国 AI 实验室 出逃"}})

	if r.IsError {
		t.Fatalf("兜底不该报硬错误，实得 %q", r.Content)
	}
	// 应含低相关标注 **逐字**
	if !strings.Contains(r.Content, "⚠ 相关性提示：以下结果只覆盖了查询中的个别词，可能不是你要找的内容。") {
		t.Errorf("应含低置信标注（逐字），实得：\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "请勿直接采信为答案") {
		t.Errorf("应含「勿直接采信」提示，实得：\n%s", r.Content)
	}
	// 应标注后端与「低相关」
	if !strings.Contains(r.Content, "（经 bing，低相关）") {
		t.Errorf("应标注低相关，实得：\n%s", r.Content)
	}
	// 结果本身应仍在（不丢弃唯一可得信息）
	if !strings.Contains(r.Content, "美国 - 维基百科") {
		t.Errorf("兜底结果应予保留，实得：\n%s", r.Content)
	}
}

// TestWebSearchLowConfidenceOnlyWhenNoWinner —— 有相关结果时**不**走兜底。
func TestWebSearchLowConfidenceOnlyWhenNoWinner(t *testing.T) {
	b := &fakeBackend{name: "bing", available: true, results: []search.Result{
		{Title: "Go 语言圣经", URL: "https://x.com/", Snippet: "Golang 教程"},
	}}
	r, _ := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "Golang 教程"}})
	if strings.Contains(r.Content, "⚠ 相关性提示") {
		t.Errorf("有相关结果时不该出现低置信标注，实得：\n%s", r.Content)
	}
}

// TestWebSearchMultiBackendFallthrough —— 链序落空到下一个后端。
func TestWebSearchMultiBackendFallthrough(t *testing.T) {
	b1 := &fakeBackend{name: "no-key", available: false}
	b2 := &fakeBackend{name: "empty", available: true, results: nil}
	b3 := &fakeBackend{name: "win", available: true, results: []search.Result{
		{Title: "命中", URL: "https://x.com/", Snippet: "内容"},
	}}
	r, _ := mkSearch(b1, b2, b3).Execute(nil, &CallParams{Input: map[string]any{"query": "q"}})
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "（经 win）") {
		t.Errorf("应标注胜出后端，实得：\n%s", r.Content)
	}
}

// TestWebSearchNilContext —— nil ctx 不 panic。
func TestWebSearchNilContext(t *testing.T) {
	b := &fakeBackend{name: "b", available: true, results: []search.Result{
		{Title: "T", URL: "https://x.com/", Snippet: "S"},
	}}
	r, err := mkSearch(b).Execute(nil, &CallParams{Input: map[string]any{"query": "q"}})
	if err != nil || r.IsError {
		t.Errorf("nil ctx 应正常工作，err=%v content=%q", err, r.Content)
	}
}

func TestWebSearchTimeoutDeclared(t *testing.T) {
	tool := WebSearchWithBackends(search.SearchConfig{}, nil, 7000)
	if got := tool.Timeout(nil); got.Milliseconds() != 7000 {
		t.Errorf("实得 %v", got)
	}
	// 零值时回退默认
	tool2 := WebSearchWithBackends(search.SearchConfig{}, nil, 0)
	if got := tool2.Timeout(nil).Milliseconds(); got != 15_000 {
		t.Errorf("零值应回退 15000ms，实得 %d", got)
	}
}

// ── BuildBackends ───────────────────────────────────────────────────────

func TestBuildBackendsFromConfig(t *testing.T) {
	cfg := search.SearchConfig{
		Backends:        []string{"bing", "duckduckgo", "brave"},
		BraveAPIKeyEnv:  "ABSENT_BRAVE_KEY",
		TavilyAPIKeyEnv: "TAVILY_API_KEY",
		BochaAPIKeyEnv:  "BOCHA_API_KEY",
		TimeoutMs:       1000,
	}
	got := search.BuildBackends(cfg, search.BuildOptions{Fetch: func(context.Context, *search.Request) (*search.Response, error) {
		return &search.Response{Status: 200}, nil
	}})

	if len(got) != 3 {
		t.Fatalf("应构造 3 个后端，实得 %d", len(got))
	}
	if got[0].Name() != "bing" || got[1].Name() != "duckduckgo" || got[2].Name() != "brave" {
		t.Errorf("链序应保序，实得 %s/%s/%s", got[0].Name(), got[1].Name(), got[2].Name())
	}
	// brave 无 key → 不可用（但**仍被构造**，对账 TS：「列出但未配置」落到下一环）
	if got[2].IsAvailable() {
		t.Error("无 key 的 brave 应 IsAvailable=false")
	}
	// 抓取型后端零配置可用
	if !got[0].IsAvailable() || !got[1].IsAvailable() {
		t.Error("bing/duckduckgo 应恒可用")
	}
}

// TestBuildBackendsUnknownNameSkipped —— **未知后端名跳过，不废掉整条链**。
func TestBuildBackendsUnknownNameSkipped(t *testing.T) {
	cfg := search.SearchConfig{
		Backends:  []string{"bing", "typo-backend", "duckduckgo"},
		TimeoutMs: 1000,
	}
	got := search.BuildBackends(cfg, search.BuildOptions{Fetch: func(context.Context, *search.Request) (*search.Response, error) {
		return &search.Response{Status: 200}, nil
	}})
	if len(got) != 2 {
		t.Fatalf("应跳过未知名保留 2 个，实得 %d（%v）", len(got), names(got))
	}
}

// TestBuildBackendsEmptyFallsBackToDDG —— 链空时加 DDG 零配置兜底。
func TestBuildBackendsEmptyFallsBackToDDG(t *testing.T) {
	cfg := search.SearchConfig{Backends: nil, TimeoutMs: 1000}
	got := search.BuildBackends(cfg, search.BuildOptions{Fetch: func(context.Context, *search.Request) (*search.Response, error) {
		return &search.Response{Status: 200}, nil
	}})
	if len(got) != 1 || got[0].Name() != "duckduckgo" {
		t.Errorf("应退化为 DDG 单后端，实得 %v", names(got))
	}
	// 全未知名同样触发兜底
	cfg2 := search.SearchConfig{Backends: []string{"nope"}, TimeoutMs: 1000}
	got2 := search.BuildBackends(cfg2, search.BuildOptions{Fetch: func(context.Context, *search.Request) (*search.Response, error) {
		return &search.Response{Status: 200}, nil
	}})
	if len(got2) != 1 || got2[0].Name() != "duckduckgo" {
		t.Errorf("全未知名应退化为 DDG，实得 %v", names(got2))
	}
}

func names(bs []search.Backend) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Name())
	}
	return out
}
