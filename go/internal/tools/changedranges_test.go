package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestChangedRanges_WriteFileEditFile —— ★ W4 前置：写族工具必须报告
// `ChangedRanges`（LSP 诊断区域收敛的依据）。
//
// # 为什么这条测试重要
//
// `contract.Result.ChangedRanges` 在接线前**只有定义无写入方**——正是本项目
// 反复出现的「休眠字段」模式。若无本测试，接线若有误（忘了填、填错坐标系）
// 不会有任何信号：`FilterDiagnosticsForEdit` 收到空 ranges 会**静默降级**
// 为「整文件」（功能上仍工作，只是失去精度）。
//
// 故必须断言**非空且值正确**——只测「不崩」会漏掉整个降级路径。
func TestChangedRanges_WriteFileEditFile(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "a.txt")
	if err := os.WriteFile(target, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(Options{Cwd: cwd})

	// ── edit_file：只改第 3 行 ──
	res, err := reg.Execute(context.Background(), "edit_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{"file_path": target, "old_string": "l3", "new_string": "L3-changed"},
	})
	if err != nil || res.IsError {
		t.Fatalf("edit_file 失败：%v %s", err, res.Content)
	}
	if len(res.ChangedRanges) == 0 {
		t.Fatal("★ edit_file 未报告 ChangedRanges（LSP 诊断会静默降级为整文件）")
	}
	if got := res.ChangedRanges[0]; got.Start != 3 || got.End != 3 {
		t.Errorf("改动只有第 3 行，期望 {3,3}，实得 %+v（坐标系可能用错 0-based）", got)
	}

	// ── write_file：整文件覆盖 ──
	res2, err := reg.Execute(context.Background(), "write_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{"file_path": target, "content": "new1\nnew2\n"},
	})
	if err != nil || res2.IsError {
		t.Fatalf("write_file 失败：%v %s", err, res2.Content)
	}
	if len(res2.ChangedRanges) == 0 {
		t.Fatal("★ write_file 未报告 ChangedRanges")
	}

	// ── 无变化（写入同样的内容）→ 无区间（对齐 TS“No change yields []”）──
	before := "same\n"
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	res3, err := reg.Execute(context.Background(), "write_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{"file_path": target, "content": before},
	})
	if err != nil || res3.IsError {
		t.Fatalf("write_file 失败：%v %s", err, res3.Content)
	}
	if len(res3.ChangedRanges) != 0 {
		t.Errorf("内容没变应无区间（TS: No change yields []），实得 %+v", res3.ChangedRanges)
	}
}

// TestChangedRanges_NewFileCoversWhole —— 新文件 → 覆盖全文件的区间。
//
// 对账 TS：brand-new file (before === ”) yields one range covering the whole file。
func TestChangedRanges_NewFileCoversWhole(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "brand-new.txt")
	reg := NewDefaultRegistry(Options{Cwd: cwd})

	res, err := reg.Execute(context.Background(), "write_file", &CallParams{
		Cwd: cwd, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{"file_path": target, "content": "a\nb\nc\n"},
	})
	if err != nil || res.IsError {
		t.Fatalf("write_file 失败：%v %s", err, res.Content)
	}
	if len(res.ChangedRanges) != 1 {
		t.Fatalf("新文件应得单个区间，实得 %+v", res.ChangedRanges)
	}
	if r := res.ChangedRanges[0]; r.Start != 1 || r.End < 3 {
		t.Errorf("新文件区间应覆盖全文（{1,≥3}），实得 %+v", r)
	}
}
