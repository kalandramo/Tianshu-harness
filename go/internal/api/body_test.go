package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// oracleCase 是 wire oracle 的一个用例。
type oracleCase struct {
	Raw      string   `json:"raw"`
	KeyOrder []string `json:"key_order"`
}

// loadWireOracle 读取真实客户端捕获的 oracle。
func loadWireOracle(t *testing.T) map[string]oracleCase {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "wire", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：node_modules/.bin/tsx go/testdata/wire/gen-oracle.ts", p, err)
	}
	var out map[string]oracleCase
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空——测试前置条件失败")
	}
	return out
}

// ── 测试夹具：与 gen-oracle.ts 的 BASE_* 逐字对应 ──

func baseConfig() *ClientConfig {
	temp := 0.7
	return &ClientConfig{
		Model:                     "deepseek-v4-pro",
		MaxTokens:                 8192,
		ProviderName:              "deepseek",
		Thinking:                  "disabled",
		ThinkingBlockType:         ThinkingEnabled,
		EffortFormat:              EffortReasoningEffort,
		Temperature:               &temp,
		PreservedThinkingProtocol: true,
	}
}

func baseMessages() []*wire.OrderedMap {
	return []*wire.OrderedMap{
		wire.NewOrderedMap().Set("role", "system").Set("content", "你是天枢。证据先行。"),
		wire.NewOrderedMap().Set("role", "user").Set("content", "refactor this function"),
		wire.NewOrderedMap().
			Set("role", "assistant").
			Set("content", "ok").
			Set("tool_calls", []any{
				wire.NewOrderedMap().
					Set("id", "c1").
					Set("type", "function").
					Set("function", wire.NewOrderedMap().
						Set("name", "read_file").
						Set("arguments", `{"path":"a.ts"}`)),
			}),
		wire.NewOrderedMap().Set("role", "tool").Set("tool_call_id", "c1").Set("content", "line1\nline2"),
	}
}

func baseTools() []*wire.OrderedMap {
	return []*wire.OrderedMap{
		wire.NewOrderedMap().
			Set("type", "function").
			Set("function", wire.NewOrderedMap().
				Set("name", "read_file").
				Set("description", "Read a file").
				Set("parameters", wire.NewOrderedMap().
					Set("type", "object").
					Set("properties", wire.NewOrderedMap().
						Set("path", wire.NewOrderedMap().Set("type", "string"))).
					Set("required", []any{"path"}))),
		wire.NewOrderedMap().
			Set("type", "function").
			Set("function", wire.NewOrderedMap().
				Set("name", "bash").
				Set("description", "Run a command <careful>").
				Set("parameters", wire.NewOrderedMap().
					Set("type", "object").
					Set("properties", wire.NewOrderedMap().
						Set("command", wire.NewOrderedMap().Set("type", "string"))).
					Set("required", []any{"command"}))),
	}
}

func f64(v float64) *float64 { return &v }

func baseRequest() *ChatRequest {
	return &ChatRequest{
		Model:       "deepseek-v4-pro",
		Messages:    baseMessages(),
		Tools:       baseTools(),
		Temperature: f64(0.7),
		MaxTokens:   intPtr(8192),
	}
}

func intPtr(v int) *int { return &v }

// TestBuildWireBodyGoldenParity 用真实客户端捕获的字节对账请求体构造。
//
// 这是 Wave 1c 的主判据。oracle 由 mock fetch 捕获真实 OpenAIClient.stream()
// 发送的字节，覆盖 15 个条件分支组合。
func TestBuildWireBodyGoldenParity(t *testing.T) {
	oracle := loadWireOracle(t)

	builders := map[string]func() (*wire.OrderedMap, *ClientConfig){
		"base": func() (*wire.OrderedMap, *ClientConfig) {
			return BuildWireBody(baseRequest(), baseConfig()), baseConfig()
		},
		"no_tools": func() (*wire.OrderedMap, *ClientConfig) {
			req := baseRequest()
			req.Tools = nil
			return BuildWireBody(req, baseConfig()), baseConfig()
		},
		"thinking_enabled_with_effort": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.Thinking = "enabled"
			cfg.ReasoningEffort = "max"
			cfg.Temperature = f64(0.9) // 应被抑制
			req := baseRequest()
			req.Temperature = nil
			return BuildWireBody(req, cfg), cfg
		},
		"thinking_enabled_no_block": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.ProviderName = "openai"
			cfg.Thinking = "enabled"
			cfg.ThinkingBlockType = ThinkingNone
			cfg.ReasoningEffort = "high"
			req := baseRequest()
			req.Temperature = nil
			return BuildWireBody(req, cfg), cfg
		},
		"effort_cap_clamp": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.ProviderName = "codex"
			cfg.Thinking = "enabled"
			cfg.ThinkingBlockType = ThinkingNone
			cfg.ReasoningEffort = "max"
			cfg.EffortCap = map[string]string{"max": "xhigh"}
			req := baseRequest()
			req.Temperature = nil
			return BuildWireBody(req, cfg), cfg
		},
		"max_completion_tokens": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.UseMaxCompletionTokens = true
			return BuildWireBody(baseRequest(), cfg), cfg
		},
		"no_stream_options": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.Unsupported = []string{"stream_options"}
			return BuildWireBody(baseRequest(), cfg), cfg
		},
		"json_mode": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.JSONMode = true
			req := baseRequest()
			req.ResponseFormat = nil
			return BuildWireBody(req, cfg), cfg
		},
		"request_response_format_wins": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.JSONMode = true
			req := baseRequest()
			// 必须用 OrderedMap —— 嵌套键序影响 wire 字节
			req.ResponseFormat = wire.NewOrderedMap().Set("type", "json_schema").Set("name", "x")
			return BuildWireBody(req, cfg), cfg
		},
		"system_suffix": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.PreservedThinkingProtocol = false // 非保留式 → 不注入后缀
			return BuildWireBody(baseRequest(), cfg), cfg
		},
		"tool_choice": func() (*wire.OrderedMap, *ClientConfig) {
			req := baseRequest()
			req.ToolChoice = "required"
			return BuildWireBody(req, baseConfig()), baseConfig()
		},
		"reasoning_split": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.ProviderName = "minimax"
			cfg.ReasoningSplit = true
			return BuildWireBody(baseRequest(), cfg), cfg
		},
		"preserved_thinking_missing_reasoning": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.Thinking = "enabled"
			req := &ChatRequest{
				Model: "deepseek-v4-pro",
				Messages: []*wire.OrderedMap{
					wire.NewOrderedMap().Set("role", "user").Set("content", "hi"),
					wire.NewOrderedMap().
						Set("role", "assistant").
						Set("content", "").
						Set("tool_calls", []any{
							wire.NewOrderedMap().
								Set("id", "c1").
								Set("type", "function").
								Set("function", wire.NewOrderedMap().
									Set("name", "read_file").
									Set("arguments", "{}")),
						}),
				},
				Tools:     baseTools(),
				MaxTokens: intPtr(8192),
			}
			return BuildWireBody(req, cfg), cfg
		},
		"strip_reasoning_non_preserved": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.ProviderName = "glm"
			req := &ChatRequest{
				Model: "deepseek-v4-pro",
				Messages: []*wire.OrderedMap{
					wire.NewOrderedMap().Set("role", "user").Set("content", "hi"),
					wire.NewOrderedMap().
						Set("role", "assistant").
						Set("content", "answer").
						Set("reasoning_content", "thinking..."),
				},
				Tools:       baseTools(),
				Temperature: f64(0.7),
				MaxTokens:   intPtr(8192),
			}
			return BuildWireBody(req, cfg), cfg
		},
		"budget_tokens": func() (*wire.OrderedMap, *ClientConfig) {
			cfg := baseConfig()
			cfg.ProviderName = "claude"
			cfg.Thinking = "enabled"
			cfg.ThinkingBlockType = ThinkingEnabled
			cfg.ThinkingBudgetField = "budget_tokens"
			cfg.ReasoningEffort = "high"
			req := baseRequest()
			req.Temperature = nil
			return BuildWireBody(req, cfg), cfg
		},
	}

	// 正向：oracle 的每个用例都必须在 builders 中
	for name := range oracle {
		if _, ok := builders[name]; !ok {
			t.Errorf("oracle 含未覆盖的用例 %q——gen-oracle.ts 与测试不同步", name)
		}
	}
	// 反向：builders 的每个用例都必须在 oracle 中
	for name := range builders {
		if _, ok := oracle[name]; !ok {
			t.Errorf("用例 %q 未在 oracle 中——需重新生成", name)
		}
	}

	for name, tc := range oracle {
		build, ok := builders[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			body, _ := build()
			got := body.Marshal()

			if got != tc.Raw {
				idx := firstDiff(got, tc.Raw)
				t.Errorf("字节与真实客户端不符（首差 @ %d）\n  got  ...%s...\n  want ...%s...",
					idx, clip(got, idx), clip(tc.Raw, idx))
			}
			if !reflect.DeepEqual(body.Keys(), tc.KeyOrder) {
				t.Errorf("顶层键序不符\n  got:  %v\n  want: %v", body.Keys(), tc.KeyOrder)
			}
		})
	}
}

// 反证 A：字段序必须与真实客户端一致。
// 把 model/messages 顺序对调，本测试变红。
func TestFieldOrderIsEnforced(t *testing.T) {
	oracle := loadWireOracle(t)
	want := oracle["base"].Raw

	// 正确实现
	got := BuildWireBody(baseRequest(), baseConfig()).Marshal()
	if got != want {
		t.Fatalf("正确实现未通过——先修实现再谈反证\n  got:  %s\n  want: %s", got, want)
	}

	// 错误顺序（模拟手抄错误）
	bad := wire.NewOrderedMap()
	bad.Set("messages", "x")
	bad.Set("model", "y")
	badOut := bad.Marshal()
	if badOut != `{"messages":"x","model":"y"}` {
		t.Fatalf("OrderedMap 未保序：%s", badOut)
	}
	// 确认两者确实不同（证明顺序是判别维度）
	if badOut == `{"model":"y","messages":"x"}` {
		t.Fatal("顺序未生效——本反证失去意义")
	}
}

// 反证 B：thinking=enabled 时 config.temperature 不得注入。
func TestThinkingSuppressesConfigTemperature(t *testing.T) {
	oracle := loadWireOracle(t)
	tc := oracle["thinking_enabled_with_effort"]

	cfg := baseConfig()
	cfg.Thinking = "enabled"
	cfg.ReasoningEffort = "max"
	cfg.Temperature = f64(0.9) // 应被抑制
	req := baseRequest()
	req.Temperature = nil

	body := BuildWireBody(req, cfg)
	if _, has := body.Get("temperature"); has {
		t.Errorf("thinking=enabled 时注入了 config.temperature：%s", body.Marshal())
	}
	if body.Marshal() != tc.Raw {
		t.Errorf("字节不符\n  got:  %s\n  want: %s", body.Marshal(), tc.Raw)
	}
}

// 反证 C：tools 为空时必须**缺席**，而非空数组。
func TestEmptyToolsOmitted(t *testing.T) {
	oracle := loadWireOracle(t)
	req := baseRequest()
	req.Tools = nil
	body := BuildWireBody(req, baseConfig())
	got := body.Marshal()

	if got != oracle["no_tools"].Raw {
		t.Errorf("空 tools 处理不符\n  got:  %s\n  want: %s", got, oracle["no_tools"].Raw)
	}
	// 确认确实没有 tools 键（缺席而非空数组）
	if body.Has("tools") {
		t.Error("空 tools 应缺席，实际存在该键")
	}
}

// 反证 D：系统后缀必须 copy-on-write，不得污染调用方的消息对象。
//
// 若 applySystemSuffix 原地修改，本测试变红——且会造成重入时双写
// （2026-07-06 wireDiverged idx 0 事故）。
func TestSystemSuffixIsCopyOnWrite(t *testing.T) {
	cfg := baseConfig()
	cfg.Thinking = "enabled" // 触发自动派生中文思考后缀
	req := baseRequest()
	req.Temperature = nil

	origSystem, _ := req.Messages[0].Get("content")
	origStr := origSystem.(string)

	body := BuildWireBody(req, cfg)
	_ = body

	// 调用方的消息对象不得被改动
	after, _ := req.Messages[0].Get("content")
	if after != origStr {
		t.Fatalf("调用方消息被污染：%q → %q", origStr, after)
	}

	// 二次调用不得双写后缀
	body2 := BuildWireBody(req, cfg)
	msgs2, _ := body2.Get("messages")
	arr2 := msgs2.([]any)
	sys2 := arr2[0].(*wire.OrderedMap)
	c2, _ := sys2.Get("content")
	want := origStr + ChineseThinkingSuffix
	if c2.(string) != want {
		t.Fatalf("后缀被双写或未追加\n  got:  %q\n  want: %q", c2.(string), want)
	}
}

// 后缀自动派生规则：仅 preservedThinkingProtocol && thinking==='enabled' 时注入。
func TestSystemSuffixDerivation(t *testing.T) {
	cases := []struct {
		name       string
		preserved  bool
		thinking   string
		wantSuffix bool
	}{
		{"保留式+enabled → 注入", true, "enabled", true},
		{"保留式+disabled → 不注入", true, "disabled", false},
		{"非保留式+enabled → 不注入", false, "enabled", false},
		{"非保留式+disabled → 不注入", false, "disabled", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.PreservedThinkingProtocol = tc.preserved
			cfg.Thinking = tc.thinking
			req := baseRequest()
			req.Temperature = nil

			body := BuildWireBody(req, cfg)
			msgs, _ := body.Get("messages")
			arr := msgs.([]any)
			sys := arr[0].(*wire.OrderedMap)
			c, _ := sys.Get("content")
			got := strings.HasSuffix(c.(string), ChineseThinkingSuffix)
			if got != tc.wantSuffix {
				t.Errorf("后缀注入 = %v, want %v（content 尾部: %q）", got, tc.wantSuffix, c.(string))
			}
		})
	}
}

// 反证 E：effortCap 必须钳制档位。
func TestEffortCapClamps(t *testing.T) {
	oracle := loadWireOracle(t)
	cfg := baseConfig()
	cfg.ProviderName = "codex"
	cfg.Thinking = "enabled"
	cfg.ThinkingBlockType = ThinkingNone
	cfg.ReasoningEffort = "max"
	cfg.EffortCap = map[string]string{"max": "xhigh"}
	req := baseRequest()
	req.Temperature = nil

	body := BuildWireBody(req, cfg)
	effort, _ := body.Get("reasoning_effort")
	if effort != "xhigh" {
		t.Errorf("effortCap 未钳制：got %v, want xhigh", effort)
	}
	if body.Marshal() != oracle["effort_cap_clamp"].Raw {
		t.Errorf("字节不符\n  got:  %s\n  want: %s", body.Marshal(), oracle["effort_cap_clamp"].Raw)
	}
}

func firstDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func clip(s string, at int) string {
	lo := at - 50
	if lo < 0 {
		lo = 0
	}
	hi := at + 50
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}
