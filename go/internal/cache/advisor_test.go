package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// delayOracle 是 TS 侧真实 CacheAdvisor.shouldDelayCompact /
// SessionWarmthTracker.predict 的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/delaycompact/gen-oracle.ts
type delayOracle struct {
	Warmth map[string]struct {
		Temperature   CacheTemperature `json:"temperature"`
		Opportunistic bool             `json:"opportunistic"`
	} `json:"warmth"`
	Delay map[string]struct {
		Decision bool               `json:"decision"`
		Reason   DelayCompactReason `json:"reason"`
	} `json:"delay"`
}

func loadDelayOracle(t *testing.T) delayOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "delaycompact", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/delaycompact/gen-oracle.ts",
			path, err)
	}
	var o delayOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// ── warmth 用例（与 gen-oracle.ts 的 warmthCases 逐条对应）──

const warmthT0 = int64(1_000_000_000_000)

func warmthCaseInput(name string) (elapsedMs int64, ttlMs int64, neverCalled bool, ok bool) {
	switch name {
	case "never_called":
		return 0, 0, true, true
	case "just_called":
		return 0, 0, false, true
	case "hot_59s":
		return 59_999, 0, false, true
	case "boundary_60s":
		return 60_000, 0, false, true
	case "warm_1h_minus_1":
		return 3_599_999, 0, false, true
	case "boundary_ttl":
		return 3_600_000, 0, false, true
	case "cold_beyond":
		return 3_600_001, 0, false, true
	case "custom_ttl_hot":
		return 59_999, 120_000, false, true
	case "custom_ttl_warm":
		return 60_000, 120_000, false, true
	case "custom_ttl_cold":
		return 120_000, 120_000, false, true
	}
	return 0, 0, false, false
}

// TestSessionWarmthParity —— 热度判定与 TS 等价（含边界）。
func TestSessionWarmthParity(t *testing.T) {
	o := loadDelayOracle(t)
	if len(o.Warmth) == 0 {
		t.Fatal("oracle 无 warmth 用例")
	}
	for name, want := range o.Warmth {
		elapsed, ttl, neverCalled, ok := warmthCaseInput(name)
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应输入", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			nowVal := warmthT0
			tr := SessionWarmthTracker{TTLMs: ttl, Now: func() int64 { return nowVal }}
			if !neverCalled {
				tr.RecordAPICall()
				nowVal = warmthT0 + elapsed
			}
			if got := tr.Predict(); got != want.Temperature {
				t.Errorf("Predict() = %q, want %q", got, want.Temperature)
			}
			if got := tr.ShouldOpportunisticCompact(); got != want.Opportunistic {
				t.Errorf("ShouldOpportunisticCompact() = %v, want %v", got, want.Opportunistic)
			}
		})
	}
}

// ── delay 用例（与 gen-oracle.ts 的 delayCases 逐条对应）──

type delayCaseInput struct {
	hitRate *float64
	tier    int
	ctx     *PressureContext
	// warmthElapsed 为 nil 表示「从未调用 API」（cold）；否则表示在 T0 调用后
	// 推进的毫秒数。**必须与 oracle 的 warmth 字段一致**——首版这里语义脱节，
	// 导致 Go 走 warmth 分支而 TS 走 cold 分支（见 HANDOFF）。
	warmthElapsed *int64
}

func fptr(f float64) *float64 { return &f }

func delayCase(name string) (delayCaseInput, bool) {
	i64 := func(i int64) *int64 { return &i }
	switch name {
	case "tier3_never_delays":
		return delayCaseInput{hitRate: fptr(0.99), tier: 3, ctx: &PressureContext{1000, 1_000_000}}, true
	case "tier4_never_delays":
		return delayCaseInput{hitRate: fptr(0.99), tier: 4, ctx: &PressureContext{1000, 1_000_000}}, true
	case "hot_low_pressure_delays":
		return delayCaseInput{hitRate: fptr(0.95), tier: 1, ctx: &PressureContext{100_000, 1_000_000}}, true
	case "hot_high_pressure_allows":
		return delayCaseInput{hitRate: fptr(0.95), tier: 1, ctx: &PressureContext{900_000, 1_000_000}}, true
	case "protection_boundary_exact":
		return delayCaseInput{hitRate: fptr(0.9), tier: 2, ctx: &PressureContext{500_000, 1_000_000}}, true
	case "protection_just_below":
		return delayCaseInput{hitRate: fptr(0.89), tier: 2, ctx: &PressureContext{500_000, 1_000_000}}, true
	case "cold_low_pressure_allows":
		return delayCaseInput{hitRate: fptr(0.1), tier: 1, ctx: &PressureContext{100_000, 1_000_000}, warmthElapsed: i64(4_000_000)}, true
	case "warmth_hot_branch_delays":
		return delayCaseInput{hitRate: fptr(0.4), tier: 1, ctx: &PressureContext{100_000, 1_000_000}, warmthElapsed: i64(30_000)}, true
	case "warmth_hot_tier2_allows":
		return delayCaseInput{hitRate: fptr(0.4), tier: 2, ctx: &PressureContext{100_000, 1_000_000}, warmthElapsed: i64(30_000)}, true
	case "warmth_hot_high_pressure_allows":
		return delayCaseInput{hitRate: fptr(0.4), tier: 1, ctx: &PressureContext{600_000, 1_000_000}, warmthElapsed: i64(30_000)}, true
	case "legacy_hitrate_delays":
		return delayCaseInput{hitRate: fptr(0.85), tier: 1, warmthElapsed: i64(4_000_000)}, true
	case "legacy_hitrate_boundary":
		return delayCaseInput{hitRate: fptr(0.8), tier: 1, warmthElapsed: i64(4_000_000)}, true
	case "legacy_hitrate_below_warmth_hot":
		return delayCaseInput{hitRate: fptr(0.79), tier: 1, warmthElapsed: i64(30_000)}, true
	case "legacy_hitrate_below_cold_allows":
		return delayCaseInput{hitRate: fptr(0.79), tier: 1, warmthElapsed: i64(4_000_000)}, true
	case "legacy_warmth_hot_delays":
		return delayCaseInput{hitRate: fptr(0.5), tier: 1, warmthElapsed: i64(30_000)}, true
	case "legacy_warmth_hot_tier2_allows":
		return delayCaseInput{hitRate: fptr(0.5), tier: 2, warmthElapsed: i64(30_000)}, true
	case "legacy_allow":
		return delayCaseInput{hitRate: fptr(0.5), tier: 1, warmthElapsed: i64(200_000)}, true
	// hitRate=null 的三个：cold 两个 + hot 一个。
	case "null_hitrate_with_ctx_cold":
		return delayCaseInput{hitRate: nil, tier: 1, ctx: &PressureContext{100_000, 1_000_000}}, true
	case "null_hitrate_no_ctx_cold":
		return delayCaseInput{hitRate: nil, tier: 1}, true
	case "null_hitrate_no_ctx_hot":
		return delayCaseInput{hitRate: nil, tier: 1, warmthElapsed: i64(30_000)}, true
	}
	return delayCaseInput{}, false
}

// TestShouldDelayCompactParity —— delay 判定与 TS 逐用例等价（含 reason）。
//
// 覆盖三条分支：reactive 短路 / protection 公式 / legacy 回退。
// **reason 也断言**——它是可观测性契约，错的 reason 会让离线诊断误导。
func TestShouldDelayCompactParity(t *testing.T) {
	o := loadDelayOracle(t)
	if len(o.Delay) == 0 {
		t.Fatal("oracle 无 delay 用例")
	}
	for name, want := range o.Delay {
		in, ok := delayCase(name)
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应输入", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			nowVal := warmthT0
			adv := &Advisor{
				RecentHitRate: in.hitRate,
				Warmth:        SessionWarmthTracker{Now: func() int64 { return nowVal }},
			}
			// 复刻 oracle 的 warmth 语义：
			//   warmthElapsed == nil → **从不调用** API（lastApiCallTime 保持 0 = cold）
			//   warmthElapsed != nil → 在 T0 调用一次，再推进该毫秒数
			if in.warmthElapsed != nil {
				adv.Warmth.RecordAPICall()
				nowVal = warmthT0 + *in.warmthElapsed
			}

			var captured *DelayCompactDecision
			adv.OnDecision = func(d DelayCompactDecision) { captured = &d }

			got := adv.ShouldDelayCompact(in.tier, in.ctx)

			if got != want.Decision {
				t.Errorf("decision: Go=%v TS=%v", got, want.Decision)
			}
			if captured == nil {
				t.Fatal("决策未记录（OnDecision 未被调用）")
			}
			if captured.Reason != want.Reason {
				t.Errorf("reason: Go=%q TS=%q", captured.Reason, want.Reason)
			}
			if captured.Decision != got {
				t.Errorf("记录与返回值不一致：记录=%v 返回=%v", captured.Decision, got)
			}
		})
	}
}

// TestDelayDecisionCarriesInputs —— 决策记录携带完整输入（可观测性契约）。
func TestDelayDecisionCarriesInputs(t *testing.T) {
	hit := 0.95
	adv := &Advisor{RecentHitRate: &hit, CurrentTurn: 7}
	adv.Warmth.RecordAPICall()

	var got DelayCompactDecision
	adv.OnDecision = func(d DelayCompactDecision) { got = d }

	adv.ShouldDelayCompact(1, &PressureContext{EstimatedTokens: 100_000, ContextWindow: 1_000_000})

	if got.Turn != 7 {
		t.Errorf("turn: Go=%d want 7", got.Turn)
	}
	if got.RecentHitRate == nil || *got.RecentHitRate != 0.95 {
		t.Errorf("recentHitRate 未携带：%v", got.RecentHitRate)
	}
	if got.EstimatedTokens == nil || *got.EstimatedTokens != 100_000 {
		t.Errorf("estimatedTokens 未携带：%v", got.EstimatedTokens)
	}
	if got.ContextWindow == nil || *got.ContextWindow != 1_000_000 {
		t.Errorf("contextWindow 未携带：%v", got.ContextWindow)
	}
	if got.Protection == nil {
		t.Fatal("protection 未携带")
	}
	// protection = 0.95 × (1 − 0.1) = 0.855
	if diff := *got.Protection - 0.855; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("protection = %v, want 0.855", *got.Protection)
	}
}

// TestDelayDecisionListenerNeverAffectsVerdict —— 账本异常不影响判定。
//
// 对账 TS 的 try/catch 注释：「ledger is best-effort — never affect the
// decision」。Go 侧无 panic 捕获，但 nil 监听器必须安全。
func TestDelayDecisionListenerNeverAffectsVerdict(t *testing.T) {
	hit := 0.95
	adv := &Advisor{RecentHitRate: &hit} // OnDecision 为 nil
	adv.Warmth.RecordAPICall()

	// 不应 panic。
	if !adv.ShouldDelayCompact(1, &PressureContext{EstimatedTokens: 100_000, ContextWindow: 1_000_000}) {
		t.Error("nil 监听器不应影响判定（应延迟）")
	}
}

// TestPressureClamped —— pressure 钳在 [0,1]。
//
// 对账 TS 的 `Math.min(1, Math.max(0, v))`。构造超窗口（pressure > 1）与
// 负值，断言钳制后行为正确。
func TestPressureClamped(t *testing.T) {
	hit := 0.9
	adv := &Advisor{RecentHitRate: &hit}
	adv.Warmth.RecordAPICall()

	// estimatedTokens 远超窗口 → pressure 钳为 1 → protection = 0 → 不延迟。
	if adv.ShouldDelayCompact(1, &PressureContext{EstimatedTokens: 5_000_000, ContextWindow: 1_000_000}) {
		t.Error("pressure>1 应钳为 1（protection=0）→ 不延迟")
	}
	// 负 estimatedTokens → pressure 钳为 0 → protection = hitRate。
	// 但 warmth 分支：tier=1 + pressure=0 < 0.5 → 若 protection < 0.45 才走 warmth。
	// hitRate=0.9 → protection=0.9 >= 0.45 → protection 支延迟。
	var got DelayCompactDecision
	adv.OnDecision = func(d DelayCompactDecision) { got = d }
	if !adv.ShouldDelayCompact(1, &PressureContext{EstimatedTokens: -1000, ContextWindow: 1_000_000}) {
		t.Error("负压力应钳为 0 → protection=hitRate → 延迟")
	}
	if got.Protection == nil || *got.Protection != 0.9 {
		t.Errorf("钳制后 protection 应为 0.9，实得 %v", got.Protection)
	}
}

// TestClamp01NaN —— NaN 不被钳制（对账 JS 的 Math.min/max 遇 NaN 返回 NaN）。
func TestClamp01NaN(t *testing.T) {
	nan := 0.0
	nan = nan / nan                      // 构造 NaN
	if got := clamp01(nan); got == got { // NaN != NaN
		t.Errorf("clamp01(NaN) 应保持 NaN，实得 %v", got)
	}
}
