package session

import (
	"encoding/json"
	"fmt"
	"github.com/kalandramo/tianshu/go/internal/prompt"
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

// OnReplace 处理一次全量替换（压缩 / 会话切分 / 重置）。
//
// 对账 TS `compactOai`（`session-persist.ts:424`）：**全量原子重写**文件，
// 而不是追加——历史已被替换，旧行必须消失。
//
// # 三步（对账 TS 的 compactOai）
//
//  1. **先 flush** 排空待写缓冲——否则缓冲里的旧行会在重写后又被写出
//  2. **保留审计行**（`collectAuditLines`）：重写只从内存消息重建文件，
//     而审计行（compact_start / compact_end / model_switch）**从不进内存**
//     （`parseSessionLine` 在回放时跳过它们）。不保留的话，第一次重写就会
//     **静默销毁审计轨迹**（TS 注释记录的回归）。
//  3. **原子写**（tmp + rename）——中途崩溃不会留下半个文件
//
// **本实现替代了此前的显式未实现占位**（原先只 report 一个错误，
// 让调用方以为替换没生效）。
func (l *PersistListener) OnReplace(messages []OaiMessage) {
	if err := l.replaceAll(messages); err != nil {
		l.report(fmt.Errorf("会话全量重写失败：%w", err))
	}
}

// replaceAll 执行全量原子重写（对账 TS compactOai 的三步）。
func (l *PersistListener) replaceAll(messages []OaiMessage) error {
	// 1) flush 排空缓冲——TS 的 `batchWriter.flushSync()`。
	if err := l.persist.Flush(); err != nil {
		return err
	}

	// 2) 收集审计行（从磁盘上的既有内容里挑出审计类型）。
	audit := l.persist.collectAuditLines()

	// 3) 组装：审计行 + 全部消息（各带校验和），原子写。
	lines := make([]string, 0, len(audit)+len(messages))
	lines = append(lines, audit...)
	for _, m := range messages {
		lines = append(lines, prompt.AppendChecksum(
			SerializeOaiSessionMessage(m, MaxSessionMessageJSONChars)))
	}
	content := strings.Join(lines, "\n") + "\n"
	return l.persist.rewriteTranscript(content)
}

// collectAuditLines 从既有文件里挑出审计行（重新加校验和）。
//
// 对账 TS `collectAuditLines`。**为什么必须保留**：重写只从内存消息重建文件，
// 而审计行从不进内存（回放时被跳过）——不保留就会在第一次重写时静默销毁
// 审计轨迹。
//
// 无法读取时返回空切片（不阻塞重写）。
func (p *Persist) collectAuditLines() []string {
	content := p.ReadTranscriptText()
	if content == "" {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 审计行是带 type 字段的裸 JSON（非 OAI 消息形态）。
		// 直接解析原始行——校验和在重写时重新生成。
		raw, ok := stripChecksum(line)
		if !ok {
			continue // 校验和不符——损坏行，跳过
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			continue
		}
		if auditLineTypes[probe.Type] {
			out = append(out, prompt.AppendChecksum(raw))
		}
	}
	return out
}

// auditLineTypes 对账 TS 的 `AUDIT_LINE_TYPES`。
//
// **必须逐字相同**——这些类型名是文件格式的一部分。
var auditLineTypes = map[string]bool{
	"compact_start": true,
	"compact_end":   true,
	"model_switch":  true,
}

// stripChecksum 去掉行尾的校验和后缀，返回裸 JSON。
//
// 复用 `prompt.VerifyAndExtract`（对账 TS 的 `verifyAndExtract`）——
// 它已处理所有边界：无 `|`、`|` 后非校验和格式、`|` 是内容一部分。
// 校验失败（Valid=false）时返回错误——审计行不该损坏。
func stripChecksum(line string) (string, bool) {
	r := prompt.VerifyAndExtract(line)
	if r.Valid {
		return r.JSON, true
	}
	return "", false
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
