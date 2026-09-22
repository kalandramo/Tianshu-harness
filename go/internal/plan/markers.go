package plan

import (
	"regexp"
	"strings"
)

// planStatusLineRe 匹配 approve/reject 写入的状态标记行（H1 前）。
//
// 对账 TS `plan-store.ts:47`：
//
//	/^>\s*\*\*Status:\s*(?:APPROVED|REJECTED|EXECUTED)\*\*.*(?:\r?\n)+/gm
//
// **三个必须复刻的细节**：
//  1. 只认**大写**三种状态（大小写敏感）——`parsePlanStatus` 用 `/i`，
//     两侧口径**不对称**。这是 TS 既有行为，照抄。
//  2. `.*(?:\r?\n)+` 吞掉标记行及其后的**所有**换行（含空行）。
//  3. `\r?\n` 而非入口归一化——该 regex 用于 replace 回写文件，
//     归一化会改动用户文件的换行风格。
var planStatusLineRe = regexp.MustCompile(`(?m)^>\s*\*\*Status:\s*(?:APPROVED|REJECTED|EXECUTED)\*\*.*(?:\r?\n)+`)

// planModelLineRe 匹配产出模型留痕行（H1 前，与 Status 同款）。
//
// 对账 TS `plan-store.ts:59`：
//
//	/^>\s*\*\*Model:\s*(.+?)(?:\s*\((cheap|balanced|strong)\))?\*\*.*(?:\r?\n)+/m
var planModelLineRe = regexp.MustCompile(`(?m)^>\s*\*\*Model:\s*(.+?)(?:\s*\((cheap|balanced|strong)\))?\*\*.*(?:\r?\n)+`)

// planStatusParseRe 已由 ParsePlanStatus 的三条优先级正则取代（见该函数）。
// 保留注释说明，避免后人误加回来——**取第一个匹配是错的**（见 ParsePlanStatus）。
// h1Re 匹配第一个 H1 标题行（标记行的插入锚点）。
var h1Re = regexp.MustCompile(`(?m)^#\s+.*$`)

// draftSlugRe 是 plan-mode 活动草稿的 slug 形状。
//
// 对账 TS `plan-store.ts:102` 的 `/^draft-\d+$/`。
var draftSlugRe = regexp.MustCompile(`^draft-\d+$`)

// PlanStatus 是计划的审批状态。
//
// **取值对账 TS `PlanDocument['status']`**（plan-store.ts:14-35 的字段类型）
// ——注意是**小写**，且有一个 `submitted` 兜底态（不是空串）。
type PlanStatus string

const (
	// StatusSubmitted 是「已提交待批」——无任何状态标记时的默认值。
	StatusSubmitted PlanStatus = "submitted"
	StatusApproved  PlanStatus = "approved"
	StatusRejected  PlanStatus = "rejected"
	StatusExecuted  PlanStatus = "executed"
)

// StripPlanStatusMarkers 剥离 approve/reject 留下的状态标记行。
//
// 对账 TS `plan-store.ts:54-56`。重新提交（尤其是省略 plan 字段、从活动计划
// 文件整读的路径）时必须清掉，否则旧的 REJECTED 标记会让新提交被
// ParsePlanStatus 误判为 rejected，从待批准列表里消失。
func StripPlanStatusMarkers(content string) string {
	return planStatusLineRe.ReplaceAllString(content, "")
}

// ParsePlanStatus 解析计划的状态。
//
// 对账 TS `parsePlanStatus`（plan-store.ts:422-427）：
//
//	if (/Status:\s*EXECUTED/i.test(content)) return 'executed'
//	if (/Status:\s*APPROVED/i.test(content)) return 'approved'
//	if (/Status:\s*REJECTED/i.test(content)) return 'rejected'
//	return 'submitted'
//
// **三个必须复刻的细节**（首版实现曾全部搞错，由 `TestApproveRejectPlan`
// 抓出——approve 后再 reject，内容里两个标记并存）：
//
//  1. **优先级固定** EXECUTED > APPROVED > REJECTED——**不是**「取第一个匹配」。
//     `insertPlanStatusMarker` 不幂等（照抄 TS），approve 后 reject 会在文件头
//     叠加两个标记，此时优先级决定结果。
//  2. 返回**小写**状态名。
//  3. 无标记返回 **`submitted`**（不是空串）——调用方据此判定「待批」。
func ParsePlanStatus(content string) PlanStatus {
	switch {
	case executedProbeRe.MatchString(content):
		return StatusExecuted
	case approvedProbeRe.MatchString(content):
		return StatusApproved
	case rejectedProbeRe.MatchString(content):
		return StatusRejected
	}
	return StatusSubmitted
}

var (
	executedProbeRe = regexp.MustCompile(`(?i)Status:\s*EXECUTED`)
	approvedProbeRe = regexp.MustCompile(`(?i)Status:\s*APPROVED`)
	rejectedProbeRe = regexp.MustCompile(`(?i)Status:\s*REJECTED`)
)

// IsDraftSlug 报告 slug 是否为 plan-mode 活动草稿。
//
// 对账 TS `plan-store.ts:101-103`。草稿是规划中的工作文件，不是已提交的计划
// ——ListPlans 过滤它们，防止空草稿以 "Untitled Plan" 的形态冒充待审计划。
func IsDraftSlug(slug string) bool {
	return draftSlugRe.MatchString(slug)
}

// InsertPlanModelMarker 写入/刷新产出模型留痕（幂等）。
//
// 对账 TS `plan-store.ts:77-88`。**幂等**：先用 `planModelLineRe` 剥旧标记再插入。
// 放 H1 前，与 Status 标记同一可视位置——审批人在计划正文里直接看到产出模型。
//
// `tier` 为空串时不写 `(tier)` 部分。
func InsertPlanModelMarker(content, model, tier string) string {
	stripped := planModelLineRe.ReplaceAllString(content, "")
	line := "> **Model: " + model
	if tier != "" {
		line += " (" + tier + ")"
	}
	line += "**\n\n"

	if loc := h1Re.FindStringIndex(stripped); loc != nil {
		return stripped[:loc[0]] + line + stripped[loc[0]:]
	}
	return line + stripped
}

// InsertPlanStatusMarker 在第一个 H1 前插入状态标记（纯函数，无 IO）。
//
// 对账 TS `plan-store.ts:377-389`。用于 plan close 直接对已读入的 markdown
// 打 EXECUTED 标记（含 docs/superpowers/plans 下、无 slug 语义的计划文件）。
//
// **不幂等**（对账 TS）：不剥离旧 Status 行即插入。TS 的
// `insertPlanModelMarker` 幂等而本函数不幂等——这个不对称是既有行为，照抄。
//
// `timestamp` 是 ISO-8601 字符串（由调用方提供，便于测试确定性）。
//
// **状态名转大写写入**——对账 TS 写的是 `> **Status: APPROVED**` 大写形态
// （`stripPlanStatusMarkers` 的正则只认大写）。
func InsertPlanStatusMarker(content string, status PlanStatus, timestamp string) string {
	statusLine := "> **Status: " + strings.ToUpper(string(status)) + "** — " + timestamp + "\n\n"
	if loc := h1Re.FindStringIndex(content); loc != nil {
		return content[:loc[0]] + statusLine + content[loc[0]:]
	}
	return statusLine + content
}

// PlanModelProvenance 是计划的产出模型留痕。
//
// 对账 TS `plan-store.ts:61-65`。
type PlanModelProvenance struct {
	Model string
	Tier  string // "" 表示无 tier
}

// ParsePlanModel 解析计划的产出模型留痕。
//
// 对账 TS `plan-store.ts:67-70`。无标记（旧计划）返回 (nil)。
func ParsePlanModel(content string) *PlanModelProvenance {
	m := planModelLineRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	return &PlanModelProvenance{
		Model: strings.TrimSpace(m[1]),
		Tier:  m[2],
	}
}
