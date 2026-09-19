package prompt

import "strings"

// TodoEmptyResult 是模型可见的空清单文案。
//
// 对账 src/tools/todo-store.ts:2 的 TODO_EMPTY_RESULT——该注释明确写了
// 「todo.ts / formatList 共用，勿改成另一份字面量」，故此处必须逐字一致。
const TodoEmptyResult = "暂无待办。请用 write 动作创建清单。"

// TodoItem 是一个待办项。
//
// 对账 todo-store.ts 的 todoItemSchema。ActiveForm 是「进行中的现在时说法」
// （对标 Claude Code），但**渲染时未使用**（见 FormatTodoList 注释）。
type TodoItem struct {
	ID         string
	Content    string
	Status     string
	ActiveForm string
}

// todoIcon 对账 TS 的三元 icon 选择：
//
//	t.status === 'completed' ? '✓' : t.status === 'in_progress' ? '►' : '○'
//
// 注意兜底分支是 '○'（pending），故**任何非 completed/in_progress 的状态
// 都渲染为 '○'**——包括未知状态。
func todoIcon(status string) string {
	switch status {
	case "completed":
		return "✓"
	case "in_progress":
		return "►"
	default:
		return "○"
	}
}

// FormatTodoList 复刻 TodoStore.formatList —— 模型可见的清单文本。
//
// 格式：`{icon} [{id}] {content} ({status})`，换行连接。
// 空清单返回 TodoEmptyResult。
//
// **顺序敏感**：输出顺序 = 输入顺序（不排序）。
//
// **activeForm 不参与渲染**（oracle 的 withActiveForm 用例锁定）：尽管
// schema 里有该字段，formatList/formatSummary 都渲染 content。这是真实
// 行为——activeForm 的消费方在别处（TUI 状态带）。
func FormatTodoList(todos []TodoItem) string {
	if len(todos) == 0 {
		return TodoEmptyResult
	}
	lines := make([]string, len(todos))
	for i, t := range todos {
		lines[i] = todoIcon(t.Status) + " [" + t.ID + "] " + t.Content + " (" + t.Status + ")"
	}
	return strings.Join(lines, "\n")
}

// FormatTodoSummary 复刻 TodoStore.formatSummary —— 带计数的摘要。
//
// 格式：首行 `已更新：{completed}/{total} 已完成`，随后每行
// `{icon} [{id}] {content}`（**无 status 后缀**，与 formatList 的区别）。
//
// 空清单**不特判**（与 formatList 不同）——会渲染出
// `已更新：0/0 已完成` 加空行。这是 TS 的真实行为（oracle 锁定）。
func FormatTodoSummary(todos []TodoItem) string {
	completed := 0
	for _, t := range todos {
		if t.Status == "completed" {
			completed++
		}
	}
	summary := "已更新：" + itoa(completed) + "/" + itoa(len(todos)) + " 已完成"
	items := make([]string, len(todos))
	for i, t := range todos {
		items[i] = todoIcon(t.Status) + " [" + t.ID + "] " + t.Content
	}
	return summary + "\n" + strings.Join(items, "\n")
}
