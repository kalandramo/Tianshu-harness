// edit-tool-advisory hook：postTool 检测同一轮内对同一文件的连续 hash_edit。
//
// 对账 TS 的 `createEditToolAdvisoryHook`
// （src/agent/hooks/edit-tool-advisory-hook.ts）。
//
// ## 为什么需要（TS 文件头原文）
//
//	Edit-Tool Advisory Hook — postTool detection of consecutive hash_edit calls
//	on the same file within a turn.
//
//	When the agent uses hash_edit ≥2 times on the same file_path in a single
//	turn, the second call's anchors are stale (the first edit shifted line
//	numbers). This is the #1 cause of bracket-mismatch debris (see 53e1e4a8).
//
// ## 为什么用 turn-scoped Map 而非 recentToolHistory
//
// TS 注释明确说明：
//
//	Uses a turn-scoped Map instead of recentToolHistory (which only keeps 5
//	entries and would miss early hash_edit calls in heavy turns).
//
// Go 的 `recentToolHistory` 窗口也是 5 条（`toolHistoryWindow = 5`）——同样的
// 盲区。故本 hook 自持 turn-scoped 状态。
//
// ## Tier 协调（TS 原文）
//
//	Tier coordination: key='edit-tool-advisory', category='discipline',
//	priority=0.5. Deliberately lower than self-verify (0.58) and
//	discipline-reanchor (0.55) so it yields first under MAX_PER_CATEGORY=2.
//
// **Go 侧差异（一处）**：TS 不设 `tier` 字段（走缺省）；Go 侧同样不设——
// `AdvisoryTier` 零值是空串，与「缺省 bus」语义一致。
package agent

import "context"

// editAdvisoryPriority 对账 TS 的 `priority: 0.5`。
const editAdvisoryPriority = 0.5

// editAdvisoryThreshold 是触发阈值（同轮同文件 hash_edit 次数）。
//
// 对账 TS 的 `count >= 2`。
const editAdvisoryThreshold = 2

// turnEditTracker 是 turn-scoped 状态：文件 → 本轮 hash_edit 次数。
//
// 对账 TS 的 `TurnEditTracker`。
type turnEditTracker struct {
	turn           int
	hashEditByFile map[string]int
}

// NewEditToolAdvisoryHook 构造 edit-tool-advisory hook。
//
// **session-scoped 状态**（对账 TS 的闭包变量）：tracker 跨轮复用，
// 轮次变化时重置。故构造一次。
func NewEditToolAdvisoryHook(bus AdvisorySink) RuntimeHook {
	tracker := turnEditTracker{turn: -1, hashEditByFile: map[string]int{}}

	return RuntimeHook{
		Name:  "edit-tool-advisory",
		Phase: PhasePostTool,
		Run: func(_ context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if hctx == nil || hctx.Snapshot == nil || tool == nil {
				return nil
			}

			// 轮次变化 → 重置（对账 TS 的 `if (ctx.snapshot.turn !== tracker.turn)`）。
			//
			// **为什么必须重置**：计数是「本轮内连续」的语义——跨轮累积会
			// 让「昨天编辑过两次」也触发提醒。
			if hctx.Snapshot.Turn != tracker.turn {
				tracker.turn = hctx.Snapshot.Turn
				tracker.hashEditByFile = map[string]int{}
			}

			if tool.Name != "hash_edit" {
				return nil
			}

			// 取 file_path：优先 Input 的结构化字段，回退 Target（展示兜底）。
			//
			// 对账 TS：
			//   `typeof tool.input?.file_path === 'string' ? tool.input.file_path : tool.target`
			filePath := ""
			if tool.Input != nil {
				if s, ok := tool.Input["file_path"].(string); ok {
					filePath = s
				}
			}
			if filePath == "" {
				filePath = tool.Target
			}
			if filePath == "" {
				return nil
			}

			count := tracker.hashEditByFile[filePath] + 1
			tracker.hashEditByFile[filePath] = count

			if count < editAdvisoryThreshold {
				return nil
			}
			if bus == nil {
				return nil
			}
			bus.Submit(AdvisoryEntry{
				Key:      "edit-tool-advisory",
				Priority: editAdvisoryPriority,
				Category: CategoryDiscipline,
				Content: "你已连续用 hash_edit 编辑同一文件 " + itoaAgent(count) +
					" 次。每次编辑使后续锚点 stale——大括号配对容易错乱。" +
					"考虑用 edit_file（old_string 精确匹配，不依赖行号）或 " +
					"write_file（全量覆写）完成剩余修改。",
				TTL: 1,
			})
			return nil
		},
	}
}

// itoaAgent 是 strconv.Itoa 的本地别名（与 reasoning_spiral 的 itoa 同类）。
func itoaAgent(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
