// Package config —— 用户全局配置读取（最小子集：permissions）。
//
// 对账 TS `src/config/paths.ts` + `src/config/manager.ts` + `src/config/schema.ts`。
//
// **本包的范围（有意收窄）**：只读 `agent.permissions`（allow / deny）。
// 完整的 `internal/config`（默认 → `~/.rivet` → 项目三层合并 + schema 校验
// + 迁移）是独立大工程，见 HANDOFF 的待办清单。
//
// **为什么需要本包**：第五十二刀交付了 deny 规则的**判定层 + 消费端**
// （`agent.IsToolDenied` + `loop.go` 决策链），但**生产者缺失**——
// `Config.Permissions` 只能由代码/测试注入，用户在 `~/.rivet/config.json`
// 里写的 deny 规则**读不到**。本包补上这一环，让链路端到端闭合。
//
// **对账 TS 的关键点**：
//
//  1. permissions 在 `agent.permissions`（**不是**顶层）——
//     `permissionsSchema` 挂在 `agent` 下（`schema.ts:555`）
//  2. 路径解析走 `paths.ts` 的三级优先：RIVET_CONFIG_PATH > RIVET_HOME >
//     平台默认。**Windows 用 `%LOCALAPPDATA%\.rivet` 而非 `~/.rivet`**——
//     这条最容易错，错了就永远读不到 Windows 用户的配置
//  3. **项目层 `.rivet-config.json` 不含 permissions**（`workspace-schema.ts`
//     零命中）——permissions 只存在于用户全局层。故本包只读用户层即
//     **语义完整**，不是"最小子集"
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/kalandramo/tianshu/go/internal/agent"
)

// DefaultRivetHome 返回平台默认的 .rivet 数据根（忽略 RIVET_HOME）。
//
// 对账 TS `defaultRivetHome()`（`paths.ts:27-32`）：
//
//	if (process.platform === 'win32') {
//	  return join(process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local'), '.rivet')
//	}
//	return join(homedir(), '.rivet')
//
// **Windows 分支是易错点**：TS 在 win32 上用 `%LOCALAPPDATA%\.rivet`，
// 而**不是** `~/.rivet`。若 Go 侧统一用 home，Windows 用户配置永远读不到。
// `%LOCALAPPDATA%` 未设时回退到 `<home>\AppData\Local`（与 TS 一致）。
func DefaultRivetHome() string {
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, ".rivet")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Local", ".rivet")
		}
		return filepath.Join(".rivet")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".rivet")
	}
	return ".rivet"
}

// RivetHome 返回当前生效的数据根（RIVET_HOME 优先，否则平台默认）。
//
// 对账 TS `rivetHome()`（`paths.ts:35-37`）：
//
//	return process.env.RIVET_HOME || defaultRivetHome()
func RivetHome() string {
	if home := os.Getenv("RIVET_HOME"); home != "" {
		return home
	}
	return DefaultRivetHome()
}

// UserConfigPath 返回用户全局配置文件的路径。
//
// 对账 TS `userConfigPath()`（`paths.ts:40-63`）：
//
//	const fromEnv = process.env.RIVET_CONFIG_PATH
//	if (fromEnv) return fromEnv
//	return join(rivetHome(), 'config.json')
//
// **有意省略的部分**：TS 在 `RIVET_HOME` 指向新位置但那里没有 config.json
// 时，会向 stderr 打一条「旧配置还在默认位置」的提示。那是**桌面端 UX
// 辅助**（引导用户迁移），Go 侧 CLI 无对应场景，省略。此差异不影响
// 路径解析语义——本函数返回值与 TS 逐字节一致。
func UserConfigPath() string {
	if p := os.Getenv("RIVET_CONFIG_PATH"); p != "" {
		return p
	}
	return filepath.Join(RivetHome(), "config.json")
}

// userConfigFile 是配置文件的 JSON 形态（只取本包需要的字段）。
//
// 对账 TS `Config` 的嵌套结构（`schema.ts:555`）：
//
//	agent: {
//	  permissions: permissionsSchema.default({})
//	}
//
// **为什么用嵌套结构体而非 map**：JSON 解码到结构体能自动忽略无关字段，
// 且嵌套层级由类型**强制**表达——写错层级会解码为空（测试
// `★嵌套位置必须是agent.permissions` 钉住这一点）。
type userConfigFile struct {
	Agent struct {
		Permissions *permissionsRaw `json:"permissions"`
	} `json:"agent"`
}

// permissionsRaw 对账 TS `permissionsSchema` 的 allow / deny 部分。
//
// 对账 `schema.ts:289-292`：
//
//	allow: z.array(permissionAllowRuleSchema).default([]),
//	deny:  z.array(permissionAllowRuleSchema).default([]),
//
// **`*permissionsRaw` 指针是关键**：区分「无 permissions 字段」（nil）与
// 「有 permissions 但是空规则」（非 nil 空切片）。前者返回 nil
// （表示未配置），后者返回空规则集——两者的调用方行为可能不同。
type permissionsRaw struct {
	Allow []agent.PermissionAllowRule `json:"allow"`
	Deny  []agent.PermissionAllowRule `json:"deny"`
}

// LoadPermissions 从用户全局配置读取 `agent.permissions`。
//
// 返回：
//   - `(nil, nil)`：配置文件不存在，或配置里没有 permissions 字段
//     ——**这是正常情况**，表示用户未配置权限规则
//   - `(perms, nil)`：成功读到 permissions（即使规则为空切片）
//   - `(nil, err)`：文件存在但解析失败（非法 JSON / 类型不符）
//
// **fail-closed 语义**：任何错误都返回 nil permissions（= 无规则 =
// 不误拦），绝不 panic。理由：配置问题不该崩掉 agent 运行时；且 nil 的
// 行为方向是安全的（deny 门跳过 = 保持现状，而非意外拒绝一切）。
//
// **对账 TS 的一处刻意差异**：TS 用 zod schema 校验，字段类型不符时
// **整个配置加载失败**（抛异常/返回默认值），而本函数只让 permissions
// 部分失效（其他配置仍可用）。差异理由是 scope：本包只读 permissions，
// 不该因它的问题影响其他配置面。
func LoadPermissions() (*agent.PermissionConfig, error) {
	path := UserConfigPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 配置文件不存在 = 用户未配置。正常情况，不是错误。
			return nil, nil
		}
		// 权限不足 / IO 错误等——报告但由调用方决定是否致命。
		return nil, fmt.Errorf("读取用户配置 %s 失败：%w", path, err)
	}

	var cfg userConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析用户配置 %s 失败：%w", path, err)
	}

	if cfg.Agent.Permissions == nil {
		// 无 permissions 字段——用户未配置权限规则。
		return nil, nil
	}

	// 规范化 nil 切片为空切片：让「有 permissions 字段」的语义在调用方
	// 侧一致（`perms != nil` 即可判断"用户配了"）。
	allow := cfg.Agent.Permissions.Allow
	if allow == nil {
		allow = []agent.PermissionAllowRule{}
	}
	deny := cfg.Agent.Permissions.Deny
	if deny == nil {
		deny = []agent.PermissionAllowRule{}
	}

	return &agent.PermissionConfig{Allow: allow, Deny: deny}, nil
}

// LoadPermissionsOrNil 是 LoadPermissions 的便利包装：忽略错误。
//
// **使用场景**：装配层（`cmd/tianshu/main.go`）不想因配置问题中断启动。
// 错误被丢弃——若需要诊断，直接调 LoadPermissions。
func LoadPermissionsOrNil() *agent.PermissionConfig {
	perms, err := LoadPermissions()
	if err != nil {
		return nil
	}
	return perms
}
