package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/filehistory"
)

// ── 测试 ─────────────────────────────────────────────────────────

// TestTrackEditPaths_ResolvesPerToolSchema —— 各工具的路径解析逐条对账。
func TestTrackEditPaths_ResolvesPerToolSchema(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
		want  []string
	}{
		{"write_file 用 file_path", "write_file", map[string]any{"file_path": "a.txt"}, []string{"a.txt"}},
		{"edit_file 用 file_path", "edit_file", map[string]any{"file_path": "b.txt"}, []string{"b.txt"}},
		{"hash_edit 用 file_path", "hash_edit", map[string]any{"file_path": "c.txt"}, []string{"c.txt"}},
		{"非写工具 → 空", "read_file", map[string]any{"file_path": "x.txt"}, nil},
		{"缺 file_path → 空", "write_file", map[string]any{}, nil},
		{"空 file_path → 空", "write_file", map[string]any{"file_path": ""}, nil},
		{
			// 对账 TS `extractPatchTargetPathsFromDiff`：**只看 `+++ ` 头**
			// （`line.startsWith('+++ ')`）——`--- ` 行不入集。
			"apply_patch 从 diff 的 +++ 头取（不看 --- ）",
			"apply_patch",
			map[string]any{"diff": "--- a/old.txt\n+++ b/new.txt\n@@ -1 +1 @@\n-x\n+y\n"},
			[]string{"new.txt"},
		},
		{"apply_patch 无 diff → 空", "apply_patch", map[string]any{}, nil},
	}
	for _, c := range cases {
		got := trackEditPaths(c.tool, c.input)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s：应 %v，实得 %v", c.name, c.want, got)
		}
	}
}

// TestTrackEditsBeforeExecution_RecordsPreEditContent —— ★★ 本修正的核心断言。
//
// 在工具执行**前**调 `trackEditsBeforeExecution` → 备份应是**执行前**的内容。
//
// **判别力**：把调用点移到执行后（W3 的倒置）→ 本用例必红。
func TestTrackEditsBeforeExecution_RecordsPreEditContent(t *testing.T) {
	cwd := t.TempDir()
	l := newLoopForTrackTest(t, cwd)

	target := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(target, []byte("BEFORE"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 执行前记账
	l.trackEditsBeforeExecution("write_file", map[string]any{"file_path": target}, "call_1")

	// 然后才"执行"（模拟写盘）
	if err := os.WriteFile(target, []byte("AFTER"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 备份内容必须是 BEFORE
	snaps := l.FileHistory.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("应有 1 个快照，实得 %d", len(snaps))
	}
	b := snaps[0].Files[target]
	raw, err := os.ReadFile(filepath.Join(l.FileHistory.BackupDir(), b.FileName))
	if err != nil {
		t.Fatalf("读备份失败：%v", err)
	}
	if string(raw) != "BEFORE" {
		t.Errorf("★ 备份必须是执行前内容（TS 在执行前记账），实得 %q", raw)
	}

	// 预览应报 1 个变更（倒置时恒空）
	stats, ok := l.FileHistory.GetDiffStats("call_1")
	if !ok || len(stats.FilesChanged) != 1 {
		t.Errorf("★ 应报 1 个变更文件（倒置时恒空→undo 永远说没变更），实得 ok=%v %v",
			ok, stats)
	}
}

// TestTrackEditsBeforeExecution_RelativePathResolvedAgainstCwd —— 入参是相对
// 路径时按 cwd 解析（工具的 file_path 多为相对）。
func TestTrackEditsBeforeExecution_RelativePathResolvedAgainstCwd(t *testing.T) {
	cwd := t.TempDir()
	l := newLoopForTrackTest(t, cwd)

	if err := os.WriteFile(filepath.Join(cwd, "rel.txt"), []byte("X"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.trackEditsBeforeExecution("write_file", map[string]any{"file_path": "rel.txt"}, "call_1")

	snaps := l.FileHistory.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("应有 1 个快照，实得 %d", len(snaps))
	}
	// 键应是**绝对路径**（filehistory 的键空间）
	abs := filepath.Join(cwd, "rel.txt")
	if _, ok := snaps[0].Files[abs]; !ok {
		t.Errorf("应以绝对路径为键（实得 %v）", snaps[0].Files)
	}
}

// TestTrackEditsBeforeExecution_NoHistoryIsNoop —— 无 FileHistory（无会话）→
// 静默空操作，不 panic。
func TestTrackEditsBeforeExecution_NoHistoryIsNoop(t *testing.T) {
	cwd := t.TempDir()
	l := &Loop{}
	l.cfg.Cwd = cwd
	// FileHistory 为 nil
	l.trackEditsBeforeExecution("write_file", map[string]any{"file_path": "a.txt"}, "call_1")
}

// TestTrackEditsBeforeExecution_NonWriteToolIsNoop —— 非写工具不记账。
func TestTrackEditsBeforeExecution_NonWriteToolIsNoop(t *testing.T) {
	cwd := t.TempDir()
	l := newLoopForTrackTest(t, cwd)
	l.trackEditsBeforeExecution("read_file", map[string]any{"file_path": "a.txt"}, "call_1")
	if got := len(l.FileHistory.Snapshots()); got != 0 {
		t.Errorf("非写工具不该记账，实得 %d 个快照", got)
	}
}

// TestTrackEditsBeforeExecution_EmptyIDFallsBack —— 空 toolUseID → 用哨兵。
func TestTrackEditsBeforeExecution_EmptyIDFallsBack(t *testing.T) {
	cwd := t.TempDir()
	l := newLoopForTrackExecution(t, cwd)
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.trackEditsBeforeExecution("write_file", map[string]any{"file_path": "a.txt"}, "")

	snaps := l.FileHistory.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("应记账（用哨兵 id），实得 %d", len(snaps))
	}
	if snaps[0].MessageID != "write" {
		t.Errorf("空 id 应用哨兵 write，实得 %q", snaps[0].MessageID)
	}
}

// ── 测试辅助 ─────────────────────────────────────────────────────

// newLoopForTrackTest 造一个带真 filehistory 的 Loop（仅够记账用）。
func newLoopForTrackTest(t *testing.T, cwd string) *Loop {
	t.Helper()
	return newLoopForTrackExecution(t, cwd)
}

// newLoopForTrackExecution 与上同（别名，语义更明确）。
func newLoopForTrackExecution(t *testing.T, cwd string) *Loop {
	t.Helper()
	l := &Loop{}
	l.cfg.Cwd = cwd
	l.FileHistory = filehistory.New(cwd, "sess-test")
	return l
}

// TestTrackEdit_CallSitePrecedesExecution —— ★ 接线次序的源码级断言。
//
// # 为什么需要源码断言（而非只有行为测试）
//
// `executeTool` 需要完整 `Loop`（registry / cfg / hooks…）才能驱动，
// 单测构造成本高且脆。而本刀要钉的恰恰是**两个语句的先后次序**——
// 那是纯文本事实，源码断言是确定性的、失败信息直指问题。
//
// 判别力：把记账挪到 `Execute` 之后 → 本用例必红。
//
// 对账 TS `tool-pipeline.ts:1404`：「五件写工具的编辑都要**在执行前**
// 进 file-history」。
func TestTrackEdit_CallSitePrecedesExecution(t *testing.T) {
	src, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatalf("读 loop.go 失败：%v", err)
	}
	text := string(src)

	track := strings.Index(text, "l.trackEditsBeforeExecution(tc.name")
	exec := strings.Index(text, "l.registry.Execute(ctx, tc.name")
	if track < 0 {
		t.Fatal("★ loop.go 里找不到 trackEditsBeforeExecution 调用（接线缺失）")
	}
	if exec < 0 {
		t.Fatal("loop.go 里找不到 registry.Execute 调用")
	}
	if track > exec {
		t.Errorf("★ 记账必须在工具执行**之前**（对账 TS tool-pipeline.ts:1404）；"+
			"当前记账在 Execute 之后（字符位置 track=%d exec=%d）", track, exec)
	}
}
