package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// TestCompactBoundary_TriggersOnPressure —— **接线集成测试**。
//
// 用**真实依赖**（真实 `CompactBoundary` + 真实 `compact` 包），不是 mock：
// 构造一份超窗口的历史，断言压缩真的改动了消息列表。
//
// 对账 TS：turn-orchestrator Step 6b → maybeCompact 的端到端行为。
func TestCompactBoundary_TriggersOnPressure(t *testing.T) {
	// 构造历史：锚（前 2）+ 中间大 tool 结果 + 近 4 条。
	big := strings.Repeat("x", 300000) // 约 75K token（按 4 字符/token）
	msgs := []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "锚用户"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "中间用户"),
		wire.NewOrderedMap().Set("role", "assistant").
			Set("content", "").
			Set("tool_calls", []any{
				wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
					Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").Set("content", big),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手1"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户1"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手2"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户2"),
	}

	// 小窗口 → 必然触发压缩。
	b := NewCompactBoundary(100000)
	oai := orderedMapsToOai(msgs)

	compacted, changed := b.MaybeCompact(oai, 0)

	if !changed {
		t.Fatalf("超窗口历史应触发压缩，但 changed=false（决策=%+v）", b.LastDecision)
	}
	if b.LastReclaimed == 0 {
		t.Errorf("LastReclaimed 应为正数，实得 0")
	}
	// 压缩后总 token 应下降。
	before := 0
	for _, m := range oai {
		before += estimateForTest(m)
	}
	after := 0
	for _, m := range compacted {
		after += estimateForTest(m)
	}
	if after >= before {
		t.Errorf("压缩后 token 应下降：before=%d after=%d", before, after)
	}
	t.Logf("压缩：%d 条 → %d 条，回收 %d，token %d → %d",
		len(oai), len(compacted), b.LastReclaimed, before, after)
}

// TestCompactBoundary_NoOpWhenBelowThreshold —— **无压力时不改动历史**。
//
// 对账 TS：`action === 'none'` → 短路返回 `compacted: false`。
func TestCompactBoundary_NoOpWhenBelowThreshold(t *testing.T) {
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtr("你好")},
		{Role: "assistant", Content: strPtr("你好，有什么可以帮你")},
	}
	b := NewCompactBoundary(1000000) // 大窗口 → 不触发

	compacted, changed := b.MaybeCompact(msgs, 0)

	if changed {
		t.Errorf("低压力下不应压缩，但 changed=true")
	}
	if len(compacted) != len(msgs) {
		t.Errorf("消息数不应变：%d → %d", len(msgs), len(compacted))
	}
	if b.LastDecision == nil {
		t.Fatal("LastDecision 应被记录（供观测）")
	}
	if b.LastDecision.Action != compact.ActionNone {
		t.Errorf("决策应为 none，实得 %q", b.LastDecision.Action)
	}
}

// TestCompactBoundary_CircuitBreakerSkips —— **熔断器开启时跳过压缩**。
//
// 对账 TS：`failures.disabledUntilTurn > turn` 时不压缩（非 force 路径）。
func TestCompactBoundary_CircuitBreakerSkips(t *testing.T) {
	big := strings.Repeat("x", 300000)
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtr("锚")},
		{Role: "assistant", Content: strPtr("锚助手")},
		{Role: "user", Content: strPtr("中")},
		{Role: "assistant", Content: strPtr("")},
		{Role: "tool", Content: &big, ToolCallID: "read_file_1"},
		{Role: "assistant", Content: strPtr("近1")},
		{Role: "user", Content: strPtr("近2")},
		{Role: "assistant", Content: strPtr("近3")},
		{Role: "user", Content: strPtr("近4")},
	}
	b := NewCompactBoundary(100000)
	disabled := 10
	b.Failures = compact.CompactCircuitBreakerState{
		ConsecutiveFailures: 3,
		DisabledUntilTurn:   &disabled,
	}

	_, changed := b.MaybeCompact(msgs, 5) // turn 5 < disabledUntil 10

	if changed {
		t.Errorf("熔断器开启（turn=5 < disabledUntil=10）时应跳过压缩，但 changed=true")
	}
}

// TestCompactBoundary_ForceOverridesBreaker —— **超硬天花板时 force 优先于熔断器**。
//
// 对账 `DecideCompactAction` 的 ceiling 分支：超窗口请求是硬 API 失败，
// 熔断器不该拦住它。
func TestCompactBoundary_ForceOverridesBreaker(t *testing.T) {
	big := strings.Repeat("x", 2000000) // 约 500K token，远超 100K 窗口
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtr("锚")},
		{Role: "assistant", Content: strPtr("锚助手")},
		{Role: "tool", Content: &big, ToolCallID: "read_file_1"},
	}
	b := NewCompactBoundary(100000)
	disabled := 10
	b.Failures = compact.CompactCircuitBreakerState{
		ConsecutiveFailures: 3,
		DisabledUntilTurn:   &disabled,
	}

	compacted, changed := b.MaybeCompact(msgs, 5)

	if b.LastDecision == nil {
		t.Fatal("LastDecision 应被记录")
	}
	if !b.LastDecision.Force {
		t.Errorf("超天花板应 force=true，实得 false（action=%q）", b.LastDecision.Action)
	}
	if !changed {
		t.Errorf("force 应绕过熔断器并执行压缩，但 changed=false")
	}
	_ = compacted
}

// TestCompactBoundary_SuccessResetsBreaker —— **成功后重置熔断器**。
func TestCompactBoundary_SuccessResetsBreaker(t *testing.T) {
	big := strings.Repeat("x", 300000)
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtr("锚")},
		{Role: "assistant", Content: strPtr("锚助手")},
		{Role: "user", Content: strPtr("中")},
		{Role: "assistant", Content: strPtr("")},
		{Role: "tool", Content: &big, ToolCallID: "read_file_1"},
		{Role: "assistant", Content: strPtr("近1")},
		{Role: "user", Content: strPtr("近2")},
		{Role: "assistant", Content: strPtr("近3")},
		{Role: "user", Content: strPtr("近4")},
	}
	b := NewCompactBoundary(100000)
	disabled := 3
	b.Failures = compact.CompactCircuitBreakerState{
		ConsecutiveFailures: 3,
		DisabledUntilTurn:   &disabled,
	}

	_, changed := b.MaybeCompact(msgs, 5) // turn 5 >= 3 → 熔断器已过期

	if !changed {
		t.Fatalf("熔断器过期后应压缩，但 changed=false")
	}
	if b.Failures.ConsecutiveFailures != 0 {
		t.Errorf("成功后熔断器应重置，实得 consecutiveFailures=%d", b.Failures.ConsecutiveFailures)
	}
	if b.Failures.DisabledUntilTurn != nil {
		t.Errorf("成功后 disabledUntilTurn 应被丢弃，实得 %v", *b.Failures.DisabledUntilTurn)
	}
}

// TestLoop_CompactAtBoundary_WiresThrough —— **loop 接线端到端**。
//
// 验证 `maybeCompactAtBoundary` 真的把压缩结果写回 `l.messages`，
// 且键序未被破坏（前缀缓存安全的前提）。
func TestLoop_CompactAtBoundary_WiresThrough(t *testing.T) {
	big := strings.Repeat("x", 300000)
	l := &Loop{
		cfg: Config{Model: "test", MaxTokens: 1000},
		messages: []*wire.OrderedMap{
			wire.NewOrderedMap().Set("role", "user").Set("content", "锚"),
			wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
			wire.NewOrderedMap().Set("role", "user").Set("content", "中"),
			wire.NewOrderedMap().Set("role", "assistant").Set("content", "").
				Set("tool_calls", []any{
					wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
						Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
				}),
			wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").Set("content", big),
			wire.NewOrderedMap().Set("role", "assistant").Set("content", "近1"),
			wire.NewOrderedMap().Set("role", "user").Set("content", "近2"),
			wire.NewOrderedMap().Set("role", "assistant").Set("content", "近3"),
			wire.NewOrderedMap().Set("role", "user").Set("content", "近4"),
		},
		Compact: NewCompactBoundary(100000),
	}
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	// **截断路径不改消息数**（只改内容）；轮次删除才改数量。
	// 本用例的窗口 100000 对应 `micro` 决策 → 走截断。
	beforeLen := contentLenOf(l.messages, "tool")
	l.maybeCompactAtBoundary(0)
	afterLen := contentLenOf(l.messages, "tool")

	if afterLen >= beforeLen {
		t.Fatalf("压缩后 tool 内容应缩短：%d → %d", beforeLen, afterLen)
	}
	if len(l.messages) != 9 {
		t.Errorf("截断路径不应改消息数，实得 %d", len(l.messages))
	}

	// 事件应被发出（可观测性）。
	found := false
	for _, e := range events {
		if e.Kind == "compaction" {
			found = true
		}
	}
	if !found {
		t.Errorf("应发出 compaction 事件，实得 %d 个事件", len(events))
	}

	// **键序完整性**：压缩后每条消息的首键应是 role（原始序）。
	for i, m := range l.messages {
		keys := m.Keys()
		if len(keys) == 0 {
			t.Errorf("第 %d 条消息无键", i)
			continue
		}
		if keys[0] != "role" {
			t.Errorf("第 %d 条消息首键应为 role，实得 %q（键序=%v）", i, keys[0], keys)
		}
	}
}

// TestCompactBoundary_ZeroReclaimReportsUnchanged —— **零回收时报告未改动**。
//
// 对账 TS：`compactMessages` 返回的 `truncated` 为 0 时不回写历史——
// 「决策说该压缩」不等于「真的压出了东西」。
//
// **为什么这条重要**：若不检查 `Truncated == 0`，调用方会拿到
// `changed=true` 但内容原封不动——上游会误判「已压缩」，导致：
//   - 无谓的对象重建（每次都重建消息列表）
//   - 熔断器被误重置（把「没压出东西」当成功）
//   - 观测事件误报（emit 一条"压缩了 0 条"的事件）
//
// 构造：决策走 `micro`（ratio 够高）但 tool 内容都低于 previewChars
// （100K 窗口 → 30000 字符），故截断不触发、轮次删除也不够本。
func TestCompactBoundary_ZeroReclaimReportsUnchanged(t *testing.T) {
	// **构造要点**（探针实测标定，非推算）：
	//
	//   - ratio = 0.563，落在 `[Watch=0.5, Compact=0.7)` → 决策 `micro` tier=1
	//   - 近 4 条含 3 条大 assistant 消息（30000 ASCII 字符 = 7500 token 各）
	//     ——**assistant 不走截断路径**，故无内容可截
	//   - 可删区（锚之后、近 4 条之前）只有 1 个小轮次，删掉后
	//     `22511 - 1 = 22510` 仍 < `70%×40000 = 28000` → **不够本，不删**
	//   - 合计：决策说该压，但零回收 ✓
	//
	// **为什么必须探针标定**：`context` 的估算器对 ASCII 按 `len/4`、CJK 按
	// `len/1.5`，且 assistant 只算 content+reasoning+tool_calls。推算容易差
	// 一个数量级（我曾按 30000 字符 = 30000 token 估算，实际是 7500）。
	const win = 40000
	bigAsst := strings.Repeat("y", 30000)
	msgs := []session.OaiMessage{
		{Role: "user", Content: strPtr("锚")},
		{Role: "assistant", Content: strPtr("锚助手")},
		{Role: "user", Content: strPtr("mid")},
		{Role: "assistant", Content: strPtr("ok")},
		{Role: "assistant", Content: &bigAsst},
		{Role: "user", Content: strPtr("近2")},
		{Role: "assistant", Content: &bigAsst},
		{Role: "user", Content: strPtr("近3")},
		{Role: "assistant", Content: &bigAsst},
		{Role: "user", Content: strPtr("近4")},
	}
	b := NewCompactBoundary(win)

	compacted, changed := b.MaybeCompact(msgs, 0)

	if b.LastDecision == nil {
		t.Fatal("LastDecision 应被记录")
	}
	if b.LastDecision.Action == compact.ActionNone {
		t.Fatalf("决策为 none——本用例需 ratio 达到 micro 档（win=%d）", win)
	}
	if changed {
		t.Errorf("零回收时应报告 changed=false，实得 true（LastReclaimed=%d）", b.LastReclaimed)
	}
	if len(compacted) != len(msgs) {
		t.Errorf("零回收时消息数不应变：%d → %d", len(msgs), len(compacted))
	}
	if b.LastReclaimed != 0 {
		t.Errorf("LastReclaimed 应为 0，实得 %d", b.LastReclaimed)
	}
}

// TestCompactBoundary_NilSafe —— **nil 边界安全**（未装配时跳过）。
func TestCompactBoundary_NilSafe(t *testing.T) {
	l := &Loop{messages: []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "a"),
	}}
	l.maybeCompactAtBoundary(0) // Compact == nil
	if len(l.messages) != 1 {
		t.Errorf("未装配时不应改动消息，实得 %d 条", len(l.messages))
	}
}

func strPtr(s string) *string { return &s }

// estimateForTest 复刻 rounds 口径的估算（仅供本测试内部比较用）。
func estimateForTest(m session.OaiMessage) int {
	n := 0
	if m.Content != nil {
		n = len(*m.Content)
	}
	if m.Extra != nil {
		if rc, ok := m.Extra["reasoning_content"].(string); ok {
			n += len(rc)
		}
	}
	return (n + 3) / 4
}

// contentLenOf 返回指定 role 的消息内容总长度。
func contentLenOf(msgs []*wire.OrderedMap, role string) int {
	total := 0
	for _, m := range msgs {
		r, _ := m.Get("role")
		if r != role {
			continue
		}
		if v, ok := m.Get("content"); ok {
			if s, ok := v.(string); ok {
				total += len(s)
			}
		}
	}
	return total
}
