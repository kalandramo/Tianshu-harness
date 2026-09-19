package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// VerifyRoute 对账 TS 的 VerifyRoute（schema.ts 的 routes 元素）。
type VerifyRoute struct {
	Match string `json:"match"`
	Run   string `json:"run"`
	Kind  string `json:"kind"`
}

// VerifyConfig 对账 TS 的 VerifyConfig（schema.ts:1103）。
//
// 全部字段可选——空 config 表示无声明。
type VerifyConfig struct {
	Test      string        `json:"test"`
	Build     string        `json:"build"`
	Typecheck string        `json:"typecheck"`
	Lint      string        `json:"lint"`
	Routes    []VerifyRoute `json:"routes"`
}

// RenderDeclaredVerify 复刻 volatile.ts 的 renderDeclaredVerify 的**渲染部分**。
//
// 对账点：
//   - 四种 kind 的**固定顺序** test → build → typecheck → lint（与 config 里
//     的书写顺序无关）
//   - trim 后为空的项被过滤
//   - routes 追加在四种之后，格式 `${kind} [${match}]: ${run}`
//   - 整体经 escapeXml（只转 & < > "，& 最先）
//   - 无任何声明时返回 ""（对应 TS 的 null）
//
// 读取部分在 LoadDeclaredVerify（见下）——拆开是为了让渲染可对账。
func RenderDeclaredVerify(cfg VerifyConfig) string {
	lines := []string{}
	for _, kv := range []struct{ kind, cmd string }{
		{"test", cfg.Test},
		{"build", cfg.Build},
		{"typecheck", cfg.Typecheck},
		{"lint", cfg.Lint},
	} {
		if trimmed := trimSpaceUnicode(kv.cmd); trimmed != "" {
			lines = append(lines, kv.kind+": "+trimmed)
		}
	}
	for _, r := range cfg.Routes {
		lines = append(lines, r.Kind+" ["+r.Match+"]: "+r.Run)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<verify-commands source=\".rivet-config.json\">\n" +
		EscapeXML(strings.Join(lines, "\n")) + "\n</verify-commands>"
}

// projectConfigFile 对账 TS 的 PROJECT_CONFIG_FILE。
const projectConfigFile = ".rivet-config.json"

// LoadDeclaredVerify 读取 cwd（或其祖先，最多 20 层）的 .rivet-config.json
// 的 verify 节。
//
// 对账 TS 的 findProjectConfig + loadDeclaredVerify：
//   - 向上查找最多 20 层
//   - 文件不存在 / JSON 非法 / verify 节缺失 → 返回空 config
//
// **未移植的部分（有意）**：TS 的信任门（isProjectTrusted）——未授信项目
// 返回空声明。Go 侧暂无 trust store，故不实现该门。这是已知的行为差异，
// 记于 HANDOFF；若后续需要，应在此处加。
func LoadDeclaredVerify(cwd string) VerifyConfig {
	path := findProjectConfig(cwd)
	if path == "" {
		return VerifyConfig{}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return VerifyConfig{}
	}
	var wrapper struct {
		Verify *VerifyConfig `json:"verify"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return VerifyConfig{}
	}
	if wrapper.Verify == nil {
		return VerifyConfig{}
	}
	return *wrapper.Verify
}

// findProjectConfig 对账 TS 的 findProjectConfig —— 向上最多 20 层查找。
func findProjectConfig(startDir string) string {
	dir := startDir
	for i := 0; i < 20; i++ {
		candidate := filepath.Join(dir, projectConfigFile)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 到达根
		}
		dir = parent
	}
	return ""
}

// DetectDeclaredVerifyBlock 是生产入口：读取 + 渲染。
//
// 返回 "" 表示无声明（对应 TS 的 null）。
func DetectDeclaredVerifyBlock(cwd string) string {
	return RenderDeclaredVerify(LoadDeclaredVerify(cwd))
}
