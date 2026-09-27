package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// wrapper.go —— 把 MCP 工具定义包成 `tools.Tool`。
//
// 对账 `src/mcp/wrapper.ts:67-180` 的 `createMcpToolWrapper`。
//
// # 本刀范围收窄（诚实标注）
//
// **未移植**：`workspaceContext`（TS issue #147 的 subAgent 工作区处置）。
// 理由：Go 侧无 subAgent 体系（对账 `planmode.go` 记载的同类收窄）。
// TS 的 `workdirSuffix` 在无 workspaceContext 时恒返回空串——故**不接它
// 不改变输出字节**（对账其 `planned === null` 路径）。
//
// **未移植**：`consent`（connector opt-in 记录）。TS 侧是 per-session 的
// 「首次调用需批准」状态；Go 侧的工具审批走 `tools.Registry` 的门链
// （见 `agent/approval_gate.go`），不在此层重复实现。故本实现把
// 「未声明/非只读能力 → 恒需批准」保留（那是安全语义），
// 而「只读工具的首次 opt-in」交由门链处理（见 `TestWrapToolReadCapabilityStillNeedsFirstConsent`
// 的说明）。

// ContentPart 是 MCP 结果的内容分片。
//
// 对账 MCP 的 `content: [{type, text}]`。
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallResult 是一次 `tools/call` 的结果。
//
// 对账 `manager.ts` 里 `client.callTool(...)` 的返回形态。
type CallResult struct {
	Content []ContentPart
	IsError bool
}

// CallFn 是「调用远端工具」的回调。
type CallFn func(args map[string]any) (CallResult, error)

// InputSchema 是 MCP 工具声明的入参 schema。
//
// 对账 `wrapper.ts:100-104` 的透传（`properties` + `required`）。
type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
}

// ToolDef 是 MCP server 声明的工具定义。
//
// 对账 MCP `tools/list` 的元素。
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema InputSchema `json:"inputSchema"`
}

// WrapOptions 是包装选项。
type WrapOptions struct {
	// Capability 是 server 策略声明的能力（空 = 未声明）。
	Capability Capability
	// RequireApproval 强制每次调用都需批准（对账 securityPolicy.requireApproval）。
	RequireApproval bool
	// Transport 供错误归因（stdio / remote）。
	Transport ErrorTransport
}

// mcpTool 实现 `tools.Tool`。
type mcpTool struct {
	serverID string
	def      ToolDef
	call     CallFn
	opts     WrapOptions

	// policy 在构造时算定（对账 wrapper.ts:80-88——它是**构造期**求值，
	// 不是每次调用求值）。故此处缓存，避免每次 RequiresApproval 重算。
	policy PolicyDecision
	// needsApproval 同上（构造期算定）。
	needsApproval bool
}

// WrapTool 把 MCP 工具定义包成 `tools.Tool`。
//
// 对账 `wrapper.ts:67`。**审批判定的实际调用形态**（关键，逐字对账）：
//
//	policy = evaluateMcpPolicy({
//	  toolName: rivetName,
//	  declaredCapability: securityPolicy?.capability,
//	  trustedServers: [], blockedTools: [], allowedTools: [],   ← 三个空数组
//	  mustConfirmCapabilities: ['write', 'execute', 'network'],
//	})
//	needsApproval = securityPolicy?.requireApproval === true || policy.action !== 'allow'
//
// **为什么要照搬「三个空数组」**：它不是遗漏，而是**有意的**——
// 用户级白/黑名单由更上层（配置/审批门）处理，wrapper 只负责
// 「能力声明缺失 → 需确认」与「非只读能力 → 需确认」两条。
// 若在此层自作主张填白名单，会引入 TS 没有的放行路径（安全回归）。
//
// # 订正：写能力「需批准」的**实际判据**是哪一条（变异 M3 的分诊结论）
//
// 初版注释把归因写成「`mustConfirmCapabilities` 含 write 起作用」。
// **变异 M3（清空 mustConfirmCapabilities）红 0** 证伪了这个归因——
// 真正先命中的是 `policy.go` 里这条：
//
//	if !trusted && capability != CapabilityRead { return ActionConfirm }
//
// 因 `TrustedServers` 传的是空数组 → `trusted=false` → **任何非只读能力**
// 都在此转 confirm，根本走不到 `mustConfirmCapabilities` 那条。
//
// 故 `mustConfirmCapabilities` 在本调用形态下是**冗余的**（`!trusted` 已覆盖
// write/execute/network 三种）。那为何仍照搬 TS 保留它？——
// **它是防御性的第二道门**：若将来 wrapper 开始传非空 `TrustedServers`
// （如「用户标记某 server 受信」），`!trusted` 那条会放行，此时
// `mustConfirm` 才成为唯一拦住「受信 server 却声明写能力」的判据。
// 删掉它会在那个将来引入安全缺口，故**保留并标注**（而非任其成为
// 「读者以为在起作用」的装饰——那正是本次订正要消除的误解）。
func WrapTool(serverID string, def ToolDef, call CallFn, opts WrapOptions) tools.Tool {
	rivetName := ToolName(serverID, def.Name)

	policy := EvaluatePolicy(policyInputFor(rivetName, opts.Capability))

	needsApproval := opts.RequireApproval || policy.Action != ActionAllow

	return &mcpTool{
		serverID:      serverID,
		def:           def,
		call:          call,
		opts:          opts,
		policy:        policy,
		needsApproval: needsApproval,
	}
}

// policyInputFor 构造 wrapper 传给策略层的输入。
//
// **为什么把它抽出来（不是为了好看）**：初版把这段字面量**内联**在 `WrapTool` 里，
// 于是「wrapper 到底传了什么」**不可观测**——变异 M3 把
// `MustConfirmCapabilities` 清空时，全部测试仍绿（因为直击判据的测试自己
// 传参，不经过这段字面量）。**不可测 = 无效断言**。
// 抽成函数后，`TestPolicyInputForMatchesTSShape` 可直接断言这段输入，
// M3 变异在它上面必红。
//
// 三个空数组是照搬 TS 的有意选择（见 `WrapTool` 的说明）。
func policyInputFor(rivetName string, declared Capability) PolicyInput {
	return PolicyInput{
		ToolName:           rivetName,
		DeclaredCapability: declared,
		// 对账 wrapper.ts:84 的 `trustedServers: []` 等三个空数组
		TrustedServers: []string{},
		BlockedTools:   []string{},
		AllowedTools:   []string{},
		// 对账 wrapper.ts:87 的字面量
		MustConfirmCapabilities: []Capability{CapabilityWrite, CapabilityExecute, CapabilityNetwork},
	}
}

func (t *mcpTool) Definition() contract.Definition {
	desc := t.def.Description
	if desc == "" {
		// 对账 wrapper.ts:78 的兜底
		desc = "MCP tool: " + t.def.Name + " (from " + t.serverID + ")"
	}
	return contract.Definition{
		Name:        ToolName(t.serverID, t.def.Name),
		Description: desc,
		// Capability 供 assessToolRisk 用（对账 contract/types.go 的字段注释）
		Capability: string(t.opts.Capability),
		InputSchema: &contract.InputSchema{
			Type:       t.def.InputSchema.Type,
			Properties: t.def.InputSchema.Properties,
			// # 键序：不传 PropOrder，走字典序回退（已称量，非疏漏）
			//
			// `Properties` 是 `map[string]any`——无插入序。序列化走
			// `tools.OrderedProps(props, PropOrder)`（消费点 `agent/loop.go:1564`）；
			// PropOrder 为 nil 时它**按字典序**回退（`schema.go` 实现）——
			// 故键序**确定**，两次请求产出**相同字节**，不破坏 Go 侧前缀缓存
			// 的稳定性（这是缓存正确性的**充分条件**）。
			//
			// **与 TS 的差异（已披露，判定为无实际影响）**：TS 侧属性序 =
			// MCP server 返回的 JSON **声明序**。差异仅在「声明序 ≠ 字典序」
			// 时显现，且只影响「Go 与 TS 产出**逐字节相同**的工具定义」。
			// 那需要**跨实现共享前缀缓存**才有人受益——而 Go runtime 与 TS
			// runtime 是独立进程、独立 provider 连接（DeepSeek 前缀缓存按
			// 请求内容哈希且不跨 API key 共享），该场景不存在。
			// 故不引入 JSON token 状态机去捕获声明序（复杂度 > 收益）。
			//
			// 若将来真需逐字节 parity：在 `manager.listTools` 用 `json.Decoder`
			// 的 token 流捕获 properties 的键序填入此处即可，消费端无需改。
			Required: t.def.InputSchema.Required,
		},
	}
}

// annotation 生成结果注解（对账 wrapper.ts:120 的格式）。
func (t *mcpTool) annotation() string {
	s := "[MCP: " + t.serverID + " · " + string(t.policy.Capability)
	if t.needsApproval {
		s += " · approval-required"
	}
	return s + "]"
}

// firstLine 取首行并截到 200 字符（对账 wrapper.ts:131 的 `.slice(0, 200)`）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// 按 rune 截断（TS 的 slice 按 UTF-16 码元；此处按 rune 是 Go 侧惯例，
	// 差异仅在 200 附近的代理对边界——对错误提示文案无实质影响）
	r := []rune(s)
	if len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

func (t *mcpTool) Execute(_ context.Context, p *tools.CallParams) (contract.Result, error) {
	var input map[string]any
	if p != nil {
		input = p.Input
	}

	result, err := t.call(input)
	if err != nil {
		// 对账 wrapper.ts:145-152：分类后把 class + suggestion 放进注解
		classified := ClassifyMcpError(err, ErrorContext{Transport: t.opts.Transport})
		ann := "[MCP: " + t.serverID + " · " + string(t.policy.Capability)
		if t.needsApproval {
			ann += " · approval-required"
		}
		ann += " · error: " + string(classified.Class) + " · " + classified.Suggestion + "]"

		full := err.Error()
		return contract.Result{
			Content:   "MCP tool error (" + ToolName(t.serverID, t.def.Name) + "): " + firstLine(full) + "\n" + ann,
			UIContent: "MCP tool error (" + ToolName(t.serverID, t.def.Name) + "): " + full + "\n" + ann,
			IsError:   true,
		}, nil
	}

	// 拼正文：只取 text 分片（对账 wrapper.ts:113-116）
	var parts []string
	for _, c := range result.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	content := strings.Join(parts, "\n")
	if content == "" {
		content = "(no text content)"
	}

	if result.IsError {
		// **失败时只留首行**（对账 wrapper.ts:131-138 的注释）：
		// 模型只需知道「失败 + 首行原因」，完整原文走 UIContent。
		return contract.Result{
			Content:   t.annotation() + " · tool error\n" + firstLine(content),
			UIContent: t.annotation() + " · tool error\n" + content,
			IsError:   true,
		}, nil
	}

	return contract.Result{
		Content: content + "\n" + t.annotation(),
	}, nil
}

// RequiresApproval 报告是否需要批准。
//
// 对账 `wrapper.ts:163-169`：
//
//	if (needsApproval) return true
//	if (consent && !consent.hasConsented(serverId)) return true
//	return false
//
// **Go 侧收窄**：`consent` 交由门链（见文件头说明），故此处只剩第一句。
func (t *mcpTool) RequiresApproval(*tools.CallParams) bool {
	return t.needsApproval
}

// ConcurrencySafe 恒真（对账 wrapper.ts:172-174）。
//
// 理由：MCP 工具的并发安全由 server 侧保证，客户端不做串行化
// （TS 同款判定）。
func (t *mcpTool) ConcurrencySafe() bool { return true }

// Enabled 恒真（对账 wrapper.ts:176-178）。
//
// **为什么恒真而非「连接是否活着」**：断连时 `call` 会返回错误
// （manager 在 callFn 里检连接），届时模型看到的是**明确的错误信息**
// 而非「工具消失」——后者会让模型以为该能力不存在。
func (t *mcpTool) Enabled() bool { return true }

// Timeout 返回调用超时（对账 manager.ts:48 的 60s）。
func (t *mcpTool) Timeout(*tools.CallParams) time.Duration {
	return time.Duration(DefaultTimeoutMS) * time.Millisecond
}
