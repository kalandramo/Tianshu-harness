// Package session 提供会话状态容器。
//
// 对账 src/agent/session-state.ts（326 行，TS 侧零 import——完全自包含）。
//
// 定位：**纯状态容器 + 渲染器**，不碰文件 IO（IO 在 session-persist 层，
// 属后续波次）。当前实现 SessionStateManager 的状态管理与 volatile 渲染，
// 为动态 appendix 与 hook 管线提供会话感知。
package session

import (
	"strings"
	"time"
	"unicode/utf16"
)

// 容量上限。
//
// 对账 TS 的 MAX_* 常量。注意 decisions 的**存储**上限（20）与**渲染**上限
// （5）是两个不同数字——易错点。
const (
	maxDecisions     = 20
	maxVerifications = 30
	maxFacts         = 15
	maxTaskItems     = 30
	volatileMaxChars = 500
	renderDecisions  = 5
	renderFiles      = 10
	taskContentMax   = 160
)

// FileEntry 是一个被访问文件的记录。
type FileEntry struct {
	LastRead     int64
	ArtifactID   string
	ModifiedByMe bool
}

// DecisionEntry 是一条决策记录。
type DecisionEntry struct {
	Decision string
	Reason   string
	Turn     int
}

// VerificationEntry 是一条验证记录。
type VerificationEntry struct {
	Target     string
	Status     string // "passed" | "failed" | "not-run"
	VerifiedAt int64
}

// FactEntry 是一条事实记录。
type FactEntry struct {
	Fact       string
	Evidence   string
	VerifiedAt int64
}

// TaskListItem 是一个任务项。
type TaskListItem struct {
	ID          string
	Content     string
	Status      string // "pending" | "in_progress" | "completed" | "blocked"
	TurnCreated int
	TurnUpdated int
}

// Task 是会话的当前任务描述。
type Task struct {
	Objective   string
	Status      string
	Plan        []string
	CurrentStep *int
}

// SessionState 是会话状态快照。
type SessionState struct {
	Version      int
	SessionID    string
	UpdatedAt    int64
	Task         Task
	TaskList     []TaskListItem
	KnownFacts   []FactEntry
	Decisions    []DecisionEntry
	FileIndex    *FileIndex
	Verification []VerificationEntry
}

// FileIndex 是**保持插入序**的文件记录表。
//
// 为什么不用 map：TS 侧 fileIndex 是普通对象，`Object.entries` 保留**插入序**，
// 而 `renderForVolatile` 依赖该顺序（`Modified:` 行按插入序列出）。
// Go 的 map 遍历顺序随机，排序成字典序会产出与 TS 不同的字节
// （oracle 的 manyModifiedFiles 用例锁定：TS 是 file0,file1,...,file9，
// 字典序会变成 file0,file1,file10,...）。
type FileIndex struct {
	keys   []string
	values map[string]FileEntry
}

// NewFileIndex 构造一个空的有序文件索引。
func NewFileIndex() *FileIndex {
	return &FileIndex{values: map[string]FileEntry{}}
}

// Get 读取一条记录。
func (f *FileIndex) Get(path string) (FileEntry, bool) {
	v, ok := f.values[path]
	return v, ok
}

// Set 写入一条记录（新键追加到尾部，保持插入序）。
func (f *FileIndex) Set(path string, entry FileEntry) {
	if _, ok := f.values[path]; !ok {
		f.keys = append(f.keys, path)
	}
	f.values[path] = entry
}

// Keys 返回插入序的键拷贝。
func (f *FileIndex) Keys() []string {
	return append([]string(nil), f.keys...)
}

// Len 返回条目数。
func (f *FileIndex) Len() int { return len(f.keys) }

// Clone 深拷贝。
func (f *FileIndex) Clone() *FileIndex {
	out := &FileIndex{
		keys:   append([]string(nil), f.keys...),
		values: make(map[string]FileEntry, len(f.values)),
	}
	for k, v := range f.values {
		out.values[k] = v
	}
	return out
}

// statusMarker 是一个状态标记规则。
//
// 对账 STATUS_MARKERS，**顺序敏感**——更具体/终态的先检测
// （completed → blocked → in_progress）。
type statusMarker struct {
	status string
	substr []string // 字面量子串匹配
}

var statusMarkers = []statusMarker{
	{"completed", []string{"✓", "✔", "✅", "[x]"}},
	{"blocked", []string{"⊗", "🚫", "阻塞", "受阻", "卡住"}},
	{"in_progress", []string{"◼", "⏳", "[~]", "进行中", "正在"}},
}

// TotalUsage 是会话累计的 token 计量（对账 TS 的 `state.totalUsage`）。
//
// **InputTokens 是 cache-INCLUSIVE 的总量**（= uncached + cacheRead +
// cacheCreation）。不要再叠加 CacheRead/CacheCreation——DeepSeek 下会
// 恰好翻倍（cache-log 6bfc4465: meta 11.34M vs real 5.67M）。
type TotalUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	ReasoningTokens          int
	HasReasoning             bool
}

// Manager 是会话状态管理器。
type Manager struct {
	state SessionState
	now   func() int64 // 可注入时钟（测试用）
	usage TotalUsage
}

// New 构造一个会话状态管理器。
func New(sessionID string) *Manager {
	return &Manager{
		state: SessionState{
			Version:   1,
			SessionID: sessionID,
			UpdatedAt: time.Now().UnixMilli(),
			Task:      Task{Status: "exploring"},
			FileIndex: NewFileIndex(),
		},
		now: func() int64 { return time.Now().UnixMilli() },
	}
}

// AddUsage 累加一次 API 响应的 token 计量。
//
// 对账 TS 的 `addUsage`——但**只取累计部分**。TS 版还夹带了上下文占用
// 估算（`lastRealPromptTokens` / `tailEstimate` / `contextCalibrationRatio`
// 的 EMA 校准），那属于 `internal/context` 层（Wave 4），此处不做。
//
// 累计语义（对账 TS）：
//   - 四个计数器都是**条件累加**——值为 0/falsy 时跳过（不是无脑加）
//   - `reasoning_tokens` 特殊：它是 OutputTokens 的**子集**（不叠加到
//     output），且用 `?? 0` 语义——一旦提供商报过就持续累加
func (m *Manager) AddUsage(u TotalUsage) {
	if u.InputTokens != 0 {
		m.usage.InputTokens += u.InputTokens
	}
	if u.OutputTokens != 0 {
		m.usage.OutputTokens += u.OutputTokens
	}
	if u.CacheReadInputTokens != 0 {
		m.usage.CacheReadInputTokens += u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != 0 {
		m.usage.CacheCreationInputTokens += u.CacheCreationInputTokens
	}
	if u.HasReasoning {
		m.usage.ReasoningTokens += u.ReasoningTokens
		m.usage.HasReasoning = true
	}
}

// TotalUsage 返回累计计量的副本。
func (m *Manager) TotalUsage() TotalUsage { return m.usage }

// Snapshot 返回状态的深拷贝（调用方不可改内部状态）。
func (m *Manager) Snapshot() SessionState {
	s := m.state
	s.TaskList = append([]TaskListItem(nil), m.state.TaskList...)
	s.KnownFacts = append([]FactEntry(nil), m.state.KnownFacts...)
	s.Decisions = append([]DecisionEntry(nil), m.state.Decisions...)
	s.Verification = append([]VerificationEntry(nil), m.state.Verification...)
	s.FileIndex = m.state.FileIndex.Clone()
	if m.state.Task.Plan != nil {
		s.Task.Plan = append([]string(nil), m.state.Task.Plan...)
	}
	return s
}

// UpdateTask 更新任务描述。
func (m *Manager) UpdateTask(objective, status string, plan []string, currentStep *int) {
	m.state.Task = Task{Objective: objective, Status: status, Plan: plan, CurrentStep: currentStep}
	m.state.UpdatedAt = m.now()
}

// TrackFileRead 记录一次文件读取。
//
// 保留既有的 ModifiedByMe（读不撤销改的标记）。
func (m *Manager) TrackFileRead(path, artifactID string) {
	existing, _ := m.state.FileIndex.Get(path)
	m.state.FileIndex.Set(path, FileEntry{
		LastRead:     m.now(),
		ArtifactID:   artifactID,
		ModifiedByMe: existing.ModifiedByMe,
	})
	m.state.UpdatedAt = m.now()
}

// TrackFileModified 记录一次文件修改。
func (m *Manager) TrackFileModified(path string) {
	existing, ok := m.state.FileIndex.Get(path)
	entry := FileEntry{ModifiedByMe: true}
	if ok {
		entry.LastRead = existing.LastRead
		entry.ArtifactID = existing.ArtifactID
	} else {
		entry.LastRead = m.now()
	}
	m.state.FileIndex.Set(path, entry)
	m.state.UpdatedAt = m.now()
}

// RecordDecision 记录一条决策（超 maxDecisions 保留最近）。
func (m *Manager) RecordDecision(decision, reason string, turn int) {
	m.state.Decisions = append(m.state.Decisions, DecisionEntry{decision, reason, turn})
	if len(m.state.Decisions) > maxDecisions {
		m.state.Decisions = m.state.Decisions[len(m.state.Decisions)-maxDecisions:]
	}
	m.state.UpdatedAt = m.now()
}

// RecordVerification 记录一条验证（同 target 是**替换**而非追加）。
func (m *Manager) RecordVerification(target, status string) {
	entry := VerificationEntry{Target: target, Status: status, VerifiedAt: m.now()}
	found := false
	for i, v := range m.state.Verification {
		if v.Target == target {
			m.state.Verification[i] = entry
			found = true
			break
		}
	}
	if !found {
		m.state.Verification = append(m.state.Verification, entry)
	}
	if len(m.state.Verification) > maxVerifications {
		m.state.Verification = m.state.Verification[len(m.state.Verification)-maxVerifications:]
	}
	m.state.UpdatedAt = m.now()
}

// RecordFact 记录一条事实（超 maxFacts 保留最近）。
func (m *Manager) RecordFact(fact, evidence string) {
	m.state.KnownFacts = append(m.state.KnownFacts, FactEntry{fact, evidence, m.now()})
	if len(m.state.KnownFacts) > maxFacts {
		m.state.KnownFacts = m.state.KnownFacts[len(m.state.KnownFacts)-maxFacts:]
	}
	m.state.UpdatedAt = m.now()
}

// TaskList 返回当前任务列表的拷贝。
func (m *Manager) TaskList() []TaskListItem {
	return append([]TaskListItem(nil), m.state.TaskList...)
}

// detectStatusMarker 检测行上的显式状态标记；无信号返回空串。
//
// 对账 detectStatusMarker。**顺序敏感**（completed 优先）。
func detectStatusMarker(line string) string {
	lower := strings.ToLower(line)
	for _, mk := range statusMarkers {
		for _, s := range mk.substr {
			// 中文/符号子串直接匹配；ASCII 词（如 [x]）大小写不敏感
			if strings.Contains(line, s) || strings.Contains(lower, strings.ToLower(s)) {
				return mk.status
			}
		}
	}
	// TS 侧还有 \bdone\b / \bblocked\b / \bwip\b / \bin[ -]?progress\b 词边界匹配
	for _, mk := range []struct {
		status string
		words  []string
	}{
		{"completed", []string{"done"}},
		{"blocked", []string{"blocked"}},
		{"in_progress", []string{"wip", "in progress", "in-progress"}},
	} {
		for _, w := range mk.words {
			if containsWord(lower, w) {
				return mk.status
			}
		}
	}
	return ""
}

// containsWord 粗略的「词边界」包含检查（ASCII 字母数字算词字符）。
func containsWord(s, word string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i < 0 {
			return false
		}
		pos := idx + i
		before := pos == 0 || !isWordChar(s[pos-1])
		afterPos := pos + len(word)
		after := afterPos >= len(s) || !isWordChar(s[afterPos])
		if before && after {
			return true
		}
		idx = pos + 1
		if idx >= len(s) {
			return false
		}
	}
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// utf16Len 返回 JS 语义的字符串长度（UTF-16 code unit 数）。
//
// **关键**：JS 的 `.length` 是 code unit 数，emoji 等代理对字符计 2。
// 用 Go 的 len([]rune(s)) 会在含 emoji 时给出不同结果——本项目已
// 踩过这个坑（见 HANDOFF 的 UTF-16 计费分叉）。
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// utf16StrippedLen 计算「去掉符号字符后」的 UTF-16 长度。
//
// 对账 TS 的 content.replace(/[`*_\-\s]/g, ”).length：剥掉反引号、
// 星号、下划线、连字符与空白（含 Unicode 空白，由 \s 覆盖）。
func utf16StrippedLen(s string) int {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '`', '*', '_', '-':
			continue
		}
		if isJSSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return utf16Len(b.String())
}

// isJSSpace 近似 JS 正则 \s 的空白类。
func isJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x00A0, 0xFEFF:
		return true
	}
	return false
}

// utf16Slice 按 UTF-16 code unit 截断（对账 JS 的 .slice(0, n)）。
func utf16Slice(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}
