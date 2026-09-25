package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// approval_gate_test.go —— 档位审批门的判定与接线验证。
//
// 本门修的是**实测发现的 fail-open 缺口**：4 个写工具在 manual 档下
// `needsApproval=true` 却无人消费（`Registry.NeedsApproval` 零调用者），
// 写操作静默执行。用户选最严档期望逐次确认，实际无人把关。

// TestDecideApprovalGateMatrix —— 档位 × needsApproval × isHighRisk 的**条件矩阵**。
//
// 逐格判定（不把嵌套约束平铺成孤立 if）——对账 TS `shouldAsk` 的档位分支。
func TestDecideApprovalGateMatrix(t *testing.T) {
	cases := []struct {
		mode          string
		needsApproval bool
		isHighRisk    bool
		wantBlock     bool
		why           string
	}{
		// manual 档：needsApproval 决定（TS: `manual ? needsApproval`）
		{"manual", true, false, true, "manual + 需批准 → 拦"},
		{"manual", true, true, true, "manual + 需批准 + 高风险 → 拦"},
		{"manual", false, false, false, "manual + 不需批准 → 放行"},
		{"manual", false, true, false, "manual + 不需批准（高风险但工具不要求批准）→ 放行"},

		// auto-safe 档：isHighRisk 决定（TS: `auto-safe ? isHighRisk`）——**关键差异**
		{"auto-safe", true, false, false, "auto-safe + 需批准但低风险 → **放行**（默认档不可拦写操作）"},
		{"auto-safe", true, true, true, "auto-safe + 高风险 → 拦"},
		{"auto-safe", false, false, false, "auto-safe + 不需批准 + 低风险 → 放行"},
		{"auto-safe", false, true, true, "auto-safe + 高风险 → 拦"},

		// skip 档：恒放行（TS: `skipAllApproval ? false`）
		{"dangerously-skip-permissions", true, true, false, "skip 档恒放行（完全访问承诺）"},
		{"dangerously-skip-permissions", true, false, false, "skip 档恒放行"},
		{"dangerously-skip-permissions", false, false, false, "skip 档恒放行"},

		// 未知档位：兜底放行（TS 的 `: false`）
		{"unknown-mode", true, true, false, "未知档位兜底放行"},
	}

	for _, c := range cases {
		p := &tools.CallParams{ApprovalMode: c.mode}
		got := decideApprovalGate(p, c.needsApproval, c.isHighRisk)
		if got.Block != c.wantBlock {
			t.Errorf("[%s needsApproval=%v isHighRisk=%v] Block=%v, want %v（%s）",
				c.mode, c.needsApproval, c.isHighRisk, got.Block, c.wantBlock, c.why)
		}
		if c.wantBlock && got.Reason == "" {
			t.Errorf("[%s] 拦截时 Reason 不应为空", c.mode)
		}
		if !c.wantBlock && got.Reason != "" {
			t.Errorf("[%s] 放行时 Reason 应为空，实得 %q", c.mode, got.Reason)
		}
	}
}

// TestWriteToolsAreLowRiskInAutoSafe —— **回归守卫**：默认档下写工具必须放行。
//
// 这是本刀最容易写错的地方（首版实测即错）：若 auto-safe 分支误用
// `needsApproval` 而非 `isHighRisk`，4 个写工具会被全部拦下——`write_file`
// 在默认档直接不可用（`registry.go` 警告过「6 个既有测试转红」）。
//
// 本测试锁定「写工具的 risk 不是 high」这一前提——若将来风险评估变更把它
// 判为 high，此测试会红，提醒重新称量默认档行为。
func TestWriteToolsAreLowRiskInAutoSafe(t *testing.T) {
	writeTools := []struct {
		name  string
		input map[string]any
	}{
		{"write_file", map[string]any{"file_path": "x.txt", "content": "hi"}},
		{"edit_file", map[string]any{"file_path": "x.txt", "old_string": "a", "new_string": "b"}},
		{"hash_edit", map[string]any{"file_path": "x.txt"}},
		{"apply_patch", map[string]any{"patch": "--- a/x\n+++ b/x\n"}},
	}
	for _, wt := range writeTools {
		tc := toolCall{name: wt.name, input: wt.input}
		if isHighRiskCall(tc) {
			t.Errorf("%s 被判为高风险——auto-safe 档（默认）下会被拦，write_file 将不可用", wt.name)
		}
		// 但它们在 manual 档确实 needsApproval=true（否则本门无意义）。
		p := &tools.CallParams{Input: wt.input, ApprovalMode: "manual"}
		reg := tools.NewDefaultRegistry(tools.Options{Cwd: t.TempDir()})
		if !reg.NeedsApproval(wt.name, p) {
			t.Errorf("%s 在 manual 档应 needsApproval=true（本门的前提）", wt.name)
		}
	}
}

// TestManualGateBlocksWriteFileEndToEnd —— **接线验证**：manual 档下写工具真被拦。
//
// 单测 `decideApprovalGate` 全绿 ≠ 接线有效——必须走 `executeTool` 的**真实路径**
// （`buildToolCallParams` → 各前置门 → 本门 → 拒绝）。这是「组件在内部测试全绿
// 但生产路径未生效」的防线（本项目已多次踩过）。
//
// 判据：manual 档下 `write_file` 返回 IsError 且文案含「需人工批准」，
// 且**文件未被创建**（拦截真的生效，不是文案对了但文件已写）。
func TestManualGateBlocksWriteFileEndToEnd(t *testing.T) {
	dir := t.TempDir()

	// manual 档 → 应拦。
	lManual := newTestLoop(t, nil, Config{Model: "m", MaxTokens: 100, Cwd: dir, ApprovalMode: "manual"})
	res := lManual.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": "manual.txt", "content": "hi"},
		id:    "t1",
	})
	if !res.IsError {
		t.Errorf("manual 档下 write_file 应被拦，实得：%.300s", res.Content)
	}
	if !containsSub(res.Content, "需人工批准") {
		t.Errorf("拒绝文案应说明需人工批准，实得：%.300s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "manual.txt")); err == nil {
		t.Error("**文件被创建了**——拦截未真正生效（文案对了但副作用已发生）")
	}

	// auto-safe 档（默认）→ 应放行，文件真被写入。
	lAuto := newTestLoop(t, nil, Config{Model: "m", MaxTokens: 100, Cwd: dir, ApprovalMode: "auto-safe"})
	res2 := lAuto.executeTool(context.Background(), toolCall{
		name:  "write_file",
		input: map[string]any{"file_path": "auto.txt", "content": "hi"},
		id:    "t2",
	})
	if res2.IsError {
		t.Errorf("auto-safe 档下 write_file 不应被拦，实得：%.300s", res2.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "auto.txt")); err != nil {
		t.Errorf("auto-safe 档下文件应被写入，实得错误：%v", err)
	}
}

// TestHardGateUnaffectedByApprovalGate —— **不变量**：硬闸门不受本门影响。
//
// 硬闸门（bash 破坏性命令）在 loop.go 中**先于**本门执行，且 skip 档也不豁免。
// 本测试锁定「skip 档下 decideApprovalGate 放行」不会让破坏性命令漏过——
// 那由 RequiresHardGate 独立保证。
func TestHardGateUnaffectedByApprovalGate(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	p := &tools.CallParams{
		Input:        map[string]any{"command": "rm -rf /tmp/zzz"},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	// 硬闸门仍拦（与本门无关）。
	if !reg.RequiresHardGate("bash", p) {
		t.Fatal("skip 档下破坏性 bash 仍应命中硬闸门")
	}
	// 本门放行（skip 档语义）——但不影响硬闸门已拦的事实。
	d := decideApprovalGate(p, reg.NeedsApproval("bash", p), true)
	if d.Block {
		t.Error("skip 档下本门应放行（硬闸门独立负责）")
	}
}
