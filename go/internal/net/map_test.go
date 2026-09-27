package net

import "testing"

// map_test.go —— web_map 的 URL 发现逻辑（第九十六刀 · W3-5c）。
//
// 对账 TS `src/tools/web-crawl/map.ts`（85 行）。

func TestIsSameOrSubDomain(t *testing.T) {
	cases := []struct {
		host, seed string
		sub        bool
		want       bool
	}{
		{"example.com", "example.com", false, true},
		{"example.com", "example.com", true, true},
		{"sub.example.com", "example.com", false, false}, // 不允许子域时拒绝
		{"sub.example.com", "example.com", true, true},   // 允许时通过
		{"deep.sub.example.com", "example.com", true, true},
		{"other.com", "example.com", true, false}, // 完全异域
		// **边界**：`notexample.com` 不应被判为 `example.com` 的子域
		{"notexample.com", "example.com", true, false},
		// **边界**：`.example.com` 前缀形式（hostname 不会以此开头，但防御）
		{"xexample.com", "example.com", true, false},
	}
	for _, c := range cases {
		if got := IsSameOrSubDomain(c.host, c.seed, c.sub); got != c.want {
			t.Errorf("IsSameOrSubDomain(%q, %q, %v) = %v，期望 %v",
				c.host, c.seed, c.sub, got, c.want)
		}
	}
}

func TestFilterByPathPrefix(t *testing.T) {
	// 种子在根 → 全部通过
	if !FilterByPathPrefix("https://example.com/anything", "/") {
		t.Error("根路径种子应全部通过")
	}
	if !FilterByPathPrefix("https://example.com/anything", "") {
		t.Error("空路径应全部通过")
	}
	// 种子在子目录 → 只保留前缀下
	if !FilterByPathPrefix("https://example.com/docs/a", "/docs") {
		t.Error("/docs 下应通过")
	}
	if !FilterByPathPrefix("https://example.com/docs/", "/docs") {
		t.Error("/docs/ 自身应通过")
	}
	if FilterByPathPrefix("https://example.com/other", "/docs") {
		t.Error("/other 应被拒")
	}
	// 已带尾斜杠的种子
	if !FilterByPathPrefix("https://example.com/docs/x", "/docs/") {
		t.Error("带尾斜杠种子应一致")
	}
	// 非法 URL → false（对账 TS 的 try/catch）
	if FilterByPathPrefix("://bad", "/docs") {
		t.Error("非法 URL 应返回 false")
	}
}

func TestMapCollectorMerge(t *testing.T) {
	c := NewMapCollector()
	c.Add("https://example.com/a", "sitemap", "")
	c.Add("https://example.com/a", "page", "标题A")   // 合并 + 补 title
	c.Add("https://example.com/a", "search", "标题B") // title 不覆盖

	list := c.List()
	if len(list) != 1 {
		t.Fatalf("同 URL 应合并为 1 条，实得 %d", len(list))
	}
	it := list[0]
	if !it.Sources["sitemap"] || !it.Sources["page"] || !it.Sources["search"] {
		t.Errorf("三个来源都应记录，实得 %v", it.Sources)
	}
	if it.Title != "标题A" {
		t.Errorf("title 应保留首个非空值（不覆盖），实得 %q", it.Title)
	}
}

func TestMapCollectorOrder(t *testing.T) {
	c := NewMapCollector()
	c.Add("https://example.com/z", "page", "")
	c.Add("https://example.com/a", "page", "")
	c.Add("https://example.com/m", "page", "")
	// **保持插入序**（对账 TS 的 Map 迭代序）
	got := c.List()
	want := []string{"https://example.com/z", "https://example.com/a", "https://example.com/m"}
	for i := range want {
		if got[i].URL != want[i] {
			t.Errorf("[%d] 应为 %q，实得 %q", i, want[i], got[i].URL)
		}
	}
}

func TestTokenize(t *testing.T) {
	// 长度 < 2 的 token 丢弃
	got := tokenize("a bb ccc")
	if _, has := got["a"]; has {
		t.Error("单字符 token 应被丢弃")
	}
	if got["bb"] != 1 || got["ccc"] != 1 {
		t.Errorf("应保留长度 >= 2 的 token，实得 %v", got)
	}
	// 大小写归一
	got2 := tokenize("ABC abc")
	if got2["abc"] != 2 {
		t.Errorf("应大小写归一，实得 %v", got2)
	}
	// CJK 保留（对账 TS 的 \u4e00-\u9fff 范围）
	got3 := tokenize("中文文档")
	if len(got3) != 1 || got3["中文文档"] != 1 {
		t.Errorf("CJK 应作为整体 token，实得 %v", got3)
	}
	// 标点作分隔符
	got4 := tokenize("hello,world")
	if got4["hello"] != 1 || got4["world"] != 1 {
		t.Errorf("标点应分隔，实得 %v", got4)
	}
}

func TestCosineSim(t *testing.T) {
	a := map[string]int{"doc": 1, "guide": 1}
	// 完全相同 → 1.0
	if s := cosineSim(a, a); s < 0.999 {
		t.Errorf("相同向量应 1.0，实得 %v", s)
	}
	// 完全无关 → 0
	if s := cosineSim(a, map[string]int{"other": 1}); s != 0 {
		t.Errorf("无关向量应 0，实得 %v", s)
	}
	// 空向量 → 0（对账 TS 的 normA===0 检查，避免除零）
	if s := cosineSim(map[string]int{}, a); s != 0 {
		t.Errorf("空向量应 0，实得 %v", s)
	}
	if s := cosineSim(a, map[string]int{}); s != 0 {
		t.Errorf("空查询向量应 0，实得 %v", s)
	}
}

func TestRerankByCosine(t *testing.T) {
	items := []MapCandidate{
		{URL: "https://example.com/unrelated"},
		{URL: "https://example.com/docs/guide"}, // 与查询词匹配
		{URL: "https://example.com/another"},
	}
	got := RerankByCosine(items, "docs guide")
	// 匹配项应排首位
	if got[0].URL != "https://example.com/docs/guide" {
		t.Errorf("匹配项应排首位，实得 %v", urlsOf(got))
	}
	// **零分项保持原相对顺序**（稳定排序）
	if got[1].URL != "https://example.com/unrelated" || got[2].URL != "https://example.com/another" {
		t.Errorf("零分项应保持原序，实得 %v", urlsOf(got))
	}
}

func TestRerankByCosineEmptySearch(t *testing.T) {
	items := []MapCandidate{{URL: "https://a.com/1"}, {URL: "https://a.com/2"}}
	got := RerankByCosine(items, "")
	// 空查询 → 全零分 → 保持原序
	if got[0].URL != items[0].URL || got[1].URL != items[1].URL {
		t.Errorf("空查询应保持原序，实得 %v", urlsOf(got))
	}
}

func urlsOf(items []MapCandidate) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.URL
	}
	return out
}
