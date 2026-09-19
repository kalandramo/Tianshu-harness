// Package compact 是压缩阈值策略层。
//
// 对账 TS 的 `src/compact/constants.ts` 与 `src/context/compact-policy.ts`
// 的**策略判定部分**（不含 `decideCompactAction`——那含熔断器状态机，属另一子系统）。
//
// **为什么独立成包**：`PressureMonitor` 依赖它，而 `internal/agent` 不该反向
// 依赖压缩策略；两者是并列的消费方。
package compact

// CompactTier 是压缩档位（0 = 不压缩，4 = 必须 checkpoint-resume）。
//
// 对账 `CompactTier = 0 | 1 | 2 | 3 | 4`。
type CompactTier int

// 档位常量（对账 `reasonForTier` 的语义）。
const (
	// TierNone 低于 watch 阈值。
	TierNone CompactTier = 0
	// TierWatch 超过 watch 阈值（工具结果超限）。
	TierWatch CompactTier = 1
	// TierCompact 建议压缩会话记忆。
	TierCompact CompactTier = 2
	// TierReactive 需要响应式轮次摘要。
	TierReactive CompactTier = 3
	// TierCeiling 超过上下文天花板，必须 checkpoint-resume。
	TierCeiling CompactTier = 4
)

// CacheType 是 provider 的缓存类型。
//
// 对账 `src/api/provider-profile.ts` 的 `CacheType`。
type CacheType string

const (
	CacheExactPrefix        CacheType = "exact-prefix"
	CacheExplicitBreakpoint CacheType = "explicit-breakpoint"
	CachePartialPrefix      CacheType = "partial-prefix"
	CacheBlockKV            CacheType = "block-kv"
	CacheNone               CacheType = "none"
)

// CompactProviderStrategy 是压缩策略分层。
//
// 对账 `CompactProviderStrategy`。
type CompactProviderStrategy string

const (
	// StrategyCachePreserving 用于持久化精确前缀缓存（DeepSeek 风格）：
	// 压缩代价高（重塑历史会让已付费的前缀失效），故**推迟非紧急压缩**，
	// 但保留 95% 硬天花板。
	StrategyCachePreserving CompactProviderStrategy = "cache-preserving"
	// StrategyBalanced 用于部分缓存存活或 TTL 短的 provider（OpenAI/Gemini/Claude）。
	StrategyBalanced CompactProviderStrategy = "balanced"
	// StrategyAggressive 用于无前缀缓存损失的 provider（MiMO/本地）：
	// 早点压缩以保持活跃上下文干净。
	StrategyAggressive CompactProviderStrategy = "aggressive"
)

// LargeContextWindowTokens 是大窗口阈值——只有大窗口才启用精度天花板。
//
// 对账 `LARGE_CONTEXT_WINDOW_TOKENS = 500_000`。
const LargeContextWindowTokens = 500_000

// CompactPolicyRatios 是四档阈值比值。
//
// 对账 `CompactPolicyRatios`。
type CompactPolicyRatios struct {
	Watch    float64
	Compact  float64
	Reactive float64
	Ceiling  float64
}

// defaultPolicyRatios 是 balanced 的基准（对账 `DEFAULT_POLICY_RATIOS`）。
var defaultPolicyRatios = CompactPolicyRatios{Watch: 0.6, Compact: 0.78, Reactive: 0.88, Ceiling: 0.95}

// strategyPolicyRatios 是三种策略的基准比值（对账 `STRATEGY_POLICY_RATIOS`）。
var strategyPolicyRatios = map[CompactProviderStrategy]CompactPolicyRatios{
	StrategyCachePreserving: {Watch: 0.72, Compact: 0.86, Reactive: 0.92, Ceiling: 0.95},
	StrategyBalanced:        defaultPolicyRatios,
	StrategyAggressive:      {Watch: 0.5, Compact: 0.7, Reactive: 0.84, Ceiling: 0.95},
}

// CompactRatioProfile 是 ratio 推导所需的 provider 切片。
//
// 对账 `CompactRatioProfile`——只含 `cacheType` / `persistent` 与可选的
// per-provider 覆盖。**nil 等价于「无 profile」**（走 balanced）。
type CompactRatioProfile struct {
	CacheType  CacheType
	Persistent bool
	// HasOverrides 表示 Ratios 有效（对账 `providerProfile?.compaction?.ratios`
	// 的存在性判断——Go 无法用零值区分「未设置」与「全零覆盖」）。
	HasOverrides bool
	Ratios       CompactPolicyRatios
}

// StrategyForCacheType 由缓存类型与持久性推导策略。
//
// 对账 `strategyForCacheType`：
//
//	exact-prefix && persistent → cache-preserving
//	none                       → aggressive
//	其余                        → balanced
func StrategyForCacheType(cacheType CacheType, persistent bool) CompactProviderStrategy {
	if cacheType == CacheExactPrefix && persistent {
		return StrategyCachePreserving
	}
	if cacheType == CacheNone {
		return StrategyAggressive
	}
	return StrategyBalanced
}

// CompactProviderStrategyFor 由 profile 推导策略（nil → balanced）。
//
// 对账 `compactProviderStrategy`。
func CompactProviderStrategyFor(profile *CompactRatioProfile) CompactProviderStrategy {
	if profile == nil {
		return StrategyBalanced
	}
	return StrategyForCacheType(profile.CacheType, profile.Persistent)
}

// CompactPolicyRatiosFor 返回该 profile 的四档比值。
//
// 对账 `compactPolicyRatios`。**部分覆盖不抹掉其他档**（对账注释：
// 「Merge defined overrides only — a partial override must not erase the
// strategy defaults for the other rungs」）——Go 侧用 HasOverrides 表达
// 「是否提供覆盖」，一旦提供则四档全取（与 TS 的 `o.x ?? base.x` 语义一致，
// 因为 TS 的 partial 在类型层就允许缺档，而 Go 的 struct 无法表达缺档）。
func CompactPolicyRatiosFor(profile *CompactRatioProfile) CompactPolicyRatios {
	base := strategyPolicyRatios[CompactProviderStrategyFor(profile)]
	if profile == nil || !profile.HasOverrides {
		return base
	}
	return profile.Ratios
}

// AdaptiveCompactPolicyRatios 按近期缓存命中率自适应调整阈值。
//
// 对账 `adaptiveCompactPolicyRatios`：
//
//	hitRate >= 0.85 → 各档上移（推迟压缩，保护珍贵的缓存前缀）
//	hitRate <  0.3  → 各档下移（提前压缩）
//	其余             → 基准
//
// **ceiling 不参与调整**（对账注释：`ceiling: base.ceiling`）——95% 是硬线。
func AdaptiveCompactPolicyRatios(profile *CompactRatioProfile, hitRate float64) CompactPolicyRatios {
	base := CompactPolicyRatiosFor(profile)
	if hitRate >= 0.85 {
		return CompactPolicyRatios{
			Watch:    minFloat(base.Watch+0.05, 0.90),
			Compact:  minFloat(base.Compact+0.03, 0.93),
			Reactive: minFloat(base.Reactive+0.02, 0.95),
			Ceiling:  base.Ceiling,
		}
	}
	if hitRate < 0.3 {
		return CompactPolicyRatios{
			Watch:    maxFloat(base.Watch-0.10, 0.40),
			Compact:  maxFloat(base.Compact-0.08, 0.60),
			Reactive: maxFloat(base.Reactive-0.05, 0.75),
			Ceiling:  base.Ceiling,
		}
	}
	return base
}

// PrecisionCeilingRatio 返回窗口派生的精度天花板。
//
// 对账 `precisionCeilingRatio`。
//
// **只有大窗口才有天花板**（对账注释）：中小窗口的缓存经济比值已经压得够早，
// 模型精度退化不是瓶颈；在那里加天花板会与缓存策略打架（如 1K 测试窗口在
// 0.65 就被迫 tier 2，低于 cache-preserving 的 0.86 compact 比值）。
// 天花板是为 **1M 窗口**设的——那里纯缓存经济会把压缩推迟到精度悬崖之后。
//
// 返回 1 = 无天花板（缓存策略说了算）。
//
// 0.7 而非 0.5（2026-07-26）：0.5 会把 cache-preserving 的 watch(0.72) 砍半
// ——与 `autoFloor` clamp 修过的是同一个错误。现在它刚好在 watch 之前触发，
// 是**精度优先的早期护栏**而非替代压缩调度。
func PrecisionCeilingRatio(contextWindow int, override *float64) float64 {
	if override != nil && *override > 0 && *override < 1 {
		return *override
	}
	if contextWindow >= LargeContextWindowTokens {
		return 0.7
	}
	if contextWindow >= 200_000 {
		return 0.55
	}
	return 1
}

// TierForRatio 是五档判定。
//
// 对账 `tierForRatio`。**precisionCeiling 必须是地板，不是回退分支**——
// 当它低于 watch 阈值（cache-preserving: 0.70 vs 0.72）时，早先的
// `return 1` 会遮蔽它，让 ladder 非单调（ratio 0.71 压缩而 0.75 只 watch）。
//
// `hitRate` 为 nil 时回退基准比值（对账 `recentHitRate != null` 的判据）。
func TierForRatio(ratio float64, profile *CompactRatioProfile, hitRate *float64, precisionCeiling *float64) CompactTier {
	var ratios CompactPolicyRatios
	if hitRate != nil {
		ratios = AdaptiveCompactPolicyRatios(profile, *hitRate)
	} else {
		ratios = CompactPolicyRatiosFor(profile)
	}
	if ratio >= ratios.Ceiling {
		return TierCeiling
	}
	if ratio >= ratios.Reactive {
		return TierReactive
	}
	if ratio >= ratios.Compact {
		return TierCompact
	}
	// 精度天花板：**地板语义**——一旦越过就至少 tier 2，
	// 即使缓存经济比值（可能被热缓存上移）说不用。
	if precisionCeiling != nil && ratio >= *precisionCeiling {
		return TierCompact
	}
	if ratio >= ratios.Watch {
		return TierWatch
	}
	return TierNone
}

// ReasonForTier 返回档位的可读原因（对账 `reasonForTier`）。
func ReasonForTier(t CompactTier) string {
	switch t {
	case TierNone:
		return "context usage below watch threshold"
	case TierWatch:
		return "tool results exceeded watch threshold"
	case TierCompact:
		return "session memory compact recommended"
	case TierReactive:
		return "reactive round summarization required"
	default:
		return "context ceiling exceeded; checkpoint-resume required"
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
