package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestEndTurnTerminatesRun —— ask_user_question 的 EndTurn 必须终止回合。
//
// # 这是本刀的核心反证
//
// 在补上主循环的消费点之前，`contract.Result.EndTurn` 有定义、零消费方
// ——工具即便返回 EndTurn: true，主循环也会继续下一轮，模型自问自答，
// 用户的回答永远等不到。
//
// 断言的是**可观察行为**：脚本只给一条工具调用响应，**不给后续文本**。
// 若主循环不认 EndTurn，它会再调一次模型（scriptedServer 会重复最后一条
// 响应），产生第 2 个 tool_start —— 历史与事件数都会增长。故「只发生一次
// 工具调用 + done 且无 turn_end」即证明回合被正确终止。
func TestEndTurnTerminatesRun(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "ask_user_question", map[string]any{
			"question": "Which approach?",
			"options":  []any{"A", "B"},
		}),
		// 故意不给后续响应——若主循环继续，它会重复这条工具调用。
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	if err := l.Run(context.Background(), "选一个"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	var toolStarts, done, turnEnd int
	for _, e := range events {
		switch e.Kind {
		case "tool_start":
			toolStarts++
		case "done":
			done++
		case "turn_end":
			turnEnd++
		}
	}

	if toolStarts != 1 {
		t.Errorf("工具调用次数 = %d, want 1（EndTurn 未被消费——主循环继续了）", toolStarts)
	}
	if done != 1 {
		t.Errorf("done 事件数 = %d, want 1", done)
	}
	if turnEnd != 0 {
		t.Errorf("turn_end 事件数 = %d, want 0（endTurn 路径不应走常规 turn_end）", turnEnd)
	}
	// 模型调用只应发生 1 次——第 2 次就说明回合没终止。
	if sc.calls != 1 {
		t.Errorf("模型调用次数 = %d, want 1", sc.calls)
	}
}

// 对照：普通工具（无 EndTurn）**不**终止回合——证明上面的测试不是
// 「所有工具都终止」的假绿。
func TestNormalToolDoesNotTerminate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/data.txt", "内容")

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"data.txt"}`),
		textTurn("读到了"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	if err := l.Run(context.Background(), "读"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	// read_file 不终止 → 第二轮文本回合 → 2 次模型调用。
	if sc.calls != 2 {
		t.Errorf("模型调用次数 = %d, want 2（普通工具不应终止回合）", sc.calls)
	}
}

// EndTurn 路径必须保持历史良构：assistant(tool_calls) 与 tool 结果配对。
//
// 若提前 break 导致孤儿 tool_call，下一轮请求会因消息不成对而失败
// （OpenAI 协议硬约束）。
func TestEndTurnKeepsHistoryWellFormed(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "ask_user_question", map[string]any{"question": "Open?"}),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	if err := l.Run(context.Background(), "问"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	msgs := l.Messages()
	// user + assistant(tool_calls) + tool = 3
	if len(msgs) != 3 {
		t.Fatalf("历史长度 = %d, want 3", len(msgs))
	}
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		r, _ := m.Get("role")
		rs, _ := r.(string)
		roles = append(roles, rs)
	}
	want := []string{"user", "assistant", "tool"}
	for i, w := range want {
		if roles[i] != w {
			t.Errorf("roles[%d] = %q, want %q（完整 roles=%v）", i, roles[i], w, roles)
		}
	}
	// tool 消息必须带 tool_call_id，与 assistant 的 tool_calls 配对。
	last := msgs[len(msgs)-1]
	if id, ok := last.Get("tool_call_id"); !ok || id != "c1" {
		t.Errorf("tool 消息缺 tool_call_id 或值不符：%v", id)
	}
}

// batch 内多工具时：任一返回 EndTurn 即终止，且**其余工具仍执行完**
// （对账 TS——整批跑完才检查 endTurn）。
func TestEndTurnAfterFullBatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a.txt", "内容A")

	// 一个回合里两个工具调用：read_file 与 ask_user_question。
	twoTools := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{"file_path":"a.txt"}`}},
			map[string]any{"index": 1, "id": "c2", "type": "function",
				"function": map[string]any{"name": "ask_user_question", "arguments": `{"question":"Q?"}`}},
		}},
	}}}) + sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "tool_calls",
	}}}) + "data: [DONE]\n\n"

	sc := &scriptedServer{responses: []string{twoTools}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	if err := l.Run(context.Background(), "并行"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	toolStarts, toolResults := 0, 0
	for _, e := range events {
		switch e.Kind {
		case "tool_start":
			toolStarts++
		case "tool_result":
			toolResults++
		}
	}
	// 两个工具都要跑完——不能因 endTurn 提前 break 丢掉 read_file 的结果。
	if toolStarts != 2 || toolResults != 2 {
		t.Errorf("tool_start=%d tool_result=%d, want 2/2（batch 未跑完）", toolStarts, toolResults)
	}
	// 历史：user + assistant(2 tool_calls) + 2×tool = 4
	if len(l.Messages()) != 4 {
		t.Errorf("历史长度 = %d, want 4", len(l.Messages()))
	}
	// read_file 的结果必须在历史里（不是只跑了 ask）。
	found := false
	for _, m := range l.Messages() {
		if c, ok := m.Get("content"); ok {
			if s, ok := c.(string); ok && strings.Contains(s, "内容A") {
				found = true
			}
		}
	}
	if !found {
		t.Error("read_file 的结果未落历史——batch 被提前中断")
	}
}

// OnAskUserQuestion 注入必须从 Loop.ToolParams 透传到工具（接线反证）。
func TestAskUserQuestionCallbackWired(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "ask_user_question", map[string]any{
			"question": "Which?", "options": []any{"A", "B"},
		}),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	var got *tools.AskUserQuestionInfo
	l.ToolParams = &tools.CallParams{
		OnAskUserQuestion: func(info tools.AskUserQuestionInfo) { got = &info },
	}

	if err := l.Run(context.Background(), "问"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got == nil {
		t.Fatal("OnAskUserQuestion 未从 ToolParams 透传到工具")
	}
	if len(got.Questions) != 1 || got.Questions[0].Prompt != "Which?" {
		t.Errorf("回调载荷不符：%+v", got.Questions)
	}
}

// OnLeaveMark 注入必须透传（第三十七刀遗留的悬空接线）。
func TestLeaveMarkCallbackWired(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "leave_mark", map[string]any{
			"symbol": "✦", "summary": "做完了",
		}),
		// leave_mark 不返回 EndTurn → 回合继续，需一条终止文本回合。
		textTurn("再见"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	var got *tools.LeaveMarkInput
	l.ToolParams = &tools.CallParams{
		OnLeaveMark: func(mark tools.LeaveMarkInput) { got = &mark },
	}

	if err := l.Run(context.Background(), "再见"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got == nil {
		t.Fatal("OnLeaveMark 未从 ToolParams 透传到工具（悬空接线未修）")
	}
	if got.Symbol != "✦" || got.Summary != "做完了" {
		t.Errorf("回调载荷不符：%+v", got)
	}
}
