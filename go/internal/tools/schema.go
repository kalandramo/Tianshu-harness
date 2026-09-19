package tools

import (
	"sort"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// orderedProps 把属性 map 转为**有序**结构（决定进请求体的键序）。
//
// **键序来源**：TS 侧 schema 是对象字面量（zod 亦保声明序），而工具定义
// 变化「打的是整个前缀（system+tools 段）」（src/api/openai-client.ts:630）
// ——键序不同会让前缀缓存**完全失效**。
//
// 故：优先用 `propOrder`（对账 TS 声明序）；未给出时退化为字典序
// （旧行为，**会破坏缓存**）。`propOrder` 里未列出的键（防御：声明漏了）
// 按字典序追加在尾部，保证不丢字段。
func OrderedProps(props map[string]any, propOrder []string) *wire.OrderedMap {
	om := wire.NewOrderedMap()
	seen := make(map[string]bool, len(props))

	for _, k := range propOrder {
		if _, ok := props[k]; ok && !seen[k] {
			om.Set(k, OrderValue(props[k]))
			seen[k] = true
		}
	}

	// 未在 propOrder 中声明的键（含 propOrder 为 nil 的全部情况）
	rest := make([]string, 0, len(props))
	for k := range props {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		sort.Strings(rest)
		for _, k := range rest {
			om.Set(k, OrderValue(props[k]))
		}
	}
	return om
}

// orderValue 递归把 schema 值转为有序结构。
//
// 必须处理 **[]any 内的 map**——数组型 schema（如 todo 的 todos）
// 的 items 是嵌套 object，不递归会让 wire.writeValue 遇到裸 map 而排序键，
// 破坏 schema 的字节稳定性；更糟的是遇到非 map 类型（如 *InputSchema）直接 panic。
func OrderValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		// **嵌套 object 的键序**：oracle 未覆盖（TS 侧嵌套 schema 极少），
		// 退化为字典序。若未来发现 TS 嵌套键序非字典序，需扩展 PropOrder
		// 为树形结构——见 HANDOFF 欠账。
		return OrderedProps(x, nil)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = OrderValue(e)
		}
		return out
	default:
		return v
	}
}
