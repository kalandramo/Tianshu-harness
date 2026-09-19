package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/recovery"
)

// gitRepoForTest 建一个临时 git 仓库（apply_patch 需要有效仓库才能跑）。
func gitRepoForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
	return dir
}

func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCommitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
}

func readRepoFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// applyPatchCall 构造一次 apply_patch 调用。
func applyPatchCall(dir, diff string) (*CallParams, Tool) {
	tool := ApplyPatch(dir, nil)
	return &CallParams{
		Input:        map[string]any{"diff": diff},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}, tool
}

// TestApplyPatchSuccessWritesFile —— 成功路径：补丁真落盘。
func TestApplyPatchSuccessWritesFile(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "a.txt", "line1\nline2\n")
	gitCommitAll(t, dir, "init")

	diff := `--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 line1
-line2
+line2-changed
`
	p, tool := applyPatchCall(dir, diff)
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if res.IsError {
		t.Fatalf("应成功，得到错误：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "a.txt"); !strings.Contains(got, "line2-changed") {
		t.Errorf("补丁未落盘：%q", got)
	}
}

// TestApplyPatchFailureRollsBack —— **核心**：补丁失败时工作树回滚到补丁前状态。
//
// 这是与 TS 最显著的行为差异的修复验证：TS 主动回滚，Go 侧原本可能留下
// 部分改动（冲突标记落盘、干净文件已套用）。
func TestApplyPatchFailureRollsBack(t *testing.T) {
	dir := gitRepoForTest(t)
	// 两个文件：a.txt 会被干净套用，b.txt 的上下文不匹配（制造冲突）
	writeRepoFile(t, dir, "a.txt", "alpha\nbeta\n")
	writeRepoFile(t, dir, "b.txt", "completely different content\n")
	gitCommitAll(t, dir, "init")

	beforeA := readRepoFile(t, dir, "a.txt")

	// 补丁：a.txt 能干净应用，b.txt 上下文对不上 → git apply 失败
	diff := `--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 alpha
-beta
+beta-changed
--- a/b.txt
+++ b/b.txt
@@ -1,3 +1,3 @@
 expected-line-1
 expected-line-2
-expected-line-3
+expected-line-3-changed
`
	p, tool := applyPatchCall(dir, diff)
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if !res.IsError {
		t.Fatal("补丁应失败（b.txt 上下文不匹配）")
	}
	// 文案应声明已回滚
	if !strings.Contains(res.Content, "已回滚到补丁前状态") {
		t.Errorf("失败文案应声明回滚，得到：%s", res.Content)
	}

	// **关键断言**：a.txt 必须恢复原样（不能留下半套用的改动）
	afterA := readRepoFile(t, dir, "a.txt")
	if afterA != beforeA {
		t.Errorf("a.txt 应回滚到补丁前状态：\n  补丁前=%q\n  失败后=%q", beforeA, afterA)
	}
	if strings.Contains(afterA, "beta-changed") {
		t.Error("a.txt 不应残留半套用的改动")
	}
}

// gitRun 在 dir 里跑 git 命令（测试辅助）。
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败：%v\n%s", args, err, out)
	}
	return string(out)
}

// TestApplyPatchFailureUnstagesUU —— **索引污染场景**：`--3way` 真走合并路径时
// 留下 UU（unmerged）条目与冲突标记，失败后必须被收回。
//
// 场景构造（探针实测确认）：
//  1. 提交 v1（a.txt 的 blob 进历史，3way 才找得到它）
//  2. 用 `git diff` 生成**带 index 行**的合法 patch，然后撤销改动
//  3. 提交 v2 把 a.txt 改成与 patch 冲突的内容
//  4. 应用 patch → git 报 "Applied patch to 'a.txt' with conflicts" + `UU a.txt`
//
// 这与「回退直接应用」路径不同——那条路径本就什么都不留下。
func TestApplyPatchFailureUnstagesUU(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "a.txt", "one\ntwo\n")
	writeRepoFile(t, dir, "b.txt", "x\ny\n")
	gitCommitAll(t, dir, "v1")

	// 生成合法 patch（含 index 行——3way 找 blob 的前提）
	writeRepoFile(t, dir, "a.txt", "one\ntwo\nTHREE\n")
	diff := gitRun(t, dir, "diff")
	gitRun(t, dir, "checkout", "--", "a.txt")

	// v2：a.txt 改成与 patch 冲突的内容
	writeRepoFile(t, dir, "a.txt", "one\nCONFLICT\n")
	writeRepoFile(t, dir, "b.txt", "x\nCHANGED\n")
	gitCommitAll(t, dir, "v2")

	// 前置断言：这个 patch 确实会走合并路径（不是回退直接应用）
	p, tool := applyPatchCall(dir, diff)
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if !res.IsError {
		t.Fatal("冲突补丁应失败")
	}
	if !strings.Contains(res.Content, "已回滚到补丁前状态") {
		t.Errorf("应声明回滚：%s", res.Content)
	}

	// **关键断言 1**：UU（unmerged）条目被收回
	unmerged := strings.TrimSpace(gitRun(t, dir, "ls-files", "-u"))
	if unmerged != "" {
		t.Errorf("unmerged 条目应被收回，仍有：\n%s", unmerged)
	}

	// **关键断言 2**：冲突标记被清除（工作树回滚）
	content := readRepoFile(t, dir, "a.txt")
	if strings.Contains(content, "<<<<<<<") || strings.Contains(content, ">>>>>>>") {
		t.Errorf("冲突标记应被回滚清除，得到：\n%s", content)
	}
	if content != "one\nCONFLICT\n" {
		t.Errorf("a.txt 应回到补丁前内容，得到 %q", content)
	}

	// **关键断言 3**：`git checkout -- <file>` 不再报 "path is unmerged"
	// （这是 TS 注释点明的实际危害）
	cmd := exec.Command("git", "checkout", "--", "a.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("git checkout -- a.txt 应可用（索引已解阻塞）：%v\n%s", err, out)
	}
}

// TestApplyPatchFailureNoBackupWithoutVerify —— verify 关闭时保持 legacy 行为。
func TestApplyPatchFailureNoBackupWithoutVerify(t *testing.T) {
	t.Setenv("RIVET_APPLY_PATCH_VERIFY", "0")

	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "a.txt", "x\ny\n")
	gitCommitAll(t, dir, "init")

	diff := `--- a/a.txt
+++ b/a.txt
@@ -1,9 +1,9 @@
 no-such-1
 no-such-2
 no-such-3
 no-such-4
 no-such-5
 no-such-6
 no-such-7
 no-such-8
-no-such-9
+no-such-9-changed
`
	p, tool := applyPatchCall(dir, diff)
	res, _ := tool.Execute(context.Background(), p)
	if !res.IsError {
		t.Fatal("补丁应失败")
	}
	// 文案**不应**声称回滚（targets 为空，无备份可恢复）
	if strings.Contains(res.Content, "已回滚") {
		t.Errorf("verify 关闭时不应声称回滚，得到：%s", res.Content)
	}
}

// TestApplyPatchCheckOnlyDoesNotBackup —— check_only 不写盘也不备份。
func TestApplyPatchCheckOnlyDoesNotBackup(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "a.txt", "p\nq\n")
	gitCommitAll(t, dir, "init")

	diff := `--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 p
-q
+q-changed
`
	p, tool := applyPatchCall(dir, diff)
	p.Input["check_only"] = true
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if res.IsError {
		t.Fatalf("check_only 应成功：%s", res.Content)
	}
	// 文件未变
	if got := readRepoFile(t, dir, "a.txt"); strings.Contains(got, "q-changed") {
		t.Error("check_only 不应修改文件")
	}
	// 无备份目录（checkOnly 跳过 targets 构造）
	backups := filepath.Join(dir, ".rivet", "backups")
	if entries, err := os.ReadDir(backups); err == nil && len(entries) > 0 {
		t.Errorf("check_only 不应产生备份，得到 %d 个目录", len(entries))
	}
}

// TestApplyPatchNewFileRolledBackByDeletion —— 补丁新建的文件失败时被删除。
func TestApplyPatchNewFileRolledBackByDeletion(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "exist.txt", "keep\n")
	gitCommitAll(t, dir, "init")

	// 新建 new.txt（能成功）+ 一个必然失败的 hunk
	diff := `--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+created-line-1
+created-line-2
--- a/exist.txt
+++ b/exist.txt
@@ -1,5 +1,5 @@
 wrong-1
 wrong-2
 wrong-3
 wrong-4
-wrong-5
+wrong-5-changed
`
	p, tool := applyPatchCall(dir, diff)
	res, _ := tool.Execute(context.Background(), p)
	if !res.IsError {
		t.Fatal("补丁应失败")
	}
	// 新建的文件应被删除（existedBefore=false → 回滚策略是删除）
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Error("补丁新建的文件失败后应被删除")
	}
}

// ── recovery.Stack 的行为测试 ──

// TestStackTrackAndRestore —— 备份后恢复内容。
func TestStackTrackAndRestore(t *testing.T) {
	cwd := t.TempDir()
	writeRepoFile(t, cwd, "f.txt", "original content\n")

	st := recovery.NewStack()
	if _, err := st.TrackFileChange(cwd, recovery.FileChangeRecord{FilePath: "f.txt", Action: "edit"}); err != nil {
		t.Fatalf("TrackFileChange 失败：%v", err)
	}
	// 覆写
	writeRepoFile(t, cwd, "f.txt", "NEW content\n")

	if !st.RestoreLatestBackup(cwd, "f.txt", "sess") {
		t.Fatal("RestoreLatestBackup 应返回 true")
	}
	if got := readRepoFile(t, cwd, "f.txt"); got != "original content\n" {
		t.Errorf("应恢复原内容，得到 %q", got)
	}
}

// TestStackTrackNonexistentFile —— 不存在的文件不产生备份（对账 TS）。
func TestStackTrackNonexistentFile(t *testing.T) {
	cwd := t.TempDir()
	st := recovery.NewStack()
	rec, err := st.TrackFileChange(cwd, recovery.FileChangeRecord{FilePath: "nope.txt", Action: "write"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if rec.BackupPath != "" {
		t.Errorf("不存在的文件不应有备份路径，得到 %q", rec.BackupPath)
	}
	if st.RestoreLatestBackup(cwd, "nope.txt", "") {
		t.Error("无备份时 RestoreLatestBackup 应返回 false")
	}
}

// TestStackRestoreRecordsJournal —— 恢复事件记入 journal。
func TestStackRestoreRecordsJournal(t *testing.T) {
	cwd := t.TempDir()
	writeRepoFile(t, cwd, "g.txt", "v1\n")

	st := recovery.NewStack()
	if _, err := st.TrackFileChange(cwd, recovery.FileChangeRecord{FilePath: "g.txt", Action: "edit"}); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, cwd, "g.txt", "v2\n")
	if !st.RestoreLatestBackup(cwd, "g.txt", "sess-x") {
		t.Fatal("恢复应成功")
	}

	entries := recovery.ReadUnacknowledged(cwd, "sess-x")
	if len(entries) != 1 {
		t.Fatalf("应记 1 条恢复事件，得到 %d", len(entries))
	}
	if entries[0].File != "g.txt" || entries[0].Action != "restore latest backup" {
		t.Errorf("事件内容不符：%+v", entries[0])
	}
}

// TestStackEvictOldBackups —— 超出上限的最旧备份被淘汰。
func TestStackEvictOldBackups(t *testing.T) {
	cwd := t.TempDir()
	backupsDir := filepath.Join(cwd, ".rivet", "backups")
	// 造 5 个时间戳目录 + 1 个外来目录（不应被淘汰）
	for _, name := range []string{"1000", "2000", "3000", "4000", "5000", "not-a-timestamp"} {
		if err := os.MkdirAll(filepath.Join(backupsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	st := recovery.NewStack()
	st.EvictOldBackups(cwd, 3)

	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, ",")
	// 应保留最年轻的 3 个时间戳 + 外来目录
	for _, want := range []string{"3000", "4000", "5000", "not-a-timestamp"} {
		if !strings.Contains(joined, want) {
			t.Errorf("应保留 %q，得到 %v", want, names)
		}
	}
	for _, gone := range []string{"1000", "2000"} {
		if strings.Contains(joined, gone) {
			t.Errorf("应淘汰 %q，得到 %v", gone, names)
		}
	}
}

// TestStackBinaryFileFallback —— 二进制文件走拷贝路径（内存备份不适用）。
func TestStackBinaryFileFallback(t *testing.T) {
	cwd := t.TempDir()
	bin := []byte{0x00, 0x01, 0x02, 0xFF, 0x00}
	if err := os.WriteFile(filepath.Join(cwd, "bin.dat"), bin, 0o644); err != nil {
		t.Fatal(err)
	}

	st := recovery.NewStack()
	rec, err := st.TrackFileChange(cwd, recovery.FileChangeRecord{FilePath: "bin.dat", Action: "edit"})
	if err != nil {
		t.Fatalf("TrackFileChange 失败：%v", err)
	}
	if rec.BackupPath == "" {
		t.Fatal("二进制文件也应有备份路径")
	}
	// 覆写后恢复应还原二进制内容
	if err := os.WriteFile(filepath.Join(cwd, "bin.dat"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !st.RestoreLatestBackup(cwd, "bin.dat", "") {
		t.Fatal("恢复应成功")
	}
	got, _ := os.ReadFile(filepath.Join(cwd, "bin.dat"))
	if string(got) != string(bin) {
		t.Errorf("二进制内容应还原：%v vs %v", got, bin)
	}
}

// TestStackEstimateLinesLost —— 行数丢失估算。
func TestStackEstimateLinesLost(t *testing.T) {
	cwd := t.TempDir()
	writeRepoFile(t, cwd, "e.txt", "1\n2\n3\n4\n5\n")

	st := recovery.NewStack()
	if _, err := st.TrackFileChange(cwd, recovery.FileChangeRecord{FilePath: "e.txt", Action: "edit"}); err != nil {
		t.Fatal(err)
	}
	// 覆写成 2 行 → 丢 3 行
	writeRepoFile(t, cwd, "e.txt", "1\n2\n")

	if got := st.EstimateLinesLost(cwd, "e.txt", ""); got != 3 {
		t.Errorf("应估算丢 3 行，得到 %d", got)
	}
}
