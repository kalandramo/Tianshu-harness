// approval_gate.go —— 档位驱动的审批门决策（`NeedsApproval` 的消费端）。
//
// 对账 TS `tool-pipeline.ts` 的 `shouldAsk` 决策树（`let shouldAsk = …` 三元链）。
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
// # Go 侧当前状态（逐条核实，第六十九刀对账）
//
// ## A. 决策树**之前**的守卫分支（TS `tool-pipeline.ts` 的 `denied || bashDenied || selfKill` 块）
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
//	selfKill          isSelfDestructiveKill       ✅ 第六十八刀接线（**仅 PID 类**）
//	                  (selfProcessTree())           镜像名类有意不移植（见下节）
//	                                                已知盲区：动态求值 PID 不拦
//
// **`bashDenied` 与 `denied` 的语义差别**（两者**都已接线**，非缺口）：
// `denied` 走 `IsToolDenied`——匹配 `{tool:"bash", params:{command:"rm -rf*"}}`
// 这类**参数模式**规则；`bashDenied` 走 `IsBashCommandDenied`——匹配
// `permissions.bash.denylist` 的**命令前缀**（`taskkill` 匹配
// `taskkill /f /im x.exe`，且穿透 `;` / `&&` / `$( … )` 找隐藏段）。
//
// **第六十五刀前的缺口**（已修，保留记录）：`PermissionConfig` 当时**无
// `bash` 字段** → 用户配的 `permissions.bash.denylist` 被 `json.Unmarshal`
// 静默丢弃。现已有 `PermissionConfig.Bash`（`permissions.go` 的同名字段）+
// `permissionsRaw.bash` + `LoadPermissions` 读取。
//
// **`selfKill` 的称量结论（第六十七刀称量 / 第六十八刀执行）**：
//
// TS 的 `isSelfDestructiveKill` 挡两类命令，**Go 侧只移植了第②类**：
//
//	TS 模式                                      Go 侧
//	① taskkill /IM node.exe · pkill node         有意**不移植**——理由见下
//	  killall node · wmic … node.exe delete
//	② kill <pid> · taskkill /PID <n> 命中自身/祖先  ✅ 第六十八刀接线
//
// **① 不移植的真实理由是「收益低」，不是「不适用」**（第六十八刀修正——
// 称量时我曾写成「不适用」，不准确）：Go 侧**确有**对应物
// （`pkill tianshu` 能杀 agent，二进制名可经 `os.Executable()` 取到）。
// 但 TS 做①是因为「node」是**极常见**进程名、用户常有 `pkill node`
// 「重启本地服务」的习惯（TS 字段报告正是 `taskkill //F //IM node.exe`）；
// Go 二进制名是 `tianshu`，**没有**该习惯性动机。
// 详见 `self_preservation.go` 文件头「有意差异」。
//
// **② 是决定性的**——**实测进程树**（bash 工具内 `echo $PPID; ps -p $PPID`）：
//
//	SELF=72921 PPID=72918
//	  PID  PPID COMM
//	72918 72916 /tmp/ts-probe      ← Go agent 自身
//
// bash 工具用 `exec.Command` 启动 shell，故 **agent 就是命令的直接父进程**——
// `kill <该 pid>` 直接杀掉 agent 自己。**这与语言无关**。
//
// **已知盲区（TS 同样存在，第六十八刀端到端实测）**：判定是静态字符串
// 分析，无法求值 shell 变量/命令替换——`kill $PPID` / `kill ${PPID}` /
// `kill $(echo 1000)` **都不拦**（TS 侧实测同样全 false）。真实 CLI 验证过：
// 该形态会让 agent `context canceled`。要闭合需**动态求值**（先跑一次 shell
// 展开再判），代价与风险高得多——见 `self_preservation.go` 的「已知盲区」段。
//
// **但收益的「量级」低于 TS**（结构差异，非判断）：
//   - TS 动机是「杀 sidecar → **API 认证上下文丢失 → 401 级联**」。Go 侧
//     **无 `auth` 包**，API key 来自环境变量（`main.go` 的 `firstEnv(...)`）
//     → **进程重启无鉴权损失**。
//   - Go 会话**增量落盘**（`.rivet/sessions/<id>.jsonl`，batchwriter 首行
//     同步 flush）→ 被杀丢的是**当轮未 flush 的部分**，不是全部历史。
//
// 故 Go 侧真实损失 = 当前 turn 中断 + 少量未落盘上下文。**风险真实但轻于 TS**。
//
// **同族风险（Go 特有，已处理）**：`proctree_unix.go` 的
// `configureProcessGroupPlatform` 注释记录——「没有 `Setpgid`，`kill(-pid)`
// 会命中调用者自己的进程组，那会杀掉整个天枢进程（含 TUI 与所有并行
// worker）」。已由 `Setpgid: true` 解决。**这条比 selfKill 更该被记住**：
// 它是 Go 侧特有的自毁路径（TS 无进程组杀）。
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
//	unconditionalApproval     requiresUnconditionalApproval  ✅ **第八十一刀起可达**
//	                                                          （此前记为「不可达」——
//	                                                          见下方订正说明）
//	yoloBypassesUnconditional skipAllApproval（=YOLO 档）   ✅ 语义等价：TS 让 YOLO
//	                                                          豁免 unconditional 门；
//	                                                          Go 侧由本函数首行
//	                                                          `skip 档 return 放行` 等价
//	                                                          实现。但**条件表达式里它
//	                                                          是独立输入**，故列出
//
//	**订正（第八十一刀，独立审查发现）**：上表此前记 `request_path_access` /
//	`computer_use` 均未注册 → unconditionalApproval 分支**实际不可达**。该记录
//	在第八十一刀后**已失效**：
//
//	  - `request_path_access` **已注册**（`default_registry.go`），且
//	    `RequiresUnconditionalApproval` 对它恒返回 true（`approval_risk.go`）。
//	  - 故该分支**现在可达**：manual 档经 `needsApproval` 拦、auto-safe 档经
//	    `isHighRisk` 拦（实测：两档下工具调用 isError=true 且不产生授权）。
//	  - `computer_use` **仍未注册**，其分支（js_eval / browser_adopt / sequence）
//	    仍不可达。
//
//	**Go 侧与 TS 的行为差异（诚实标注）**：TS 在 auto-safe 档下 `shouldAsk=true`
//	后走 `onApprovalRequired` **弹审批**，用户批准后 `grantPath(...)` 授权
//	（`tool-pipeline.ts:1343`）。Go 侧**无交互审批通道**，故 auto-safe/manual 档
//	下该工具被**硬拒**——语义是「不可用」而非「待批准」。
//	**这是既有的架构缺口**（Go CLI 无弹窗），非本工具引入。
//	protectionMode            doomLoop + destructiveGit      ❌ 无 doom-loop 会话态
//	                                                          （`DoomLoop` 零生产文件）
//	                                                          **第六十九刀称量：
//	                                                          接线后行为不变，故不做**
//	                                                          ——见下「protectionMode 称量」
//	allowlisted               isToolAllowed(allowRules)      ✅ 第六十四刀接线
//	                                                          （档位门内豁免，全工具面）
//	                                                          注：`bashWriteNeedsApproval`
//	                                                          内另有一处豁免（bash 写门）
//	bashAllowlisted           isBashCommandAllowlisted       ✅ 第六十六刀接线
//	                                                          （`bashAllowlistedFor`）
//	canAutoApprove            sensorium 置信度               ❌ 无 sensorium
//	                                                          （**且实测冗余**：
//	                                                          256 组穷举 0 差异
//	                                                          ——见 E 节的链称量）
//	                                                          （仅注释/字符串表）
//	computerUsePerAppGate     computer_use 逐应用            ❌ 工具未移植
//
// **未接的输入不改变**已接分支**的正确性**——但「不影响正确性」不等于
// 「无缺口」：上表 ⚠️ 与 ❌ 各行都是**已声明的能力未生效**，各自需要独立的刀。
//
// ## B'. 规则**来源**的缺口：`permissionsOverlay`（第六十九刀新发现）
//
// 上表 B 节的四行（`allowlisted` / `bashAllowlisted` / `denied` / `bashDenied`）
// 在 TS 里都读**两个**来源，Go 侧只读**一个**：
//
//	规则              TS 来源（两来源合并）                        Go 现状
//	allowRules        permissions.allow + overlay.allow            ⚠️ 只读前者
//	denyRules         permissions.deny  + overlay.deny             ⚠️ 只读前者
//	bashAllowPrefixes permissions.bash.allowlist + overlay.bashAllow ⚠️ 只读前者
//	bashDenyPrefixes  permissions.bash.denylist  + overlay.bashDeny ⚠️ 只读前者
//
// **`permissionsOverlay` 是什么**：**会话级运行时 overlay**。TS 在用户于审批
// 提示上批准后写它——**写入者两处，目标不同**（`tool-pipeline.ts` 的
// `learnBashPrefix` / `learnFileApproval` 调用点）：
//
//	learnBashPrefix    → 写 `permissions`（**持久**，跨会话生效）
//	learnFileApproval  → 写 `permissionsOverlay`（**会话级**，仅 manual 档）
//
// 另有「永久记住」双轨：审批卡勾选 `remember` 时，bash 前缀额外落盘
// （`appendBashAllowPrefix`）。**共同点**：都由「用户批准」事件驱动。
//
// **Go 侧缺的不只是"读第二个来源"**：Go 无提示通道 → 无「用户批准」事件 →
// **overlay 无写入者**（`LearnBashPrefix`/`LearnFileApproval` 均未移植，
// 实测零命中）。故这不是「补一行合并切片」，而是「需先有审批交互」——
// 与 B 节剩余三项同属「要先造子系统」类。
//
// **影响**：Go 侧每次都按配置判定（无会话级学习）。**行为不错误**（配置仍在
// 生效），但缺少 TS 的「批准一次、本会话免问」体验——是**能力缺失**，非缺陷。
//
// ## C. `shouldAsk` **之后**的两段（TS `tool-pipeline.ts` 的 YOLO fallback
// 与 headless override 两块）
//
// **本表此前完全漏列**（第六十九刀补）——它们不在三元链里，但改变了判定结果。
//
//	段                        TS 语义                        Go 现状
//	YOLO fallback             skip 档 + pathGrantNeed →       ✅ 语义等价，已实现
//	                          **首触即授**（会话级）            （`loop.go` pathGrant 门的
//	                                                          skip 档分支：`GrantPath`）
//	headless override         `headless && shouldAsk` →       ⚠️ **设计差异**（见下）
//	                          自动放行 5 个写工具
//
// **headless override 的差异是设计选择，不是缺口**：
//   - TS 在 `deps.config.headless` 下对 `HEADLESS_AUTO_APPROVE_WRITE_TOOLS`
//     （`edit_file`/`write_file`/`hash_edit`/`apply_patch`/`ast_edit`）
//     **自动放行**——因为 sidecar/worker 场景无人可答，挂起会耗尽 turn 预算。
//   - Go 侧**无 `Headless` 配置字段**（`headless` 只出现在注释里指「不装 hook
//     的场景」）。`-p` 单次模式**实测**：manual 档写文件 → 被拦 + 指令性
//     拒绝 + **正常结束**（2 个请求，未挂死）。
//   - **为何 Go 不照搬自动放行**：Go 无提示通道，被拦时给模型「换路」指引
//     （第五十刀确立的「指令性非重试拒绝」范式）——这比 TS 的「静默放行写操作」
//     **更保守**。若将来引入 sidecar/worker（无人可答但需推进），再评估是否
//     需要自动放行。**当前行为是有意选择，非缺陷。**
//
// ## D. 剩余项的性质与称量结论（第六十九刀）
//
// **三项的共性是「要先造子系统」，不是接线**（与前面几刀性质不同）：
//
//	protectionMode     需 doom-loop 会话态（状态机 + 检测逻辑）
//	canAutoApprove     需 sensorium 置信度（认知状态，Go 侧仅注释/字符串表）
//	unconditional…     需先移植触发它的工具（request_path_access / computer_use）
//
// **protectionMode 的称量结论：接线后行为不变，故不做**（第六十九刀实测）：
//
//   - 它的作用是「doom-loop 期间**破坏性 git** 需批准」。而
//     `git reset --hard` / `git checkout --` / `git restore` 等**已在
//     `destructivePatterns`**（`bash.go`）→ **硬闸门在任何档位都拦**。
//     接线后该路径**行为完全不变**。
//   - 它的次要产出（非破坏性工具在 doom-loop 下 `Reasons` 加文案、`Level`
//     升至 Medium）**无人消费**——`isHighRiskCall` 只看 `Level == RiskHigh`，
//     Medium 不触发任何门。
//   - 真正的 doom-loop **终止**已有实现：`wedge_guard.go`（`loop.go` 的
//     `observeBatch`/`shouldTerminate`）在「同一批次反复全错」达 3 次时
//     终止 run——那是比「提升风险等级」更彻底的处置。
//
// 故接线 `doomLoopLevel` 会造出**声明但无人消费**的机制（`NeedsApproval`
// 曾经的形态）。**除非将来出现「doom-loop 期间的非破坏性工具需额外审批」
// 这一需求**，否则不做。
//
// `canAutoApprove` / `unconditionalApproval` 同理需先称量收益——接入时
// 按上表逐条补，并更新状态列与日期。
//
// ## E. 认知状态子系统的依赖链称量（第七十刀）
//
// 「造 sensorium」不是一件事，而是一条**链**。逐项核实后，**链上每一项的
// 消费者都缺失**——故整条链当前都属「造了没人用」：
//
//	pressureResult   ✅ **Go 侧已完整实现**（`compact.PressureMonitor`，
//	                    含 `Suggestion` 字段与 oracle 对账
//	                    `TestPressureCheckParity`）——**零实例化**
//	evidenceState    ✅ 已实现（`evidence.go` + `session.Manager`）——见下
//	sensorium         ❌ 未实现——且其消费者 `StrategyProfile` / `vigor` /
//	                    `turn-intent` / `model-policy-selection` **全为 0**
//	turn-intent       ❌ 未移植——**这才是 `pressureResult` 的独立消费者**
//
// **关键事实（推翻「pressureResult 该做」的前提）**：`pressureResult` 在 TS
// 侧**有独立于 sensorium 的消费者**——`turn-intent` 的
// `thrashingSuggestion: input.pressureResult.suggestion`。但 Go 侧
// **无 `turn-intent` 实现**（`TurnIntent` 标识符在全仓生产代码里零出现），
// 且它自身还依赖 `strategy` + `sensorium` + `pheromones` +
// `recentToolHistory`（**四项 Go 侧皆缺**）。
//
// **故「做 pressureResult」在当前 Go 侧是空转**——它已做完，缺的是
// **消费者**。接线它需要先有 `turn-intent`，而 `turn-intent` 需要 sensorium
// 与 pheromone 两条链。**与 `protectionMode` / `canAutoApprove` 同型**：
// 造出来没有消费者，就是 `NeedsApproval` 曾经的形态。
//
// **什么时候该做**：当 Go 侧出现「需要按上下文压力做**方向提示**（thrashing
// 时建议任务分解）」这一需求时——那时 `pressureResult` 直接可用（已实现），
// 只需接一条消费路径。**现在不做**，因为该需求本身尚未被提出。
//
// **sensorium 链的最小起点建议**（若将来要做）：从 `turn-intent` 反向切入
// ——它是**唯一有明确外部价值**（给用户方向提示）的节点，而非从 sensorium
// 这个中间层开始造。见 `evidence.go` 的「范围声明」段。
//
// **`canAutoApprove` 的冗余性（第七十刀探针实测）**：它是 sensorium 的
// **直接消费者**，但**接线它不改变任何行为**——穷举 256 组输入（档位 ×
// 风险级 × 置信度 × sensorium 有无 × needsApproval），结果差异 **0 组**。
//
// 推导：它的定义要求 `mode === 'auto-safe'` 且 `risk ∈ {none, low}`，而那个
// 位置的回退分支 `auto-safe ? isHighRisk` 在**同条件下必然 false**——两者
// 同值。故为它造 sensorium 是**白做**（与 `protectionMode` 同型）。
//
// **这进一步削弱了「造 sensorium」的理由**：它的两个直接消费者
// （`canAutoApprove` 冗余、`pressureResult` 无消费者）都不构成需求。
//
// # 已知欠账（`registry.go` 的前置③，**仍未做**）
//
// `registry.go` 列的接线条件③「写工具的 `RequiresApproval` 语义订正」
// **仍未完成**（第六十九刀核实：4 处仍是 `p.ApprovalMode !=
// "dangerously-skip-permissions"`，而 TS 是 `requiresApproval: () => true`）。
//
// **①②已完成**（本文件即②的落地：确定性解析取代提示往返通道；
// `AssessToolRisk` 即①）。故 `registry.go` 那段「零调用者 / 需前置①②③」
// 的表述已过期——见该文件的新注释。
//
// **为什么③仍未做**：本门已独立按档位判定（`skip` 档在最前短路），故
// **行为当前正确**。但这是**重复判定**（工具层与本门各判一次档位），
// 将来任一处改动会导致不一致——是结构性隐患，非当前缺陷。
// 订正会改变工具层语义并可能波及既有断言（如 `bash_test.go` 记录的同类修正），
// 属独立的语义对齐刀。**一次改两处会让「哪处生效」不可归因**——与本文件
// 「一次只接一条输入」的原则一致。
//
// 订正时的判据：4 处 `RequiresApproval` 改为恒 `true`，并确认本门仍覆盖
// skip 档（本文件的 skip 短路即为此保留）。**注意**：`bashTool` 的
// `RequiresApproval` 已在第五十刀订正为 `isDestructiveCommand`（**不在此列**，
// 它是硬闸门语义，不是档位语义）——故③只涉及 4 个**写工具**。
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
// TS（`tool-pipeline.ts` 的 `bashWriteRequiresApproval` 复合条件）：
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
// 对账 TS `const isHighRisk = risk.level === 'high'`。
//
// **doomLoopLevel 传空串**：Go 侧无 doom-loop 会话态（`AssessToolRisk` 的该参数
// 只影响 `SuggestedAction`，不改变 `Level`）——故传空串与 TS 省略该输入等价。
func isHighRiskCall(tc toolCall) bool {
	return AssessToolRisk(tc.name, tc.input, "").Level == RiskHigh
}
