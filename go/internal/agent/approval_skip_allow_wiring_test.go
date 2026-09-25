package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// approval_skip_allow_wiring_test.go —— 第六十四刀回归测试（提交后审查发现）。
//
// 两个缺口，都是**接线层**的：
//  1. [HIGH，本刀引入的回归] `bashWriteNeedsApproval` 未复刻 TS 外层三元的
//     `skipAllApproval` 短路——skip 档下所有 bash 写命令被拦。
//  2. [MEDIUM] `allowlisted` 只被 bash 写门消费；TS 里它是**全工具面**的
//     独立分支，其余工具的 allow 规则未生效。
//
// 判据一律用**磁盘副作用**（目录/文件真被创建），不用文案——文案对不等于
// 拦截生效，反之亦然。

// TestBashWriteGateSurvivesSkipMode —— skip 档下 bash 写命令**不得**被拦。
//
// 对账 TS `tool-pipeline.ts:1213-1216`：
//
//	let shouldAsk = (unconditionalApproval && !yoloBypassesUnconditional) ? true
//	  : skipAllApproval ? false      ← skip 短路在 pathGrantNeed **之前**
//	  : pathGrantNeed ? true
//	  ...
//
// `dangerously-skip-permissions` 是「完全访问档零审批打扰承诺」——用户显式
// 选择最大自治。Go 侧该档只在 `decideApprovalGate` 内部短路（L924 档位门），
// 流程**继续走到** L1010 的 bash 写门 → 回归。
//
// **headless 下后果更重**：无人可批 → 写命令全被拒 → 任务死锁。
func TestBashWriteGateSurvivesSkipMode(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "mkdir skip-probe-dir"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root,
		ApprovalMode: "dangerously-skip-permissions",
	})
	if err := l.Run(context.TODO(), "建个目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 观察 1：目录**真被创建**（skip 档承诺零审批打扰）。
	if _, err := os.Stat(filepath.Join(root, "skip-probe-dir")); err != nil {
		t.Errorf("**skip 档下 bash 写命令被拦**——违反「完全访问档零审批打扰承诺」："+
			"目录未创建（%v）。headless 下无人可批 → 任务死锁。", err)
	}
	// 观察 2：无审批拒绝文案。
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			t.Errorf("skip 档下不应出现审批拒绝文案，请求体片段：%.500s", b)
		}
	}
}

// TestBashWriteGateSkipModeStillBlocksDestructive —— **反面**：skip 档下
// 硬闸门仍然生效（破坏性命令不可被 skip 豁免）。
//
// 这是不变量测试：修 skip 短路时**不能**顺手把硬闸门也放行了。
// 对账 TS：`unconditionalApproval && !yoloBypassesUnconditional` 在 skip 之前，
// 而 bash 破坏性命令由 Go 的独立硬闸门（loop.go:889）覆盖——先于本门。
func TestBashWriteGateSkipModeStillBlocksDestructive(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "rm -rf /tmp/skip-mode-not-here"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root,
		ApprovalMode: "dangerously-skip-permissions",
	})
	if err := l.Run(context.TODO(), "删目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			found = true
		}
	}
	if !found {
		t.Error("skip 档下破坏性命令仍应被硬闸门拦下——硬闸门不变量被破坏")
	}
}

// TestAllowRulesExemptNonBashTools —— allow 规则对**非 bash 工具**也生效。
//
// 对账 TS `tool-pipeline.ts:1226-1227`：
//
//	: allowlisted ? false      ← 全工具面的独立分支
//	: canAutoApprove ? false
//	: approvalMode === 'manual' ? needsApproval
//
// Go 侧 `IsToolAllowed` + `allowRulesOf` 已有，但**只在
// `bashWriteNeedsApproval` 内部消费**（豁免 bash 写门）——其余工具在 manual
// 档下即使命中 allow 规则仍被拦。
func TestAllowRulesExemptNonBashTools(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "write_file", map[string]any{
			"file_path": filepath.Join(root, "allowed.txt"),
			"content":   "hi",
		}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			Allow: []PermissionAllowRule{{Tool: "write_file"}},
		},
	})
	if err := l.Run(context.TODO(), "写个文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 观察：文件**真被写入**（allow 规则命中 → 豁免 manual 档审批）。
	if _, err := os.Stat(filepath.Join(root, "allowed.txt")); err != nil {
		t.Errorf("**allow 规则对非 bash 工具未生效**：manual 档下命中 allow 规则的 "+
			"write_file 仍被拦（文件未创建：%v）。TS 的 allowlisted 是全工具面分支。", err)
	}
}

// TestAllowRulesDoNotExemptHardGate —— **反面**：allow 规则**不能**豁免硬闸门。
//
// 对账 TS 三元链顺序：`bashWriteRequiresApproval` 在 `allowlisted` **之前**，
// 但更前的 `unconditionalApproval`/`skipAllApproval` 决定硬边界。Go 侧硬闸门
// 是独立一层（先于本门）——allow 规则不该穿透它。
func TestAllowRulesDoNotExemptHardGate(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "rm -rf /tmp/allow-not-here"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			Allow: []PermissionAllowRule{{Tool: "bash"}},
		},
	})
	if err := l.Run(context.TODO(), "删目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			found = true
		}
	}
	if !found {
		t.Error("allow 规则不应豁免硬闸门——破坏性命令仍须被拦")
	}
}
