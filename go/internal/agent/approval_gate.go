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
// # Go 侧当前状态（逐条核实，2026-06 第六十四刀对账）
//
// ## A. 决策树**之前**的守卫分支（TS `tool-pipeline.ts:1126-1143`）
//
// 这三条在 `shouldAsk` 之前、**不受档位影响**（TS 注释：「Deny rules always
// win, even in dangerously-skip-permissions」/「Always on, independent of
// config/approval mode」）。**本表此前漏列**——它们不在 `shouldAsk` 里，
// 但决定了调用能否走到本门。
//
//	输入              TS 来源                     Go 现状
//	denied            isToolDenied(denyRules)     ✅ 已接线（loop.go，决策链最前）
//	bashDenied        isBashCommandDenied         ✅ 第六十五刀接线
//	                  (bashDenyPrefixes)            （`permissions_shellsplit.go`
//	                                                + PermissionConfig.Bash）
//	selfKill          isSelfDestructiveKill       ❌ 未移植（self-preservation.ts:85）
//	                  (selfProcessTree())
//
// **`bashDenied` 与 `denied` 的差距**：`denied` 走 `IsToolDenied`——它能匹配
// `{tool:"bash", params:{command:"rm -rf*"}}` 这类**参数模式**规则，故用户写的
// bash 前缀 deny 规则**部分**仍生效。但 TS 的 `permissions.bash.denylist`
// （`schema.ts:286`）是**独立字段**，Go 的 `PermissionConfig` 无此字段 →
// 用户在 `permissions.bash.denylist` 里写的规则被**静默忽略**。
// （严重性低于第五十二刀：参数模式 deny 仍可用，但配了独立字段的用户会失效。）
//
// **`selfKill` 的 Go 侧适配性（待判定，非缺陷结论）**：TS 的动机是「杀掉
// agent 自身所在的 Node sidecar 进程 → 会话中断 + API 认证丢失」。Go CLI 的
// 运行形态不同（单进程，无 Node sidecar）——**该风险是否成立需要单独称量**，
// 本表只记录「无对应实现」，不判定为缺口。
//
// ## B. `shouldAsk` 决策树的输入
//
//	输入                      TS 来源                        Go 现状
//	skipAllApproval           approvalMode 派生              ✅ ApprovalMode 字段
//	needsApproval             toolRegistry.needsApproval     ✅ 本文件消费
//	isHighRisk                assessToolRisk().level         ✅ 本文件消费
//	pathGrantNeed             outOfWorkspaceFilePaths        ✅ 已接线（loop.go，先于本门）
//	bashWriteRequiresApproval requiresBashWriteApproval      ✅ 第六十三刀接线
//	                                                          （`bashWriteNeedsApproval`）
//	unconditionalApproval     requiresUnconditionalApproval  ⚠️ 判定已接线（经
//	                                                          AssessToolRisk 置 high），
//	                                                          但 Go 侧无触发它的工具：
//	                                                          `request_path_access` /
//	                                                          `computer_use` 均未移植
//	                                                          → **分支实际不可达**
//	protectionMode            doomLoop + destructiveGit      ❌ 无 doom-loop 会话态
//	                                                          （`DoomLoop` 零生产文件）
//	allowlisted               isToolAllowed(allowRules)      ✅ 第六十四刀接线
//	                                                          （档位门内豁免，全工具面）
//	                                                          注：`bashWriteNeedsApproval`
//	                                                          内另有一处豁免（bash 写门）
//	bashAllowlisted           isBashCommandAllowlisted       ✅ 第六十六刀接线
//	                                                          （`bashAllowlistedFor`）
//	canAutoApprove            sensorium 置信度               ❌ 无 sensorium
//	                                                          （仅注释/字符串表）
//	computerUsePerAppGate     computer_use 逐应用            ❌ 工具未移植
//
// **未接的输入不改变**已接分支**的正确性**——但「不影响正确性」不等于
// 「无缺口」：上表 ⚠️ 与 ❌ 各行都是**已声明的能力未生效**，各自需要独立的刀。
// 接入时按上表逐条补，并在此更新状态列与日期。
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

// allowRulesOf 取出会话的 allow 规则（nil-safe）。
//
// `IsToolAllowed` 对空规则返回 false（fail-closed）——故 nil 与空切片等价，
// 无需区分。对账 TS 的 `deps.config.permissions?.allow ?? []`。
func allowRulesOf(cfg Config) []PermissionAllowRule {
	if cfg.Permissions == nil {
		return nil
	}
	return cfg.Permissions.Allow
}

// bashWriteNeedsApproval 对账 TS 的复合条件 `bashWriteRequiresApproval`。
//
// TS（`tool-pipeline.ts:1163-1167`）：
//
//	bashWriteRequiresApproval = requiresBashWriteApproval(tu.name, tu.input)
//	  && !allowlisted && !bashAllowlisted
//	  && !safeWriteInNoSandbox
//	  && noSandbox
//
// **Go 侧代入**（一处恒值，理由见下）：
//   - `noSandbox` 恒 **true**：Go 侧无内核沙箱（`isSandboxActive` 零命中）。
//
// `bashAllowlisted` 原为恒 false（`isBashCommandAllowlisted` 未移植）；
// **第六十六刀已接线**——现由 `bashAllowlistedFor` 消费
// `permissions.bash.allowlist`。
//
// 故化简为：
//
//	RequiresBashWriteApproval && !allowlisted && !bashAllowlisted
//	  && !safeWriteInAutoSafe
//
// **`safeWriteInNoSandbox` 的档位依赖不可省**：它含
// `approvalMode === 'auto-safe'`——即「安全写（mkdir/touch/cp/echo>file，
// 且写目标在工作区内）只在 auto-safe 档自动放行」。manual 档下同一命令
// **仍要拦**。漏掉这个档位判断，会让 manual 档的写命令静默通过。
//
// **`HasOutOfWorkspaceWriteTarget` 是第二道闸（不可省）**：bash 的写目标
// 不经文件工具的路径校验，`echo key >> ~/.ssh/authorized_keys` 若不在此
// 拦下，会在 auto-safe 档零提示执行。
//
// 与硬闸门的关系：本函数管的**写**命令（`mkdir`/`cp`/`echo >`/`chmod`/
// `rm`（无 -rf））与硬闸门管的**破坏**命令（`rm -rf`/`git reset --hard`）
// 只有部分交集——`mkdir` 不在硬闸门内，故本门是独立的补强，不是重复。
func bashWriteNeedsApproval(toolName string, input map[string]any, approvalMode string, allowRules []PermissionAllowRule, perms *PermissionConfig) bool {
	if !RequiresBashWriteApproval(toolName, input) {
		return false
	}
	// TS: `!allowlisted` —— 用户显式 allow 规则可豁免。
	if IsToolAllowed(toolName, input, allowRules) {
		return false
	}
	// TS: `!bashAllowlisted` —— `permissions.bash.allowlist` 前缀覆盖（第六十六刀）。
	// 与上面的 `allowlisted` 是**两个不同输入**：前者是规则模式（工具名+参数），
	// 后者是命令前缀（含 5 道 fail-closed 守卫）。
	if bashAllowlistedFor(perms, toolName, input) {
		return false
	}
	// TS: `!safeWriteInNoSandbox` —— 仅 auto-safe 档豁免安全写。
	cmd, _ := input["command"].(string)
	if approvalMode == "auto-safe" && IsSafeWriteOnly(cmd) && !HasOutOfWorkspaceWriteTarget(cmd) {
		return false
	}
	return true
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
