package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// plan_draft_wiring_test.go —— `ActivePlanFilePath` 的跨包接线（第一百零七刀）。
//
// # 为什么必须有这个文件
//
// `tools` 包的 `plan_draft_test.go` 直调 `&CallParams{ActivePlanFilePath: ...}`
// ——那只证明「给了字段会回读」。**不证明生产会填它**：字段存在但零写入方
// 正是本仓库的高频缺陷模式（`SessionModifiedFiles`/`OwnedFiles`/`Artifacts` 均栽过）。
//
// 本文件走**真装配路径**：
//
//	plan enter_mode（真工具）→ Loop.enterPlanMode（真建草稿 + 置字段）
//	→ write_file 写草稿（真盘）
//	→ buildToolCallParams（唯一构造点）
//	→ plan submit（省略 plan 字段）→ 回读草稿 → 落规范文件 → 回收草稿
//
// 任一环断了，`tools` 包的单测都不会红。

// newPlanDraftLoop 构造走真装配的 Loop（含 plan 工具与写工具）。
func newPlanDraftLoop(t *testing.T, dir string) *Loop {
	t.Helper()
	l := &Loop{}
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("plan-draft-wiring")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	l.registry.Register(tools.Plan())
	l.registry.Register(tools.WriteFile(dir, nil))
	return l
}

// TestPlanDraftPathInjectedIntoCallParams —— ★ 接线断言：构造点带出活动计划文件。
//
// 这是本刀新增字段的**写入方**证明（对照 `registry.go` 里 `OwnedFiles` 的登记：
// 那种「有读取方零写入方」让功能静默退化）。
func TestPlanDraftPathInjectedIntoCallParams(t *testing.T) {
	dir := t.TempDir()
	l := newPlanDraftLoop(t, dir)

	// 进入计划模式（真回调链：工具 → Loop.enterPlanMode）。
	enter := l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "enter_mode"},
	})
	if enter.IsError {
		t.Fatalf("enter_mode 应成功：%s", enter.Content)
	}
	if l.ActivePlanFilePath == "" {
		t.Fatal("enter_mode 后 Loop.ActivePlanFilePath 应有值")
	}

	// 唯一构造点应把它带给工具。
	p := l.buildToolCallParams(toolCall{id: "t1", name: "plan"})
	if p.ActivePlanFilePath == "" {
		t.Fatal("buildToolCallParams 应注入 ActivePlanFilePath（否则 submit 无法回读草稿）")
	}
	if p.ActivePlanFilePath != l.ActivePlanFilePath {
		t.Errorf("注入值应与 Loop 字段一致：got %q want %q", p.ActivePlanFilePath, l.ActivePlanFilePath)
	}
}

// TestPlanDraftRoundTripEndToEnd —— ★ 全链路：enter → 写草稿 → submit（省略 plan）。
//
// 这是「plan-mode 指令块那句『省略 plan 字段则从活动计划文件读取』变真」的
// 用户级证据。**修前此用例在 submit 处报「plan 必填」**。
func TestPlanDraftRoundTripEndToEnd(t *testing.T) {
	dir := t.TempDir()
	l := newPlanDraftLoop(t, dir)

	// ① enter_mode —— 真建草稿（空文件）
	if enter := l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "enter_mode"},
	}); enter.IsError {
		t.Fatalf("enter_mode 应成功：%s", enter.Content)
	}
	draftRel := l.ActivePlanFilePath

	// ② 写草稿正文（经真写工具——plan mode 下对活动计划文件放行）
	body := "# 接线验证计划\n\n## 需求提炼\n\n目标：验证草稿回读。非目标：无。\n\n" +
		"```mermaid\nflowchart TD\n    A(输入) --> B([输出])\n```\n\n## 反证/复现\n\n断言——证据：本测试。\n"
	if w := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(dir, filepath.FromSlash(draftRel)), "content": body},
	}); w.IsError {
		t.Fatalf("写活动计划文件应放行：%s", w.Content)
	}

	// ③ submit **省略 plan 字段** —— 回读草稿
	sub := l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "submit", "title": "接线验证计划"},
	})
	if sub.IsError {
		t.Fatalf("省略 plan 字段时应回读草稿并提交成功（修前报「plan 必填」）：%s", sub.Content)
	}
	if !strings.Contains(sub.Content, "计划已提交") {
		t.Errorf("应报提交成功：%q", sub.Content)
	}

	// ④ 规范计划文件已落盘，内容来自草稿
	written, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "接线验证计划.md"))
	if err != nil {
		t.Fatalf("规范计划文件应存在：%v", err)
	}
	if !strings.Contains(string(written), "验证草稿回读") {
		t.Errorf("落盘内容应来自草稿：%q", string(written))
	}

	// ⑤ 草稿被回收（对账 TS plan.ts:588）
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(draftRel))); !os.IsNotExist(err) {
		t.Errorf("草稿应在提交成功后回收，但仍存在：%v", err)
	}
}

// TestPlanDraftInjectionEmptyOutsidePlanMode —— 反向对照：非 plan mode 时为空。
//
// 证明上两条不是恒真——若把注入写成「总是某值」，本用例会红。
// 同时它守住「非 plan mode 下 submit 无草稿可读 → plan 必填」的 fail-closed。
func TestPlanDraftInjectionEmptyOutsidePlanMode(t *testing.T) {
	dir := t.TempDir()
	l := newPlanDraftLoop(t, dir)

	p := l.buildToolCallParams(toolCall{id: "t1", name: "plan"})
	if p.ActivePlanFilePath != "" {
		t.Errorf("非 plan mode 时不该有活动计划文件，实得 %q", p.ActivePlanFilePath)
	}

	// 无草稿 + 无 plan → 仍 fail-closed
	sub := l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "submit", "title": "T"},
	})
	if !sub.IsError {
		t.Fatalf("无草稿无 plan 应报错，实得成功：%s", sub.Content)
	}
	if strings.Contains(sub.Content, "enter_mode 未支持") {
		t.Errorf("不该再声称「enter_mode 未支持」（早已实现）：%s", sub.Content)
	}
}

// TestPlanDraftClearedAfterExitMode —— exit_mode 后字段清空（不残留陈旧草稿路径）。
//
// 对账 `Loop.exitPlanMode` 清 `ActivePlanFilePath`。若不变量被破坏，
// 退出 plan mode 后再 submit 会去读**已不存在的**草稿 → 误报读失败。
func TestPlanDraftClearedAfterExitMode(t *testing.T) {
	dir := t.TempDir()
	l := newPlanDraftLoop(t, dir)

	l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "enter_mode"},
	})
	if l.ActivePlanFilePath == "" {
		t.Fatal("enter 后应有草稿路径")
	}
	l.executeTool(context.Background(), toolCall{
		name: "plan", input: map[string]any{"action": "exit_mode"},
	})

	p := l.buildToolCallParams(toolCall{id: "t1", name: "plan"})
	if p.ActivePlanFilePath != "" {
		t.Errorf("exit_mode 后构造点不该再带草稿路径，实得 %q", p.ActivePlanFilePath)
	}
}
