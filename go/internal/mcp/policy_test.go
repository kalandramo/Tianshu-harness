package mcp

import "testing"

// policy_test.go —— MCP 工具名与策略判定（V3）。
//
// 对账 `src/mcp/wrapper.ts:7-11`（`mcpToolName`）与
// `src/mcp/policy.ts:19-22`（`parseMcpTool`）。

// TestToolNameEscapesDoubleUnderscore —— ★ 转义规则（对账 wrapper.ts:8-9）。
//
// `serverId` 与 `toolName` 各自先 `replaceAll('__','_')`。
// **为什么必须转义**：分隔符就是 `__`，若 id 本身含 `__`，
// 解析时的 `/^mcp__(.+)__(.+)$/` 会贪婪匹配到**错误的位置**——
// 工具名与 server 归属错乱，风险策略会挂到错的 server 上。
//
// **期望值来自 `node -e` 实测**（非推断）：
//
//	'a___b'.replaceAll('__','_') === 'a__b'   ← 三连不是变单个
//	'a____b'.replaceAll('__','_') === 'a__b'  ← 四连变两个
func TestToolNameEscapesDoubleUnderscore(t *testing.T) {
	cases := []struct {
		serverID string
		toolName string
		want     string
	}{
		{"srv", "read", "mcp__srv__read"},
		// id 含双下划线 → 压成单下划线
		{"my__server", "do__thing", "mcp__my_server__do_thing"},
		// 三连：单遍非重叠 → 前两个配对被替换，第三个留下（实测 a___b -> a__b）
		{"a___b", "c___d", "mcp__a__b__c__d"},
		// 四连 → 两个 `__` 各被替换（实测 a____b -> a__b）
		{"a____b", "x", "mcp__a__b__x"},
	}
	for _, c := range cases {
		if got := ToolName(c.serverID, c.toolName); got != c.want {
			t.Errorf("ToolName(%q,%q) = %q, want %q", c.serverID, c.toolName, got, c.want)
		}
	}
}

// TestParseMcpTool —— 反向解析（对账 policy.ts:19-22 的正则）。
func TestParseMcpTool(t *testing.T) {
	cases := []struct {
		in       string
		wantSrv  string
		wantTool string
		wantOK   bool
	}{
		{"mcp__srv__read", "srv", "read", true},
		{"mcp__my_server__do_thing", "my_server", "do_thing", true},
		// 非 MCP 工具
		{"read_file", "", "", false},
		{"mcp__only", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		srv, tool, ok := ParseMcpTool(c.in)
		if ok != c.wantOK || srv != c.wantSrv || tool != c.wantTool {
			t.Errorf("ParseMcpTool(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, srv, tool, ok, c.wantSrv, c.wantTool, c.wantOK)
		}
	}
}

// TestEvaluatePolicyBlockedHasEscapePath —— ★ 拦截必须带替代路径。
//
// 对账 policy.ts 的 `blockedTools` 分支：reason 文案含
// 「Not a dead end — achieve the goal via built-in tools」。
// 这是本仓库的「出路契约」——被拦不是死路。
func TestEvaluatePolicyBlockedHasEscapePath(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:       "mcp__srv__danger",
		BlockedTools:   []string{"mcp__srv__danger"},
		TrustedServers: []string{"srv"},
	})
	if d.Action != ActionBlock {
		t.Errorf("blockedTools 命中应 block，实得 %q", d.Action)
	}
	if d.ServerID != "srv" || d.McpToolName != "danger" {
		t.Errorf("被拦时也应回填解析结果：%+v", d)
	}
}

// TestEvaluatePolicyAllowedWinsOverUnknownCapability —— 显式允许优先。
func TestEvaluatePolicyAllowedWinsOverUnknownCapability(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:     "mcp__srv__read",
		AllowedTools: []string{"mcp__srv__read"},
		// 未声明 capability → 默认会 confirm；但显式 allow 优先
	})
	if d.Action != ActionAllow {
		t.Errorf("allowedTools 应优先于 unknown-capability 的 confirm，实得 %q（reason=%s）", d.Action, d.Reason)
	}
}

// TestEvaluatePolicyUnknownCapabilityConfirms —— 未声明能力 → confirm。
//
// 对账 policy.ts：`capability === 'unknown'` → confirm（要求逐次确认）。
func TestEvaluatePolicyUnknownCapabilityConfirms(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:       "mcp__srv__read",
		TrustedServers: []string{"srv"}, // 即使 server 受信
	})
	if d.Action != ActionConfirm {
		t.Errorf("未声明能力应 confirm，实得 %q（reason=%s）", d.Action, d.Reason)
	}
	if d.Capability != CapabilityUnknown {
		t.Errorf("capability 应为 unknown，实得 %q", d.Capability)
	}
}

// TestEvaluatePolicyUntrustedNonReadConfirms —— 未受信 + 非只读 → confirm。
func TestEvaluatePolicyUntrustedNonReadConfirms(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:           "mcp__srv__write",
		DeclaredCapability: CapabilityWrite,
		// TrustedServers 为空 → 未受信
	})
	if d.Action != ActionConfirm {
		t.Errorf("未受信 + write 应 confirm，实得 %q", d.Action)
	}
}

// TestEvaluatePolicyTrustedReadAllows —— 受信 + 只读 → allow。
func TestEvaluatePolicyTrustedReadAllows(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:           "mcp__srv__read",
		DeclaredCapability: CapabilityRead,
		TrustedServers:     []string{"srv"},
	})
	if d.Action != ActionAllow {
		t.Errorf("受信 + read 应 allow，实得 %q（reason=%s）", d.Action, d.Reason)
	}
}

// TestEvaluatePolicyMustConfirmOverridesTrust —— mustConfirm 优先于受信。
func TestEvaluatePolicyMustConfirmOverridesTrust(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{
		ToolName:                "mcp__srv__read",
		DeclaredCapability:      CapabilityRead,
		TrustedServers:          []string{"srv"},
		MustConfirmCapabilities: []Capability{CapabilityRead},
	})
	if d.Action != ActionConfirm {
		t.Errorf("mustConfirm 命中应 confirm（即使受信+只读），实得 %q", d.Action)
	}
}

// TestEvaluatePolicyNonMcpToolAllows —— 非 MCP 工具不归本策略管。
func TestEvaluatePolicyNonMcpToolAllows(t *testing.T) {
	d := EvaluatePolicy(PolicyInput{ToolName: "read_file"})
	if d.Action != ActionAllow {
		t.Errorf("非 MCP 工具应 allow，实得 %q", d.Action)
	}
	if d.ServerID != "" {
		t.Errorf("非 MCP 工具不该有 serverId：%q", d.ServerID)
	}
}
