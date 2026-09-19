// Package wire 实现模型请求体的**有序** JSON 序列化。
//
// 为什么不能用 encoding/json 或 stablejson：
//
//	天枢真实 wire 路径是 JSON.stringify(effectiveBody)
//	（src/api/openai-client.ts:783），它**保持键的插入顺序**。
//	而：
//	  - encoding/json 的 map[string]any 会**排序**键
//	  - stablejson.Stringify 也**排序**键（那是它的设计目的，用于工具
//	    arguments 的确定性）
//
// 前缀缓存要求请求体逐字节稳定。TS 侧的顺序由 openai-client.ts:430+ 的
// 逐字段赋值序决定（messages → model → stream → max_tokens → stream_options
// → tools → temperature），嵌套对象的顺序同理来自对象字面量的书写序。
// Go 侧若用 map，键会被排序，整个请求体与 TS 不同 → 缓存必然碎裂。
//
// 因此本包提供 OrderedMap：显式记录键的插入顺序，序列化时按序输出。
//
// 实测证据（探针对账 go/testdata/wire/oracle.json）：
//
//	TS:  {"messages":[{"role":"system","content":"你是天枢。证据先行。"},...]}
//	Go(map): {"messages":[{"content":"你是天枢。证据先行。","role":"system"},...]}
//	                     ^^^^^^^^ 键序被改变 → 缓存碎裂
package wire

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OrderedMap 是保持插入顺序的 JSON 对象。
//
// 零值不可用，请用 NewOrderedMap 或 &OrderedMap{} 构造。
type OrderedMap struct {
	keys   []string
	values map[string]any
}

// NewOrderedMap 创建空的有序对象。
func NewOrderedMap() *OrderedMap {
	return &OrderedMap{values: make(map[string]any)}
}

// Set 追加或更新一个键。首次设置时记录顺序；已存在的键保持原位置。
// 返回自身以便链式调用。
func (m *OrderedMap) Set(key string, value any) *OrderedMap {
	if m.values == nil {
		m.values = make(map[string]any)
	}
	if _, exists := m.values[key]; !exists {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
	return m
}

// SetIf 在 cond 为真时设置键。用于对齐 TS 侧的条件赋值
// （如 `if (request.temperature !== undefined) body.temperature = ...`）。
func (m *OrderedMap) SetIf(cond bool, key string, value any) *OrderedMap {
	if cond {
		m.Set(key, value)
	}
	return m
}

// Get 读取键值。
func (m *OrderedMap) Get(key string) (any, bool) {
	if m.values == nil {
		return nil, false
	}
	v, ok := m.values[key]
	return v, ok
}

// Has 报告键是否存在（区别于值为 nil）。
func (m *OrderedMap) Has(key string) bool {
	if m.values == nil {
		return false
	}
	_, ok := m.values[key]
	return ok
}

// Delete 删除键，保持其余键的相对顺序。
func (m *OrderedMap) Delete(key string) {
	if m.values == nil {
		return
	}
	if _, ok := m.values[key]; !ok {
		return
	}
	delete(m.values, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// Clone 浅拷贝（值共享引用，键序复制）。
//
// 用于消息变换：TS 侧用 `{ ...m }` 展开创建新对象，绝不原地改共享消息
// （原地改会导致重入时双写——2026-07-06 wireDiverged idx 0 事故）。
func (m *OrderedMap) Clone() *OrderedMap {
	out := &OrderedMap{
		keys:   make([]string, len(m.keys)),
		values: make(map[string]any, len(m.values)),
	}
	copy(out.keys, m.keys)
	for k, v := range m.values {
		out.values[k] = v
	}
	return out
}

// Keys 返回键的插入顺序（副本）。
func (m *OrderedMap) Keys() []string {
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

// Len 返回键数量。
func (m *OrderedMap) Len() int { return len(m.keys) }

// Marshal 按插入顺序序列化 m，转义规则对齐 JSON.stringify。
//
// 与 stablejson 的关键差异：**不排序键**。这正是 wire 路径需要的语义。
func (m *OrderedMap) Marshal() string {
	var b strings.Builder
	m.writeTo(&b)
	return b.String()
}

func (m *OrderedMap) writeTo(b *strings.Builder) {
	b.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, k)
		b.WriteByte(':')
		writeValue(b, m.values[k])
	}
	b.WriteByte('}')
}

// MarshalValue 序列化任意值，语义对齐 JSON.stringify（保插入序、不转义 HTML）。
//
// 支持的 Go 类型：nil、bool、string、各宽度整数、float32/64、
// *OrderedMap、map[string]any（**会排序**，仅在顺序无关时使用）、
// []any、[]string。
func MarshalValue(v any) string {
	var b strings.Builder
	writeValue(&b, v)
	return b.String()
}

func writeValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, x)
	case float64:
		writeNumber(b, x)
	case float32:
		writeNumber(b, float64(x))
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int8:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint8:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint16:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case *OrderedMap:
		if x == nil {
			b.WriteString("null")
			return
		}
		x.writeTo(b)
	case map[string]any:
		// 注意：map 的键序在 Go 中不稳定，这里排序以保证**确定性**
		// （不是与 TS 对齐——TS 保插入序）。仅用于顺序无关的场景。
		writeSortedMap(b, x)
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeValue(b, item)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, s := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, s)
		}
		b.WriteByte(']')
	default:
		panic("wire: unsupported type")
	}
}

func writeSortedMap(b *strings.Builder, m map[string]any) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 简单插入排序——对象通常很小（<10 键），避免引入 sort 依赖
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, k)
		b.WriteByte(':')
		writeValue(b, m[k])
	}
	b.WriteByte('}')
}

// writeNumber 按 ECMAScript Number::toString 语义格式化（与 stablejson 一致）。
func writeNumber(b *strings.Builder, f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		b.WriteString("null")
		return
	}
	if f == 0 {
		b.WriteString("0") // 负零归一
		return
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	s := strconv.FormatFloat(f, format, -1, 64)
	if format == 'e' && len(s) >= 4 && s[len(s)-4] == 'e' && s[len(s)-3] == '-' && s[len(s)-2] == '0' {
		s = s[:len(s)-2] + s[len(s)-1:]
	}
	b.WriteString(s)
}

// writeString 按 JSON.stringify 的转义规则输出字符串（不转义 &<> 与 U+2028/29）。
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					writeHexEscape(b, uint16(c))
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	b.WriteByte('"')
}

const hexDigits = "0123456789abcdef"

func writeHexEscape(b *strings.Builder, r uint16) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xF])
	b.WriteByte(hexDigits[(r>>8)&0xF])
	b.WriteByte(hexDigits[(r>>4)&0xF])
	b.WriteByte(hexDigits[r&0xF])
}
