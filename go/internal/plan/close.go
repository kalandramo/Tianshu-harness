package plan

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PlanCloseOptions 是闭环选项。
//
// 对账 TS `plan-close.ts:1-7`。
type PlanCloseOptions struct {
	// Tasks 是任务选择表达式（"1" / "1-3" / "1,3-4" / "all"）。
	Tasks string
	// VerifiedCommands 是闭环摘要中记录的验证命令。
	VerifiedCommands []string
	// DeliveryState 是交付门状态（"" 表示不写）。
	DeliveryState string
	// Note 是可选备注。
	Note string
	// UpdateClosure 控制是否 upsert 闭环段（nil = true）。
	//
	// **用指针区分「未传」与「显式 false」**——对账 TS 的
	// `options.updateClosure !== false`（undefined 与 false 语义不同）。
	UpdateClosure *bool
}

// PlanCloseChange 是单个任务块的勾选变更统计。
//
// 对账 TS `plan-close.ts:9-13`。
type PlanCloseChange struct {
	TaskNumber           int
	CheckboxCount        int
	ChangedCheckboxCount int
}

// PlanCloseResult 是闭环结果。
//
// 对账 TS `plan-close.ts:15-22`。
type PlanCloseResult struct {
	Content                string
	Changes                []PlanCloseChange
	TotalChangedCheckboxes int
	AlreadyClosed          bool
	ClosureInserted        bool
	ClosureUpdated         bool
}

// taskBlock 是任务块的行区间（endLineExclusive 不含）。
//
// 对账 TS `plan-close.ts:24-28`。
type taskBlock struct {
	taskNumber       int
	startLine        int
	endLineExclusive int
}

// fenceRe 匹配围栏行（``` 或 ~~~，允许前导空白）。
var fenceRe = regexp.MustCompile("^\\s*(```|~~~)")

// taskBlockRe 匹配任务块标题。
//
// 对账 TS `plan-close.ts:64`：
//
//	/^###\s+(?:Task|Wave|任务)\s+(\d+)\b/
//
// **三种合法形态**：### Task N（基础模板）、### Wave N（>8 任务大计划的 submit
// 门禁强制分波格式）、### 任务 N（中文习惯写法）。只认 Task 时，按门禁要求
// 分波的计划 close 永远 0 匹配——「No matching task blocks found for
// selection: all」（2026-08-09 会话实录，模型被迫手工编辑计划文件闭环）。
var taskBlockRe = regexp.MustCompile(`^###\s+(?:Task|Wave|任务)\s+(\d+)\b`)

// checkboxAnyRe 匹配任意状态的 checkbox（用于计数）。
var checkboxAnyRe = regexp.MustCompile(`^\s*- \[[ xX]\]`)

// checkboxUncheckedRe 匹配**未勾选**的 checkbox（用于改写）。
//
// 捕获组 1 = 前缀（含缩进与 `- [`），组 2 = `]` 之后的全部内容。
var checkboxUncheckedRe = regexp.MustCompile(`^(\s*- \[) \](.*)$`)

// closureHeadingRe 匹配闭环段标题。
//
// 对账 TS `plan-close.ts:112`：`/^##\s+7\.\s+Execution\s+(handoff|closure)\b/`
var closureHeadingRe = regexp.MustCompile(`^##\s+7\.\s+Execution\s+(handoff|closure)\b`)

// execStatusPrefix / techStackPrefix 是 upsert 的定位前缀。
const (
	execStatusPrefix = "**执行状态：**"
	techStackPrefix  = "**技术栈：**"
)

// ParseTaskSelection 解析任务选择表达式。
//
// 对账 TS `plan-close.ts:30-57`。
//
//	"all"  → 空切片（**全选哨兵**，不是「未选」）
//	"1"    → [1]
//	"1-3"  → [1,2,3]
//	"1,3-4"→ [1,3,4]
//
// 非法输入返回错误（TS 抛 Error，消息含**原始未 trim 的** selection）。
// 最终去重并升序返回。
func ParseTaskSelection(selection string) ([]int, error) {
	trimmed := strings.ToLower(strings.TrimSpace(selection))
	if trimmed == "all" {
		return []int{}, nil
	}
	if trimmed == "" {
		return nil, fmt.Errorf("Invalid task selection: %s", selection)
	}

	selected := map[int]bool{}
	for _, token := range strings.Split(trimmed, ",") {
		part := strings.TrimSpace(token)
		if part == "" {
			return nil, fmt.Errorf("Invalid task selection: %s", selection)
		}

		if m := rangeRe.FindStringSubmatch(part); m != nil {
			start, err1 := strconv.Atoi(m[1])
			end, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil || start <= 0 || end <= 0 || start > end {
				return nil, fmt.Errorf("Invalid task selection: %s", selection)
			}
			for n := start; n <= end; n++ {
				selected[n] = true
			}
			continue
		}

		if !singleRe.MatchString(part) {
			return nil, fmt.Errorf("Invalid task selection: %s", selection)
		}
		value, err := strconv.Atoi(part)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("Invalid task selection: %s", selection)
		}
		selected[value] = true
	}

	out := make([]int, 0, len(selected))
	for n := range selected {
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

var (
	rangeRe  = regexp.MustCompile(`^(\d+)-(\d+)$`)
	singleRe = regexp.MustCompile(`^\d+$`)
)

// findTaskBlocks 扫描任务块（跳过围栏内的行）。
//
// 对账 TS `plan-close.ts:59-77`。每遇到新块标题，把上一块的 endLineExclusive
// 设为当前行；最后一块延伸到文件末。
func findTaskBlocks(lines []string) []taskBlock {
	var blocks []taskBlock
	inFence := false

	for i, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		m := taskBlockRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1].endLineExclusive = i
		}
		n, _ := strconv.Atoi(m[1])
		blocks = append(blocks, taskBlock{
			taskNumber:       n,
			startLine:        i,
			endLineExclusive: len(lines),
		})
	}
	return blocks
}

// computeFenceMask 标记每行是否位于围栏内（围栏行本身也为 true）。
//
// 对账 TS `plan-close.ts:79-93`。
func computeFenceMask(lines []string) []bool {
	mask := make([]bool, len(lines))
	inFence := false

	for i, line := range lines {
		if fenceRe.MatchString(line) {
			mask[i] = true
			inFence = !inFence
			continue
		}
		mask[i] = inFence
	}
	return mask
}

// restoreTrailingNewline 按原样恢复尾换行。
//
// 对账 TS `plan-close.ts:95-98`。
func restoreTrailingNewline(lines []string, hasTrailingNewline bool) string {
	content := strings.Join(lines, "\n")
	if hasTrailingNewline {
		return content + "\n"
	}
	return content
}

// formatTaskLabel 生成任务标签（"Task 1-3" / "Task 1,3" / "Task 0"）。
//
// 对账 TS `plan-close.ts:100-107`。
func formatTaskLabel(tasks string, selected, allTaskNumbers []int) string {
	numbers := selected
	if strings.ToLower(strings.TrimSpace(tasks)) == "all" {
		numbers = allTaskNumbers
	}
	if len(numbers) == 0 {
		return "Task 0"
	}
	contiguous := true
	for i, n := range numbers {
		if i > 0 && n != numbers[i-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous && len(numbers) > 1 {
		return fmt.Sprintf("Task %d-%d", numbers[0], numbers[len(numbers)-1])
	}
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = strconv.Itoa(n)
	}
	return "Task " + strings.Join(parts, ",")
}

// upsertExecutionStatus 更新或插入执行状态行。
//
// 对账 TS `plan-close.ts:109-131`。三级定位：
//  1. 已有 `**执行状态：**` 行（围栏外）→ 替换
//  2. 有 `**技术栈：**` 行 → 插到其后的第一个空行处（无空行则紧随其后）
//  3. 都没有 → 插到第 2 行（索引 1）后
func upsertExecutionStatus(lines []string, statusLine string) (out []string, updated, inserted bool) {
	fenceMask := computeFenceMask(lines)
	existingIndex := -1
	for i, line := range lines {
		if !fenceMask[i] && strings.HasPrefix(line, execStatusPrefix) {
			existingIndex = i
			break
		}
	}
	if existingIndex >= 0 {
		next := append([]string(nil), lines...)
		next[existingIndex] = statusLine
		return next, true, false
	}

	techIndex := -1
	for i, line := range lines {
		if !fenceMask[i] && strings.HasPrefix(line, techStackPrefix) {
			techIndex = i
			break
		}
	}
	if techIndex >= 0 {
		insertAt := -1
		for i := techIndex + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				insertAt = i
				break
			}
		}
		target := insertAt
		if target < 0 {
			target = techIndex + 1
		}
		return splice(lines, target, "", statusLine), false, true
	}

	return splice(lines, 1, "", statusLine), false, true
}

// upsertExecutionClosure 更新或插入闭环段。
//
// 对账 TS `plan-close.ts:133-143`。有 `## 7. Execution (handoff|closure)`
// 标题则**截断到该处**再追加（丢弃旧闭环段及其后内容）；否则去掉尾部空行后追加。
func upsertExecutionClosure(lines []string, closure []string) (out []string, updated, inserted bool) {
	fenceMask := computeFenceMask(lines)
	headingIndex := -1
	for i, line := range lines {
		if !fenceMask[i] && closureHeadingRe.MatchString(line) {
			headingIndex = i
			break
		}
	}
	if headingIndex >= 0 {
		next := append([]string(nil), lines[:headingIndex]...)
		next = append(next, closure...)
		return next, true, false
	}

	next := append([]string(nil), lines...)
	for len(next) > 0 && strings.TrimSpace(next[len(next)-1]) == "" {
		next = next[:len(next)-1]
	}
	next = append(next, "")
	next = append(next, closure...)
	return next, false, true
}

// splice 在 index 处插入 items（返回新切片，不改原切片）。
func splice(lines []string, index int, items ...string) []string {
	if index < 0 {
		index = 0
	}
	if index > len(lines) {
		index = len(lines)
	}
	out := make([]string, 0, len(lines)+len(items))
	out = append(out, lines[:index]...)
	out = append(out, items...)
	out = append(out, lines[index:]...)
	return out
}

// buildClosure 构造闭环段行。
//
// 对账 TS `plan-close.ts:145-169`。
func buildClosure(taskLabel string, options PlanCloseOptions) []string {
	lines := []string{
		"## 7. Execution closure",
		"",
		"已闭环：" + taskLabel + " 均已完成并通过验证。",
		"",
	}

	if len(options.VerifiedCommands) > 0 {
		lines = append(lines, "最终验证记录：", "", "```bash")
		lines = append(lines, options.VerifiedCommands...)
		lines = append(lines, "```")
	} else {
		lines = append(lines, "最终验证记录：本次未传入显式验证命令。")
	}

	if options.DeliveryState != "" {
		lines = append(lines, "", "交付门检查："+options.DeliveryState+"。")
	}

	if strings.TrimSpace(options.Note) != "" {
		lines = append(lines, "", "备注："+strings.TrimSpace(options.Note))
	}

	return lines
}

// ClosePlanMarkdown 闭环计划：勾选 checkbox + upsert 状态/闭环段。
//
// 对账 TS `plan-close.ts:178-233`。
//
// **两个必须复刻的边界**：
//   - 尾换行：`markdown.endsWith('\n')` 决定是否在末尾补 `\n`；计算时先剥掉
//     尾换行再 split（否则会多一个空行）。
//   - `selected` 为空（即 "all"）时用**全部块的任务号**；否则用 selected。
//
// 无匹配任务块返回错误（TS 抛 Error，消息含原始 tasks）。
func ClosePlanMarkdown(markdown string, options PlanCloseOptions) (*PlanCloseResult, error) {
	hasTrailingNewline := strings.HasSuffix(markdown, "\n")
	body := markdown
	if hasTrailingNewline {
		body = markdown[:len(markdown)-1]
	}
	lines := strings.Split(body, "\n")

	fenceMask := computeFenceMask(lines)
	blocks := findTaskBlocks(lines)
	selected, err := ParseTaskSelection(options.Tasks)
	if err != nil {
		return nil, err
	}

	var selectedSet map[int]bool
	if len(selected) > 0 {
		selectedSet = map[int]bool{}
		for _, n := range selected {
			selectedSet[n] = true
		}
	} else {
		selectedSet = map[int]bool{}
		for _, b := range blocks {
			selectedSet[b.taskNumber] = true
		}
	}

	var targetBlocks []taskBlock
	for _, b := range blocks {
		if selectedSet[b.taskNumber] {
			targetBlocks = append(targetBlocks, b)
		}
	}

	if len(targetBlocks) == 0 {
		return nil, errors.New("No matching task blocks found for selection: " + options.Tasks)
	}

	nextLines := append([]string(nil), lines...)
	changes := make([]PlanCloseChange, 0, len(targetBlocks))

	for _, block := range targetBlocks {
		checkboxCount := 0
		changedCheckboxCount := 0
		for i := block.startLine; i < block.endLineExclusive; i++ {
			if fenceMask[i] {
				continue
			}
			line := nextLines[i]
			if checkboxAnyRe.MatchString(line) {
				checkboxCount++
			}
			if m := checkboxUncheckedRe.FindStringSubmatch(line); m != nil {
				nextLines[i] = m[1] + "x]" + m[2]
				changedCheckboxCount++
			}
		}
		changes = append(changes, PlanCloseChange{
			TaskNumber:           block.taskNumber,
			CheckboxCount:        checkboxCount,
			ChangedCheckboxCount: changedCheckboxCount,
		})
	}

	finalLines := nextLines
	closureInserted := false
	closureUpdated := false

	updateClosure := options.UpdateClosure == nil || *options.UpdateClosure
	if updateClosure {
		allTaskNumbers := make([]int, 0, len(blocks))
		for _, b := range blocks {
			allTaskNumbers = append(allTaskNumbers, b.taskNumber)
		}
		sort.Ints(allTaskNumbers)

		taskLabel := formatTaskLabel(options.Tasks, selected, allTaskNumbers)
		statusLine := "**执行状态：** 已闭环。" + taskLabel + " 均已完成；验证通过"
		if options.DeliveryState != "" {
			statusLine += "；交付门检查：" + options.DeliveryState
		}
		statusLine += "。"

		finalLines, _, _ = upsertExecutionStatus(finalLines, statusLine)
		var inserted, updated bool
		finalLines, updated, inserted = upsertExecutionClosure(finalLines, buildClosure(taskLabel, options))
		closureInserted = inserted
		closureUpdated = updated
	}

	totalChanged := 0
	allZero := true
	for _, c := range changes {
		totalChanged += c.ChangedCheckboxCount
		if c.ChangedCheckboxCount != 0 {
			allZero = false
		}
	}

	return &PlanCloseResult{
		Content:                restoreTrailingNewline(finalLines, hasTrailingNewline),
		Changes:                changes,
		TotalChangedCheckboxes: totalChanged,
		AlreadyClosed:          allZero,
		ClosureInserted:        closureInserted,
		ClosureUpdated:         closureUpdated,
	}, nil
}
