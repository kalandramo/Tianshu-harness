package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// approval_bashwrite_wiring_test.go —— bash 写命令审批门的接线测试（第六十三刀）。
//
// # 缺口背景（第六十三刀核实）
//
// TS `shouldAsk` 决策树里有一条独立分支 `bashWriteRequiresApproval`：
//
//	bashWriteRequiresApproval = requiresBashWriteApproval(tu.name, tu.input)
//	  && !allowlisted && !bashAllowlisted && !safeWriteInNoSandbox && noSandbox
//
// 它管的是 **bash 的写类命令**（`mkdir` / `cp` / `echo x > file` / `chmod` /
// `rm`（无 -rf）等）——与硬闸门管的**破坏性**命令（`rm -rf` / `git reset --hard`）
// **只有部分交集**。
//
// **Go 侧此前只有硬闸门**（`RequiresHardGate` → `isDestructiveCommand`），
// 而 `RequiresBashWriteApproval`（`approval_risk.go:372`）**零调用者**。
// 实测后果：`mkdir foo` 在 **manual 档**下——
//   - 硬闸门 false（非破坏性）
//   - 档位门 false（bash 的 `RequiresApproval` 已订正为 `isDestructiveCommand`）
//
// → **静默执行**。TS 侧同一命令被 `bashWriteRequiresApproval` 拦下。
// 这与第五十刀/第六十二刀是**同族缺陷**（安全判定已移植但未接线）。

// TestBashWriteGateBlocksMkdirInManual —— manual 档下 bash 写命令必须被拦。
//
// 判据：请求体出现审批拒绝文案，且**目录真未创建**（非仅文案对）。
func TestBashWriteGateBlocksMkdirInManual(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "mkdir gate-probe-dir"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
	})
	if err := l.Run(context.TODO(), "建个目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			found = true
		}
	}
	if !found {
		t.Errorf("**bash 写审批门未接线**：manual 档下 `mkdir` 未被拦截——"+
			"没有任何请求体含 %q。检查 loop.go 是否消费 RequiresBashWriteApproval。",
			approvalBlockedMarker)
	}
}

// TestBashWriteGateAllowsMkdirInAutoSafe —— 反面对照：auto-safe 档下**不拦**。
//
// 关键回归：若把 bashWriteRequiresApproval 误接成档位无关（如放在 skip 短路
// 之外或对 auto-safe 也生效），默认档下所有 bash 写命令会被拦——bash 的
// 常规使用（mkdir/cp/重定向）直接不可用。
//
// 对账 TS：`bashWriteRequiresApproval` 的三元链位置在**档位分支之前**，
// 但 TS 的 `safeWriteInNoSandbox` 依赖 `approvalMode === 'auto-safe'`；
// 而 Go 侧无沙箱（`isSandboxActive` 零命中 → `noSandbox` 恒 true）。
// **这是本刀最重要的对账取舍**——见 approval_gate.go 的注释。
func TestBashWriteGateAllowsMkdirInAutoSafe(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "mkdir allowed-probe-dir"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	if err := l.Run(context.TODO(), "建个目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, approvalBlockedMarker) {
			t.Errorf("**门控过宽**：auto-safe 档下 `mkdir` 被拦——"+
				"默认档下 bash 常规写操作会完全不可用。请求体片段：%.600s", b)
		}
	}
}

// TestBashWriteGateAllowsBenignReadInManual —— manual 档下**只读命令不拦**。
//
// 对账 TS：`bashCommandMayWrite('ls -la')` 为 false → 不拦。
// 若此测试红，说明门控把只读命令也当写命令拦了——manual 档下连 `ls` 都跑不了。
func TestBashWriteGateAllowsBenignReadInManual(t *testing.T) {
	root := t.TempDir()

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo bash-write-gate-read-probe"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
	})
	if err := l.Run(context.TODO(), "跑条只读命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	sawOutput := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "bash-write-gate-read-probe") {
			sawOutput = true
		}
		if strings.Contains(b, approvalBlockedMarker) {
			t.Errorf("只读命令在 manual 档被误拦——门控过宽")
		}
	}
	if !sawOutput {
		t.Errorf("只读命令未执行（输出未出现在请求体）")
	}
}

// ── 条件矩阵（纯函数层，对账 TS `bashWriteRequiresApproval` 的复合条件）──
//
// wiring 测试走 loop 端到端，只覆盖「manual 拦 / auto-safe 放」两条路径。
// 矩阵补齐其余维度：安全写 vs risky 写、越界写目标、allowlist 豁免。
//
// **为什么必须逐格判定而非抽样**：TS 的复合条件有 4 个合取项
// （`!allowlisted && !safeWriteInNoSandbox && noSandbox`），抽样会漏掉
// 某一项的短路——例如漏掉 `HasOutOfWorkspaceWriteTarget` 时，
// `echo key >> ~/.ssh/authorized_keys` 会在 auto-safe 档零提示执行。

// TestBashWriteGateMatrix —— 档位 × 命令类型 × allowlist 的条件矩阵。
func TestBashWriteGateMatrix(t *testing.T) {
	allowBash := []PermissionAllowRule{{Tool: "bash"}}

	cases := []struct {
		name  string
		tool  string
		cmd   string
		mode  string
		rules []PermissionAllowRule
		want  bool
		why   string
	}{
		// ── 非写命令：任何档都不拦（否则 manual 档连 ls 都跑不了）──
		{"只读-manual", "bash", "ls -la", "manual", nil, false, "非写命令"},
		{"只读-auto-safe", "bash", "ls -la", "auto-safe", nil, false, "非写命令"},

		// ── 安全写：manual 拦，auto-safe 放（档位依赖的核心）──
		{"mkdir-manual", "bash", "mkdir x", "manual", nil, true, "manual 档安全写仍拦"},
		{"mkdir-auto-safe", "bash", "mkdir x", "auto-safe", nil, false, "auto-safe 档安全写放行"},
		{"重定向-auto-safe", "bash", "echo hi > f", "auto-safe", nil, false, "安全写"},
		{"touch-auto-safe", "bash", "touch f", "auto-safe", nil, false, "安全写"},

		// ── risky 写：**两档都拦**（不是安全写，auto-safe 无豁免）──
		{"rm-无rf-manual", "bash", "rm x", "manual", nil, true, "risky 写"},
		{"rm-无rf-auto-safe", "bash", "rm x", "auto-safe", nil, true, "risky 写在 auto-safe 也拦"},
		{"chmod-auto-safe", "bash", "chmod 777 x", "auto-safe", nil, true, "risky 写"},
		{"mv-auto-safe", "bash", "mv a b", "auto-safe", nil, true, "risky 写"},

		// ── 越界写目标：auto-safe 的「安全写」豁免**失效**（第二道闸）──
		{"越界重定向-auto-safe", "bash", "echo k >> /etc/passwd", "auto-safe", nil, true,
			"写目标越界——安全写豁免必须失效"},
		{"家目录重定向-auto-safe", "bash", "echo k >> ~/.ssh/authorized_keys", "auto-safe", nil, true,
			"写目标越界"},
		{"越界mkdir-auto-safe", "bash", "mkdir /tmp/outside", "auto-safe", nil, true,
			"写目标越界"},

		// ── allowlist 豁免：用户显式 allow 规则覆盖本门 ──
		{"allow豁免-manual", "bash", "mkdir x", "manual", allowBash, false, "用户 allow 规则"},
		{"allow豁免-auto-safe", "bash", "rm x", "auto-safe", allowBash, false, "用户 allow 规则"},

		// ── 非 bash 工具：本门不管（工具级写审批由档位门负责）──
		{"非bash工具", "write_file", "", "manual", nil, false, "本门只管 bash"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := map[string]any{}
			if c.cmd != "" {
				input["command"] = c.cmd
			}
			got := bashWriteNeedsApproval(c.tool, input, c.mode, c.rules)
			if got != c.want {
				t.Errorf("bashWriteNeedsApproval(%q, %q, %q) = %v, want %v（%s）",
					c.tool, c.cmd, c.mode, got, c.want, c.why)
			}
		})
	}
}
