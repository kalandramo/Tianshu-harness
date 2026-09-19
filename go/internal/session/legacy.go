package session

import (
	"github.com/kalandramo/tianshu/go/internal/api/stablejson"
)

// LegacyMessageToOaiMessages 把传统 Message（content 为块数组）迁移为 OAI 消息。
//
// 对账 legacyMessageToOaiMessages。三种情况：
//  1. `content` 是字符串 → 直接 `[{role, content}]`
//  2. `role === 'user'` → 拆成文本 user 消息 + 若干 tool 结果消息
//  3. 其他（assistant）→ 文本 + thinking + tool_use 块拆成一条 assistant 消息
//
// **tool_use 的 arguments 用 `stableStringify`**（排序键的序列化器）——
// 对账 TS 的 stableStringify，不是 JSON.stringify。
func LegacyMessageToOaiMessages(m map[string]any) []OaiMessage {
	role, _ := m["role"].(string)

	// 情况 1：content 是字符串
	if s, ok := m["content"].(string); ok {
		c := s
		return []OaiMessage{{Role: role, Content: &c}}
	}

	blocks, _ := m["content"].([]any)

	// 情况 2：user
	if role == "user" {
		var text string
		var tools []OaiMessage
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := bm["type"].(string)
			switch typ {
			case "text":
				t, _ := bm["text"].(string)
				text += t
			case "tool_result":
				useID, _ := bm["tool_use_id"].(string)
				content, _ := bm["content"].(string)
				cc := content
				tools = append(tools, OaiMessage{
					Role: "tool", ToolCallID: useID, Content: &cc,
				})
			}
		}
		out := []OaiMessage{}
		if text != "" {
			t := text
			out = append(out, OaiMessage{Role: "user", Content: &t})
		}
		out = append(out, tools...)
		return out
	}

	// 情况 3：assistant（或其他）
	var text, reasoning string
	var toolCalls []OaiToolCall
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := bm["type"].(string)
		switch typ {
		case "text":
			t, _ := bm["text"].(string)
			text += t
		case "thinking":
			th, _ := bm["thinking"].(string)
			reasoning += th
		case "tool_use":
			id, _ := bm["id"].(string)
			name, _ := bm["name"].(string)
			input := bm["input"]
			toolCalls = append(toolCalls, OaiToolCall{
				ID:   id,
				Type: "function",
				Function: &OaiFunction{
					Name: name,
					// **stableStringify**（排序键）——对账 TS
					Arguments: stableStringifyValue(input),
				},
			})
		}
	}

	// content 语义：text 非空用 text；否则若**无** toolCalls 用空串，
	// 有 toolCalls 用 nil（对账 `text || (toolCalls.length === 0 ? '' : null)`）
	var content *string
	if text != "" {
		content = &text
	} else if len(toolCalls) == 0 {
		empty := ""
		content = &empty
	}

	out := OaiMessage{Role: "assistant", Content: content}
	if reasoning != "" {
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		out.Extra["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		out.ToolCalls = toolCalls
	}
	// 键序对账 TS 的对象字面量：
	// role → content → reasoning_content → tool_calls
	order := []string{"role", "content"}
	if reasoning != "" {
		order = append(order, "reasoning_content")
	}
	if len(toolCalls) > 0 {
		order = append(order, "tool_calls")
	}
	out.KeyOrder = order
	return []OaiMessage{out}
}

// stableStringifyValue 用排序键的方式序列化值（对账 TS 的 stableStringify）。
func stableStringifyValue(v any) string {
	return stablejson.Stringify(v)
}
