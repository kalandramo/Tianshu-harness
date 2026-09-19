package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RuntimeHookPhase 是 hook 的五个执行阶段。
//
// 对账 src/agent/runtime-hooks.ts 的 RuntimeHookPhase。
type RuntimeHookPhase string

const (
	PhasePreTurn         RuntimeHookPhase = "preTurn"
	PhaseAfterPerception RuntimeHookPhase = "afterPerception"
	PhasePostTool        RuntimeHookPhase = "postTool"
	PhasePostTurn        RuntimeHookPhase = "postTurn"
	PhasePostSession     RuntimeHookPhase = "postSession"
)

// 默认预算（对账 DEFAULT_HOOK_TIMEOUT_MS / DEFAULT_HOOK_SLOW_MS）。
const (
	defaultHookTimeoutMs = 10_000
	defaultHookSlowMs    = 2_000
)

// RuntimeHookRunOutcome 是一次 hook 执行的结果分类。
type RuntimeHookRunOutcome string

const (
	OutcomeCompleted RuntimeHookRunOutcome = "completed"
	OutcomeFailed    RuntimeHookRunOutcome = "failed"
	OutcomeTimedOut  RuntimeHookRunOutcome = "timed_out"
	OutcomeSkipped   RuntimeHookRunOutcome = "skipped"
)

// RuntimeToolEvent 是 postTool 阶段传入的工具事件。
//
// 对账 RuntimeToolEvent。**只有 postTool 阶段**能看到它。
type RuntimeToolEvent struct {
	Name    string
	Success bool
	// Target 是展示/历史用的目标（文件路径等）。
	//
	// **注意**：需要文件语义的 hook 应优先用 Input——Target 是展示兜底。
	Target string
	// Input 是原始的结构化工具入参。
	Input   map[string]any
	IsError bool
	// FailureClass 是失败分类（语义失败 vs 环境问题的区分依据）。
	FailureClass string
	// ResultContent 是工具结果内容（供 hook 检查 lossy 标记等内容级信号）。
	ResultContent string
	// ApprovalRequired 是**模式级近似**：审批模式要求写操作交互批准时为 true。
	//
	// **不是逐调用审计**——allowlist 自动批准与「always allow」也算 true。
	ApprovalRequired bool
}

// RuntimeHookEffects 是 hook 影响主流程的通道。
//
// 对账 RuntimeHookEffects。**默认全是 no-op**——未接线的 effect 不应让 hook 崩。
type RuntimeHookEffects struct {
	InjectUserMessage func(message string)
	RequestThetaCheck func(reason string)
	EmitPhaseChange   func(phase string, detail map[string]any)
	MarkClaimStale    func(claimID string)
	EmitControlSignal func(signal any)
	SetGitChangeRate  func(rate float64)
}

// RuntimeHookContext 是传给每个 hook 的上下文。
type RuntimeHookContext struct {
	Snapshot *RuntimeHookSnapshot
	Effects  RuntimeHookEffects
}

// RuntimeHookSnapshot 是 hook 可见的会话状态快照。
//
// **本移植的范围**：只带 Go 侧当前已有的字段（cwd / turn / 工具历史摘要 /
// git 变更率）。TS 侧的 sensorium / strategy / vigor / season 等认知状态依赖
// `internal/context`（尚未移植）——那些字段留待认知层落地后扩展。
//
// 这是**有意的分步**：管线本体（分派 / 超时 / 记账）是地基，认知状态是上层。
type RuntimeHookSnapshot struct {
	Cwd  string
	Turn int
	// RecentToolHistory 是近期工具调用的摘要（供 hook 做窗口式判断）。
	RecentToolHistory []ToolHistoryEntry
	// GitChangeRate 是 git 工作区变更率（0-1）。
	GitChangeRate float64
	// TouchedTSFiles 报告本会话是否写过 TS 文件（**任务级**，非窗口）。
	TouchedTSFiles bool
	// SawTypecheck 报告自上次 TS 编辑后是否跑过 typecheck。
	SawTypecheck bool
	// TouchedUIFiles 报告本会话是否写过 UI 文件（任务级）。
	TouchedUIFiles bool
	// SawVisualVerify 报告本会话是否用过视觉验证工具。
	SawVisualVerify bool
	// LastThinkingLength 是上一轮思考内容长度（推理螺旋守卫用）。
	LastThinkingLength int
	// LastTurnHadTools 报告上一轮是否有工具调用。
	LastTurnHadTools bool
}

// ToolHistoryEntry 是工具历史的摘要项（对账 TS 的 Pick<ToolHistoryEntry, ...>）。
type ToolHistoryEntry struct {
	Tool         string
	Status       string
	Target       string
	ArgsHash     string
	ErrorClass   string
	BashActivity string
}

// RuntimeHook 是一个运行时 hook。
//
// **阶段由 Phase 字段声明**，Run 的签名统一为「ctx + 可选 tool 事件」——
// Go 无联合类型，故 postTool 通过 tool 参数区分（其他阶段传 nil）。
type RuntimeHook struct {
	Name  string
	Phase RuntimeHookPhase
	// BudgetMs 是单 hook 预算覆盖（0 = 用全局）。
	//
	// 对账 budgetMs：**内外层超时层级的 hook 必须声明**——外层预算要大于
	// 内层的 fail-closed 预算，否则外层先超时会吞掉内层的诊断信息。
	BudgetMs int
	// Run 是 hook 本体。postTool 阶段会传入 tool 事件，其余阶段为 nil。
	Run func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error
}

// RuntimeHookError 是一次 hook 失败的记录。
type RuntimeHookError struct {
	Phase    RuntimeHookPhase
	HookName string
	Message  string
	Err      error
}

// RuntimeHookRunEvent 是一次 hook 调用的遥测事件（**含 skipped**）。
type RuntimeHookRunEvent struct {
	ID         string
	Phase      RuntimeHookPhase
	Outcome    RuntimeHookRunOutcome
	DurationMs int64
	// BudgetMs 是本次执行生效的预算（单 hook 或全局）——遥测可归因。
	BudgetMs int
	Slow     bool
	Message  string
}

// RuntimeHookStats 是单个 (phase, hook) 的聚合统计。
type RuntimeHookStats struct {
	ID              string
	Phase           RuntimeHookPhase
	Runs            int
	Skipped         int
	Failures        int
	Timeouts        int
	SlowRuns        int
	TotalDurationMs int64
	MaxDurationMs   int64
}

// RuntimeHookManifestEntry 是 manifest 的一项。
type RuntimeHookManifestEntry struct {
	ID      string
	Phase   RuntimeHookPhase
	Enabled bool
}

// PipelineOptions 是管线选项。
type PipelineOptions struct {
	// OnError 接收 hook 失败（含**迟到失败**）。
	OnError func(RuntimeHookError)
	// OnRun 接收每次调用的事件（含 skipped）。
	OnRun func(RuntimeHookRunEvent)
	// DisabledHookIDs 是保留在 manifest 但**排除执行**的 hook id。
	DisabledHookIDs []string
	// HookTimeoutMs 是全局单 hook 预算（0 = 用默认 10s；负数 = 禁用超时）。
	HookTimeoutMs int
	// HookSlowMs 是「慢」阈值（默认 2000）。
	HookSlowMs int
	// now 可注入时钟（测试用）。
	now func() time.Time
}

// Pipeline 是五阶段 hook 管线。
//
// 对账 RuntimeHookPipeline。**语义要点**：
//   - 阶段内**串行**执行（hook 有顺序依赖：perception 的输出供后续 hook 读）
//   - 单 hook 失败/超时**不中断**该阶段——继续跑下一个
//   - stats 按 `phase:id` 键控（**跨阶段同名 hook 不冲突**）
//   - 超时后**迟到收尾**：pending 仍会 settle，迟到失败送 OnError 但不重复计 runs
type Pipeline struct {
	mu sync.Mutex

	byPhase map[RuntimeHookPhase][]RuntimeHook
	// registered 保留注册序（供 manifest）。
	registered []RuntimeHook
	stats      map[string]*RuntimeHookStats
	disabled   map[string]bool
	options    PipelineOptions
}

// NewPipeline 构造管线。
func NewPipeline(opts PipelineOptions) *Pipeline {
	p := &Pipeline{
		byPhase:  map[RuntimeHookPhase][]RuntimeHook{},
		stats:    map[string]*RuntimeHookStats{},
		disabled: map[string]bool{},
		options:  opts,
	}
	if p.options.now == nil {
		p.options.now = time.Now
	}
	for _, id := range opts.DisabledHookIDs {
		p.disabled[id] = true
	}
	return p
}

// Register 注册一个 hook。
//
// 未知阶段**静默忽略**（对账 TS 的 switch 无 default 分支——不 panic，
// 因为阶段由编译期装配决定，未知阶段意味着装配错误但不应崩运行时）。
func (p *Pipeline) Register(h RuntimeHook) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registered = append(p.registered, h)
	switch h.Phase {
	case PhasePreTurn, PhaseAfterPerception, PhasePostTool, PhasePostTurn, PhasePostSession:
		p.byPhase[h.Phase] = append(p.byPhase[h.Phase], h)
	}
}

// SetDisabledHookIDs 运行时替换禁用集（config 热更入口）。
//
// 只影响运行时行为（跳过/执行），**不改变注册集**——装配仍由编译期决定。
func (p *Pipeline) SetDisabledHookIDs(ids []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.disabled = map[string]bool{}
	for _, id := range ids {
		p.disabled[id] = true
	}
}

// IsEnabled 查询单个 hook 当前是否启用（热更后与 Manifest 同源）。
func (p *Pipeline) IsEnabled(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.disabled[name]
}

// Manifest 返回注册表（含启用状态）。
func (p *Pipeline) Manifest() []RuntimeHookManifestEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]RuntimeHookManifestEntry, 0, len(p.registered))
	for _, h := range p.registered {
		out = append(out, RuntimeHookManifestEntry{
			ID:      h.Name,
			Phase:   h.Phase,
			Enabled: !p.disabled[h.Name],
		})
	}
	return out
}

// Stats 返回统计副本（对账 getStats 的 `{...stat}` 深拷贝语义）。
func (p *Pipeline) Stats() []RuntimeHookStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]RuntimeHookStats, 0, len(p.stats))
	for _, s := range p.stats {
		out = append(out, *s)
	}
	return out
}

// RunPreTurn 执行 preTurn 阶段。
func (p *Pipeline) RunPreTurn(ctx context.Context, hctx *RuntimeHookContext) {
	p.runPhase(ctx, PhasePreTurn, hctx, nil)
}

// RunAfterPerception 执行 afterPerception 阶段。
func (p *Pipeline) RunAfterPerception(ctx context.Context, hctx *RuntimeHookContext) {
	p.runPhase(ctx, PhaseAfterPerception, hctx, nil)
}

// RunPostTool 执行 postTool 阶段（携带工具事件）。
func (p *Pipeline) RunPostTool(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) {
	p.runPhase(ctx, PhasePostTool, hctx, tool)
}

// RunPostTurn 执行 postTurn 阶段。
func (p *Pipeline) RunPostTurn(ctx context.Context, hctx *RuntimeHookContext) {
	p.runPhase(ctx, PhasePostTurn, hctx, nil)
}

// RunPostSession 执行 postSession 阶段。
func (p *Pipeline) RunPostSession(ctx context.Context, hctx *RuntimeHookContext) {
	p.runPhase(ctx, PhasePostSession, hctx, nil)
}

// runPhase 执行某阶段的所有 hook（**串行**）。
func (p *Pipeline) runPhase(ctx context.Context, phase RuntimeHookPhase, hctx *RuntimeHookContext, tool *RuntimeToolEvent) {
	// 取快照（避免持锁执行 hook——hook 可能调 Register）
	p.mu.Lock()
	hooks := append([]RuntimeHook(nil), p.byPhase[phase]...)
	p.mu.Unlock()

	for _, h := range hooks {
		p.runOne(ctx, phase, h, hctx, tool)
	}
}

// runOne 执行单个 hook（含超时与记账）。
func (p *Pipeline) runOne(ctx context.Context, phase RuntimeHookPhase, h RuntimeHook, hctx *RuntimeHookContext, tool *RuntimeToolEvent) {
	p.mu.Lock()
	disabled := p.disabled[h.Name]
	p.mu.Unlock()

	if disabled {
		p.publishRun(RuntimeHookRunEvent{
			ID: h.Name, Phase: phase, Outcome: OutcomeSkipped, Message: "disabled",
		})
		return
	}

	timeoutMs := h.BudgetMs
	if timeoutMs == 0 {
		timeoutMs = p.options.HookTimeoutMs
	}
	if timeoutMs == 0 {
		timeoutMs = defaultHookTimeoutMs
	}
	slowMs := p.options.HookSlowMs
	if slowMs == 0 {
		slowMs = defaultHookSlowMs
	}

	started := p.options.now()
	outcome := OutcomeCompleted
	var msg string
	timedOut := false

	// 用 channel 实现超时——Go 无法中断 goroutine，故「超时」的语义是
	// **不等待它**，与 TS 的 Promise.race 同义（迟到的 settle 仍会发生）。
	type done struct{ err error }
	ch := make(chan done, 1)
	go func() {
		defer func() {
			// hook panic 不应崩进程——转成 error（TS 侧由 Promise reject 等价表达）
			if r := recover(); r != nil {
				ch <- done{err: fmt.Errorf("hook panicked: %v", r)}
			}
		}()
		ch <- done{err: h.Run(ctx, hctx, tool)}
	}()

	if timeoutMs > 0 {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		select {
		case d := <-ch:
			timer.Stop()
			if d.err != nil {
				outcome = OutcomeFailed
				msg = d.err.Error()
				p.reportError(RuntimeHookError{Phase: phase, HookName: h.Name, Message: msg, Err: d.err})
			}
		case <-timer.C:
			timedOut = true
			outcome = OutcomeTimedOut
			msg = fmt.Sprintf("Runtime hook '%s' timed out after %dms", h.Name, timeoutMs)
			p.reportError(RuntimeHookError{
				Phase: phase, HookName: h.Name, Message: msg,
				Err: fmt.Errorf("timeout after %dms", timeoutMs),
			})
		}
	} else {
		d := <-ch
		if d.err != nil {
			outcome = OutcomeFailed
			msg = d.err.Error()
			p.reportError(RuntimeHookError{Phase: phase, HookName: h.Name, Message: msg, Err: d.err})
		}
	}

	durationMs := p.options.now().Sub(started).Milliseconds()

	// **迟到收尾**：超时后 goroutine 仍会 settle。迟到的失败若不接住，
	// 在 Go 侧表现为 goroutine 泄漏（channel 有缓冲故不阻塞，但错误无声丢失）。
	// 这里起一个 goroutine 接迟到结果并送 OnError，保留现场。
	//
	// **不重复计 runs**——run 统计已在上方以 timed_out 记账。
	if timedOut {
		go func() {
			d := <-ch
			if d.err != nil {
				p.reportError(RuntimeHookError{
					Phase:    phase,
					HookName: h.Name,
					Message:  "late failure after timeout: " + d.err.Error(),
					Err:      d.err,
				})
			}
		}()
	}

	p.publishRun(RuntimeHookRunEvent{
		ID: h.Name, Phase: phase, Outcome: outcome,
		DurationMs: durationMs, BudgetMs: timeoutMs,
		Slow:    outcome == OutcomeCompleted && durationMs >= int64(slowMs),
		Message: msg,
	})
}

// reportError 安全地调用 OnError（回调 panic 不应崩主流程）。
func (p *Pipeline) reportError(e RuntimeHookError) {
	if p.options.OnError == nil {
		return
	}
	defer func() { _ = recover() }()
	p.options.OnError(e)
}

// publishRun 记账 + 发事件。
//
// **键控规则**：`phase:id`——跨阶段同名 hook 不冲突（对账 TS 的
// `${event.phase}:${event.id}`）。
func (p *Pipeline) publishRun(e RuntimeHookRunEvent) {
	p.mu.Lock()
	key := string(e.Phase) + ":" + e.ID
	s, ok := p.stats[key]
	if !ok {
		s = &RuntimeHookStats{ID: e.ID, Phase: e.Phase}
		p.stats[key] = s
	}

	if e.Outcome == OutcomeSkipped {
		s.Skipped++
	} else {
		s.Runs++
		s.TotalDurationMs += e.DurationMs
		if e.DurationMs > s.MaxDurationMs {
			s.MaxDurationMs = e.DurationMs
		}
		switch e.Outcome {
		case OutcomeFailed:
			s.Failures++
		case OutcomeTimedOut:
			s.Timeouts++
		}
		if e.Slow {
			s.SlowRuns++
		}
	}
	p.mu.Unlock()

	if p.options.OnRun != nil {
		// **插桩绝不打断 agent 执行**（对账 TS 的 try/catch）
		func() {
			defer func() { _ = recover() }()
			p.options.OnRun(e)
		}()
	}
}
