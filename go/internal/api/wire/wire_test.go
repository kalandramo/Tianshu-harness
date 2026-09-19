package wire

import (
	"testing"
)

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

func negZero() float64 {
	z := 0.0
	return -z
}
