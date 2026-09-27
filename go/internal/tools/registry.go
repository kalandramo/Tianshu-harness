// Package tools 实现工具内核：注册表、工具接口与核心工具。
//
// 对账 src/tools/registry.ts 与 src/tools/types.ts。
package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
	"github.com/kalandramo/tianshu/go/internal/skills"
)

// Tool 是一个可被模型调用的工具。
//
// 对账 TS 的 Tool 接口（src/tools/types.ts:437）：
//
//	interface Tool {
//	  definition; execute; requiresApproval;
//	  isConcurrencySafe; isEnabled; timeoutMs?
//	}
type Tool interface {
	// Definition 是模型可见的接口声明。
	Definition() contract.Definition
	// Execute 执行工具。
	Execute(ctx context.Context, p *CallParams) (contract.Result, error)
	// RequiresApproval 报告该次调用是否需要用户批准。
	RequiresApproval(p *CallParams) bool
	// ConcurrencySafe 报告该工具可否与其他工具并发执行。
	ConcurrencySafe() bool
	// Enabled 报告工具当前是否可用。
	Enabled() bool
	// Timeout 返回执行超时（0 = 用默认）。
	Timeout(p *CallParams) time.Duration
}

// CallParams 是一次工具调用的上下文与注入依赖。
//
// 对账 TS 的 ToolCallParams。为控制规模，Go 版先收录核心字段；
// 新增字段时保持「零值 = 缺席」的语义（对齐 TS 的可选字段）。
type CallParams struct {
	// Input 是模型给出的结构化入参。
	Input map[string]any
	// ToolUseID 是本次调用的唯一标识。
	ToolUseID string
	// Cwd 是工作目录。
	Cwd string
	// ApprovalMode 是当前会话审批档（仅少数工具按档分叉）。
	ApprovalMode string
	// OnOutput 接收流式输出分片。
	OnOutput func(chunk string)
	// OnFileWrite 登记工具内部写入的文件（让证据追踪感知）。
	OnFileWrite func(path string)
	// OnLeaveMark 接收 leave_mark 工具落下的离别印记。
	//
	// 对账 TS 的 `params.onLeaveMark`（`tool-pipeline.ts:363`）。
	// nil = 无运行时挂接（如 worker 上下文）——工具走降级路径
	// 「确认但不持久化」，与 TS 一致。
	//
	// **依赖方向**：`tools` 包内定义（LeaveMarkInput 在 leavemark.go），无跨包环。
	OnLeaveMark func(mark LeaveMarkInput)
	// OnAskUserQuestion 接收 ask_user_question 派发的结构化提问（供 TUI 开
	// 箭头选择器）。nil = 无 UI 挂接——工具仍返回占位符与 EndTurn，
	// 只是不做结构化派发。
	//
	// 对账 TS 的 `params.onAskUserQuestion`（ask-user-question.ts:213）。
	// **依赖方向**：`tools` 包内定义（AskUserQuestionInfo 在
	// askuserquestion.go），无跨包环。
	OnAskUserQuestion func(info AskUserQuestionInfo)
	// SkillRegistry 是 skill 工具的注册表（nil = 空注册表）。
	//
	// 对账 TS 的模块级单例 `skillRegistry`。Go 侧参数化注入，使测试可隔离
	// （TS 的测试靠共享单例 + beforeEach 重注册，有跨测试污染风险）。
	SkillRegistry *skills.Registry
	// OnSkillInvoked 在 skill 被加载时回调（供 UI / 遥测感知）。
	//
	// 对账 TS 的 `params.onSkillInvoked?.(skill.name)`。
	OnSkillInvoked func(name string)
	// OnSkillCompleted 在 skill 被标记完成时回调。
	//
	// 对账 TS 的 `params.onSkillCompleted?.(skill.name)`。
	OnSkillCompleted func(name string)
	// EnterPlanMode 把主 agent 切入计划模式（第七十九刀接线）。
	//
	// 对账 TS `params.enterPlanMode?.()`（`plan.ts:329`）。返回活动计划草稿路径
	// 与「是否已在计划模式」。
	//
	// **nil = 当前上下文不可用**（对账 TS 的 fail-closed）：子代理/worker
	// 不能把主代理切入计划模式——`plan` 工具会明确报错而非静默成功。
	EnterPlanMode func() (activePlanFilePath string, alreadyPlanning bool)
	// ExitPlanMode 退出计划模式（解除写限制）。
	//
	// 对账 TS `params.exitPlanMode?.()`（`plan.ts:363`）。正常流程审批即自动
	// 退出；本回调是**后备**——审批后系统未自动退出时手动调用。
	//
	// nil = 当前上下文不可用（fail-closed）。
	ExitPlanMode func()
	// SessionID 用于隔离按会话的状态（读历史、去重跟踪），
	// 防同 cwd 的并发会话交叉污染。
	SessionID string
	// ReadRefStats 是 per-session 的 read-ref telemetry 累加器（read_file 用）。
	//
	// 对账 TS 的 `params.readRefStats`（`tool-pipeline.ts` 注入）。
	// nil = 退回进程级兜底（对账 TS 的模块级 `readRefSavedBytes`）。
	//
	// **依赖方向**：`tools` 包内定义（ReadRefStats 在 readdedup.go），无跨包环。
	ReadRefStats *ReadRefStats
	// skipReadDedup 抑制 read_file 的**读去重记录**（表1a/表1b/表2）。
	//
	// **为什么需要**：多读分支复用 `t.Execute` 做子读，而 TS 的 `handleMultiRead`
	// 是**直接调 `readFilePayload`**（不走工具）——故 TS 的多读**只写表1b**，
	// 不写表1a。若子调用照常记录，表1a 会多出 TS 没有的条目，且使表1b 的
	// 「全文件包含」判定路径**永不可达**（后续单读总先命中同键的表1a）。
	//
	// 多读分支自己在末尾写表1b + 表2（对账 TS `:1099-1107`）。
	skipReadDedup bool
	// SessionTurnCount 是当前会话轮次（启用渐进式超时策略）。
	SessionTurnCount int
	// OwnedFiles 是当前任务拥有的文件（用于作用域写入）。
	OwnedFiles []string
	// FileHistory 返回本会话的文件历史（nil = 不可用）。
	//
	// **为什么是回调**（对账 TS `createUndoTool(getFileHistory)`）：历史实例
	// 在**会话建立后**才存在（late-bound），且 cwd 变更时需重建。
	// 与 `EnterPlanMode` / `GrantPath` 同一注入模式。
	FileHistory func() UndoHistory
	// SessionModifiedFiles 是本会话已修改的文件。
	SessionModifiedFiles []string
	// Jobs 是本会话的后台任务注册表（bash 的 run_in_background 与 job 工具用）。
	//
	// 对账 TS 的 `params.jobs?: JobRegistry`（`types.ts:247`）——TS 侧由
	// `AgentLoop` 在 `if (config.sessionId)` 时创建（`loop.ts:850`）并经
	// `tool-pipeline.ts:842` 注入。**nil = 无会话上下文**：bash 退回前台执行、
	// job 工具提示「后台任务系统在当前上下文不可用」——两者都是 TS 的既有语义。
	Jobs JobRegistry
	// GrantPath 授予目录子树的访问权（request_path_access 用）。
	//
	// **为什么是回调而非接口**：`GrantMode`/`PathGrant` 定义在
	// `internal/agent`，而 `agent` 已依赖 `tools`——反向 import 会成 import 环。
	// 故按本仓库既有模式（同 `EnterPlanMode`/`ExitPlanMode`）：`agent` 注入
	// `func` 回调，`tools` 只依赖签名。
	//
	// **nil = 无会话上下文** → 工具 fail-closed 报错，不假装授权成功。
	//
	// `cwd` 非空时授权绑定该工作区（sidecar 多会话隔离，见 `PathGrant.Scope`）。
	GrantPath func(root string, mode GrantMode, cwd string)
	// Grants 是**会话级**的路径授权存储（越界读写判定用）。
	//
	// **为什么需要它**（第八十一刀发现的既有缺陷）：`writeFileTool.Grants`
	// 等是**构造时**绑定的（`NewDefaultRegistry` 的参数），而会话的授权存储
	// 由 `Loop` 在 `New()` 里创建——**两个实例**。且 `main.go` 装配 registry
	// 时**根本没传** `Grants`（零值 nil）→ 工具内部的越界检查永远看不到授权。
	//
	// 后果：门链（`loop.go` 的 pathGrant 门）授了权、放行了，但工具内部
	// 的 `pathsafe.Validate` 仍拒绝——**授权形同虚设**。
	//
	// 对账 TS：TS 的 `isWriteGranted`（`path-grants.ts:181`）读**包级单例**
	// `_grants`——所有工具共享同一状态，无构造时注入。Go 侧经本字段对齐
	// 到同一实例。
	//
	// 工具应**优先用本字段**、回退构造时字段（向后兼容）。
	Grants pathsafe.GrantChecker
	// AbortSignal 在工具级超时触发时取消。
	AbortSignal context.Context
	// ArtifactStore 是 artifact 存储（read_section 用）。
	//
	// 对账 TS 的 `params.artifactStore`（read-section.ts）。
	// nil 时 read_section 报「未配置 artifactStore」——与 TS 一致。
	//
	// **依赖方向**：`tools → artifact`（artifact 不依赖 tools，无环）。
	ArtifactStore *artifact.Store
	// ContextWindow 是当前上下文窗口（read_section 的截断上限按它缩放）。
	ContextWindow int
	// ProviderProfile 是提供商切片（read_file 的读上限按策略系数缩放）。
	//
	// 对账 TS 的 `params.providerProfile`（`tool-pipeline.ts:465,840` 注入）。
	// nil = 无 profile，走 balanced（系数 1.0）——与 TS 的默认一致。
	ProviderProfile *compact.CompactRatioProfile
	// perFileCap* 是多读分支的**按文件均分 cap**（对账 TS `handleMultiRead` 的
	// `Math.floor(computedCap.maxChars / paths.length)`）。
	//
	// **为什么需要**：cap 由 `ComputeModelReadCap(ContextWindow)` 算出，无法用
	// ContextWindow 精确表达「除以 N」的结果。0 = 不覆盖（用窗口算出的值）。
	perFileCapMax  int
	perFileCapHead int
	perFileCapTail int
}

// Registry 是工具注册表。
//
// 对账 TS 的 ToolRegistry（src/tools/registry.ts:19）。
type Registry struct {
	tools map[string]Tool
	order []string // 注册顺序（用于确定性遍历）

	// defaultToolTimeout 是**未声明** Timeout 的工具（返回 0）所用的兜底值。
	//
	// 零值 = 用 `DefaultToolTimeout`（120s，对账 TS `DEFAULT_TOOL_TIMEOUT_MS`）。
	// 测试用 `SetDefaultToolTimeout` 注入短值，避免真等 2 分钟。
	defaultToolTimeout time.Duration
}

// DefaultToolTimeout 是**未声明** `Timeout` 的工具所用的超时。
//
// 对账 TS `tool-pipeline.ts:129` 的 `DEFAULT_TOOL_TIMEOUT_MS = 120_000`。
//
// # 为什么必须有默认值
//
// TS `tool-pipeline.ts:1501`：`toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS`
// ——工具**没声明**超时 ≠ 无超时。`web_fetch` / `web_map` 等正属此类
// （TS 侧 grep 确认未声明 `timeoutMs`）。
//
// 若把「未声明」当作「无限等待」，一个挂死的工具会**冻住整个回合**
// （TS 注释 `:725-728` 写的 2026-09-08 写后挂起事故）。
const DefaultToolTimeout = 120 * time.Second

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// SetDefaultToolTimeout 覆盖未声明工具的兜底超时（测试用）。
//
// 非正值不生效——防止误传 0 把兜底取消掉（那就是本刀要防的挂死）。
func (r *Registry) SetDefaultToolTimeout(d time.Duration) {
	if d > 0 {
		r.defaultToolTimeout = d
	}
}

// executeWithToolTimeout 在工具级超时下执行工具。
//
// 对账 TS `withToolTimeout`（`tool-pipeline.ts:311-338`）：
//
//	// Guard against NaN/Infinity/negative timeout (e.g. parameter misplacement bugs)
//	if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) timeoutMs = DEFAULT_TOOL_TIMEOUT_MS
//	const timer = setTimeout(() => { timeoutController?.abort(); reject(new Error(
//	  `Tool ${toolName} timed out after ${timeoutMs / 1000}s ${TOOL_TIMEOUT_RECOVERY_HINT}`)) }, timeoutMs)
//
// # 与 TS 的差异（Go 的并发模型决定，语义等价）
//
// TS 用 `Promise.race` + `setTimeout` 让**外层 Promise 拒绝**，同时
// `timeoutController.abort()` 把取消**级联**给底层操作。
// Go 直接在 `ctx` 上设 deadline——`Execute` 内的 select 会立刻看到 `Done()`，
// **级联天然成立**，不需要额外的 race。
//
// # 超时后仍等待结果返回
//
// 超时触发时返回**超时错误**（而不是空结果）。但底层 goroutine 可能仍在
// 收尾——本实现**不等它**，与 TS 的 `Promise.race` 一致（TS 也不等被
// 超时抛弃的那个 Promise）。ctx 取消会让守规矩的工具自行退出。
func (r *Registry) executeWithToolTimeout(
	ctx context.Context, tool Tool, name string, p *CallParams,
) (contract.Result, error) {
	// **nil ctx 防御**（接线引入的回归）：
	// `context.WithTimeout(nil, …)` 会 panic。而 `Registry.Execute` 的既有
	// 调用方**有传 nil 的**（`acceptance_exportfile_test.go:31` 等测试捷径），
	// 且各工具自己在 `Execute` 里做了 `if ctx == nil { ctx = context.Background() }`
	// ——即 nil 在此层是**被接受的输入**。
	//
	// 故本层必须与工具层同样容忍 nil，否则接线把「可用」变成「panic」。
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := r.effectiveToolTimeout(tool, p)

	// 无超时可用（理论上不会走到：effectiveToolTimeout 恒返回正值）
	if timeout <= 0 {
		return tool.Execute(ctx, p)
	}

	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		result contract.Result
		err    error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := tool.Execute(tctx, p)
		ch <- outcome{result: res, err: err}
	}()

	select {
	case o := <-ch:
		// 工具正常返回。若同时恰好超时，以结果为准（与 TS 的 race 语义一致：
		// 先 settle 的赢；此处结果已到，说明它先完成）。
		return o.result, o.err
	case <-tctx.Done():
		// 区分「工具超时」与「父 ctx 取消」——两者都触发 Done()，
		// 但只有前者是我们的超时。父取消时回传父的 error（保持原语义）。
		if ctx.Err() != nil {
			return contract.Result{}, ctx.Err()
		}
		return contract.Result{}, &ToolTimeoutError{
			Tool:    name,
			Timeout: timeout,
		}
	}
}

// ToolTimeoutError 报告工具级超时。
//
// 对账 TS 的文案：
//
//	`Tool ${toolName} timed out after ${timeoutMs / 1000}s ${TOOL_TIMEOUT_RECOVERY_HINT}`
//
// 秒数用 `%g`（TS 的 `timeoutMs / 1000` 是浮点除法——50ms → `0.05`）。
type ToolTimeoutError struct {
	Tool    string
	Timeout time.Duration
}

func (e *ToolTimeoutError) Error() string {
	return fmt.Sprintf("Tool %s timed out after %gs %s",
		e.Tool, e.Timeout.Seconds(), ToolTimeoutRecoveryHint)
}

// ToolTimeoutRecoveryHint 对账 TS `TOOL_TIMEOUT_RECOVERY_HINT`（`tool-pipeline.ts:308-309`）。
//
// **逐字对账**（进模型上下文）：TS 原文
//
//	'— 底层执行可能仍在后台继续（worker 写入已落盘）。检查 git status / 会话
//	 checkpoint；可用 executePlanWaves fromWave=N 续跑或 deliver 已完成的波次'
//
// 注意 TS 以 `—` 开头（前一个空格由格式化模板给出：`…after ${n}s ${HINT}`）。
const ToolTimeoutRecoveryHint = "— 底层执行可能仍在后台继续（worker 写入已落盘）。" +
	"检查 git status / 会话 checkpoint；可用 executePlanWaves fromWave=N 续跑或 deliver 已完成的波次"

// effectiveToolTimeout 解析某次调用该用多长的超时。
//
// 对账 TS `toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS`。
// 工具返回 0（未声明）时用兜底值。
func (r *Registry) effectiveToolTimeout(tool Tool, p *CallParams) time.Duration {
	d := tool.Timeout(p)
	if d > 0 {
		return d
	}
	if r.defaultToolTimeout > 0 {
		return r.defaultToolTimeout
	}
	return DefaultToolTimeout
}

// Register 注册工具（同名覆盖）。
func (r *Registry) Register(t Tool) {
	name := t.Definition().Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Remove 移除工具。返回是否确实移除了。
func (r *Registry) Remove(name string) bool {
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// Get 按名取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Has 报告工具是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// All 返回全部已注册工具（按注册顺序）。
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

// Names 返回全部工具名（升序，供 did-you-mean 提示用）。
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Definitions 返回全部**启用中**工具的声明（按名升序）。
//
// 升序是刻意的：tool definitions 进请求体，顺序必须稳定才不破坏前缀缓存。
func (r *Registry) Definitions() []contract.Definition {
	defs := make([]contract.Definition, 0, len(r.tools))
	for _, t := range r.tools {
		if !t.Enabled() {
			continue
		}
		defs = append(defs, t.Definition())
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// foreignAliases 把其他 agent 框架的工具名映射到本运行时的等价物。
//
// 透明重映射避免让模型记忆框架特定名——调用直接生效。
// 键为外来名（小写查找），值为本运行时工具名。
var foreignAliases = map[string]string{
	"todowrite": "todo",
	"task":      "delegate_task",
	"agent":     "delegate_task",
}

// ResolveName 把可能是外来的工具名解析为已注册的规范名。
//
// 管线必须在权限门禁（plan-mode 白名单、deny 规则、审批、风险评估）**之前**
// 调用它——否则按规范名配置的 deny 规则可被用别名绕过（如调 `task` 绕过
// `delegate_task` 的 deny）。
func (r *Registry) ResolveName(name string) string {
	if r.Has(name) {
		return name
	}
	if canonical, ok := foreignAliases[strings.ToLower(name)]; ok && r.Has(canonical) {
		return canonical
	}
	return name
}

// ErrUnknownTool 表示工具未注册。
type ErrUnknownTool struct {
	Name       string
	DidYouMean string
}

func (e *ErrUnknownTool) Error() string {
	msg := fmt.Sprintf("Unknown tool: %s.", e.Name)
	if e.DidYouMean != "" {
		msg += " " + e.DidYouMean
	}
	return msg
}

// Execute 按名执行工具。
//
// 别名重映射在此做一次兜底（管线的 ResolveName 是主路径），
// 并对未知名给出 did-you-mean 提示——裸 "Unknown tool" 会让模型下一轮
// 靠记忆猜真名（session 6176a17f 的 task/delegate_task 事故）。
func (r *Registry) Execute(ctx context.Context, name string, p *CallParams) (contract.Result, error) {
	tool, ok := r.tools[name]
	resolvedName := name
	var aliasNote string

	if !ok {
		if canonical, isAlias := foreignAliases[strings.ToLower(name)]; isAlias {
			if t2, ok2 := r.tools[canonical]; ok2 {
				tool = t2
				resolvedName = canonical
				aliasNote = fmt.Sprintf("[NOTE: %q 自动映射为 %q — 下次请直接调 %s]", name, canonical, canonical)
			}
		}
	}

	if tool == nil {
		return contract.Result{}, &ErrUnknownTool{
			Name:       name,
			DidYouMean: DidYouMean(name, r.Names()),
		}
	}
	if !tool.Enabled() {
		return contract.Result{}, fmt.Errorf("Tool %s is disabled", resolvedName)
	}

	// ── 工具级超时（第九十九刀）──
	//
	// 对账 TS `tool-pipeline.ts:1501` + `withToolTimeout`（`:311`）：
	//
	//	const toolTimeout = toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS
	//	… await withToolTimeout(execution, tu.name, toolTimeout, …)
	//
	// **为什么必须包在 ctx 上**（对账 TS 注释 P0/H1）：超时要**级联下去**
	// 中止底层操作（子进程 / fetch），而不只是让外层 Promise 拒绝——
	// 否则工具内部的 goroutine/连接会继续跑，形成泄漏。
	result, err := r.executeWithToolTimeout(ctx, tool, resolvedName, p)
	if err != nil {
		return result, err
	}
	if aliasNote != "" {
		result.Content = aliasNote + "\n" + result.Content
	}
	return result, nil
}

// HardGate 是「任何档位都不能绕过」的硬闸门工具。
//
// **与 RequiresApproval 的区别（关键）**：
//
//   - `RequiresApproval` 表达「**按档位**可能需要批准」——它的返回值依赖
//     `p.ApprovalMode`（如写工具：非放开档返回 true）。TS 侧这类返回值由
//     `tool-pipeline` 的完整决策树消费（档位 × 风险分级 × pathGrant ×
//     allowlist × headless 中和），**不是直接拒绝**。
//   - `HardGate` 表达「**无条件**需人工批准」——与档位无关（如 bash 的
//     破坏性命令、git commit）。这类调用在任何档位下都不该静默执行。
//
// **为什么单独提取**（**历史理由，已部分过期**——第六十九刀核实）：
// 提取时的理由是「Go 侧尚无审批提示往返通道、无 `assessToolRisk` 风险分级，
// 直接消费 `RequiresApproval` 会把写工具一并拦下」。**这两项现已具备**
// （`agent.AssessToolRisk` + `agent.decideApprovalGate`），故「完整的档位
// 门控待提示通道落地后接入」**已完成**（第六十二刀）。
//
// **第八十一刀三续补注（读本条时请配合）**：上句的「这两项」中，② 由
// **「确定性解析」**替代弹窗通道满足（见下方「接线条件」② 的「取后者：
// `decideApprovalGate` 的确定性解析，不造弹窗、不争 stdin」）——**这是有意
// 的架构决策**，故引用 `decideApprovalGate` 是正确的，非错误声称。
//
// **但需知其后果（多处已记录）**：确定性解析对「**本质需用户决定**」的工具
// （如 `request_path_access`——「是否授权访问 /etc」不能由程序决定）**无能为力**。
// 非 skip 档下这类调用得到**硬拒绝**（「需人工批准，agent 无法自行授权」），
// 而 TS 是「弹审批 → 用户批准 → 放行」。这是**既有的架构缺口**，
// 非某个工具的缺陷（`approval_gate.go` / `loop.go` 已分别记录）。
//
// 但 `HardGate` **仍然必要**，理由变了：它是「**任何档位都不能绕过**」的
// 独立语义层——优先级**高于**档位门。`RequiresApproval` 是**档位驱动**的
// （可能被 skip 档放行），而硬闸门（bash 破坏性命令）在 skip 档下**也必须
// 拦**。两者不是替代关系。
//
// **实际门链顺序**（`agent/loop.go` 的 `executeTool`，按源码出现序）：
//
//	deny → selfKill → HardGate → 档位门 → pathGrant → bash 写门 → Execute
//
// 即硬闸门**先于**档位门——故档位门放行 skip 档时，硬闸门已拦过一遍。
// **注意**：后两门的次序与 TS 三元链**不同**（TS 是 pathGrant 先于档位分支）；
// Go 侧相反，但不影响结果（见 `loop.go` 档位门段的说明）。
//
// **不写行号**：门链顺序稳定，但行号随注释增删漂移（本段初版写的
// `988`/`1041` 在同一次编辑中就失效了）。要定位用函数名或门名 grep。
type HardGate interface {
	RequiresHardGate(p *CallParams) bool
}

// RequiresHardGate 报告该次调用是否命中硬闸门。
//
// 未实现 `HardGate` 的工具恒返回 false（不参与硬闸门）。
func (r *Registry) RequiresHardGate(name string, p *CallParams) bool {
	t, ok := r.tools[name]
	if !ok {
		return false
	}
	hg, ok := t.(HardGate)
	if !ok {
		return false
	}
	return hg.RequiresHardGate(p)
}

// NeedsApproval 报告该次调用是否需要批准（**按档位**语义）。
//
// 对账 TS `ToolRegistry.needsApproval`（`tool-pipeline.ts:1092` 有真实消费者）。
//
// ## 当前状态：**已有生产消费者**（第六十二刀起）
//
// **本段曾写「零调用者（有意暂缓）」——已过期**（第六十九刀核实）。现状：
// `agent.decideApprovalGate`（`approval_gate.go`）消费它——那是 TS `shouldAsk`
// 决策树的 Go 落地，含档位分支（manual → needsApproval / auto-safe →
// isHighRisk / skip → 放行）与 pathGrant / bash 写门 / allowlist 等前置门。
//
// **历史缺口（保留记录）**：第五十刀时它确实零调用者，后果是 4 个写工具在
// manual 档下 `needsApproval=true` 却无人消费 → 写操作静默执行（fail-open）。
// 第六十二刀接线后闭合。
//
// ## 接线条件（**①②③ 全部完成**）
//
// 原列三条前置：
//
//	① `assessToolRisk` 的纯函数子集 —— ✅ 已完成（`agent.AssessToolRisk`）
//	② 审批提示往返通道（或 TS headless 语义的确定性解析）—— ✅ 已完成
//	   （取**后者**：`decideApprovalGate` 的确定性解析，不造弹窗、不争 stdin）
//	③ 写工具的 `RequiresApproval` 语义订正 —— ✅ **第七十四刀完成**
//	   （4 个写工具改为**恒真** `() => true`，对账 TS `write-file.ts:349` /
//	   `edit.ts:390` / `hash-edit.ts:569` / `apply-patch.ts:262`）
//
// **③ 完成后的语义**：档位判定**单点**在 `decideApprovalGate`（它开头处理
// skip 档、manual 分支读 `needsApproval`、auto-safe 分支读 `isHighRisk`）。
// 工具层不再读档位——故本函数返回的是「**这个工具本质是否需要批准**」，
// 而非「当前档位下是否需要」。
//
// **③ 完成前的影响（已消除）**：本函数与 `decideApprovalGate` 曾**各判一次
// 档位**（重复判定）——将来任一处改动会导致不一致。第七十四刀把工具层
// 的档位判定移除后，档位语义只剩 `decideApprovalGate` 一处。
func (r *Registry) NeedsApproval(name string, p *CallParams) bool {
	t, ok := r.tools[name]
	if !ok {
		return false
	}
	return t.RequiresApproval(p)
}

// DidYouMean 给出最接近的工具名建议（编辑距离 ≤3）。
//
// 返回空串表示无明显候选。
func DidYouMean(input string, candidates []string) string {
	type scored struct {
		name string
		dist int
	}
	var near []scored
	for _, c := range candidates {
		d := editDistance(strings.ToLower(input), strings.ToLower(c))
		if d <= 3 {
			near = append(near, scored{c, d})
		}
	}
	if len(near) == 0 {
		return ""
	}
	sort.Slice(near, func(i, j int) bool {
		if near[i].dist != near[j].dist {
			return near[i].dist < near[j].dist
		}
		return near[i].name < near[j].name
	})
	return fmt.Sprintf("Did you mean %q?", near[0].name)
}

// editDistance 是标准 Levenshtein 距离。
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}
