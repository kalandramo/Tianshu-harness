package agent

import (
	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// CompactBoundary 是压缩的**确定性路径**接线。
//
// 对账 TS 的 `CompactBoundaryCoordinator.runCompaction` → `maybeCompact` 的
// **micro / stale-round 分支**（LLM 路径需 summaryClient，本刀不接）。
//
// **为什么需要这一层**：`internal/compact` 的判定层（`DecideCompactAction`）与
// 执行层（`MicroCompactOai`）此前都只有测试调用方——本层把它们串起来，
// 让压缩在真实 turn 边界上被触发。
//
// **边界语义**（对账 TS turn-orchestrator Step 6b）：压缩**只在 turn 边界**运行，
// 不在 mid-turn 改历史。理由：mid-turn 改历史会让已发出的请求前缀失效，
// 前缀缓存命中率归零。
type CompactBoundary struct {
	// ContextWindow 是上下文窗口（token）。
	ContextWindow int
	// Profile 是计费/缓存画像（决定 **LLM 阶梯**——per-token + exact-prefix
	// 会抬升阶梯到 0.75/0.85 以保护已付费的前缀）。
	Profile compact.CompactionProfile
	// ProviderProfile 决定**策略阈值**（Watch / Compact / Reactive / Ceiling）。
	//
	// ⚠️ **与 Profile 是两个不同的输入**：nil 时走 balanced（Watch=0.6），
	// 非 nil 时按 cacheType 推导（none→aggressive Watch=0.5、
	// exact-prefix+persistent→cache-preserving Watch=0.72）。
	//
	// **踩过的坑**：只设 Profile 不设 ProviderProfile，会让阈值停在 balanced，
	// 而 Profile 的 cache=exact-prefix 只改了阶梯——两者不联动。
	// 对账 TS：`decideCompactAction` 同时收 `providerProfile` 与 `profile`。
	ProviderProfile *compact.CompactRatioProfile
	// RecentHitRate 是近期缓存命中率（nil = 未知，走中性判定）。
	RecentHitRate *float64
	// Failures 是熔断器状态（跨轮持有）。
	Failures compact.CompactCircuitBreakerState
	// LLMLadder 是 LLM 动作阶梯覆盖（nil = 用 profile 派生的基准阶梯）。
	LLMLadder *compact.LLMActionRatios

	// LastDecision 是最近一次决策（供观测/测试断言）。
	LastDecision *compact.CompactActionDecision
	// LastReclaimDecision 是最近一次 reclaim gate 决策（供观测/测试断言）。
	//
	// 对账 TS 的 `onReclaimDecision` 回调——**提交与拒绝都记录**，让
	// 「压了但没回收」在离线可见，而非伪装成一次成功的压缩。
	LastReclaimDecision *compact.ReclaimDecisionRecord
	// LastReclaimed 是最近一次实际回收的消息数。
	LastReclaimed int
}

// NewCompactBoundary 创建压缩边界（默认订阅制 + 无缓存保护）。
//
// 对账 TS 的 `deriveCompactionProfile` 缺省分支。
func NewCompactBoundary(contextWindow int) *CompactBoundary {
	return &CompactBoundary{
		ContextWindow: contextWindow,
		Profile: compact.CompactionProfile{
			Billing: compact.BillingSubscription,
			Cache:   compact.CompactionCacheNone,
		},
		// cache=none → aggressive 策略（Watch=0.5）——无缓存保护时该压就压。
		ProviderProfile: &compact.CompactRatioProfile{CacheType: compact.CacheNone},
	}
}

// MaybeCompact 在 turn 边界尝试压缩。
//
// 对账 TS `maybeCompact`。**返回是否真的改动了历史**。
//
// **流程**（确定性路径）：
//
//  1. 估算当前 token 数（`context.EstimateOaiTokens`——rounds 口径）
//  2. `DecideCompactAction` 决策
//  3. `action == none` → 短路返回 false（不记失败）
//  4. 熔断器开启且非 force → 短路（**force 优先于熔断器**）
//  5. `MicroCompactOai` 执行
//  6. 成功 → `RecordCompactSuccess`（重置熔断器）
//
// **未接**：LLM 路径（partial-llm / full-llm / checkpoint）、reclaim gate、
// 缓存顾问延迟、session split。见 PLAN.md 的架构欠账。
func (b *CompactBoundary) MaybeCompact(messages []session.OaiMessage, turn int) ([]session.OaiMessage, bool) {
	if b.ContextWindow <= 0 {
		return messages, false
	}

	// token 估算走 **rounds 口径**（对账 TS：maybeCompact 用 session 的
	// getEstimatedTokens，其底层是 rounds 的估算器）。注意这与
	// `compact.EstimateOaiTokens`（micro 口径，6 个 CJK 区间 / cjk÷1.2）
	// **不同**——两套口径并存是 TS 的历史遗留，见 compact/micro.go 的说明。
	estimated := 0
	for _, m := range messages {
		estimated += context.EstimateOaiMessageTokens(m)
	}

	decision := compact.DecideCompactAction(compact.CompactActionInput{
		EstimatedTokens:   estimated,
		MaxTokens:         b.ContextWindow,
		Turn:              turn,
		Profile:           b.Profile,
		ProviderProfile:   b.ProviderProfile,
		Failures:          b.Failures,
		RecentHitRate:     b.RecentHitRate,
		LLMLadderOverride: b.LLMLadder,
	})
	b.LastDecision = &decision

	// `none` = 无需压缩（不是失败）。
	//
	// **注意这是经济否决而非错误**——熔断器开启、ratio 未达阈值都会走到这里。
	// 对账 TS：`if (actionDecision.action === 'none') return { compacted: false }`，
	// 且**不记失败**（熔断器只跟踪管线错误，不跟踪经济否决）。
	if decision.Action == compact.ActionNone {
		return messages, false
	}

	// LLM 路径未接——本刀只走确定性路径。
	//
	// **为什么不静默跳过**：静默会让「决策说该压缩但没压」变成不可观测的
	// 黑洞。这里显式记录到 LastDecision，由调用方决定如何呈现。
	if !isDeterministicAction(decision.Action) {
		return messages, false
	}

	// **熔断器检查不在这里**——`DecideCompactAction` 内部已做（熔断器开启时
	// 直接返回 `action=none` + reason "automatic compact circuit breaker is
	// open"）。此处再查一遍是**死分支**：变异测试证明删掉它行为不变
	// （B1 变异红 0），因为 `none` 短路已经拦住了。
	//
	// **force 优先于熔断器**的语义同样在决策层实现（ceiling 分支在 breaker
	// 分支之前）——超窗口请求是硬 API 失败，不该被熔断器拦住。

	result := compact.MicroCompactOai(messages, b.ContextWindow, estimated)

	// ── reclaim gate：候选必须证明回收够本 ──
	//
	// 对账 TS `compact-boundary-coordinator.ts:254` 的 `buildReclaimDecision`
	// 接线。**这是 gate 存在的理由**：确定性重写曾无条件提交，导致只回收
	// 几百 token（有时甚至让输入变大）却击碎 200k+ token 的热前缀缓存。
	//
	// `decision.Force` 来自决策层（硬天花板分支）——**force 绕过地板**，
	// 因为替代方案是 OOM 或超窗口 API 失败。
	reclaimDecision := compact.BuildReclaimDecision(
		decision.Action,
		compact.EstimateReclaim(messages, result.Messages),
		b.reclaimProfile(),
		decision.Force,
	)
	b.LastReclaimDecision = &reclaimDecision

	if !reclaimDecision.Commit {
		// 不够本（或未改动）——**原消息原样保留**，下个边界重试。
		//
		// 不算失败：这是经济否决（与 `none` 同类），熔断器只跟踪管线错误。
		b.LastReclaimed = 0
		return messages, false
	}

	b.LastReclaimed = result.Truncated
	if result.Truncated == 0 {
		// 决策说该压、gate 也放行，但没压出东西——不算失败（内容都太短）。
		return messages, false
	}

	b.Failures = compact.RecordCompactSuccess()
	return result.Messages, true
}

// reclaimProfile 返回 reclaim gate 用的 profile。
//
// 对账 TS `reclaimProfile()`：优先用注入的 profile，否则按 economics 派生。
// Go 侧无 `getCompactionProfile` 注入点，故直接由 `Profile` 的 billing/cache
// 与 `ContextWindow` 派生（语义等价：TS 的缺省分支用同一组输入）。
//
// **注意派生而非直接用 b.Profile**：`b.Profile` 的地板字段可能是零值
// （手工构造的 profile 不经 derive）。派生保证地板与窗口/billing/cache 一致。
func (b *CompactBoundary) reclaimProfile() compact.CompactionProfile {
	return compact.DeriveCompactionProfile(compact.CompactionProfileInput{
		ContextWindow: b.ContextWindow,
		Billing:       b.Profile.Billing,
		Cache:         b.Profile.Cache,
	})
}

// isDeterministicAction 报告动作是否属于确定性路径。
//
// 对账 TS `maybeCompact` 末尾的注释：「Deterministic actions ('micro' /
// 'stale-round') from here on」。
func isDeterministicAction(a compact.CompactionAction) bool {
	return a == compact.ActionMicro || a == compact.ActionStaleRound
}

// orderedMapsToOai 把 loop 的 wire 形态转成 session.OaiMessage。
//
// **为什么需要**：loop 用 `[]*wire.OrderedMap` 存历史（保插入序，前缀缓存的
// 前提），而 `internal/compact` 吃 `[]session.OaiMessage`（结构化形态）。
// 两者用途不同——转换只读取所需字段，**不重建 KeyOrder**（压缩后的消息
// 要回写，键序由调用方按原序重建）。
func orderedMapsToOai(messages []*wire.OrderedMap) []session.OaiMessage {
	out := make([]session.OaiMessage, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		out = append(out, orderedMapToOai(m))
	}
	return out
}

func orderedMapToOai(m *wire.OrderedMap) session.OaiMessage {
	msg := session.OaiMessage{}
	if v, ok := m.Get("role"); ok {
		if s, ok := v.(string); ok {
			msg.Role = s
		}
	}
	if v, ok := m.Get("content"); ok {
		if s, ok := v.(string); ok {
			msg.Content = &s
		}
	}
	if v, ok := m.Get("tool_call_id"); ok {
		if s, ok := v.(string); ok {
			msg.ToolCallID = s
		}
	}
	if v, ok := m.Get("tool_calls"); ok {
		if calls, ok := v.([]any); ok {
			msg.ToolCalls = toOaiToolCalls(calls)
		}
	}
	// 其余字段透传（reasoning_content 等）——token 估算会用到。
	extra := map[string]any{}
	for _, k := range m.Keys() {
		switch k {
		case "role", "content", "tool_call_id", "tool_calls":
			continue
		}
		if v, ok := m.Get(k); ok {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		msg.Extra = extra
	}
	msg.KeyOrder = m.Keys()
	return msg
}

func toOaiToolCalls(raw []any) []session.OaiToolCall {
	out := make([]session.OaiToolCall, 0, len(raw))
	for _, item := range raw {
		om, ok := item.(*wire.OrderedMap)
		if !ok {
			continue
		}
		tc := session.OaiToolCall{}
		if v, ok := om.Get("id"); ok {
			if s, ok := v.(string); ok {
				tc.ID = s
			}
		}
		if v, ok := om.Get("type"); ok {
			if s, ok := v.(string); ok {
				tc.Type = s
			}
		}
		if v, ok := om.Get("function"); ok {
			if fn, ok := v.(*wire.OrderedMap); ok {
				f := &session.OaiFunction{}
				if n, ok := fn.Get("name"); ok {
					if s, ok := n.(string); ok {
						f.Name = s
					}
				}
				if a, ok := fn.Get("arguments"); ok {
					if s, ok := a.(string); ok {
						f.Arguments = s
					}
				}
				tc.Function = f
			}
		}
		out = append(out, tc)
	}
	return out
}

// oaiToOrderedMaps 把压缩后的 OaiMessage 回写成 loop 的 wire 形态。
//
// **键序**：用 `msg.KeyOrder`（原始序）重建——**压缩不改变键序**，
// 只改 content。这是前缀缓存安全的前提。
func oaiToOrderedMaps(messages []session.OaiMessage) []*wire.OrderedMap {
	out := make([]*wire.OrderedMap, 0, len(messages))
	for _, m := range messages {
		om := wire.NewOrderedMap()
		// 按 KeyOrder 重建（nil 时用默认序）。
		order := m.KeyOrder
		if len(order) == 0 {
			order = defaultKeyOrder(m)
		}
		for _, k := range order {
			switch k {
			case "role":
				om.Set("role", m.Role)
			case "content":
				if m.Content != nil {
					om.Set("content", *m.Content)
				} else {
					om.Set("content", "")
				}
			case "tool_call_id":
				if m.ToolCallID != "" {
					om.Set("tool_call_id", m.ToolCallID)
				}
			case "tool_calls":
				if m.ToolCalls != nil {
					om.Set("tool_calls", fromOaiToolCalls(m.ToolCalls))
				}
			default:
				if m.Extra != nil {
					if v, ok := m.Extra[k]; ok {
						om.Set(k, v)
					}
				}
			}
		}
		out = append(out, om)
	}
	return out
}

func defaultKeyOrder(m session.OaiMessage) []string {
	order := []string{"role"}
	if m.Content != nil {
		order = append(order, "content")
	}
	if m.ToolCalls != nil {
		order = append(order, "tool_calls")
	}
	if m.ToolCallID != "" {
		order = append(order, "tool_call_id")
	}
	return order
}

func fromOaiToolCalls(calls []session.OaiToolCall) []any {
	out := make([]any, 0, len(calls))
	for _, c := range calls {
		om := wire.NewOrderedMap().Set("id", c.ID).Set("type", c.Type)
		fn := wire.NewOrderedMap()
		if c.Function != nil {
			fn.Set("name", c.Function.Name).Set("arguments", c.Function.Arguments)
		}
		om.Set("function", fn)
		out = append(out, om)
	}
	return out
}
