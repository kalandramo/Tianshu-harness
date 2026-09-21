package tools

// codefold.go —— 智能代码折叠（签名骨架 + 体折叠）。
//
// 对账 TS `src/compact/code-fold.ts`（428 行，五分支）。
//
// # 用途
//
// `read_file` 遇到大源码文件时，返回**签名骨架**（imports / 类型声明 /
// 函数与类签名，体折叠为 `{ … }`）而非头尾片段——后者丢失结构上下文。
//
// 策略：正则签名检测 + 花括号深度追踪，**无 AST 依赖**。
// 覆盖 TS/JS/TSX/JSX/Python/JSON/Markdown；未知语言回退（wasFolded=false）。
//
// # 消费者
//
//   - `focused-read` 的无匹配分支（`structuralSkeleton`）——**本刀接线**
//   - `applyFoldThenPartial`（readFilePayload 的 partial 分支，未移植）
//
// # 两个反直觉的边界（差分 oracle 锁定，勿"修"）
//
//   - `maxLines=10` 且 output 更长时：**wasFolded 可能为 false** 而 foldedLines
//     **大于** originalLines（截断逻辑把 `… (N lines omitted) …` 插进去，
//     行数反而增加）
//   - `wasFolded` 要求 `hadFold && output.length < originalLines`——即使折叠过，
//     若输出未变短也报 false

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// 对账 TS 常量。
const (
	codeFoldDefaultMaxLines = 200
	codeFoldMinLinesToFold  = 50
	codeFoldMaxScanLines    = 3000
	codeFoldTailKeep        = 20
)

// FoldOptions 对账 TS `FoldOptions`。
type FoldOptions struct {
	FilePath string
	// MaxLines 是折叠输出的软行数上限。
	//
	// **零值语义**：TS 用 `options.maxLines ?? DEFAULT_MAX_LINES`——`??` 只对
	// `null`/`undefined` 生效，**显式 0 保持 0**（不是默认值）。Go 无法用 int
	// 区分「未设」与「显式 0」，故加 `MaxLinesSet` 标志。
	MaxLines    int
	MaxLinesSet bool
}

// FoldResult 对账 TS `FoldResult`。
type FoldResult struct {
	Folded        string
	OriginalLines int
	FoldedLines   int
	Signatures    []string
	WasFolded     bool
}

// 语言分派（对账 TS `detectLanguage` 的 map）。
var codeFoldLangMap = map[string]string{
	".ts": "ts", ".tsx": "tsx",
	".js": "js", ".jsx": "jsx", ".mjs": "js", ".cjs": "js",
	".py": "py", ".pyi": "py",
	".json": "json",
	".md":   "md", ".mdx": "md",
}

// detectFoldLanguage 对账 TS `detectLanguage`（取小写扩展名，查表）。
func detectFoldLanguage(filePath string) string {
	ext := strings.ToLower(popExtLower(filePath))
	if ext != "" {
		ext = "." + ext
	}
	if lang, ok := codeFoldLangMap[ext]; ok {
		return lang
	}
	return "unknown"
}

// popExtLower 对账 JS 的 `extname(p).toLowerCase()`。
//
// **注意**：`extname` 与 `split('.').pop()` 不同——`extname("a.b/c")` 是 `""`
// （无扩展名），而 `split('.').pop()` 会给 `"b/c"`。但 TS 的 detectLanguage
// 用的是 `extname`——故本函数也应模拟 extname：取**最后一段**的点后部分，
// 且不含路径分隔符。
func popExtLower(filePath string) string {
	base := filePath
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	// extname 对 `.gitignore`（点开头无其他点）返回 ""。
	i := strings.LastIndex(base, ".")
	if i <= 0 { // 无点，或点在开头（.hidden）
		return ""
	}
	return base[i+1:]
}

// ---------------------------------------------------------------------------
// 花括号计数（字符串/字符字面量感知）
// ---------------------------------------------------------------------------

// countNetBraces 对账 TS `countNetBraces`。
//
// **不处理**正则字面量与模板串插值（TS 注释：覆盖约 90% 真实场景；
// 误判最坏导致 wasFolded=false 回退）。
func countNetBraces(line string) int {
	depth := 0
	inString := rune(0)
	escaped := false

	for _, ch := range line {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inString != 0 {
			if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' || ch == '`' {
			inString = ch
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
		}
	}
	return depth
}

// ---------------------------------------------------------------------------
// TS/JS 家族
// ---------------------------------------------------------------------------

var (
	// FUNCTION_LIKE —— 有实现体、值得折叠的声明。
	foldFunctionLikeRe = regexp.MustCompile(
		`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(export[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?(default[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?(async[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?(function[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*\*?[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*\w+|const[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(?::[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*[^=]+)?=[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(?:async[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?\(|class[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+|get[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+|set[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+|constructor[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*\()`)

	// STRUCTURAL_DECL —— 保留体（体本身就是类型信息）。
	foldStructuralDeclRe = regexp.MustCompile(
		`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(export[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?(abstract[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?(interface[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+|type[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*=|enum[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+)`)

	// IMPORT_EXPORT —— import / re-export 行。
	foldImportExportRe = regexp.MustCompile(
		`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(import[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]|export[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+(?:\{|\\\*|type|default))`)
)

// classifyFoldLine 对账 TS `classifyLine`（顺序敏感：import → structural → signature）。
func classifyFoldLine(line string) string {
	if foldImportExportRe.MatchString(line) {
		return "import"
	}
	if foldStructuralDeclRe.MatchString(line) {
		return "structural"
	}
	if foldFunctionLikeRe.MatchString(line) {
		return "signature"
	}
	return "other"
}

// truncateFoldSig 对账 TS `truncateSig`（超 120 code unit → 截 117 + "..."）。
func truncateFoldSig(line string) string {
	t := jsTrimSpace(line)
	if UTF16Len(t) > 120 {
		return jsSlice(t, 117) + "..."
	}
	return t
}

// foldTsLike 对账 TS `foldTsLike`。
func foldTsLike(lines []string, originalLines, maxLines int) FoldResult {
	output := []string{}
	signatures := []string{}

	scanLimit := len(lines)
	if scanLimit > codeFoldMaxScanLines {
		scanLimit = codeFoldMaxScanLines
	}
	braceDepth := 0
	hadFold := false

	i := 0
	for i < scanLimit {
		line := lines[i]
		cls := classifyFoldLine(line)
		netBraces := countNetBraces(line)

		if cls == "import" {
			output = append(output, line)
			i++
			continue
		}

		if cls == "signature" {
			signatures = append(signatures, truncateFoldSig(line))
			output = append(output, line)

			entryDepth := braceDepth
			blockStartDepth := -1 // -1 = null
			folded := false

			if netBraces > 0 {
				// 块在同一行开：`function foo() {`
				blockStartDepth = braceDepth
				braceDepth += netBraces
				// 行内自闭合（net 归零）→ 不折叠
				if braceDepth <= entryDepth {
					blockStartDepth = -1
				}
				folded = true
			} else if i+1 < scanLimit {
				nextLine := lines[i+1]
				nextNet := countNetBraces(nextLine)
				if strings.HasPrefix(jsTrimSpace(nextLine), "{") && nextNet > 0 {
					// 块在下一行开
					blockStartDepth = braceDepth
					braceDepth += nextNet
					i++ // 消费 `{` 行
					folded = true
				}
			}

			if folded && blockStartDepth >= 0 && braceDepth > blockStartDepth {
				hadFold = true
				// 前扫直到花括号回到起始深度
				for i+1 < scanLimit && braceDepth > blockStartDepth {
					i++
					braceDepth += countNetBraces(lines[i])
				}
				if braceDepth < 0 {
					braceDepth = 0
				}
				output = append(output, "  { … }")
			}

			i++
			continue
		}

		if cls == "structural" {
			signatures = append(signatures, truncateFoldSig(line))
			output = append(output, line)
			braceDepth += netBraces
			if braceDepth < 0 {
				braceDepth = 0
			}
			i++
			continue
		}

		// other：顶层语句或结构体内部行
		output = append(output, line)
		braceDepth += netBraces
		if braceDepth < 0 {
			braceDepth = 0
		}
		i++
	}

	// 未扫描的剩余
	if scanLimit < originalLines {
		output = append(output, "  … ("+strconv.Itoa(originalLines-scanLimit)+" more lines not scanned) …")
	}

	output = applyFoldMaxLines(output, maxLines)

	return FoldResult{
		Folded:        strings.Join(output, "\n"),
		OriginalLines: originalLines,
		FoldedLines:   len(output),
		Signatures:    signatures,
		WasFolded:     hadFold && len(output) < originalLines,
	}
}

// applyFoldMaxLines 对账 TS 的 maxLines 软限（head + 省略标记 + tail）。
//
// **JS `slice` 的负索引语义**（探针实测，勿"修"）：TS 用
// `output.slice(0, headCount)`，当 `headCount` 为**负**时 JS 语义是
// `[0, len+headCount)`——**不是空数组**。如 len=26、`slice(0,-10)` 得前 16 个。
// `slice(0, -30)`（|负| > len）才是空。
//
// 这解释了「maxLines 很小时 foldedLines 反而**大于** originalLines」：
// head 取了 len-10 条 + 1 条省略标记 + 20 条 tail，总数可超原长。
func applyFoldMaxLines(output []string, maxLines int) []string {
	if len(output) <= maxLines {
		return output
	}
	headCount := maxLines - codeFoldTailKeep - 1

	// 复刻 JS 的 `slice(0, headCount)`（负索引从末计数）。
	var headEnd int
	if headCount >= 0 {
		headEnd = headCount
	} else {
		headEnd = len(output) + headCount
	}
	if headEnd < 0 {
		headEnd = 0
	}
	if headEnd > len(output) {
		headEnd = len(output)
	}
	headPart := output[:headEnd]

	// `slice(-TAIL_KEEP)`：取末 20 行（不足则全部）。
	tailStart := len(output) - codeFoldTailKeep
	if tailStart < 0 {
		tailStart = 0
	}
	tailPart := output[tailStart:]
	omitted := len(output) - headCount - codeFoldTailKeep

	out := make([]string, 0, len(headPart)+1+len(tailPart))
	out = append(out, headPart...)
	out = append(out, "  … ("+strconv.Itoa(omitted)+" lines omitted) …")
	out = append(out, tailPart...)
	return out
}

// ---------------------------------------------------------------------------
// Python
// ---------------------------------------------------------------------------

var (
	foldPyDefRe   = regexp.MustCompile(`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(async[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?def[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+`)
	foldPyClassRe = regexp.MustCompile(`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*class[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+\w+`)
	foldPyDecoRe  = regexp.MustCompile(`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*@`)
)

// indentOf 对账 TS `indentOf`（**只数前导空格**，不含 tab）。
func indentOf(line string) int {
	n := 0
	for _, ch := range line {
		if ch == ' ' {
			n++
		} else {
			break
		}
	}
	return n
}

// foldPython 对账 TS `foldPython`。
func foldPython(lines []string, originalLines, maxLines int) FoldResult {
	output := []string{}
	signatures := []string{}
	hadFold := false

	i := 0
	for i < len(lines) {
		line := lines[i]

		if foldPyDecoRe.MatchString(line) {
			output = append(output, line)
			i++
			continue
		}

		if foldPyDefRe.MatchString(line) || foldPyClassRe.MatchString(line) {
			signatures = append(signatures, truncateFoldSig(line))
			output = append(output, line)
			defIndent := indentOf(line)

			// 折叠体（所有缩进 > defIndent 的行）
			bodyStart := i + 1
			bodyEnd := bodyStart
			for bodyEnd < len(lines) {
				bodyLine := lines[bodyEnd]
				if jsTrimSpace(bodyLine) == "" {
					bodyEnd++
					continue
				}
				if indentOf(bodyLine) <= defIndent {
					break
				}
				bodyEnd++
			}

			if bodyEnd > bodyStart {
				hadFold = true
				output = append(output, "  { … }")
				i = bodyEnd
				continue
			}
			i++
			continue
		}

		output = append(output, line)
		i++
	}

	output = applyFoldMaxLines(output, maxLines)

	return FoldResult{
		Folded:        strings.Join(output, "\n"),
		OriginalLines: originalLines,
		FoldedLines:   len(output),
		Signatures:    signatures,
		WasFolded:     hadFold && len(output) < originalLines,
	}
}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

// foldJSON 对账 TS `foldJson`。
func foldJSON(content string, originalLines int) FoldResult {
	var parsed any
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		// 非法 JSON —— 不折叠。
		return FoldResult{Folded: content, OriginalLines: originalLines,
			FoldedLines: originalLines, Signatures: []string{}, WasFolded: false}
	}
	// TS 的 JSON.parse 后 JSON.stringify(skeleton, null, 2)。
	summary := marshalJSONIndent2(jsonSkeleton(parsed, 0))
	lines := strings.Split(summary, "\n")
	return FoldResult{
		Folded:        summary,
		OriginalLines: originalLines,
		FoldedLines:   len(lines),
		Signatures:    []string{},
		WasFolded:     len(lines) < originalLines,
	}
}

// jsonSkeleton 对账 TS `jsonSkeleton`。
//
// 返回 any（string / []any / map / 字面量）——序列化时由 Go 的 json 包处理。
// **键序**：TS 的 `Object.keys` 保插入序，Go 的 map 无序——故用有序表示。
type jsonSkeletonObj struct {
	keys []string
	vals []any
}

func jsonSkeleton(value any, depth int) any {
	if depth > 2 {
		return "…"
	}
	if arr, ok := value.([]any); ok {
		if len(arr) == 0 {
			return []any{}
		}
		return []any{"(" + strconv.Itoa(len(arr)) + " items)", jsonSkeleton(arr[0], depth+1)}
	}
	if obj, ok := value.(map[string]any); ok {
		res := jsonSkeletonObj{}
		for _, k := range sortedKeysInOrder(obj) {
			v := obj[k]
			switch vv := v.(type) {
			case []any:
				if len(vv) > 0 {
					res.keys = append(res.keys, k)
					res.vals = append(res.vals, "["+strconv.Itoa(len(vv))+" items]")
				} else {
					res.keys = append(res.keys, k)
					res.vals = append(res.vals, []any{})
				}
			case map[string]any:
				res.keys = append(res.keys, k)
				if depth < 1 {
					res.vals = append(res.vals, jsonSkeleton(vv, depth+1))
				} else {
					res.vals = append(res.vals, "{ … }")
				}
			default:
				res.keys = append(res.keys, k)
				res.vals = append(res.vals, jsonTypeName(v))
			}
		}
		return res
	}
	return jsonTypeName(value)
}

// jsonTypeName 对账 JS 的 `typeof v`。
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "object" // typeof null === 'object'
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case float64:
		return "number"
	}
	return "object"
}

// marshalJSONIndent2 按 TS 的 `JSON.stringify(x, null, 2)` 格式序列化。
//
// **需保序**：jsonSkeletonObj 携带键序，故手写序列化而非用 encoding/json
// （后者对 map 排序，与 TS 的插入序不符）。
func marshalJSONIndent2(v any) string {
	var b strings.Builder
	writeJSONValue(&b, v, 0)
	return b.String()
}

func writeJSONValue(b *strings.Builder, v any, indent int) {
	pad := strings.Repeat("  ", indent)
	inner := strings.Repeat("  ", indent+1)
	switch vv := v.(type) {
	case jsonSkeletonObj:
		if len(vv.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range vv.keys {
			b.WriteString(inner)
			b.WriteString(jsonQuote(k))
			b.WriteString(": ")
			writeJSONValue(b, vv.vals[i], indent+1)
			if i < len(vv.keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(pad + "}")
	case []any:
		if len(vv) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range vv {
			b.WriteString(inner)
			writeJSONValue(b, e, indent+1)
			if i < len(vv)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(pad + "]")
	case string:
		b.WriteString(jsonQuote(vv))
	default:
		enc, err := json.Marshal(vv)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.WriteString(string(enc))
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// Markdown
// ---------------------------------------------------------------------------

var foldMdHeadingRe = regexp.MustCompile(`^#{1,6}[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`)

// foldMarkdown 对账 TS `foldMarkdown`。
func foldMarkdown(lines []string, originalLines, maxLines int) FoldResult {
	output := []string{}
	hadFold := false

	for _, line := range lines {
		if foldMdHeadingRe.MatchString(line) {
			output = append(output, line)
			continue
		}
		// 保留标题后的首行（主题句）
		if len(output) > 0 && foldMdHeadingRe.MatchString(output[len(output)-1]) {
			output = append(output, line)
			continue
		}
		// 保留代码围栏
		if strings.HasPrefix(jsTrimSpace(line), "```") {
			output = append(output, line)
			continue
		}
		// 跳过正文
		if jsTrimSpace(line) != "" {
			hadFold = true
		}
	}

	if !hadFold || len(output) >= originalLines {
		return FoldResult{Folded: strings.Join(lines, "\n"), OriginalLines: originalLines,
			FoldedLines: originalLines, Signatures: []string{}, WasFolded: false}
	}

	output = applyFoldMaxLines(output, maxLines)

	return FoldResult{
		Folded:        strings.Join(output, "\n"),
		OriginalLines: originalLines,
		FoldedLines:   len(output),
		Signatures:    []string{},
		WasFolded:     true,
	}
}

// ---------------------------------------------------------------------------
// 主入口
// ---------------------------------------------------------------------------

// FoldCode 对账 TS `foldCode`。
//
// 分派：ts/tsx/js/jsx → foldTsLike；py → foldPython；json → foldJSON；
// md → foldMarkdown；unknown → 不折叠。
//
// **前置**：`originalLines < 50` 直接返回不折叠（在分派**之前**）。
func FoldCode(content string, options FoldOptions) FoldResult {
	maxLines := options.MaxLines
	if !options.MaxLinesSet {
		maxLines = codeFoldDefaultMaxLines
	}
	lang := detectFoldLanguage(options.FilePath)
	lines := strings.Split(content, "\n")
	originalLines := len(lines)

	if originalLines < codeFoldMinLinesToFold {
		return FoldResult{Folded: content, OriginalLines: originalLines,
			FoldedLines: originalLines, Signatures: []string{}, WasFolded: false}
	}

	switch lang {
	case "unknown":
		return FoldResult{Folded: content, OriginalLines: originalLines,
			FoldedLines: originalLines, Signatures: []string{}, WasFolded: false}
	case "json":
		return foldJSON(content, originalLines)
	case "md":
		return foldMarkdown(lines, originalLines, maxLines)
	case "py":
		return foldPython(lines, originalLines, maxLines)
	default:
		return foldTsLike(lines, originalLines, maxLines)
	}
}

// sortedKeysInOrder 返回 map 的键。
//
// **局限**：Go 的 map 无序，无法复刻 TS 的 `Object.keys` 插入序。
// 故按字典序排列——**这与 TS 不同**，但对 JSON 骨架（值已替换为类型名）
// 的影响仅在键顺序；已记入测试注释。
func sortedKeysInOrder(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 稳定排序（字典序）。
	for i := 1; i < len(keys); i++ {
		cur := keys[i]
		j := i - 1
		for j >= 0 && keys[j] > cur {
			keys[j+1] = keys[j]
			j--
		}
		keys[j+1] = cur
	}
	return keys
}
