package agent

// turnbudget.go —— 单轮工具结果预算 + 耗尽时的 `<stored>` 包装。
//
// 对账 TS `src/agent/turn-budget.ts`（25 行纯逻辑）与
// `tool-pipeline.ts:1609-1630` 的预算消费 / `<stored>` 包装段。
//
// # 为什么需要
//
// 单轮里工具结果可能累积到撑爆上下文。预算把"这一轮已吃进多少 token"记账，
// 耗尽后把后续工具结果换成 `<stored ref="<rawPath>" chars=N tool="...">` 短引用
// ——**模型据 ref 用 read_file 取回全文**，而不是让长输出继续灌进历史。
//
// # 与 rawPath / uiContent 的关系（本刀解锁的两项）
//
// - `rawPath` 的**真实消费者**就在这里（`refPath := rawToolResult?.rawPath ?? "unknown"`）
//   ——不是 TUI 渲染层。故本刀让 `contract.Result.RawPath` 有了生产读取方。
// - `uiContent` 的消费者是展示优先级 `displayContent = uiContent ?? result`
//   （纯取值逻辑，不需要 ANSI 渲染层）。
//
// # scope（明示未做）
//
// **不做** `src/tui/engine/`（13,684 行：ANSI 渲染、终端帧、事件循环）——
// 那是独立的大模块。本刀只做**逻辑层**：预算记账 + 包装 + 展示优先级。

import (
	"fmt"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// 对账 TS `turn-budget.ts` 的三个常量。
const (
	// BaseBudgetTokens 对账 TS `BASE_BUDGET_TOKENS`。
	BaseBudgetTokens = 50_000
	// PressureBudgetTokens 对账 TS `PRESSURE_BUDGET_TOKENS`。
	PressureBudgetTokens = 25_000
	// criticalRSSRatio 对账 TS `CRITICAL_RSS_RATIO`。
	criticalRSSRatio = 0.85
	// pressureRSSRatio 对账 TS 里 `rssRatio >= 0.7` 的阈值。
	pressureRSSRatio = 0.7
)

// storedPreviewChars 对账 TS 的 `finalContent.slice(0, 500)`。
const storedPreviewChars = 500

// TurnBudget 是单轮工具结果的 token 预算。
//
// 对账 TS `TurnBudget` 接口。**值语义**（Go 无 getter 技巧，用指针方法）。
type TurnBudget struct {
	// MaxTokensPerTurn 是本轮预算上限；**0 表示已耗尽**（对账 TS：
	// rssRatio ≥ 0.85 时 maxTokensPerTurn = 0，`used >= 0` 恒真）。
	MaxTokensPerTurn int
	usedTokens       int
}

// CreateTurnBudget 按内存压力比选档（对账 TS `createTurnBudget`）。
//
// 三档（**顺序敏感**）：
//   - `rssRatio >= 0.85` → **0**（内存危急：立即视为耗尽）
//   - `rssRatio >= 0.7`  → 25,000（压力）
//   - 否则               → 50,000（基线）
func CreateTurnBudget(rssRatio float64) *TurnBudget {
	max := BaseBudgetTokens
	switch {
	case rssRatio >= criticalRSSRatio:
		max = 0
	case rssRatio >= pressureRSSRatio:
		max = PressureBudgetTokens
	}
	return &TurnBudget{MaxTokensPerTurn: max}
}

// UsedTokens 返回已消费的 token 数。
func (b *TurnBudget) UsedTokens() int { return b.usedTokens }

// IsExhausted 报告预算是否已耗尽。
//
// **注意 `max == 0` 的情形**：`used(0) >= 0` 为真 → 立即耗尽。
// 这不是边界 bug，是对账 TS 的**有意设计**（内存危急时不再放任何工具结果）。
func (b *TurnBudget) IsExhausted() bool { return b.usedTokens >= b.MaxTokensPerTurn }

// Consume 记入本次消费（对账 TS `consume`）。
func (b *TurnBudget) Consume(tokens int) { b.usedTokens += tokens }

// Reset 清零已用量（对账 TS `reset`）——每轮开始时调用。
func (b *TurnBudget) Reset() { b.usedTokens = 0 }

// BudgetFraction 返回剩余预算占比（对账 TS 的 `budgetFraction` 算式）。
//
// `max <= 0` 时返回 1（对账 TS：`maxTokensPerTurn > 0 ? ... : 1`）。
func (b *TurnBudget) BudgetFraction() float64 {
	if b.MaxTokensPerTurn <= 0 {
		return 1
	}
	return 1 - float64(b.usedTokens)/float64(b.MaxTokensPerTurn)
}

// WrapStoredIfExhausted 在预算耗尽时把工具结果换成 `<stored>` 短引用。
//
// 对账 TS `tool-pipeline.ts:1622-1628`：
//
//	consume(ceil(len/4))
//	if isExhausted() {
//	  preview = content.slice(0, 500)
//	  refPath = rawPath ?? 'unknown'
//	  content = `<stored ref="${refPath}" chars=${contentChars} tool="${name}">\n${preview}\n...(turn budget exceeded ...)</stored>`
//	}
//
// **三个对账细节**：
//  1. `chars` 是**包装前**的长度（TS 先算 `contentChars = finalContent.length` 再替换）。
//  2. `refPath` 为空时用字面量 **`"unknown"`**——不是省略该属性。
//  3. preview 用 `slice(0, 500)`（UTF-16 语义 → 用 `UTF16Len`/切片辅助）。
//
// 返回 (结果, 是否包装)。
func WrapStoredIfExhausted(b *TurnBudget, name, content, rawPath string) (string, bool) {
	// 消费：`Math.ceil(content.length / 4)`。
	b.Consume((utf16Len(content) + 3) / 4)

	if !b.IsExhausted() {
		return content, false
	}
	preview := jsSliceHead(content, storedPreviewChars)
	refPath := rawPath
	if refPath == "" {
		refPath = "unknown"
	}
	wrapped := fmt.Sprintf("<stored ref=%q chars=%d tool=%q>\n%s\n...(turn budget exceeded — use read_file with offset/limit for full content)</stored>",
		refPath, utf16Len(content), name, preview)
	return wrapped, true
}

// DisplayContent 返回 UI 展示内容（对账 TS `displayContent = uiContent ?? result`）。
//
// **这就是 `uiContent` 的消费者**——纯取值优先级：uiContent 非空则用它，
// 否则回退 result。不需要 ANSI 渲染层即可验证。
func DisplayContent(uiContent, result string) string {
	if uiContent != "" {
		return uiContent
	}
	return result
}

// jsSliceHead 对账 JS 的 `s.slice(0, n)`：`n <= 0` → 空串；`n >= len` → 全串。
//
// **为什么本包内实现而非用 tools 的**：`tools.jsSliceHead` 是**未导出**的；
// 为一个小需求把它提升为导出 API 会扩大 `tools` 的公开面。此处 5 行等价实现，
// 语义与 `tools/truncation.go:150` 逐字一致（该处已有差分对账）。
func jsSliceHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx]
		}
		i++
	}
	return s
}

// UTF16Len 对账 JS 的 `.length`（UTF-16 code unit 计数）。
//
// 复用 `tools` 的实现——JS 字符串语义在本仓库应有单一来源。
var utf16Len = tools.UTF16Len
