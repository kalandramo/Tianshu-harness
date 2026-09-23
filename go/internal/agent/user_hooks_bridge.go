// user-hooks-bridge：把 user hooks runner 桥接成 runtime hook。
//
// 对账 TS 的 `src/agent/hooks/user-hooks-bridge.ts`（108 行）。
//
// ## 结构（对账 TS）
//
// 一个 `Runner` → 4 个 runtime hook（preTurn / postTurn / postTool /
// postSession）+ 一个独立的 `RunOnError`（不走 phase 管线，对账 TS 的
// `runOnErrorHooks`）。
//
// ## 与 TS 的差异（显式）
//
//  1. **`emitHookResult` → `ResultSink` 回调**：TS 把结果送到桌面端事件流
//     （I4 的 `hook_result` 事件）。Go 侧无桌面端——结果经 sink 回调暴露，
//     CLI 可选择消费（缺省 nil = 丢弃）。**不静默**：hook 脚本的 stdout/stderr
//     本身已由 Runner 捕获并交给 sink。
//  2. **无 `getPluginHooks`**：TS 惰性读取插件贡献的 hook（插件在 agent 装配
//     之后加载）。Go 侧无 plugins 子系统。
//  3. **`onError` 的触发点**：TS 挂在 pipeline 的 `onError` 上（**hook 自身失败**
//     时触发，不是 run 崩溃时——见 loop-factory.ts:977）。Go 侧对应点是
//     `PipelineOptions.OnError`，签名一致（`func(RuntimeHookError)`）。
package agent

import (
	"context"
	"fmt"

	"github.com/kalandramo/tianshu/go/internal/hooks"
)

// UserHooksDeps 是 bridge 的依赖（对账 TS 的 `UserHooksBridgeDeps`）。
type UserHooksDeps struct {
	Cwd       string
	SessionID string
	// GetTurn 惰性读取当前轮次（对账 TS 的 `getTurn: () => number`）。
	GetTurn func() int
	// Sink 接收执行结果（可选；nil = 不消费）。
	Sink hooks.ResultSink
}

// newUserHookRunner 构造一个 Runner（每次调用新建，共享同一份 deps）。
func newUserHookRunner(deps UserHooksDeps) *hooks.Runner {
	return &hooks.Runner{
		Cwd:       deps.Cwd,
		SessionID: deps.SessionID,
		GetTurn:   deps.GetTurn,
		Sink:      deps.Sink,
	}
}

// emitUserHookResults 把结果交给 sink（对账 TS 的 `emit` 闭包）。
func emitUserHookResults(deps UserHooksDeps, results []hooks.HookResult, ev hooks.HookEvent, toolName, errMsg string) {
	if deps.Sink == nil {
		return
	}
	deps.Sink(results, ev, deps.GetTurn(), toolName, errMsg)
}

// CreateUserHooksBridge 构造 4 个 runtime hook（对账 TS 的 `createUserHooksBridge`）。
//
// **顺序对账 TS**：preTurn / postTurn / postTool / postSession。
func CreateUserHooksBridge(deps UserHooksDeps) []RuntimeHook {
	return []RuntimeHook{
		userHookForPhase(deps, hooks.EventPreTurn, PhasePreTurn),
		userHookForPhase(deps, hooks.EventPostTurn, PhasePostTurn),
		userHookForPhase(deps, hooks.EventPostTool, PhasePostTool),
		userHookForPhase(deps, hooks.EventPostSession, PhasePostSession),
	}
}

// userHookForPhase 构造单阶段的 bridge hook。
//
// **注意 postTool 的额外上下文**（对账 TS）：
//
//	run({ ...base, event: 'postTool', turn, toolName: tool.name,
//	      toolResult: tool.success ? 'success' : 'failure' })
//
// `toolResult` 是**字符串** 'success'/'failure'（不是布尔）——TS 即如此，
// 保持逐字对账。
func userHookForPhase(deps UserHooksDeps, ev hooks.HookEvent, phase RuntimeHookPhase) RuntimeHook {
	return RuntimeHook{
		Name:  "user-hooks-" + string(ev),
		Phase: phase,
		Run: func(_ context.Context, _ *RuntimeHookContext, tool *RuntimeToolEvent) error {
			runner := newUserHookRunner(deps)
			ctx := hooks.HookContext{
				Event:     ev,
				Cwd:       deps.Cwd,
				SessionID: deps.SessionID,
				Turn:      deps.GetTurn(),
			}
			toolName := ""
			if ev == hooks.EventPostTool && tool != nil {
				toolName = tool.Name
				if tool.Success {
					ctx.ToolResult = "success"
				} else {
					ctx.ToolResult = "failure"
				}
			}
			results := runner.RunForEvent(ctx)
			emitUserHookResults(deps, results, ev, toolName, "")
			return nil
		},
	}
}

// RunOnErrorHooks 执行 `onError` 事件的用户 hook。
//
// 对账 TS 的 `runOnErrorHooks`——**不走 phase 管线**，由 pipeline 的
// `OnError` 回调直接调用（hook 自身失败时）。
//
// **TS 的守卫**：`if (!deps.emitHookResult) return`——无 sink 时**直接返回，
// 不执行脚本**。Go 侧保持同样语义：sink 为 nil 时是 no-op。
//
// 这是**刻意的**：onError 是诊断通道，若无人消费结果，跑脚本只是浪费
// （且可能递归——脚本失败又触发 onError）。
func RunOnErrorHooks(deps UserHooksDeps, errMsg string) {
	if deps.Sink == nil {
		return
	}
	runner := newUserHookRunner(deps)
	results := runner.RunForEvent(hooks.HookContext{
		Event:     hooks.EventOnError,
		Cwd:       deps.Cwd,
		SessionID: deps.SessionID,
		Turn:      deps.GetTurn(),
		Error:     errMsg,
	})
	emitUserHookResults(deps, results, hooks.EventOnError, "", errMsg)
}

// UserHooksErrorSink 把 RunOnErrorHooks 适配成 PipelineOptions.OnError 的签名。
//
// **为什么需要适配**：`PipelineOptions.OnError` 收 `RuntimeHookError`
// （含 Phase/HookName/Message/Err），而 user hooks 只关心消息文本。
//
// **递归防护（重要）**：user hook 脚本自身失败也会走 `reportError` → 若此处
// 再触发 onError hook，会形成**无限递归**（脚本失败 → 跑 onError 脚本 →
// 该脚本也失败 → ...）。
//
// 防护：**只对非 user-hooks 来源的错误触发**。user-hooks 的 hook 名以
// `user-hooks-` 开头——它们的失败不再回灌。
func UserHooksErrorSink(deps UserHooksDeps) func(RuntimeHookError) {
	return func(e RuntimeHookError) {
		// 递归防护：user-hooks 自身的失败不再触发 onError 链。
		if len(e.HookName) >= 11 && e.HookName[:11] == "user-hooks-" {
			return
		}
		msg := e.Message
		if e.Err != nil {
			msg = fmt.Sprintf("%s: %v", e.Message, e.Err)
		}
		RunOnErrorHooks(deps, msg)
	}
}
