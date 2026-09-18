package api

import (
	"reflect"
	"testing"
)

// 三层合并顺序：wellKnown → provider 覆盖 → model 覆盖（后者胜）。
func TestResolveCapabilitiesLayerOrder(t *testing.T) {
	// deepseek 基线：EffortFormat = reasoning_effort
	base := ResolveCapabilities("deepseek", nil, nil)
	if base.EffortFormat != EffortReasoningEffort {
		t.Fatalf("deepseek 基线 EffortFormat = %q, want %q", base.EffortFormat, EffortReasoningEffort)
	}

	// provider 层覆盖
	providerOv := &CapabilitiesOverride{EffortFormat: ptr(EffortNone)}
	got := ResolveCapabilities("deepseek", providerOv, nil)
	if got.EffortFormat != EffortNone {
		t.Errorf("provider 层未生效：%q", got.EffortFormat)
	}

	// model 层覆盖 provider 层（后者胜）
	modelOv := &CapabilitiesOverride{EffortFormat: ptr(EffortOutputConfig)}
	got = ResolveCapabilities("deepseek", providerOv, modelOv)
	if got.EffortFormat != EffortOutputConfig {
		t.Errorf("model 层未覆盖 provider 层：%q", got.EffortFormat)
	}
}

// 未声明字段必须穿透到前一层（nil ≠ 零值）。
func TestUndeclaredFieldsFallThrough(t *testing.T) {
	// 只覆盖 EffortFormat，其余应保持 deepseek 基线
	got := ResolveCapabilities("deepseek", &CapabilitiesOverride{EffortFormat: ptr(EffortNone)}, nil)

	if got.PrefixCacheStrategy != CacheDeepSeekNative {
		t.Errorf("未声明字段被清零：PrefixCacheStrategy = %q", got.PrefixCacheStrategy)
	}
	if !got.PreservedThinkingProtocol {
		t.Error("未声明字段被清零：PreservedThinkingProtocol")
	}
	if !got.HasToolJSONInContentBug {
		t.Error("未声明字段被清零：HasToolJSONInContentBug")
	}
	if got.MapUsage == nil {
		t.Error("未声明字段被清零：MapUsage")
	}
}

// 反证：StripParams 空切片必须与「未声明」同义，不得清空前层。
//
// 若把 applyOverrides 的 `len(ov.StripParams) > 0` 改成 `ov.StripParams != nil`，
// 本测试变红——且会让内置预设的剥离清单静默失效（top_k/cache_control 重新进请求）。
func TestStripParamsEmptyIsNoOpinion(t *testing.T) {
	// deepseek 基线含 4 项剥离参数
	base := ResolveCapabilities("deepseek", nil, nil)
	if len(base.StripParams) != 4 {
		t.Fatalf("deepseek 基线 StripParams 应有 4 项，实为 %d: %v", len(base.StripParams), base.StripParams)
	}

	// 空切片覆盖 → 应视为「无意见」，保持基线 4 项
	got := ResolveCapabilities("deepseek", &CapabilitiesOverride{StripParams: []string{}}, nil)
	if len(got.StripParams) != 4 {
		t.Errorf("空切片被当成显式清空——剥离清单静默失效\n  got:  %v\n  want: %v",
			got.StripParams, base.StripParams)
	}

	// 非空切片 → 正常覆盖
	got = ResolveCapabilities("deepseek", &CapabilitiesOverride{StripParams: []string{"only_this"}}, nil)
	if !reflect.DeepEqual(got.StripParams, []string{"only_this"}) {
		t.Errorf("非空切片未覆盖：%v", got.StripParams)
	}
}

// SupportsThinking 的派生规则。
func TestSupportsThinkingDerivation(t *testing.T) {
	cases := []struct {
		name string
		ov   *CapabilitiesOverride
		want bool
	}{
		{
			"thinkingBlock=enabled 派生 true",
			&CapabilitiesOverride{ThinkingBlock: ptr(ThinkingEnabled)},
			true,
		},
		{
			"thinkingBlock=adaptive 派生 true",
			&CapabilitiesOverride{ThinkingBlock: ptr(ThinkingAdaptive)},
			true,
		},
		{
			"effortFormat=reasoning_effort 派生 true",
			&CapabilitiesOverride{EffortFormat: ptr(EffortReasoningEffort)},
			true,
		},
		{
			"effortFormat=output_config 派生 true",
			&CapabilitiesOverride{EffortFormat: ptr(EffortOutputConfig)},
			true,
		},
		{
			"block=none 且 effort=none 派生 false",
			&CapabilitiesOverride{ThinkingBlock: ptr(ThinkingNone), EffortFormat: ptr(EffortNone)},
			false,
		},
		{
			// 只有 block=none，无 effort 信号 → 保留基线（longcat 基线 false）
			"仅 block=none 时保留基线",
			&CapabilitiesOverride{ThinkingBlock: ptr(ThinkingNone)},
			false,
		},
		{
			// 只有 effort=reasoning_effort → 派生 true（覆盖 longcat 的 false 基线）
			"仅 effort 声明可翻转基线",
			&CapabilitiesOverride{EffortFormat: ptr(EffortReasoningEffort)},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCapabilities("longcat", tc.ov, nil)
			if got.SupportsThinking != tc.want {
				t.Errorf("SupportsThinking = %v, want %v", got.SupportsThinking, tc.want)
			}
		})
	}
}

// 未知提供商回退默认能力。
func TestUnknownProviderFallsBackToDefault(t *testing.T) {
	got := ResolveCapabilities("no-such-provider-xyz", nil, nil)
	if got.SupportsThinking {
		t.Error("未知提供商不应声明支持思考")
	}
	if got.PrefixCacheStrategy != CacheNone {
		t.Errorf("未知提供商 PrefixCacheStrategy = %q, want none", got.PrefixCacheStrategy)
	}
	if !got.SupportsCacheControl {
		t.Error("未知提供商默认支持 cache_control（DEFAULT_CAPABILITIES 语义）")
	}
}

// 反证：ResolveCapabilities 不得让调用方通过返回的切片污染全局表。
//
// 若去掉 cloneStrings / EffortCap 深拷，本测试变红——且会造成跨会话污染：
// 一个会话改了 StripParams，另一个会话跟着变。
func TestResolveDoesNotMutateGlobalTable(t *testing.T) {
	before := ResolveCapabilities("deepseek", nil, nil)

	// 篡改第一次调用的返回值
	mutated := ResolveCapabilities("deepseek", nil, nil)
	mutated.StripParams[0] = "TAMPERED"

	after := ResolveCapabilities("deepseek", nil, nil)
	if after.StripParams[0] == "TAMPERED" {
		t.Fatalf("全局表被返回值污染：%v", after.StripParams)
	}
	if !reflect.DeepEqual(before.StripParams, after.StripParams) {
		t.Errorf("两次调用结果不一致：\n  before: %v\n  after:  %v", before.StripParams, after.StripParams)
	}

	// EffortCap 同理（codex 有 EffortCap）
	m1 := ResolveCapabilities("codex", nil, nil)
	m1.EffortCap["max"] = "TAMPERED"
	m2 := ResolveCapabilities("codex", nil, nil)
	if m2.EffortCap["max"] == "TAMPERED" {
		t.Fatalf("EffortCap 全局表被污染：%v", m2.EffortCap)
	}
}

// MapDeepSeekUsage 的字段优先级与点号路径。
func TestMapDeepSeekUsage(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want Usage
	}{
		{
			"DeepSeek 原生格式",
			map[string]any{
				"prompt_tokens":            100,
				"completion_tokens":        20,
				"prompt_cache_hit_tokens":  80,
				"prompt_cache_miss_tokens": 20,
			},
			Usage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 80, CacheCreationInputTokens: 20},
		},
		{
			"Anthropic 兼容格式",
			map[string]any{
				"input_tokens":                100,
				"output_tokens":               20,
				"cache_read_input_tokens":     80,
				"cache_creation_input_tokens": 20,
			},
			Usage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 80, CacheCreationInputTokens: 20},
		},
		{
			"嵌套 cached_tokens 路径",
			map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 20,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 55,
				},
			},
			Usage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 55},
		},
		{
			"原生字段优先于兼容字段",
			map[string]any{
				"prompt_tokens":           111,
				"input_tokens":            999,
				"completion_tokens":       22,
				"output_tokens":           888,
				"prompt_cache_hit_tokens": 70,
				"cache_read_input_tokens": 777,
			},
			Usage{InputTokens: 111, OutputTokens: 22, CacheReadInputTokens: 70},
		},
		{
			"空 map 得零值",
			map[string]any{},
			Usage{},
		},
		{
			"浮点数值可转换",
			map[string]any{"prompt_tokens": 100.0, "completion_tokens": 20.0},
			Usage{InputTokens: 100, OutputTokens: 20},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapDeepSeekUsage(tc.raw)
			if got != tc.want {
				t.Errorf("MapDeepSeekUsage 不符\n  got:  %+v\n  want: %+v", got, tc.want)
			}
		})
	}
}

// 关键提供商的基线能力（防止表被误改）。
func TestWellKnownBaselines(t *testing.T) {
	cases := []struct {
		name            string
		provider        string
		wantCacheStrat  PrefixCacheStrategy
		wantToolJSONBug bool
	}{
		{"deepseek 用原生前缀缓存且有 toolJsonBug", "deepseek", CacheDeepSeekNative, true},
		{"glm 共享 deepseek-native 缓存", "glm", CacheDeepSeekNative, false},
		{"longcat 隐式前缀缓存", "longcat", CacheDeepSeekNative, false},
		{"claude 无前缀缓存", "claude", CacheNone, false},
		{"minimax 无前缀缓存", "minimax", CacheNone, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCapabilities(tc.provider, nil, nil)
			if got.PrefixCacheStrategy != tc.wantCacheStrat {
				t.Errorf("PrefixCacheStrategy = %q, want %q", got.PrefixCacheStrategy, tc.wantCacheStrat)
			}
			if got.HasToolJSONInContentBug != tc.wantToolJSONBug {
				t.Errorf("HasToolJSONInContentBug = %v, want %v", got.HasToolJSONInContentBug, tc.wantToolJSONBug)
			}
		})
	}
}

// PreservedThinkingProtocol 只由线格式派生自 DeepSeek 的提供商声明——
// 共享 deepseek-native 缓存策略不等于共享该协议（GLM/longcat 有独立推理）。
func TestPreservedThinkingProtocolOnlyDeepSeekDerived(t *testing.T) {
	shouldHave := []string{"deepseek", "mimo"}
	shouldNotHave := []string{"glm", "longcat", "siliconflow", "kimi", "claude"}

	for _, p := range shouldHave {
		if !ResolveCapabilities(p, nil, nil).PreservedThinkingProtocol {
			t.Errorf("%s 应声明 PreservedThinkingProtocol", p)
		}
	}
	for _, p := range shouldNotHave {
		if ResolveCapabilities(p, nil, nil).PreservedThinkingProtocol {
			t.Errorf("%s 不应声明 PreservedThinkingProtocol（仅共享缓存策略，推理实现独立）", p)
		}
	}
}

func ptr[T any](v T) *T { return &v }
