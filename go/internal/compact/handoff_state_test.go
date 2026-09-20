package compact

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// 这些测试锁定 `BuildSessionHandoffWithState` 的**接线契约**：
// task-state / trajectory 必须真的进入 handoff 文本——否则新模块是悬空的。

func msg(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}

func toolMsg(content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: "tool", Content: &c}
}

// TestHandoffWithStateIncludesTaskState —— task-state 的四个字段必须出现。
func TestHandoffWithStateIncludesTaskState(t *testing.T) {
	messages := []session.OaiMessage{
		msg("user", "做点什么"),
		msg("assistant", "I'll use the adapter pattern."),
		toolMsg("read /repo/src/foo.ts done"),
	}
	todos := []prompt.TodoItem{
		{ID: "T1", Content: "搭建骨架", Status: "completed"},
		{ID: "T2", Content: "实现核心", Status: "in_progress"},
		{ID: "T3", Content: "写测试", Status: "pending"},
	}
	rec := NewTrajectoryRecorder(10)
	rec.Record(TrajectoryEntry{Turn: 0, Tool: "read_file", Target: "src/foo.ts", Status: TrajectorySuccess, DurationMs: 5})

	got := BuildSessionHandoffWithState(messages, 0.9, rec, todos, "I'll use the adapter pattern.")

	// current / completed / remaining 来自 taskStateFromTodos。
	for _, want := range []string{"实现核心", "搭建骨架", "写测试"} {
		if !strings.Contains(got, want) {
			t.Errorf("handoff 应含 todo 项 %q，实得：\n%s", want, got)
		}
	}
	// 决策来自 extractTaskState 的启发式。
	if !strings.Contains(got, "adapter") {
		t.Errorf("handoff 应含决策文本（adapter），实得：\n%s", got)
	}
	// 轨迹必须出现（tool + target）。
	if !strings.Contains(got, "read_file") {
		t.Errorf("handoff 应含工具轨迹 read_file，实得：\n%s", got)
	}
	if !strings.Contains(got, "src/foo.ts") {
		t.Errorf("handoff 应含轨迹 target，实得：\n%s", got)
	}
}

// TestHandoffWithStateIncludesFailureSection —— 失败记录必须出现在 handoff。
func TestHandoffWithStateIncludesFailureSection(t *testing.T) {
	rec := NewTrajectoryRecorder(10)
	rec.Record(TrajectoryEntry{Turn: 1, Tool: "run_tests", Target: "x.test.ts", Status: TrajectoryFailed, ErrorClass: "assert"})

	got := BuildSessionHandoffWithState([]session.OaiMessage{msg("user", "hi")}, 0.9, rec, nil, "")
	if !strings.Contains(got, "run_tests") || !strings.Contains(got, "assert") {
		t.Errorf("handoff 应含失败记录（run_tests/assert），实得：\n%s", got)
	}
}

// TestHandoffWithStateNilRecorderSafe —— nil recorder 不得 panic。
func TestHandoffWithStateNilRecorderSafe(t *testing.T) {
	got := BuildSessionHandoffWithState([]session.OaiMessage{msg("user", "hi")}, 0.5, nil, nil, "")
	if !strings.Contains(got, "<session-handoff>") {
		t.Errorf("nil recorder 时应仍产出基本 handoff，实得：\n%s", got)
	}
}

// TestHandoffBackwardCompatible —— 旧签名必须仍然可用（委托到新函数）。
func TestHandoffBackwardCompatible(t *testing.T) {
	messages := []session.OaiMessage{msg("assistant", "thinking...")}
	got := BuildSessionHandoff(messages, 0.9)
	if !strings.Contains(got, "<session-handoff>") {
		t.Errorf("旧签名应仍产出 handoff，实得：\n%s", got)
	}
	if !strings.Contains(got, "thinking...") {
		t.Errorf("旧签名应保留近期推理，实得：\n%s", got)
	}
}

// TestHandoffTrajectoryCapped —— 轨迹章节最多 12 条（对账 TS slice(-12)）。
//
// **注意**：必须**只数第 8 章**——`toolX` 也会出现在第 6 章（已完成工作，
// 来自 task-state 的 completed）。首版用全局 `strings.Count` 数到 20，
// 误判成实现 bug（实际实现是对的）。
func TestHandoffTrajectoryCapped(t *testing.T) {
	rec := NewTrajectoryRecorder(50)
	for i := 0; i < 30; i++ {
		rec.Record(TrajectoryEntry{Turn: i, Tool: "toolX", Target: "f.ts", Status: TrajectorySuccess})
	}
	got := BuildSessionHandoffWithState([]session.OaiMessage{msg("user", "hi")}, 0.9, rec, nil, "")

	section := handoffSection(t, got, "8. 最近工具轨迹", "9. 下一步")
	n := strings.Count(section, "toolX")
	if n > 12 {
		t.Errorf("轨迹章节应最多 12 条（对账 TS slice(-12)），实得 %d 条", n)
	}
	if n == 0 {
		t.Error("轨迹章节不应为空")
	}
}

// handoffSection 切出 `## <title>` 到下一个 `## ` 之间的正文。
func handoffSection(t *testing.T, doc, title, nextTitle string) string {
	t.Helper()
	start := strings.Index(doc, "## "+title)
	if start < 0 {
		t.Fatalf("未找到章节 %q", title)
	}
	rest := doc[start:]
	end := strings.Index(rest, "## "+nextTitle)
	if end < 0 {
		t.Fatalf("未找到下一章节 %q", nextTitle)
	}
	return rest[:end]
}
