package net

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// htmltomd.go —— HTML → Markdown 转换（第八十九刀 · W3-3）。
//
// 对账 TS `src/tools/web-fetch/extract.ts`（339 行）。
//
// # 依赖决策
//
// TS 用 `turndown`（npm，4721 行）。Go 侧选 `golang.org/x/net/html` 做解析 +
// **自建转换器**——理由：
//   - `x/net/html` 是官方 HTML5 解析器，已在 modcache
//   - 转换规则（黑名单/图片修复/链接绝对化/后处理）是**本项目特有**的
//     （对账 firecrawl 实战清单），通用库提供不了
//
// # 分层（对账 TS 的四层）
//
//  1. `extractMainContent` —— 正向提取 `<main>`/`<article>`
//  2. 转换规则 —— 黑名单清除 / 图片修复 / 链接绝对化
//  3. `postProcessMarkdown` —— 链接内换行转义 / 删 skip-to-content / 折叠空行
//  4. `HTMLToMarkdownSmart` —— 主内容提空（< 200 字符）自动回退全量

// minSubstantialLength 是实质内容最小长度（对账 TS `MIN_SUBSTANTIAL_LENGTH`）。
//
// 低于此视为提取失败/错误壳（与 isJinaQualityHeuristic 同阈值）。
const minSubstantialLength = 200

// ── A1: onlyMainContent 黑名单（对账 TS `EXCLUDE_NON_MAIN_SELECTORS`）─────

var excludeNonMainSelectors = []string{
	"header", "footer", "nav", "aside",
	".header", ".top", ".navbar", "#header", ".footer", ".bottom", "#footer",
	".sidebar", ".side", ".aside", "#sidebar",
	".modal", ".popup", "#modal", ".overlay",
	".ad", ".ads", ".advert", "#ad",
	".lang-selector", ".language", "#language-selector",
	".social", ".social-media", ".social-links", "#social",
	".menu", ".navigation", "#nav",
	".breadcrumbs", "#breadcrumbs",
	".share", "#share",
	".widget", "#widget",
	".cookie", "#cookie",
}

// forceIncludeSelectors 对账 TS `FORCE_INCLUDE_SELECTORS`——子树豁免黑名单。
var forceIncludeSelectors = []string{"#main", "#content"}

// ── 选项 ────────────────────────────────────────────────────────────────

// HTMLToMarkdownOptions 对账 TS `HtmlToMarkdownOptions`。
type HTMLToMarkdownOptions struct {
	// PageURL：链接/图片绝对化的回退 base（`<base href>` 优先于它）。
	PageURL string
}

// SmartConvertOptions 对账 TS `SmartConvertOptions`。
type SmartConvertOptions struct {
	HTMLToMarkdownOptions
	// OnlyMainContent：主内容提取开关（对账 `config fetch.extractMainContent`，
	// 默认 true）。**用指针**以区分「未设置」（默认 true）与「显式 false」。
	OnlyMainContent *bool
}

// BoolPtr 返回 *bool（供设置 SmartConvertOptions.OnlyMainContent）。
func BoolPtr(v bool) *bool { return &v }

// ── 公开 API ────────────────────────────────────────────────────────────

// HTMLToMarkdown 把 HTML 转为 Markdown。
//
// 对账 TS `htmlToMarkdown`。
func HTMLToMarkdown(rawHTML string, opts HTMLToMarkdownOptions) (string, error) {
	base := resolveBaseURL(rawHTML, opts.PageURL)
	root, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", err
	}
	conv := &mdConverter{base: base, forceInclude: map[*html.Node]bool{}}
	// 先标注 force-include 子树，避免遍历顺序影响。
	markForceInclude(root, conv.forceInclude)

	var sb strings.Builder
	conv.walk(root, &sb)
	return postProcessMarkdown(sb.String()), nil
}

// HTMLToMarkdownSmart 智能转换：先提主内容，产出过薄则回退全量。
//
// 对账 TS `htmlToMarkdownSmart`——SPA div 布局没有 `<main>`/`<article>` 时
// 正向提取必然提空；黑名单规则在两条路径上都生效。
func HTMLToMarkdownSmart(rawHTML string, opts SmartConvertOptions) (string, error) {
	onlyMain := true
	if opts.OnlyMainContent != nil {
		onlyMain = *opts.OnlyMainContent
	}
	if onlyMain {
		md, err := HTMLToMarkdown(extractMainContent(rawHTML), opts.HTMLToMarkdownOptions)
		if err == nil && len(strings.TrimSpace(md)) >= minSubstantialLength {
			return md, nil
		}
	}
	return HTMLToMarkdown(rawHTML, opts.HTMLToMarkdownOptions)
}

// ── 1. 主内容提取（对账 TS `extractMainContent`）────────────────────────

// mainNoiseTags 对账 TS `NOISE_TAGS`。
var mainNoiseTags = []string{"script", "style", "noscript", "svg", "nav", "header", "footer", "aside"}

// extractMainContent 提取 `<main>` 或 `<article>` 区域，并剥离噪声标签。
//
// 对账 TS `extractMainContent`：优先 main，其次 article，都无则全量。
//
// # 为什么不用单个正则（Go 的限制）
//
// TS 用一个正则 `<(${tags.join('|')})\b[^>]*>[\s\S]*?</\1>` —— **反向引用**
// `\1` 保证开合标签同名。**Go 的 `regexp` 不支持反向引用**（RE2 语义，无回溯），
// 故逐个标签分别剥离（每个标签的正则里名字是字面量，无需反向引用）。
//
// 诚实标注：逐标签剥离与单正则在**嵌套同名标签**时有差异——
// 例如 `<nav>a<nav>b</nav>c</nav>`：JS 的非贪婪 `.*?` 会匹配到**第一个**
// `</nav>`（即 `<nav>a<nav>b</nav>`），留下 `c</nav>`；Go 逐个剥离同样
// 匹配到第一个闭合。**两者一致**（都是非贪婪语义）。
func extractMainContent(rawHTML string) string {
	region, ok := extractRegion(rawHTML, "main")
	if !ok {
		region, ok = extractRegion(rawHTML, "article")
	}
	source := rawHTML
	if ok {
		source = region
	}
	return strings.TrimSpace(stripNoiseTags(source))
}

// noiseTagPatterns 是每个噪声标签的剥离正则（惰性匹配，大小写不敏感）。
//
// **预编译**（包级一次性）——避免每次调用都编译 8 个正则。
var noiseTagPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(mainNoiseTags))
	for _, tag := range mainNoiseTags {
		out = append(out, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`\s*>`))
	}
	return out
}()

// stripNoiseTags 逐标签剥离噪声标签及其内容。
func stripNoiseTags(s string) string {
	for _, re := range noiseTagPatterns {
		s = re.ReplaceAllString(s, "")
	}
	return s
}

// extractRegion 提取指定标签的**最外层**区域内容（深度匹配）。
//
// 对账 TS `extractRegion`：扫描嵌套的开合标签，找配对的闭合位置。
func extractRegion(rawHTML, tag string) (string, bool) {
	openRe := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>`)
	closeRe := regexp.MustCompile(`(?is)</` + tag + `\s*>`)

	open := openRe.FindStringIndex(rawHTML)
	if open == nil {
		return "", false
	}
	start := open[1]

	depth := 1
	idx := start
	for depth > 0 {
		nextOpen := openRe.FindStringIndex(rawHTML[idx:])
		nextClose := closeRe.FindStringIndex(rawHTML[idx:])
		if nextClose == nil {
			return "", false
		}
		absOpen := -1
		if nextOpen != nil {
			absOpen = idx + nextOpen[0]
		}
		absClose := idx + nextClose[0]

		if absOpen >= 0 && absOpen < absClose {
			depth++
			idx = absOpen + (nextOpen[1] - nextOpen[0])
		} else {
			depth--
			if depth == 0 {
				return rawHTML[start:absClose], true
			}
			idx = absClose + (nextClose[1] - nextClose[0])
		}
	}
	return "", false
}

// ── 2. 转换器 ───────────────────────────────────────────────────────────

// mdConverter 承载一次转换的状态（对账 TS「每调用新建实例，并发安全」）。
type mdConverter struct {
	base         string
	forceInclude map[*html.Node]bool
	// listDepth 跟踪有序/无序列表嵌套深度（用于缩进）。
	listStack []string
}

// markForceInclude 标注所有 force-include 子树内的节点。
func markForceInclude(n *html.Node, out map[*html.Node]bool) {
	if n.Type == html.ElementNode {
		for _, sel := range forceIncludeSelectors {
			if matchSimpleSelector(n, sel) {
				markSubtree(n, out)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		markForceInclude(c, out)
	}
}

// markSubtree 把节点及其后代全部标记。
func markSubtree(n *html.Node, out map[*html.Node]bool) {
	out[n] = true
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		markSubtree(c, out)
	}
}

// skipTag 是无条件剥离的标签（对账 TS `td.remove([...])`）。
var skipTag = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"svg": true, "iframe": true, "template": true, "head": true,
}

// walk 遍历并产出 markdown。
//
// **设计**：Go 的 x/net/html 不像 turndown 有「规则引擎」，故这里用
// 显式的 switch 分派——语义等价于 TS 的 `addRule`。
func (c *mdConverter) walk(n *html.Node, sb *strings.Builder) {
	if n.Type == html.ElementNode {
		if skipTag[n.Data] {
			return
		}
		// 黑名单清除（force-include 子树豁免）。
		if c.matchesExclude(n) && !c.forceInclude[n] {
			return
		}
		switch n.Data {
		case "img":
			sb.WriteString(c.renderImage(n))
			return
		case "a":
			sb.WriteString(c.renderLink(n))
			return
		case "base":
			return
		}
	}

	switch n.Type {
	case html.TextNode:
		// 文本原样输出（转义由上下文负责——见 escapeText 的调用点）。
		sb.WriteString(n.Data)
		return
	case html.CommentNode, html.DoctypeNode:
		return
	}

	if n.Type != html.ElementNode {
		for c2 := n.FirstChild; c2 != nil; c2 = c2.NextSibling {
			c.walk(c2, sb)
		}
		return
	}

	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(n.Data[1:])
		sb.WriteString("\n" + strings.Repeat("#", level) + " ")
		c.walkChildren(n, sb)
		sb.WriteString("\n\n")
	case "p":
		sb.WriteString("\n")
		c.walkChildren(n, sb)
		sb.WriteString("\n\n")
	case "br":
		sb.WriteString("  \n")
	case "hr":
		sb.WriteString("\n---\n\n")
	case "strong", "b":
		sb.WriteString("**")
		c.walkChildren(n, sb)
		sb.WriteString("**")
	case "em", "i":
		sb.WriteString("*")
		c.walkChildren(n, sb)
		sb.WriteString("*")
	case "del", "s", "strike":
		sb.WriteString("~~")
		c.walkChildren(n, sb)
		sb.WriteString("~~")
	case "code":
		// 在 <pre> 里的 code 由 pre 处理。
		if n.Parent != nil && n.Parent.Data == "pre" {
			c.walkChildren(n, sb)
			return
		}
		sb.WriteString("`")
		c.walkChildren(n, sb)
		sb.WriteString("`")
	case "pre":
		sb.WriteString("\n```\n")
		c.walkChildren(n, sb)
		sb.WriteString("\n```\n\n")
	case "blockquote":
		inner := &strings.Builder{}
		c.walkChildren(n, inner)
		for _, line := range strings.Split(strings.TrimRight(inner.String(), "\n"), "\n") {
			sb.WriteString("\n> " + line)
		}
		sb.WriteString("\n\n")
	case "ul", "ol":
		c.listStack = append(c.listStack, n.Data)
		sb.WriteString("\n")
		c.walkChildren(n, sb)
		c.listStack = c.listStack[:len(c.listStack)-1]
		sb.WriteString("\n")
	case "li":
		indent := strings.Repeat("  ", maxInt(0, len(c.listStack)-1))
		marker := "- "
		if len(c.listStack) > 0 && c.listStack[len(c.listStack)-1] == "ol" {
			marker = "1. "
		}
		sb.WriteString("\n" + indent + marker)
		c.walkChildren(n, sb)
	case "table":
		c.renderTable(n, sb)
	case "tr", "td", "th", "thead", "tbody", "tfoot", "caption":
		// 由 renderTable 处理；独立出现时当普通容器。
		c.walkChildren(n, sb)
	default:
		c.walkChildren(n, sb)
	}
}

func (c *mdConverter) walkChildren(n *html.Node, sb *strings.Builder) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		c.walk(child, sb)
	}
}

// matchesExclude 判断节点是否命中黑名单选择器（对账 TS `safeMatches`）。
func (c *mdConverter) matchesExclude(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	for _, sel := range excludeNonMainSelectors {
		if matchSimpleSelector(n, sel) {
			return true
		}
	}
	return false
}

// matchSimpleSelector 匹配简单选择器（标签 / .类 / #id）。
//
// **范围说明**：只支持 TS 清单里出现的形式（无复合选择器、无伪类）——
// 清单本身就是这些形式。
func matchSimpleSelector(n *html.Node, sel string) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch {
	case strings.HasPrefix(sel, "."):
		want := sel[1:]
		for _, a := range n.Attr {
			if a.Key == "class" {
				for _, cls := range strings.Fields(a.Val) {
					if cls == want {
						return true
					}
				}
			}
		}
		return false
	case strings.HasPrefix(sel, "#"):
		want := sel[1:]
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == want {
				return true
			}
		}
		return false
	default:
		return n.Data == sel
	}
}

// maxInt 返回两数较大者。
//
// **为什么不直接用内建 max**：Go 的 `max` 内建是 1.21 引入的泛型函数，
// 与本文件可能存在的同名辅助冲突；显式命名避免歧义。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// attrOf 取属性值（不存在返回空）。
func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// ── A3: 图片与链接 ─────────────────────────────────────────────────────

// renderImage 对账 TS `imageFix` 规则。
func (c *mdConverter) renderImage(n *html.Node) string {
	src := pickImageSource(n)
	if src == "" {
		return ""
	}
	if strings.HasPrefix(src, "data:") {
		return "<Base64-Image-Removed>"
	}
	abs := absolutizeURL(src, c.base)
	if abs == "" {
		abs = src
	}
	alt := strings.NewReplacer("[", "", "]", "").Replace(attrOf(n, "alt"))
	alt = strings.TrimSpace(alt)
	return fmt.Sprintf("![%s](%s)", alt, abs)
}

// renderLink 对账 TS `absolutizeLink` 规则。
func (c *mdConverter) renderLink(n *html.Node) string {
	inner := &strings.Builder{}
	c.walkChildren(n, inner)
	text := strings.TrimSpace(inner.String())

	href := attrOf(n, "href")
	if href == "" || strings.HasPrefix(href, "#") || protocolLinkRe.MatchString(href) {
		if strings.HasPrefix(href, "#") && skipToRe.MatchString(text) {
			return ""
		}
		return text
	}

	final := absolutizeURL(href, c.base)
	if final == "" {
		final = href
	}
	if text == "" {
		return final
	}
	if title := attrOf(n, "title"); title != "" {
		return fmt.Sprintf(`[%s](%s "%s")`, text, final, title)
	}
	return fmt.Sprintf("[%s](%s)", text, final)
}

// protocolLinkRe 对账 TS `/^(mailto|tel|javascript):/i`。
var protocolLinkRe = regexp.MustCompile(`(?i)^(mailto|tel|javascript):`)

// skipToRe 对账 TS `/skip to/i`。
var skipToRe = regexp.MustCompile(`(?i)skip to`)

// pickImageSource 对账 TS `pickImageSource`。
func pickImageSource(n *html.Node) string {
	srcset := attrOf(n, "srcset")
	if srcset == "" {
		srcset = attrOf(n, "data-srcset")
	}
	if srcset != "" {
		if best := pickBestSrcsetCandidate(srcset, attrOf(n, "src")); best != "" {
			return best
		}
	}
	// 懒加载占位图回退：data-src 优先于 src（src 常是 1px 占位）。
	if v := attrOf(n, "data-src"); v != "" {
		return v
	}
	return attrOf(n, "src")
}

// pickBestSrcsetCandidate 对账 TS `pickBestSrcsetCandidate`。
//
// srcset 候选取最大档（w 描述符优先，x 描述符 ×1000 归一；
// 含 1x/无描述符/低分候选时把 src 纳入竞争）。
func pickBestSrcsetCandidate(srcset, src string) string {
	type cand struct {
		url   string
		score float64
	}
	var candidates []cand
	hasLowRes := false

	for _, part := range strings.Split(srcset, ",") {
		bits := strings.Fields(strings.TrimSpace(part))
		if len(bits) == 0 {
			continue
		}
		u := bits[0]
		if u == "" {
			continue
		}
		desc := ""
		if len(bits) > 1 {
			desc = bits[1]
		}
		score := 1.0
		switch {
		case strings.HasSuffix(desc, "w"):
			if v, err := strconv.ParseFloat(strings.TrimSuffix(desc, "w"), 64); err == nil {
				score = v
			}
		case strings.HasSuffix(desc, "x"):
			v, err := strconv.ParseFloat(strings.TrimSuffix(desc, "x"), 64)
			if err != nil {
				v = 1
			}
			score = v * 1000
		default:
			hasLowRes = true
		}
		if score <= 1000 {
			hasLowRes = true
		}
		candidates = append(candidates, cand{url: u, score: score})
	}

	if src != "" && hasLowRes {
		candidates = append(candidates, cand{url: src, score: 1000})
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.url
}

// absolutizeURL 对账 TS `absolutizeURL`。
//
// `data:`/`blob:`/`about:` 不绝对化（返回空，调用方回退原值）。
func absolutizeURL(raw, base string) string {
	if base == "" || dataBlobAboutRe.MatchString(raw) {
		return ""
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return baseURL.ResolveReference(ref).String()
}

// dataBlobAboutRe 对账 TS `/^(data|blob|about):/i`。
var dataBlobAboutRe = regexp.MustCompile(`(?i)^(data|blob|about):`)

// resolveBaseURL 对账 TS `resolveBaseURL`——`<base href>` 优先，回退 PageURL。
//
// **窗口扫描**：`<base>` 只存在于 `<head>`，扫前 16KB 即可。
func resolveBaseURL(rawHTML, pageURL string) string {
	head := rawHTML
	if len(head) > 16_384 {
		head = head[:16_384]
	}
	m := baseHrefRe.FindStringSubmatch(head)
	if m != nil && m[1] != "" {
		if pageURL == "" {
			return m[1]
		}
		baseURL, err := url.Parse(pageURL)
		if err != nil {
			return m[1]
		}
		hrefURL, err := url.Parse(m[1])
		if err != nil {
			return m[1]
		}
		return baseURL.ResolveReference(hrefURL).String()
	}
	return pageURL
}

// baseHrefRe 对账 TS `/<base\b[^>]*?href=["']([^"']+)["']/i`。
var baseHrefRe = regexp.MustCompile(`(?is)<base\b[^>]*?href=["']([^"']+)["']`)

// ── 3. 表格 ─────────────────────────────────────────────────────────────

// renderTable 渲染简单表格（首行为表头）。
func (c *mdConverter) renderTable(n *html.Node, sb *strings.Builder) {
	var rows [][]string
	var walkRows func(*html.Node)
	walkRows = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			if child.Data == "tr" {
				var cells []string
				for cell := child.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
						inner := &strings.Builder{}
						c.walkChildren(cell, inner)
						cells = append(cells, strings.TrimSpace(inner.String()))
					}
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
				continue
			}
			walkRows(child)
		}
	}
	walkRows(n)

	if len(rows) == 0 {
		return
	}
	sb.WriteString("\n")
	for i, row := range rows {
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			sep := make([]string, len(row))
			for j := range sep {
				sep[j] = "---"
			}
			sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
		}
	}
	sb.WriteString("\n")
}

// ── 4. 后处理（对账 TS `postProcessMarkdown`）────────────────────────────

// postProcessMarkdown 对账 TS `postProcessMarkdown` 的三步。
func postProcessMarkdown(md string) string {
	out := escapeLinkNewlines(md)
	// 删无障碍跳转链接（skip-to-content 系）。
	out = skipToLinkRe.ReplaceAllString(out, "")
	// 保险：markdown 层残留 base64 图。
	out = base64ImageRe.ReplaceAllString(out, "<Base64-Image-Removed>")
	// 折叠 3+ 连续换行。
	out = multiNewlineRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out) + "\n"
}

// escapeLinkNewlines 对账 TS 的链接内换行转义。
//
// 跟踪未转义 `[` `]` 配对深度；代码围栏内的 `[` `]` 是代码，跳过。
// 深度 > 0 时遇到的换行转义为 `\\\n`（防 markdown 多行链接断裂）。
func escapeLinkNewlines(md string) string {
	var out strings.Builder
	depth := 0
	inFence := false
	rs := []rune(md)
	for i := 0; i < len(rs); i++ {
		ch := rs[i]
		if ch == '`' && i+2 < len(rs) && rs[i+1] == '`' && rs[i+2] == '`' {
			inFence = !inFence
			out.WriteString("```")
			i += 2
			continue
		}
		if !inFence {
			prev := rune(0)
			if i > 0 {
				prev = rs[i-1]
			}
			switch {
			case ch == '[' && prev != '\\':
				depth++
			case ch == ']' && prev != '\\' && depth > 0:
				depth--
			case ch == '\n' && depth > 0:
				out.WriteString("\\\n")
				continue
			}
		}
		out.WriteRune(ch)
	}
	return out.String()
}

// skipToLinkRe 对账 TS `/\[ *skip to (?:main )?content *\]\([^)]*\) *\n?/gi`。
var skipToLinkRe = regexp.MustCompile(`(?i)\[ *skip to (?:main )?content *\]\([^)]*\) *\n?`)

// base64ImageRe 对账 TS `/!\[[^\]]*\]\(data:[^)]+\)/g`。
var base64ImageRe = regexp.MustCompile(`!\[[^\]]*\]\(data:[^)]+\)`)

// multiNewlineRe 对账 TS `/\n{3,}/g`。
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)
