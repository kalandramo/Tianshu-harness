package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// Persist 是会话持久化器（编排层）。
//
// 对账 src/agent/session-persist.ts 的 `SessionPersist` 类（928 行）——但**只
// 移植编排核心**：把已验证的组件串起来（读文件 → checksum 校验 → 逐行解析 →
// 三层链路 → BatchWriter / MetadataStore）。
//
// **不移植的部分**（见 HANDOFF）：
//   - `compact` 系列（压缩重写，依赖 boundary coordinator）
//   - `evictOldSessions` / `deleteSessionFiles`（清理策略）
//   - 会话记忆（`appendSessionMemory` / `loadSessionMemory`，依赖 context 层）
//   - frozen 快照（`<id>.frozen.json`）
type Persist struct {
	sessionID string
	cwd       string
	filePath  string
	metaPath  string

	writer   *BatchWriter
	metadata *MetadataStore
}

// NewPersist 构造会话持久化器。
//
// 目录布局对账 TS 的 `getSessionDir(cwd)`：`<cwd>/.rivet/sessions/`。
func NewPersist(sessionID, cwd string) (*Persist, error) {
	dir := SessionDir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	filePath := filepath.Join(dir, sessionID+".jsonl")
	metaPath := filepath.Join(dir, sessionID+".meta.json")

	tr, err := NewTranscript()
	if err != nil {
		return nil, err
	}
	w, err := NewBatchWriter(filePath, func() string {
		backup := filepath.Join(dir, sessionID, "backups")
		_ = os.MkdirAll(backup, 0o755)
		return backup
	}, tr)
	if err != nil {
		return nil, err
	}

	return &Persist{
		sessionID: sessionID,
		cwd:       cwd,
		filePath:  filePath,
		metaPath:  metaPath,
		writer:    w,
		metadata:  NewMetadataStore(metaPath),
	}, nil
}

// SessionDir 返回会话目录（对账 TS 的 getSessionDir）。
func SessionDir(cwd string) string {
	return filepath.Join(cwd, ".rivet", "sessions")
}

// FilePath 返回会话文件路径。
func (p *Persist) FilePath() string { return p.filePath }

// Metadata 返回元数据存储（供调用方读写）。
func (p *Persist) Metadata() *MetadataStore { return p.metadata }

// Close 释放资源。
func (p *Persist) Close() { p.writer.Close() }

// AppendOai 追加一条 OAI 消息（带校验和），排入批量写。
//
// 对账 appendOaiWithChecksum。`flush: true` 时立即落盘——用于持久性关键的
// 记录（assistant 的 tool_calls / tool 结果 / user 回合），让硬崩溃不会丢掉
// 已完成的工具结果。
func (p *Persist) AppendOai(message OaiMessage, flush bool) error {
	jsonStr := SerializeOaiSessionMessage(message, MaxSessionMessageJSONChars)
	line := prompt.AppendChecksum(jsonStr) + "\n"
	p.writer.EnqueueLine(line)
	if flush {
		return p.writer.Flush()
	}
	return nil
}

// AppendRaw 追加一行已序列化的 JSON（带校验和）。
//
// 对账 append（传统 Message 路径）：序列化 + 加校验和 + 入队。
func (p *Persist) AppendRaw(jsonStr string) error {
	p.writer.EnqueueLine(prompt.AppendChecksum(jsonStr) + "\n")
	return nil
}

// Flush 排空批量缓冲（含元数据）。
//
// 对账 flushSessionBuffer：transcript 与元数据共享同一节拍。
func (p *Persist) Flush() error {
	if err := p.writer.Flush(); err != nil {
		return err
	}
	return p.metadata.Flush()
}

// ReadTranscriptText 读会话文件并解码为 JSONL 文本（含未 flush 的 pending）。
//
// 对账 readTranscriptText：进程内读者必须看到仍在写缓冲里的行
// （append 后立刻 load 是合法用法）。
func (p *Persist) ReadTranscriptText() string {
	onDisk := ""
	if raw, err := os.ReadFile(p.filePath); err == nil {
		if text, err := p.writer.transcript.DecodeTranscriptText(raw); err == nil {
			onDisk = text
		}
	}
	return p.writer.MergePending(onDisk)
}

// LoadOai 读回全部 OAI 消息（含 checksum 校验 + 孤儿修复）。
//
// 对账 loadOai。完整链路：
//  1. 读 transcript 文本（zstd 解码 + pending 合并）
//  2. 按行切分 → VerifyLines 校验和过滤
//  3. 逐行 parseSessionLine（跳过审计行）→ isOaiMessage 判定
//     → 非 OAI 行走 legacyMessageToOaiMessages 迁移
//  4. NormalizeOaiMessages（移除空 tool_calls 数组）
//  5. RepairOrphanToolCalls（孤儿 tool_call/result 清理）
//  6. 若有孤儿 → 首位插入 system-reminder
func (p *Persist) LoadOai() []OaiMessage {
	content := p.ReadTranscriptText()
	rawLines := strings.Split(strings.TrimSpace(content), "\n")
	var lines []string
	for _, l := range rawLines {
		if l != "" {
			lines = append(lines, l)
		}
	}
	valid := prompt.VerifyLines(lines)

	var messages []OaiMessage
	for _, line := range valid.ValidLines {
		parsed, skip := ParseSessionLine(line)
		if skip {
			continue
		}
		if IsOaiMessage(parsed) {
			messages = append(messages, mapToOai(parsed))
		} else {
			// 传统 Message → OAI 迁移
			for _, m := range LegacyMessageToOaiMessages(parsed) {
				messages = append(messages, m)
			}
		}
	}

	normalized := NormalizeOaiMessages(messages)
	rep := RepairOrphanToolCalls(normalized)
	if !rep.HadOrphans {
		return rep.Messages
	}
	reminder := OrphanReminderGeneric
	if rep.StrippedWriteTool {
		reminder = OrphanReminderWriteTool
	}
	out := make([]OaiMessage, 0, len(rep.Messages)+1)
	out = append(out, OaiMessage{Role: "system", Content: &reminder})
	out = append(out, rep.Messages...)
	return out
}

// ParseSessionLine 解析一行 JSON，返回 (值, 是否跳过)。
//
// 对账 parseSessionLine：审计行（compact_start / compact_end / model_switch）
// 跳过——它们是审计面包屑，不是对话历史。
func ParseSessionLine(line string) (map[string]any, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		return nil, true // 损坏行跳过
	}
	if t, ok := parsed["type"].(string); ok {
		switch t {
		case "compact_start", "compact_end", "model_switch":
			return nil, true
		}
	}
	return parsed, false
}

// mapToOai 把解析后的 map 转成 OaiMessage（保留键序）。
func mapToOai(m map[string]any) OaiMessage {
	out := OaiMessage{}
	out.Role, _ = m["role"].(string)
	if c, ok := m["content"].(string); ok {
		out.Content = &c
	}
	out.ToolCallID, _ = m["tool_call_id"].(string)
	if tcs, ok := m["tool_calls"].([]any); ok {
		out.ToolCalls = []OaiToolCall{}
		for _, t := range tcs {
			tcm, ok := t.(map[string]any)
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
	// 记录键序（对账 JSON.parse 的键序 → 序列化时保持）
	for k := range m {
		out.KeyOrder = append(out.KeyOrder, k)
	}
	// Go 的 map 遍历无序——按 TS 的常见序重排（role 通常最先）
	out.KeyOrder = orderKeys(out.KeyOrder)
	return out
}

// orderKeys 把键序规范化（role 优先，其余保持相对稳定）。
//
// **已知偏差**：Go 的 map 遍历无序，无法完全复现 TS 的 JSON 键序。
// 生产路径里 `role` 几乎总是第一个键，其余键的标准序见下。
// 见 HANDOFF 的欠账。
func orderKeys(keys []string) []string {
	preferred := []string{"role", "content", "tool_calls", "tool_call_id", "reasoning_content"}
	seen := map[string]bool{}
	var out []string
	for _, p := range preferred {
		for _, k := range keys {
			if k == p {
				out = append(out, k)
				seen[k] = true
			}
		}
	}
	for _, k := range keys {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}
