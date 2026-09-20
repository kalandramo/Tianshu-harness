package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/compact"
)

// TestCompactBoundary_ReclaimGateBlocksUnprofitable —— **接线集成测试**。
//
// 验证 reclaim gate 真的在 `MaybeCompact` 路径上生效：候选回收不够本时
// **不提交**（原消息原样保留），而非无条件替换历史。
//
// 对账 TS `compact-boundary-coordinator.ts:254` 的 `buildReclaimDecision` 接线。
//
// **构造思路**（需要精确控制回收量——首版构造错了，实测走了放行分支）：
//
//	截断目标 = floor(contextWindow × 0.3)   （ToolResultMaxTokens）
//	回收量   ≈ tool 结果 token − 截断目标
//
// 故取窗口 20000 → 截断目标 6000 token；tool 结果约 8000 token → 回收约
// 2000 token，**低于 per-token+exact-prefix 的地板 8192**。
func TestCompactBoundary_ReclaimGateBlocksUnprofitable(t *testing.T) {
	// 窗口 20000 → ToolResultMaxTokens = 6000。
	// tool 结果 32000 字符 ≈ 8000 token → 截断后回收约 2000 token < 地板 8192。
	msgs := []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "锚用户"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
		wire.NewOrderedMap().Set("role", "assistant").
			Set("content", "").
			Set("tool_calls", []any{
				wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
					Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").
			Set("content", strings.Repeat("x", 32000)),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手1"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户1"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手2"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户2"),
	}

	// per-token + exact-prefix → 最高地板（保护已付费前缀）。
	b := NewCompactBoundary(20000)
	b.Profile = compact.CompactionProfile{
		Billing: compact.BillingPerToken,
		Cache:   compact.CompactionCacheExactPrefix,
	}
	// 强制触发压缩（用极低阈值）——我们测的是 gate，不是决策层。
	b.ProviderProfile = &compact.CompactRatioProfile{
		CacheType:    compact.CacheExactPrefix,
		Persistent:   true,
		HasOverrides: true,
		Ratios: compact.CompactPolicyRatios{
			Watch: 0.001, Compact: 0.002, Reactive: 0.003, Ceiling: 0.95,
		},
	}

	oai := orderedMapsToOai(msgs)
	_, changed := b.MaybeCompact(oai, 0)

	// 决策必须发生过（否则测的不是 gate）。
	if b.LastDecision == nil || b.LastDecision.Action == compact.ActionNone {
		t.Fatalf("应触发压缩决策，实得 %+v", b.LastDecision)
	}
	// gate 决策必须被记录（提交或拒绝都要记录——可观测性契约）。
	if b.LastReclaimDecision == nil {
		t.Fatal("reclaim gate 决策未被记录（LastReclaimDecision 为 nil）")
	}

	// **本用例必须命中「拒绝」**——否则它测不到想测的东西。
	if b.LastReclaimDecision.Commit {
		t.Fatalf("构造应使回收低于地板而被拒，实际放行（reclaimed=%d floor=%d ratio=%v floorRatio=%v）——"+
			"调整 tool 结果大小/窗口",
			b.LastReclaimDecision.ReclaimedTokens,
			b.reclaimProfile().MinReclaimTokens,
			b.LastReclaimDecision.ReclaimRatio,
			b.reclaimProfile().MinReclaimRatio)
	}
	if b.LastReclaimDecision.Reason != compact.SkipBelowReclaimFlr {
		t.Errorf("拒绝理由应为 below-reclaim-floor，实得 %q", b.LastReclaimDecision.Reason)
	}

	// gate 拒了 → 历史不得改动。
	if changed {
		t.Error("gate 拒绝时不得改动历史")
	}
	if b.LastReclaimed != 0 {
		t.Errorf("gate 拒绝时 LastReclaimed 应为 0，实得 %d", b.LastReclaimed)
	}
	t.Logf("gate 正确拒绝：reason=%q reclaimed=%d floor=%d",
		b.LastReclaimDecision.Reason,
		b.LastReclaimDecision.ReclaimedTokens,
		b.reclaimProfile().MinReclaimTokens)
}

// TestCompactBoundary_ReclaimGateCommitsProfitable —— 回收够本时正常提交。
//
// 与上一条对偶：制造「回收量远超地板」的场景，断言提交且历史真的变了。
func TestCompactBoundary_ReclaimGateCommitsProfitable(t *testing.T) {
	// 巨型 tool 结果——截断后回收量巨大，必过任何地板。
	msgs := []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "锚用户"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
		wire.NewOrderedMap().Set("role", "assistant").
			Set("content", "").
			Set("tool_calls", []any{
				wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
					Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").
			Set("content", strings.Repeat("x", 300000)), // 约 75K token
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手1"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "近用户1"),
	}

	b := NewCompactBoundary(100000)
	oai := orderedMapsToOai(msgs)
	_, changed := b.MaybeCompact(oai, 0)

	if !changed {
		t.Fatalf("大额回收应提交，但 changed=false（gate=%+v）", b.LastReclaimDecision)
	}
	if b.LastReclaimDecision == nil {
		t.Fatal("gate 决策未被记录")
	}
	if !b.LastReclaimDecision.Commit {
		t.Errorf("gate 应放行大额回收，实得 reason=%q reclaimed=%d floor=%d",
			b.LastReclaimDecision.Reason,
			b.LastReclaimDecision.ReclaimedTokens,
			b.reclaimProfile().MinReclaimTokens)
	}
	if b.LastReclaimDecision.Reason != compact.CommitAboveFloor {
		t.Errorf("放行理由应为 reclaim-above-floor，实得 %q", b.LastReclaimDecision.Reason)
	}
	if b.LastReclaimed == 0 {
		t.Error("提交时 LastReclaimed 应为正数")
	}
}

// TestCompactBoundary_ForceBypassesFloor —— force 绕过 reclaim 地板。
//
// 对账 TS：`heapEmergency` 是 force 动作（替代方案是 OOM）。
// 这里用硬天花板（Ceiling）触发 force，断言即使地板高也提交。
func TestCompactBoundary_ForceBypassesFloor(t *testing.T) {
	msgs := []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "锚用户"),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "锚助手"),
		wire.NewOrderedMap().Set("role", "assistant").
			Set("content", "").
			Set("tool_calls", []any{
				wire.NewOrderedMap().Set("id", "read_file_1").Set("type", "function").
					Set("function", wire.NewOrderedMap().Set("name", "read_file").Set("arguments", "{}")),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "read_file_1").
			Set("content", strings.Repeat("x", 60000)),
		wire.NewOrderedMap().Set("role", "assistant").Set("content", "近助手1"),
	}

	// 窗口设得比历史小 → 越过 0.95 硬天花板 → force。
	b := NewCompactBoundary(10000)
	oai := orderedMapsToOai(msgs)
	_, changed := b.MaybeCompact(oai, 0)

	if b.LastDecision == nil {
		t.Fatal("应有决策")
	}
	if !b.LastDecision.Force {
		t.Skipf("本用例依赖 force 触发，实际决策未 force（action=%q reason=%q）——"+
			"窗口/历史比例未达天花板，调整构造", b.LastDecision.Action, b.LastDecision.Reason)
	}
	if !changed {
		t.Fatalf("force 应绕过地板提交，但 changed=false（gate=%+v）", b.LastReclaimDecision)
	}
	if b.LastReclaimDecision.Reason != compact.CommitForced {
		t.Errorf("force 提交理由应为 forced，实得 %q", b.LastReclaimDecision.Reason)
	}
}

// TestCompactBoundary_GateRecordsDecisionAlways —— 可观测性契约。
//
// 决策被做出时（非 none），gate 记录**必然**产出——提交或拒绝都记录。
// 这是 TS `onReclaimDecision` 回调的语义。
func TestCompactBoundary_GateRecordsDecisionAlways(t *testing.T) {
	msgs := []*wire.OrderedMap{
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
	b := NewCompactBoundary(100000)
	oai := orderedMapsToOai(msgs)
	b.MaybeCompact(oai, 0)

	if b.LastDecision.Action == compact.ActionNone {
		t.Skip("本用例需要非 none 决策")
	}
	if b.LastReclaimDecision == nil {
		t.Fatal("决策非 none 时 gate 记录不得为 nil")
	}
	// 记录必须带完整的可观测字段。
	if b.LastReclaimDecision.WindowBand == "" {
		t.Error("记录应带 windowBand（可观测性契约）")
	}
	if b.LastReclaimDecision.Billing == "" {
		t.Error("记录应带 billing")
	}
	if b.LastReclaimDecision.Cache == "" {
		t.Error("记录应带 cache")
	}
	if b.LastReclaimDecision.Action != b.LastDecision.Action {
		t.Errorf("记录 action 应与决策一致：gate=%q decision=%q",
			b.LastReclaimDecision.Action, b.LastDecision.Action)
	}
}

// TestCompactBoundary_NoneDecisionLeavesGateNil —— `none` 决策不产出 gate 记录。
//
// 对账：`action === 'none'` 时短路返回，不进入 gate。
func TestCompactBoundary_NoneDecisionLeavesGateNil(t *testing.T) {
	b := NewCompactBoundary(1_000_000) // 大窗口 + 小历史 → 不触发
	oai := orderedMapsToOai([]*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "user").Set("content", "短"),
	})
	_, changed := b.MaybeCompact(oai, 0)
	if changed {
		t.Fatal("小历史不应触发压缩")
	}
	if b.LastReclaimDecision != nil {
		t.Errorf("none 决策不应产出 gate 记录，实得 %+v", b.LastReclaimDecision)
	}
}
