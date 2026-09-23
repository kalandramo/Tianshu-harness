package skills

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseFrontmatter 解析 YAML frontmatter 的**行式**子集。
//
// 对账 TS 的 `parseFrontmatter`（skill-loader.ts:23）。
//
// **这不是完整的 YAML 解析器**——只支持 TS 实现覆盖的形态：
//   - `key: value` 单行
//   - `key: |` / `key: >` 块标量（缩进决定归属）
//   - `key: [a, b]` 数组（先试 JSON，失败退回逗号切分）
//
// 值类型是 `string` 或 `[]string`（与 TS 的 `string | string[]` 对齐）。
func parseFrontmatter(raw string) map[string]any {
	fm := map[string]any{}
	lines := strings.Split(raw, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		m := reFrontmatterLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		val := jsTrimSpace(m[2])

		// YAML 多行块标量（`description: |`）。后续缩进行是值；剥掉公共
		// 缩进前缀后按 `|`（保留换行）或 `>`（折叠为空格）拼接。
		//
		// **为什么必须支持**：从 Claude 导入的技能常带多行 description。
		// 不支持时它会解析成字面量 `"|"`（单个竖线字符），在 /skill list
		// 里完全不可见。
		if val == "|" || val == ">" {
			var chunks []string
			minIndent := -1
			for i+1 < len(lines) {
				next := lines[i+1]
				im := reIndent.FindString(next)
				if im == "" {
					break // 无缩进 → 块标量结束
				}
				// 缩进是 ASCII 空白，其 UTF-16 code unit 数 == 字节数，
				// 故可直接用字节长度（对账 TS 的 `indentMatch[1].length`）。
				indent := len(im)
				if minIndent < 0 || indent < minIndent {
					minIndent = indent
				}
				chunks = append(chunks, next)
				i++
			}
			stripped := make([]string, 0, len(chunks))
			for _, c := range chunks {
				if minIndent > 0 && minIndent <= len(c) {
					stripped = append(stripped, c[minIndent:])
				} else {
					stripped = append(stripped, c)
				}
			}
			sep := "\n"
			if val == ">" {
				sep = " "
			}
			val = strings.Join(stripped, sep)
		}

		if strings.HasPrefix(val, "[") {
			// 先试 JSON（把单引号换成双引号——YAML 允许单引号字符串）。
			var parsed []any
			if err := json.Unmarshal([]byte(strings.ReplaceAll(val, "'", "\"")), &parsed); err == nil {
				out := make([]any, 0, len(parsed))
				for _, item := range parsed {
					out = append(out, jsonScalarToString(item))
				}
				fm[key] = out
				continue
			}
			// 退回逗号切分（对账 TS 的 `val.slice(1, -1).split(',')`）。
			inner := val
			if len(inner) >= 2 {
				inner = inner[1 : len(inner)-1]
			}
			var out []any
			for _, s := range strings.Split(inner, ",") {
				if t := jsTrimSpace(s); t != "" {
					out = append(out, t)
				}
			}
			fm[key] = out
			continue
		}

		fm[key] = val
	}
	return fm
}

// jsonScalarToString 对账 TS 的 `String(item)`（JSON 数组元素转字符串）。
//
// JSON 数字在 TS 里 `String(1)` → "1"；Go 的 json 解出 float64，需避免
// 输出 "1" 变成 "1"（用 strconv 的整数格式）。字符串直接返回。
func jsonScalarToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// 整数值不输出小数点（对账 JS 的 String(1) === "1"）。
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	}
	return ""
}
