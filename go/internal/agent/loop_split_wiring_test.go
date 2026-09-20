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
	var sawSplit bool
	for _, e := range events {
		if strings.Contains(e.Text, "会话切分判定触发") {
			sawSplit = true
			if !strings.Contains(e.Text, "执行层未移植") {
				t.Errorf("事件应如实说明执行层未移植，实得 %q", e.Text)
			}
		}
	}
	if !sawSplit {
		t.Errorf("应发出切分判定事件（不静默），实得事件 %+v", events)
	}
}

// TestMaybeCompactAtBoundary_SplitDoesNotReplaceHistory —— **不谎称已切分**。
//
// 执行层（`replaceWithCheckpoint`）未移植——故判定触发时**不得**改动
// `l.messages`。这条锁定诚实性：不假装完成了它没做的事。
func TestMaybeCompactAtBoundary_SplitDoesNotReplaceHistory(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(950_000)),
	}
	before := len(l.messages)
	beforeFirst := l.messages[0].Keys()

	l.maybeCompactAtBoundary(0)

	if !l.Compact.LastSplitDecision.ShouldSplit {
		t.Fatal("本用例需要 split 判定触发")
	}
	if len(l.messages) != before {
		t.Errorf("执行层未移植时不应改动历史长度：%d → %d", before, len(l.messages))
	}
	if len(l.messages) > 0 && len(l.messages[0].Keys()) != len(beforeFirst) {
		t.Error("不应改动首条消息的键集")
	}
}

// TestMaybeCompactAtBoundary_SplitDoesNotBlockCompact —— split 判定不阻断常规压缩。
//
// 判定触发但**未执行**——若因此 return，上下文压力会完全无人处理。
// 故 split 之后仍应尝试 maybeCompact（TS 是 split 成功才 userMessageConsumed=true；
// Go 侧执行层未移植 = 未成功，故不该阻断）。
func TestMaybeCompactAtBoundary_SplitDoesNotBlockCompact(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(950_000)),
	}
	l.Emit = func(Event) {}

	l.maybeCompactAtBoundary(0)

	// 常规压缩的决策也应发生（证明没被 split 的 return 阻断）。
	if l.Compact.LastDecision == nil {
		t.Error("split 判定不应阻断常规压缩（LastDecision 为 nil）")
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
