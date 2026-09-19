package compact

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type pressureOracle struct {
	Constants struct {
		LargeContextWindowTokens int `json:"largeContextWindowTokens"`
	} `json:"constants"`
	Strategies []struct {
		Name   string              `json:"name"`
		Ratios CompactPolicyRatios `json:"ratios"`
	} `json:"strategies"`
	Adaptive []struct {
		HitRate         float64             `json:"hitRate"`
		Balanced        CompactPolicyRatios `json:"balanced"`
		CachePreserving CompactPolicyRatios `json:"cachePreserving"`
	} `json:"adaptive"`
	Ceilings []struct {
		Window   int      `json:"window"`
		Override *float64 `json:"override"`
		Want     float64  `json:"want"`
	} `json:"ceilings"`
	Tiers []struct {
		Name    string   `json:"name"`
		Ratio   float64  `json:"ratio"`
		HitRate *float64 `json:"hitRate"`
		Ceiling *float64 `json:"ceiling"`
		Want    int      `json:"want"`
	} `json:"tiers"`
	Checks []struct {
		Name   string `json:"name"`
		Window int    `json:"window"`
		Want   []struct {
			Turn              int      `json:"turn"`
			Tokens            int      `json:"tokens"`
			Tier              int      `json:"tier"`
			ShouldCompact     bool     `json:"shouldCompact"`
			Thrashing         bool     `json:"thrashing"`
			FastGrowth        bool     `json:"fastGrowth"`
			Ratio             float64  `json:"ratio"`
			GrowthRate        float64  `json:"growthRate"`
			CvmOverheadRatio  float64  `json:"cvmOverheadRatio"`
			ShouldThrottleCvm bool     `json:"shouldThrottleCvm"`
			PressureRelative  *float64 `json:"pressureRelative"`
			Suggestion        *string  `json:"suggestion"`
		} `json:"want"`
	} `json:"checks"`
	Cvm []struct {
		Name   string `json:"name"`
		Window int    `json:"window"`
		Want   []struct {
			Injected   int     `json:"injected"`
			Ratio      float64 `json:"ratio"`
			Throttling bool    `json:"throttling"`
			Ceiling    bool    `json:"ceiling"`
		} `json:"want"`
	} `json:"cvm"`
	Thrashing []struct {
		Name        string `json:"name"`
		Compactions []int  `json:"compactions"`
		Turn        int    `json:"turn"`
		Want        bool   `json:"want"`
	} `json:"thrashing"`
}

func loadPressureOracle(t *testing.T) pressureOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "pressure", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/pressure/gen-oracle.ts）：%v", err)
	}
	var out pressureOracle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func ratiosEqual(a, b CompactPolicyRatios) bool {
	return near(a.Watch, b.Watch) && near(a.Compact, b.Compact) &&
		near(a.Reactive, b.Reactive) && near(a.Ceiling, b.Ceiling)
}

// TestPressureConstants —— 常量与 TS 一致。
func TestPressureConstants(t *testing.T) {
	o := loadPressureOracle(t)
	if LargeContextWindowTokens != o.Constants.LargeContextWindowTokens {
		t.Errorf("LargeContextWindowTokens：Go=%d TS=%d",
			LargeContextWindowTokens, o.Constants.LargeContextWindowTokens)
	}
}

// TestCompactPolicyRatiosParity —— **三策略分层的比值逐例对账**。
func TestCompactPolicyRatiosParity(t *testing.T) {
	o := loadPressureOracle(t)
	for _, c := range o.Strategies {
		t.Run(c.Name, func(t *testing.T) {
			// 由用例名推导 profile（与生成器的 strategies 表对应）
			var profile *CompactRatioProfile
			switch c.Name {
			case "none-persistent":
				profile = &CompactRatioProfile{CacheType: CacheNone, Persistent: true}
			case "none-ephemeral":
				profile = &CompactRatioProfile{CacheType: CacheNone, Persistent: false}
			case "exact-prefix-persistent":
				profile = &CompactRatioProfile{CacheType: CacheExactPrefix, Persistent: true}
			case "exact-prefix-ephemeral":
				profile = &CompactRatioProfile{CacheType: CacheExactPrefix, Persistent: false}
			case "explicit-breakpoint":
				profile = &CompactRatioProfile{CacheType: CacheExplicitBreakpoint, Persistent: false}
			case "partial-prefix":
				profile = &CompactRatioProfile{CacheType: CachePartialPrefix, Persistent: true}
			case "block-kv":
				profile = &CompactRatioProfile{CacheType: CacheBlockKV, Persistent: false}
			case "no-profile":
				profile = nil
			default:
				t.Fatalf("未知用例 %q", c.Name)
			}
			got := CompactPolicyRatiosFor(profile)
			if !ratiosEqual(got, c.Ratios) {
				t.Errorf("比值不符：\nGo=%+v\nTS=%+v", got, c.Ratios)
			}
		})
	}
}

// TestAdaptiveRatiosParity —— **缓存命中率自适应调整**（含两个边界 0.85 / 0.3）。
func TestAdaptiveRatiosParity(t *testing.T) {
	o := loadPressureOracle(t)
	cpProfile := &CompactRatioProfile{CacheType: CacheExactPrefix, Persistent: true}
	for _, c := range o.Adaptive {
		t.Run(itoaF(c.HitRate), func(t *testing.T) {
			if got := AdaptiveCompactPolicyRatios(nil, c.HitRate); !ratiosEqual(got, c.Balanced) {
				t.Errorf("balanced 不符：Go=%+v TS=%+v", got, c.Balanced)
			}
			if got := AdaptiveCompactPolicyRatios(cpProfile, c.HitRate); !ratiosEqual(got, c.CachePreserving) {
				t.Errorf("cache-preserving 不符：Go=%+v TS=%+v", got, c.CachePreserving)
			}
		})
	}
}

// TestPrecisionCeilingParity —— **精度天花板**（含 override 的合法性判据）。
func TestPrecisionCeilingParity(t *testing.T) {
	o := loadPressureOracle(t)
	for _, c := range o.Ceilings {
		name := itoa(c.Window)
		if c.Override != nil {
			name += "/override=" + itoaF(*c.Override)
		}
		t.Run(name, func(t *testing.T) {
			if got := PrecisionCeilingRatio(c.Window, c.Override); !near(got, c.Want) {
				t.Errorf("天花板不符：Go=%v TS=%v", got, c.Want)
			}
		})
	}
}

// TestTierForRatioParity —— **五档判定逐值对账**。
//
// 关键用例是 `precision-ceiling-floor`：0.71 落在 cache-preserving 的
// watch(0.72) 之下、天花板(0.70) 之上 → **必须是 tier 2**（地板语义）。
// 若写成回退分支（`return 1` 在前），0.71 会变成 1，而 0.75 是 2——
// ladder 非单调。
func TestTierForRatioParity(t *testing.T) {
	o := loadPressureOracle(t)
	cpProfile := &CompactRatioProfile{CacheType: CacheExactPrefix, Persistent: true}
	noneProfile := &CompactRatioProfile{CacheType: CacheNone, Persistent: false}
	for _, c := range o.Tiers {
		t.Run(c.Name, func(t *testing.T) {
			var profile *CompactRatioProfile
			switch c.Name {
			case "cache-preserving-0.71", "cache-preserving-0.72", "cache-preserving-0.86",
				"precision-ceiling-floor", "precision-ceiling-above", "precision-ceiling-not-hit":
				profile = cpProfile
			case "aggressive-0.5", "aggressive-0.7":
				profile = noneProfile
			}
			got := TierForRatio(c.Ratio, profile, c.HitRate, c.Ceiling)
			if int(got) != c.Want {
				t.Errorf("tier 不符：ratio=%v Go=%d TS=%d", c.Ratio, got, c.Want)
			}
		})
	}
}

// TestPressureCheckParity —— **check 的八字段逐轮对账**（四条 token 序列）。
func TestPressureCheckParity(t *testing.T) {
	o := loadPressureOracle(t)
	for _, seq := range o.Checks {
		t.Run(seq.Name, func(t *testing.T) {
			m := NewPressureMonitor(seq.Window)
			for i, w := range seq.Want {
				got := m.Check(w.Tokens, w.Turn)
				if int(got.Tier) != w.Tier {
					t.Errorf("第 %d 轮 tier：Go=%d TS=%d", i, got.Tier, w.Tier)
				}
				if got.ShouldCompact != w.ShouldCompact {
					t.Errorf("第 %d 轮 shouldCompact：Go=%v TS=%v", i, got.ShouldCompact, w.ShouldCompact)
				}
				if got.Thrashing != w.Thrashing {
					t.Errorf("第 %d 轮 thrashing：Go=%v TS=%v", i, got.Thrashing, w.Thrashing)
				}
				if got.FastGrowth != w.FastGrowth {
					t.Errorf("第 %d 轮 fastGrowth：Go=%v TS=%v", i, got.FastGrowth, w.FastGrowth)
				}
				if !near(got.Ratio, w.Ratio) {
					t.Errorf("第 %d 轮 ratio：Go=%v TS=%v", i, got.Ratio, w.Ratio)
				}
				if !near(got.GrowthRate, w.GrowthRate) {
					t.Errorf("第 %d 轮 growthRate：Go=%v TS=%v", i, got.GrowthRate, w.GrowthRate)
				}
				if !near(got.CvmOverheadRatio, w.CvmOverheadRatio) {
					t.Errorf("第 %d 轮 cvmOverheadRatio：Go=%v TS=%v", i, got.CvmOverheadRatio, w.CvmOverheadRatio)
				}
				if got.ShouldThrottleCvm != w.ShouldThrottleCvm {
					t.Errorf("第 %d 轮 shouldThrottleCvm：Go=%v TS=%v", i, got.ShouldThrottleCvm, w.ShouldThrottleCvm)
				}
				// pressureRelative：nil 与非 nil 必须严格区分
				if (got.PressureRelative == nil) != (w.PressureRelative == nil) {
					t.Errorf("第 %d 轮 pressureRelative 的 nil 性不符：Go=%v TS=%v",
						i, got.PressureRelative, w.PressureRelative)
				} else if got.PressureRelative != nil && !near(*got.PressureRelative, *w.PressureRelative) {
					t.Errorf("第 %d 轮 pressureRelative：Go=%v TS=%v", i, *got.PressureRelative, *w.PressureRelative)
				}
				// suggestion
				gotSug := got.Suggestion
				wantSug := ""
				if w.Suggestion != nil {
					wantSug = *w.Suggestion
				}
				if gotSug != wantSug {
					t.Errorf("第 %d 轮 suggestion：Go=%q TS=%q", i, gotSug, wantSug)
				}
			}
		})
	}
}

// TestCvmThrottlingParity —— **CVM 节流阈值（5%）与天花板（8%）**。
func TestCvmThrottlingParity(t *testing.T) {
	o := loadPressureOracle(t)
	for _, c := range o.Cvm {
		t.Run(c.Name, func(t *testing.T) {
			m := NewPressureMonitor(c.Window)
			for i, w := range c.Want {
				m.RecordCvmInjection(w.Injected, SourceProjection)
				if !near(m.CvmOverheadRatio(), w.Ratio) {
					t.Errorf("第 %d 次 ratio：Go=%v TS=%v", i, m.CvmOverheadRatio(), w.Ratio)
				}
				if m.IsCvmThrottling() != w.Throttling {
					t.Errorf("第 %d 次 throttling：Go=%v TS=%v", i, m.IsCvmThrottling(), w.Throttling)
				}
				if m.IsCvmThrottlingCeiling() != w.Ceiling {
					t.Errorf("第 %d 次 ceiling：Go=%v TS=%v", i, m.IsCvmThrottlingCeiling(), w.Ceiling)
				}
			}
		})
	}
}

// TestThrashingDetectionParity —— **抖动检测**（近 4 轮内压缩 ≥ 3 次）。
func TestThrashingDetectionParity(t *testing.T) {
	o := loadPressureOracle(t)
	for _, c := range o.Thrashing {
		t.Run(c.Name, func(t *testing.T) {
			m := NewPressureMonitor(1_000_000)
			for _, turn := range c.Compactions {
				m.RecordCompaction(turn)
			}
			got := m.Check(100_000, c.Turn).Thrashing
			if got != c.Want {
				t.Errorf("thrashing：Go=%v TS=%v（compactions=%v turn=%d）",
					got, c.Want, c.Compactions, c.Turn)
			}
		})
	}
}

// TestP90Semantics —— **p90 是「排序后按下标取」而非插值分位**。
//
// TS 用 `sorted[floor(len * 0.9)]`——Go 侧若用插值（如 gonum 的
// stat.Quantile）会得到不同值，进而让 pressureRelative 漂移。
func TestP90Semantics(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{[]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 10}, // floor(10*0.9)=9 → 第 10 个
		{[]float64{1, 2, 3, 4, 5}, 5},                  // floor(5*0.9)=4 → 第 5 个
		{[]float64{5, 1, 3}, 5},                        // floor(3*0.9)=2 → 最大
		{[]float64{1}, 1},
		{[]float64{}, 0},
		{[]float64{2, 1}, 2}, // floor(2*0.9)=1 → 第 2 个
	}
	for _, c := range cases {
		if got := p90(c.in); !near(got, c.want) {
			t.Errorf("p90(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// TestReasonForTierParity —— 档位原因文本（逐字对账）。
func TestReasonForTierParity(t *testing.T) {
	want := map[CompactTier]string{
		TierNone:     "context usage below watch threshold",
		TierWatch:    "tool results exceeded watch threshold",
		TierCompact:  "session memory compact recommended",
		TierReactive: "reactive round summarization required",
		TierCeiling:  "context ceiling exceeded; checkpoint-resume required",
	}
	for tier, w := range want {
		if got := ReasonForTier(tier); got != w {
			t.Errorf("ReasonForTier(%d)：Go=%q TS=%q", tier, got, w)
		}
	}
}

// TestCvmBySourceInvariant —— **不变量：各来源之和 == 累计值**。
func TestCvmBySourceInvariant(t *testing.T) {
	m := NewPressureMonitor(1_000_000)
	m.RecordCvmInjection(100, SourceProjection)
	m.RecordCvmInjection(200, SourceSystemReminder)
	m.RecordCvmInjection(50, SourceProjection)

	sum := 0
	for _, v := range m.CvmInjectionBySource() {
		sum += v
	}
	if sum != m.cvmTokenAccumulator {
		t.Errorf("来源之和 %d ≠ 累计值 %d（不变量被破坏）", sum, m.cvmTokenAccumulator)
	}
	if m.cvmTokenAccumulator != 350 {
		t.Errorf("累计值应为 350，得到 %d", m.cvmTokenAccumulator)
	}

	// Reset 后应全清
	m.ResetCvmOverhead()
	if m.cvmTokenAccumulator != 0 || len(m.CvmInjectionBySource()) != 0 {
		t.Error("ResetCvmOverhead 未清空状态")
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func itoaF(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ── 以下为 decideCompactAction / 熔断器 / 阶梯 / 阈值的对账 ──

type actionOracle struct {
	Breaker []struct {
		Name      string `json:"name"`
		Failures  []int  `json:"failures"`
		Successes []int  `json:"successes"`
		StartTurn int    `json:"startTurn"`
		Want      []struct {
			ConsecutiveFailures int  `json:"consecutiveFailures"`
			DisabledUntilTurn   *int `json:"disabledUntilTurn"`
			Turn                int  `json:"turn"`
		} `json:"want"`
	} `json:"breaker"`
	Ladders []struct {
		Name    string `json:"name"`
		Billing string `json:"billing"`
		Cache   string `json:"cache"`
		Want    struct {
			Partial float64 `json:"partial"`
			Full    float64 `json:"full"`
		} `json:"want"`
	} `json:"ladders"`
	Decides []struct {
		Name            string `json:"name"`
		MaxTokens       int    `json:"maxTokens"`
		EstimatedTokens int    `json:"estimatedTokens"`
		Turn            int    `json:"turn"`
		Billing         string `json:"billing"`
		Cache           string `json:"cache"`
		Failures        *struct {
			ConsecutiveFailures int  `json:"consecutiveFailures"`
			DisabledUntilTurn   *int `json:"disabledUntilTurn"`
		} `json:"failures"`
		LLMLadder *struct {
			Partial float64 `json:"partial"`
			Full    float64 `json:"full"`
		} `json:"llmLadder"`
		Want struct {
			Action        string `json:"action"`
			Force         bool   `json:"force"`
			PrecisionRisk bool   `json:"precisionRisk"`
			Tier          int    `json:"tier"`
			ShouldCompact bool   `json:"shouldCompact"`
		} `json:"want"`
	} `json:"decides"`
	Thresholds []struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Want  struct {
			AutoThreshold       int `json:"autoThreshold"`
			AutoFloor           int `json:"autoFloor"`
			ToolResultMaxTokens int `json:"toolResultMaxTokens"`
		} `json:"want"`
	} `json:"thresholds"`
}

func loadActionOracle(t *testing.T) actionOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "pressure", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var out actionOracle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

// TestCircuitBreakerParity —— **熔断器状态机逐序列对账**。
//
// 关键语义：连续失败达 3 次触发禁用；**每次失败都重算禁用窗口**
// （`fail-four` 的 turn 14 → 禁用至 16）；成功重置且**丢弃 disabledUntilTurn**。
func TestCircuitBreakerParity(t *testing.T) {
	o := loadActionOracle(t)
	for _, c := range o.Breaker {
		t.Run(c.Name, func(t *testing.T) {
			st := CompactCircuitBreakerState{}
			turn := c.StartTurn
			var got []CompactCircuitBreakerState
			for _, f := range c.Failures {
				for i := 0; i < f; i++ {
					st = RecordCompactFailure(st, turn)
					turn++
				}
				got = append(got, st)
			}
			for _, sc := range c.Successes {
				for i := 0; i < sc; i++ {
					st = RecordCompactSuccess()
				}
				got = append(got, st)
			}
			if len(got) != len(c.Want) {
				t.Fatalf("状态数：Go=%d TS=%d", len(got), len(c.Want))
			}
			for i, w := range c.Want {
				if got[i].ConsecutiveFailures != w.ConsecutiveFailures {
					t.Errorf("第 %d 步 consecutiveFailures：Go=%d TS=%d",
						i, got[i].ConsecutiveFailures, w.ConsecutiveFailures)
				}
				if (got[i].DisabledUntilTurn == nil) != (w.DisabledUntilTurn == nil) {
					t.Errorf("第 %d 步 disabledUntilTurn 的 nil 性不符：Go=%v TS=%v",
						i, got[i].DisabledUntilTurn, w.DisabledUntilTurn)
				} else if got[i].DisabledUntilTurn != nil && *got[i].DisabledUntilTurn != *w.DisabledUntilTurn {
					t.Errorf("第 %d 步 disabledUntilTurn：Go=%d TS=%d",
						i, *got[i].DisabledUntilTurn, *w.DisabledUntilTurn)
				}
			}
		})
	}
}

// TestLLMActionRatiosParity —— **阶梯派生**（billing × cache 四组合）。
func TestLLMActionRatiosParity(t *testing.T) {
	o := loadActionOracle(t)
	for _, c := range o.Ladders {
		t.Run(c.Name, func(t *testing.T) {
			got := LLMActionRatiosFor(CompactionProfile{
				Billing: CompactionBilling(c.Billing),
				Cache:   CompactionCache(c.Cache),
			})
			if !near(got.Partial, c.Want.Partial) || !near(got.Full, c.Want.Full) {
				t.Errorf("阶梯不符：Go=%+v TS={%v %v}", got, c.Want.Partial, c.Want.Full)
			}
		})
	}
}

// TestDecideCompactActionParity —— **六种 action 的判定对账**（14 个用例）。
//
// 关键用例：
//   - `breaker-open-but-ceiling`：force 优先于熔断器
//   - `large-precision-band`：精度带 → stale-round（非强制 LLM 重写）
//   - `large-cache-preserving-at-075`：缓存保护阶梯的 0.75 档
func TestDecideCompactActionParity(t *testing.T) {
	o := loadActionOracle(t)
	for _, c := range o.Decides {
		t.Run(c.Name, func(t *testing.T) {
			in := CompactActionInput{
				EstimatedTokens: c.EstimatedTokens,
				MaxTokens:       c.MaxTokens,
				Turn:            c.Turn,
				Profile: CompactionProfile{
					Billing: CompactionBilling(c.Billing),
					Cache:   CompactionCache(c.Cache),
				},
			}
			if in.Profile.Billing == "" {
				in.Profile.Billing = BillingSubscription
			}
			if in.Profile.Cache == "" {
				in.Profile.Cache = CompactionCacheNone
			}
			if c.Failures != nil {
				in.Failures = CompactCircuitBreakerState{
					ConsecutiveFailures: c.Failures.ConsecutiveFailures,
					DisabledUntilTurn:   c.Failures.DisabledUntilTurn,
				}
			}
			if c.LLMLadder != nil {
				in.LLMLadderOverride = &LLMActionRatios{Partial: c.LLMLadder.Partial, Full: c.LLMLadder.Full}
			}
			got := DecideCompactAction(in)
			if string(got.Action) != c.Want.Action {
				t.Errorf("action：Go=%q TS=%q（reason=%q）", got.Action, c.Want.Action, got.Reason)
			}
			if got.Force != c.Want.Force {
				t.Errorf("force：Go=%v TS=%v", got.Force, c.Want.Force)
			}
			if got.PrecisionRisk != c.Want.PrecisionRisk {
				t.Errorf("precisionRisk：Go=%v TS=%v", got.PrecisionRisk, c.Want.PrecisionRisk)
			}
			if int(got.Tier) != c.Want.Tier {
				t.Errorf("tier：Go=%d TS=%d", got.Tier, c.Want.Tier)
			}
			if got.ShouldCompact != c.Want.ShouldCompact {
				t.Errorf("shouldCompact：Go=%v TS=%v", got.ShouldCompact, c.Want.ShouldCompact)
			}
		})
	}
}

// TestCompactThresholdsParity —— **阈值计算对账**（含数字/具名两条路径）。
//
// **注意两条路径的 reactive 不同**（0.8 vs 0.88）——TS 的历史遗留。
func TestCompactThresholdsParity(t *testing.T) {
	o := loadActionOracle(t)
	for _, c := range o.Thresholds {
		t.Run(c.Name, func(t *testing.T) {
			var got CompactThresholds
			// input 是数字或对象——用首字符判别
			trimmed := string(c.Input)
			if len(trimmed) > 0 && trimmed[0] != '{' {
				var w int
				if err := json.Unmarshal(c.Input, &w); err != nil {
					t.Fatalf("解析数字失败：%v", err)
				}
				got = CompactThresholdsForWindow(w)
			} else {
				var spec struct {
					ContextWindow   int `json:"contextWindow"`
					ProviderProfile *struct {
						CacheType  string `json:"cacheType"`
						Persistent bool   `json:"persistent"`
					} `json:"providerProfile"`
				}
				if err := json.Unmarshal(c.Input, &spec); err != nil {
					t.Fatalf("解析对象失败：%v", err)
				}
				in := CompactStrategyInput{ContextWindow: spec.ContextWindow}
				if spec.ProviderProfile != nil {
					in.ProviderProfile = &CompactRatioProfile{
						CacheType:  CacheType(spec.ProviderProfile.CacheType),
						Persistent: spec.ProviderProfile.Persistent,
					}
				}
				got = CompactThresholdsForProfile(in)
			}
			if got.AutoThreshold != c.Want.AutoThreshold {
				t.Errorf("autoThreshold：Go=%d TS=%d", got.AutoThreshold, c.Want.AutoThreshold)
			}
			if got.AutoFloor != c.Want.AutoFloor {
				t.Errorf("autoFloor：Go=%d TS=%d", got.AutoFloor, c.Want.AutoFloor)
			}
			if got.ToolResultMaxTokens != c.Want.ToolResultMaxTokens {
				t.Errorf("toolResultMaxTokens：Go=%d TS=%d", got.ToolResultMaxTokens, c.Want.ToolResultMaxTokens)
			}
		})
	}
}
