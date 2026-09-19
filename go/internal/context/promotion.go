package context

import (
	"os"
	"path/filepath"
)

// ClaimStatusCounts 是 claim 状态计数。
//
// 对账 ClaimStatusCounts。
type ClaimStatusCounts struct {
	Active           int
	Stale            int
	Conflicted       int
	Durable          int
	DurableCandidate int
	Quarantined      int
	// RecallBlocked 是被召回门禁挡住的 claim 数（证据文件已不存在）。
	RecallBlocked int
}

// CanRecallClaim 判定 claim 是否可召回（NREM 巩固过滤器）。
//
// 对账 canRecallClaim。**原则**：只巩固仍可检索、可验证的信息。
// 若证据文件已被删除或移走，claim 的基础不可恢复——晋升被阻断。
//
// **cwd 为空时跳过检查**（对账 TS：`if (!cwd) return true`）——
// 让无文件系统的测试路径不被阻断。
//
// **注意**：TS 用 `existsSync(join(cwd, p))`——相对路径拼接。Go 侧同义。
func CanRecallClaim(c ContextClaim, cwd string) bool {
	if cwd == "" {
		return true
	}
	hasFileEvidence := false
	for _, e := range c.Evidence {
		if e.Path == "" {
			continue
		}
		hasFileEvidence = true
		if _, err := os.Stat(filepath.Join(cwd, e.Path)); err == nil {
			// 至少一个证据文件仍存在
			return true
		}
	}
	// 无文件证据 → 无需召回检查
	return !hasFileEvidence
}

// EvaluatePromotion 评估 claim 是否可晋升。
//
// 对账 evaluatePromotion。返回 nil 表示不晋升（对账 TS 的 `null`）。
//
// **规则**：
//
//	active → durable_candidate：需 ≥3 个**去重**消费者
//	durable_candidate → durable：需年龄 ≥10 分钟 **且** ≥5 个去重消费者
//	其他状态：不晋升
//
// **前置门禁**：不可进 prompt 的 claim（过期/状态不符）与有反证的 claim 不晋升。
func EvaluatePromotion(c ContextClaim, now int64) *ContextClaimStatus {
	if !IsPromptEligibleClaim(c, now) {
		return nil
	}
	if len(c.Counterevidence) > 0 {
		return nil
	}

	switch c.Status {
	case StatusActive:
		// **去重**计数——TS 用 `new Set(consumers.map(c => c.id)).size`
		if uniqueConsumerCount(c.Consumers) < 3 {
			return nil
		}
		s := StatusDurableCandidate
		return &s

	case StatusDurableCandidate:
		age := now - c.CreatedAt
		if age < 10*60_000 {
			return nil
		}
		if uniqueConsumerCount(c.Consumers) < 5 {
			return nil
		}
		s := StatusDurable
		return &s
	}

	return nil
}

// uniqueConsumerCount 数去重的消费者 id 数。
//
// 对账 TS 的 `new Set(claim.consumers.map(c => c.id)).size`。
func uniqueConsumerCount(consumers []ConsumerRef) int {
	if len(consumers) == 0 {
		return 0
	}
	seen := make(map[string]bool, len(consumers))
	for _, c := range consumers {
		seen[c.ID] = true
	}
	return len(seen)
}

// ClaimHasFileEvidence 判定 claim 是否引用指定文件。
//
// 对账 claimHasFileEvidence。**注意 kind 门禁**：只有 `file_observation` 与
// `verification_fact` 两种 kind 参与判定——其他 kind 即使证据里有该路径也返回 false。
func ClaimHasFileEvidence(c ContextClaim, path string) bool {
	if c.Kind != ClaimFileObservation && c.Kind != ClaimVerificationFact {
		return false
	}
	for _, e := range c.Evidence {
		if e.Path == path {
			return true
		}
	}
	return false
}

// CountClaimsByStatus 按状态计数。
//
// 对账 countClaimsByStatus。**注意**：`ephemeral` 不在任何计数桶里
// （TS 的 reduce 对未知状态直接返回原 counts）。
func CountClaimsByStatus(claims []ContextClaim) ClaimStatusCounts {
	var counts ClaimStatusCounts
	for _, c := range claims {
		switch c.Status {
		case StatusActive:
			counts.Active++
		case StatusStale:
			counts.Stale++
		case StatusConflicted:
			counts.Conflicted++
		case StatusDurable:
			counts.Durable++
		case StatusDurableCandidate:
			counts.DurableCandidate++
		case StatusQuarantined:
			counts.Quarantined++
		}
	}
	return counts
}
