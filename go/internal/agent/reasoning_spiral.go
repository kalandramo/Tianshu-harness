// reasoning-spiral hook：检测「单轮推理过长且无工具调用」。
//
// 对账 TS 的 `createReasoningSpiralHook`（src/agent/hooks/reasoning-spiral-hook.ts）。
//
// ## 为什么需要它
//
// TS 源码的原始说明（逐条对账）：
//
//	prompt 约束（GLM calibration block）：
//	  每轮推理只产出两件事……不要在推理里写完整代码
//
//	核心缺口：convergence-detector / exploration-stall / thinking-retry 都不度量
//	单轮推理长度。模型可以在一个 turn 输出 8000+ 字符推理，不调任何工具，
//	也不触发任何现有检测器——直到超时。
//
// ## 信号
//
//	lastThinkingLength > SPIRAL_THRESHOLD && lastTurnHadTools === false
//
// ## 与 TS 的差异（明示）
//
// TS 的「简化决策」注释列了两条不做的分档（GLM 分档 / 分析任务豁免）——
// 那些字段在 TS 侧也不存在，Go 同样不做。
//
// **Go 侧的额外差异**：TS 的 `deps.obligations`（义务追踪器）在 Go 侧**未移植**
// （`internal/context` 只有 rounds/pressure）。故本 hook 只走**无义务**分支
// ——即通用文案，不做「点名具体义务」的升级。接口留了口（deps.Obligations
// 为 nil 时走通用分支），待义务追踪器移植后接上。
package agent

import (
	"context"
	"fmt"
	"strconv"
)

// spiralThreshold 是触发阈值（单轮推理**字符数**）。
//
// 对账 TS 的 `SPIRAL_THRESHOLD = 3000`。
const spiralThreshold = 3000

// spiralCooldownTurns 是触发后的冷却轮数。
//
// 对账 TS 的 `COOLDOWN_TURNS = 2`。
const spiralCooldownTurns = 2

// ReasoningSpiralDeps 是 hook 的依赖。
type ReasoningSpiralDeps struct {
	// Bus 接收 advisory 提交（nil = 不提交，hook 退化为 no-op）。
	Bus AdvisorySink
	// Obligations 是义务追踪器（nil = 走通用分支）。
	//
	// **Go 侧未移植**（`internal/context` 只有 rounds/pressure）——保留字段
	// 以便将来接上，当前恒 nil。
	Obligations ObligationSource
}

// ObligationSource 是义务追踪器的**最小接口**（供 hook 用）。
//
// 对账 TS 的 `Pick<ObligationTracker, 'unresolvedHigh' | 'recordAttempt'>`。
type ObligationSource interface {
	// UnresolvedHigh 返回未决的高风险义务（按优先级）。
	UnresolvedHigh() []Obligation
	// RecordAttempt 登记一次「无新证据的尝试」（升级压力）。
	RecordAttempt(id string)
}

// Obligation 是一条未决义务（最小形态）。
//
// 对账 TS 的义务对象（只用其中四个字段）。
type Obligation struct {
	ID             string
	Claim          string
	RequiredAction string
	Targets        []string
}

// NewReasoningSpiralHook 构造 reasoning-spiral hook。
//
// **session-scoped 状态**（对账 TS 的闭包变量）：`recentLengths` 记录最近 3 轮
// 推理长度（升级检测），`lastAdvisoryTurn` 记冷却。故构造一次、跨轮复用
// ——每次 Run 新建会丢失趋势与冷却状态。
func NewReasoningSpiralHook(deps ReasoningSpiralDeps) RuntimeHook {
	recentLengths := []int{}
	lastAdvisoryTurn := -1

	return RuntimeHook{
		Name:  "reasoning-spiral",
		Phase: PhasePreTurn,
		Run: func(_ context.Context, hctx *RuntimeHookContext, _ *RuntimeToolEvent) error {
			if hctx == nil || hctx.Snapshot == nil {
				return nil
			}
			s := hctx.Snapshot

			// 尚无上一轮数据 → 跳过（对账 TS 的 undefined 检查）。
			if s.LastThinkingLength < 0 {
				return nil
			}

			// 未触发：短推理 或 有工具调用。
			if s.LastThinkingLength < spiralThreshold || s.LastTurnHadTools {
				recentLengths = recentLengths[:0]
				return nil
			}

			// Cooldown（对账 TS 的 `turn - lastAdvisoryTurn < COOLDOWN_TURNS`）。
			if s.Turn-lastAdvisoryTurn < spiralCooldownTurns {
				return nil
			}

			// 趋势跟踪（最近 3 轮）。
			recentLengths = append(recentLengths, s.LastThinkingLength)
			if len(recentLengths) > 3 {
				recentLengths = recentLengths[1:]
			}

			isEscalating := isStrictlyIncreasing(recentLengths)
			lastAdvisoryTurn = s.Turn

			if deps.Bus == nil {
				return nil
			}

			// 义务升级分支（Go 侧 Obligations 恒 nil → 走通用分支）。
			if deps.Obligations != nil {
				if unresolved := deps.Obligations.UnresolvedHigh(); len(unresolved) > 0 {
					first := unresolved[0]
					deps.Obligations.RecordAttempt(first.ID)
					targets := "见义务描述"
					if len(first.Targets) > 0 {
						targets = joinStrings(first.Targets, ", ")
					}
					deps.Bus.Submit(AdvisoryEntry{
						Key:      "reasoning-spiral",
						Priority: 0.54,
						Category: CategoryDiscipline,
						TTL:      1,
						Content: "上一轮输出了 " + formatLen(s.LastThinkingLength) +
							" 推理但未调用任何工具，而高风险义务「" + first.Claim +
							"」仍未关闭。继续推理不能关闭它——下一步：" + first.RequiredAction +
							"（目标：" + targets + "）。",
					})
					return nil
				}
			}

			content := "上一轮输出了 " + formatLen(s.LastThinkingLength) +
				" 推理但未调用任何工具。若在分析瘫痪中，选一个最可能的方向用工具验证" +
				"——工具结果比继续推理更能帮你收敛。若任务本身就是分析，输出结论而非继续扩展。"
			if isEscalating {
				content = "连续 " + strconv.Itoa(len(recentLengths)) + " 轮长推理未行动（" +
					joinIntsFormatted(recentLengths) + "）。推理链在自我放大。停：" +
					"用一个工具对当前最可能的假设打探针。工具结果比继续推理更能帮你收敛。"
			}
			deps.Bus.Submit(AdvisoryEntry{
				Key:      "reasoning-spiral",
				Priority: 0.54,
				Category: CategoryDiscipline,
				TTL:      1,
				Content:  content,
			})
			return nil
		},
	}
}

// isStrictlyIncreasing 报告序列是否**严格递增**。
//
// 对账 TS：
//
//	recentLengths.length >= 2 && recentLengths.every((v, i) => i === 0 || v > recentLengths[i-1])
//
// 即「至少 2 项」且「每项都大于前一项」。
func isStrictlyIncreasing(v []int) bool {
	if len(v) < 2 {
		return false
	}
	for i := 1; i < len(v); i++ {
		if v[i] <= v[i-1] {
			return false
		}
	}
	return true
}

// formatLen 对账 TS 的 `formatLen`。
//
//	>= 1000 → "N.NK"（一位小数）；否则原样数字。
func formatLen(chars int) string {
	if chars >= 1000 {
		return fmt.Sprintf("%.1fK", float64(chars)/1000)
	}
	return strconv.Itoa(chars)
}

// joinIntsFormatted 把长度序列渲染为 "3.2K → 4.1K"。
func joinIntsFormatted(v []int) string {
	out := ""
	for i, n := range v {
		if i > 0 {
			out += " → "
		}
		out += formatLen(n)
	}
	return out
}

// joinStrings 是 strings.Join 的本地别名（避免为一个调用引入 strings 依赖）。
func joinStrings(v []string, sep string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
