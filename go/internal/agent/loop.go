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
	"errors"
	"fmt"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/api/sse"
	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/client"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/session"
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
	// SessionID 用于缓存路由亲和。
	SessionID string
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
	// State 是会话状态容器（跨轮的文件/验证/决策感知）。
	//
	// 对账 TS 侧 loop.ts:848 的 `new SessionStateManager(this.config.sessionId)`。
	// nil 时跳过状态更新（最小可跑路径）。
	State *session.Manager
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
	if cfg.SessionID != "" {
		l.State = session.New(cfg.SessionID)
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

// FlushSession 排空会话落盘缓冲（会话结束时调用，确保不留未写尾部）。
func (l *Loop) FlushSession() {
	if l.Listener != nil {
		_ = l.Listener.Drain()
	}
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

// Run 执行一轮用户交互，直到模型给出终答或触及预算。
func (l *Loop) Run(ctx context.Context, userMessage string) error {
	l.appendAndPersist(wire.NewOrderedMap().
		Set("role", "user").
		Set("content", userMessage))

	maxTurns := l.cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50
	}

	for turn := 0; turn < maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		// ── preTurn hook ──
		//
		// 在调模型**之前**——hook 可注入消息 / 调整感知。
		l.runHookPhase(ctx, PhasePreTurn, turn, nil)

		// ── 调模型（流式）──
		collector := &turnCollector{}
		req := &api.ChatRequest{
			Model:     l.cfg.Model,
			Messages:  l.messages,
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
		for _, tc := range collector.toolCalls {
			l.emit(Event{
				Kind: "tool_start", ToolName: tc.name, ToolInput: tc.input,
				ToolID: tc.id, Turn: turn,
			})

			result := l.executeTool(ctx, tc)
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
			l.runHookPhase(ctx, PhasePostTool, turn, toolEvent)

			l.appendAndPersist(wire.NewOrderedMap().
				Set("role", "tool").
				Set("tool_call_id", tc.id).
				Set("content", result.Content))
		}

		u := collector.usage
		l.emit(Event{
			Kind: "turn_end", Turn: turn,
			Usage: &u, StopReason: collector.stopReason,
		})

		// ── postTurn hook ──
		//
		// 轮末——hook 在此做跨轮判断（如"改了 TS 但没 typecheck"）。
		l.runHookPhase(ctx, PhasePostTurn, turn, nil)
	}

	return fmt.Errorf("已达最大轮数 %d——任务未完成（防无限循环）", maxTurns)
}

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

	p := &tools.CallParams{
		Input:        tc.input,
		ToolUseID:    tc.id,
		Cwd:          l.cfg.Cwd,
		ApprovalMode: l.cfg.ApprovalMode,
		SessionID:    l.cfg.SessionID,
	}
	if l.ToolParams != nil {
		// 继承注入依赖（OnFileWrite 等）
		p.OnFileWrite = l.ToolParams.OnFileWrite
		p.OnOutput = l.ToolParams.OnOutput
	}

	result, err := l.registry.Execute(ctx, tc.name, p)
	if err != nil {
		return contract.Result{
			Content: fmt.Sprintf("工具执行失败：%v", err),
			IsError: true,
		}
	}
	l.observeToolResult(tc.name, tc.input, result)
	return result
}

// observeToolResult 把工具调用的结果记进会话状态。
//
// 对账 TS 侧的证据追踪（trackFileRead / trackFileModified / recordVerification）。
// 只在工具**成功**时记账——失败的工具调用不该污染状态。
func (l *Loop) observeToolResult(name string, input map[string]any, res contract.Result) {
	if l.State == nil || res.IsError {
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
		}
	case "apply_patch":
		// 目标路径从 diff 提取（与工具内部同一函数）
		if d, ok := input["diff"].(string); ok {
			for _, rel := range prompt.ExtractPatchTargetPaths(d) {
				l.State.TrackFileModified(rel)
			}
		}
	case "run_tests":
		// 测试通过/失败记进 verification（target 用命令或固定标签）
		target, _ := input["filter"].(string)
		if target == "" {
			target = "全部测试"
		}
		status := "passed"
		if res.IsError {
			status = "failed"
		}
		l.State.RecordVerification(target, status)
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
