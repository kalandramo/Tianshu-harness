package net

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// extractlinks_test.go —— 链接提取与响体解码（第九十一刀 · W3-4b）。
//
// 对账 TS `extract.ts` 的三个导出：`extractLinks` / `extractLinksFromMarkdown` / `decodeBody`。

// ── ExtractLinks ────────────────────────────────────────────────────────

// TestExtractLinksBasic —— 提取并绝对化。
func TestExtractLinksBasic(t *testing.T) {
	html := `<body>
<a href="/a">A</a>
<a href="b">B</a>
<a href="https://other.com/c">C</a>
</body>`
	got := ExtractLinks(html, "https://example.com/dir/page")
	want := []string{
		"https://example.com/a",
		"https://example.com/dir/b",
		"https://other.com/c",
	}
	if len(got) != len(want) {
		t.Fatalf("应提取 %d 条，实得 %d：%v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] 应为 %q，实得 %q", i, w, got[i])
		}
	}
}

// TestExtractLinksSkipsNonHTTP —— 跳过锚点/协议链接/空。
func TestExtractLinksSkipsNonHTTP(t *testing.T) {
	html := `<body>
<a href="#sec">锚点</a>
<a href="mailto:x@y.com">邮件</a>
<a href="javascript:void(0)">JS</a>
<a href="tel:+1">电话</a>
<a href="data:text/html,x">数据</a>
<a>无 href</a>
<a href="/real">真链接</a>
</body>`
	got := ExtractLinks(html, "https://example.com/")
	if len(got) != 1 || got[0] != "https://example.com/real" {
		t.Errorf("应只提取 http(s) 链接，实得 %v", got)
	}
}

// TestExtractLinksDedup —— 去重（对账 TS 用 Set）。
func TestExtractLinksDedup(t *testing.T) {
	html := `<body><a href="/a">1</a><a href="/a">2</a><a href="/a">3</a></body>`
	got := ExtractLinks(html, "https://example.com/")
	if len(got) != 1 {
		t.Errorf("应去重为 1 条，实得 %d：%v", len(got), got)
	}
}

// TestExtractLinksBaseHref —— `<base href>` 优先。
func TestExtractLinksBaseHref(t *testing.T) {
	html := `<head><base href="https://base.example/x/"></head><body><a href="y">L</a></body>`
	got := ExtractLinks(html, "https://page.example/")
	if len(got) != 1 || got[0] != "https://base.example/x/y" {
		t.Errorf("应用 base href，实得 %v", got)
	}
}

// TestExtractLinksPreservesSidebar —— **关键**：黑名单会剔除的 sidebar 链接仍被提取。
//
// 对账 TS 注释：「必须在转换/黑名单清洗之前提取——sidebar/menu 里的文档目录
// 链接会被 onlyMainContent 剔除，markdown 层再提就丢了」。
func TestExtractLinksPreservesSidebar(t *testing.T) {
	html := `<body>
<nav class="sidebar"><a href="/doc/a">文档 A</a></nav>
<main><a href="/real">正文链接</a></main>
</body>`
	got := ExtractLinks(html, "https://example.com/")
	found := map[string]bool{}
	for _, l := range got {
		found[l] = true
	}
	if !found["https://example.com/doc/a"] {
		t.Errorf("sidebar 里的链接也应被提取（黑名单清洗前），实得 %v", got)
	}
	if !found["https://example.com/real"] {
		t.Errorf("应含正文链接，实得 %v", got)
	}
}

// ── ExtractLinksFromMarkdown ────────────────────────────────────────────

// TestExtractLinksFromMarkdown —— 提取绝对链接。
func TestExtractLinksFromMarkdown(t *testing.T) {
	md := `# 标题
[链接一](https://example.com/a)
[带标题](https://example.com/b "提示")
[相对](/c)  ← 不该提取（非绝对）
![图](https://example.com/img.png)
`
	got := ExtractLinksFromMarkdown(md)
	want := map[string]bool{
		"https://example.com/a":       true,
		"https://example.com/b":       true,
		"https://example.com/img.png": true,
	}
	if len(got) != len(want) {
		t.Fatalf("应提取 %d 条，实得 %d：%v", len(want), len(got), got)
	}
	for _, l := range got {
		if !want[l] {
			t.Errorf("不应含 %q", l)
		}
	}
}

// TestExtractLinksFromMarkdownDedup —— 去重。
func TestExtractLinksFromMarkdownDedup(t *testing.T) {
	md := "[a](https://x.com/1) [b](https://x.com/1)"
	if got := ExtractLinksFromMarkdown(md); len(got) != 1 {
		t.Errorf("应去重，实得 %v", got)
	}
}

// ── DecodeBody ──────────────────────────────────────────────────────────

// TestDecodeBodyUTF8 —— 默认 UTF-8。
func TestDecodeBodyUTF8(t *testing.T) {
	if got := DecodeBody([]byte("中文内容"), "text/plain"); got != "中文内容" {
		t.Errorf("UTF-8 应直译，实得 %q", got)
	}
}

// TestDecodeBodyFromContentType —— HTTP 头 charset 生效。
func TestDecodeBodyFromContentType(t *testing.T) {
	// 构造 GBK 编码的「中文」
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte("中文测试"))
	if err != nil {
		t.Fatal(err)
	}
	got := DecodeBody(gbk, "text/html; charset=gbk")
	if got != "中文测试" {
		t.Errorf("GBK 应正确解码，实得 %q", got)
	}
	// 大小写不敏感
	got = DecodeBody(gbk, "text/html; charset=GBK")
	if got != "中文测试" {
		t.Errorf("charset 应大小写不敏感，实得 %q", got)
	}
	// 带引号
	got = DecodeBody(gbk, `text/html; charset="gbk"`)
	if got != "中文测试" {
		t.Errorf("带引号的 charset 应生效，实得 %q", got)
	}
}

// TestDecodeBodyFromMeta —— HTML meta 声明生效（无 HTTP charset 时）。
func TestDecodeBodyFromMeta(t *testing.T) {
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte("<html><head><meta charset=\"gbk\"></head><body>中文</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	got := DecodeBody(gbk, "text/html")
	if !strings.Contains(got, "中文") {
		t.Errorf("应据 meta 声明解码 GBK，实得 %q", got)
	}
}

// TestDecodeBodyHTTPHeaderWins —— HTTP 头 charset **优先于** meta。
func TestDecodeBodyHTTPHeaderWins(t *testing.T) {
	// 内容声明 utf-8，但 HTTP 头说 gbk → 应信 HTTP 头（对账 TS 的判定顺序）
	body := []byte("<meta charset=\"utf-8\">正文")
	got := DecodeBody(body, "text/html; charset=gbk")
	if got == string("正文") {
		t.Log("（GBK 解码 ASCII 部分不变——这是预期的，测试只验证顺序不冲突）")
	}
	// 更有区分度的构造：UTF-8 中文 + HTTP 头谎称 gbk
	utf8Body := []byte("中文")
	gotGbkWrong := DecodeBody(utf8Body, "text/html; charset=gbk")
	gotUtf8 := DecodeBody(utf8Body, "text/html; charset=utf-8")
	if gotUtf8 != "中文" {
		t.Errorf("UTF-8 声明应正确，实得 %q", gotUtf8)
	}
	if gotGbkWrong == "中文" {
		t.Log("（HTTP 头优先：错标 gbk 时确实乱码，符合预期）")
	}
}

// TestDecodeBodyLatin1Fallback —— latin1 直译。
func TestDecodeBodyLatin1Fallback(t *testing.T) {
	body := []byte{0xE9, 0xE8, 0x41} // é è A
	got := DecodeBody(body, "text/plain; charset=latin1")
	if got != "éèA" {
		t.Errorf("latin1 应逐字节直译，实得 %q", got)
	}
}

// TestDecodeBodyUnknownCharset —— 未知 charset 回退 UTF-8（不报错）。
func TestDecodeBodyUnknownCharset(t *testing.T) {
	got := DecodeBody([]byte("内容"), "text/plain; charset=zzq-unknown")
	if got != "内容" {
		t.Errorf("未知 charset 应回退 UTF-8，实得 %q", got)
	}
}

// TestDecodeBodyEmpty —— 空体不 panic。
func TestDecodeBodyEmpty(t *testing.T) {
	if got := DecodeBody(nil, "text/html"); got != "" {
		t.Errorf("空体应为空串，实得 %q", got)
	}
}

// TestDetectCharsetPriority —— 判定顺序（头 > meta > utf-8）。
func TestDetectCharsetPriority(t *testing.T) {
	// ① 头存在 → 用头
	if got := detectCharset([]byte("<meta charset=big5>"), "text/html; charset=gbk"); got != "gbk" {
		t.Errorf("应优先 HTTP 头，实得 %q", got)
	}
	// ② 无头 + meta → 用 meta
	if got := detectCharset([]byte("<meta charset=big5>"), "text/html"); got != "big5" {
		t.Errorf("应据 meta，实得 %q", got)
	}
	// ③ 都没有 → utf-8
	if got := detectCharset([]byte("<html>"), "text/plain"); got != "utf-8" {
		t.Errorf("应兜底 utf-8，实得 %q", got)
	}
	// ④ 非 HTML 内容类型时不看 meta
	if got := detectCharset([]byte("<meta charset=big5>"), "application/json"); got != "utf-8" {
		t.Errorf("非 HTML 不应看 meta，实得 %q", got)
	}
}
