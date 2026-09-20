package prompt

// todo-deps.go —— 待办项之间的依赖检测与可执行性排序。
//
// 对账 TS `src/tools/todo-deps.ts`。
//
// # 为什么需要它
//
// `taskStateFromTodos`（task-state.go）用 `OrderPendingByExecutability` 决定
// 「下一步」显示哪一项——可执行的排前、被阻塞的排后。**核心不变量**：只重排，
// **绝不丢弃**任何 pending 项（伪依赖边不得让真实工作从提示词里消失）。

import "regexp"

// TodoDep 是一条依赖边。
//
// 对账 TS `interface TodoDep { id, dependsOn }`。
type TodoDep struct {
	ID        string   `json:"id"`
	DependsOn []string `json:"dependsOn"`
}

// depCueRe 对账 TS 的 `DEP_CUE`——裸数字 id 前必须出现的依赖提示词。
//
// 为什么需要：`"还剩 1 个测试"` / `"fix 3 files"` 里的数字是**数量**，不是
// 依赖引用。只有带提示词（如「基于 1」）才算边。
var depCueRe = regexp.MustCompile(`(?i)(?:基于|依赖|需先|先完成|完成后|建立在|after|depends?\s+on|requires?|blocked\s+by|builds?\s+on|based\s+on)`)

// hasLetterRe 对账 TS 的 `/[A-Za-z]/`——判定 id 是否为「结构化 id」。
var hasLetterRe = regexp.MustCompile(`[A-Za-z]`)

// regexpEscape 对账 TS 的 `escapeRe`（转义正则元字符）。
var regexpMetaRe = regexp.MustCompile(`[.*+?^${}()|\[\]\\]`)

func regexpEscape(s string) string {
	return regexpMetaRe.ReplaceAllString(s, `\$0`)
}

// referencesID 报告 `content` 是否把 `id` 当作真实依赖引用。
//
// 对账 TS 的 `referencesId`——两条**不同**匹配路径：
//
//   - 结构化 id（含字母，如 "T1" / "task-2"）：独立 token 匹配（`\b` 边界）。
//     "T1" 命中「基于 T1」，但**不**命中 "T10"/"T12"。
//   - 纯数字 id（如 "1"）：**必须**紧跟依赖提示词才算边，否则「还剩 1 个」
//     会被误判为依赖。
func referencesID(content, id string) bool {
	escaped := regexpEscape(id)
	if hasLetterRe.MatchString(id) {
		re, err := regexp.Compile(`\b` + escaped + `\b`)
		if err != nil {
			return false
		}
		return re.MatchString(content)
	}
	// 纯数字 id：提示词 + 可选 # + 数字 + 词边界。
	// 对账 TS 的 `${DEP_CUE.source}\s*#?${escaped}\b`。
	re, err := regexp.Compile(`(?i)` + depCueRe.String() + `\s*#?` + escaped + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(content)
}

// DetectDependencies 扫描每个待办项的 content，提取对其他 id 的引用边。
//
// 对账 TS `detectDependencies`——纯静态分析（无 NLP）。返回顺序与入参一致，
// 每项的 dependsOn 按**其他 id 的遍历序**（即入参序，跳过自身）。
func DetectDependencies(todos []TodoItem) []TodoDep {
	out := make([]TodoDep, 0, len(todos))
	for _, t := range todos {
		deps := []string{}
		for _, other := range todos {
			if other.ID == t.ID {
				continue
			}
			if referencesID(t.Content, other.ID) {
				deps = append(deps, other.ID)
			}
		}
		out = append(out, TodoDep{ID: t.ID, DependsOn: deps})
	}
	return out
}

// ComputeMaxDepth 计算最大依赖链深度。
//
// 对账 TS `computeMaxDepth`：无依赖 → 0；单链 → 1；**有环 → -1**
// （TS 用 `Infinity`；Go 无 int Infinity，故用 -1 表示，调用方须检查）。
func ComputeMaxDepth(deps []TodoDep) int {
	depMap := make(map[string][]string, len(deps))
	for _, d := range deps {
		depMap[d.ID] = d.DependsOn
	}
	cache := make(map[string]int)

	var depth func(id string, visiting map[string]bool) int
	depth = func(id string, visiting map[string]bool) int {
		if v, ok := cache[id]; ok {
			return v
		}
		if visiting[id] {
			return -1 // 环
		}
		visiting[id] = true
		ups := depMap[id]
		if len(ups) == 0 {
			cache[id] = 0
			delete(visiting, id)
			return 0
		}
		max := 0
		for _, up := range ups {
			d := depth(up, visiting)
			if d < 0 {
				cache[id] = -1
				delete(visiting, id)
				return -1
			}
			if d+1 > max {
				max = d + 1
			}
		}
		cache[id] = max
		delete(visiting, id)
		return max
	}

	result := 0
	for _, d := range deps {
		v := depth(d.ID, map[string]bool{})
		if v < 0 {
			return -1
		}
		if v > result {
			result = v
		}
	}
	return result
}

// FindExecutable 过滤出依赖全部已完成的 pending 项，保持原序。
//
// 对账 TS `findExecutable`。
func FindExecutable(todos []TodoItem, deps []TodoDep) []TodoItem {
	completed := make(map[string]bool)
	for _, t := range todos {
		if t.Status == "completed" {
			completed[t.ID] = true
		}
	}
	depMap := make(map[string][]string, len(deps))
	for _, d := range deps {
		depMap[d.ID] = d.DependsOn
	}

	out := []TodoItem{}
	for _, t := range todos {
		if t.Status != "pending" {
			continue
		}
		ok := true
		for _, dep := range depMap[t.ID] {
			if !completed[dep] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// OrderPendingByExecutability 把可执行的 pending 项排前、被阻塞的排后。
//
// 对账 TS `orderPendingByExecutability`——**与 FindExecutable 的关键差异**：
// 每一项 pending **都保留**（只是重排），伪依赖或过期边**不会**让工作消失。
// 这正是 TaskState.remaining 使用它的原因。
func OrderPendingByExecutability(todos []TodoItem, deps []TodoDep) []TodoItem {
	execIDs := make(map[string]bool)
	for _, t := range FindExecutable(todos, deps) {
		execIDs[t.ID] = true
	}
	executable := []TodoItem{}
	blocked := []TodoItem{}
	for _, t := range todos {
		if t.Status != "pending" {
			continue
		}
		if execIDs[t.ID] {
			executable = append(executable, t)
		} else {
			blocked = append(blocked, t)
		}
	}
	return append(executable, blocked...)
}
