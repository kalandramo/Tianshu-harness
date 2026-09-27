package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditFile_DryRunDoesNotWrite —— ★★ 本组测试的核心。
//
// # 这是一个真实缺陷的回归测试
//
// `edit_file` 的 schema 声明了 `dry_run`，但 Execute **从不读取它**——
// 故 `dry_run: true` 时**文件直接落盘**：模型以为在预览，实际已改。
// 这比「参数无效」更坏：**静默地做了不该做的事**。
//
// # 判别力
//
// 去掉 Execute 里的 `if boolArg(p.Input, "dry_run")` 早退 → 本用例立即红
// （文件内容会变成 MODIFIED）。
func TestEditFile_DryRunDoesNotWrite(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.txt")
	const original = "l1\nl2\nl3\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(Options{Cwd: cwd})
	res, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": target, "old_string": "l2", "new_string": "L2-CHANGED",
			"dry_run": true,
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("dry_run 预览失败：err=%v %s", err, res.Content)
	}

	// ★ ① 磁盘内容必须**原封不动**
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("★ dry_run 不该写盘！\n 期望 %q\n 实得 %q", original, string(got))
	}

	// ② 预览文案（对账 TS `预览（dry_run）<path> — 未写入任何更改：`）
	if !strings.Contains(res.Content, "预览（dry_run）") {
		t.Errorf("应含 dry_run 预览头：%q", res.Content)
	}
	if !strings.Contains(res.Content, "未写入任何更改") {
		t.Errorf("应明确声明未写入：%q", res.Content)
	}

	// ③ 应含 diff（体现「应用后会怎样」）
	if !strings.Contains(res.Content, "L2-CHANGED") {
		t.Errorf("预览应展示应用后的内容：%q", res.Content)
	}
}

// TestEditFile_DryRunNoBackupSideEffect —— dry_run 也不该产生备份副作用。
//
// 早退位置必须在 `TrackFileChange` **之前**——否则虽然没写文件，却留下
// 一份「幽灵备份」，undo 时会恢复出一个从未发生过的状态。
func TestEditFile_DryRunNoBackupSideEffect(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(Options{Cwd: cwd})
	_, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": target, "old_string": "x", "new_string": "y", "dry_run": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 备份目录不该有本次编辑的记录
	backupRoot := filepath.Join(cwd, ".rivet", "backups")
	if entries, e := os.ReadDir(backupRoot); e == nil && len(entries) > 0 {
		t.Errorf("dry_run 不该产生备份，但 %s 下有 %d 项", backupRoot, len(entries))
	}
}

// TestEditFile_DryRunSyntaxWarning —— ★ dry_run 的主要价值：**预测语法错误**。
//
// 对账 TS：`若应用将出现语法错误：${check.fatal}`。
// 常规路径是「写盘后检查、失败则回滚」；dry_run 不能这么做，
// 故必须在内存内容上**预测**。
func TestEditFile_DryRunSyntaxWarning(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.go")
	if err := os.WriteFile(target, []byte("package a\n\nfunc f() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(Options{Cwd: cwd})
	// 故意制造语法破坏（删掉右花括号）
	res, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": target, "old_string": "func f() {}", "new_string": "func f() {",
			"dry_run": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 语法检查若可用，应给出预警（本机无 go 语法检查器时跳过此断言）
	if !strings.Contains(res.Content, "若应用将出现语法错误") {
		t.Skipf("本机未触发语法检查（可能无对应检查器）——预览正文：%q", res.Content)
	}
	// 即便报语法错误，也**不得写盘**
	got, _ := os.ReadFile(target)
	if string(got) != "package a\n\nfunc f() {}\n" {
		t.Errorf("★ 语法错误时 dry_run 更不该写盘，实得 %q", string(got))
	}
}

// TestEditFile_NoDryRunStillWrites —— 反向对照：不传 dry_run 时**照常写盘**。
//
// 防止「为了修 dry_run 而把常规路径也变成只读」。
func TestEditFile_NoDryRunStillWrites(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(Options{Cwd: cwd})
	res, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{"file_path": target, "old_string": "old", "new_string": "new"},
	})
	if err != nil || res.IsError {
		t.Fatalf("常规编辑失败：%v %s", err, res.Content)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Errorf("常规路径应写盘，实得 %q", string(got))
	}
	if strings.Contains(res.Content, "预览（dry_run）") {
		t.Errorf("常规路径不该出现 dry_run 文案：%q", res.Content)
	}
}

// TestEditFile_DryRunNoChangePlaceholder —— 无文本变更时的占位文案。
//
// 对账 TS：`diff || '（无文本变更）'`。
func TestEditFile_DryRunNoChangePlaceholder(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(Options{Cwd: cwd})
	// 替换成相同内容 → 无变化
	res, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": target, "old_string": "same", "new_string": "same", "dry_run": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "（无文本变更）") {
		t.Errorf("无变化时应给占位文案：%q", res.Content)
	}
}
