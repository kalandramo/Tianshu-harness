package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// splitMsgs 构造约 tokens 个 token 的历史（4 字符 ≈ 1 token）。
func splitMsgs(tokens int) []session.OaiMessage {
	n := tokens / 1000
	if n < 1 {
		n = 1
	}
	s := strings.Repeat("x", (tokens*4)/n)
	msgs := make([]session.OaiMessage, n)
	for i := range msgs {
		c := s
		msgs[i] = session.OaiMessage{Role: "user", Content: &c}
	}
	return msgs
}

// TestCompactBoundary_TrySessionSplitTriggers —— **接线集成测试**。
//
// 验证 split 判定在真实边界上生效：大窗口 + 高占用 → 触发，且产出候选 handoff。
func TestCompactBoundary_TrySessionSplitTriggers(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	out := b.TrySessionSplit(splitMsgs(950_000))

	if !out.SplitTriggered() {
		t.Fatalf("95%% 占用 + 1M 窗口应触发 split（reason=%q ratio=%.3f）",
			out.Decision.Reason, out.Decision.Ratio)
	}
	if out.Handoff == "" {
		t.Fatal("触发时应产出候选 handoff")
	}
	if !strings.Contains(out.Handoff, "<session-handoff>") {
		t.Errorf("handoff 应被 <session-handoff> 包裹，实得 %q", out.Handoff)
	}
	if b.LastSplitDecision == nil || !b.LastSplitDecision.ShouldSplit {
		t.Error("判定应被记录（LastSplitDecision）")
	}
}

// TestCompactBoundary_TrySessionSplitWindowGate —— 窗口门槛优先于比例。
//
// **关键语义**：`small_window` 的 ratio 远超 0.86 但窗口 < 500K → 不 split。
// 这条证明窗口门槛**不是**「比例够就行」。
func TestCompactBoundary_TrySessionSplitWindowGate(t *testing.T) {
	b := NewCompactBoundary(128_000)
	out := b.TrySessionSplit(splitMsgs(200_000)) // ratio ≈ 1.56

	if out.SplitTriggered() {
		t.Errorf("小窗口不应 split（ratio=%.3f）", out.Decision.Ratio)
	}
	if out.Decision.Reason != compact.SplitWindowTooSmall {
		t.Errorf("reason 应为 %q，实得 %q", compact.SplitWindowTooSmall, out.Decision.Reason)
	}
	if out.Handoff != "" {
		t.Error("未触发时不应产出 handoff")
	}
}

// TestCompactBoundary_TrySessionSplitRatioGate —— 比例门槛。
func TestCompactBoundary_TrySessionSplitRatioGate(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	out := b.TrySessionSplit(splitMsgs(500_000)) // ratio = 0.5

	if out.SplitTriggered() {
		t.Errorf("50%% 占用不应 split（ratio=%.3f）", out.Decision.Ratio)
	}
	if out.Decision.Reason != compact.SplitBelowRatio {
		t.Errorf("reason 应为 %q，实得 %q", compact.SplitBelowRatio, out.Decision.Reason)
	}
}

// TestCompactBoundary_TrySessionSplitDoesNotMutateMessages —— **不半套用**。
//
// 本实现只判定 + 构造候选，**不改消息列表**——执行层（replaceWithCheckpoint）
// 未移植，直接改会让会话处于中间态。这条锁定该契约。
func TestCompactBoundary_TrySessionSplitDoesNotMutateMessages(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	msgs := splitMsgs(950_000)
	before := len(msgs)
	beforeFirst := *msgs[0].Content

	out := b.TrySessionSplit(msgs)

	if !out.SplitTriggered() {
		t.Fatal("本用例需要触发 split")
	}
	if len(msgs) != before {
		t.Errorf("不应改动消息列表长度：%d → %d", before, len(msgs))
	}
	if *msgs[0].Content != beforeFirst {
		t.Error("不应改动消息内容")
	}
}

// TestCompactBoundary_TrySessionSplitRecordsEvenWhenNotTriggered —— 可观测性契约。
//
// 未触发时**也要记录**——离线才能看出「为何没 split」（窗口太小 / 比例不足）。
func TestCompactBoundary_TrySessionSplitRecordsEvenWhenNotTriggered(t *testing.T) {
	b := NewCompactBoundary(128_000)
	b.TrySessionSplit(splitMsgs(200_000))

	if b.LastSplitDecision == nil {
		t.Fatal("未触发时也应记录判定")
	}
	if b.LastSplitDecision.ShouldSplit {
		t.Error("记录应反映未触发")
	}
	if b.LastSplitDecision.Reason == "" {
		t.Error("记录应带 reason")
	}
}
