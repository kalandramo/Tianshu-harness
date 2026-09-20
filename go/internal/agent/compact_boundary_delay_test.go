package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/cache"
	"github.com/kalandramo/tianshu/go/internal/compact"
)

// delayTestMsgs 构造一份会触发压缩的历史（大 tool 结果 + 小窗口）。
func delayTestMsgs() []*wire.OrderedMap {
	return []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "锚用户"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
		wire.NewOrderedMap().Set("role", "assistant").
			Set("content", "").
			Set("tool_calls", []any{
				wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
					Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").
			Set("content", strings.Repeat("x", 300000)),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手1"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户1"),
	}
}

// TestCompactBoundary_AdvisorDelaysOnHotCache —— **接线集成测试**。
//
// 验证缓存顾问真的在 `MaybeCompact` 路径上生效：热缓存 + 低压力时**推迟**
// 压缩（历史不变），而非照压不误。
//
// 对账 TS `compaction-controller.ts:550` 的 `shouldDelayCompact` 接线。
//
// **构造要点（两处踩坑，值得记住）**：
//
//  1. 必须让决策层产出 `ActionMicro`（`Tier<=2` + `ShouldCompact`）。若历史
//     大到越过 reactive 阈值，决策层直接返回 `none`，根本到不了 delay 检查。
//  2. **`CompactBoundary.RecentHitRate` 必须为 nil**——它会进 `TierForRatio`
//     触发 `AdaptiveCompactPolicyRatios`（hitRate>=0.85 时各档上移），
//     与 advisor 的 hitRate 是**两个独立输入**。本用例只想测 advisor 的行为，
//     故决策层的 hitRate 保持 nil（走基准比值）。
func TestCompactBoundary_AdvisorDelaysOnHotCache(t *testing.T) {
	hit := 0.95
	b := NewCompactBoundary(200_000)
	b.Advisor = &cache.Advisor{RecentHitRate: &hit}
	b.Advisor.Warmth.RecordAPICall()
	// 决策层 hitRate 保持 nil（默认）——避免自适应比值上移干扰。
	// watch 极低（必触发）+ reactive/ceiling 极高（不越过）→ Tier 落在 watch/compact。
	b.ProviderProfile = &compact.CompactRatioProfile{
		CacheType:    compact.CacheExactPrefix,
		Persistent:   true,
		HasOverrides: true,
		Ratios: compact.CompactPolicyRatios{
			Watch: 0.00001, Compact: 0.00002, Reactive: 0.90, Ceiling: 0.95,
		},
	}

	oai := orderedMapsToOai(delayTestMsgs())
	_, changed := b.MaybeCompact(oai, 0)

	if b.LastDecision == nil || b.LastDecision.Action == compact.ActionNone {
		t.Fatalf("应触发压缩决策（ActionMicro），实得 %+v", b.LastDecision)
	}
	if !b.LastDelayed {
		t.Fatalf("热缓存 + 低压力应推迟压缩（decision=%+v）", b.LastDecision)
	}
	if changed {
		t.Error("推迟时不得改动历史")
	}
	if b.LastReclaimed != 0 {
		t.Errorf("推迟时 LastReclaimed 应为 0，实得 %d", b.LastReclaimed)
	}
	if b.LastReclaimDecision != nil {
		t.Error("推迟时不应产出 reclaim gate 记录（gate 在 delay 之后）")
	}
}

// TestCompactBoundary_AdvisorAllowsUnderPressure —— 高压力时顾问放行。
//
// 与上条对偶：同样的热缓存，但压力高 → protection 低 → 放行 → 压缩真的发生。
// **这条证明 delay 不是「一律推迟」**。
//
// **构造要点**：pressure = estimatedTokens / ContextWindow 必须**过半**
// （> 0.5）才能压过 protection 与 warmth 两支。首版用 200K 窗口 + 约 75K token
// 历史 → pressure 仅 0.375，仍在延迟侧。此处用 100K 窗口使 pressure ≈ 0.75。
func TestCompactBoundary_AdvisorAllowsUnderPressure(t *testing.T) {
	hit := 0.95
	b := NewCompactBoundary(100_000) // 窗口 100K，历史约 75K token → pressure ≈ 0.75
	b.Advisor = &cache.Advisor{RecentHitRate: &hit}
	b.Advisor.Warmth.RecordAPICall()
	b.Profile = compact.CompactionProfile{
		Billing: compact.BillingPerToken,
		Cache:   compact.CompactionCacheExactPrefix,
	}
	b.ProviderProfile = &compact.CompactRatioProfile{
		CacheType:    compact.CacheExactPrefix,
		Persistent:   true,
		HasOverrides: true,
		Ratios: compact.CompactPolicyRatios{
			Watch: 0.0001, Compact: 0.0002, Reactive: 0.90, Ceiling: 0.95,
		},
	}

	oai := orderedMapsToOai(delayTestMsgs())
	_, changed := b.MaybeCompact(oai, 0)

	if b.LastDelayed {
		t.Fatalf("高压应放行（不延迟），decision=%+v", b.LastDecision)
	}
	if !changed {
		t.Fatalf("放行后应真的压缩（decision=%+v gate=%+v）", b.LastDecision, b.LastReclaimDecision)
	}
	if b.LastReclaimDecision == nil {
		t.Error("放行路径应产出 reclaim gate 记录")
	}
}

// TestCompactBoundary_ForceTierIsCeilingSoAdvisorAllows —— **记录一个结构性事实**。
//
// 探针（2026-09-20）确认：`force` 动作的 `Tier` **恒为 4（ceiling）**
// （决策层的 ceiling 分支硬编码 `Tier: TierCeiling`）。而 advisor 的
// `tier >= 3` 短路**自己就返回 false**——故 `!decision.Force` 守卫在当前
// 实现中是**死分支**：即便去掉它，force 场景也不会被延迟。
//
// 这与 reclaim gate 那轮发现的「CompactBoundary 里的熔断器检查是死分支」
// 同类。**保留守卫**（对齐 TS 的 `!actionDecision.force && ...` 语义，
// 且 tier 语义未来可能变），但用本测试锁定事实，避免后人误以为它被覆盖。
//
// 本测试**不假装测到了 force 绕过**——它断言的是「advisor 在 ceiling tier 下
// 恒放行」这个真实约束。
func TestCompactBoundary_ForceTierIsCeilingSoAdvisorAllows(t *testing.T) {
	hit := 0.99
	b := NewCompactBoundary(200_000)
	b.Advisor = &cache.Advisor{RecentHitRate: &hit}
	b.Advisor.Warmth.RecordAPICall()
	// 极低 ceiling → ratio 必越过 → force。
	b.ProviderProfile = &compact.CompactRatioProfile{
		CacheType:    compact.CacheExactPrefix,
		Persistent:   true,
		HasOverrides: true,
		Ratios: compact.CompactPolicyRatios{
			Watch: 0.00001, Compact: 0.00002, Reactive: 0.00003, Ceiling: 0.00004,
		},
	}

	oai := orderedMapsToOai(delayTestMsgs())
	_, changed := b.MaybeCompact(oai, 0)

	if b.LastDecision == nil || !b.LastDecision.Force {
		t.Fatalf("本用例需要 force 决策，实得 %+v", b.LastDecision)
	}
	// 事实一：force 时 tier 恒为 ceiling。
	if b.LastDecision.Tier != compact.TierCeiling {
		t.Errorf("force 动作的 tier 应为 TierCeiling(4)，实得 %d", b.LastDecision.Tier)
	}
	// 事实二：即便 advisor 在（低压力 + 热缓存）下本会延迟，ceiling tier 也放行。
	probe := &cache.Advisor{RecentHitRate: &hit}
	probe.Warmth.RecordAPICall()
	if probe.ShouldDelayCompact(int(compact.TierCeiling), &cache.PressureContext{
		EstimatedTokens: 75_000, ContextWindow: 200_000,
	}) {
		t.Error("ceiling tier 应被 advisor 短路放行（tier>=3）——否则 `!force` 守卫不是死分支")
	}
	// 结论：压缩照常完成。
	if b.LastDelayed {
		t.Error("force 场景不应被延迟")
	}
	if !changed {
		t.Errorf("force 应完成压缩（gate=%+v）", b.LastReclaimDecision)
	}
}

// TestCompactBoundary_NilAdvisorSkipsDelay —— nil 顾问 = 不延迟（可选依赖）。
//
// 对账 TS 的可选链 `deps.cacheAdvisor?.shouldDelayCompact(...)`：顾问缺失时
// 跳过延迟检查，压缩照常。
func TestCompactBoundary_NilAdvisorSkipsDelay(t *testing.T) {
	b := NewCompactBoundary(100_000)
	if b.Advisor != nil {
		t.Fatal("默认构造不应带顾问")
	}
	oai := orderedMapsToOai(delayTestMsgs())
	_, changed := b.MaybeCompact(oai, 0)

	if b.LastDelayed {
		t.Error("nil 顾问不应产生延迟")
	}
	if !changed {
		t.Errorf("nil 顾问时压缩应照常（decision=%+v）", b.LastDecision)
	}
}
