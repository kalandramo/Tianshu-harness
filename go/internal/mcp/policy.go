package mcp

import "regexp"

// policy.go —— MCP 工具名与策略判定。
//
// 对账 `src/mcp/wrapper.ts:7-11`（`mcpToolName`）与
// `src/mcp/policy.ts:11-65`（`parseMcpTool` / `evaluateMcpPolicy`）。

// Capability 是 MCP 工具声明的能力。
//
// 对账 `src/mcp/policy.ts:1` 的 `McpCapability`。
type Capability string

const (
	CapabilityUnknown Capability = "unknown"
	CapabilityRead    Capability = "read"
	CapabilityWrite   Capability = "write"
	CapabilityExecute Capability = "execute"
	CapabilityNetwork Capability = "network"
)

// Action 是策略判定结果。
//
// 对账 `src/mcp/policy.ts:2` 的 `McpPolicyAction`。
type Action string

const (
	ActionAllow   Action = "allow"
	ActionConfirm Action = "confirm"
	ActionBlock   Action = "block"
	ActionRequire Action = "require"
)

// mcpToolNameRe 对账 `src/mcp/policy.ts:20` 的 `/^mcp__(.+)__(.+)$/`。
//
// **与 `agent/approval_assess.go` 的同名正则重复**（那里也有一份
// `mcpToolNameRe`）。它们服务不同层：那处做**风险定级**（本包不依赖 agent
// 包，反向依赖会成环），本处做**策略判定**。两份同源同形，
// 由各自的测试钉住（见 `TestParseMcpTool` 与 agent 包对应用例）。
var mcpToolNameRe = regexp.MustCompile(`^mcp__(.+)__(.+)$`)

// ParseMcpTool 从工具名解析出 (serverID, toolName)。
//
// 对账 `policy.ts:19-22` 的 `parseMcpTool`。非 MCP 工具返回 ok=false。
//
// **贪婪匹配的后果（有意对账 TS）**：正则的 `(.+)` 是贪婪的，故
// `mcp__a__b__c` 会解析成 serverID=`a__b`、tool=`c`——与 TS 逐字一致。
// 这也是 `ToolName` **必须转义**的原因（见其注释）。
func ParseMcpTool(toolName string) (serverID, mcpToolName string, ok bool) {
	m := mcpToolNameRe.FindStringSubmatch(toolName)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ToolName 构造 MCP 工具名。
//
// 对账 `src/mcp/wrapper.ts:7-11`：
//
//	const safeServerId = serverId.replaceAll('__', '_')
//	const safeToolName = toolName.replaceAll('__', '_')
//	return `mcp__${safeServerId}__${safeToolName}`
//
// **为什么必须转义**：分隔符就是 `__`。若 id 本身含 `__`，`ParseMcpTool` 的
// 贪婪正则会把归属解析到**错误的位置**——风险策略挂到错的 server 上。
func ToolName(serverID, toolName string) string {
	safeServer := replaceAllDoubleUnderscore(serverID)
	safeTool := replaceAllDoubleUnderscore(toolName)
	return "mcp__" + safeServer + "__" + safeTool
}

// replaceAllDoubleUnderscore 对账 JS 的 `replaceAll('__','_')`。
//
// # ★ 语义以**实测**为准（探针推翻了我的初版猜测）
//
// `node -e` 实测（非推断）：
//
//	'a___b'    -> 'a__b'      ← 三连下划线**不是**变成单个 '_'
//	'c___d'    -> 'c__d'
//	'my__server' -> 'my_server'
//	'a____b'   -> 'a__b'
//
// 即：**单遍、非重叠、从左扫**。`'a___b'` 里 `___` 的前两个 `_` 配成一对
// 替换为 `_`，第三个 `_` 未被再配对 → 结果 `a` + `_` + `_` + `b` = `a__b`。
//
// 初版我写成「循环替换直到无 `__`」——那会把 `a___b` 变成 `a_b`，**与 TS 分叉**。
// 单遍实现即正确（`replaceDoubleUnderscoreOnce`）。
func replaceAllDoubleUnderscore(s string) string {
	return replaceDoubleUnderscoreOnce(s)
}

// replaceDoubleUnderscoreOnce 单遍非重叠替换（对账 JS `replaceAll` 的扫描语义）。
func replaceDoubleUnderscoreOnce(s string) string {
	var out []byte
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '_' && s[i+1] == '_' {
			out = append(out, '_')
			i += 2 // 匹配后从匹配**之后**继续 —— 与 JS 非重叠语义一致
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// PolicyInput 是策略判定的输入。
//
// 对账 `policy.ts:5-13` 的 `McpPolicyInput`。
type PolicyInput struct {
	ToolName string
	// DeclaredCapability 来自本地 MCP 配置；未声明 = unknown。
	DeclaredCapability      Capability
	TrustedServers          []string
	BlockedTools            []string
	AllowedTools            []string
	MustConfirmCapabilities []Capability
}

// PolicyDecision 是策略判定的输出。
//
// 对账 `policy.ts:15-21` 的 `McpPolicyDecision`。
type PolicyDecision struct {
	Action      Action
	ServerID    string
	McpToolName string
	Capability  Capability
	Reason      string
}

// EvaluatePolicy 判定一次 MCP 工具调用。
//
// 对账 `policy.ts:23-65` 的 `evaluateMcpPolicy`。**分支顺序敏感**（照搬 TS）：
//
//	① 非 MCP 工具        → allow（不归本策略管）
//	② blockedTools 命中  → block（**带出路契约**，见下）
//	③ allowedTools 命中  → allow（显式允许优先于能力判定）
//	④ capability unknown → confirm（逐次确认）
//	⑤ 未受信 且 非只读    → confirm
//	⑥ mustConfirm 命中   → confirm
//	⑦ 兜底               → allow
//
// **出路契约**（对账 TS 注释「Not a dead end」）：被 block 时 reason 必须
// 给出替代路径——被拦不该是死路。
func EvaluatePolicy(in PolicyInput) PolicyDecision {
	serverID, mcpTool, ok := ParseMcpTool(in.ToolName)
	if !ok {
		return PolicyDecision{
			Action:     ActionAllow,
			Capability: CapabilityRead,
			Reason:     "Not an MCP tool.",
		}
	}

	capability := in.DeclaredCapability
	if capability == "" {
		capability = CapabilityUnknown
	}

	if containsStr(in.BlockedTools, in.ToolName) {
		return PolicyDecision{
			Action:      ActionBlock,
			ServerID:    serverID,
			McpToolName: mcpTool,
			Capability:  capability,
			Reason: "MCP tool is explicitly blocked by user config. Not a dead end — " +
				"achieve the goal via built-in tools (read_file/grep/bash) or another MCP tool, " +
				"or ask the user to unblock \"" + mcpTool + "\" if it is genuinely required.",
		}
	}

	if containsStr(in.AllowedTools, in.ToolName) {
		return PolicyDecision{
			Action:      ActionAllow,
			ServerID:    serverID,
			McpToolName: mcpTool,
			Capability:  capability,
			Reason:      "MCP tool is explicitly allowed.",
		}
	}

	if capability == CapabilityUnknown {
		return PolicyDecision{
			Action:      ActionConfirm,
			ServerID:    serverID,
			McpToolName: mcpTool,
			Capability:  capability,
			Reason: "MCP tool " + mcpTool + " has no declared capability. " +
				"Confirm each call or declare it in the server policy.",
		}
	}

	trusted := containsStr(in.TrustedServers, serverID)
	if !trusted && capability != CapabilityRead {
		return PolicyDecision{
			Action:      ActionConfirm,
			ServerID:    serverID,
			McpToolName: mcpTool,
			Capability:  capability,
			Reason: "MCP server " + serverID + " is unknown and requests " +
				string(capability) + " capability.",
		}
	}

	if containsCap(in.MustConfirmCapabilities, capability) {
		return PolicyDecision{
			Action:      ActionConfirm,
			ServerID:    serverID,
			McpToolName: mcpTool,
			Capability:  capability,
			Reason:      "MCP " + string(capability) + " capability requires confirmation.",
		}
	}

	return PolicyDecision{
		Action:      ActionAllow,
		ServerID:    serverID,
		McpToolName: mcpTool,
		Capability:  capability,
		Reason:      "MCP policy allows this tool.",
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsCap(list []Capability, c Capability) bool {
	for _, x := range list {
		if x == c {
			return true
		}
	}
	return false
}
