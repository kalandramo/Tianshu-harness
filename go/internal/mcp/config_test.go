package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// config_test.go —— 从配置文件读取 `mcp` 段（W4 装配的前置）。
//
// 对账 TS `src/config/schema.ts:1038` 的顶层键：
//
//	mcp: mcpConfigSchema.default({}),
//
// **层级注意**：`mcp` 是**顶层**键（与 `agent` / `tools` / `verify` 并列），
// **不是** `agent.mcp`。这与权限配置不同（`agent.permissions` 是嵌套的，
// 见 `internal/config/permissions.go` 的 `userConfigFile`）。
// 写错层级会静默解析为空——下面的测试专门钉住这一点。

// writeConfig 写一个临时配置文件并返回路径。
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadConfigFromFileBasic —— 基本读取：enabled + servers + command。
func TestLoadConfigFromFileBasic(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "enabled": true,
	    "servers": {
	      "fs": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"] }
	    }
	  }
	}`)

	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !cfg.Enabled {
		t.Error("enabled 应为 true")
	}
	fs, ok := cfg.Servers["fs"]
	if !ok {
		t.Fatalf("应有 fs server，实得 %v", cfg.Servers)
	}
	if fs.Command != "npx" {
		t.Errorf("command 应为 npx，实得 %q", fs.Command)
	}
	if len(fs.Args) != 3 || fs.Args[0] != "-y" {
		t.Errorf("args 应透传，实得 %v", fs.Args)
	}
}

// TestLoadConfigTopLevelNotNested —— ★ 钉住层级：`mcp` 是顶层键。
//
// 若有人误写成 `{"agent":{"mcp":{...}}}`，配置应**读不到**（解析为空），
// 而不是「碰巧读到」——本用例同时验证正确层级能读到，
// 这样「写错层级」与「功能没实现」两种失败不会互相掩盖。
func TestLoadConfigTopLevelNotNested(t *testing.T) {
	// 正确层级：mcp 在顶层
	good := writeConfig(t, `{"mcp":{"enabled":true,"servers":{"s":{"command":"echo"}}}}`)
	cfg, err := LoadConfigFromFile(good)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(cfg.Servers) != 1 {
		t.Fatalf("顶层 mcp 应被读到，实得 %v", cfg.Servers)
	}

	// 错误层级：mcp 嵌在 agent 下 → 读不到
	bad := writeConfig(t, `{"agent":{"mcp":{"enabled":true,"servers":{"s":{"command":"echo"}}}}}`)
	cfg2, err := LoadConfigFromFile(bad)
	if err != nil {
		t.Fatalf("读取不该报错（只是读不到）：%v", err)
	}
	if len(cfg2.Servers) != 0 {
		t.Errorf("agent.mcp 是错误层级，不该被读到，实得 %v", cfg2.Servers)
	}
}

// TestLoadConfigMissingFile —— 文件不存在 = 未配置（正常，非错误）。
//
// 对账 `internal/config/permissions.go` 的既有语义：`os.IsNotExist` → `(nil, nil)`。
func TestLoadConfigMissingFile(t *testing.T) {
	cfg, err := LoadConfigFromFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if cfg.Enabled {
		t.Error("未配置时 Enabled 应为 false（零值）")
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("未配置时无 server，实得 %v", cfg.Servers)
	}
}

// TestLoadConfigNoMcpKey —— 有配置文件但无 mcp 键 = 未配置。
func TestLoadConfigNoMcpKey(t *testing.T) {
	p := writeConfig(t, `{"agent":{"permissions":{"allow":[]}}}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if cfg.Enabled || len(cfg.Servers) != 0 {
		t.Errorf("无 mcp 键时应为空配置，实得 %+v", cfg)
	}
}

// TestLoadConfigMalformedJSON —— 坏 JSON 报错（fail-closed，不静默吞）。
func TestLoadConfigMalformedJSON(t *testing.T) {
	p := writeConfig(t, `{"mcp": {`)
	if _, err := LoadConfigFromFile(p); err == nil {
		t.Error("坏 JSON 应报错（而非静默返回空配置——那会让用户以为配好了）")
	}
}

// TestLoadConfigServerFields —— 全字段透传（含 disabled / cwd / env）。
func TestLoadConfigServerFields(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "enabled": true,
	    "timeoutMs": 30000,
	    "servers": {
	      "a": {"command":"x","args":["1"],"env":{"K":"V"},"cwd":"/tmp","disabled":true}
	    }
	  }
	}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if cfg.TimeoutMS != 30000 {
		t.Errorf("timeoutMs 应透传，实得 %d", cfg.TimeoutMS)
	}
	a := cfg.Servers["a"]
	if a.Env["K"] != "V" {
		t.Errorf("env 应透传，实得 %v", a.Env)
	}
	if a.Cwd != "/tmp" {
		t.Errorf("cwd 应透传，实得 %q", a.Cwd)
	}
	if !a.Disabled {
		t.Error("disabled 应透传")
	}
}

// TestLoadConfigEmptyServersObject —— `servers: {}` 合法（不是错误）。
func TestLoadConfigEmptyServersObject(t *testing.T) {
	p := writeConfig(t, `{"mcp":{"enabled":true,"servers":{}}}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("空 servers 不该报错：%v", err)
	}
	if !cfg.Enabled {
		t.Error("enabled 应为 true")
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("servers 应为空，实得 %v", cfg.Servers)
	}
}

// TestConfigJSONRoundTrip —— Config 的 JSON 形态与 TS 一致（字段名对齐）。
func TestConfigJSONRoundTrip(t *testing.T) {
	raw := `{"enabled":true,"timeoutMs":1000,"servers":{"s":{"command":"c"}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if !cfg.Enabled || cfg.TimeoutMS != 1000 || cfg.Servers["s"].Command != "c" {
		t.Errorf("字段名与 TS 不一致，实得 %+v", cfg)
	}
}
