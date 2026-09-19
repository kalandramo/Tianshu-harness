package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEfficacyCooldownInRealRequests —— **负反馈环的端到端验收**。
//
// 用户动作：一条**零采纳**的提醒连续多轮投递（会话内统计显示 delivered=3）。
// 观察到：该提醒在真实请求体里**间隔缺席**（冷却生效）——而不是每轮都占位。
//
// **接线依赖**：`bus.SetEfficacyStatsProvider(readback.Stats())`。若未接，
// 负反馈环不生效，零采纳提醒会一直渲染。
//
// **与习惯化静音的区别**（本刀的核心语义）：习惯化依赖 `ignoredStreak`，而它
// 依赖 expect 谓词——**无 expect 的 key（如 convergence 的多数变体）ignored
// 永远是 0，只有这条环能拦住它**。
func TestEfficacyCooldownInRealRequests(t *testing.T) {
	root := t.TempDir()
	responses := make([]string, 0, 16)
	for i := 0; i < 6; i++ {
		responses = append(responses,
			toolTurn("c"+string(rune('a'+i)), "read_file", `{"file_path":"a.ts"}`),
			textTurn("r"))
	}
	responses = append(responses, textTurn("done"))
	sc := &scriptedServer{responses: responses}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 生产装配（与 CLI 一致）
	bus.SetEfficacyStatsProvider(func(key string) *EfficacyStats {
		s, ok := rb.Stats()[key]
		if !ok {
			return nil
		}
		return &EfficacyStats{Delivered: s.Delivered, Adopted: s.Adopted}
	})

	// 预置会话内统计：零采纳且已达冷却阈值
	rb.Track([]DeliveredAdvisory{
		{Key: "noisy", Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}}},
		{Key: "noisy", Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}}},
		{Key: "noisy", Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}}},
	}, 1)

	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePreTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "noisy", Priority: 0.6, Category: CategoryDiscipline, Content: "重复提醒",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}},
			})
			return nil
		},
	})
	l.Hooks = p

	for i := 0; i < 6; i++ {
		if err := l.Run(t.Context(), "go"); err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
	}

	var present []bool
	for _, b := range sc.handlerBodies() {
		present = append(present, strings.Contains(b, `key=\"noisy\"`))
	}
	t.Logf("present=%v", present)

	if len(present) == 0 {
		t.Fatal("测试构造有误：从未渲染过")
	}
	// ── 观察：出现「缺席」（冷却生效）──
	anyAbsent := false
	for _, p := range present {
		if !p {
			anyAbsent = true
		}
	}
	if !anyAbsent {
		t.Errorf("**负反馈环未生效**：零采纳提醒每轮都渲染\n"+
			"present=%v\n（delivered>=3 零采纳应触发冷却，间隔缺席）", present)
	}
}

// TestEfficacySilenceInRealRequests —— **会话内静默**（delivered >= 6 零采纳）。
//
// 用户动作：一条零采纳提醒的会话内统计已达静默阈值（delivered=6）。
// 观察到：该提醒**完全不再渲染**。
func TestEfficacySilenceInRealRequests(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("r1"),
		toolTurn("c2", "read_file", `{"file_path":"b.ts"}`), textTurn("r2"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb
	bus.SetEfficacyStatsProvider(func(key string) *EfficacyStats {
		s, ok := rb.Stats()[key]
		if !ok {
			return nil
		}
		return &EfficacyStats{Delivered: s.Delivered, Adopted: s.Adopted}
	})

	// 预置 delivered=6 零采纳
	var d []DeliveredAdvisory
	for i := 0; i < 6; i++ {
		d = append(d, DeliveredAdvisory{
			Key: "hopeless", Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}},
		})
	}
	rb.Track(d, 1)

	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePreTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "hopeless", Priority: 0.6, Category: CategoryDiscipline, Content: "无用提醒",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}},
			})
			return nil
		},
	})
	l.Hooks = p

	for i := 0; i < 2; i++ {
		if err := l.Run(t.Context(), "go"); err != nil {
			t.Fatal(err)
		}
	}

	for i, b := range sc.handlerBodies() {
		if strings.Contains(b, `key=\"hopeless\"`) {
			t.Errorf("**会话内静默未生效**：第 %d 个请求仍渲染了 hopeless\n"+
				"（delivered>=6 零采纳应完全静默）", i)
		}
	}
}

// TestEfficacyFailOpenExempt —— **fail-open：constitutional / priority >= 0.8 不受约束**。
//
// 关键点：高优先级提醒（含宪法级）**不该被统计意义上的「无效」静默掉**。
func TestEfficacyFailOpenExempt(t *testing.T) {
	cases := []struct {
		name string
		e    AdvisoryEntry
	}{
		{"constitutional", AdvisoryEntry{Key: "c", Tier: TierConstitutional, Priority: 0.5}},
		{"priority >= 0.8", AdvisoryEntry{Key: "h", Priority: 0.85}},
		{"priority = 0.8 边界", AdvisoryEntry{Key: "b", Priority: 0.8}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := NewAdvisoryBus()
			// 极端统计：delivered 远超静默阈值且零采纳
			bus.SetEfficacyStatsProvider(func(string) *EfficacyStats {
				return &EfficacyStats{Delivered: 100, Adopted: 0}
			})
			e := c.e
			e.Category = CategoryDiscipline
			e.Content = "X"
			bus.Submit(e)
			out := bus.Render("", 0)
			if out == "" {
				t.Errorf("**fail-open 失效**：%s 被负反馈环静默了\n"+
					"（constitutional / priority>=0.8 应永不受约束）", c.name)
			}
		})
	}
}

// TestEfficacyPositiveArmInSorting —— **正向臂：采纳 >= 3 → 排序加成**。
//
// 用户动作：一条被采纳 3 次的提醒（priority 0.66）与一条零采纳的（0.70）竞争。
// 观察到：**被采纳的排在前面**（+0.05 加成 → 0.71 > 0.70）。
func TestEfficacyPositiveArmInSorting(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.SetEfficacyStatsProvider(func(key string) *EfficacyStats {
		if key == "praised" {
			return &EfficacyStats{Delivered: 5, Adopted: 3}
		}
		return &EfficacyStats{Delivered: 5, Adopted: 0}
	})
	// 需要触发一次 render 来建立 positiveArmKeys
	bus.Submit(AdvisoryEntry{Key: "praised", Priority: 0.66, Category: CategoryDiscipline, Content: "R"})
	bus.Submit(AdvisoryEntry{Key: "plain", Priority: 0.70, Category: CategoryDiscipline, Content: "P"})
	out := bus.Render("", 0)

	hi := strings.Index(out, "praised")
	lo := strings.Index(out, "plain")
	if hi < 0 || lo < 0 {
		t.Fatalf("两条都未渲染：%q", out)
	}
	if hi > lo {
		t.Errorf("**正向臂加成未生效**：被采纳 3 次的应排在前面\n"+
			"（0.66 + 0.05 = 0.71 > 0.70）\n输出=%q", out)
	}
}

// TestEfficacyNoStatsProvider —— **无 provider 时不做负反馈环**（缺省行为）。
func TestEfficacyNoStatsProvider(t *testing.T) {
	bus := NewAdvisoryBus()
	// 不注入 provider
	bus.Submit(AdvisoryEntry{Key: "k", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	bus.Submit(AdvisoryEntry{Key: "k", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	// 两轮都应渲染（无 provider → 无约束）
	if out := bus.Render("", 0); out == "" {
		t.Error("无 provider 时不该静默")
	}
}

// TestEfficacyStatsExcludesPriors —— **负反馈环用会话内统计，不含跨会话先验**。
//
// 这是口径差异的关键：T7 排序含先验（`GetDeliveredCount` 合并 priors），
// 而负反馈环看「本次会话里说了几次没人听」——**不含先验**。
func TestEfficacyStatsExcludesPriors(t *testing.T) {
	rb := NewAdvisoryReadback()
	// 播种大量先验（若被计入，会触发静默）
	rb.SeedPriors(map[string]EfficacyPriorCounts{
		"k": {Delivered: 100, Adopted: 0},
	})
	// 会话内统计为空
	bus := NewAdvisoryBus()
	bus.SetEfficacyStatsProvider(func(key string) *EfficacyStats {
		s, ok := rb.Stats()[key]
		if !ok {
			return nil // 会话内无统计 → nil（不受约束）
		}
		return &EfficacyStats{Delivered: s.Delivered, Adopted: s.Adopted}
	})
	bus.Submit(AdvisoryEntry{Key: "k", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	if out := bus.Render("", 0); out == "" {
		t.Error("**口径混淆**：负反馈环不该把跨会话先验计入——" +
			"它看的是会话内 delivered/adopted")
	}
}
