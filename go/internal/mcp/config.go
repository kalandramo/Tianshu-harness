package mcp

import (
	"encoding/json"
	"fmt"
	"os"
)

// config.go —— 从配置文件读取 `mcp` 段。
//
// 对账 TS `src/config/schema.ts:1038` 的顶层键 `mcp: mcpConfigSchema.default({})`。
//
// # 层级（易错点，已用测试钉住）
//
// `mcp` 是**顶层**键，与 `agent` / `tools` / `verify` 并列：
//
//	{ "mcp": { "enabled": true, "servers": {...} } }
//
// **不是** `agent.mcp`。这与既有 `internal/config/permissions.go` 的
// `agent.permissions`（嵌套两层）不同——见 `userConfigFile` 的注释。
// 写错层级会**静默解析为空**，故 `TestLoadConfigTopLevelNotNested` 同时
// 验证「正确层级读得到」与「错误层级读不到」，两个方向都不放过。
//
// # 与既有配置读取的分工
//
// 本包只管自己的段（`mcp`），与 `config.LoadPermissions` 只管 `agent.permissions`
// 同款。**不造「统一配置加载器」**——那需要把 20+ 个 schema 都搬过来，
// 远超本刀范围；且各包的失败语义不同（权限失败要 fail-closed 关掉危险能力，
// MCP 失败只是没工具可用）。让每个包读自己那段，读失败各自决定后果。

// configFileShape 是配置文件里本包关心的形态。
//
// 用嵌套结构体而非 `map[string]any`：JSON 解码自动忽略无关字段，
// 且**顶层键名由类型强制**（写错字段名/tag 会解码为空，测试会红）。
type configFileShape struct {
	Mcp *Config `json:"mcp"`
}

// LoadConfigFromFile 从指定路径读取 `mcp` 配置。
//
// 语义（对齐既有 `config.LoadPermissions` 的约定）：
//   - 文件不存在 → `(空 Config, nil)`：用户未配置，**不是错误**
//   - 无 `mcp` 键 → `(空 Config, nil)`：同上
//   - JSON 坏 → `(空 Config, err)`：**报错**，不静默吞
//
// **为什么坏 JSON 要报错**（与「文件不存在」区别对待）：
// 文件存在说明用户**有意配置**了；此时解析失败若静默返回空配置，
// 用户会以为「配好了但没生效」，而真正的问题是语法错误。
// 大声失败才能让用户修。这是 fail-closed 在配置面的一贯取向。
func LoadConfigFromFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("读取 MCP 配置 %s 失败：%w", path, err)
	}

	var shape configFileShape
	if err := json.Unmarshal(data, &shape); err != nil {
		return Config{}, fmt.Errorf("解析 MCP 配置 %s 失败：%w", path, err)
	}

	if shape.Mcp == nil {
		return Config{}, nil
	}

	cfg := *shape.Mcp
	// 规范化：servers 为 nil 时给空 map（让「有 mcp 段」的语义在调用方一致）
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerConfig{}
	}
	return cfg, nil
}
