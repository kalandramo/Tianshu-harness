package stablejson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOracleGoldenParity 用**真实 TS oracle 产出**的 golden 做字节对账。
//
// 与 TestOracleParity 的区别：那个测试的期望值是我手写的（可能编码我的误解）；
// 本测试的期望值来自实际运行 src/api/stable-json.ts 的输出，是真正的独立通道。
//
// golden 生成方式（可复现）：
//
//	npx tsx go/testdata/stablejson/gen-oracle.ts > go/testdata/stablejson/oracle.json
//
// 载荷定义在 gen-oracle.ts 中，两侧共用同一份输入定义。
func TestOracleGoldenParity(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "..", "testdata", "stablejson", "oracle.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取 golden 失败（%s）：%v\n生成命令见本测试注释", goldenPath, err)
	}
	var golden map[string]string
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("解析 golden 失败：%v", err)
	}
	if len(golden) == 0 {
		t.Fatal("golden 为空——测试前置条件失败")
	}

	// 载荷必须与 gen-oracle.ts 中的定义逐字一致。
	// 注意：Go 侧用 float64 表达 JS 的 number，用 int 表达整数字面量。
	payloads := map[string]any{
		"real_chat_request": map[string]any{
			"model": "deepseek-v4-pro",
			"messages": []any{
				map[string]any{"role": "system", "content": "你是天枢。证据先行。"},
				map[string]any{"role": "user", "content": "refactor this function"},
				map[string]any{"role": "assistant", "content": "ok", "tool_calls": []any{
					map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a.ts"}`}},
				}},
				map[string]any{"role": "tool", "tool_call_id": "c1", "content": "line1\nline2"},
			},
			"tools": []any{
				map[string]any{"type": "function", "function": map[string]any{
					"name": "read_file", "description": "Read a file",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}},
				}},
				map[string]any{"type": "function", "function": map[string]any{
					"name": "bash", "description": "Run a command",
					"parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}},
				}},
			},
			"temperature": 0.7,
			"max_tokens":  8192,
			"stream":      true,
		},
		"html_in_content": map[string]any{"content": `<div class="x">&amp;</div>`, "nested": map[string]any{"a": "<b>"}},
		"numbers": map[string]any{
			"a": 1.0, "b": 0.1, "c": 1e21, "d": 1e-7, "e": 1.0 / 3.0,
			"f": negZero(), "g": 100.0, "h": 1e20,
		},
		"mixed_keys": map[string]any{
			"中文键": 1, "ascii": 2, "🎉": 3, "Z": 4, "\uE000": 5, "\U00010000": 6,
		},
		"deep_nesting": map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{"e": []any{1, []any{2, []any{3}}}}}}}},
		"empty_containers": map[string]any{
			"o": map[string]any{}, "a": []any{}, "n": nil, "s": "", "z": 0.0, "f": false,
		},
		"unicode_escapes": map[string]any{"t": "a\tb\nc\"d\\e", "u": "\u2028\u2029", "c": "\u0000\u001f"},
		"tool_schema_like": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Regex, e.g. <a>&</a>"},
			},
			"required": []any{}, "additionalProperties": false,
		},
	}

	for name, want := range golden {
		in, ok := payloads[name]
		if !ok {
			t.Errorf("golden 含未定义的载荷 %q——gen-oracle.ts 与本测试的载荷集不同步", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := Stringify(in)
			if got != want {
				t.Errorf("与 TS oracle 字节不一致\n  got:  %q\n  want: %q", got, want)
			}
		})
	}

	// 反向覆盖：本测试定义的载荷都必须在 golden 中出现（防载荷漏测）
	for name := range payloads {
		if _, ok := golden[name]; !ok {
			t.Errorf("载荷 %q 未在 golden 中——需重新生成 golden", name)
		}
	}
}

func negZero() float64 {
	z := 0.0
	return -z
}
