// Package stablejson 实现与天枢 TS 版 src/api/stable-json.ts 字节等价的确定性 JSON 序列化。
//
// 为什么不能用 encoding/json：前缀缓存（prefix cache）依赖模型请求体逐字节稳定，
// 而 Go 标准库与 Node 的 JSON.stringify 在若干处不一致——见下表（均为实测结论，
// 探针证据见计划文档「三个高危差异点」）：
//
//	维度            Node (oracle)                    Go stdlib
//	&<> 转义        <a>&b</a>                        \u003ca\u003e...（默认）
//	非 ASCII 键序   UTF-16 code unit 序              UTF-8 字节序
//	负零            0                                -0
//	大整数          2^53 精度截断                    精确整数
//	U+2028/29       原样输出                         \u2028\u2029
//
// 本包的关键约束是**键排序必须按 UTF-16 code unit 序**——这是最隐蔽的差异：
// BMP 外字符（UTF-16 代理对 D800–DFFF）与 U+E000–U+FFFF 区间的相对顺序在两种
// 编码下完全相反，一旦排序不同则整个 JSON 文本不同，缓存静默碎裂。
package stablejson

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Stringify 按 TS 版 stableStringify 的语义序列化 v，输出与之字节等价的字符串。
//
// 语义对齐点（逐条对应 oracle 行为）：
//   - 对象键排序：UTF-16 code unit 序（不是 UTF-8 字节序，也不是 Go 的 sort.Strings）
//   - 值为 undefined 的键被跳过（Go 侧以 Undefined 哨兵表达，见下）
//   - null 原样输出
//   - 数字按 JS Number 语义（float64）
//   - HTML 字符 &<> 不转义
//   - U+2028/U+2029 不转义（与 JSON.stringify 一致）
//   - 控制字符按 \uXXXX 短转义
//
// 与 oracle 的**有意偏离**：oracle 对数组内 undefined 会产出非法 JSON（`[1,,2]`），
// 因为 stableStringify(undefined) 返回 JS 的 undefined 值而非字符串，join 时留空。
// 本实现不复制该缺陷——数组内 undefined 序列化为 null（合法 JSON，且与
// JSON.stringify 对数组 undefined 的行为一致）。这是修正而非偏离语义。
func Stringify(v any) string {
	var b strings.Builder
	writeValue(&b, v)
	return b.String()
}

// Undefined 是不存在值的哨兵。用于区分「键不存在 / 值为 undefined」与「值为 null」。
//
// Go 的 map[string]any 无法表达「键存在但值为 undefined」——调用方在构造 map 时
// 直接不放入该键即可。本类型主要用于 struct 化路径与显式调用场景。
type undefined struct{}

// Undefined 是 undefined 哨兵的单例。
var Undefined = undefined{}

func writeValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case undefined:
		// 顶层/数组内的 undefined：oracle 产出非法 JSON，这里输出 null（见 Stringify 注释）
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
		writeInt(b, int64(x))
	case int8:
		writeInt(b, int64(x))
	case int16:
		writeInt(b, int64(x))
	case int32:
		writeInt(b, int64(x))
	case int64:
		writeInt(b, x)
	case uint:
		writeUint(b, uint64(x))
	case uint8:
		writeUint(b, uint64(x))
	case uint16:
		writeUint(b, uint64(x))
	case uint32:
		writeUint(b, uint64(x))
	case uint64:
		writeUint(b, x)
	case []any:
		writeArray(b, x)
	case map[string]any:
		writeObject(b, x)
	case []string:
		arr := make([]any, len(x))
		for i, s := range x {
			arr[i] = s
		}
		writeArray(b, arr)
	default:
		// 兜底：未知类型不应静默产出错误 JSON。用 fmt 的 %v 会产出非 JSON，
		// 因此显式 panic——序列化层的静默错误会击穿缓存且难排查。
		panic("stablejson: unsupported type")
	}
}

func writeArray(b *strings.Builder, arr []any) {
	b.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			b.WriteByte(',')
		}
		writeValue(b, item)
	}
	b.WriteByte(']')
}

func writeObject(b *strings.Builder, obj map[string]any) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return utf16Less(keys[i], keys[j])
	})
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		v := obj[k]
		// oracle: keys.filter(k => obj[k] !== undefined) —— undefined 值的键被跳过
		if _, isUndef := v.(undefined); isUndef {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeString(b, k)
		b.WriteByte(':')
		writeValue(b, v)
	}
	b.WriteByte('}')
}

// utf16Less 比较两个字符串的排序，语义等价于 JS 的 Array.prototype.sort 默认
// 字符串比较（按 UTF-16 code unit 序）。
//
// 为什么不能直接比 UTF-8 字节：UTF-8 是码点序，UTF-16 code unit 序在
// U+E000–U+FFFF 与 U+10000 以上（代理对）之间不同。实测：
//
//	JS:   Z _ a é ÿ 中 𐀀 🎉 \ue000 \ufffd
//	Go:   Z _ a é ÿ 中 \ue000 \ufffd 𐀀 🎉
func utf16Less(a, b string) bool {
	// 快速路径：两者都是纯 ASCII/3 字节以内 BMP，UTF-8 字节序 == 码点序 == UTF-16 序。
	// 只有当字符串含 ≥U+10000 字符（4 字节 UTF-8 / 代理对）或与 U+E000+ 混排时才需细比。
	if !needsUTF16Compare(a) && !needsUTF16Compare(b) {
		return a < b
	}
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	n := len(ua)
	if len(ub) < n {
		n = len(ub)
	}
	for i := 0; i < n; i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// needsUTF16Compare 判断字符串是否可能因编码差异导致排序不同。
// 含 BMP 外字符（≥U+10000，需代理对）或含 U+E000–U+FFFF 区间字符时返回 true。
func needsUTF16Compare(s string) bool {
	for _, r := range s {
		if r >= 0x10000 || (r >= 0xE000 && r <= 0xFFFF) {
			return true
		}
	}
	return false
}

// writeInt / writeUint 保持整数原样输出（不转 float64）。
func writeInt(b *strings.Builder, v int64) {
	b.WriteString(strconv.FormatInt(v, 10))
}

func writeUint(b *strings.Builder, v uint64) {
	b.WriteString(strconv.FormatUint(v, 10))
}

// writeNumber 按 JS Number.toString() 语义格式化 float64。
//
// 实测已确认与 Go 一致的形态（strconv 短期格式）：
//
//	1.0      -> 1        （Go: FormatFloat('g') 给 1）
//	0.1      -> 0.1
//	1e21     -> 1e+21
//	1e20     -> 100000000000000000000
//	1e-6     -> 0.000001
//	1e-7     -> 1e-7
//	0.333... -> 0.3333333333333333
//
// 需要修正的差异：
//   - 负零：Go 输出 -0，JS 输出 0
func writeNumber(b *strings.Builder, f float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// JSON 无 NaN/Infinity 字面量。JS 的 JSON.stringify 输出 null；
		// oracle (stableStringify) 走 JSON.stringify(v) 分支同样得 null。
		b.WriteString("null")
		return
	}
	// 负零归一：JS 的 (-0).toString() === '0'
	if f == 0 {
		b.WriteString("0")
		return
	}
	b.WriteString(formatJSNumber(f))
}

// formatJSNumber 复现 ECMAScript Number::toString(10) 的格式化。
//
// ECMAScript 规则（按 |x| 分段）：
//   - 1e-6 <= |x| < 1e21 → 定点表示（'f'）：0.000001 / 100000000000000000000
//   - 否则                → 指数表示（'e'）：1e-7 / 1e+21
//
// 关键：strconv.FormatFloat 的 'g' 格式**不能**直接用——它的阈值与 ES 不同
// （1e-6 会被输出成 1e-06 而非 0.000001），且指数部分带前导零（1e-07）。
// 这是实测发现的标准库差异；encoding/json 内部有等价的 ES6 逻辑，strconv 没有。
//
// 证据：TestOracleParity 的 float_1e-6 / float_1e-7 用例（对照 TS 版实测输出）。
func formatJSNumber(f float64) string {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	s := strconv.FormatFloat(f, format, -1, 64)
	if format == 'e' {
		// 归一 e-07 → e-7：JS 指数不写前导零。
		// Go 的 'e' 格式固定两位指数（1e-07），正指数带 '+'（1e+21，无需处理）。
		if n := len(s); n >= 4 && s[n-4] == 'e' && s[n-3] == '-' && s[n-2] == '0' {
			s = s[:n-2] + s[n-1:]
		}
	}
	return s
}

// writeString 按 JSON.stringify 的转义规则输出字符串字面量。
//
// 与 encoding/json 的差异：
//   - 不转义 & < >（encoding/json 默认转义为 \u0026 等）
//   - 不转义 U+2028/U+2029（encoding/json 会转义，JSON.stringify 不会）
//   - 控制字符 0x00–0x1f 用 \uXXXX 短转义（\b \t \n \f \r 用单字符转义）
//
// 注意：JSON.stringify 对孤立代理项（lone surrogate）会输出 \udXXX 转义。
// Go 的 string 是合法 UTF-8，无法携带孤立代理项；若上游数据含此类值，
// 应在进入本层前处理。此处对非法 UTF-8 序列输出 U+FFFD（与 encoding/json 同）。
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			// ASCII 快路径
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
					// 含 & < > 也不转义（JSON.stringify 行为）
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 非法 UTF-8 字节：替换为 U+FFFD
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		// U+2028 / U+2029：JSON.stringify 原样输出，不转义
		b.WriteString(s[i : i+size])
		i += size
	}
	b.WriteByte('"')
}

const hexDigits = "0123456789abcdef"

// writeHexEscape 输出 \uXXXX（小写十六进制，与 JSON.stringify 一致）。
func writeHexEscape(b *strings.Builder, r uint16) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xF])
	b.WriteByte(hexDigits[(r>>8)&0xF])
	b.WriteByte(hexDigits[(r>>4)&0xF])
	b.WriteByte(hexDigits[r&0xF])
}
