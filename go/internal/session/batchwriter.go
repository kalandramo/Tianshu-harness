package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// batchFlushWindow 是 write-behind 的定时窗口。
//
// 对账 session-batch-writer.ts 的 200ms。
const batchFlushWindow = 200 * time.Millisecond

// BatchWriter 是会话 transcript 的 write-behind 批量写入器。
//
// 对账 src/agent/session-batch-writer.ts（129 行）。语义：
//   - 行先排队在内存，成批 flush 为**一个带校验和的 zstd 帧**
//   - flush 触发点：200ms 定时窗口 / 显式 flush 屏障 / 压缩
//   - **新会话首行同步 flush**——会话文件立即存在（listSessions 靠 .jsonl
//     存在性解析会话），且崩溃在前 200ms 内不会丢掉整个会话
//
// 并发：内部持锁，EnqueueLine 与 Flush 可并发调用。
type BatchWriter struct {
	filePath string
	// backupDirProvider 惰性提供备份目录（避免构造时建目录）。
	backupDirProvider func() string

	mu         sync.Mutex
	pending    string
	timer      *time.Timer
	codecReady bool
	transcript *Transcript
	closed     bool
}

// NewBatchWriter 构造批量写入器。
//
// transcript 为 nil 时内部自建（便于测试注入）。
func NewBatchWriter(filePath string, backupDirProvider func() string, tr *Transcript) (*BatchWriter, error) {
	if tr == nil {
		var err error
		tr, err = NewTranscript()
		if err != nil {
			return nil, err
		}
	}
	return &BatchWriter{
		filePath:          filePath,
		backupDirProvider: backupDirProvider,
		transcript:        tr,
	}, nil
}

// Close 停掉定时器并释放 codec。
func (w *BatchWriter) Close() {
	w.mu.Lock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
	w.transcript.Close()
}

// EnqueueLine 把一行 JSONL 排入批次。
//
// 对账 enqueueLine。**首行同步落盘**是核心语义：新会话（codec 未就绪且文件
// 不存在）时立即 flush，让文件马上存在。
func (w *BatchWriter) EnqueueLine(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.pending += line

	if w.timer != nil {
		return // 已有定时器在跑，等它
	}
	if !w.codecReady {
		if _, err := os.Stat(w.filePath); os.IsNotExist(err) {
			// 新会话首行：同步 flush
			w.flushLocked()
			return
		}
	}
	w.timer = time.AfterFunc(batchFlushWindow, func() {
		w.mu.Lock()
		w.timer = nil
		w.mu.Unlock()
		_ = w.Flush()
	})
}

// Flush 是 flush 屏障：取消定时器，把 pending 排空成一个 zstd 帧追加。
//
// 对账 flush。失败时 pending 被**放回缓冲区头部**，下次 flush 重试
// （ENOSPC 之类的失败不能让排队的行永久丢失）。
func (w *BatchWriter) Flush() error {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()

	for {
		w.mu.Lock()
		if w.pending == "" {
			w.mu.Unlock()
			return nil
		}
		text := w.pending
		w.pending = ""
		w.mu.Unlock()

		if err := w.writeBatch(text); err != nil {
			// 放回头部，不丢行
			w.mu.Lock()
			w.pending = text + w.pending
			w.mu.Unlock()
			return err
		}
	}
}

// flushLocked 是持锁状态下的同步 flush（首行路径用）。
func (w *BatchWriter) flushLocked() {
	text := w.pending
	w.pending = ""
	if text == "" {
		return
	}
	w.ensureCodecFormatLocked()
	frame := w.transcript.EncodeBatch(text)
	if len(frame) == 0 {
		return
	}
	f, err := os.OpenFile(w.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		w.pending = text + w.pending
		return
	}
	defer f.Close()
	_, _ = f.Write(frame)
}

// MergePending 把磁盘上的 transcript 文本与仍在排队的行合并。
//
// 对账 mergePending。**进程内读者必须看到未 flush 的行**——
// append 后立刻 load（不 flush）是合法用法。
func (w *BatchWriter) MergePending(onDiskText string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		return onDiskText + w.pending
	}
	return onDiskText
}

// writeBatch 把一个批次的文本编码成帧并追加。
func (w *BatchWriter) writeBatch(text string) error {
	w.mu.Lock()
	w.ensureCodecFormatLocked()
	w.mu.Unlock()

	frame := w.transcript.EncodeBatch(text)
	if len(frame) == 0 {
		return nil
	}
	f, err := os.OpenFile(w.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("追加会话帧失败：%w", err)
	}
	defer f.Close()
	if _, err := f.Write(frame); err != nil {
		return fmt.Errorf("写入会话帧失败：%w", err)
	}
	// fdatasync——非关键路径，失败不影响内存态权威性
	_ = f.Sync()
	return nil
}

// ensureCodecFormatLocked 是一次性迁移：首次写入时把 legacy 纯文本
// transcript 备份并转码成单个 zstd 帧。
//
// 对账 ensureCodecFormat。新文件与已压缩文件不受影响。
func (w *BatchWriter) ensureCodecFormatLocked() {
	if w.codecReady {
		return
	}
	w.codecReady = true

	head, err := os.ReadFile(w.filePath)
	if err != nil {
		return // 文件不存在 → 新会话，无需迁移
	}
	if prompt.IsZstdFrameStream(head) {
		return // 已是帧流
	}
	// 备份原文件（best-effort；备份失败仍继续转码）
	if w.backupDirProvider != nil {
		dir := w.backupDirProvider()
		if dir != "" {
			backup := filepath.Join(dir, filepath.Base(w.filePath)+".pre-zstd")
			_ = copyFile(w.filePath, backup)
		}
	}
	frame := w.transcript.EncodeBatch(string(head))
	_ = writeFileAtomic(w.filePath, frame)
}

// copyFile 复制文件（best-effort）。
//
// **不建目标目录**——对账 TS 的 `copyFileSync`：备份目录不存在时它抛错，
// 而调用点 `catch {}` 吞掉，转码照常进行。即「目录不存在 = 无备份」是
// 真实行为（oracle 的 legacyMigration 用例锁定：backups 为空）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// writeFileAtomic 原子写文件（先写临时文件再 rename）。
//
// 对账 src/fs-atomic.ts 的 writeFileAtomicSync。rename 在同文件系统内是
// 原子的——避免半写状态被读到。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// rename 成功后 tmpName 已不存在，Remove 会失败——忽略
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
