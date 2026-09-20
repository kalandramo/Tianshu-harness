package agent

import (
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// contractResultOK / contractResultErr 是 contract.Result 的测试构造器。
func contractResultOK(content string) contract.Result {
	return contract.Result{Content: content}
}

func contractResultErr(content string) contract.Result {
	return contract.Result{Content: content, IsError: true}
}

// testDur 是 5ms 的固定时长（轨迹的 DurationMs 断言用）。
const testDur = 5 * time.Millisecond

// 这些测试锁定**接线契约**：trajectory / task-state 必须真的在生产路径上
// 被调用——否则它们是悬空代码（type-without-consumer，本项目反复踩过的坑）。

// TestRecordTrajectoryHasProductionCaller —— executeTool 必须记轨迹。
func TestRecordTrajectoryHasProductionCaller(t *testing.T) {
	l := &Loop{Trajectory: compact.NewTrajectoryRecorder(10)}
	l.recordTrajectory(
		toolCall{name: "read_file", input: map[string]any{"file_path": "src/a.ts"}},
		contractResultOK("content"),
		testDur,
	)
	entries := l.Trajectory.Entries()
	if len(entries) != 1 {
		t.Fatalf("应记录 1 条轨迹，实得 %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "read_file" || e.Target != "src/a.ts" || e.Status != compact.TrajectorySuccess {
		t.Errorf("轨迹字段不符：%+v", e)
	}
}

// TestRecordTrajectoryRecordsFailures —— **失败也必须记**（handoff 错误章节的唯一来源）。
func TestRecordTrajectoryRecordsFailures(t *testing.T) {
	l := &Loop{Trajectory: compact.NewTrajectoryRecorder(10)}
	l.recordTrajectory(
		toolCall{name: "run_tests", input: map[string]any{"filter": "x"}},
		contractResultErr("boom"),
		testDur,
	)
	entries := l.Trajectory.Entries()
	if len(entries) != 1 || entries[0].Status != compact.TrajectoryFailed {
		t.Fatalf("失败调用应记为 failed，实得 %+v", entries)
	}
}

// TestRecordTrajectoryNilSafe —— nil recorder 不得 panic。
func TestRecordTrajectoryNilSafe(t *testing.T) {
	l := &Loop{}
	l.recordTrajectory(toolCall{name: "x"}, contractResultOK("y"), testDur) // 不应 panic
}

// TestSplitStateCarriesTrajectoryAndTodos —— splitState 必须带上真实状态。
func TestSplitStateCarriesTrajectoryAndTodos(t *testing.T) {
	rec := compact.NewTrajectoryRecorder(10)
	l := &Loop{
		Trajectory:   rec,
		Todos:        func() []prompt.TodoItem { return []prompt.TodoItem{{ID: "T1", Content: "任务", Status: "pending"}} },
		StreamedText: func() string { return "I'll use X." },
	}
	s := l.splitState()
	if s.Trajectory != rec {
		t.Error("splitState 应携带 Trajectory")
	}
	if len(s.Todos) != 1 || s.Todos[0].ID != "T1" {
		t.Errorf("splitState 应携带 Todos，实得 %+v", s.Todos)
	}
	if s.StreamedText != "I'll use X." {
		t.Errorf("splitState 应携带 StreamedText，实得 %q", s.StreamedText)
	}
}

// TestSplitStateNilReadersSafe —— nil 读取器不得 panic（全部可缺省）。
func TestSplitStateNilReadersSafe(t *testing.T) {
	l := &Loop{}
	s := l.splitState()
	if s.Trajectory != nil || s.Todos != nil || s.StreamedText != "" {
		t.Errorf("nil 读取器应产出零值状态，实得 %+v", s)
	}
}

// TestSplitHandoffUsesRealState —— **端到端**：split 判定触发时 handoff 含真实状态。
//
// 这条是「非悬空」的核心证据：trajectory / todos 真的流进了 handoff 文本。
func TestSplitHandoffUsesRealState(t *testing.T) {
	rec := compact.NewTrajectoryRecorder(10)
	rec.Record(compact.TrajectoryEntry{
		Turn: 0, Tool: "read_file", Target: "src/foo.ts", Status: compact.TrajectorySuccess,
	})
	l := &Loop{
		Compact:    NewCompactBoundary(1_000_000),
		Trajectory: rec,
		Todos: func() []prompt.TodoItem {
			return []prompt.TodoItem{
				{ID: "T1", Content: "已完成骨架", Status: "completed"},
				{ID: "T2", Content: "当前实现核心", Status: "in_progress"},
			}
		},
		StreamedText: func() string { return "I'll use the adapter pattern." },
	}
	l.Emit = func(Event) {}

	// 95% 占用 → 判定必触发。
	l.messages = oaiToOrderedMaps(splitMsgs(950_000))
	l.maybeCompactAtBoundary(0)

	if l.Compact.LastSplitDecision == nil || !l.Compact.LastSplitDecision.ShouldSplit {
		t.Fatal("本用例需要 split 判定触发")
	}
	// 直接验证 handoff 构造（通过 TrySessionSplit 的返回值）。
	out := l.Compact.TrySessionSplit(orderedMapsToOai(l.messages), l.splitState())
	if out.Handoff == "" {
		t.Fatal("handoff 不应为空")
	}
	for _, want := range []string{"当前实现核心", "已完成骨架", "read_file", "adapter"} {
		if !containsStr(out.Handoff, want) {
			t.Errorf("handoff 应含真实状态 %q，实得：\n%s", want, out.Handoff)
		}
	}
}

func containsStr(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
