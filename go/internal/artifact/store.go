package artifact

// store.go —— artifact 存储层。
//
// 对账 TS `src/artifact/store.ts` 的 `ArtifactStore`。
//
// # 关键语义（逐条对账 TS）
//
//   - **append-only 索引**：`_index.jsonl` 每行一个 Artifact；重启后
//     `loadIndex` 恢复。**单行损坏不影响其余**（TS 注释：一行坏不能让
//     所有 artifact 不可读）。
//   - **完整性校验**：`ReadRaw` 比对 sha256，不符抛 `CorruptionError`
//     （TS 的 `ArtifactCorruptionError`）——防止把损坏原文喂给模型。
//   - **range 读取有硬上限**：`ReadLineRange` 单次最多 `MaxRangeLines` 行，
//     防止一次把巨型文件拉回上下文。
//   - **流式读取不校验哈希**：`ReadLineRange` 无法验全文哈希（那需要全量
//     字节）——冷归档场景优先可读性（TS 注释明示的取舍）。
//   - **fallback 会话**：可跨会话解析（worker 会话产出的 artifact 不复制）。

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxRangeLines 是单次 range 读取的硬上限。
//
// 对账 TS `MAX_RANGE_LINES`——防止一次 recall 把巨型跨度拉回上下文。
const MaxRangeLines = 5000

// artifactSessionTTL 是 artifact 会话目录的存活期（对账 TS 的 7 天）。
const artifactSessionTTL = 7 * 24 * time.Hour

// maxArtifactSessions 是保留的会话目录上限（对账 TS）。
const maxArtifactSessions = 50

// SaveInput 是保存一条 artifact 的输入。
//
// 对账 TS `SaveArtifactInput`。
type SaveInput struct {
	Tool       string
	Target     string
	RawContent string
	Summary    string
	Sections   []ArtifactSection
}

// Options 是存储层的可注入依赖（对账 TS `ArtifactStoreOptions`）。
//
// **为什么可注入**：`Now` 与 `IDGenerator` 决定持久化字节——测试需要确定性。
type Options struct {
	// Now 返回毫秒时间戳（nil = time.Now().UnixMilli()）。
	Now func() int64
	// IDGenerator 返回 id 后缀（nil = 8 位随机 hex）。
	IDGenerator func() string
}

// CorruptionError 报告 artifact 原文与记录的哈希不符。
//
// 对账 TS `ArtifactCorruptionError`——消息文案逐字对账。
type CorruptionError struct {
	ArtifactID     string
	RawPath        string
	ExpectedSHA256 string
	ActualSHA256   string
}

func (e *CorruptionError) Error() string {
	return fmt.Sprintf("Artifact %s raw content is corrupted; re-read source", e.ArtifactID)
}

// Store 是 artifact 存储。
//
// 对账 TS `ArtifactStore`。零值不可用——用 `NewStore` 构造。
type Store struct {
	artifacts map[string]Artifact
	dir       string
	baseDir   string
	sessionID string
	now       func() int64
	idGen     func() string

	fallbackSessionIDs []string
	fallbackArtifacts  map[string]Artifact
}

// NewStore 构造存储并加载既有索引。
//
// 对账 TS 构造函数：`dir = join(baseDir, sessionId)`，随后 `loadIndex()`。
func NewStore(baseDir, sessionID string, opts Options) *Store {
	s := &Store{
		artifacts:         map[string]Artifact{},
		baseDir:           baseDir,
		dir:               filepath.Join(baseDir, sessionID),
		sessionID:         sessionID,
		now:               opts.Now,
		idGen:             opts.IDGenerator,
		fallbackArtifacts: map[string]Artifact{},
	}
	if s.now == nil {
		s.now = func() int64 { return time.Now().UnixMilli() }
	}
	if s.idGen == nil {
		s.idGen = randomID
	}
	s.loadIndex()
	return s
}

// randomID 返回 8 位随机 hex（对账 TS 的 `randomUUID().slice(0, 8)`）。
//
// **与 TS 的差异（有意）**：TS 截 UUID 的前 8 位（含连字符前的部分）；
// Go 直接取 4 字节随机数的 hex——**随机性等价，字符集不同**。
// id 只用于文件命名与引用，不参与任何字节对账。
func randomID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 熵源失败时退化为时间派生（不阻塞保存——id 只需唯一性）
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

// AddFallbackSession 允许从另一个会话目录解析 artifact（不复制文件）。
//
// 对账 TS `addFallbackSession`——**重复调用同一 id 安全**（去重）。
func (s *Store) AddFallbackSession(sessionID string) {
	for _, id := range s.fallbackSessionIDs {
		if id == sessionID {
			return
		}
	}
	s.fallbackSessionIDs = append(s.fallbackSessionIDs, sessionID)
}

// ForSession 派生一个绑定到另一会话的存储（共享 baseDir / 时钟 / id 生成器）。
//
// 对账 TS `forSession`——coordinator 用它把 worker 产物写进
// `worker-<orderId>` 目录，主存储再经 `AddFallbackSession` 解析。
func (s *Store) ForSession(sessionID string) *Store {
	return NewStore(s.baseDir, sessionID, Options{Now: s.now, IDGenerator: s.idGen})
}

// Save 落盘一条 artifact 并返回其 id。
//
// 对账 TS `save`：mkdir → 写 raw → 建记录 → 追加索引。
//
// **顺序有意义**：先写 raw 再追加索引——若写 raw 失败，索引里不会留下
// 指向不存在文件的记录。
func (s *Store) Save(input SaveInput) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}

	id := s.nextArtifactID(input.Tool)
	rawPath := filepath.Join(s.dir, safeArtifactFileStem(id)+".raw")
	if err := os.WriteFile(rawPath, []byte(input.RawContent), 0o644); err != nil {
		return "", err
	}

	sections := input.Sections
	if sections == nil {
		sections = []ArtifactSection{}
	}
	a := Artifact{
		ID:        id,
		Tool:      input.Tool,
		Target:    input.Target,
		SessionID: s.sessionID,
		CreatedAt: s.now(),
		Summary:   input.Summary,
		Sections:  sections,
		RawPath:   rawPath,
		CharCount: charLen(input.RawContent),
		LineCount: lineCount(input.RawContent),
		SHA256:    sha256Hex(input.RawContent),
	}

	s.artifacts[id] = a
	line, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(s.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return "", err
	}
	return id, nil
}

// Get 按 id 取记录（主会话优先，其次 fallback 会话）。未找到返回 nil。
func (s *Store) Get(id string) *Artifact {
	if a, ok := s.artifacts[id]; ok {
		return &a
	}
	if a, ok := s.fallbackArtifacts[id]; ok {
		return &a
	}
	for _, sid := range s.fallbackSessionIDs {
		if a := s.loadArtifactFromSessionIndex(sid, id); a != nil {
			s.fallbackArtifacts[id] = *a
			return a
		}
	}
	return nil
}

// loadArtifactFromSessionIndex 从另一会话的索引里按 id 找记录。
//
// 对账 TS `loadArtifactFromSessionIndex`——**re-anchor rawPath** 到该会话
// 目录，否则读取会指向错误路径。
func (s *Store) loadArtifactFromSessionIndex(sessionID, id string) *Artifact {
	indexPath := filepath.Join(s.baseDir, sessionID, "_index.jsonl")
	f, err := os.Open(indexPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a Artifact
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			continue // 跳过畸形行
		}
		if a.ID == id && isArtifactForSession(a, sessionID) {
			a.RawPath = filepath.Join(s.baseDir, sessionID, safeArtifactFileStem(id)+".raw")
			return &a
		}
	}
	return nil
}

// ListByTarget 返回目标匹配的 artifact（对账 TS `listByTarget`）。
func (s *Store) ListByTarget(target string) []Artifact {
	out := []Artifact{}
	for _, a := range s.artifacts {
		if a.Target == target {
			out = append(out, a)
		}
	}
	return out
}

// List 返回本会话全部 artifact（对账 TS `list`）。
func (s *Store) List() []Artifact {
	out := make([]Artifact, 0, len(s.artifacts))
	for _, a := range s.artifacts {
		out = append(out, a)
	}
	return out
}

// ReadRaw 读取原文并**校验完整性**。未找到返回 ("", nil)。
//
// 对账 TS `readRaw`——哈希不符返回 `*CorruptionError`。
func (s *Store) ReadRaw(id string) (string, error) {
	a := s.Get(id)
	if a == nil {
		return "", nil
	}
	raw, err := os.ReadFile(a.RawPath)
	if err != nil {
		return "", err
	}
	content := string(raw)
	if got := sha256Hex(content); got != a.SHA256 {
		return "", &CorruptionError{
			ArtifactID:     a.ID,
			RawPath:        a.RawPath,
			ExpectedSHA256: a.SHA256,
			ActualSHA256:   got,
		}
	}
	return content, nil
}

// ReadLines 读取 [startLine, endLine] 行（1-based 闭区间）。
//
// 对账 TS `readLines`：`start = max(1, trunc(startLine))`、
// `end = max(start, trunc(endLine))`，然后 `split('\n').slice(start-1, end)`。
func (s *Store) ReadLines(id string, startLine, endLine int) (string, bool, error) {
	raw, err := s.ReadRaw(id)
	if err != nil {
		return "", false, err
	}
	a := s.Get(id)
	if a == nil {
		return "", false, nil
	}
	start := startLine
	if start < 1 {
		start = 1
	}
	end := endLine
	if end < start {
		end = start
	}
	lines := strings.Split(raw, "\n")
	// slice(start-1, end)：end 超出长度时截断（JS slice 语义）。
	if start-1 >= len(lines) {
		return "", true, nil
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n"), true, nil
}

// RangeResult 是一次 range 读取的结果。
//
// 对账 TS `readLineRange` 的返回对象。
type RangeResult struct {
	Content    string
	TotalLines int
	// Capped 报告请求是否被 MaxRangeLines 截断。
	Capped bool
}

// ReadLineRange **流式**读取行窗口，不把整个文件载入内存。
//
// 对账 TS `readLineRange`。**这是大归档的召回路径**——`compact-history`
// 常超过内存读取上限，正是本特性针对的会话。
//
// **取舍**：流式读无法校验文件级 sha256（需要全量字节），故此处**不校验**。
// 冷归档优先可读性（TS 注释明示）。
//
// 未找到返回 (nil, nil)。
func (s *Store) ReadLineRange(id string, startLine, endLine int) (*RangeResult, error) {
	a := s.Get(id)
	if a == nil {
		return nil, nil
	}

	start := startLine
	if start < 1 {
		start = 1
	}
	requestedEnd := endLine
	if requestedEnd < start {
		requestedEnd = start
	}
	capped := requestedEnd-start+1 > MaxRangeLines
	end := requestedEnd
	if capped {
		end = start + MaxRangeLines - 1
	}

	f, err := os.Open(a.RawPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	collected := []string{}
	lineNo := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lineNo++
		if lineNo >= start && lineNo <= end {
			collected = append(collected, sc.Text())
		}
		// 越过窗口即停——不必扫完多 MB 文件的剩余部分。
		// （故 TotalLines 在成功路径上是**下界**；仅在范围为空/越界时精确，
		// 因为那时会读到 EOF 而从未越过 end。对账 TS 的同名注释。）
		if lineNo > end {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &RangeResult{
		Content:    strings.Join(collected, "\n"),
		TotalLines: lineNo,
		Capped:     capped,
	}, nil
}

// nextArtifactID 生成不冲突的 id：`<tool>:<suffix>`。
//
// 对账 TS `nextArtifactId`：工具名为空时用字面量 "artifact"。
func (s *Store) nextArtifactID(tool string) string {
	safeTool := strings.TrimSpace(tool)
	if safeTool == "" {
		safeTool = "artifact"
	}
	id := safeTool + ":" + s.idGen()
	for {
		if _, exists := s.artifacts[id]; !exists {
			return id
		}
		id = safeTool + ":" + s.idGen()
	}
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "_index.jsonl")
}

// loadIndex 从 append-only 索引恢复本会话的 artifact。
//
// 对账 TS `loadIndex`——**畸形行跳过**（一行坏不能让其余不可读）。
func (s *Store) loadIndex() {
	f, err := os.Open(s.indexPath())
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a Artifact
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			continue
		}
		if isArtifactForSession(a, s.sessionID) {
			s.artifacts[a.ID] = a
		}
	}
}

// isArtifactForSession 校验记录的形态与归属（对账 TS 的同名类型守卫）。
//
// **为什么需要**：索引是 append-only 的共享文件，可能混入其他会话的行或
// 早期版本的畸形记录。逐字段校验比只看 id 更稳。
func isArtifactForSession(a Artifact, sessionID string) bool {
	return a.ID != "" &&
		a.Tool != "" &&
		a.Target != "" &&
		a.SessionID == sessionID &&
		a.RawPath != "" &&
		a.Summary != "" &&
		a.SHA256 != ""
}

// safeArtifactFileStem 把 id 转为安全的文件名主干。
//
// 对账 TS `safeArtifactFileStem`：非 `[A-Za-z0-9._-]` 的字符替换为 `-`。
func safeArtifactFileStem(id string) string {
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// lineCount 对账 TS `lineCount`：空串为 0，否则按 '\n' 分割计数。
//
// **注意**：`"a\n"` 得 2（尾随换行产生一个空段）——这是 TS split 的语义，
// 不是"行数"的直觉定义。
func lineCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// charLen 返回字符数（对账 JS 的 `String.prototype.length`）。
//
// **JS 的 length 是 UTF-16 code unit 数**，不是码点数——补充平面字符
// （emoji）在 JS 里算 2。Go 的 len(string) 是字节数，utf8.RuneCountInString
// 是码点数，**都不等价**。
func charLen(content string) int {
	n := 0
	for _, r := range content {
		if r > 0xFFFF {
			n += 2 // 代理对
		} else {
			n++
		}
	}
	return n
}

// sha256Hex 返回内容的 SHA-256 十六进制摘要（对账 TS 的 `sha256`）。
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// itoa 对账 JS 的 `String(n)`。
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// CleanupOldSessions 清理超期或超量的 artifact 会话目录。
//
// 对账 TS `cleanupOldArtifactSessions`。启动时调一次回收磁盘。
//
// **activeSessionID 永不删除**（对账 TS 的 `continue`）。
//
// 返回删除的目录数。
func CleanupOldSessions(baseDir, activeSessionID string) int {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0
	}

	type sessionDir struct {
		name    string
		path    string
		mtimeMs int64
	}
	dirs := []sessionDir{}
	for _, e := range entries {
		if e.Name() == activeSessionID {
			continue
		}
		full := filepath.Join(baseDir, e.Name())
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, sessionDir{name: e.Name(), path: full, mtimeMs: info.ModTime().UnixMilli()})
	}

	// 按 mtime 升序（最旧在前）。
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mtimeMs < dirs[j].mtimeMs })

	cutoff := time.Now().Add(-artifactSessionTTL).UnixMilli()
	cleaned := 0
	for _, d := range dirs {
		// 超期，或剩余数量仍超上限 → 删除。
		if d.mtimeMs < cutoff || len(dirs)-cleaned > maxArtifactSessions {
			if err := os.RemoveAll(d.path); err == nil {
				cleaned++
			}
		}
	}
	return cleaned
}
