// Package sse 实现 OpenAI 兼容的 SSE 流式响应解析。
//
// 对账 src/api/openai-client.ts 的 processDelta / flushToolCalls /
// resolveToolCallIndex。三个关键语义（均有真实 oracle 覆盖）：
//
//  1. **通道单调性**：content 开始后到达的 reasoning_content 是协议异常，
//     重归类为 text（而非折叠进 thinking）——保证结论留在可见回复里。
//  2. **两阶段 flush**：finish_reason 时先 flush 一次，参数未成合法 JSON 的
//     条目**留在缓冲**（GLM-5.2 会在 finish_reason 之后补发参数分片）；
//     流结束时 final flush，仍不可解析的产出 argsTruncated=true（拒绝执行
//     半个参数的命令，比失败更安全）。
//  3. **续块归属**：无 index 的续块按 id 匹配 → 唯一打开缓冲 → 否则**丢弃**。
//     绝不能退化为 `index ?? 0`：那会把续块嫁接到**另一个工具**上，
//     同时污染两者（oh-my-pi/384919c7 事故）。
package sse

import (
	"encoding/json"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// Event 是解析过程中产出的事件。
type Event struct {
	Kind string // text | thinking | block | stop

	// text/thinking 的增量
	Value string

	// block 字段
	BlockType     string // text | thinking | tool_use
	Text          string
	ToolID        string
	ToolName      string
	ToolInput     map[string]any
	ArgsTruncated bool

	// stop 字段
	StopReason string
	Usage      contract.Usage
}

// Handler 接收解析事件。任一字段为 nil 表示该通道不关心。
type Handler struct {
	OnTextDelta     func(string)
	OnThinkingDelta func(string)
	OnContentBlock  func(Event)
	OnStopReason    func(reason string, usage contract.Usage)
	OnToolCallDelta func()
	OnToolCallHint  func(name string, partial map[string]any)
}

// toolBuf 是一个工具调用的累积缓冲。
type toolBuf struct {
	id        string
	typ       string
	name      string
	arguments string
}

// Parser 解析 SSE 流。零值不可用，请用 NewParser。
type Parser struct {
	handler Handler

	contentStarted bool
	textAccum      strings.Builder
	// reasonAccum 累积 thinking 文本，用于流结束时产出 thinking 块。
	// 注意：contentStarted 之后到达的 reasoning 走 textAccum（通道单调性），
	// 不进这里。
	reasonAccum    strings.Builder
	toolCallBuffer map[int]*toolBuf
	hintFired      map[int]bool
	pendingStop    string
	hasPendingStop bool
}

// NewParser 创建解析器。
func NewParser(h Handler) *Parser {
	return &Parser{
		handler:        h,
		toolCallBuffer: make(map[int]*toolBuf),
		hintFired:      make(map[int]bool),
	}
}

// Feed 喂入一个 SSE 文本分片。跨片不完整的行会被缓冲。
func (p *Parser) Feed(chunk string) {
	// 调用方应通过 FeedBytes 或自行按行切分；此处保留整块入口以简化 API。
	p.feedRaw(chunk)
}

// feedRaw 处理一段原始 SSE 文本（可能含多行、可能行被截断）。
//
// 返回是否遇到 [DONE]。
func (p *Parser) feedRaw(s string) bool {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "data:") {
			continue // 空行与注释行（`:` 心跳）不算数据事件
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "[DONE]" {
			return true
		}
		p.ProcessPayload(payload)
	}
	return false
}

// ProcessPayload 处理一条 `data:` 后的 JSON 负载。
//
// 导出以便直接单测（对齐 TS 的 processDelta 暴露给测试）。
func (p *Parser) ProcessPayload(payload string) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens          *int `json:"prompt_tokens"`
			CompletionTokens      *int `json:"completion_tokens"`
			PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens"`
			PromptTokensDetails   *struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens *int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return // JSON 解码失败可恢复（对齐 TS 的 catch { return }）
	}

	// ── usage-only 块（无 choices）──
	if chunk.Usage != nil && len(chunk.Choices) == 0 {
		stop := "end_turn"
		if p.hasPendingStop {
			stop = p.pendingStop
		}
		p.pendingStop = ""
		p.hasPendingStop = false
		if p.handler.OnStopReason != nil {
			p.handler.OnStopReason(mapFinishReason(stop), p.usageFrom(chunk.Usage))
		}
		return
	}

	if len(chunk.Choices) == 0 {
		return
	}
	choice := chunk.Choices[0]
	delta := choice.Delta

	// ── 通道单调性：content 已开始后到的 reasoning_content 重归类为 text ──
	if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
		if p.contentStarted {
			p.textAccum.WriteString(*delta.ReasoningContent)
			if p.handler.OnTextDelta != nil {
				p.handler.OnTextDelta(*delta.ReasoningContent)
			}
		} else {
			p.reasonAccum.WriteString(*delta.ReasoningContent)
			if p.handler.OnThinkingDelta != nil {
				p.handler.OnThinkingDelta(*delta.ReasoningContent)
			}
		}
	}

	if delta.Content != nil && *delta.Content != "" {
		p.contentStarted = true
		p.textAccum.WriteString(*delta.Content)
		if p.handler.OnTextDelta != nil {
			p.handler.OnTextDelta(*delta.Content)
		}
	}

	// ── 工具调用分片 ──
	for _, tc := range delta.ToolCalls {
		if p.handler.OnToolCallDelta != nil {
			p.handler.OnToolCallDelta()
		}
		idx, ok := p.resolveToolCallIndex(tc.Index, tc.ID)
		if !ok {
			continue // 歧义 → 丢弃（fail-safe）
		}
		buf, exists := p.toolCallBuffer[idx]
		if !exists {
			buf = &toolBuf{}
			p.toolCallBuffer[idx] = buf
		}
		if tc.ID != "" {
			buf.id = tc.ID
		}
		if tc.Type != "" {
			buf.typ = tc.Type
		}
		if tc.Function.Name != "" {
			buf.name += tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			buf.arguments += tc.Function.Arguments
		}

		// 投机预热提示：名字与参数都可解析时触发一次
		if p.handler.OnToolCallHint != nil && buf.name != "" && !p.hintFired[idx] {
			var partial map[string]any
			if json.Unmarshal([]byte(buf.arguments), &partial) == nil {
				p.hintFired[idx] = true
				p.handler.OnToolCallHint(buf.name, partial)
			}
		}
	}

	// ── finish_reason：先 flush，再缓存 stop reason ──
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		p.FlushToolCalls(false)
		p.pendingStop = *choice.FinishReason
		p.hasPendingStop = true
	}

	// ── usage 与 finish_reason 同块（DeepSeek 式）：立即产出 stop ──
	// 必须在 flushToolCalls 之后（tool_use 块先出）且 pendingStop 已设置。
	if chunk.Usage != nil && p.hasPendingStop {
		stop := p.pendingStop
		p.pendingStop = ""
		p.hasPendingStop = false
		if p.handler.OnStopReason != nil {
			p.handler.OnStopReason(mapFinishReason(stop), p.usageFrom(chunk.Usage))
		}
	}
}

// Finish 在流结束时调用：final flush + 产出累积的 thinking/text 块 +
// 补发未产出的 stop reason。
func (p *Parser) Finish() {
	p.FlushToolCalls(true)

	if p.handler.OnContentBlock != nil {
		// thinking 块：TS 侧单独累积 reasoningAccum 并在结束时产出。
		// 本实现把 thinking 累积在 textAccum 之外的独立路径（见 reasonAccum）。
		if p.reasonAccum.Len() > 0 {
			p.handler.OnContentBlock(Event{
				Kind:      "block",
				BlockType: "thinking",
				Text:      p.reasonAccum.String(),
			})
		}
		if p.textAccum.Len() > 0 {
			p.handler.OnContentBlock(Event{
				Kind:      "block",
				BlockType: "text",
				Text:      p.textAccum.String(),
			})
		}
	}
	p.textAccum.Reset()
	p.reasonAccum.Reset()
	p.contentStarted = false

	// 无 usage 块时补发 stop reason
	if p.hasPendingStop {
		if p.handler.OnStopReason != nil {
			p.handler.OnStopReason(mapFinishReason(p.pendingStop), contract.Usage{})
		}
		p.pendingStop = ""
		p.hasPendingStop = false
	}
}

// resolveToolCallIndex 决定分片归属的缓冲槽位。
//
// 返回 (index, ok)；ok=false 表示**丢弃**该分片。
//
// 规则（对齐 TS 的 resolveToolCallIndex）：
//  1. 有 index → 直接使用
//  2. 有 id 且匹配已开缓冲 → 归属该槽
//  3. 恰有一个缓冲打开 → 归属它（单调用续块的常见情形）
//  4. 无缓冲打开 → 归 0（退化情形，保持历史行为）
//  5. 多个缓冲打开且无身份 → **丢弃**（宁可一个调用 JSON 不完整，
//     也不能嫁接到另一个工具上污染两者）
func (p *Parser) resolveToolCallIndex(index *int, id string) (int, bool) {
	if index != nil {
		return *index, true
	}
	if id != "" {
		for idx, buf := range p.toolCallBuffer {
			if buf.id == id {
				return idx, true
			}
		}
	}
	if len(p.toolCallBuffer) == 1 {
		for idx := range p.toolCallBuffer {
			return idx, true
		}
	}
	if len(p.toolCallBuffer) == 0 {
		return 0, true
	}
	return 0, false // 歧义 → 丢弃
}

// FlushToolCalls 产出缓冲中的 tool_use 块。
//
// final=false（finish_reason 时）：参数不可解析的条目**保留**在缓冲，
// 等后续分片补全。final=true（流结束）：仍不可解析的产出
// ArgsTruncated=true + 空 input，并**移除**该条目。
//
// 只移除已产出的条目——绝不整体清空缓冲，使延后的条目能活到 final flush。
//
// **顺序**：按槽位索引升序产出。TS 用 Map 保插入序；Go 的 map 迭代无序，
// 若直接 range 会让并行工具块的顺序随机化，模型看到的 tool_use 顺序与请求
// 不一致（且每次运行不同 → 非确定性）。
func (p *Parser) FlushToolCalls(final bool) {
	for _, idx := range p.sortedIndices() {
		buf, exists := p.toolCallBuffer[idx]
		if !exists {
			continue
		}
		if buf.id == "" || buf.name == "" {
			// 头部（id/name）尚未出现，final 时也不可能补全
			if final {
				delete(p.toolCallBuffer, idx)
			}
			continue
		}
		parsed, ok := tryParseToolArguments(buf.arguments)
		if !ok {
			if !final {
				continue // 延后到 final flush
			}
			// final 仍不可解析：响亮暴露，而非静默喂 {} 给工具
			if p.handler.OnContentBlock != nil {
				p.handler.OnContentBlock(Event{
					Kind:          "block",
					BlockType:     "tool_use",
					ToolID:        buf.id,
					ToolName:      buf.name,
					ToolInput:     map[string]any{},
					ArgsTruncated: true,
				})
			}
			delete(p.toolCallBuffer, idx)
			continue
		}
		if p.handler.OnContentBlock != nil {
			p.handler.OnContentBlock(Event{
				Kind:      "block",
				BlockType: "tool_use",
				ToolID:    buf.id,
				ToolName:  buf.name,
				ToolInput: parsed,
			})
		}
		delete(p.toolCallBuffer, idx)
	}
}

// sortedIndices 返回当前缓冲槽位的升序索引。
func (p *Parser) sortedIndices() []int {
	out := make([]int, 0, len(p.toolCallBuffer))
	for idx := range p.toolCallBuffer {
		out = append(out, idx)
	}
	// 槽位数通常 <10，插入排序足够且无依赖
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// tryParseToolArguments 解析工具参数。
//
// 空字符串视为**不可解析**（对齐 TS 的 tryParseToolArguments：空串返回 null），
// 因为「参数还没开始到达」与「参数是空对象」语义不同——前者应等待。
func tryParseToolArguments(s string) (map[string]any, bool) {
	if s == "" {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	return out, true
}

// mapFinishReason 把提供商的 finish_reason 映射为规范化的 stop reason。
func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "insufficient_system_resource":
		return "end_turn" // DeepSeek 特有
	default:
		return "end_turn"
	}
}

// usageFrom 把原始 usage 归一化为 contract.Usage。
func (p *Parser) usageFrom(u *struct {
	PromptTokens          *int `json:"prompt_tokens"`
	CompletionTokens      *int `json:"completion_tokens"`
	PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails   *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}) contract.Usage {
	deref := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	// cache read 优先级：prompt_cache_hit_tokens → prompt_tokens_details.cached_tokens
	cacheRead := deref(u.PromptCacheHitTokens)
	if cacheRead == 0 && u.PromptTokensDetails != nil {
		cacheRead = deref(u.PromptTokensDetails.CachedTokens)
	}
	out := contract.Usage{
		InputTokens:              deref(u.PromptTokens),
		OutputTokens:             deref(u.CompletionTokens),
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: deref(u.PromptCacheMissTokens),
	}
	if u.CompletionTokensDetails != nil && u.CompletionTokensDetails.ReasoningTokens != nil {
		rt := *u.CompletionTokensDetails.ReasoningTokens
		out.ReasoningTokens = &rt
	}
	return out
}
