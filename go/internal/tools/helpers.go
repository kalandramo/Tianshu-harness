package tools

import (
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

// strProp 构造一个 string 型属性。
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// intProp 构造一个 integer 型属性。
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// boolProp 构造一个 boolean 型属性。
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// arrayProp 构造一个数组型属性。
func arrayProp(desc, itemType string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]any{"type": itemType},
	}
}

// arrProp 构造一个「元素为 object」的数组型属性（todos 等）。
//
// items 用**平铺 map** 表达（而非嵌套 *contract.InputSchema）——
// wire.writeValue 不认 *InputSchema，且 orderedProps 只递归 map[string]any
// 与 []any。用平铺 map 才能安全穿过序列化路径。
func arrProp(desc string, items map[string]any) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       items,
	}
}

// objPropMap 构造一个「object 型」的平铺 map（供 arrProp 的 items 用）。
func objPropMap(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

// enumProp 构造一个带枚举约束的 string 型属性。
func enumProp(desc string, values []string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": desc,
		"enum":        values,
	}
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
