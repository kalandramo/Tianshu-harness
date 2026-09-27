package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 测试辅助 ──────────────────────────────────────────────────────

// newTestHistory 造一个用临时目录的 History。
func newTestHistory(t *testing.T) (*History, string) {
	t.Helper()
	root := t.TempDir()
	h := New(root, "sess1")
	return h, root
}

// writeFile 写一个文件（必要时建父目录），返回绝对路径。
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func readFile(t *testing.T, abs string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// backupName 复刻 TS 的备份文件名算法（测试用它定位备份文件）。
func backupName(abs string, version int) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16] + "@v" + itoa(version)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ── TrackEdit：分组与 version ────────────────────────────────────

// TestTrackEdit_GroupsByMessageID —— ★ 快照按 messageId 分组。
//
// 对账 TS `trackEdit`：同一 messageId 内同文件只备份一次（幂等）；
// 不同 messageId 各自独立快照。
//
// 判别力：若实现不按 id 分组（如按文件），下面的第二条断言会红。
func TestTrackEdit_GroupsByMessageID(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "v1")

	if err := h.TrackEdit(abs, "call_1"); err != nil {
		t.Fatalf("track 失败：%v", err)
	}
	// 同 messageId 第二次 → 幂等，不新增快照
	if err := h.TrackEdit(abs, "call_1"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.Snapshots()); got != 1 {
		t.Fatalf("同 messageId 应只有 1 个快照，实得 %d", got)
	}

	if err := h.TrackEdit(abs, "call_2"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.Snapshots()); got != 2 {
		t.Errorf("不同 messageId 应有 2 个快照，实得 %d", got)
	}
}

// TestTrackEdit_VersionAccumulatesAcrossSnapshots —— ★ version 跨快照累加。
//
// 对账 TS：`for (const s of this.snapshots) { if (b.version >= version) version = b.version + 1 }`
//
// **为什么重要**：version 进备份文件名。若不累加（恒 1），第二个快照的备份
// 会**覆盖**第一个 → rewind 到早期快照时读到的内容全错。
//
// 判别力：把 version 改成恒 1 → 本用例的「备份文件各不相同」断言必红。
func TestTrackEdit_VersionAccumulatesAcrossSnapshots(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "v1")

	_ = h.TrackEdit(abs, "call_1")
	writeFile(t, root, "a.txt", "v2")
	_ = h.TrackEdit(abs, "call_2")
	writeFile(t, root, "a.txt", "v3")
	_ = h.TrackEdit(abs, "call_3")

	snaps := h.Snapshots()
	if len(snaps) != 3 {
		t.Fatalf("应 3 个快照，实得 %d", len(snaps))
	}
	versions := []int{}
	names := map[string]bool{}
	for _, s := range snaps {
		b := s.Files[abs]
		versions = append(versions, b.Version)
		names[b.FileName] = true
	}
	for i, want := range []int{1, 2, 3} {
		if versions[i] != want {
			t.Errorf("第 %d 个快照的 version 应为 %d，实得 %d", i+1, want, versions[i])
		}
	}
	if len(names) != 3 {
		t.Errorf("3 个备份文件名必须各不相同（否则互相覆盖），实得 %d 个：%v", len(names), names)
	}
}

// TestTrackEdit_BackupHoldsPreEditContent —— ★ 备份的是**编辑前**的内容。
//
// 对账 TS：`trackEdit` 在编辑**后**调，读的是**磁盘当前内容**
// ——即「这次编辑之后的状态」正是下一次 rewind 的目标。
//
// **注意与 recovery.Stack 的差异**：Stack 是**写前**捕获（防丢旧内容）；
// FileHistory 是**写后**读取（它是「历史的每一层」）。
func TestTrackEdit_BackupHoldsPostEditContent(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "AFTER-EDIT")
	if err := h.TrackEdit(abs, "call_1"); err != nil {
		t.Fatal(err)
	}

	b := h.Snapshots()[0].Files[abs]
	got, ok := readFile(t, filepath.Join(h.BackupDir(), b.FileName))
	if !ok {
		t.Fatalf("备份文件不存在：%s", b.FileName)
	}
	if got != "AFTER-EDIT" {
		t.Errorf("备份应存编辑后的内容（TS trackEdit 语义），实得 %q", got)
	}
}

// TestTrackEdit_FileNameMatchesTSAlgorithm —— 备份文件名 = sha256(abs)[:16]@vN。
func TestTrackEdit_FileNameMatchesTSAlgorithm(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "x")
	_ = h.TrackEdit(abs, "call_1")

	got := h.Snapshots()[0].Files[abs].FileName
	want := backupName(abs, 1)
	if got != want {
		t.Errorf("备份文件名不符\n want %q\n  got %q", want, got)
	}
}

// ── Rewind：三分支条件矩阵 ───────────────────────────────────────

// TestRewind_RestoresBackupContent —— 分支 3：正常备份 → 写回。
func TestRewind_RestoresBackupContent(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "GOOD")
	_ = h.TrackEdit(abs, "call_1")

	// 之后又改坏
	writeFile(t, root, "a.txt", "BROKEN")

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatalf("rewind 失败：%v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("应改动 1 个文件，实得 %d", len(changed))
	}
	if got, _ := readFile(t, abs); got != "GOOD" {
		t.Errorf("应恢复为 GOOD，实得 %q", got)
	}
}

// TestRewind_UnlinksFileThatDidNotExist —— 分支 2：`FileName==""` → unlink。
//
// 对账 TS：`backupFileName === null`（trackEdit 时文件不存在）→ rewind unlink。
func TestRewind_UnlinksFileThatDidNotExist(t *testing.T) {
	h, root := newTestHistory(t)
	// 先 track 一个**不存在**的文件（模拟「该次编辑把它新建出来」）
	abs := filepath.Join(root, "new.txt")
	if err := h.TrackEdit(abs, "call_1"); err != nil {
		t.Fatal(err)
	}
	// 编辑后它存在了
	writeFile(t, root, "new.txt", "created")

	b := h.Snapshots()[0].Files[abs]
	if b.FileName != "" {
		t.Fatalf("不存在文件的备份名应为空，实得 %q", b.FileName)
	}
	if b.Unreadable {
		t.Fatal("文件不存在不该标 Unreadable（那是「读失败」的语义）")
	}

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("应报告 1 个改动，实得 %d", len(changed))
	}
	if _, exists := readFile(t, abs); exists {
		t.Error("rewind 应把「当时不存在」的文件删掉")
	}
}

// TestRewind_SkipsUnreadableInsteadOfDeleting —— ★★ 本刀最重要的断言。
//
// 对账 TS 注释逐字：
//
//	旧内容存在但备份读取失败（AV/EDR 锁、EBUSY…）。rewind 必须跳过该文件：
//	此时「没有备份」≠「文件当时不存在」，null-only 语义会把 undo 变成**删除**。
//
// **判别力**：若实现把「备份读失败」与「当时不存在」都当 unlink
// → 本用例必红（文件被误删 = 数据丢失）。
func TestRewind_SkipsUnreadableInsteadOfDeleting(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "locked.txt", "IMPORTANT")

	// 构造「备份读失败」：让备份路径指向一个**目录**（readFile 会 EISDIR）
	if err := h.TrackEdit(abs, "call_1"); err != nil {
		t.Fatal(err)
	}
	b := h.Snapshots()[0].Files[abs]
	backupPath := filepath.Join(h.BackupDir(), b.FileName)
	if err := os.Remove(backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupPath, 0o755); err != nil { // 目录占位 → 读必失败
		t.Fatal(err)
	}
	// 把该条目标成 Unreadable（等价于 trackEdit 时读失败）
	h.MarkUnreadableForTest("call_1", abs)

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range changed {
		if f == abs {
			t.Error("Unreadable 的文件不该被报告为「已改动」")
		}
	}
	if got, exists := readFile(t, abs); !exists || got != "IMPORTANT" {
		t.Errorf("★ Unreadable 必须**跳过**（文件原样保留），实得 exists=%v content=%q", exists, got)
	}
}

// TestRewind_RecreatesDeletedFile —— 分支 3 的边界：文件已被删 → 重建。
func TestRewind_RecreatesDeletedFile(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "sub/deep/a.txt", "ORIGINAL")
	_ = h.TrackEdit(abs, "call_1")

	if err := os.RemoveAll(filepath.Dir(abs)); err != nil { // 连父目录一起删
		t.Fatal(err)
	}

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("应报告 1 个，实得 %d", len(changed))
	}
	got, exists := readFile(t, abs)
	if !exists {
		t.Fatal("应重建被删文件（含父目录）")
	}
	if got != "ORIGINAL" {
		t.Errorf("内容应还原，实得 %q", got)
	}
}

// TestRewind_UnknownSnapshotErrors —— 快照不存在 → 报错（不静默）。
//
// 对账 TS：`throw new Error(\`Snapshot for ${targetMessageId} not found\`)`
func TestRewind_UnknownSnapshotErrors(t *testing.T) {
	h, _ := newTestHistory(t)
	if _, err := h.Rewind("nope"); err == nil {
		t.Error("未知快照应返回错误（不静默返回空）")
	}
}

// TestRewind_OnlyTouchesFilesInTargetSnapshot —— 只回滚该快照里的文件。
//
// 对账 TS：`if (targetBackup === undefined) continue`
func TestRewind_OnlyTouchesFilesInTargetSnapshot(t *testing.T) {
	h, root := newTestHistory(t)
	a := writeFile(t, root, "a.txt", "A1")
	b := writeFile(t, root, "b.txt", "B1")

	_ = h.TrackEdit(a, "call_1") // 只有 a 在 call_1
	_ = h.TrackEdit(b, "call_2")

	writeFile(t, root, "a.txt", "A2")
	writeFile(t, root, "b.txt", "B2")

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != a {
		t.Fatalf("只应回滚 call_1 里的 a，实得 %v", changed)
	}
	if got, _ := readFile(t, b); got != "B2" {
		t.Errorf("b 不在 call_1 里，不该被动，实得 %q", got)
	}
}

// ── GetDiffStats ─────────────────────────────────────────────────

// TestGetDiffStats_CountsChangedFilesAndLines —— 统计变更文件与增删行。
func TestGetDiffStats_CountsChangedFilesAndLines(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "line1\nline2\nline3\n")
	_ = h.TrackEdit(abs, "call_1")

	writeFile(t, root, "a.txt", "line1\nCHANGED\nline3\nline4\n")

	stats, ok := h.GetDiffStats("call_1")
	if !ok {
		t.Fatal("应能算出统计")
	}
	if len(stats.FilesChanged) != 1 || stats.FilesChanged[0] != abs {
		t.Errorf("应报告 1 个变更文件，实得 %v", stats.FilesChanged)
	}
	if stats.Insertions == 0 || stats.Deletions == 0 {
		t.Errorf("增删行应都非零，实得 +%d/-%d", stats.Insertions, stats.Deletions)
	}
}

// TestGetDiffStats_SkipsUnchangedFiles —— 内容相同 → 不计入。
//
// 对账 TS：`if (oldContent === newContent) continue`
func TestGetDiffStats_SkipsUnchangedFiles(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "same")
	_ = h.TrackEdit(abs, "call_1")
	// 不动它

	stats, ok := h.GetDiffStats("call_1")
	if !ok {
		t.Fatal("应能算出统计")
	}
	if len(stats.FilesChanged) != 0 {
		t.Errorf("内容未变不该计入，实得 %v", stats.FilesChanged)
	}
}

// TestGetDiffStats_SkipsUnreadable —— Unreadable → 跳过（不可比）。
//
// 对账 TS：`if (targetBackup.unreadable) continue // 无备份可比对，
// 避免把「撤不了」误报成整文件删除`
func TestGetDiffStats_SkipsUnreadable(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "x")
	_ = h.TrackEdit(abs, "call_1")
	h.MarkUnreadableForTest("call_1", abs)
	writeFile(t, root, "a.txt", "y")

	stats, ok := h.GetDiffStats("call_1")
	if !ok {
		t.Fatal("应能算出统计")
	}
	if len(stats.FilesChanged) != 0 {
		t.Errorf("Unreadable 应跳过（否则会把「撤不了」误报成整文件删除），实得 %v", stats.FilesChanged)
	}
}

// TestGetDiffStats_UnknownSnapshot —— 未知快照 → (nil, false)。
func TestGetDiffStats_UnknownSnapshot(t *testing.T) {
	h, _ := newTestHistory(t)
	if _, ok := h.GetDiffStats("nope"); ok {
		t.Error("未知快照应返回 false")
	}
}

// ── LatestSnapshotID / HasSnapshot ───────────────────────────────

func TestLatestSnapshotID(t *testing.T) {
	h, root := newTestHistory(t)
	if _, ok := h.LatestSnapshotID(); ok {
		t.Error("无快照时应返回 false")
	}
	abs := writeFile(t, root, "a.txt", "x")
	_ = h.TrackEdit(abs, "call_1")
	_ = h.TrackEdit(abs, "call_2")

	id, ok := h.LatestSnapshotID()
	if !ok || id != "call_2" {
		t.Errorf("应返回最新 id call_2，实得 %q ok=%v", id, ok)
	}
	if !h.HasSnapshot("call_1") || h.HasSnapshot("nope") {
		t.Error("HasSnapshot 判定不符")
	}
}

// ── 上限淘汰 ─────────────────────────────────────────────────────

// TestEviction_CapsSnapshotsAndRemovesBackupFiles —— ★ 超上限淘汰且不留孤儿。
//
// 对账 TS：超过 MAX_SNAPSHOTS 时 `this.snapshots.slice(0, len - MAX)` 淘汰，
// 并 `unlink` 其备份文件。
//
// 判别力：若淘汰但不删备份文件 → 本用例的孤儿检查必红。
func TestEviction_CapsSnapshotsAndRemovesBackupFiles(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "x")

	total := MaxSnapshots + 5
	for i := 0; i < total; i++ {
		writeFile(t, root, "a.txt", "content-"+itoa(i))
		if err := h.TrackEdit(abs, "call_"+itoa(i)); err != nil {
			t.Fatal(err)
		}
	}

	snaps := h.Snapshots()
	if len(snaps) != MaxSnapshots {
		t.Errorf("应保留 %d 个快照，实得 %d", MaxSnapshots, len(snaps))
	}
	// 最旧的 5 个已被淘汰
	if snaps[0].MessageID != "call_5" {
		t.Errorf("首个快照应是 call_5（前 5 个被淘汰），实得 %q", snaps[0].MessageID)
	}

	// **孤儿检查**：备份目录里的文件数应等于存活快照引用的文件数
	referenced := map[string]bool{}
	for _, s := range snaps {
		for _, b := range s.Files {
			if b.FileName != "" {
				referenced[b.FileName] = true
			}
		}
	}
	entries, err := os.ReadDir(h.BackupDir())
	if err != nil {
		t.Fatal(err)
	}
	onDisk := 0
	for _, e := range entries {
		if !e.IsDir() {
			onDisk++
		}
	}
	if onDisk != len(referenced) {
		t.Errorf("★ 淘汰必须删掉被淘汰快照的备份文件（否则留孤儿）：盘上 %d 个，引用 %d 个",
			onDisk, len(referenced))
	}
}

// ── 并发 ─────────────────────────────────────────────────────────

// TestConcurrentTrackAndRead —— 并发不得数据竞争（配合 -race）。
func TestConcurrentTrackAndRead(t *testing.T) {
	h, root := newTestHistory(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			abs := writeFile(t, root, "f"+itoa(i%5)+".txt", "c"+itoa(i))
			_ = h.TrackEdit(abs, "call_"+itoa(i))
		}
	}()
	for i := 0; i < 50; i++ {
		_, _ = h.LatestSnapshotID()
		_, _ = h.GetDiffStats("call_0")
	}
	<-done
}

// TestSnapshotAccessorsReturnCopies —— 取快照不得暴露内部可变状态。
func TestSnapshotAccessorsReturnCopies(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "x")
	_ = h.TrackEdit(abs, "call_1")

	snaps := h.Snapshots()
	if len(snaps) == 0 {
		t.Fatal("应有快照")
	}
	snaps[0].MessageID = "MUTATED"
	if h.Snapshots()[0].MessageID == "MUTATED" {
		t.Error("Snapshots() 应返回副本（否则调用方可篡改内部状态）")
	}
}

// TestBackupPathUnderDedicatedDir —— 备份落在 file-history 而非 recovery 的 backups 目录。
//
// **为什么单列一条**：两个包的目录若撞在一起，recovery 的淘汰（按时间戳目录）
// 会误删 filehistory 的备份。
func TestBackupPathUnderDedicatedDir(t *testing.T) {
	h, root := newTestHistory(t)
	abs := writeFile(t, root, "a.txt", "x")
	_ = h.TrackEdit(abs, "call_1")

	p := filepath.Join(h.BackupDir(), h.Snapshots()[0].Files[abs].FileName)
	if !strings.Contains(filepath.ToSlash(p), "/file-history/") {
		t.Errorf("备份路径应含 /file-history/（与 recovery 的 /backups/ 分开），实得 %s", p)
	}
}
