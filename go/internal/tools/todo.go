package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// todoTool 是 todo 工具的实现。
//
// 对账 src/tools/todo.ts。状态保存在进程内（单会话 CLI 的默认形态，
// 对账 TS 的 defaultStore 语义）；多会话场景应注入 per-session store
// （Go 侧暂未做，见 HANDOFF）。
//
// 渲染复用 `internal/prompt` 的 FormatTodoList / FormatTodoSummary ——
// 那两个函数已与 TS 逐字节对账（含 icon 三元组、计数、status 后缀差异、
// 空清单特判差异）。
type todoTool struct {
	def     contract.Definition
	todos   []prompt.TodoItem
	enabled bool
}

// Todo 构造 todo 工具。
func Todo() Tool {
	t := &todoTool{enabled: true}
	t.def = contract.Definition{
		Name: "todo",
		Description: `读写会话内的待办清单。

- read：返回当前清单（空时返回固定提示文案）
- write：**整体替换**清单（不是增量追加）
- 多步任务（3+ 步）开始前先 write 建清单，每完成一步就更新状态
- status 只接受 pending / in_progress / completed
- 任何时刻恰好一个 in_progress——同时进行多项会失去进度锚点`,
		InputSchema: objSchemaOrdered([]string{"action", "todos", "acceptance"}, map[string]any{
			"action": enumPropOrdered("read 读当前清单；write 写新清单", []string{"read", "write"}),
			"todos": arrPropOrdered("完整 todo 清单（仅 write 用）", objPropMapOrdered(
				[]string{"id", "content", "status", "activeForm"},
				map[string]any{
					"id":         strProp("任务唯一标识"),
					"content":    strProp("任务描述（祈使式，如「修复认证 bug」）"),
					"status":     enumPropOrdered("任务状态", []string{"pending", "in_progress", "completed"}),
					"activeForm": strProp("进行中的现在时说法（如「正在修复认证 bug」）；可选，缺省时面板显示 content"),
				}, "id", "content", "status")),
			"acceptance": arrPropOrdered(
				"用户级验收面（可选，代码任务建议首次 write 就带；声明须早于验证）。正例「按 ESC 后弹窗 isVisible() 为 False」。反例一律拒绝：「信号连通」「函数逻辑正确」是内部属性；「所有测试通过」「跑 test_x.py 看到 3 passed」是信号级——单测覆盖的是函数分支，不是用户按键后的行为。",
				objPropMapOrdered(
					[]string{"criterion", "status", "evidence"},
					map[string]any{
						"criterion": strProp("用户做什么动作 → 看到什么可观察结果"),
						"status":    enumPropOrdered("", []string{"pending", "met", "blocked"}),
						"evidence":  strProp("met：实际做了什么、观察到什么；blocked：为何执行不了"),
					}, "criterion", "status")),
		}, "action"),
	}
	return t
}

func (t *todoTool) Definition() contract.Definition { return t.def }

func (t *todoTool) Enabled() bool { return t.enabled }

func (t *todoTool) ConcurrencySafe() bool { return false }

func (t *todoTool) RequiresApproval(*CallParams) bool { return false }

func (t *todoTool) Timeout(*CallParams) time.Duration { return 10 * time.Second }

func (t *todoTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	action, _ := p.Input["action"].(string)

	switch action {
	case "read":
		return contract.Result{Content: prompt.FormatTodoList(t.todos)}, nil

	case "write":
		items, errMsg := parseTodoItems(p.Input["todos"])
		if errMsg != "" {
			return contract.Result{Content: errMsg, IsError: true}, nil
		}
		t.todos = items
		return contract.Result{Content: prompt.FormatTodoSummary(items)}, nil

	default:
		return contract.Result{
			Content: "无效输入：action 必须是 read 或 write，得到 " + action,
			IsError: true,
		}, nil
	}
}

// parseTodoItems 解析 todos 入参。
//
// 接受两种形态（对账 TS 的宽松解析）：
//   - 结构化数组（[]any of map）
//   - **原始 JSON 字符串**（模型常把数组序列化成字符串）
//
// 校验：id 与 content 非空、status 必须是三值之一。
func parseTodoItems(raw any) ([]prompt.TodoItem, string) {
	if raw == nil {
		return nil, "无效输入：write 需要 todos 字段"
	}

	// 原始 JSON 字符串形态
	if s, ok := raw.(string); ok {
		var arr []map[string]any
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return nil, "无效输入：todos 字符串不是合法 JSON 数组"
		}
		return validateTodoItems(arr)
	}

	arr, ok := raw.([]any)
	if !ok {
		return nil, "无效输入：todos 必须是数组"
	}
	maps := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, "无效输入：todos 的每项必须是对象"
		}
		maps = append(maps, m)
	}
	return validateTodoItems(maps)
}

func validateTodoItems(maps []map[string]any) ([]prompt.TodoItem, string) {
	valid := map[string]bool{"pending": true, "in_progress": true, "completed": true}
	out := make([]prompt.TodoItem, 0, len(maps))
	for i, m := range maps {
		id, _ := m["id"].(string)
		content, _ := m["content"].(string)
		status, _ := m["status"].(string)
		if id == "" {
			return nil, fmt.Sprintf("无效输入：第 %d 项缺 id", i+1)
		}
		if content == "" {
			return nil, fmt.Sprintf("无效输入：第 %d 项缺 content", i+1)
		}
		if !valid[status] {
			return nil, fmt.Sprintf(
				"无效输入：第 %d 项的 status %q 非法（只接受 pending / in_progress / completed）",
				i+1, status)
		}
		activeForm, _ := m["activeForm"].(string)
		out = append(out, prompt.TodoItem{
			ID: id, Content: content, Status: status, ActiveForm: activeForm,
		})
	}
	return out, ""
}
