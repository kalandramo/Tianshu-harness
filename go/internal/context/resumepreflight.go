package context

// resumepreflight.go —— 会话恢复前的消息邻接修复。
//
// 对账 TS `src/context/resume-preflight.ts` 的 `runResumePreflightOai`。
//
// # 为什么需要它
//
// 供应商对 tool_call / tool_result 的**邻接**有硬要求：每条带 `tool_calls`
// 的 assistant 消息后面**必须紧跟**对应的 tool 消息（同序、无外来 id、无重复），
// 且不允许有游离的 tool 消息。不满足就报
// "insufficient tool messages following tool_calls" 并拒绝生成。
//
// **id 存在性检查（`RepairOrphanToolCalls`）是必要但不充分的**：一个 tool 结果
// 可能**存在**却位于中间的 user/assistant 消息**之后**（如 tool 批次中途中止、
// 迟到的 addToolResults 落到了下一轮之后）——它有匹配 id，但邻接仍然破坏。
//
// # 本函数与 `RepairOrphanToolCalls` 的区别（关键）
//
// | | 策略 |
// |---|---|
// | `session.RepairOrphanToolCalls` | **剔除**孤儿（丢弃 tool_call / tool_result） |
// | 本函数 | **拉回 + 合成**：从历史任意位置拉回匹配结果；仅当根本不存在时才合成占位 |
//
// 后者更接近供应商的真实要求（保留信息而非删除）。
//
// # 幂等（no-op 契约）
//
// 邻接已经干净时返回**原切片**（同一引用）——前缀缓存不被触碰。

import (
	"github.com/kalandramo/tianshu/go/internal/session"
)

// ResumePreflightReport 是一次 preflight 的结果。
//
// 对账 TS `OaiResumePreflightReport`。
type ResumePreflightReport struct {
	// MessageCount 是原始消息数。
	MessageCount int
	// RoundCount 是修复后的轮数。
	RoundCount int
	// Repaired 报告是否真的改动了（规范化或修复）。
	Repaired bool
	// SyntheticResultsInserted 是合成插入的结果数。
	SyntheticResultsInserted int
	// Messages 是修复后的消息（未修复时为原切片）。
	Messages []session.OaiMessage
}

// RunResumePreflightOai 修复消息序列的 tool 邻接。
//
// 对账 TS `runResumePreflightOai`。写入探测可缺省（nil = 不做磁盘证据）。
//
// # 流程
//
//  1. **规范化**（`NormalizeOaiMessages`）：清除 `tool_calls: []` 这类会被
//     供应商拒绝的空数组。**必须在邻接快路径之前**——否则含空数组的恢复会话
//     会被原样送回，供应商在生成开始前就拒绝。
//  2. 邻接已干净 → 返回（`Repaired` = 规范化是否改动）
//  3. **按 id 建 FIFO 队列**索引所有 tool 消息——同 id 结果（罕见，污染）
//     按前到后确定性消费
//  4. 走一遍：tool 消息**跳过**（在下面按位重新发出，多余的丢弃）；
//     assistant 的每个 tool_call 从队列取匹配结果，**取不到则合成占位**
//  5. 返回修复后的序列
func RunResumePreflightOai(messages []session.OaiMessage, writeProbe WriteProbe) ResumePreflightReport {
	normalized := session.NormalizeOaiMessages(messages)
	// `NormalizeOaiMessages` 无改动时返回**原切片**——用长度+首地址比较不可靠，
	// 故用「是否产生了新切片」的间接判据：逐条比长度不充分，这里信任其对
	// 「无改动返回原切片」的契约（Go 无法比较切片身份，改用元素级比较）。
	normalizationChanged := !sameMessages(normalized, messages)

	if isToolAdjacencyCleanOai(normalized) {
		return ResumePreflightReport{
			MessageCount:             len(normalized),
			RoundCount:               CountRoundsOai(normalized),
			Repaired:                 normalizationChanged,
			SyntheticResultsInserted: 0,
			Messages:                 normalized,
		}
	}

	// 按 id 建 FIFO 队列。
	toolMsgsByID := map[string][]session.OaiMessage{}
	for _, m := range normalized {
		if m.Role == "tool" {
			toolMsgsByID[m.ToolCallID] = append(toolMsgsByID[m.ToolCallID], m)
		}
	}

	repaired := make([]session.OaiMessage, 0, len(normalized))
	inserted := 0
	// 历史里已有的合成恢复数——驱动重复升级。
	priorRecoveries := CountPriorRecoveries(messages)

	for _, m := range normalized {
		if m.Role == "tool" {
			continue // 在下面按位重新发出；多余的（孤儿）丢弃
		}
		repaired = append(repaired, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}

		for _, tc := range m.ToolCalls {
			q := toolMsgsByID[tc.ID]
			if len(q) > 0 {
				// 拉回：取队列头（确定性消费）。
				existing := q[0]
				toolMsgsByID[tc.ID] = q[1:]
				repaired = append(repaired, toolResultMessage(tc.ID, contentOf(existing)))
			} else {
				// 合成占位。
				toolName := ""
				args := ""
				if tc.Function != nil {
					toolName = tc.Function.Name
					args = tc.Function.Arguments
				}
				filePath := ExtractTargetPath(args)
				var evidence *WriteEvidence
				if writeProbe != nil {
					evidence = writeProbe(toolName, args)
				}
				repaired = append(repaired, toolResultMessage(tc.ID,
					FormatWriteRecoveryContent(toolName, filePath, evidence, priorRecoveries)))
				priorRecoveries++
				inserted++
			}
		}
	}

	return ResumePreflightReport{
		MessageCount:             len(messages),
		RoundCount:               CountRoundsOai(repaired),
		Repaired:                 true,
		SyntheticResultsInserted: inserted,
		Messages:                 repaired,
	}
}

// toolResultMessage 构造一条 tool 结果消息。
func toolResultMessage(toolCallID, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: "tool", ToolCallID: toolCallID, Content: &c}
}

func contentOf(m session.OaiMessage) string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// sameMessages 做元素级比较（Go 不能直接比切片身份）。
//
// **为什么不用 `&a[0] == &b[0]`**：空切片会 panic，且两切片共享底层数组时
// 首地址相同但长度可能不同。
//
// **ToolCalls 的 nil vs 空切片差异必须算「不同」**：规范化正是把
// `[]OaiToolCall{}` 变成 `nil`——只比 `len` 会把这次改动判为「无变化」，
// 从而让 `Repaired` 漏报（首版踩过）。
func sameMessages(a, b []session.OaiMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].ToolCallID != b[i].ToolCallID {
			return false
		}
		if contentOf(a[i]) != contentOf(b[i]) {
			return false
		}
		if len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
		// 长度相同但 nil/非 nil 不同 → 仍是改动（规范化清除了空数组）。
		if (a[i].ToolCalls == nil) != (b[i].ToolCalls == nil) {
			return false
		}
	}
	return true
}

// isToolAdjacencyCleanOai 判定 tool 邻接是否满足供应商要求。
//
// 对账 TS `isToolAdjacencyCleanOai`。**这是供应商的真实要求**：
//
//   - 每条 `tool` 消息必须紧跟在带匹配 id 的 assistant(tool_calls) 之后
//   - 那段连续的 tool 消息里 id 不得重复、不得有外来 id
//   - assistant 声明的每个 tool_call 都要在紧随的 tool 段里有结果
//   - 任何位置都不得出现游离的 tool 消息
func isToolAdjacencyCleanOai(messages []session.OaiMessage) bool {
	for i := 0; i < len(messages); i++ {
		m := messages[i]
		if m.Role == "tool" {
			// 游离 tool（没有前置 assistant(tool_calls)）——注意：上面的循环
			// 会在 assistant 分支里消费掉连续的 tool 段，故走到这里的就是游离的。
			return false
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}

		ids := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			ids[tc.ID] = true
		}
		seen := map[string]bool{}
		j := i + 1
		for j < len(messages) && messages[j].Role == "tool" {
			id := messages[j].ToolCallID
			if !ids[id] || seen[id] {
				return false // 外来 id 或重复 id
			}
			seen[id] = true
			j++
		}
		if len(seen) != len(ids) {
			return false // 有 tool_call 缺结果
		}
		i = j - 1 // 跳过已消费的 tool 段
	}
	return true
}
