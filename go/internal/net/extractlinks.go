package net

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// extractlinks.go —— 链接提取与响体解码（第九十一刀 · W3-4b）。
//
// 对账 TS `src/tools/web-fetch/extract.ts` 里的三个导出：
// `extractLinks` / `extractLinksFromMarkdown` / `decodeBody`。

// ── 链接提取 ────────────────────────────────────────────────────────────

// anchorHrefRe 对账 TS `/<a\b[^>]*?href=["']([^"']+)["']/gi`。
var anchorHrefRe = regexp.MustCompile(`(?is)<a\b[^>]*?href=["']([^"']+)["']`)

// ExtractLinks 从原始 HTML 提取 `<a href>` 链接并绝对化（crawl 发现源）。
//
// 对账 TS `extractLinks`。
//
// **必须在转换/黑名单清洗之前提取**——sidebar/menu 里的文档目录链接会被
// onlyMainContent 剔除，markdown 层再提就丢了。
func ExtractLinks(rawHTML, pageURL string) []string {
	base := resolveBaseURL(rawHTML, pageURL)
	seen := map[string]bool{}
	var out []string

	for _, m := range anchorHrefRe.FindAllStringSubmatch(rawHTML, -1) {
		if len(m) < 2 {
			continue
		}
		href := strings.TrimSpace(m[1])
		if href == "" || strings.HasPrefix(href, "#") || ExtractLinkProtocolRe.MatchString(href) {
			continue
		}
		abs := absolutizeURL(href, base)
		if abs != "" && (strings.HasPrefix(abs, "http:") || strings.HasPrefix(abs, "https:")) {
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out
}

// ExtractLinkProtocolRe 对账 TS `/^(mailto|tel|javascript|data):/i`。
var ExtractLinkProtocolRe = regexp.MustCompile(`(?i)^(mailto|tel|javascript|data):`)

// markdownLinkRe 对账 TS `/\]\((https?:\/\/[^)\s]+?)(?:\s+"[^"]*")?\)/g`。
var markdownLinkRe = regexp.MustCompile(`\]\((https?://[^)\s]+?)(?:\s+"[^"]*")?\)`)

// ExtractLinksFromMarkdown 从 markdown 提取绝对链接。
//
// 对账 TS `extractLinksFromMarkdown`——缓存命中/Jina 路径的 crawl 发现源。
func ExtractLinksFromMarkdown(markdown string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range markdownLinkRe.FindAllStringSubmatch(markdown, -1) {
		if len(m) < 2 {
			continue
		}
		link := m[1]
		if !seen[link] {
			seen[link] = true
			out = append(out, link)
		}
	}
	return out
}

// ── 响体解码（对账 TS `decodeBody` / `detectCharset`）───────────────────

// charsetHeaderRe 对账 TS `/charset=([^;]+)/i`。
var charsetHeaderRe = regexp.MustCompile(`(?i)charset=([^;]+)`)

// metaCharsetRe 对账 TS `/<meta[^>]+charset=["']?([^"';>\s]+)/i`。
var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset=["']?([^"';>\s]+)`)

// DecodeBody 按 charset 解码响应体为字符串。
//
// 对账 TS `decodeBody`：charset 判定顺序是
// **HTTP 头 charset → HTML meta charset → utf-8 兜底**。
//
// **与 `internal/tools/windecode.go` 的分工**（避免重复）：
// windecode 做的是**流式二进制解码**（累积探测 UTF-8/GBK，服务于控制台输出）；
// 本函数做的是**按声明的 charset 解码**（服务于 HTTP 响应体）。两者输入形态
// 与判定依据都不同。
func DecodeBody(body []byte, contentType string) string {
	charset := detectCharset(body, contentType)
	return decodeWithCharset(body, charset)
}

// detectCharset 对账 TS `detectCharset`。
func detectCharset(body []byte, contentType string) string {
	if m := charsetHeaderRe.FindStringSubmatch(contentType); len(m) > 1 {
		if cs := strings.Trim(strings.TrimSpace(m[1]), `"'`); cs != "" {
			return strings.ToLower(cs)
		}
	}
	// HTML 声明：只扫前 1024 字节（对账 TS `bytes.slice(0, 1024)`）。
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		head := body
		if len(head) > 1024 {
			head = head[:1024]
		}
		// meta 声明可能是任意编码——用「尽力而为」的解码扫 ASCII 部分。
		headStr := string(head)
		if !utf8.Valid(head) {
			// 非 UTF-8 时先按 Latin-1 直译（meta 标签本身必然是 ASCII）。
			headStr = latin1ToString(head)
		}
		if m := metaCharsetRe.FindStringSubmatch(headStr); len(m) > 1 {
			return strings.ToLower(m[1])
		}
	}
	return "utf-8"
}

// decodeWithCharset 按 charset 名解码。
//
// **支持的编码**：utf-8 / gbk / gb2312 / gb18030 / big5 / latin1 / utf-16*。
// 不支持的名字回退 UTF-8（对账 TS 的 catch 分支）。
func decodeWithCharset(body []byte, charset string) string {
	switch charset {
	case "utf-8", "utf8":
		return string(body) // Go 的 string 转换对非法 UTF-8 保留原始字节
	case "gbk", "gb2312", "gb18030":
		if s, err := decodeVia(simplifiedchinese.GBK.NewDecoder(), body); err == nil {
			return s
		}
	case "big5":
		if s, err := decodeVia(traditionalchinese.Big5.NewDecoder(), body); err == nil {
			return s
		}
	case "latin1", "iso-8859-1", "windows-1252":
		return latin1ToString(body)
	case "utf-16", "utf-16le":
		if s, err := decodeVia(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), body); err == nil {
			return s
		}
	case "utf-16be":
		if s, err := decodeVia(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder(), body); err == nil {
			return s
		}
	}
	// 兜底：UTF-8 直译（对账 TS 的 `new TextDecoder('utf-8', {fatal:false})`）。
	return string(body)
}

// decodeVia 用指定 transformer 解码。
func decodeVia(t transform.Transformer, body []byte) (string, error) {
	out, _, err := transform.Bytes(t, body)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// latin1ToString 把字节按 Latin-1 直译为字符串（每字节一码点）。
func latin1ToString(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}
