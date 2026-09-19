// Package recovery 提供文件变更的备份与恢复（写工具的横切关注点）。
//
// 对账 src/agent/recovery-stack.ts（243 行）+ recovery-journal.ts（93 行）。
//
// 定位：**写工具的共享地基**。write_file / edit_file / hash_edit / apply_patch
// 在覆写前调 TrackFileChange 捕获旧内容，失败时调 RestoreLatestBackup 回滚。
// 与「会话」概念正交——它是文件操作的横切关注点，故独立成包。
//
// **核心不变量**：旧内容必须在覆写**前**捕获。调用方必须等待
// TrackFileChange 返回后再写文件，否则备份会拷到新内容。
package recovery

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileChangeRecord 是一次文件变更的轻量记录（含用于撤销的备份）。
type FileChangeRecord struct {
	FilePath   string
	Action     string // "edit" | "write" | "delete"
	BackupPath string
	ToolCallID string
	TS         int64
}

const (
	// maxBackupDirs 是 `.rivet/backups/` 保留的时间戳目录上限。
	maxBackupDirs = 100
	// evictInterval 是淘汰去频窗口（见 evictOldBackupsDebounced 注释）。
	evictInterval = 5 * time.Minute
	// memoryBackupCap 是内存备份条数上限（FIFO 淘汰）。
	memoryBackupCap = 20
	// memoryBackupMaxBytes 超过此体积或含 NUL 时退回磁盘拷贝路径。
	memoryBackupMaxBytes = 10 * 1024 * 1024
)

// memoryBackup 是内存备份项：旧内容在覆写前已读入（回滚正确性所系）。
type memoryBackup struct {
	content    string
	backupPath string
}

// stack 是备份栈（每 cwd 一个实例，由 Stack 管理）。
//
// 对账 TS 的模块级 Map——Go 侧显式持有状态，便于测试隔离（TS 用
// __resetEvictDebounceForTest 这类测试钩子，Go 用实例更干净）。
type Stack struct {
	mu sync.Mutex
	// latestBackups 是 (cwd, relPath) → 最近备份路径。
	latestBackups map[string]string
	// memoryBackups 是 (cwd, relPath) → 内存备份（回滚优先用它）。
	memoryBackups map[string]memoryBackup
	// memoryOrder 是内存备份的插入序（FIFO 淘汰用）。
	memoryOrder []string
	// lastEvict 是 (cwd) → 上次淘汰时间（去频）。
	lastEvict map[string]int64
	// now 可注入时钟（测试用）。
	now func() int64
}

// NewStack 构造备份栈。
func NewStack() *Stack {
	return &Stack{
		latestBackups: map[string]string{},
		memoryBackups: map[string]memoryBackup{},
		lastEvict:     map[string]int64{},
		now:           func() int64 { return time.Now().UnixMilli() },
	}
}

// backupKey 构造 (cwd, relPath) 的复合键。
//
// 对账 backupKey：TS 用 join(cwd, filePath)——用**规范化绝对路径**做键，
// 避免跨会话碰撞。
func backupKey(cwd, relPath string) string {
	return filepath.Join(cwd, relPath)
}

// TrackFileChange 在变更**前**备份文件并记录变更。
//
// 对账 trackFileChange。备份落在 `.rivet/backups/<时间戳>/<相对路径>`。
//
// **调用方纪律**：必须等本函数返回后再写文件——旧内容在返回前已捕获入内存。
//
// 与 TS 的差异：TS 把备份文件的磁盘写放到后台（fire-and-forget）以避开
// Windows Defender 扫描时延。Go 侧**同步落盘**——因为 Go 没有同样的
// 主线程阻塞问题（每次工具调用在独立 goroutine），且同步语义更简单、
// 崩溃时不丢备份。内存备份仍保留（回滚优先用内存，避免读盘）。
func (s *Stack) TrackFileChange(cwd string, rec FileChangeRecord) (FileChangeRecord, error) {
	absPath := filepath.Join(cwd, rec.FilePath)

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		// 文件不存在（新建）→ 无备份可做（对账：trackFileChange 只备份已存在文件）
		rec.TS = s.now()
		return rec, nil
	}

	ts := s.now()
	backupDir := filepath.Join(cwd, ".rivet", "backups", strconv.FormatInt(ts, 10))
	relDir := filepath.Dir(rec.FilePath)
	backupPath := filepath.Join(backupDir, rec.FilePath)

	content, readErr := os.ReadFile(absPath)
	if readErr == nil && len(content) <= memoryBackupMaxBytes && !hasNUL(content) {
		// 文本文件：内存捕获（回滚优先用内存）
		if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
			return rec, err
		}
		if err := os.WriteFile(backupPath, content, 0o644); err != nil {
			return rec, err
		}
		key := backupKey(cwd, rec.FilePath)
		s.mu.Lock()
		s.memoryBackups[key] = memoryBackup{content: string(content), backupPath: backupPath}
		s.memoryOrder = append(s.memoryOrder, key)
		// FIFO 上限
		for len(s.memoryOrder) > memoryBackupCap {
			oldest := s.memoryOrder[0]
			s.memoryOrder = s.memoryOrder[1:]
			delete(s.memoryBackups, oldest)
		}
		s.latestBackups[key] = backupPath
		s.mu.Unlock()
	} else {
		// 二进制/超大：走文件拷贝
		dir := backupDir
		if relDir != "." && relDir != "" {
			dir = filepath.Join(backupDir, relDir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return rec, err
		}
		if err := copyFile(absPath, backupPath); err != nil {
			return rec, err
		}
		s.mu.Lock()
		s.latestBackups[backupKey(cwd, rec.FilePath)] = backupPath
		s.mu.Unlock()
	}

	s.evictDebounced(cwd)

	rec.BackupPath = backupPath
	rec.TS = ts
	return rec, nil
}

// RestoreLatestBackup 把文件恢复到最近一次备份的内容。
//
// 对账 restoreLatestBackup。返回 true 表示确有备份并被恢复。
//
// **内存优先**：覆写前捕获的旧内容直接在手里（磁盘备份可能因崩溃缺失）。
//
// 记账是 best-effort 审计副作用：journal 写失败**不得**把已成功的回滚
// 伪装成失败——文件此刻已恢复，报 false 会诱发调用方重复编辑，把刚恢复的
// 旧内容又盖掉。
func (s *Stack) RestoreLatestBackup(cwd, relPath, sessionID string) bool {
	key := backupKey(cwd, relPath)

	s.mu.Lock()
	mem, hasMem := s.memoryBackups[key]
	diskPath, hasDisk := s.latestBackups[key]
	s.mu.Unlock()

	restored := false
	if hasMem {
		if err := os.WriteFile(filepath.Join(cwd, relPath), []byte(mem.content), 0o644); err == nil {
			restored = true
		}
	} else if hasDisk {
		if _, err := os.Stat(diskPath); err == nil {
			if err := copyFile(diskPath, filepath.Join(cwd, relPath)); err == nil {
				restored = true
			}
		}
	}
	if !restored {
		return false
	}

	// best-effort 记账：失败不影响回滚结论
	_ = RecordRecovery(cwd, RecoveryEntry{
		File:      relPath,
		Action:    "restore latest backup",
		LinesLost: 0,
	}, sessionID)
	return true
}

// EvictOldBackups 淘汰超出上限的最旧时间戳目录。
//
// 对账 evictOldBackups：目录名是时间戳（数字），故**名字序 = 年龄序**；
// 只有纯数字名可淘汰，外来目录永不触碰。best-effort——失败静默降级。
func (s *Stack) EvictOldBackups(cwd string, maxDirs int) {
	backupsDir := filepath.Join(cwd, ".rivet", "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && isAllDigits(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	excess := len(names) - maxDirs
	if excess <= 0 {
		return
	}
	for _, name := range names[:excess] {
		_ = os.RemoveAll(filepath.Join(backupsDir, name))
	}
}

// evictDebounced 是去频版淘汰：窗口内至多一次。
//
// 对账 evictOldBackupsDebounced。淘汰（readdir 全目录 + 递归删）不逐编辑跑，
// 每 cwd 至多 5 分钟一次——窗口内目录数可短暂超上限，下一窗口收敛。
func (s *Stack) evictDebounced(cwd string) {
	now := s.now()
	s.mu.Lock()
	last, ok := s.lastEvict[cwd]
	if ok && now-last < int64(evictInterval/time.Millisecond) {
		s.mu.Unlock()
		return
	}
	s.lastEvict[cwd] = now
	s.mu.Unlock()
	s.EvictOldBackups(cwd, maxBackupDirs)
}

// ResetEvictWindow 清去频窗口（测试钩子，对账 __resetEvictDebounceForTest）。
func (s *Stack) ResetEvictWindow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEvict = map[string]int64{}
}

// ── 辅助 ──

func hasNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// EstimateLinesLost 估算相对备份丢失的行数。
//
// 对账 estimateLinesLost：文件已删除时返回备份行数。
func (s *Stack) EstimateLinesLost(cwd, relPath, backupPath string) int {
	key := backupKey(cwd, relPath)
	s.mu.Lock()
	mem, hasMem := s.memoryBackups[key]
	s.mu.Unlock()

	var backupContent string
	if hasMem {
		backupContent = mem.content
	} else if backupPath != "" {
		if b, err := os.ReadFile(backupPath); err == nil {
			backupContent = string(b)
		} else {
			return 0
		}
	} else {
		return 0
	}

	backupLines := strings.Count(backupContent, "\n") + 1
	current, err := os.ReadFile(filepath.Join(cwd, relPath))
	if err != nil {
		return backupLines // 文件已删除
	}
	currentLines := strings.Count(string(current), "\n") + 1
	if backupLines-currentLines < 0 {
		return 0
	}
	return backupLines - currentLines
}
