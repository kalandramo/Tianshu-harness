package agent

// checkpoint.go —— replaceWithCheckpoint 的接线层。
//
// 对账 TS `CompactionController.replaceWithCheckpoint`
// （`compaction-controller.ts:1082`）。
//
// # 作用
//
// 把会话历史**替换**为「缓存锚 + 摘要 + （可选的）尾随原文」——这是
// session split / 紧急上限 / LLM 阶梯三条路径共用的**唯一历史重写入口**。
//
// 本刀之前 Go 侧没有这个入口：`TrySessionSplit` 只能「判定 + 构造候选
// handoff」，无法真正替换历史（那是自 `1b3a552` 起一直 blocked 的验收面）。
//
// # 依赖分层（全部可缺省）
//
// 核心路径（anchor 保留 / 尾随 user 保护 / reclaim gate / 替换）**完整实现**；
// 三个增强作为**可注入依赖**，nil 时跳过（与 Go 侧既有的 Advisor / EfficacyStore
// 同模式）：
//
//   - `ArchiveDiscarded`：把丢弃段归档为 compact-history artifact 并回填召回引用
//     （需 `serializeMessagesForArchive` + catalog，未移植）
//   - `TaskAnchor`：追加权威任务锚（需 `getActiveContract` + `renderTaskAnchor`，未移植）
//   - `Preflight`：替换前修复孤儿 tool_call（需 `runResumePreflightOai`，未移植）
//
// **未接不等于没做**：三项都在本文件的 CheckpointDeps 里显式声明，缺省时
// 行为与 TS 的对应分支（`archive ? ... : ...`）一致。

import (
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// CheckpointDeps 是 checkpoint 替换的可注入依赖。
//
// 全部可缺省——nil 时对应增强跳过（见文件头说明）。
type CheckpointDeps struct {
	// ArchiveDiscarded 把被丢弃的历史段归档，返回追加到摘要末尾的召回引用块。
	//
	// 对账 TS `archiveDiscardedHistory`：返回 `{id, ref}`，ref 会被拼到
	// summary 与 fallback 两处。返回空串表示不归档（对账 TS 的 `archive ? ...`）。
	ArchiveDiscarded func(discarded []session.OaiMessage, reason string) string

	// TaskAnchor 返回要追加到候选末尾的权威任务锚（空串 = 不追加）。
	//
	// 对账 TS `buildTaskAnchorAppendix`。**必须置于 appendix 区**
	// （候选末尾）——绝不进冻结前缀，否则前缀缓存失效。
	TaskAnchor func() string

	// Preflight 在替换前修复历史（如孤儿 tool_call）。返回修复后的消息。
	//
	// 对账 TS `runResumePreflightOai`。nil 时原样返回（对账 TS 的
	// `preflight.repaired` 为 false 的分支）。
	Preflight func(messages []session.OaiMessage) []session.OaiMessage

	// OnReclaimDecision 接收 reclaim gate 的决策（提交与拒绝都记）。
	//
	// 对账 TS 的 `deps.onReclaimDecision?.(decision)`。
	OnReclaimDecision func(compact.ReclaimDecisionRecord)
}

// CheckpointParams 是一次 checkpoint 替换的输入。
//
// 对账 TS `replaceWithCheckpoint` 的参数对象。
type CheckpointParams struct {
	// Tier 是压缩层级（记录用）。
	Tier int
	// Reason 是替换原因（记录用，如 "session split at 95% context"）。
	Reason string
	// Summary 是主摘要文本。
	Summary string
	// MaxFallback 是候选超此 token 数时回退到 FallbackText。
	MaxFallback int
	// FallbackText 是更短的兜底摘要。
	FallbackText string
	// Action 是压缩动作（reclaim gate 的输入）。
	Action compact.CompactionAction
	// Force 为 true 时 reclaim gate 必定提交（对账 TS 的 force 语义：
	// ceiling / session split 的替代方案是超窗 API 失败）。
	Force bool
}

// CheckpointOutcome 是一次替换的结果。
type CheckpointOutcome struct {
	// Committed 报告替换是否真的执行了。
	Committed bool
	// ReclaimedTokens 是回收的 token 数（来自 reclaim 决策）。
	ReclaimedTokens int
	// Reason 是未提交时的原因（提交时为空）。
	Reason compact.ReclaimSkipReason
	// Messages 是替换后的历史（未提交时为原历史）。
	Messages []session.OaiMessage
}

// ReplaceWithCheckpoint 用「锚 + 摘要 + 尾随原文」替换历史。
//
// 对账 TS `replaceWithCheckpoint`。**返回是否真的提交**。
//
// # 流程（对账 TS 逐段）
//
//  1. 取锚：前 `CacheAnchorMessages` 条（前缀缓存的前提，**逐字节不动**）
//  2. **尾随未消费 user 保护**：末尾若是 user（模型尚未消费），其原文保留在
//     末尾、不进归档——否则用户刚发的指令会被摘要替换（用户观感 = 消息被截断）
//  3. 归档丢弃段（可缺省）→ 把召回引用拼到摘要与兜底两处
//  4. 组候选：`[锚..., 摘要, 尾随原文?]`；超 MaxFallback 时换兜底摘要
//  5. 追加任务锚（可缺省，置于末尾 appendix 区）
//  6. **reclaim gate**：不提交则返回 false（不碰历史）
//  7. 替换历史 + 持久化
//
// # 摘要角色（对账 TS 的 summaryRole）
//
// 有尾随原文时摘要用 **assistant** 角色——避免中间位置出现第二条 user
// 消息（promptEngine 对非 trailer 的历史 user 会做 volatileBlock 回退注入，
// 导致双份注入/膨胀）。无尾随原文时用 user（旧形态，摘要收尾）。
func (b *CompactBoundary) ReplaceWithCheckpoint(
	messages []session.OaiMessage,
	params CheckpointParams,
	deps CheckpointDeps,
) CheckpointOutcome {
	// ── 1. 锚 ──
	anchorCount := compact.CacheAnchorMessages
	if anchorCount > len(messages) {
		anchorCount = len(messages)
	}
	anchor := messages[:anchorCount]

	// ── 2. 尾随未消费 user 保护 ──
	//
	// 判据「末尾即 user」复用 history-invariant 探针口径：末尾 user 必未被
	// 模型消费（消费后会接 assistant 回复）；turn 内工具循环中末尾是
	// assistant/tool，不命中，压缩范围与旧行为一致。
	//
	// `>= anchorCount` 含 index == anchorCount 的最小边界（对账 TS）。
	tailIsFreshUser := false
	var tailPreserved []session.OaiMessage
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Role == "user" && len(messages)-1 >= anchorCount {
			tailIsFreshUser = true
			tailPreserved = []session.OaiMessage{last}
		}
	}
	archiveEnd := len(messages)
	if tailIsFreshUser {
		archiveEnd = len(messages) - 1
	}

	// ── 3. 归档丢弃段（可缺省）──
	discarded := messages[anchorCount:archiveEnd]
	summaryText := params.Summary
	fallbackText := params.FallbackText
	if deps.ArchiveDiscarded != nil {
		if ref := deps.ArchiveDiscarded(discarded, params.Reason); ref != "" {
			summaryText += ref
			fallbackText += ref
		}
	}

	// ── 4. 组候选 ──
	summaryRole := "user"
	if len(tailPreserved) > 0 {
		summaryRole = "assistant"
	}
	candidate := make([]session.OaiMessage, 0, len(anchor)+1+len(tailPreserved))
	candidate = append(candidate, anchor...)
	candidate = append(candidate, textMessage(summaryRole, summaryText))
	candidate = append(candidate, tailPreserved...)

	if params.MaxFallback > 0 && compact.EstimateOaiTokens(candidate) > params.MaxFallback {
		candidate = make([]session.OaiMessage, 0, len(anchor)+1+len(tailPreserved))
		candidate = append(candidate, anchor...)
		candidate = append(candidate, textMessage(summaryRole, fallbackText))
		candidate = append(candidate, tailPreserved...)
	}

	// ── 5. 任务锚（可缺省，末尾 appendix 区）──
	if deps.TaskAnchor != nil {
		if anchorText := deps.TaskAnchor(); anchorText != "" {
			candidate = append(candidate, textMessage("user", anchorText))
		}
	}

	// ── 6. reclaim gate ──
	est := compact.EstimateReclaim(messages, candidate)
	decision := compact.BuildReclaimDecision(params.Action, est, b.reclaimProfile(), params.Force)
	if deps.OnReclaimDecision != nil {
		deps.OnReclaimDecision(decision)
	}
	b.LastReclaimDecision = &decision
	if !decision.Commit {
		return CheckpointOutcome{
			Committed:       false,
			ReclaimedTokens: decision.ReclaimedTokens,
			Reason:          decision.Reason,
			Messages:        messages,
		}
	}

	// ── 7. 替换（preflight 可缺省）──
	final := candidate
	if deps.Preflight != nil {
		final = deps.Preflight(candidate)
	}
	return CheckpointOutcome{
		Committed:       true,
		ReclaimedTokens: decision.ReclaimedTokens,
		Messages:        final,
	}
}

// textMessage 构造一条纯文本 OaiMessage。
func textMessage(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}
