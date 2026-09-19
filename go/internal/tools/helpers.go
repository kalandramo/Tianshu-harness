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
