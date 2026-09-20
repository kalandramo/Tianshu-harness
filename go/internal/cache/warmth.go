// Package cache 实现缓存顾问（advisor）——压缩的缓存经济学判定。
//
// 对账 src/cache/advisor.ts 的 `shouldDelayCompact` 与 src/cache/session-warmth.ts
// 的 `SessionWarmthTracker`。
//
// **为什么需要它**：压缩会击碎前缀缓存——已付费建立的前缀作废，下次请求
// 全量重建。但缓存重建成本 ∝ 命中率（被缓存的输入占比），而压缩收益随窗口
// 压力上升（接近阈值时余量价值高于缓存保护）。故存在一个**显式权衡**：
// 热缓存 + 低压力时推迟压缩（余量还够，不值得重建），压力升高时放行
// （1M 下 OOM 风险 > 重建成本）。
//
// 判定错了的代价是双面的：该延迟不延迟 → 白白重建前缀；不该延迟却延迟
// → 上下文爆掉。故本包全部是纯逻辑（时间经注入），可逐用例对账。
package cache

// CacheTemperature 是会话热度。
//
// 对账 TS `CacheTemperature`。
type CacheTemperature string

const (
	// TempHot 是最近有 API 调用（< 60s）——前缀大概率还热。
	TempHot CacheTemperature = "hot"
	// TempWarm 是 TTL 内但已过 60s——可能还热。
	TempWarm CacheTemperature = "warm"
	// TempCold 是超出 TTL 或从未调用——前缀大概率已失效。
	TempCold CacheTemperature = "cold"
)

// 热度阈值（对账 TS 字面量）。
const (
	// hotWindowMs 是「热」窗口——60 秒内有 API 调用。
	hotWindowMs = 60_000
	// defaultTTLMs 是默认 TTL（1 小时）。
	defaultTTLMs = 3_600_000
)

// SessionWarmthTracker 跟踪会话热度。
//
// 对账 TS `SessionWarmthTracker`。**时间经注入**（`Now` 函数）——
// 否则判定依赖真实时钟，无法对账（TS 侧本就支持注入，此处沿用）。
type SessionWarmthTracker struct {
	// LastAPICallTime 是最近一次 API 调用的时间戳（毫秒）；0 = 从未调用。
	LastAPICallTime int64
	// TTLMs 是缓存 TTL（毫秒）；<=0 时用 defaultTTLMs。
	TTLMs int64
	// Now 返回当前时间戳（毫秒）；nil 时用真实时钟。
	Now func() int64
}

// Predict 预测当前热度。
//
// 对账 `predict`：
//
//	从未调用          → cold
//	elapsed < 60s     → hot
//	elapsed < ttl     → warm
//	其余              → cold
func (t *SessionWarmthTracker) Predict() CacheTemperature {
	if t.LastAPICallTime == 0 {
		return TempCold
	}
	now := t.now()
	elapsed := now - t.LastAPICallTime
	if elapsed < hotWindowMs {
		return TempHot
	}
	if elapsed < t.ttl() {
		return TempWarm
	}
	return TempCold
}

// ShouldOpportunisticCompact 报告是否该趁机压缩（冷会话时是）。
//
// 对账 `shouldOpportunisticCompact`。
func (t *SessionWarmthTracker) ShouldOpportunisticCompact() bool {
	return t.Predict() == TempCold
}

// RecordAPICall 记录一次 API 调用（刷新热度）。
//
// 对账 `recordApiCall`。
func (t *SessionWarmthTracker) RecordAPICall() {
	t.LastAPICallTime = t.now()
}

func (t *SessionWarmthTracker) ttl() int64 {
	if t.TTLMs <= 0 {
		return defaultTTLMs
	}
	return t.TTLMs
}

func (t *SessionWarmthTracker) now() int64 {
	if t.Now != nil {
		return t.Now()
	}
	return realNowMs()
}
