package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// 这些测试锁定**接线契约**：`ArchiveDiscarded` 必须真的在替换路径上被调用，
// 且其结果（召回引用）进入摘要——否则它是悬空依赖。

// TestCheckpointArchiveRefInSummary —— 归档引用被拼进摘要。
func TestCheckpointArchiveRefInSummary(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "discarded-1"),
		cpMsg("user", "discarded-2"),
		cpMsg("assistant", "discarded-3"),
	}
	var seenDiscarded int
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{
		ArchiveDiscarded: func(discarded []session.OaiMessage, reason string) string {
			seenDiscarded = len(discarded)
			return "\n[ARCHIVE-REF-MARKER]"
		},
	})

	if !out.Committed {
		t.Fatal("应提交")
	}
	if seenDiscarded == 0 {
		t.Fatal("归档函数应收到非空丢弃段")
	}
	joined := ""
	for _, m := range out.Messages {
		if m.Content != nil {
			joined += *m.Content
		}
	}
	if !strings.Contains(joined, "ARCHIVE-REF-MARKER") {
		t.Errorf("归档引用应进摘要，实得：%s", joined)
	}
}

// TestCheckpointArchiveEmptyRefNotAppended —— 归档返回空串时不追加（fail-soft）。
func TestCheckpointArchiveEmptyRefNotAppended(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"), cpMsg("user", "first"),
		cpMsg("assistant", "d1"), cpMsg("user", "d2"), cpMsg("assistant", "d3"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "CLEAN-SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{
		ArchiveDiscarded: func([]session.OaiMessage, string) string { return "" },
	})
	joined := ""
	for _, m := range out.Messages {
		if m.Content != nil {
			joined += *m.Content
		}
	}
	// 摘要应保持原样（无多余后缀）。
	if !strings.Contains(joined, "CLEAN-SUMMARY") {
		t.Error("摘要应保留")
	}
	if strings.Contains(joined, "\n\n\n") {
		t.Error("空引用不应留下多余空行")
	}
}

// TestArchiveEndToEndPersistsAndRecallable —— **端到端**：丢弃段真的落盘且可召回。
//
// 完整链路：丢弃段 → 序列化 → 存 artifact → 引用进摘要 → read_section 取回。
func TestArchiveEndToEndPersistsAndRecallable(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "arch" },
	})

	archive := context.BuildArchiveDiscarded(
		func(in artifact.SaveInput) (string, error) { return store.Save(in) },
		func() int { return 5 },
		nil,
	)

	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "UNIQUE-DISCARDED-CONTENT"),
		cpMsg("user", "more"),
		cpMsg("assistant", "tail"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{ArchiveDiscarded: archive})

	if !out.Committed {
		t.Fatal("应提交")
	}

	// 1) 引用块含 artifact id 与召回指引。
	joined := ""
	for _, m := range out.Messages {
		if m.Content != nil {
			joined += *m.Content
		}
	}
	if !strings.Contains(joined, "已归档") || !strings.Contains(joined, "read_section") {
		t.Fatalf("摘要应含归档引用与召回指引，实得：%s", joined)
	}

	// 2) store 里真的有归档 artifact。
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("应有 1 条归档 artifact，实得 %d", len(list))
	}
	if list[0].Tool != context.CompactHistoryTool {
		t.Errorf("工具名应为 %q，实得 %q", context.CompactHistoryTool, list[0].Tool)
	}
	if list[0].Target != "session-history@turn5" {
		t.Errorf("target 不符：%q", list[0].Target)
	}

	// 3) 原文可逐字取回（含被丢内容）。
	raw, err := store.ReadRaw(list[0].ID)
	if err != nil {
		t.Fatalf("ReadRaw 失败：%v", err)
	}
	if !strings.Contains(raw, "UNIQUE-DISCARDED-CONTENT") {
		t.Errorf("归档原文应含被丢内容，实得：%s", raw[:minI(200, len(raw))])
	}
	// 4) divider 格式正确（read_section 靠它定位）。
	if !strings.Contains(raw, "--- turn:0 role:user ---") {
		t.Errorf("归档应含 divider，实得：%s", raw[:minI(200, len(raw))])
	}
}

// TestArchiveTailUserNotArchived —— 尾随未消费 user **不进归档**（对账 TS）。
func TestArchiveTailUserNotArchived(t *testing.T) {
	store := artifact.NewStore(t.TempDir(), "s1", artifact.Options{
		Now: func() int64 { return 1 }, IDGenerator: func() string { return "a" },
	})
	archive := context.BuildArchiveDiscarded(
		func(in artifact.SaveInput) (string, error) { return store.Save(in) },
		func() int { return 1 }, nil,
	)
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "mid"),
		cpMsg("user", "FRESH-TAIL-INSTRUCTION"), // 末尾未消费 user
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "S", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{ArchiveDiscarded: archive})

	if !out.Committed {
		t.Fatal("应提交")
	}
	// 归档原文里**不应**含尾随 user 的原文（它在 archiveEnd 之前就被排除了）。
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("应有 1 条归档，实得 %d", len(list))
	}
	raw, _ := store.ReadRaw(list[0].ID)
	if strings.Contains(raw, "FRESH-TAIL-INSTRUCTION") {
		t.Error("尾随未消费 user 不应进归档（原文留在上下文）")
	}
	// 且它仍在历史里。
	joined := ""
	for _, m := range out.Messages {
		if m.Content != nil {
			joined += *m.Content
		}
	}
	if !strings.Contains(joined, "FRESH-TAIL-INSTRUCTION") {
		t.Error("尾随 user 原文应保留在历史")
	}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
