package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// serializeOracle 是 TS 序列化函数的真实产出。
// 生成命令：npx tsx go/testdata/serialize/gen-oracle.ts
type serializeOracle struct {
	DefaultMaxChars int `json:"defaultMaxChars"`
	Cases           map[string]struct {
		Note     string `json:"note"`
		Kind     string `json:"kind"`
		MaxChars int    `json:"maxChars"`
		Output   string `json:"output"`
		// Message 原样保留（用于 Go 侧重放同一输入）
	} `json:"cases"`
	// 输入用例表（生成器未直接写入，此处按 case 名重建）
}

func loadSerializeOracle(t *testing.T) serializeOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "serialize", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/serialize/gen-oracle.ts", path, err)
	}
	var o serializeOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestTruncateStringBasic —— 短串不截断、长串附 marker。
func TestTruncateStringBasic(t *testing.T) {
	if got := TruncateString("abc", 10); got != "abc" {
		t.Errorf("短串不应截断，得到 %q", got)
	}
	// 恰好等于
	if got := TruncateString("abcde", 5); got != "abcde" {
		t.Errorf("恰好等于不应截断，得到 %q", got)
	}
	// 超长
	got := TruncateString(strings.Repeat("x", 100), 50)
	if !strings.Contains(got, "<session-message-truncated") {
		t.Errorf("应含截断 marker：%q", got)
	}
	if !strings.Contains(got, `original_chars="100"`) {
		t.Errorf("marker 应含 original_chars=100：%q", got)
	}
	if !strings.Contains(got, `kept_chars="50"`) {
		t.Errorf("marker 应含 kept_chars=50：%q", got)
	}
	// **注意**：结果长度**可能超过 maxChars**——marker 本身有长度，
	// 当 maxChars 小于 marker 长度时（此处 marker ≈ 67 > 50），
	// 输出就是纯 marker。这是 TS 的原样行为，不要"修正"。
	if !strings.HasPrefix(got, "\n<session-message-truncated") {
		t.Errorf("额度不足时应只剩 marker：%q", got)
	}
}

// TestTruncateStringTinyBudget —— 极小额度时 keep 归 0，marker 仍完整。
func TestTruncateStringTinyBudget(t *testing.T) {
	got := TruncateString(strings.Repeat("x", 100), 10)
	// marker 本身比 10 长 → 只保留 marker
	if !strings.HasPrefix(got, "\n<session-message-truncated") {
		t.Errorf("应只剩 marker：%q", got)
	}
	if strings.Contains(got, "x") {
		t.Errorf("不应保留原字符：%q", got)
	}
}

// TestTruncateStringUTF16 —— **UTF-16 语义**：emoji 计 2。
func TestTruncateStringUTF16(t *testing.T) {
	// 50 个 emoji = 100 UTF-16 units
	s := strings.Repeat("😀", 50)
	if utf16Len(s) != 100 {
		t.Fatalf("前置：emoji 串应为 100 units，得到 %d", utf16Len(s))
	}
	// maxChars=100 → 恰好等于，不截断
	if got := TruncateString(s, 100); got != s {
		t.Error("恰好等于不应截断")
	}
	// maxChars=99 → 触发截断
	got := TruncateString(s, 99)
	if !strings.Contains(got, "<session-message-truncated") {
		t.Errorf("应截断：%q", got)
	}
}

// TestCapJSONValueRecursive —— 递归 cap：字符串 / 数组 / 对象。
func TestCapJSONValueRecursive(t *testing.T) {
	long := strings.Repeat("x", 100)
	// 对象（有序）
	om := wire.NewOrderedMap().Set("a", long).Set("b", "short")
	capped := CapJSONValue(om, 20).(*wire.OrderedMap)
	a, _ := capped.Get("a")
	if a.(string) == long {
		t.Error("对象里的长串应被 cap")
	}
	b, _ := capped.Get("b")
	if b.(string) != "short" {
		t.Error("短串不应被改")
	}
	// 数组
	arr := CapJSONValue([]any{long, "short"}, 20).([]any)
	if arr[0].(string) == long {
		t.Error("数组里的长串应被 cap")
	}
	if arr[1].(string) != "short" {
		t.Error("数组里的短串不应被改")
	}
	// 非字符串原样
	if v := CapJSONValue(42, 20); v.(int) != 42 {
		t.Error("数字应原样")
	}
	if v := CapJSONValue(true, 20); v.(bool) != true {
		t.Error("布尔应原样")
	}
}

// TestCapBudget —— 预算计算：max(1000, floor(maxChars*0.8))。
func TestCapBudget(t *testing.T) {
	cases := map[int]int{
		100_000: 80_000,
		1500:    1200,
		1250:    1000,
		100:     1000, // 下限
		0:       1000,
		2000:    1600,
	}
	for in, want := range cases {
		if got := capBudget(in); got != want {
			t.Errorf("capBudget(%d) = %d，期望 %d", in, got, want)
		}
	}
}

// TestSerializeSessionMessageLayers —— 三层路径各自可达。
func TestSerializeSessionMessageLayers(t *testing.T) {
	// 第 1 层：原样
	msg := wire.NewOrderedMap().Set("role", "user").Set("content", "hello")
	got := SerializeSessionMessage("user", msg, 100_000)
	if got != `{"role":"user","content":"hello"}` {
		t.Errorf("第 1 层输出不符：%q", got)
	}

	// 第 2 层：cap 后 ≤ 上限
	msg2 := wire.NewOrderedMap().Set("role", "user").Set("content", strings.Repeat("x", 2000))
	got2 := SerializeSessionMessage("user", msg2, 1500)
	if utf16Len(got2) > 1500 {
		t.Errorf("第 2 层输出应 ≤ 1500，得到 %d", utf16Len(got2))
	}
	if !strings.Contains(got2, "<session-message-truncated") {
		t.Errorf("第 2 层应含截断 marker：%q", got2)
	}

	// 第 3 层：fallback（cap 后仍超长）
	//
	// **注意**：cap 用的是 `max(1000, maxChars*0.8)`——预算有 1000 下限，
	// 故小 maxChars 时 cap 后的结果仍远超上限，从而**必然**走 fallback。
	// 上例（maxChars=1500）的 cap 预算是 1200，cap 后 ≤1500 就停在第 2 层。
	msg3 := wire.NewOrderedMap().Set("role", "user").Set("content", strings.Repeat("x", 5000))
	got3 := SerializeSessionMessage("user", msg3, 30)
	// fallback 形态：content 是**整条消息 JSON 的截断**（转义后）。
	//
	// **注意**：当额度（30）远小于 marker 长度时，`keep` 归 0，
	// content 只剩 marker——看不到原 JSON 片段。这是 TS 的原样行为
	// （oracle 的 fallbackTriggered 用例：original_chars=437、kept_chars=30）。
	if !strings.Contains(got3, `"role":"user"`) {
		t.Errorf("fallback 的外层应有 role：%q", got3)
	}
	if !strings.Contains(got3, "<session-message-truncated") {
		t.Errorf("fallback 应含截断 marker：%q", got3)
	}
	// 内层 content 是转义后的 marker（引号被转义）
	if !strings.Contains(got3, `original_chars=`) {
		t.Errorf("fallback 的 content 应含转义 marker：%q", got3)
	}
}

// TestSerializeOaiSessionMessageNormalizes —— 先归一化再序列化。
func TestSerializeOaiSessionMessageNormalizes(t *testing.T) {
	// 空 tool_calls 数组应被移除
	text := "text"
	m := OaiMessage{Role: "assistant", Content: &text, ToolCalls: []OaiToolCall{}}
	got := SerializeOaiSessionMessage(m, 100_000)
	if strings.Contains(got, "tool_calls") {
		t.Errorf("空 tool_calls 应被移除：%q", got)
	}
	if got != `{"role":"assistant","content":"text"}` {
		t.Errorf("输出不符：%q", got)
	}

	// content 为 nil → 补空串
	m2 := OaiMessage{Role: "assistant", ToolCalls: []OaiToolCall{}}
	got2 := SerializeOaiSessionMessage(m2, 100_000)
	if got2 != `{"role":"assistant","content":""}` {
		t.Errorf("content 应补空串：%q", got2)
	}
}

// TestSerializeOaiToolCallsKeyOrder —— tool_calls 的键序。
func TestSerializeOaiToolCallsKeyOrder(t *testing.T) {
	empty := ""
	m := OaiMessage{
		Role:    "assistant",
		Content: &empty,
		ToolCalls: []OaiToolCall{
			{ID: "c1", Type: "function", Function: &OaiFunction{Name: "read_file", Arguments: `{"path":"a"}`}},
		},
	}
	got := SerializeOaiSessionMessage(m, 100_000)
	want := `{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}`
	if got != want {
		t.Errorf("键序不符\n  Go =%q\n  TS =%q", got, want)
	}
}

// TestSerializeOaiToolRoleIncludesID —— tool 角色带 tool_call_id。
func TestSerializeOaiToolRoleIncludesID(t *testing.T) {
	c := "result"
	m := OaiMessage{Role: "tool", ToolCallID: "c1", Content: &c}
	got := SerializeOaiSessionMessage(m, 100_000)
	if got != `{"role":"tool","content":"result","tool_call_id":"c1"}` {
		t.Errorf("输出不符：%q", got)
	}
}

// TestSerializeOracleParity —— 逐用例对账 TS oracle。
func TestSerializeOracleParity(t *testing.T) {
	o := loadSerializeOracle(t)
	// 各用例的输入与 maxChars（从生成器复刻）
	type in struct {
		kind  string
		build func() (any, string, int) // 返回 (message, role, maxChars)
	}
	_ = in{}
	checked := 0
	for name, c := range o.Cases {
		fn, ok := serializeInputs[name]
		if !ok {
			t.Errorf("[%s] 缺输入构造", name)
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			msg, role, maxChars := fn()
			var got string
			if c.Kind == "session" {
				got = SerializeSessionMessage(role, msg, c.MaxChars)
			} else {
				got = SerializeOaiSessionMessage(msg.(OaiMessage), c.MaxChars)
			}
			_ = maxChars
			if got != c.Output {
				t.Errorf("输出不符\n  Go =%q\n  TS =%q", got, c.Output)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// serializeInputs 按 case 名重建输入。
var serializeInputs = map[string]func() (any, string, int){
	"shortSession": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", "hello"), "user", 0
	},
	"shortOai": func() (any, string, int) {
		c := "hello"
		return OaiMessage{Role: "user", Content: &c}, "", 0
	},
	"oaiWithToolCalls": func() (any, string, int) {
		e := ""
		return OaiMessage{Role: "assistant", Content: &e, ToolCalls: []OaiToolCall{
			{ID: "c1", Type: "function", Function: &OaiFunction{Name: "read_file", Arguments: `{"path":"a"}`}},
		}}, "", 0
	},
	"oaiToolRole": func() (any, string, int) {
		c := "result"
		return OaiMessage{Role: "tool", ToolCallID: "c1", Content: &c,
			KeyOrder: []string{"role", "tool_call_id", "content"}}, "", 0
	},
	"longStringCap": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", strings.Repeat("x", 2000)), "user", 0
	},
	"nestedCap": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").
			Set("content", strings.Repeat("y", 3000)).
			Set("extra", wire.NewOrderedMap().Set("deep", strings.Repeat("z", 3000))), "user", 0
	},
	"arrayCap": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").
			Set("content", []any{strings.Repeat("a", 2000), strings.Repeat("b", 2000)}), "user", 0
	},
	"fallbackTriggered": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").
			Set("content", strings.Repeat("x", 200)).
			Set("tag", strings.Repeat("y", 200)), "user", 0
	},
	"fallbackOai": func() (any, string, int) {
		c := strings.Repeat("x", 300)
		return OaiMessage{Role: "tool", ToolCallID: "c9", Content: &c}, "", 0
	},
	"fallbackNonTool": func() (any, string, int) {
		c := strings.Repeat("x", 300)
		return OaiMessage{Role: "user", Content: &c}, "", 0
	},
	"exactlyAtLimit": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", "abc"), "user", 0
	},
	"oneOverLimit": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", "abcd"), "user", 0
	},
	"tinyBudget": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", strings.Repeat("x", 100)), "user", 0
	},
	"emojiLength": func() (any, string, int) {
		return wire.NewOrderedMap().Set("role", "user").Set("content", strings.Repeat("😀", 50)), "user", 0
	},
	"emptyToolCallsNormalized": func() (any, string, int) {
		c := "text"
		return OaiMessage{Role: "assistant", Content: &c, ToolCalls: []OaiToolCall{}}, "", 0
	},
	"nullContent": func() (any, string, int) {
		return OaiMessage{Role: "assistant", ToolCalls: []OaiToolCall{}}, "", 0
	},
	"keyOrderRoleContent": func() (any, string, int) {
		c := "hi"
		return OaiMessage{Role: "user", Content: &c, KeyOrder: []string{"role", "content"}}, "", 0
	},
	"keyOrderContentRole": func() (any, string, int) {
		c := "hi"
		return OaiMessage{Role: "user", Content: &c, KeyOrder: []string{"content", "role"}}, "", 0
	},
	"keyOrderToolCallIdFirst": func() (any, string, int) {
		c := "r"
		return OaiMessage{Role: "tool", ToolCallID: "c1", Content: &c,
			KeyOrder: []string{"tool_call_id", "role", "content"}}, "", 0
	},
	"keyOrderAssistant": func() (any, string, int) {
		e := ""
		return OaiMessage{Role: "assistant", Content: &e,
			KeyOrder: []string{"role", "content", "tool_calls"},
			ToolCalls: []OaiToolCall{
				{ID: "c1", Type: "function", Function: &OaiFunction{Name: "read_file", Arguments: "{}"}},
			}}, "", 0
	},
}
