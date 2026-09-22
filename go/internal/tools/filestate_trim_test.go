package tools

import (
	"testing"
)

// filestate_trim_test.go —— `trimLastKnownLocked` 的裁剪语义（对账 TS `trimLastKnown`）。
//
// # 修正的不等价（第二十八刀）
//
// Go 原先裁 `size - max` 条（501 → 500），TS 裁 `ceil(size*0.2)` 条（501 → 400）。
// 差异后果：Go 每次只裁 1 条，表容量长期贴着上限反复触发裁剪。
//
// 与 `trimReadHistoryLocked`（readdedup.go）用**同一算式**——两表语义一致。

// TestTrimLastKnownDropsOldestTwentyPercent —— 501 条 → 裁 101 → 剩 400。
func TestTrimLastKnownDropsOldestTwentyPercent(t *testing.T) {
	ResetFileStateForTests()

	// 写 lastKnownMax+1 条触发裁剪。
	for i := 0; i < lastKnownMax+1; i++ {
		NoteFileObserved("/path/file"+itoa(i)+".go", 1, 1, "s1")
	}

	lastKnownMu.Lock()
	n := len(lastKnownState)
	lastKnownMu.Unlock()

	// 501 → 裁 ceil(501*0.2)=101 → 剩 400。
	if n != 400 {
		t.Errorf("裁剪后应剩 400 条（501-101），实得 %d\n"+
			"（若为 500 则是旧的 size-max 语义——不等价于 TS）", n)
	}
}

// TestTrimLastKnownKeepsNewest —— 裁掉的是**最旧**的，最新的必须留下。
func TestTrimLastKnownKeepsNewest(t *testing.T) {
	ResetFileStateForTests()

	for i := 0; i < lastKnownMax+1; i++ {
		NoteFileObserved("/path/f"+itoa(i)+".go", int64(i), 1, "s1")
	}

	// 最新一条（i = lastKnownMax）应在。
	newest := "/path/f" + itoa(lastKnownMax) + ".go"
	if _, ok := GetFileReadMtime(newest, "s1"); !ok {
		t.Errorf("最新的条目应被保留：%s", newest)
	}
	// 最旧一条（i = 0）应被裁掉。
	oldest := "/path/f0.go"
	if _, ok := GetFileReadMtime(oldest, "s1"); ok {
		t.Errorf("最旧的条目应被裁掉：%s", oldest)
	}
}

// TestTrimLastKnownNoopUnderLimit —— 未超限时不裁。
func TestTrimLastKnownNoopUnderLimit(t *testing.T) {
	ResetFileStateForTests()
	for i := 0; i < 100; i++ {
		NoteFileObserved("/p/f"+itoa(i)+".go", 1, 1, "s1")
	}
	lastKnownMu.Lock()
	n := len(lastKnownState)
	lastKnownMu.Unlock()
	if n != 100 {
		t.Errorf("未超限不应裁剪，实得 %d", n)
	}
}

// TestTrimSemanticsMatchReadDedup —— **两表算式一致**（同一 ceil(size*0.2)）。
//
// 防的是「两表各自演化出不同裁剪语义」——TS 侧两者都是 `Math.ceil(size * 0.2)`。
func TestTrimSemanticsMatchReadDedup(t *testing.T) {
	// 用同一 size 比较两个 trim 的裁剪量。
	size := 501
	wantDrop := (size + 4) / 5 // ceil(size*0.2) = 101

	// trimLastKnownLocked 的算式。
	ResetFileStateForTests()
	for i := 0; i < size; i++ {
		NoteFileObserved("/x/f"+itoa(i)+".go", 1, 1, "s")
	}
	// 触发一次裁剪（size 已达 501，但 NoteFileObserved 只在写入时 trim——
	// 上面循环的最后一次写入已触发）。
	lastKnownMu.Lock()
	gotDrop := size - len(lastKnownState)
	lastKnownMu.Unlock()

	if gotDrop != wantDrop {
		t.Errorf("trimLastKnown 裁了 %d 条，want %d（ceil(size*0.2)）", gotDrop, wantDrop)
	}

	// trimReadHistoryLocked 的算式（同 size）。
	ResetReadDedupForTests()
	for i := 0; i < size; i++ {
		RecordRead("k"+itoa(i), 1, 1, 10, 10, "", "s")
	}
	readHistoryMu.Lock()
	gotDropRH := size - len(readHistory)
	readHistoryMu.Unlock()

	if gotDropRH != wantDrop {
		t.Errorf("trimReadHistory 裁了 %d 条，want %d", gotDropRH, wantDrop)
	}
	if gotDrop != gotDropRH {
		t.Errorf("两表裁剪量应一致：lastKnown=%d readHistory=%d", gotDrop, gotDropRH)
	}
}
