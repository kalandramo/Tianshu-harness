package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/client"
	"github.com/kalandramo/tianshu/go/internal/retry"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// sseData 构造一条 SSE data 行。
func sseData(obj any) string {
	b, _ := json.Marshal(obj)
	return "data: " + string(b) + "\n\n"
}

// textTurn 构造一个纯文本回合的 SSE 响应。
func textTurn(text string) string {
	return sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": text},
	}}}) + sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "stop",
	}}}) + "data: [DONE]\n\n"
}

// toolTurn 构造一个工具调用回合的 SSE 响应。
func toolTurn(id, name, args string) string {
	return sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		}}},
	}}}) + sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "tool_calls",
	}}}) + "data: [DONE]\n\n"
}

// scriptedServer 按脚本依次返回响应，并记录收到的请求体。
type scriptedServer struct {
	responses []string
	calls     int64
	bodies    []string
}

func (s *scriptedServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&s.calls, 1)
		// 用 io.ReadAll 完整读取：单次 Read 未必读满大 body（工具 schema 可达数十 KB），
		// 用 Read 会让断言在大请求体上随机失败。
		body, _ := io.ReadAll(r.Body)
		s.bodies = append(s.bodies, string(body))

		idx := int(n) - 1
		if idx >= len(s.responses) {
			idx = len(s.responses) - 1 // 超出脚本则重复最后一条
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, s.responses[idx])
	}
}

// newTestLoop 构造测试用 loop。
func newTestLoop(t *testing.T, srv *httptest.Server, cfg Config) *Loop {
	t.Helper()
	if cfg.Cwd == "" {
		cfg.Cwd = t.TempDir()
	}
	cl := client.New(client.Config{
		BaseURL: srv.URL, APIKey: "k", Model: "test-model", MaxTokens: 100,
		Retry: &retry.Options{MaxTotalRetries: intPtr(0)},
	})
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: cfg.Cwd})
	return New(cfg, cl, reg)
}

// 纯文本回合：一轮结束。
func TestLoopSingleTextTurn(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("你好")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	if err := l.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 应有 text 事件与 done 事件
	var sawText, sawDone bool
	for _, e := range events {
		if e.Kind == "text" && e.Text == "你好" {
			sawText = true
		}
		if e.Kind == "done" {
			sawDone = true
		}
	}
	if !sawText {
		t.Errorf("缺少 text 事件：%+v", events)
	}
	if !sawDone {
		t.Errorf("缺少 done 事件：%+v", events)
	}
	// 历史：system(无) + user + assistant
	if len(l.Messages()) != 2 {
		t.Errorf("历史长度 = %d, want 2", len(l.Messages()))
	}
}

// 工具调用回合：执行工具并把结果回灌，第二轮给终答。
func TestLoopToolCallCycle(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/data.txt", "文件内容")

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"path":"data.txt"}`),
		textTurn("读到了"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	if err := l.Run(context.Background(), "读一下 data.txt"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	var toolStart, toolResult, done bool
	for _, e := range events {
		switch e.Kind {
		case "tool_start":
			toolStart = true
			if e.ToolName != "read_file" {
				t.Errorf("工具名 = %q", e.ToolName)
			}
		case "tool_result":
			toolResult = true
			if !strings.Contains(e.Text, "文件内容") {
				t.Errorf("工具结果应含文件内容：%s", e.Text)
			}
		case "done":
			done = true
		}
	}
	if !toolStart || !toolResult || !done {
		t.Errorf("事件不全：start=%v result=%v done=%v", toolStart, toolResult, done)
	}

	// 历史：user + assistant(tool_calls) + tool + assistant(text) = 4
	if len(l.Messages()) != 4 {
		t.Errorf("历史长度 = %d, want 4", len(l.Messages()))
	}
}

// 反证 A：assistant 回合有工具调用时必须带 tool_calls 字段，
// 否则下一轮的 tool 结果消息失去配对，历史不完整。
func TestAssistantMessageCarriesToolCalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a.txt", "x")

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"path":"a.txt"}`),
		textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	if err := l.Run(context.Background(), "read"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	msgs := l.Messages()
	// 找 assistant 消息
	var assistant *map[string]any
	for _, m := range msgs {
		role, _ := m.Get("role")
		if role == "assistant" {
			if _, hasTC := m.Get("tool_calls"); hasTC {
				mm := map[string]any{}
				for _, k := range m.Keys() {
					v, _ := m.Get(k)
					mm[k] = v
				}
				assistant = &mm
			}
		}
	}
	if assistant == nil {
		t.Fatal("assistant 消息缺少 tool_calls——历史不完整")
	}

	// 验证第二轮请求体里带了 tool 消息
	if len(sc.handlerBodies()) < 2 {
		t.Fatal("应有两次请求")
	}
	second := sc.handlerBodies()[1]
	if !strings.Contains(second, `"role":"tool"`) {
		t.Errorf("第二轮请求体应含 tool 消息：%.400s", second)
	}
	if !strings.Contains(second, `"tool_call_id":"c1"`) {
		t.Errorf("tool 消息应带 tool_call_id：%.400s", second)
	}
}

// 反证 B：截断参数的工具调用**不得执行**。
//
// 半个参数的命令（尤其 bash）比失败更危险——session 4df36bcd 把截断的
// bash 调用当 {} 执行了。
func TestTruncatedToolCallNotExecuted(t *testing.T) {
	root := t.TempDir()

	// 构造一个参数截断的 tool 响应（参数永远不完整）
	truncated := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "c1", "type": "function",
			"function": map[string]any{"name": "bash", "arguments": `{"command":"rm -rf `},
		}}},
	}}}) + sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "tool_calls",
	}}}) + "data: [DONE]\n\n"

	sc := &scriptedServer{responses: []string{truncated, textTurn("ok")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	var toolResults []Event
	l.Emit = func(e Event) {
		if e.Kind == "tool_result" {
			toolResults = append(toolResults, e)
		}
	}

	if err := l.Run(context.Background(), "test"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if len(toolResults) == 0 {
		t.Fatal("截断调用应产出 tool_result（保持历史良构）")
	}
	if !toolResults[0].IsError {
		t.Error("截断调用必须标记错误")
	}
	if !strings.Contains(toolResults[0].Text, "截断") {
		t.Errorf("应说明参数截断：%s", toolResults[0].Text)
	}
}

// 反证 C：最大轮数必须生效（防无限循环）。
func TestMaxTurnsEnforced(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a.txt", "x")

	// 每次都返回工具调用 → 若无预算限制会无限循环
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"path":"a.txt"}`),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root, MaxTurns: 3})
	err := l.Run(context.Background(), "loop")
	if err == nil {
		t.Fatal("达最大轮数应报错")
	}
	if !strings.Contains(err.Error(), "最大轮数") {
		t.Errorf("错误应说明轮数上限：%v", err)
	}
	// 应恰好请求 3 次（MaxTurns）
	if n := atomic.LoadInt64(&sc.calls); n != 3 {
		t.Errorf("请求次数 = %d, want 3", n)
	}
}

// 系统提示词应作为首条消息。
func TestSystemPromptFirst(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("ok")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, SystemPrompt: "你是测试助手",
	})
	if err := l.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	msgs := l.Messages()
	role, _ := msgs[0].Get("role")
	if role != "system" {
		t.Errorf("首条消息应为 system，实际 %v", role)
	}
	content, _ := msgs[0].Get("content")
	if content != "你是测试助手" {
		t.Errorf("system 内容 = %v", content)
	}
}

// 反证 D：工具声明必须进请求体（否则模型看不到工具）。
func TestToolDefinitionsInRequest(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("ok")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	if err := l.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	body := sc.handlerBodies()[0]
	for _, name := range []string{"read_file", "write_file", "edit_file", "glob", "grep"} {
		if !strings.Contains(body, `"name":"`+name+`"`) {
			t.Errorf("请求体缺少工具声明 %s", name)
		}
	}
	// 工具声明按名升序（字节稳定的前提）
	idxEdit := strings.Index(body, `"name":"edit_file"`)
	idxGlob := strings.Index(body, `"name":"glob"`)
	idxRead := strings.Index(body, `"name":"read_file"`)
	if !(idxEdit < idxGlob && idxGlob < idxRead) {
		t.Errorf("工具声明未按名升序：edit=%d glob=%d read=%d", idxEdit, idxGlob, idxRead)
	}
}

// 多轮工具调用（链式）。
func TestLoopChainedToolCalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/a.txt", "AAA")

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"path":"a.txt"}`),
		toolTurn("c2", "grep", `{"pattern":"AAA"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	var toolsCalled []string
	l.Emit = func(e Event) {
		if e.Kind == "tool_start" {
			toolsCalled = append(toolsCalled, e.ToolName)
		}
	}

	if err := l.Run(context.Background(), "分析"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if len(toolsCalled) != 2 {
		t.Fatalf("应调用 2 个工具，实际 %v", toolsCalled)
	}
	if toolsCalled[0] != "read_file" || toolsCalled[1] != "grep" {
		t.Errorf("工具顺序错误：%v", toolsCalled)
	}
	// 历史：user + a(tc) + tool + a(tc) + tool + a(text) = 6
	if len(l.Messages()) != 6 {
		t.Errorf("历史长度 = %d, want 6", len(l.Messages()))
	}
}

// 工具执行失败应作为错误结果回灌（不中断循环）。
func TestToolErrorFedBack(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"path":"nonexistent.txt"}`),
		textTurn("文件不存在"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	var toolResult *Event
	l.Emit = func(e Event) {
		if e.Kind == "tool_result" {
			ev := e
			toolResult = &ev
		}
	}

	if err := l.Run(context.Background(), "读文件"); err != nil {
		t.Fatalf("工具失败不应中断循环：%v", err)
	}
	if toolResult == nil {
		t.Fatal("应有 tool_result")
	}
	if !toolResult.IsError {
		t.Error("不存在的文件应标记错误")
	}
}

// ── 测试辅助 ──

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFileHelper(path, content); err != nil {
		t.Fatal(err)
	}
}

// handlerBodies 让 scriptedServer 暴露请求体（供断言用）。
func (s *scriptedServer) handlerBodies() []string { return s.bodies }

var _ = api.ChatRequest{}

// writeFileHelper 是 os.WriteFile 的包装（保持测试文件 import 简洁）。
func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// 反证 E：usage 必须透传到事件上。
//
// 丢掉 usage 等于放弃缓存可观测性——cache_read_input_tokens 是前缀缓存
// 是否命中的唯一直接指标（本轮真实端点验证正是靠它拿到 95% 命中率）。
func TestUsagePropagatedToEvent(t *testing.T) {
	// 构造带 usage 的响应（usage 与 finish_reason 同块，DeepSeek 式）
	withUsage := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": "ok"},
	}}}) + sseData(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
		"usage": map[string]any{
			"prompt_tokens": 1600, "completion_tokens": 5,
			"prompt_cache_hit_tokens": 1536,
		},
	}) + "data: [DONE]\n\n"

	sc := &scriptedServer{responses: []string{withUsage}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	var got *Event
	l.Emit = func(e Event) {
		if e.Kind == "done" {
			ev := e
			got = &ev
		}
	}

	if err := l.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if got == nil {
		t.Fatal("应产出 done 事件")
	}
	if got.Usage == nil {
		t.Fatal("done 事件必须携带 Usage——否则缓存指标不可观测")
	}
	if got.Usage.InputTokens != 1600 {
		t.Errorf("InputTokens = %d, want 1600", got.Usage.InputTokens)
	}
	if got.Usage.CacheReadInputTokens != 1536 {
		t.Errorf("CacheReadInputTokens = %d, want 1536", got.Usage.CacheReadInputTokens)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", got.StopReason)
	}
}
