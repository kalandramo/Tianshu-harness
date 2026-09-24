package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUserConfigPathParity —— 对账 TS `src/config/paths.ts` 的路径解析。
//
// 判据（逐字对账 TS）：
//
//	userConfigPath():
//	  1. RIVET_CONFIG_PATH 环境变量（最高优先）
//	  2. rivetHome()/config.json
//	rivetHome():
//	  1. RIVET_HOME 环境变量
//	  2. 平台默认：Windows = %LOCALAPPDATA%\.rivet，其他 = ~/.rivet
//
// **为什么逐字对账**：路径错了就永远读不到用户的配置——功能"存在但静默
// 失效"，正是本刀要修的那类缺陷。Windows 上尤其易错：TS 用
// `%LOCALAPPDATA%\.rivet` 而**不是** `~/.rivet`。
func TestUserConfigPathParity(t *testing.T) {
	t.Run("RIVET_CONFIG_PATH_最高优先", func(t *testing.T) {
		t.Setenv("RIVET_CONFIG_PATH", "/custom/path/config.json")
		t.Setenv("RIVET_HOME", "/should/be/ignored")
		if got := UserConfigPath(); got != "/custom/path/config.json" {
			t.Errorf("RIVET_CONFIG_PATH 应最高优先：got=%q", got)
		}
	})

	t.Run("RIVET_HOME_次优先", func(t *testing.T) {
		t.Setenv("RIVET_CONFIG_PATH", "")
		home := t.TempDir()
		t.Setenv("RIVET_HOME", home)
		want := filepath.Join(home, "config.json")
		if got := UserConfigPath(); got != want {
			t.Errorf("RIVET_HOME 应生效：got=%q want=%q", got, want)
		}
	})

	t.Run("平台默认_无环境变量时", func(t *testing.T) {
		t.Setenv("RIVET_CONFIG_PATH", "")
		t.Setenv("RIVET_HOME", "")
		got := UserConfigPath()
		if !filepath.IsAbs(got) {
			t.Errorf("默认路径应为绝对路径：got=%q", got)
		}
		if filepath.Base(got) != "config.json" {
			t.Errorf("默认路径文件名应为 config.json：got=%q", got)
		}
		// 目录部分应含 .rivet（Windows 在 LOCALAPPDATA 下，其他在 home 下）
		dir := filepath.Dir(got)
		if filepath.Base(dir) != ".rivet" {
			t.Errorf("默认路径目录应为 .rivet：got=%q", dir)
		}
	})

	t.Run("★Windows用LOCALAPPDATA非home", func(t *testing.T) {
		// ★ 关键对账点：TS 在 win32 上用 %LOCALAPPDATA%\.rivet。
		// 若误用 ~/.rivet，Windows 用户配置永远读不到。
		t.Setenv("RIVET_CONFIG_PATH", "")
		t.Setenv("RIVET_HOME", "")
		if os.Getenv("LOCALAPPDATA") == "" {
			t.Skip("非 Windows 环境（LOCALAPPDATA 未设），跳过平台断言")
		}
		got := DefaultRivetHome()
		wantPrefix := filepath.Join(os.Getenv("LOCALAPPDATA"), ".rivet")
		if got != wantPrefix {
			t.Errorf("★ Windows 默认应为 %%LOCALAPPDATA%%\\.rivet：got=%q want=%q", got, wantPrefix)
		}
	})
}

// TestLoadPermissionsDeny —— 从配置文件读取 permissions.deny。
//
// 判据：读 `~/.rivet/config.json` 的 `agent.permissions.deny`（对账 TS
// `permissionsSchema` 的嵌套位置——它在 `agent` 下，不是顶层）。
func TestLoadPermissionsDeny(t *testing.T) {
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

	t.Run("读取agent.permissions.deny", func(t *testing.T) {
		writeConfig(t, `{
			"agent": {
				"permissions": {
					"deny": [
						{"tool": "bash", "params": {"command": "rm -rf*"}},
						{"tool": "write_file"}
					]
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
		if len(perms.Deny) != 2 {
			t.Fatalf("deny 规则数应为 2：got=%d", len(perms.Deny))
		}
		if perms.Deny[0].Tool != "bash" {
			t.Errorf("第 1 条工具名：got=%q want=bash", perms.Deny[0].Tool)
		}
		if perms.Deny[0].Params["command"] != "rm -rf*" {
			t.Errorf("第 1 条参数：got=%q want=rm -rf*", perms.Deny[0].Params["command"])
		}
		if perms.Deny[1].Tool != "write_file" {
			t.Errorf("第 2 条工具名：got=%q want=write_file", perms.Deny[1].Tool)
		}
	})

	t.Run("同时读allow", func(t *testing.T) {
		writeConfig(t, `{
			"agent": {"permissions": {
				"allow": [{"tool": "read_*"}],
				"deny": [{"tool": "bash"}]
			}}
		}`)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if len(perms.Allow) != 1 || perms.Allow[0].Tool != "read_*" {
			t.Errorf("allow 规则：got=%+v", perms.Allow)
		}
		if len(perms.Deny) != 1 || perms.Deny[0].Tool != "bash" {
			t.Errorf("deny 规则：got=%+v", perms.Deny)
		}
	})

	t.Run("★嵌套位置必须是agent.permissions", func(t *testing.T) {
		// ★ 若把 permissions 读成顶层（而非 agent 下），配置永远读不到。
		// 本用例钉住嵌套层级：顶层 permissions 应被忽略。
		writeConfig(t, `{
			"permissions": {"deny": [{"tool": "bash"}]}
		}`)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("顶层 permissions 不该报错（只是被忽略）：%v", err)
		}
		if perms != nil && len(perms.Deny) > 0 {
			t.Error("★ 顶层 permissions 应被忽略——正确位置是 agent.permissions")
		}
	})
}

// TestLoadPermissionsFailClosed —— 异常输入必须 fail-closed（不 panic）。
//
// 对账 TS 的语义：配置问题不该崩掉运行时。Go 侧选择返回 nil + error
// （调用方忽略 error 时得到 nil = 无规则 = 不误拦，方向安全）。
func TestLoadPermissionsFailClosed(t *testing.T) {
	t.Run("文件不存在_返回nil", func(t *testing.T) {
		t.Setenv("RIVET_CONFIG_PATH", filepath.Join(t.TempDir(), "nonexistent.json"))
		perms, err := LoadPermissions()
		if err != nil {
			t.Errorf("文件不存在不该报错（正常情况）：%v", err)
		}
		if perms != nil {
			t.Errorf("文件不存在应返回 nil：got=%+v", perms)
		}
	})

	t.Run("★非法JSON_不panic", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{not valid json`), 0o644); err != nil {
			t.Fatalf("写配置失败：%v", err)
		}
		t.Setenv("RIVET_CONFIG_PATH", p)

		// 必须不 panic——配置损坏不该崩掉 agent
		perms, err := LoadPermissions()
		if err == nil {
			t.Error("非法 JSON 应返回 error（让调用方可见）")
		}
		if perms != nil {
			t.Errorf("非法 JSON 应返回 nil permissions：got=%+v", perms)
		}
	})

	t.Run("无permissions字段_返回nil", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"agent": {"model": "x"}}`), 0o644); err != nil {
			t.Fatalf("写配置失败：%v", err)
		}
		t.Setenv("RIVET_CONFIG_PATH", p)
		perms, err := LoadPermissions()
		if err != nil {
			t.Errorf("无 permissions 字段不该报错：%v", err)
		}
		if perms != nil {
			t.Errorf("无 permissions 字段应返回 nil：got=%+v", perms)
		}
	})

	t.Run("空deny数组_返回空规则", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(`{"agent": {"permissions": {"deny": []}}}`), 0o644); err != nil {
			t.Fatalf("写配置失败：%v", err)
		}
		t.Setenv("RIVET_CONFIG_PATH", p)
		perms, err := LoadPermissions()
		if err != nil {
			t.Fatalf("空 deny 不该报错：%v", err)
		}
		if perms == nil {
			t.Fatal("有 permissions 字段应返回非 nil（即使是空规则）")
		}
		if len(perms.Deny) != 0 {
			t.Errorf("空 deny 数组应得 0 条规则：got=%d", len(perms.Deny))
		}
	})
}
