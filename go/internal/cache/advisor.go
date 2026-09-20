package cache

import "time"

// realNowMs 返回真实时钟（毫秒）。
//
// 独立成函数便于测试替换——生产路径用它，对账路径注入固定时钟。
func realNowMs() int64 {
	return time.Now().UnixMilli()
}

// DelayCompactReason 是 delay 判定的分支来源。
//
// 对账 TS `DelayCompactDecision['reason']`——**可观测性契约**：
// 离线日志能看出是哪条分支产出的决定，而不是只看到一个布尔值。
type DelayCompactReason string

const (
	// ReasonReactiveTier 是 tier>=3 短路（响应式及以上永不延迟）。
	ReasonReactiveTier DelayCompactReason = "reactive-tier"
	// ReasonProtection 是 protection 公式命中（热缓存 + 低压力）。
	ReasonProtection DelayCompactReason = "protection"
	// ReasonWarmthHot 是 warmth 热 + 低档 + 低压力。
	ReasonWarmthHot DelayCompactReason = "warmth-hot"
	// ReasonPressureAllow 是有压力上下文但放行（压力压倒缓存保护）。
	ReasonPressureAllow DelayCompactReason = "pressure-allow"
	// ReasonLegacyHitrate 是无压力上下文时的命中率回退。
	ReasonLegacyHitrate DelayCompactReason = "legacy-hitrate"
	// ReasonLegacyWarmth 是无压力上下文时的热度回退。
	ReasonLegacyWarmth DelayCompactReason = "legacy-warmth"
	// ReasonLegacyAllow 是回退路径的放行。
	ReasonLegacyAllow DelayCompactReason = "legacy-allow"
)

// DelayCompactDecision 是一次 delay 判定及其输入。
//
// 对账 TS `DelayCompactDecision`。
type DelayCompactDecision struct {
	Turn     int
	Tier     int
	Decision bool
	Reason   DelayCompactReason
	// RecentHitRate 是判定时的近期命中率（nil = 未知）。
	RecentHitRate *float64
	// EstimatedTokens / ContextWindow / Protection 仅在提供压力上下文时有值。
	EstimatedTokens *int
	ContextWindow   *int
	Protection      *float64
}

// protectionThreshold 是 protection 公式的延迟门槛。
//
// 对账 TS 的 `protection >= 0.45`。语义：hitRate × (1 − pressure) ≥ 0.45
// 时推迟压缩——即「缓存足够热 且 压力足够低」。
const protectionThreshold = 0.45

// warmthHotPressureLimit 是 warmth 支的压力上限。
//
// 对账 TS 的 `pressure < 0.5`——压力过半时不因热度延迟（余量优先）。
const warmthHotPressureLimit = 0.5

// legacyHitRateThreshold 是回退路径的命中率门槛。
//
// 对账 TS 的 `recentHitRate >= 0.8`。
const legacyHitRateThreshold = 0.8

// Advisor 是缓存顾问。
//
// 对账 TS `CacheAdvisor` 的 **delay-compact 子集**。完整类还含
// ghostRegistry / behaviorLearner / adaptiveThreshold / recallMetrics
// （依赖尚未移植的模块），本包只做 delay 判定。
type Advisor struct {
	// RecentHitRate 是近期命中率（nil = 未知）。
	//
	// 语义：cacheRead / (cacheRead + cacheCreation)。未知时回退路径一律放行
	// （对账 TS：`recentHitRate !== null` 才进入命中率分支）。
	RecentHitRate *float64

	// Warmth 是会话热度跟踪器。
	Warmth SessionWarmthTracker

	// CurrentTurn 是当前轮次（供决策记录）。
	CurrentTurn int

	// OnDecision 是决策账本监听器（**observe-only，绝不影响判定**）。
	//
	// 对账 TS `setDelayDecisionListener`。TS 用 try/catch 保证账本异常不影响
	// 判定——Go 侧由调用方保证（nil 安全）。
	OnDecision func(DelayCompactDecision)
}

// NewAdvisor 构造顾问。
//
// contextWindow 用于把窗口透传给自适应阈值控制器（TS 的
// `AdaptiveThresholdController({ contextWindow })`）——本包未移植该控制器，
// 参数保留为文档说明：延迟判定本身**不用** contextWindow（只用 ctx 里的）。
func NewAdvisor(recentHitRate *float64, warmth SessionWarmthTracker) *Advisor {
	return &Advisor{RecentHitRate: recentHitRate, Warmth: warmth}
}

// PressureContext 是压力上下文（estimatedTokens / contextWindow）。
//
// 对账 TS `shouldDelayCompact` 的可选第二参数。
type PressureContext struct {
	EstimatedTokens int
	ContextWindow   int
}

// ShouldDelayCompact 判定是否应推迟压缩。
//
// 对账 `shouldDelayCompact`。三条分支，顺序敏感：
//
//  1. **tier >= 3 永不延迟**（reactive / ceiling）——响应式压缩是应对压力的，
//     延迟它等于放任上下文爆掉。
//  2. **有压力上下文 + 有命中率** → protection 公式：
//     `protection = hitRate × (1 − pressure)`
//     - `protection >= 0.45` → 延迟（热缓存 + 低压力，重建不划算）
//     - 否则若 `warmth=hot && tier<=1 && pressure<0.5` → 延迟
//     - 否则放行
//  3. **无压力上下文** → 回退：`hitRate >= 0.8` 延迟；否则 `warmth=hot && tier<=1`
//     延迟；否则放行。
//
// `pressure` 钳在 [0, 1]（对账 TS 的 `Math.min(1, Math.max(0, ...))`）。
func (a *Advisor) ShouldDelayCompact(tier int, ctx *PressureContext) bool {
	// 1. 响应式及以上永不延迟。
	if tier >= 3 {
		a.emit(DelayCompactDecision{Tier: tier, Decision: false, Reason: ReasonReactiveTier})
		return false
	}

	// 2. protection 公式（需压力上下文 + 已知命中率）。
	if ctx != nil && ctx.ContextWindow > 0 && a.RecentHitRate != nil {
		pressure := clamp01(float64(ctx.EstimatedTokens) / float64(ctx.ContextWindow))
		protection := *a.RecentHitRate * (1 - pressure)

		est, win, prot := ctx.EstimatedTokens, ctx.ContextWindow, protection
		base := DelayCompactDecision{
			Tier: tier, EstimatedTokens: &est, ContextWindow: &win, Protection: &prot,
		}

		if protection >= protectionThreshold {
			base.Decision = true
			base.Reason = ReasonProtection
			a.emit(base)
			return true
		}
		if a.Warmth.Predict() == TempHot && tier <= 1 && pressure < warmthHotPressureLimit {
			base.Decision = true
			base.Reason = ReasonWarmthHot
			a.emit(base)
			return true
		}
		base.Decision = false
		base.Reason = ReasonPressureAllow
		a.emit(base)
		return false
	}

	// 3. 回退（无压力上下文）。
	if a.RecentHitRate != nil && *a.RecentHitRate >= legacyHitRateThreshold {
		a.emit(DelayCompactDecision{Tier: tier, Decision: true, Reason: ReasonLegacyHitrate})
		return true
	}
	if a.Warmth.Predict() == TempHot && tier <= 1 {
		a.emit(DelayCompactDecision{Tier: tier, Decision: true, Reason: ReasonLegacyWarmth})
		return true
	}
	a.emit(DelayCompactDecision{Tier: tier, Decision: false, Reason: ReasonLegacyAllow})
	return false
}

// emit 发送决策记录（observe-only）。
func (a *Advisor) emit(d DelayCompactDecision) {
	d.Turn = a.CurrentTurn
	d.RecentHitRate = a.RecentHitRate
	if a.OnDecision != nil {
		a.OnDecision(d)
	}
}

// clamp01 把 v 钳在 [0, 1]。
//
// 对账 TS 的 `Math.min(1, Math.max(0, v))`。**NaN 处理**：NaN 与任何数比较
// 都为 false，故 Go 的 `math.Min/Max` 语义与 JS 不同——这里显式处理 NaN
// （TS 的 Math.min/max 遇 NaN 返回 NaN，后续比较全 false → 走 allow 分支）。
func clamp01(v float64) float64 {
	if v != v { // NaN
		return v
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
