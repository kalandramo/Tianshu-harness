package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestHoldoutProducesShadowSamples —— **holdout 在真实 loop 里产生 shadow 样本**。
//
// 这是本刀的**核心验收**：holdout 是 shadow 样本的唯一来源，而 shadow 是
// lift 的分母。此前 Go 侧 shadow 恒为 0 → GetMatureLift 恒 nil → lift 静音
// 永不触发（上一刀标 blocked 的那条）。
//
// 用户动作：一条合格的提醒（历史送达 >= 3）在**首个 user 轮**被投递，抽样率 100%。
// 观察到：`Stats()` 里该 key 的 **ShadowHeld > 0**（扣留样本被记录）。
//
// **构造要点**（避免「一个 Run 内多轮只送达一次」的时序陷阱）：
// 用 **preTurn** 投递——第 1 轮的 render 立即取到条目；且只需一个 Run。
func TestHoldoutProducesShadowSamples(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 播种先验让 key 有资格（历史送达 >= 3）
	rb.SeedPriors(map[string]EfficacyPriorCounts{"hot": {Delivered: 5}})

	// 抽样率 100%（必然扣留），资格门与生产一致
	bus.SetHoldoutPolicy(HoldoutPolicy{
		Rate:       1.0,
		IsEligible: func(k string) bool { return rb.GetDeliveredCount(k) >= HoldoutMinDelivered },
	})

	// **preTurn** 投递——第 1 轮 render 立即取到
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePreTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "hot", Priority: 0.6, Category: CategoryDiscipline, Content: "提醒",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}},
			})
			return nil
		},
	})
	l.Hooks = p

	if err := l.Run(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}

	s := rb.Stats()["hot"]
	t.Logf("stats=%+v", s)

	// ── 观察：shadow 样本被记录 ──
	if s.ShadowHeld == 0 {
		t.Errorf("**holdout 未产生 shadow 样本**（ShadowHeld=0）\n"+
			"（没有它，GetMatureLift 恒 nil → 负 lift 静音永不触发）\nstats=%+v", s)
	}
	// 扣留 ≠ 送达：抽样率 100% 时不应有真实送达
	if s.Delivered > 0 {
		t.Errorf("抽样率 100%% 时不该有真实送达，得到 Delivered=%d", s.Delivered)
	}
}

// TestHoldoutDisabledByRateZero —— **rate=0 关闭抽样**。
//
// 用户动作：抽样率设为 0（对应 RIVET_ADVISORY_HOLDOUT=0）。
// 观察到：提醒正常渲染，不产生 shadow 样本。
func TestHoldoutDisabledByRateZero(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("r1"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb
	rb.SeedPriors(map[string]EfficacyPriorCounts{"hot": {Delivered: 5}})

	// rate = 0（对账 ParseHoldoutRate("0") 的语义）
	bus.SetHoldoutPolicy(HoldoutPolicy{
		Rate:       ParseHoldoutRate("0"),
		IsEligible: func(k string) bool { return true },
	})

	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "hot", Priority: 0.6, Category: CategoryDiscipline, Content: "提醒",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}},
			})
			return nil
		},
	})
	l.Hooks = p

	if err := l.Run(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}

	s := rb.Stats()["hot"]
	if s.ShadowHeld != 0 {
		t.Errorf("rate=0 应关闭抽样，得到 ShadowHeld=%d", s.ShadowHeld)
	}
	if s.Delivered == 0 {
		t.Error("rate=0 时提醒应正常送达，得到 Delivered=0")
	}
}

// TestParseHoldoutRate —— **env 解析语义**（对账 parseHoldoutRate）。
func TestParseHoldoutRate(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{"", 0.1},    // 未设置 → 默认
		{"0", 0},     // 显式关闭
		{"0.5", 0.5}, // 合法值
		{"1", 1},     // 边界
		{"2", 0.1},   // 越界 → 默认
		{"-1", 0.1},  // 负数 → 默认
		{"abc", 0.1}, // 非法 → 默认
		{"NaN", 0.1}, // NaN → 默认
		{"Inf", 0.1}, // Inf → 默认
	}
	for _, c := range cases {
		if got := ParseHoldoutRate(c.raw); got != c.want {
			t.Errorf("ParseHoldoutRate(%q) = %v，期望 %v", c.raw, got, c.want)
		}
	}
}

// TestHoldoutEligibilityWhitelist —— **holdout 资格白名单**（逐类判别）。
//
// 对账 TS 的资格判定（advisory-bus.ts:1147-1151）——四类不扣留：
//
//	constitutional tier / immediate / star_domain / **无 expect 谓词**
//
// 用抽样率 100%（必然命中）隔离资格判定本身：只有不合格的才留下。
//
// **为什么必须逐类测**：白名单是四个独立条件，任一失效都会让不该扣留的条目
// 被扣留（如宪法级约束被静默）——后果严重但单点测试易漏。
func TestHoldoutEligibilityWhitelist(t *testing.T) {
	expect := &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}}

	cases := []struct {
		name     string
		entry    AdvisoryEntry
		eligible bool // isEligible 返回值
		wantHeld bool // 是否应被扣留
	}{
		{"普通条目（合格）", AdvisoryEntry{Key: "k", Tier: TierOperational, Category: CategoryDiscipline, Expect: expect}, true, true},
		{"constitutional 豁免", AdvisoryEntry{Key: "k", Tier: TierConstitutional, Category: CategoryDiscipline, Expect: expect}, true, false},
		{"immediate 豁免", AdvisoryEntry{Key: "k", Tier: TierOperational, Category: CategoryRepair, Immediate: true, Expect: expect}, true, false},
		{"star_domain 豁免", AdvisoryEntry{Key: "k", Tier: TierOperational, Category: CategoryStarDomain, Expect: expect}, true, false},
		{"无 expect 谓词豁免", AdvisoryEntry{Key: "k", Tier: TierOperational, Category: CategoryDiscipline, Expect: nil}, true, false},
		{"isEligible 为 false 豁免", AdvisoryEntry{Key: "k", Tier: TierOperational, Category: CategoryDiscipline, Expect: expect}, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := NewAdvisoryBus()
			bus.SetHoldoutPolicy(HoldoutPolicy{
				Rate:       1.0, // 必然命中
				IsEligible: func(string) bool { return c.eligible },
				RNG:        func() float64 { return 0.0 },
			})
			e := c.entry
			e.Priority = 0.6
			e.Content = "X"
			bus.Submit(e)
			bus.Render("", 0)

			ledger := bus.DrainLedger()
			held := ledger.HeldOut > 0
			if held != c.wantHeld {
				t.Errorf("HeldOut=%v，期望 %v（Rate=1.0，资格判定是唯一变量）", held, c.wantHeld)
			}
		})
	}
}
