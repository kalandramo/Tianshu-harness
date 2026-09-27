package search

import (
	"context"
	"net/url"
	"regexp"
	"strings"
)

// html.go —— HTML 抓取型后端的共享解析工具（第九十七刀 · W4）。
//
// 对账 TS `duckduckgo.ts` 里的 `stripHtml` / `decodeHtmlEntities`。

// entityRe 匹配 HTML 实体（命名 + 十进制 + 十六进制）。
//
// 对账 TS：`&(#x[0-9a-fA-F]+|#\d+|amp|lt|gt|quot|apos|#39|nbsp|ensp|emsp|thinsp|rsaquo|lsaquo);`
var entityRe = regexp.MustCompile(
	`&(#x[0-9a-fA-F]+|#\d+|amp|lt|gt|quot|apos|#39|nbsp|ensp|emsp|thinsp|rsaquo|lsaquo);`)

// tagRe 匹配 HTML 标签（用于 stripHtml）。
var tagRe = regexp.MustCompile(`<[^>]+>`)

// DecodeHTMLEntities 解码 HTML 实体（命名 + 数字 `&#92;` / `&#x27;`）。
//
// **单遍替换**（对账 TS 注释）：命中的实体只替换一次、不重新扫描，
// 故 `&amp;#x27;` 解码为字面量 `&#x27;`，**绝不会二次解码成 `'`**。
//
// Go 的 `ReplaceAllStringFunc` 天然单遍——与 TS 语义一致。
func DecodeHTMLEntities(text string) string {
	return entityRe.ReplaceAllStringFunc(text, func(m string) string {
		// 去掉 `&` 与 `;`
		name := m[1 : len(m)-1]
		switch name {
		case "amp":
			return "&"
		case "lt":
			return "<"
		case "gt":
			return ">"
		case "quot":
			return `"`
		case "apos", "#39":
			return "'"
		case "nbsp":
			return "\u00A0"
		case "ensp":
			return "\u2002"
		case "emsp":
			return "\u2003"
		case "thinsp":
			return "\u2009"
		case "rsaquo":
			return "\u203A"
		case "lsaquo":
			return "\u2039"
		}
		// 数字实体：&#92;（十进制）或 &#x27;（十六进制）
		var code int
		var ok bool
		if len(name) > 1 && (name[1] == 'x' || name[1] == 'X') {
			code, ok = parseIntBase(name[2:], 16)
		} else if len(name) > 1 && name[0] == '#' {
			code, ok = parseIntBase(name[1:], 10)
		}
		if !ok || code <= 0 {
			return m // 无法解码 → 原样保留（对账 TS）
		}
		return string(rune(code))
	})
}

// parseIntBase 是极简的整数解析（避免为两个调用点引 strconv 的宽签名）。
func parseIntBase(s string, base int) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		var d int
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		if d >= base {
			return 0, false
		}
		// 溢出防护：实体码点不会超过 Unicode 上限
		n = n*base + d
		if n > 0x10FFFF {
			return 0, false
		}
	}
	return n, true
}

// StripHTML 去掉 HTML 标签。
func StripHTML(text string) string {
	return strings.TrimSpace(tagRe.ReplaceAllString(text, ""))
}

// collapseSpaces 把连续空白折叠成单个空格（对账 TS 的 `.replace(/\s+/g, ' ')`）。
//
// **Go 的差异**：TS 的 `\s` 不匹配 `\u00A0`（非断行空格），而 Go 的 `\s` 在
// 字符类里匹配 `[\t\n\f\r ]`——也不含 `\u00A0`。两者一致，无需特殊处理。
var spaceRe = regexp.MustCompile(`\s+`)

func collapseSpaces(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// ── DuckDuckGo ──────────────────────────────────────────────────────────

// ddgEndpoint 对账 TS：`https://html.duckduckgo.com/html/?q=`
const ddgEndpoint = "https://html.duckduckgo.com/html/?q="

// ddgSplit 对账 TS：按 `<h2 class="result__title">` 切块（首块是前导片段，丢弃）。
const ddgSplit = `<h2 class="result__title">`

var (
	ddgLinkRe    = regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
)

// ParseDuckDuckGoResults 解析 DuckDuckGo lite 端点的 HTML 结果。
//
// 对账 TS `parseDuckDuckGoResults`。HTML 结构（对账 TS 注释）：
//
//	<div class="result">
//	  <h2 class="result__title"><a class="result__a" href="URL">TITLE</a></h2>
//	  <a class="result__snippet" href="URL">SNIPPET</a>
//	</div>
func ParseDuckDuckGoResults(html string, maxCount int) []Result {
	results := []Result{}
	blocks := strings.Split(html, ddgSplit)
	for i := 1; i < len(blocks) && len(results) < maxCount; i++ {
		block := blocks[i]

		m := ddgLinkRe.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		rawURL := DecodeHTMLEntities(m[1])
		// **先去标签，再解码实体**（对账 TS 注释）→ 给模型可读文本
		title := strings.TrimSpace(DecodeHTMLEntities(StripHTML(m[2])))
		if title == "" || rawURL == "" {
			continue
		}

		snippet := ""
		if sm := ddgSnippetRe.FindStringSubmatch(block); sm != nil {
			snippet = strings.TrimSpace(DecodeHTMLEntities(StripHTML(sm[1])))
		}

		results = append(results, Result{
			Title:   title,
			URL:     extractDDGActualURL(rawURL),
			Snippet: snippet,
		})
	}
	return results
}

var httpSchemeRe = regexp.MustCompile(`(?i)^https?://`)

// extractDDGActualURL 从 DuckDuckGo 的跳转包装里取出真实目标 URL。
//
// 对账 TS `extractActualUrl`：**只接受 http/https**。
// 处理 `uddg` 与 `u` 两个查询参数（DDG 不同时期用过不同的名字）。
func extractDDGActualURL(ddgURL string) string {
	// 协议相对 URL（`//duckduckgo.com/l/?...`）补上 https
	normalized := ddgURL
	if strings.HasPrefix(normalized, "//") {
		normalized = "https:" + normalized
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return ddgURL
	}
	for _, key := range []string{"uddg", "u"} {
		raw := parsed.Query().Get(key)
		if raw == "" {
			continue
		}
		decoded, err := url.QueryUnescape(raw)
		if err != nil {
			continue
		}
		if httpSchemeRe.MatchString(decoded) {
			return decoded
		}
	}
	return ddgURL
}

// DuckDuckGoBackend 是免费零配置的兜底后端。
//
// 对账 TS `DuckDuckGoBackend`：抓 `html.duckduckgo.com` lite 端点，
// **无需 API key**，故 `IsAvailable()` 恒 true。
type DuckDuckGoBackend struct{ fetch Fetch }

// NewDuckDuckGoBackend 创建 DDG 后端。
func NewDuckDuckGoBackend(fetch Fetch) *DuckDuckGoBackend {
	return &DuckDuckGoBackend{fetch: fetch}
}

func (b *DuckDuckGoBackend) Name() string      { return "duckduckgo" }
func (b *DuckDuckGoBackend) IsAvailable() bool { return true }

func (b *DuckDuckGoBackend) Search(ctx context.Context, query string, count int) ([]Result, error) {
	req := &Request{
		URL: ddgEndpoint + url.QueryEscape(query),
		Headers: map[string]string{
			"User-Agent": "terminal-coding-agent/1.0",
		},
		// 对账 TS：`redirect: 'manual'`——lite 端点直接返回 200。
		// 拒绝自动跟随重定向，避免被弹到未校验主机（3xx 会显示为非 ok 状态，
		// 链就此落空）。
		NoRedirect: true,
	}
	res, err := b.fetch(ctx, req)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, &HTTPStatusError{Status: res.Status}
	}
	return ParseDuckDuckGoResults(string(res.Body), count), nil
}
