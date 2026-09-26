package agent

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// selfkill_wiring_test.go —— selfKill 门的接线测试（第六十八刀）。
//
// # 缺口背景
//
// TS `shouldAsk` 之前有独立守卫 `denied || bashDenied || selfKill`。
// Go 侧此前只接了前两个（第五十二/六十五刀），`selfKill` 无对应实现。
//
// **为什么危险**：bash 工具用 `exec.Command` 起 shell，agent 就是命令的
// 直接父进程 → shell 里 `kill $PPID` 杀掉 agent 自己，当前 turn 中断。
//
// **判据**：请求体里出现 selfKill 拒绝文案（而非命令实际执行的结果）。
// selfKill 门**独立于权限配置**（TS：Always on, independent of config）——
// 故测试**不配 Permissions**，验证它在无配置时也生效。

// TestSelfKillBlocksOwnPidKill —— 杀自身 PID 的命令被拦。
//
// 用真实进程树（`l.selfTree` 由构造时快照）构造 `kill <自身PID>`——
// 这是**真实会中断会话**的命令形态。
func TestSelfKillBlocksOwnPidKill(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "kill 1"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	// 先建 loop 取它的 selfTree（构造时快照），再用该 PID 构造命令。
	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})

	// 用真实自身 PID 替换命令——**这才是会自杀的形态**。
	sc.responses[0] = toolTurnArgs("c1", "bash", map[string]any{
		"command": "kill " + strconv.Itoa(l.selfTree.SelfPid),
	})

	if err := l.Run(context.TODO(), "重启服务"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "terminate the agent's own runtime") {
			found = true
		}
	}
	if !found {
		t.Errorf("**selfKill 门未接线**：`kill <自身PID>` 未被拦——没有任何请求体含"+
			"selfKill 拒绝文案。selfTree=%+v", l.selfTree)
	}
}

// TestSelfKillBlocksHiddenKill —— 藏在执行符后的自杀命令仍被拦。
//
// 对账 TS：分段后逐段判——`echo hi; kill $PPID` 必须捕获。
func TestSelfKillBlocksHiddenKill(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	sc.responses[0] = toolTurnArgs("c1", "bash", map[string]any{
		"command": "echo hi; kill " + strconv.Itoa(l.selfTree.SelfPid),
	})

	if err := l.Run(context.TODO(), "跑命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "terminate the agent's own runtime") {
			found = true
		}
	}
	if !found {
		t.Error("藏在分号后的自杀命令未被拦——分段逻辑未生效")
	}
}

// TestSelfKillAllowsUnrelatedKill —— **反面对照**：杀无关 PID 不拦。
//
// 对账 TS：`kill <无关 pid>` 与 `npx kill-port <port>` 是正当用法，
// 若被拦会让 agent 无法重启本地服务。
func TestSelfKillAllowsUnrelatedKill(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{
			// **不用 `npx kill-port`**：那会真去拉包（实测 107s，拖慢测试）。
			// 判据是「命令头不是 kill / 不指向自身 PID」，用等价的本地命令即可。
			"command": "echo selfkill-allows-probe; kill 2147483646",
		}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	if err := l.Run(context.TODO(), "重启服务"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	sawOutput := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "selfkill-allows-probe") {
			sawOutput = true
		}
		if strings.Contains(b, "terminate the agent's own runtime") {
			t.Errorf("杀无关 PID 被误拦——会让 agent 无法重启本地服务")
		}
	}
	if !sawOutput {
		t.Error("命令应被执行（输出未出现在请求体）")
	}
}

// TestSelfKillSurvivesSkipMode —— **不变量**：skip 档下 selfKill 仍生效。
//
// 对账 TS：selfKill 是「Always on, independent of config/approval mode」——
// 即使用户选了完全访问档，自杀命令也不能放行（用户选的是"免审批"，
// 不是"允许中断会话"）。
func TestSelfKillSurvivesSkipMode(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo x"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root,
		ApprovalMode: "dangerously-skip-permissions",
	})
	sc.responses[0] = toolTurnArgs("c1", "bash", map[string]any{
		"command": "kill " + strconv.Itoa(l.selfTree.SelfPid),
	})

	if err := l.Run(context.TODO(), "跑命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "terminate the agent's own runtime") {
			found = true
		}
	}
	if !found {
		t.Error("skip 档下 selfKill 门被绕过——违反「Always on, independent of config」")
	}
}

// TestSelfKillWorksWithoutPermissions —— selfKill **不依赖权限配置**。
//
// 对账 TS：它是运行时自我保护，不是用户配的边界。`Permissions == nil`
// 时也须生效（与 deny 门不同——那个需要配置才有内容）。
func TestSelfKillWorksWithoutPermissions(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo x"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
		// Permissions 保持 nil。
	})
	if l.cfg.Permissions != nil {
		t.Fatal("前提：Permissions 应为 nil")
	}
	sc.responses[0] = toolTurnArgs("c1", "bash", map[string]any{
		"command": "kill " + strconv.Itoa(l.selfTree.SelfPid),
	})

	if err := l.Run(context.TODO(), "跑命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "terminate the agent's own runtime") {
			found = true
		}
	}
	if !found {
		t.Error("无权限配置时 selfKill 门未生效——它是运行时保护，不该依赖 Permissions")
	}
}
