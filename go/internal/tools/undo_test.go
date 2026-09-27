package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 假 History（避免工具层依赖 filehistory 包）────────────────────

type fakeUndoHistory struct {
	latestID    string
	hasLatest   bool
	stats       *UndoDiffStats
	hasStats    bool
	rewindFiles []string
	rewindErr   error

	statsCalls  int
	rewindCalls int
	gotStatsArg string
	gotRewindID string
}

func (f *fakeUndoHistory) LatestSnapshotID() (string, bool) { return f.latestID, f.hasLatest }

func (f *fakeUndoHistory) GetDiffStats(id string) (*UndoDiffStats, bool) {
	f.statsCalls++
	f.gotStatsArg = id
	return f.stats, f.hasStats
}

func (f *fakeUndoHistory) Rewind(id string) ([]string, error) {
	f.rewindCalls++
	f.gotRewindID = id
	return f.rewindFiles, f.rewindErr
}

// undoParams 构造一次 undo 调用参数。
func undoParams(cwd string, input map[string]any) *CallParams {
	if input == nil {
		input = map[string]any{}
	}
	return &CallParams{Cwd: cwd, Input: input, SessionID: "sess1"}
}

// ── definition 逐字 ──────────────────────────────────────────────

// TestUndoDefinition_Exact —— ★ definition 逐字对账 TS（进请求体，前缀缓存）。
func TestUndoDefinition_Exact(t *testing.T) {
	d := Undo().Definition()
	if d.Name != "undo" {
		t.Errorf("name 应为 undo，实得 %q", d.Name)
	}
	want := "撤销最近一次文件改动，将其恢复到之前的备份。恢复前先展示将发生的变化。" +
		"该操作以文件为单位——只回退上一次工具调用中修改过的文件。"
	if d.Description != want {
		t.Errorf("description 逐字不符\n want %q\n  got %q", want, d.Description)
	}
	if d.InputSchema == nil {
		t.Fatal("缺 InputSchema")
	}
	if d.InputSchema.Type != "object" {
		t.Errorf("type 应为 object，实得 %q", d.InputSchema.Type)
	}
	// confirm 是唯一属性
	if len(d.InputSchema.Properties) != 1 {
		t.Errorf("应只有 1 个属性，实得 %d", len(d.InputSchema.Properties))
	}
	if _, ok := d.InputSchema.Properties["confirm"]; !ok {
		t.Error("应有 confirm 属性")
	}
	if strings.Join(d.InputSchema.PropOrder, ",") != "confirm" {
		t.Errorf("PropOrder 应为 [confirm]，实得 %v", d.InputSchema.PropOrder)
	}
	// ★ required 为空（对账 TS：input_schema 无 required 字段）
	if len(d.InputSchema.Required) != 0 {
		t.Errorf("required 应为空（对账 TS），实得 %v", d.InputSchema.Required)
	}
}

// ── 七个分支 ─────────────────────────────────────────────────────

// TestUndo_NoHistory —— 无 history → `文件历史不可用。` + isError。
func TestUndo_NoHistory(t *testing.T) {
	res, err := Undo().Execute(context.Background(), undoParams(t.TempDir(), nil))
	if err != nil {
		t.Fatalf("不该返回 error：%v", err)
	}
	if !res.IsError {
		t.Error("应 isError")
	}
	if res.Content != "文件历史不可用。" {
		t.Errorf("文案逐字不符：%q", res.Content)
	}
}

// TestUndo_NoSnapshot —— 无快照 → `没有可撤销的文件历史快照。`（不 isError）。
func TestUndo_NoSnapshot(t *testing.T) {
	h := &fakeUndoHistory{hasLatest: false}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if res.IsError {
		t.Errorf("无快照不该是 error（TS 如此），实得 %q", res.Content)
	}
	if res.Content != "没有可撤销的文件历史快照。" {
		t.Errorf("文案逐字不符：%q", res.Content)
	}
}

// TestUndo_PreviewNoChanges —— 预览且无可撤 → `最近快照中没有可撤销的变更。`
func TestUndo_PreviewNoChanges(t *testing.T) {
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		hasStats: true, stats: &UndoDiffStats{FilesChanged: nil},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if res.Content != "最近快照中没有可撤销的变更。" {
		t.Errorf("文案逐字不符：%q", res.Content)
	}
	if h.rewindCalls != 0 {
		t.Error("预览不该触发 rewind")
	}
}

// TestUndo_PreviewFormat —— ★ 预览文案逐字（含 `+N/-M 行` 与尾部提示）。
//
// TS 原文：
//
//	`预览：将恢复 ${n} 个文件：\n${fileList}\n+${ins}/-${del} 行${unownedNote}\n\n传入 confirm: true 以执行。`
func TestUndo_PreviewFormat(t *testing.T) {
	cwd := t.TempDir()
	a := filepath.Join(cwd, "a.txt")
	b := filepath.Join(cwd, "sub", "b.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_9",
		hasStats: true,
		stats:    &UndoDiffStats{FilesChanged: []string{a, b}, Insertions: 3, Deletions: 1},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})

	// 期望用**相对路径**列出（工具应把绝对路径归一化后再展示与比较）
	want := "预览：将恢复 2 个文件：\n  - a.txt\n  - sub/b.txt\n+3/-1 行\n\n传入 confirm: true 以执行。"
	if res.Content != want {
		t.Errorf("预览文案不符\n want %q\n  got %q", want, res.Content)
	}
	if h.gotStatsArg != "call_9" {
		t.Errorf("应查询最新 id，实得 %q", h.gotStatsArg)
	}
}

// TestUndo_ConfirmNoFiles —— 确认后无文件 → `没有需要恢复的文件。`
func TestUndo_ConfirmNoFiles(t *testing.T) {
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		rewindFiles: nil, // 返回空
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{"confirm": true}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if res.Content != "没有需要恢复的文件。" {
		t.Errorf("文案逐字不符：%q", res.Content)
	}
}

// TestUndo_ConfirmFormat —— 确认后成功文案逐字。
//
// TS：`已恢复 ${n} 个文件：\n${restored.map(f => `  - ${f}`).join('\n')}${unownedNote}`
func TestUndo_ConfirmFormat(t *testing.T) {
	cwd := t.TempDir()
	a := filepath.Join(cwd, "x.txt")
	b := filepath.Join(cwd, "y.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_2",
		rewindFiles: []string{a, b},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{"confirm": true}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	want := "已恢复 2 个文件：\n  - x.txt\n  - y.txt"
	if res.Content != want {
		t.Errorf("确认文案不符\n want %q\n  got %q", want, res.Content)
	}
	if h.rewindCalls != 1 {
		t.Errorf("应调用一次 rewind，实得 %d", h.rewindCalls)
	}
}

// TestUndo_RewindError —— 失败 → `撤销失败：{msg}` + isError。
func TestUndo_RewindError(t *testing.T) {
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		rewindErr: os.ErrPermission,
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{"confirm": true}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if !res.IsError {
		t.Error("应 isError")
	}
	if !strings.HasPrefix(res.Content, "撤销失败：") {
		t.Errorf("应含前缀「撤销失败：」，实得 %q", res.Content)
	}
}

// ── confirm 严格判定 ─────────────────────────────────────────────

// TestUndo_ConfirmStrictlyTrue —— ★ `confirm` 必须**严格**是 true。
//
// 对账 TS：`const confirm = params.input.confirm === true`
//
// 判别力：用 truthy 判断（`v.(bool)` 的零值 / 非空字符串）→ 下面这些用例会红。
func TestUndo_ConfirmStrictlyTrue(t *testing.T) {
	for _, c := range []struct {
		name  string
		value any
	}{
		{"字符串 \"true\"", "true"},
		{"数字 1", float64(1)},
		{"nil", nil},
		{"空字符串", ""},
		{"false", false},
	} {
		cwd := t.TempDir()
		h := &fakeUndoHistory{
			hasLatest: true, latestID: "call_1",
			hasStats:    true,
			stats:       &UndoDiffStats{FilesChanged: []string{filepath.Join(cwd, "a.txt")}, Insertions: 1},
			rewindFiles: []string{filepath.Join(cwd, "a.txt")},
		}
		res, _ := Undo().Execute(context.Background(), &CallParams{
			Cwd: cwd, Input: map[string]any{"confirm": c.value}, SessionID: "s",
			FileHistory: func() UndoHistory { return h },
		})
		if c.name == "false" {
			// false 也是「非 true」→ 预览
		}
		if h.rewindCalls != 0 {
			t.Errorf("%s：非严格 true 应走**预览**（不执行），但 rewind 被调了 %d 次", c.name, h.rewindCalls)
		}
		if !strings.HasPrefix(res.Content, "预览：") {
			t.Errorf("%s：应走预览分支，实得 %q", c.name, res.Content)
		}
	}
}

// TestUndo_ConfirmTrueExecutes —— 严格 true → 执行。
func TestUndo_ConfirmTrueExecutes(t *testing.T) {
	cwd := t.TempDir()
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		rewindFiles: []string{filepath.Join(cwd, "a.txt")},
	}
	_, _ = Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{"confirm": true}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if h.rewindCalls != 1 {
		t.Errorf("confirm:true 应执行，实得 rewind 调用 %d 次", h.rewindCalls)
	}
}

// ── unownedNote（两处文案不同）────────────────────────────────────

// TestUndo_PreviewUnownedNote —— ★ 预览时的归属告警文案逐字。
//
// TS：
//
//	`\n\n⚠️  警告：${n} 个文件不属于当前任务，可能属于并行会话：\n${列表}\n确认前请核实归属。`
//
// 注意「警告」后有**两个空格**（TS 原文 `⚠️  警告`）。
func TestUndo_PreviewUnownedNote(t *testing.T) {
	cwd := t.TempDir()
	owned := filepath.Join(cwd, "mine.txt")
	other := filepath.Join(cwd, "other.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		hasStats: true,
		stats:    &UndoDiffStats{FilesChanged: []string{owned, other}, Insertions: 2, Deletions: 0},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{}, SessionID: "s",
		OwnedFiles:  []string{"mine.txt"}, // 相对路径（与真实装配一致）
		FileHistory: func() UndoHistory { return h },
	})

	if !strings.Contains(res.Content, "⚠️  警告：1 个文件不属于当前任务，可能属于并行会话：") {
		t.Errorf("应含预览归属告警（注意「警告」前是 ⚠️ 加两个空格）：\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "\n  - other.txt\n确认前请核实归属。") {
		t.Errorf("告警应列出不属于的文件并用相对路径：\n%s", res.Content)
	}
	if strings.Contains(res.Content, "  - mine.txt\n确认前请核实归属") == false {
		// owned 文件仍在列表里（只是不进告警）
	}
}

// TestUndo_ConfirmUnownedNote —— ★ 确认后的告警文案（与预览**不同**）。
//
// TS：`\n⚠️  ${n} 个文件不属于本任务：${逗号连接}`
//
// **注意与预览的差异**：更短、单行、逗号连接。
func TestUndo_ConfirmUnownedNote(t *testing.T) {
	cwd := t.TempDir()
	a := filepath.Join(cwd, "m1.txt")
	b := filepath.Join(cwd, "m2.txt")
	other := filepath.Join(cwd, "o.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		rewindFiles: []string{a, b, other},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{"confirm": true}, SessionID: "s",
		OwnedFiles:  []string{"m1.txt", "m2.txt"},
		FileHistory: func() UndoHistory { return h },
	})

	if !strings.Contains(res.Content, "\n⚠️  1 个文件不属于本任务：o.txt") {
		t.Errorf("应含确认后归属告警（单行 + 逗号连接）：\n%s", res.Content)
	}
}

// TestUndo_NoNoteWhenOwnedFilesEmpty —— ★ OwnedFiles 为空 → **不加告警**。
//
// 对账 TS：`params.ownedFiles?.length ? ... : []`
//
// **为什么关键**：OwnedFiles 为空表示「无归属信息」（如测试直调、非任务上下文）；
// 若照常比较，**所有文件**都会被判为「不属于本任务」→ 每份预览都带误导性告警。
func TestUndo_NoNoteWhenOwnedFilesEmpty(t *testing.T) {
	cwd := t.TempDir()
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		hasStats: true,
		stats:    &UndoDiffStats{FilesChanged: []string{filepath.Join(cwd, "a.txt")}, Insertions: 1},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{}, SessionID: "s",
		// OwnedFiles 留空
		FileHistory: func() UndoHistory { return h },
	})
	if strings.Contains(res.Content, "不属于") {
		t.Errorf("OwnedFiles 为空时不该有归属告警（会误报全部文件）：\n%s", res.Content)
	}
}

// TestUndo_UnownedNoteAcceptsAbsoluteOwnedFiles —— ★ 口径兼容：OwnedFiles 也
// 可能被填成绝对路径。
//
// **为什么要这条**：真实装配里 `OwnedFiles` 是**相对路径**（实测其他工具
// 的用法：`[]string{"src/a.ts"}`），但工具不能假定它一定是相对的——
// 若调用方填了绝对路径，比较必须仍正确（否则又变成「全部不属于」）。
func TestUndo_UnownedNoteAcceptsAbsoluteOwnedFiles(t *testing.T) {
	cwd := t.TempDir()
	a := filepath.Join(cwd, "a.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		hasStats: true,
		stats:    &UndoDiffStats{FilesChanged: []string{a}, Insertions: 1},
	}
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: map[string]any{}, SessionID: "s",
		OwnedFiles:  []string{a}, // 绝对路径形态
		FileHistory: func() UndoHistory { return h },
	})
	if strings.Contains(res.Content, "不属于") {
		t.Errorf("OwnedFiles 用绝对路径时也应判为「属于」（两种口径都要支持）：\n%s", res.Content)
	}
}

// ── 审计 best-effort ─────────────────────────────────────────────

// TestUndo_AuditFailureDoesNotMaskSuccess —— ★ 审计写失败**不得**让撤销报错。
//
// 对账 TS：`try { trackFileRestore(...) } catch { /* audit journal unavailable
// — restore already applied */ }`
//
// **为什么关键**：文件此刻已恢复。若审计失败冒泡成「撤销失败」，模型会以为
// 没撤成 → 重试 → 把刚恢复的旧内容又盖掉（二次伤害）。
//
// 构造：cwd 指向一个**不可写**的路径（.rivet 目录建不成）。
func TestUndo_AuditFailureDoesNotMaskSuccess(t *testing.T) {
	// 用一个只读目录当 cwd → RecordRecovery 写 journal 会失败
	base := t.TempDir()
	roDir := filepath.Join(base, "ro")
	if err := os.MkdirAll(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	restored := filepath.Join(roDir, "a.txt")
	h := &fakeUndoHistory{
		hasLatest: true, latestID: "call_1",
		rewindFiles: []string{restored},
	}
	res, err := Undo().Execute(context.Background(), &CallParams{
		Cwd: roDir, Input: map[string]any{"confirm": true}, SessionID: "s",
		FileHistory: func() UndoHistory { return h },
	})
	if err != nil {
		t.Fatalf("不该返回 error：%v", err)
	}
	if res.IsError {
		t.Errorf("★ 审计失败不得让撤销报错（会导致模型重试并覆盖已恢复内容）：%q", res.Content)
	}
	if !strings.HasPrefix(res.Content, "已恢复 1 个文件：") {
		t.Errorf("仍应报成功，实得 %q", res.Content)
	}
}

// ── 工具元数据 ───────────────────────────────────────────────────

// TestUndo_Metadata —— 逐条对账 TS：
//
//	requiresApproval: () => true   （**撤销是破坏性操作**）
//	isConcurrencySafe: () => false
//	isEnabled: () => true
func TestUndo_Metadata(t *testing.T) {
	tool := Undo()
	if !tool.RequiresApproval(&CallParams{}) {
		t.Error("RequiresApproval 应恒 true（对账 TS：撤销是破坏性操作）")
	}
	if tool.ConcurrencySafe() {
		t.Error("ConcurrencySafe 应恒 false（对账 TS）")
	}
	if !tool.Enabled() {
		t.Error("Enabled 应恒 true（对账 TS）")
	}
	if d := tool.Timeout(&CallParams{}); d != 0 {
		t.Errorf("Timeout 应用默认（0），实得 %v", d)
	}
}

// TestUndo_NilFileHistoryCallback —— FileHistory 回退为 nil → 走「不可用」分支。
func TestUndo_NilFileHistoryCallback(t *testing.T) {
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{},
		FileHistory: nil,
	})
	if !res.IsError || res.Content != "文件历史不可用。" {
		t.Errorf("nil 回调应报「文件历史不可用。」，实得 isError=%v %q", res.IsError, res.Content)
	}
}

// TestUndo_CallbackReturnsNil —— 回调返回 nil（会话未建立）→ 同「不可用」。
func TestUndo_CallbackReturnsNil(t *testing.T) {
	res, _ := Undo().Execute(context.Background(), &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{},
		FileHistory: func() UndoHistory { return nil },
	})
	if !res.IsError || res.Content != "文件历史不可用。" {
		t.Errorf("回调返回 nil 应报「文件历史不可用。」，实得 %q", res.Content)
	}
}
