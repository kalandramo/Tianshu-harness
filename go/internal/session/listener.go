package session

import (
	"fmt"
	"os"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// systemReminderPrefix 是 TTSR 注入的护栏提醒前缀。
//
// 这类 role:user 消息**不是真实用户回合**（历史回放也排除它们），
// 故不计 title、不计 turnCount。
const systemReminderPrefix = "<system-reminder>"

// 会话消息的落盘 flush 策略（对账 session-persist-listener.ts）。
//
// 崩溃恢复的关键：把「硬杀损失窗口」压到**在途记录**。工具调用、工具结果、
// 用户回合立即落盘；流式 assistant 增量走批量节拍（~200ms）。
//
// 语义：user / tool 无条件 flush；assistant **仅当带 tool_calls 时** flush
// （纯文本回复不 flush——它是流的末尾，下一轮会触发）。
const (
	flushAlways = true
	flushBatch  = false
)

// PersistListener 把内存消息变更镜像到持久化存储。
//
// 对账 `attachSessionPersistListener`。设计要点：
//   - **单写链**：所有 append 串行化，保证文件顺序稳定（连续 tool_result
//     快速触发时尤其重要）
//   - **失败不崩主循环**：落盘失败只报 stderr，内存态仍是权威
//   - **元数据同步更新**：每次 append 后更新 title / turnCount /
//     toolCallCount / tokenUsage
//
// 与 TS 的差异：TS 用 Promise 链做串行化；Go 侧调用方在单 goroutine 内
// 驱动（主循环），故不需要额外的链——但仍保留 `drain` 语义用于收尾。
type PersistListener struct {
	persist *Persist
	state   *Manager
	// onError 是落盘失败的回调（nil 时写 stderr）。
	onError func(error)
}

// NewPersistListener 构造监听器。
//
// state 可为 nil（无状态容器时只落盘、不更新元数据）。
func NewPersistListener(p *Persist, state *Manager) *PersistListener {
	return &PersistListener{persist: p, state: state}
}

// SetErrorHandler 注入错误处理器（测试用）。
func (l *PersistListener) SetErrorHandler(fn func(error)) { l.onError = fn }

func (l *PersistListener) report(err error) {
	if l.onError != nil {
		l.onError(err)
		return
	}
	fmt.Fprintf(os.Stderr, "[session-persist] append failed: %v\n", err)
}

// shouldFlushNow 判定该消息是否应立即落盘。
//
// 对账 TS：
//
//	msg.role === 'user' || msg.role === 'tool' ||
//	  (msg.role === 'assistant' && tool_calls?.length > 0)
func shouldFlushNow(m OaiMessage) bool {
	if m.Role == "user" || m.Role == "tool" {
		return flushAlways
	}
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		return flushAlways
	}
	return flushBatch
}

// OnAppend 处理一次消息追加（落盘 + 元数据）。
//
// 对账 mutation listener 的 `append` 分支。
func (l *PersistListener) OnAppend(m OaiMessage) {
	if err := l.persist.AppendOai(m, shouldFlushNow(m)); err != nil {
		l.report(err)
	}
	l.updateMetadata(m)
}

// updateMetadata 更新会话元数据（对账 TS 的 append 分支元数据段）。
//
// 语义要点：
//   - `<system-reminder>` 开头的 user 消息**不算真实用户回合**（TTSR 注入的
//     护栏提醒，历史回放也排除它们）——不计 title、不计 turnCount
//   - title 只在**尚无 title** 时设置，取前 120 字符
//   - toolCallCount 累加 tool_calls 数量
//   - tokenUsage 用 `TotalUsage` 的累计值（**不是**本轮的）
func (l *PersistListener) updateMetadata(m OaiMessage) {
	if l.state == nil {
		return
	}
	snapshot := l.persist.Metadata().Load()

	patch := &SessionMetadata{}

	if m.Role == "user" {
		isReminder := m.Content != nil && strings.HasPrefix(*m.Content, systemReminderPrefix)
		if !isReminder {
			if m.Content != nil && (snapshot == nil || snapshot.Title == "") {
				patch.Title = sliceRunes(*m.Content, 120)
				patch.PresentKeys = append(patch.PresentKeys, "title")
			}
			prev := 0
			if snapshot != nil {
				prev = snapshot.TurnCount
			}
			patch.TurnCount = prev + 1
			patch.PresentKeys = append(patch.PresentKeys, "turnCount")
		}
	}

	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		prev := 0
		if snapshot != nil {
			prev = snapshot.ToolCallCount
		}
		patch.ToolCallCount = prev + len(m.ToolCalls)
		patch.PresentKeys = append(patch.PresentKeys, "toolCallCount")
	}

	u := l.state.TotalUsage()
	patch.TokenUsage = &TokenUsage{
		Prompt:     u.InputTokens,
		Completion: u.OutputTokens,
		Total:      u.InputTokens + u.OutputTokens,
	}
	patch.PresentKeys = append(patch.PresentKeys, "tokenUsage")

	if len(patch.PresentKeys) > 0 {
		l.persist.Metadata().Update(patch)
	}
}

// OnReplace 处理一次全量替换（压缩 / 重置）。
//
// 对账 mutation listener 的 `replace` 分支（TS 走 `compactOaiAsync`）。
// **本移植未实现压缩重写**——见 HANDOFF 欠账。当前仅记录意图，避免
// 调用方以为替换已生效。
func (l *PersistListener) OnReplace(messages []OaiMessage) {
	l.report(fmt.Errorf("OnReplace 未实现（压缩重写属未移植范围，%d 条消息被忽略）", len(messages)))
}

// Drain 排空待写缓冲（会话结束 / 切换目录 / 中止路径调用）。
//
// 对账 TS 的 `drain`：await 写链 + flush 批量缓冲，确保不留未写尾部。
func (l *PersistListener) Drain() error {
	return l.persist.Flush()
}

// sliceRunes 取字符串前 n 个 rune（对账 JS 的 `slice(0, 120)`）。
//
// **注意**：JS 的 `String.prototype.slice` 按 **UTF-16 code unit** 计数，
// 不是 rune。对含代理对的字符串（emoji）两者会分歧——此处的 rune 语义
// 是**已知偏差**，与 `truncateString` 的处理保持一致（见 serialize.go）。
func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// OaiMessageFromWire 把线上格式的 OrderedMap 转成落盘用的 OaiMessage。
//
// 桥接层：主循环用 wire.OrderedMap 传线上格式，落盘用结构化 OaiMessage。
// 键序从 OrderedMap 原样搬运（保字节稳定）。
func OaiMessageFromWire(m *wire.OrderedMap) OaiMessage {
	out := OaiMessage{KeyOrder: m.Keys()}

	if v, ok := m.Get("role"); ok {
		out.Role, _ = v.(string)
	}
	if v, ok := m.Get("content"); ok {
		switch c := v.(type) {
		case string:
			cc := c
			out.Content = &cc
		case nil:
			// 显式 null——Content 保持 nil（与缺失同形，序列化时按 KeyOrder 区分）
		}
	}
	if v, ok := m.Get("tool_call_id"); ok {
		out.ToolCallID, _ = v.(string)
	}
	if v, ok := m.Get("tool_calls"); ok {
		if arr, ok := v.([]any); ok {
			out.ToolCalls = []OaiToolCall{}
			for _, item := range arr {
				tcm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				id, _ := tcm["id"].(string)
				typ, _ := tcm["type"].(string)
				tc := OaiToolCall{ID: id, Type: typ}
				if fn, ok := tcm["function"].(map[string]any); ok {
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					tc.Function = &OaiFunction{Name: name, Arguments: args}
				}
				out.ToolCalls = append(out.ToolCalls, tc)
			}
		}
	}
	return out
}
