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
}

// New 创建 agent loop。
func New(cfg Config, cl *client.Client, reg *tools.Registry) *Loop {
	l := &Loop{cfg: cfg, client: cl, registry: reg}
	if cfg.SystemPrompt != "" {
		l.messages = append(l.messages, wire.NewOrderedMap().
			Set("role", "system").
			Set("content", cfg.SystemPrompt))
	}
	return l
}

// Messages 返回当前会话历史（只读副本的浅拷贝）。
func (l *Loop) Messages() []*wire.OrderedMap {
	out := make([]*wire.OrderedMap, len(l.messages))
	copy(out, l.messages)
	return out
}

// Run 执行一轮用户交互，直到模型给出终答或触及预算。
func (l *Loop) Run(ctx context.Context, userMessage string) error {
	l.messages = append(l.messages, wire.NewOrderedMap().
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

		// ── 把 assistant 回合追加到历史 ──
		assistantMsg := l.buildAssistantMessage(collector)
		l.messages = append(l.messages, assistantMsg)

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

			l.messages = append(l.messages, wire.NewOrderedMap().
				Set("role", "tool").
				Set("tool_call_id", tc.id).
				Set("content", result.Content))
		}

		u := collector.usage
		l.emit(Event{
			Kind: "turn_end", Turn: turn,
			Usage: &u, StopReason: collector.stopReason,
		})
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
	return result
}

// toolDefs 返回工具声明（转为 wire 有序结构，保字节稳定）。
func (l *Loop) toolDefs() []*wire.OrderedMap {
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
				params.Set("properties", orderedProps(d.InputSchema.Properties))
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
func orderedProps(props map[string]any) *wire.OrderedMap {
	om := wire.NewOrderedMap()
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	// 插入排序（键数极少）
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		om.Set(k, orderValue(props[k]))
	}
	return om
}

// orderValue 递归把 schema 值转为有序结构。
//
// 必须处理 **[]any 内的 map**——数组型 schema（如 todo 的 todos）
// 的 items 是嵌套 object，不递归会让 wire.writeValue 遇到裸 map 而排序键，
// 破坏 schema 的字节稳定性；更糟的是遇到非 map 类型（如 *InputSchema）直接 panic。
func orderValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return orderedProps(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = orderValue(e)
		}
		return out
	default:
		return v
	}
}

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
