package agent

import (
	"context"
	"strings"
	"testing"
)

// ── reasoning-spiral hook ──

// spySink 捕获提交的 advisory。
type spySink struct{ entries []AdvisoryEntry }

func (s *spySink) Submit(e AdvisoryEntry) { s.entries = append(s.entries, e) }

// 长推理 + 无工具 → 提交 advisory。
func TestSpiralFiresOnLongThinkingNoTools(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})

	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 4000, LastTurnHadTools: false},
	}, nil)

	if len(sink.entries) != 1 {
		t.Fatalf("应提交 1 条 advisory，得到 %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Key != "reasoning-spiral" {
		t.Errorf("key = %q", e.Key)
	}
	if e.Priority != 0.54 {
		t.Errorf("priority = %v, want 0.54", e.Priority)
	}
	if e.TTL != 1 {
		t.Errorf("ttl = %d, want 1", e.TTL)
	}
	if e.Category != CategoryDiscipline {
		t.Errorf("category = %q", e.Category)
	}
	if !strings.Contains(e.Content, "4.0K") {
		t.Errorf("应含格式化长度 4.0K：%q", e.Content)
	}
}

// **反证**：短推理不触发。
func TestSpiralSilentOnShortThinking(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 2999, LastTurnHadTools: false},
	}, nil)
	if len(sink.entries) != 0 {
		t.Errorf("2999 < 3000 不应触发，得到 %d 条", len(sink.entries))
	}
}

// **反证**：有工具调用不触发（无论推理多长）。
func TestSpiralSilentWhenToolsUsed(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 9000, LastTurnHadTools: true},
	}, nil)
	if len(sink.entries) != 0 {
		t.Errorf("有工具调用不应触发，得到 %d 条", len(sink.entries))
	}
}

// **反证**：无上一轮数据（-1）不触发。
func TestSpiralSilentWithoutPriorTurn(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 0, LastThinkingLength: -1},
	}, nil)
	if len(sink.entries) != 0 {
		t.Errorf("无上一轮数据不应触发，得到 %d 条", len(sink.entries))
	}
}

// Cooldown：触发后 2 轮内不再提交。
func TestSpiralCooldown(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})

	// 第 1 轮触发
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 4000},
	}, nil)
	if len(sink.entries) != 1 {
		t.Fatalf("首轮应触发")
	}
	// 第 2 轮（turn=2，距上次 1 < 2）→ 冷却
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 2, LastThinkingLength: 4000},
	}, nil)
	if len(sink.entries) != 1 {
		t.Errorf("冷却期内不应再触发，得到 %d 条", len(sink.entries))
	}
	// 第 3 轮（turn=3，距上次 2 >= 2）→ 可再触发
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 3, LastThinkingLength: 4000},
	}, nil)
	if len(sink.entries) != 2 {
		t.Errorf("冷却过后应再触发，得到 %d 条", len(sink.entries))
	}
}

// 升级检测：连续递增的长推理 → 升级文案。
//
// **turn 序列必须避开初始冷却期**：`lastAdvisoryTurn` 初值 -1，故
// `Turn=0` 时 `0-(-1)=1 < 2` 落在冷却内**不触发**——首版测试用 Turn=0
// 起步，导致趋势数组始终为空，M174/M175 变异 0 红。从 Turn=2 起。
func TestSpiralEscalatingWording(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})

	// 三次触发（间隔 >= cooldown），长度递增
	for i, ln := range []int{4000, 5000, 6000} {
		h.Run(context.Background(), &RuntimeHookContext{
			Snapshot: &RuntimeHookSnapshot{Turn: 2 + i*3, LastThinkingLength: ln},
		}, nil)
	}
	if len(sink.entries) != 3 {
		t.Fatalf("应触发 3 次，得到 %d（turn 序列或冷却有问题）", len(sink.entries))
	}
	last := sink.entries[len(sink.entries)-1]
	if !strings.Contains(last.Content, "推理链在自我放大") {
		t.Errorf("递增序列应产出升级文案：%q", last.Content)
	}
}

// **反证**：非递增序列不产升级文案。
func TestSpiralNonEscalatingWording(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})
	for i, ln := range []int{6000, 4000} {
		h.Run(context.Background(), &RuntimeHookContext{
			Snapshot: &RuntimeHookSnapshot{Turn: 2 + i*3, LastThinkingLength: ln},
		}, nil)
	}
	if len(sink.entries) != 2 {
		t.Fatalf("应触发 2 次，得到 %d", len(sink.entries))
	}
	last := sink.entries[len(sink.entries)-1]
	if strings.Contains(last.Content, "推理链在自我放大") {
		t.Errorf("非递增不应产升级文案：%q", last.Content)
	}
}

// 短推理重置趋势（中断递增链）。
//
// **这是 M174 的判别用例**：变异「短推理不重置」会让 [4000, 9000] 构成
// 递增 → 误产升级文案。首版测试用 Turn=0 起步落进初始冷却期，该变异逃逸。
func TestSpiralShortThinkingResetsTrend(t *testing.T) {
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})

	// 第 1 次触发（Turn=2，避开初始冷却）
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 2, LastThinkingLength: 4000},
	}, nil)
	if len(sink.entries) != 1 {
		t.Fatalf("首次应触发，得到 %d", len(sink.entries))
	}
	// 短推理 → 重置 recentLengths
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 5, LastThinkingLength: 100},
	}, nil)
	// 再长推理 → 趋势只剩 1 项，不构成递增
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 8, LastThinkingLength: 9000},
	}, nil)

	if len(sink.entries) != 2 {
		t.Fatalf("应触发 2 次，得到 %d", len(sink.entries))
	}
	last := sink.entries[len(sink.entries)-1]
	if strings.Contains(last.Content, "推理链在自我放大") {
		t.Errorf("趋势应已被短推理重置：%q", last.Content)
	}
}

// Bus 为 nil 时不 panic（退化为 no-op）。
func TestSpiralNilBusNoPanic(t *testing.T) {
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: nil})
	if err := h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 9000},
	}, nil); err != nil {
		t.Errorf("nil bus 不应报错：%v", err)
	}
}

// 快照为 nil 时不 panic。
func TestSpiralNilSnapshotNoPanic(t *testing.T) {
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: &spySink{}})
	if err := h.Run(context.Background(), nil, nil); err != nil {
		t.Errorf("nil 快照不应报错：%v", err)
	}
}

// 义务分支：有未决义务时点名具体动作。
func TestSpiralObligationBranch(t *testing.T) {
	sink := &spySink{}
	ob := &fakeObligations{items: []Obligation{{
		ID: "o1", Claim: "改了 TS 但没 typecheck",
		RequiredAction: "跑 npm run typecheck", Targets: []string{"src/a.ts"},
	}}}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink, Obligations: ob})
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1, LastThinkingLength: 4000},
	}, nil)

	if len(sink.entries) != 1 {
		t.Fatalf("应提交 1 条")
	}
	c := sink.entries[0].Content
	for _, want := range []string{"改了 TS 但没 typecheck", "跑 npm run typecheck", "src/a.ts"} {
		if !strings.Contains(c, want) {
			t.Errorf("义务分支文案应含 %q：%q", want, c)
		}
	}
	if !ob.recorded {
		t.Error("应登记一次无新证据尝试")
	}
}

type fakeObligations struct {
	items    []Obligation
	recorded bool
}

func (f *fakeObligations) UnresolvedHigh() []Obligation { return f.items }
func (f *fakeObligations) RecordAttempt(string)         { f.recorded = true }

// formatLen 逐字对账 TS。
func TestFormatLen(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1.0K", 4000: "4.0K", 4500: "4.5K", 12345: "12.3K"}
	for in, want := range cases {
		if got := formatLen(in); got != want {
			t.Errorf("formatLen(%d) = %q, want %q", in, got, want)
		}
	}
}

// ── wedge-loop guard ──

func mkCalls(names ...string) []toolCall {
	out := make([]toolCall, 0, len(names))
	for i, n := range names {
		out = append(out, toolCall{id: "c" + string(rune('0'+i)), name: n, input: map[string]any{}})
	}
	return out
}

// 连续 3 次同批次全错 → 终止。
func TestWedgeTerminatesAfterThreeRepeats(t *testing.T) {
	var s wedgeState
	calls := mkCalls("bash")
	fp := toolBatchFingerprint(calls)

	for i := 1; i <= 2; i++ {
		s.observeBatch(true, fp)
		if s.shouldTerminate() {
			t.Fatalf("第 %d 次不应终止（阈值 3）", i)
		}
	}
	s.observeBatch(true, fp)
	if !s.shouldTerminate() {
		t.Errorf("第 3 次应终止，repeatCount=%d", s.repeatCount)
	}
}

// **反证**：非全错不累积。
func TestWedgeResetOnSuccess(t *testing.T) {
	var s wedgeState
	fp := toolBatchFingerprint(mkCalls("bash"))
	s.observeBatch(true, fp)
	s.observeBatch(true, fp)
	s.observeBatch(false, "") // 有成功
	if s.repeatCount != 0 {
		t.Errorf("成功后应重置为 0，得到 %d", s.repeatCount)
	}
}

// **反证**：非全错**且指纹相同**也不累积——这是 M177 的判别用例。
//
// **为什么需要单独一条**：`TestWedgeResetOnSuccess` 传的是**空指纹**
// （对账 TS 的 `allErrored ? fp : ”`），与已存的非空指纹天然不同，走
// else 分支重置——变异「去掉 allErrored 判定」在那里与正常产出**相同**，
// 故逃逸。本用例传**相同指纹**，把 `allErrored` 变成唯一判别因素。
//
// 调用方语义：非全错时调用方应传空串；但守卫自身**必须**不依赖这一点
// （防御性——传错指纹不应导致误终止）。
func TestWedgeNonErroredSameFingerprintDoesNotAccumulate(t *testing.T) {
	var s wedgeState
	fp := toolBatchFingerprint(mkCalls("bash"))
	// 先建立 2 次全错
	s.observeBatch(true, fp)
	s.observeBatch(true, fp)
	if s.repeatCount != 2 {
		t.Fatalf("前置：应为 2，得到 %d", s.repeatCount)
	}
	// 非全错但指纹相同 → 不得累积
	s.observeBatch(false, fp)
	if s.repeatCount != 0 {
		t.Errorf("非全错不应累积（即便指纹相同），得到 %d", s.repeatCount)
	}
	if s.shouldTerminate() {
		t.Error("非全错不应触发终止")
	}
}

// **反证**：全错但批次不同 → 重置为新序列第 1 次（**不是 0**）。
func TestWedgeDifferentBatchResetsToOne(t *testing.T) {
	var s wedgeState
	s.observeBatch(true, toolBatchFingerprint(mkCalls("bash")))
	s.observeBatch(true, toolBatchFingerprint(mkCalls("grep")))
	if s.repeatCount != 1 {
		t.Errorf("批次不同应重置为 1，得到 %d", s.repeatCount)
	}
}

// **反证**：交错的不同批次永不达阈值。
func TestWedgeInterleavedNeverTerminates(t *testing.T) {
	var s wedgeState
	a := toolBatchFingerprint(mkCalls("bash"))
	b := toolBatchFingerprint(mkCalls("grep"))
	for i := 0; i < 10; i++ {
		s.observeBatch(true, a)
		s.observeBatch(true, b)
		if s.shouldTerminate() {
			t.Fatalf("交错批次不应终止（第 %d 轮）", i)
		}
	}
}

// 指纹：同工具同参数 → 同指纹；参数不同 → 不同指纹。
func TestToolBatchFingerprint(t *testing.T) {
	a := []toolCall{{name: "read_file", input: map[string]any{"file_path": "x.ts"}}}
	b := []toolCall{{name: "read_file", input: map[string]any{"file_path": "x.ts"}}}
	c := []toolCall{{name: "read_file", input: map[string]any{"file_path": "y.ts"}}}

	if toolBatchFingerprint(a) != toolBatchFingerprint(b) {
		t.Error("同工具同参数应产出同指纹")
	}
	if toolBatchFingerprint(a) == toolBatchFingerprint(c) {
		t.Error("参数不同应产出不同指纹")
	}
}

// **反证**：顺序敏感——同工具不同顺序应产出不同指纹。
func TestToolBatchFingerprintOrderSensitive(t *testing.T) {
	a := toolBatchFingerprint(mkCalls("read_file", "grep"))
	b := toolBatchFingerprint(mkCalls("grep", "read_file"))
	if a == b {
		t.Error("批次顺序不同应产出不同指纹")
	}
}

// wedgeDetail 渲染。
func TestWedgeDetail(t *testing.T) {
	var s wedgeState
	s.repeatCount = 3
	got := s.wedgeDetail(mkCalls("bash", "grep"))
	if got != "bash,grep ×3" {
		t.Errorf("wedgeDetail = %q", got)
	}
}

// isStrictlyIncreasing 边界。
func TestIsStrictlyIncreasing(t *testing.T) {
	cases := []struct {
		v    []int
		want bool
	}{
		{nil, false}, {[]int{1}, false}, {[]int{1, 2}, true},
		{[]int{2, 1}, false}, {[]int{1, 2, 3}, true},
		{[]int{1, 2, 2}, false}, {[]int{1, 1}, false},
	}
	for _, c := range cases {
		if got := isStrictlyIncreasing(c.v); got != c.want {
			t.Errorf("isStrictlyIncreasing(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}
