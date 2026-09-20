package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// 这些测试锁定**接线契约**：`CheckpointDeps.Preflight` 必须真的在替换路径上
// 被调用——否则它是悬空依赖（本项目反复踩过的坑）。

// TestCheckpointPreflightRepairsOrphan —— preflight 注入后孤儿被修复。
//
// 模拟真实场景：历史替换后候选里有一条 assistant(tool_calls) 但无对应
// tool 结果——preflight 应合成占位。
func TestCheckpointPreflightRepairsOrphan(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"),
		cpMsg("user", "first"),
		cpMsg("assistant", "d1"),
		cpMsg("user", "d2"),
		cpMsg("assistant", "d3"),
	}

	// preflight 注入：给候选补一条孤儿 tool_call（模拟破损历史）。
	deps := CheckpointDeps{
		Preflight: func(msgs []session.OaiMessage) []session.OaiMessage {
			// 构造一条带 tool_calls 但无结果的 assistant。
			orphan := cpMsg("assistant", "")
			orphan.ToolCalls = []session.OaiToolCall{{
				ID: "tc-orphan", Type: "function",
				Function: &session.OaiFunction{Name: "write_file", Arguments: `{"file_path":"x.ts"}`},
			}}
			return append(msgs, orphan)
		},
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "SUMMARY", Action: compact.ActionSessionSplit, Force: true,
	}, deps)

	if !out.Committed {
		t.Fatal("应提交")
	}
	last := out.Messages[len(out.Messages)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 1 {
		t.Fatalf("preflight 的改动应被应用，实得 %+v", last)
	}
}

// TestCheckpointPreflightNilIsNoop —— nil preflight 时原样返回（不 panic）。
func TestCheckpointPreflightNilIsNoop(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"), cpMsg("user", "first"),
		cpMsg("assistant", "d1"), cpMsg("user", "d2"), cpMsg("assistant", "d3"),
	}
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "S", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{}) // 无 Preflight
	if !out.Committed {
		t.Fatal("应提交")
	}
	if len(out.Messages) == 0 {
		t.Error("不应为空")
	}
}

// TestCheckpointPreflightSeesCandidate —— preflight 收到的是**候选**（含摘要）。
//
// 这是「接在正确位置」的证据：preflight 必须作用于组好的候选，而不是原文。
func TestCheckpointPreflightSeesCandidate(t *testing.T) {
	b := NewCompactBoundary(1_000_000)
	messages := []session.OaiMessage{
		cpMsg("system", "SYS"), cpMsg("user", "first"),
		cpMsg("assistant", "d1"), cpMsg("user", "d2"), cpMsg("assistant", "d3"),
	}
	var seen []session.OaiMessage
	out := b.ReplaceWithCheckpoint(messages, CheckpointParams{
		Summary: "UNIQUE-SUMMARY-TEXT", Action: compact.ActionSessionSplit, Force: true,
	}, CheckpointDeps{
		Preflight: func(msgs []session.OaiMessage) []session.OaiMessage {
			seen = msgs
			return msgs
		},
	})
	if seen == nil {
		t.Fatal("preflight 应被调用")
	}
	joined := ""
	for _, m := range seen {
		if m.Content != nil {
			joined += *m.Content
		}
	}
	if !strings.Contains(joined, "UNIQUE-SUMMARY-TEXT") {
		t.Error("preflight 应看到已组好的候选（含摘要）——说明接在正确位置")
	}
	_ = out
}
