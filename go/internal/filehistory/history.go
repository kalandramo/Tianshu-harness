// Package filehistory 提供按**工具调用**分组的多快照文件历史与精确回滚。
//
// 对账 TS `src/agent/file-history.ts`（346 行）。
//
// # 与 internal/recovery 的分工（为什么是两个包）
//
//   - `recovery.Stack` = **写时地基**：写工具覆写**前**备份、失败时回滚
//     **最近一次**。其文件头明写定位「写工具的共享地基……与『会话』概念正交」。
//   - `filehistory` = **会话相关的历史**：在编辑**后**按 tool_use id 分组保存
//     每个被编辑文件的**历次**版本，支持回滚到任意一次调用 + 变更预览。
//
// 两者都落在 `.rivet/` 下但**目录不同**（`backups/` vs `file-history/`），
// 键空间与生命周期也不同。合包会造成职责倒置——把会话相关状态塞进
// 「与会话正交」的包。
//
// **方向性与 `recovery` 相同**（都是「编辑**前**」的内容），但**时机要求相反**：
//
//   - `recovery.TrackFileChange` 由**工具自己**在覆写前调（它紧邻写操作）
//   - 本包的 `TrackEdit` 由**管线**在工具执行前调（`agent.trackEditsBeforeExecution`）
//
// # ★ 时序是本包最容易搞反的地方（曾经搞反过）
//
// 本包在**调用时刻**读磁盘，故备份内容 = 「调用时的磁盘状态」。
// 正确时序是**工具执行前**调用（对账 TS `tool-pipeline.ts:1404`
// 「五件写工具的编辑都要**在执行前**进 file-history」）。
//
// 若改成「写盘成功后」调用（第一百零二刀 W3 的错误实现），后果链是：
//
//	备份 = 编辑后内容 → GetDiffStats 的 oldContent==newContent 恒真
//	→ 预览恒说「没有可撤销的变更」→ Rewind 原样写回却仍报「已恢复 N 个文件」
//
// ——安全网**静默失效**且自称成功。修正见 `agent.trackEditsBeforeExecution`。
package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// MaxSnapshots 是快照数上限（对账 TS `MAX_SNAPSHOTS = 100`）。
const MaxSnapshots = 100

// Backup 是某文件在某快照里的备份描述。
//
// **`FileName == ""` 与 `Unreadable` 是两种不同的世界**（对账 TS 注释）：
//
//   - `FileName == ""` 且 **不** `Unreadable` → 该次 TrackEdit 时**文件不存在**
//     （Rewind 据此 unlink——把新建的文件撤回去）
//   - `Unreadable == true` → 备份**没拿到**（AV/EDR 锁、EBUSY…），
//     Rewind **必须跳过**：此时「没有备份」≠「文件当时不存在」，
//     按前者处理会把 undo 变成**删除**（数据丢失）。
type Backup struct {
	FileName   string `json:"backupFileName"`
	Version    int    `json:"version"`
	Timestamp  int64  `json:"timestamp"`
	Unreadable bool   `json:"unreadable,omitempty"`
}

// Snapshot 是一次工具调用触发的快照（key = tool_use id）。
type Snapshot struct {
	MessageID string            `json:"messageId"`
	Files     map[string]Backup `json:"trackedFileBackups"` // 绝对路径 → 备份
	Timestamp int64             `json:"timestamp"`
}

// DiffStats 是 rewind 预览的统计（对账 TS `DiffStats`）。
type DiffStats struct {
	FilesChanged []string
	Insertions   int
	Deletions    int
}

// History 是快照序列。
//
// 对账 TS 的 `class FileHistory`。与 TS 的差异：Go 侧**显式持锁**
// （TS 单线程靠事件循环串行）——工具调用在 goroutine 里跑，并发保护是必需的。
type History struct {
	mu sync.Mutex
	// cwd 是工作区根；备份落在 `<cwd>/.rivet/file-history/<sessionId>/`。
	//
	// **为什么由本包决定路径**（而非让调用方传 backupDir）：
	// `recovery` 用的是 `<cwd>/.rivet/backups/`，且它按**时间戳目录**做保留
	// 上限（`EvictOldBackups` 只删纯数字名的目录）。若两个包共用一个根，
	// recovery 的淘汰逻辑与 filehistory 的文件会互相干扰。
	// 把路径焊在本包内，从结构上排除这种耦合。
	cwd       string
	sessionID string
	snapshots []Snapshot
	tracked   map[string]struct{}
	// now 可注入时钟（测试用；对账 TS 的 `Date.now()`）。
	now func() int64
}

// New 创建 History（生产路径）。
//
// `cwd` 是工作区根——备份落在 `<cwd>/.rivet/file-history/<sessionID>/`。
func New(cwd, sessionID string) *History {
	return NewWithClock(cwd, sessionID, realNow)
}

// NewWithClock 同上，但注入时钟（测试用）。
func NewWithClock(cwd, sessionID string, now func() int64) *History {
	if now == nil {
		now = realNow
	}
	return &History{
		cwd:       cwd,
		sessionID: sessionID,
		tracked:   map[string]struct{}{},
		now:       now,
	}
}

// BackupDir 返回本会话的备份目录（`<cwd>/.rivet/file-history/<sessionId>`）。
//
// 测试与诊断用（对账 TS 的 `join(this.backupDir, this.sessionId)` 组合后的值）。
func (h *History) BackupDir() string {
	return filepath.Join(h.cwd, ".rivet", "file-history", h.sessionID)
}

// SessionID 返回会话 id。
func (h *History) SessionID() string { return h.sessionID }

// TrackEdit 记录一次快照（读的是**调用时刻**的磁盘内容）。
//
// **调用时机由调用方保证**：必须在写工具**执行前**调（见包注释的时序说明）。
//
// 对账 TS `trackEdit(filePath, messageId)`。
//
// 语义（逐条对账）：
//
//  1. 同一 messageId + 同文件已有备份 → 直接返回（**幂等**，不重复备份）
//  2. version 是**该文件历史上出现过的最大 version + 1**（跨快照累加，
//     保证备份文件名不冲突）
//  3. 备份文件名 = `sha256(absPath)[:16]@v<version>`
//  4. 读不到时**区分** ENOENT（→ `FileName=""`）与其他错误（→ `Unreadable`）
//  5. 超过 MaxSnapshots → 淘汰最旧，并**删除其备份文件**（不留孤儿）
func (h *History) TrackEdit(absPath, messageID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.tracked[absPath] = struct{}{}

	// ① 幂等：同一快照里同文件已有备份
	if n := len(h.snapshots); n > 0 {
		last := &h.snapshots[n-1]
		if last.MessageID == messageID {
			if _, exists := last.Files[absPath]; exists {
				return nil
			}
		}
	}

	// ② version 跨快照累加
	version := 1
	for _, s := range h.snapshots {
		if b, ok := s.Files[absPath]; ok && b.Version >= version {
			version = b.Version + 1
		}
	}

	// ③④ 备份：成功写文件；失败按 ENOENT / 其他分流
	backup := h.makeBackup(absPath, version)

	// ⑤ 入快照（同 messageId 追加，否则新建）
	if n := len(h.snapshots); n > 0 && h.snapshots[n-1].MessageID == messageID {
		h.snapshots[n-1].Files[absPath] = backup
	} else {
		h.snapshots = append(h.snapshots, Snapshot{
			MessageID: messageID,
			Files:     map[string]Backup{absPath: backup},
			Timestamp: h.now(),
		})
	}

	h.evictLocked()
	return nil
}

// makeBackup 读磁盘内容并落一份备份（调用方须持有 h.mu）。
func (h *History) makeBackup(absPath string, version int) Backup {
	ts := h.now()
	content, err := os.ReadFile(absPath)
	if err != nil {
		// 空备份名只保留一个含义：「TrackEdit 时文件不存在」（Rewind 据此 unlink）。
		// 存在但读不了是另一个世界——标 Unreadable，让 Rewind **跳过**而不是删掉原文。
		unreadable := !errors.Is(err, os.ErrNotExist)
		v := Backup{Version: version, Timestamp: ts, Unreadable: unreadable}
		return v
	}

	name := backupFileName(absPath, version)
	dir := h.BackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// 建目录失败 → 视作「备份没拿到」（不删原文）
		return Backup{Version: version, Timestamp: ts, Unreadable: true}
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		return Backup{Version: version, Timestamp: ts, Unreadable: true}
	}
	return Backup{FileName: name, Version: version, Timestamp: ts}
}

// backupFileName 复刻 TS 的命名：`sha256(absPath)[:16] + "@v" + version`。
//
// 用**绝对路径**做哈希（TS 的 `createHash('sha256').update(filePath)`）——
// 同一文件在不同消息里得到同名基础，version 区分各层。
func backupFileName(absPath string, version int) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:16] + "@v" + strconv.Itoa(version)
}

// evictLocked 淘汰超上限的最旧快照并删除其备份文件（调用方须持有 h.mu）。
func (h *History) evictLocked() {
	if len(h.snapshots) <= MaxSnapshots {
		return
	}
	excess := len(h.snapshots) - MaxSnapshots
	evicted := h.snapshots[:excess]
	h.snapshots = h.snapshots[excess:]

	dir := h.BackupDir()
	for _, s := range evicted {
		for _, b := range s.Files {
			if b.FileName == "" {
				continue
			}
			// best-effort：已被删或权限问题都跳过（对账 TS 的 `catch { /* already gone */ }`）
			_ = os.Remove(filepath.Join(dir, b.FileName))
		}
	}
}

// Rewind 回滚到指定快照，返回被改动的文件（绝对路径）。
//
// 对账 TS `rewind(targetMessageId)`。
//
// **三条分支（顺序不可换）**：
//
//  1. `Unreadable` → **跳过**（不删！）——「没有备份」≠「文件当时不存在」
//  2. `FileName == ""` → unlink（该文件在当时还不存在）
//  3. 否则 → 写回备份内容（`MkdirAll` 建父目录）
//
// 快照不存在 → 返回错误（对账 TS 的 `throw new Error(...)`，不静默）。
func (h *History) Rewind(targetMessageID string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap, ok := h.findSnapshotLocked(targetMessageID)
	if !ok {
		return nil, fmt.Errorf("Snapshot for %s not found", targetMessageID)
	}

	// **遍历 tracked 而非快照的键**（对账 TS：`for (const filePath of this.trackedFiles)`
	// 再查 `targetSnapshot.trackedFileBackups[filePath]`）——这样顺序稳定
	// （tracked 是插入序），且与 TS 的遍历语义一致。
	paths := make([]string, 0, len(h.tracked))
	for p := range h.tracked {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var changed []string
	for _, filePath := range paths {
		b, ok := snap.Files[filePath]
		if !ok {
			continue // 该快照没碰过这个文件
		}

		// ① Unreadable → 跳过（**不可**当删除）
		if b.Unreadable {
			continue
		}

		// ② 当时不存在 → unlink
		if b.FileName == "" {
			if err := os.Remove(filePath); err == nil {
				changed = append(changed, filePath)
			}
			// 已不存在 → 静默（对账 TS 的 `catch { /* already gone */ }`）
			continue
		}

		// ③ 写回备份内容
		content, err := os.ReadFile(filepath.Join(h.BackupDir(), b.FileName))
		if err != nil {
			continue // 备份缺失 → 跳过（对账 TS 的 `catch { /* backup missing, skip */ }`）
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(filePath, content, 0o644); err != nil {
			continue
		}
		changed = append(changed, filePath)
	}
	return changed, nil
}

// GetDiffStats 计算「回滚到该快照」会造成的变化量（预览用）。
//
// 对账 TS `getDiffStats`：
//
//   - `Unreadable` → 跳过（无备份可比对，避免把「撤不了」误报成整文件删除）
//   - 备份内容 == 磁盘当前内容 → 不计入
//   - 否则计入 `FilesChanged`，并累加行级增删
func (h *History) GetDiffStats(targetMessageID string) (*DiffStats, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	snap, ok := h.findSnapshotLocked(targetMessageID)
	if !ok {
		return nil, false
	}

	paths := make([]string, 0, len(h.tracked))
	for p := range h.tracked {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	stats := &DiffStats{}
	for _, filePath := range paths {
		b, ok := snap.Files[filePath]
		if !ok {
			continue
		}
		if b.Unreadable {
			continue
		}

		// 「备份内容」：无备份名 → 该文件当时不存在 → 旧内容为空字符串
		oldContent := ""
		if b.FileName != "" {
			raw, err := os.ReadFile(filepath.Join(h.BackupDir(), b.FileName))
			if err != nil {
				continue // 备份读不到 → 跳过
			}
			oldContent = string(raw)
		}

		newContent := ""
		if raw, err := os.ReadFile(filePath); err == nil {
			newContent = string(raw)
		}

		if oldContent == newContent {
			continue
		}
		stats.FilesChanged = append(stats.FilesChanged, filePath)

		ins, del := diffStats(oldContent, newContent)
		stats.Insertions += ins
		stats.Deletions += del
	}
	return stats, true
}

// LatestSnapshotID 返回最近一次快照的 id（无快照时返回 "", false）。
func (h *History) LatestSnapshotID() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.snapshots) == 0 {
		return "", false
	}
	return h.snapshots[len(h.snapshots)-1].MessageID, true
}

// HasSnapshot 报告是否存在该 id 的快照。
func (h *History) HasSnapshot(messageID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.findSnapshotLocked(messageID)
	return ok
}

// Snapshots 返回快照的**深拷贝**（测试与诊断用）。
//
// **必须拷贝**：否则调用方能通过返回的切片篡改内部状态
// （TS 的 `getAllSnapshots()` 返回的是内部数组引用——那是 JS 的习惯，
// Go 侧不照搬这种泄漏）。
func (h *History) Snapshots() []Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Snapshot, 0, len(h.snapshots))
	for _, s := range h.snapshots {
		files := make(map[string]Backup, len(s.Files))
		for k, v := range s.Files {
			files[k] = v
		}
		out = append(out, Snapshot{MessageID: s.MessageID, Files: files, Timestamp: s.Timestamp})
	}
	return out
}

// MarkUnreadableForTest 把某快照里的某文件标成 Unreadable。
//
// **为什么需要**：`Unreadable` 由「备份写失败」产生，而正常测试环境下
// 写备份很难失败。测试要验证「Unreadable → 跳过（不删）」这条**数据丢失防线**，
// 必须能精确构造该状态。名字带 ForTest 以标明它是测试钩子
// （对账 TS 的 `__resetEvictDebounceForTest` 惯例）。
func (h *History) MarkUnreadableForTest(messageID, absPath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.snapshots {
		if h.snapshots[i].MessageID != messageID {
			continue
		}
		if b, ok := h.snapshots[i].Files[absPath]; ok {
			b.FileName = ""
			b.Unreadable = true
			h.snapshots[i].Files[absPath] = b
		}
	}
}

// findSnapshotLocked 从后往前找（对账 TS：`for (let i = len-1; i >= 0; i--)`）。
func (h *History) findSnapshotLocked(messageID string) (*Snapshot, bool) {
	for i := len(h.snapshots) - 1; i >= 0; i-- {
		if h.snapshots[i].MessageID == messageID {
			return &h.snapshots[i], true
		}
	}
	return nil, false
}

// diffStats 计算一次比对的增删行数。
//
// 对账 TS 的降级链（worker pool 4s → inline 1s → **行数估算**）。
//
// **为什么 TS 要 worker**：同步 diff 会阻塞事件循环（大文件重写时无界）。
// Go 侧每次工具调用在独立 goroutine，无此约束——故用确切的
// 「行集合比较」实现，无需 worker，也无需超时（复杂度是 O(n) 的哈希比较，
// 不是 Myers 的最坏 O(ND)）。
//
// 统计口径（对账 TS 的 `RawChange{added, removed, count}` 累加）：
// 数出「新增的行数」与「删除的行数」。
func diffStats(before, after string) (insertions, deletions int) {
	if before == after {
		return 0, 0
	}
	oldLines := splitLinesForStats(before)
	newLines := splitLinesForStats(after)

	// 用计数多重集求增删（O(n)），而非 Myers diff——
	// 预览只要「多少行进/多少行出」，不需要逐 hunk 的最优对齐。
	oldCount := map[string]int{}
	for _, l := range oldLines {
		oldCount[l]++
	}
	newCount := map[string]int{}
	for _, l := range newLines {
		newCount[l]++
	}
	for l, n := range newCount {
		if d := n - oldCount[l]; d > 0 {
			insertions += d
		}
	}
	for l, n := range oldCount {
		if d := n - newCount[l]; d > 0 {
			deletions += d
		}
	}
	return insertions, deletions
}

// splitLinesForStats 按行切分（与 TS 的 `content.split('\n')` 同语义）。
//
// 注意：空字符串切成 **0 行**（TS 的 `”.split('\n')` 是 `[”]` 即 1 个空元素，
// 但 TS 在降级路径里用的是 `newContent.length === 0 ? 0 : split(...).length`
// ——即空内容算 0 行）。此处对齐后者的口径。
func splitLinesForStats(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// 尾换行会切出一个空元素——去掉它（"a\n" 是 1 行，不是 2 行）
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
