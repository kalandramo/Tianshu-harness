// Package agent —— 权限规则判定层。
//
// 对账 TS `src/agent/permissions.ts`（前 68 行：结构 + 判定函数）。
//
// **为什么需要这一层**：TS 的 `tool-pipeline.ts` 把 deny 规则放在决策树的
// **最前面**，注释写明「Deny rules always win, even in
// dangerously-skip-permissions」——它是**覆盖一切**的门，优先于
// `unconditionalApproval` / 硬闸门 / 路径授权。Go 侧此前**完全没有**
// permissions 面（Config 无字段、无判定函数），意味着用户在配置里写的
// `permissions.deny` 被**静默忽略**——用户设的硬边界不生效。
//
// **与第五十刀的同类性**：那刀修的是「`NeedsApproval` 全仓零调用者」；
// 本刀修的是「deny 规则零实现」。同一类问题：安全机制存在但没接线。
//
// **本刀范围**（最小闭合）：结构 + `PatternMatches` + `ParamsMatch` +
// `IsToolAllowed` / `IsToolDenied`。**不含** bash 分段能力
// （`splitShellSegments` / `IsBashCommandAllowlisted` / `IsBashCommandDenied`）
// ——那些依赖 shell 分段，是独立工作量，见 HANDOFF 的遗留段。
package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// PermissionAllowRule 是一条权限规则（allow 或 deny 通用）。
//
// 对账 TS `PermissionAllowRule`。`Params` 的每个键值对都必须匹配才成立；
// 值为通配模式（`*` 通配，但不跨 shell 操作符——见 wildcardExclude）。
type PermissionAllowRule struct {
	// Tool 是工具名模式（支持 `*`，如 `read_*`）。
	Tool string `json:"tool"`
	// Params 是参数模式表（可省略 = 只按工具名匹配）。
	Params map[string]string `json:"params,omitempty"`
}

// PermissionConfig 是用户配置的权限面。
//
// 对账 TS `PermissionConfig`（仅 allow/deny 部分——bash 子配置属
// `splitShellSegments` 家族，本刀不含）。
type PermissionConfig struct {
	// Allow 是放行规则（匹配则跳过审批）。
	Allow []PermissionAllowRule `json:"allow"`
	// Deny 覆盖 allow 规则与审批档位——**任何档位都不能绕过**。
	Deny []PermissionAllowRule `json:"deny"`
}

// wildcardExclude 是 `*` 通配符**不匹配**的字符类（TS 侧逐字节对账）。
//
// TS 源码（`permissions.ts:30`）：
//
//	const WILDCARD_EXCLUDE = `[^&|;<>()$\\x60\\\\!"']`
//
// 模板字面量求值后，Go 里等价的原始字符串是 `[^&|;<>()$\x60\\!"']`。
//
// **为什么排除这些**：防止通配符跨 token 匹配——`git status*` 绝不能匹配
// `git status&&curl evil`。通配符不得跨越 shell 操作符。空白**不**排除，
// 所以 `--short` 这类正常参数仍能匹配。
//
// **RE2 兼容性**（探针 `zz_probe_permregex_test.go` 实测）：negated
// character class + 量词，无 lookaround，Go RE2 原生支持。
const wildcardExclude = `[^&|;<>()$\x60\\!"']`

// patternEscapeRe 匹配需要转义的**正则元字符**（TS 侧逐字节对账）。
//
// TS 源码（`permissions.ts:33`）：`/[.+?^${}()|[\]\\]/g`
//
// **注意不含 `*`**：`*` 必须留给通配符替换，转义它就杀死了通配语义。
// 这也是**不能用 Go 的 `regexp.QuoteMeta`** 的原因——QuoteMeta 会转义 `*`
// （探针实测 `QuoteMeta("a+b") = "a\\+b"`，且会输出 `a\\*b`），语义不符。
var patternEscapeRe = regexp.MustCompile(`[.+?^${}()|\[\]\\]`)

// PatternMatches 判定一个通配模式是否匹配给定值。
//
// 对账 TS `patternMatches`（`permissions.ts:33-36`）：
//
//	const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&')
//	                        .replace(/\*/g, `${WILDCARD_EXCLUDE}*`)
//	return new RegExp(`^${escaped}$`).test(value)
//
// **与 TS 的一处刻意差异**：TS 的 `new RegExp` 在模式非法时**抛异常**；
// Go 侧 `regexp.Compile` 返回 error，本函数选择**返回 false 而非 panic**。
// 理由：模式来自用户配置，配置错误不该崩掉 agent 运行时——fail-closed
// （不匹配 = 不授权）方向正确。此差异在 oracle 测试中显式记录。
func PatternMatches(pattern, value string) bool {
	escaped := patternEscapeRe.ReplaceAllString(pattern, `\$0`)
	escaped = strings.ReplaceAll(escaped, "*", wildcardExclude+"*")

	re, err := regexp.Compile("^" + escaped + "$")
	if err != nil {
		// 用户配置里的非法模式：不 panic，判为不匹配（fail-closed）。
		return false
	}
	return re.MatchString(value)
}

// ParamsMatch 判定实际参数是否满足期望的参数模式表。
//
// 对账 TS `paramsMatch`（`permissions.ts:38-46`）：
//
//	if (!expected) return true          // 无期望 = 全部通过
//	return Object.entries(expected).every(([key, pattern]) => {
//	  const value = actual[key]
//	  return typeof value === 'string' && patternMatches(pattern, value)
//	})
//
// **`typeof value === 'string'` 是硬条件**：实际参数里该键不存在（nil）、
// 或是数字/布尔/对象，一律不匹配。Go 侧用类型断言表达同一语义——
// `map[string]any` 取出后必须是 `string` 才继续。
func ParamsMatch(expected map[string]string, actual map[string]any) bool {
	if len(expected) == 0 {
		return true
	}

	for key, pattern := range expected {
		raw, ok := actual[key]
		if !ok {
			return false
		}
		value, ok := raw.(string)
		if !ok {
			return false
		}
		if !PatternMatches(pattern, value) {
			return false
		}
	}
	return true
}

// IsToolAllowed 判定工具调用是否命中规则集（任一规则命中即通过）。
//
// 对账 TS `isToolAllowed`（`permissions.ts:54-58`）：
//
//	if (!rules?.length) return false    // 空规则 = 不授权（fail-closed）
//	return rules.some(rule =>
//	  patternMatches(rule.tool, toolName) && paramsMatch(rule.params, input))
//
// **空规则返回 false** 是 fail-closed 语义：没有规则就不是"全部放行"，
// 而是"没有依据放行"。调用方（allow 判定）与 deny 判定共用本函数，
// 但语义相反——见 `IsToolDenied` 的注释。
func IsToolAllowed(toolName string, input map[string]any, rules []PermissionAllowRule) bool {
	if len(rules) == 0 {
		return false
	}

	for _, rule := range rules {
		if PatternMatches(rule.Tool, toolName) && ParamsMatch(rule.Params, input) {
			return true
		}
	}
	return false
}

// IsToolDenied 判定工具调用是否命中 deny 规则。
//
// 对账 TS `isToolDenied`（`permissions.ts:61-63`）——**它就是
// `isToolAllowed` 的别名**（TS 侧逐字节如此），因为两者的匹配逻辑相同，
// 差异只在调用方的语义解读：命中 allow 规则 = 放行；命中 deny 规则 = 拒绝。
//
// **为什么不合并成一个函数**：语义不同的两个概念共用实现是合理的，
// 但共用**名字**会让调用点失去自解释性——`IsToolDenied(...)` 读起来
// 明确是"是否被禁"，而 `IsToolAllowed(..., denyRules)` 需要读者自己
// 意识到"这里的 allowed 其实是 denied"。
func IsToolDenied(toolName string, input map[string]any, rules []PermissionAllowRule) bool {
	return IsToolAllowed(toolName, input, rules)
}

// DeniedRuleReason 生成 deny 拒绝的模型可见文案（对账 TS 措辞）。
//
// 对账 TS `tool-pipeline.ts:1138-1139` 的 denyMsg：
//
//	Tool execution denied: ${tu.name} matches an active deny rule.
//	This is a user-configured permission boundary, not a dead end —
//	continue via another route: gather evidence with read-only tools
//	(read_file/grep/glob), write probes under .rivet/scratch/, or ask
//	the user to adjust the permissions deny rules if this operation is
//	genuinely required.
//
// **为什么逐字对账**：这段文案的要点是「这不是死路」——它告诉模型
// **换路而非重试**（重试只会撞同一道门，浪费 turn 预算）。这是
// 第五十刀「指令性非重试拒绝」的同款形态。
func DeniedRuleReason(toolName string) string {
	return fmt.Sprintf(
		"Tool execution denied: %s matches an active deny rule. "+
			"This is a user-configured permission boundary, not a dead end — "+
			"continue via another route: gather evidence with read-only tools "+
			"(read_file/grep/glob), write probes under .rivet/scratch/, or ask "+
			"the user to adjust the permissions deny rules if this operation is "+
			"genuinely required.",
		toolName,
	)
}
