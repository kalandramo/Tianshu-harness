package prompt

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// TruncateBlock 复刻 volatile.ts:1209 的 truncateBlock —— frozen 块的预算截断。
//
// ## 三重 UTF-16 语义（本函数是移植中最难对账的一处）
//
//  1. `block.length <= maxChars` —— code unit 数比较（用 UTF16Len）
//  2. `content.slice(0, N)`     —— **JS 的 slice 会切断代理对**，产生的孤立
//     代理在 UTF-8 编码时变成 U+FFFD（ef bf bd）。Go 无法表示孤立代理
//     （string 是 UTF-8），但必须产出**同样的字节**——故用 sliceByUTF16
//     显式复刻"切断 + 替换为 U+FFFD"。
//  3. 截断标记里嵌入 `block.length` —— code unit 数，会进最终字节
//
// 第 2 点反直觉：看起来是在故意制造无效字符，但这是 TS 的真实行为，
// 字节等价要求复刻它。探针已验证 Go 侧可达（"ab😀cd" 切到 3 → 61 62 ef bf bd）。
//
// 三条分支（与 TS 一致）：
//   - kind == "codebase-index" → 特殊标记文案（含工具指引）
//   - 单根 XML 匹配成功 → 保留标签、内容切片、闭合
//   - 兜底 → 裸切片 + 通用标记
func TruncateBlock(block string, maxChars int, kind string) string {
	if UTF16Len(block) <= maxChars {
		return block
	}

	if kind == "codebase-index" {
		truncated := sliceByUTF16(block, maxChars)
		return truncated + "\n<!-- codebase index truncated (" + itoa(UTF16Len(block)) +
			" chars → " + itoa(maxChars) + "); use repo_map/repo_graph tools for details -->"
	}

	// 单根 XML 匹配：^<tag ...>content</tag>$
	//
	// TS 用 /^<([a-z-]+)[^>]*>([\s\S]*)<\/\1>$/m。Go 无法直接表达：
	//  - 无反向引用（\1）
	//  - RE2 的贪婪语义与 JS 回溯不同（实测 (?s)(?m) 会跨行吃到错误位置）
	//
	// 故手工实现，逐条对账 JS 语义（已用探针实测确认）：
	//  1. 必须从**串首**匹配开标签 `^<tag ...>`
	//  2. content 贪婪：找**最后一个**满足"其后是行尾或串尾"的同名闭合标签
	//  3. 闭合后必须是行尾或串尾（m 标志下 $ 的语义）
	// 实测：`<a>x</a>\n<a>y</a>` → tag=a content=`x</a>\n<a>y`（吃到最后）
	//       `<a>x</a>notlineend` → 不匹配（闭合后非行尾）
	if tag, content, ok := matchSingleRoot(block); ok {
		// trimmed = content.slice(0, maxChars - tag.length * 2 - 10)
		// tag.length 是 code unit 数（标签名是 ASCII，与码点相同）
		// TS: content.slice(0, maxChars - tag.length*2 - 10)
		// tag.length 是 code unit 数（标签名 ASCII，与码点同）。
		// **注意 n 可能为负**：JS 的 slice(0, n<0) 从末尾倒数（|n| 个字符处截断），
		// 超出长度则为空串。这里用 contentLen + n 折算成绝对位置。
		limit := maxChars - UTF16Len(tag)*2 - 10
		if limit < 0 {
			limit = UTF16Len(content) + limit
			if limit < 0 {
				limit = 0
			}
		}
		trimmed := sliceByUTF16(content, limit)
		return "<" + tag + ">\n" + trimmed + "\n<!-- truncated: " +
			itoa(UTF16Len(block)) + " → " + itoa(maxChars) + " chars -->\n</" + tag + ">"
	}

	return sliceByUTF16(block, maxChars) + "\n<!-- " + kind + " truncated: " +
		itoa(UTF16Len(block)) + " → " + itoa(maxChars) + " chars -->"
}

// openTagRe 匹配串首的开标签 `^<tag ...>`（不含反向引用，Go 可表达）。
var openTagRe = regexp.MustCompile(`^<([a-z-]+)[^>]*>`)

// matchSingleRoot 手工复刻 /^<([a-z-]+)[^>]*>([\s\S]*)<\/\1>$/m 的语义。
//
// 算法：从串首取开标签 → 从**末尾往前**找最后一个 `</tag>`，且要求其结束位置
// 是行尾或串尾（`$` 在 m 标志下的语义）。找不到则返回 ok=false。
//
// 为什么从末尾往前：JS 的 [\s\S]* 是贪婪的，会尽可能多吃。实测
// `<a>x</a>\n<a>y</a>` 得到 content=`x</a>\n<a>y`（最后一个闭合）。
func matchSingleRoot(block string) (tag string, content string, ok bool) {
	m := openTagRe.FindStringSubmatch(block)
	if m == nil {
		return "", "", false
	}
	tag = m[1]
	openEnd := len(m[0])
	closing := "</" + tag + ">"

	// 从末尾往前搜最后一个满足行尾条件的闭合标签
	searchEnd := len(block)
	for {
		idx := strings.LastIndex(block[:searchEnd], closing)
		if idx < openEnd {
			return "", "", false
		}
		after := idx + len(closing)
		// 闭合后必须是行尾或串尾（m 标志下 $ 的语义）
		if after == len(block) || block[after] == '\n' {
			return tag, block[openEnd:idx], true
		}
		searchEnd = idx
	}
}

// sliceByUTF16 复刻 JS 的 String.prototype.slice(0, n)。
//
// 关键语义：按 **UTF-16 code unit** 切，切在代理对中间时产生孤立代理——
// 其 UTF-8 编码为 U+FFFD（ef bf bd）。Go 的 []rune 切片不会这样（它按码点切，
// 永不切断），直接照搬会在含 emoji 的文本上产生不同字节。
//
// 实现：编码为 UTF-16 → 取前 n 个 unit → 解码回 rune。utf16.Decode 会把
// 孤立代理替换为 U+FFFD，与 JS 的 UTF-8 编码行为一致（探针已验证）。
func sliceByUTF16(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(s))
	if n >= len(units) {
		return s
	}
	decoded := utf16.Decode(units[:n])
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range decoded {
		if r == utf8.RuneError {
			b.WriteRune('\uFFFD')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StripFirstMarkdownTable 复刻 volatile.ts:1233 —— 剥离首个 markdown 表格。
//
// 语义（对账 TS）：
//   - 表格 = 连续以 `|` 开头（trimStart 后）的行
//   - 首个表格块之前若紧邻以 `>` 开头的行，一并删除
//   - 表格块之后若紧邻空行，一并删除
//   - 只处理**第一个**表格块
//
// 纯行操作，不涉计数——与 UTF-16 无关。
func StripFirstMarkdownTable(text string) string {
	lines := strings.Split(text, "\n")
	tableStart := -1
	tableEnd := -1

	for i, line := range lines {
		trimmed := trimStartUnicode(line)
		if strings.HasPrefix(trimmed, "|") {
			if tableStart == -1 {
				tableStart = i
			}
			tableEnd = i
		} else if tableStart != -1 {
			break
		}
	}

	if tableStart == -1 {
		return text
	}

	removeStart := tableStart
	if removeStart > 0 && strings.HasPrefix(lines[removeStart-1], ">") {
		removeStart--
	}
	removeEnd := tableEnd
	if removeEnd+1 < len(lines) && strings.TrimSpace(lines[removeEnd+1]) == "" {
		removeEnd++
	}

	result := append(append([]string{}, lines[:removeStart]...), lines[removeEnd+1:]...)
	return strings.Join(result, "\n")
}

// trimStartUnicode 复刻 JS 的 String.prototype.trimStart（Unicode 感知）。
func trimStartUnicode(s string) string {
	return strings.TrimLeftFunc(s, isJSSpace)
}

// isJSSpace 对账 JS 正则 \s 的字符集（比 Go 的 unicode.IsSpace 多 U+FEFF）。
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		'\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}
