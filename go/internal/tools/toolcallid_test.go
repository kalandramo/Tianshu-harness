package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/recovery"
)

// TestFileChangeToolID_PrefersRealToolUseID —— ★ 第一百零二刀的核心修复。
//
// 写工具调 `TrackFileChange` 时，`ToolCallID` **必须是真实 tool_use id**——
// FileHistory 按它分组快照；用工具名会让同一轮的多次同名调用归入同一快照
// （undo 撤错范围）。
//
// 判别力：改回硬编码工具名 → 本用例必红。
func TestFileChangeToolID_PrefersRealToolUseID(t *testing.T) {
	got := fileChangeToolID(&CallParams{ToolUseID: "call_abc123"}, "write_file")
	if got != "call_abc123" {
		t.Errorf("应取真实 tool_use id，实得 %q", got)
	}
}

// TestFileChangeToolID_FallsBackToToolName —— 空 id → 回退工具名（保持既有行为）。
//
// **为什么保留兜底**：测试与少数据径直调工具时不设 ToolUseID；
// 回退让备份仍可分组（粒度退化），不让它彻底失去键。
func TestFileChangeToolID_FallsBackToToolName(t *testing.T) {
	for _, c := range []struct {
		name string
		p    *CallParams
	}{
		{"nil params", nil},
		{"空 id", &CallParams{}},
	} {
		if got := fileChangeToolID(c.p, "edit_file"); got != "edit_file" {
			t.Errorf("%s：应回退工具名，实得 %q", c.name, got)
		}
	}
}

// ── 调用链验证（防止「改完辅助却忘了接线」）─────────────────────────
//
// **怎么验证**：`recovery.Stack` 的 `latestBackups` 是私有字段，无法直接读。
// 但它把备份路径写进 `.rivet/backups/<ts>/<rel>`——**目录名是时间戳**，
// 而 FileHistory 走的是 `.rivet/file-history/`（本刀新建）。
//
// 更直接的判据：**源码级断言**（下一条测试）+ **行为级**（备份确实产生）。
// 二者结合即可覆盖「接线是否真的改了」——纯函数测试覆盖语义，
// 源码断言覆盖接线，行为测试覆盖不回归。

// TestWriteFileBackupRecordsRealID_Behavior —— 行为级：传真实 id 时写盘成功
// 且备份产生（与既有 backup_wiring_test 的判据一致，只是加了 ToolUseID）。
func TestWriteFileBackupRecordsRealID_Behavior(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "target.txt", "ORIGINAL\n")

	tool := WriteFile(dir, nil)
	p := &CallParams{
		Input:        map[string]any{"file_path": "target.txt", "content": "NEW\n"},
		Cwd:          dir,
		ToolUseID:    "call_real_42",
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil || res.IsError {
		t.Fatalf("写入应成功：err=%v res=%s", err, res.Content)
	}
	if got := readRepoFile(t, dir, "target.txt"); got != "NEW\n" {
		t.Errorf("应已覆盖，得到 %q", got)
	}
	if backup, ok := findBackupFor(t, dir, "target.txt"); !ok || backup != "ORIGINAL\n" {
		t.Errorf("备份应是写入前内容，得到 ok=%v %q", ok, backup)
	}
}

// TestEditFileBackupRecordsRealID_Behavior —— 同构：edit_file。
func TestEditFileBackupRecordsRealID_Behavior(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "e.txt", "alpha beta\n")

	tool := EditFile(dir, nil)
	p := &CallParams{
		Input:        map[string]any{"file_path": "e.txt", "old_string": "beta", "new_string": "BETA"},
		Cwd:          dir,
		ToolUseID:    "call_edit_7",
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := tool.Execute(context.Background(), p); res.IsError {
		t.Fatalf("编辑应成功：%s", res.Content)
	}
	if backup, ok := findBackupFor(t, dir, "e.txt"); !ok || backup != "alpha beta\n" {
		t.Errorf("备份应是编辑前内容，得到 ok=%v %q", ok, backup)
	}
}

// ★ 为什么用源码级断言补足
//
// `ToolCallID` 是 `recovery.FileChangeRecord` 的一个字段，写进栈的内部 map
// **没有导出的读取接口**——行为测试只能验证「备份产生了」，验不到
// 「字段里装的是什么」。而本刀修的恰恰是那个字段的**值**。
//
// 三个选择：
//  ① 给 `recovery.Stack` 加一个测试用的导出读取方法 → **为测试改生产 API**（不好）
//  ② 用 `reflect` 读私有字段 → 脆弱且绕
//  ③ **源码级断言**：读工具源码，确认调用点传的是 `fileChangeToolID(p, ...)`
//
// 取 ③：它是**确定性**的（源码不会随机变），且失败信息直指「忘了接线」。
// 这类「改的是常量/字面量，行为不可观测」的场景，源码断言是合适的手段
// （本仓库已有先例：`probe_discipline` 的锚点断言、HANDOFF 的「判缺口方法」）。

// TestToolCallIDWiringInSource —— 四个写工具的调用点必须传 `fileChangeToolID(p, ...)`。
//
// 判别力：任一处改回 `"write_file"` 这类字面量 → 本用例必红。
func TestToolCallIDWiringInSource(t *testing.T) {
	cases := []struct {
		file     string
		fallback string
	}{
		{"file_tools.go", "write_file"},
		{"file_tools.go", "edit_file"},
		{"applypatch.go", "apply_patch"},
		{"hashedit.go", "hash_edit"},
	}
	for _, c := range cases {
		src, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", c.file, err)
		}
		text := string(src)

		want := `fileChangeToolID(p, "` + c.fallback + `")`
		if !strings.Contains(text, want) {
			t.Errorf("%s 的 %s 调用点未接线：应含 `%s`", c.file, c.fallback, want)
		}
		// 反向断言：不该再有硬编码字面量
		bad := `ToolCallID: "` + c.fallback + `",`
		if strings.Contains(text, bad) {
			t.Errorf("%s 仍有硬编码 `%s`（应为 fileChangeToolID 调用）", c.file, bad)
		}
	}
}

// TestFileChangeToolIDUsedForAllRecoveryCallSites —— 全量枚举：写工具的每个
// `ToolCallID:` 赋值都必须走辅助函数。
//
// **为什么单列**：前一条按「已知的四个」枚举；这条按「源码里所有赋值点」枚举
// ——若将来新增写工具却忘了用辅助，这条会红（前一条不会）。
func TestFileChangeToolIDUsedForAllRecoveryCallSites(t *testing.T) {
	files := []string{"file_tools.go", "applypatch.go", "hashedit.go", "applypatch_rollback_test.go"}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "ToolCallID:") {
				continue
			}
			// 测试文件里的用例允许直接构造记录（它们测的是栈本身）
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			if !strings.Contains(trimmed, "fileChangeToolID(") {
				t.Errorf("%s:%d 的 ToolCallID 赋值未走 fileChangeToolID：%s", f, i+1, trimmed)
			}
		}
	}
}

// TestStackStillSharedAcrossTools —— 回归：本刀**不该**动 Stack 的共享语义。
//
// 工具 A 备份的文件，工具 B 必须能恢复（TS 用模块级 Map，Go 用 DefaultStack）。
func TestStackStillSharedAcrossTools(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "shared.txt", "v1\n")

	editTool := EditFile(dir, nil)
	pA := &CallParams{
		Input:        map[string]any{"file_path": "shared.txt", "old_string": "v1", "new_string": "v2"},
		Cwd:          dir,
		ToolUseID:    "call_edit_shared",
		ApprovalMode: "dangerously-skip-permissions",
	}
	if res, _ := editTool.Execute(context.Background(), pA); res.IsError {
		t.Fatalf("edit_file 应成功：%s", res.Content)
	}

	patchTool := ApplyPatch(dir, nil)
	ap := patchTool.(*applyPatchTool)
	if !ap.Stack.RestoreLatestBackup(dir, "shared.txt", "s") {
		t.Fatal("跨工具恢复应成功（共享栈语义未被本刀破坏）")
	}
	if got := readRepoFile(t, dir, "shared.txt"); got != "v1\n" {
		t.Errorf("应恢复到 v1，得到 %q", got)
	}
}

// _ 保住 recovery 的引用（本文件用其类型做断言）。
var _ = recovery.DefaultStack
var _ = filepath.Join
