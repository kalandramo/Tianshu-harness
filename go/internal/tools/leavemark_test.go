package tools

import (
	"context"
	"strings"
	"testing"
)

func runLeaveMark(t *testing.T, input map[string]any, onMark func(LeaveMarkInput)) (string, bool) {
	t.Helper()
	tool := LeaveMark()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:       input,
		Cwd:         t.TempDir(),
		ToolUseID:   "t",
		OnLeaveMark: onMark,
	})
	if err != nil {
		t.Fatalf("Execute 返回 error: %v", err)
	}
	return res.Content, res.IsError
}

// TestLeaveMarkCapturesViaCallback 对账 TS 用例 1：回调捕获 symbol/summary/type/tags。
func TestLeaveMarkCapturesViaCallback(t *testing.T) {
	var calls []LeaveMarkInput
	content, isErr := runLeaveMark(t, map[string]any{
		"symbol":  "⚘",
		"summary": "wired the starmap",
		"type":    "feature",
		"tags":    []any{"ui"},
	}, func(m LeaveMarkInput) { calls = append(calls, m) })

	if isErr {
		t.Fatalf("不应报错，content=%q", content)
	}
	if len(calls) != 1 {
		t.Fatalf("回调次数期望 1，实得 %d", len(calls))
	}
	got := calls[0]
	if got.Symbol != "⚘" {
		t.Errorf("symbol 期望 ⚘，实得 %q", got.Symbol)
	}
	if got.Summary != "wired the starmap" {
		t.Errorf("summary 期望 'wired the starmap'，实得 %q", got.Summary)
	}
	if got.Type != "feature" {
		t.Errorf("type 期望 feature，实得 %q", got.Type)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "ui" {
		t.Errorf("tags 期望 [ui]，实得 %v", got.Tags)
	}
}

// TestLeaveMarkDropsUnknownType 对账 TS 用例 2：未知 type 被丢弃而非透传。
func TestLeaveMarkDropsUnknownType(t *testing.T) {
	var calls []LeaveMarkInput
	_, _ = runLeaveMark(t, map[string]any{
		"symbol": "✦", "summary": "x", "type": "bogus",
	}, func(m LeaveMarkInput) { calls = append(calls, m) })

	if len(calls) != 1 {
		t.Fatalf("回调次数期望 1，实得 %d", len(calls))
	}
	if calls[0].Type != "" {
		t.Errorf("未知 type 应被丢弃（空串），实得 %q", calls[0].Type)
	}
}

// TestLeaveMarkRequiresSymbolAndSummary 对账 TS 用例 3：两个必填字段。
func TestLeaveMarkRequiresSymbolAndSummary(t *testing.T) {
	c, e := runLeaveMark(t, map[string]any{"summary": "x"}, nil)
	if !e {
		t.Errorf("缺 symbol 应报错，实得 isError=false content=%q", c)
	}
	if !strings.Contains(c, "symbol 必填") {
		t.Errorf("错误消息应含 'symbol 必填'，实得 %q", c)
	}

	c, e = runLeaveMark(t, map[string]any{"symbol": "✦"}, nil)
	if !e {
		t.Errorf("缺 summary 应报错，实得 isError=false content=%q", c)
	}
	if !strings.Contains(c, "summary 必填") {
		t.Errorf("错误消息应含 'summary 必填'，实得 %q", c)
	}
}

// TestLeaveMarkInertWithoutCallback 对账 TS 用例 4：无回调时不抛错、走降级路径。
func TestLeaveMarkInertWithoutCallback(t *testing.T) {
	content, isErr := runLeaveMark(t, map[string]any{"symbol": "✦", "summary": "x"}, nil)
	if isErr {
		t.Errorf("无回调不应报错，content=%q", content)
	}
	if !strings.Contains(content, "未挂接星图") {
		t.Errorf("降级消息应含 '未挂接星图'，实得 %q", content)
	}
}

// TestLeaveMarkToolFlags 对账 TS 用例 5：三个布尔标志。
func TestLeaveMarkToolFlags(t *testing.T) {
	tool := LeaveMark()
	if tool.RequiresApproval(nil) {
		t.Error("RequiresApproval 应为 false")
	}
	if !tool.ConcurrencySafe() {
		t.Error("ConcurrencySafe 应为 true")
	}
	if !tool.Enabled() {
		t.Error("Enabled 应为 true")
	}
}

// TestLeaveMarkTrimsWhitespace 锁定 symbol/summary 的 trim 行为。
//
// 对账 TS：`symbol: symbol.trim(), summary: summary.trim()`。且校验用的是
// `!x.trim()`——**纯空白视为缺失**。
func TestLeaveMarkTrimsWhitespace(t *testing.T) {
	var calls []LeaveMarkInput
	_, isErr := runLeaveMark(t, map[string]any{
		"symbol": "  ✦  ", "summary": "  padded  ",
	}, func(m LeaveMarkInput) { calls = append(calls, m) })
	if isErr {
		t.Fatal("不应报错")
	}
	if calls[0].Symbol != "✦" {
		t.Errorf("symbol 应被 trim，实得 %q", calls[0].Symbol)
	}
	if calls[0].Summary != "padded" {
		t.Errorf("summary 应被 trim，实得 %q", calls[0].Summary)
	}

	// 纯空白 = 缺失
	if _, e := runLeaveMark(t, map[string]any{"symbol": "   ", "summary": "x"}, nil); !e {
		t.Error("纯空白 symbol 应视为缺失并报错")
	}
	if _, e := runLeaveMark(t, map[string]any{"symbol": "✦", "summary": "\t\n"}, nil); !e {
		t.Error("纯空白 summary 应视为缺失并报错")
	}
}

// TestLeaveMarkNonStringTypes 锁定非字符串输入的处理。
//
// TS：`typeof symbol !== 'string'` → 报错；`typeof rawType === 'string'` 才取 type。
// Go 侧类型断言失败得零值，等价于 TS 的非字符串分支。
func TestLeaveMarkNonStringTypes(t *testing.T) {
	if _, e := runLeaveMark(t, map[string]any{"symbol": 123, "summary": "x"}, nil); !e {
		t.Error("非字符串 symbol 应报错")
	}
	if _, e := runLeaveMark(t, map[string]any{"symbol": "✦", "summary": []any{"x"}}, nil); !e {
		t.Error("非字符串 summary 应报错")
	}
	// 非字符串 type → 丢弃，不报错
	var calls []LeaveMarkInput
	_, isErr := runLeaveMark(t, map[string]any{
		"symbol": "✦", "summary": "x", "type": 42,
	}, func(m LeaveMarkInput) { calls = append(calls, m) })
	if isErr {
		t.Fatal("非字符串 type 不应报错")
	}
	if calls[0].Type != "" {
		t.Errorf("非字符串 type 应被丢弃，实得 %q", calls[0].Type)
	}
}

// TestLeaveMarkTagsFilterNonStrings 锁定 tags 的元素级过滤。
//
// 对账 TS：`tags.filter(t => typeof t === 'string')`——非字符串元素被剔除，
// 字符串元素保留。
func TestLeaveMarkTagsFilterNonStrings(t *testing.T) {
	var calls []LeaveMarkInput
	_, _ = runLeaveMark(t, map[string]any{
		"symbol": "✦", "summary": "x",
		"tags": []any{"a", 42, "b", nil, true, "c"},
	}, func(m LeaveMarkInput) { calls = append(calls, m) })

	want := []string{"a", "b", "c"}
	if len(calls[0].Tags) != len(want) {
		t.Fatalf("tags 期望 %v，实得 %v", want, calls[0].Tags)
	}
	for i, w := range want {
		if calls[0].Tags[i] != w {
			t.Errorf("tags[%d] 期望 %q，实得 %q", i, w, calls[0].Tags[i])
		}
	}
}

// TestLeaveMarkAllValidTypes 锁定五个合法 type 全部透传。
func TestLeaveMarkAllValidTypes(t *testing.T) {
	for _, typ := range []string{"feature", "fix", "refactor", "architecture", "milestone"} {
		var calls []LeaveMarkInput
		_, isErr := runLeaveMark(t, map[string]any{
			"symbol": "✦", "summary": "x", "type": typ,
		}, func(m LeaveMarkInput) { calls = append(calls, m) })
		if isErr {
			t.Fatalf("type=%s 不应报错", typ)
		}
		if calls[0].Type != typ {
			t.Errorf("type=%s 应透传，实得 %q", typ, calls[0].Type)
		}
	}
}

// TestLeaveMarkSuccessMessageShape 锁定成功消息的形状（对账 TS 模板）。
func TestLeaveMarkSuccessMessageShape(t *testing.T) {
	content, _ := runLeaveMark(t, map[string]any{
		"symbol": "✦", "summary": "did the thing",
	}, func(LeaveMarkInput) {})

	if !strings.Contains(content, "✦") {
		t.Errorf("成功消息应含符号，实得 %q", content)
	}
	if !strings.Contains(content, "已落下") {
		t.Errorf("成功消息应含 '已落下'，实得 %q", content)
	}
	if !strings.Contains(content, "摘要：did the thing") {
		t.Errorf("成功消息应含 '摘要：<summary>'，实得 %q", content)
	}
}
