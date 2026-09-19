// Package prompt —— project-instructions 按节选取。
//
// 复刻 src/prompt/project-instructions.ts。算法本身是纯函数，但对账要求
// 逐字节精确，故几处 JS 语义必须显式还原：
//
//   - `trimEnd()` 是 **Unicode 感知**的空白移除（非仅 ASCII）→ 用 TrimRightFunc
//   - 正则 `\s` 在 JS 里覆盖到 U+3000/U+FEFF 等 → 用 jsSpaceClass 显式列举
//   - `selectSections` 里首个选中项**不计**分隔符（`chosen.size > 0` 才加）
//
// 为什么这个算法值得逐字节对账：它的两轮预算预留、贪心非单调性、tier 分类
// 优先级都是易错点，且错误后果是静默的（少保一节纪律 vs 多丢一节参考，
// 字节上只差几十字符，肉眼看不出）。
package prompt

import (
	"regexp"
	"strings"
	"unicode"
)

// Tier 章节优先级。数字小的先保。
type Tier int

const (
	// TierGate 硬门禁与纪律——主控事后补救不了，必须常驻。
	TierGate Tier = 0
	// TierProse 散文说明。
	TierProse Tier = 1
	// TierReference 表格为主的参考资料——agent 需要时能自己查。
	TierReference Tier = 2
)

// hardGate 混在正文里的硬门禁措辞（与 tools/description-compact.ts 同一张表）。
var hardGate = regexp.MustCompile(`禁止|必须|不得|绝不|不能|NEVER|MUST|Do NOT|DO NOT`)

// gateHeading 标题本身就宣告这是纪律/约定类章节。命中即免于被丢。
var gateHeading = regexp.MustCompile(`(?i)纪律|闸门|禁令|安全|规范|约定|守则|rule|convention|discipline|safety|security|polic`)

// tableHeavyRatio 表格行占比高于此值 → 判为参考类。
const tableHeavyRatio = 0.4

// jsSpaceClass 是 JS 正则 `\s` 的等价字符类。
// Go 的 `\s` 默认只含 [\t\n\f\r ]，比 JS 窄——直接用会漏掉全角空格等，
// 导致标题判定分叉。这里显式列举 JS 的全部空白码点。
const jsSpaceClass = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	fenceLineRe   = regexp.MustCompile(`^` + jsSpaceClass + `*` + "```")
	h1h2LineRe    = regexp.MustCompile(`^#{1,2}` + jsSpaceClass + `+`)
	docTitleRe    = regexp.MustCompile(`^#` + jsSpaceClass + `+`)
	headingTrimRe = regexp.MustCompile(`^#+` + jsSpaceClass + `*`)
)

// DocSection 是切分出的一个章节。
type DocSection struct {
	// Heading 标题行原文；第一个标题之前的散文为 ""。
	Heading string
	// Title 标题去掉 `#` 与空白后的纯文本，用于略去标记。
	Title string
	// Text 含标题行的整段原文（不含结尾换行）。
	Text string
	// Tier 优先级。
	Tier Tier
}

// SelectionResult 是选取结果。
type SelectionResult struct {
	// Text 选中章节按原文顺序拼接的结果。
	Text string
	// Omitted 被略去的章节标题，按原文顺序。
	Omitted []string
}

// tableRatio 计算表格行（以 | 开头）在非空行中的占比。
func tableRatio(body string) float64 {
	lines := strings.Split(body, "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return 0
	}
	table := 0
	for _, l := range nonEmpty {
		if strings.HasPrefix(strings.TrimLeftFunc(l, unicode.IsSpace), "|") {
			table++
		}
	}
	return float64(table) / float64(len(nonEmpty))
}

// classify 判定章节优先级。表格判定**先于**门禁词——索引表里偶然出现一个
// 「必须」不该把整张表提到最高优先级。
func classify(heading, body string) Tier {
	if tableRatio(body) >= tableHeavyRatio {
		return TierReference
	}
	if heading != "" && gateHeading.MatchString(heading) {
		return TierGate
	}
	if hardGate.MatchString(body) {
		return TierGate
	}
	return TierProse
}

// SplitSections 按 `#` / `##` 切分文档。
//
// `#` 也算边界，因为 project-instructions 是 AGENTS.md + .rivet.md 拼接的结果——
// 只认 `##` 会让第二份文档的标题被吞进第一份的末节。更深的层级不拆。
//
// 第一个标题之前的散文与文档首节同样按 Gate 处理——"这是什么项目"是后续
// 判断的锚。围栏（```）内的标题行不算标题。
func SplitSections(md string) []DocSection {
	lines := strings.Split(md, "\n")
	headingAt := []int{}
	inFence := false
	for i, line := range lines {
		if fenceLineRe.MatchString(line) {
			inFence = !inFence
		} else if !inFence && h1h2LineRe.MatchString(line) {
			headingAt = append(headingAt, i)
		}
	}

	out := []DocSection{}
	leadEnd := len(lines)
	if len(headingAt) > 0 {
		leadEnd = headingAt[0]
	}
	lead := trimEndUnicode(strings.Join(lines[:leadEnd], "\n"))
	if strings.TrimSpace(lead) != "" {
		out = append(out, DocSection{Heading: "", Title: "(前言)", Text: lead, Tier: TierGate})
	}

	for i, start := range headingAt {
		end := len(lines)
		if i+1 < len(headingAt) {
			end = headingAt[i+1]
		}
		heading := lines[start]
		body := strings.Join(lines[start+1:end], "\n")
		isDocTitle := docTitleRe.MatchString(heading)

		tier := TierProse
		if isDocTitle {
			tier = TierGate
		} else {
			tier = classify(heading, body)
		}

		out = append(out, DocSection{
			Heading: heading,
			Title:   trimSpaceUnicode(headingTrimRe.ReplaceAllString(heading, "")),
			Text:    trimEndUnicode(strings.Join(lines[start:end], "\n")),
			Tier:    tier,
		})
	}
	return out
}

// SelectSections 在 budget 字符内选出尽可能多的章节，优先级高的先占位，
// 输出保持原文顺序。
//
// measure 让调用方按**渲染后**的长度计费（project-instructions 要经 escapeXml，
// 转义膨胀在本仓库是 31%）。传 nil 按原文长度。
func SelectSections(sections []DocSection, budget int, measure func(string) int) SelectionResult {
	if measure == nil {
		measure = utf16Len
	}
	const sep = "\n\n"
	sepCost := measure(sep)
	chosen := make([]bool, len(sections))
	chosenCount := 0
	used := 0

	for _, tier := range []Tier{TierGate, TierProse, TierReference} {
		for i, section := range sections {
			if section.Tier != tier {
				continue
			}
			cost := measure(section.Text)
			if chosenCount > 0 {
				cost += sepCost
			}
			if used+cost > budget {
				continue
			}
			chosen[i] = true
			chosenCount++
			used += cost
		}
	}

	texts := []string{}
	omitted := []string{}
	for i, s := range sections {
		if chosen[i] {
			texts = append(texts, s.Text)
		} else {
			omitted = append(omitted, s.Title)
		}
	}
	return SelectionResult{Text: strings.Join(texts, sep), Omitted: omitted}
}

// SelectProjectInstructions 选取 + 渲染略去标记。返回的文本尚未转义。
//
// 略去标记本身要占预算，否则加完就超。首轮按最坏情况（全部章节被略）预留，
// 再用实际略去集重算一次把余量还回去。
//
// 二轮不保证更好：按序贪心对预算**不单调**（预算变大可能换进一个大章节、
// 挤掉两个小的），略去集因此可能换成标题更长的一组，总长反而溢出。所以二轮
// 只作为候选，装不下就退回首轮——首轮按最坏情况预留，恒定装得下。
func SelectProjectInstructions(md string, budget int, measure func(string) int) SelectionResult {
	if measure == nil {
		measure = utf16Len
	}
	sections := SplitSections(md)
	if measure(md) <= budget {
		return SelectionResult{Text: md, Omitted: []string{}}
	}

	sepCost := measure("\n\n")
	reserve := func(note string) int {
		if note == "" {
			return budget
		}
		return budget - (measure(note) + sepCost)
	}

	allTitles := make([]string, len(sections))
	for i, s := range sections {
		allTitles[i] = s.Title
	}
	first := SelectSections(sections, reserve(renderNote(allTitles)), measure)
	second := SelectSections(sections, reserve(renderNote(first.Omitted)), measure)

	best := first
	if measure(render(second)) <= budget {
		best = second
	}

	// 预算连一节都装不下——退回调用方的整块截断，不在这里制造只剩标记的空块。
	if best.Text == "" {
		return SelectionResult{Text: md, Omitted: []string{}}
	}
	return SelectionResult{Text: render(best), Omitted: best.Omitted}
}

func renderNote(omitted []string) string {
	if len(omitted) == 0 {
		return ""
	}
	return "[本块超出前缀预算，已略去 " + itoa(len(omitted)) + " 节：" +
		strings.Join(omitted, "、") + "。需要时直接读 AGENTS.md / .rivet.md 原文。]"
}

func render(r SelectionResult) string {
	if len(r.Omitted) == 0 {
		return r.Text
	}
	return r.Text + "\n\n" + renderNote(r.Omitted)
}

// UTF16Len 复刻 JS 的 String.prototype.length —— UTF-16 code unit 数。
//
// **这是移植中最易漏的一处分叉**：JS 的 .length 是 code unit 数（BMP 外字符
// 计 2，如 emoji），不是 Unicode 码点数（Go 的 len([]rune(s))）。
// TS 侧 selectSections 的默认 measure 是 `t => t.length`，生产调用方
// （volatile.ts:1122）是 `t => escapeXml(t).length` —— 两者都是 code unit。
//
// 后果：预算临界点上，按码点计费会多丢/少丢章节。含 emoji 的 AGENTS.md
// 就会触发（实测文档码点 98 / UTF16 122，budget=79 时结果截然不同）。
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Len 是包内短名（对外导出 UTF16Len 供调用方构造 measure）。
func utf16Len(s string) int { return UTF16Len(s) }

// EscapeXML 复刻 volatile.ts:493 的 escapeXml。
//
// 关键：只转 `& < > "` 四个，**不含单引号**；且 `&` 必须最先替换，
// 否则 `<` 转成 `&lt;` 后其中的 `&` 会被二次转义成 `&amp;lt;`。
func EscapeXML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	text = strings.ReplaceAll(text, `"`, "&quot;")
	return text
}

// trimEndUnicode 复刻 JS 的 String.prototype.trimEnd（Unicode 感知）。
func trimEndUnicode(s string) string {
	return strings.TrimRightFunc(s, unicode.IsSpace)
}

// trimSpaceUnicode 复刻 JS 的 String.prototype.trim。
func trimSpaceUnicode(s string) string {
	return strings.TrimFunc(s, unicode.IsSpace)
}

// itoa 避免为单个数字引入 strconv 的语义歧义（这里只要十进制）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := []byte{}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}
