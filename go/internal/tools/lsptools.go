package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// LspNavigator 是 `lsp_goto_definition` / `lsp_find_references` 依赖的 LSP 能力面。
//
// # 为什么用接口而非直接 import internal/lsp
//
// `internal/lsp` 不依赖 `internal/tools`，而本包**已经**依赖 `contract` 与
// 多个子系统。若本包直接 import `internal/lsp`，本身无环——但 LSP 的
// **装配**（spawn 语言服务器、按扩展名路由）属运行时设施，与工具层职责不同；
// 且 `internal/lsp` 的 manager 需要 cwd 与 spawn 缝，由上层（agent/CLI 装配）
// 构造后注入更自然。
//
// 用接口的额外好处：测试无需真 server（注入假实现即可），
// 与 TS 侧把 `lspManager` 经 `ToolCallParams` 注入的形状一致。
type LspNavigator interface {
	// IsReady 报告 LSP 子系统是否可用（决定工具是否出现在模型可见列表里）。
	IsReady() bool
	// SupportsDefinition 报告是否支持定义跳转。
	SupportsDefinition() bool
	// SupportsReferences 报告是否支持引用查找。
	SupportsReferences() bool
	// GotoDefinition 返回定义位置（相对路径形式）。
	GotoDefinition(filePath string, line, column int) ([]LspLocation, error)
	// FindReferences 返回引用位置。
	FindReferences(filePath string, line, column int) ([]LspLocation, error)
}

// LspLocation 是 LSP 返回的位置（与 `internal/lsp.Location` 同形）。
//
// **为什么在本包重声明**：避免 `tools → lsp` 的 import（见 LspNavigator 说明）。
// 上层装配时做一次字段拷贝即可；两侧都用 json tag 对齐同一协议形态。
type LspLocation struct {
	URI   string   `json:"uri"`
	Range LspRange `json:"range"`
}

// LspRange 是行列区间（0-based，LSP 原生语义）。
type LspRange struct {
	Start LspPosition `json:"start"`
	End   LspPosition `json:"end"`
}

// LspPosition 是 0-based 行列。
type LspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// ── lsp_goto_definition ──────────────────────────────────────────

type lspGotoTool struct {
	nav LspNavigator
}

// GotoDefinition 创建 `lsp_goto_definition` 工具。
//
// 对账 TS `createGotoDefinitionTool`（`src/lsp/tools.ts:14`）。
func GotoDefinition(nav LspNavigator) Tool { return &lspGotoTool{nav: nav} }

func (t *lspGotoTool) Definition() contract.Definition {
	return contract.Definition{
		Name:        "lsp_goto_definition",
		Description: "跳转到给定文件位置符号的定义。返回定义的文件路径、行号和列号。用于理解函数、类、变量或类型的定义位置。",
		InputSchema: objSchemaOrdered(
			// PropOrder 是**声明序**——进请求体，顺序影响前缀缓存（对账 TS 的对象字面量序）
			[]string{"file_path", "line", "column"},
			map[string]any{
				"file_path": strProp("包含该符号的源文件路径"),
				"line":      numProp("符号所在行号（从 1 开始）"),
				"column":    numProp("符号所在列号（从 0 开始）"),
			},
			"file_path", "line", "column",
		),
	}
}

func (t *lspGotoTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	filePath, line, column, errMsg := resolveLspParams(p.Input)
	if errMsg != "" {
		return contract.Result{Content: errMsg, IsError: true}, nil
	}
	if t.nav == nil {
		return contract.Result{Content: lspUnavailableNote, IsError: true}, nil
	}

	locs, err := t.nav.GotoDefinition(filePath, line, column)
	if err != nil {
		return contract.Result{}, err
	}
	if len(locs) == 0 {
		// 对账 TS：零结果文案用**入参原值**（不转换）
		return contract.Result{Content: fmt.Sprintf(
			"No definition found for symbol at %s:%d:%d", filePath, line, column)}, nil
	}
	return contract.Result{Content: formatLspLocations(locs, "definition")}, nil
}

// RequiresApproval 恒 false（对账 TS `requiresApproval(): false`）——只读导航。
func (t *lspGotoTool) RequiresApproval(_ *CallParams) bool { return false }

// ConcurrencySafe 恒 true（对账 TS `isConcurrencySafe(): true`）。
func (t *lspGotoTool) ConcurrencySafe() bool { return true }

// Enabled 对账 TS：`manager.isReady() && manager.supportsDefinition()`。
//
// ★ **这是本工具「不做也不会坏」的另一种体现**：server 不可用时它**不出现在
// 模型可见的工具列表**里（`Definitions()` 过滤 `Enabled()`），行为等价于
// 「工具不存在」——与 Go 侧移植前的状态一致，故渐进上线是安全的。
func (t *lspGotoTool) Enabled() bool {
	return t.nav != nil && t.nav.IsReady() && t.nav.SupportsDefinition()
}

// Timeout 用默认（0）——LSP 请求自带 45s 超时，无需工具级超时叠加。
func (t *lspGotoTool) Timeout(_ *CallParams) time.Duration { return 0 }

// ── lsp_find_references ──────────────────────────────────────────

type lspRefsTool struct {
	nav LspNavigator
}

// FindReferences 创建 `lsp_find_references` 工具。
//
// 对账 TS `createFindReferencesTool`（`src/lsp/tools.ts:74`）。
func FindReferences(nav LspNavigator) Tool { return &lspRefsTool{nav: nav} }

func (t *lspRefsTool) Definition() contract.Definition {
	return contract.Definition{
		Name:        "lsp_find_references",
		Description: "查找给定文件位置符号的所有引用。返回符号被使用的文件路径、行号和列号列表。用于理解修改函数、类或变量的影响范围。",
		InputSchema: objSchemaOrdered(
			// PropOrder 是**声明序**——进请求体，顺序影响前缀缓存（对账 TS 的对象字面量序）
			[]string{"file_path", "line", "column"},
			map[string]any{
				"file_path": strProp("包含该符号的源文件路径"),
				"line":      numProp("符号所在行号（从 1 开始）"),
				"column":    numProp("符号所在列号（从 0 开始）"),
			},
			"file_path", "line", "column",
		),
	}
}

func (t *lspRefsTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	filePath, line, column, errMsg := resolveLspParams(p.Input)
	if errMsg != "" {
		return contract.Result{Content: errMsg, IsError: true}, nil
	}
	if t.nav == nil {
		return contract.Result{Content: lspUnavailableNote, IsError: true}, nil
	}

	locs, err := t.nav.FindReferences(filePath, line, column)
	if err != nil {
		return contract.Result{}, err
	}
	if len(locs) == 0 {
		return contract.Result{Content: fmt.Sprintf(
			"No references found for symbol at %s:%d:%d", filePath, line, column)}, nil
	}
	return contract.Result{Content: formatLspLocations(locs, "reference")}, nil
}

func (t *lspRefsTool) RequiresApproval(_ *CallParams) bool { return false }
func (t *lspRefsTool) ConcurrencySafe() bool               { return true }
func (t *lspRefsTool) Enabled() bool {
	return t.nav != nil && t.nav.IsReady() && t.nav.SupportsReferences()
}
func (t *lspRefsTool) Timeout(_ *CallParams) time.Duration { return 0 }

// lspUnavailableNote 在**工具被强制调用**（Enabled 已短路的路径之外，
// 如测试直调）时给出明确说明——不静默返回「未找到」，那会让模型以为
// 符号真的没有定义。
const lspUnavailableNote = "LSP 子系统不可用（未检测到已安装的语言服务器）。" +
	"该工具在不可用时不会出现在可用工具列表中。"

// resolveLspParams 校验入参。
//
// 对账 TS `resolveParams`（`src/lsp/tools.ts:4-12`）——**三处文案逐字**：
//
//	缺 file_path      → "Missing required parameter: file_path"
//	line 非数或 <1    → "Missing or invalid parameter: line (must be >= 1)"
//	column 非数或 <0  → "Missing or invalid parameter: column (must be >= 0)"
func resolveLspParams(input map[string]any) (filePath string, line, column int, errMsg string) {
	if input == nil {
		return "", 0, 0, "Missing required parameter: file_path"
	}
	fp, ok := input["file_path"].(string)
	if !ok || fp == "" {
		return "", 0, 0, "Missing required parameter: file_path"
	}
	ln, ok := asNumber(input["line"])
	if !ok || ln < 1 {
		return "", 0, 0, "Missing or invalid parameter: line (must be >= 1)"
	}
	col, ok := asNumber(input["column"])
	if !ok || col < 0 {
		return "", 0, 0, "Missing or invalid parameter: column (must be >= 0)"
	}
	return fp, ln, col, ""
}

// asNumber 把 JSON 数值（float64）或整型转为 int。
//
// **为什么需要**：JSON 反序列化把数字给成 `float64`，而测试/内部调用可能传
// `int`。两者都要接受（TS 侧只有 number，无此区分）。
func asNumber(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

// formatLspLocations 格式化位置列表。
//
// 对账 TS（`tools.ts:52-60` / `:112-120`）：
//
//	content: `${locations.length} ${kind}(s) found:\n${formatted}`
//	  formatted = locations.map(loc => `${loc.uri}:${line + 1}:${character}`).join('\n')
//
// 注意：**行号 +1**（LSP 0-based → 展示 1-based），**列号不加**（TS 同）。
// `(s)` 是字面量（不复数变形）。
func formatLspLocations(locs []LspLocation, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s(s) found:\n", len(locs), kind)
	for i, loc := range locs {
		if i > 0 {
			b.WriteByte('\n')
		}
		// 对账 TS：`loc.range.start.line + 1` 与 `loc.range.start.character`（不加 1）
		fmt.Fprintf(&b, "%s:%d:%d", loc.URI, loc.Range.Start.Line+1, loc.Range.Start.Character)
	}
	return b.String()
}
