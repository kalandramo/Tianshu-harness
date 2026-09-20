package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

func cpMsg(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}

func cpContent(m session.OaiMessage) string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// TestCheckpointPreservesAnchor —— 前 2 条锚必须逐字节保留（前缀缓存前提）。
func TestCheckpointPreservesAnchor(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYSTEM-PROMPT"),
		cpMsg("user", "first-user"),
		cpMsg("assistant", "old-1"),
		cpMsg("user", "old-2"),
		cpMsg("assistant", "old-3"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{})

	if !out.Committed {
		t.Fatalf("force 应提交，实得 reason=%q", out.Reason)
	}
	if len(out.Messages) < 2 {
		t.Fatalf("替换后至少应有锚，实得 %d 条", len(out.Messages))
	}
	if cpContent(out.Messages[0]) != "SYSTEM-PROMPT" || cpContent(out.Messages[1]) != "first-user" {
		t.Errorf("前 2 条锚必须逐字节保留，实得 %q / %q",
			cpContent(out.Messages[0]), cpContent(out.Messages[1]))
	}
	// 摘要应在锚之后。
	if !strings.Contains(cpContent(out.Messages[2]), "SUMMARY") {
		t.Errorf("第 3 条应是摘要，实得 %q", cpContent(out.Messages[2]))
	}
}

// TestCheckpointPreservesFreshUserTail —— **核心不变量**：末尾未消费 user 原文保留。
//
// 对账 TS 的 tailIsFreshUser 保护：末尾 user 是用户刚发的指令，整体归档会让
// 模型下一请求看到压缩版指令（用户观感 = 消息被截断）。
func TestCheckpointPreservesFreshUserTail(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "mid-1"),
		cpMsg("user", "mid-2"),
		cpMsg("assistant", "mid-3"),
		cpMsg("user", "FRESH-INSTRUCTION"), // 末尾 user = 未消费
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{})

	if !out.Committed {
		t.Fatal("应提交")
	}
	last := out.Messages[len(out.Messages)-1]
	if cpContent(last) != "FRESH-INSTRUCTION" {
		t.Errorf("末尾 user 原文必须保留，实得 %q（完整历史：%+v）", cpContent(last), out.Messages)
	}
	// 摘要角色应为 assistant（避免中间出现第二条 user）。
	var summaryMsg *session.OaiMessage
	for i, m := range out.Messages {
		if strings.Contains(cpContent(m), "SUMMARY") {
			summaryMsg = &out.Messages[i]
			break
		}
	}
	if summaryMsg == nil {
		t.Fatal("应含摘要消息")
	}
	if summaryMsg.Role != "assistant" {
		t.Errorf("有尾随 user 时摘要角色应为 assistant（防 volatileBlock 双份注入），实得 %q", summaryMsg.Role)
	}
}

// TestCheckpointNoTailUsesUserRole —— 无尾随 user 时摘要用 user 角色（旧形态）。
func TestCheckpointNoTailUsesUserRole(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "mid-1"),
		cpMsg("assistant", "mid-2"), // 末尾是 assistant（turn 内工具循环）
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{})

	var summaryMsg *session.OaiMessage
	for i, m := range out.Messages {
		if strings.Contains(cpContent(m), "SUMMARY") {
			summaryMsg = &out.Messages[i]
			break
		}
	}
	if summaryMsg == nil {
		t.Fatal("应含摘要消息")
	}
	if summaryMsg.Role != "user" {
		t.Errorf("无尾随 user 时摘要角色应为 user，实得 %q", summaryMsg.Role)
	}
}

// TestCheckpointFallbackWhenTooLarge —— 候选超 MaxFallback 时换兜底摘要。
func TestCheckpointFallbackWhenTooLarge(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", strings.Repeat("x", 10000)),
	}
	// MaxFallback 设得很小 → 主摘要必超 → 换兜底。
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary:      strings.Repeat("LONG", 5000),
		FallbackText: "FALLBACK",
		MaxFallback:  10,
		Action:       compact.ActionSessionSplit,
		Force:        true,
	}, CheckpointDeps{})

	joined := ""
	for _, m := range out.Messages {
		joined += cpContent(m)
	}
	if !strings.Contains(joined, "FALLBACK") {
		t.Errorf("超 MaxFallback 时应换兜底摘要，实得：%s", joined[:minInt(200, len(joined))])
	}
}

// TestCheckpointReclaimGateRejects —— reclaim gate 拒绝时不替换历史。
func TestCheckpointReclaimGateRejects(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	// 候选与原文几乎等长（没有真正回收）→ gate 应拒绝。
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "x"),
	}
	// 不传 Force，且摘要很大 → 回收为负/不足 → 拒绝。
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: strings.Repeat("y", 100000),
		Action:  compact.ActionMicro,
		Force:   false,
	}, CheckpointDeps{})

	if out.Committed {
		t.Error("回收不足时 gate 应拒绝提交")
	}
	if len(out.Messages) != len(messages) {
		t.Error("拒绝时不得改动历史")
	}
	if out.Reason == "" {
		t.Error("拒绝应给出 reason")
	}
}

// TestCheckpointCallsArchiveAndAnchor —— 注入依赖被调用且结果进入候选。
func TestCheckpointCallsArchiveAndAnchor(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "discarded-1"),
		cpMsg("user", "discarded-2"),
		cpMsg("assistant", "discarded-3"),
	}
	var archiveCalled bool
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{
		ArchiveDiscarded: func(discarded []session.OaiMessage, reason string) string {
			archiveCalled = true
			if len(discarded) == 0 {
				t.Error("丢弃段不应为空")
			}
			return "\n[ARCHIVE-REF]"
		},
		TaskAnchor: func() string { return "TASK-ANCHOR-TEXT" },
	})

	if !archiveCalled {
		t.Error("ArchiveDiscarded 应被调用")
	}
	joined := ""
	for _, m := range out.Messages {
		joined += cpContent(m)
	}
	if !strings.Contains(joined, "ARCHIVE-REF") {
		t.Error("归档引用应拼进摘要")
	}
	// 任务锚应在末尾（appendix 区）。
	if cpContent(out.Messages[len(out.Messages)-1]) != "TASK-ANCHOR-TEXT" {
		t.Errorf("任务锚应在末尾，实得 %q", cpContent(out.Messages[len(out.Messages)-1]))
	}
}

// TestCheckpointPreflightApplied —— preflight 注入会被应用。
func TestCheckpointPreflightApplied(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"), cpMsg("user", "first"),
		cpMsg("assistant", "d1"), cpMsg("user", "d2"), cpMsg("assistant", "d3"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{
		Preflight: func(msgs []session.OaiMessage) []session.OaiMessage {
			return append(msgs, cpMsg("user", "PREFLIGHT-ADDED"))
		},
	})
	if cpContent(out.Messages[len(out.Messages)-1]) != "PREFLIGHT-ADDED" {
		t.Error("preflight 结果应被应用")
	}
}

// TestCheckpointDecisionRecorded —— 决策被记录（供观测/测试断言）。
func TestCheckpointDecisionRecorded(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"), cpMsg("user", "first"),
		cpMsg("assistant", strings.Repeat("x", 5000)),
	}
	var got *compact.ReclaimDecisionRecord
	b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "S", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{OnReclaimDecision: func(d compact.ReclaimDecisionRecord) { got = &d }})

	if got == nil {
		t.Fatal("OnReclaimDecision 应被调用")
	}
	if b.LastReclaimDecision == nil {
		t.Error("LastReclaimDecision 应被记录")
	}
}

// TestCheckpointShortHistorySafe —— 历史短于锚数时不 panic。
func TestCheckpointShortHistorySafe(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	for _, msgs := range [][]session.OaiMessage{
		nil,
		{cpMsg("user", "only")},
		{cpMsg("system", "s"), cpMsg("user", "u")},
	} {
		out := b.ReplaceWithCheckpoint(msgs, CheckpointParams{
			Summary: "S", Action: compact.ActionSessionSplit, Force: true,
		}, CheckpointDeps{})
		if !out.Committed {
			t.Errorf("短历史（%d 条）force 应提交，实得 reason=%q", len(msgs), out.Reason)
		}
	}
}

// TestCheckpointTailAtIndexAnchorBoundary —— 边界：末尾 user 恰在 index==anchorCount。
//
// 对账 TS 的 `messages.length - 1 >= CACHE_ANCHOR_MESSAGES`。
func TestCheckpointTailAtIndexAnchorBoundary(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	// 3 条：index 0,1 是锚，index 2 是 user（== anchorCount=2）→ 应命中保护。
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("user", "TAIL-USER"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "S", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{})
	last := out.Messages[len(out.Messages)-1]
	if cpContent(last) != "TAIL-USER" {
		t.Errorf("index==anchorCount 的末尾 user 应受保护，实得 %q（历史：%+v）", cpContent(last), out.Messages)
	}
}
