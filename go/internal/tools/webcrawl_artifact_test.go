package tools

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// webcrawl_artifact_test.go —— web_crawl 的 artifact 接线（第九十八刀）。
//
// # 背景
//
// `webcrawl.go` 文件头（第九十五刀）写着「**artifact 落盘未接**」——
// `buildCrawlArtifact` 已实现、`formatCrawlSummary` 已接受 `artifactNote` 参数，
// 但 `Execute` 恒传 `""`。这是本仓库高频缺陷模式「**实现已有但零消费**」。
//
// # 复用了什么
//
// `CallParams.ArtifactStore`（`registry.go:171`）**早已存在**，
// 且 `bash.go:425` / `grep.go:181` / `read_file.go:444` 都在用它。
// 故本刀**只接线，不造机制**。

// ── 语义钉子 ────────────────────────────────────────────────────────────

// TestSaveCrawlArtifactWritesAndNotes —— **核心验收**：落盘 + 注记。
func TestSaveCrawlArtifactWritesAndNotes(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "sess-1", artifact.Options{
		Now:         func() int64 { return 1700000000000 },
		IDGenerator: func() string { return "abcd1234" },
	})
	tool := &webCrawlTool{}
	p := &CallParams{ArtifactStore: store}

	result := tnet.CrawlResult{Pages: []tnet.CrawlPage{
		{URL: "https://example.com/", Markdown: "首页正文"},
		{URL: "https://example.com/a", Markdown: "A 页正文"},
	}}

	note := tool.saveCrawlArtifact(p, "https://example.com/", result)

	if !strings.HasPrefix(note, "\n\n完整内容已存 artifact：") {
		t.Fatalf("注记格式不符，实得 %q", note)
	}
	if !strings.Contains(note, "（可用 read_section 分页细读）") {
		t.Errorf("应含 read_section 提示，实得 %q", note)
	}

	// **落盘真的发生了**（不只是返回了注记）——查 store 的内容
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("应落盘 1 条 artifact，实得 %d", len(list))
	}
	a := list[0]
	if a.Tool != "web_crawl" {
		t.Errorf("tool 应为 web_crawl，实得 %q", a.Tool)
	}
	if a.Target != "https://example.com/" {
		t.Errorf("target 实得 %q", a.Target)
	}
	// 注记里的 id 应能在 store 里查到
	id := strings.TrimSuffix(strings.TrimPrefix(note, "\n\n完整内容已存 artifact："), "（可用 read_section 分页细读）")
	if a.ID != id {
		t.Errorf("注记 id %q 与落盘 id %q 不符", id, a.ID)
	}
	// 原文应含两页内容
	raw, err := store.ReadRaw(a.ID)
	if err != nil {
		t.Fatalf("读原文失败：%v", err)
	}
	if !strings.Contains(raw, "首页正文") || !strings.Contains(raw, "A 页正文") {
		t.Errorf("原文应含两页内容，实得：\n%s", raw)
	}
	// sections 按页切分
	if len(a.Sections) != 2 {
		t.Errorf("应有 2 个 section，实得 %d", len(a.Sections))
	}
}

// TestSaveCrawlArtifactNilStoreSilent —— **没有 Store 时注记为空**。
//
// 对账 TS 的 `params.artifactStore &&` 短路。
// 「调用方没配」与「写失败了」必须可区分——前者静默（设计如此），
// 后者明示（见下条）。
func TestSaveCrawlArtifactNilStoreSilent(t *testing.T) {
	tool := &webCrawlTool{}
	result := tnet.CrawlResult{Pages: []tnet.CrawlPage{{URL: "u", Markdown: "m"}}}

	if got := tool.saveCrawlArtifact(&CallParams{}, "https://x/", result); got != "" {
		t.Errorf("无 Store 时注记应为空，实得 %q", got)
	}
	if got := tool.saveCrawlArtifact(nil, "https://x/", result); got != "" {
		t.Errorf("nil CallParams 应安全返回空，实得 %q", got)
	}
}

// TestSaveCrawlArtifactNoPagesSilent —— **零页时注记为空**。
//
// 对账 TS 的 `&& result.pages.length > 0`——没有内容可存。
func TestSaveCrawlArtifactNoPagesSilent(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "sess-1", artifact.Options{})
	tool := &webCrawlTool{}

	got := tool.saveCrawlArtifact(&CallParams{ArtifactStore: store}, "https://x/", tnet.CrawlResult{})

	if got != "" {
		t.Errorf("零页时注记应为空，实得 %q", got)
	}
	// 不该落盘空 artifact
	if len(store.List()) != 0 {
		t.Errorf("零页不该落盘，实得 %d 条", len(store.List()))
	}
}

// TestSaveCrawlArtifactFailureIsExplicit —— ★ **写失败必须明示降级**。
//
// 对账 TS 的 `catch { artifactNote = '\n\n（artifact 持久化失败，仅返回摘要）' }`。
// 这是「失败要大声」的具体形态——静默省略会让模型以为内容完整。
func TestSaveCrawlArtifactFailureIsExplicit(t *testing.T) {
	// 用一个**不可写的 baseDir** 制造 Save 失败
	store := artifact.NewStore("/dev/null/definitely/not/writable", "sess-1", artifact.Options{})
	tool := &webCrawlTool{}
	result := tnet.CrawlResult{Pages: []tnet.CrawlPage{{URL: "https://x/", Markdown: "正文"}}}

	got := tool.saveCrawlArtifact(&CallParams{ArtifactStore: store}, "https://x/", result)

	if got != "\n\n（artifact 持久化失败，仅返回摘要）" {
		t.Errorf("写失败应明示降级（逐字对账 TS），实得 %q", got)
	}
}

// ── 端到端（走完整 Execute，注入 Doer）──────────────────────────────────

// TestWebCrawlEndToEndWritesArtifact —— **走 Execute 验证接线真的生效**。
//
// # 为什么必须端到端
//
// `saveCrawlArtifact` 单测通过**不等于** `Execute` 调了它——
// 这正是本刀要修的缺陷形态（函数存在但零调用）。
// 若 Execute 仍传 ""，本测试必须红。
func TestWebCrawlEndToEndWritesArtifact(t *testing.T) {
	pages := map[string]string{
		"https://example.com/": `<html><body><main><p>` + strings.Repeat("种子页正文。", 30) +
			`</p><a href="/a">A</a></main></body></html>`,
		"https://example.com/a": `<html><body><main><p>` + strings.Repeat("A 页正文。", 30) +
			`</p></main></body></html>`,
	}
	doer := func(req *http.Request) (*http.Response, error) {
		body, ok := pages["https://example.com"+req.URL.Path]
		if !ok {
			return crawlTestResponse(404, "text/plain", "not found"), nil
		}
		return crawlTestResponse(200, "text/html; charset=utf-8", body), nil
	}

	store := artifact.NewStore(t.TempDir(), "sess-e2e", artifact.Options{
		Now:         func() int64 { return 1700000000000 },
		IDGenerator: func() string { return "e2e00001" },
	})
	tool := WebCrawlWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(nil, &CallParams{
		Input:         map[string]any{"url": "https://example.com/"},
		ArtifactStore: store,
	})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("应成功，实得 %q", r.Content)
	}

	// ★ 接线生效的判定：摘要里**真的**有 artifact 注记
	if !strings.Contains(r.Content, "完整内容已存 artifact：") {
		t.Errorf("Execute 应把 artifact 注记拼进摘要（这是本刀要修的核心），实得：\n%s", r.Content)
	}
	// 且 store 里真的有东西
	if len(store.List()) != 1 {
		t.Errorf("Execute 应落盘 1 条 artifact，实得 %d", len(store.List()))
	}
	// 摘要仍保留原有的结构（首行「爬取完成」+「页面清单」段）
	if !strings.Contains(r.Content, "爬取完成：") {
		t.Errorf("原有摘要首行不该被破坏，实得：\n%s", r.Content)
	}
	if !strings.Contains(r.Content, "页面清单：") {
		t.Errorf("原有页面清单不该被破坏，实得：\n%s", r.Content)
	}
	// 注记应在**末尾**（对账 TS：`lines.join('\n') + artifactNote`）
	if !strings.HasSuffix(strings.TrimRight(r.Content, "\n"), "（可用 read_section 分页细读）") {
		t.Errorf("artifact 注记应在摘要末尾，实得：\n%s", r.Content)
	}
}

// TestWebCrawlEndToEndWithoutStoreStillWorks —— 没配 Store 时**照常工作**。
//
// 回归钉子：接线不能让「不配 artifact 的调用方」失效。
func TestWebCrawlEndToEndWithoutStoreStillWorks(t *testing.T) {
	doer := func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/" {
			return crawlTestResponse(200, "text/html; charset=utf-8",
				`<html><body><main><p>`+strings.Repeat("正文。", 30)+`</p></main></body></html>`), nil
		}
		return crawlTestResponse(404, "text/plain", "not found"), nil
	}
	tool := WebCrawlWithDeps(t.TempDir(),
		tnet.FetchCoreDeps{
			Lookup: func(host string) (tnet.ResolvedAddress, error) {
				return tnet.ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
			},
			Doer: doer,
		},
		tnet.FetchMarkdownOptions{},
	)

	r, err := tool.Execute(nil, &CallParams{Input: map[string]any{"url": "https://example.com/"}})
	if err != nil || r.IsError {
		t.Fatalf("无 Store 应正常工作：err=%v content=%q", err, r.Content)
	}
	if strings.Contains(r.Content, "artifact") {
		t.Errorf("无 Store 时不该出现 artifact 注记，实得：\n%s", r.Content)
	}
}

// ── 类型去重验证 ────────────────────────────────────────────────────────

// TestBuildCrawlArtifactUsesSharedSectionType —— 本地冗余类型已删除。
//
// 第九十八刀把本地 `artifactSection` 换成 `artifact.ArtifactSection`
// （字段完全一致：Name/LineStart/LineEnd/CharCount）——
// 避免两套定义漂移。本测试钉住「用共享类型」这一事实。
func TestBuildCrawlArtifactUsesSharedSectionType(t *testing.T) {
	pages := []tnet.CrawlPage{
		{URL: "https://example.com/", Markdown: "一页"},
		{URL: "https://example.com/b", Markdown: "二页"},
	}
	raw, sections := buildCrawlArtifact(pages)

	if raw == "" {
		t.Fatal("不应为空")
	}
	// 类型断言：编译期已保证是 []artifact.ArtifactSection，此处验证内容
	if len(sections) != 2 {
		t.Fatalf("应有 2 个 section，实得 %d", len(sections))
	}
	var _ []artifact.ArtifactSection = sections
	if sections[0].Name != "https://example.com/" {
		t.Errorf("section 名应为页 URL，实得 %q", sections[0].Name)
	}
	if sections[0].LineStart != 1 {
		t.Errorf("首个 section 应从第 1 行起，实得 %d", sections[0].LineStart)
	}
	// 第二段应接在第一段之后
	if sections[1].LineStart <= sections[0].LineEnd {
		t.Errorf("section 应连续不重叠：第一段 %d-%d，第二段起于 %d",
			sections[0].LineStart, sections[0].LineEnd, sections[1].LineStart)
	}
}
