package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPermissionDenyGateWiring —— **用户 deny 规则门的端到端接线测试**。
//
// 判据（对账 TS `tool-pipeline.ts:1126-1143`）：
//
//   - deny 命中 → 拦下（指令性非重试拒绝）
//   - deny 未命中 → 放行（不误拦）
//   - **任何档位都不能绕过**——含 `dangerously-skip-permissions`
//   - deny 优先于硬闸门与路径授权（决策链最前）
//
// **为什么必须端到端**：单元测试只证明 `IsToolDenied` 判定正确；若
// `loop.go` 没消费它（悬空实现），单测照样全绿而用户的边界仍不生效。
// 本刀修的就是「安全机制根本不存在」——必须证明它**被消费**。
func TestPermissionDenyGateWiring(t *testing.T) {
	// 判据：Run 之后，工具是否**未被执行**且返回了 deny 文案。
	//
	// 为什么看文案而非只看 is_error：deny 门与硬闸门/路径门都返回
	// is_error=true——只看 is_error 无法区分是哪道门拦的，测试会失去
	// 判别力（变异反证时摘掉 deny 门，硬闸门可能仍返回 true，测试假绿）。
	run := func(t *testing.T, mode string, perms *PermissionConfig, toolName string, args map[string]any) (denied bool, blocked bool) {
		t.Helper()
		root := t.TempDir()
		sc := &scriptedServer{responses: []string{
			toolTurnArgs("c1", toolName, args),
			textTurn("完成"),
		}}
		srv := httptest.NewServer(sc.handler())
		defer srv.Close()

		l := newTestLoop(t, srv, Config{
			Model: "m", MaxTokens: 100, Cwd: root,
			ApprovalMode: mode, Permissions: perms,
		})
		if err := l.Run(context.TODO(), "执行"); err != nil {
			t.Fatalf("Run 失败：%v", err)
		}
		for _, b := range sc.handlerBodies() {
			if strings.Contains(b, "matches an active deny rule") {
				denied = true
			}
			if strings.Contains(b, approvalBlockedMarker) {
				blocked = true
			}
		}
		return denied, blocked
	}

	denyBashRm := &PermissionConfig{
		Deny: []PermissionAllowRule{
			{Tool: "bash", Params: map[string]string{"command": "rm -rf*"}},
		},
	}

	t.Run("deny命中_被拦", func(t *testing.T) {
		denied, _ := run(t, "auto-safe", denyBashRm, "bash", map[string]any{"command": "rm -rf /"})
		if !denied {
			t.Error("命中 deny 规则应被拦下")
		}
	})

	t.Run("deny未命中_放行", func(t *testing.T) {
		denied, blocked := run(t, "auto-safe", denyBashRm, "bash", map[string]any{"command": "echo hello"})
		if denied {
			t.Error("未命中 deny 规则不该被 deny 门拦下（误拦会让工具不可用）")
		}
		if blocked {
			t.Error("普通 echo 不该撞硬闸门")
		}
	})

	t.Run("★skip档也不能绕过deny", func(t *testing.T) {
		// ★ 核心语义：TS 注释「Deny rules always win, even in
		// dangerously-skip-permissions」。这是用户**显式配置的边界**，
		// 比档位更高——档位是"要不要问"，deny 是"绝不允许"。
		denied, _ := run(t, "dangerously-skip-permissions", denyBashRm, "bash",
			map[string]any{"command": "rm -rf /"})
		if !denied {
			t.Error("★ deny 规则在 skip 档下仍须生效——它优先于审批档位")
		}
	})

	t.Run("★deny优先于硬闸门", func(t *testing.T) {
		// 决策链顺序验证：同一命令同时命中 deny 与硬闸门时，
		// 应返回 deny 文案（deny 在最前）而非硬闸门文案。
		//
		// 为什么这个顺序重要：deny 是用户的**显式意图**，硬闸门是
		// 系统的**通用保护**。前者信息量更高——用户看到"我配的规则拦了"
		// 比"系统认为这危险"更可操作。
		denied, blocked := run(t, "auto-safe", denyBashRm, "bash", map[string]any{"command": "rm -rf /"})
		if !denied {
			t.Error("★ deny 应优先于硬闸门（决策链最前）")
		}
		if blocked {
			t.Error("★ 命中 deny 时不该走到硬闸门——顺序错误会让文案失去用户意图信息")
		}
	})

	t.Run("无Permissions配置_不误拦", func(t *testing.T) {
		// nil Permissions 必须安全跳过（等价空规则集）——不能 panic 或误拦
		denied, _ := run(t, "auto-safe", nil, "bash", map[string]any{"command": "echo hi"})
		if denied {
			t.Error("nil Permissions 不该拦任何调用")
		}
	})

	t.Run("★deny优先于路径授权", func(t *testing.T) {
		// 出界写同时命中 deny 时，deny 应先生效——若路径门先跑，
		// skip 档会「首触即授」放行，deny 就形同虚设。
		denyWrite := &PermissionConfig{
			Deny: []PermissionAllowRule{
				{Tool: "write_file", Params: map[string]string{"file_path": "/etc/*"}},
			},
		}
		denied, _ := run(t, "dangerously-skip-permissions", denyWrite, "write_file",
			map[string]any{"file_path": "/etc/passwd", "content": "x"})
		if !denied {
			t.Error("★ deny 应优先于路径授权——否则 skip 档的「首触即授」会绕过 deny")
		}
	})

	t.Run("工具名通配deny_生效", func(t *testing.T) {
		// deny 支持工具名通配（如禁掉整族工具）
		denyAll := &PermissionConfig{
			Deny: []PermissionAllowRule{{Tool: "bash"}},
		}
		denied, _ := run(t, "auto-safe", denyAll, "bash", map[string]any{"command": "echo hi"})
		if !denied {
			t.Error("工具名 deny 应拦下该工具的所有调用")
		}
		// 其他工具不受影响
		deniedOther, _ := run(t, "auto-safe", denyAll, "read_file", map[string]any{"file_path": "a.txt"})
		if deniedOther {
			t.Error("工具名 deny 不该影响其他工具")
		}
	})
}
