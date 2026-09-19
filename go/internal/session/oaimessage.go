package session

// OaiMessage 是 OpenAI 兼容协议的消息（会话 transcript 的存储形态）。
//
// 对账 src/api/oai-types.ts。Go 侧主循环用 wire.OrderedMap 传线上格式，
// 这里是**会话落盘与恢复**用的结构化形态——两者用途不同，不要混用。
//
// 字段用指针/切片区分「缺失」与「零值」：
//   - ToolCalls 为 nil = 字段缺失；非 nil 空切片 = 显式空数组（非法，需归一化）
//   - Content 为 nil = 缺失（normalizeOaiMessage 会补空串）
type OaiMessage struct {
	Role       string
	Content    *string
	ToolCalls  []OaiToolCall
	ToolCallID string
	// 其余字段原样保留（透传，不解析）
	Extra map[string]any

	// KeyOrder 是**原始 JSON 的键序**。
	//
	// 为什么必须保留：TS 的 `JSON.stringify(message)` 保**插入序**，而插入序
	// 由对象构造决定（从 JSON.parse 得来的是文件序、代码字面量是书写序）。
	// 用固定序重建会产出与 TS 不同的字节——oracle 的 keyOrderContentRole /
	// keyOrderToolCallIdFirst 用例锁定这一点。
	//
	// 为 nil 时退化为「role → content → tool_calls → tool_call_id」的默认序。
	KeyOrder []string
}

// OaiToolCall 是一次工具调用。
type OaiToolCall struct {
	ID       string
	Type     string
	Function *OaiFunction
	Extra    map[string]any
}

// OaiFunction 是工具调用的函数体。
type OaiFunction struct {
	Name      string
	Arguments string
}

// WriteToolNames 是**孤儿恢复必须非破坏性**的工具集合。
//
// 对账 WRITE_TOOL_NAMES：文件可能已经持有预期改动，模型必须先核实再重写。
var WriteToolNames = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"hash_edit":   true,
	"ast_edit":    true,
	"apply_patch": true,
}

// NormalizeOaiMessage 归一化一条 assistant 消息。
//
// 对账 normalizeOaiMessage。OpenAI 兼容 API 区分「省略 tool_calls」与
// 「空数组」——后者非法（minItems: 1），即便 assistant 有普通文本内容。
// 空数组可能残留在旧会话文件里，故移除它（不修改调用方的对象）。
//
// 返回归一化后的消息与「是否发生了改动」。
func NormalizeOaiMessage(m OaiMessage) (OaiMessage, bool) {
	if m.Role != "assistant" || m.ToolCalls == nil || len(m.ToolCalls) > 0 {
		return m, false
	}
	// 移除空 tool_calls 数组；content 为 nil 时补空串
	out := m
	out.ToolCalls = nil
	if out.Content == nil {
		empty := ""
		out.Content = &empty
	}
	return out, true
}

// NormalizeOaiMessages 归一化整个消息数组。
//
// 对账 normalizeOaiMessages：**无改动时返回原数组**（避免不必要的拷贝）。
func NormalizeOaiMessages(msgs []OaiMessage) []OaiMessage {
	var out []OaiMessage
	for i, m := range msgs {
		norm, changed := NormalizeOaiMessage(m)
		if !changed {
			continue
		}
		if out == nil {
			out = append([]OaiMessage(nil), msgs...)
		}
		out[i] = norm
	}
	if out == nil {
		return msgs
	}
	return out
}

// OrphanRepair 是孤儿修复的结果。
type OrphanRepair struct {
	Messages []OaiMessage
	// HadOrphans 表示有孤儿被剔除（调用方据此给模型警告）。
	HadOrphans bool
	// StrippedWriteTool 表示被剔除的孤儿里有**写类工具**——
	// 警告文案必须用非破坏性版本（文件可能已改，先核实再重写）。
	StrippedWriteTool bool
}

// RepairOrphanToolCalls 移除损坏/缺失行留下的孤儿 tool_use / tool_result 对。
//
// 对账 repairOrphanToolCalls（压#7）。孤儿 tool_use 的产生场景：流已送出
// 完整 tool_calls 块，但进程在工具执行前被杀（强杀/断电/流错误）。
// **工具从未运行**——它本会创建/修改的文件不存在。
//
// 两个 pass：
//  1. 收集所有 tool_call id 与 tool_result 的行号
//  2. 走一遍消息：丢孤儿 result；从 assistant 消息里剔除孤儿 tool_call；
//     若某条 assistant 的 tool_call **全部**孤儿且 content 为空 → 整条丢弃
//
// 返回修复后的消息、是否有孤儿、被剔除的孤儿里是否含写类工具。
func RepairOrphanToolCalls(msgs []OaiMessage) OrphanRepair {
	toolCallIDs := map[string]bool{}
	toolResultIdx := map[string]int{}
	for i, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					toolCallIDs[tc.ID] = true
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResultIdx[m.ToolCallID] = i
		}
	}

	orphanResultIdx := map[int]bool{}
	for id, idx := range toolResultIdx {
		if !toolCallIDs[id] {
			orphanResultIdx[idx] = true
		}
	}

	var result []OaiMessage
	hadOrphans := false
	strippedWriteTool := false
	noteStripped := func(tc OaiToolCall) {
		name := ""
		if tc.Function != nil {
			name = tc.Function.Name
		}
		if WriteToolNames[name] {
			strippedWriteTool = true
		}
	}

	for i, m := range msgs {
		// 丢孤儿 tool 结果
		if orphanResultIdx[i] {
			hadOrphans = true
			continue
		}
		// 从 assistant 消息里剔除孤儿 tool_call
		if m.Role == "assistant" && m.ToolCalls != nil {
			var valid, orphaned []OaiToolCall
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && hasKey(toolResultIdx, tc.ID) {
					valid = append(valid, tc)
				} else {
					orphaned = append(orphaned, tc)
				}
			}
			// 全部孤儿且 content 为空 → 整条丢弃
			if len(valid) == 0 && !hasContent(m.Content) {
				hadOrphans = true
				for _, tc := range orphaned {
					noteStripped(tc)
				}
				continue
			}
			if len(valid) != len(m.ToolCalls) {
				hadOrphans = true
				for _, tc := range orphaned {
					noteStripped(tc)
				}
				trimmed := m
				trimmed.ToolCalls = valid
				result = append(result, trimmed)
				continue
			}
		}
		result = append(result, m)
	}
	return OrphanRepair{Messages: result, HadOrphans: hadOrphans, StrippedWriteTool: strippedWriteTool}
}

// hasContent 复刻 JS 的 `!msg.content` 真值判断。
//
// JS 里空串、null、undefined 都是 falsy——故 `!msg.content` 在
// content="" 或缺失时为 true。
func hasContent(c *string) bool {
	return c != nil && *c != ""
}

func hasKey(m map[string]int, k string) bool {
	_, ok := m[k]
	return ok
}

// OrphanReminderGeneric 是「通用」中断恢复提示（无写类工具被剔除）。
//
// 对账 session-persist.ts 的 else 分支，**逐字**。
const OrphanReminderGeneric = "<system-reminder>The previous session was interrupted mid-turn. " +
	"Some tool calls from the last assistant message were stripped because " +
	"their results were not recorded. Re-run any read-only/search steps you " +
	"still need; verify state before assuming any side effects took hold.</system-reminder>"

// OrphanReminderWriteTool 是「非破坏性」中断恢复提示（含写类工具被剔除）。
//
// 对账 session-persist.ts 的 strippedWriteTool 分支，**逐字**。
// 语义要点：中断可能发生在文件写入**之前或之后**，故文件可能已含预期
// 改动——不要盲目重跑写入，先核实。
const OrphanReminderWriteTool = "<system-reminder>The previous session was interrupted mid-turn. " +
	"A write/edit tool call from the last assistant message was stripped " +
	"because its result was not recorded — but the interruption may have " +
	"happened either before OR after the file was actually written, so the " +
	"file may or may not already contain the intended changes. Do NOT blindly " +
	"re-run the write. First verify the file's current state with read_file " +
	"or grep, then only write what is still missing. This is host-process " +
	"interruption recovery, NOT a tool malfunction — the write tools remain " +
	"fully functional; keep using them normally instead of bash workarounds.</system-reminder>"

// IsOaiMessage 判定一条解析后的 JSON 是否为合法的 OaiMessage。
//
// 对账 session-persist.ts 的 isOaiMessage。这是 `loadOai` 的**第一层过滤**：
// 不合法的行在进入 normalize/repair 之前就被跳过。
//
// **易错点**：assistant 要求 `content` 是 string 或 **null**——
// 字段完全缺失（JSON 里没有 content 键）时为 undefined，既不等于 null
// 也不是 string → **整行被跳过**。这正是 `{role:'assistant',tool_calls:[]}`
// 这种残缺行消失的原因（oracle 的 emptyToolCallsNullContent 用例锁定）。
func IsOaiMessage(raw map[string]any) bool {
	role, _ := raw["role"].(string)
	switch role {
	case "system", "user":
		_, ok := raw["content"].(string)
		return ok
	case "assistant":
		c, exists := raw["content"]
		if !exists {
			return false // 字段缺失 → undefined → 不合法
		}
		if c == nil {
			return true // 显式 null 合法
		}
		_, ok := c.(string)
		return ok
	case "tool":
		_, hasID := raw["tool_call_id"].(string)
		_, hasContent := raw["content"].(string)
		return hasID && hasContent
	}
	return false
}
