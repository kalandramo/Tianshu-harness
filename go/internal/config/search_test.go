package config

import (
	"os"
	"path/filepath"
	"testing"
)

// search_test.go —— SearchConfig + 密钥解析（第九十七刀 · W2）。
//
// 对账 TS `src/config/schema.ts` 的 `searchSchema` +
// `src/tools/web-search/build-backends.ts` 的 `resolveSearchKey`。

// ── LoadSearch 默认值（对账 searchSchema 的 zod .default）────────────

// TestLoadSearchDefaultsWhenFileMissing —— 配置文件不存在 → 全默认值。
func TestLoadSearchDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(t.TempDir(), "nope.json"))
	cfg := LoadSearch()

	if len(cfg.Backends) != 2 || cfg.Backends[0] != "bing" || cfg.Backends[1] != "duckduckgo" {
		t.Errorf("默认后端链应为 [bing duckduckgo]，实得 %#v", cfg.Backends)
	}
	if cfg.BraveAPIKeyEnv != "BRAVE_API_KEY" {
		t.Errorf("默认 braveApiKeyEnv 应为 BRAVE_API_KEY，实得 %q", cfg.BraveAPIKeyEnv)
	}
	if cfg.TavilyAPIKeyEnv != "TAVILY_API_KEY" {
		t.Errorf("默认 tavilyApiKeyEnv 实得 %q", cfg.TavilyAPIKeyEnv)
	}
	if cfg.BochaAPIKeyEnv != "BOCHA_API_KEY" {
		t.Errorf("默认 bochaApiKeyEnv 实得 %q", cfg.BochaAPIKeyEnv)
	}
	if cfg.TimeoutMs != 15000 {
		t.Errorf("默认 timeoutMs 应为 15000，实得 %d", cfg.TimeoutMs)
	}
}

// TestLoadSearchExplicitValues —— 显式配置覆盖默认值。
func TestLoadSearchExplicitValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeJSON(t, path, `{
	  "search": {
	    "backends": ["brave", "duckduckgo"],
	    "braveApiKeyEnv": "MY_BRAVE",
	    "tavilyApiKeyEnv": "MY_TAVILY",
	    "bochaApiKeyEnv": "MY_BOCHA",
	    "braveApiKey": "inline-brave",
	    "timeoutMs": 3000,
	    "region": "cn"
	  }
	}`)
	t.Setenv("RIVET_CONFIG_PATH", path)

	cfg := LoadSearch()
	if len(cfg.Backends) != 2 || cfg.Backends[0] != "brave" {
		t.Errorf("实得 %#v", cfg.Backends)
	}
	if cfg.BraveAPIKeyEnv != "MY_BRAVE" {
		t.Errorf("实得 %q", cfg.BraveAPIKeyEnv)
	}
	if cfg.BraveAPIKey != "inline-brave" {
		t.Errorf("inline key 实得 %q", cfg.BraveAPIKey)
	}
	if cfg.TimeoutMs != 3000 {
		t.Errorf("实得 %d", cfg.TimeoutMs)
	}
	if cfg.Region != "cn" {
		t.Errorf("实得 %q", cfg.Region)
	}
}

// TestLoadSearchInvalidJSONFallsBackToDefaults —— 坏 JSON → 默认值，不 panic。
func TestLoadSearchInvalidJSONFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeJSON(t, path, `{ this is not json`)
	t.Setenv("RIVET_CONFIG_PATH", path)

	cfg := LoadSearch()
	if len(cfg.Backends) != 2 || cfg.Backends[0] != "bing" {
		t.Errorf("坏配置应回退默认，实得 %#v", cfg.Backends)
	}
}

// ── ResolveSearchKey 四层回退（对账 build-backends.ts resolveSearchKey）──

// TestResolveSearchKeyEnvFallbackChain —— 后三层零密码学路径。
func TestResolveSearchKeyEnvFallbackChain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "absent.json"))

	t.Run("第 2 层：显式 env 变量名", func(t *testing.T) {
		t.Setenv("MY_BRAVE_KEY", "from-explicit-env")
		cfg := SearchConfig{BraveAPIKeyEnv: "MY_BRAVE_KEY"}
		if got := ResolveSearchKey(cfg, "brave"); got != "from-explicit-env" {
			t.Errorf("实得 %q", got)
		}
	})

	t.Run("第 3 层：标准变量名 BRAVE_API_KEY", func(t *testing.T) {
		t.Setenv("BRAVE_API_KEY", "from-standard-env")
		cfg := SearchConfig{BraveAPIKeyEnv: "NOT_SET_ANYWHERE"}
		if got := ResolveSearchKey(cfg, "brave"); got != "from-standard-env" {
			t.Errorf("实得 %q", got)
		}
	})

	t.Run("第 1 层：inline config 优先于 env", func(t *testing.T) {
		t.Setenv("BRAVE_API_KEY", "from-env")
		cfg := SearchConfig{BraveAPIKey: "from-inline", BraveAPIKeyEnv: "BRAVE_API_KEY"}
		if got := ResolveSearchKey(cfg, "brave"); got != "from-inline" {
			t.Errorf("inline 应优先于 env，实得 %q", got)
		}
	})

	t.Run("三层皆空 → 空字符串（isAvailable 将为 false）", func(t *testing.T) {
		cfg := SearchConfig{BraveAPIKeyEnv: "ABSENT_VAR"}
		if got := ResolveSearchKey(cfg, "brave"); got != "" {
			t.Errorf("实得 %q", got)
		}
	})
}

// TestResolveSearchKeyPerBackend —— 每个后端走各自的键。
func TestResolveSearchKeyPerBackend(t *testing.T) {
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("BRAVE_API_KEY", "b")
	t.Setenv("TAVILY_API_KEY", "t")
	t.Setenv("BOCHA_API_KEY", "c")

	cfg := SearchConfig{
		BraveAPIKeyEnv:  "BRAVE_API_KEY",
		TavilyAPIKeyEnv: "TAVILY_API_KEY",
		BochaAPIKeyEnv:  "BOCHA_API_KEY",
	}
	for _, c := range []struct {
		backend SearchBackendName
		want    string
	}{
		{BackendBrave, "b"}, {BackendTavily, "t"}, {BackendBocha, "c"},
	} {
		if got := ResolveSearchKey(cfg, c.backend); got != c.want {
			t.Errorf("%s 实得 %q，期望 %q", c.backend, got, c.want)
		}
	}
}

// ── keyRef → secrets.json（第 0 层，最优先）─────────────────────────

// TestResolveSearchKeyKeyRefBeatsInline —— keyRef 命中时**优先于** inline。
func TestResolveSearchKeyKeyRefBeatsInline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeJSON(t, path, `{"search":{"braveKeyRef":"search:brave","braveApiKey":"inline"}}`)
	t.Setenv("RIVET_CONFIG_PATH", path)
	// 明文格式的 secrets.json（旧格式——TS decodeSecret 原样返回）
	writeSecretsPlain(t, dir, `{"version":1,"keys":{"search:brave":"from-secrets"}}`)

	cfg := LoadSearch()
	if got := ResolveSearchKey(cfg, "brave"); got != "from-secrets" {
		t.Errorf("keyRef 应优先于 inline，实得 %q", got)
	}
}

// TestResolveSearchKeyKeyRefMissFallsThrough —— keyRef 指向不存在 → 落到 inline。
func TestResolveSearchKeyKeyRefMissFallsThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeJSON(t, path, `{"search":{"braveKeyRef":"search:missing","braveApiKey":"inline"}}`)
	t.Setenv("RIVET_CONFIG_PATH", path)
	writeSecretsPlain(t, dir, `{"version":1,"keys":{"other":"x"}}`)

	cfg := LoadSearch()
	if got := ResolveSearchKey(cfg, "brave"); got != "inline" {
		t.Errorf("keyRef 未命中应落 inline，实得 %q", got)
	}
}

// TestResolveSearchKeyEncryptedSecretsFallsThrough —— **加密格式 fail-open**。
//
// # 这是本刀最重要的语义钉子
//
// Go 侧**不实现 AES-256-GCM 解密**（对账 TS `auth/secure-store.ts` 315 行 +
// OS 密钥库）。当 secrets.json 是加密信封时，`ReadSecret` 必须返回未命中，
// 让回退链自然落到 env——**等价于「用户没配 secrets.json」**。
//
// 上游 TS `secrets-store.ts` 文件头明写 `Reads are fail-open`，
// 故这是**符合设计的收窄**，而非静默失败。
func TestResolveSearchKeyEncryptedSecretsFallsThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeJSON(t, path, `{"search":{"braveKeyRef":"search:brave"}}`)
	t.Setenv("RIVET_CONFIG_PATH", path)
	// 加密信封格式
	writeJSON(t, filepath.Join(dir, "secrets.json"),
		`{"v":1,"s":"aes-256-gcm","d":"AAAA","b":"file"}`)
	t.Setenv("BRAVE_API_KEY", "from-env-after-encrypted-store")

	cfg := LoadSearch()
	if got := ResolveSearchKey(cfg, "brave"); got != "from-env-after-encrypted-store" {
		t.Errorf("加密 secrets 应 fail-open 落到 env，实得 %q", got)
	}
}

// TestSecretsEncryptedDetection —— 加密格式**可见**（不静默）。
//
// Go 侧读不了加密 key 是**真实降级**，必须可诊断——否则用户会困惑
// 「我在桌面端配了 key，为什么 CLI 说后端不可用」。
func TestSecretsEncryptedDetection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "config.json"))

	// 无文件 → 不算加密
	if SecretsIsEncrypted() {
		t.Error("无 secrets.json 时不应报加密")
	}
	// 明文 → 不算加密
	writeSecretsPlain(t, dir, `{"version":1,"keys":{}}`)
	if SecretsIsEncrypted() {
		t.Error("明文格式不应报加密")
	}
	// 信封 → 报加密
	writeJSON(t, filepath.Join(dir, "secrets.json"), `{"v":1,"s":"aes-256-gcm","d":"AA"}`)
	if !SecretsIsEncrypted() {
		t.Error("信封格式应报加密")
	}
}

// TestSecretsPathHonorsConfigPath —— secrets.json 与 config.json 同目录。
func TestSecretsPathHonorsConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "sub", "config.json"))
	want := filepath.Join(dir, "sub", "secrets.json")
	if got := SecretsPath(); got != want {
		t.Errorf("实得 %q，期望 %q", got, want)
	}
}

// TestReadSecretPlainFormat —— 明文格式正常读取。
func TestReadSecretPlainFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "config.json"))
	writeSecretsPlain(t, dir, `{"version":1,"keys":{"k1":"v1","k2":""}}`)

	if got := ReadSecret("k1"); got != "v1" {
		t.Errorf("实得 %q", got)
	}
	// 空值不算命中（对账 TS：`value.length > 0`）
	if got := ReadSecret("k2"); got != "" {
		t.Errorf("空值应返回空，实得 %q", got)
	}
	if got := ReadSecret("absent"); got != "" {
		t.Errorf("不存在的 key 应返回空，实得 %q", got)
	}
}

// TestReadSecretCorruptFailsOpen —— 坏文件 fail-open，不 panic。
func TestReadSecretCorruptFailsOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "config.json"))
	writeJSON(t, filepath.Join(dir, "secrets.json"), `{ broken`)

	if got := ReadSecret("any"); got != "" {
		t.Errorf("坏文件应 fail-open 返回空，实得 %q", got)
	}
}

// ── 辅助 ────────────────────────────────────────────────────────────────

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}

// writeSecretsPlain 写与 config.json 同目录的**明文** secrets.json。
func writeSecretsPlain(t *testing.T, dir, content string) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, "secrets.json"), content)
}

// TestReadSecretEncryptedNeverMisreadAsPlain —— **对抗性输入**：信封+明文混杂字段。
//
// # 为什么补这条（变异反证 M2' 的教训）
//
// 首版加密测试用的信封 `{v,s,d,b}` 恰好**不含** `version`/`keys` 字段，
// 于是删掉 `isEnvelope` 检查后，仍被 `file.Version != 1` 拦住——**结果相同**，
// 测试看不出 `isEnvelope` 有没有在工作（双层防护的等价变异）。
//
// 本用例构造一个**同时像信封又像明文**的文件：若 `isEnvelope` 检查存在，
// 必须拒绝读取（返回空）；若不存在，就会把密文信封当明文读，返回 `keys` 里
// 的伪造值——**降级为静默误读**，这正是要防的。
func TestReadSecretEncryptedNeverMisreadAsPlain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "config.json"))
	// 信封判据三项齐备（v 数字 / s 算法名 / d 字符串）+ 明文格式字段
	writeJSON(t, filepath.Join(dir, "secrets.json"),
		`{"v":1,"s":"aes-256-gcm","d":"AAAA","version":1,"keys":{"search:brave":"MUST-NOT-BE-READ"}}`)

	if got := ReadSecret("search:brave"); got != "" {
		t.Errorf("加密信封绝不能被当明文读，实得 %q（应为空）", got)
	}
}

// TestSecretsIsEncryptedRejectsWrongAlg —— 算法名不符**不算**加密信封。
//
// # 为什么补这条（变异反证 M5' 的教训）
//
// 首版测试只用了正确算法名 `aes-256-gcm`——把判据放松成「只要 s 存在就算」
// 照样通过（红 0）。必须覆盖 `s` 为**其它算法名**时才判定得。
func TestSecretsIsEncryptedRejectsWrongAlg(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_CONFIG_PATH", filepath.Join(dir, "config.json"))

	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"正确算法名 → 加密", `{"v":1,"s":"aes-256-gcm","d":"AA"}`, true},
		{"算法名不符 → 不是加密", `{"v":1,"s":"chacha20-poly1305","d":"AA"}`, false},
		{"缺 s → 不是加密", `{"v":1,"d":"AA"}`, false},
		{"缺 d → 不是加密", `{"v":1,"s":"aes-256-gcm"}`, false},
		{"v 非数字 → 不是加密", `{"v":"1","s":"aes-256-gcm","d":"AA"}`, false},
		{"d 非字符串 → 不是加密", `{"v":1,"s":"aes-256-gcm","d":123}`, false},
		{"明文格式 → 不是加密", `{"version":1,"keys":{"k":"v"}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeJSON(t, filepath.Join(dir, "secrets.json"), c.content)
			if got := SecretsIsEncrypted(); got != c.want {
				t.Errorf("SecretsIsEncrypted() = %v，期望 %v（输入 %s）", got, c.want, c.content)
			}
		})
	}
}
