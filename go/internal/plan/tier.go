package plan

import (
	"regexp"
	"strings"
)

// ModelTier 是模型档位。
//
// 对账 TS `model-tier-policy.ts:5` 的 `ModelTier = 'cheap' | 'balanced' | 'strong'`。
type ModelTier string

const (
	TierCheap    ModelTier = "cheap"
	TierBalanced ModelTier = "balanced"
	TierStrong   ModelTier = "strong"
)

// tierCheapRe / tierStrongRe 是名字推断的两条正则。
//
// 对账 TS `model-tier-policy.ts:125-126`：
//
//	/\b(flash|mini|lite|cheap|small|haiku)\b|m2/
//	/\b(pro|strong|large|opus|max|ultra)\b|gpt-5/
//
// **注意 `|m2` 与 `|gpt-5` 是顶层分支**（不受 `\b` 约束）。
var (
	tierCheapRe  = regexp.MustCompile(`\b(flash|mini|lite|cheap|small|haiku)\b|m2`)
	tierStrongRe = regexp.MustCompile(`\b(pro|strong|large|opus|max|ultra)\b|gpt-5`)
)

// InferModelTierFromName 从模型名推断档位。
//
// 对账 TS `model-tier-policy.ts:123-127`。纯名字推断——无能力卡时的降级路径
// （如计划留痕）。识别不出返回空串（对账 TS 的 `null`）。
//
// **cheap 分支优先**：同时含两类关键词时返回 cheap。
// **永不返回 balanced**（balanced 仅由 inferModelTierFromCard 兜底产生）。
func InferModelTierFromName(model string) ModelTier {
	// 对账 JS 的 `model.toLowerCase()`——Unicode 感知，非仅 ASCII。
	name := strings.ToLower(model)
	if tierCheapRe.MatchString(name) {
		return TierCheap
	}
	if tierStrongRe.MatchString(name) {
		return TierStrong
	}
	return ""
}
