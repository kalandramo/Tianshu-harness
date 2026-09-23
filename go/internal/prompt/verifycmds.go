package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/trust"
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
//   - **项目未授信 → 返回空 config + 单次提示**（安全门，见下）
//   - 文件不存在 / JSON 非法 / verify 节缺失 → 返回空 config
//
// ## 信任门（2026-09-23 补，此前是缺失的安全缺口）
//
// **为什么必须有**：verify 声明是**仓库内容驱动的执行通道**——它被渲染进
// system prompt 后，模型会照着执行里面的命令。未授信项目因此能通过
// `.rivet-config.json` 让 agent 跑任意命令，**绕过所有审批**（声明来自
// 仓库、agent 认为它是项目约定）。
//
// 这是**接线缺口而非移植缺口**：本文件的 `LoadDeclaredVerify` 早已存在，
// 但其注释曾写「Go 侧暂无 trust store，故不实现该门」——该前提在第四十七刀
// （`internal/trust` 落地）后**已过期**。本刀据实修正。
//
// **对账 TS 的 memo 语义**：TS 用 `memo` 缓存**已授信**的解析结果，但
// **刻意不缓存未授信结果**——这样 `/trust` 授信后无需 invalidate 即刻生效。
// Go 侧当前无 memo（每次重读文件），故天然满足该语义；若将来加缓存，
// **必须保持「不缓存未授信结果」**。
func LoadDeclaredVerify(cwd string) VerifyConfig {
	path := findProjectConfig(cwd)
	if path == "" {
		return VerifyConfig{}
	}

	// 信任门：声明命令是仓库内容驱动的执行通道，未授信项目一律不声明。
	// 与 TS 的 `loadDeclaredVerify` 逐字对账（projectDir = dirname(path)）。
	projectDir := filepath.Dir(path)
	if !trust.IsProjectTrusted(projectDir) {
		trust.NotifyUntrustedOnce("config", projectDir, nil)
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
