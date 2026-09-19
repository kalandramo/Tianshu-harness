package session

import (
	"strconv"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// MaxSessionMessageJSONChars 是单条会话消息的 JSON 字符上限。
//
// 对账 MAX_SESSION_MESSAGE_JSON_CHARS = 100_000。
const MaxSessionMessageJSONChars = 100_000

// SessionMessageTruncatedMarkerFmt 是截断标记的模板。
//
// 对账 truncateString 的 marker，**逐字**：
//
//	`\n<session-message-truncated original_chars="${value.length}" kept_chars="${maxChars}" />`
//
// 注意 marker 里的 `original_chars` 是**原始值长度**、`kept_chars` 是**传入的
// maxChars**（不是实际保留的字符数——实际保留的是 `maxChars - marker长度`）。
// 这是 TS 的原样行为，不要"修正"。
const sessionMessageTruncatedMarkerFmt = "\n<session-message-truncated original_chars=\"%d\" kept_chars=\"%d\" />"

// TruncateString 按 UTF-16 code unit 截断字符串并附截断标记。
//
// 对账 truncateString。**关键语义**：
//   - 长度判定与截断都用 JS 的 `.length`（**UTF-16 code unit**，emoji 计 2）
//   - marker 占用长度从保留额度里扣（`maxChars - marker.length`）
//   - `Math.max(0, ...)` —— 额度不足时保留 0 字符（marker 仍完整附上）
func TruncateString(value string, maxChars int) string {
	if utf16Len(value) <= maxChars {
		return value
	}
	marker := truncationMarker(utf16Len(value), maxChars)
	keep := maxChars - utf16Len(marker)
	if keep < 0 {
		keep = 0
	}
	return utf16Slice(value, keep) + marker
}

func truncationMarker(originalChars, maxChars int) string {
	return "\n<session-message-truncated original_chars=\"" +
		strconv.Itoa(originalChars) + "\" kept_chars=\"" + strconv.Itoa(maxChars) + "\" />"
}

// CapJSONValue 递归把值里的字符串截断到 maxChars。
//
// 对账 capJsonValue：
//   - 字符串 → TruncateString
//   - 数组 → 逐元素递归
//   - 对象 → 逐键递归（**保留键序**）
//   - 其他（数字/布尔/nil）→ 原样
//
// 入参用 wire 的有序结构（保插入序）——对账 JS 的 `Object.entries`。
func CapJSONValue(value any, maxChars int) any {
	switch v := value.(type) {
	case string:
		return TruncateString(v, maxChars)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = CapJSONValue(item, maxChars)
		}
		return out
	case *wire.OrderedMap:
		if v == nil {
			return v
		}
		out := wire.NewOrderedMap()
		for _, k := range v.Keys() {
			child, _ := v.Get(k)
			out.Set(k, CapJSONValue(child, maxChars))
		}
		return out
	default:
		return value
	}
}

// SerializeJSONValue 是三层截断的核心。
//
// 对账 serializeSessionJsonValue：
//  1. 原样序列化；若 ≤ maxChars → 返回
//  2. 用 `max(1000, floor(maxChars*0.8))` 作上限递归 cap 后重序列化；若 ≤ maxChars → 返回
//  3. 否则走 fallback（调用方提供）
func SerializeJSONValue(message any, maxChars int, fallback func() any) string {
	json := wire.MarshalValue(message)
	if utf16Len(json) <= maxChars {
		return json
	}
	capped := CapJSONValue(message, capBudget(maxChars))
	json = wire.MarshalValue(capped)
	if utf16Len(json) <= maxChars {
		return json
	}
	return wire.MarshalValue(fallback())
}

// capBudget 计算第二层的截断预算：`max(1000, floor(maxChars * 0.8))`。
//
// 对账 `Math.max(1_000, Math.floor(maxChars * 0.8))`。
func capBudget(maxChars int) int {
	b := int(float64(maxChars) * 0.8) // Go 的 int() 对正数即 floor
	if b < 1000 {
		return 1000
	}
	return b
}

// SerializeSessionMessage 序列化一条传统 Message（带三层截断）。
//
// 对账 serializeSessionMessage。fallback 是：
//
//	{ role: message.role, content: truncateString(JSON.stringify(message), maxChars) }
//
// 注意 fallback 里的 content 是**整条消息的 JSON 再截断**——即消息被"压扁"
// 成一个字符串塞进 content。
func SerializeSessionMessage(role string, message any, maxChars int) string {
	return SerializeJSONValue(message, maxChars, func() any {
		fb := wire.NewOrderedMap()
		fb.Set("role", role)
		fb.Set("content", TruncateString(wire.MarshalValue(message), maxChars))
		return fb
	})
}

// SerializeOaiSessionMessage 序列化一条 OaiMessage（带三层截断）。
//
// 对账 serializeOaiSessionMessage。**先归一化**（移除空 tool_calls 数组），
// fallback 是：
//
//	{ role, content: truncateString(JSON.stringify(normalized), maxChars),
//	  ...(role === 'tool' ? { tool_call_id } : {}) }
//
// 注意 tool_call_id 只在 role 为 tool 时带——对账 TS 的条件展开。
func SerializeOaiSessionMessage(m OaiMessage, maxChars int) string {
	normalized, _ := NormalizeOaiMessage(m)
	raw := oaiToWire(normalized)
	return SerializeJSONValue(raw, maxChars, func() any {
		fb := wire.NewOrderedMap()
		fb.Set("role", normalized.Role)
		fb.Set("content", TruncateString(wire.MarshalValue(raw), maxChars))
		if normalized.Role == "tool" {
			fb.Set("tool_call_id", normalized.ToolCallID)
		}
		return fb
	})
}

// oaiToWire 把 OaiMessage 转成 wire 有序结构。
//
// **键序对账 JSON.stringify 的键序**——即原对象键的插入序。TS 侧 OaiMessage
// 从 JSON.parse 得来，键序是文件里的原始序；Go 侧按下面的固定序重建。
//
// 这是已知偏差：若会话文件里的键序与此不同，序列化结果会不同。但**读-改-写
// 的往返里键序由首次写入决定**，故只要写入端一致即可自洽（见 HANDOFF）。
func oaiToWire(m OaiMessage) *wire.OrderedMap {
	om := wire.NewOrderedMap()
	// 按 KeyOrder 输出（保 TS 的插入序）；nil 时用默认序
	order := m.KeyOrder
	if len(order) == 0 {
		order = []string{"role", "content", "tool_calls", "tool_call_id"}
	}
	for _, k := range order {
		switch k {
		case "role":
			om.Set("role", m.Role)
		case "content":
			if m.Content != nil {
				om.Set("content", *m.Content)
			}
		case "tool_calls":
			if m.ToolCalls != nil {
				om.Set("tool_calls", oaiToolCallsToWire(m.ToolCalls))
			}
		case "tool_call_id":
			if m.ToolCallID != "" {
				om.Set("tool_call_id", m.ToolCallID)
			}
		}
	}
	return om
}

func oaiToolCallsToWire(tcs []OaiToolCall) []any {
	arr := make([]any, len(tcs))
	for i, tc := range tcs {
		tcm := wire.NewOrderedMap()
		tcm.Set("id", tc.ID)
		tcm.Set("type", tc.Type)
		if tc.Function != nil {
			fn := wire.NewOrderedMap()
			fn.Set("name", tc.Function.Name)
			fn.Set("arguments", tc.Function.Arguments)
			tcm.Set("function", fn)
		}
		arr[i] = tcm
	}
	return arr
}
