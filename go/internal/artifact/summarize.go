package artifact

// summarize.go —— artifact 内容的结构化摘要与片段提取。
//
// 对账 TS `src/artifact/summarize.ts`（407 行）。
//
// # 消费者（**必须先确认存在，否则是无消费者的空壳**）
//
// 唯一调用方是**工具自己的 L0 包装**：
//   - `read-file.ts:1016` → `summarizeFileContent`
//   - `grep.ts:155,488`   → `summarizeGrepResult`
//   - `bash.ts:770`       → `summarizeBashOutput`
//
// **L1 不调用它**——`tool-pipeline.ts:607` 显式传 `sections: []`。
//
// Go 侧 L0 包装尚未移植（`internal/tools/read_file.go` 无 artifact 逻辑），
// 故本文件当前**暂无生产调用方**——它是 L0 的依赖，接线属下一刀。
// 这与「发明 TS 不存在的功能」不同：函数本身与调用点在 TS 侧都已核实存在。
//
// # 字节稳定性
//
// `Summary` 会进 tool_result（**不在冻结前缀里**），但仍有对账测试逐字比对，
// 故所有截断按 JS 的 **UTF-16 code unit** 语义（见 `jsSlice`）。

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SummarizeResult 对账 TS `SummarizeResult`。
type SummarizeResult struct {
	Summary string
	// Sections 恒为**非 nil** 空切片（对账 TS 的 `[]`——Go 的 nil 会序列化成
	// `null`，与 TS 的 `[]` 不等价）。
	Sections []ArtifactSection
}

// SummarizeFileContent 按扩展名分派摘要器。
//
// 对账 TS `summarizeFileContent`：ext 取**小写**用于分派，但各摘要器内部的
// summary 文案用**原始大小写**的 ext（`.TS` → 分派到 jsTs，文案写 `TS file`）。
func SummarizeFileContent(content, filePath string) SummarizeResult {
	switch strings.ToLower(popExt(filePath)) {
	case "ts", "tsx", "js", "jsx":
		return summarizeJsTs(content, filePath)
	case "py":
		return summarizePython(content, filePath)
	case "rs":
		return summarizeRust(content, filePath)
	case "go":
		return summarizeGo(content, filePath)
	case "md", "mdx":
		return summarizeMarkdown(content, filePath)
	case "json":
		return summarizeJson(content, filePath)
	default:
		return summarizeGeneric(content, filePath)
	}
}

// popExt 对账 JS 的 `filePath.split('.').pop() ?? ”`。
//
// 逐例核实：`"a.b.ts"`→`"ts"`；`"a."`→`""`；`".a"`→`"a"`；`"foo"`→`"foo"`；`""`→`""`。
func popExt(filePath string) string {
	if i := strings.LastIndex(filePath, "."); i >= 0 {
		return filePath[i+1:]
	}
	return filePath
}

// ---------------------------------------------------------------------------
// JS / TS
// ---------------------------------------------------------------------------

var (
	reJsImport     = jsRe(`(?m)^import` + jsWS + `+.*?` + jsWS + `+from` + jsWS + `+['"](.+?)['"]`)
	reJsImportBare = jsRe(`(?m)^import` + jsWS + `+['"](.+?)['"]`)
	reJsReExport   = jsRe(`(?m)^export` + jsWS + `+\{[^}]*\}` + jsWS + `+from` + jsWS + `+['"](.+?)['"]`)
	reJsExport     = jsRe(`(?m)^export` + jsWS + `+(?:default` + jsWS + `+)?(?:function|class|const|let|var)` + jsWS + `+(\w+)`)
	reJsBareExport = jsRe(`(?m)^export` + jsWS + `+\{([^}]+)\}`)
	reJsFn         = jsRe(`(?m)^(?:export` + jsWS + `+)?(?:async` + jsWS + `+)?function` + jsWS + `+(\w+)`)
	reJsClass      = jsRe(`(?m)^(?:export` + jsWS + `+)?(?:default` + jsWS + `+)?class` + jsWS + `+(\w+)`)
	reJsAsSplit    = jsRe(jsWS + `+as` + jsWS)
)

func summarizeJsTs(content, filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	sections := []ArtifactSection{}

	var exports, functions, classes, imports []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// import 检测（先试 `import x from '...'` 再试 `import '...'`）。
		if m := reJsImport.FindStringSubmatch(line); m != nil {
			imports = append(imports, m[1])
			continue
		}
		if m := reJsImportBare.FindStringSubmatch(line); m != nil {
			imports = append(imports, m[1])
			continue
		}

		// re-export：`export { x } from '...'`——**计入 imports**（对账 TS）。
		if m := reJsReExport.FindStringSubmatch(line); m != nil {
			imports = append(imports, m[1])
			continue
		}

		// 具名导出。
		if m := reJsExport.FindStringSubmatch(line); m != nil {
			exports = append(exports, m[1])
			end := findBlockEnd(lines, i)
			sections = append(sections, ArtifactSection{
				Name:      "export:" + m[1],
				LineStart: i + 1,
				LineEnd:   end + 1,
				CharCount: charLen(strings.Join(lines[i:end+1], "\n")),
			})
			continue
		}

		// `export { a, b as c }`——名字取 `as` 之后的别名。
		if m := reJsBareExport.FindStringSubmatch(line); m != nil {
			for _, n := range strings.Split(m[1], ",") {
				parts := reJsAsSplit.Split(jsTrimSpace(n), -1)
				name := jsTrimSpace(parts[len(parts)-1])
				if name != "" {
					exports = append(exports, name)
				}
			}
			continue
		}

		// 非导出函数 / 类。
		if m := reJsFn.FindStringSubmatch(line); m != nil && !containsStr(exports, m[1]) {
			functions = append(functions, m[1])
		}
		if m := reJsClass.FindStringSubmatch(line); m != nil && !containsStr(exports, m[1]) {
			classes = append(classes, m[1])
		}
	}

	if len(imports) > 0 {
		sections = append([]ArtifactSection{{
			Name:      "imports",
			LineStart: 1,
			LineEnd:   minInt(len(imports), len(lines)),
			CharCount: charLen(strings.Join(imports, "\n")),
		}}, sections...)
	}

	// **ext 用原始大小写**（对账 TS 的局部变量，与分派用的小写 ext 不同）。
	ext := popExt(filePath)
	parts := []string{ext + " file, " + strconv.Itoa(len(lines)) + " lines."}
	if len(exports) > 0 {
		p := "Exports: " + strings.Join(head(exports, 8), ", ")
		if len(exports) > 8 {
			p += " (+" + strconv.Itoa(len(exports)-8) + ")"
		}
		parts = append(parts, p)
	}
	if len(functions) > 0 {
		parts = append(parts, "Functions: "+strings.Join(head(functions, 5), ", "))
	}
	if len(classes) > 0 {
		parts = append(parts, "Classes: "+strings.Join(classes, ", "))
	}

	return SummarizeResult{Summary: strings.Join(parts, " "), Sections: sections}
}

// ---------------------------------------------------------------------------
// Python
// ---------------------------------------------------------------------------

var (
	rePyClass = jsRe(`(?m)^class` + jsWS + `+(\w+)`)
	rePyFn    = jsRe(`(?m)^(?:async` + jsWS + `+)?def` + jsWS + `+(\w+)`)
)

func summarizePython(content, _filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	sections := []ArtifactSection{}

	var classes, functions []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if m := rePyClass.FindStringSubmatch(line); m != nil {
			classes = append(classes, m[1])
			end := findPythonBlockEnd(lines, i)
			sections = append(sections, ArtifactSection{
				Name:      "class:" + m[1],
				LineStart: i + 1,
				LineEnd:   end + 1,
				CharCount: charLen(strings.Join(lines[i:end+1], "\n")),
			})
			continue
		}
		if m := rePyFn.FindStringSubmatch(line); m != nil {
			functions = append(functions, m[1])
			end := findPythonBlockEnd(lines, i)
			sections = append(sections, ArtifactSection{
				Name:      "function:" + m[1],
				LineStart: i + 1,
				LineEnd:   end + 1,
				CharCount: charLen(strings.Join(lines[i:end+1], "\n")),
			})
		}
	}

	parts := []string{"py file, " + strconv.Itoa(len(lines)) + " lines."}
	if len(classes) > 0 {
		parts = append(parts, "Classes: "+strings.Join(classes, ", "))
	}
	if len(functions) > 0 {
		parts = append(parts, "Functions: "+strings.Join(functions, ", "))
	}

	return SummarizeResult{Summary: strings.Join(parts, " "), Sections: sections}
}

// ---------------------------------------------------------------------------
// Rust
// ---------------------------------------------------------------------------

var (
	reRsFn     = jsRe(`(?m)^(?:pub` + jsWS + `+)?(?:async` + jsWS + `+)?fn` + jsWS + `+(\w+)`)
	reRsStruct = jsRe(`(?m)^(?:pub` + jsWS + `+)?struct` + jsWS + `+(\w+)`)
	reRsEnum   = jsRe(`(?m)^(?:pub` + jsWS + `+)?enum` + jsWS + `+(\w+)`)
	reRsImpl   = jsRe(`(?m)^impl(?:<[^>]*>)?` + jsWS + `+(?:\w+` + jsWS + `+for` + jsWS + `+)?(\w+)`)
	reRsTrait  = jsRe(`(?m)^(?:pub` + jsWS + `+)?trait` + jsWS + `+(\w+)`)
)

func summarizeRust(content, _filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	sections := []ArtifactSection{}

	var fns, structs, enums, impls, traits []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if m := reRsFn.FindStringSubmatch(line); m != nil {
			fns = append(fns, m[1])
			sections = append(sections, ArtifactSection{
				Name:      "fn:" + m[1],
				LineStart: i + 1,
				LineEnd:   findBlockEnd(lines, i) + 1,
				CharCount: 0, // 对账 TS：Rust fn 的 charCount 写死 0
			})
			continue
		}
		if m := reRsStruct.FindStringSubmatch(line); m != nil {
			structs = append(structs, m[1])
			continue
		}
		if m := reRsEnum.FindStringSubmatch(line); m != nil {
			enums = append(enums, m[1])
			continue
		}
		if m := reRsImpl.FindStringSubmatch(line); m != nil {
			impls = append(impls, m[1])
			continue
		}
		if m := reRsTrait.FindStringSubmatch(line); m != nil {
			traits = append(traits, m[1])
			continue
		}
	}

	parts := []string{"rs file, " + strconv.Itoa(len(lines)) + " lines."}
	if len(structs) > 0 {
		parts = append(parts, "Structs: "+strings.Join(structs, ", "))
	}
	if len(enums) > 0 {
		parts = append(parts, "Enums: "+strings.Join(enums, ", "))
	}
	if len(traits) > 0 {
		parts = append(parts, "Traits: "+strings.Join(traits, ", "))
	}
	if len(impls) > 0 {
		parts = append(parts, "Impls: "+strings.Join(impls, ", "))
	}
	if len(fns) > 0 {
		parts = append(parts, "Fns: "+strings.Join(head(fns, 8), ", "))
	}

	return SummarizeResult{Summary: strings.Join(parts, " "), Sections: sections}
}

// ---------------------------------------------------------------------------
// Go —— **有意低细节**（对账 TS 注释：no Go structural parser yet）
// ---------------------------------------------------------------------------

func summarizeGo(content, _filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	return SummarizeResult{
		Summary:  "go file, " + strconv.Itoa(len(lines)) + " lines. low-detail summary (no Go structural parser yet), consider read_section for details.",
		Sections: []ArtifactSection{},
	}
}

// ---------------------------------------------------------------------------
// Markdown
// ---------------------------------------------------------------------------

var reMdHeading = jsRe(`(?m)^(#{1,6})` + jsWS + `+(.+)`)

func summarizeMarkdown(content, _filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	sections := []ArtifactSection{}
	var headings []string

	for i := 0; i < len(lines); i++ {
		m := reMdHeading.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		title := jsTrimSpace(m[2])
		headings = append(headings, title)
		level := len(m[1])
		end := findMarkdownSectionEnd(lines, i, level)
		sections = append(sections, ArtifactSection{
			Name:      "heading:" + title,
			LineStart: i + 1,
			LineEnd:   end + 1,
			CharCount: charLen(strings.Join(lines[i:end+1], "\n")),
		})
	}

	parts := []string{"md file, " + strconv.Itoa(len(lines)) + " lines."}
	if len(headings) > 0 {
		parts = append(parts, "Headings: "+strings.Join(head(headings, 10), ", "))
	}

	return SummarizeResult{Summary: strings.Join(parts, " "), Sections: sections}
}

// ---------------------------------------------------------------------------
// JSON
// ---------------------------------------------------------------------------

func summarizeJson(content, _filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	sections := []ArtifactSection{}

	// **保序解析**：TS 的 `Object.keys` 按 JSON 里的键出现顺序，而 Go 的
	// `map` 遍历顺序随机——直接用 map 会让 summary 字节不稳定。
	// 故用 `json.Decoder` 流式读键。
	keys, nested, ok := jsonTopKeys(content)
	if ok {
		for _, key := range keys {
			idx := -1
			needle := `"` + key + `"`
			for i, l := range lines {
				if strings.Contains(l, needle) {
					idx = i
					break
				}
			}
			if idx >= 0 {
				sections = append(sections, ArtifactSection{
					Name:      "key:" + key,
					LineStart: idx + 1,
					LineEnd:   idx + 1,
					CharCount: 0, // 对账 TS：JSON key 的 charCount 写死 0
				})
			}
		}

		parts := []string{"json file, " + strconv.Itoa(len(lines)) + " lines."}
		parts = append(parts, "Keys: "+strings.Join(keys, ", "))
		if len(nested) > 0 {
			parts = append(parts, "Nested: "+strings.Join(nested, ", "))
		}
		return SummarizeResult{Summary: strings.Join(parts, " "), Sections: sections}
	}

	return SummarizeResult{
		Summary:  "json file, " + strconv.Itoa(len(lines)) + " lines.",
		Sections: sections,
	}
}

// jsonTopKeys 按**出现顺序**返回顶层键，以及其中的「对象或数组」键。
//
// 对账 TS 的 `Object.keys(parsed)` + `typeof val === 'object' && val !== null`：
//   - 顶层必须是对象（数组 / null / 标量 → ok=false，走 catch 路径）
//   - nested 判定含**数组**（JS 的 `typeof [] === 'object'`）
//
// **重复键**（探针实测 JS 行为）：`JSON.parse` 对同名的后者**覆盖**前者——
// 故 `{"a":1,"a":{"b":2}}` 的 nested 含 `a`（取**最后**一个值），
// 而键序仍是**首次**出现的位置（`Object.keys` 只留一个 `a`）。
func jsonTopKeys(content string) (keys, nested []string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(content))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, false
	}
	if d, isDelim := tok.(json.Delim); !isDelim || d != '{' {
		return nil, nil, false
	}

	order := []string{}           // 首次出现顺序
	isNested := map[string]bool{} // 末次赋值的 nested 判定
	seen := map[string]bool{}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, false
		}
		key, isStr := keyTok.(string)
		if !isStr {
			return nil, nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, false
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
		t := strings.TrimSpace(string(raw))
		isNested[key] = len(t) > 0 && (t[0] == '{' || t[0] == '[')
	}

	for _, k := range order {
		if isNested[k] {
			nested = append(nested, k)
		}
	}
	return order, nested, true
}

// ---------------------------------------------------------------------------
// Generic
// ---------------------------------------------------------------------------

func summarizeGeneric(content, filePath string) SummarizeResult {
	lines := strings.Split(content, "\n")
	ext := popExt(filePath)
	return SummarizeResult{
		Summary:  ext + " file, " + strconv.Itoa(len(lines)) + " lines. low-detail summary, consider read_section for details.",
		Sections: []ArtifactSection{},
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// findBlockEnd 找花括号块的结束行（对账 TS `findBlockEnd`）。
//
// 从 startIdx 起累计 `{` / `}`；见到首个 `{` 后一旦计数 <= 0 即返回该行。
// **未见 `{` 时**（如单行声明）返回 `min(startIdx+20, len(lines)-1)`。
func findBlockEnd(lines []string, startIdx int) int {
	braceCount := 0
	foundOpen := false
	for i := startIdx; i < len(lines); i++ {
		for _, ch := range lines[i] {
			if ch == '{' {
				braceCount++
				foundOpen = true
			}
			if ch == '}' {
				braceCount--
			}
		}
		if foundOpen && braceCount <= 0 {
			return i
		}
	}
	return minInt(startIdx+20, len(lines)-1)
}

// findPythonBlockEnd 找缩进块的结束行（对账 TS `findPythonBlockEnd`）。
//
// 跳过空行；首个缩进 <= 起始行缩进的行 → 返回其**前一行**。
// **起始行全空白时** startIndent = -1，后续行缩进均 > -1，故走到末尾
// （对账 JS 的同一行为）。
func findPythonBlockEnd(lines []string, startIdx int) int {
	startIndent := firstNonSpaceIndex(lines[startIdx])
	for i := startIdx + 1; i < len(lines); i++ {
		if jsTrimSpace(lines[i]) == "" {
			continue
		}
		if firstNonSpaceIndex(lines[i]) <= startIndent {
			return i - 1
		}
	}
	return len(lines) - 1
}

// findMarkdownSectionEnd 找 Markdown 章节结束行（对账 TS）。
//
// 首个「同级或更高级」标题的前一行，否则文末。
func findMarkdownSectionEnd(lines []string, startIdx, level int) int {
	for i := startIdx + 1; i < len(lines); i++ {
		m := reMdHeading.FindStringSubmatch(lines[i])
		if m != nil && len(m[1]) <= level {
			return i - 1
		}
	}
	return len(lines) - 1
}

// grepLineFile 从 ripgrep 的 `path:line:content` 行里提取路径。
//
// 对账 TS `grepLineFile`——**锚定行号**以保住 Windows 盘符冒号
// （`C:\foo.ts:42:…` → `C:\foo.ts`，而非 `C`）。先试 `:` 形再试 `-` 形。
func grepLineFile(line string) string {
	// **用 jsTrimSpace**（对账 JS 的 `.trim()`——含 U+FEFF 等 Go 的
	// `strings.TrimSpace` 不处理的空白）。
	if m := reGrepColon.FindStringSubmatch(line); m != nil {
		return jsTrimSpace(m[1])
	}
	if m := reGrepDash.FindStringSubmatch(line); m != nil {
		return jsTrimSpace(m[1])
	}
	// 兜底：`line.split(':')[0] ?? ''`。
	if i := strings.Index(line, ":"); i >= 0 {
		return jsTrimSpace(line[:i])
	}
	return jsTrimSpace(line)
}

var (
	reGrepColon = jsRe(`(?m)^(.+?):\d+:`)
	reGrepDash  = jsRe(`(?m)^(.+?)-\d+-`)
)

// SummarizeGrepResult 摘要 grep 输出（对账 TS `summarizeGrepResult`）。
func SummarizeGrepResult(content, pattern string) SummarizeResult {
	lines := []string{}
	for _, l := range reCRLF.Split(content, -1) {
		if jsTrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}

	seen := map[string]bool{}
	var files []string
	for _, l := range lines {
		f := grepLineFile(l)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}

	s := `grep "` + pattern + `": ` + strconv.Itoa(len(lines)) + " matches in " + strconv.Itoa(len(files)) + " files. Files: " + strings.Join(head(files, 5), ", ")
	if len(files) > 5 {
		s += " (+" + strconv.Itoa(len(files)-5) + ")"
	}
	return SummarizeResult{Summary: s, Sections: []ArtifactSection{}}
}

var (
	reCRLF        = jsRe(`\r?\n`)
	reTestSummary = jsRe(`(?i)tests?` + jsWS + `*(?:pass|passed|fail|failed)|total`)
	reTestPass    = jsRe(`(?i)\d+` + jsWS + `+tests?` + jsWS + `+pass`)
	reTestPassed  = jsRe(`(?i)\d+` + jsWS + `+tests?` + jsWS + `+passed`)
	reErrLine     = jsRe(`(?i)error|fail`)
)

// SummarizeBashOutput 摘要 bash 输出（对账 TS `summarizeBashOutput`）。
func SummarizeBashOutput(content, command string, exitCode int) SummarizeResult {
	lines := strings.Split(content, "\n")
	status := "success"
	if exitCode != 0 {
		status = "failed (exit " + strconv.Itoa(exitCode) + ")"
	}

	// 测试总结行：三级回退（对账 TS 的 `??` 链）。
	testSummary := ""
	found := false
	for _, re := range []*regexp.Regexp{reTestSummary, reTestPass, reTestPassed} {
		for _, l := range lines {
			if re.MatchString(l) {
				testSummary = l
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	var errorLines []string
	for _, l := range lines {
		if reErrLine.MatchString(l) {
			errorLines = append(errorLines, l)
			if len(errorLines) >= 3 {
				break
			}
		}
	}

	parts := []string{"[" + jsSlice(command, 40) + "] " + status + ", " + strconv.Itoa(len(lines)) + " lines."}
	if found {
		parts = append(parts, jsTrimSpace(testSummary))
	}
	// 成功且无测试总结时，补最后一行非空内容作上下文。
	if exitCode == 0 && !found {
		last := ""
		for _, l := range lines {
			if jsTrimSpace(l) != "" {
				last = l
			}
		}
		if last != "" {
			parts = append(parts, jsTrimSpace(last))
		}
	}
	if len(errorLines) > 0 && exitCode != 0 {
		trimmed := make([]string, 0, len(errorLines))
		for _, l := range errorLines {
			trimmed = append(trimmed, jsSlice(jsTrimSpace(l), 60))
		}
		parts = append(parts, "Errors: "+strings.Join(trimmed, "; "))
	}

	return SummarizeResult{Summary: strings.Join(parts, " "), Sections: []ArtifactSection{}}
}

// ---------------------------------------------------------------------------
// JS 语义辅助
// ---------------------------------------------------------------------------

// jsWS 是 JS 正则 `\s` 的**字符类**（含外层方括号）。
//
// **Go 的 `\s` 只有 `[\t\n\f\r ]`**，而 JS 还含 `\v`、`\p{Zs}`、U+FEFF 等
// ——直接用 Go 的 `\s` 会在这些字符上产生字节差异。
//
// 用法形如 `jsWS + `+“ → `[...]+`、`jsWS + `*“ → `[...]*`。
const jsWS = `[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`

// jsRe 编译带 JS 语义的 `\s` 的正则。
//
// 失败即 panic（编译期常量模式，语法错误是编码错误而非运行时状况）。
func jsRe(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }

// isJSSpace 报告 r 是否为 JS 的空白字符。
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// jsTrimSpace 对账 JS 的 `String.prototype.trim()`（含 U+FEFF，Go 的
// `strings.TrimSpace` 不含）。
func jsTrimSpace(s string) string {
	start := 0
	for start < len(s) {
		r, size := utf8.DecodeRuneInString(s[start:])
		if !isJSSpace(r) {
			break
		}
		start += size
	}
	end := len(s)
	for end > start {
		r, size := utf8.DecodeLastRuneInString(s[:end])
		if !isJSSpace(r) {
			break
		}
		end -= size
	}
	return s[start:end]
}

// firstNonSpaceIndex 对账 JS 的 `line.search(/\S/)`。
//
// 返回**字节**索引（-1 = 全空白）。行首空白均为 ASCII，故与 JS 的
// UTF-16 code unit 索引一致。
func firstNonSpaceIndex(line string) int {
	for i, r := range line {
		if !isJSSpace(r) {
			return i
		}
	}
	return -1
}

// jsSlice 对账 JS 的 `s.slice(0, n)`——按 **UTF-16 code unit** 截断。
//
// 中文等 BMP 外字符占 2 个 code unit，故字节切会截错长度（HANDOFF 坑 7）。
func jsSlice(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i, r := range s {
		if count >= n {
			return s[:i]
		}
		if r > 0xFFFF {
			count += 2
			if count > n {
				return s[:i] // 截在代理对中间 → JS 会产出孤立代理，此处取安全截断
			}
		} else {
			count++
		}
	}
	return s
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// head 返回前 n 个元素（不足则全部）——对账 JS 的 `.slice(0, n)`。
func head(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
