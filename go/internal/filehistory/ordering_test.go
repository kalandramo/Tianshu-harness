package filehistory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTrackEdit_BackupHoldsContentAtCallTime —— ★★ 时序语义（本刀修正的核心）。
//
// # 缺陷背景（第一百零二刀 W3 的时序倒置）
//
// W3 把 `TrackEdit` 接到了写盘的**成功之后**，理由写在注释里：「对账 TS 的
// `trackEdit` 语义——它记录的是**编辑后**的内容」。**那个断言是错的**。
//
// TS 的真实时序（`src/agent/tool-pipeline.ts:1404` 注释逐字）：
//
//	// E4 记账收口：五件写工具（WRITE_TOOL_NAMES）的编辑都要**在执行前**进
//	// file-history（/undo 与边界回溯的记账源头）。
//
// 后果链（倒置时）：
//
//	备份 = 编辑后内容
//	→ `GetDiffStats` 的 `oldContent == newContent` 恒真 → FilesChanged 恒空
//	→ undo 预览恒返回「最近快照中没有可撤销的变更。」
//	→ `Rewind` 把当前内容原样写回，却仍计入 changed → 报「已恢复 N 个文件」
//	  但文件**零变化**。
//
// 即：整个「撤销安全网」静默失效。本用例用**时序**把它钉住。
func TestTrackEdit_BackupHoldsContentAtCallTime(t *testing.T) {
	root := t.TempDir()
	h := New(root, "sess1")
	target := filepath.Join(root, "a.txt")

	// 编辑前
	if err := os.WriteFile(target, []byte("BEFORE"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ★ 正确时序：编辑**前**登记
	if err := h.TrackEdit(target, "call_1"); err != nil {
		t.Fatal(err)
	}
	// 然后才编辑
	if err := os.WriteFile(target, []byte("AFTER"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ① 备份里必须是**编辑前**的内容
	b := h.Snapshots()[0].Files[target]
	raw, err := os.ReadFile(filepath.Join(h.BackupDir(), b.FileName))
	if err != nil {
		t.Fatalf("读备份失败：%v", err)
	}
	if string(raw) != "BEFORE" {
		t.Errorf("★ 备份必须是**编辑前**内容（TS 在执行前记账），实得 %q", raw)
	}

	// ② 预览应报出 1 个变更文件（倒置时恒空）
	stats, ok := h.GetDiffStats("call_1")
	if !ok {
		t.Fatal("应能算统计")
	}
	if len(stats.FilesChanged) != 1 {
		t.Errorf("★ 应报告 1 个变更文件（倒置时恒空 → undo 永远说「没有可撤销的变更」），实得 %v",
			stats.FilesChanged)
	}

	// ③ Rewind 真的把文件改回去
	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("应报告 1 个改动，实得 %d", len(changed))
	}
	got, _ := os.ReadFile(target)
	if string(got) != "BEFORE" {
		t.Errorf("★ 文件应恢复为 BEFORE，实得 %q", got)
	}
}

// TestTrackEdit_BackupAfterEditMeansNoOp —— 反证：**编辑后**才登记会让 undo 变成空操作。
//
// 这条不是「另一种正确实现」，而是**把缺陷行为写成断言**——它与上一条
// 是对偶的：只要上一条绿、这条就必然红（若实现正确）。
//
// **为什么要它**：倒置的实现能让上一条红而这条绿。两条一起，任何方向
// 的错误都无处可藏。变异反证时它提供第二个观测面。
func TestTrackEdit_BackupAfterEditMeansNoOp(t *testing.T) {
	root := t.TempDir()
	h := New(root, "sess1")
	target := filepath.Join(root, "a.txt")

	if err := os.WriteFile(target, []byte("BEFORE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("AFTER"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ✗ 错误时序：编辑后才登记
	if err := h.TrackEdit(target, "call_1"); err != nil {
		t.Fatal(err)
	}

	// 这种时序下，预览必然「无变更」——这正是缺陷的可观测形态。
	stats, _ := h.GetDiffStats("call_1")
	if len(stats.FilesChanged) != 0 {
		t.Errorf("编辑后登记 → 备份==当前 → 预览应「无变更」（这是缺陷特征）；"+
			"若此处非空，说明备份不是从磁盘读的，另有问题。实得 %v", stats.FilesChanged)
	}
}

// TestRewind_PostEditBackupReportsFalsePositive —— ★ 缺陷的另一半：
// 倒置时 `Rewind` 会**谎报**「已恢复」（文件其实没变）。
//
// 这是最危险的部分：模型收到「已恢复 N 个文件」会以为安全网生效了。
func TestRewind_PostEditBackupReportsFalsePositive(t *testing.T) {
	root := t.TempDir()
	h := New(root, "sess1")
	target := filepath.Join(root, "a.txt")

	_ = os.WriteFile(target, []byte("V1"), 0o644)
	_ = os.WriteFile(target, []byte("V2"), 0o644)
	_ = h.TrackEdit(target, "call_1") // ✗ 编辑后登记

	changed, err := h.Rewind("call_1")
	if err != nil {
		t.Fatal(err)
	}
	// 缺陷行为：报告「改动了」，但内容其实没变
	//
	// 本用例断言的是**内容未被改成 V1**（因为备份里存的是 V2）——
	// 即把「谎报」的事实钉住，供修复后回头确认它已消失。
	got, _ := os.ReadFile(target)
	if len(changed) == 1 && string(got) == "V1" {
		t.Error("若此处成立，说明备份确实是编辑前内容——那上一条（正确时序）就无意义了")
	}
	if string(got) != "V2" {
		t.Errorf("倒置时序下内容应保持 V2（备份==当前），实得 %q", got)
	}
}
