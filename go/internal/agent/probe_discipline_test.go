package agent

import (
	"context"
	"strings"
	"testing"
)

// 注：`recordingSink` 定义在 typecheck_reminder_test.go（同包共享）——此处复用。

// probeEv 构造一个只读工具事件。
func probeEv(name string) *RuntimeToolEvent {
	return &RuntimeToolEvent{Name: name, Success: true, Input: map[string]any{}}
}

// runProbe 按序跑一串工具名。
func runProbe(t *testing.T, h RuntimeHook, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := h.Run(context.Background(), &RuntimeHookContext{}, probeEv(n)); err != nil {
			t.Fatalf("hook 返回错误（%s）: %v", n, err)
		}
	}
}

// TestProbeDisciplineFiresAfterThreshold —— 连续 5 个只读 → 注入 1 条。
//
// 对账 TS `fires after 5 consecutive read-only tools`。
func TestProbeDisciplineFiresAfterThreshold(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "read_file", "grep", "glob", "read_file", "repo_map")

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条注入，实得 %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Category != CategoryDiscipline {
		t.Errorf("category = %q，期望 %q", e.Category, CategoryDiscipline)
	}
	if e.Priority != 0.5 {
		t.Errorf("priority = %v，期望 0.5", e.Priority)
	}
	if e.Tier != TierOperational {
		t.Errorf("tier = %q，期望 %q", e.Tier, TierOperational)
	}
	if e.TTL != 2 {
		t.Errorf("TTL = %d，期望 2", e.TTL)
	}
	// **本 hook 的通道是 SR**——决定它绕过 CVM 注入预算。
	if e.Channel != ChannelSystemReminder {
		t.Errorf("channel = %q，期望 %q", e.Channel, ChannelSystemReminder)
	}
}

// TestProbeDisciplineBelowThreshold —— 未达阈值不注入。
//
// 对账 TS `does not fire before the threshold`。
func TestProbeDisciplineBelowThreshold(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "read_file", "grep")

	if len(sink.entries) != 0 {
		t.Fatalf("未达阈值不应注入，实得 %d 条", len(sink.entries))
	}
}

// TestProbeDisciplineWriteBreaksStreak —— 写类工具打断只读串。
//
// 对账 TS `a write/verify tool breaks the read streak`。
func TestProbeDisciplineWriteBreaksStreak(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "read_file", "grep", "bash", "read_file", "grep")

	if len(sink.entries) != 0 {
		t.Fatalf("只读串被打断，不应注入，实得 %d 条", len(sink.entries))
	}
}

// TestProbeDisciplineCooldown —— 12 次调用冷却窗口。
//
// 对账 TS `cooldown prevents repeated injection within 12 calls`。
// 序列：第 5 次触发 → 再 11 次仍在窗口内（不注入）→ 第 17 次跨窗再注入。
func TestProbeDisciplineCooldown(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "read_file", "read_file", "read_file", "read_file", "read_file")
	if len(sink.entries) != 1 {
		t.Fatalf("第 5 次应触发，实得 %d 条", len(sink.entries))
	}

	for i := 0; i < 11; i++ {
		runProbe(t, h, "read_file")
	}
	if len(sink.entries) != 1 {
		t.Fatalf("冷却窗口内不应重复注入，实得 %d 条", len(sink.entries))
	}

	runProbe(t, h, "read_file")
	if len(sink.entries) != 2 {
		t.Fatalf("跨冷却窗口应再次注入，实得 %d 条", len(sink.entries))
	}
}

// TestProbeDisciplineZeroAnchorFiresEvidence —— 零锚点只读串 → 催「取证」。
//
// 对账 TS `zero-anchor read streak fires 取证 (evidence-first), not plain probe`。
func TestProbeDisciplineZeroAnchorFiresEvidence(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	// 全是裸只读（read_file / grep / glob / repo_map 都不在锚点集里）
	runProbe(t, h, "read_file", "grep", "glob", "read_file", "repo_map")

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条注入，实得 %d", len(sink.entries))
	}
	c := sink.entries[0].Content
	if !strings.Contains(c, "取证") {
		t.Errorf("零锚点应催取证，实际文案: %s", c)
	}
	if !strings.Contains(c, "锚点") {
		t.Errorf("零锚点文案应提到「锚点」，实际: %s", c)
	}
}

// TestProbeDisciplineAnchoredFiresProbe —— 有锚点只读串 → 催「探针」。
//
// 对账 TS `anchored read streak fires plain probe`。
//
// **注意断言不能只用 Contains("探针")**——取证文案末句也含「探针」
// （「探针杀假设，但杀不了编出来的假设」）。故必须同时断言
// **不含**「锚点」（仅取证文案含）。
func TestProbeDisciplineAnchoredFiresProbe(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	// read_section 恒算锚点；其余为普通只读
	runProbe(t, h, "read_section", "grep", "glob", "read_file", "repo_map")

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条注入，实得 %d", len(sink.entries))
	}
	c := sink.entries[0].Content
	if !strings.Contains(c, "探针") {
		t.Errorf("有锚点应催探针，实际文案: %s", c)
	}
	if strings.Contains(c, "锚点") {
		t.Errorf("有锚点不应催取证（文案含「锚点」），实际: %s", c)
	}
}

// TestProbeDisciplineAnchoredByLSPTools —— lsp_* 也计入锚点集。
//
// **Go 侧前瞻项**：lsp_goto_definition / lsp_find_references 在 Go 工具表里
// 尚不存在，但判定集保留了它们（对账 TS）。本测试锁住「判定集含它们」这一
// 事实——若将来误删，此测试转红。
func TestProbeDisciplineAnchoredByLSPTools(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "lsp_goto_definition", "grep", "glob", "read_file", "repo_map")

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条注入，实得 %d", len(sink.entries))
	}
	if strings.Contains(sink.entries[0].Content, "锚点") {
		t.Error("lsp_goto_definition 应算锚点，走探针分支")
	}
}

// TestProbeDisciplineAnchoredReadsResetByWrite —— 写工具同时重置锚点计数。
//
// 不变量：`anchoredReads` 与 `readStreak` **同步重置**——若只重置 streak
// 而漏了 anchoredReads，上一轮的锚点会「漏」进下一轮，让本该催取证的
// 裸读串误走探针分支。
func TestProbeDisciplineAnchoredReadsResetByWrite(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	// 先积累锚点（read_section），再用写工具打断
	runProbe(t, h, "read_section", "bash")
	// 之后全是裸读——anchoredReads 应已归零 → 催取证
	runProbe(t, h, "read_file", "grep", "glob", "read_file", "repo_map")

	if len(sink.entries) != 1 {
		t.Fatalf("期望 1 条注入，实得 %d", len(sink.entries))
	}
	if !strings.Contains(sink.entries[0].Content, "锚点") {
		t.Error("写工具后锚点计数应归零，本轮应催取证")
	}
}

// TestProbeDisciplineKeysAreUniqueAcrossInjections —— **key 唯一性不变量**。
//
// 这是 Go 侧移植的**关键设计点**（对账 TS 的 `Date.now()` 动态 key）：
//
//	TTL = 2 → 条目进 `alive` 池存活到下一轮渲染。若 key 固定，第二次注入的
//	条目会与 alive 里的旧条目**撞键**，去重时（同优先级保留先出现者）
//	**新内容被静默丢弃**——第二次注入形同虚设。
//
// 本测试断言：跨冷却窗口的两次注入 key **不相同**。
func TestProbeDisciplineKeysAreUniqueAcrossInjections(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	// 第一次触发
	runProbe(t, h, "read_file", "read_file", "read_file", "read_file", "read_file")
	// 跨过冷却窗口
	for i := 0; i < 12; i++ {
		runProbe(t, h, "read_file")
	}

	if len(sink.entries) != 2 {
		t.Fatalf("期望 2 次注入，实得 %d", len(sink.entries))
	}
	if sink.entries[0].Key == sink.entries[1].Key {
		t.Errorf("两次注入 key 相同（%q）——TTL=2 时会与 alive 池撞键致新条目被去重吞掉",
			sink.entries[0].Key)
	}
}

// TestProbeDisciplineSRChannelSurvivesBusDedup —— **端到端：SR 条目不被去重吞掉**。
//
// 这是本 hook 与第四十五刀 SR 通道的**集成不变量**：hook 投 TTL=2 的 SR 条目，
// 经真实 bus 渲染两轮——两轮都应渲染出内容（第二轮来自 alive 池）。
//
// 若 key 固定，第二轮的新内容会与 alive 撞键；本测试锁住「key 唯一」的
// 端到端后果。
func TestProbeDisciplineSRChannelSurvivesBusDedup(t *testing.T) {
	bus := NewAdvisoryBus()
	h := NewProbeDisciplineHook(bus)

	runProbe(t, h, "read_file", "read_file", "read_file", "read_file", "read_file")

	first := bus.Render("", 0)
	if !strings.Contains(first, "取证") {
		t.Fatalf("第一轮应渲染取证条目，实际:\n%s", first)
	}
	// TTL=2 → 条目进 alive，第二轮仍应渲染
	second := bus.Render("", 1)
	if !strings.Contains(second, "取证") {
		t.Fatalf("TTL=2 的第二轮应仍渲染（alive 池），实际:\n%s", second)
	}
}

// TestProbeDisciplineNilBusDoesNotPanic —— bus 为 nil 时安全 no-op。
//
// 不变量：**先消费触发再检查出口**（对账 TS 的重置时机）——nil bus 时
// 计数仍应被消费，避免每轮都试图注入。
func TestProbeDisciplineNilBusDoesNotPanic(t *testing.T) {
	h := NewProbeDisciplineHook(nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil bus 不应 panic: %v", r)
		}
	}()

	runProbe(t, h, "read_file", "read_file", "read_file", "read_file", "read_file")
}

// TestProbeDisciplineNilToolDoesNotPanic —— 事件为 nil 时安全 no-op。
func TestProbeDisciplineNilToolDoesNotPanic(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil tool 不应 panic: %v", r)
		}
	}()

	if err := h.Run(context.Background(), &RuntimeHookContext{}, nil); err != nil {
		t.Fatalf("nil tool 应静默返回，实得错误: %v", err)
	}
}

// TestProbeDisciplineUnknownToolBreaksStreak —— 未在只读集里的工具视为写类。
//
// 不变量：判定集是**白名单**——未知工具名必须打断只读串（保守：宁可漏提醒，
// 不可在写操作期间误报「取证停滞」）。
func TestProbeDisciplineUnknownToolBreaksStreak(t *testing.T) {
	sink := &recordingSink{}
	h := NewProbeDisciplineHook(sink)

	runProbe(t, h, "read_file", "read_file", "some_new_tool", "read_file", "read_file")

	if len(sink.entries) != 0 {
		t.Fatalf("未知工具应打断只读串，实得 %d 条注入", len(sink.entries))
	}
}
