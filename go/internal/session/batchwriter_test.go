package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// bwOracle 是 TS SessionBatchWriter 的真实产出。
// 生成命令：npx tsx go/testdata/batchwriter/gen-oracle.ts
type bwOracle struct {
	Cases map[string]struct {
		Note         string            `json:"note"`
		Preexisting  *string           `json:"preexisting"`
		Observations []json.RawMessage `json:"observations"`
		FinalDecoded *string           `json:"finalDecoded"`
		BackupFiles  []string          `json:"backupFiles"`
	} `json:"cases"`
}

func loadBWOracle(t *testing.T) bwOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "batchwriter", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/batchwriter/gen-oracle.ts", path, err)
	}
	var o bwOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// decodeFile 读文件并解码为 JSONL 文本（文件不存在返回 nil）。
func decodeFile(t *testing.T, tr *Transcript, path string) *string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s, err := tr.DecodeTranscriptText(raw)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	return &s
}

// TestBatchWriterFirstLineSync —— 首行同步落盘（新会话文件立即存在）。
func TestBatchWriterFirstLineSync(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, err := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	defer w.Close()

	w.EnqueueLine("line1\n")

	if _, err := os.Stat(fp); err != nil {
		t.Fatal("首行后文件应存在（同步落盘）")
	}
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "line1\n" {
		t.Errorf("首行后内容应为 line1，得到 %v", got)
	}
}

// TestBatchWriterSecondLineQueued —— 第二行排队，文件仍是首行内容。
func TestBatchWriterSecondLineQueued(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	w.EnqueueLine("line1\n")
	w.EnqueueLine("line2\n")

	// 未 flush → 文件仍只有首行
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "line1\n" {
		t.Errorf("第二行应仍在队列，文件应为 line1，得到 %v", got)
	}
	// mergePending 反映的是**磁盘内容 + pending**：磁盘已有 line1（首行
	// 同步落盘），pending 只剩 line2。故传空串时得到 line2。
	// 要看到完整两行，应传磁盘内容（见 TestBatchWriterMergePending）。
	if merged := w.MergePending(""); merged != "line2\n" {
		t.Errorf("pending 应只剩第二行，得到 %q", merged)
	}
}

// TestBatchWriterMergePending —— 未 flush 的行对进程内读者可见。
func TestBatchWriterMergePending(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	// pending 为空时原样返回磁盘内容
	if got := w.MergePending("ONDISK\n"); got != "ONDISK\n" {
		t.Errorf("pending 为空应原样返回，得到 %q", got)
	}

	w.EnqueueLine("line1\n") // 同步落盘（pending 清空）
	w.EnqueueLine("line2\n") // 排队
	// mergePending(磁盘内容) = 磁盘内容 + pending
	if got := w.MergePending("ONDISK\n"); got != "ONDISK\nline2\n" {
		t.Errorf("应合并 pending 行，得到 %q", got)
	}
}

// TestBatchWriterFlushDrains —— flush 把 pending 排空。
func TestBatchWriterFlushDrains(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	w.EnqueueLine("a\n")
	w.EnqueueLine("b\n")
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "a\nb\n" {
		t.Errorf("flush 后应为 a\\nb，得到 %v", got)
	}
	// flush 后 pending 应清空
	if merged := w.MergePending("X\n"); merged != "X\n" {
		t.Errorf("flush 后 pending 应为空，得到 %q", merged)
	}
}

// TestBatchWriterMultiBatch —— 多批次产生多个帧。
func TestBatchWriterMultiBatch(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	w.EnqueueLine("a\n")
	w.EnqueueLine("b\n")
	_ = w.Flush()
	w.EnqueueLine("c\n")
	_ = w.Flush()

	got := decodeFile(t, tr, fp)
	if got == nil || *got != "a\nb\nc\n" {
		t.Errorf("多批次应为 a\\nb\\nc，得到 %v", got)
	}
	// **3 个帧**：首行同步落盘（帧1）+ flush(a,b)（帧2）+ flush(c)（帧3）。
	// 首行同步 flush 是独立的一帧——这是新会话「文件立即存在」语义的直接后果。
	raw, _ := os.ReadFile(fp)
	sc, err := prompt.ScanZstdFrames(raw)
	if err != nil {
		t.Fatalf("扫描帧失败：%v", err)
	}
	if len(sc.Frames) != 3 {
		t.Errorf("应有 3 个帧（首行同步 + 两次 flush），得到 %d", len(sc.Frames))
	}
}

// TestBatchWriterEmptyLineNoFile —— 空行不产帧、不建文件。
func TestBatchWriterEmptyLineNoFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	w.EnqueueLine("")
	if _, err := os.Stat(fp); err == nil {
		t.Error("空行不应建文件")
	}
}

// TestBatchWriterLegacyMigration —— legacy 纯文本首次写入时被转码。
//
// **注意备份语义**：TS 的 copyFileSync 在备份目录不存在时抛错并被 catch
// 吞掉 → 无备份。oracle 的 legacyMigration 用例锁定 backups 为空。
func TestBatchWriterLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	backupDir := filepath.Join(dir, "backups") // **故意不建**
	if err := os.WriteFile(fp, []byte("legacy-1\nlegacy-2\n"), 0o644); err != nil {
		t.Fatalf("预置失败：%v", err)
	}
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return backupDir }, tr)
	defer w.Close()

	w.EnqueueLine("new\n")
	_ = w.Flush()

	// 转码后应是 zstd 帧流
	raw, _ := os.ReadFile(fp)
	if !prompt.IsZstdFrameStream(raw) {
		t.Error("迁移后应是 zstd 帧流")
	}
	// 内容应是 legacy + new
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "legacy-1\nlegacy-2\nnew\n" {
		t.Errorf("迁移后内容不符：%v", got)
	}
	// 备份目录不存在 → 无备份（对账 TS）
	if entries, err := os.ReadDir(backupDir); err == nil {
		t.Errorf("备份目录不应存在，却有 %d 项", len(entries))
	}
}

// TestBatchWriterLegacyBackupWhenDirExists —— 备份目录存在时产生备份。
func TestBatchWriterLegacyBackupWhenDirExists(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("建备份目录失败：%v", err)
	}
	if err := os.WriteFile(fp, []byte("legacy\n"), 0o644); err != nil {
		t.Fatalf("预置失败：%v", err)
	}
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return backupDir }, tr)
	defer w.Close()

	w.EnqueueLine("new\n")
	_ = w.Flush()

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("读备份目录失败：%v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应有 1 个备份，得到 %d", len(entries))
	}
	// 备份名：<basename>.pre-zstd
	if entries[0].Name() != "s.jsonl.pre-zstd" {
		t.Errorf("备份名不符：%q", entries[0].Name())
	}
	// 备份内容是**原始纯文本**
	bak, _ := os.ReadFile(filepath.Join(backupDir, entries[0].Name()))
	if string(bak) != "legacy\n" {
		t.Errorf("备份应是原始纯文本，得到 %q", string(bak))
	}
}

// TestBatchWriterAlreadyZstdNoMigration —— 已压缩文件不迁移、无备份。
func TestBatchWriterAlreadyZstdNoMigration(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("建备份目录失败：%v", err)
	}
	tr, _ := NewTranscript()
	defer tr.Close()
	// 预置一个真实帧
	if err := os.WriteFile(fp, tr.EncodeBatch("already\n"), 0o600); err != nil {
		t.Fatalf("预置失败：%v", err)
	}
	w, _ := NewBatchWriter(fp, func() string { return backupDir }, tr)
	defer w.Close()

	_ = w.Flush()

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 0 {
		t.Errorf("已压缩文件不应产生备份，得到 %d 项", len(entries))
	}
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "already\n" {
		t.Errorf("内容应不变，得到 %v", got)
	}
}

// TestBatchWriterOracleParity —— 逐用例对账 TS oracle 的最终态。
func TestBatchWriterOracleParity(t *testing.T) {
	o := loadBWOracle(t)
	// 各用例的操作序列（从生成器复刻）
	type act struct {
		kind string
		line string
	}
	seq := map[string][]act{
		"firstLineSyncFlush":     {{"enqueue", "line1\n"}},
		"secondLineQueued":       {{"enqueue", "line1\n"}, {"enqueue", "line2\n"}},
		"mergePendingVisible":    {{"enqueue", "line1\n"}, {"enqueue", "line2\n"}},
		"mergePendingEmpty":      {},
		"flushDrainsBatch":       {{"enqueue", "line1\n"}, {"enqueue", "line2\n"}, {"flush", ""}},
		"flushAfterFirstLine":    {{"enqueue", "line1\n"}, {"flush", ""}},
		"multiBatch":             {{"enqueue", "a\n"}, {"enqueue", "b\n"}, {"flush", ""}, {"enqueue", "c\n"}, {"flush", ""}},
		"emptyLine":              {{"enqueue", ""}},
		"legacyMigration":        {{"enqueue", "new-line\n"}, {"flush", ""}},
		"alreadyZstdNoMigration": {{"flush", ""}},
	}

	checked := 0
	for name, c := range o.Cases {
		acts, ok := seq[name]
		if !ok {
			t.Errorf("[%s] 缺操作序列", name)
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			fp := filepath.Join(dir, "s.jsonl")
			backupDir := filepath.Join(dir, "backups")

			tr, _ := NewTranscript()
			defer tr.Close()

			// 预置
			if name == "alreadyZstdNoMigration" {
				if err := os.WriteFile(fp, tr.EncodeBatch("already-compressed\n"), 0o600); err != nil {
					t.Fatalf("预置失败：%v", err)
				}
			} else if c.Preexisting != nil && *c.Preexisting != "" {
				if err := os.WriteFile(fp, []byte(*c.Preexisting), 0o644); err != nil {
					t.Fatalf("预置失败：%v", err)
				}
			}

			w, _ := NewBatchWriter(fp, func() string { return backupDir }, tr)
			defer w.Close()

			for _, a := range acts {
				switch a.kind {
				case "enqueue":
					w.EnqueueLine(a.line)
				case "flush":
					if err := w.Flush(); err != nil {
						t.Fatalf("flush 失败：%v", err)
					}
				}
			}

			// 最终态对账
			got := decodeFile(t, tr, fp)
			switch {
			case c.FinalDecoded == nil && got != nil:
				t.Errorf("期望无文件，得到 %q", *got)
			case c.FinalDecoded != nil && got == nil:
				t.Errorf("期望 %q，文件不存在", *c.FinalDecoded)
			case c.FinalDecoded != nil && got != nil && *got != *c.FinalDecoded:
				t.Errorf("最终内容不符\n  Go =%q\n  TS =%q", *got, *c.FinalDecoded)
			}

			// 备份文件对账
			var backups []string
			if entries, err := os.ReadDir(backupDir); err == nil {
				for _, e := range entries {
					backups = append(backups, e.Name())
				}
				sort.Strings(backups)
			}
			if len(backups) != len(c.BackupFiles) {
				t.Errorf("备份数不符：Go=%v TS=%v", backups, c.BackupFiles)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestBatchWriterFlushFailureKeepsPending —— **写入失败时行被放回队列**。
//
// 这是 TS 注释明确强调的语义：「a failed append must not permanently drop
// queued lines (ENOSPC etc.)」。构造：把会话文件路径指向一个**目录**
// （写文件必然失败），验证 Flush 报错且 pending 未丢。
func TestBatchWriterFlushFailureKeepsPending(t *testing.T) {
	dir := t.TempDir()
	// 用一个目录冒充文件路径 → OpenFile(O_WRONLY) 必然失败
	fp := filepath.Join(dir, "as-dir")
	if err := os.Mkdir(fp, 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	// 先塞进 pending（不走首行同步路径——文件"存在"（是目录））
	w.mu.Lock()
	w.codecReady = true // 跳过迁移，直接走 flush
	w.mu.Unlock()
	w.EnqueueLine("keep-me\n")

	err := w.Flush()
	if err == nil {
		t.Fatal("写目录应失败")
	}
	// **关键**：失败后 pending 必须还在
	if merged := w.MergePending(""); merged != "keep-me\n" {
		t.Errorf("失败后行应留在 pending，得到 %q", merged)
	}
}

// TestBatchWriterEmptyBatchNoFrame —— 空批次不产帧（不写魔数占位）。
//
// 构造：先建一个已有内容的会话（codec 就绪），再 enqueue 空行并 flush——
// 若实现给空批次写了 4 字节魔数，解码会失败。
func TestBatchWriterEmptyBatchNoFrame(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	tr, _ := NewTranscript()
	defer tr.Close()
	w, _ := NewBatchWriter(fp, func() string { return filepath.Join(dir, "backups") }, tr)
	defer w.Close()

	w.EnqueueLine("real\n")
	_ = w.Flush()
	before, _ := os.ReadFile(fp)
	beforeLen := len(before)

	// 空行入队 + flush → 不应产生任何字节
	w.EnqueueLine("")
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	after, _ := os.ReadFile(fp)
	if len(after) != beforeLen {
		t.Errorf("空批次不应写字节：%d → %d", beforeLen, len(after))
	}
	// 且仍能解码
	got := decodeFile(t, tr, fp)
	if got == nil || *got != "real\n" {
		t.Errorf("内容应不变，得到 %v", got)
	}
}
