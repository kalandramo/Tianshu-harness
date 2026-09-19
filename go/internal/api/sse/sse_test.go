package sse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// oracleEvent 是真实 TS 客户端捕获的一个事件。
type oracleEvent struct {
	Kind          string         `json:"kind"`
	Value         string         `json:"value"`
	BlockType     string         `json:"blockType"`
	Text          string         `json:"text"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Input         map[string]any `json:"input"`
	ArgsTruncated bool           `json:"argsTruncated"`
	Reason        string         `json:"reason"`
	Usage         *struct {
		InputTokens              *int `json:"input_tokens"`
		OutputTokens             *int `json:"output_tokens"`
		CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		ReasoningTokens          *int `json:"reasoning_tokens"`
	} `json:"usage"`
}

func loadSSEOracle(t *testing.T) map[string][]oracleEvent {
	t.Helper()
	p := filepath.Join("..", "..", "..", "testdata", "sse", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 SSE oracle 失败（%s）：%v\n生成：node_modules/.bin/tsx go/testdata/sse/gen-oracle.ts", p, err)
	}
	var out map[string][]oracleEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空——测试前置条件失败")
	}
	return out
}

// sseLine 构造一条 SSE data 行（与 gen-oracle.ts 的 sse() 一致）。
func sseLine(obj any) string {
	b, _ := json.Marshal(obj)
	return "data: " + string(b) + "\n\n"
}

// scenarios 是各场景的原始 SSE 分片（与 gen-oracle.ts 逐字对应）。
func scenarios() map[string][]string {
	return map[string][]string{
		"text_only": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": " world"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}),
			sseLine(map[string]any{"choices": []any{}, "usage": map[string]any{
				"prompt_tokens": 100, "completion_tokens": 5,
				"prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20,
			}}),
			"data: [DONE]\n\n",
		},
		"reasoning_then_text": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "思考中"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "..."}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "答案"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}),
			"data: [DONE]\n\n",
		},
		"reasoning_after_content": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "先说话"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "后到的推理"}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}),
			"data: [DONE]\n\n",
		},
		"single_tool_call": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": ""}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": `{"path"`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "function": map[string]any{"arguments": `:"a.ts"}`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			"data: [DONE]\n\n",
		},
		"parallel_tool_calls": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c0", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a"}`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 1, "id": "c1", "type": "function", "function": map[string]any{"name": "grep", "arguments": `{"pattern":"x"}`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			"data: [DONE]\n\n",
		},
		"trailing_args_after_finish": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c0", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"function": map[string]any{"arguments": `.ts"}`}},
			}}}}}),
			"data: [DONE]\n\n",
		},
		"ambiguous_continuation": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c0", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 1, "id": "c1", "type": "function", "function": map[string]any{"name": "grep", "arguments": `{"pattern":"x`}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"function": map[string]any{"arguments": `"}`}},
			}}}}}),
			"data: [DONE]\n\n",
		},
		"truncated_args": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c0", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"rm -rf `}},
			}}}}}),
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
			"data: [DONE]\n\n",
		},
		"usage_with_finish": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}}}),
			sseLine(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 3, "prompt_cache_hit_tokens": 40},
			}),
			"data: [DONE]\n\n",
		},
		"split_across_chunks": {
			`data: {"choices":[{"delta":{"cont`,
			"ent\":\"分割\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},",
			"\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		},
		"heartbeat_lines": {
			": keepalive\n\n",
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "hi"}}}}),
			":\n\n",
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}),
			"data: [DONE]\n\n",
		},
		"reasoning_tokens": {
			sseLine(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "x"}}}}),
			sseLine(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
				"usage": map[string]any{
					"prompt_tokens": 10, "completion_tokens": 20,
					"completion_tokens_details": map[string]any{"reasoning_tokens": 15},
				},
			}),
			"data: [DONE]\n\n",
		},
	}
}

// runScenario 用给定分片驱动解析器，收集事件。
//
// 关键：**逐片喂入**（不是拼接后一次喂），才能验证跨片缓冲语义。
func runScenario(chunks []string) []oracleEvent {
	var events []oracleEvent
	h := Handler{
		OnTextDelta:     func(s string) { events = append(events, oracleEvent{Kind: "text", Value: s}) },
		OnThinkingDelta: func(s string) { events = append(events, oracleEvent{Kind: "thinking", Value: s}) },
		OnContentBlock: func(e Event) {
			ev := oracleEvent{
				Kind: e.Kind, BlockType: e.BlockType, Text: e.Text,
				ID: e.ToolID, Name: e.ToolName, Input: e.ToolInput,
				ArgsTruncated: e.ArgsTruncated,
			}
			if ev.Kind == "" {
				ev.Kind = "block"
			}
			events = append(events, ev)
		},
		OnStopReason: func(reason string, u contract.Usage) {
			ou := &struct {
				InputTokens              *int `json:"input_tokens"`
				OutputTokens             *int `json:"output_tokens"`
				CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
				ReasoningTokens          *int `json:"reasoning_tokens"`
			}{}
			if u.InputTokens != 0 || u.OutputTokens != 0 {
				it, ot := u.InputTokens, u.OutputTokens
				cr, cc := u.CacheReadInputTokens, u.CacheCreationInputTokens
				ou.InputTokens, ou.OutputTokens = &it, &ot
				ou.CacheReadInputTokens, ou.CacheCreationInputTokens = &cr, &cc
			}
			if u.ReasoningTokens != nil {
				ou.ReasoningTokens = u.ReasoningTokens
			}
			events = append(events, oracleEvent{Kind: "stop", Reason: reason, Usage: ou})
		},
	}

	p := NewParser(h)
	// 模拟真实读取：累积缓冲 + 按行切分（保留不完整尾行）
	var buf string
	for _, chunk := range chunks {
		buf += chunk
		// 找出完整的行（以 \n 结尾）
		for {
			idx := indexOfNewline(buf)
			if idx < 0 {
				break
			}
			line := buf[:idx+1]
			buf = buf[idx+1:]
			if done := p.feedRaw(line); done {
				p.Finish()
				return events
			}
		}
	}
	// 残余缓冲（无尾换行）
	if buf != "" {
		p.feedRaw(buf)
	}
	p.Finish()
	return events
}

func indexOfNewline(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return i
		}
	}
	return -1
}

// TestSSEGoldenParity 用真实 TS 客户端捕获的回调序列对账 Go 解析器。
func TestSSEGoldenParity(t *testing.T) {
	oracle := loadSSEOracle(t)
	sc := scenarios()

	// 双向覆盖检查
	for name := range oracle {
		if _, ok := sc[name]; !ok {
			t.Errorf("oracle 含未覆盖的场景 %q——gen-oracle.ts 与测试不同步", name)
		}
	}
	for name := range sc {
		if _, ok := oracle[name]; !ok {
			t.Errorf("场景 %q 未在 oracle 中——需重新生成", name)
		}
	}

	for name, want := range oracle {
		chunks, ok := sc[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := runScenario(chunks)
			if len(got) != len(want) {
				t.Fatalf("事件数不符：got %d, want %d\n  got:  %s\n  want: %s",
					len(got), len(want), dumpEvents(got), dumpEvents(want))
			}
			for i := range want {
				if !eventsEqual(got[i], want[i]) {
					t.Errorf("事件 #%d 不符\n  got:  %s\n  want: %s",
						i, dumpEvent(got[i]), dumpEvent(want[i]))
				}
			}
		})
	}
}

// eventsEqual 比较两个事件（忽略未设置的零值字段差异）。
func eventsEqual(a, b oracleEvent) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case "text", "thinking":
		return a.Value == b.Value
	case "stop":
		return a.Reason == b.Reason && usageEqual(a.Usage, b.Usage)
	case "block":
		if a.BlockType != b.BlockType {
			return false
		}
		if a.BlockType == "tool_use" {
			return a.ID == b.ID && a.Name == b.Name &&
				a.ArgsTruncated == b.ArgsTruncated &&
				reflect.DeepEqual(normalizeInput(a.Input), normalizeInput(b.Input))
		}
		return a.Text == b.Text
	}
	return false
}

// normalizeInput 把 nil 与空 map 视为等价（TS 侧 {} 与 Go 的 map 表达差异）。
func normalizeInput(m map[string]any) map[string]any {
	if len(m) == 0 {
		return map[string]any{}
	}
	return m
}

func usageEqual(a, b *struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	ReasoningTokens          *int `json:"reasoning_tokens"`
}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// 一方为 nil、另一方为零值 → 视为等价（TS 侧 usage:{} 与 Go 零值）
		other := a
		if other == nil {
			other = b
		}
		return derefI(other.InputTokens) == 0 && derefI(other.OutputTokens) == 0 &&
			derefI(other.CacheReadInputTokens) == 0 && derefI(other.CacheCreationInputTokens) == 0
	}
	return derefI(a.InputTokens) == derefI(b.InputTokens) &&
		derefI(a.OutputTokens) == derefI(b.OutputTokens) &&
		derefI(a.CacheReadInputTokens) == derefI(b.CacheReadInputTokens) &&
		derefI(a.CacheCreationInputTokens) == derefI(b.CacheCreationInputTokens) &&
		derefI(a.ReasoningTokens) == derefI(b.ReasoningTokens)
}

func derefI(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func dumpEvent(e oracleEvent) string {
	b, _ := json.Marshal(e)
	return string(b)
}

func dumpEvents(es []oracleEvent) string {
	b, _ := json.Marshal(es)
	return string(b)
}
