package tools

// focusedread.go —— 任务导向的聚焦读取。
//
// 对账 TS `src/tools/focused-read.ts`（193 行）。
//
// # 为什么需要它
//
// `read_file` 的 schema 声明了 `focus` / `focus_max_matches` 参数，但 Go 侧
// **此前完全未实现**——用户传 focus 时被静默忽略，模型以为拿到聚焦结果、
// 实际是全文件。这比"未移植"更糟：**静默失效**让模型基于错误前提推理。
//
// # 语义
//
// 确定性、只读。选中高信号行区间，并**明示遗漏**（不声称未选中的代码无关），
// 指向精确范围重读。
//
// # 与 TS 的已知差异（明示）
//
// TS 的 `structuralSkeleton`（无匹配分支）用 `foldCode`（428 行，未移植）
// 产折叠骨架。Go 侧首版走**回退路径**：`STRUCTURAL_LINE` 过滤前 100 行——
// 这是 TS 里 `foldCode` 不可用时的同一回退。差异仅在「无匹配」分支的骨架
// 内容，header/footer 形态一致。

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

// FocusedReadRange 对账 TS `FocusedReadRange`。
type FocusedReadRange struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine"`
	Score     int `json:"score"`
}

// FocusedReadResult 对账 TS `FocusedReadResult`。
type FocusedReadResult struct {
	Content      string
	Ranges       []FocusedReadRange
	MatchedLines int
	OmittedLines int
	Matched      bool
}

// FocusedReadOptions 对账 TS `FocusedReadOptions`。
type FocusedReadOptions struct {
	FilePath     string
	Content      string
	Focus        string
	MaxChars     int
	MaxMatches   int
	ContextLines int
}

// 对账 TS 常量。
const (
	defaultMaxMatches   = 8
	defaultContextLines = 2
	maxFocusLength      = 240
)

// focusStopWords 对账 TS `FOCUS_STOP_WORDS`（含中文）。
var focusStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "for": true, "from": true,
	"find": true, "file": true, "into": true, "look": true, "read": true,
	"the": true, "this": true, "with": true, "please": true, "show": true,
	"where": true, "what": true, "which": true, "code": true,
	"source": true, "implementation": true,
	"请": true, "帮我": true, "查找": true, "读取": true, "看看": true,
	"文件": true, "代码": true, "实现": true, "相关": true, "问题": true,
	"一下": true, "里面": true,
}

// structuralLineRe 对账 TS `STRUCTURAL_LINE`。
//
// `^\s*(?:import\b|export\b|(?:async\s+)?function\b|class\b|interface\b|type\b|enum\b|const\b|let\b|var\b|def\b|struct\b|impl\b|trait\b|#{1,6}\s)`
//
// **注意**：逐行调用，故用 `(?m)^`（与 summarize.go 同一处理）。
var structuralLineRe = regexp.MustCompile(`(?m)^[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(?:import\b|export\b|(?:async[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+)?function\b|class\b|interface\b|type\b|enum\b|const\b|let\b|var\b|def\b|struct\b|impl\b|trait\b|#{1,6}[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}])`)

// reJSSpaces 对账 JS 的 `/\s+/g`（含 U+FEFF 等，见 summarize.go 的 jsWS 说明）。
var reJSSpaces = regexp.MustCompile(`[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+`)

// reFocusFragment 对账 TS `/[a-z_$][a-z0-9_$-]{1,}|[\u4e00-\u9fff]+/g`。
var reFocusFragment = regexp.MustCompile(`[a-z_$][a-z0-9_$-]{1,}|[\x{4e00}-\x{9fff}]+`)

// jsSlice 对账 JS 的 `s.slice(0, n)`（UTF-16 code unit 语义）。
//
// 与 `jsSliceHead` 同实现——此处命名对齐 TS 的 `.slice(0, N)` 调用点。
func jsSlice(s string, n int) string { return jsSliceHead(s, n) }

// jsTrimSpace 对账 JS 的 `.trim()`（含 U+FEFF 等，Go 的 strings.TrimSpace 不含）。
func jsTrimSpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return reJSSpaces.MatchString(string(r))
	})
}

// normalizeFocus 对账 TS `normalizeFocus`。
//
// `focus.replace(/\s+/g, ' ').trim().slice(0, MAX_FOCUS_LENGTH)`
func normalizeFocus(focus string) string {
	collapsed := reJSSpaces.ReplaceAllString(focus, " ")
	trimmed := jsTrimSpace(collapsed)
	return jsSlice(trimmed, maxFocusLength)
}

// tokenizeFocus 对账 TS `tokenizeFocus`。
//
// 英文片段：长度 >= 2 且非停用词 → 整词加入。
// 中文片段：长度 1 → 直接加入（非停用词）；否则产**所有相邻 bigram**。
func tokenizeFocus(focus string) []string {
	fragments := reFocusFragment.FindAllString(strings.ToLower(normalizeFocus(focus)), -1)
	tokens := []string{}
	seen := map[string]bool{}

	for _, frag := range fragments {
		if len(frag) > 0 && (frag[0] == '$' || frag[0] == '_' ||
			(frag[0] >= 'a' && frag[0] <= 'z')) {
			// 英文/标识符片段
			if len([]rune(frag)) >= 2 && !focusStopWords[frag] {
				if !seen[frag] {
					seen[frag] = true
					tokens = append(tokens, frag)
				}
			}
			continue
		}
		// 中文片段（按 rune 处理）
		runes := []rune(frag)
		if len(runes) == 1 {
			if !focusStopWords[frag] && !seen[frag] {
				seen[frag] = true
				tokens = append(tokens, frag)
			}
			continue
		}
		for i := 0; i < len(runes)-1; i++ {
			bigram := string(runes[i : i+2])
			if !focusStopWords[bigram] && !seen[bigram] {
				seen[bigram] = true
				tokens = append(tokens, bigram)
			}
		}
	}
	return tokens
}

// scoreLine 对账 TS `scoreLine`。
func scoreLine(line, focus string, tokens []string) int {
	lower := strings.ToLower(line)
	nf := strings.ToLower(normalizeFocus(focus))
	score := 0

	if UTF16Len(nf) >= 4 && strings.Contains(lower, nf) {
		score += 24
	}
	matches := 0
	for _, tok := range tokens {
		if !strings.Contains(lower, tok) {
			continue
		}
		matches++
		if strings.Contains(tok, "_") || UTF16Len(tok) >= 6 {
			score += 7
		} else {
			score += 3
		}
	}
	if matches > 1 {
		score += matches * 2
	}
	if matches > 0 && structuralLineRe.MatchString(line) {
		score += 4
	}
	return score
}

// mergeRanges 对账 TS `mergeRanges`。
//
// 按 startLine 排序；相邻（`range.startLine <= prev.endLine + 1`）则合并，
// score 取 max。
func mergeRanges(ranges []FocusedReadRange) []FocusedReadRange {
	sorted := make([]FocusedReadRange, len(ranges))
	copy(sorted, ranges)
	// 稳定排序（对账 JS 的 sort——现代 V8 是稳定的）。
	stableSortByStart(sorted)

	merged := []FocusedReadRange{}
	for _, r := range sorted {
		if len(merged) == 0 {
			merged = append(merged, r)
			continue
		}
		prev := &merged[len(merged)-1]
		if r.StartLine > prev.EndLine+1 {
			merged = append(merged, r)
			continue
		}
		if r.EndLine > prev.EndLine {
			prev.EndLine = r.EndLine
		}
		if r.Score > prev.Score {
			prev.Score = r.Score
		}
	}
	return merged
}

func stableSortByStart(rs []FocusedReadRange) {
	// 插入排序（稳定；ranges 数量级很小——maxMatches <= 20）。
	for i := 1; i < len(rs); i++ {
		cur := rs[i]
		j := i - 1
		for j >= 0 && rs[j].StartLine > cur.StartLine {
			rs[j+1] = rs[j]
			j--
		}
		rs[j+1] = cur
	}
}

// structuralSkeleton 对账 TS `structuralSkeleton`。
//
// TS 首选 `foldCode` 产折叠骨架；`wasFolded=false` 时回退到
// `STRUCTURAL_LINE` 过滤前 100 行。**本刀已移植 foldCode**，故走首选路径。
func structuralSkeleton(filePath string, lines []string, content string, maxChars int) string {
	folded := FoldCode(content, FoldOptions{FilePath: filePath, MaxLines: 100, MaxLinesSet: true})
	var structural string
	if folded.WasFolded {
		structural = folded.Folded
	} else {
		// 回退（对账 TS 的 `: lines.filter(...).slice(0, 100).join('\n')`）。
		parts := []string{}
		for _, l := range lines {
			if structuralLineRe.MatchString(l) {
				parts = append(parts, l)
				if len(parts) >= 100 {
					break
				}
			}
		}
		structural = strings.Join(parts, "\n")
	}
	body := jsTrimSpace(structural)
	if body == "" {
		end := 40
		if len(lines) < end {
			end = len(lines)
		}
		body = strings.Join(lines[:end], "\n")
	}
	floor := 600
	if maxChars > floor {
		floor = maxChars
	}
	return jsSlice(body, floor)
}

// renderRanges 对账 TS `renderRanges`。
func renderRanges(lines []string, ranges []FocusedReadRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		start := r.StartLine
		end := r.EndLine + 1
		if start < 0 {
			start = 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		if start > len(lines) {
			start = len(lines)
		}
		if end < start {
			end = start
		}
		seg := lines[start:end]
		var b strings.Builder
		for i, l := range seg {
			b.WriteString(padStart(strconv.Itoa(r.StartLine+i+1), 5))
			b.WriteString(" | ")
			b.WriteString(l)
			b.WriteString("\n")
		}
		body := strings.TrimRight(b.String(), "\n")
		parts = append(parts, "--- L"+strconv.Itoa(r.StartLine+1)+"-L"+strconv.Itoa(r.EndLine+1)+
			" (relevance "+strconv.Itoa(r.Score)+") ---\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

// padStart 对账 JS 的 `String.prototype.padStart`。
func padStart(s string, width int) string {
	n := UTF16Len(s)
	if n >= width {
		return s
	}
	return strings.Repeat(" ", width-n) + s
}

// renderFocusedContent 对账 TS `renderFocusedContent`。
func renderFocusedContent(filePath, focus string, lines []string, summary string,
	ranges []FocusedReadRange, maxChars int) string {

	shownLines := 0
	for _, r := range ranges {
		shownLines += r.EndLine - r.StartLine + 1
	}
	headerParts := []string{
		"[focused-read] " + filePath,
		"focus: " + normalizeFocus(focus),
		"source: " + strconv.Itoa(len(lines)) + " lines, " +
			strconv.Itoa(UTF16Len(strings.Join(lines, "\n"))) + " chars; showing " +
			strconv.Itoa(shownLines) + " relevant lines",
	}
	if summary != "" {
		headerParts = append(headerParts, "structural summary: "+summary)
	}
	headerParts = append(headerParts, "Only the ranges below are selected evidence; this is not the complete file.")
	header := strings.Join(headerParts, "\n")

	var body string
	if len(ranges) > 0 {
		body = renderRanges(lines, ranges)
	} else {
		inner := maxChars - UTF16Len(header)
		if inner < 800 {
			inner = 800
		}
		body = "No direct focus match. Structural outline only:\n" +
			structuralSkeleton(filePath, lines, strings.Join(lines, "\n"), inner)
	}
	omitted := len(lines) - shownLines
	if omitted < 0 {
		omitted = 0
	}
	footer := "\n\n[focused-read] omitted " + strconv.Itoa(omitted) +
		" source lines; use read_file(offset, limit) for an exact range."
	output := header + "\n\n" + body + footer
	if UTF16Len(output) <= maxChars {
		return output
	}
	cap := maxChars - 80
	if cap < 0 {
		cap = 0
	}
	return jsSlice(output, cap) + "\n...[focused-read output capped at " + strconv.Itoa(maxChars) + " chars]"
}

// BuildFocusedReadView 对账 TS `buildFocusedReadView`。
func BuildFocusedReadView(o FocusedReadOptions) FocusedReadResult {
	focus := normalizeFocus(o.Focus)
	lines := strings.Split(o.Content, "\n")
	tokens := tokenizeFocus(focus)

	contextLines := int(floorDiv(o.ContextLines, 1))
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > 8 {
		contextLines = 8
	}
	maxMatches := o.MaxMatches
	if maxMatches < 1 {
		maxMatches = 1
	}
	if maxMatches > 20 {
		maxMatches = 20
	}
	maxChars := o.MaxChars
	if maxChars < 800 {
		maxChars = 800
	}
	summary := jsTrimSpace(artifact.SummarizeFileContent(o.Content, o.FilePath).Summary)

	// 打分排序。
	ranked := []focusEntry{}
	if len(tokens) > 0 {
		for i, l := range lines {
			s := scoreLine(l, focus, tokens)
			if s > 0 {
				ranked = append(ranked, focusEntry{index: i, score: s})
			}
		}
		// 对账 TS：`(a, b) => b.score - a.score || a.index - b.index`
		stableSortEntries(ranked)
	}

	// 取前 maxMatches。
	selected := []FocusedReadRange{}
	for i := 0; i < len(ranked) && i < maxMatches; i++ {
		e := ranked[i]
		sl := e.index - contextLines
		if sl < 0 {
			sl = 0
		}
		el := e.index + contextLines
		if el > len(lines)-1 {
			el = len(lines) - 1
		}
		selected = append(selected, FocusedReadRange{StartLine: sl, EndLine: el, Score: e.score})
	}
	ranges := mergeRanges(selected)

	// 预算裁剪：超出时丢弃**最低分**区间。
	for len(ranges) > 1 {
		budget := maxChars - 600
		if budget < 400 {
			budget = 400
		}
		if UTF16Len(renderRanges(lines, ranges)) <= budget {
			break
		}
		lowest := 0
		for i := range ranges {
			if ranges[i].Score < ranges[lowest].Score {
				lowest = i
			}
		}
		ranges = append(ranges[:lowest], ranges[lowest+1:]...)
	}

	content := renderFocusedContent(o.FilePath, focus, lines, summary, ranges, maxChars)
	matchedLines := 0
	for _, r := range ranges {
		matchedLines += r.EndLine - r.StartLine + 1
	}
	omitted := len(lines) - matchedLines
	if omitted < 0 {
		omitted = 0
	}
	return FocusedReadResult{
		Content:      content,
		Ranges:       ranges,
		MatchedLines: matchedLines,
		OmittedLines: omitted,
		Matched:      len(ranges) > 0,
	}
}

// focusEntry 是打分排序的条目。
type focusEntry struct {
	index int
	score int
}

// stableSortEntries 对账 TS 的 `(a,b) => b.score - a.score || a.index - b.index`。
func stableSortEntries(es []focusEntry) {
	// 插入排序（稳定）。
	for i := 1; i < len(es); i++ {
		cur := es[i]
		j := i - 1
		for j >= 0 && (es[j].score < cur.score ||
			(es[j].score == cur.score && es[j].index > cur.index)) {
			es[j+1] = es[j]
			j--
		}
		es[j+1] = cur
	}
}

func floorDiv(a, b int) int { return a / b }
