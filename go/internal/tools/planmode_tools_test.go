package tools

import (
	"context"
	"strings"
	"testing"
)

// planmode_tools_test.go —— plan 工具 enter_mode/exit_mode 的接线（第七十九刀）。
//
// # 缺口（本刀修）
//
// 此前 `enter_mode`/`exit_mode` 走 `planModeUnsupported` **明确报错**，理由写
// 「Go 侧尚未移植写工具禁用机制」——**但机制早已实现**（`agent/planmode.go`
// 的 `CheckPlanMode`），只是没接线。本刀补上：工具经 `CallParams` 的两个回调
// 改 `Loop` 的状态（对账 TS `plan.ts:329/363` 的 `params.enterPlanMode()`）。

// planCall 跑一次 plan 工具。
func planCall(t *testing.T, p *CallParams) (string, bool) {
	t.Helper()
	tool := Plan()
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 不该返回 error：%v", err)
	}
	return res.Content, res.IsError
}

// TestPlanEnterModeWiresCallback —— **核心**：enter_mode 调回调并报已进入。
func TestPlanEnterModeWiresCallback(t *testing.T) {
	called := 0
	p := &CallParams{
		Input: map[string]any{"action": "enter_mode"},
		EnterPlanMode: func() (string, bool) {
			called++
			return ".rivet/plans/draft-123.md", false
		},
	}
	content, isErr := planCall(t, p)
	if isErr {
		t.Fatalf("enter_mode 应成功，实得错误：%s", content)
	}
	if called != 1 {
		t.Errorf("应调用 EnterPlanMode 一次，实得 %d", called)
	}
	if !strings.Contains(content, "已进入计划模式") {
		t.Errorf("文案应含「已进入计划模式」，实得 %q", content)
	}
	if !strings.Contains(content, "draft-123.md") {
		t.Errorf("文案应含草稿路径，实得 %q", content)
	}
	// 对账 TS：进入后给出「下一步」指引
	if !strings.Contains(content, "plan action=submit") {
		t.Errorf("文案应含 submit 指引，实得 %q", content)
	}
}

// TestPlanEnterModeAlreadyPlanning —— 已在计划模式时幂等提示（不重复建草稿）。
func TestPlanEnterModeAlreadyPlanning(t *testing.T) {
	p := &CallParams{
		Input: map[string]any{"action": "enter_mode"},
		EnterPlanMode: func() (string, bool) {
			return ".rivet/plans/draft-123.md", true
		},
	}
	content, isErr := planCall(t, p)
	if isErr {
		t.Fatalf("幂等路径应成功：%s", content)
	}
	if !strings.Contains(content, "已在计划模式中") {
		t.Errorf("文案应含「已在计划模式中」，实得 %q", content)
	}
	// 幂等路径**不该**给「下一步」长指引
	if strings.Contains(content, "下一步：") {
		t.Errorf("幂等路径不该重复给指引，实得 %q", content)
	}
}

// TestPlanEnterModeFailClosedWithoutCallback —— **反面对照**：无回调时明确报错。
//
// 对账 TS：子代理不能把主代理切入计划模式 → fail-closed。
func TestPlanEnterModeFailClosedWithoutCallback(t *testing.T) {
	content, isErr := planCall(t, &CallParams{
		Input: map[string]any{"action": "enter_mode"},
		// EnterPlanMode 为 nil
	})
	if !isErr {
		t.Error("无 EnterPlanMode 回调时应报错（fail-closed）")
	}
	if !strings.Contains(content, "不可用 enter_mode") {
		t.Errorf("文案应说明上下文不可用，实得 %q", content)
	}
}

// TestPlanExitModeWiresCallback —— exit_mode 调回调并报已退出。
func TestPlanExitModeWiresCallback(t *testing.T) {
	called := 0
	p := &CallParams{
		Input:        map[string]any{"action": "exit_mode"},
		ExitPlanMode: func() { called++ },
	}
	content, isErr := planCall(t, p)
	if isErr {
		t.Fatalf("exit_mode 应成功：%s", content)
	}
	if called != 1 {
		t.Errorf("应调用 ExitPlanMode 一次，实得 %d", called)
	}
	if !strings.Contains(content, "已退出计划模式") {
		t.Errorf("文案应含「已退出计划模式」，实得 %q", content)
	}
}

// TestPlanExitModeFailClosedWithoutCallback —— 无回调时 fail-closed。
func TestPlanExitModeFailClosedWithoutCallback(t *testing.T) {
	content, isErr := planCall(t, &CallParams{
		Input: map[string]any{"action": "exit_mode"},
	})
	if !isErr {
		t.Error("无 ExitPlanMode 回调时应报错")
	}
	if !strings.Contains(content, "不可用 exit_mode") {
		t.Errorf("文案应说明上下文不可用，实得 %q", content)
	}
}

// TestPlanModeUnsupportedMessageGone —— **回归**：旧的「暂不支持」文案已移除。
//
// 钉住本刀的实质变更——若有人回退成报错，本测试红。
func TestPlanModeUnsupportedMessageGone(t *testing.T) {
	p := &CallParams{
		Input:         map[string]any{"action": "enter_mode"},
		EnterPlanMode: func() (string, bool) { return "", false },
	}
	content, _ := planCall(t, p)
	if strings.Contains(content, "暂不支持") {
		t.Errorf("enter_mode 不该再报「暂不支持」（机制已实现），实得 %q", content)
	}
}
