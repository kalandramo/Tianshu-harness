package compact

import (
	"fmt"
	"math"
)

// CompactionAction 是压缩动作。
//
// 对账 `CompactionAction`——七种取值（Go 侧本刀覆盖六种，`session-split`
// 留给会话分割子系统）。
type CompactionAction string

const (
	// ActionNone 不压缩。
	ActionNone CompactionAction = "none"
	// ActionStaleRound 确定性回收陈旧轮次（仍需过下游 reclaim gate 与缓存延迟）。
	ActionStaleRound CompactionAction = "stale-round"
	// ActionMicro 微压缩（确定性）。
	ActionMicro CompactionAction = "micro"
	// ActionPartialLLM 部分 LLM 重写。
	ActionPartialLLM CompactionAction = "partial-llm"
	// ActionFullLLM 全量 LLM 重写。
	ActionFullLLM CompactionAction = "full-llm"
	// ActionSessionSplit 会话分割。
	ActionSessionSplit CompactionAction = "session-split"
	// ActionCheckpoint 检查点恢复（仅 1M 窗口的硬天花板）。
	ActionCheckpoint CompactionAction = "checkpoint"
)

// CompactionBilling 是计费模式。
//
// 对账 `CompactionBilling`。
type CompactionBilling string

const (
	// BillingPerToken 按 token 计费。
	BillingPerToken CompactionBilling = "per-token"
	// BillingSubscription 订阅制（扁平计费）。
	BillingSubscription CompactionBilling = "subscription"
)

// CompactionCache 是缓存能力。
//
// 对账 `CompactionCache`。
type CompactionCache string

const (
	CompactionCacheNone        CompactionCache = "none"
	CompactionCachePartial     CompactionCache = "partial"
	CompactionCacheExactPrefix CompactionCache = "exact-prefix"
)

// CompactionProfile 是压缩决策所需的 profile。
//
// 对账 `CompactionProfile`。分两层用途：
//
//   - **LLM 阶梯**：`Billing` × `Cache`（`LLMActionRatiosFor` 的判据）
//   - **reclaim 地板**：`WindowBand` / `ContextWindow` / `MinReclaimTokens` /
//     `MinReclaimRatio`（reclaim gate 判「回收是否够本」）
//
// 地板字段由 `deriveCompactionProfile` 填充。**零值语义**：手工构造的
// profile（不经 derive）地板为 0——此时 gate 只要求「有回收」即放行
// （`reclaimedTokens <= 0` 仍拒），不会因缺字段而误拒一切。
type CompactionProfile struct {
	Billing CompactionBilling
	Cache   CompactionCache

	// WindowBand 是窗口档位（reclaim 地板的分档依据）。
	WindowBand CompactionWindowBand
	// ContextWindow 是上下文窗口（token）。
	ContextWindow int
	// EffectiveInputBudget 是策略规划所用的输入预算。
	//
	// 第一版等于 ContextWindow——减去 max_tokens 会假设 provider 层存在
	// 「输入窗口 = 总窗口 − 输出预留」的契约，而该契约对我们的 provider
	// **未经核实**（TS plan §3.1）。OutputReserveTokens 是契约确认后的扩展点。
	EffectiveInputBudget int
	// OutputReserveTokens 是输出预留（nil = 未设置）。
	OutputReserveTokens *int
	// CacheWritePricePerMillion / CacheReadPricePerMillion 是缓存价格（nil = 未知）。
	CacheWritePricePerMillion *float64
	CacheReadPricePerMillion  *float64

	// MinReclaimTokens 是绝对地板：非 force 的重写至少要回收这么多 token。
	MinReclaimTokens int
	// MinReclaimRatio 是相对地板：reclaimed / beforeTokens 要达到该比例。
	MinReclaimRatio float64
}

// CompactCircuitBreakerState 是熔断器状态。
//
// 对账 `CompactCircuitBreakerState`。
type CompactCircuitBreakerState struct {
	ConsecutiveFailures int
	// DisabledUntilTurn 为 nil 表示未禁用。
	DisabledUntilTurn *int
}

// 熔断器常量（对账 TS 的字面量）。
const (
	// breakerFailureThreshold 是触发禁用的连续失败次数。
	breakerFailureThreshold = 3
	// breakerDisableTurns 是禁用持续的轮数。
	breakerDisableTurns = 3
	// largeWindowForCheckpoint 是走 checkpoint 而非 micro 的窗口下限。
	largeWindowForCheckpoint = 1_000_000
	// llmActionPartial 是默认阶梯的下档。
	llmActionPartial = 0.60
	// llmActionFull 是默认阶梯的上档。
	llmActionFull = 0.75
	// cachePreservingLLMPartial 是缓存保护阶梯的下档。
	cachePreservingLLMPartial = 0.75
	// cachePreservingLLMFull 是缓存保护阶梯的上档。
	cachePreservingLLMFull = 0.85
)

// LLMActionRatios 是 LLM 阶梯的两档。
//
// 对账 `{ partial, full }`。
type LLMActionRatios struct {
	Partial float64
	Full    float64
}

// LLMActionRatiosFor 按 billing × cache 派生 LLM 阶梯。
//
// 对账 `llmActionRatiosFor`：
//
//	per-token && exact-prefix → {0.75, 0.85}（缓存保护）
//	其余                       → {0.60, 0.75}
//
// **为什么 per-token + exact-prefix 要抬高阶梯**（TS 注释原文）：这类 provider
// 的前缀缓存是持久且精确的（DeepSeek），一次 LLM 重写会让用户**已经付费建立**
// 的前缀失效——回收必须大到值得重建。订阅制即使缓存保护也用基准阶梯：
// 扁平计费下提前回收只花延迟不花钱。
func LLMActionRatiosFor(profile CompactionProfile) LLMActionRatios {
	if profile.Billing == BillingPerToken && profile.Cache == CompactionCacheExactPrefix {
		return LLMActionRatios{Partial: cachePreservingLLMPartial, Full: cachePreservingLLMFull}
	}
	return LLMActionRatios{Partial: llmActionPartial, Full: llmActionFull}
}

// CompactActionInput 是动作决策的输入。
//
// 对账 `CompactActionInput`（= `CompactPolicyInput` + `profile`）。
type CompactActionInput struct {
	EstimatedTokens int
	MaxTokens       int
	Turn            int
	Failures        CompactCircuitBreakerState
	// ProviderProfile 用于比值覆盖与阶梯覆盖（nil = 用默认）。
	ProviderProfile *CompactRatioProfile
	// RecentHitRate 为 nil 时用基准比值（对账 `recentHitRate != null` 判据）。
	RecentHitRate *float64
	// PrecisionCeilingOverride 为 nil 时由窗口派生。
	PrecisionCeilingOverride *float64
	// Profile 是计费/缓存特征。
	Profile CompactionProfile
	// LLMLadderOverride 是 provider 级阶梯覆盖（如 spark 的「85% 前不重写」）。
	//
	// **优先于 billing/cache 派生的默认阶梯**（对账
	// `input.providerProfile?.compaction?.llmLadder ?? llmActionRatiosFor(...)`）。
	LLMLadderOverride *LLMActionRatios
}

// CompactActionDecision 是动作决策的结果。
//
// 对账 `CompactActionDecision`。
type CompactActionDecision struct {
	Action CompactionAction
	Reason string
	// Force 表示强制动作（硬天花板）**绕过 reclaim gate 与缓存顾问延迟**。
	Force bool
	// PrecisionRisk 表示上下文占用越过模型精度天花板。
	PrecisionRisk bool
	// Tier 是遗留档位，保留供观测与既有消费方。
	Tier          CompactTier
	ShouldCompact bool
}

// DecideCompactAction 是统一的窗口感知动作决策。
//
// 对账 `decideCompactAction`。取代旧的「1M 窗口提前返回专用 60%/75% 分支」——
// 那条路径绕过了 `decideCompactTier`，也就绕过了精度天花板。现在所有窗口共享
// 一套动作词汇，窗口只移动阈值：
//
//   - 硬天花板（0.95）：force——1M 给 `checkpoint`，小窗口给强制 `micro`。
//     **force 优先于熔断器**：超窗口请求是硬 API 失败，不是调优偏好。
//   - 熔断器开启：无自主动作。
//   - 1M LLM 阶梯：provider 相关，见 `LLMActionRatiosFor`。
//   - 精度带：越过精度天花板但在阶梯之下——表现为确定性的 `stale-round`，
//     **仍需过下游 reclaim gate 与缓存顾问延迟，永不强制 LLM 重写**。
//   - 其余：tier 策略决定确定性的 `micro`。
func DecideCompactAction(input CompactActionInput) CompactActionDecision {
	ratio := 1.0
	if input.MaxTokens > 0 {
		ratio = float64(input.EstimatedTokens) / float64(input.MaxTokens)
	}
	ceiling := PrecisionCeilingRatio(input.MaxTokens, input.PrecisionCeilingOverride)
	precisionRisk := ceiling < 1 && ratio >= ceiling

	var ratios CompactPolicyRatios
	if input.RecentHitRate != nil {
		ratios = AdaptiveCompactPolicyRatios(input.ProviderProfile, *input.RecentHitRate)
	} else {
		ratios = CompactPolicyRatiosFor(input.ProviderProfile)
	}

	// ── 硬天花板：force（优先于熔断器）──
	if ratio >= ratios.Ceiling {
		if input.MaxTokens >= largeWindowForCheckpoint {
			return CompactActionDecision{
				Action: ActionCheckpoint, Reason: "context ceiling exceeded; checkpoint-resume required",
				Force: true, PrecisionRisk: precisionRisk, Tier: TierCeiling, ShouldCompact: true,
			}
		}
		return CompactActionDecision{
			Action: ActionMicro, Reason: "context ceiling exceeded; forced deterministic reclaim",
			Force: true, PrecisionRisk: precisionRisk, Tier: TierCeiling, ShouldCompact: true,
		}
	}

	// ── 熔断器开启：无自主动作 ──
	if input.Failures.DisabledUntilTurn != nil && input.Turn < *input.Failures.DisabledUntilTurn {
		return CompactActionDecision{
			Action: ActionNone, Reason: "automatic compact circuit breaker is open",
			Force: false, PrecisionRisk: precisionRisk, Tier: TierNone, ShouldCompact: false,
		}
	}

	tierDecision := decideTierForAction(input, ratio, ceiling)

	if input.MaxTokens >= largeWindowForCheckpoint {
		// provider 阶梯覆盖优先于 billing/cache 派生
		ladder := LLMActionRatiosFor(input.Profile)
		if input.LLMLadderOverride != nil {
			ladder = *input.LLMLadderOverride
		}
		if ratio >= ladder.Full {
			return CompactActionDecision{
				Action: ActionFullLLM, Reason: fmt.Sprintf("full LLM compact ladder at %.0f%%", ratio*100),
				Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: true,
			}
		}
		if ratio >= ladder.Partial {
			return CompactActionDecision{
				Action: ActionPartialLLM, Reason: fmt.Sprintf("partial LLM compact ladder at %.0f%%", ratio*100),
				Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: true,
			}
		}
		if precisionRisk {
			return CompactActionDecision{
				Action: ActionStaleRound, Reason: "precision-risk: past accuracy ceiling; deterministic reclaim only (gated)",
				Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: true,
			}
		}
		return CompactActionDecision{
			Action: ActionNone, Reason: tierDecision.Reason,
			Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: false,
		}
	}

	if tierDecision.ShouldCompact {
		return CompactActionDecision{
			Action: ActionMicro, Reason: tierDecision.Reason,
			Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: true,
		}
	}
	return CompactActionDecision{
		Action: ActionNone, Reason: tierDecision.Reason,
		Force: false, PrecisionRisk: precisionRisk, Tier: tierDecision.Tier, ShouldCompact: false,
	}
}

// tierDecision 是动作决策内部的档位结果（对账 `CompactDecision`）。
type tierDecisionResult struct {
	Tier          CompactTier
	Reason        string
	ShouldCompact bool
}

// decideTierForAction 是动作决策内部的档位判定（对账 `decideCompactTier`）。
//
// 熔断器检查在调用方已处理（对账 TS 的两处重复检查——上层负责动作短路，
// 这里负责档位判定）。
func decideTierForAction(input CompactActionInput, ratio, ceiling float64) tierDecisionResult {
	ceilingPtr := &ceiling
	tier := TierForRatio(ratio, input.ProviderProfile, input.RecentHitRate, ceilingPtr)
	return tierDecisionResult{Tier: tier, Reason: ReasonForTier(tier), ShouldCompact: tier > 0}
}

// RecordCompactFailure 记录一次压缩失败并推进熔断器。
//
// 对账 `recordCompactFailure`：连续失败 +1；**达到阈值则禁用**（从当前轮起
// 3 轮）。**注意每次失败都重算 disabledUntilTurn**——已达阈值后继续失败会
// 把禁用窗口往后推（见 oracle 的 `fail-four`：turn 14 失败 → 禁用至 16）。
func RecordCompactFailure(state CompactCircuitBreakerState, turn int) CompactCircuitBreakerState {
	consecutive := state.ConsecutiveFailures + 1
	next := CompactCircuitBreakerState{ConsecutiveFailures: consecutive, DisabledUntilTurn: state.DisabledUntilTurn}
	if consecutive >= breakerFailureThreshold {
		until := turn + breakerDisableTurns
		next.DisabledUntilTurn = &until
	}
	return next
}

// RecordCompactSuccess 记录一次压缩成功并重置熔断器。
//
// 对账 `recordCompactSuccess`：**只保留 consecutiveFailures=0**——
// `disabledUntilTurn` 被丢弃（成功即解禁）。
func RecordCompactSuccess() CompactCircuitBreakerState {
	return CompactCircuitBreakerState{ConsecutiveFailures: 0}
}

// CompactThresholds 是压缩阈值（token 数）。
//
// 对账 `CompactThresholds`。
type CompactThresholds struct {
	AutoThreshold       int
	AutoFloor           int
	ToolResultMaxTokens int
}

// CompactStrategyInput 是阈值计算的具名输入。
//
// 对账 `CompactStrategyInput`。
type CompactStrategyInput struct {
	ContextWindow   int
	ProviderProfile *CompactRatioProfile
}

// 阈值常量（对账 TS 字面量）。
const (
	// toolResultHardCapLarge 是大窗口的工具结果硬上限。
	toolResultHardCapLarge = 200_000
	// toolResultHardCapSmall 是中小窗口的工具结果硬上限。
	toolResultHardCapSmall = 100_000
	// toolResultRatio 是工具结果上限占窗口的比例。
	toolResultRatio = 0.3
)

// CompactThresholdsFor 计算压缩阈值。
//
// 对账 `compactThresholds`——**两种重载**：
//
//	数字输入 → 硬编码比值 {watch:0.6, compact:0.78, reactive:0.8, ceiling:0.95}
//	具名输入 → compactPolicyRatios(providerProfile)
//
// **注意两者的 reactive 不同**（0.8 vs 0.88）——这是 TS 的历史遗留：数字重载
// 是旧路径。Go 侧用 `compactThresholdsNumeric` / `CompactThresholdsForProfile`
// 两个函数显式区分，避免调用方误用。
func compactThresholdsNumeric(contextWindow int) CompactThresholds {
	// 对账数字重载路径的硬编码比值（**reactive 是 0.8 而非 0.88**）
	const legacyReactive = 0.8
	const legacyWatch = 0.6
	return compactThresholdsFromRatios(contextWindow, CompactPolicyRatios{
		Watch: legacyWatch, Compact: 0.78, Reactive: legacyReactive, Ceiling: 0.95,
	})
}

// CompactThresholdsForProfile 按 profile 计算阈值（具名路径）。
func CompactThresholdsForProfile(input CompactStrategyInput) CompactThresholds {
	return compactThresholdsFromRatios(input.ContextWindow, CompactPolicyRatiosFor(input.ProviderProfile))
}

// compactThresholdsFromRatios 是共用的阈值计算。
func compactThresholdsFromRatios(contextWindow int, ratios CompactPolicyRatios) CompactThresholds {
	hardCap := toolResultHardCapSmall
	if contextWindow >= LargeContextWindowTokens {
		hardCap = toolResultHardCapLarge
	}
	toolResultMax := int(math.Floor(float64(contextWindow) * toolResultRatio))
	if toolResultMax > hardCap {
		toolResultMax = hardCap
	}
	return CompactThresholds{
		AutoThreshold: int(math.Floor(float64(contextWindow) * ratios.Reactive)),
		// **纯比值，不做 Math.min(..., 500K) 钳制**（TS 注释原文）：旧的钳制把
		// 1M 缓存保护窗口的 watch 地板从 720K 拖到 500K，让 watch 档在窗口 50%
		// 就触发，而非预期的 72%。
		AutoFloor:           int(math.Floor(float64(contextWindow) * ratios.Watch)),
		ToolResultMaxTokens: toolResultMax,
	}
}

// CompactThresholdsForWindow 是数字重载路径的导出入口。
//
// **仅供对账旧路径**——新代码应使用 `CompactThresholdsForProfile`。
func CompactThresholdsForWindow(contextWindow int) CompactThresholds {
	return compactThresholdsNumeric(contextWindow)
}
