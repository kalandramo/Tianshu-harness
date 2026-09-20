package compact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// reclaimOracle 是 TS 侧真实 deriveCompactionProfile / estimateReclaim /
// shouldCommitReclaim 的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/reclaim/gen-oracle.ts
type reclaimOracle struct {
	WindowBands map[string]CompactionWindowBand `json:"windowBands"`
	Profiles    map[string]struct {
		WindowBand           CompactionWindowBand `json:"windowBand"`
		ContextWindow        int                  `json:"contextWindow"`
		EffectiveInputBudget int                  `json:"effectiveInputBudget"`
		Billing              CompactionBilling    `json:"billing"`
		Cache                CompactionCache      `json:"cache"`
		MinReclaimTokens     int                  `json:"minReclaimTokens"`
		MinReclaimRatio      float64              `json:"minReclaimRatio"`
	} `json:"profiles"`
	Reclaim map[string]struct {
		Estimate struct {
			BeforeTokens    int     `json:"beforeTokens"`
			AfterTokens     int     `json:"afterTokens"`
			ReclaimedTokens int     `json:"reclaimedTokens"`
			ReclaimRatio    float64 `json:"reclaimRatio"`
			Changed         bool    `json:"changed"`
		} `json:"estimate"`
		Verdict struct {
			Commit bool              `json:"commit"`
			Reason ReclaimSkipReason `json:"reason"`
		} `json:"verdict"`
		Record struct {
			Action     CompactionAction     `json:"action"`
			Commit     bool                 `json:"commit"`
			Reason     ReclaimSkipReason    `json:"reason"`
			Force      bool                 `json:"force"`
			WindowBand CompactionWindowBand `json:"windowBand"`
			Billing    CompactionBilling    `json:"billing"`
			Cache      CompactionCache      `json:"cache"`
		} `json:"record"`
	} `json:"reclaim"`
}

func loadReclaimOracle(t *testing.T) reclaimOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "reclaim", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/reclaim/gen-oracle.ts",
			path, err)
	}
	var o reclaimOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// ── 用例构造（与 gen-oracle.ts 逐条对应）──

func reclaimMsg(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}

func reclaimMsgs(n int, text string) []session.OaiMessage {
	out := make([]session.OaiMessage, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, reclaimMsg("user", text+" "+itoa(i)))
	}
	return out
}

func f64Ptr(f float64) *float64 { return &f }
func intPtrLocal(i int) *int    { return &i }

func profileInputFor(name string) (CompactionProfileInput, bool) {
	switch name {
	case "small_pertoken_exact", "tiny_pertoken_exact":
		return CompactionProfileInput{ContextWindow: 100_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix}, true
	case "medium_pertoken_exact":
		return CompactionProfileInput{ContextWindow: 200_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix}, true
	case "verytiny_pertoken_exact":
		return CompactionProfileInput{ContextWindow: 10_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix}, true
	case "large_pertoken_exact":
		return CompactionProfileInput{ContextWindow: 500_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix}, true
	case "large_pertoken_exact_1m":
		return CompactionProfileInput{ContextWindow: 1_000_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix}, true
	case "sub_exact":
		return CompactionProfileInput{ContextWindow: 200_000, Billing: BillingSubscription, Cache: CompactionCacheExactPrefix}, true
	case "pertoken_none":
		return CompactionProfileInput{ContextWindow: 200_000, Billing: BillingPerToken, Cache: CompactionCacheNone}, true
	case "pertoken_partial":
		return CompactionProfileInput{ContextWindow: 200_000, Billing: BillingPerToken, Cache: CompactionCachePartial}, true
	case "sub_none_large":
		return CompactionProfileInput{ContextWindow: 1_000_000, Billing: BillingSubscription, Cache: CompactionCacheNone}, true
	case "with_output_reserve":
		return CompactionProfileInput{ContextWindow: 200_000, Billing: BillingSubscription, Cache: CompactionCacheNone, OutputReserveTokens: intPtrLocal(8192)}, true
	}
	return CompactionProfileInput{}, false
}

// TestWindowBandForParity —— 窗口档位边界与 TS 等价。
func TestWindowBandForParity(t *testing.T) {
	o := loadReclaimOracle(t)
	if len(o.WindowBands) == 0 {
		t.Fatal("oracle 无 windowBands")
	}
	for k, want := range o.WindowBands {
		var w int
		if _, err := json.Number(k).Int64(); err == nil {
			n, _ := json.Number(k).Int64()
			w = int(n)
		} else {
			t.Fatalf("oracle 键 %q 不是整数", k)
		}
		if got := WindowBandFor(w); got != want {
			t.Errorf("WindowBandFor(%d) = %q, want %q", w, got, want)
		}
	}
}

// TestDeriveCompactionProfileParity —— 阈值矩阵与 TS 逐字段等价。
//
// 覆盖三档地板：max(8192, w*0.03) / max(32768, w*0.05) / max(4096, w*0.01)。
func TestDeriveCompactionProfileParity(t *testing.T) {
	o := loadReclaimOracle(t)
	if len(o.Profiles) == 0 {
		t.Fatal("oracle 无 profiles")
	}
	for name, want := range o.Profiles {
		input, ok := profileInputFor(name)
		if !ok {
			t.Errorf("oracle 有 profile %q 而 Go 侧无对应输入", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := DeriveCompactionProfile(input)
			if got.WindowBand != want.WindowBand {
				t.Errorf("windowBand: Go=%q TS=%q", got.WindowBand, want.WindowBand)
			}
			if got.ContextWindow != want.ContextWindow {
				t.Errorf("contextWindow: Go=%d TS=%d", got.ContextWindow, want.ContextWindow)
			}
			if got.EffectiveInputBudget != want.EffectiveInputBudget {
				t.Errorf("effectiveInputBudget: Go=%d TS=%d", got.EffectiveInputBudget, want.EffectiveInputBudget)
			}
			if got.MinReclaimTokens != want.MinReclaimTokens {
				t.Errorf("minReclaimTokens: Go=%d TS=%d", got.MinReclaimTokens, want.MinReclaimTokens)
			}
			if got.MinReclaimRatio != want.MinReclaimRatio {
				t.Errorf("minReclaimRatio: Go=%v TS=%v", got.MinReclaimRatio, want.MinReclaimRatio)
			}
			if got.Billing != want.Billing {
				t.Errorf("billing: Go=%q TS=%q", got.Billing, want.Billing)
			}
			if got.Cache != want.Cache {
				t.Errorf("cache: Go=%q TS=%q", got.Cache, want.Cache)
			}
		})
	}
}

// ── reclaim 判定矩阵（与 gen-oracle.ts 的 reclaimCases 逐条对应）──

func baseReclaimProfile() CompactionProfile {
	return DeriveCompactionProfile(CompactionProfileInput{
		ContextWindow: 200_000, Billing: BillingPerToken, Cache: CompactionCacheExactPrefix,
	})
}

func subReclaimProfile() CompactionProfile {
	return DeriveCompactionProfile(CompactionProfileInput{
		ContextWindow: 200_000, Billing: BillingSubscription, Cache: CompactionCacheNone,
	})
}

// reclaimCaseInput 返回该用例的 (before, after, profile, force)。
//
// **注意 unchanged_same_ref 用同一底层切片**——对账 TS 的 `before === after`
// 引用短路。
func reclaimCaseInput(name string) (before, after []session.OaiMessage, profile CompactionProfile, force bool, ok bool) {
	sameRef := reclaimMsgs(10, "x")
	switch name {
	case "unchanged_same_ref":
		return sameRef, sameRef, baseReclaimProfile(), false, true
	case "unchanged_same_bytes":
		return reclaimMsgs(10, "hello"), reclaimMsgs(10, "hello"), baseReclaimProfile(), false, true
	case "negative_reclaim":
		return reclaimMsgs(5, "hello"), reclaimMsgs(10, "hello"), baseReclaimProfile(), false, true
	case "below_floor_small":
		return reclaimMsgs(10, "hello"), reclaimMsgs(9, "hello"), baseReclaimProfile(), false, true
	case "above_floor_big":
		return reclaimMsgs(1000, "hello world this is a long message"),
			reclaimMsgs(10, "hello world this is a long message"), baseReclaimProfile(), false, true
	case "force_below_floor":
		return reclaimMsgs(10, "hello"), reclaimMsgs(9, "hello"), baseReclaimProfile(), true, true
	case "force_unchanged":
		return reclaimMsgs(10, "hello"), reclaimMsgs(10, "hello"), baseReclaimProfile(), true, true
	case "sub_profile_below":
		return reclaimMsgs(10, "hello"), reclaimMsgs(9, "hello"), subReclaimProfile(), false, true
	}
	return nil, nil, CompactionProfile{}, false, false
}

// TestEstimateReclaimParity —— token 数学与 TS 等价。
func TestEstimateReclaimParity(t *testing.T) {
	o := loadReclaimOracle(t)
	if len(o.Reclaim) == 0 {
		t.Fatal("oracle 无 reclaim 用例")
	}
	for name, want := range o.Reclaim {
		before, after, _, _, ok := reclaimCaseInput(name)
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应输入", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := EstimateReclaim(before, after)
			if got.BeforeTokens != want.Estimate.BeforeTokens {
				t.Errorf("beforeTokens: Go=%d TS=%d", got.BeforeTokens, want.Estimate.BeforeTokens)
			}
			if got.AfterTokens != want.Estimate.AfterTokens {
				t.Errorf("afterTokens: Go=%d TS=%d", got.AfterTokens, want.Estimate.AfterTokens)
			}
			if got.ReclaimedTokens != want.Estimate.ReclaimedTokens {
				t.Errorf("reclaimedTokens: Go=%d TS=%d", got.ReclaimedTokens, want.Estimate.ReclaimedTokens)
			}
			if diff := got.ReclaimRatio - want.Estimate.ReclaimRatio; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("reclaimRatio: Go=%v TS=%v", got.ReclaimRatio, want.Estimate.ReclaimRatio)
			}
			if got.Changed != want.Estimate.Changed {
				t.Errorf("changed: Go=%v TS=%v", got.Changed, want.Estimate.Changed)
			}
		})
	}
}

// TestShouldCommitReclaimParity —— 五条判定分支与 TS 等价。
func TestShouldCommitReclaimParity(t *testing.T) {
	o := loadReclaimOracle(t)
	for name, want := range o.Reclaim {
		before, after, profile, force, ok := reclaimCaseInput(name)
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			est := EstimateReclaim(before, after)
			got := ShouldCommitReclaim(est, profile, force)
			if got.Commit != want.Verdict.Commit {
				t.Errorf("commit: Go=%v TS=%v", got.Commit, want.Verdict.Commit)
			}
			if got.Reason != want.Verdict.Reason {
				t.Errorf("reason: Go=%q TS=%q", got.Reason, want.Verdict.Reason)
			}
		})
	}
}

// TestBuildReclaimDecisionParity —— 决策记录字段与 TS 等价。
func TestBuildReclaimDecisionParity(t *testing.T) {
	o := loadReclaimOracle(t)
	for name, want := range o.Reclaim {
		before, after, profile, force, ok := reclaimCaseInput(name)
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			est := EstimateReclaim(before, after)
			got := BuildReclaimDecision(ActionMicro, est, profile, force)
			if got.Action != want.Record.Action {
				t.Errorf("action: Go=%q TS=%q", got.Action, want.Record.Action)
			}
			if got.Commit != want.Record.Commit {
				t.Errorf("commit: Go=%v TS=%v", got.Commit, want.Record.Commit)
			}
			if got.Reason != want.Record.Reason {
				t.Errorf("reason: Go=%q TS=%q", got.Reason, want.Record.Reason)
			}
			if got.Force != want.Record.Force {
				t.Errorf("force: Go=%v TS=%v", got.Force, want.Record.Force)
			}
			if got.WindowBand != want.Record.WindowBand {
				t.Errorf("windowBand: Go=%q TS=%q", got.WindowBand, want.Record.WindowBand)
			}
			if got.Billing != want.Record.Billing {
				t.Errorf("billing: Go=%q TS=%q", got.Billing, want.Record.Billing)
			}
			if got.Cache != want.Record.Cache {
				t.Errorf("cache: Go=%q TS=%q", got.Cache, want.Record.Cache)
			}
		})
	}
}

// TestReclaimZeroValueProfileAllowsAnyReclaim —— 零值 profile 不误拒。
//
// 手工构造的 profile（不经 derive）地板为 0——此时只要「有正向回收」即放行。
// 这保证 gate 不会因缺少 profile 字段而拒绝一切候选（fail-open 方向的
// 有意选择：压缩能力不该因配置缺失而完全失效）。
func TestReclaimZeroValueProfileAllowsAnyReclaim(t *testing.T) {
	before := reclaimMsgs(10, "hello")
	after := reclaimMsgs(9, "hello")
	est := EstimateReclaim(before, after)
	if est.ReclaimedTokens <= 0 {
		t.Fatalf("构造的用例应有正向回收，实际 %d", est.ReclaimedTokens)
	}
	got := ShouldCommitReclaim(est, CompactionProfile{}, false)
	if !got.Commit {
		t.Errorf("零值 profile 应放行正向回收，得到 %q", got.Reason)
	}
	_ = f64Ptr // 保留辅助函数引用（供未来价格字段用例）
}
