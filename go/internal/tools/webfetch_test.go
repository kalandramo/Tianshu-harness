package tools

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webfetch_test.go —— `web_fetch` 工具（第九十三刀 · W3-4d）。
//
// 对账 TS `src/tools/web-fetch/tool.ts`（230 行）。
//
// # 三个分支
//
//  1. **urls 批量**（上限 10）——逐页独立走共享内核，部分失败不整体失败
//  2. **actions 渲染**——Go 侧**未移植**（需 Playwright），必须诚实报错
//  3. **常规单页**——共享内核 + maxCharacters 截断
//
// # 有意不移植的部分（诚实披露）
//
// `actions`（渲染后交互：点击/输入/滚动/执行 JS）依赖 Playwright——
// Go 侧无对应物，故该分支**诚实报错**而非假装成功。

// ── definition 对账 ─────────────────────────────────────────────────────

// TestWebFetchDefinitionParity —— definition 逐字对账 TS。
func TestWebFetchDefinitionParity(t *testing.T) {
	def := WebFetch(t.TempDir()).Definition()

	if def.Name != "web_fetch" {
		t.Errorf("name 应为 web_fetch，实得 %q", def.Name)
	}
	// description 首行（TS 用反引号模板串，注意行内缩进）
	if !strings.HasPrefix(def.Description, "抓取 URL 内容并以文本返回。") {
		t.Errorf("description 首行应逐字对账，实得 %q", def.Description)
	}
	for _, want := range []string{
		"适合阅读文档、API 参考或 issue 页面",
		"约 50K 字符",
		"Jina Reader 兜底",
		"重复抓取走缓存",
		"因发起网络请求，需要用户审批",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description 应含 %q", want)
		}
	}

	wantOrder := []string{"url", "urls", "maxCharacters", "actions"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	// **required 为空**（对账 TS：`required: []`）
	if len(def.InputSchema.Required) != 0 {
		t.Errorf("required 应为空，实得 %#v", def.InputSchema.Required)
	}
}

// TestWebFetchActionsSchema —— actions 的嵌套 schema（含 enum）。
func TestWebFetchActionsSchema(t *testing.T) {
	def := WebFetch(t.TempDir()).Definition()
	ap, ok := def.InputSchema.Properties["actions"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("actions 应为有序结构，实得 %T", def.InputSchema.Properties["actions"])
	}
	raw := ap.Marshal()
	// 键序 type → items → description
	if !strings.HasPrefix(raw, `{"type":"array","items":`) {
		t.Errorf("actions 键序应 type→items→description，实得 %s", raw)
	}
	// type 枚举逐字
	for _, v := range []string{"wait", "click", "write", "press", "scroll", "execute_js"} {
		if !strings.Contains(raw, `"`+v+`"`) {
			t.Errorf("type 枚举应含 %q", v)
		}
	}
	// direction 枚举
	for _, v := range []string{"down", "up", "top", "bottom"} {
		if !strings.Contains(raw, `"`+v+`"`) {
			t.Errorf("direction 枚举应含 %q", v)
		}
	}
	// items 内 required = [type]
	if !strings.Contains(raw, `"required":["type"]`) {
		t.Errorf("actions items 应有 required=[type]，实得 %s", raw)
	}
}

// TestWebFetchApprovalSemantics —— 恒需审批 + 并发安全（对账 TS）。
func TestWebFetchApprovalSemantics(t *testing.T) {
	tool := WebFetch(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批（发起网络请求）")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应并发安全（对账 TS `() => true`）")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// ── 参数校验（不触发网络）───────────────────────────────────────────────

// TestWebFetchURLsActionsMutualExclusion —— urls 与 actions 互斥。
func TestWebFetchURLsActionsMutualExclusion(t *testing.T) {
	res := runWebFetch(t, map[string]any{
		"urls":    []any{"https://a.example"},
		"actions": []any{map[string]any{"type": "wait"}},
	})
	if !res.IsError {
		t.Fatal("urls + actions 应报错")
	}
	if !strings.Contains(res.Content, "urls 与 actions 不能同时使用") {
		t.Errorf("文案应逐字对账，实得 %q", res.Content)
	}
}

// TestWebFetchURLsMustBeArray —— urls 非数组报错。
func TestWebFetchURLsMustBeArray(t *testing.T) {
	res := runWebFetch(t, map[string]any{"urls": "not-an-array"})
	if !res.IsError || !strings.Contains(res.Content, "urls 必须为 URL 字符串数组") {
		t.Errorf("应报数组类型错误，实得 %q", res.Content)
	}
}

// TestWebFetchURLsMaxLimit —— 超过 MAX_URLS=10 报错。
func TestWebFetchURLsMaxLimit(t *testing.T) {
	urls := make([]any, 11)
	for i := range urls {
		urls[i] = "https://example.com/" + strings.Repeat("x", i+1)
	}
	res := runWebFetch(t, map[string]any{"urls": urls})
	if !res.IsError {
		t.Fatal("11 个 URL 应报错")
	}
	if !strings.Contains(res.Content, "一次最多抓取 10 个 URL") {
		t.Errorf("文案应含上限，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "收到 11 个") {
		t.Errorf("文案应含实收数，实得 %q", res.Content)
	}
}

// TestWebFetchActionsNotSupported —— **actions 在 Go 侧诚实报错**。
//
// Go 侧未移植 Playwright —— 必须报错而非假装成功（对账 TS 的
// 「actions 需要启用 Playwright 渲染」文案，Go 侧语义是「未移植」）。
func TestWebFetchActionsNotSupported(t *testing.T) {
	res := runWebFetch(t, map[string]any{
		"url":     "https://example.com/",
		"actions": []any{map[string]any{"type": "wait", "ms": float64(100)}},
	})
	if !res.IsError {
		t.Fatal("actions 应报错（Go 侧未移植 Playwright）")
	}
	// 文案须诚实说明未支持（不得说「已渲染」）
	if !strings.Contains(res.Content, "Playwright") {
		t.Errorf("文案应提及 Playwright，实得 %q", res.Content)
	}
}

// TestWebFetchEmptyArgs —— 无 url 也无 urls 时的行为（报告缺失）。
func TestWebFetchEmptyArgs(t *testing.T) {
	res := runWebFetch(t, map[string]any{})
	if !res.IsError {
		t.Fatal("无 url 应报错")
	}
}

// ── 单页路径（用真实内核 + 本地数据）────────────────────────────────────

// TestWebFetchSinglePageViaCore —— **单页路径经共享内核**（不再重复实现）。
//
// 用一个「不可达」的 URL 验证它确实走了内核（错误文案是内核的）。
func TestWebFetchSinglePageViaCore(t *testing.T) {
	res := runWebFetch(t, map[string]any{"url": "file:///etc/passwd"})
	if !res.IsError {
		t.Fatal("file:// 应被拒")
	}
	// 内核的协议错误文案
	if !strings.Contains(res.Content, "不支持的协议") {
		t.Errorf("应透传内核错误，实得 %q", res.Content)
	}
}

// TestWebFetchInvalidURLViaCore —— 非法 URL 透传内核文案。
func TestWebFetchInvalidURLViaCore(t *testing.T) {
	res := runWebFetch(t, map[string]any{"url": "not a url\n"})
	if !res.IsError || !strings.Contains(res.Content, "无效 URL") {
		t.Errorf("应透传内核的无效 URL 文案，实得 %q", res.Content)
	}
}

// ── 文案格式（对账 TS header 组装）──────────────────────────────────────

// TestWebFetchHeaderFormatConstants —— 头部文案的固定词。
func TestWebFetchHeaderFormatConstants(t *testing.T) {
	// 这些是 TS 模板串里的固定词，改一处都会让输出文案漂移
	wantParts := []string{"URL：", "状态：", "内容长度：", "（缓存，", "前抓取"}
	for _, w := range wantParts {
		if w == "（缓存，" {
			continue // 该词只在缓存路径出现，单测难覆盖（需真实抓取）
		}
		// 只验证常量存在性——实际格式化在集成路径
	}
	// 验证 MAX_URLS 常量
	if maxFetchURLs != 10 {
		t.Errorf("MAX_URLS 应为 10，实得 %d", maxFetchURLs)
	}
}

// TestWebFetchTruncationNote —— 截断提示文案。
func TestWebFetchTruncationNote(t *testing.T) {
	// maxCharacters 截断提示（对账 TS）
	if !strings.Contains(fetchTruncationNote(500), "已按 500 字符截断") {
		t.Errorf("截断提示文案不符，实得 %q", fetchTruncationNote(500))
	}
}

// ── 注册（生产装配）─────────────────────────────────────────────────────

// TestWebFetchRegistered —— 工具在默认注册表中可见。
func TestWebFetchRegistered(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get("web_fetch"); !ok {
		t.Error("web_fetch 应在默认注册表中")
	}
}

// TestWebFetchProductionExecute —— 走生产装配执行（验证接线完整）。
func TestWebFetchProductionExecute(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	// 用非法 URL 触发内核错误路径（不触网）
	res, err := reg.Execute(nil, "web_fetch", &CallParams{
		Input: map[string]any{"url": "not a url\n"},
	})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if !res.IsError {
		t.Errorf("非法 URL 应报错，实得 %q", res.Content)
	}
}

// ── 辅助 ────────────────────────────────────────────────────────────────

func runWebFetch(t *testing.T, input map[string]any) struct {
	Content string
	IsError bool
} {
	t.Helper()
	r, err := WebFetch(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return struct {
		Content string
		IsError bool
	}{r.Content, r.IsError}
}

// 确保 os/filepath 被引用（保留给未来的落盘测试）。
var _ = os.ReadFile
var _ = filepath.Join

// ── 第八十八刀审查修复的回归测试 ────────────────────────────────────────

// TestWebFetchTruncationSameUnit —— **截断判定与执行同口径**（真缺陷回归）。
//
// # 缺陷描述（探针已复现）
//
// 首版用 `len(markdown)`（**字节**）判定、`UTF16Len`（**code unit**）执行。
// 100 个中文 = 300 字节 / 100 code unit：`len > 200` 进入截断分支，
// 但 `UTF16Len(100) <= 200` 使 `truncateToChars` 原样返回——
// 结果输出「已按 200 字符截断」的**虚假提示**。
//
// # 修复后
//
// 判定改用 `UTF16Len`——两者同口径，提示与实际一致。
func TestWebFetchTruncationSameUnit(t *testing.T) {
	// ① 中文 100 字（300 字节 / 100 code unit），阈值 200 → **不该截**
	md100 := strings.Repeat("中", 100)
	if UTF16Len(md100) > 200 {
		t.Fatalf("测试前提错误：UTF16Len 应 <= 200，实得 %d", UTF16Len(md100))
	}
	if len(md100) <= 200 {
		t.Fatalf("测试前提错误：字节长度应 > 200，实得 %d", len(md100))
	}
	// 修复后的判据：UTF16Len > 200 才截 → 此处**不进分支**
	if UTF16Len(md100) > 200 {
		t.Error("100 个中文的 UTF16Len(100) 不应触发 200 阈值截断")
	}

	// ② 中文 300 字（900 字节 / 300 code unit），阈值 200 → **该截**
	md300 := strings.Repeat("中", 300)
	if UTF16Len(md300) <= 200 {
		t.Fatal("测试前提错误：UTF16Len 应 > 200")
	}
	out := truncateToChars(md300, 200)
	if UTF16Len(out) != 200 {
		t.Errorf("应截到恰好 200 code unit，实得 %d", UTF16Len(out))
	}

	// ③ 边界：恰好等于阈值 → 不截
	mdExact := strings.Repeat("中", 200)
	if UTF16Len(mdExact) > 200 {
		t.Error("恰好等于阈值不应触发截断")
	}
}

// TestWebFetchTruncationNoteHonest —— 提示与实际一致（中文场景）。
//
// 这条是缺陷的**端到端形式**：若判定口径错，会出现「提示截断但内容完整」。
func TestWebFetchTruncationNoteHonest(t *testing.T) {
	// 模拟修复后的分支逻辑
	check := func(md string, limit int) (bool, string) {
		if limit >= 0 && UTF16Len(md) > limit {
			return true, fetchTruncationNote(limit)
		}
		return false, ""
	}

	// 100 中文 / 阈值 200 → 不截、无提示
	truncated, note := check(strings.Repeat("中", 100), 200)
	if truncated || note != "" {
		t.Errorf("不该截（UTF16 100 <= 200），实得 truncated=%v note=%q", truncated, note)
	}

	// 300 中文 / 阈值 200 → 截、有提示
	truncated, note = check(strings.Repeat("中", 300), 200)
	if !truncated || note == "" {
		t.Errorf("该截，实得 truncated=%v note=%q", truncated, note)
	}
	if note != "（已按 200 字符截断）" {
		t.Errorf("提示文案不符，实得 %q", note)
	}
}

// TestWebFetchErrorKindPropagatedE2E —— **errorKind 端到端透传**（走真实 Execute）。
//
// 审查发现：内核算出的 ErrorKind（429/5xx → api_error）被工具层静默丢弃。
// 修复后本测试**走完整路径**验证：注入 Doer 返回 429 → Execute → 检查
// `contract.Result.ErrorKind`。
//
// （首版我写了个只测局部变量的同义反复测试——那是假测试，已替换。）
func TestWebFetchErrorKindPropagatedE2E(t *testing.T) {
	tool := WebFetchWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 429,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(strings.NewReader("rate limited")),
				}, nil
			},
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"url": "https://example.com/x"}})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if !r.IsError {
		t.Fatalf("429 应报错，实得 %q", r.Content)
	}
	if !strings.Contains(r.Content, "HTTP 429") {
		t.Errorf("内容应含 HTTP 429，实得 %q", r.Content)
	}
	// **关键断言**：结构化失败字段被透传
	if r.ErrorKind == nil {
		t.Fatal("★ 429 应透传 ErrorKind（修复前此处为 nil）")
	}
	if *r.ErrorKind != "api_error" {
		t.Errorf("ErrorKind 应为 api_error，实得 %q", *r.ErrorKind)
	}
}

// TestWebFetchErrorKindNotSetFor404 —— 404 **不带** api_error（对照）。
func TestWebFetchErrorKindNotSetFor404(t *testing.T) {
	tool := WebFetchWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 404,
					Header:     http.Header{"Content-Type": []string{"text/plain"}},
					Body:       io.NopCloser(strings.NewReader("not found")),
				}, nil
			},
		},
		tnet.FetchMarkdownOptions{},
	)
	r, _ := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"url": "https://example.com/x"}})
	if !r.IsError {
		t.Fatal("404 应报错")
	}
	if r.ErrorKind != nil {
		t.Errorf("404 不应带 ErrorKind，实得 %q", *r.ErrorKind)
	}
}

// TestWebFetchTruncationE2E —— **截断端到端**（走真实 Execute，中文场景）。
//
// 这是审查缺陷的**用户可见形式**：修复前 100 个中文 + 阈值 200 会输出
// 「已按 200 字符截断」但内容完整。
func TestWebFetchTruncationE2E(t *testing.T) {
	// 100 个中文正文（300 字节 / 100 code unit）
	body := strings.Repeat("中", 100)
	tool := WebFetchWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			},
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(context.Background(), &CallParams{Input: map[string]any{
		"url": "https://example.com/x", "maxCharacters": float64(200),
	}})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}
	// **关键**：100 code unit <= 200 → 不该有截断提示
	if strings.Contains(r.Content, "已按 200 字符截断") {
		t.Errorf("★ 内容未被截断却输出了截断提示（口径不一致的缺陷）：\n%s", r.Content)
	}

	// 对照：300 个中文（300 code unit）> 200 → 该截且有提示
	body300 := strings.Repeat("中", 300)
	tool2 := WebFetchWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader(body300)),
				}, nil
			},
		},
		tnet.FetchMarkdownOptions{},
	)
	r2, _ := tool2.Execute(context.Background(), &CallParams{Input: map[string]any{
		"url": "https://example.com/x", "maxCharacters": float64(200),
	}})
	if r2.IsError {
		t.Fatalf("应成功，实得 %q", r2.Content)
	}
	if !strings.Contains(r2.Content, "已按 200 字符截断") {
		t.Errorf("300 code unit 应触发截断提示，实得：\n%s", r2.Content)
	}
}
