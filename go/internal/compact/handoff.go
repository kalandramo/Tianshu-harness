package compact

// handoff.go —— 结构化 handoff 文本构造（完整版）。
//
// 对账 TS `buildStructuredHandoff`（`compaction-controller.ts:199`）——9 个章节。
//
// # 与 BuildSessionHandoff 的关系
//
// `sessionsplit.go` 的 `BuildSessionHandoff` 是**降级入口**（无轨迹/todo 输入）
// ——task-state / trajectory 未移植时的占位。本文件是**完整版**，补齐 TS 的
// 9 章。旧函数保留（向后兼容），内部委托到本实现。

import (
	"strconv"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// 9 个章节标题，对账 TS 的 `STRUCTURED_HANDOFF_SECTIONS`。
//
// **必须逐字相同**——这些标题进 handoff 文本，进而进后续请求的前缀。
var handoffSections = [9]string{
	"1. 用户核心需求",
	"2. 关键技术决策",
	"3. 文件与代码",
	"4. 错误与修复",
	"5. 当前工作",
	"6. 已完成工作",
	"7. 待办事项",
	"8. 最近工具轨迹",
	"9. 下一步",
}

// handoff 各章节的条数上限，对账 TS 的 slice 参数。
const (
	handoffMaxDecisions = 8  // decisions.slice(-8)
	handoffMaxFiles     = 15 // filesSeen.slice(0, 15)
	handoffMaxErrors    = 8  // errors.slice(0, 8)
	handoffMaxCompleted = 8  // completed.slice(-8)
	handoffMaxRemaining = 8  // remaining.slice(0, 8)
	handoffMaxToolHist  = 12 // toolHistory.slice(-12)
)

// handoffStatusLabel 对账 TS 的 `statusLabel`。
func handoffStatusLabel(s TrajectoryStatus) string {
	switch s {
	case TrajectoryFailed, TrajectoryRetriedFailed:
		return "FAIL"
	case TrajectoryRetriedSuccess:
		return "ok*"
	case "running":
		return "running"
	default:
		return "ok"
	}
}

// BuildSessionHandoffWithState 构造完整结构化 handoff。
//
// 对账 TS `buildStructuredHandoff`。参数：
//   - messages：会话历史（供文件清单与近期推理提取）
//   - ratio：触发 split 的上下文占用比例（仅用于标题行）
//   - rec：轨迹记录器（**可为 nil**——nil 时轨迹章节为空）
//   - todos：权威 todo 清单（**可为 nil**——nil 时 current 回退启发式）
//   - streamedText：本回合模型流式文本（供 `ExtractTaskState` 的决策提取）
//
// **task-state 的来源优先级**（对账 TS 的 `processTurnEnd`）：
// todos 非空 → `TaskStateFromTodos`（权威，带依赖排序）；
// 否则 → `ExtractTaskState`（启发式回退）。决策**始终**来自启发式
// （todos 不携带决策）。
func BuildSessionHandoffWithState(
	messages []session.OaiMessage,
	ratio float64,
	rec *TrajectoryRecorder,
	todos []prompt.TodoItem,
	streamedText string,
) string {
	var entries []TrajectoryEntry
	if rec != nil {
		entries = rec.Entries()
	}

	// task-state：todos 优先（权威），否则启发式。
	heuristic := ExtractTaskState(entries, streamedText)
	var ts TaskState
	if len(todos) > 0 {
		ts = TaskStateFromTodos(todos, heuristic.Decisions)
	} else {
		ts = heuristic
	}

	var b strings.Builder
	b.WriteString("<session-handoff>\n")
	b.WriteString("Session split at ")
	b.WriteString(formatPercent(ratio))
	b.WriteString(" context.\n\n")

	// ## 1. 用户核心需求 —— current
	b.WriteString("## " + handoffSections[0] + "\n")
	if ts.Current != "" {
		b.WriteString(ts.Current + "\n")
	} else {
		b.WriteString("（无明确记录）\n")
	}

	// ## 2. 关键技术决策
	b.WriteString("\n## " + handoffSections[1] + "\n")
	if n := len(ts.Decisions); n > 0 {
		start := n - handoffMaxDecisions
		if start < 0 {
			start = 0
		}
		for _, d := range ts.Decisions[start:] {
			b.WriteString("- " + d + "\n")
		}
	} else {
		b.WriteString("（无记录）\n")
	}

	// ## 3. 文件与代码
	b.WriteString("\n## " + handoffSections[2] + "\n")
	files := extractFilePaths(messages)
	if len(files) > 0 {
		if len(files) > handoffMaxFiles {
			files = files[:handoffMaxFiles]
		}
		for _, f := range files {
			b.WriteString("- " + f)
			// 工具标注：该文件被哪些工具碰过（去重，保序）。
			if tools := toolsForTarget(entries, f); len(tools) > 0 {
				b.WriteString(" [" + strings.Join(tools, ", ") + "]")
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("（无文件记录）\n")
	}

	// ## 4. 错误与修复
	b.WriteString("\n## " + handoffSections[3] + "\n")
	failures := []TrajectoryEntry{}
	for _, e := range entries {
		if e.IsFailure() {
			failures = append(failures, e)
		}
	}
	if len(failures) > 0 {
		b.WriteString("- Error count: " + strconv.Itoa(len(failures)) + "\n")
		if len(failures) > handoffMaxErrors {
			failures = failures[:handoffMaxErrors]
		}
		for _, f := range failures {
			ec := f.ErrorClass
			if ec == "" {
				ec = "unknown"
			}
			b.WriteString("- [Turn " + strconv.Itoa(f.Turn) + "] failed: " + f.Tool + " " + f.Target + " (" + ec + ")\n")
		}
	} else {
		b.WriteString("（无错误）\n")
	}

	// ## 5. 当前工作
	b.WriteString("\n## " + handoffSections[4] + "\n")
	if ts.Current != "" {
		b.WriteString(ts.Current + "\n")
	} else {
		b.WriteString("（无记录）\n")
	}

	// ## 6. 已完成工作
	b.WriteString("\n## " + handoffSections[5] + "\n")
	if n := len(ts.Completed); n > 0 {
		start := n - handoffMaxCompleted
		if start < 0 {
			start = 0
		}
		for _, c := range ts.Completed[start:] {
			b.WriteString("- [x] " + c + "\n")
		}
	} else {
		b.WriteString("（无记录）\n")
	}

	// ## 7. 待办事项
	b.WriteString("\n## " + handoffSections[6] + "\n")
	if len(ts.Remaining) > 0 {
		items := ts.Remaining
		if len(items) > handoffMaxRemaining {
			items = items[:handoffMaxRemaining]
		}
		for _, r := range items {
			b.WriteString("- [ ] " + r + "\n")
		}
	} else {
		b.WriteString("（无明确待办）\n")
	}

	// ## 8. 最近工具轨迹
	b.WriteString("\n## " + handoffSections[7] + "\n")
	if len(entries) > 0 {
		hist := entries
		if len(hist) > handoffMaxToolHist {
			hist = hist[len(hist)-handoffMaxToolHist:]
		}
		for _, e := range hist {
			b.WriteString("- " + e.Tool + " " + e.Target + " [" + handoffStatusLabel(e.Status) + "]\n")
		}
	} else {
		b.WriteString("（无工具记录）\n")
	}

	// ## 9. 下一步
	b.WriteString("\n## " + handoffSections[8] + "\n")
	next := ts.Current
	if len(ts.Remaining) > 0 {
		next = ts.Remaining[0]
	}
	if next == "" {
		next = "继续当前任务"
	}
	b.WriteString(next + "\n")

	// 附录：最近推理摘要（对账 TS 的 reasoningSnippet 章节）。
	const maxReasoningChars = 2000
	reasoning := recentReasoning(messages, maxReasoningChars)
	if strings.TrimSpace(reasoning) != "" {
		b.WriteString("\n## 附录：最近推理摘要\n")
		b.WriteString(reasoning)
		b.WriteString("\n")
	}

	b.WriteString("\n</session-handoff>")
	return b.String()
}

// toolsForTarget 返回碰过该 target 的工具名（去重，保持首次出现序）。
//
// 对账 TS：`[...new Set(toolHistory.filter(t => t.target === file).map(t => t.tool))]`。
func toolsForTarget(entries []TrajectoryEntry, target string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range entries {
		if e.Target != target || seen[e.Tool] {
			continue
		}
		seen[e.Tool] = true
		out = append(out, e.Tool)
	}
	return out
}

// recentReasoning 从末尾往前累计 assistant 文本，上限 maxChars。
//
// 对账 TS 的 `buildHandoffFromState` 里 reasoningParts 的收集逻辑。
// **注意**：TS 侧先 `unshift`（保持时序）再 `join('\n\n---\n\n')`。
func recentReasoning(messages []session.OaiMessage, maxChars int) string {
	var parts []string
	total := 0
	for i := len(messages) - 1; i >= 0 && total < maxChars; i-- {
		m := messages[i]
		if m.Role != "assistant" || m.Content == nil || *m.Content == "" {
			continue
		}
		s := *m.Content
		if total+len(s) > maxChars {
			s = s[:maxChars-total]
		}
		parts = append([]string{s}, parts...)
		total += len(s)
	}
	return strings.Join(parts, "\n\n---\n\n")
}
