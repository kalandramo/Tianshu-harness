package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// TestSessionSplitEndToEndReplacesHistory —— **端到端验收**。
//
// 这是自 `1b3a552`（session split 判定层）起一直 blocked 的验收面：
// 「1M 窗口 + 86% 占用 → 会话历史真的被切分为 handoff」。
//
// 前置（task-state / trajectory / artifact store / replaceWithCheckpoint）
// 现已全部就位，这条验收面应当转 met。
func TestSessionSplitEndToEndReplacesHistory(t *testing.T) {
	l := &Loop{
		Compact: NewCompactBoundary(1_000_000),
	}
	l.Emit = func(Event) {}

	// 造 95% 占用的历史（含可辨识的锚）。
	history := splitMsgs(950_000)
	// 手工设置前两条为可辨识内容（验证锚保留）。
	first := "SYSTEM-ANCHOR"
	second := "USER-ANCHOR"
	history[0] = session.OaiMessage{Role: "system", Content: &first}
	history[1] = session.OaiMessage{Role: "user", Content: &second}
	l.messages = oaiToOrderedMaps(history)
	before := len(l.messages)

	// 触发 turn 边界的压缩检查。
	l.maybeCompactAtBoundary(0)

	// 1) 判定确实触发过。
	if l.Compact.LastSplitDecision == nil {
		t.Fatal("split 判定应被记录")
	}
	if !l.Compact.LastSplitDecision.ShouldSplit {
		t.Fatalf("95%% 占用应触发 split，实得 reason=%q", l.Compact.LastSplitDecision.Reason)
	}

	// 2) **历史真的被替换了**（这是本验收的核心）。
	after := len(l.messages)
	if after >= before {
		t.Fatalf("历史应被替换为更短形态：%d → %d", before, after)
	}
	if after == 0 {
		t.Fatal("替换后不应为空")
	}

	// 3) 前两条锚逐字节保留（前缀缓存前提）。
	gotFirst, _ := l.messages[0].Get("content")
	gotSecond, _ := l.messages[1].Get("content")
	if gotFirst != "SYSTEM-ANCHOR" || gotSecond != "USER-ANCHOR" {
		t.Errorf("前 2 条锚必须保留，实得 %v / %v", gotFirst, gotSecond)
	}

	// 4) 替换后的历史含 handoff 内容（9 章节标记）。
	joined := ""
	for _, m := range l.messages {
		if v, ok := m.Get("content"); ok {
			if c, isStr := v.(string); isStr {
				joined += c
			}
		}
	}
	if !strings.Contains(joined, "<session-handoff>") {
		t.Errorf("替换后的历史应含 session handoff，实得：%s", joined[:minInt(300, len(joined))])
	}

	// 5) reclaim 决策被记录。
	if l.Compact.LastReclaimDecision == nil {
		t.Error("reclaim 决策应被记录")
	} else if !l.Compact.LastReclaimDecision.Commit {
		t.Error("force=true 的 split 应提交")
	}
}

// TestSessionSplitEndToEndPersists —— 替换后落盘（OnReplace 真的写了）。
func TestSessionSplitEndToEndPersists(t *testing.T) {
	p, err := session.NewPersist("e2e-split", t.TempDir())
	if err != nil {
		t.Fatalf("NewPersist 失败：%v", err)
	}
	t.Cleanup(func() { p.Close() })

	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		Persist:  p,
		Listener: session.NewPersistListener(p, session.New("e2e-split")),
	}
	l.Emit = func(Event) {}

	history := splitMsgs(950_000)
	l.messages = oaiToOrderedMaps(history)
	// 先把原历史落盘（模拟真实会话的既有 transcript）。
	for _, m := range history {
		_ = p.AppendOai(m, false)
	}
	_ = p.Flush()

	l.maybeCompactAtBoundary(0)

	// 落盘的消息数应远少于原始（替换生效）。
	loaded := p.LoadOai()
	if len(loaded) >= len(history) {
		t.Errorf("落盘历史应被替换为更短形态：%d → %d", len(history), len(loaded))
	}
	if len(loaded) == 0 {
		t.Fatal("落盘不应为空")
	}
}

// TestSessionSplitNotTriggeredBelowThreshold —— 未达门槛时历史不动。
func TestSessionSplitNotTriggeredBelowThreshold(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(1_000_000),
		messages: oaiToOrderedMaps(splitMsgs(100_000)), // 10%
	}
	l.Emit = func(Event) {}
	before := len(l.messages)

	l.maybeCompactAtBoundary(0)

	if l.Compact.LastSplitDecision.ShouldSplit {
		t.Error("10% 占用不应触发 split")
	}
	if len(l.messages) > before {
		t.Errorf("未触发时不应增长历史：%d → %d", before, len(l.messages))
	}
}

// TestSessionSplitSmallWindowNeverReplaces —— 小窗口永不 split（即使比例极高）。
func TestSessionSplitSmallWindowNeverReplaces(t *testing.T) {
	l := &Loop{
		Compact:  NewCompactBoundary(128_000),
		messages: oaiToOrderedMaps(splitMsgs(200_000)), // ratio > 1
	}
	l.Emit = func(Event) {}
	before := len(l.messages)

	l.maybeCompactAtBoundary(0)

	if l.Compact.LastSplitDecision.ShouldSplit {
		t.Error("小窗口不应触发 split（对账 TS 的 500K 门槛）")
	}
	// 常规压缩可能生效，但历史不应因 split 而被替换成 handoff。
	joined := ""
	for _, m := range l.messages {
		if v, ok := m.Get("content"); ok {
			if c, isStr := v.(string); isStr {
				joined += c
			}
		}
	}
	if strings.Contains(joined, "<session-handoff>") {
		t.Error("小窗口不应产出 session handoff")
	}
	_ = before
}
