package session

import (
	"regexp"
	"strings"
)

// taskPatterns 对账 TS 的三个 patterns，**顺序敏感**（一行只匹配第一个命中）。
//
// 逐字对账 src/agent/session-state.ts:188-190：
//
//	/^[\s*\-\d.#]*\*?\*?([PpTtSs]\d+)\*?\*?[\s:\-.]+(.+)/
//	/^\s*\*?\*?([PpTtSs]\d+)\*?\*?[\s:\-.]+(.+)/
//	/\b([PpTtSs]\d+)\b\s*(?:-|=>|->|:|：)\s*(.+)/
//
// ID 前缀是 **P/T/S**（大小写均可）。
var taskPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^[\s*\-\d.#]*\*?\*?([PpTtSs]\d+)\*?\*?[\s:\-.]+(.+)`),
	regexp.MustCompile(`^\s*\*?\*?([PpTtSs]\d+)\*?\*?[\s:\-.]+(.+)`),
	regexp.MustCompile(`\b([PpTtSs]\d+)\b\s*(?:-|=>|->|:|：)\s*(.+)`),
}

// strippedRe 对账 content.replace(/[`*_\-\s]/g, ”)。
var strippedRe = regexp.MustCompile("[`*_\\-\\s]")

// ExtractTaskList 从 Assistant 回复文本中提取任务列表（支持 Markdown 列表、
// 编号、粗体等格式）。
//
// 对账 extractTaskList。**合并语义（非覆盖）**：已存在的 id 保留其
// status/turnCreated，仅在检测到显式状态标记时更新 status，并刷新
// content/turnUpdated；新 id 追加到尾部。这样跨多轮的计划不会被后续
// 含新编号的回复整体冲掉。
//
// 注意：提取不到任何项时**返回现有列表的拷贝**（不清空）。
func (m *Manager) ExtractTaskList(text string, turn int) []TaskListItem {
	type parsed struct {
		id      string
		content string
		status  string // 空串 = 无信号
	}

	var items []parsed
	seen := map[string]bool{}

	for _, line := range strings.Split(text, "\n") {
		for _, re := range taskPatterns {
			match := re.FindStringSubmatch(line)
			if match == nil || match[1] == "" || match[2] == "" {
				continue
			}
			id := strings.ToUpper(match[1])
			content := strings.TrimSpace(match[2])
			// 过滤过短或纯符号内容——**length > 3 是 UTF-16 code unit 语义**
			// （中文三字 = 3 units，会被过滤；这是真实行为，已由 oracle 锁定）
			if utf16StrippedLen(content) > 3 && !seen[id] {
				items = append(items, parsed{
					id:      id,
					content: utf16Slice(content, taskContentMax),
					status:  detectStatusMarker(line),
				})
				seen[id] = true
			}
			break // 一行只匹配第一个命中的模式
		}
	}

	if len(items) == 0 {
		return append([]TaskListItem(nil), m.state.TaskList...)
	}

	// 合并：保留既有项的 status/turnCreated（除非检测到显式状态标记），追加新项。
	existingByID := map[string]TaskListItem{}
	for _, it := range m.state.TaskList {
		existingByID[it.ID] = it
	}
	merged := append([]TaskListItem(nil), m.state.TaskList...)

	for _, p := range items {
		existing, ok := existingByID[p.id]
		if ok {
			status := existing.Status
			if p.status != "" {
				status = p.status
			}
			for i := range merged {
				if merged[i].ID == p.id {
					merged[i].Content = p.content
					merged[i].Status = status
					merged[i].TurnUpdated = turn
					break
				}
			}
		} else {
			status := p.status
			if status == "" {
				status = "pending"
			}
			merged = append(merged, TaskListItem{
				ID:          p.id,
				Content:     p.content,
				Status:      status,
				TurnCreated: turn,
				TurnUpdated: turn,
			})
		}
	}

	// 容量上限：**保留最近更新的项**（按 turnUpdated 降序取前 maxTaskItems）
	if len(merged) > maxTaskItems {
		sorted := append([]TaskListItem(nil), merged...)
		// 稳定降序（Go 的 sort.Slice 不稳定，用 SliceStable 保证同 turn 保持原序）
		stableSortByTurnDesc(sorted)
		merged = sorted[:maxTaskItems]
	}

	m.state.TaskList = merged
	m.state.UpdatedAt = m.now()
	return append([]TaskListItem(nil), merged...)
}

// stableSortByTurnDesc 按 TurnUpdated 降序稳定排序。
//
// 对账 TS 的 [...merged].sort((a,b) => b.turnUpdated - a.turnUpdated)——
// V8 的 Array.sort 自 ES2019 起稳定，故 Go 侧用等价稳定排序。
func stableSortByTurnDesc(items []TaskListItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].TurnUpdated > items[j-1].TurnUpdated; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// UpdateTaskListItem 更新单个任务项的状态；id 不存在返回 false。
//
// 对账 updateTaskListItem（不可变语义：替换而非原地改）。
func (m *Manager) UpdateTaskListItem(id, status string, turn int) bool {
	idx := -1
	for i, it := range m.state.TaskList {
		if it.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	next := append([]TaskListItem(nil), m.state.TaskList...)
	next[idx].Status = status
	next[idx].TurnUpdated = turn
	m.state.TaskList = next
	m.state.UpdatedAt = m.now()
	return true
}

// RenderForVolatile 渲染紧凑的 XML 块供 volatile 块注入。
//
// 对账 renderForVolatile。目标 < 500 字符。
//
// **两个反直觉行为**（都有 oracle 用例锁定）：
//  1. 无内容时返回**空串**——不是 `<session-state></session-state>` 空壳。
//     空壳会让下游的 truthiness 检查误判「状态存在」。
//  2. 超 500 字符时**先砍 decisions 段**；若砍完只剩开标签则整体丢弃
//     （又回到空壳问题）；仍超长则整体截断加省略号。
func (m *Manager) RenderForVolatile() string {
	s := m.state
	var lines []string

	// objective 不在本块渲染（它在 task-contract 通道）——
	// TS 侧注释明确记录了这段曾被移除的死代码。
	var modified []string
	// 按**插入序**遍历（FileIndex 保证）——对账 TS 的 Object.entries
	for _, k := range s.FileIndex.Keys() {
		if v, ok := s.FileIndex.Get(k); ok && v.ModifiedByMe {
			modified = append(modified, k)
		}
	}
	if len(modified) > 0 {
		if len(modified) > renderFiles {
			modified = modified[:renderFiles]
		}
		lines = append(lines, "Modified: "+strings.Join(modified, ", "))
	}

	if len(s.Decisions) > 0 {
		lines = append(lines, "Decisions:")
		start := len(s.Decisions) - renderDecisions
		if start < 0 {
			start = 0
		}
		for _, d := range s.Decisions[start:] {
			lines = append(lines, "  - "+d.Decision)
		}
	}

	var failed []string
	for _, v := range s.Verification {
		if v.Status == "failed" {
			failed = append(failed, v.Target)
		}
	}
	if len(failed) > 0 {
		lines = append(lines, "Failed: "+strings.Join(failed, ", "))
	}

	// 无内容 → 无块（返回空串，不是空壳）
	if len(lines) == 0 {
		return ""
	}

	closing := "\n</session-state>"
	result := "<session-state>\n" + strings.Join(lines, "\n") + closing

	if utf16Len(result) > volatileMaxChars {
		// 先砍 decisions 段——仅当存在时。indexOf 无匹配返回 -1，
		// 用 slice(0,-1) 会吃掉收尾标签的 '>'。
		if at := strings.Index(result, "Decisions:"); at > 0 {
			head := strings.TrimRight(result[:at], " \t\n\r")
			if head == "<session-state>" {
				// 砍完只剩开标签 = 空壳形态，整体丢弃
				result = ""
			} else {
				result = head + closing
			}
		}
		// 仍超长 → 整体截断加省略号
		if result != "" && utf16Len(result) > volatileMaxChars {
			maxContent := volatileMaxChars - utf16Len(closing) - 3 // "..."
			result = utf16Slice(result, maxContent) + "..." + closing
		}
	}

	return result
}
