package mcp

import (
	"strings"
	"testing"
)

// config_semantics_test.go —— 第一百一十刀 W1：配置语义。
//
// 本刀的核心判据：**用户写进配置的每个字段，要么生效、要么被明确拒绝**，
// 不存在「写了但不生效且无提示」的中间态。
//
// 覆盖三条真缺陷（编号对账计划的核验结论表）：
//   - finding #1：`enabled` 缺席语义（TS `.default(true)` vs Go bool 零值）
//   - finding #2：`url` 型 server 静默失效
//   - finding #3：`timeoutMs` 是死配置

// ---- finding #1：enabled 三态 ----

// TestConfigEnabledDefaultsTrue —— ★ 缺席 enabled = 启用（对齐 TS `.default(true)`）。
//
// **这是本刀最重要的一条**：TS 的 `mcpConfigSchema` 写的是
// `enabled: z.boolean().default(true)`（`src/mcp/config.ts:83`），
// 故 `{"mcp": {"servers": {...}}}`（不写 enabled）在 TS 里是**启用**的。
// Go 侧此前用 `bool` 承载 → 缺席与显式 false 都塌缩为 false → **整个 MCP 被静默关掉**。
func TestConfigEnabledDefaultsTrue(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "servers": { "fs": { "command": "npx" } }
	  }
	}`)

	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !cfg.EnabledOrDefault() {
		t.Error("enabled 键缺席应视作 true（对齐 TS .default(true)）——否则用户照 TS 写配置会被静默关掉全部 MCP")
	}
	if len(cfg.Servers) != 1 {
		t.Errorf("server 应被保留，实得 %v", cfg.Servers)
	}
}

// TestConfigEnabledExplicitFalseWins —— 显式 false 优先级最高。
//
// 与上一条配对：证明「缺席=true」不是「恒 true」。
// 若只测上一条，一个「恒返回 true」的实现也能过——那是真空断言。
func TestConfigEnabledExplicitFalseWins(t *testing.T) {
	p := writeConfig(t, `{"mcp": {"enabled": false, "servers": {"fs": {"command": "npx"}}}}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if cfg.EnabledOrDefault() {
		t.Error("显式 false 应被尊重")
	}
}

// TestConfigEnabledExplicitTrue —— 显式 true（冗余但合法）。
func TestConfigEnabledExplicitTrue(t *testing.T) {
	p := writeConfig(t, `{"mcp": {"enabled": true, "servers": {}}}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !cfg.EnabledOrDefault() {
		t.Error("显式 true 应为 true")
	}
}

// TestConfigEnabledZeroValueIsEnabled —— 零值 Config{} 也视作启用。
//
// **为什么这条重要**：`Config{}` 是「用户没配过 MCP」的表示，而
// `NewManager(Config{}, "")` 在既有测试里被多处构造。
// 「零值 = 未配置 = 不启用」会让 `TestManagerDisabledConfigIsNoop`
// 与「配置省略 enabled」两条语义打架。
// 本刀明确：**「是否启用」由 EnabledOrDefault 判定，且默认启用**；
// 而「有没有 server 可连」由 `len(Servers) == 0` 判定——两者正交。
func TestConfigEnabledZeroValueIsEnabled(t *testing.T) {
	var cfg Config
	if !cfg.EnabledOrDefault() {
		t.Error("零值 Config 应视作启用（未配置 MCP 时 Servers 为空，自然无工具）")
	}
}

// ---- finding #2：url 型 server 明确拒绝 ----

// TestConfigURLServerRejectedWithReason —— ★ url 型 server 被拒绝且给出原因。
//
// TS 支持 url 型（`src/mcp/config.ts:46` 的 `url` 字段 + `:59-67` 的 refine），
// Go 侧本刀不实现 HTTP 传输。但**不能静默失效**——用户配了它，
// 就该被告知「没生效，因为未实现」。
func TestConfigURLServerRejectedWithReason(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "servers": { "remote1": { "url": "https://example.com/mcp" } }
	  }
	}`)

	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, ok := cfg.Servers["remote1"]; ok {
		t.Error("url 型 server 不应留在 Servers（无法连接）")
	}
	if len(cfg.Unsupported) != 1 {
		t.Fatalf("应记录 1 条 Unsupported，实得 %v", cfg.Unsupported)
	}
	u := cfg.Unsupported[0]
	if u.ID != "remote1" {
		t.Errorf("Unsupported.ID 应为 remote1，实得 %q", u.ID)
	}
	if !strings.Contains(u.Reason, "未实现") {
		t.Errorf("原因应说明「未实现」（让用户知道是能力缺失而非配置错），实得 %q", u.Reason)
	}
}

// TestConfigBothCommandAndURLRejected —— 两者同给 → 拒绝（对账 TS refine 互斥）。
//
// 对账 `src/mcp/config.ts:59-67` 的 refine：
// 「MCP server must have either "command" (stdio) or "url" ..., but not both」。
func TestConfigBothCommandAndURLRejected(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "servers": { "bad": { "command": "npx", "url": "https://example.com/mcp" } }
	  }
	}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, ok := cfg.Servers["bad"]; ok {
		t.Error("command 与 url 同给应被拒绝（对账 TS refine）")
	}
	if len(cfg.Unsupported) != 1 || !strings.Contains(cfg.Unsupported[0].Reason, "不可同时") {
		t.Errorf("应记录互斥冲突，实得 %v", cfg.Unsupported)
	}
}

// TestConfigNeitherCommandNorURLRejected —— 两者皆无 → 拒绝。
func TestConfigNeitherCommandNorURLRejected(t *testing.T) {
	p := writeConfig(t, `{"mcp": {"servers": {"empty": {}}}}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, ok := cfg.Servers["empty"]; ok {
		t.Error("既无 command 又无 url 应被拒绝")
	}
	if len(cfg.Unsupported) != 1 {
		t.Errorf("应记录 1 条 Unsupported，实得 %v", cfg.Unsupported)
	}
}

// TestConfigMixedStillConnectsStdio —— ★ 单点失败不阻塞其余。
//
// **为什么这条是核心**：若「一个 url server」导致整个配置加载失败
// （方案 B），用户的 stdio server 会连带不可用。本刀选方案 A 正是为此。
func TestConfigMixedStillConnectsStdio(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": {
	    "servers": {
	      "good":   { "command": "npx" },
	      "remote": { "url": "https://example.com/mcp" }
	    }
	  }
	}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, ok := cfg.Servers["good"]; !ok {
		t.Error("★ stdio server 应仍然可用（一个 url server 不该拖垮其余）")
	}
	if _, ok := cfg.Servers["remote"]; ok {
		t.Error("url server 应被移除")
	}
	if len(cfg.Unsupported) != 1 {
		t.Errorf("应有 1 条诊断，实得 %v", cfg.Unsupported)
	}
}

// TestConfigStdioUnaffectedByValidation —— 纯 stdio 配置零变化（回归钉子）。
func TestConfigStdioUnaffectedByValidation(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": { "servers": { "a": {"command":"x","args":["1"]}, "b": {"command":"y"} } }
	}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Errorf("两个 stdio server 都应保留，实得 %v", cfg.Servers)
	}
	if len(cfg.Unsupported) != 0 {
		t.Errorf("纯 stdio 配置不该有诊断，实得 %v", cfg.Unsupported)
	}
}

// ---- finding #2 补充：Disabled 与 validate 的交互 ----

// TestConfigDisabledServerStillValidated —— disabled 的 server 不做语义校验。
//
// 理由：用户明确禁用了它，此时「command 为空」不构成问题——
// 报告一条「需 command 或 url」的诊断反而会误导（用户根本没打算用它）。
func TestConfigDisabledServerStillValidated(t *testing.T) {
	p := writeConfig(t, `{
	  "mcp": { "servers": { "off": {"disabled": true} } }
	}`)
	cfg, err := LoadConfigFromFile(p)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(cfg.Unsupported) != 0 {
		t.Errorf("disabled server 不该被报为 Unsupported，实得 %v", cfg.Unsupported)
	}
	if _, ok := cfg.Servers["off"]; !ok {
		t.Error("disabled server 应保留在 Servers（由 Manager 跳过）")
	}
}

// ---- finding #3：timeoutMs 生效 ----

// TestConfigTimeoutAppliedToManager —— ★ timeoutMs 传到 Manager 的请求超时。
//
// 此前 `Config.Timeout()` 零生产调用方、`manager.go` 三处硬编码
// `DefaultTimeoutMS` → 用户在配置里写的 timeoutMs 完全不生效。
func TestConfigTimeoutAppliedToManager(t *testing.T) {
	cfg := Config{TimeoutMS: 1234}
	m := NewManager(cfg, "")
	if m.requestTimeoutMS != 1234 {
		t.Errorf("★ Manager 应采用配置的 timeoutMs，实得 %d（配置未生效）", m.requestTimeoutMS)
	}
}

// TestConfigTimeoutDefaultsWhenUnset —— 未配置时用默认。
func TestConfigTimeoutDefaultsWhenUnset(t *testing.T) {
	m := NewManager(Config{}, "")
	if m.requestTimeoutMS != DefaultTimeoutMS {
		t.Errorf("未配置时应为 %d，实得 %d", DefaultTimeoutMS, m.requestTimeoutMS)
	}
}
