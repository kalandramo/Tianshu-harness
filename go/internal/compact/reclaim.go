package compact

import (
	"encoding/json"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// reclaim gate —— 有效回收量闸门。
//
// 对账 src/compact/reclaim-estimate.ts。
//
// # 为什么需要它
//
// 确定性重写（micro / stale-round）曾**无条件提交**候选。会话 2c1186f5 显示：
// 重写只回收 617–1,701 token（有时甚至让输入变大），却每次都击碎 200k+ token
// 的热前缀缓存。这两个纯函数位于「候选已产出」与「历史被替换」之间——
// 候选必须证明回收量值得这次缓存重建，否则原消息原样保留、下个边界重试。
//
// token 数学用 `EstimateOaiTokens`（与压缩阶梯同一估算器）——**不是消息条数，
// 也不是原始字符长度**。

// ReclaimEstimate 是一次候选的回收估算。
//
// 对账 `ReclaimEstimate`。
type ReclaimEstimate struct {
	BeforeTokens    int
	AfterTokens     int
	ReclaimedTokens int
	// ReclaimRatio 是 reclaimedTokens / beforeTokens，无回收时钳为 0。
	ReclaimRatio float64
	// Changed 表示候选与原消息**有任何差异**（引用或字节）。
	Changed bool
}

// ReclaimSkipReason / ReclaimCommitReason —— 判定理由（对账 TS 的联合类型）。
type ReclaimSkipReason string

const (
	SkipUnchanged       ReclaimSkipReason = "unchanged"
	SkipNoReclaim       ReclaimSkipReason = "no-reclaim"
	SkipBelowReclaimFlr ReclaimSkipReason = "below-reclaim-floor"
	CommitAboveFloor    ReclaimSkipReason = "reclaim-above-floor"
	CommitForced        ReclaimSkipReason = "forced"
)

// ReclaimVerdict 是闸门判定结果。
//
// 对账 `ReclaimVerdict`。Reason 取值跨「提交」与「跳过」两族
// （TS 用 `ReclaimSkipReason | ReclaimCommitReason` 联合）。
type ReclaimVerdict struct {
	Commit bool
	Reason ReclaimSkipReason
}

// ReclaimDecisionRecord 是一次闸门决策的结构化记录。
//
// 对账 `ReclaimDecisionRecord`——cache-log `event:'compact_decision'` 行的
// 可观测性契约。**提交与拒绝都产出**，让「压了但没回收」在离线可见，
// 而不是伪装成一次成功的压缩。
type ReclaimDecisionRecord struct {
	ReclaimEstimate
	Action     CompactionAction
	Commit     bool
	Reason     ReclaimSkipReason
	Force      bool
	WindowBand CompactionWindowBand
	Billing    CompactionBilling
	Cache      CompactionCache
}

// messagesChanged 判定候选是否真的改动过。
//
// 对账 `messagesChanged`：
//  1. 同一引用（切片头相同）→ 未改动
//  2. 长度不同 → 改动
//  3. 逐条比对引用；引用不同时**比序列化字节**——避免「重建了等价消息」
//     的假阳性（transforms 常重建结构相同的消息）
func messagesChanged(before, after []session.OaiMessage) bool {
	if len(before) == 0 && len(after) == 0 {
		return false
	}
	// 切片同引用：Go 无法直接比较切片头，但可用「同长度且逐元素同引用」近似。
	// TS 的 `before === after` 是数组引用相等——Go 侧由调用方传同一切片时，
	// 元素引用也相同，故下面的逐元素比对会命中「引用相同」而返回 false。
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		// 比序列化字节（对账 TS：先比引用，不同则比 JSON）。
		// Go 的结构体无引用语义，故直接比字节——语义等价且更直接。
		if !oaiMessagesEqual(before[i], after[i]) {
			return true
		}
	}
	return false
}

// oaiMessagesEqual 比较两条消息是否字节等价（用 JSON 序列化）。
func oaiMessagesEqual(a, b session.OaiMessage) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		// 无法序列化时退化为保守判断（视为不同）。
		return false
	}
	return string(ab) == string(bb)
}

// EstimateReclaim 估算一次候选的回收量。
//
// 对账 `estimateReclaim`。
func EstimateReclaim(before, after []session.OaiMessage) ReclaimEstimate {
	beforeTokens := EstimateOaiTokens(before)
	afterTokens := EstimateOaiTokens(after)
	reclaimed := beforeTokens - afterTokens

	ratio := 0.0
	if beforeTokens > 0 && reclaimed > 0 {
		ratio = float64(reclaimed) / float64(beforeTokens)
	}

	return ReclaimEstimate{
		BeforeTokens:    beforeTokens,
		AfterTokens:     afterTokens,
		ReclaimedTokens: reclaimed,
		ReclaimRatio:    ratio,
		Changed:         messagesChanged(before, after),
	}
}

// ShouldCommitReclaim 判定候选重写是否可以替换历史。
//
// 对账 `shouldCommitReclaim`。五条分支，顺序敏感：
//
//  1. **未改动永不提交**——即使 force。提交它仍会推进 appendix 基线与
//     compact 标记，纯副作用无收益。
//  2. force=true（堆紧急 / 上下文天花板 / 硬会话分割）→ 提交任何**已改动**的
//     候选，无视地板——替代方案是 OOM 或超窗口 API 失败。
//  3. 无回收（<=0）→ 拒。
//  4. 绝对地板或相对地板未达 → 拒。
//  5. 否则提交。
func ShouldCommitReclaim(est ReclaimEstimate, profile CompactionProfile, force bool) ReclaimVerdict {
	if !est.Changed {
		return ReclaimVerdict{Commit: false, Reason: SkipUnchanged}
	}
	if force {
		return ReclaimVerdict{Commit: true, Reason: CommitForced}
	}
	if est.ReclaimedTokens <= 0 {
		return ReclaimVerdict{Commit: false, Reason: SkipNoReclaim}
	}
	if est.ReclaimedTokens < profile.MinReclaimTokens || est.ReclaimRatio < profile.MinReclaimRatio {
		return ReclaimVerdict{Commit: false, Reason: SkipBelowReclaimFlr}
	}
	return ReclaimVerdict{Commit: true, Reason: CommitAboveFloor}
}

// BuildReclaimDecision 构造带上下文的决策记录。
//
// 对账 `buildReclaimDecision`。
func BuildReclaimDecision(action CompactionAction, est ReclaimEstimate, profile CompactionProfile, force bool) ReclaimDecisionRecord {
	v := ShouldCommitReclaim(est, profile, force)
	return ReclaimDecisionRecord{
		ReclaimEstimate: est,
		Action:          action,
		Commit:          v.Commit,
		Reason:          v.Reason,
		Force:           force,
		WindowBand:      profile.WindowBand,
		Billing:         profile.Billing,
		Cache:           profile.Cache,
	}
}
