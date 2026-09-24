package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestApprovalGateBlocksDestructiveBash —— **审批硬闸门接线测试**（用户级判据）。
//
// 缺口背景（第五十刀）：`tools.Registry.NeedsApproval` 此前**零调用者**——
// 6 个工具的 `RequiresApproval` 实现（bash 的破坏性命令硬闸门、git commit、
// 写工具等）返回值**无人消费**。默认档 `auto-safe` 下 `rm -rf` 这类命令会
// 静默执行——工具层声明了「需批准」，管线层从不问。
//
// 判据：模型请求一条破坏性命令时，**请求体里出现拒绝文案**（而非命令输出）。
//
// 手法：`scriptedServer` 捕获请求体——工具被拦时，第二轮请求体会含
// 「需人工批准」的指令性文案。
func TestApprovalGateBlocksDestructiveBash(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "rm -rf /tmp/definitely-not-here"}),
		textTurn("好的，我换个方式"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	if err := l.Run(context.TODO(), "删掉一个目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	bodies := sc.handlerBodies()
	if len(bodies) < 2 {
		t.Fatalf("应有至少 2 个请求体，得到 %d", len(bodies))
	}

	// 第二轮请求体含第一轮的工具结果——被拦时应含拒绝文案。
	// 用「含 A 且不含 B」组合，避免侥幸通过：
	//   A = 需人工批准的指令性文案（approvalBlockedMarker）
	//   B = 命令实际执行的痕迹（rm 成功时不会有，但这里确保不是真跑了）
	found := false
	for i, b := range bodies {
		if strings.Contains(b, approvalBlockedMarker) {
			t.Logf("请求体[%d] 含审批拒绝文案", i)
			found = true
		}
	}
	if !found {
		t.Errorf("**硬闸门未接线**：破坏性命令未被拦截——没有任何请求体含 %q。"+
			"检查 loop.go 是否在 registry.Execute 前调用 NeedsApproval。"+
			"共 %d 个请求体。", approvalBlockedMarker, len(bodies))
	}
}

// TestApprovalGateAllowsBenignBash —— 反面对照：普通命令**不得**被拦。
//
// 若此测试红，说明门控过宽（把非破坏性命令也拦了）——那会让 bash 在
// 默认档下完全不可用。
func TestApprovalGateAllowsBenignBash(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo approval-gate-ok"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	if err := l.Run(context.TODO(), "跑一条无害命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	bodies := sc.handlerBodies()
	// 命令确实执行了 → 输出出现在某个请求体里。
	sawOutput := false
	for _, b := range bodies {
		if strings.Contains(b, "approval-gate-ok") {
			sawOutput = true
		}
		if strings.Contains(b, approvalBlockedMarker) {
			t.Errorf("普通命令被误拦——不该含审批拒绝文案")
		}
	}
	if !sawOutput {
		t.Errorf("普通命令未执行（输出未出现在请求体）——门控过宽")
	}
}

// TestApprovalGateSurvivesSkipMode —— 硬闸门**不受档位影响**：
// 即使 dangerously-skip-permissions 也要拦破坏性命令。
func TestApprovalGateSurvivesSkipMode(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "git reset --hard HEAD"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root,
		ApprovalMode: "dangerously-skip-permissions",
	})
	if err := l.Run(context.TODO(), "重置"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			found = true
		}
	}
	if !found {
		t.Errorf("硬闸门被档位绕过了——dangerously-skip-permissions 档下"+
			"破坏性命令仍应被拦（没有任何请求体含 %q）", approvalBlockedMarker)
	}
}
