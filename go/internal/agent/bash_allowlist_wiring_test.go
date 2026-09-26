package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bash_allowlist_wiring_test.go —— `permissions.bash.allowlist` 的接线测试（第六十六刀）。
//
// # 缺口背景
//
// TS `shouldAsk` 的 `bashWriteRequiresApproval` 复合条件含 `!bashAllowlisted`，
// 消费 `isBashCommandAllowlisted`。Go 侧此前只有 deny 侧——用户配了
// `permissions.bash.allowlist` 也不会生效（每次仍要审批）。
//
// **严重性**：可用性（审批疲劳），非安全。但同属「配置被静默忽略」族。
//
// **判据用磁盘副作用**：bash 写命令被放行时会真建目录/写文件——
// 比断言文案可靠（文案对不等于放行生效）。

// TestBashAllowlistExemptsWriteGate —— allowlist 命中时 bash 写门放行。
//
// 对账 TS：`!bashAllowlisted` 让 `mkdir` 在 manual 档也放行（用户显式授权）。
func TestBashAllowlistExemptsWriteGate(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "mkdir allowlisted-dir"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			Bash: &BashPermissionConfig{Allowlist: []string{"mkdir"}},
		},
	})
	if err := l.Run(context.TODO(), "建目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 观察：目录**真被创建**（allowlist 命中 → 豁免 manual 档审批）。
	if _, err := os.Stat(filepath.Join(root, "allowlisted-dir")); err != nil {
		t.Errorf("**allowlist 未生效**：manual 档下命中 `bash.allowlist` 的 mkdir 仍被拦"+
			"（目录未创建：%v）", err)
	}
}

// TestBashAllowlistNotConfiguredStillBlocks —— 反面对照：未配置 allowlist 仍拦。
//
// 防止「接线后把 bash 写门整个绕过了」。
func TestBashAllowlistNotConfiguredStillBlocks(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "mkdir should-be-blocked"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			Bash: &BashPermissionConfig{}, // 空 allowlist
		},
	})
	if err := l.Run(context.TODO(), "建目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "should-be-blocked")); err == nil {
		t.Error("空 allowlist 时 mkdir 应被拦（manual 档），但目录被创建了")
	}
}

// TestBashAllowlistDoesNotBypassFailClosedGuards —— **安全不变量**：
// allowlist 的 fail-closed 守卫不得被接线绕过。
//
// 即使用户把 `env` 写进 allowlist，`env rm -rf /` 也**不能**被放行——
// wrapper 洗白守卫必须仍然生效。这是「allowlist 接线」最容易引入的安全洞。
func TestBashAllowlistDoesNotBypassFailClosedGuards(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "bash", map[string]any{"command": "env rm -rf /tmp/never"}),
		textTurn("好的"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "manual",
		Permissions: &PermissionConfig{
			// 用户把 wrapper 名也写进了 allowlist——守卫必须拒绝。
			Bash: &BashPermissionConfig{Allowlist: []string{"env", "rm"}},
		},
	})
	if err := l.Run(context.TODO(), "删目录"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 硬闸门 + wrapper 守卫：该命令必须被拦（不能因 allowlist 命中而放行）。
	found := false
	for _, b := range sc.handlerBodies() {
		if strings.Contains(b, "denied") || strings.Contains(b, "需人工批准") || strings.Contains(b, "deny rule") {
			found = true
		}
	}
	if !found {
		t.Error("**安全不变量被破坏**：allowlist 含 env 时 `env rm -rf` 未被拦")
	}
}

// TestBashAllowlistedForHelper —— `bashAllowlistedFor` 的纯函数边界。
func TestBashAllowlistedForHelper(t *testing.T) {
	perms := &PermissionConfig{Bash: &BashPermissionConfig{Allowlist: []string{"ls"}}}

	cases := []struct {
		name  string
		perms *PermissionConfig
		tool  string
		input map[string]any
		want  bool
	}{
		{"命中", perms, "bash", map[string]any{"command": "ls -la"}, true},
		{"未命中", perms, "bash", map[string]any{"command": "rm x"}, false},
		{"非bash工具", perms, "write_file", map[string]any{"command": "ls"}, false},
		{"无Bash配置", &PermissionConfig{}, "bash", map[string]any{"command": "ls"}, false},
		{"nil配置", nil, "bash", map[string]any{"command": "ls"}, false},
		{"command非字符串", perms, "bash", map[string]any{"command": 42}, false},
		{"command缺失", perms, "bash", map[string]any{}, false},
		{"空allowlist", &PermissionConfig{Bash: &BashPermissionConfig{}}, "bash",
			map[string]any{"command": "ls"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bashAllowlistedFor(c.perms, c.tool, c.input); got != c.want {
				t.Errorf("bashAllowlistedFor(%s) = %v, want %v", c.tool, got, c.want)
			}
		})
	}
}
