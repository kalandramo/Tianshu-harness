package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// oracleProvider 是 TS oracle 导出的提供商能力快照（字段名与 gen-oracle.ts 对齐）。
type oracleProvider struct {
	SupportsThinking          bool              `json:"supportsThinking"`
	ThinkingBlockType         string            `json:"thinkingBlockType"`
	ReasoningSplit            bool              `json:"reasoningSplit"`
	ThinkingBudgetField       string            `json:"thinkingBudgetField"`
	PreservedThinkingProtocol bool              `json:"preservedThinkingProtocol"`
	SupportsCacheControl      bool              `json:"supportsCacheControl"`
	StripParams               []string          `json:"stripParams"`
	HasToolJSONInContentBug   bool              `json:"hasToolJsonInContentBug"`
	EffortFormat              string            `json:"effortFormat"`
	PrefixCacheStrategy       string            `json:"prefixCacheStrategy"`
	SupportsResponseFormat    bool              `json:"supportsResponseFormat"`
	EffortCap                 map[string]string `json:"effortCap"`
	HasMapUsage               bool              `json:"hasMapUsage"`
}

type oracleCuts struct {
	ProviderLayer               string   `json:"providerLayer"`
	ModelLayerWins              string   `json:"modelLayerWins"`
	EmptyStripParamsIsNoOpinion []string `json:"emptyStripParamsIsNoOpinion"`
	ExplicitStripParams         []string `json:"explicitStripParams"`
	DeriveThinkingFromBlock     bool     `json:"deriveThinkingFromBlock"`
	DeriveNoThinking            bool     `json:"deriveNoThinking"`
	UndeclaredFallsThrough      struct {
		PrefixCacheStrategy       string `json:"prefixCacheStrategy"`
		PreservedThinkingProtocol bool   `json:"preservedThinkingProtocol"`
	} `json:"undeclaredFallsThrough"`
}

type oracleUsageEntry struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type oracleUsage struct {
	DeepSeekNative  *oracleUsageEntry `json:"deepseekNative"`
	AnthropicCompat *oracleUsageEntry `json:"anthropicCompat"`
	NestedCached    *oracleUsageEntry `json:"nestedCached"`
}

// TestProviderGoldenParity 用真实 TS oracle 输出对账 Go 实现。
//
// 覆盖三类断言：
//  1. 全部已知提供商的基线能力（逐字段）
//  2. 三层合并与覆盖语义（含「空切片无意见」边界）
//  3. usage 归一化的字段优先级
//
// golden 生成：npx tsx go/testdata/provider/gen-oracle.ts
func TestProviderGoldenParity(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "testdata", "provider", "oracle.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取 golden 失败（%s）：%v\n生成命令见本测试注释", goldenPath, err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("解析 golden 失败：%v", err)
	}

	// ── 1. 提供商基线逐字段对账 ──
	for name, rawEntry := range golden {
		if name == "__cuts" || name == "__usage" {
			continue
		}
		var want oracleProvider
		if err := json.Unmarshal(rawEntry, &want); err != nil {
			t.Fatalf("解析 %s 失败：%v", name, err)
		}
		t.Run("provider/"+name, func(t *testing.T) {
			got := ResolveCapabilities(name, nil, nil)

			if got.SupportsThinking != want.SupportsThinking {
				t.Errorf("SupportsThinking = %v, want %v", got.SupportsThinking, want.SupportsThinking)
			}
			if string(got.ThinkingBlockType) != want.ThinkingBlockType {
				t.Errorf("ThinkingBlockType = %q, want %q", got.ThinkingBlockType, want.ThinkingBlockType)
			}
			if got.ReasoningSplit != want.ReasoningSplit {
				t.Errorf("ReasoningSplit = %v, want %v", got.ReasoningSplit, want.ReasoningSplit)
			}
			if got.ThinkingBudgetField != want.ThinkingBudgetField {
				t.Errorf("ThinkingBudgetField = %q, want %q", got.ThinkingBudgetField, want.ThinkingBudgetField)
			}
			if got.PreservedThinkingProtocol != want.PreservedThinkingProtocol {
				t.Errorf("PreservedThinkingProtocol = %v, want %v", got.PreservedThinkingProtocol, want.PreservedThinkingProtocol)
			}
			if got.SupportsCacheControl != want.SupportsCacheControl {
				t.Errorf("SupportsCacheControl = %v, want %v", got.SupportsCacheControl, want.SupportsCacheControl)
			}
			if !sameStrings(got.StripParams, want.StripParams) {
				t.Errorf("StripParams = %v, want %v", got.StripParams, want.StripParams)
			}
			if got.HasToolJSONInContentBug != want.HasToolJSONInContentBug {
				t.Errorf("HasToolJSONInContentBug = %v, want %v", got.HasToolJSONInContentBug, want.HasToolJSONInContentBug)
			}
			if string(got.EffortFormat) != want.EffortFormat {
				t.Errorf("EffortFormat = %q, want %q", got.EffortFormat, want.EffortFormat)
			}
			if string(got.PrefixCacheStrategy) != want.PrefixCacheStrategy {
				t.Errorf("PrefixCacheStrategy = %q, want %q", got.PrefixCacheStrategy, want.PrefixCacheStrategy)
			}
			if got.SupportsResponseFormat != want.SupportsResponseFormat {
				t.Errorf("SupportsResponseFormat = %v, want %v", got.SupportsResponseFormat, want.SupportsResponseFormat)
			}
			if !reflect.DeepEqual(got.EffortCap, want.EffortCap) {
				t.Errorf("EffortCap = %v, want %v", got.EffortCap, want.EffortCap)
			}
			if (got.MapUsage != nil) != want.HasMapUsage {
				t.Errorf("MapUsage 存在性 = %v, want %v", got.MapUsage != nil, want.HasMapUsage)
			}
		})
	}

	// ── 2. 合并与覆盖语义 ──
	t.Run("cuts", func(t *testing.T) {
		var want oracleCuts
		if err := json.Unmarshal(golden["__cuts"], &want); err != nil {
			t.Fatalf("解析 __cuts 失败：%v", err)
		}

		if got := ResolveCapabilities("deepseek", &CapabilitiesOverride{EffortFormat: ptr(EffortFormat(want.ProviderLayer))}, nil); string(got.EffortFormat) != want.ProviderLayer {
			t.Errorf("provider 层覆盖：got %q, want %q", got.EffortFormat, want.ProviderLayer)
		}
		if got := ResolveCapabilities("deepseek",
			&CapabilitiesOverride{EffortFormat: ptr(EffortFormat("none"))},
			&CapabilitiesOverride{EffortFormat: ptr(EffortFormat(want.ModelLayerWins))}); string(got.EffortFormat) != want.ModelLayerWins {
			t.Errorf("model 层应胜出：got %q, want %q", got.EffortFormat, want.ModelLayerWins)
		}
		if got := ResolveCapabilities("deepseek", &CapabilitiesOverride{StripParams: []string{}}, nil); !sameStrings(got.StripParams, want.EmptyStripParamsIsNoOpinion) {
			t.Errorf("空切片应视为无意见：got %v, want %v", got.StripParams, want.EmptyStripParamsIsNoOpinion)
		}
		if got := ResolveCapabilities("deepseek", &CapabilitiesOverride{StripParams: want.ExplicitStripParams}, nil); !sameStrings(got.StripParams, want.ExplicitStripParams) {
			t.Errorf("显式切片应覆盖：got %v, want %v", got.StripParams, want.ExplicitStripParams)
		}
		if got := ResolveCapabilities("longcat", &CapabilitiesOverride{ThinkingBlock: ptr(ThinkingEnabled)}, nil); got.SupportsThinking != want.DeriveThinkingFromBlock {
			t.Errorf("从 thinkingBlock 派生：got %v, want %v", got.SupportsThinking, want.DeriveThinkingFromBlock)
		}
		if got := ResolveCapabilities("deepseek",
			&CapabilitiesOverride{ThinkingBlock: ptr(ThinkingNone), EffortFormat: ptr(EffortNone)}, nil); got.SupportsThinking != want.DeriveNoThinking {
			t.Errorf("双 none 派生 false：got %v, want %v", got.SupportsThinking, want.DeriveNoThinking)
		}
		got := ResolveCapabilities("deepseek", &CapabilitiesOverride{EffortFormat: ptr(EffortNone)}, nil)
		if string(got.PrefixCacheStrategy) != want.UndeclaredFallsThrough.PrefixCacheStrategy {
			t.Errorf("未声明字段穿透：PrefixCacheStrategy got %q, want %q",
				got.PrefixCacheStrategy, want.UndeclaredFallsThrough.PrefixCacheStrategy)
		}
		if got.PreservedThinkingProtocol != want.UndeclaredFallsThrough.PreservedThinkingProtocol {
			t.Errorf("未声明字段穿透：PreservedThinkingProtocol got %v, want %v",
				got.PreservedThinkingProtocol, want.UndeclaredFallsThrough.PreservedThinkingProtocol)
		}
	})

	// ── 3. usage 归一化 ──
	t.Run("usage", func(t *testing.T) {
		var want oracleUsage
		if err := json.Unmarshal(golden["__usage"], &want); err != nil {
			t.Fatalf("解析 __usage 失败：%v", err)
		}

		check := func(name string, raw map[string]any, exp *oracleUsageEntry) {
			if exp == nil {
				t.Skipf("%s: oracle 未提供 mapUsage", name)
			}
			got := MapDeepSeekUsage(raw)
			if got.InputTokens != exp.InputTokens || got.OutputTokens != exp.OutputTokens ||
				got.CacheReadInputTokens != exp.CacheReadInputTokens || got.CacheCreationInputTokens != exp.CacheCreationInputTokens {
				t.Errorf("%s\n  got:  %+v\n  want: %+v", name, got, *exp)
			}
		}

		check("deepseekNative", map[string]any{
			"prompt_tokens": 100, "completion_tokens": 20,
			"prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20,
		}, want.DeepSeekNative)
		check("anthropicCompat", map[string]any{
			"input_tokens": 100, "output_tokens": 20,
			"cache_read_input_tokens": 80, "cache_creation_input_tokens": 20,
		}, want.AnthropicCompat)
		check("nestedCached", map[string]any{
			"prompt_tokens": 100, "completion_tokens": 20,
			"prompt_tokens_details": map[string]any{"cached_tokens": 55},
		}, want.NestedCached)
	})
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}
