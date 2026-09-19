package sse

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// collect 用简单处理器收集事件。
func collect() (*Parser, *[]Event) {
	var events []Event
	h := Handler{
		OnTextDelta:     func(s string) { events = append(events, Event{Kind: "text", Value: s}) },
		OnThinkingDelta: func(s string) { events = append(events, Event{Kind: "thinking", Value: s}) },
		OnContentBlock:  func(e Event) { events = append(events, e) },
		OnStopReason: func(r string, u contract.Usage) {
			events = append(events, Event{Kind: "stop", StopReason: r, Usage: u})
		},
	}
	return NewParser(h), &events
}

// 反证 A：续块归属绝不能退化为 `index ?? 0`。
//
// 这是跨工具污染事故（oh-my-pi/384919c7）的核心：两个缓冲打开时，
// 无 index 无 id 的续块若归到 index 0，会把 grep 的参数嫁接到 read_file 上，
// 同时污染两者。
//
// 若把 resolveToolCallIndex 的「歧义丢弃」改成 `return 0, true`，本测试变红。
func TestAmbiguousContinuationIsDropped(t *testing.T) {
	p, events := collect()

	// 打开两个缓冲，参数都不完整
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a"}}]}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"x"}}]}}]}`)
	// finish_reason → 非 final flush（两者参数不完整，应保留）
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	if len(*events) != 0 {
		t.Fatalf("非 final flush 不应产出未完成块，实际产出 %d 个", len(*events))
	}
	// 无身份续块 → 必须丢弃
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"}"}}]}}]}`)
	p.Finish()

	// 两个块都应 argsTruncated（参数不完整），且**参数未被嫁接**
	var toolBlocks []Event
	for _, e := range *events {
		if e.BlockType == "tool_use" {
			toolBlocks = append(toolBlocks, e)
		}
	}
	if len(toolBlocks) != 2 {
		t.Fatalf("应产出 2 个 tool_use 块，实际 %d", len(toolBlocks))
	}
	for _, b := range toolBlocks {
		if !b.ArgsTruncated {
			t.Errorf("%s 的参数不应完整（续块被丢弃）——若完整说明续块被错误嫁接", b.ToolName)
		}
		if len(b.ToolInput) != 0 {
			t.Errorf("%s 的 input 应为空，实际 %v（污染）", b.ToolName, b.ToolInput)
		}
	}
	// 关键断言：read_file 绝不能拿到 grep 的参数
	for _, b := range toolBlocks {
		if b.ToolName == "read_file" && b.ToolInput["pattern"] != nil {
			t.Fatalf("read_file 拿到了 pattern 参数——跨工具污染！input=%v", b.ToolInput)
		}
		if b.ToolName == "grep" && b.ToolInput["path"] != nil {
			t.Fatalf("grep 拿到了 path 参数——跨工具污染！input=%v", b.ToolInput)
		}
	}
}

// 反证 B：单缓冲打开时，无身份续块应被 reattach（唯一性归属）。
func TestSoleBufferReattach(t *testing.T) {
	p, events := collect()

	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a"}}]}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	// 无 index 无 id，但只有一个缓冲打开 → 应 reattach
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":".ts\"}"}}]}}]}`)
	p.Finish()

	var toolBlocks []Event
	for _, e := range *events {
		if e.BlockType == "tool_use" {
			toolBlocks = append(toolBlocks, e)
		}
	}
	if len(toolBlocks) != 1 {
		t.Fatalf("应产出 1 个 tool_use 块，实际 %d", len(toolBlocks))
	}
	b := toolBlocks[0]
	if b.ArgsTruncated {
		t.Fatal("单缓冲续块应被 reattach，参数应完整")
	}
	if b.ToolInput["path"] != "a.ts" {
		t.Errorf("参数拼接错误：%v", b.ToolInput)
	}
}

// 反证 C：通道单调性——content 开始后到的 reasoning_content 重归类为 text。
func TestChannelMonotonicity(t *testing.T) {
	p, events := collect()

	p.ProcessPayload(`{"choices":[{"delta":{"content":"先说话"}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{"reasoning_content":"后到的推理"}}]}`)
	p.Finish()

	var thinkingCount, textCount int
	var textAll string
	for _, e := range *events {
		switch e.Kind {
		case "thinking":
			thinkingCount++
		case "text":
			textCount++
			textAll += e.Value
		}
	}
	if thinkingCount != 0 {
		t.Errorf("content 已开始后不应有 thinking 事件，实际 %d 个", thinkingCount)
	}
	if textCount != 2 {
		t.Errorf("应有两个 text 事件（含重归类的 reasoning），实际 %d", textCount)
	}
	if textAll != "先说话后到的推理" {
		t.Errorf("文本累积错误：%q", textAll)
	}
}

// 反证 D：参数截断必须产出 ArgsTruncated=true，绝不静默喂空对象当成功。
//
// 若 tryParseToolArguments 对空/不可解析参数返回 ok=true，本测试变红——
// 且会导致半个命令被真的执行（session 4df36bcd 事故）。
func TestTruncatedArgsMarkedNotSilentlyExecuted(t *testing.T) {
	p, events := collect()

	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"bash","arguments":"{\"command\":\"rm -rf "}}]}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	p.Finish()

	var found bool
	for _, e := range *events {
		if e.BlockType == "tool_use" {
			found = true
			if !e.ArgsTruncated {
				t.Fatalf("截断的参数必须标记 ArgsTruncated（否则半个命令会被执行）")
			}
			if len(e.ToolInput) != 0 {
				t.Errorf("截断时 input 应为空，实际 %v", e.ToolInput)
			}
		}
	}
	if !found {
		t.Fatal("未产出 tool_use 块")
	}
}

// 反证 E：两阶段 flush——finish_reason 时不可解析的条目必须保留到 final flush。
//
// 若非 final flush 直接丢弃或直接产出，本测试变红。
func TestTwoPhaseFlushKeepsUnparseable(t *testing.T) {
	p, events := collect()

	// 参数不完整
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`)
	// finish_reason → 非 final flush，应保留（不产出）
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	if len(*events) != 0 {
		t.Fatalf("非 final flush 不应产出（参数未完成），实际 %d 个事件", len(*events))
	}
	// 补全参数
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"b.ts\"}"}}]}}]}`)
	p.Finish()

	var blocks []Event
	for _, e := range *events {
		if e.BlockType == "tool_use" {
			blocks = append(blocks, e)
		}
	}
	if len(blocks) != 1 {
		t.Fatalf("应产出 1 个块，实际 %d", len(blocks))
	}
	if blocks[0].ArgsTruncated {
		t.Error("参数已补全，不应标记截断")
	}
	if blocks[0].ToolInput["path"] != "b.ts" {
		t.Errorf("参数错误：%v", blocks[0].ToolInput)
	}
}

// 反证 F：并行工具块的产出顺序必须稳定（按槽位升序），不得随 map 迭代随机化。
func TestParallelToolOrderIsDeterministic(t *testing.T) {
	// 跑多次确认顺序稳定
	for run := 0; run < 20; run++ {
		p, events := collect()
		p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"aaa","arguments":"{}"}}]}}]}`)
		p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","type":"function","function":{"name":"bbb","arguments":"{}"}}]}}]}`)
		p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c2","type":"function","function":{"name":"ccc","arguments":"{}"}}]}}]}`)
		p.Finish()

		var names []string
		for _, e := range *events {
			if e.BlockType == "tool_use" {
				names = append(names, e.ToolName)
			}
		}
		got := strings.Join(names, ",")
		if got != "aaa,bbb,ccc" {
			t.Fatalf("第 %d 次运行顺序不符：%s（应为 aaa,bbb,ccc）", run, got)
		}
	}
}

// 心跳注释行与空行不得被当作数据事件。
func TestHeartbeatIgnored(t *testing.T) {
	p, events := collect()
	p.feedRaw(": keepalive\n\n")
	p.feedRaw(":\n\n")
	p.ProcessPayload(`{"choices":[{"delta":{"content":"hi"}}]}`)
	p.Finish()

	for _, e := range *events {
		if e.Kind == "text" && e.Value != "hi" {
			t.Errorf("心跳行被当作数据：%q", e.Value)
		}
	}
}

// 跨片分割：SSE 行被切在中间时必须正确缓冲。
func TestSplitAcrossChunksBuffered(t *testing.T) {
	p, events := collect()

	// 模拟真实读取循环：累积缓冲，按 \n 切分
	var buf string
	feed := func(chunk string) {
		buf += chunk
		for {
			i := strings.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			line := buf[:i+1]
			buf = buf[i+1:]
			p.feedRaw(line)
		}
	}

	feed(`data: {"choices":[{"delta":{"cont`)
	feed("ent\":\"分割\"}}]}\n\n")
	feed(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n")
	feed("data: [DONE]\n\n")
	p.Finish()

	var textAll string
	for _, e := range *events {
		if e.Kind == "text" {
			textAll += e.Value
		}
	}
	if textAll != "分割" {
		t.Errorf("跨片文本错误：%q", textAll)
	}
}

// usage 与 finish_reason 同块时，stop 事件必须带 usage。
func TestUsageWithFinishReason(t *testing.T) {
	p, events := collect()
	p.ProcessPayload(`{"choices":[{"delta":{"content":"ok"}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":3,"prompt_cache_hit_tokens":40}}`)
	p.Finish()

	var stop *Event
	for i := range *events {
		if (*events)[i].Kind == "stop" {
			stop = &(*events)[i]
		}
	}
	if stop == nil {
		t.Fatal("未产出 stop 事件")
	}
	if stop.Usage.InputTokens != 50 || stop.Usage.OutputTokens != 3 || stop.Usage.CacheReadInputTokens != 40 {
		t.Errorf("usage 错误：%+v", stop.Usage)
	}
}

// reasoning_tokens 应透传（是 output 的子集，不叠加）。
func TestReasoningTokensPassthrough(t *testing.T) {
	p, events := collect()
	p.ProcessPayload(`{"choices":[{"delta":{"content":"x"}}]}`)
	p.ProcessPayload(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":15}}}`)
	p.Finish()

	var stop *Event
	for i := range *events {
		if (*events)[i].Kind == "stop" {
			stop = &(*events)[i]
		}
	}
	if stop == nil || stop.Usage.ReasoningTokens == nil {
		t.Fatal("reasoning_tokens 未透传")
	}
	if *stop.Usage.ReasoningTokens != 15 {
		t.Errorf("reasoning_tokens = %d, want 15", *stop.Usage.ReasoningTokens)
	}
}

// finish_reason 映射表。
func TestMapFinishReason(t *testing.T) {
	cases := map[string]string{
		"stop":                         "end_turn",
		"tool_calls":                   "tool_use",
		"length":                       "max_tokens",
		"insufficient_system_resource": "end_turn",
		"unknown_reason":               "end_turn",
	}
	for in, want := range cases {
		if got := mapFinishReason(in); got != want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// 空参数串必须视为不可解析（「还没开始到达」≠「空对象」）。
func TestEmptyArgsNotParsedAsEmptyObject(t *testing.T) {
	if _, ok := tryParseToolArguments(""); ok {
		t.Fatal("空参数串不应被解析为合法（会导致空对象被当成功）")
	}
	if _, ok := tryParseToolArguments("{}"); !ok {
		t.Fatal("显式空对象应可解析")
	}
}

// JSON 语法错误不得 panic（流式分片常出现不完整 JSON）。
func TestMalformedJSONDoesNotPanic(t *testing.T) {
	p, _ := collect()
	p.ProcessPayload("{not json")
	p.ProcessPayload("")
	p.ProcessPayload(`{"choices":[]}`)
	p.Finish()
	// 无 panic 即通过
}

// 反证 G：hint 只在参数首次可解析时触发一次。
func TestToolCallHintFiresOnce(t *testing.T) {
	var hints []string
	h := Handler{
		OnToolCallHint: func(name string, partial map[string]any) {
			hints = append(hints, name)
		},
	}
	p := NewParser(h)

	// 第一次：参数可解析 → 触发
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"read_file","arguments":"{}"}}]}}]}`)
	// 第二次：参数追加（仍可解析）→ 不应再触发
	p.ProcessPayload(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" "}}]}}]}`)
	p.Finish()

	if len(hints) != 1 {
		t.Errorf("hint 应只触发一次，实际 %d 次：%v", len(hints), hints)
	}
	if len(hints) > 0 && hints[0] != "read_file" {
		t.Errorf("hint 名称错误：%s", hints[0])
	}
}

var _ = json.Marshal
