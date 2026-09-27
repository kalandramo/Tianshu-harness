package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 端到端：写 → undo → 恢复 ─────────────────────────────────────
//
// 本组走**生产装配路径**（`NewDefaultRegistry` + 注入真 History 适配器），
// 而不只是直调工具。这是唯一能抓到「实现已有但零消费」的判据
// （HANDOFF 坑 49：装配层是最后一道缺口）。

// e2eHistory 是测试用的 History 最小实现（避免 tools 测试 import filehistory 包，
// 保持依赖方向；真实适配器在 agent 层）。
type e2eHistory struct {
	snapshots map[string]map[string]string // messageID → absPath → 内容快照
	order     []string
}

func newE2EHistory() *e2eHistory {
	return &e2eHistory{snapshots: map[string]map[string]string{}}
}

func (e *e2eHistory) LatestSnapshotID() (string, bool) {
	if len(e.order) == 0 {
		return "", false
	}
	return e.order[len(e.order)-1], true
}

func (e *e2eHistory) GetDiffStats(id string) (*UndoDiffStats, bool) {
	snap, ok := e.snapshots[id]
	if !ok {
		return nil, false
	}
	stats := &UndoDiffStats{}
	for path, old := range snap {
		cur, err := os.ReadFile(path)
		if err != nil {
			cur = nil
		}
		if string(cur) == old {
			continue
		}
		stats.FilesChanged = append(stats.FilesChanged, path)
		stats.Insertions++
	}
	return stats, true
}

func (e *e2eHistory) Rewind(id string) ([]string, error) {
	snap, ok := e.snapshots[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	var changed []string
	for path, content := range snap {
		if err := os.WriteFile(path, []byte(content), 0o644); err == nil {
			changed = append(changed, path)
		}
	}
	return changed, nil
}

// track 记录一次快照（模拟写工具成功后的登记）。
func (e *e2eHistory) track(absPath string, content []byte) {
	if _, ok := e.snapshots["call_1"]; !ok {
		e.snapshots["call_1"] = map[string]string{}
		e.order = append(e.order, "call_1")
	}
	e.snapshots["call_1"][absPath] = string(content)
}

// TestUndoEndToEnd_ViaProductionRegistry —— ★ 端到端：
// 走生产注册表 → write_file 改文件 → undo 预览 → undo 确认 → 文件回到改前。
func TestUndoEndToEnd_ViaProductionRegistry(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hist := newE2EHistory()
	reg := NewDefaultRegistry(Options{Cwd: cwd, Extra: []Tool{Undo()}})

	// ① 改文件（走生产注册表）
	res, err := reg.Execute(context.Background(), "write_file", &CallParams{
		Cwd:          cwd,
		ToolUseID:    "call_1",
		ApprovalMode: "dangerously-skip-permissions",
		Input:        map[string]any{"file_path": target, "content": "MODIFIED\n"},
	})
	if err != nil || res.IsError {
		t.Fatalf("写入失败：err=%v %s", err, res.Content)
	}
	if got := readRepoFileAbs(t, target); got != "MODIFIED\n" {
		t.Fatalf("写入未生效：%q", got)
	}
	// 模拟写工具登记（真实路径由 Loop 的 TrackFileEdit 回调完成）
	hist.track(target, []byte("ORIGINAL\n"))

	// ② undo 预览（不带 confirm）
	preview, err := reg.Execute(context.Background(), "undo", &CallParams{
		Cwd: cwd, SessionID: "s",
		FileHistory: func() UndoHistory { return hist },
		Input:       map[string]any{},
	})
	if err != nil || preview.IsError {
		t.Fatalf("预览失败：err=%v %s", err, preview.Content)
	}
	if !strings.HasPrefix(preview.Content, "预览：将恢复 1 个文件：") {
		t.Fatalf("预览文案不符：%q", preview.Content)
	}
	if !strings.Contains(preview.Content, "传入 confirm: true 以执行。") {
		t.Errorf("预览应含执行提示：%q", preview.Content)
	}
	// 预览不该改动文件
	if got := readRepoFileAbs(t, target); got != "MODIFIED\n" {
		t.Errorf("预览不该改文件，实得 %q", got)
	}

	// ③ undo 确认 → 恢复
	done, err := reg.Execute(context.Background(), "undo", &CallParams{
		Cwd: cwd, SessionID: "s",
		FileHistory: func() UndoHistory { return hist },
		Input:       map[string]any{"confirm": true},
	})
	if err != nil || done.IsError {
		t.Fatalf("撤销失败：err=%v %s", err, done.Content)
	}
	if !strings.HasPrefix(done.Content, "已恢复 1 个文件：") {
		t.Fatalf("确认文案不符：%q", done.Content)
	}
	if got := readRepoFileAbs(t, target); got != "ORIGINAL\n" {
		t.Errorf("★ 文件应恢复到改前内容，实得 %q", got)
	}
}

// TestUndoEndToEnd_ToolVisibleInDefinitions —— 工具在模型可见列表里。
func TestUndoEndToEnd_ToolVisibleInDefinitions(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	var found bool
	for _, d := range reg.Definitions() {
		if d.Name == "undo" {
			found = true
		}
	}
	if !found {
		t.Error("undo 应在 Definitions 里（恒注册，对账 TS 的 isEnabled: () => true）")
	}
}

// TestUndoEndToEnd_NoHistoryViaRegistry —— 未注入 History（无会话上下文）→
// 工具仍可见，调用时报「文件历史不可用。」（**不 panic**）。
func TestUndoEndToEnd_NoHistoryViaRegistry(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	res, err := reg.Execute(context.Background(), "undo", &CallParams{
		Cwd: t.TempDir(), Input: map[string]any{},
		// FileHistory 未注入
	})
	if err != nil {
		t.Fatalf("不该返回 error：%v", err)
	}
	if !res.IsError || res.Content != "文件历史不可用。" {
		t.Errorf("应报「文件历史不可用。」，实得 isError=%v %q", res.IsError, res.Content)
	}
}

// TestUndoEndToEnd_ApprovalRequired —— ★ 走注册表的审批判定：undo 恒需批准。
//
// **这是门链生效的一半**：`NeedsApproval("undo", …)` 应 true。
func TestUndoEndToEnd_ApprovalRequired(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if !reg.NeedsApproval("undo", &CallParams{}) {
		t.Error("★ undo 应恒需审批（对账 TS requiresApproval: () => true）")
	}
}

// readRepoFileAbs 读绝对路径文件（测试辅助）。
func readRepoFileAbs(t *testing.T, abs string) string {
	t.Helper()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", abs, err)
	}
	return string(b)
}

// TestUndoGate_HighRiskViaAssess —— ★ 门链生效回归（跨包验证）。
//
// `agent/approval_assess.go:305` 早已按名预置：
//
//	// rollback / undo 总是高风险
//	if toolName == "rollback" || toolName == "undo" { … level = RiskHigh }
//
// 此前「undo」这个名字**永不匹配**（工具不存在）。本刀注册后它首次生效。
//
// **注意**：这条断言在 `tools` 包内做不了（`AssessToolRisk` 在 `agent` 包，
// 而 agent 依赖 tools——反向 import 会成环）。故用**源码级断言**验证门链
// 存续（钉住「别把它删了」），实际生效由 `agent` 包的既有测试覆盖。
func TestUndoGate_HighRiskSourceStillPresent(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "agent", "approval_assess.go"))
	if err != nil {
		t.Fatalf("读 approval_assess.go 失败：%v", err)
	}
	text := string(src)
	if !strings.Contains(text, `toolName == "undo"`) {
		t.Error("★ approval_assess.go 的 undo 高风险判定被删了（门链失效）")
	}
	if !strings.Contains(text, "state rollback changes working tree") {
		t.Error("★ undo 的风险理由文案被改了（对账 TS）")
	}
}

// TestUndoGate_NeedsApprovalViaRegistry —— 注册表侧的审批判定。
func TestUndoGate_NeedsApprovalViaRegistry(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if !reg.NeedsApproval("undo", &CallParams{}) {
		t.Error("undo 应恒需审批")
	}
}
