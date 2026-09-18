package stablejson

import (
	"math"
	"testing"
)

// oracle 的语义基准测试——用例与期望值全部来自 TS 版 src/api/stable-json.ts 的实测输出。
// 期望值是**字节**，不是语义：语义比较会漏掉转义/键序差异。
//
// 证据来源：2026-09-19 探针实测（Node 侧运行真实 stableStringify，Go 侧运行
// encoding/json + SetEscapeHTML(false)），逐行对账后固化为本表。
func TestOracleParity(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		// ── HTML 字符：不转义（encoding/json 默认会转成 \u003c） ──
		{"html_escape", map[string]any{"x": "<a>&b</a>"}, `{"x":"<a>&b</a>"}`},

		// ── 数字格式（实测 8 组全部与 Node 一致） ──
		{"float_int_valued", map[string]any{"v": 1.0}, `{"v":1}`},
		{"float_0_1", map[string]any{"v": 0.1}, `{"v":0.1}`},
		{"float_1_5", map[string]any{"v": 1.5}, `{"v":1.5}`},
		{"float_1e21", map[string]any{"v": 1e21}, `{"v":1e+21}`},
		{"float_1e20", map[string]any{"v": 1e20}, `{"v":100000000000000000000}`},
		{"float_1e-6", map[string]any{"v": 1e-6}, `{"v":0.000001}`},
		{"float_1e-7", map[string]any{"v": 1e-7}, `{"v":1e-7}`},
		{"float_third", map[string]any{"v": 1.0 / 3.0}, `{"v":0.3333333333333333}`},

		// ── 负零：Node 输出 0，Go 原生输出 -0（反证点 A） ──
		{"negative_zero", map[string]any{"v": math.Copysign(0, -1)}, `{"v":0}`},

		// ── 非 ASCII 键序：UTF-16 code unit 序（反证点 B） ──
		// Node 实测顺序：Z _ a é ÿ 中 𐀀 🎉 \ue000 \ufffd
		// 注意 𐀀(U+10000, 代理对 d800 dc00) 排在 \ue000 之前——
		// 若按 UTF-8 字节序（Go sort.Strings），\ue000 会排到 𐀀 前面。
		{
			"key_order_utf16",
			map[string]any{
				"a": "1", "中": "2", "\uFFFD": "3", "\uE000": "4",
				"\U0001F389": "5", "Z": "6", "_": "7", "é": "8", "\u00FF": "9",
				"\U00010000": "10",
			},
			"{\"Z\":\"6\",\"_\":\"7\",\"a\":\"1\",\"é\":\"8\",\"ÿ\":\"9\",\"中\":\"2\",\"𐀀\":\"10\",\"🎉\":\"5\",\"\uE000\":\"4\",\"\uFFFD\":\"3\"}",
		},

		// ── ASCII 键序（两种编码一致，作为对照） ──
		{"key_order_ascii", map[string]any{"b": "x", "a": 1, "C": 2, "_z": 3}, `{"C":2,"_z":3,"a":1,"b":"x"}`},

		// ── 转义 ──
		{"escapes", map[string]any{"v": "a\"b\\c"}, `{"v":"a\"b\\c"}`},
		{"ctrl_short", map[string]any{"v": "\n\t\r"}, `{"v":"\n\t\r"}`},
		{"ctrl_hex", map[string]any{"v": "\u0000\u001f"}, `{"v":"\u0000\u001f"}`},

		// ── U+2028/2029：JSON.stringify 原样输出（反证点 C） ──
		{"line_separators_raw", map[string]any{"v": "\u2028\u2029"}, "{\"v\":\"\u2028\u2029\"}"},

		// ── Unicode 常规 ──
		{"cjk", map[string]any{"v": "中文"}, `{"v":"中文"}`},
		{"emoji", map[string]any{"v": "🎉"}, `{"v":"🎉"}`},

		// ── 结构 ──
		{"nested", map[string]any{"a": map[string]any{"c": []any{1, map[string]any{"b": 2}}, "d": nil}},
			`{"a":{"c":[1,{"b":2}],"d":null}}`},
		{"empty_obj", map[string]any{}, `{}`},
		{"empty_arr", []any{}, `[]`},
		{"array", []any{1, "a", nil, true}, `[1,"a",null,true]`},
		{"scalar_str", "str", `"str"`},
		{"scalar_num", 42, `42`},
		{"scalar_null", nil, `null`},

		// ── 与 oracle 的有意偏离：数组内 undefined ──
		// oracle 产出非法 JSON `[1,,2]`；本实现输出合法 null（修正而非偏离语义）
		{"array_undefined_fixed", []any{1, Undefined, 2}, `[1,null,2]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Stringify(tc.in)
			if got != tc.want {
				t.Errorf("byte mismatch\n  got:  %q\n  want: %q", got, tc.want)
			}
		})
	}
}

// 反证 A：负零必须归一。若 writeNumber 去掉 `if f == 0` 分支，本测试变红。
func TestNegativeZeroNormalized(t *testing.T) {
	got := Stringify(map[string]any{"v": math.Copysign(0, -1)})
	if got != `{"v":0}` {
		t.Fatalf("负零未归一：got %q, want %q", got, `{"v":0}`)
	}
	// 确认输入确实是负零（防测试自身失效）
	if !math.Signbit(math.Copysign(0, -1)) {
		t.Fatal("测试前置条件失败：输入不是负零")
	}
}

// 反证 B：键排序必须是 UTF-16 code unit 序。
// 若 utf16Less 退化为 `a < b`（UTF-8 字节序），本测试变红。
func TestKeyOrderIsUTF16NotUTF8(t *testing.T) {
	// U+E000 (UTF-8: ee 80 80) vs U+10000 (UTF-8: f0 90 80 80)
	// UTF-8 字节序：ee < f0 → U+E000 在前
	// UTF-16 序：  U+E000 = [e000]；U+10000 = [d800, dc00] → d800 < e000 → U+10000 在前
	in := map[string]any{"\uE000": "priv", "\U00010000": "astral"}
	got := Stringify(in)
	want := "{\"𐀀\":\"astral\",\"\uE000\":\"priv\"}"
	if got != want {
		t.Fatalf("键序不是 UTF-16 序\n  got:  %q\n  want: %q", got, want)
	}

	// 直接验证比较函数，锁定根因
	if !utf16Less("\U00010000", "\uE000") {
		t.Error("utf16Less 判定错误：U+10000 应排在 U+E000 之前")
	}
	if utf16Less("\uE000", "\U00010000") {
		t.Error("utf16Less 反向判定错误")
	}
	// ASCII 快速路径正确性
	if !utf16Less("a", "b") || utf16Less("b", "a") {
		t.Error("ASCII 快速路径错误")
	}
}

// 反证 C：HTML 字符与 U+2028/29 不得转义。
// 若改用 encoding/json 默认（无 SetEscapeHTML(false)）或加上 U+2028 转义，本测试变红。
func TestNoHTMLEscapeNoLineSeparatorEscape(t *testing.T) {
	if got := Stringify(map[string]any{"x": "<a>&b</a>"}); got != `{"x":"<a>&b</a>"}` {
		t.Errorf("HTML 字符被转义：%q", got)
	}
	if got := Stringify(map[string]any{"v": "\u2028"}); got != "{\"v\":\"\u2028\"}" {
		t.Errorf("U+2028 被转义：%q", got)
	}
	if got := Stringify(map[string]any{"v": "\u2029"}); got != "{\"v\":\"\u2029\"}" {
		t.Errorf("U+2029 被转义：%q", got)
	}
}

// 不变量：同语义不同构造顺序的 map 必须产出相同字节（这是缓存正确性的核心）。
func TestDeterministicAcrossInsertionOrder(t *testing.T) {
	a := map[string]any{}
	a["z"] = 1
	a["a"] = 2
	a["m"] = map[string]any{"y": 1, "b": 2}

	b := map[string]any{}
	b["m"] = map[string]any{"b": 2, "y": 1}
	b["a"] = 2
	b["z"] = 1

	if Stringify(a) != Stringify(b) {
		t.Fatalf("插入顺序影响输出：\n  a: %q\n  b: %q", Stringify(a), Stringify(b))
	}
}

// 未知类型必须显式失败，不得静默产出非 JSON。
func TestUnsupportedTypePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("未知类型未 panic——静默产出非 JSON 会击穿缓存且难排查")
		}
	}()
	_ = Stringify(struct{ X int }{X: 1})
}

// NaN / Inf 映射为 null（JSON 无此字面量，与 JSON.stringify 一致）。
func TestNonFiniteToNull(t *testing.T) {
	if got := Stringify(map[string]any{"v": math.NaN()}); got != `{"v":null}` {
		t.Errorf("NaN: got %q", got)
	}
	if got := Stringify(map[string]any{"v": math.Inf(1)}); got != `{"v":null}` {
		t.Errorf("+Inf: got %q", got)
	}
	if got := Stringify(map[string]any{"v": math.Inf(-1)}); got != `{"v":null}` {
		t.Errorf("-Inf: got %q", got)
	}
}

// 整数类型保持原样（不被转成 float64 丢精度）。
func TestIntegerTypesPreserved(t *testing.T) {
	if got := Stringify(map[string]any{"v": int64(9007199254740993)}); got != `{"v":9007199254740993}` {
		t.Errorf("int64 精度丢失：%q", got)
	}
	if got := Stringify(map[string]any{"v": uint64(18446744073709551615)}); got != `{"v":18446744073709551615}` {
		t.Errorf("uint64 精度丢失：%q", got)
	}
}

// 基准：确保序列化本身不是性能瓶颈（真实请求体量级）。
func BenchmarkStringifyRealisticPayload(b *testing.B) {
	payload := map[string]any{
		"model": "deepseek-v4-pro",
		"messages": []any{
			map[string]any{"role": "system", "content": string(make([]byte, 20000))},
			map[string]any{"role": "user", "content": "帮我重构这个函数"},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "读取文件"}},
		},
		"temperature": 0.7,
		"stream":      true,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Stringify(payload)
	}
}
