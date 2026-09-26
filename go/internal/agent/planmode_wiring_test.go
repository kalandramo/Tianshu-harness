package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// planmode_wiring_test.go —— Plan Mode 门接线到 `executeTool`（第七十九刀）。
//
// # 缺口（本刀修）
//
// `internal/agent/planmode.go` 的 `CheckPlanMode`（5 段分支，oracle 对账过）
// **在生产代码零消费**——`Loop` 没有 planMode 状态字段，门链也不调它。
// 后果：`plan` 工具的 `enter`/`exit` action **明确报错**说「Go 侧尚未移植
// 写工具禁用机制」（`plan.go:176`）——**但机制其实已实现**。
//
// 本刀把三者接起来：`Loop.PlanModeState` 字段 + 门链调 `CheckPlanMode`
// + `plan` 工具的 enter/exit 真正改状态。
//
// # 对账 TS
//
// `tool-pipeline.ts:1056-1066` 的 plan-mode gate（在 ask-mode gate 之前）。
//
// **Go 侧的差异（有意）**：TS 的 `delegatesWriteCapableProfile` 依赖
// `delegateProfilesFromInput` + `profileIsPlanModeSafe`——**Go 侧无 delegate
// 工具**，故该字段恒 false（该分支不触发）。

// planModeProbe 跑一次 executeTool，返回是否被 plan-mode 门拦下。
func planModeProbe(t *testing.T, state PlanModeState, toolName string, input map[string]any) (string, bool) {
	t.Helper()
	l := &Loop{}
	l.cfg.ApprovalMode = "dangerously-skip-permissions" // 跳过审批门，隔离 plan 门
	l.cfg.Cwd = t.TempDir()
	l.PlanModeState = state
	l.State = session.New("planmode-wiring-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry() // 空 registry：放行时会报「工具未找到」

	res := l.executeTool(context.Background(), toolCall{name: toolName, input: input})
	return res.Content, res.IsError
}

// isPlanModeBlock 判定文案是否来自 plan-mode 门。
func isPlanModeBlock(content string) bool {
	return len(content) >= 9 && content[:9] == "Plan Mode"
}

// TestPlanModeBlocksWriteTools —— **核心接线断言**：planning 态下写工具被拦。
func TestPlanModeBlocksWriteTools(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "hash_edit", "apply_patch"} {
		content, isErr := planModeProbe(t, PlanModePlanning, name,
			map[string]any{"file_path": "/tmp/x.ts"})
		if !isErr {
			t.Errorf("planning 态下 %s 应被拦，实得通过：%s", name, content)
			continue
		}
		if !isPlanModeBlock(content) {
			t.Errorf("planning 态下 %s 的拦截文案应来自 plan-mode 门，实得 %q", name, content)
		}
	}
}

// TestPlanModeOffAllowsEverything —— **反面对照**：off 态不拦（默认行为不变）。
func TestPlanModeOffAllowsEverything(t *testing.T) {
	// 阳性对照：同状态（planning）必须拦——否则本测试恒真
	if _, isErr := planModeProbe(t, PlanModePlanning, "write_file",
		map[string]any{"file_path": "/tmp/x.ts"}); !isErr {
		t.Fatal("阳性对照失败：planning 态应拦 write_file")
	}
	// off 态：放行（走到空 registry → 报「工具未找到」，非 plan-mode 文案）
	content, _ := planModeProbe(t, PlanModeOff, "write_file",
		map[string]any{"file_path": "/tmp/x.ts"})
	if isPlanModeBlock(content) {
		t.Errorf("off 态不该被 plan-mode 门拦，实得 %q", content)
	}
}

// TestPlanModeAllowsWhitelistedTools —— 白名单工具（只读）在 planning 态放行。
func TestPlanModeAllowsWhitelistedTools(t *testing.T) {
	if _, isErr := planModeProbe(t, PlanModePlanning, "write_file",
		map[string]any{"file_path": "/tmp/x.ts"}); !isErr {
		t.Fatal("阳性对照失败：planning 态应拦 write_file")
	}
	for _, name := range []string{"read_file", "grep", "glob", "run_tests", "todo"} {
		content, _ := planModeProbe(t, PlanModePlanning, name, map[string]any{})
		if isPlanModeBlock(content) {
			t.Errorf("planning 态下白名单工具 %s 不该被拦，实得 %q", name, content)
		}
	}
}

// TestPlanModeAllowsActivePlanFile —— 写**活动计划文件**在 planning 态放行。
//
// 对账 TS：`planModePathsMatch(cwd, target, activePlanFilePath)` 的例外。
func TestPlanModeAllowsActivePlanFile(t *testing.T) {
	dir := t.TempDir()
	planPath := dir + "/.rivet/plans/my-plan.md"

	l := &Loop{}
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.PlanModeState = PlanModePlanning
	l.ActivePlanFilePath = planPath
	l.State = session.New("planmode-wiring-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()

	res := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": planPath},
	})
	if isPlanModeBlock(res.Content) {
		t.Errorf("写活动计划文件应放行（否则无法写计划），实得 %q", res.Content)
	}
}

// TestPlanModeAllowsScratchProbe —— scratch 探针在 planning 态放行。
//
// 对账 TS：`planModeIsUnderScratchDir` 的 A5 例外（计划期验证声称的标准出路）。
func TestPlanModeAllowsScratchProbe(t *testing.T) {
	dir := t.TempDir()
	l := &Loop{}
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.PlanModeState = PlanModePlanning
	l.State = session.New("planmode-wiring-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()

	res := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": dir + "/.rivet/scratch/probe.ts"},
	})
	if isPlanModeBlock(res.Content) {
		t.Errorf("scratch 探针应放行，实得 %q", res.Content)
	}
}

// TestPlanModeEndToEndEnterBlocksExitUnblocks —— **端到端闭环**（本刀核心）。
//
// 走真装配路径：`plan` 工具 enter_mode → `Loop.enterPlanMode`（真建草稿）
// → 门链拦截写工具 → `plan` 工具 exit_mode → 写工具恢复放行。
//
// 这是「机制真的接上了」的正面证据——单测 `CheckPlanMode` 绿不等于接线绿。
func TestPlanModeEndToEndEnterBlocksExitUnblocks(t *testing.T) {
	dir := t.TempDir()
	l := &Loop{}
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("planmode-e2e")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	l.registry.Register(tools.Plan())
	l.registry.Register(tools.WriteFile(dir, nil))

	// ① 进入前：写工具**不被 plan 门拦**（走到空 registry 之外的路径）
	before := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": dir + "/a.txt", "content": "x"},
	})
	if isPlanModeBlock(before.Content) {
		t.Fatalf("进入前不该被 plan 门拦，实得 %q", before.Content)
	}

	// ② enter_mode（经真工具 + 真回调）
	enter := l.executeTool(context.Background(), toolCall{
		name:  "plan",
		input: map[string]any{"action": "enter_mode"},
	})
	if enter.IsError {
		t.Fatalf("enter_mode 应成功，实得：%s", enter.Content)
	}
	if l.PlanModeState != PlanModePlanning {
		t.Fatalf("enter_mode 后状态应为 planning，实得 %q", l.PlanModeState)
	}
	if l.ActivePlanFilePath == "" {
		t.Fatal("enter_mode 后应有活动计划文件")
	}
	// 草稿文件应真被创建
	if _, err := os.Stat(filepath.Join(dir, l.ActivePlanFilePath)); err != nil {
		t.Errorf("草稿文件应已创建：%v", err)
	}

	// ③ 进入后：写普通文件**被拦**
	blocked := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": dir + "/b.txt", "content": "x"},
	})
	if !isPlanModeBlock(blocked.Content) {
		t.Errorf("planning 态下写普通文件应被拦，实得 %q", blocked.Content)
	}

	// ④ 写**活动计划文件**放行
	planWrite := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": filepath.Join(dir, l.ActivePlanFilePath), "content": "# plan"},
	})
	if isPlanModeBlock(planWrite.Content) {
		t.Errorf("写活动计划文件应放行，实得 %q", planWrite.Content)
	}

	// ⑤ exit_mode
	exit := l.executeTool(context.Background(), toolCall{
		name:  "plan",
		input: map[string]any{"action": "exit_mode"},
	})
	if exit.IsError {
		t.Fatalf("exit_mode 应成功，实得：%s", exit.Content)
	}
	if l.PlanModeState != PlanModeOff {
		t.Errorf("exit_mode 后状态应为 off，实得 %q", l.PlanModeState)
	}

	// ⑥ 退出后：写工具恢复放行
	after := l.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": dir + "/c.txt", "content": "x"},
	})
	if isPlanModeBlock(after.Content) {
		t.Errorf("exit 后写工具应放行，实得 %q", after.Content)
	}
}
