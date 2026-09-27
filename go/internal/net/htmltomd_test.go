package net

import (
	"strings"
	"testing"
)

// htmltomd_test.go —— HTML → Markdown 转换（第八十九刀 · W3-3）。
//
// 对账 TS `src/tools/web-fetch/extract.ts`（339 行）。
//
// # 依赖决策（已用探针验证）
//
// TS 用 `turndown`（npm，4721 行）做转换。Go 侧选 `golang.org/x/net/html`
// 做解析 + **自建转换器**（而非引入 turndown 的 Go 等价物）——理由：
//   - `x/net/html` 是官方维护的 HTML5 解析器，已在 modcache（无需新下载）
//   - 转换规则（黑名单/图片修复/链接绝对化/后处理）是**本项目特有**的
//     （对账 firecrawl 的实战清单），不是通用库能提供的
//
// # 分层（对账 TS 的四层）
//
//  1. extractMainContent —— 正向提取 `<main>`/`<article>`
//  2. 转换规则层 —— 黑名单清除 / 图片修复 / 链接绝对化
//  3. postProcessMarkdown —— 链接内换行转义 / 删 skip-to-content / 折叠空行
//  4. htmlToMarkdownSmart —— 主内容提空（< 200 字符）自动回退全量

// ── 基础转换 ────────────────────────────────────────────────────────────

// TestHTMLToMarkdownBasic —— 标题/段落/强调/代码。
func TestHTMLToMarkdownBasic(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<h1>一级标题</h1>
<h2>二级标题</h2>
<p>普通段落</p>
<strong>粗体</strong>
<em>斜体</em>
<code>行内代码</code>
<pre><code>代码块</code></pre>
<ul><li>项目一</li><li>项目二</li></ul>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	for _, want := range []string{
		"# 一级标题", "## 二级标题", "普通段落",
		"**粗体**", "*斜体*", "`行内代码`",
		"```\n代码块\n```", "项目一", "项目二",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("应含 %q，实得：\n%s", want, md)
		}
	}
}

// TestHTMLToMarkdownStripsNoise —— 无条件剥离 script/style/svg/iframe/head。
func TestHTMLToMarkdownStripsNoise(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><head><title>T</title></head><body>
<script>alert(1)</script>
<style>.x{color:red}</style>
<svg><circle/></svg>
<iframe src="x"></iframe>
<noscript>ns</noscript>
<p>真内容</p>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	for _, bad := range []string{"alert(1)", "color:red", "circle", "iframe", "noscript"} {
		if strings.Contains(md, bad) {
			t.Errorf("不应含噪声 %q，实得：\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "真内容") {
		t.Errorf("应保留真内容，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownExcludeNonMain —— onlyMainContent 黑名单清除。
//
// 对账 TS `EXCLUDE_NON_MAIN_SELECTORS`（firecrawl 实战清单）。
func TestHTMLToMarkdownExcludeNonMain(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<header>页头</header>
<nav>导航</nav>
<aside class="sidebar">侧栏</aside>
<footer>页脚</footer>
<div class="ad">广告</div>
<div class="social">社交</div>
<div class="cookie">Cookie 提示</div>
<main>正文内容</main>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	for _, bad := range []string{"页头", "导航", "侧栏", "页脚", "广告", "社交", "Cookie 提示"} {
		if strings.Contains(md, bad) {
			t.Errorf("黑名单内容 %q 应被清除，实得：\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "正文内容") {
		t.Errorf("应保留正文，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownForceInclude —— `#main`/`#content` 子树豁免黑名单。
//
// # 语义说明（重要，易误解）
//
// TS 的规则是 `node.matches(excludeSel) && !forceInclude.some(sel => node.closest(sel))`。
// turndown 命中 blacklist 时**直接返回 replacement（空串）且不遍历子树**——
// 故 `<nav><div id="main">X</div></nav>` 里的 **`<nav>` 自己**命中黑名单，
// 而它的 `closest('#main')` 是 `null`（`#main` 是它的**后代**不是祖先）→
// **整棵 `<nav>` 子树被删**，`#main` 也救不回来。
//
// 正确的豁免方向是反的：`#main` 在**外层**时，其内部的 `<nav>` 才被豁免
// （因为 `<nav>.closest('#main')` 命中）。
func TestHTMLToMarkdownForceInclude(t *testing.T) {
	// ① 反向：`#main` 在**外层**，内部 `<nav>` 被豁免
	md, err := HTMLToMarkdown(`<html><body>
<div id="main"><nav>内部导航（应豁免）</nav>正文</div>
<div class="sidebar">应清除</div>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "内部导航") {
		t.Errorf("`#main` 内的 nav 应豁免，实得：\n%s", md)
	}
	if !strings.Contains(md, "正文") {
		t.Errorf("应保留正文，实得：\n%s", md)
	}
	if strings.Contains(md, "应清除") {
		t.Errorf("普通 sidebar 应清除，实得：\n%s", md)
	}

	// ② 正向：`<nav>` 在**外层**，内部 `#main` **不能**豁免（整棵 nav 被删）
	md2, err := HTMLToMarkdown(`<html><body>
<nav><div id="main">被 nav 吞掉</div></nav>
<p>正文二</p>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md2, "被 nav 吞掉") {
		t.Errorf("外层 nav 命中黑名单时应整棵删除（closest 查祖先不含后代），实得：\n%s", md2)
	}
	if !strings.Contains(md2, "正文二") {
		t.Errorf("应保留 nav 外的正文，实得：\n%s", md2)
	}
}

// TestHTMLToMarkdownContentIdExempt —— `#content` 同样豁免其内部的黑名单标签。
func TestHTMLToMarkdownContentIdExempt(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<div id="content"><aside>侧栏（应豁免）</aside>主内容</div>
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "侧栏") {
		t.Errorf("`#content` 内的 aside 应豁免，实得：\n%s", md)
	}
	if !strings.Contains(md, "主内容") {
		t.Errorf("应保留主内容，实得：\n%s", md)
	}
}

// ── 链接绝对化 ──────────────────────────────────────────────────────────

// TestHTMLToMarkdownLinkAbsolutize —— 相对链接按 base 绝对化。
func TestHTMLToMarkdownLinkAbsolutize(t *testing.T) {
	md, err := HTMLToMarkdown(
		`<html><body><a href="/a/b">链接</a><a href="c">相对</a></body></html>`,
		HTMLToMarkdownOptions{PageURL: "https://example.com/dir/page"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "[链接](https://example.com/a/b)") {
		t.Errorf("根相对链接应绝对化，实得：\n%s", md)
	}
	if !strings.Contains(md, "[相对](https://example.com/dir/c)") {
		t.Errorf("文档相对链接应绝对化，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownBaseHrefPriority —— `<base href>` **优先于** PageURL。
func TestHTMLToMarkdownBaseHrefPriority(t *testing.T) {
	html := `<html><head><base href="https://base.example/x/"></head><body>
<a href="y">链接</a></body></html>`
	md, err := HTMLToMarkdown(html, HTMLToMarkdownOptions{PageURL: "https://page.example/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "https://base.example/x/y") {
		t.Errorf("应优先用 base href，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownAnchorAndProtocolLinks —— 锚点/协议链接还原为纯文本。
func TestHTMLToMarkdownAnchorAndProtocolLinks(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<a href="#sec">锚点</a>
<a href="mailto:x@y.com">邮件</a>
<a href="javascript:void(0)">JS</a>
<a href="tel:+123">电话</a>
</body></html>`, HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md, "mailto:") || strings.Contains(md, "javascript:") || strings.Contains(md, "tel:") {
		t.Errorf("协议链接不应保留为链接，实得：\n%s", md)
	}
	if !strings.Contains(md, "锚点") || !strings.Contains(md, "邮件") {
		t.Errorf("应保留链接文本，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownSkipToContentDropped —— skip-to-content 无障碍链接被丢弃。
//
// 对账 TS：`href?.startsWith('#') && /skip to/i.test(text)` → 返回空。
func TestHTMLToMarkdownSkipToContentDropped(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<a href="#main">Skip to main content</a>
<p>正文</p>
</body></html>`, HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(strings.ToLower(md), "skip to") {
		t.Errorf("skip-to-content 应被丢弃，实得：\n%s", md)
	}
	if !strings.Contains(md, "正文") {
		t.Errorf("应保留正文，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownLinkTitle —— 有 title 属性时保留。
func TestHTMLToMarkdownLinkTitle(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body><a href="/x" title="提示">文本</a></body></html>`,
		HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, `[文本](https://example.com/x "提示")`) {
		t.Errorf("应保留 title，实得：\n%s", md)
	}
}

// ── 图片修复 ────────────────────────────────────────────────────────────

// TestHTMLToMarkdownImageAbsolute —— 图片链接绝对化。
func TestHTMLToMarkdownImageAbsolute(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body><img src="/img/a.png" alt="图"></body></html>`,
		HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "![图](https://example.com/img/a.png)") {
		t.Errorf("图片应绝对化，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownBase64ImageRemoved —— base64 图片替换为占位符。
func TestHTMLToMarkdownBase64ImageRemoved(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<img src="data:image/png;base64,iVBORw0KGgo=" alt="x">
</body></html>`, HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md, "iVBORw0KGgo") {
		t.Errorf("base64 数据不应出现，实得：\n%s", md)
	}
	if !strings.Contains(md, "Base64-Image-Removed") {
		t.Errorf("应含占位符，实得：\n%s", md)
	}
}

// TestHTMLToMarkdownAltBracketsStripped —— alt 里的方括号被剥离（防 markdown 断裂）。
func TestHTMLToMarkdownAltBracketsStripped(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body><img src="/i.png" alt="a[b]c"></body></html>`,
		HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md, "a[b]c") {
		t.Errorf("alt 里的方括号应剥离，实得：\n%s", md)
	}
	if !strings.Contains(md, "![abc]") {
		t.Errorf("应为 ![abc]，实得：\n%s", md)
	}
}

// ── srcset 最大档 ───────────────────────────────────────────────────────

// TestPickBestSrcsetCandidate —— srcset 取最大档（对账 TS 评分逻辑）。
func TestPickBestSrcsetCandidate(t *testing.T) {
	cases := []struct {
		name   string
		srcset string
		src    string
		want   string
	}{
		{"w 描述符取最大", "a.png 100w, b.png 2000w, c.png 500w", "", "b.png"},
		{"x 描述符 ×1000", "a.png 1x, b.png 2x", "", "b.png"},
		{"w 优先于 x", "a.png 3000w, b.png 2x", "", "a.png"},
		{"无描述符时把 src 纳入竞争", "a.png, b.png", "orig.png", "orig.png"},
		{"低分 w 触发 src 竞争", "a.png 500w", "orig.png", "orig.png"},
		{"高分 w 不触发 src 竞争", "a.png 2000w", "orig.png", "a.png"},
		// 空 srcset → 返回空。**契约说明**：TS 的 `pickImageSource` 先判
		// `if (srcset)` 才调用本函数，故空输入不进该函数；本函数对空输入
		// 返回空是**正确的**（调用方负责回退到 src）。
		{"空 srcset 返回空", "", "only.png", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickBestSrcsetCandidate(c.srcset, c.src)
			if got != c.want {
				t.Errorf("pickBestSrcsetCandidate(%q, %q) = %q，期望 %q",
					c.srcset, c.src, got, c.want)
			}
		})
	}
}

// TestHTMLToMarkdownSrcsetBest —— 转换时 srcset 生效。
func TestHTMLToMarkdownSrcsetBest(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body>
<img src="/small.png" srcset="/s.png 100w, /big.png 2000w" alt="x">
</body></html>`, HTMLToMarkdownOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "https://example.com/big.png") {
		t.Errorf("应取 srcset 最大档，实得：\n%s", md)
	}
}

// ── 后处理 ──────────────────────────────────────────────────────────────

// TestHTMLToMarkdownCollapseBlankLines —— 折叠 3+ 连续换行。
func TestHTMLToMarkdownCollapseBlankLines(t *testing.T) {
	md, err := HTMLToMarkdown(`<html><body><p>a</p><div></div><div></div><div></div><p>b</p></body></html>`,
		HTMLToMarkdownOptions{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md, "\n\n\n") {
		t.Errorf("不应有 3+ 连续换行，实得：\n%q", md)
	}
}

// ── 主内容提取 ──────────────────────────────────────────────────────────

// TestExtractMainContent —— 优先 `<main>`，其次 `<article>`。
func TestExtractMainContent(t *testing.T) {
	// main 优先
	html := `<html><body><article>文章</article><main>主区</main></body></html>`
	got := extractMainContent(html)
	if !strings.Contains(got, "主区") {
		t.Errorf("应优先 main，实得：%s", got)
	}
	if strings.Contains(got, "文章") {
		t.Errorf("main 存在时不应取 article，实得：%s", got)
	}

	// 无 main → article
	html2 := `<html><body><div>外</div><article>文章内容</article></body></html>`
	got = extractMainContent(html2)
	if !strings.Contains(got, "文章内容") {
		t.Errorf("无 main 应取 article，实得：%s", got)
	}

	// 都无 → 全量（但剥离噪声标签）
	html3 := `<html><body><nav>导航</nav><p>正文</p></body></html>`
	got = extractMainContent(html3)
	if strings.Contains(got, "导航") {
		t.Errorf("应剥离 nav，实得：%s", got)
	}
	if !strings.Contains(got, "正文") {
		t.Errorf("应保留正文，实得：%s", got)
	}
}

// TestExtractMainContentNested —— 嵌套 `<main>` 深度匹配。
func TestExtractMainContentNested(t *testing.T) {
	html := `<body><main>外层<div><main>内层</main></div>尾部</main></body>`
	got := extractMainContent(html)
	if !strings.Contains(got, "外层") || !strings.Contains(got, "内层") || !strings.Contains(got, "尾部") {
		t.Errorf("嵌套 main 应完整提取，实得：%s", got)
	}
}

// ── smart 回退 ──────────────────────────────────────────────────────────

// TestHTMLToMarkdownSmartFallback —— 主内容过薄（< 200 字符）回退全量。
//
// 对账 TS `htmlToMarkdownSmart`——SPA div 布局没有 `<main>`/`<article>` 时
// 正向提取必然提空。
func TestHTMLToMarkdownSmartFallback(t *testing.T) {
	// 有 main 但内容极短 → 回退全量（能取到 main 外的长内容）
	longBody := strings.Repeat("这是一段足够长的正文内容。", 30)
	html := `<html><body><main>短</main><div id="content">` + longBody + `</div></body></html>`

	md, err := HTMLToMarkdownSmart(html, SmartConvertOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "这是一段足够长的正文内容") {
		t.Errorf("过薄时应回退全量转换，实得：\n%s", md)
	}

	// 主内容充足 → 不包含 main 外的内容
	richMain := strings.Repeat("主区内容。", 50)
	html2 := `<html><body><main>` + richMain + `</main><div>外部不该出现</div></body></html>`
	md2, err := HTMLToMarkdownSmart(html2, SmartConvertOptions{PageURL: "https://example.com/"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(md2, "外部不该出现") {
		t.Errorf("主内容充足时不应回退全量，实得：\n%s", md2)
	}
}

// TestHTMLToMarkdownSmartDisable —— `OnlyMainContent=false` 时不做正向提取。
func TestHTMLToMarkdownSmartDisable(t *testing.T) {
	html := `<html><body><main>短</main><div>外部内容</div></body></html>`
	md, err := HTMLToMarkdownSmart(html, SmartConvertOptions{
		PageURL: "https://example.com/", OnlyMainContent: BoolPtr(false),
	})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if !strings.Contains(md, "外部内容") {
		t.Errorf("关闭主内容提取时应含全量，实得：\n%s", md)
	}
}

// ── 常量对账 ────────────────────────────────────────────────────────────

// TestMinSubstantialLength —— 阈值逐字对账 TS。
func TestMinSubstantialLength(t *testing.T) {
	if minSubstantialLength != 200 {
		t.Errorf("阈值应为 200，实得 %d", minSubstantialLength)
	}
}

// TestExcludeSelectorsCoverage —— 黑名单选择器表完整（对账 TS 清单）。
func TestExcludeSelectorsCoverage(t *testing.T) {
	// TS 清单里的标签类
	for _, tag := range []string{"header", "footer", "nav", "aside"} {
		found := false
		for _, s := range excludeNonMainSelectors {
			if s == tag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("黑名单应含标签 %q", tag)
		}
	}
	// 关键类名
	for _, cls := range []string{".sidebar", ".ad", ".cookie", ".social"} {
		found := false
		for _, s := range excludeNonMainSelectors {
			if s == cls {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("黑名单应含类名 %q", cls)
		}
	}
	// force-include
	if len(forceIncludeSelectors) != 2 ||
		forceIncludeSelectors[0] != "#main" || forceIncludeSelectors[1] != "#content" {
		t.Errorf("forceInclude 应为 [#main #content]，实得 %#v", forceIncludeSelectors)
	}
}
