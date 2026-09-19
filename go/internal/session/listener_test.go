package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// ── AddUsage：条件累加语义 ──

func TestAddUsageAccumulates(t *testing.T) {
	m := New("s")
	m.AddUsage(TotalUsage{InputTokens: 100, OutputTokens: 20})
	m.AddUsage(TotalUsage{InputTokens: 50, OutputTokens: 10})
	u := m.TotalUsage()
	if u.InputTokens != 150 {
		t.Errorf("InputTokens 应累加为 150，得到 %d", u.InputTokens)
	}
	if u.OutputTokens != 30 {
		t.Errorf("OutputTokens 应累加为 30，得到 %d", u.OutputTokens)
	}
}

// TestAddUsageSkipsZero —— 零值**跳过**（对账 TS 的 `if (usage.x)` 语义）。
func TestAddUsageSkipsZero(t *testing.T) {
	m := New("s")
	m.AddUsage(TotalUsage{InputTokens: 100})
	m.AddUsage(TotalUsage{OutputTokens: 0, InputTokens: 0}) // 全零
	u := m.TotalUsage()
	if u.InputTokens != 100 {
		t.Errorf("零值不应改变计数，得到 %d", u.InputTokens)
	}
	if u.OutputTokens != 0 {
		t.Errorf("OutputTokens 应保持 0，得到 %d", u.OutputTokens)
	}
}

// TestAddUsageReasoningIsSubsetNotAdditive —— reasoning 不叠加到 output。
func TestAddUsageReasoningIsSubsetNotAdditive(t *testing.T) {
	m := New("s")
	// OutputTokens=100 其中 Reasoning=40（子集，不是额外 40）
	m.AddUsage(TotalUsage{OutputTokens: 100, ReasoningTokens: 40, HasReasoning: true})
	u := m.TotalUsage()
	if u.OutputTokens != 100 {
		t.Errorf("OutputTokens 不应被 reasoning 影响，得到 %d", u.OutputTokens)
	}
	if u.ReasoningTokens != 40 {
		t.Errorf("ReasoningTokens 应为 40，得到 %d", u.ReasoningTokens)
	}
}

// TestAddUsageReasoningNotReported —— 提供商未报 reasoning 时不计。
func TestAddUsageReasoningNotReported(t *testing.T) {
	m := New("s")
	m.AddUsage(TotalUsage{OutputTokens: 100, ReasoningTokens: 40, HasReasoning: false})
	if m.TotalUsage().ReasoningTokens != 0 {
		t.Errorf("HasReasoning=false 时不应累计，得到 %d", m.TotalUsage().ReasoningTokens)
	}
}

// TestAddUsageCacheFields —— cache 字段独立累加。
func TestAddUsageCacheFields(t *testing.T) {
	m := New("s")
	m.AddUsage(TotalUsage{InputTokens: 1000, CacheReadInputTokens: 900, CacheCreationInputTokens: 50})
	m.AddUsage(TotalUsage{InputTokens: 2000, CacheReadInputTokens: 1800})
	u := m.TotalUsage()
	if u.CacheReadInputTokens != 2700 {
		t.Errorf("CacheRead 应为 2700，得到 %d", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreation 应为 50，得到 %d", u.CacheCreationInputTokens)
	}
	// InputTokens 是 cache-inclusive 的独立计数，不是相加
	if u.InputTokens != 3000 {
		t.Errorf("InputTokens 应为 3000，得到 %d", u.InputTokens)
	}
}

// ── shouldFlushNow：落盘策略 ──

func TestShouldFlushNow(t *testing.T) {
	c := "x"
	cases := []struct {
		name string
		msg  OaiMessage
		want bool
	}{
		{"user 立即落盘", OaiMessage{Role: "user", Content: &c}, true},
		{"tool 立即落盘", OaiMessage{Role: "tool", Content: &c}, true},
		{"assistant 带 tool_calls 立即落盘", OaiMessage{
			Role: "assistant", ToolCalls: []OaiToolCall{{ID: "c1"}},
		}, true},
		{"assistant 纯文本走批量", OaiMessage{Role: "assistant", Content: &c}, false},
		{"assistant 空 tool_calls 走批量", OaiMessage{
			Role: "assistant", Content: &c, ToolCalls: []OaiToolCall{},
		}, false},
		{"system 走批量", OaiMessage{Role: "system", Content: &c}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFlushNow(tc.msg); got != tc.want {
				t.Errorf("shouldFlushNow=%v，期望 %v", got, tc.want)
			}
		})
	}
}

// ── PersistListener：端到端 ──

func TestListenerAppendsToFile(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("ls", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	state := New("ls")
	l := NewPersistListener(p, state)

	c := "hello"
	l.OnAppend(OaiMessage{Role: "user", Content: &c})

	got := p.LoadOai()
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("应落盘 1 条 user，得到 %+v", got)
	}
}

// TestListenerUserImmediateFlush —— user 消息立即落盘（硬杀不丢）。
func TestListenerUserImmediateFlush(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lf", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("lf"))
	c := "durable"
	l.OnAppend(OaiMessage{Role: "user", Content: &c})

	// **不调 Drain** —— 直接读磁盘，验证已落盘
	raw, err := os.ReadFile(p.FilePath())
	if err != nil {
		t.Fatalf("读文件失败：%v", err)
	}
	if !strings.Contains(string(raw), "durable") {
		t.Error("user 消息应已落盘（未 flush 也应可见）")
	}
}

// TestListenerAssistantTextBatched —— assistant 纯文本走批量节拍。
//
// **注意**：BatchWriter 对**新会话首行**无条件同步 flush（codecReady 未就绪
// 时先写一帧建立文件头）。故这里先写一条 user 消息建立文件，再验证
// assistant 纯文本**不**立即落盘。
func TestListenerAssistantTextBatched(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lb", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("lb"))
	// 首行：建立文件（会同步 flush）
	first := "first"
	l.OnAppend(OaiMessage{Role: "user", Content: &first})

	// 此后 assistant 纯文本走批量——未 flush 时磁盘不含它
	batched := "batched-assistant-text"
	l.OnAppend(OaiMessage{Role: "assistant", Content: &batched})

	raw, err := os.ReadFile(p.FilePath())
	if err != nil {
		t.Fatalf("读文件失败：%v", err)
	}
	if strings.Contains(string(raw), batched) {
		t.Error("assistant 纯文本应走批量节拍，未 flush 时不应落盘")
	}

	// Drain 后应可见
	if err := l.Drain(); err != nil {
		t.Fatalf("Drain 失败：%v", err)
	}
	raw2, err := os.ReadFile(p.FilePath())
	if err != nil {
		t.Fatalf("Drain 后读文件失败：%v", err)
	}
	if !strings.Contains(string(raw2), batched) {
		t.Error("Drain 后应已落盘")
	}
}

// TestListenerFirstLineSyncFlush —— 新会话首行同步落盘。
//
// 对账：BatchWriter 在 codecReady 未就绪时**无条件** flush 首行——
// 保证文件头（zstd 帧）立即建立，后续 append 有确定的追加点。
func TestListenerFirstLineSyncFlush(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lfl", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("lfl"))
	c := "first-line"
	l.OnAppend(OaiMessage{Role: "assistant", Content: &c}) // assistant 也同步（首行特例）

	if _, err := os.Stat(p.FilePath()); os.IsNotExist(err) {
		t.Error("首行应同步落盘（建立文件）")
	}
}

// TestListenerTurnCountIncrements —— user 回合计数累加。
func TestListenerTurnCountIncrements(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lt", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	state := New("lt")
	l := NewPersistListener(p, state)

	c1 := "first"
	l.OnAppend(OaiMessage{Role: "user", Content: &c1})
	meta := p.Metadata().Load()
	if meta == nil || meta.TurnCount != 1 {
		t.Fatalf("turnCount 应为 1，得到 %+v", meta)
	}

	c2 := "second"
	l.OnAppend(OaiMessage{Role: "user", Content: &c2})
	meta = p.Metadata().Load()
	if meta.TurnCount != 2 {
		t.Errorf("turnCount 应为 2，得到 %d", meta.TurnCount)
	}
	// title 只设一次
	if meta.Title != "first" {
		t.Errorf("title 应保持首次值 first，得到 %q", meta.Title)
	}
}

// TestListenerReminderNotCounted —— `<system-reminder>` 开头的 user 不算真实回合。
func TestListenerReminderNotCounted(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lr", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("lr"))
	rem := "<system-reminder>guardrail text</system-reminder>"
	l.OnAppend(OaiMessage{Role: "user", Content: &rem})

	meta := p.Metadata().Load()
	if meta != nil && meta.TurnCount != 0 {
		t.Errorf("reminder 不应计入 turnCount，得到 %d", meta.TurnCount)
	}
}

// TestListenerToolCallCount —— assistant 的 tool_calls 累加计数。
func TestListenerToolCallCount(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("ltc", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("ltc"))
	l.OnAppend(OaiMessage{
		Role:      "assistant",
		ToolCalls: []OaiToolCall{{ID: "c1"}, {ID: "c2"}},
	})
	meta := p.Metadata().Load()
	if meta == nil || meta.ToolCallCount != 2 {
		t.Fatalf("toolCallCount 应为 2，得到 %+v", meta)
	}
}

// TestListenerTokenUsageFromState —— tokenUsage 取自状态容器的累计值。
func TestListenerTokenUsageFromState(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lu", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	state := New("lu")
	state.AddUsage(TotalUsage{InputTokens: 1000, OutputTokens: 200})
	l := NewPersistListener(p, state)

	c := "hi"
	l.OnAppend(OaiMessage{Role: "user", Content: &c})

	meta := p.Metadata().Load()
	if meta == nil || meta.TokenUsage == nil {
		t.Fatal("tokenUsage 应被设置")
	}
	if meta.TokenUsage.Prompt != 1000 {
		t.Errorf("prompt 应为 1000，得到 %d", meta.TokenUsage.Prompt)
	}
	if meta.TokenUsage.Completion != 200 {
		t.Errorf("completion 应为 200，得到 %d", meta.TokenUsage.Completion)
	}
	if meta.TokenUsage.Total != 1200 {
		t.Errorf("total 应为 1200（input+output），得到 %d", meta.TokenUsage.Total)
	}
}

// TestListenerReplaceUnimplemented —— OnReplace 报告未实现（不静默吞掉）。
func TestListenerReplaceUnimplemented(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("lrep", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	var captured error
	l := NewPersistListener(p, New("lrep"))
	l.SetErrorHandler(func(e error) { captured = e })
	l.OnReplace([]OaiMessage{{Role: "user"}})

	if captured == nil {
		t.Error("OnReplace 应报告未实现，不能静默")
	}
	if !strings.Contains(captured.Error(), "未实现") {
		t.Errorf("错误消息应说明未实现，得到 %q", captured.Error())
	}
}

// ── OaiMessageFromWire：桥接层 ──

func TestOaiMessageFromWire(t *testing.T) {
	m := wire.NewOrderedMap().
		Set("role", "assistant").
		Set("content", "text").
		Set("tool_calls", []any{
			map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{"path":"a"}`},
			},
		})
	got := OaiMessageFromWire(m)
	if got.Role != "assistant" {
		t.Errorf("role 应为 assistant，得到 %q", got.Role)
	}
	if got.Content == nil || *got.Content != "text" {
		t.Errorf("content 应为 text，得到 %v", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "c1" {
		t.Fatalf("应有 1 个 tool_call c1，得到 %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].Function == nil || got.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("function.name 应为 read_file，得到 %+v", got.ToolCalls[0].Function)
	}
	// 键序从 OrderedMap 搬运
	if len(got.KeyOrder) != 3 || got.KeyOrder[0] != "role" {
		t.Errorf("键序应搬运自 OrderedMap，得到 %v", got.KeyOrder)
	}
}

func TestOaiMessageFromWireToolResult(t *testing.T) {
	m := wire.NewOrderedMap().
		Set("role", "tool").
		Set("tool_call_id", "c1").
		Set("content", "result")
	got := OaiMessageFromWire(m)
	if got.Role != "tool" || got.ToolCallID != "c1" {
		t.Errorf("tool 消息转换不符：%+v", got)
	}
	if got.Content == nil || *got.Content != "result" {
		t.Errorf("content 应为 result，得到 %v", got.Content)
	}
}

// TestListenerAppendLoadRoundTrip —— 完整往返：append → drain → load。
func TestListenerAppendLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("rt2", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	l := NewPersistListener(p, New("rt2"))
	c1, c2 := "q", "a"
	l.OnAppend(OaiMessage{Role: "user", Content: &c1})
	l.OnAppend(OaiMessage{Role: "assistant", Content: &c2})
	if err := l.Drain(); err != nil {
		t.Fatalf("Drain 失败：%v", err)
	}

	got := p.LoadOai()
	if len(got) != 2 {
		t.Fatalf("应读回 2 条，得到 %d", len(got))
	}
	if got[0].Role != "user" || got[1].Role != "assistant" {
		t.Errorf("顺序不符：%q, %q", got[0].Role, got[1].Role)
	}
}

// TestSessionDirLayout —— 会话目录布局对账。
func TestSessionDirLayout(t *testing.T) {
	got := SessionDir("/tmp/proj")
	want := filepath.Join("/tmp/proj", ".rivet", "sessions")
	if got != want {
		t.Errorf("会话目录应为 %q，得到 %q", want, got)
	}
}
