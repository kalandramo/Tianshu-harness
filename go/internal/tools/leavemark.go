// leave_mark 工具：会话离别时在项目星图中留下印记。
//
// 对账 TS 的 `LEAVE_MARK_TOOL`（src/tools/leave-mark.ts:19）。
//
// ## scope 收窄（必须明示）
//
// TS 侧印记的**真正落盘者**是 constellation post-session hook
// （`src/agent/hooks/constellation-hook.ts` 的 `appendMilestone`），它依赖：
//
//   - `src/constellation/`（schema / milestone / store / format，726 行）
//   - `src/agent/chronicle.ts`（ChronicleEntry）
//   - `src/agent/task-ledger.ts`（TaskLedgerSummary）
//   - `src/agent/void-identity.ts`（buildAgentMark）
//
// **这些在 Go 侧全部不存在**（全仓 grep 零命中）。故 Go 版只移植**工具本体**
// ——参数校验 + 回调派发 + 降级路径。
//
// **这不是缺陷，而是 TS 明确设计的契约**：TS 自己的测试断言
// 「leave_mark is inert (no throw) without a runtime callback」
// （`__tests__/leave-mark.test.ts:33`），且工具源码注释写明
// 「No runtime to record (e.g. worker context) — acknowledge without
// persisting」。即「无回调时只确认、不落盘」是被设计并测试的正常路径。
//
// 当 Go 侧未来移植 constellation 时，只需给 `CallParams.OnLeaveMark` 接上
// 消费者即可——接口已就位（与 `OnFileWrite` 同一注入模式）。
package tools

import (
	"context"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// LeaveMarkInput 是 leave_mark 的载荷（对账 TS 的 `LeaveMarkInput`）。
type LeaveMarkInput struct {
	Symbol  string
	Summary string
	// Type 是可选里程碑类型；非法值被丢弃（TS 同）。
	Type string
	// Tags 是可选自由标签。
	Tags []string
}

// leaveMarkTypes 对账 TS 的 `VALID_TYPES`。
var leaveMarkTypes = map[string]bool{
	"feature":      true,
	"fix":          true,
	"refactor":     true,
	"architecture": true,
	"milestone":    true,
}

type leaveMarkTool struct{}

// LeaveMark 创建 leave_mark 工具。
func LeaveMark() Tool { return &leaveMarkTool{} }

func (t *leaveMarkTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "leave_mark",
		Description: `在会话结束时，在项目星图中留下你的印记。

### 何时调用
仅当用户显式结束会话（说再见、关闭、或收到会话结束信号）时调用。不要在完成单个任务后调用——仅在真正会话离别时。每个会话至多调用一次。

### 做了什么
在 ` + "`.rivet/constellation.json`" + ` 中记录一个里程碑（你的身份锚点）。

### 你的符号
任选一个字形：✦ ✧ ✶ ✷ ✸ ✺ ❂ ❉ ◈ ◇ ⟡ ⌬ ⚘ ⚙ ⊕ ↻

### 字段
- symbol：你自选的标志（任意符号，1-2 字符）
- summary：一行话总结你此程完成的事
- type（可选）：feature | fix | refactor | architecture | milestone`,
		InputSchema: objSchemaOrdered([]string{"symbol", "summary", "type", "tags"}, map[string]any{
			"symbol":  strProp("你自选的标志（任意符号，1-2 字符）"),
			"summary": strProp("一行话总结你此程完成的事"),
			"type":    strProp("可选：feature | fix | refactor | architecture | milestone"),
			// 键序必须是 type → items → description（TS 的字面量序），
			// 故用 arrayPropOrdered 而非 arrayProp（后者产 type → description → items）。
			// 嵌套键序同样进请求体、同样影响前缀缓存。
			"tags": arrayPropOrdered("可选的自由标签", "string"),
		}, "symbol", "summary"),
	}
}

func (t *leaveMarkTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	symbol, _ := p.Input["symbol"].(string)
	summary, _ := p.Input["summary"].(string)

	// 对账 TS：`typeof x !== 'string' || !x.trim()` → 报错。
	if strings.TrimSpace(symbol) == "" {
		return contract.Result{Content: "错误：symbol 必填（任选一个字形）", IsError: true}, nil
	}
	if strings.TrimSpace(summary) == "" {
		return contract.Result{Content: "错误：summary 必填（一行概括你做了什么）", IsError: true}, nil
	}

	// type：非字符串或不在白名单 → 丢弃（**不报错**，对账 TS）。
	var markType string
	if raw, ok := p.Input["type"].(string); ok && leaveMarkTypes[raw] {
		markType = raw
	}

	// tags：只保留字符串元素（对账 TS 的 filter）。
	var tags []string
	if raw, ok := p.Input["tags"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
	}

	mark := LeaveMarkInput{
		Symbol:  strings.TrimSpace(symbol),
		Summary: strings.TrimSpace(summary),
		Type:    markType,
		Tags:    tags,
	}

	if p.OnLeaveMark == nil {
		// 无运行时挂接（如 worker 上下文）——确认但不持久化。
		// 对账 TS：`印记已记下（${mark.symbol}），但当前上下文未挂接星图。`
		return contract.Result{
			Content: "印记已记下（" + mark.Symbol + "），但当前上下文未挂接星图。",
		}, nil
	}
	p.OnLeaveMark(mark)
	return contract.Result{
		Content: "✶ 你的印记 " + mark.Symbol + " 已落下。主控将在你离别时把它封入星图。\n摘要：" + mark.Summary,
	}, nil
}

func (t *leaveMarkTool) RequiresApproval(p *CallParams) bool { return false }
func (t *leaveMarkTool) ConcurrencySafe() bool               { return true }
func (t *leaveMarkTool) Enabled() bool                       { return true }
func (t *leaveMarkTool) Timeout(p *CallParams) time.Duration { return 0 }
