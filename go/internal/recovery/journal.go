package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecoveryEntry 是一条恢复事件记录。
//
// 对账 src/agent/recovery-journal.ts 的 RecoveryEntry。
type RecoveryEntry struct {
	File      string `json:"file"`
	Action    string `json:"action"`
	TS        string `json:"ts"`
	LinesLost int    `json:"linesLost"`
	// SessionID 是**实际执行恢复的会话**。绝不能用它推断会话存活状态。
	SessionID string `json:"sessionId,omitempty"`
	// Acknowledged 为 true 表示 deliver_task 已展示过该警告，不再重复。
	Acknowledged bool `json:"acknowledged,omitempty"`
	// HandedOff 表示交接已把上下文传递给后继会话。
	HandedOff bool `json:"handedOff,omitempty"`
}

// journalPath 返回 journal 文件路径。
func journalPath(cwd string) string {
	return filepath.Join(cwd, ".rivet", "recovery-journal.jsonl")
}

// journalJSON 是 journal 行的**键序精确**序列化。
//
// 为什么手写：Go 的 struct tag 序列化按**字段声明序**输出，且 `omitempty`
// 只能省略零值——但 TS 的键序由对象展开顺序决定：
//
//	{ ...entry, ts, ...(sessionId ? { sessionId } : {}) }
//
// → `file, action, linesLost, ts[, sessionId]`（探针实测，见 testdata）。
// 注意 `linesLost` 在 `ts` **前**（entry 展开时已带它），而 sessionId 最后。
func journalJSON(e RecoveryEntry) string {
	var b strings.Builder
	b.WriteString(`{"file":`)
	b.WriteString(jsonString(e.File))
	b.WriteString(`,"action":`)
	b.WriteString(jsonString(e.Action))
	b.WriteString(`,"linesLost":`)
	b.WriteString(fmt.Sprintf("%d", e.LinesLost))
	b.WriteString(`,"ts":`)
	b.WriteString(jsonString(e.TS))
	if e.SessionID != "" {
		b.WriteString(`,"sessionId":`)
		b.WriteString(jsonString(e.SessionID))
	}
	// acknowledged / handedOff 只在为 true 时输出（对账 TS 的可选字段语义）
	if e.Acknowledged {
		b.WriteString(`,"acknowledged":true`)
	}
	if e.HandedOff {
		b.WriteString(`,"handedOff":true`)
	}
	b.WriteString("}")
	return b.String()
}

// jsonString 序列化字符串（对账 JSON.stringify 的转义规则）。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// isoMillis 格式化 ISO 8601 时间戳，**固定 3 位毫秒**。
//
// 为什么不能用 time.RFC3339Nano：它会**省略尾随零**（860ms → `.86`），
// 而 JS 的 `toISOString()` 总是补零到 3 位（探针实测：`.000`/`.010`/`.100`/
// `.860`）。字节不等价。
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// readRecoveryEntries 读全部 journal 条目（损坏行跳过）。
//
// 对账 readEntries：文件不存在返回空，解析失败的行被过滤。
func readRecoveryEntries(cwd string) []RecoveryEntry {
	raw, err := os.ReadFile(journalPath(cwd))
	if err != nil {
		return nil
	}
	var out []RecoveryEntry
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var e RecoveryEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // 损坏行跳过（对账 TS 的 try/catch）
		}
		out = append(out, e)
	}
	return out
}

// writeRecoveryEntries 全量重写 journal（对账 writeEntries）。
func writeRecoveryEntries(cwd string, entries []RecoveryEntry) error {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(journalJSON(e))
		b.WriteString("\n")
	}
	return os.WriteFile(journalPath(cwd), []byte(b.String()), 0o644)
}

// RecordRecovery 追加一条恢复事件。
//
// 对账 recordRecovery。**这是 best-effort 审计副作用**——写失败不应让
// 已成功的回滚伪装成失败（见 RestoreLatestBackup 的注释）。
func RecordRecovery(cwd string, entry RecoveryEntry, sessionID string) error {
	dir := filepath.Join(cwd, ".rivet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entry.TS = isoMillis(time.Now())
	entry.SessionID = sessionID

	f, err := os.OpenFile(journalPath(cwd), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(journalJSON(entry) + "\n")
	return err
}

// ReadUnacknowledged 返回未确认、未交接的条目。
//
// 对账 readUnacknowledged：sessionID 为 nil（Go 用空串表示）时返回全部；
// 否则**只返回该会话的条目**——legacy 无会话归属的条目是历史证据，
// 不构成「另一会话仍拥有该文件」的证明。
func ReadUnacknowledged(cwd, sessionID string) []RecoveryEntry {
	var out []RecoveryEntry
	for _, e := range readRecoveryEntries(cwd) {
		if e.Acknowledged || e.HandedOff {
			continue
		}
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// AcknowledgeAll 把符合条件的条目标记为已确认。
//
// 对账 acknowledgeAll：只写盘**当确有变更**时（避免无谓 IO）。
func AcknowledgeAll(cwd, sessionID string) error {
	entries := readRecoveryEntries(cwd)
	changed := false
	for i := range entries {
		e := &entries[i]
		if e.Acknowledged || e.HandedOff {
			continue
		}
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		e.Acknowledged = true
		changed = true
	}
	if !changed {
		return nil
	}
	return writeRecoveryEntries(cwd, entries)
}

// HandoffRecoveries 把源会话的恢复证据标记为已交接。
//
// 对账 handoffRecoveries：**注意与 AcknowledgeAll 的差别**——这里 sessionID
// 是必填的（只标记该会话的条目），不做「空串=全部」的退化。
func HandoffRecoveries(cwd, sessionID string) error {
	entries := readRecoveryEntries(cwd)
	changed := false
	for i := range entries {
		e := &entries[i]
		if e.Acknowledged || e.HandedOff {
			continue
		}
		if e.SessionID != sessionID {
			continue
		}
		e.HandedOff = true
		changed = true
	}
	if !changed {
		return nil
	}
	return writeRecoveryEntries(cwd, entries)
}

// RenderRecoveryStack 渲染恢复栈（供 CLI/提示展示）。
//
// 对账 renderRecoveryStack。
func RenderRecoveryStack(cwd, sessionID string) string {
	entries := ReadUnacknowledged(cwd, sessionID)
	if len(entries) == 0 {
		return "Recovery stack empty — no unacknowledged recovery events."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Recovery stack (%d):\n", len(entries)))
	for i, e := range entries {
		b.WriteString(fmt.Sprintf("%d. %s — %s (%d lines lost, %s)\n",
			i+1, e.File, e.Action, e.LinesLost, e.TS))
	}
	b.WriteString("\nThese files were restored during the session; verify intent before deliver_task.")
	return b.String()
}
