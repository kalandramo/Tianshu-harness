package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// TestTodoToolDefinition —— todo 工具的定义与 schema。
func TestTodoToolDefinition(t *testing.T) {
	tool := Todo()
	def := tool.Definition()
	if def.Name != "todo" {
		t.Errorf("工具名应为 todo，得到 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("应有描述")
	}
	if !tool.Enabled() {
		t.Error("应默认启用")
	}
	// 两个 action 的枚举
	props := def.InputSchema.Properties
	if _, ok := props["action"]; !ok {
		t.Error("schema 应有 action 字段")
	}
	if _, ok := props["todos"]; !ok {
		t.Error("schema 应有 todos 字段")
	}
}

// TestTodoToolReadEmpty —— read 空清单返回固定文案。
func TestTodoToolReadEmpty(t *testing.T) {
	tool := Todo()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"action": "read"},
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.IsError {
		t.Errorf("read 空清单不应报错：%s", res.Content)
	}
	if res.Content != "暂无待办。请用 write 动作创建清单。" {
		t.Errorf("空清单文案不符：%q", res.Content)
	}
}

// TestTodoToolWriteThenRead —— write 后 read 应返回清单。
func TestTodoToolWriteThenRead(t *testing.T) {
	tool := Todo()
	writeInput := map[string]any{
		"action": "write",
		"todos": []any{
			map[string]any{"id": "1", "content": "任务甲", "status": "completed"},
			map[string]any{"id": "2", "content": "任务乙", "status": "in_progress"},
		},
	}
	res, err := tool.Execute(context.Background(), &CallParams{Input: writeInput})
	if err != nil {
		t.Fatalf("write 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("write 不应报错：%s", res.Content)
	}
	// write 的返回应是 summary 形态（含计数）
	if !containsStr(res.Content, "已更新：1/2 已完成") {
		t.Errorf("write 返回应含计数摘要，实际 %q", res.Content)
	}

	// 再 read 应是 list 形态（含 status 后缀）
	res2, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"action": "read"},
	})
	if err != nil {
		t.Fatalf("read 失败：%v", err)
	}
	if !containsStr(res2.Content, "(completed)") {
		t.Errorf("read 返回应含 status 后缀，实际 %q", res2.Content)
	}
	if !containsStr(res2.Content, "✓ [1] 任务甲") {
		t.Errorf("read 返回应含 completed icon，实际 %q", res2.Content)
	}
	if !containsStr(res2.Content, "► [2] 任务乙") {
		t.Errorf("read 返回应含 in_progress icon，实际 %q", res2.Content)
	}
}

// TestTodoToolWriteInvalidStatus —— 非法 status 应报格式错误。
func TestTodoToolWriteInvalidStatus(t *testing.T) {
	tool := Todo()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{
			"action": "write",
			"todos": []any{
				map[string]any{"id": "1", "content": "x", "status": "bogus"},
			},
		},
	})
	if err != nil {
		t.Fatalf("执行不应返回 error（应走 isError 通道）：%v", err)
	}
	if !res.IsError {
		t.Error("非法 status 应标记 isError")
	}
}

// TestTodoToolWriteMissingFields —— 缺 id/content 应报格式错误。
func TestTodoToolWriteMissingFields(t *testing.T) {
	tool := Todo()
	for _, bad := range []map[string]any{
		{"action": "write", "todos": []any{map[string]any{"content": "x", "status": "pending"}}},
		{"action": "write", "todos": []any{map[string]any{"id": "1", "status": "pending"}}},
	} {
		res, err := tool.Execute(context.Background(), &CallParams{Input: bad})
		if err != nil {
			t.Fatalf("不应返回 error：%v", err)
		}
		if !res.IsError {
			t.Errorf("缺字段应标记 isError，输入 %v", bad)
		}
	}
}

// TestTodoToolUnknownAction —— 未知 action 应报错。
func TestTodoToolUnknownAction(t *testing.T) {
	tool := Todo()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"action": "bogus"},
	})
	if err != nil {
		t.Fatalf("不应返回 error：%v", err)
	}
	if !res.IsError {
		t.Error("未知 action 应标记 isError")
	}
}

// TestTodoToolWriteReplaces —— write 是**整体替换**（非增量）。
func TestTodoToolWriteReplaces(t *testing.T) {
	tool := Todo()
	_, _ = tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{
			"action": "write",
			"todos": []any{
				map[string]any{"id": "1", "content": "旧的", "status": "pending"},
				map[string]any{"id": "2", "content": "也旧的", "status": "pending"},
			},
		},
	})
	_, _ = tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{
			"action": "write",
			"todos": []any{
				map[string]any{"id": "3", "content": "新的", "status": "pending"},
			},
		},
	})
	res, _ := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"action": "read"}})
	if containsStr(res.Content, "旧的") {
		t.Errorf("write 应整体替换，旧项不应残留：%q", res.Content)
	}
	if !containsStr(res.Content, "新的") {
		t.Errorf("应含新项：%q", res.Content)
	}
}

// TestTodoToolWriteRawJSON —— todos 以原始 JSON 字符串形式给出（模型常见形态）。
func TestTodoToolWriteRawJSON(t *testing.T) {
	tool := Todo()
	raw, _ := json.Marshal([]map[string]any{
		{"id": "1", "content": "甲", "status": "completed"},
	})
	res, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"action": "write", "todos": string(raw)},
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("JSON 字符串形态应被接受：%s", res.Content)
	}
}

func containsStr(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
