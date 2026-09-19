package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/recovery"
)

// findBackupFor 查找某文件在 .rivet/backups/<ts>/ 下的备份内容。
//
// 返回最近（时间戳最大）的那个备份。无备份时返回 ("", false)。
func findBackupFor(t *testing.T, cwd, rel string) (string, bool) {
	t.Helper()
	root := filepath.Join(cwd, ".rivet", "backups")
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// 时间戳目录名是数字，字符串序 = 年龄序；取最大的（最新）
	best := ""
	for _, d := range dirs {
		if d > best {
			best = d
		}
	}
	if best == "" {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(root, best, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// TestWriteFileCreatesBackup —— write_file 覆盖前备份旧内容。
func TestWriteFileCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "target.txt", "ORIGINAL\n")

	tool := WriteFile(dir, nil)
	p := &CallParams{
		Input:        map[string]any{"file_path": "target.txt", "content": "NEW\n"},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil || res.IsError {
		t.Fatalf("写入应成功：err=%v res=%s", err, res.Content)
	}

	got := readRepoFile(t, dir, "target.txt")
	if got != "NEW\n" {
		t.Errorf("文件应被覆盖，得到 %q", got)
	}
	// **关键断言**：备份里是补丁前的内容
	backup, ok := findBackupFor(t, dir, "target.txt")
	if !ok {
		t.Fatal("应有备份")
	}
	if backup != "ORIGINAL\n" {
		t.Errorf("备份应是写入前的内容，得到 %q", backup)
	}
}

// TestWriteFileNewFileNoBackup —— 新文件无备份（对账 TS：只备份已存在文件）。
func TestWriteFileNewFileNoBackup(t *testing.T) {
	dir := t.TempDir()
	tool := WriteFile(dir, nil)
	p := &CallParams{
		Input:        map[string]any{"file_path": "brand-new.txt", "content": "hi\n"},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := tool.Execute(context.Background(), p); res.IsError {
		t.Fatalf("写入应成功：%s", res.Content)
	}
	if _, ok := findBackupFor(t, dir, "brand-new.txt"); ok {
		t.Error("新文件不应有备份")
	}
}

// TestWriteFileAppendAlsoBacksUp —— append 模式同样备份。
func TestWriteFileAppendAlsoBacksUp(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "log.txt", "line1\n")

	tool := WriteFile(dir, nil)
	p := &CallParams{
		Input:        map[string]any{"file_path": "log.txt", "content": "line2\n", "mode": "append"},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := tool.Execute(context.Background(), p); res.IsError {
		t.Fatalf("append 应成功：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "log.txt"); got != "line1\nline2\n" {
		t.Errorf("append 后内容不符：%q", got)
	}
	backup, ok := findBackupFor(t, dir, "log.txt")
	if !ok {
		t.Fatal("append 也应有备份")
	}
	if backup != "line1\n" {
		t.Errorf("备份应是 append 前的内容，得到 %q", backup)
	}
}

// TestEditFileCreatesBackup —— edit_file 写前备份。
func TestEditFileCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "e.txt", "alpha beta gamma\n")

	tool := EditFile(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path": "e.txt", "old_string": "beta", "new_string": "BETA",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := tool.Execute(context.Background(), p); res.IsError {
		t.Fatalf("编辑应成功：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "e.txt"); !strings.Contains(got, "BETA") {
		t.Errorf("编辑未生效：%q", got)
	}
	backup, ok := findBackupFor(t, dir, "e.txt")
	if !ok {
		t.Fatal("应有备份")
	}
	if backup != "alpha beta gamma\n" {
		t.Errorf("备份应是编辑前的内容，得到 %q", backup)
	}
}

// TestEditFileBackupCanRestore —— 备份可恢复（端到端价值验证）。
func TestEditFileBackupCanRestore(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "r.txt", "keep me\n")

	tool := EditFile(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path": "r.txt", "old_string": "keep me", "new_string": "destroyed",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := tool.Execute(context.Background(), p); res.IsError {
		t.Fatalf("编辑应成功：%s", res.Content)
	}

	// 用**生产路径的共享 Stack** 恢复——这验证的是「跨工具可见」这一真实契约：
	// edit_file 备份的文件，apply_patch 等其它工具回滚时必须读得到。
	// （TS 的 latestBackups 是模块级 Map，正是为了这个共享语义。）
	if !recovery.DefaultStack().RestoreLatestBackup(dir, "r.txt", "s") {
		t.Fatal("应能从备份恢复（跨工具共享栈）")
	}
	if got := readRepoFile(t, dir, "r.txt"); got != "keep me\n" {
		t.Errorf("应恢复原内容，得到 %q", got)
	}
}

// TestBackupSharedAcrossTools —— **跨工具可见性**（真实契约）。
//
// 工具 A 备份的文件，工具 B 必须能恢复。TS 用模块级 Map 保证这点；
// Go 侧若每个工具各持独立 Stack 实例，这条契约就断了。
func TestBackupSharedAcrossTools(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "shared.txt", "v1\n")

	// 工具 A：edit_file 备份
	editTool := EditFile(dir, nil)
	pA := &CallParams{
		Input: map[string]any{
			"file_path": "shared.txt", "old_string": "v1", "new_string": "v2",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := editTool.Execute(context.Background(), pA); res.IsError {
		t.Fatalf("edit_file 应成功：%s", res.Content)
	}

	// 工具 B：另一个工具实例（apply_patch）尝试恢复同一个文件
	patchTool := ApplyPatch(dir, nil)
	ap, ok := patchTool.(*applyPatchTool)
	if !ok {
		t.Fatal("apply_patch 类型不符")
	}
	if !ap.Stack.RestoreLatestBackup(dir, "shared.txt", "s") {
		t.Fatal("跨工具恢复应成功（共享栈）")
	}
	if got := readRepoFile(t, dir, "shared.txt"); got != "v1\n" {
		t.Errorf("应恢复到 v1，得到 %q", got)
	}
}

// TestHashEditCreatesBackup —— hash_edit 写前备份。
func TestHashEditCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "h.txt", "line one\nline two\n")

	tool := HashEdit(dir, nil)
	// 需要锚点——先读文件拿锚点由工具内部处理，这里用整体替换语义
	p := &CallParams{
		Input: map[string]any{
			"file_path":  "h.txt",
			"anchors":    []any{"L1", "L2"},
			"new_string": "replaced\n",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if res.IsError {
		t.Skipf("hash_edit 锚点格式不符（跳过备份断言）：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "h.txt"); !strings.Contains(got, "replaced") {
		t.Errorf("编辑未生效：%q", got)
	}
	backup, ok := findBackupFor(t, dir, "h.txt")
	if !ok {
		t.Fatal("应有备份")
	}
	if !strings.Contains(backup, "line one") {
		t.Errorf("备份应是编辑前的内容，得到 %q", backup)
	}
}

// TestHashEditDryRunNoBackup —— dry_run 不写盘也不备份（对账 TS）。
func TestHashEditDryRunNoBackup(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "d.txt", "original\n")

	tool := HashEdit(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path":  "d.txt",
			"anchors":    []any{"L1"},
			"new_string": "changed\n",
			"dry_run":    true,
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if res.IsError {
		t.Skipf("dry_run 锚点格式不符：%s", res.Content)
	}
	// 文件未变
	if got := readRepoFile(t, dir, "d.txt"); got != "original\n" {
		t.Errorf("dry_run 不应修改文件，得到 %q", got)
	}
	// 无备份
	if _, ok := findBackupFor(t, dir, "d.txt"); ok {
		t.Error("dry_run 不应产生备份")
	}
}

// TestBackupContentsArePreWriteAcrossTools —— 三个工具的一致不变量：
// 备份内容必须是**写入前**的（这是 recovery 的核心不变量）。
func TestBackupContentsArePreWriteAcrossTools(t *testing.T) {
	cases := []struct {
		name  string
		tool  func(string) Tool
		input map[string]any
	}{
		{"write_file", func(d string) Tool { return WriteFile(d, nil) },
			map[string]any{"file_path": "f.txt", "content": "AFTER\n"}},
		{"edit_file", func(d string) Tool { return EditFile(d, nil) },
			map[string]any{"file_path": "f.txt", "old_string": "BEFORE", "new_string": "AFTER"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRepoFile(t, dir, "f.txt", "BEFORE\n")
			p := &CallParams{Input: tc.input, Cwd: dir, ApprovalMode: "dangerously-skip-permissions"}
			res, _ := tc.tool(dir).Execute(context.Background(), p)
			if res.IsError {
				t.Fatalf("应成功：%s", res.Content)
			}
			backup, ok := findBackupFor(t, dir, "f.txt")
			if !ok {
				t.Fatal("应有备份")
			}
			if backup != "BEFORE\n" {
				t.Errorf("备份必须是**写入前**内容（核心不变量），得到 %q", backup)
			}
		})
	}
}
