package tools

// modeloutput.go —— bash 结果的**模型可见形态**构造。
//
// 对账 TS `src/tools/output-store.ts` 的 `buildModelOutput` +
// `extractErrorAwareLines` 及六个常量。
//
// # 为什么需要它
//
// TS 的 bash 工具把原始输出交给 `buildModelOutput` 做**有损但信息密度高**的
// 整形：成功折叠只留尾部、失败走 error-aware 精选、超长走 head+tail。
// Go 侧此前直接返回原始拼接——**所有** bash 结果的形态都与 TS 不同。
//
// # 本刀修的一个真实偏差
//
// 第二十一刀我实现 `successFold` 时**折叠成单行提示**（`[cmd] exit=0 (N lines)
// — success output folded...`），而 TS 是 `SUCCESS_TAIL_LINES`=20 → **保留末尾
// 20 行** + 截断 footer。单行提示让模型失去"最后发生了什么"的观测——正是
// TS 注释强调要保留的信息。本刀修正。
//
// # scope（明示）
//
// **不含** `applyCommandFilter`（`command-filters.ts`，~300 行的独立子系统：
// tsc/test/git 五族过滤器）。它影响的是「失败输出经命令感知过滤」这一额外层，
// 与本文件的整形正交。`buildModelOutput` 里保留调用点（`commandFilter` 返回
// nil 即不过滤），接线时替换即可。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 对账 TS 的六个常量（逐字）。
const (
	modelMaxLines            = 200
	modelHeadLines           = 100
	modelTailLines           = 80
	successInlineLinesForOut = 20
	successTailLines         = 20
	modelErrorAwareThreshold = 40
	modelErrorAwareBudget    = 60
)

// ToolOutputMeta 对账 TS `ToolOutputMeta`（buildModelOutput 的入参）。
type ToolOutputMeta struct {
	Command    string
	ExitCode   int
	DurationMs int64
	// RawPath 是持久化的原始输出路径。
	//
	// 对账 TS 的 `rawPath`——**当前 Go 侧未接**（`persistRawOutput` 未移植），
	// 故调用方传空串，`recovery` 提示随之缺席。
	RawPath string
}

// countOutputLines 对账 TS `countLines`：**末尾空行不计**。
//
// 与 `len(strings.Split(s,"\n"))` 的差异：`"a\n"` 是 1 行（不是 2）。
func countOutputLines(raw string) int {
	if len(raw) == 0 {
		return 0
	}
	parts := strings.Split(raw, "\n")
	if parts[len(parts)-1] == "" {
		return len(parts) - 1
	}
	return len(parts)
}

// CommandFilterFunc 是命令感知过滤器的签名（对账 TS `applyCommandFilter`）。
//
// **本刀未实现**（见文件头 scope）。nil = 不过滤。
// 接线时传 `func(cmd, out string, exit int) (string, bool)`。
type CommandFilterFunc func(command, stdout string, exitCode int) (string, bool)

// BuildModelOutput 构造 bash 结果的模型可见内容。
//
// 对账 TS `buildModelOutput`。分支顺序**逐条对账**（顺序敏感）：
//  1. 命令过滤（**仅失败时**——成功输出不过滤，避免藏掉有用信息）
//  2. 空输出且 exit=0 → 显式确认 + 写文件提示
//  3. 成功且 >20 行 → 留末尾 20 行 + 截断 footer
//  4. 失败且 >40 行 → error-aware 精选
//  5. <=200 行 → 完整
//  6. >200 行 → head 100 + tail 80
func BuildModelOutput(raw string, meta ToolOutputMeta, filter CommandFilterFunc) string {
	// 1. 命令过滤。**仅失败时应用**（对账 TS：`exitCode !== 0 ? applyCommandFilter(...) : null`）
	effectiveRaw := raw
	if meta.ExitCode != 0 && filter != nil {
		if out, ok := filter(meta.Command, raw, meta.ExitCode); ok {
			effectiveRaw = out
		}
	}

	lines := strings.Split(effectiveRaw, "\n")
	lineCount := countOutputLines(effectiveRaw)
	header := fmt.Sprintf("[%s] exit=%d time=%.1fs lines=%d",
		meta.Command, meta.ExitCode, float64(meta.DurationMs)/1000, lineCount)

	// recovery 提示：无它则模型会用 sed/head/tee 变体重跑同一命令
	// （doom-loop 触发点）。对账 TS。
	recovery := ""
	if meta.RawPath != "" {
		recovery = " · full output: read_file " + meta.RawPath + " — 不要重跑命令"
	}

	// 2. 空输出：显式确认"确实空"，而非被吞掉。
	if lineCount == 0 && len(effectiveRaw) == 0 {
		emptyHint := ""
		if meta.ExitCode == 0 {
			emptyHint = "\n命令已执行成功，只是没有 stdout（写文件 / 重定向 `> file` / 静默成功都属正常）。" +
				"若你写了文件或重定向了输出，用 read_file 读它来确认——不要因为这里为空就判定命令没执行。"
		}
		return header + "\n[output complete: 0 lines — confirmed empty]" + emptyHint
	}

	// 3. 成功输出折叠：留**末尾 20 行**（不是单行提示——那是第二十一刀的偏差）。
	if meta.ExitCode == 0 && lineCount > successInlineLinesForOut {
		tail := lines[len(lines)-successTailLines:]
		omitted := lineCount - successTailLines
		return fmt.Sprintf("%s\n... %d lines omitted ...\n%s\n[output truncated: last %d of %d lines shown — %d lines omitted%s]",
			header, omitted, strings.Join(tail, "\n"),
			successTailLines, lineCount, omitted, recovery)
	}

	// 4. 失败且行数多：error-aware 精选（聚焦报错、丢噪声）。
	if meta.ExitCode != 0 && lineCount > modelErrorAwareThreshold {
		picked := ExtractErrorAwareLines(lines, modelErrorAwareBudget)
		omitted := lineCount - len(picked)
		if omitted < 0 {
			omitted = 0
		}
		return fmt.Sprintf("%s\n%s\n[error-aware: %d of %d lines shown, %d lines omitted%s]",
			header, strings.Join(picked, "\n"), len(picked), lineCount, omitted, recovery)
	}

	// 5. 在 200 行内 → 完整。
	if len(lines) <= modelMaxLines {
		return header + " — output complete\n" + effectiveRaw
	}

	// 6. 超 200 行 → head + tail。
	head := lines[:modelHeadLines]
	tail := lines[len(lines)-modelTailLines:]
	omitted := len(lines) - modelHeadLines - modelTailLines
	return fmt.Sprintf("%s\n%s\n... (%d lines omitted) ...\n%s\n[output truncated: head %d + tail %d of %d lines shown — %d lines omitted%s]",
		header, strings.Join(head, "\n"), omitted, strings.Join(tail, "\n"),
		modelHeadLines, modelTailLines, len(lines), omitted, recovery)
}

// errorMarkerRe 对账 TS `extractErrorAwareLines` 的 markerRegex。
//
// **大小写不敏感**（TS 用 `/i`）；`^`/`$` 作用于**整串**（Go 的 `^`/`$` 默认
// 匹配文本首尾，与 JS **非多行**正则一致——TS 未加 `m` 标志，逐行调用 `test`）。
var errorMarkerRe = regexp.MustCompile(`(?i)error\b|Error:|FAIL\b|AssertionError|assert|✗|✘|×|at\s+\S+\.(ts|tsx|js|jsx):\d+|^\s+\d+\s+\|\s|^\s+>`)

// ExtractErrorAwareLines 扫描错误标记行并保留其上下文。
//
// 对账 TS `extractErrorAwareLines`。无标记时回退 head+tail。
//
// **返回行的构成**：前 3 行（命令头等）+ 末 2 行（汇总）+ 各错误点 ±2 行，
// 间隔处插 `...`。若结果超 `maxLines+5` 则回退 head+tail（并保证含最后一个错误）。
func ExtractErrorAwareLines(lines []string, maxLines int) []string {
	// 找错误行索引。
	var errorIdxs []int
	for i, l := range lines {
		if errorMarkerRe.MatchString(l) {
			errorIdxs = append(errorIdxs, i)
		}
	}

	// 无标记 → head + tail 回退。
	if len(errorIdxs) == 0 {
		head := (maxLines + 2) / 3 // ceil(maxLines/3)
		tail := maxLines - head
		out := append([]string{}, lines[:min(head, len(lines))]...)
		skipped := len(lines) - maxLines
		if skipped < 0 {
			skipped = 0
		}
		out = append(out, fmt.Sprintf("... (%d lines skipped, no error markers detected) ...", skipped))
		if len(lines) > tail {
			out = append(out, lines[len(lines)-tail:]...)
		}
		return out
	}

	// 收集错误点 ±2 行，去重重叠区间。
	const contextRadius = 2
	included := map[int]bool{}
	for i := 0; i < min(3, len(lines)); i++ {
		included[i] = true
	}
	for i := max(0, len(lines)-2); i < len(lines); i++ {
		included[i] = true
	}
	// **从后往前**（紧密场景优先靠后的错误）——对账 TS 的排序意图。
	sorted := append([]int{}, errorIdxs...)
	for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}
	for _, idx := range sorted {
		start := max(0, idx-contextRadius)
		end := min(len(lines), idx+contextRadius+1)
		for i := start; i < end; i++ {
			included[i] = true
		}
	}

	// 按序输出，间隔插 `...`。
	idxList := make([]int, 0, len(included))
	for k := range included {
		idxList = append(idxList, k)
	}
	sortInts(idxList)
	var result []string
	prev := -2
	for _, idx := range idxList {
		if idx > prev+1 {
			result = append(result, "...")
		}
		result = append(result, lines[idx])
		prev = idx
	}

	// 装得下就返回；否则回退 head+tail（保证含最后一个错误）。
	if len(result) <= maxLines+5 {
		return result
	}
	lastErrorIdx := errorIdxs[len(errorIdxs)-1]
	headSize := min((maxLines+1)/2, lastErrorIdx-3)
	if headSize < 0 {
		headSize = 0
	}
	tailStart := max(headSize, lastErrorIdx-maxLines/2)
	tailEnd := min(tailStart+maxLines-headSize, len(lines))
	out := append([]string{}, lines[:min(headSize, len(lines))]...)
	out = append(out, fmt.Sprintf("... (%d lines skipped) ...", tailStart-headSize))
	if tailStart < len(lines) {
		out = append(out, lines[tailStart:tailEnd]...)
	}
	return out
}

// sortInts 用标准库排序（Go 1.21+ 内置 min/max，勿自定义遮蔽）。
func sortInts(a []int) {
	sort.Ints(a)
}
