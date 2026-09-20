package compact

import "github.com/kalandramo/tianshu/go/internal/session"

// session split 阈值判定。
//
// 对账 src/agent/compaction-controller.ts 的 `trySessionSplit` 的**判定层**：
//
//	if (contextWindow < 500_000) return false
//	if (ratio < 0.86) return false
//	→ split
//
// # 为什么需要它
//
// 1M 窗口下，纯缓存经济会把压缩一路推迟到精度悬崖之后。session split 是
// **主动**的护栏：86% 时把历史替换为结构化 handoff（task-state + 轨迹 +
// 近期推理），让会话以干净状态继续，而不是等上下文爆掉。
//
// **时机关键**：它在 `addUserMessage` **之前**运行（TS 的
// `preUserMessageSplit`）——若在之后，刚发送的 user 指令会被连同历史一起
// 摘要替换，用户观感是「消息被截断」。
//
// # 范围（有意收窄）
//
// 本函数只做**判定**。执行层（`replaceWithCheckpoint` /
// `buildStructuredHandoff` / `extractTaskState` / artifact 归档 /
// `promptEngine.resetAppendixBaseline`）依赖 Go 侧尚未移植的模块
// （task-state / trajectory / artifact store）——需先移植那三个子系统。
// 判定层是纯阈值逻辑、确定性、可逐用例对账，故先落地。

// SessionSplitThresholds 是 split 的两个门槛（对账 TS 的字面量）。
const (
	// SessionSplitMinWindow 是启用 split 的最小上下文窗口。
	//
	// **为什么是 500K**（TS 语义）：中小窗口的缓存经济比值已压得够早，
	// 精度退化不是瓶颈；split 是为**大窗口**设的——那里纯缓存经济会把压缩
	// 推迟到精度悬崖之后。
	SessionSplitMinWindow = 500_000
	// SessionSplitMinRatio 是触发 split 的上下文占用比例。
	SessionSplitMinRatio = 0.86
)

// SessionSplitDecision 是一次 split 判定及其输入。
//
// 可观测性契约：离线日志能看出「为何没 split」（窗口不够 / 比例不够）。
type SessionSplitDecision struct {
	// ShouldSplit 是判定结果。
	ShouldSplit bool
	// Reason 是判定的分支来源。
	Reason SessionSplitReason
	// EstimatedTokens 是判定时的估算 token 数。
	EstimatedTokens int
	// ContextWindow 是判定时的窗口。
	ContextWindow int
	// Ratio 是 estimatedTokens / contextWindow。
	Ratio float64
}

// SessionSplitReason 是 split 判定的分支来源。
type SessionSplitReason string

const (
	// SplitWindowTooSmall 是窗口低于门槛（中小窗口不启用）。
	SplitWindowTooSmall SessionSplitReason = "window-too-small"
	// SplitBelowRatio 是占用比例低于门槛。
	SplitBelowRatio SessionSplitReason = "below-ratio"
	// SplitTriggered 是两门槛都满足。
	SplitTriggered SessionSplitReason = "session-split-triggered"
)

// ShouldSessionSplit 判定是否应主动切分会话。
//
// 对账 `trySessionSplit` 的判定层。**两条门槛的顺序敏感**：
//
//  1. `contextWindow < 500_000` → 不 split（即使比例极高——中小窗口靠常规压缩）
//  2. `ratio < 0.86` → 不 split
//  3. 否则 split
//
// **注意 ratio 在窗口门槛之后才计算**——TS 里 `ratio` 的求值在窗口检查之后，
// 故小窗口下即便 ratio 远超 0.86 也不会 split（oracle 的
// `small_window_never_splits` 用例：ratio 1.562 但窗口 128K → false）。
func ShouldSessionSplit(messages []session.OaiMessage, contextWindow int) SessionSplitDecision {
	dec := SessionSplitDecision{ContextWindow: contextWindow}

	if contextWindow < SessionSplitMinWindow {
		dec.Reason = SplitWindowTooSmall
		return dec
	}

	estimated := EstimateOaiTokens(messages)
	dec.EstimatedTokens = estimated

	ratio := 0.0
	if contextWindow > 0 {
		ratio = float64(estimated) / float64(contextWindow)
	}
	dec.Ratio = ratio

	if ratio < SessionSplitMinRatio {
		dec.Reason = SplitBelowRatio
		return dec
	}

	dec.ShouldSplit = true
	dec.Reason = SplitTriggered
	return dec
}

// BuildSessionHandoff 构造最小结构化 handoff 文本。
//
// 对账 TS `buildStructuredHandoff` 的**最小子集**。
//
// **降级说明（重要）**：TS 的完整 handoff 含 8 个章节（task-state 的
// current/completed/remaining、工具轨迹、失败记录、近期推理、文件清单等），
// 依赖 `extractTaskState`（`src/agent/task-state.ts`，需 trajectory）与
// `getTrajectoryEntries`。Go 侧尚无这两个模块，故此处只产出**能从消息列表
// 直接提取**的部分：近期推理摘要 + 文件清单。
//
// 这是**有意的降级**（记于 HANDOFF），不是「等价的简化版」——移植
// task-state/trajectory 后应**替换**本函数而非叠加。
func BuildSessionHandoff(messages []session.OaiMessage, ratio float64) string {
	var b []byte
	b = append(b, "<session-handoff>"...)
	b = append(b, "Session split at "...)
	b = append(b, formatPercent(ratio)...)
	b = append(b, " context.\n"...)

	// 近期推理：从末尾往前取 assistant 文本，累计不超过 MAX_REASONING_CHARS。
	const maxReasoningChars = 2000
	var reasoning []string
	total := 0
	for i := len(messages) - 1; i >= 0 && total < maxReasoningChars; i-- {
		m := messages[i]
		if m.Role != "assistant" || m.Content == nil || *m.Content == "" {
			continue
		}
		s := *m.Content
		if total+len(s) > maxReasoningChars {
			s = s[:maxReasoningChars-total]
		}
		reasoning = append([]string{s}, reasoning...)
		total += len(s)
	}
	b = append(b, "\n## 近期推理\n"...)
	if len(reasoning) > 0 {
		for _, r := range reasoning {
			b = append(b, r...)
			b = append(b, '\n')
		}
	} else {
		b = append(b, "（无记录）\n"...)
	}

	// 文件清单：从 tool 消息里提取路径形态的 token。
	files := extractFilePaths(messages)
	b = append(b, "\n## 涉及文件\n"...)
	if len(files) > 0 {
		for _, f := range files {
			b = append(b, "- "...)
			b = append(b, f...)
			b = append(b, '\n')
		}
	} else {
		b = append(b, "（无记录）\n"...)
	}

	b = append(b, "</session-handoff>"...)
	return string(b)
}
