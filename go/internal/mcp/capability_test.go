package mcp

import (
	"context"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// capability_test.go —— 第一百一十刀 W2：`policy` 配置 → 工具审批判定（修 finding #4）。
//
// # 缺陷回顾
//
// `connectOne` 此前构造 `WrapOptions` 只传 `Transport`
// → `policyInputFor` 恒收空 capability
// → `EvaluatePolicy` 恒走 `CapabilityUnknown` 分支
// → **所有** MCP 工具 `RequiresApproval()` 恒为 true。
//
// 对比 TS `src/mcp/manager.ts:480`：它传 `serverConfig.policy?.tools[mcpDef.name]`，
// 故用户声明 `capability: 'read'` 的工具在 TS 里**不需批准**。
//
// **为什么走端到端而非直接测 `WrapTool`**：直接测 `WrapTool` 只能证明
// 「传对参数时行为对」——而缺陷恰在「**没人传**」这一步。
// 必须从「配置 → Manager → 工具」整条链验证，才打得中。

// fakeServerConfigWithPolicy 在假 server 配置上叠加 policy。
//
// 键用**原始**工具名（`echo`），不是 rivet 名（`mcp__srv__echo`）——
// 对账 TS 的 `policy.tools` 键是 `mcpDef.name`（加前缀前）。
func fakeServerConfigWithPolicy(serverID string, p *ServerPolicy) Config {
	cfg := fakeServerConfig(serverID)
	sc := cfg.Servers[serverID]
	sc.Policy = p
	cfg.Servers[serverID] = sc
	return cfg
}

// findTool 按 rivet 名找工具。
func findTool(t *testing.T, m *Manager, name string) tools.Tool {
	t.Helper()
	for _, tl := range m.AllTools() {
		if tl.Definition().Name == name {
			return tl
		}
	}
	names := make([]string, 0, len(m.AllTools()))
	for _, tl := range m.AllTools() {
		names = append(names, tl.Definition().Name)
	}
	t.Fatalf("未找到 %q，实得 %v", name, names)
	return nil
}

// TestManagerDeclaredReadCapabilitySkipsApproval —— ★ V8 核心。
//
// 用户声明 `policy.tools.echo.capability = "read"` → 该工具**不需批准**。
// 这是本刀之前**做不到**的事（恒需批准）。
func TestManagerDeclaredReadCapabilitySkipsApproval(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			"echo": {Capability: CapabilityRead},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	echo := findTool(t, m, "mcp__srv__echo")
	if echo.RequiresApproval(nil) {
		t.Error("★ 已声明 read 能力的工具不该需批准——这正是 finding #4 的修复点" +
			"（此前 Capability 恒为空 → 恒需批准）")
	}
}

// TestManagerUndeclaredCapabilityStillNeedsApproval —— ★ V9：守住「未声明须确认」。
//
// 与上一条配对。只测「声明 read 可放行」会被一个「恒不需批准」的实现蒙混过关
// ——那是**安全回归**（未声明的工具被静默放行）。两条必须同时成立。
func TestManagerUndeclaredCapabilityStillNeedsApproval(t *testing.T) {
	// 没有任何 policy
	m := NewManager(fakeServerConfig("srv"), "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	echo := findTool(t, m, "mcp__srv__echo")
	if !echo.RequiresApproval(nil) {
		t.Error("★ 未声明能力的工具必须仍需批准（安全底线，不可被 W2 改动破坏）")
	}
}

// TestManagerPolicyUsesOriginalToolName —— 查表键是**原始**名。
//
// 若实现误用 rivet 名（`mcp__srv__echo`）查表，用户写 `"echo"` 会 miss
// → 静默退回「需批准」→ 与本缺陷同形（**查表键错了也表现为「声明无效」**）。
// 本用例用 `write__thing` 这个**含双下划线**的工具名，同时覆盖
// 「名字转换不影响查表」这一点。
func TestManagerPolicyUsesOriginalToolName(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			// 键是原始名 `write__thing`（不是折叠后的 `write_thing`，
			// 也不是 rivet 名）——对账 TS 用 mcpDef.name
			"write__thing": {Capability: CapabilityRead},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	// rivet 名是折叠加前缀后的 `mcp__srv__write_thing`
	tl := findTool(t, m, "mcp__srv__write_thing")
	if tl.RequiresApproval(nil) {
		t.Error("policy 查表应用**原始**工具名（`write__thing`）；用 rivet 名查会 miss → 声明静默失效")
	}
}

// TestManagerWriteCapabilityStillNeedsApproval —— 声明 write 仍需批准。
//
// 对账 `wrapper.go` 的策略：write/execute/network 在 `mustConfirmCapabilities`
// 与「非受信 server 非只读」两条判据下都转 confirm。声明了也要批——
// 这是**安全语义**，不是「声明无效」。
func TestManagerWriteCapabilityStillNeedsApproval(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			"echo": {Capability: CapabilityWrite},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	echo := findTool(t, m, "mcp__srv__echo")
	if !echo.RequiresApproval(nil) {
		t.Error("声明 write 能力的工具仍须批准（安全语义，不是缺陷）")
	}
}

// TestManagerRequireApprovalFlagForcesApproval —— `requireApproval: true` 强制批准。
//
// 对账 `src/mcp/wrapper.ts:83` 的 `securityPolicy?.requireApproval === true`。
// 即使声明 read，显式 requireApproval 也须批准（用户更严的意愿要尊重）。
func TestManagerRequireApprovalFlagForcesApproval(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			"echo": {Capability: CapabilityRead, RequireApproval: true},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	echo := findTool(t, m, "mcp__srv__echo")
	if !echo.RequiresApproval(nil) {
		t.Error("requireApproval:true 应强制批准（即使声明了 read）")
	}
}

// TestManagerPolicyPartialApplication —— policy 只覆盖列出的工具。
//
// 假 server 提供 `echo` 与 `write__thing` 两个工具；policy 只声明 echo。
// → echo 放行、write__thing 仍需批准（**未声明的不受影响**）。
func TestManagerPolicyPartialApplication(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			"echo": {Capability: CapabilityRead},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	if findTool(t, m, "mcp__srv__echo").RequiresApproval(nil) {
		t.Error("policy 已声明的 echo 应放行")
	}
	if !findTool(t, m, "mcp__srv__write_thing").RequiresApproval(nil) {
		t.Error("policy 未声明的 write__thing 应仍需批准（不得连带放行）")
	}
}

// TestManagerDefinitionCarriesCapability —— 能力进 `Definition().Capability`。
//
// 该字段的**生产者**此前不存在（`contract/types.go` 的注释说它「被
// assessToolRisk 消费」，实则零消费者）。W2 补上生产者：声明了能力时
// 它应出现在工具定义里，供将来风险分支消费。
func TestManagerDefinitionCarriesCapability(t *testing.T) {
	cfg := fakeServerConfigWithPolicy("srv", &ServerPolicy{
		Tools: map[string]ToolPolicy{
			"echo": {Capability: CapabilityRead},
		},
	})

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	if got := findTool(t, m, "mcp__srv__echo").Definition().Capability; got != string(CapabilityRead) {
		t.Errorf("Definition().Capability 应为 read，实得 %q", got)
	}
	// 未声明的仍为空（而非硬编码某个值）
	if got := findTool(t, m, "mcp__srv__write_thing").Definition().Capability; got != "" {
		t.Errorf("未声明能力的工具 Capability 应为空，实得 %q", got)
	}
}
