// Package api 实现模型接入层。
//
// provider.go 对账 TS 版 src/api/provider.ts——描述「某提供商支持什么、请求/响应
// 如何适配」，让共享的客户端无需硬编码分支即可处理差异。
package api

import "github.com/kalandramo/tianshu/go/internal/contract"

// Usage 是 contract.Usage 的别名，避免本包调用方重复 import 契约包。
type Usage = contract.Usage

// ThinkingBlockType 是请求体中 thinking 块的类型。
type ThinkingBlockType string

const (
	// ThinkingEnabled = {thinking:{type:'enabled'}}（DeepSeek、GLM、MiMo、Claude）
	ThinkingEnabled ThinkingBlockType = "enabled"
	// ThinkingAdaptive = {thinking:{type:'adaptive'}}（MiniMax）
	ThinkingAdaptive ThinkingBlockType = "adaptive"
	// ThinkingNone = 不发 thinking 块，改用 reasoning_effort 参数
	// （OpenAI、Codex、Kimi）
	ThinkingNone ThinkingBlockType = "none"
)

// EffortFormat 是请求中 effort / reasoning 控制的格式。
type EffortFormat string

const (
	EffortReasoningEffort EffortFormat = "reasoning_effort"
	EffortOutputConfig    EffortFormat = "output_config"
	EffortNone            EffortFormat = "none"
)

// PrefixCacheStrategy 是提供商的缓存策略。
type PrefixCacheStrategy string

const (
	// CacheDeepSeekNative = DeepSeek 的透明精确前缀缓存（无需 cache_control）
	CacheDeepSeekNative PrefixCacheStrategy = "deepseek-native"
	// CacheAnthropicControl = Anthropic 式显式 cache_control 断点
	CacheAnthropicControl PrefixCacheStrategy = "anthropic-cache-control"
	// CacheNone = 无前缀缓存，跳过缓存指纹计算
	CacheNone PrefixCacheStrategy = "none"
)

// UsageMapper 把提供商的原始 usage 字段归一化为标准 Usage。
type UsageMapper func(raw map[string]any) Usage

// Capabilities 描述提供商能力。
type Capabilities struct {
	SupportsThinking bool
	// ThinkingBlockType 见 ThinkingBlockType 常量。
	ThinkingBlockType ThinkingBlockType
	// ReasoningSplit = 提供商把推理分离到 `reasoning_content` 字段（MiniMax）。
	ReasoningSplit bool
	// ThinkingBudgetField 是 thinking 块内承载预算的字段名（Claude: 'budget_tokens'）。
	ThinkingBudgetField string
	// EffortCap 是逐提供商的上限——超出此上限的值被钳制
	// （Codex: max→xhigh，Kimi: max→high）。
	EffortCap map[string]string
	// PreservedThinkingProtocol = DeepSeek 保留式思考线协议：assistant 的
	// tool-call 回合必须回显 `reasoning_content`，且适用中文思考系统后缀。
	// 仅由线格式派生自 DeepSeek 的提供商声明（DeepSeek、MiMo），**不**由
	// 仅仅共享 deepseek-native 前缀缓存策略的提供商声明（GLM/longcat/
	// siliconflow 有独立的推理实现）。
	PreservedThinkingProtocol bool
	SupportsCacheControl      bool
	// StripParams 是发送前需剥离的顶层请求参数。
	StripParams []string
	// HasToolJSONInContentBug = 提供商有已知缺陷：工具 JSON 出现在 text content 中。
	HasToolJSONInContentBug bool
	EffortFormat            EffortFormat
	PrefixCacheStrategy     PrefixCacheStrategy
	// SupportsResponseFormat = 支持 `response_format: {type:'json_object'}`
	// 强制 JSON 输出。worker 会话用它消除自由文本解析失败
	// （DeepSeek/GLM/OpenAI 兼容支持；部分提供商拒绝）。
	SupportsResponseFormat bool
	// MapUsage 可选：归一化原始 usage 字段。
	MapUsage UsageMapper
}

// CapabilitiesOverride 是一层能力覆盖（用户配置或模型级）。
//
// 全部字段用指针：nil = 「未声明」，穿透到前一层；显式值（含 false / "none"）
// 覆盖前层。
//
// **例外**：StripParams 用 []string，空切片与未声明同义（空数组不覆盖前层）。
// 原因见 applyOverrides 内的注释——这是与 TS 版对齐的必要语义。
type CapabilitiesOverride struct {
	// 遗留字段
	CacheControl *bool
	StripParams  []string
	ToolJSONBug  *bool
	PrefixCache  *PrefixCacheStrategy

	// 思考相关字段——直接赋值；'none' 是合法显式值
	ThinkingBlock             *ThinkingBlockType
	EffortFormat              *EffortFormat
	EffortCap                 map[string]string
	ReasoningSplit            *bool
	ThinkingBudgetField       *string
	PreservedThinkingProtocol *bool
}

// defaultCapabilities 是未知提供商的兜底（对应 TS 的 DEFAULT_CAPABILITIES）。
var defaultCapabilities = Capabilities{
	SupportsThinking:        false,
	ThinkingBlockType:       ThinkingNone,
	SupportsCacheControl:    true,
	StripParams:             nil,
	HasToolJSONInContentBug: false,
	EffortFormat:            EffortNone,
	PrefixCacheStrategy:     CacheNone,
	SupportsResponseFormat:  false,
}

// deepSeekCapabilities 对应 TS 的 DEEPSEEK_CAPABILITIES。
var deepSeekCapabilities = Capabilities{
	SupportsThinking:          true,
	ThinkingBlockType:         ThinkingEnabled,
	PreservedThinkingProtocol: true,
	SupportsCacheControl:      false,
	StripParams:               []string{"top_k", "metadata", "service_tier", "cache_control"},
	HasToolJSONInContentBug:   true,
	EffortFormat:              EffortReasoningEffort,
	PrefixCacheStrategy:       CacheDeepSeekNative,
	SupportsResponseFormat:    true,
	MapUsage:                  MapDeepSeekUsage,
}

// wellKnownDefaults 是已知提供商的默认能力（对应 TS 的 WELL_KNOWN_DEFAULTS）。
//
// 新增提供商只需在此登记，无需改动其他代码；配置级能力可覆盖这些默认值。
var wellKnownDefaults = map[string]Capabilities{
	"deepseek": deepSeekCapabilities,
	"kimi": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	"glm": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheDeepSeekNative,
		SupportsResponseFormat:  true,
		MapUsage:                MapDeepSeekUsage,
	},
	"minimax": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingAdaptive,
		ReasoningSplit:          true,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortNone,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	"mimo": {
		SupportsThinking:          true,
		ThinkingBlockType:         ThinkingEnabled,
		PreservedThinkingProtocol: true,
		SupportsCacheControl:      false,
		StripParams:               []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug:   false,
		EffortFormat:              EffortNone,
		PrefixCacheStrategy:       CacheNone,
		SupportsResponseFormat:    false,
	},
	"mimo-api": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortNone,
		PrefixCacheStrategy:     CacheDeepSeekNative,
		SupportsResponseFormat:  false,
	},
	// opencode-go：上游不认 thinking 块（默认返回 reasoning_content），但接受
	// reasoning_effort——曾配成 EffortNone 导致用户的档位选择被静默吞掉。
	// 天枢的职责是别把它丢掉。
	"opencode-go": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	"openai": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    true,
		StripParams:             nil,
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  true,
	},
	"codex": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		EffortCap:               map[string]string{"max": "xhigh"},
		SupportsCacheControl:    true,
		StripParams:             nil,
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  true,
	},
	"claude": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		ThinkingBudgetField:     "budget_tokens",
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	// LongCat 官方文档只支持 model/messages/stream/max_tokens/temperature/top_p——
	// 无 response_format（json-mode 修复不可用），无 cache_control 断点
	// （服务端隐式前缀缓存，命中免费）。显式登记以免行为骑在 defaultCapabilities
	// 的兜底上（session 2c1186f5 scout 事故）。
	"longcat": {
		SupportsThinking:        false,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortNone,
		PrefixCacheStrategy:     CacheDeepSeekNative,
		SupportsResponseFormat:  false,
		MapUsage:                MapDeepSeekUsage,
	},
	"ccswitch": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	"siliconflow": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheDeepSeekNative,
		SupportsResponseFormat:  false,
		MapUsage:                MapDeepSeekUsage,
	},
	"dashscope": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingEnabled,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  true,
	},
	"openrouter": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
	"relay": {
		SupportsThinking:        true,
		ThinkingBlockType:       ThinkingNone,
		SupportsCacheControl:    false,
		StripParams:             []string{"top_k", "metadata", "service_tier", "cache_control"},
		HasToolJSONInContentBug: false,
		EffortFormat:            EffortReasoningEffort,
		PrefixCacheStrategy:     CacheNone,
		SupportsResponseFormat:  false,
	},
}

// MapDeepSeekUsage 把 DeepSeek 的 usage 字段（原生与 Anthropic 兼容两种格式）
// 归一化为标准 Usage。
//
// 字段来源优先级（与 TS 版一致）：
//
//	input_tokens  ← prompt_tokens | input_tokens
//	output_tokens ← completion_tokens | output_tokens
//	cache_read    ← prompt_cache_hit_tokens | cache_read_input_tokens
//	                | prompt_tokens_details.cached_tokens
//	cache_creation ← prompt_cache_miss_tokens | cache_creation_input_tokens
func MapDeepSeekUsage(raw map[string]any) Usage {
	return Usage{
		InputTokens:              firstInt(raw, "prompt_tokens", "input_tokens"),
		OutputTokens:             firstInt(raw, "completion_tokens", "output_tokens"),
		CacheReadInputTokens:     firstInt(raw, "prompt_cache_hit_tokens", "cache_read_input_tokens", "prompt_tokens_details.cached_tokens"),
		CacheCreationInputTokens: firstInt(raw, "prompt_cache_miss_tokens", "cache_creation_input_tokens"),
	}
}

// firstInt 按顺序返回第一个存在且可转成整数的键值，全都不存在则返回 0。
// 支持点号路径（如 "prompt_tokens_details.cached_tokens"）。
func firstInt(raw map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := lookupPath(raw, k); ok {
			if n, ok := toInt(v); ok {
				return n
			}
		}
	}
	return 0
}

func lookupPath(m map[string]any, path string) (any, bool) {
	cur := any(m)
	for _, seg := range splitPath(path) {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return append(out, p[start:])
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	default:
		return 0, false
	}
}

// ResolveCapabilities 按名称解析提供商能力，并与可选的配置级 / 模型级覆盖合并。
//
// 合并顺序（后者胜）：
//  1. wellKnownDefaults[providerName]（未知提供商回退 defaultCapabilities）
//  2. providerOverrides（用户配置 / 预设中的 provider.capabilities）
//  3. modelOverrides（provider.models[i].capabilities）
//
// 全部覆盖字段可选：省略则穿透到前一层。显式值（含 'none' / false）胜出。
// 例外：StripParams 空切片与未声明同义（不覆盖前层）。
func ResolveCapabilities(providerName string, providerOverrides, modelOverrides *CapabilitiesOverride) Capabilities {
	base, ok := wellKnownDefaults[providerName]
	if !ok {
		base = defaultCapabilities
	}
	// 浅拷贝——EffortCap 需深拷（applyOverrides 会赋新对象），StripParams 亦需
	// 避免别名共享导致的跨调用污染。
	base.StripParams = cloneStrings(base.StripParams)
	if base.EffortCap != nil {
		ec := make(map[string]string, len(base.EffortCap))
		for k, v := range base.EffortCap {
			ec[k] = v
		}
		base.EffortCap = ec
	}

	applyOverrides(&base, providerOverrides)
	applyOverrides(&base, modelOverrides)
	return base
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// applyOverrides 把一层覆盖就地应用到 base 上。nil 字段视为「未声明」，
// 穿透到 base（base 通常是 wellKnownDefaults[name] 或前一层的结果）。
//
// 同时从用户声明的思考字段派生 SupportsThinking：
//   - ThinkingBlock ∈ {enabled, adaptive} 或 EffortFormat ∈ {reasoning_effort, output_config}
//     → SupportsThinking = true
//   - ThinkingBlock == none 且 EffortFormat == none
//     → SupportsThinking = false
//   - 其余：保留 base 值（无信号）
func applyOverrides(base *Capabilities, ov *CapabilitiesOverride) {
	if ov == nil {
		return
	}

	// ── 遗留字段 ──
	if ov.CacheControl != nil {
		base.SupportsCacheControl = *ov.CacheControl
	}
	// 空切片与「未声明」同义（恢复旧模型语义）。
	//
	// 历史教训：曾把 [] 改为「显式清空」，但旧模型里 [] 一直是「无意见」的占位
	// （预设/快照里全是 []，旧判定 length>0 才生效），新语义会让 WELL_KNOWN 剥离
	// 清单对存量配置与内置预设静默失效（top_k/cache_control 等重新进请求）。
	// 「strip-nothing」在旧模型本就无法表达，恢复 length>0 判定无回归。
	if len(ov.StripParams) > 0 {
		base.StripParams = cloneStrings(ov.StripParams)
	}
	if ov.ToolJSONBug != nil {
		base.HasToolJSONInContentBug = *ov.ToolJSONBug
	}
	if ov.PrefixCache != nil {
		base.PrefixCacheStrategy = *ov.PrefixCache
	}

	// ── 思考字段（直接赋值；'none' 是合法显式值） ──
	if ov.ThinkingBlock != nil {
		base.ThinkingBlockType = *ov.ThinkingBlock
	}
	if ov.EffortFormat != nil {
		base.EffortFormat = *ov.EffortFormat
	}
	if ov.EffortCap != nil {
		ec := make(map[string]string, len(ov.EffortCap))
		for k, v := range ov.EffortCap {
			ec[k] = v
		}
		base.EffortCap = ec
	}
	if ov.ReasoningSplit != nil {
		base.ReasoningSplit = *ov.ReasoningSplit
	}
	if ov.ThinkingBudgetField != nil {
		base.ThinkingBudgetField = *ov.ThinkingBudgetField
	}
	if ov.PreservedThinkingProtocol != nil {
		base.PreservedThinkingProtocol = *ov.PreservedThinkingProtocol
	}

	// ── 从声明的思考能力派生 SupportsThinking ──
	declaresThinking := (ov.ThinkingBlock != nil &&
		(*ov.ThinkingBlock == ThinkingEnabled || *ov.ThinkingBlock == ThinkingAdaptive)) ||
		(ov.EffortFormat != nil &&
			(*ov.EffortFormat == EffortReasoningEffort || *ov.EffortFormat == EffortOutputConfig))
	declaresNoThinking := ov.ThinkingBlock != nil && ov.EffortFormat != nil &&
		*ov.ThinkingBlock == ThinkingNone && *ov.EffortFormat == EffortNone

	if declaresThinking {
		base.SupportsThinking = true
	} else if declaresNoThinking {
		base.SupportsThinking = false
	}
}
