package compact

// taskstate.go —— 从轨迹 + 模型文本提取任务状态。
//
// 对账 TS `src/agent/task-state.ts` 的 `extractTaskState` 与 `taskStateFromTodos`。
//
// # 为什么需要它
//
// session split 的 handoff 需要「当前在做什么 / 已完成 / 待办 / 关键决策」。
// TS 有两个来源：
//   - `extractTaskState`：从**轨迹 + 模型流式文本**启发式提取（正则）
//   - `taskStateFromTodos`：从**权威 todo 清单**构建（模型自己的目标分解）
//
// 后者更可靠（todo 是模型亲自写的），且带依赖感知排序。
//
// # 包位置
//
// TS 在 `src/agent/task-state.ts`，Go 放 `internal/compact`——消费方
// `BuildSessionHandoff` 在此。依赖 `internal/prompt` 的 TodoItem 与依赖排序。

import (
	"regexp"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// TaskState 是提取出的任务状态。
//
// 字段顺序对账 TS 接口（completed → current → remaining → decisions）。
type TaskState struct {
	Completed []string `json:"completed"`
	Current   string   `json:"current"`
	Remaining []string `json:"remaining"`
	// Decisions 是从模型文本提取的关键决策/发现（用于反思式压缩）。
	Decisions []string `json:"decisions"`
}

// 三条启发式正则，对账 TS 的 NEXT_STEP_RE / DECISION_RE / FINDING_RE。
//
// **必须用 (?i) + 逐字相同的模式**——它们决定提取出什么文本，进 handoff
// 后改变字节。TS 用 `g` 标志（findall 语义），Go 的 FindAllString 等价。
var (
	nextStepRe = regexp.MustCompile(`(?i)(?:next|then|after that|i will|step \d|接下来|然后|下一步)[^.。]*(?:[.。]|$)`)
	decisionRe = regexp.MustCompile(`(?i)(?:I(?:'ll| will) (?:use|go with|choose|pick|implement)|decided to|approach:|strategy:)[^.。]*(?:[.。]|$)`)
	findingRe  = regexp.MustCompile(`(?i)(?:found that|discovered|the (?:issue|problem|root cause) (?:is|was)|turns out|发现|原因是)[^.。]*(?:[.。]|$)`)
)

// maxCompleted 对账 TS 的 `successful.slice(-5)`。
const maxCompleted = 5

// maxRemaining 对账 TS 的 `if (remaining.length >= 3) break`。
const maxRemaining = 3

// maxDecisionHits 对账 TS 的 `if (decisions.length >= 3) break`。
const maxDecisionHits = 3

// maxFindingHits 对账 TS 的 `if (decisions.length >= 5) break`。
//
// **注意**：DECISION 与 FINDING 共享 decisions 数组——FINDING 的上限是 5
// （不是 3+5），因为判断条件用的是累计长度。
const maxFindingHits = 5

// utf16Slice 按 **UTF-16 code unit** 截断，对账 TS 的 `String.prototype.slice`。
//
// **为什么不能用字节或符文切片**：TS 的 slice 按 UTF-16 code unit 计数。
// 对 BMP 字符（含中文）1 码点 = 1 code unit，与 Go 的符文数一致；但对
// 补充平面字符（emoji 等）1 码点 = 2 code units——此时符文切片会**多留**。
// oracle 的 truncation_boundary_cjk 用例锁定中文场景。
//
// n <= 0 返回空串（对账 slice(0,0)）。
func utf16Slice(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2 // 补充平面：代理对占 2 个 code unit
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// ExtractTaskState 从轨迹与模型文本提取任务状态。
//
// 对账 TS `extractTaskState`。**空轨迹走 early-return 分支**——返回
// `{[], "starting", [], []}`（不是零值！current 是字面量 "starting"）。
func ExtractTaskState(entries []TrajectoryEntry, lastModelText string) TaskState {
	if len(entries) == 0 {
		return TaskState{Completed: []string{}, Current: "starting", Remaining: []string{}, Decisions: []string{}}
	}

	// completed：成功项（success / retried-success）的最后 5 条，
	// 格式 `"<tool> <basename(target)>"`。
	successful := []TrajectoryEntry{}
	for _, e := range entries {
		if e.Status == TrajectorySuccess || e.Status == TrajectoryRetriedSuccess {
			successful = append(successful, e)
		}
	}
	if len(successful) > maxCompleted {
		successful = successful[len(successful)-maxCompleted:]
	}
	completed := make([]string, 0, len(successful))
	for _, e := range successful {
		completed = append(completed, e.Tool+" "+basename(e.Target))
	}

	// current：末条失败 → "fixing <errorClass|error> in <basename>"；否则
	// "<tool> <basename>"。
	last := entries[len(entries)-1]
	var current string
	if last.IsFailure() {
		ec := last.ErrorClass
		if ec == "" {
			ec = "error"
		}
		current = "fixing " + ec + " in " + basename(last.Target)
	} else {
		current = last.Tool + " " + basename(last.Target)
	}

	// remaining：NEXT_STEP 命中，截 60 code unit，最多 3 条。
	remaining := []string{}
	for _, m := range nextStepRe.FindAllString(lastModelText, -1) {
		remaining = append(remaining, utf16Slice(strings.TrimSpace(m), 60))
		if len(remaining) >= maxRemaining {
			break
		}
	}

	// decisions：先 DECISION（上限 3），再 FINDING（**共享数组**，上限 5）。
	decisions := []string{}
	for _, m := range decisionRe.FindAllString(lastModelText, -1) {
		decisions = append(decisions, utf16Slice(strings.TrimSpace(m), 80))
		if len(decisions) >= maxDecisionHits {
			break
		}
	}
	for _, m := range findingRe.FindAllString(lastModelText, -1) {
		decisions = append(decisions, utf16Slice(strings.TrimSpace(m), 80))
		if len(decisions) >= maxFindingHits {
			break
		}
	}

	return TaskState{Completed: completed, Current: current, Remaining: remaining, Decisions: decisions}
}

// basename 对账 TS 的 `target.split('/').pop() ?? target`。
//
// **注意**：TS 用 '/' 分割（不认反斜杠）。Windows 路径 `a\b.ts` 在 TS 侧
// 整串保留（因为无 '/'）——Go 必须复刻，不能用 filepath.Base。
func basename(target string) string {
	if i := strings.LastIndex(target, "/"); i >= 0 {
		return target[i+1:]
	}
	return target
}

// TaskStateFromTodos 从权威 todo 清单构建 TaskState。
//
// 对账 TS `taskStateFromTodos`：
//   - completed = 所有 completed 项的 content
//   - 依赖感知排序：可执行项在前、被阻塞项在后（**不丢项**）
//   - current = 首个 in_progress 的 content，否则 ordered[0]，否则 "working"
//   - remaining = ordered 中**除 current 之外**的全部
//
// decisions 由调用方传入（todos 不携带决策——TS 用启发式提取）。
func TaskStateFromTodos(todos []prompt.TodoItem, decisions []string) TaskState {
	completed := []string{}
	for _, t := range todos {
		if t.Status == "completed" {
			completed = append(completed, t.Content)
		}
	}

	deps := prompt.DetectDependencies(todos)
	ordered := prompt.OrderPendingByExecutability(todos, deps)

	var currentID, current string
	found := false
	for _, t := range todos {
		if t.Status == "in_progress" {
			current = t.Content
			currentID = t.ID
			found = true
			break
		}
	}
	if !found {
		if len(ordered) > 0 {
			current = ordered[0].Content
			currentID = ordered[0].ID
		} else {
			current = "working"
		}
	}

	remaining := []string{}
	for _, t := range ordered {
		if t.ID != currentID {
			remaining = append(remaining, t.Content)
		}
	}
	if decisions == nil {
		decisions = []string{}
	}
	return TaskState{Completed: completed, Current: current, Remaining: remaining, Decisions: decisions}
}
