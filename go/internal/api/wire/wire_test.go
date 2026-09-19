package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestWireGoldenParity 用真实 TS oracle 对账请求体构造。
//
// oracle 由**真实 OpenAIClient** 经 mock fetch 捕获实际发送的字节
// （go/testdata/wire/gen-oracle.ts），不是手抄字段序——手抄曾导致
// golden 与 Go 实现自洽的假绿（两边都写成 messages→model→stream，
// 真实为 model→messages→stream）。
//
// golden 生成：node_modules/.bin/tsx go/testdata/wire/gen-oracle.ts
func TestWireGoldenParity(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "..", "testdata", "wire", "oracle.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取 golden 失败（%s）：%v\n生成命令见本测试注释", goldenPath, err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("解析 golden 失败：%v", err)
	}

	// 主判据：真实客户端发送的字节
	var want string
	if err := json.Unmarshal(golden["wire_raw"], &want); err != nil {
		t.Fatalf("golden 缺少 wire_raw 字段（旧格式？需重新生成）：%v", err)
	}
	got := BuildDeepSeekBody()
	if got != want {
		idx := firstDiff(got, want)
		t.Errorf("请求体字节与真实客户端不符（首差 @ %d）\n  got  ...%s...\n  want ...%s...",
			idx, snippet(got, idx), snippet(want, idx))
	}

	// 诊断维度：顶层键序（提供更可读的失败信息）
	var wantOrder []string
	if err := json.Unmarshal(golden["key_order"], &wantOrder); err != nil {
		t.Fatalf("解析 key_order 失败：%v", err)
	}
	if !reflect.DeepEqual(deepSeekBodyKeys(), wantOrder) {
		t.Errorf("顶层键序不符\n  got:  %v\n  want: %v", deepSeekBodyKeys(), wantOrder)
	}
}

// 反证：map[string]any 会排序键，破坏 wire 顺序。
//
// 若把 BuildDeepSeekBody 的嵌套对象改回 map[string]any，本测试变红——
// 这正是原计划会踩的坑。
func TestMapWouldBreakWireOrder(t *testing.T) {
	// 用 map 构造同一对象
	viaMap := MarshalValue(map[string]any{"role": "system", "content": "x"})
	// 用 OrderedMap 构造
	viaOrdered := NewOrderedMap().Set("role", "system").Set("content", "x").Marshal()

	if viaMap != `{"content":"x","role":"system"}` {
		t.Logf("map 输出（预期被排序）：%s", viaMap)
	}
	if viaOrdered != `{"role":"system","content":"x"}` {
		t.Fatalf("OrderedMap 未保序：%s", viaOrdered)
	}
	if viaMap == viaOrdered {
		t.Fatal("map 与 OrderedMap 输出相同——本测试失去反证意义")
	}
}

// 反证：stablejson（排序键）与 wire（保序）必须产出不同的字节。
// 若两者混用，缓存会碎裂。
func TestWireDiffersFromSortedSerialization(t *testing.T) {
	om := NewOrderedMap().Set("z", 1).Set("a", 2).Set("m", 3)
	wireOut := om.Marshal()
	if wireOut != `{"z":1,"a":2,"m":3}` {
		t.Fatalf("wire 未保插入序：%s", wireOut)
	}
	// 排序后的形态（stablejson 会产出这个）
	sortedOut := MarshalValue(map[string]any{"z": 1, "a": 2, "m": 3})
	if sortedOut != `{"a":2,"m":3,"z":1}` {
		t.Fatalf("排序形态不符预期：%s", sortedOut)
	}
	if wireOut == sortedOut {
		t.Fatal("保序与排序输出相同——至少一方实现有误")
	}
}

// 已存在的键再次 Set 应保持原位置（TS 的对象赋值语义）。
func TestOrderedMapUpdateKeepsPosition(t *testing.T) {
	om := NewOrderedMap().Set("a", 1).Set("b", 2).Set("c", 3)
	om.Set("b", 99) // 更新
	got := om.Marshal()
	if got != `{"a":1,"b":99,"c":3}` {
		t.Fatalf("更新键改变了位置：%s", got)
	}
}

// SetIf 对齐 TS 的条件赋值。
func TestSetIfMatchesConditionalAssignment(t *testing.T) {
	om := NewOrderedMap().
		Set("always", 1).
		SetIf(false, "skipped", 2).
		SetIf(true, "included", 3)
	got := om.Marshal()
	if got != `{"always":1,"included":3}` {
		t.Fatalf("SetIf 语义不符：%s", got)
	}
}

// 数字格式化必须与 JSON.stringify 一致（复用 stablejson 的 ES6 语义）。
func TestNumberFormattingMatchesJS(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1.0, "1"},
		{0.1, "0.1"},
		{1e21, "1e+21"},
		{1e20, "100000000000000000000"},
		{1e-6, "0.000001"},
		{1e-7, "1e-7"},
		{1.0 / 3.0, "0.3333333333333333"},
		{negZero(), "0"},
	}
	for _, tc := range cases {
		if got := MarshalValue(tc.in); got != tc.want {
			t.Errorf("MarshalValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 字符串转义必须与 JSON.stringify 一致（不转义 HTML 与 U+2028/29）。
func TestStringEscapingMatchesJS(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"html", "<a>&b</a>", `"<a>&b</a>"`},
		{"line_sep", "\u2028", "\"\u2028\""},
		{"ctrl", "\u0000\u001f", `"\u0000\u001f"`},
		{"short_escapes", "\n\t\r", `"\n\t\r"`},
		{"cjk", "中文", `"中文"`},
		{"emoji", "🎉", `"🎉"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarshalValue(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// 未知类型必须显式失败（静默产出非 JSON 会击穿缓存且难排查）。
func TestUnsupportedTypePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("未知类型未 panic")
		}
	}()
	_ = MarshalValue(struct{ X int }{X: 1})
}

// ── 测试辅助 ──

// BuildDeepSeekBody 复现 src/api/openai-client.ts:471-511 的 body 构造顺序。
//
// 顺序必须逐字照抄真实源码——这是本测试的全部价值所在。曾因写成
// messages→model→stream（真实为 model→messages→stream）导致 golden 与
// 实现自洽的假绿：两边都错，测试照绿。
func BuildDeepSeekBody() string {
	return buildDeepSeekBody().Marshal()
}

// deepSeekBodyKeys 返回顶层键的插入顺序（供键序断言）。
func deepSeekBodyKeys() []string {
	return buildDeepSeekBody().Keys()
}

func buildDeepSeekBody() *OrderedMap {
	mt := 8192
	temp := 0.7

	body := NewOrderedMap()
	// L474-L476：字面量内三字段
	body.Set("model", "deepseek-v4-pro")
	body.Set("messages", deepSeekMessages())
	body.Set("stream", true)
	// L479-L483
	body.Set("max_tokens", mt)
	// L487-L489
	body.Set("stream_options", NewOrderedMap().Set("include_usage", true))
	// L491-L494
	body.Set("tools", deepSeekTools())
	// L506-L511
	body.Set("temperature", temp)

	return body
}

func deepSeekMessages() []any {
	return []any{
		NewOrderedMap().Set("role", "system").Set("content", "你是天枢。证据先行。"),
		NewOrderedMap().Set("role", "user").Set("content", "refactor this function"),
		NewOrderedMap().
			Set("role", "assistant").
			Set("content", "ok").
			Set("tool_calls", []any{
				NewOrderedMap().
					Set("id", "c1").
					Set("type", "function").
					Set("function", NewOrderedMap().
						Set("name", "read_file").
						Set("arguments", `{"path":"a.ts"}`)),
			}),
		NewOrderedMap().Set("role", "tool").Set("tool_call_id", "c1").Set("content", "line1\nline2"),
	}
}

func deepSeekTools() []any {
	return []any{
		NewOrderedMap().
			Set("type", "function").
			Set("function", NewOrderedMap().
				Set("name", "read_file").
				Set("description", "Read a file").
				Set("parameters", NewOrderedMap().
					Set("type", "object").
					Set("properties", NewOrderedMap().
						Set("path", NewOrderedMap().Set("type", "string"))).
					Set("required", []any{"path"}))),
		NewOrderedMap().
			Set("type", "function").
			Set("function", NewOrderedMap().
				Set("name", "bash").
				Set("description", "Run a command <careful>").
				Set("parameters", NewOrderedMap().
					Set("type", "object").
					Set("properties", NewOrderedMap().
						Set("command", NewOrderedMap().Set("type", "string"))).
					Set("required", []any{"command"}))),
	}
}

func negZero() float64 {
	z := 0.0
	return -z
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

func snippet(s string, at int) string {
	lo := at - 50
	if lo < 0 {
		lo = 0
	}
	hi := at + 50
	if hi > len(s) {
		hi = len(s)
	}
	return strings.ReplaceAll(s[lo:hi], "\n", "\\n")
}
