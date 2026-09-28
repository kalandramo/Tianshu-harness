package agent

import (
	"strings"
	"testing"
)

// appendix_planmode_test.go —— 第一百一十一刀 W2：接线 plan mode 块。
//
// # 缺口
//
// `prompt/modeblocks.go` 的 `RenderPlanModeBlock` / `RenderPlanExitReminder`
// **已完整移植**且带逐字节 oracle，但**生产代码零调用**——模型在 plan mode 下
// 完全不知道自己在计划模式（门链在拦写，模型却在按普通模式作答）。
//
// # TS 对账（执行期解 H1 所得，`src/prompt/volatile.ts:824-841`）
//
//	if (ctx.planModeState === 'planning') {
//	  push(renderPlanModeBlock(ctx.activePlanFilePath))
//	} else if (ctx.planExitReminderPending) {
//	  push(renderPlanExitReminder())
//	}
//
// 两条要点：① **if / else if 互斥**（planning 优先）；② 块序在 terseness 之前。

// TestAppendixPlanModeBlockWhenPlanning —— ★ V4：planning 态产出 `<plan-mode>`。
func TestAppendixPlanModeBlockWhenPlanning(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState: PlanModePlanning,
	})
	if !strings.Contains(got, "<plan-mode>") {
		t.Errorf("★ planning 态应产出 <plan-mode> 块（这正是本刀要修的缺口），实得：%q",
			truncateRunes(got, 120))
	}
	if !strings.Contains(got, "你处于规划模式") {
		t.Error("<plan-mode> 块内容不符（应为 RenderPlanModeBlock 的输出）")
	}
}

// TestAppendixNoPlanBlockWhenOff —— ★ V5：off 态**不产出** plan 块。
//
// 与上一条配对——只测「planning 出块」会被一个「恒出块」的实现蒙混过关，
// 那会让**每个**普通轮次都背上 3200 字符的 plan-mode 指令（前缀缓存灾难）。
func TestAppendixNoPlanBlockWhenOff(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{PlanModeState: PlanModeOff})
	if strings.Contains(got, "<plan-mode>") {
		t.Errorf("★ off 态不该产出 plan 块，实得：%q", truncateRunes(got, 120))
	}
	if got != "" {
		t.Errorf("off 态且无其他块时应为空串，实得：%q", truncateRunes(got, 120))
	}
}

// TestAppendixPlanModeBlockCarriesActivePath —— ★ V6：块内含活动计划文件行。
func TestAppendixPlanModeBlockCarriesActivePath(t *testing.T) {
	path := ".rivet/plans/draft-123.md"
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState:      PlanModePlanning,
		ActivePlanFilePath: path,
	})
	if !strings.Contains(got, "活动计划文件: `"+path+"`") {
		t.Errorf("块内应含活动计划文件行。实得：%q", truncateRunes(got, 200))
	}

	// 反向：空路径时**不含**该行（对账 A11 的 truthy 语义）
	got2 := BuildDynamicAppendix(AppendixContext{PlanModeState: PlanModePlanning})
	if strings.Contains(got2, "活动计划文件:") {
		t.Error("无活动计划文件时不该含「活动计划文件」行")
	}
}

// TestAppendixPlanExitReminderWhenPending —— ★ V7 前半：pending 态产出 exit 提示。
func TestAppendixPlanExitReminderWhenPending(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState:           PlanModeOff,
		PlanExitReminderPending: true,
	})
	if !strings.Contains(got, "<plan-mode-exit>") {
		t.Errorf("pending 态应产出 <plan-mode-exit>，实得：%q", truncateRunes(got, 120))
	}
}

// TestAppendixPlanModeWinsOverExitReminder —— ★ H1 的互斥语义。
//
// TS 是 `if planning { plan } else if pending { exit }`——**planning 优先**。
// 本用例钉住这个优先级（若实现写成两个独立 if，两者会同时出现）。
func TestAppendixPlanModeWinsOverExitReminder(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState:           PlanModePlanning,
		PlanExitReminderPending: true,
	})
	if !strings.Contains(got, "<plan-mode>") {
		t.Fatal("planning 态应出 plan 块")
	}
	if strings.Contains(got, "<plan-mode-exit>") {
		t.Error("★ planning 与 pending 同时成立时只應出 plan 块（对账 volatile.ts 的 if/else if）")
	}
}

// TestAppendixPlanBlockBeforeTerseness —— ★ H1 的块序。
//
// 对账 `volatile.ts`：plan 块（`:824`）在 terseness（`:838`）**之前**。
func TestAppendixPlanBlockBeforeTerseness(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState: PlanModePlanning,
		TerseEnv:      "1",
	})
	planPos := strings.Index(got, "<plan-mode>")
	stylePos := strings.Index(got, "<output-style>")
	if planPos < 0 || stylePos < 0 {
		t.Fatalf("前置失败：两块应都产出。got=%q", truncateRunes(got, 150))
	}
	if planPos > stylePos {
		t.Errorf("★ plan 块应在 terseness 之前（对账 volatile.ts:824 vs :838）："+
			"planPos=%d stylePos=%d", planPos, stylePos)
	}
}

// TestAppendixAskModeNeverWired —— V8：Go 侧恒不含 `<ask-mode>`。
//
// **为什么要有这条**：`RenderAskModeBlock` 也已移植但 Go 侧**无 ask mode
// 状态载体**（grep 零命中），故本刀不接它。显式钉住「不出现」，
// 以免将来有人误以为本刀漏了它（或反过来凭空造一个 ask 状态）。
func TestAppendixAskModeNeverWired(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		PlanModeState: PlanModePlanning,
		TerseEnv:      "1",
	})
	if strings.Contains(got, "<ask-mode>") {
		t.Error("Go 侧无 ask mode 状态载体，不该产出 <ask-mode>——" +
			"若将来接了 ask 模式机，须同时更新本用例与计划的范围说明")
	}
}
