// Package syntaxcheck 提供写入后的语法与结构完整性检查。
//
// 对账 src/tools/syntax-check.ts（566 行）的**判定契约**：
//
//	type SyntaxCheckResult struct { Warning, Fatal string }
//
// `Fatal` 非空时调用方应回滚写入（配合 recovery 包）。
//
// ## 与 TS 版的**架构差异**（有意为之，非降级）
//
// TS 版用 esbuild（+ TypeScript 编译器二次确认）检查 `.ts/.tsx/.js/.jsx`，
// 用 tree-sitter 检查 `.py`——因为**它自己是 TS 项目**，首要语言是 TS/JS。
//
// Go 版天枢的首要语言是 **Go**，而 Go 有**原生解析器**（`go/parser`），
// 比 esbuild 更权威（esbuild 的解析器比 tsc 更严格，TS 版自己都要做
// 「二次确认」来过滤误报）。故本移植：
//
//   - `.go` → `go/parser`（原生、权威、零依赖）
//   - `.json` / `.css` / `.html` → **纯算法**（与 TS 逐字节对账，见 oracle）
//
// **未覆盖**：`.ts/.tsx/.js/.jsx`（需 esbuild，Go 侧无等价物）、
// `.py`（需 tree-sitter）。这两类是**已知边界**，不是遗漏——
// 若未来 Go 版要服务 TS 项目，应引入 esbuild 的 Go 绑定而非自己写解析器。
package syntaxcheck

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// Result 是语法检查结果。
//
// 对账 TS 的 SyntaxCheckResult：`Fatal` 非空时调用方回滚。
type Result struct {
	// Warning 是给模型看的非致命提示。
	Warning string
	// Fatal 是致命解析/完整性错误。
	Fatal string
}

// ok 是「通过」的零值结果。
var ok = Result{}

// 尺寸上限（对账 TS 的常量）。
const (
	// syncScanSizeLimit 是纯算法扫描（css/html）的上限（2 MB）。
	syncScanSizeLimit = 2 * 1024 * 1024
	// externalParseSizeLimit 是外部解析器的上限（8 MB）。
	externalParseSizeLimit = 8 * 1024 * 1024
)

// Check 对内容做语言相关的语法与结构完整性检查。
//
// 对账 checkSyntax(filePath, content)。未知扩展名一律返回 OK
// （对账 TS：走完所有分支后 `return OK`）。
func Check(filePath, content string) Result {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".go":
		return checkGo(filePath, content)
	case ".json":
		return checkJSON(content)
	case ".css":
		return checkCSS(content)
	case ".html", ".htm":
		return checkHTML(content)
	default:
		// 未覆盖的扩展名（含 TS/JS/Python——见包注释的架构说明）
		return ok
	}
}

// checkGo 用 go/parser 做原生语法检查。
//
// **这是 Go 版的核心价值**：`go/parser` 是 Go 官方解析器，
// 对 `.go` 文件的判定就是编译器的判定——无误报、无二次确认需求。
func checkGo(filePath, content string) Result {
	if len(content) > externalParseSizeLimit {
		return ok
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, filePath, content, parser.AllErrors)
	if err == nil {
		return ok
	}
	// go/parser 的错误列表已含位置信息（file:line:col: msg）
	msg := "⚠️ Go 语法错误：" + err.Error() + "\n\n文件已写入但存在语法问题，编译将失败。"
	return Result{Warning: msg, Fatal: msg}
}

// checkJSON 用 encoding/json 做严格解析。
//
// 对账 TS 的 `.json` 分支（`JSON.parse` + 错误消息透传）。
// **差异**：Go 的错误消息文本与 JS 不同（各自的解析器），
// 故 oracle 只对账**判定**（fatal 是否非空），不对账消息文本。
func checkJSON(content string) Result {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err == nil {
		return ok
	} else {
		msg := "⚠️ Invalid JSON: " + err.Error()
		return Result{Warning: msg, Fatal: msg}
	}
}

// checkCSS 做花括号平衡检查（含字符串与注释状态机）。
//
// 对账 TS 的 `.css` 分支——**逐字符状态机**，包括：
//   - 块注释 `/* */` 内不计数
//   - 字符串 `"` / `'` 内不计数（含反斜杠转义）
//   - 深度为负 → 多余 `}`；结尾深度为正 → 缺 `}`
func checkCSS(content string) Result {
	if len(content) > syncScanSizeLimit {
		return ok
	}
	depth := 0
	inString := false
	var stringChar byte
	inComment := false

	for i := 0; i < len(content); i++ {
		c := content[i]
		var prev byte
		if i > 0 {
			prev = content[i-1]
		}

		if inComment {
			if c == '/' && prev == '*' {
				inComment = false
			}
			continue
		}
		if c == '/' && i+1 < len(content) && content[i+1] == '*' {
			inComment = true
			i++ // 跳过 '*'（对账 TS 的 i++）
			continue
		}
		if inString {
			if c == stringChar && prev != '\\' {
				inString = false
			}
			continue
		}
		if c == '"' || c == '\'' {
			inString = true
			stringChar = c
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
		}
		if depth < 0 {
			msg := "⚠️ CSS brace mismatch: unmatched '}' at position " + itoa(i) +
				". Remove the extra closing brace."
			return Result{Warning: msg, Fatal: msg}
		}
	}
	if depth > 0 {
		msg := "⚠️ CSS brace mismatch: " + itoa(depth) + " unmatched '{' (missing closing '}')." +
			" Check for unclosed blocks like @media or rule sets."
		return Result{Warning: msg, Fatal: msg}
	}
	return ok
}

// htmlVoids 是 HTML 的空元素（自闭合，不入栈）。
//
// 对账 TS 的 voids 集合——顺序与内容一致。
var htmlVoids = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// htmlTagRe 匹配开/闭标签（对账 TS 的正则 `<\/?([a-zA-Z][a-zA-Z0-9]*)[^>]*\/?>`）。
var htmlTagRe = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)[^>]*/?>`)

// checkHTML 做标签平衡检查。
//
// 对账 TS 的 `.html` 分支：正则扫描 + 栈匹配。
func checkHTML(content string) Result {
	if len(content) > syncScanSizeLimit {
		return ok
	}
	type frame struct {
		tag string
		pos int
	}
	var stack []frame

	for _, m := range htmlTagRe.FindAllStringSubmatchIndex(content, -1) {
		full := content[m[0]:m[1]]
		tag := strings.ToLower(content[m[2]:m[3]])
		isClose := strings.HasPrefix(full, "</")
		isSelfClose := strings.HasSuffix(full, "/>")

		if isSelfClose || htmlVoids[tag] {
			continue
		}
		if isClose {
			if len(stack) == 0 || stack[len(stack)-1].tag != tag {
				expected := "nothing"
				if len(stack) > 0 {
					expected = stack[len(stack)-1].tag
				}
				msg := "⚠️ HTML tag mismatch: unexpected </" + tag + "> at position " +
					itoa(m[0]) + " (expected </" + expected + ">)"
				return Result{Warning: msg, Fatal: msg}
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, frame{tag: tag, pos: m[0]})
		}
	}
	if len(stack) > 0 {
		var tags []string
		for _, f := range stack {
			tags = append(tags, "<"+f.tag+">")
		}
		msg := "⚠️ HTML tag mismatch: " + itoa(len(stack)) + " unclosed tag(s): " +
			strings.Join(tags, ", ") + ". Add the missing closing tags."
		return Result{Warning: msg, Fatal: msg}
	}
	return ok
}

// itoa 是 strconv.Itoa 的本地别名（避免为单处调用引入 import）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
