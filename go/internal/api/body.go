// Package api —— 请求体构造。
//
// buddyBodyBuilder 对账 src/api/openai-client.ts:471-560 的裸 body 构造。
// 字段顺序即 wire 顺序（JSON.stringify 保插入序），因此本文件的 Set 调用
// **必须**与 TS 源码的赋值序逐字对应——顺序错误会让前缀缓存静默碎裂。
//
// 验证方式：go/testdata/wire 的 oracle 由真实 OpenAIClient 经 mock fetch
// 捕获实际发送字节；任何顺序偏差会被 TestWireGoldenParity 捕获。
package api

import (
	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// ChatRequest 是一次模型调用请求（对应 TS 的 OaiChatRequest）。
type ChatRequest struct {
	Model    string
	Messages []*wire.OrderedMap
	// Tools 为 nil 或空时不出现在 body 中（对齐 TS 的 `if (request.tools && request.tools.length > 0)`）。
	Tools      []*wire.OrderedMap
	ToolChoice any
	// MaxTokens 为 nil 时不设（body 用 config 的 maxTokens）。
	MaxTokens *int
	// Temperature 为 nil 时按 config 回退（且 thinking=enabled 时不注入）。
	Temperature *float64
	// ResponseFormat 优先于 config.jsonMode。
	//
	// 类型为 *wire.OrderedMap 而非 map[string]any —— 嵌套键序会影响 wire 字节
	// （map 会被排序，TS 侧保插入序）。实测：TS 产出
	// {"type":"json_schema","name":"x"}，map 会排成 {"name":"x","type":"json_schema"}。
	ResponseFormat *wire.OrderedMap
	// ReasoningEffort 会覆盖 config 的推理档位。
	ReasoningEffort string
}

// ClientConfig 是客户端绑定配置（对应 TS 的 OpenAIClientConfig 中被 body 构造消费的部分）。
type ClientConfig struct {
	Model string
	// MaxTokens 是 config 级默认上限。
	MaxTokens int
	// UseMaxCompletionTokens = 发 max_completion_tokens 而非 max_tokens（MiMo API）。
	UseMaxCompletionTokens bool
	// Unsupported 列出提供商不支持的特性（如 'stream_options'）。
	Unsupported []string
	// JSONMode 是常驻 JSON 输出的回退（request.ResponseFormat 优先）。
	JSONMode bool
	// Temperature 是 config 级采样温度默认值。
	Temperature *float64
	// Thinking 为 'enabled' 时抑制 config.Temperature 的注入，并启用 thinking 块。
	Thinking string
	// ThinkingBlockType 决定 thinking 块形态（enabled/adaptive/none）。
	ThinkingBlockType ThinkingBlockType
	// ThinkingBudgetField 为 'budget_tokens' 时按档位换算预算（Claude）。
	ThinkingBudgetField string
	// ReasoningSplit = 发 reasoning_split: true（MiniMax）。
	ReasoningSplit bool
	// ReasoningEffort 是 config 级推理档位。
	ReasoningEffort string
	// EffortFormat 决定推理控制的编码方式。
	EffortFormat EffortFormat
	// EffortCap 钳制档位值（Codex: max→xhigh）。
	EffortCap map[string]string
	// ProviderName 用于提供商特化分支。
	ProviderName string
	// PreservedThinkingProtocol = 声明保留式思考协议（DeepSeek 派生线格式）。
	// 与 Thinking=enabled 共同决定是否自动追加中文思考后缀。
	PreservedThinkingProtocol bool
	// SystemSuffix 覆盖自动计算的后缀。留空时按 PreservedThinkingProtocol +
	// Thinking 自动派生（对齐 TS 的构造期计算）。非空则直接使用该值
	//（空字符串表示显式禁用后缀，见 SystemSuffixDisabled）。
	SystemSuffix string
	// SystemSuffixDisabled 显式禁用自动后缀（对应 TS 中 systemSuffix 为空的情形）。
	SystemSuffixDisabled bool
}

// ChineseThinkingSuffix 是保留式思考协议下的系统后缀（对齐
// openai-client.ts:396-397 的固定文本）。
//
// 作用：约束模型在思考链中使用中文，且不在回复中输出推理过程。
const ChineseThinkingSuffix = "\n\n请在内部思考链中使用中文进行推理。不要在回复中输出你的推理过程，只输出最终答案或工具调用。"

// resolveSystemSuffix 计算本次请求要追加的系统后缀。
//
// 对齐 TS 的构造期计算（openai-client.ts:396-397）：
//
//	preservedThinkingProtocol && thinking === 'enabled' ? 中文思考后缀 : ''
func (c *ClientConfig) resolveSystemSuffix() string {
	if c.SystemSuffixDisabled {
		return ""
	}
	if c.SystemSuffix != "" {
		return c.SystemSuffix
	}
	if c.PreservedThinkingProtocol && c.Thinking == "enabled" {
		return ChineseThinkingSuffix
	}
	return ""
}

// suppressed 报告 config.Unsupported 是否含该特性。
func (c *ClientConfig) suppressed(name string) bool {
	for _, u := range c.Unsupported {
		if u == name {
			return true
		}
	}
	return false
}

// BuildWireBody 构造请求体，字段序与 src/api/openai-client.ts 逐字对应。
//
// 调用方拿到的是已应用消息变换（reasoning_content 剥离/系统后缀）的 body，
// 可直接 Marshal 发送。
func BuildWireBody(req *ChatRequest, cfg *ClientConfig) *wire.OrderedMap {
	body := wire.NewOrderedMap()

	// ── L474-L476：字面量内三字段。顺序：model → messages → stream ──
	model := req.Model
	if model == "" {
		model = cfg.Model // 空 model 回退到 client 绑定值（侧路调用依赖此行为）
	}
	body.Set("model", model)
	body.Set("messages", transformMessages(req.Messages, cfg))
	body.Set("stream", true)

	// ── L479-L483：max_tokens 分支 ──
	maxTokens := cfg.MaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	if cfg.UseMaxCompletionTokens {
		body.Set("max_completion_tokens", maxTokens)
	} else {
		body.Set("max_tokens", maxTokens)
	}

	// ── L487-L489：stream_options（部分提供商 400 拒绝） ──
	if !cfg.suppressed("stream_options") {
		body.Set("stream_options", wire.NewOrderedMap().Set("include_usage", true))
	}

	// ── L491-L494：tools ──
	if len(req.Tools) > 0 {
		tools := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = t
		}
		body.Set("tools", tools)
		if req.ToolChoice != nil {
			body.Set("tool_choice", req.ToolChoice)
		}
	}

	// ── L500-L504：response_format（request 优先于 config） ──
	if req.ResponseFormat != nil {
		body.Set("response_format", req.ResponseFormat)
	} else if cfg.JSONMode {
		body.Set("response_format", wire.NewOrderedMap().Set("type", "json_object"))
	}
	// ── L506-L511：temperature。thinking=enabled 时**不注入** config 值 —— 多数推理
	// 服务端拒绝调温。request 显式给的值仍生效。 ──
	if req.Temperature != nil {
		body.Set("temperature", *req.Temperature)
	} else if cfg.Temperature != nil && cfg.Thinking != "enabled" {
		body.Set("temperature", *cfg.Temperature)
	}

	// ── L516-L518：reasoning_split（MiniMax 专用） ──
	if cfg.ReasoningSplit {
		body.Set("reasoning_split", true)
	}

	// ── L527-L551：thinking / reasoning_effort 分派（能力驱动） ──
	blockType := cfg.ThinkingBlockType
	if blockType == "" {
		blockType = ThinkingNone
	}

	if cfg.Thinking == "enabled" {
		if blockType != ThinkingNone {
			thinking := wire.NewOrderedMap().Set("type", string(blockType))

			// thinking 预算（Claude 的 budget_tokens）：thinking 块内编码档位。
			if cfg.ThinkingBudgetField == "budget_tokens" && cfg.ReasoningEffort != "" {
				thinking.Set("budget_tokens", budgetFor(cfg.ReasoningEffort, cfg.MaxTokens))
			}
			body.Set("thinking", thinking)

			// DeepSeek 式：thinking 块与 reasoning_effort **并存**。
			// 用块内编码档位的提供商（budget_tokens / adaptive）不需要独立字段。
			if cfg.EffortFormat == EffortReasoningEffort &&
				cfg.ReasoningEffort != "" && cfg.ReasoningEffort != "off" {
				body.Set("reasoning_effort", cfg.ReasoningEffort)
			}
		} else if cfg.EffortFormat != EffortNone {
			// 无 thinking 块的提供商（OpenAI/Codex）用 reasoning_effort，
			// 缺省 medium。
			effort := cfg.ReasoningEffort
			if effort == "" {
				effort = "medium"
			}
			body.Set("reasoning_effort", effort)
		}
	}

	// request 级 reasoning_effort 覆盖（L553-L555）。
	if req.ReasoningEffort != "" && cfg.EffortFormat != EffortNone {
		body.Set("reasoning_effort", req.ReasoningEffort)
	}

	// ── L557-L562：档位钳制 ──
	if len(cfg.EffortCap) > 0 {
		if cur, ok := body.Get("reasoning_effort"); ok {
			if s, isStr := cur.(string); isStr {
				if capped, found := cfg.EffortCap[s]; found {
					body.Set("reasoning_effort", capped)
				}
			}
		}
	}

	// ── L571-L578：系统后缀（构造期派生）。**copy-on-write，绝不原地拼接** ——
	// 消息对象与调用方的 request.Messages 共享引用，同一 request 可能重入
	// stream()（投机解码复用主请求消息；FallbackStreamClient 故障转移重放）。
	// 原地改会双写后缀 → system 字节中途变化 → 该请求完全缓存 miss
	//（2026-07-06 wireDiverged idx 0 事故）。
	if suffix := cfg.resolveSystemSuffix(); suffix != "" {
		applySystemSuffix(body, suffix)
	}

	return body
}

// budgetFor 按档位换算 thinking 预算（对齐 TS 的 budgetMap）。
func budgetFor(effort string, maxTokens int) int {
	switch effort {
	case "max":
		return maxTokens
	case "high":
		return maxTokens * 6 / 10
	case "medium":
		return maxTokens * 3 / 10
	case "low":
		return 8192
	case "off":
		return 0
	default:
		return maxTokens * 6 / 10 // 缺省按 high
	}
}

// transformMessages 应用 reasoning_content 剥离规则。
//
// 规则（对齐 openai-client.ts:438-468）：
//   - 非 assistant 消息原样返回
//   - 保留式思考协议（DeepSeek/MiMo）+ 有 tool_calls：保留 reasoning_content；
//     若该字段缺失则补空串——「缺失」与「存在」改变 wire 字节，会在下一个
//     用户边界击碎前缀缓存（8396ac51 事故：截断与首个无 reasoning 的 assistant 对齐）
//   - 其余情况：剥离 reasoning_content
//   - 剥离后若既无 content 又无 tool_calls，补 content: ""
func transformMessages(msgs []*wire.OrderedMap, cfg *ClientConfig) []any {
	out := make([]any, 0, len(msgs))
	preservedThinking := cfg.Thinking == "enabled" && cfg.preservesThinking()

	for _, m := range msgs {
		role, _ := m.Get("role")
		roleStr, _ := role.(string)
		if roleStr != "assistant" {
			out = append(out, m)
			continue
		}

		// hasToolCalls
		hasToolCalls := false
		if tc, ok := m.Get("tool_calls"); ok {
			if arr, isArr := tc.([]any); isArr && len(arr) > 0 {
				hasToolCalls = true
			}
		}
		hasReasoning := m.Has("reasoning_content")

		if preservedThinking && hasToolCalls {
			// 有 tool_calls：协议要求回显 reasoning_content。
			if !hasReasoning {
				// 缺失则补空串。TS 侧是 `{ ...m, reasoning_content: '' }` ——
				// 展开在前，新键**追加到末尾**（在 tool_calls 之后）。
				// 位置错误会让 wire 字节不同 → 缓存碎裂。
				clone := m.Clone()
				clone.Set("reasoning_content", "")
				out = append(out, clone)
				continue
			}
			out = append(out, m)
			continue
		}

		if !hasReasoning {
			out = append(out, m)
			continue
		}

		// 剥离 reasoning_content（copy-on-write）
		clone := m.Clone()
		clone.Delete("reasoning_content")

		// 剥离后 DeepSeek 要求 assistant 消息有 content 或 tool_calls
		if !clone.Has("content") && !hasToolCalls {
			clone.Set("content", "")
		}
		out = append(out, clone)
	}
	return out
}

// preservesThinking 报告是否启用保留式思考协议。
//
// 判据是配置字段 PreservedThinkingProtocol（对齐 TS 的
// `Boolean(this.config.preservedThinkingProtocol)`），**不是** provider 名——
// 同一 provider 的不同模型可有不同能力，且预设可覆盖。
func (c *ClientConfig) preservesThinking() bool {
	return c.PreservedThinkingProtocol
}

// applySystemSuffix 把后缀追加到首条 system 消息的 content（copy-on-write）。
func applySystemSuffix(body *wire.OrderedMap, suffix string) {
	msgsAny, ok := body.Get("messages")
	if !ok {
		return
	}
	msgs, ok := msgsAny.([]any)
	if !ok {
		return
	}
	for i, m := range msgs {
		om, ok := m.(*wire.OrderedMap)
		if !ok {
			continue
		}
		if role, _ := om.Get("role"); role != "system" {
			continue
		}
		content, _ := om.Get("content")
		contentStr, ok := content.(string)
		if !ok {
			continue // 非字符串 content（多模态数组）不追加，对齐 TS 的 typeof 检查
		}
		clone := om.Clone()
		clone.Set("content", contentStr+suffix)
		msgs[i] = clone
		return
	}
}

// MarshalBody 序列化请求体（保插入序）。
func MarshalBody(body *wire.OrderedMap) string {
	return body.Marshal()
}
