// Package config —— `search.*` 配置读取 + 密钥解析（第九十七刀 · W2）。
//
// 对账 TS：
//   - `src/config/schema.ts` 的 `searchSchema`（`schema.ts:654-682`）
//   - `src/tools/web-search/build-backends.ts` 的 `resolveSearchKey`
//   - `src/config/secrets-store.ts` + `src/auth/secure-store.ts`
//
// # 密钥解析的四层回退链（对账 TS `resolveSearchKey`）
//
//  0. keyRef → secrets.json（**Go 侧仅支持旧明文格式**，见下）
//  1. inline `search.<backend>ApiKey`（运行时物化值 / 旧版明文配置）
//  2. `search.<backend>ApiKeyEnv` 命名的环境变量
//  3. 标准 `<BACKEND>_API_KEY` 环境变量
//
// # ★ 有意收窄：secrets.json 的加密格式不解密
//
// TS 的 `secrets.json` 内容自加固起是 **AES-256-GCM 信封**（数据密钥托管在
// OS 密钥库），解密实现在 `src/auth/secure-store.ts`（315 行）+ `token-store.ts`。
// Go 侧**不实现该密码学栈**——故：
//
//   - 旧**明文**格式（`{version:1,keys:{}}`）：正常读取（TS 对此原样返回）
//   - **加密信封**（`{v,s,d,b}`）：`ReadSecret` 返回未命中 → 回退链自然落到 env
//
// **为什么这是符合设计的收窄而非静默失败**：上游 `secrets-store.ts` 文件头
// 明写 `Reads are fail-open: a missing/corrupt store yields undefined and the
// caller's existing fallback chain takes over`。「读不到 → 走回退」正是其设计。
//
// **代价（诚实披露）**：桌面端 UI 存的 key（走 keyRef → secrets.json 加密路径）
// 在 Go 侧读不到。CLI 用户用环境变量完全不受影响。`SecretsIsEncrypted()`
// 让这一降级**可诊断**——调用方可以据此提示用户「改用环境变量」，
// 而不是只报「后端不可用」。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// SearchConfig 对账 TS `searchSchema`。
//
// **零值语义**：`LoadSearch()` 会填好默认值；直接构造 `SearchConfig{}` 时
// `Backends` 为空切片（调用方需自行处理，或经 `LoadSearch`）。
type SearchConfig struct {
	// Backends 是有序后端链。首个可用且返回非空结果的后端胜出。
	// 对账 TS 默认 `['bing','duckduckgo']`——覆盖国内（cn.bing.com 直连）
	// 与海外（DDG），无需 API key。
	Backends []string

	// 各后端的 API key 环境变量名（可被配置覆盖）。
	BraveAPIKeyEnv  string
	TavilyAPIKeyEnv string
	BochaAPIKeyEnv  string

	// Inline API key（**运行时物化值**——明文只活在内存）。
	// 对账 TS：`loadConfig` 按 `<backend>KeyRef` 从 secrets.json 读回；
	// config.json 只留 keyRef 指针，绝不落明文（issue #220）。
	BraveAPIKey  string
	TavilyAPIKey string
	BochaAPIKey  string

	// secrets.json 中的 keyRef 指针（如 `search:brave`）。
	BraveKeyRef  string
	TavilyKeyRef string
	BochaKeyRef  string

	// TimeoutMs 是**单后端**请求超时（毫秒）。
	TimeoutMs int

	// Region 是可选的区域提示（目前仅 Brave 的 `country` 参数使用）。
	Region string
}

// 默认值（对账 TS searchSchema 的 zod `.default`）。
const (
	defaultSearchTimeoutMs = 15_000
	defaultBackendBing     = "bing"
	defaultBackendDDG      = "duckduckgo"
)

// searchRaw 是 config.json 里 `search` 字段的 JSON 形态。
//
// **为什么用指针**：区分「无 search 字段」（nil → 全默认）与「有 search 但字段为空」。
type searchRaw struct {
	Backends        []string `json:"backends"`
	BraveAPIKeyEnv  *string  `json:"braveApiKeyEnv"`
	TavilyAPIKeyEnv *string  `json:"tavilyApiKeyEnv"`
	BochaAPIKeyEnv  *string  `json:"bochaApiKeyEnv"`
	BraveAPIKey     *string  `json:"braveApiKey"`
	TavilyAPIKey    *string  `json:"tavilyApiKey"`
	BochaAPIKey     *string  `json:"bochaApiKey"`
	BraveKeyRef     *string  `json:"braveKeyRef"`
	TavilyKeyRef    *string  `json:"tavilyKeyRef"`
	BochaKeyRef     *string  `json:"bochaKeyRef"`
	TimeoutMs       *int     `json:"timeoutMs"`
	Region          *string  `json:"region"`
}

// LoadSearch 从用户全局配置读 `search` 段，缺失字段填默认值。
//
// **fail-open**：文件不存在 / 坏 JSON → 返回全默认配置（不返错）。
// 理由与 `LoadPermissions` 一致——配置问题不该崩掉运行时；且默认配置
// 本身就是可用的（bing + duckduckgo 零配置）。
func LoadSearch() SearchConfig {
	cfg := SearchConfig{
		Backends:        []string{defaultBackendBing, defaultBackendDDG},
		BraveAPIKeyEnv:  "BRAVE_API_KEY",
		TavilyAPIKeyEnv: "TAVILY_API_KEY",
		BochaAPIKeyEnv:  "BOCHA_API_KEY",
		TimeoutMs:       defaultSearchTimeoutMs,
	}

	data, err := os.ReadFile(UserConfigPath())
	if err != nil {
		return cfg
	}

	var file struct {
		Search *searchRaw `json:"search"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Search == nil {
		return cfg
	}

	s := file.Search
	if len(s.Backends) > 0 {
		cfg.Backends = s.Backends
	}
	assignStr(&cfg.BraveAPIKeyEnv, s.BraveAPIKeyEnv)
	assignStr(&cfg.TavilyAPIKeyEnv, s.TavilyAPIKeyEnv)
	assignStr(&cfg.BochaAPIKeyEnv, s.BochaAPIKeyEnv)
	assignStr(&cfg.BraveAPIKey, s.BraveAPIKey)
	assignStr(&cfg.TavilyAPIKey, s.TavilyAPIKey)
	assignStr(&cfg.BochaAPIKey, s.BochaAPIKey)
	assignStr(&cfg.BraveKeyRef, s.BraveKeyRef)
	assignStr(&cfg.TavilyKeyRef, s.TavilyKeyRef)
	assignStr(&cfg.BochaKeyRef, s.BochaKeyRef)
	assignStr(&cfg.Region, s.Region)
	if s.TimeoutMs != nil && *s.TimeoutMs > 0 {
		cfg.TimeoutMs = *s.TimeoutMs
	}
	return cfg
}

func assignStr(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

// SearchBackendName 是 `ResolveSearchKey` 支持的后端名（需 key 的三个）。
type SearchBackendName string

// 需要 API key 的后端（bing/duckduckgo 是抓取型，零配置）。
const (
	BackendBrave  SearchBackendName = "brave"
	BackendTavily SearchBackendName = "tavily"
	BackendBocha  SearchBackendName = "bocha"
)

// ResolveSearchKey 解析某后端的 API key（四层回退链，对账 TS `resolveSearchKey`）。
//
// 返回空字符串表示「未配置」——调用方的 `isAvailable()` 应为 false，链会跳过。
//
// # 与 TS 的差异
//
// TS 的第 0 层（keyRef → secrets.json）能解密 AES 信封；Go 侧只读旧明文格式
// （见包注释的「有意收窄」）。加密信封时本函数**自然落到第 1-3 层**——
// 这与「用户没配 secrets.json」在 TS 侧的行为一致。
func ResolveSearchKey(cfg SearchConfig, backend SearchBackendName) string {
	// 0. keyRef 指针 → secrets.json（**优先于 inline**，对账 TS 同序）
	var keyRef string
	switch backend {
	case BackendBrave:
		keyRef = cfg.BraveKeyRef
	case BackendTavily:
		keyRef = cfg.TavilyKeyRef
	case BackendBocha:
		keyRef = cfg.BochaKeyRef
	}
	if keyRef != "" {
		if secret := ReadSecret(keyRef); secret != "" {
			return secret
		}
	}

	// 1. inline config value（运行时物化值）
	var inline string
	switch backend {
	case BackendBrave:
		inline = cfg.BraveAPIKey
	case BackendTavily:
		inline = cfg.TavilyAPIKey
	case BackendBocha:
		inline = cfg.BochaAPIKey
	}
	if inline != "" {
		return inline
	}

	// 2. 显式 env 变量名（apiKeyEnv 字段）
	var envName string
	switch backend {
	case BackendBrave:
		envName = cfg.BraveAPIKeyEnv
	case BackendTavily:
		envName = cfg.TavilyAPIKeyEnv
	case BackendBocha:
		envName = cfg.BochaAPIKeyEnv
	}
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}

	// 3. 标准变量名回退（apiKeyEnv 丢失 / 手动编辑场景）
	return os.Getenv(strings.ToUpper(string(backend)) + "_API_KEY")
}

// ── secrets.json ────────────────────────────────────────────────────────

// SecretsPath 返回 secrets.json 的路径（与 config.json 同目录）。
//
// 对账 TS `secretsPath`（`secrets-store.ts:28-34`）：优先用显式 base，
// 否则取 `dirname(userConfigPath())`；userConfigPath 抛异常时退回 rivetHome。
func SecretsPath() string {
	return filepath.Join(filepath.Dir(UserConfigPath()), "secrets.json")
}

// secretsEnvelope 用于**探测**加密信封（对账 TS `decodeSecret` 的 isEnvelope 判据）。
//
// TS 判据：`typeof env.d === 'string' && typeof env.v === 'number' && env.s === ENVELOPE_ALG`。
// Go 侧用 json.RawMessage 拿到原始值再逐项判，避免类型不符时整体解码失败。
type secretsEnvelope struct {
	V json.RawMessage `json:"v"`
	S json.RawMessage `json:"s"`
	D json.RawMessage `json:"d"`
}

// SecretsIsEncrypted 报告 secrets.json 是否为**加密信封**格式。
//
// # 为什么要这个函数
//
// Go 侧读不了加密 key 是**真实降级**。若只让 `ReadSecret` 静默返回空，
// 用户会困惑「我在桌面端配了 key，为什么 CLI 说后端不可用」。
// 本函数让调用方能把这条降级**说清楚**（提示用户改用环境变量）。
func SecretsIsEncrypted() bool {
	raw, err := os.ReadFile(SecretsPath())
	if err != nil {
		return false // 无文件不算加密
	}
	return isEnvelope(raw)
}

// isEnvelope 对账 TS `decodeSecret` 的 isEnvelope 判定。
func isEnvelope(raw []byte) bool {
	var env secretsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	// d 必须是字符串
	var d string
	if env.D == nil || json.Unmarshal(env.D, &d) != nil {
		return false
	}
	// v 必须是数字
	var v float64
	if env.V == nil || json.Unmarshal(env.V, &v) != nil {
		return false
	}
	// s 必须是算法名（对账 TS ENVELOPE_ALG）
	var s string
	if env.S == nil || json.Unmarshal(env.S, &s) != nil {
		return false
	}
	return s == envelopeAlg
}

// envelopeAlg 对账 TS `ENVELOPE_ALG`。
const envelopeAlg = "aes-256-gcm"

// secretsFile 是**旧明文格式** secrets.json 的形态。
type secretsFile struct {
	Version int               `json:"version"`
	Keys    map[string]string `json:"keys"`
}

// ReadSecret 从 secrets.json 读一个 keyRef 的值。
//
// **fail-open**（对账 TS `readStore` 的 try/catch + `readSecret`）：
// 文件缺失、坏 JSON、版本不符、加密信封 → 一律返回空字符串。
//
// **不 panic、不返错**——调用方的回退链会自然接管。
func ReadSecret(keyRef string) string {
	raw, err := os.ReadFile(SecretsPath())
	if err != nil {
		return ""
	}
	// 加密信封 → Go 侧不解密（见包注释）。返回空让回退链接管。
	if isEnvelope(raw) {
		return ""
	}
	var file secretsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return ""
	}
	if file.Version != 1 || file.Keys == nil {
		return ""
	}
	// 空值不算命中（对账 TS：`value.length > 0`）
	return file.Keys[keyRef]
}
