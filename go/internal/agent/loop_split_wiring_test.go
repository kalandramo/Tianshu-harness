package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/compact"
)

// TestMaybeCompactAtBoundary_SessionSplitHasProductionCaller —— **消费方回归**。
//
// 这条测试的存在理由：`TrySessionSplit` 首版**没有生产调用方**（只有定义与
// 测试引用）——是项目纪律警告的 `type-without-consumer`（悬空代码）。本测试
// 锁定「loop 的压缩边界真的会调它」。
//
// 对账 TS `runCompaction`（compact-boundary-coordinator.ts:124）：先
// `trySessionSplit()`，再 `maybeCompact(...)`。
func TestMaybeCompactAtBoundary_SessionSplitHasProductionCaller(t *testing.T) {
	// 1M 窗口 + 95% 占用 → split 判定必触发。
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(950_000)),
	}
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	l.maybeCompactAtBoundary(0)

	// 判定必须被记录（证明调用真的发生）。
	if l.Compact.LastSplitDecision == nil {
		t.Fatal("TrySessionSplit 未被调用（LastSplitDecision 为 nil）——悬空回归")
	}
	if !l.Compact.LastSplitDecision.ShouldSplit {
		t.Fatalf("95%% 占用应触发 split，实得 reason=%q", l.Compact.LastSplitDecision.Reason)
	}

	// 必须发出可见事件——**不静默**。
	//
	// **行为变更**：首版断言事件文案含「执行层未移植」（当时只判定不执行）。
	// `replaceWithCheckpoint` 落地后事件改为「会话切分执行」+ 替换结果。
	var sawSplit bool
	for _, e := range events {
		if strings.Contains(e.Text, "会话切分执行") {
			sawSplit = true
			// 事件应如实报告替换结果（条数 + 回收 token）。
			if !strings.Contains(e.Text, "历史替换为") || !strings.Contains(e.Text, "回收") {
				t.Errorf("事件应如实报告替换结果，实得 %q", e.Text)
			}
		}
	}
	if !sawSplit {
		t.Errorf("应发出切分执行事件（不静默），实得事件 %+v", events)
	}
}

// （`TestMaybeCompactAtBoundary_SplitDoesNotReplaceHistory` 已移除：
// 它断言的是「执行层未移植」时期的降级行为。`replaceWithCheckpoint` 落地后
// 该断言与事实相反——由下面的 `_SplitReplacesHistory` 取代，断言方向反转。）

// TestMaybeCompactAtBoundary_SplitReplacesHistory —— **执行层已接**。
//
// 首版这条测试断言「执行层未移植时不应改动历史长度」——那是当时的**有意
// 降级**（判定 + 候选，不替换）。`replaceWithCheckpoint` 落地后行为反转：
// 判定通过 → **真的替换历史**。这条测试随之反转断言，记录这次行为变更。
func TestMaybeCompactAtBoundary_SplitReplacesHistory(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(950_000)),
	}
	before := len(l.messages)
	l.Emit = func(Event) {}

	l.maybeCompactAtBoundary(0)

	if !l.Compact.LastSplitDecision.ShouldSplit {
		t.Fatal("本用例需要 split 判定触发")
	}
	// 历史应被替换（anchor + 摘要，远短于原 950 条）。
	if len(l.messages) >= before {
		t.Errorf("执行层已接——历史应被替换为更短形态：%d → %d", before, len(l.messages))
	}
	if len(l.messages) == 0 {
		t.Error("替换后不应为空")
	}
}

// TestMaybeCompactAtBoundary_SplitDoesNotBlockCompact —— split **未提交**时不阻断常规压缩。
//
// **行为变更说明**：首版断言「split 判定不阻断压缩」——当时 split 不替换
// 历史，故必须继续压缩。现在 split **提交后**会 return（历史已是新形态，
// 无需再压）；仅当 gate 拒绝时才继续走常规压缩。
//
// 本测试改为验证**未触发 split 的场景**下常规压缩照常发生（守住「不阻断」
// 的原意）。
func TestMaybeCompactAtBoundary_SplitDoesNotBlockCompact(t *testing.T) {
	// 10% 占用 → split 不触发 → 常规压缩路径应正常走。
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(100_000)),
	}
	l.Emit = func(Event) {}

	l.maybeCompactAtBoundary(0)

	// 常规压缩的决策也应发生。
	if l.Compact.LastDecision == nil {
		t.Error("未触发 split 时常规压缩应照常决策（LastDecision 为 nil）")
	}
}

// TestMaybeCompactAtBoundary_NoSplitUnderThreshold —— 未达门槛时不触发。
func TestMaybeCompactAtBoundary_NoSplitUnderThreshold(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(100_000)), // 10% 占用
	}
	l.Emit = func(Event) {}

	l.maybeCompactAtBoundary(0)

	if l.Compact.LastSplitDecision == nil {
		t.Fatal("判定应被记录（即使未触发）")
	}
	if l.Compact.LastSplitDecision.ShouldSplit {
		t.Errorf("10%% 占用不应触发 split（reason=%q）", l.Compact.LastSplitDecision.Reason)
	}
	if l.Compact.LastSplitDecision.Reason != compact.SplitBelowRatio {
		t.Errorf("reason 应为 %q，实得 %q", compact.SplitBelowRatio, l.Compact.LastSplitDecision.Reason)
	}
}

// TestMaybeCompactAtBoundary_NilCompactSafe —— nil Compact 不应 panic。
func TestMaybeCompactAtBoundary_NilCompactSafe(t *testing.T) {
	l := &Loop{messages: nil}
	l.Emit = func(Event) {}
	l.maybeCompactAtBoundary(0) // 不应 panic
}
