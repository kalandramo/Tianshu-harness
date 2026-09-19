package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEfficacySortingInRealRequests —— **T7 效力排序的端到端验收**。
//
// 用户动作：两条提醒竞争渲染位，一条**采纳率高但 priority 低**，另一条相反。
// 观察到：**高采纳率的低 priority 条目排在前面**（胜出预算竞争）。
//
// 这修的是本刀取证时发现的真实问题（TS 注释原文）：
//
//	实测 self-verify 采纳 77% 却因 0.58 < 0.70 恒输给采纳 12% 的 todo-missing。
//
// **接线依赖**：`bus.SetEfficacySignalProvider(readback.GetMatureLift → GetAdoptionRate)`
// 与 `bus.SetEfficacySpan(...)`。若未接，排序退化为纯 priority。
func TestEfficacySortingInRealRequests(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 播种先验：highEff 采纳率高、lowEff 采纳率低
	rb.SeedPriors(map[string]EfficacyPriorCounts{
		"high-eff": {Adopted: 7, Ignored: 1}, // 87.5%
		"low-eff":  {Adopted: 1, Ignored: 7}, // 12.5%
	})

	// 生产装配（与 CLI 一致）
	bus.SetEfficacySpan(ParseEfficacySpan(""))
	bus.SetEfficacySignalProvider(func(key string) *EfficacySignal {
		if lift := rb.GetMatureLift(key); lift != nil {
			return &EfficacySignal{Score: (*lift + 1) / 2, Confidence: 1}
		}
		rate := rb.GetAdoptionRate(key)
		if rate == nil {
			return nil
		}
		decided := float64(rb.GetDecidedCount(key))
		conf := decided / EfficacyConfidentSamples
		if conf > 1 {
			conf = 1
		}
		return &EfficacySignal{Score: *rate, Confidence: conf}
	})

	// priority 低但效力高的条目 vs priority 高但效力低的条目
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "seed", Phase: PhasePreTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "high-eff", Priority: 0.58, Category: CategoryDiscipline, Content: "高效力",
			})
			bus.Submit(AdvisoryEntry{
				Key: "low-eff", Priority: 0.70, Category: CategoryDiscipline, Content: "低效力",
			})
			return nil
		},
	})
	l.Hooks = p

	if err := l.Run(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}

	// ── 观察：请求体里两条的顺序 ──
	var order []string
	for _, b := range sc.handlerBodies() {
		if !strings.Contains(b, "high-eff") {
			continue
		}
		hi := strings.Index(b, `key=\"high-eff\"`)
		lo := strings.Index(b, `key=\"low-eff\"`)
		if hi < 0 || lo < 0 {
			continue
		}
		if hi < lo {
			order = append(order, "high-eff 在前")
		} else {
			order = append(order, "low-eff 在前")
		}
	}
	t.Logf("渲染顺序观测=%v", order)

	if len(order) == 0 {
		t.Fatal("两条提醒未同时出现——测试构造有误（预算可能只容一条）")
	}
	if order[0] != "high-eff 在前" {
		t.Errorf("**效力排序未生效**：低 priority 但高采纳率的条目应胜出\n"+
			"观测=%v\n（0.58 的 87.5%% 采纳率应压过 0.70 的 12.5%%——span=0.15 时\n"+
			" 0.58 + 0.15×1×1 = 0.73 > 0.70 - 0.15×1×1 = 0.55）", order)
	}
}

// TestEfficacyExemptInSorting —— **三类豁免不参与效力调整**。
//
// 关键点：豁免时 `efficacyAdjust` 返回 0 → **优先级原样透传，不进 clamp**。
// 否则 CONSTITUTIONAL_PRIORITY(0.9) 会被压到 0.79。
func TestEfficacyExemptInSorting(t *testing.T) {
	cases := []struct {
		name string
		e    AdvisoryEntry
	}{
		{"constitutional", AdvisoryEntry{Key: "c", Tier: TierConstitutional, Priority: 0.9}},
		{"immediate", AdvisoryEntry{Key: "i", Immediate: true, Priority: 0.7}},
		{"star_domain", AdvisoryEntry{Key: "s", Category: CategoryStarDomain, Priority: 0.5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := NewAdvisoryBus()
			bus.SetEfficacySpan(0.15)
			// 极低 score——若未豁免会把优先级压到下限 0.05
			bus.SetEfficacySignalProvider(func(string) *EfficacySignal {
				return &EfficacySignal{Score: 0.0, Confidence: 1.0}
			})
			got := bus.effectivePriority(c.e)
			if got != c.e.Priority {
				t.Errorf("**豁免失效**：有效优先级 %v ≠ 原值 %v\n"+
					"（豁免时必须原样透传，否则 0.9 的宪法级会被压到 0.79）", got, c.e.Priority)
			}
		})
	}
}

// TestEfficacyPriorityBounds —— **调整量有界**（clamp 到 [0.05, 0.79]）。
func TestEfficacyPriorityBounds(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.SetEfficacySpan(0.15)
	bus.SetEfficacySignalProvider(func(string) *EfficacySignal {
		return &EfficacySignal{Score: 1.0, Confidence: 1.0}
	})

	// 上限：0.79（不得触及 0.8 豁免线）
	high := bus.effectivePriority(AdvisoryEntry{Key: "h", Priority: 0.75, Category: CategoryDiscipline})
	if high != efficacyPriorityCap {
		t.Errorf("上限应为 %v，得到 %v（0.75 + 0.15 = 0.90 应被压到 0.79）", efficacyPriorityCap, high)
	}

	// 下限：0.05（保留参赛资格，不等于静音）
	busLow := NewAdvisoryBus()
	busLow.SetEfficacySpan(0.15)
	busLow.SetEfficacySignalProvider(func(string) *EfficacySignal {
		return &EfficacySignal{Score: 0.0, Confidence: 1.0}
	})
	low := busLow.effectivePriority(AdvisoryEntry{Key: "l", Priority: 0.10, Category: CategoryDiscipline})
	if low != efficacyPriorityFloor {
		t.Errorf("下限应为 %v，得到 %v（0.10 - 0.15 = -0.05 应被抬到 0.05）", efficacyPriorityFloor, low)
	}
}

// TestEfficacyConfidenceScaling —— **置信度按决出样本数缩放**。
//
// 用户动作：两个条目 score 相同但 confidence 不同（1.0 vs 0.2）。
// 观察到：高置信度条目被抬升更多——单样本不该改写优先级。
func TestEfficacyConfidenceScaling(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.SetEfficacySpan(0.15)
	bus.SetEfficacySignalProvider(func(key string) *EfficacySignal {
		if key == "confident" {
			return &EfficacySignal{Score: 1.0, Confidence: 1.0}
		}
		return &EfficacySignal{Score: 1.0, Confidence: 0.2}
	})

	base := AdvisoryEntry{Priority: 0.60, Category: CategoryDiscipline}
	confident := bus.effectivePriority(AdvisoryEntry{Key: "confident", Priority: base.Priority, Category: base.Category})
	shaky := bus.effectivePriority(AdvisoryEntry{Key: "shaky", Priority: base.Priority, Category: base.Category})

	if confident <= shaky {
		t.Errorf("高置信度应被抬升更多：confident=%v shaky=%v", confident, shaky)
	}
	// confident: 0.60 + 0.15*1.0 = 0.75；shaky: 0.60 + 0.15*0.2 = 0.63
	if !floatNear(confident, 0.75) || !floatNear(shaky, 0.63) {
		t.Errorf("调整量不符：confident=%v（期望 0.75）shaky=%v（期望 0.63）", confident, shaky)
	}
}

// TestEfficacySpanZeroDisables —— **span=0 关闭调整**（回退纯 priority 排序）。
func TestEfficacySpanZeroDisables(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.SetEfficacySpan(ParseEfficacySpan("0"))
	bus.SetEfficacySignalProvider(func(string) *EfficacySignal {
		return &EfficacySignal{Score: 1.0, Confidence: 1.0}
	})
	e := AdvisoryEntry{Key: "k", Priority: 0.58, Category: CategoryDiscipline}
	if got := bus.effectivePriority(e); got != 0.58 {
		t.Errorf("span=0 时有效优先级应等于原值，得到 %v", got)
	}
}

// TestParseEfficacySpan —— **env 解析语义**（对账 parseEfficacySpan）。
func TestParseEfficacySpan(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{"", 0.15},     // 未设置 → 默认
		{"0", 0},       // 显式关闭
		{"0.3", 0.3},   // 合法值
		{"0.5", 0.5},   // 边界
		{"0.6", 0.15},  // 越界 → 默认
		{"-0.1", 0.15}, // 负数 → 默认
		{"abc", 0.15},  // 非法 → 默认
		{"NaN", 0.15},  // NaN → 默认
	}
	for _, c := range cases {
		if got := ParseEfficacySpan(c.raw); got != c.want {
			t.Errorf("ParseEfficacySpan(%q) = %v，期望 %v", c.raw, got, c.want)
		}
	}
}

// floatNear 是容差比较（浮点运算的舍入误差）。
func floatNear(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
