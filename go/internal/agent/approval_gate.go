// approval_gate.go —— 档位驱动的审批门决策（`NeedsApproval` 的消费端）。
//
// 对账 TS `tool-pipeline.ts:1213-1232` 的 `shouldAsk` 决策树。
//
// # 为什么需要它（Go 侧的 fail-open 缺口）
//
// `tools.Registry.NeedsApproval` 此前**零调用者**（`registry.go` 明记「有意暂缓」）。
// 实测后果：4 个写工具（write_file / edit_file / hash_edit / apply_patch）在
// **manual 档**下 `needsApproval=true`，但无人消费 → 写操作**静默执行**。
// 用户选 manual 期望每个写操作都确认，实际无人把关——fail-open，与
// 「用户配的边界不生效」同类（第五十二刀修过 deny 规则的同型缺口）。
//
// # 逐条对账 TS 的档位分支（**顺序与取值都不可改**）
//
//	shouldAsk = (unconditionalApproval && !yoloBypassesUnconditional) ? true
//	  : skipAllApproval ? false
//	  : pathGrantNeed ? true
//	  : protectionMode ? true
//	  : bashWriteRequiresApproval ? true
//	  : computerUsePerAppGate ? true
//	  : allowlisted ? false
//	  : canAutoApprove ? false
//	  : approvalMode === 'manual'    ? needsApproval
//	  : approvalMode === 'auto-safe' ? isHighRisk      ← **不是 needsApproval**
//	  : false
//
// **`auto-safe` 用 `isHighRisk` 是本文件最容易写错的一点**（首版实测即错）：
// 若照搬 manual 分支用 `needsApproval`，Go 默认档（auto-safe）下 4 个写工具
// 会被全部拦下——`write_file` 直接不可用（`registry.go` 警告过「6 个既有
// 测试转红」）。而写工具的风险实测为 low/none，故 auto-safe 下应当放行。
//
// # Go 侧当前可表达的输入（其余留口，明示不静默收窄）
//
//	输入                      TS 来源                        Go 现状
//	skipAllApproval           approvalMode 派生              ✅ ApprovalMode 字段
//	needsApproval             toolRegistry.needsApproval     ✅ 本文件消费
//	isHighRisk                assessToolRisk().level         ✅ 本文件消费
//	pathGrantNeed             outOfWorkspaceFilePaths        ✅ 已接线（loop.go，先于本门）
//	unconditionalApproval     requiresUnconditionalApproval  ⚠️ 已移植；其覆盖的工具
//	                                                          （computer_use /
//	                                                          request_path_access）
//	                                                          Go 侧未移植 → 暂不接
//	bashWriteRequiresApproval requiresBashWriteApproval      ⚠️ 已移植；bash 破坏性
//	                                                          命令已由 RequiresHardGate
//	                                                          覆盖（loop.go，先于本门）
//	protectionMode            doomLoop + destructiveGit      ❌ 无 doom-loop 会话态
//	allowlisted               isToolAllowed(allowRules)      ❌ 无 allowRules 装配
//	canAutoApprove            sensorium 置信度               ❌ 无 sensorium
//	computerUsePerAppGate     computer_use 逐应用            ❌ 无 computer_use 工具
//
// **未接的输入不改变本门的正确性**：它们要么已被 loop.go 前置门覆盖，要么对应
// 的工具尚未移植。接入时按上表逐条补，并在此更新状态列。
//
// # 已知欠账（前置③，本刀**有意未做**）
//
// `registry.go:358` 列出的接线条件③「写工具的 `RequiresApproval` 语义订正」
// **未在本刀完成**。现状：4 个写工具（write_file / edit_file / hash_edit /
// apply_patch）返回 `p.ApprovalMode != "dangerously-skip-permissions"`，
// 而 TS 是 `requiresApproval: () => true`（本质属性，档位判定只在 pipeline 一处）。
//
// **为什么不在本刀做**：本门已独立按档位判定（`skip` 档在最前短路），故
// **行为当前正确**。但这是**重复判定**（工具层与本门各判一次档位），
// 将来任一处改动会导致不一致——是结构性隐患，非当前缺陷。
// 订正会改变工具层语义并可能波及既有断言（如 `bash_test.go` 记录的同类修正），
// 属独立的语义对齐刀。**一次改两处会让「哪处生效」不可归因**——与本文件
// 「一次只接一条输入」的原则一致。
//
// 订正时的判据：4 处 `RequiresApproval` 改为恒 `true`，并确认本门仍覆盖
// skip 档（本文件的 skip 短路即为此保留）。
package agent

import (
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// approvalGateDecision 是审批门的判定结果。
type approvalGateDecision struct {
	// Block 为真表示拦截该次调用。
	Block bool
	// Reason 是拦截原因（仅 Block 时非空，供文案拼接）。
	Reason string
}

// decideApprovalGate 判定是否拦截该次工具调用。
//
// 纯函数（无 IO、无会话态）——可单测，不引入新不确定性。
//
// 参数：
//   - needsApproval：`Registry.NeedsApproval` 的结果（档位驱动的 `RequiresApproval`）
//   - isHighRisk：`AssessToolRisk().Level == RiskHigh`
func decideApprovalGate(p *tools.CallParams, needsApproval, isHighRisk bool) approvalGateDecision {
	// TS: `skipAllApproval ? false` —— 完全访问档零审批打扰承诺。
	//
	// **硬闸门不受此影响**：那是独立一层（loop.go 的 RequiresHardGate 先于
	// 本函数执行），故此处放行不会让破坏性命令漏过。
	if p.ApprovalMode == "dangerously-skip-permissions" {
		return approvalGateDecision{Block: false}
	}

	switch p.ApprovalMode {
	case "manual":
		// TS: `approvalMode === 'manual' ? needsApproval` —— 每个需批准的工具都拦。
		if needsApproval {
			return approvalGateDecision{
				Block:  true,
				Reason: "当前为 manual 档：该工具需逐次人工批准",
			}
		}
	case "auto-safe":
		// TS: `approvalMode === 'auto-safe' ? isHighRisk` —— **只拦高风险**。
		//
		// 写工具的 risk 实测为 low/none → 放行（默认档行为不变）。
		// 与 manual 的差异是**有意设计**：auto-safe 是「智能安全」档，
		// 工作区内的常规写操作不该逐次打断。
		if isHighRisk {
			return approvalGateDecision{
				Block:  true,
				Reason: "当前为 auto-safe 档：该调用被评估为高风险",
			}
		}
	}
	// 未知档位（TS 的 `: false` 兜底）——不拦，保持既有行为。
	return approvalGateDecision{Block: false}
}

// isHighRiskCall 报告该次调用是否被评估为高风险。
//
// 对账 TS `const isHighRisk = risk.level === 'high'`（`tool-pipeline.ts:1097`）。
//
// **doomLoopLevel 传空串**：Go 侧无 doom-loop 会话态（`AssessToolRisk` 的该参数
// 只影响 `SuggestedAction`，不改变 `Level`）——故传空串与 TS 省略该输入等价。
func isHighRiskCall(tc toolCall) bool {
	return AssessToolRisk(tc.name, tc.input, "").Level == RiskHigh
}
