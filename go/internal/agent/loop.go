// Package agent 实现最小可用的 agent 主循环。
//
// 这是 Go 版运行时的「心脏」最小版：接收用户消息 → 调模型 → 执行工具调用 →
// 把结果回灌 → 循环直到模型给出终答或触及预算。
//
// 对账 src/agent/loop.ts 的骨架（3,126 行的完整版含 hook 管线、证据门禁、
// 交付门禁、投机解码等——那些是后续分波目标）。本版实现的是**闭环可跑**的
// 最小路径：read → edit → test → 交付 四步能走通。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/api/sse"
	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/client"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/skills"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// Config 是 agent 配置。
type Config struct {
	// Model 是模型名。
	Model string
	// MaxTokens 是单次回复上限。
	MaxTokens int
	// MaxTurns 是单次 run 的最大轮数（防无限循环）。
	MaxTurns int
	// SystemPrompt 是系统提示词。
	SystemPrompt string
	// Cwd 是工作目录。
	Cwd string
	// ApprovalMode 是审批档位。
	ApprovalMode string
	// Permissions 是用户配置的权限面（allow / deny 规则）。
	//
	// 对账 TS `config.permissions`。**nil = 无配置**（等价于空规则集）。
	//
	// **为什么经 Config 注入而非读配置文件**：Go 侧没有通用配置读取面
	// （只有 `.rivet-config.json` 的 verify 声明），与既有模式一致——
	// 依赖由装配层注入，使测试可隔离。
	//
	// **deny 的语义**（对账 TS `tool-pipeline.ts:1126`）：deny 规则覆盖
	// allow 规则**与审批档位**——「Deny rules always win, even in
	// dangerously-skip-permissions」。它在决策链的**最前面**，优先于
	// 硬闸门与路径授权。
	Permissions *PermissionConfig
	// TerseEnv 是 `RIVET_TERSE` 环境变量的原始取值（空串 = 未设）。
	//
	// 消费方：`BuildDynamicAppendix` → `ResolveTersenessFlags` —— 决定是否
	// 注入 `<output-style>`（terse 输出风格指令）。
	//
	// **为什么经 Config 注入而非直接读 os.Getenv**：与 `Permissions` 同一
	// 模式——依赖由装配层注入，使测试可隔离（测试不需要污染真实环境变量）。
	// 生产路径在 `cmd/tianshu/main.go` 用 `os.Getenv("RIVET_TERSE")` 填充。
	TerseEnv string
	// TddGateEnv 是 `RIVET_TDD_GATE` 环境变量的原始取值（空串 = 未设）。
	//
	// 消费方：`ParseTddGateConfig` → `EvaluateTddGate`（`tddgate.go`）——
	// 决定编辑工具是否被 TDD gate 建议/拦截。
	//
	// **为什么经 Config 注入而非直接读 os.Getenv**：与 `TerseEnv` 同一模式
	// ——依赖由装配层注入，使测试可隔离。生产路径在 `cmd/tianshu/main.go`
	// 用 `os.Getenv("RIVET_TDD_GATE")` 填充。
	//
	// **对账 TS**：TS 侧在会话构造时调一次 `parseTddGateConfig()` 并持有
	// （`_TDD_GATE_CONFIG`）。Go 侧改为每轮解析——`ParseTddGateConfig` 是
	// 纯函数且开销可忽略，换来「测试可直接注入配置字符串」的可测性。
	// **语义等价**（同一 env 取值 → 同一配置）。
	TddGateEnv string
	// SessionID 用于缓存路由亲和。
	SessionID string
	// StarDomain 是当前星域名（用于 advisory 预算与措辞适配）。
	//
	// 对账 TS 的 activeStarName。空串 = 无星域（用全局预算）。
	// **注意**：自主判断型星域（天权/瑶光）的 advisory 预算减为 1 条。
	StarDomain string
	// SkillRegistry 是 skill 注册表（供 Tier-1 发现层渲染 + skill 工具取用）。
	//
	// 对账 TS 的模块级单例 `skillRegistry`。Go 侧经 Config 注入，使测试可隔离。
	//
	// **两个消费方**：
	//   - `renderSkillDiscoveryBlock`（发现层，本刀）——把可用 skill 的
	//     name + description 注入请求尾部，让模型知道有哪些 skill 可加载
	//   - `buildToolCallParams`（工具）——`skill` 工具按名加载正文
	//
	// nil = 无发现层注入（且 skill 工具回退到包级 Default）。
	SkillRegistry *skills.Registry
}

// Event 是循环产出的事件（供 CLI 展示）。
type Event struct {
	Kind string // text | thinking | tool_start | tool_result | turn_end | done | error

	Text      string
	ToolName  string
	ToolInput map[string]any
	ToolID    string
	IsError   bool
	Turn      int
	// Usage 在 turn_end 事件上携带本回合的 token 计量。
	// 关键指标：CacheReadInputTokens > 0 表示前缀缓存命中。
	Usage *contract.Usage
	// StopReason 是本回合的结束原因（end_turn / tool_use / max_tokens）。
	StopReason string
}

// Loop 是 agent 主循环。
type Loop struct {
	cfg      Config
	client   *client.Client
	registry *tools.Registry
	// messages 是会话历史（wire.OrderedMap 保插入序——前缀缓存的前提）。
	messages []*wire.OrderedMap
	// Emit 接收事件。nil = 丢弃。
	Emit func(Event)
	// ToolParams 是工具调用的基础参数（注入依赖）。
	ToolParams *tools.CallParams
	// PlanModeState 是计划模式状态（第七十九刀接线）。
	//
	// 对账 TS `loop.ts:261` 的**实例字段** `planModeState`（由 `enterPlanMode`
	// 变更、`exitPlanMode` 清除）。**放在 Loop 而非 Config**：它是**会话可变
	// 状态**，不是启动期配置——`Config` 的其它字段（如 `TddGateEnv`）在会话内
	// 不变，而本字段由 `plan` 工具在运行中改写。
	//
	// **Go 侧此前无该字段**——`planmode.go` 的 `CheckPlanMode`（5 段分支，
	// oracle 对账过）在生产代码**零消费**，`plan` 工具的 `enter`/`exit` action
	// 只能报错（`plan.go:176` 说「尚未移植写工具禁用机制」），**而机制其实已实现**。
	//
	// 取值：`PlanModeOff`（默认）/ `PlanModePlanning`。
	PlanModeState PlanModeState
	// ActivePlanFilePath 是当前活动计划文件——**仅允许对该路径写入**（计划期
	// 唯一的写例外，对账 TS `planModePathsMatch`）。
	//
	// 对账 TS `loop.ts` 的 `activePlanFilePath`。空串 = 无活动计划文件。
	// 由 `plan` 工具的 enter action 设置、exit 清除。
	ActivePlanFilePath string
	// State 是会话状态容器（跨轮的文件/验证/决策感知）。
	//
	// 对账 TS 侧 loop.ts:848 的 `new SessionStateManager(this.config.sessionId)`。
	// nil 时跳过状态更新（最小可跑路径）。
	State *session.Manager

	// turnBudget 是单轮工具结果的 token 预算（跨轮复用，每轮 reset）。	//
	// 对账 TS `loop.ts:543` 的 `turnBudget: TurnBudget = createTurnBudget(0)`
	// 与 `turn-orchestrator.ts:593` 的**每轮重建**：
	// `turnBudget = createTurnBudget(rssRatio)`。
	//
	// **为什么每轮重建而非 reset**：档位随内存压力变化——压力升高时预算应
	// 收紧（50k → 25k → 0）。重建才能换档；reset 只清用量。
	//
	// nil 时跳过预算（最小可跑路径，对账 TS 的"无快照"分支）。
	turnBudget *TurnBudget

	// wedge 是跨轮的死循环检测状态。
	//
	// 对账 TS 的 `deps.state.wedgeToolFingerprint` / `wedgeRepeatCount`
	// （turn-orchestrator.ts:124-126）。**必须在 Loop 上而非 Run 局部**——
	// 状态跨轮累积才构成「连续重复」的判定基础。
	wedge wedgeState

	// RSSRatioFn 覆盖内存压力比来源（测试注入用）。
	//
	// nil = 用 `CurrentRSSRatio()`（真实探针）。对账 TS 的
	// `options.memoryUsage` 注入点（resource-sensor.ts:31）。
	RSSRatioFn func() float64

	// EfficacyStore 是跨会话效能信息素（JSONL 持久化）。
	//
	// nil 时不做先验播种与写回——增强而非必需。
	EfficacyStore *AdvisoryEfficacyStore
	// lastEfficacyFlush 是上次 flush 时的 per-key 计数快照。
	//
	// **mergeAndSave 只收增量**，差分基线在这里。不维护它会导致每次写回都
	// 把累计值当增量重复叠加（计数翻倍）。
	lastEfficacyFlush map[string]EfficacyDelta
	// Persist 是会话持久化器（transcript 落盘 + 元数据）。
	//
	// nil 时跳过落盘（headless 一次性跑或测试场景）。
	Persist *session.Persist
	// Listener 把内存消息变更镜像到 Persist（落盘 + 元数据更新）。
	//
	// 对账 TS 的 `attachSessionPersistListener`。nil 时跳过。
	Listener *session.PersistListener

	// Hooks 是运行时 hook 管线（五阶段 CVM）。
	//
	// 对账 TS 侧 loop.ts 的 runtimeHooks。nil 时跳过全部 hook 调用——
	// hook 是增强而非必需（headless 一次性跑或测试场景不装）。
	Hooks *Pipeline
	// pathGrants 是会话级路径授权存储（越界文件读写的运行时授权）。
	//
	// 对账 TS 的模块级 `_grants`（`path-grants.ts:59`）。**为什么挂在 Loop
	// 而非包级**：TS 是单进程单会话假设下的包级数组，但 sidecar 在一个进程里
	// 承载多会话——包级会让 A 工作区的授权泄漏给 B。Go 侧用实例级存储 +
	// scope 字段双保险。
	//
	// **消费者**：`executeTool` 的 `pathGrantNeed` 分支（skip 档授予 / 其他档拦）。
	// nil 时不授予也不拦（最小可跑路径——但出界写仍被 pathsafe 拦在工作区外）。
	pathGrants *pathGrantStore
	// selfTree 是本进程的自身 + 祖先 PID（自身进程树保护用）。
	//
	// **在构造时快照**（对账 TS `selfProcessTree()` 的缓存语义）：PID 在
	// 进程生命周期内不变，每轮重读是浪费；且快照让门控成为纯判定。
	//
	// **消费者**：`executeTool` 的 selfKill 门（`kill $PPID` 类命令）。
	selfTree selfKillProcessTree
	// evidence 是 TDD gate 的编辑计数（补 `session.Manager` 缺的相对计数）。
	//
	// **与 `State` 的分工**：`State`（`session.Manager`）记累计事实
	// （FileIndex / Verification 列表）；本计数器记**距上次验证的连续代码
	// 编辑数**——那是相对计数，验证即归零，累计式状态表达不了。
	//
	// **消费者**：`HasVerificationDebt()`（阈值 3）与将来的 sensorium
	// `evidenceState`。**第七十二刀起已有 gate 拦截消费方**——
	// `EvaluateTddGate`（`tddgate.go`）在门链末尾消费它（allow/suggest/block 三态）；
	// 本字段建立时（第一刀）只做追踪与派生，拦截是后续独立的一刀。
	evidence *evidenceTracker
	// hookState 是跨轮累积的 hook 快照状态。
	//
	// **为什么需要**：部分快照字段是**任务级**而非窗口级（如 TouchedTSFiles
	// ——"本会话写过 TS 文件"），它们必须跨轮存活。TS 侧这些字段由
	// buildRuntimeSnapshot 从 AgentLoop 的实例字段计算；Go 侧在此累积。
	hookState hookSnapshotState
	// Effects 是 hook 影响主流程的出口（注入消息 / 请求 theta / 标记 claim 等）。
	//
	// nil 时 hook 的 effect 调用退化为 no-op（*Safe 方法兜底）。
	Effects RuntimeHookEffects

	// Compact 是压缩边界（确定性路径：决策 → micro-compact）。
	//
	// 对账 TS turn-orchestrator Step 6b 的 `runCompaction`。
	// nil 时跳过压缩（增强而非必需——headless 一次性跑或测试场景不装）。
	//
	// **为什么挂在 Loop 上而非每轮新建**：熔断器状态（Failures）必须跨轮
	// 持有——否则「连续失败 3 次禁用 3 轮」永远攒不满。
	Compact *CompactBoundary

	// Trajectory 是工具调用轨迹（跨轮累积）。
	//
	// 对账 TS 的 `loop.ts:285 trajectory = new TrajectoryRecorder()`。
	// nil 时跳过记录（最小可跑路径）——**但 session split 的 handoff 会因此
	// 缺「工具轨迹 / 错误修复」章节**（降级）。生产装配应设它。
	Trajectory *compact.TrajectoryRecorder

	// Todos 是权威 todo 清单的读取器。
	//
	// 对账 TS 的 `config.getTodos ?? getTodos`（进程单例）。Go 侧无单例——
	// todo 工具持有实例字段，故由装配方注入读取函数。
	//
	// nil 时 handoff 的 task-state 回退到轨迹启发式（`ExtractTaskState`）。
	Todos func() []prompt.TodoItem

	// StreamedText 返回本回合模型流式文本（供 handoff 的决策提取）。
	//
	// nil 时视为空串（决策章节为空）。
	StreamedText func() string

	// Artifacts 是 artifact 存储（大工具结果的落盘与召回）。
	//
	// 对账 TS 的 `this.artifactStore`（`loop.ts:847` 的
	// `new ArtifactStore(join(cwd, '.rivet', 'artifacts'), sessionId)`）。
	//
	// nil 时跳过 L1 拦截与 read_section 召回（增强而非必需）。
	Artifacts *artifact.Store

	// Jobs 是本会话的后台任务注册表（bash 的 run_in_background 与 job 工具用）。
	//
	// 对账 TS 的 `this._jobs`（`loop.ts:850`）：
	//
	//	this._jobs = new SessionJobs(join(artifactDir, 'jobs'), source => touchActivity(...))
	//
	// **创建条件也对账 TS**：TS 侧在 `if (this.config.sessionId)` 内创建
	// ——无会话时 `getJobs()` 返回 undefined，bash 退回前台执行（TS 同语义）。
	//
	// **与 `Artifacts` 的区别（重要）**：本字段**有真实创建者**
	// （`NewLoop` 在 sessionId 非空时构造）。`Artifacts` 目前全库零赋值点，
	// 是既有的悬空字段——不要照它抄（那是「声明但无人消费」的既有缺陷）。
	//
	// nil 时：bash 走前台同步执行、job 工具提示「后台任务系统在当前上下文不可用」。
	Jobs *tools.SessionJobs

	// CheckpointDeps 是 checkpoint 替换的可注入增强。
	//
	// 对账 TS 的 `archiveDiscardedHistory` / `buildTaskAnchorAppendix` /
	// `runResumePreflightOai`——三者未移植，故此处以依赖注入形式留口
	// （nil 时对应增强跳过，行为与 TS 的缺省分支一致）。
	CheckpointDeps CheckpointDeps

	// Advisories 是劝导总线（hook 投递 → 渲染 → 注入 prompt）。
	//
	// nil 时跳过 advisory 注入——增强而非必需。
	//
	// 对账 TS 侧 `config.promptEngine.setHarnessAdvisoryBlock(advisoryBus.render(...))`
	// （turn-step-producer.ts:654-655）。**Go 侧的最小实现**：在每轮调模型前
	// render，把非空结果作为 **system-reminder 消息追加到尾部**——对账 TS 的
	// append-only 细断点通道（缓存安全：只追加尾部，不重写历史）。
	//
	// **未移植**：完整的 prompt appendix 机制（promptEngine 的
	// setHarnessAdvisoryBlock）。那是独立模块，见 HANDOFF。
	Advisories *AdvisoryBus

	// OnToolResult 是工具结果的观察回调（claim 提取的接入点）。
	//
	// **为什么用回调而非直接 import**：`internal/agent` 不该依赖
	// `internal/context`（认知层）——分层方向相反。回调让装配留在 CLI，
	// agent 包保持独立（与 consistency-check 的 getFileObservations 同模式）。
	//
	// 对账 TS 侧的 claim 提取触发点：工具执行完成后调用
	// `extractClaimsFromToolResult(ctx, meta)` 并把提案 propose 进 claim store。
	// nil 时跳过（认知层未装配）。
	OnToolResult func(ev *RuntimeToolEvent, turn int)

	// Readback 是劝导采纳率台账（核销闭环）。
	//
	// nil 时跳过全部核销调用——增强而非必需。
	//
	// **四个调用点**（对账 TS turn-step-producer / tool-execution / postTurn）：
	//   - render 后：`Track`（送达跟踪）
	//   - postTool：`ObserveTool`（行为观察，核销的证据源）
	//   - postTurn：`Evaluate`（核销评估）
	//   - postSession：`FlushAtSessionEnd`（未到期的如实报出，不判 ignored）
	Readback *AdvisoryReadback

	// toolDefsCache 缓存工具定义的构造结果。
	//
	// **为什么需要**：`toolDefs()` 原本每轮重建全部工具的 schema 并重新
	// 序列化——N 个工具 × M 轮的无谓开销。工具集在 Loop 生命周期内**不变**
	// （Registry 有 Register/Remove，但 loop 不调用）。
	//
	// **契约**：缓存值是**只读**的——调用方（client.Stream）不得修改切片或
	// 其中的 OrderedMap。`toolDefs()` 每次返回同一个切片指针。
	toolDefsCache []*wire.OrderedMap
}

// New 创建 agent loop。
func New(cfg Config, cl *client.Client, reg *tools.Registry) *Loop {
	l := &Loop{cfg: cfg, client: cl, registry: reg}
	// lastThinkingLength 的哨兵初值。
	//
	// **为什么必须显式设 -1**：Go 的零值是 0，而 0 是合法的「上一轮思考
	// 长度」（模型没输出思考）。不初始化则 reasoning-spiral hook 在第 1 轮
	// 看到 0，与「上一轮思考 0 字符」无法区分——虽然 0 < 3000 阈值不会误触发，
	// 但语义已错（快照声称「有上一轮数据」）。见 hookSnapshotState 的注释。
	l.hookState.lastThinkingLength = -1
	// 会话级路径授权存储（越界读写的运行时授权）。
	//
	// 对账 TS 的包级 `_grants`——Go 侧实例化以避免 sidecar 多会话串授权。
	l.pathGrants = newPathGrantStore()
	// 自身进程树快照（selfKill 门用）。
	//
	// 对账 TS `selfProcessTree()` 的**缓存语义**——PID 在进程生命周期内不变。
	// 构造时读一次，门控内是纯判定（可测，不引入 IO）。
	l.selfTree = currentProcessTree()
	// TDD gate 的编辑计数器（见字段注释）。
	l.evidence = newEvidenceTracker()
	if cfg.SessionID != "" {
		l.State = session.New(cfg.SessionID)
		// 后台任务注册表——**对账 TS 的创建条件**：`loop.ts:850` 在
		// `if (this.config.sessionId)` 内 `new SessionJobs(join(artifactDir,'jobs'), ...)`。
		// 无会话 → 保持 nil → bash 退回前台执行（TS 同语义）。
		//
		// 日志目录：`.rivet/artifacts/jobs`（对账 TS 的 `join(artifactDir, 'jobs')`，
		// artifactDir = `<cwd>/.rivet/artifacts`，见 TS `loop.ts:847`）。
		l.Jobs = tools.NewSessionJobs(
			filepath.Join(cfg.Cwd, ".rivet", "artifacts", "jobs"),
			nil, // 心跳上报：Go 侧无 touchActivity 对应物（stall-observer 未移植）
			0,   // → 默认 30s
		)
		// 会话持久化：落盘到 <cwd>/.rivet/sessions/<id>.jsonl。
		// 构造失败不阻塞会话（降级为无持久化）——持久化是增强而非必需。
		if p, err := session.NewPersist(cfg.SessionID, cfg.Cwd); err == nil {
			l.Persist = p
			l.Listener = session.NewPersistListener(p, l.State)
		}
	}
	if cfg.SystemPrompt != "" {
		l.messages = append(l.messages, wire.NewOrderedMap().
			Set("role", "system").
			Set("content", cfg.SystemPrompt))
	}
	return l
}

// rssRatio 返回当前内存压力比（供 `CreateTurnBudget` 选档）。
//
// 对账 TS `turn-orchestrator.ts:590` 的 `snap.memory.rssBytes /
// snap.memory.memoryLimitBytes`。**Go 侧无 V8 堆上限**——见
// `resourcesensor.go` 的差异说明。
//
// `RSSRatioFn` 非 nil 时用它（测试注入，对账 TS 的 `options.memoryUsage`）。
func (l *Loop) rssRatio() float64 {
	if l.RSSRatioFn != nil {
		return l.RSSRatioFn()
	}
	return CurrentRSSRatio()
}

// appendAndPersist 追加消息到历史并镜像到持久化存储。
//
// 对账 TS 的 `session.append()` → mutation listener → persist。
// 落盘失败不阻塞主循环（内存态仍是权威）——错误走 stderr。
func (l *Loop) appendAndPersist(msg *wire.OrderedMap) {
	l.messages = append(l.messages, msg)
	if l.Listener != nil {
		l.Listener.OnAppend(session.OaiMessageFromWire(msg))
	}
}

// FlushAdvisoryEfficacy 把会话内效能计数的**增量**合并写回跨会话信息素文件。
//
// 对账 TS `flushAdvisoryEfficacy`（loop.ts:443）。差分基线在
// `lastEfficacyFlush`——每 20 轮 + 会话结束各调一次，**重复调用安全**
// （零增量直接跳过）。失败不致命（信息素是尽力而为）。
//
// **为什么必须是增量**：mergeAndSave 内部是 `base[f] += delta[f]`——若传累计值，
// 第二次 flush 会把已写过的计数再叠加一次，计数翻倍。
func (l *Loop) FlushAdvisoryEfficacy() {
	if l.EfficacyStore == nil || l.Readback == nil {
		return
	}
	now := time.Now().UnixMilli()
	deltas := map[string]EfficacyDelta{}
	for key, s := range l.Readback.Stats() {
		base := l.lastEfficacyFlush[key]
		d := EfficacyDelta{
			Delivered:       float64(s.Delivered) - base.Delivered,
			Adopted:         float64(s.Adopted) - base.Adopted,
			Ignored:         float64(s.Ignored) - base.Ignored,
			ShadowHeld:      float64(s.ShadowHeld) - base.ShadowHeld,
			ShadowSatisfied: float64(s.ShadowSatisfied) - base.ShadowSatisfied,
		}
		// 只提交非零增量（对账 TS 的 `if (delta.x > 0 || ...)`）
		if d.Delivered > 0 || d.Adopted > 0 || d.Ignored > 0 ||
			d.ShadowHeld > 0 || d.ShadowSatisfied > 0 {
			deltas[key] = d
		}
		// **基线无条件推进**——即使本次增量为零，也要同步当前计数，
		// 否则下次会把这期间的累积误算成新增量。
		if l.lastEfficacyFlush == nil {
			l.lastEfficacyFlush = map[string]EfficacyDelta{}
		}
		l.lastEfficacyFlush[key] = EfficacyDelta{
			Delivered:       float64(s.Delivered),
			Adopted:         float64(s.Adopted),
			Ignored:         float64(s.Ignored),
			ShadowHeld:      float64(s.ShadowHeld),
			ShadowSatisfied: float64(s.ShadowSatisfied),
		}
	}
	if len(deltas) == 0 {
		return
	}
	// 写回失败不阻断会话（对账 TS 的 try/catch 吞掉）
	_ = l.EfficacyStore.MergeAndSave(deltas, now)
}

// SeedEfficacyPriors 从持久化文件加载先验并播种给 readback。
//
// 对账 TS loop.ts:781-790 的装配：
//
//	const priors = this.advisoryEfficacyStore.load()
//	this.advisoryReadback.seedPriors(...)
//
// **会话启动时调用一次**。加载失败回退冷启动（不致命）。
func (l *Loop) SeedEfficacyPriors() {
	if l.EfficacyStore == nil || l.Readback == nil {
		return
	}
	priors := l.EfficacyStore.Load(time.Now().UnixMilli())
	if len(priors) == 0 {
		return
	}
	out := make(map[string]EfficacyPriorCounts, len(priors))
	for k, p := range priors {
		out[k] = EfficacyPriorCounts{
			Delivered:       p.Delivered,
			Adopted:         p.Adopted,
			Ignored:         p.Ignored,
			ShadowHeld:      p.ShadowHeld,
			ShadowSatisfied: p.ShadowSatisfied,
		}
	}
	l.Readback.SeedPriors(out)
}

// FlushSession 在**会话结束**时调用：排空落盘缓冲 + 会话级核销收尾。
//
// **粒度很关键**——这里不是「每个 Run 结束」，而是整个会话退出（CLI 的输入
// 循环结束 / 进程收尾）。对账 TS 的 postSession hook：
//
//	`deps.readback.flushAtSessionEnd(ctx.snapshot.turn)`
//
// **为什么不能在 Run 里 flush**（TS advisory-readback.ts:250-260 的原始说明）：
// flush 会把仍未到期的 pending **清空**。若每个 user 轮都 flush，跨轮送达的
// advisory 在第 1 轮末就被丢弃——第 2 轮即使满足了谓词也无 pending 可核销，
// 表现为 `Adopted:0 Ignored:0`（既没采纳也没忽略，观测凭空消失）。
//
// 未到期的**不判 ignored**：advisory 在末轮送达时模型没走完观察窗口，判忽略
// 会把「没机会响应」记成「听了不做」，经 ignoredStreak / efficacy / 跨会话
// lift 三条路径压低效力评分。worker 尤其吃这一刀——中位只跑 2 轮。
// 返回未到期的 pending 列表（观测价值：TS 会把它写进遥测）。调用方可忽略。
func (l *Loop) FlushSession() []UnresolvedExpectation {
	if l.Listener != nil {
		_ = l.Listener.Drain()
	}
	// 终止所有后台 job——会话关闭时防孤儿（对账 TS 的 `killAll()`，
	// `loop.ts` 的会话收尾路径）。不这么做的话，dev server 会活过会话，
	// 占住端口直到进程退出。
	if l.Jobs != nil {
		l.Jobs.KillAll()
	}
	// 跨会话效能写回（postSession 兜底——对账 TS loop.ts:2401）
	l.FlushAdvisoryEfficacy()

	if l.Readback != nil {
		_, unresolved := l.Readback.FlushAtSessionEnd(l.SessionTurn())
		return unresolved
	}
	return nil
}

// recordUsage 把本轮 API 计量累加到会话状态。
//
// 对账 TS 的 `session.addUsage(usage)`。**InputTokens 是 cache-inclusive**，
// 不再叠加 cache_read/cache_creation（见 contract.Usage 的约定）。
func (l *Loop) recordUsage(u contract.Usage) {
	if l.State == nil {
		return
	}
	tu := session.TotalUsage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	}
	if u.ReasoningTokens != nil {
		tu.ReasoningTokens = *u.ReasoningTokens
		tu.HasReasoning = true
	}
	l.State.AddUsage(tu)
}

// Messages 返回当前会话历史（只读副本的浅拷贝）。
func (l *Loop) Messages() []*wire.OrderedMap {
	out := make([]*wire.OrderedMap, len(l.messages))
	copy(out, l.messages)
	return out
}

// SessionTurn 返回**会话级**轮次——对账 TS 的 `session.getTurnCount()`。
//
// **语义**（TS context.ts:360 / context.ts:202）：
//
//	turnCount = messages.filter(m => m.role === 'user').length
//
// 即「历史里 user 消息的条数」，**在单个 Run 内恒定**（一个 Run 只追加一条
// user 消息），跨 user 轮才推进。
//
// **为什么不能用 run 局部 turn**（TS turn-step-producer.ts:682-688 的原始警告）：
//
//	「必须与 runtime hook snapshot 使用同一 session turn 时钟；这里的 turn 是
//	 TurnOrchestrator.run 局部序号，而 postTool/postTurn 观察事件使用 session
//	 turn。混用会让 B2 在局部 turn=13 送达、事件却落在 session turn=2，
//	 course_changed 永远无法核销。」
//
// 移植时踩了同一个坑：Track 硬编码 0、Observe/Evaluate 用 run 局部 turn——
// 单 Run 内窗口就闭合，习惯化触发比 TS 频繁，且跨轮核销区间错位。
//
// **注意**：`l.messages` 只含持久化消息（buildRequestMessages 的 advisory
// 注入不写回），因此计数与 TS 的 oaiMessages 语义一致。
func (l *Loop) SessionTurn() int {
	n := 0
	for _, m := range l.messages {
		if role, ok := m.Get("role"); ok {
			if sv, ok := role.(string); ok && sv == "user" {
				n++
			}
		}
	}
	return n
}

// Run 执行一轮用户交互，直到模型给出终答或触及预算。
func (l *Loop) Run(ctx context.Context, userMessage string) error {
	// 动态 appendix（permission-note 等）注入 **user message 尾部**——
	// 不进 system prompt（那是 frozen 前缀，中途翻转会打断缓存）。
	// 详见 `dynamic_appendix.go` 的架构约束说明。
	l.appendAndPersist(wire.NewOrderedMap().
		Set("role", "user").
		Set("content", l.appendDynamicAppendix(userMessage)))

	maxTurns := l.cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50
	}

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		// ── 每轮重建预算（对账 TS `turn-orchestrator.ts:593`）──
		//
		// **重建而非 reset**：档位随内存压力变化（50k → 25k → 0），
		// reset 只清用量、换不了档。
		//
		// `rssRatio` 无快照时为 0（对账 TS 的 `snap ? ... : 0`）——此处总是
		// 有值（真实探针或注入），故直接取。
		l.turnBudget = CreateTurnBudget(l.rssRatio())

		// ── preTurn hook ──
		//
		// 在调模型**之前**——hook 可注入消息 / 调整感知。
		l.runHookPhase(ctx, PhasePreTurn, nil)

		// ── 调模型（流式）──
		collector := &turnCollector{}
		req := &api.ChatRequest{
			Model:     l.cfg.Model,
			Messages:  l.buildRequestMessages(),
			Tools:     l.toolDefs(),
			MaxTokens: intPtr(l.cfg.MaxTokens),
		}

		err := l.client.Stream(ctx, req, collector.handler(l, turn))
		if err != nil {
			l.emit(Event{Kind: "error", Text: err.Error(), Turn: turn})
			// 预算耗尽/不可重试错误——向上传播
			if client.IsBudgetExhausted(err) {
				return fmt.Errorf("重试预算耗尽：%w", err)
			}
			return err
		}

		// ── 累加本轮 token 计量（对账 session.addUsage）──
		l.recordUsage(collector.usage)

		// ── 把 assistant 回合追加到历史 ──
		assistantMsg := l.buildAssistantMessage(collector)
		l.appendAndPersist(assistantMsg)

		// ── 无工具调用 → 终答，结束 ──
		if len(collector.toolCalls) == 0 {
			u := collector.usage
			l.emit(Event{
				Kind: "done", Text: collector.text(), Turn: turn,
				Usage: &u, StopReason: collector.stopReason,
			})
			return nil
		}

		// ── 执行工具调用，把结果回灌 ──
		//
		// endTurnRequested 累积 batch 级信号（对账 TS tool-execution 的
		// `endTurn: endTurn || undefined`）：**任一**工具返回 EndTurn 即置位。
		// batch 内其余工具仍照常执行——TS 也是整个 batch 跑完才检查。
		endTurnRequested := false
		// errorCount 累积本轮失败工具数（wedge 守卫的判据之一）。
		//
		// 对账 TS `ExecuteBatchResult.errorCount`。
		errorCount := 0
		for _, tc := range collector.toolCalls {
			l.emit(Event{
				Kind: "tool_start", ToolName: tc.name, ToolInput: tc.input,
				ToolID: tc.id, Turn: turn,
			})

			result := l.executeTool(ctx, tc)

			// ── 单轮预算消费 + 耗尽包装（对账 TS `tool-pipeline.ts:1609-1630`）──
			//
			// **必须在 emit 与回灌之前**——包装后的内容要同时进入事件流与
			// 消息历史（TS 在 tool-pipeline 里替换 `finalContent` 后统一使用）。
			//
			// `result.RawPath` 由工具自己落盘（bash 的 `persistRawOutput`）；
			// 无 rawPath 时 `WrapStoredIfExhausted` 回退字面量 `"unknown"`。
			if l.turnBudget != nil {
				if wrapped, ok := WrapStoredIfExhausted(
					l.turnBudget, tc.name, result.Content, result.RawPath); ok {
					result.Content = wrapped
				}
			}

			l.emit(Event{
				Kind: "tool_result", ToolName: tc.name, ToolID: tc.id,
				Text: result.Content, IsError: result.IsError, Turn: turn,
			})

			// ── postTool hook ──
			//
			// 在工具结果回灌历史**之后**、下一轮之前。hook 在此看到完整的
			// 工具事件（含 success / target / 结果内容）。
			toolEvent := &RuntimeToolEvent{
				Name:          tc.name,
				Success:       !result.IsError,
				Target:        toolTarget(tc.input),
				Input:         tc.input,
				IsError:       result.IsError,
				ResultContent: result.Content,
			}
			l.recordToolForHooks(toolEvent)
			l.runHookPhase(ctx, PhasePostTool, toolEvent)

			// ── readback 行为观察（核销的证据源）──
			//
			// 对账 TS tool-execution：postTool 把工具事件喂给 readback。
			// **注意 target 语义**：bash → command；写/读类 → file_path。
			// toolEvent.Target 已按此规则构造（见 toolTarget）。
			if l.Readback != nil {
				l.Readback.ObserveTool(ObservedToolEvent{
					Turn:    l.SessionTurn(),
					Name:    toolEvent.Name,
					Target:  toolEvent.Target,
					IsError: toolEvent.IsError,
				})
			}

			// ── claim 提取（认知层）──
			//
			// 在 postTool hook 之后——hook 可能已改状态（如标记 claim 过期），
			// 提取看到的是最新状态。对账 TS 的工具执行后提取触发点。
			if l.OnToolResult != nil {
				l.OnToolResult(toolEvent, turn)
			}

			// ── lossy 观测守卫（纠正性内联标记）──
			//
			// 对账 TS `tool-execution.ts:600-611`（`executeBatch` 里对
			// `toolResults` 的循环）——**在结果回灌历史之前**，若内容
			// 同时带结构化有损标记与负向断言，前置注入
			// `[⚠ VERIFICATION_REQUIRED]`，让模型读到时就带上「必须独立
			// 交叉验证」的约束。
			//
			// **位置有意义**：必须在 `appendAndPersist` 之前（否则标记
			// 只进了事件流、没进历史，模型看不到）；也必须在 `emit` 之后
			// （TS 的 UI 事件发的是工具原始输出，标记是给模型的）。
			//
			// **与 lossy-observation hook 的分工**：hook 是预防性 advisory
			// （任何有损输出都提醒）；本守卫是纠正性内联标记（仅当有损
			// **且**含负向断言）。两者共用 `lossy-markers.go` 的标记表。
			//
			// **幂等**：`GuardLossyToolResult` 自带去重（已含标记则原样返回）。
			result.Content = GuardLossyToolResult(result.Content)

			l.appendAndPersist(wire.NewOrderedMap().
				Set("role", "tool").
				Set("tool_call_id", tc.id).
				Set("content", result.Content))

			// ── endTurn 信号累积 ──
			//
			// 工具（如 ask_user_question）请求终止回合时置位。**必须在回灌
			// 之后**——历史须保持良构（tool_calls 与 tool_result 配对），
			// 提前 break 会留下孤儿 tool_call。
			if result.EndTurn {
				endTurnRequested = true
			}
			if result.IsError {
				errorCount++
			}
		}

		// ── endTurn：把本回合收为 final 并退出 ──
		//
		// 对账 TS turn-orchestrator.ts:1033 / :1264 的
		//   `if (r.endTurn) { emitStop({source:'end-turn', voluntary:true});
		//    completeTurn({isFinal:true}); break }`
		//
		// **语义**：模型调了 ask_user_question 这类工具，此刻在等用户输入——
		// 继续工具循环只会让模型自问自答。收为 final 后 Run 返回，宿主
		// （REPL / headless）读下一条用户消息作为答案。
		//
		// **为什么在此处而非循环内 break**：与「无工具调用 → 终答」分支同形，
		// 且必须让本批**全部**工具结果先落历史（TS 亦然）。
		if endTurnRequested {
			u := collector.usage
			l.emit(Event{
				Kind: "done", Text: collector.text(), Turn: turn,
				Usage: &u, StopReason: collector.stopReason,
			})
			return nil
		}

		u := collector.usage
		l.emit(Event{
			Kind: "turn_end", Turn: turn,
			Usage: &u, StopReason: collector.stopReason,
		})

		// ── 记录本轮结果（供下一轮快照）──
		//
		// 对账 TS loop-factory.ts:537,543——`lastThinkingLength` 与
		// `lastTurnHadTools` 读的是**上一轮**的状态，故必须在轮末写入。
		// reasoning-spiral hook（preTurn 阶段）据此判断「上一轮长推理零工具」。
		l.recordTurnOutcome(collector.thinkingLen(), len(collector.toolCalls) > 0)

		// ── wedge-loop 守卫（语义级死循环检测）──
		//
		// 对账 TS turn-orchestrator.ts:1044-1073。**必须在工具结果全部回灌
		// 之后**——与 endTurn 同理，历史须保持良构。
		//
		// `maxTurns` 是计数上限，本守卫是语义检测：模型陷入「同批次反复
		// 被拒」时不再烧满 50 轮（每轮灌一份完整工具结果、上下文线性膨胀）。
		//
		// **`batchErrored` 的判据**：本轮**全部**工具失败。注意用
		// `len(collector.toolCalls) > 0` 兜底——零工具批次不参与（那种情况
		// 已在上方的「无工具调用 → 终答」分支返回）。
		batchErrored := len(collector.toolCalls) > 0 && errorCount == len(collector.toolCalls)
		fingerprint := ""
		if batchErrored {
			fingerprint = toolBatchFingerprint(collector.toolCalls)
		}
		l.wedge.observeBatch(batchErrored, fingerprint)
		if l.wedge.shouldTerminate() {
			l.emit(Event{
				Kind: "error", Turn: turn,
				Text: "检测到死循环（同一工具批次连续 " + strconv.Itoa(l.wedge.repeatCount) +
					" 次全部失败：" + l.wedge.wedgeDetail(collector.toolCalls) + "）——已终止本回合。",
			})
			return nil
		}

		// ── 压缩边界（Step 6b 等价）──
		//
		// 对账 TS turn-orchestrator.ts:595 的 `runCompaction(turn, snap)`
		// → compact-boundary-coordinator → `maybeCompact`。
		//
		// **为什么在 turn 边界而非 mid-turn**：mid-turn 改历史会让已发出的
		// 请求前缀失效，前缀缓存命中率归零。TS 侧同一约束（1M 窗口的确定性
		// 重写只在 `loopTurn === 0` 运行）。
		//
		// **压缩失败不中断主循环**——失败只记入熔断器（见 MaybeCompact 的
		// 说明），主循环继续。这与 TS 的 `RunCompactionResult.shouldAbort`
		// 语义一致（abort 只由用户中断触发，不由压缩失败触发）。
		l.maybeCompactAtBoundary(turn)

		// ── postTurn hook ──
		//
		// 轮末——hook 在此做跨轮判断（如"改了 TS 但没 typecheck"）。
		l.runHookPhase(ctx, PhasePostTurn, nil)

		// ── readback 核销评估 ──
		//
		// 对账 TS postTurn：`readback.evaluate(ctx.snapshot.turn)`——**session turn**，
		// 不是 run 局部序号（见 SessionTurn 的说明）。
		if l.Readback != nil {
			l.Readback.Evaluate(l.SessionTurn())
		}

		// ── 跨会话效能写回（每 20 轮）──
		//
		// 对账 TS turn-step-producer.ts:776：`if (turn > 0 && turn % 20 === 0)`。
		//
		// **为什么定期写**：崩溃不丢账（不必等到会话正常结束）。会话结束时
		// `FlushSession` 会再兜底一次（零增量时是空操作）。
		//
		// **注意 turn 语义**：TS 用的是 **run 局部序号**（此处的 `turn` 正是），
		// 不是 session turn——因为这是「每 20 个模型轮」的节流，不是窗口判定。
		if turn > 0 && turn%20 == 0 {
			l.FlushAdvisoryEfficacy()
		}
	}

	return fmt.Errorf("已达最大轮数 %d——任务未完成（防无限循环）", maxTurns)
}

// approvalBlockedMarker 是审批拒绝文案的稳定标记。
//
// **为什么用标记而非裸文案**：它同时是 ① 模型可见的拒绝信号、
// ② 接线测试的断言锚点。单独提取成常量，避免测试与实现漂移。
//
// 语义对账 TS `tool-pipeline.ts:1283` 的 deny 分支——「instructive,
// non-retry denial」：这不是对改动本身的否决，而是「需人工批准，模型无法
// 自行授予」；重复发同一调用只会再撞同一道门。
const approvalBlockedMarker = "需人工批准（agent 无法自行授权）"

// executeTool 执行单个工具调用。
func (l *Loop) executeTool(ctx context.Context, tc toolCall) contract.Result {
	// 截断的调用必须拒绝执行——把半截参数喂给工具（尤其 bash）比失败更危险。
	// 仍返回一条 tool_result 以保持历史良构（tool_calls 与 tool_result 需配对）。
	if tc.truncated {
		return contract.Result{
			Content: "工具参数在流中被截断，未能完整接收——该调用已被拒绝执行。请重试。",
			IsError: true,
		}
	}

	p := l.buildToolCallParams(tc)
	if l.ToolParams != nil {
		// 继承注入依赖（OnFileWrite 等）
		p.OnFileWrite = l.ToolParams.OnFileWrite
		p.OnOutput = l.ToolParams.OnOutput
		p.OnLeaveMark = l.ToolParams.OnLeaveMark
		p.OnAskUserQuestion = l.ToolParams.OnAskUserQuestion
		p.OnSkillInvoked = l.ToolParams.OnSkillInvoked
		p.OnSkillCompleted = l.ToolParams.OnSkillCompleted
		// SkillRegistry：skill 工具的注册表来源。
		//
		// **必须在此继承**——`buildToolCallParams` 只填 Loop 自己知道的字段
		// （Input/Cwd/ArtifactStore/...），注册表是**装配层注入**的依赖。
		// 漏了这行则工具恒走包级 `skills.Default`（生产路径为空注册表），
		// 表现为「skill 工具总是报未找到」——与第三十七刀 OnLeaveMark
		// 的悬空接线同一类缺陷。
		p.SkillRegistry = l.ToolParams.SkillRegistry
	}
	// 回退：ToolParams 未设注册表时用 Config 的。
	//
	// **为什么需要回退**：注册表有**两个**消费方——发现层读 `cfg.SkillRegistry`、
	// 工具读 `ToolParams.SkillRegistry`。装配方漏设任一个会让功能**静默半失效**
	// （发现层列得出 skill，但工具报未找到）。回退消除这个双来源陷阱：
	// 只设 `cfg.SkillRegistry` 即可全链路可用。
	if p.SkillRegistry == nil {
		p.SkillRegistry = l.cfg.SkillRegistry
	}

	// ── 计划模式回调（第七十九刀接线）──
	//
	// 对账 TS `loop.ts` 传给 plan 工具的 `enterPlanMode` / `exitPlanMode` ref。
	// **为什么在此注入**：`plan` 工具是无状态的（只收 `CallParams`），无法直接
	// 改 `Loop` 的字段——故经回调暴露。这与 `OnSkillInvoked` / `OnFileWrite`
	// 同一模式。
	//
	// **仅在主 agent 上下文注入**：worker/子代理不得切主代理的计划模式
	// （对账 TS 的 fail-closed）。Go 侧当前无子代理调用 `executeTool` 的路径，
	// 故此处无条件注入；将来引入子代理时须按上下文条件化。
	p.EnterPlanMode = l.enterPlanMode
	p.ExitPlanMode = l.exitPlanMode

	started := time.Now()

	// ── 计划模式门（第七十九刀接线）──
	//
	// 对账 TS `tool-pipeline.ts:1062` 的 plan-mode gate。**Go 侧此前零接线**：
	// `planmode.go` 的 `CheckPlanMode`（5 段分支，oracle 对账过）在生产代码
	// **零消费**——`Loop` 无状态字段、门链不调它。后果是 `plan` 工具的
	// `enter`/`exit` action 只能报错（`plan.go:176` 说「尚未移植写工具禁用
	// 机制」），**而机制其实已实现**。
	//
	// **顺序**：TS 侧 plan-mode(1062) 在 deny(1137) **之前**——故本门置于
	// 门链最前（deny 门之前）。语义：计划模式是**会话级纪律**，先于用户边界。
	//
	// **例外**（`CheckPlanMode` 内部处理，逐条对账 TS）：
	//   - 写**活动计划文件** → 放行（计划期唯一的写目标）
	//   - 写 `.rivet/scratch/` 探针 → 放行（计划期验证声称的标准出路）
	//   - 白名单的 18 个只读/无害工具 → 放行
	//
	// **Go 侧的差异（有意）**：TS 的 `delegatesWriteCapableProfile` 依赖
	// profile registry（`profileIsPlanModeSafe`）——Go 侧未移植，故恒 false
	// （该分支不触发）。
	if l.PlanModeState == PlanModePlanning {
		target, _ := tc.input["file_path"].(string)
		var activePlan *string
		if l.ActivePlanFilePath != "" {
			activePlan = &l.ActivePlanFilePath
		}
		planRes := CheckPlanMode(l.PlanModeState, tc.name, PlanModeCheckContext{
			Cwd:                l.cfg.Cwd,
			TargetFilePath:     target,
			ActivePlanFilePath: activePlan,
		})
		if !planRes.Allowed {
			reason := planRes.Reason
			if reason == "" {
				reason = "Plan Mode: write operations blocked"
			}
			return contract.Result{Content: reason, IsError: true}
		}
	}

	// ── 用户 deny 规则门（第五十二刀接线）──
	//
	// 对账 TS `tool-pipeline.ts:1126-1143` 的 deny 分支。**Go 侧此前完全没有
	// 这一环**：`Config` 无 `Permissions` 字段、无判定函数，用户配置里写的
	// `permissions.deny` 被**静默忽略**——用户设的硬边界不生效。
	//
	// **为什么必须放在决策链最前（关键）**：TS 把它放在所有检查之前，注释
	// 写明「Deny rules always win, even in dangerously-skip-permissions」。
	// 它是**覆盖一切**的门——优先于 `unconditionalApproval`、硬闸门、路径
	// 授权。放到后面就会被 `skip` 档或 pathGrant 分支绕过，等于边界失效。
	//
	// **与第五十刀/第五十一刀的同类性**：那两刀修的是「安全机制存在但没接线」
	// （`NeedsApproval` 零调用者 / 出界路径无门控）；本刀修的是「安全机制
	// 根本不存在」——同一族缺陷的第三种形态。
	//
	// **行为**：命中即拒，返回模型可见的「指令性非重试拒绝」——要点是
	// **换路而非重试**（重试只会撞同一道门，浪费 turn 预算）。
	if l.cfg.Permissions != nil &&
		(IsToolDenied(tc.name, p.Input, l.cfg.Permissions.Deny) ||
			bashDeniedFor(l.cfg.Permissions, tc)) {
		return contract.Result{Content: DeniedRuleReason(tc.name), IsError: true}
	}

	// ── 自身进程树保护（第六十八刀接线）──
	//
	// 对账 TS `tool-pipeline.ts:1136-1143` 的 `selfKill` 分支。**Go 侧此前
	// 完全没有这一环**——`IsSelfDestructiveKill` 的对应实现不存在。
	//
	// **为什么需要**：bash 工具用 `exec.Command` 启动 shell，故 agent 就是
	// 命令的**直接父进程**（实测：`SELF=72921 PPID=72918`，`72918 COMM=/tmp/ts-probe`）。
	// shell 里 `kill $PPID` 直接杀掉 agent 自己 → **当前 turn 中断**。
	//
	// **为什么独立于权限门（不与 deny 门合并）**：TS 注释写明它是
	// 「Always on, independent of config/approval mode」——**不是用户配的
	// 边界**，而是运行时自我保护。故即使 `Permissions == nil`（无配置）
	// 也须生效，且不受档位影响（skip 档也不例外）。
	//
	// **只覆盖 PID 类**（`kill <自身/祖先 pid>` / `taskkill /PID <n>`）；
	// 镜像名类（`pkill node`）**有意不移植**——理由见
	// `self_preservation.go` 文件头「有意差异」。
	if tc.name == "bash" {
		if cmd, ok := tc.input["command"].(string); ok &&
			IsSelfDestructiveKill(cmd, l.selfTree) {
			return contract.Result{Content: selfKillBlockReason(), IsError: true}
		}
	}

	// ── 审批硬闸门（第五十刀接线）──
	//
	// 对账 TS `tool-pipeline.ts` 的审批门控（`shouldAsk` 决策树）。**Go 侧此前
	// 完全没有这一环**：`tools.Registry.NeedsApproval` 零调用者，工具的
	// `RequiresApproval` 实现（bash 破坏性命令 / git commit / 写工具）返回值
	// **无人消费**——默认档 `auto-safe` 下 `rm -rf`、`git reset --hard` 会
	// 静默执行，而工具层声明了「需批准」。
	//
	// **为什么用 `RequiresHardGate` 而非 `NeedsApproval`（关键）**：
	//
	//   - `NeedsApproval` 背后是**档位驱动**的 `RequiresApproval`——写工具在
	//     非放开档恒返回 true。TS 侧这类返回值由完整决策树消费（档位 × 风险
	//     分级 × pathGrant × allowlist × headless 中和），**不是直接拒绝**。
	//     Go 侧无该决策树与提示通道，直接消费会拦下所有写操作（首版实测：
	//     6 个既有测试转红，`write_file` 在默认档下完全不可用）。
	//   - `RequiresHardGate` 表达「**任何档位都不能绕过**」——bash 的破坏性
	//     命令即此类。这是无提示通道时唯一能安全闭合的子集。
	//
	// **scope（有意收窄）**：只接硬闸门。完整的档位门控待审批提示往返通道
	// （`onApprovalRequired` 对应物）与 `assessToolRisk` 风险分级落地后接入。
	//
	// **行为**：命中时不执行工具，返回模型可见的「指令性非重试拒绝」
	// （对账 TS `tool-pipeline.ts:1283` 的 deny 分支语义）——不是否决改动
	// 本身，而是「需人工批准，模型无法自行授予」；重复发同一调用只会再撞
	// 同一道门，故文案明确要求不要重试。
	if l.registry.RequiresHardGate(tc.name, p) {
		reason := ""
		if tc.name == "bash" {
			if cmd, ok := tc.input["command"].(string); ok {
				reason = tools.DestructiveReason(cmd)
			}
		}
		target := ""
		if tc.name == "bash" {
			if cmd, ok := tc.input["command"].(string); ok {
				target = " (" + truncateRunes(cmd, 60) + ")"
			}
		}
		msg := "工具 \"" + tc.name + "\"" + target + " " + approvalBlockedMarker + "。\n"
		if reason != "" {
			msg += "命中破坏性模式：" + reason + "。\n"
		}
		msg += "这是**不可逆操作**，agent 无法自行批准。\n" +
			"不要重复发出同一调用——它会再次撞上同一道门，白白消耗轮次预算。\n" +
			"请改用其他方式完成任务，或停下来向用户说明哪一步需要授权。"
		return contract.Result{Content: msg, IsError: true}
	}

	// ── 档位审批门（第六十二刀接线）──
	//
	// 对账 TS `tool-pipeline.ts:1213-1232` 的 `shouldAsk` 档位分支。**Go 侧
	// 此前完全没有这一环**：`Registry.NeedsApproval` 零调用者，4 个写工具
	// 在 **manual 档**下 `needsApproval=true` 却无人消费 → 写操作**静默执行**
	// （fail-open：用户选最严档期望逐次确认，实际无人把关）。
	//
	// **顺序**：在硬闸门**之后**（硬闸门是「任何档位都不能绕过」，优先级更高）。
	//
	// **与 TS 的一处有意偏差（第六十九刀修正本段）**：TS 三元链里
	// `pathGrantNeed` 在**档位分支之前**（skip → pathGrant → protection →
	// bashWrite → … → 档位）。Go 侧相反——本门在 pathGrant 门**之前**。
	// （**不写行号**：注释增删会让行号漂移；定位用门名 grep。）
	//
	// **为什么这样也正确**：两门**不冲突**——档位门只对 `decideApprovalGate`
	// 判定为拦的调用生效，而 pathGrant 门处理的是「路径越界」这一**独立维度**
	// （文件工具出界 → 授权或拒绝）。一个调用可能同时命中两门，谁先拦都得到
	// 「拒绝」结果，差别只在**文案**（用户看到"需批准"还是"路径越界"）。
	// 本段此前写成「对账 TS 的三元链顺序」——**不准确**，Go 侧并未逐位对齐。
	//
	// **与硬闸门的互补**：硬闸门只覆盖 bash 破坏性命令（`HardGate` 接口）；
	// 本门覆盖「档位驱动的写工具审批」——两者覆盖不同的工具面。
	if decision := decideApprovalGate(p, l.registry.NeedsApproval(tc.name, p), isHighRiskCall(tc)); decision.Block {
		// **allowlist 豁免（第六十四刀补全）**：TS 三元链里 `allowlisted ? false`
		// 是**全工具面**的独立分支（`tool-pipeline.ts:1226`），位于
		// `bashWriteRequiresApproval` 之后、档位分支之前。Go 侧此前只把它
		// 消费在 `bashWriteNeedsApproval` 内部（豁免 bash 写门），其余工具
		// 在 manual 档下即使命中 allow 规则仍被拦。
		//
		// **为什么在档位门内做豁免而非另立一门**：TS 的语义是「命中 allow
		// 规则 → 不拦（shouldAsk=false）」，效果等同于「跳过档位分支」。
		// 硬闸门/deny 门在更前，不受影响（allow 不能豁免它们）。
		if !IsToolAllowed(tc.name, tc.input, allowRulesOf(l.cfg)) {
			target := ""
			if tc.name == "bash" {
				if cmd, ok := tc.input["command"].(string); ok {
					target = " (" + truncateRunes(cmd, 60) + ")"
				}
			}
			msg := "工具 \"" + tc.name + "\"" + target + " " + approvalBlockedMarker + "。\n"
			msg += decision.Reason + "。\n" +
				"这是**需人工批准的操作**，agent 无法自行授予。\n" +
				"不要重复发出同一调用——它会再次撞上同一道门，白白消耗轮次预算。\n" +
				"请改用其他方式完成任务，或停下来向用户说明哪一步需要授权。"
			return contract.Result{Content: msg, IsError: true}
		}
	}

	// ── 越界路径授权门（第五十一刀 Wave 2c 接线）──
	//
	// 对账 TS `tool-pipeline.ts:1240-1249` 的 `pathGrantNeed` 分支。**Go 侧
	// 此前完全没有这一环**：文件工具出界读写时，`pathsafe.Validate` 会判
	// 越界并拒绝——但那是**工具内部**的拒绝，模型只看到一句泛泛的失败，
	// 且 skip 档下用户承诺的「零审批打断」无法兑现（每次都撞同一道墙）。
	//
	// **TS 的两种处理**（本分支逐条对账）：
	//
	//   - **skip 档**（`dangerously-skip-permissions`）→ 出界路径**首触即授**
	//     （`grantPath(dirname(p), mode, {cwd})`）。TS 注释：「完全读写档：
	//     零审批打扰承诺——出界路径首触即授（会话级，不出会话）」。
	//   - **其他档** → 弹审批（`onApprovalRequired`）；Go 侧**无提示通道**，
	//     故降级为「指令性非重试拒绝」——与第五十刀硬闸门同款形态。
	//
	// **为什么授予 `dirname(p)` 而非 p**：TS 传 `dirname(pathGrantNeed.paths)`。
	// 授权粒度是**目录子树**（`IsWriteGranted` 按段边界判包含），授予文件本身
	// 会让同目录的兄弟文件仍被拦——那违背「零打断」的初衷。
	//
	// **scope 隔离**：授予时传 `l.cfg.Cwd`——授权绑定本会话工作区，
	// 不泄漏给同进程的其他会话（sidecar 场景）。
	if l.pathGrants != nil {
		if need := OutOfWorkspaceFilePaths(l.cfg.Cwd, tc.name, tc.input, l.pathGrants); need != nil {
			if l.cfg.ApprovalMode == "dangerously-skip-permissions" {
				// skip 档：首触即授（会话级）。
				for _, p := range need.Paths {
					l.pathGrants.GrantPath(filepath.Dir(p), grantModeFromPathsafe(need.Mode), l.cfg.Cwd)
				}
				// 授予后继续执行——工具内部的 pathsafe 查询会看到新授权。
			} else {
				// 其他档：无提示通道 → 指令性非重试拒绝。
				modeLabel := "读"
				if need.Mode == pathsafe.ModeWrite {
					modeLabel = "写"
				}
				msg := "工具 \"" + tc.name + "\" 的目标路径在工作区之外（" + modeLabel +
					"），" + approvalBlockedMarker + "。\n"
				msg += "越界路径："
				for i, p := range need.Paths {
					if i > 0 {
						msg += "、"
					}
					msg += p
				}
				msg += "\n这是**工作区边界**约束，agent 无法自行放宽。\n" +
					"不要重复发出同一调用——它会再次撞上同一道门。\n" +
					"请改用工作区内的路径，或停下来向用户说明需要访问哪个目录。\n" +
					"（若用户已显式选择完全访问档 `dangerously-skip-permissions`，出界路径将首触即授。）"
				return contract.Result{Content: msg, IsError: true}
			}
		}
	}

	// ── bash 写命令审批门（第六十三刀接线）──
	//
	// 对账 TS `tool-pipeline.ts:1163-1172` 的 `bashWriteRequiresApproval` 分支。
	// **Go 侧此前完全没有这一环**：`RequiresBashWriteApproval`
	// （`approval_risk.go` 的同名函数）零调用者——本门即其接线（第六十三刀）。
	//
	// **缺口实测**（第六十三刀）：`mkdir foo` 在 **manual 档**下——
	// 硬闸门 false（非破坏性）、档位门 false（bash 的 `RequiresApproval`
	// 已订正为 `isDestructiveCommand`）→ **静默执行**。TS 侧同一命令被
	// `bashWriteRequiresApproval` 拦下。
	//
	// **为什么硬闸门不能覆盖它**（曾误判为「已覆盖」）：硬闸门用
	// `isDestructiveCommand`（`rm -rf` / `git reset --hard`），本门用
	// `BashCommandMayWrite`（`mkdir|touch|cp` / `>file`，risky 含
	// `rm|mv|chmod|dd`）——两个命令集**只有部分交集**，`mkdir` 不在硬闸门内。
	//
	// **顺序**：在 pathGrant 门**之后**（对账 TS 三元链：`pathGrantNeed` →
	// `bashWriteRequiresApproval`），在 `Execute` 之前。
	//
	// **skip 档短路（第六十四刀修回归）**：TS 的三元链把
	// `skipAllApproval ? false` 放在 `pathGrantNeed` **之前**——即「完全访问档
	// 零审批打扰承诺」优先于本门。Go 侧 `decideApprovalGate` 内部虽有 skip
	// 短路，但它 return 后流程**继续走到本门** → skip 档下 bash 写命令全被拦
	// （提交后审查发现，headless 下无人可批 → 死锁）。
	// 此处显式短路，对账 TS 的优先级。
	//
	// **硬闸门不受此影响**：它在更前（`RequiresHardGate`）、且不读档位——
	// 破坏性命令在 skip 档下仍被拦（有测试钉住）。
	if l.cfg.ApprovalMode != "dangerously-skip-permissions" &&
		bashWriteNeedsApproval(tc.name, tc.input, l.cfg.ApprovalMode, allowRulesOf(l.cfg), l.cfg.Permissions) {
		cmd, _ := tc.input["command"].(string)
		msg := "工具 \"" + tc.name + "\" (" + truncateRunes(cmd, 60) + ") " + approvalBlockedMarker + "。\n"
		msg += "该命令会写入文件系统，需人工批准。\n" +
			"这是**需人工批准的操作**，agent 无法自行授予。\n" +
			"不要重复发出同一调用——它会再次撞上同一道门，白白消耗轮次预算。\n" +
			"请改用其他方式完成任务，或停下来向用户说明哪一步需要授权。"
		return contract.Result{Content: msg, IsError: true}
	}

	// ── TDD gate（第七十二刀接线 / 第七十三刀补 suggest 通道）──
	//
	// 对账 TS `tool-pipeline.ts:889-908`（判定）与 `:1748-1750`（附注）。
	// **Go 侧此前只有状态追踪、没有拦截**：`evidenceTracker` 的 `GateState` /
	// `HasVerificationDebt` 零生产消费（`loop.go` 的字段注释曾显式披露
	// 「无 gate 拦截消费方——拦截是独立的一刀」）。第七十二刀补了拦截，
	// 第七十三刀补了 suggest 附注。
	//
	// **为什么在门链末尾**（bash 写门之后）：TS 的 TDD gate 在
	// `destructiveGate` **之前**、cerebellar gate **之后**；而 Go 门链的
	// 顺序是「用户边界（deny/pathGrant/审批）→ 纪律门」。TDD 是**纪律**门
	// （不是安全边界），放在审批门之后对账这个分层——安全边界优先于纪律。
	//
	// **skip 档不豁免本门**（与审批门不同）：TDD gate 不是审批门——它
	// 不需要人工批准，拦下只是让模型先写测试。TS 侧同样不读审批档位。
	var tddSuggestNote string
	if editTools[tc.name] {
		// **配置来源唯一**：`Config.TddGateEnv`（装配层从 `os.Getenv` 填充）。
		//
		// **不在此处回退读 os.Getenv**（初版曾这么做，是设计错误）：那会让
		// 环境变量**绕过**注入层——e2e 测试设了子进程 env 后，`TddGateEnv`
		// 为空也能生效，于是「装配层漏填」这个缺陷**测不出来**（变异反证
		// 实测红 0 处）。与 `TerseEnv` 的注入模式一致：Config 是唯一来源。
		gateCfg := parseTddGateEnv(l.cfg.TddGateEnv)
		target, _ := tc.input["file_path"].(string)
		filesModified := 0
		var filesRead []string
		if l.State != nil {
			snap := l.State.Snapshot()
			filesModified = EvidenceStateFromSession(snap)
			for _, k := range snap.FileIndex.Keys() {
				if v, ok := snap.FileIndex.Get(k); ok && !v.ModifiedByMe {
					filesRead = append(filesRead, k)
				}
			}
		}
		gateState := l.evidence.GateState(filesModified, filesRead)
		decision := EvaluateTddGate(gateState, tc.name, gateCfg, target)
		if decision.Action == "block" {
			return contract.Result{Content: decision.Message, IsError: true}
		}
		// **suggest 附注的触发区域**（对账 TS `tool-pipeline.ts:900-905`）：
		//
		//	decision.action === 'suggest' && decision.message
		//	  && (gateState.hasFailedTests || gateState.editsSinceLastTest >= threshold)
		//
		// 即**只在「enforce 会拦」的区域才附注**——探索窗口（<threshold）与
		// 测试文件 RED 步骤保持安静，避免每次编辑都贴尾巴（TS 注释原文）。
		//
		// **快照时机**：此处取的是**编辑前**的状态（与 TS 一致——TS 在
		// `:891` 取 `getGateState()`，`trackFileModified` 在工具执行后）。
		if decision.Action == "suggest" && decision.Message != "" &&
			(gateState.HasFailedTests || gateState.EditsSinceLastTest >= gateCfg.Threshold) {
			tddSuggestNote = decision.Message
		}
	}

	result, err := l.registry.Execute(ctx, tc.name, p)
	if err != nil {
		result = contract.Result{
			Content: fmt.Sprintf("工具执行失败：%v", err),
			IsError: true,
		}
	}
	// ── L1 artifact 拦截（大结果落盘，历史只留引用）──
	//
	// 对账 TS tool-pipeline 的 L1 层。**时机**：工具结果出来后、
	// 记录轨迹与观察之前——保证轨迹与后续消费看到的是**最终形态**。
	result = l.interceptResultForArtifact(tc, result)

	// ── 轨迹记录（对账 TS turn-harness.ts:83 的 trajectory.record）──
	//
	// **时机**：结果出来后立即记——包括失败（失败轨迹是 handoff
	// 「错误与修复」章节的唯一来源）。
	//
	// **与 TS 的差异（已知且有意）**：TS 的 status 有 `retried-*` 两态
	// （瞬时失败重试后的结局）。Go 侧的重试在 client 层，尚未把「是否重试过」
	// 透传到此处——故当前只产出 success / failed。移植重试透传后应补齐。
	l.recordTrajectory(tc, result, time.Since(started))

	l.observeToolResult(tc.name, tc.input, result)

	// ── TDD suggest 附注（第七十三刀）──
	//
	// 对账 TS `tool-pipeline.ts:1748-1750`：
	//
	//	if (tddSuggestNote && !harnessResult.isError) {
	//	  finalContent = `${finalContent}\n\n[TDD] ${tddSuggestNote}`
	//	}
	//
	// **时机在内部记录之后**（与 TS 一致）：TS 的 `recordToolHistory` 用的是
	// `harnessResult.content`（**原始**，不带 note），只有 `onToolResult`
	// 收到 `finalContent`。Go 侧对应：`recordTrajectory` / `observeToolResult`
	// 是内部记录（用原始 content），`result.Content` 回灌进消息历史（带 note）。
	//
	// **为什么追加在尾部**（TS 注释原文）：结果尾部 = 对话历史末尾 →
	// **冻结前缀不变**（前缀缓存友好）。
	//
	// **只在工具成功时贴**（TS：`!harnessResult.isError`）：失败的编辑结果
	// 本身已是反馈，再贴 TDD 提示是噪音。
	if tddSuggestNote != "" && !result.IsError {
		result.Content = result.Content + "\n\n[TDD] " + tddSuggestNote
	}
	return result
}

// enterPlanMode 切入计划模式（第七十九刀）。
//
// 对账 TS `loop.ts:2038` 的 `enterPlanMode`：
//
//	① 已在 planning 且有活动计划文件 → 幂等早返回（alreadyPlanning=true）
//	② 否则生成草稿路径（`.rivet/plans/draft-<ms>.md`）并落盘空文件
//	③ 状态置 planning
//
// **Go 侧的收窄（有意）**：TS 还做 Ask Mode 互斥（进入 plan 静默退出 ask）、
// promptEngine 同步、调研 advisory 注入——那些子系统 Go 侧不存在或未接线，
// 故此处只做「状态 + 草稿路径」两件核心事。
func (l *Loop) enterPlanMode() (activePlanFilePath string, alreadyPlanning bool) {
	if l.PlanModeState == PlanModePlanning && l.ActivePlanFilePath != "" {
		return l.ActivePlanFilePath, true
	}
	// 生成草稿路径（对账 TS `createActivePlanDraftPath`）并确保父目录存在。
	rel := CreateActivePlanDraftPath()
	abs := filepath.Join(l.cfg.Cwd, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
		if _, statErr := os.Stat(abs); statErr != nil {
			// 空文件——对账 TS 的 `if (!existsSync(abs)) writeFileSync(abs, '', 'utf-8')`
			_ = os.WriteFile(abs, nil, 0o644)
		}
	}
	l.ActivePlanFilePath = rel
	l.PlanModeState = PlanModePlanning
	return rel, false
}

// exitPlanMode 退出计划模式（第七十九刀）。
//
// 对账 TS `loop.ts` 的 `exitPlanMode`：状态置 off、清活动计划文件。
// **不修改计划文件本身**（对账 TS 注释）。
func (l *Loop) exitPlanMode() {
	l.PlanModeState = PlanModeOff
	l.ActivePlanFilePath = ""
}

// recordTrajectory 把一次工具调用记进轨迹。
//
// 对账 TS 的 `this.trajectory.record({...})`（turn-harness.ts:83）。
// nil 时跳过（增强而非必需）。
func (l *Loop) recordTrajectory(tc toolCall, res contract.Result, dur time.Duration) {
	if l.Trajectory == nil {
		return
	}
	status := compact.TrajectorySuccess
	if res.IsError {
		status = compact.TrajectoryFailed
	}
	inputSummary, _ := json.Marshal(tc.input)
	l.Trajectory.Record(compact.TrajectoryEntry{
		Turn:          l.SessionTurn(),
		Tool:          tc.name,
		Target:        toolTarget(tc.input),
		DurationMs:    int(dur.Milliseconds()),
		Status:        status,
		InputSummary:  truncateRunes(string(inputSummary), 100),
		ResultSummary: truncateRunes(res.Content, 200),
	})
}

// splitState 汇总 session split 构造 handoff 所需的可选状态。
//
// 所有字段可缺省——缺省时 handoff 退化（见 SplitState）。
func (l *Loop) splitState() *SplitState {
	s := &SplitState{Trajectory: l.Trajectory}
	if l.Todos != nil {
		s.Todos = l.Todos()
	}
	if l.StreamedText != nil {
		s.StreamedText = l.StreamedText()
	}
	return s
}

// truncateRunes 按**符文**截断（对账 TS 的 `String.prototype.slice` 对
// BMP 字符的语义——见 compact.utf16Slice 的说明）。
//
// 这里用符文而非 UTF-16 code unit：摘要字段只进轨迹（不直接进 handoff
// 的固定文本），且补充平面字符的 1 码点差异在实践中不可观测。**若未来
// 该字段进前缀，须换成 utf16 语义**。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// observeToolResult 把工具调用的结果记进会话状态。
//
// 对账 TS 侧的证据追踪（trackFileRead / trackFileModified / recordVerification）。
// 只在工具**成功**时记账——失败的工具调用不该污染状态。
//
// **`run_tests` 是例外**（第七十刀修既有缺陷）：它在**测试失败**时返回
// `IsError: exitCode != 0`（`run_tests.go`），故「只记成功」的早退会把
// 「验证发生过且红了」这一**正是要记的事实**挡在门外——
// 原 `run_tests` 分支里的 `if res.IsError { status = "failed" }` 因此
// **永不可达**，`hasFailedTests` 恒 false。
//
// 修法：早退条件排除 `run_tests`——它的失败是**验证结果**，不是「工具坏了」。
// 其余工具（读/写类）的失败仍不记账（`TestObserveToolResultFailedRead…` 钉住）。
func (l *Loop) observeToolResult(name string, input map[string]any, res contract.Result) {
	if l.State == nil {
		return
	}
	if res.IsError && name != "run_tests" {
		return
	}
	switch name {
	case "read_file":
		// **字段名是 file_path**（工具 schema 用的就是它）——曾误用 `path`
		// 导致 read 追踪静默失效。与 hook_snapshot.go 的 toolTarget 同一约定。
		if p, ok := input["file_path"].(string); ok && p != "" {
			l.State.TrackFileRead(p, "")
		}
	case "write_file", "edit_file", "hash_edit":
		if p, ok := input["file_path"].(string); ok && p != "" {
			l.State.TrackFileModified(p)
			// TDD gate 计数（与上一行**并列**——前者记会话索引，后者记
			// 距上次验证的连续代码编辑数；两处事实不同，见 evidenceTracker 注释）
			l.evidence.TrackFileModified(p)
		}
	case "apply_patch":
		// 目标路径从 diff 提取（与工具内部同一函数）
		if d, ok := input["diff"].(string); ok {
			for _, rel := range prompt.ExtractPatchTargetPaths(d) {
				l.State.TrackFileModified(rel)
				l.evidence.TrackFileModified(rel)
			}
		}
	case "run_tests":
		// 测试通过/失败记进 verification（target 用命令或固定标签）
		target, _ := input["filter"].(string)
		if target == "" {
			target = "全部测试"
		}
		// **优先读 `res.Verification.Status`**（工具产出的权威状态，区分
		// passed/failed/blocked）；无该字段时退回 `res.IsError` 推断。
		//
		// 为什么不用 `res.IsError` 单判：它把 **blocked**（无测试框架/运行器
		// 超时）也归为 error，而 blocked 与 failed 语义不同——对账 TS 的
		// `VerificationStatus` 三态。
		status := "passed"
		if res.Verification != nil {
			status = string(res.Verification.Status)
		} else if res.IsError {
			status = "failed"
		}
		l.State.RecordVerification(target, status)
		// TDD gate 计数（与上一行并列——前者记会话验证列表，后者记
		// 「距上次验证的连续编辑数」归零 + 失败粘滞）
		l.evidence.TrackVerification(status)
	}
}

// toolDefs 返回工具声明（转为 wire 有序结构，保字节稳定）。
func (l *Loop) toolDefs() []*wire.OrderedMap {
	// 工具集在 Loop 生命周期内不变 → 构造一次后复用。
	// 缓存命中时直接返回（避免每轮重建 schema + 重新序列化）。
	if l.toolDefsCache != nil {
		return l.toolDefsCache
	}
	out := l.buildToolDefs()
	l.toolDefsCache = out
	return out
}

// buildToolDefs 实际构造工具定义（缓存未命中时调用一次）。
func (l *Loop) buildToolDefs() []*wire.OrderedMap {
	defs := l.registry.Definitions()
	out := make([]*wire.OrderedMap, 0, len(defs))
	for _, d := range defs {
		fn := wire.NewOrderedMap().
			Set("name", d.Name).
			Set("description", d.Description)
		if d.InputSchema != nil {
			params := wire.NewOrderedMap().
				Set("type", d.InputSchema.Type)
			if d.InputSchema.Properties != nil {
				params.Set("properties", tools.OrderedProps(d.InputSchema.Properties, d.InputSchema.PropOrder))
			}
			if len(d.InputSchema.Required) > 0 {
				req := make([]any, len(d.InputSchema.Required))
				for i, r := range d.InputSchema.Required {
					req[i] = r
				}
				params.Set("required", req)
			}
			fn.Set("parameters", params)
		}
		out = append(out, wire.NewOrderedMap().
			Set("type", "function").
			Set("function", fn))
	}
	return out
}

// orderedProps 把 schema properties 转为有序结构（保字节稳定）。
//
// 注意：properties 的键序按字母升序固定——schema 由我们生成，
// 只要每次生成顺序一致即可保证请求体稳定。

// buildAssistantMessage 从本回合的收集结果构造 assistant 消息。
//
// 关键：若有工具调用，必须带上 tool_calls（否则历史不完整，
// 下一轮的 tool 结果消息会失去配对）。
func (l *Loop) buildAssistantMessage(c *turnCollector) *wire.OrderedMap {
	msg := wire.NewOrderedMap().Set("role", "assistant")

	text := c.text()
	if len(c.toolCalls) == 0 {
		msg.Set("content", text)
		return msg
	}

	// 有工具调用：content 可为空串（DeepSeek 要求 content 或 tool_calls 至少一个）
	msg.Set("content", text)
	calls := make([]any, 0, len(c.toolCalls))
	for _, tc := range c.toolCalls {
		args, _ := marshalArgs(tc.input)
		calls = append(calls, wire.NewOrderedMap().
			Set("id", tc.id).
			Set("type", "function").
			Set("function", wire.NewOrderedMap().
				Set("name", tc.name).
				Set("arguments", args)))
	}
	msg.Set("tool_calls", calls)
	return msg
}

// marshalArgs 序列化工具入参（工具调用的 arguments 是 JSON 字符串字段）。
func marshalArgs(input map[string]any) (string, error) {
	om := wire.NewOrderedMap()
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		om.Set(k, input[k])
	}
	return om.Marshal(), nil
}

func (l *Loop) emit(e Event) {
	if l.Emit != nil {
		l.Emit(e)
	}
}

// ── 回合收集器 ──

type toolCall struct {
	id    string
	name  string
	input map[string]any
	// truncated 标记参数在流中被截断——该调用**不得执行**
	//（半个参数的命令比失败更危险：session 4df36bcd 把截断的 bash 当 {} 执行了）。
	truncated bool
}

type turnCollector struct {
	textBuf    strings.Builder
	thinkBuf   strings.Builder
	toolCalls  []toolCall
	stopSeen   bool
	stopReason string
	// usage 是本回合的 token 计量。**必须保留**——cache_read_input_tokens
	// 是前缀缓存是否命中的唯一直接指标，丢掉它等于放弃缓存可观测性。
	usage contract.Usage
}

func (c *turnCollector) text() string { return c.textBuf.String() }

// thinkingLen 返回本轮思考内容的长度。
//
// **为什么按 rune 计**：TS 的 `lastThinkingContent.length` 是 **UTF-16 code
// unit** 计数。Go 的 `len(s)` 是字节数——中文思考内容会 3 倍膨胀，阈值
// 3000 的语义完全不同。用 rune 计数在 BMP 内与 UTF-16 一致（中文/ASCII
// 均落在此区间），仅 BMP 外字符（emoji）有 1 vs 2 的差异——阈值是启发式
// 而非精确契约，该差异可接受（已在注释明示）。
func (c *turnCollector) thinkingLen() int {
	return utf8.RuneCountInString(c.thinkBuf.String())
}

// handler 构造 SSE 处理器，把流事件转成 agent 事件并收集工具调用。
func (c *turnCollector) handler(l *Loop, turn int) sse.Handler {
	return sse.Handler{
		OnTextDelta: func(s string) {
			c.textBuf.WriteString(s)
			l.emit(Event{Kind: "text", Text: s, Turn: turn})
		},
		OnThinkingDelta: func(s string) {
			c.thinkBuf.WriteString(s)
			l.emit(Event{Kind: "thinking", Text: s, Turn: turn})
		},
		OnContentBlock: func(e sse.Event) {
			if e.BlockType != "tool_use" {
				return
			}
			c.toolCalls = append(c.toolCalls, toolCall{
				id:        e.ToolID,
				name:      e.ToolName,
				input:     e.ToolInput,
				truncated: e.ArgsTruncated,
			})
		},
		OnStopReason: func(reason string, u contract.Usage) {
			c.stopSeen = true
			c.stopReason = reason
			c.usage = u
		},
	}
}

func intPtr(i int) *int { return &i }

var _ = errors.New

// maybeCompactAtBoundary 在 turn 边界运行压缩。
//
// 对账 TS `CompactBoundaryCoordinator.runCompaction` 的**确定性路径部分**。
//
// **转换往返**：loop 用 wire 形态存历史，compact 包吃结构化形态——这里做
// 一次往返。**只有真发生压缩才回写**（`changed == true`），避免无谓的对象
// 重建（每次重建都会让后续的键序断言失效风险上升）。
//
// **未接**（见 PLAN.md 架构欠账）：LLM 重写路径（partial-llm / full-llm /
// checkpoint）——需 summaryClient 抽象（真调模型做摘要）。
//
// **session split**：判定层已接（见下），但**执行层未移植**——判定通过时
// 无法替换历史，故只记录判定与候选 handoff，不谎称已完成切分。
func (l *Loop) maybeCompactAtBoundary(turn int) {
	if l.Compact == nil {
		return
	}
	oai := orderedMapsToOai(l.messages)

	// ── session split：**先于**常规压缩 ──
	//
	// 对账 TS `runCompaction`（compact-boundary-coordinator.ts:124）的调用序：
	// `if (await trySessionSplit()) userMessageConsumed = true` → 再 maybeCompact。
	// split 是更激进的处置（整段历史换 handoff），若先走常规压缩，split 的
	// 判定依据（历史占用）已被压缩改动。
	//
	// **执行层未移植**：TS 的 `trySessionSplit` 内部调 `replaceWithCheckpoint`
	// 真正替换历史（依赖 task-state / trajectory / artifact store——Go 侧全无）。
	// 故此处**只判定 + 记录**，不改 `l.messages`——不谎称已完成切分。
	// 候选 handoff 已构造（`outcome.Handoff`）。
	//
	// **执行层已接**：判定通过后用 `ReplaceWithCheckpoint` **真的替换历史**
	// （anchor 保留 + 尾随 user 保护 + reclaim gate）。
	//
	// state 传入真实轨迹 / todo / 流式文本——handoff 由此产出完整
	// 9 章节（而非降级版的推理+文件清单）。全部可缺省（nil 时退化）。
	if split := l.Compact.TrySessionSplit(oai, l.splitState()); split.SplitTriggered() {
		// **force=true**：split 的替代方案是超窗 API 失败（对账 TS 的
		// force 语义——ceiling / session split 的 substitute 是灾难）。
		outcome := l.Compact.ReplaceWithCheckpoint(oai, CheckpointParams{
			Tier:         4, // session split 属最高层（对账 TS 的 ceiling tier）
			Reason:       fmt.Sprintf("session split at %.0f%% context", split.Decision.Ratio*100),
			Summary:      split.Handoff,
			FallbackText: split.Handoff,
			Action:       compact.ActionSessionSplit,
			Force:        true,
		}, l.CheckpointDeps)

		if outcome.Committed {
			l.replaceHistory(outcome.Messages)
			l.emit(Event{
				Kind: "compaction",
				Turn: turn,
				Text: fmt.Sprintf("会话切分执行（占用 %.0f%%，窗口 %d）——"+
					"历史替换为 %d 条（回收 %d token）",
					split.Decision.Ratio*100, l.Compact.ContextWindow,
					len(outcome.Messages), outcome.ReclaimedTokens),
			})
			// **已替换历史 → 不再走常规压缩**（历史已是新形态）。
			return
		}

		// 未提交（gate 拒绝）→ 如实记录，继续走常规压缩。
		l.emit(Event{
			Kind: "compaction",
			Turn: turn,
			Text: fmt.Sprintf("会话切分被 reclaim gate 拒绝（%s）——未替换历史",
				outcome.Reason),
		})
	}

	compacted, changed := l.Compact.MaybeCompact(oai, turn)
	if !changed {
		return
	}
	l.messages = oaiToOrderedMaps(compacted)
	l.emit(Event{
		Kind: "compaction",
		Turn: turn,
		Text: fmt.Sprintf("压缩：回收 %d 条消息（窗口 %d）",
			l.Compact.LastReclaimed, l.Compact.ContextWindow),
	})
}

// replaceHistory 用替换后的历史重写内存消息并镜像到持久化。
//
// 对账 TS `safeReplaceMessages`：`session.replaceMessages` → mutation
// listener 的 `replace` 分支 → `compactOai` 全量原子重写。
//
// **preflight 已由 ReplaceWithCheckpoint 的 deps 处理**（若注入）——此处
// 只做替换与持久化。
func (l *Loop) replaceHistory(messages []session.OaiMessage) {
	l.messages = oaiToOrderedMaps(messages)
	if l.Listener != nil {
		l.Listener.OnReplace(messages)
	}
}
