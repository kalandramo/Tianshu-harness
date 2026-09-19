package compact

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// contextCollapseCharsPerToken 是折叠摘要的 token 估算比（对账 TS 的
// `CHARS_PER_TOKEN = 4`）。
const contextCollapseCharsPerToken = 4

// collapseMinChars 是折叠的最小内容长度——低于此不值得折叠。
//
// 对账 TS：`if (content.length < 200) return null`。
const collapseMinChars = 200

// collapseToolResultMinTurnAge 是**语义折叠**的最小轮龄。
//
// 对账 context-collapse.ts：`if (turnAge < 2) return null`。
// **注意与 micro.go 的 `collapseMinTurnAge = 4` 不同**——TS 里两处阈值不同：
// micro.ts 决定「是否尝试折叠」，context-collapse.ts 再判一次。
const collapseToolResultMinTurnAge = 2

// ArtifactMarkerRegex 是 artifact 恢复标记的正则（对账 TS 的
// `ARTIFACT_MARKER_REGEX`）。
//
// **契约**：所有产出 artifact 引用的工具 MUST 把 `[artifact:XYZ]` 放在
// 工具结果的**最后**——使用说明、摘要等后缀都放在它**之前**。
// 每个有损变换（预算驱逐、轮次读预算、上下文压力截断、工具类型预算、
// stale-round 压缩）都必须保留这个标记，让模型仍能 `read_section` 取回原文。
//
// ⚠️ **Go 侧当前无 artifact 生产端**（artifact store 未移植），故所有
// artifact 分支在生产路径上不会触发。保留复刻是为了：TS 契约完整性 +
// 未来接入 artifact store 时无需改动本模块。
var ArtifactMarkerRegex = regexp.MustCompile(`\[artifact:([A-Za-z0-9_-]+)\]\s*$`)

// artifactAnyRegex 匹配任意位置的 artifact 标记（不要求行尾）。
//
// 对账 TS `collapseGrepResult` 里的 `/\[artifact:[^\]]+\]/`。
var artifactAnyRegex = regexp.MustCompile(`\[artifact:[^\]]+\]`)

// CollapsedResult 是折叠结果。
//
// 对账 TS 的 `CollapsedResult`。
type CollapsedResult struct {
	ToolName        string
	Summary         string
	OriginalTokens  int
	CollapsedTokens int
}

// CollapseToolResult 尝试把工具结果折叠为语义摘要。
//
// 对账 `collapseToolResult`。**两个前置条件**（任一不满足返回 nil）：
//
//   - `len(content) < 200`——太小不值得折叠
//   - `turnAge < 2`——太新（可能还会被引用）
//
// **分派表**（按工具名）：
//
//	grep / search          → 统计匹配数与文件数
//	read_file              → 行数 + 类/函数/导出符号
//	bash                   → 行数 + exit code + 失败行 + tail
//	write_file / edit_file → 写入字符数
//	run_tests              → 通过/失败计数 + 失败测试名
//	delegate_task / _batch → profile + 摘要片段
//	其余                   → 通用（行数 + 字符数 + 前 3 行预览）
//
// 最后统一走 `PreserveArtifactRef` 保留恢复标记。
func CollapseToolResult(toolName, content string, turnAge int) *CollapsedResult {
	if charLen(content) < collapseMinChars {
		return nil
	}
	if turnAge < collapseToolResultMinTurnAge {
		return nil
	}

	originalTokens := tokenCeil(content)

	var result CollapsedResult
	switch toolName {
	case "grep", "search":
		result = CollapseGrepResult(toolName, content, originalTokens)
	case "read_file":
		result = CollapseReadFileResult(content, originalTokens)
	case "bash":
		result = CollapseBashResult(content, originalTokens)
	case "write_file", "edit_file":
		result = collapseWriteResult(toolName, content, originalTokens)
	case "run_tests":
		result = CollapseRunTestsResult(content, originalTokens)
	case "delegate_task", "delegate_batch":
		result = CollapseDelegateResult(toolName, content, originalTokens)
	default:
		result = CollapseGenericResult(toolName, content, originalTokens)
	}

	out := PreserveArtifactRef(content, result)
	return &out
}

// PreserveArtifactRef 在折叠摘要里保留 artifact 恢复提示。
//
// 对账 `preserveArtifactRef`。**语义**：T7 分层折叠不能成为召回盲点——
// 大工具结果携带 `[artifact:id]` 标记（存储层 artifact 拦截添加）。折叠成
// `[collapsed ...]` 摘要时要保住 id，让模型仍能 `read_section` 取回原文
// （存储原文未动，只有请求副本被折叠，引用依然有效）。无标记的结果原样
// 返回（**不在请求热路径上写盘**）。
func PreserveArtifactRef(content string, result CollapsedResult) CollapsedResult {
	m := regexp.MustCompile(`\[artifact:([^\]]+)\]`).FindStringSubmatch(content)
	if m == nil {
		return result
	}
	artifactID := m[1]
	if strings.Contains(result.Summary, artifactID) {
		return result
	}
	// 在摘要的收尾方括号前插入回读提示
	var summary string
	if strings.HasSuffix(result.Summary, "]") {
		summary = result.Summary[:len(result.Summary)-1] + " | read_section artifact:" + artifactID + "]"
	} else {
		summary = result.Summary + " [read_section artifact:" + artifactID + "]"
	}
	result.Summary = summary
	result.CollapsedTokens = tokenCeil(summary)
	return result
}

// CollapseGrepResult 折叠 grep/search 结果。
//
// 对账 `collapseGrepResult`。**两条路径**：
//
//   - **含 artifact 标记**：正文已是工具层的 head/tail 截断副本——对残缺
//     正文重算行数会把 head 的少数行包装成完整读数（TS 台账 F4：实测
//     65/20 被抹成 8/4，且 `[artifact:…]` 行本身还会被数成一个「文件」）。
//     故沿用工具层给出的全量统计行（`grep "…": N matches in M files`）；
//     解析不到则只留回读提示。
//   - **无标记**：从 `file:...` 行统计文件集与匹配数，列出前 8 个文件。
func CollapseGrepResult(toolName, content string, originalTokens int) CollapsedResult {
	if artifactAnyRegex.MatchString(content) {
		var statLine string
		for _, l := range strings.Split(content, "\n") {
			t := strings.TrimSpace(l)
			if grepStatLineRegex.MatchString(t) {
				statLine = t
				break
			}
		}
		var summary string
		if statLine != "" {
			summary = "[collapsed " + toolName + ": " + sliceRunes(statLine, 240) + "]"
		} else {
			summary = "[collapsed " + toolName + ": 正文已截断，完整匹配列表见 artifact]"
		}
		return CollapsedResult{
			ToolName:        toolName,
			Summary:         summary,
			OriginalTokens:  originalTokens,
			CollapsedTokens: tokenCeil(summary),
		}
	}

	fileSet := map[string]bool{}
	var fileOrder []string
	matchCount := 0
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		m := grepFileLineRegex.FindStringSubmatch(l)
		if m != nil {
			if !fileSet[m[1]] {
				fileSet[m[1]] = true
				fileOrder = append(fileOrder, m[1])
			}
			matchCount++
		}
	}

	topFiles := fileOrder
	if len(topFiles) > 8 {
		topFiles = topFiles[:8]
	}
	moreFiles := ""
	if len(fileOrder) > 8 {
		moreFiles = " (+" + microItoa(len(fileOrder)-8) + " more)"
	}

	summary := "[collapsed " + toolName + ": " + microItoa(matchCount) + " matches in " +
		microItoa(len(fileOrder)) + " files: " + strings.Join(topFiles, ", ") + moreFiles + "]"
	return CollapsedResult{
		ToolName:        toolName,
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// CollapseReadFileResult 折叠 read_file 结果。
//
// 对账 `collapseReadFileResult`。扫描**前 100 行**提取类名/函数名/导出符号，
// 摘要形如 `[collapsed read_file: 42 lines, classes: Foo, functions: bar, baz]`。
//
// **一处不对称逻辑**：`exports` 只在**无类且无函数**时才输出计数——
// 有类或函数时导出信息被省略（TS 原样如此）。
func CollapseReadFileResult(content string, originalTokens int) CollapsedResult {
	return collapseReadFileResultN(content, originalTokens, 100)
}

func collapseReadFileResultN(content string, originalTokens, maxScanLines int) CollapsedResult {
	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	var exports, functions, classes []string
	scan := lines
	if len(scan) > maxScanLines {
		scan = scan[:maxScanLines]
	}
	for _, line := range scan {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "export ") {
			exports = append(exports, sliceRunes(trimmed, 80))
		}
		if funcDeclRegex.MatchString(trimmed) {
			if m := funcNameRegex.FindStringSubmatch(trimmed); m != nil {
				functions = append(functions, m[1])
			}
		}
		if classDeclRegex.MatchString(trimmed) {
			if m := classNameRegex.FindStringSubmatch(trimmed); m != nil {
				classes = append(classes, m[1])
			}
		}
	}

	parts := []string{microItoa(lineCount) + " lines"}
	if len(classes) > 0 {
		parts = append(parts, "classes: "+strings.Join(sliceN(classes, 5), ", "))
	}
	if len(functions) > 0 {
		parts = append(parts, "functions: "+strings.Join(sliceN(functions, 8), ", "))
	}
	if len(exports) > 0 && len(classes) == 0 && len(functions) == 0 {
		parts = append(parts, microItoa(len(exports))+" exports")
	}

	summary := "[collapsed read_file: " + strings.Join(parts, ", ") + "]"
	return CollapsedResult{
		ToolName:        "read_file",
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// CollapseBashResult 折叠 bash 结果。
//
// 对账 `collapseBashResult`。摘要 = 行数 + exit code + 失败行（前 5，各截
// 80 字符）+ tail（后 3 行，各截 60 字符）。
//
// **失败行判定**：匹配 `fail|error|FAIL|ERROR|✗|✘|❌`（忽略大小写），
// 但**排除**以 `✓✔●◌⊙` 开头的行（那些是成功标记）。
func CollapseBashResult(content string, originalTokens int) CollapsedResult {
	var lines []string
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	lineCount := len(lines)

	lastLines := lines
	if len(lastLines) > 3 {
		lastLines = lastLines[len(lastLines)-3:]
	}

	exitCode := ""
	if m := bashExitCodeRegex.FindStringSubmatch(content); m != nil {
		exitCode = m[1]
	}

	var failLines []string
	for _, l := range lines {
		if bashFailRegex.MatchString(l) && !bashSuccessPrefixRegex.MatchString(l) {
			failLines = append(failLines, sliceRunes(l, 80))
			if len(failLines) == 5 {
				break
			}
		}
	}

	parts := []string{microItoa(lineCount) + " lines output"}
	if exitCode != "" {
		parts = append(parts, "exit "+exitCode)
	}
	if len(failLines) > 0 {
		parts = append(parts, "fails: "+strings.Join(failLines, " | "))
	}
	var tail []string
	for _, l := range lastLines {
		tail = append(tail, sliceRunes(l, 60))
	}
	parts = append(parts, "tail: "+strings.Join(tail, " | "))

	summary := "[collapsed bash: " + strings.Join(parts, ", ") + "]"
	return CollapsedResult{
		ToolName:        "bash",
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

func collapseWriteResult(toolName, content string, originalTokens int) CollapsedResult {
	summary := "[collapsed " + toolName + ": " + microItoa(charLen(content)) + " chars written]"
	return CollapsedResult{
		ToolName:        toolName,
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// CollapseRunTestsResult 折叠 run_tests 结果。
//
// 对账 `collapseRunTestsResult`。摘要 = 通过/失败计数 + 失败数 + exit code +
// 失败测试名（前 5）。
//
// ⚠️ **正则口径与 Go 生产路径不匹配**：TS 的正则匹配的是 TS 自己的
// `formatOutput` 输出（中文 `退出码：N` / `N 通过，M 失败，K 跳过`），
// 而 Go 的 `run_tests.go` 用 `formatTestResult`（`计数: passed=N failed=M`）。
// 按项目纪律忠实复刻 TS 正则——若未来 Go 侧统一输出格式，需同步改这里。
func CollapseRunTestsResult(content string, originalTokens int) CollapsedResult {
	passed := -1
	failed := -1
	if m := runTestsPassedRegex.FindStringSubmatch(content); m != nil {
		passed = atoiSafe(m[1])
	}
	if n, ok := findRunTestsFailed(content); ok {
		failed = n
	}
	exitCode := ""
	if m := runTestsExitCodeRegex.FindStringSubmatch(content); m != nil {
		exitCode = m[1]
	}

	var failureLines []string
	for _, l := range strings.Split(content, "\n") {
		if runTestsFailLineRegex.MatchString(l) {
			name := runTestsFailStripRegex.ReplaceAllString(l, "")
			name = strings.TrimSpace(name)
			if name != "" {
				failureLines = append(failureLines, sliceRunes(name, 80))
				if len(failureLines) == 5 {
					break
				}
			}
		}
	}

	var parts []string
	if passed >= 0 || failed >= 0 {
		p := passed
		if p < 0 {
			p = 0
		}
		f := failed
		if f < 0 {
			f = 0
		}
		parts = append(parts, microItoa(p)+"/"+microItoa(p+f)+" passed")
	}
	if failed > 0 {
		parts = append(parts, microItoa(failed)+" failed")
	}
	if exitCode != "" {
		parts = append(parts, "exit "+exitCode)
	}
	if len(failureLines) > 0 {
		parts = append(parts, "failures: "+strings.Join(failureLines, ", "))
	}

	summary := "[collapsed run_tests: " + strings.Join(parts, " · ") + "]"
	return CollapsedResult{
		ToolName:        "run_tests",
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// CollapseDelegateResult 折叠 delegate_task / delegate_batch 结果。
//
// 对账 `collapseDelegateResult`。提取 profile（`profile:` 或 `worker:` 后的
// 词，缺省 `worker`）+ 首个有意义句子（`summary:` 后的 20–150 字符）。
// 片段达 150 字符时追加 `…`。
func CollapseDelegateResult(toolName, content string, originalTokens int) CollapsedResult {
	firstLine := ""
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != "" {
			firstLine = l
			break
		}
	}
	if firstLine == "" {
		firstLine = sliceRunes(content, 200)
	}
	profile := "worker"
	if m := delegateProfileRegex.FindStringSubmatch(firstLine); m != nil {
		profile = m[1]
	}

	snippet := strings.TrimSpace(firstLine)
	if m := delegateSummaryRegex.FindStringSubmatch(content); m != nil {
		snippet = strings.TrimSpace(m[1])
	}
	snippet = sliceRunes(snippet, 150)
	ellipsis := ""
	if len([]rune(snippet)) >= 150 {
		ellipsis = "…"
	}

	summary := "[collapsed " + toolName + ": " + profile + " — " + snippet + ellipsis + "]"
	return CollapsedResult{
		ToolName:        toolName,
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// CollapseGenericResult 是未识别工具的兜底折叠。
//
// 对账 `collapseGenericResult`。摘要 = 行数 + 字符数 + 前 3 行预览
// （各截 80 字符，以 ` | ` 连接）。
func CollapseGenericResult(toolName, content string, originalTokens int) CollapsedResult {
	lines := strings.Split(content, "\n")
	previewN := lines
	if len(previewN) > 3 {
		previewN = previewN[:3]
	}
	var preview []string
	for _, l := range previewN {
		preview = append(preview, sliceRunes(l, 80))
	}
	summary := "[collapsed " + toolName + ": " + microItoa(len(lines)) + " lines, " +
		microItoa(charLen(content)) + " chars. Preview: " + strings.Join(preview, " | ") + "]"
	return CollapsedResult{
		ToolName:        toolName,
		Summary:         summary,
		OriginalTokens:  originalTokens,
		CollapsedTokens: tokenCeil(summary),
	}
}

// ── 正则（对账 TS 各折叠器的内联正则）──

var (
	// grepStatLineRegex 匹配工具层的全量统计行：`grep "…": N matches in M files`
	grepStatLineRegex = regexp.MustCompile(`^(grep|search) ".*": \d+ matches in \d+ files`)
	// grepFileLineRegex 匹配 `file:...` 形式的匹配行
	grepFileLineRegex = regexp.MustCompile(`^([^\s:]+):`)
	// funcDeclRegex / funcNameRegex 对账 TS 的 `/^(export\s+)?(async\s+)?function\s+\w/`
	funcDeclRegex = regexp.MustCompile(`^(export\s+)?(async\s+)?function\s+\w`)
	funcNameRegex = regexp.MustCompile(`function\s+(\w+)`)
	// classDeclRegex / classNameRegex 对账 TS 的 `/^(export\s+)?class\s+\w/`
	classDeclRegex = regexp.MustCompile(`^(export\s+)?class\s+\w`)
	classNameRegex = regexp.MustCompile(`class\s+(\w+)`)
	// bashExitCodeRegex 对账 TS 的 `/exit code[:\s]+(\d+)/i`
	bashExitCodeRegex = regexp.MustCompile(`(?i)exit code[:\s]+(\d+)`)
	// bashFailRegex / bashSuccessPrefixRegex 对账 TS 的失败行判定
	bashFailRegex          = regexp.MustCompile(`(?i)fail|error|FAIL|ERROR|✗|✘|❌`)
	bashSuccessPrefixRegex = regexp.MustCompile(`^\s*[✓✔●◌⊙]`)
	// runTests* 对账 TS run-tests.ts 导出的三个正则（**中文口径**）。
	//
	// ⚠️ TS 的失败正则是 `/(\d+)\s+失败(?!项)/`——**负向前瞻是 JS 专有语法，
	// RE2 不支持**（会 panic）。这里保留无前瞻的基础正则，由
	// `findRunTestsFailed` 手写等价逻辑（匹配后检查下一字符是否为「项」）。
	runTestsPassedRegex   = regexp.MustCompile(`(\d+)\s+通过`)
	runTestsFailedRegex   = regexp.MustCompile(`(\d+)\s+失败`)
	runTestsExitCodeRegex = regexp.MustCompile(`(?i)退出码[：:]\s*(\d+)`)
	// runTestsFailLineRegex / runTestsFailStripRegex 对账 TS 的失败测试名提取
	runTestsFailLineRegex  = regexp.MustCompile(`^\s*[✗✘❌]|^\s*FAIL\s`)
	runTestsFailStripRegex = regexp.MustCompile(`^\s*[✗✘❌]\s*|^\s*FAIL\s+`)
	// delegateProfileRegex 对账 TS 的 `/(?:profile|worker)[:\s]*(\w+)/i`
	delegateProfileRegex = regexp.MustCompile(`(?i)(?:profile|worker)[:\s]*(\w+)`)
	// delegateSummaryRegex 对账 TS 的 `/summary[:\s]+(.{20,150}?)(?:\.|$)/is`
	delegateSummaryRegex = regexp.MustCompile(`(?is)summary[:\s]+(.{20,150}?)(?:\.|$)`)
)

// ── 小工具 ──

// sliceRunes 按**码点**截断（TS 的 `slice(0, n)` 按 UTF-16 码元，但对 BMP
// 内的字符两者一致；CJK 与 ASCII 都在 BMP 内）。
func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func sliceN[T any](s []T, n int) []T {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// findRunTestsFailed 对账 TS 的 `/(\d+)\s+失败(?!项)/`。
//
// **RE2 不支持负向前瞻**，故手写等价逻辑：遍历所有 `N 失败` 匹配，返回
// **第一个**其后不跟「项」的计数（TS 的 `String.match` 同样返回首个满足
// 前瞻的匹配，而非首个字面匹配）。
//
// 为什么要排除「项」：TS 的 formatOutput 有标题行「失败项：」——不加前瞻
// 会把标题里的数字（若有）或后续内容误当计数。
func findRunTestsFailed(content string) (int, bool) {
	locs := runTestsFailedRegex.FindAllStringSubmatchIndex(content, -1)
	for _, loc := range locs {
		end := loc[1]
		if end < len(content) {
			r, _ := utf8.DecodeRuneInString(content[end:])
			if r == '项' {
				continue
			}
		}
		return atoiSafe(content[loc[2]:loc[3]]), true
	}
	return 0, false
}

// charLen 返回 TS 语义下的字符串长度。
//
// ⚠️ **不是 `len(s)`**——TS 的 `String.length` 是 **UTF-16 码元数**，而
// Go 的 `len(s)` 是**字节数**。中文/日文/韩文在 UTF-8 下 3 字节、在 UTF-16
// 下 1 码元；emoji 等增补平面字符在 UTF-16 下 2 码元（代理对）。
//
// 本模块所有 token 估算与长度判定都必须走这里，否则 CJK 内容会系统性高估。
// 实测：`'退出码：1\n3 通过…' + 'z'*200` 的码点数是 243（→ ceil/4=61），
// 字节数是 277（→ ceil/4=70）——oracle 的 `run-tests-chinese` 锁定 61。
func charLen(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2 // 增补平面 → 代理对，占 2 个 UTF-16 码元
		} else {
			n++
		}
	}
	return n
}

// tokenCeil 按 TS 的 `Math.ceil(len / 4)` 估算 token 数。
func tokenCeil(s string) int {
	return (charLen(s) + contextCollapseCharsPerToken - 1) / contextCollapseCharsPerToken
}
