package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPermissionsBash —— 从配置文件读取 `agent.permissions.bash`。
//
// 对账 TS `schema.ts:293` 的 `bash: bashAllowlistSchema`（含 allowlist / denylist）。
//
// **缺口背景**（第六十五刀）：`permissionsRaw` 此前**没有 `bash` 字段** →
// JSON 里的 `permissions.bash.denylist` 被 json.Unmarshal 静默丢弃，
// 用户设的命令前缀黑名单完全不生效。
func TestLoadPermissionsBash(t *testing.T) {
	writeConfig := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("写配置失败：%v", err)
		}
		t.Setenv("RIVET_CONFIG_PATH", p)
		return p
	}

	t.Run("读取denylist与allowlist", func(t *testing.T) {
		writeConfig(t, `{
			"agent": {
				"permissions": {
					"bash": {
						"allowlist": ["git status", "ls"],
						"denylist": ["taskkill", "format"]
					}
				}
			}
		}`)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if perms == nil || perms.Bash == nil {
			t.Fatal("应返回非 nil 的 perms.Bash")
		}
		if len(perms.Bash.Denylist) != 2 {
			t.Fatalf("denylist 应为 2 条：got=%v", perms.Bash.Denylist)
		}
		if perms.Bash.Denylist[0] != "taskkill" {
			t.Errorf("denylist[0] = %q, want %q", perms.Bash.Denylist[0], "taskkill")
		}
		if len(perms.Bash.Allowlist) != 2 {
			t.Fatalf("allowlist 应为 2 条：got=%v", perms.Bash.Allowlist)
		}
	})

	t.Run("无bash字段时Bash为nil", func(t *testing.T) {
		writeConfig(t, `{
			"agent": {
				"permissions": {
					"deny": [{"tool": "write_file"}]
				}
			}
		}`)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if perms == nil {
			t.Fatal("应返回非 nil 的 permissions")
		}
		if perms.Bash != nil {
			t.Errorf("未配置 bash 时 Bash 应为 nil，got=%+v", perms.Bash)
		}
	})

	t.Run("bash存在但列表为空", func(t *testing.T) {
		writeConfig(t, `{
			"agent": {
				"permissions": {
					"bash": {}
				}
			}
		}`)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if perms == nil || perms.Bash == nil {
			t.Fatal("配置了 bash（即使为空）应返回非 nil 的 Bash")
		}
		if len(perms.Bash.Denylist) != 0 {
			t.Errorf("空 denylist 应为空切片，got=%v", perms.Bash.Denylist)
		}
	})
}
