// lossy-observation hook：postTool 检测被折叠/截断的工具结果。
//
// 对账 TS 的 `createLossyObservationHook`
// （src/agent/hooks/lossy-observation-hook.ts）。
//
// ## 为什么需要（TS 文件头原文）
//
//	Lossy Observation Hook — postTool detection of collapsed/truncated tool
//	results, reinforcing the rule that lossy observations cannot support
//	negative conclusions.
//
// ## 与 negative-fact-detector 的分工（TS 原文）
//
//	Coordination with negative-fact-detector:
//	  - guardLossyToolResult (tool-execution.ts) injects inline
//	    [⚠ VERIFICATION_REQUIRED] markers when lossy output ALSO contains
//	    negative claims — this is corrective.
//	  - This hook fires on ANY lossy output — reminding the model about the
//	    general discipline before it can form a negative conclusion.
//	    This is preventive.
//
// **Go 侧现状**：`negative-fact-detector`（corrective 那半）**未移植**。
// 故本 hook 是 Go 侧唯一的 lossy 防线（预防性）。
//
// ## 优先级（TS 原文）
//
//	Tier: key='lossy-observation', category='discipline', priority=0.48.
//	Deliberately lower than edit-tool-advisory (0.5) and discipline-reanchor
//	(0.55) — lossy markers are routine and shouldn't crowd out higher-signal
//	advisories.
//
// ## 冷却
//
//	Cooldown: at most 1 advisory per turn, even if multiple lossy tools fire.
package agent

import "context"

// lossyAdvisoryContent 是投递文案（**逐字对账** TS 的 `ADVISORY_CONTENT`）。
const lossyAdvisoryContent = "【天枢】有损观测：上一个工具输出被折叠/截断。" +
	"禁止从中推出负向结论（\"不存在\"\"为空\"\"0 results\"等）——" +
	"用 find / glob / git status 独立交叉验证后再下断言。"

// NewLossyObservationHook 构造 lossy-observation hook。
//
// **session-scoped 状态**（对账 TS 的闭包变量）：`lastFiredTurn` 记上次触发的
// 轮次（每轮至多 1 条）。故构造一次、跨轮复用。
//
// **与 TS 的差异（一处）**：TS 用 `ctx.snapshot.turn === lastFiredTurn` 判冷却，
// Go 侧同样用快照的 `Turn`。但 Go 的快照 `Turn` 是 **run 局部序号**（
// `buildRuntimeSnapshot(turn)` 传的是循环变量），而 TS 的 snapshot.turn 语义
// 相同——故行为等价。
func NewLossyObservationHook(bus AdvisorySink) RuntimeHook {
	lastFiredTurn := -1

	return RuntimeHook{
		Name:  "lossy-observation",
		Phase: PhasePostTool,
		Run: func(_ context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if hctx == nil || hctx.Snapshot == nil || tool == nil {
				return nil
			}

			// 每轮至多 1 条（对账 TS 的 `if (ctx.snapshot.turn === lastFiredTurn) return`）。
			if hctx.Snapshot.Turn == lastFiredTurn {
				return nil
			}

			content := tool.ResultContent
			if content == "" {
				return nil
			}
			if !IsLossyObservation(content) {
				return nil
			}

			lastFiredTurn = hctx.Snapshot.Turn
			if bus == nil {
				return nil
			}
			// W3-C2 的 expect 审计（TS 注释逐条对账）：建议的交叉验证工具
			// （glob/grep/bash）在正常流程中几乎每轮都会出现——「工具出现」
			// 无法区分「因提醒而验证」与「本来就要用」。这是计划明确禁止的
			// 过宽伪 expect，**刻意不填**。
			bus.Submit(AdvisoryEntry{
				Key:      "lossy-observation",
				Priority: 0.48,
				Category: CategoryDiscipline,
				Content:  lossyAdvisoryContent,
				TTL:      1,
			})
			return nil
		},
	}
}
