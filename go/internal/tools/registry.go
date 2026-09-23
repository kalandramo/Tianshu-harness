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
	// SessionModifiedFiles 是本会话已修改的文件。
	SessionModifiedFiles []string
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
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
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

	result, err := tool.Execute(ctx, p)
	if err != nil {
		return result, err
	}
	if aliasNote != "" {
		result.Content = aliasNote + "\n" + result.Content
	}
	return result, nil
}

// NeedsApproval 报告该次调用是否需要批准。
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
