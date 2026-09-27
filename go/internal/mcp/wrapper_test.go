package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// wrapper_test.go —— MCP 工具 → tools.Tool 的包装（V4, V6 的单测面）。
//
// 对账 `src/mcp/wrapper.ts:67-180` 的 `createMcpToolWrapper`。

// testToolDef 是假 server 的 `tools/list` 返回形态（见 fakeserver_test.go）。
func testToolDef() ToolDef {
	return ToolDef{
		Name:        "echo",
		Description: "Echo back the input",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]any{"text": map[string]any{"type": "string"}},
		},
	}
}

// TestWrapToolNameAndDescription —— 工具名经 ToolName 构造，描述透传。
func TestWrapToolNameAndDescription(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), func(map[string]any) (CallResult, error) {
		return CallResult{}, nil
	}, WrapOptions{})

	def := tool.Definition()
	if def.Name != "mcp__srv__echo" {
		t.Errorf("工具名应为 mcp__srv__echo，实得 %q", def.Name)
	}
	if def.Description != "Echo back the input" {
		t.Errorf("描述应透传，实得 %q", def.Description)
	}
	if def.InputSchema == nil {
		t.Fatal("应有 inputSchema")
	}
	if def.InputSchema.Properties["text"] == nil {
		t.Error("inputSchema.properties 应透传")
	}
}

// TestWrapToolDescriptionFallback —— 无描述时的兜底文案（对账 wrapper.ts:78）。
func TestWrapToolDescriptionFallback(t *testing.T) {
	d := testToolDef()
	d.Description = ""
	tool := WrapTool("srv", d, func(map[string]any) (CallResult, error) {
		return CallResult{}, nil
	}, WrapOptions{})

	if got := tool.Definition().Description; got != "MCP tool: echo (from srv)" {
		t.Errorf("兜底描述不符，实得 %q", got)
	}
}

// TestPolicyInputForMatchesTSShape —— ★ 直击 wrapper 传给策略层的**字面量**。
//
// **这条测试的存在理由**（变异 M3 的分诊产物，值得记下）：
// 初版把策略输入内联在 `WrapTool` 里 → 「wrapper 传了什么」不可观测。
// 变异 M3（清空 `MustConfirmCapabilities`）因此**红 0**——直击判据的测试
// 自己传参、不经过那段字面量。**不可测的代码 = 断言不到的行为**。
//
// 把输入构建抽成 `policyInputFor` 后，本用例直接断言它——
// 这样 M3 变异**必红**，那段字面量不再是无保护的。
func TestPolicyInputForMatchesTSShape(t *testing.T) {
	in := policyInputFor("mcp__srv__echo", CapabilityRead)

	if in.ToolName != "mcp__srv__echo" {
		t.Errorf("ToolName 应透传，实得 %q", in.ToolName)
	}
	if in.DeclaredCapability != CapabilityRead {
		t.Errorf("DeclaredCapability 应透传，实得 %q", in.DeclaredCapability)
	}
	// 三个空数组——对账 wrapper.ts:84 的 `trustedServers: []` 等
	if len(in.TrustedServers) != 0 || len(in.BlockedTools) != 0 || len(in.AllowedTools) != 0 {
		t.Errorf("三个数组必须为空（对账 TS；自填白名单会引入 TS 没有的放行路径）："+
			"trusted=%v blocked=%v allowed=%v",
			in.TrustedServers, in.BlockedTools, in.AllowedTools)
	}
	// mustConfirm 必须含 write/execute/network 三项（对账 wrapper.ts:87）
	want := map[Capability]bool{
		CapabilityWrite: false, CapabilityExecute: false, CapabilityNetwork: false,
	}
	for _, c := range in.MustConfirmCapabilities {
		if _, ok := want[c]; ok {
			want[c] = true
		} else {
			t.Errorf("mustConfirmCapabilities 含预期外项 %q", c)
		}
	}
	for c, found := range want {
		if !found {
			t.Errorf("mustConfirmCapabilities 缺 %q（对账 wrapper.ts:87 的三项）", c)
		}
	}
	if len(in.MustConfirmCapabilities) != 3 {
		t.Errorf("mustConfirmCapabilities 应恰 3 项，实得 %d：%v",
			len(in.MustConfirmCapabilities), in.MustConfirmCapabilities)
	}
}

// TestWrapToolRequiresApprovalForWriteCapability —— ★ V4 核心：写能力恒需批准。
//
// 对账 `wrapper.ts:82-88` 的**实际调用形态**（关键！）：
//
//	const policy = evaluateMcpPolicy({
//	  toolName: rivetName,
//	  declaredCapability: securityPolicy?.capability,
//	  trustedServers: [], blockedTools: [], allowedTools: [],
//	  mustConfirmCapabilities: ['write', 'execute', 'network'],
//	})
//	const needsApproval = securityPolicy?.requireApproval === true || policy.action !== 'allow'
//
// 注意三个数组**都是空的**——本用例验的是**最终行为**（写能力需批准），
// 不锁定它由哪条内部判据实现（两条都通向 confirm）：
//   - `!trusted && capability != read`   （因 TrustedServers 为空，实际先命中）
//   - `mustConfirmCapabilities` 含 write （本调用形态下的第二道门）
//
// 逐条打这些判据的测试见下面两个用例。
func TestWrapToolRequiresApprovalForWriteCapability(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), nil2call, WrapOptions{
		Capability: CapabilityWrite,
	})
	if !tool.RequiresApproval(nil) {
		t.Error("write 能力应恒需批准")
	}
}

// TestPolicyMustConfirmGuardsWriteOnTrustedServer —— ★ 打 `mustConfirmCapabilities` 这条门本身。
//
// **为什么需要它**（变异 M3 的分诊产物）：
// wrapper 里 `TrustedServers` 是空数组 → `policy.go` 的 `!trusted && cap != read`
// 先命中，于是写能力**根本走不到** `mustConfirmCapabilities`。清空
// `mustConfirmCapabilities`（变异 M3）时 wrapper 层用例**不会红**——
// 两条判据互为冗余。
//
// 但冗余≠可删：`mustConfirm` 是「server 受信时仍拦住写能力」的**唯一**判据。
// 本用例直接调 `EvaluatePolicy` 并把 server 标为受信，专打那条门——
// 这样 M3 变异在此**必红**，冗余判据的失效不再无声。
func TestPolicyMustConfirmGuardsWriteOnTrustedServer(t *testing.T) {
	in := PolicyInput{
		ToolName:                "mcp__srv__write_thing",
		DeclaredCapability:      CapabilityWrite,
		TrustedServers:          []string{"srv"}, // ← 受信：绕过 `!trusted` 那条
		BlockedTools:            []string{},
		AllowedTools:            []string{},
		MustConfirmCapabilities: []Capability{CapabilityWrite, CapabilityExecute, CapabilityNetwork},
	}
	dec := EvaluatePolicy(in)
	if dec.Action != ActionConfirm {
		t.Errorf("受信 server 上的写能力仍须确认（mustConfirm 是唯一判据），实得 %q", dec.Action)
	}

	// 反向：清空 mustConfirm → 受信 server 上的写能力被放行（证明上一条是本门在起作用）
	in.MustConfirmCapabilities = []Capability{}
	if dec := EvaluatePolicy(in); dec.Action == ActionConfirm {
		t.Error("清空 mustConfirm 后应放行——否则本测试没打到该门（真空断言）")
	}
}

// TestPolicyUntrustedNonReadNeedsConfirm —— ★ 打 `!trusted && cap != read` 这条门。
func TestPolicyUntrustedNonReadNeedsConfirm(t *testing.T) {
	// 非受信 + 写能力 → confirm（**即使 mustConfirm 为空**——证明本门独立成立）
	dec := EvaluatePolicy(PolicyInput{
		ToolName:                "mcp__srv__write_thing",
		DeclaredCapability:      CapabilityWrite,
		TrustedServers:          []string{},
		MustConfirmCapabilities: []Capability{},
	})
	if dec.Action != ActionConfirm {
		t.Errorf("非受信 server 的写能力须确认（独立于 mustConfirm），实得 %q", dec.Action)
	}

	// 非受信 + 只读能力 → allow（read 是豁免的，对账 policy.go）
	if dec := EvaluatePolicy(PolicyInput{
		ToolName:                "mcp__srv__read_thing",
		DeclaredCapability:      CapabilityRead,
		TrustedServers:          []string{},
		MustConfirmCapabilities: []Capability{},
	}); dec.Action != ActionAllow {
		t.Errorf("只读能力应放行，实得 %q", dec.Action)
	}
}

// TestWrapToolRequiresApprovalForUnknownCapability —— 未声明能力 → 需批准。
func TestWrapToolRequiresApprovalForUnknownCapability(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), nil2call, WrapOptions{})
	if !tool.RequiresApproval(nil) {
		t.Error("未声明能力应需批准（policy.action = confirm）")
	}
}

// TestWrapToolReadCapabilityNeedsNoApproval —— 已声明只读能力 → 不需批准。
//
// **这里有一个刻意的收窄，必须说清（否则读者会以为是漏了）**：
//
// TS 的 `RequiresApproval`（`wrapper.ts:163-169`）有两句：
//
//	if (needsApproval) return true                              ← Go 保留
//	if (consent && !consent.hasConsented(serverId)) return true  ← Go 不实现
//
// 第二句是 connector opt-in（「只读工具也需用户首次选用该服务器」）。
// Go 侧不在此层实现它：`tools.Tool` 接口**没有** consent 概念，
// 而会话级的工具审批由门链（`agent/approval_gate.go`）按 `RequiresApproval`
// 的返回值统一处理。若在此层恒返回 true，会让**每次**只读调用都弹批准
// （因为无状态可记「已 opt-in」）——比 TS 更扰民，是**行为回归**而非忠实移植。
//
// 故 Go 侧语义 = 「声明缺失或非只读能力 → 需批准」；只读工具的**会话级
// 首次使用**由门链以「工具名首次出现」为粒度处理。这是**有意的架构分工**，
// 已在 wrapper.go 文件头「本刀范围收窄」中披露。
func TestWrapToolReadCapabilityNeedsNoApproval(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), nil2call, WrapOptions{
		Capability: CapabilityRead,
	})
	if tool.RequiresApproval(nil) {
		t.Error("已声明只读能力不应恒需批准（connector opt-in 由门链处理，见本测试说明）")
	}
}

// TestWrapToolExecuteAnnotatesResult —— 成功结果带注解（对账 wrapper.ts:143）。
//
// 格式：`[MCP: <serverId> · <capability>]` 追加在正文之后（换行分隔）。
func TestWrapToolExecuteAnnotatesResult(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), func(map[string]any) (CallResult, error) {
		return CallResult{Content: []ContentPart{{Type: "text", Text: "hello"}}}, nil
	}, WrapOptions{Capability: CapabilityRead})

	res, err := tool.Execute(context.Background(), &tools.CallParams{
		ToolUseID: "t1", Input: map[string]any{"text": "x"},
	})
	if err != nil {
		t.Fatalf("Execute 报错：%v", err)
	}
	if res.IsError {
		t.Fatalf("不该是错误：%s", res.Content)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Errorf("应含 callTool 返回的正文：%q", res.Content)
	}
	if !strings.Contains(res.Content, "[MCP: srv · read]") {
		t.Errorf("应含注解 [MCP: srv · read]，实得 %q", res.Content)
	}
}

// TestWrapToolExecuteErrorTakesFirstLineOnly —— ★ 失败时只取首行（对账 wrapper.ts:131-138）。
//
// TS 注释逐字：「模型只需知道『失败 + 首行原因』，不必把整段服务器错误文本
// 灌进上下文；完整原文走 uiContent 供 TUI 展示」。
func TestWrapToolExecuteErrorTakesFirstLineOnly(t *testing.T) {
	longErr := "first line reason\nsecond line detail\nthird line noise"
	tool := WrapTool("srv", testToolDef(), func(map[string]any) (CallResult, error) {
		return CallResult{
			Content: []ContentPart{{Type: "text", Text: longErr}},
			IsError: true,
		}, nil
	}, WrapOptions{Capability: CapabilityRead})

	res, err := tool.Execute(context.Background(), &tools.CallParams{ToolUseID: "t1"})
	if err != nil {
		t.Fatalf("Execute 报错：%v", err)
	}
	if !res.IsError {
		t.Fatal("应标记为错误")
	}
	if !strings.Contains(res.Content, "first line reason") {
		t.Errorf("content 应含首行：%q", res.Content)
	}
	if strings.Contains(res.Content, "third line noise") {
		t.Errorf("content 不该含后续行（模型上下文只留首行）：%q", res.Content)
	}
	if !strings.Contains(res.UIContent, "third line noise") {
		t.Errorf("uiContent 应保留完整原文：%q", res.UIContent)
	}
}

// TestWrapToolCallErrorClassified —— 调用抛错时的分类注解（对账 wrapper.ts:145-152）。
func TestWrapToolCallErrorClassified(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), func(map[string]any) (CallResult, error) {
		return CallResult{}, errTest("401 Unauthorized")
	}, WrapOptions{Capability: CapabilityRead, Transport: ErrorTransportStdio})

	res, err := tool.Execute(context.Background(), &tools.CallParams{ToolUseID: "t1"})
	if err != nil {
		t.Fatalf("Execute 不该向上抛（应转成 Result）：%v", err)
	}
	if !res.IsError {
		t.Fatal("应标记为错误")
	}
	if !strings.Contains(res.Content, "error: auth") {
		t.Errorf("应含分类 class=auth，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "MCP tool error (mcp__srv__echo)") {
		t.Errorf("应含工具名前缀，实得 %q", res.Content)
	}
}

// TestWrapToolConcurrencySafeAndEnabled —— 恒真（对账 wrapper.ts:172-178）。
func TestWrapToolConcurrencySafeAndEnabled(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), nil2call, WrapOptions{})
	if !tool.ConcurrencySafe() {
		t.Error("MCP 工具并发安全（对账 TS isConcurrencySafe: () => true）")
	}
	if !tool.Enabled() {
		t.Error("MCP 工具恒启用")
	}
}

// TestWrapToolTimeoutIsDefault —— 超时 60s（对账 manager.ts:48）。
func TestWrapToolTimeoutIsDefault(t *testing.T) {
	tool := WrapTool("srv", testToolDef(), nil2call, WrapOptions{})
	if got := tool.Timeout(nil); got.Milliseconds() != DefaultTimeoutMS {
		t.Errorf("超时应为 %dms，实得 %v", DefaultTimeoutMS, got)
	}
}

// ---- 小工具 ----

type errTest string

func (e errTest) Error() string { return string(e) }

// boolPtr 供测试构造 `Config.Enabled`（指针三态，见 types.go 的说明）。
func boolPtr(b bool) *bool { return &b }

func nil2call(map[string]any) (CallResult, error) { return CallResult{}, nil }
