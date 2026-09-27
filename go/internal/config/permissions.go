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

	"github.com/kalandramo/tianshu/go/internal/agent"
	"github.com/kalandramo/tianshu/go/internal/rivetpath"
)

// 路径解析已下沉到叶子包 `internal/rivetpath`（第九十七刀 W5）。
//
// # 为什么要下沉
//
// `internal/config` 含 `LoadPermissions`（依赖 `internal/agent`），而
// `agent → hooks → tools`。任何 `tools` 下的代码若要读配置，就会形成
// `tools → config → agent → hooks → tools` 的**导入环**（W5 实测撞上）。
//
// 把**纯路径解析**（零项目内依赖）下沉后，需要路径的包 import `rivetpath`
// 即可，不再拉进 `config` 的整条依赖链。
//
// 本文件保留同名包装函数，**外部 API 逐字不变**（`cmd/tianshu/main.go`
// 等既有调用方无需改动）。

// DefaultRivetHome 返回平台默认的 .rivet 数据根（忽略 RIVET_HOME）。
func DefaultRivetHome() string { return rivetpath.DefaultRivetHome() }

// RivetHome 返回当前生效的数据根（RIVET_HOME 优先，否则平台默认）。
func RivetHome() string { return rivetpath.RivetHome() }

// UserConfigPath 返回用户全局配置文件的路径。
func UserConfigPath() string { return rivetpath.UserConfigPath() }

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
	// Bash 对账 TS `permissions.bash`（`schema.ts:293`）——含 allowlist / denylist。
	//
	// **此前缺失的后果**：用户在 `permissions.bash.denylist` 里写的命令前缀
	// 被**静默忽略**（`PermissionConfig` 连字段都没有）。这是第五十二刀
	// 「deny 规则不生效」的同族——安全机制不存在，而非没接线。
	Bash *struct {
		Allowlist []string `json:"allowlist"`
		Denylist  []string `json:"denylist"`
	} `json:"bash"`
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

	var bash *agent.BashPermissionConfig
	if cfg.Agent.Permissions.Bash != nil {
		allow := cfg.Agent.Permissions.Bash.Allowlist
		if allow == nil {
			allow = []string{}
		}
		deny := cfg.Agent.Permissions.Bash.Denylist
		if deny == nil {
			deny = []string{}
		}
		bash = &agent.BashPermissionConfig{Allowlist: allow, Denylist: deny}
	}

	return &agent.PermissionConfig{Allow: allow, Deny: deny, Bash: bash}, nil
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
