package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// bash_denylist_wiring_test.go —— `permissions.bash.denylist` 的接线测试（第六十五刀）。
//
// # 缺口背景
//
// TS `shouldAsk` 之前有独立守卫 `denied || bashDenied || selfKill`。
// Go 侧此前只接了 `denied`（`IsToolDenied`），且 `PermissionConfig` 连 `bash`
// 字段都没有 → 用户在 `permissions.bash.denylist` 写的命令前缀被**静默忽略**。
//
// **为什么 `deny` 规则替代不了它**：`deny` 匹配工具名+参数模式，
// `denylist` 是命令前缀语义且要穿透 `;` / `&&` / `$( … )` 找隐藏段。

// TestBashDenylistBlocksInAllModes —— denylist 命中在**任何档位**都被拦。
//
// 对账 TS：「Deny rules always win, even in dangerously-skip-permissions」。
// denylist 与 deny 规则同属决策链最前，优先级高于档位。
func TestBashDenylistBlocksInAllModes(t *testing.T) {
	for _, mode := range []string{"manual", "auto-safe", "dangerously-skip-permissions"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			sc := &scriptedServer{responses: []string{
				toolTurnArgs("c1", "bash", map[string]any{"command": "echo hi; taskkill /f /im x.exe"}),
				textTurn("好的"),
			}}
			srv := httptest.NewServer(sc.handler())
			defer srv.Close()

			l := newTestLoop(t, srv, Config{
				Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: mode,
				Permissions: &PermissionConfig{
					Bash: &BashPermissionConfig{Denylist: []string{"taskkill"}},
				},
			})
			if err := l.Run(context.TODO(), "跑命令"); err != nil {
				t.Fatalf("Run 失败：%v", err)
			}

			found := false
			for _, b := range sc.handlerBodies() {
				if strings.Contains(b, "denied") || strings.Contains(b, "deny rule") {
					found = true
				}
			}
			if !found {
				t.Errorf("档位 %s：denylist 命中的命令未被拦——没有任何请求体含拒绝文案", mode)
			}
		})
	}
}

// TestBashDenylistNotConfiguredDoesNotBlock —— 未配置 denylist 时**不拦**。
//
// 反面对照：防止「接线后把所有 bash 命令都拦了」。
func TestBashDenylistNotConfiguredDoesNotBlock(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "echo denylist-absent-probe"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	// Permissions 非 nil 但无 Bash 字段。
	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{},
	})
	if err := l.Run(context.TODO(), "跑命令"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	sawOutput := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "denylist-absent-probe") {
			sawOutput = true
		}
		if strings.Contains(b, "deny rule") {
			t.Errorf("未配置 denylist 却出现拒绝文案——误拦")
		}
	}
	if !sawOutput {
		t.Errorf("未配置 denylist 时命令应执行（输出未出现在请求体）")
	}
}

// TestBashDenylistDoesNotAffectNonBashTools —— 非 bash 工具不受 denylist 影响。
//
// 对账 TS：`tu.name === 'bash'` 才判定。若不加这个条件，其他工具会被误拦。
func TestBashDenylistDoesNotAffectNonBashTools(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{
			"file_path": root + "/x.txt",
		}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			Bash: &BashPermissionConfig{Denylist: []string{"read"}},
		},
	})
	if err := l.Run(context.TODO(), "读文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "deny rule") {
			t.Errorf("非 bash 工具被 denylist 误拦——应只在 tc.name == \"bash\" 时判定")
		}
	}
}

// TestBashDenylistHelper —— `bashDeniedFor` 的纯函数边界。
func TestBashDenylistHelper(t *testing.T) {
	perms := &PermissionConfig{Bash: &BashPermissionConfig{Denylist: []string{"rm"}}}

	cases := []struct {
		name  string
		tool  string
		input map[string]any
		perms *PermissionConfig
		want  bool
	}{
		{"命中", "bash", map[string]any{"command": "rm -rf x"}, perms, true},
		{"未命中", "bash", map[string]any{"command": "ls"}, perms, false},
		{"隐藏段命中", "bash", map[string]any{"command": "ls; rm -rf x"}, perms, true},
		{"非bash工具", "write_file", map[string]any{"command": "rm -rf x"}, perms, false},
		{"无Bash配置", "bash", map[string]any{"command": "rm -rf x"}, &PermissionConfig{}, false},
		{"nil配置", "bash", map[string]any{"command": "rm -rf x"}, nil, false},
		{"command非字符串", "bash", map[string]any{"command": 42}, perms, false},
		{"command缺失", "bash", map[string]any{}, perms, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bashDeniedFor(c.perms, toolCall{name: c.tool, input: c.input})
			if got != c.want {
				t.Errorf("bashDeniedFor(%v, %s) = %v, want %v", c.perms, c.tool, got, c.want)
			}
		})
	}
}
