package tools

import (
	"context"

	"github.com/kalandramo/tianshu/go/internal/filehistory"
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

// realHistoryAdapter 把真 `filehistory.History` 适配成 `tools.UndoHistory`。
//
// 与生产侧 `agent.undoHistoryAdapter` 同构（两包类型同形但不同名）。
// **测试侧自建一份**是为了让 tools 包不依赖 agent 包（依赖方向）。
type realHistoryAdapter struct{ h *filehistory.History }

func (a realHistoryAdapter) LatestSnapshotID() (string, bool) { return a.h.LatestSnapshotID() }

func (a realHistoryAdapter) GetDiffStats(id string) (*UndoDiffStats, bool) {
	st, ok := a.h.GetDiffStats(id)
	if !ok || st == nil {
		return nil, ok
	}
	return &UndoDiffStats{FilesChanged: st.FilesChanged, Insertions: st.Insertions, Deletions: st.Deletions}, true
}

func (a realHistoryAdapter) Rewind(id string) ([]string, error) { return a.h.Rewind(id) }

// TestUndoEndToEnd_ViaProductionRegistry —— ★ 端到端：
// 走生产注册表 → write_file 改文件 → undo 预览 → undo 确认 → 文件回到改前。
func TestUndoEndToEnd_ViaProductionRegistry(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "notes.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 真 filehistory（写到临时目录）
	hist := filehistory.New(filepath.Join(cwd, ".rivet-test-isolated"), "e2e")
	reg := NewDefaultRegistry(Options{Cwd: cwd, Extra: []Tool{Undo()}})

	// ★ 记账必须在写入**之前**（对账 TS `tool-pipeline.ts:1404`：
	// 「五件写工具的编辑都要**在执行前**进 file-history」）。
	//
	// **为什么必须用真实现**：本刀第一版测试用包内假实现手工 `track` 了
	// 「编辑前内容」，把正确时序编码成期望——故时序倒置时它照样绿
	// （审查 CRITICAL-2 指出的正是这点）。用真实现 + 正确时序后，
	// 谁把记账挪到执行后，这里就红。
	if err := hist.TrackEdit(target, "call_1"); err != nil {
		t.Fatalf("记账失败：%v", err)
	}

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

	// ② undo 预览（不带 confirm）
	preview, err := reg.Execute(context.Background(), "undo", &CallParams{
		Cwd: cwd, SessionID: "s",
		FileHistory: func() UndoHistory { return realHistoryAdapter{hist} },
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
		FileHistory: func() UndoHistory { return realHistoryAdapter{hist} },
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
