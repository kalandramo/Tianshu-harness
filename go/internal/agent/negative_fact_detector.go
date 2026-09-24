// negative_fact_detector.go —— 有损观测中的负向断言检测（纠正性守卫）。
//
// 对账 TS `src/agent/negative-fact-detector.ts`（77 行，2 个导出）。
//
// ## 原则（TS 原文）
//
//	有损观测不能证明不存在。
//	A lossy observation cannot support a negative conclusion.
//
// ## 与 lossy-observation hook 的分工（TS 注释明确的一对）
//
//   - `guardLossyToolResult`（本文件，由 loop 在回灌历史前调用）——**纠正性**：
//     当有损输出**同时**含负向断言时，在模型读到之前**内联注入**
//     `[⚠ VERIFICATION_REQUIRED]` 标记。
//   - `lossy-observation` hook（`lossy_observation.go`）——**预防性**：
//     对**任何**有损输出发 advisory，提醒通则纪律。
//
// 两者共用 `lossy-markers.go` 的标记表（TS 的 W1-A4 单一真源）。
package agent

import (
	"regexp"
	"strings"
)

// negativeFactPatterns 匹配工具输出里的**负向断言**。
//
// 对账 TS 的 `NEGATIVE_FACT_PATTERNS`（13 条，逐条同序）。
//
// **为什么用 `\b` 词边界**：TS 原文如此，防止 `emptyish` / `unchangedness`
// 这类词内子串误命中。Go RE2 支持 `\b`，直接照搬（无需手工边界检查——
// 这些模式**不含 lookahead/lookbehind**，是本项目已知 RE2 不兼容面之外的）。
var negativeFactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bempty\b`),
	regexp.MustCompile(`\bnot found\b`),
	regexp.MustCompile(`\bno matches\b`),
	regexp.MustCompile(`\b0 results\b`),
	regexp.MustCompile(`\b0 files\b`),
	regexp.MustCompile(`\bnothing to commit\b`),
	regexp.MustCompile(`\bno tests found\b`),
	regexp.MustCompile(`\ball passed\b`),
	regexp.MustCompile(`\bno errors\b`),
	regexp.MustCompile(`\bunchanged\b`),
	regexp.MustCompile(`\bnot modified\b`),
	regexp.MustCompile(`\bnot detected\b`),
	regexp.MustCompile(`\bno changes\b`),
}

// NegativeFactDetection 是检测结果。
//
// 对账 TS `NegativeFactDetection`（`negative-fact-detector.ts:30-35`）。
type NegativeFactDetection struct {
	// Matched 是命中的负向断言原文（TS 的 `match[0]`）。
	Matched string
	// Reason 是怀疑理由（进入注入文案）。
	Reason string
}

// DetectNegativeFactInLossyResult 判定内容是否**同时**带结构化有损标记与
// 负向断言；两者都满足才返回检测结果，否则 nil。
//
// 对账 TS 的 `detectNegativeFactInLossyResult`。
//
// **顺序有意义**：先判有损（廉价且是前提），再逐条试负向模式。无损输出
// 即使含 "no errors"（正常的测试通过输出）也**不检出**——这是本机制的核心
// 反假阳性设计（TS 测试「returns null for "no errors" in lossless content」
// 锁住此语义）。
func DetectNegativeFactInLossyResult(content string) *NegativeFactDetection {
	// Step 1: 是否为有损观测？
	if !IsLossyObservation(content) {
		return nil
	}

	// Step 2: 是否含负向断言？
	for _, pattern := range negativeFactPatterns {
		match := pattern.FindString(content)
		if match == "" {
			continue
		}
		return &NegativeFactDetection{
			Matched: match,
			Reason: "lossy observation (collapsed/truncated) contains suspected " +
				"negative fact: \"" + match + "\" — must verify independently",
		}
	}

	return nil
}

// GuardLossyToolResult 在有损观测里检出负向断言时，于内容**前置**一段
// `[⚠ VERIFICATION_REQUIRED]` 警告；无检出则原样返回。
//
// 对账 TS 的 `guardLossyToolResult`。
//
// **幂等**：已含标记的内容不再重复注入（TS 测试
// 「does not duplicate VERIFICATION_REQUIRED if already present」锁住）。
// 首版 TS 无此显式判定——它靠 `detectNegativeFactInLossyResult` 对已注入内容
// 仍会检出，故**必须显式短路**。Go 侧同样处理，语义与 TS 一致。
func GuardLossyToolResult(content string) string {
	// 已注入过 → 不重复（幂等）。
	if strings.Contains(content, verificationRequiredMarker) {
		return content
	}

	detection := DetectNegativeFactInLossyResult(content)
	if detection == nil {
		return content
	}

	warning := strings.Join([]string{
		verificationRequiredMarker,
		detection.Reason,
		"Recommended: use find / glob / os.scandir / git status for independent cross-verification.",
		"Do NOT conclude absence/emptiness from this observation alone.",
		"---",
	}, "\n")

	return warning + "\n" + content
}

// verificationRequiredMarker 是注入标记（对账 TS 的字面量）。
//
// 单独提取成常量：既是注入文本，也是**幂等判据**（两处必须同源，
// 否则改一处会静默破坏去重）。
const verificationRequiredMarker = "[⚠ VERIFICATION_REQUIRED]"
