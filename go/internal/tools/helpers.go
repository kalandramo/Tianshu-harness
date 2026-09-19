package tools

import (
	"sort"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/contract"
)

// objSchema 构造一个 object 型入参 schema。
//
// 参数顺序在 map 中不保证，但 schema 进请求体时由 wire.OrderedMap 保持
// 插入序——本辅助用有序构造保证字节稳定。
func objSchema(props map[string]any, required ...string) *contract.InputSchema {
	return &contract.InputSchema{
		Type:       "object",
		Properties: props,
		Required:   required,
	}
}

// objSchemaOrdered 构造带**声明序**的 object 型入参 schema。
//
// `propOrder` 必须逐字对账 TS 侧 schema 的属性声明序——键序不同会让
// 工具定义变化打掉整个前缀缓存（src/api/openai-client.ts:630）。
// oracle：go/testdata/toolschema/oracle.json 的 `propOrder` 字段。
func objSchemaOrdered(propOrder []string, props map[string]any, required ...string) *contract.InputSchema {
	return &contract.InputSchema{
		Type:       "object",
		Properties: props,
		PropOrder:  propOrder,
		Required:   required,
	}
}

// strProp 构造一个 string 型属性（键序 type → description，对账 TS）。
//
// **必须返回有序结构**：`wire.writeValue` 对 `map[string]any` 会**排序键**，
// 产出 `description → type`（TS 是 `type → description`）——字节不等价。
func strProp(desc string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("type", "string").Set("description", desc)
}

// intProp 构造一个 integer 型属性（键序 type → description）。
func intProp(desc string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("type", "integer").Set("description", desc)
}

// intPropMin 构造带 minimum 约束的 integer 属性（键序 type → minimum → description）。
func intPropMin(desc string, min int) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("type", "integer").Set("minimum", min).Set("description", desc)
}

// numProp 构造一个 number 型属性（JSON Schema 的 number，非 integer）。
func numProp(desc string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("type", "number").Set("description", desc)
}

// enumPropOrdered 构造带枚举的 string 属性，**键序对账 TS**（type → enum → description）。
func enumPropOrdered(desc string, values []string) *wire.OrderedMap {
	vals := make([]any, len(values))
	for i, v := range values {
		vals[i] = v
	}
	om := wire.NewOrderedMap().
		Set("type", "string").
		Set("enum", vals)
	// 空描述时**省略该键**（对账 TS：无 description 的属性不输出该键）
	if desc != "" {
		om.Set("description", desc)
	}
	return om
}

// boolProp 构造一个 boolean 型属性（键序 type → description）。
func boolProp(desc string) *wire.OrderedMap {
	return wire.NewOrderedMap().Set("type", "boolean").Set("description", desc)
}

// arrayProp 构造一个数组型属性（键序 type → description → items）。
func arrayProp(desc, itemType string) *wire.OrderedMap {
	return wire.NewOrderedMap().
		Set("type", "array").
		Set("description", desc).
		Set("items", wire.NewOrderedMap().Set("type", itemType))
}

// arrayPropOrdered 构造数组型属性，**键序对账 TS**（type → items → description）。
//
// 为什么需要单独的构造器：TS 侧 `file_paths` 的字面量序是
// `type, items, description`（items 在 description **前**），与 `arrayProp`
// 的 `type, description, items` 不同。嵌套键序同样进请求体、同样影响缓存。
func arrayPropOrdered(desc, itemType string) *wire.OrderedMap {
	return wire.NewOrderedMap().
		Set("type", "array").
		Set("items", wire.NewOrderedMap().Set("type", itemType)).
		Set("description", desc)
}

// arrProp 构造一个「元素为 object」的数组型属性（todos 等）。
//
// items 用**平铺 map** 表达（而非嵌套 *contract.InputSchema）——
// wire.writeValue 不认 *InputSchema，且 orderedProps 只递归 map[string]any
// 与 []any。用平铺 map 才能安全穿过序列化路径。
func arrProp(desc string, items any) *wire.OrderedMap {
	return wire.NewOrderedMap().
		Set("type", "array").
		Set("description", desc).
		Set("items", items)
}

// arrPropOrdered 构造「元素为 object」的数组属性，**键序对账 TS**
// （type → description → items）。
func arrPropOrdered(desc string, items *wire.OrderedMap) *wire.OrderedMap {
	return wire.NewOrderedMap().
		Set("type", "array").
		Set("description", desc).
		Set("items", items)
}

// objPropMapOrdered 构造 object 型 items（供 arrPropOrdered 用），带声明序。
func objPropMapOrdered(propOrder []string, props map[string]any, required ...string) *wire.OrderedMap {
	m := wire.NewOrderedMap().Set("type", "object")
	om := wire.NewOrderedMap()
	for _, k := range propOrder {
		if v, ok := props[k]; ok {
			om.Set(k, v)
		}
	}
	m.Set("properties", om)
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m.Set("required", req)
	}
	return m
}

// objPropMap 构造一个「object 型」的平铺 map（供 arrProp 的 items 用）。
func objPropMap(props map[string]any, required ...string) *wire.OrderedMap {
	m := wire.NewOrderedMap().Set("type", "object")
	om := wire.NewOrderedMap()
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		om.Set(k, props[k])
	}
	m.Set("properties", om)
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m.Set("required", req)
	}
	return m
}

// enumProp 构造一个带枚举约束的 string 型属性（键序 type → description → enum）。
func enumProp(desc string, values []string) *wire.OrderedMap {
	vals := make([]any, len(values))
	for i, v := range values {
		vals[i] = v
	}
	return wire.NewOrderedMap().
		Set("type", "string").
		Set("description", desc).
		Set("enum", vals)
}

// baseTool 是工具的公共实现——各工具嵌入它以避免重复样板。
type baseTool struct {
	def        contract.Definition
	enabled    bool
	concurrent bool
	timeout    int // 秒；0 = 默认
}

// Definition 返回工具声明。
func (b *baseTool) Definition() contract.Definition { return b.def }

// ConcurrencySafe 报告可否并发。
func (b *baseTool) ConcurrencySafe() bool { return b.concurrent }

// Enabled 报告是否可用。
func (b *baseTool) Enabled() bool { return b.enabled }

// RequiresApproval 默认不要求批准。需要批准的工具覆写此方法。
func (b *baseTool) RequiresApproval(*CallParams) bool { return false }

// timeoutsFor 返回工具超时（秒），0 表示用默认。
func (b *baseTool) timeoutSeconds() int { return b.timeout }
