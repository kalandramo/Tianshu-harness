package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/plan"
)

// plan.go —— 统一计划生命周期工具。
//
// 对账 TS `src/tools/plan.ts`（771 行）。四个 action：submit / close /
// enter_mode / exit_mode。
//
// # 与 TS 的差异（**明示，非等价**）
//
//  1. ~~**enter_mode / exit_mode 是诚实声明而非实现**~~ → **第七十九刀已接线**。
//     本节原文写于机制未接线时（称「Go 侧无任何写工具禁用机制」）——**该断言已失效**：
//     `go/internal/agent/planmode.go` 的 `CheckPlanMode` + `Loop.PlanModeState` +
//     `executeTool` 门链三者已接（由 `planmode_wiring_test.go` 的
//     `TestPlanModeBlocksWriteTools` 钉住），`CallParams.EnterPlanMode` 回调已注入。
//     **保留的语义**：无回调时仍 fail-closed 报错（对账 TS——子代理不得切主代理的
//     计划模式），但那是「当前上下文不可用」而非「暂不支持」。
//     （本行与工具描述于第一百零七刀一并订正——此前只订正了测试。）
//  2. ~~**无 plan 草稿回读**~~ → **第一百零七刀已补**。原文称「Go 侧无 plan mode
//     草稿路径，故只能显式传 plan」——`enter_mode` 会真建 `.rivet/plans/draft-<ms>.md`，
//     故 `submit` 现按 TS `plan.ts:399-425` 回读该草稿，成功后回收（`IsDraftSlug` 守卫）。
//  3. **无 TUI 审批回调**。TS 的 `params.onPlanSubmitted` 推审批卡给 TUI；
//     Go 侧无渲染层，故不移植。submit 成功后只在文本里说明「等待批准」。
//  4. **无交付门禁回调**（`params.assessDelivery`）。TS 的 close 会用它做
//     evidence-gated closure（声称 GREEN 而真实门禁 RED 时拦截）；Go 侧
//     尚无该门禁接口，故 close 走 legacy（信任声明）路径——**这是安全性的
//     降低，已记明**。
//
// # 计划规模门禁的 one-shot 软拦
//
// TS 用模块级 `Set<string>`（warnedSlugs 等）实现「每项按 slug 只拦一次」。
// Go 侧同样用包级 map（进程内生命周期，与 TS 的模块级一致）。

// planWarnedSlugs 记录已就各项软门禁告警过的 slug。
//
// 对账 TS 的 `warnedSlugs` / `anchorWarnedSlugs` / `scaleWarnedSlugs` /
// `falsificationWarnedSlugs` / `requirementWarnedSlugs`（plan.ts:23-40）。
//
// **注意并发**：这些 map 在多会话并发下会互相污染（TS 的模块级 Set 同病）。
// Go 侧保持同语义——不引入锁，因为 TS 也没锁。
var (
	planWarnedSlugs        = map[string]bool{}
	planAnchorWarnedSlugs  = map[string]bool{}
	planScaleWarnedSlugs   = map[string]bool{}
	planFalsifWarnedSlugs  = map[string]bool{}
	planRequireWarnedSlugs = map[string]bool{}
)

// 门禁阈值与正则（对账 TS plan.ts:56-80）。
const (
	planScaleTaskThreshold = 8
	planScaleFileThreshold = 15
)

var (
	planWaveHeadingRe    = mustRe(`(?im)^#{2,5}\s*.*(wave\s*\d|波\s*\d|第[一二三四五六七八九十\d]+波|分波)`)
	planWaveVerifyRe     = mustRe(`(?i)(每波|波间|波后|per[- ]wave|wave 完成).{0,60}(验证|verify|verification|typecheck|测试)|验证命令`)
	planPlaceholderRe    = mustRe(`(?i)\b(TODO|FIXME|TBD|XXX|HACK|placeholder|占位符|待补充|待完善|待填写|待实现|稍后补充|略)\b`)
	planOnlyDotsRe       = mustRe(`(?m)^(\.{3,}|…+|-\s*\.{3,})\s*$`)
	planFalsifHeadingRe  = mustRe(`(?im)^#{2,5}\s*.*(反证|复现|falsification|reproduction)`)
	planRequireHeadingRe = mustRe(`(?im)^#{2,5}\s*.*(需求提炼|需求理解|requirements?(\s+(distillation|summary))?)`)
	planMermaidFenceRe   = mustRe("(?i)```\\s*mermaid")
	planHeadingRe        = mustRe(`(?m)^(#{2,6})\s+\S`)
)

// planMissingDiagramSkeleton 是缺 mermaid 图时给出的骨架（对账 TS plan.ts:50-56）。
const planMissingDiagramSkeleton = "```mermaid\n" +
	"flowchart TD\n" +
	"    U(用户输入) --> R[[入口/路由]]\n" +
	"    R --> L{{LLM/核心逻辑}}\n" +
	"    R --> S[(存储/状态)]\n" +
	"    L --产出--> OUT([结果])\n" +
	"```"

// planTool 是 plan 工具实现。
type planTool struct {
	def     contract.Definition
	enabled bool
}

// Plan 构造 plan 工具。
func Plan() Tool {
	t := &planTool{enabled: true}
	t.def = contract.Definition{
		Name: "plan",
		Description: `统一计划生命周期工具——提交计划供用户审批，或关闭已完成任务。

### 计划文件状态
` + "`.rivet/plans/*.md`" + ` 文件带状态标记行：` + "`> **Status: APPROVED/REJECTED/EXECUTED**`" + `。扫描既有计划时：
- **REJECTED** 计划已被用户否决——不要重新提交、重新提议或提醒用户，除非用户明确要求。
- **EXECUTED** 计划已完成——可作上下文参考，不要重复处理。
- **APPROVED** 计划执行中——继续执行。
- 只有**已提交待批**（无状态标记）的计划等待用户操作。

### Action: submit
提交一份完成的实现计划供用户审批。计划持久化到 ` + "`.rivet/plans/<slug>.md`" + `。

` + "`plan`" + ` 字段必须是具体、可直接实施的设计文档，不是大纲或骨架。

提交门禁——提交前必须自检六项：标题级「需求提炼」章节（H1 之后，用户原话提炼目标 + 非目标）、至少一张 mermaid 图、标题级「反证/复现」章节、大计划（checkbox 任务 >8 或引用文件 >15）的 ` + "`### Wave N`" + ` 分波 + 每波验证命令、无占位符簇/空章节（硬门禁，重提不豁免）、file:line 锚点与当前工作树一致。未达标项会在一次驳回中逐条列出，补完后用相同 title 重提（每项软门禁只拦一次）。

省略 ` + "`plan`" + ` 字段则从活动计划文件（plan mode 草稿）提交。先用 write_file/edit_file 把计划增量写入草稿。

计划包含多个方案时，传 ` + "`options`" + `（最多 3 个）供用户在审批时选择。

### Action: close
预览或应用计划闭环更新。默认预览模式（不写盘）。设 apply=true 才写回计划文件。

仅支持 docs/superpowers/plans/ 或 .rivet/plans/ 下的 Markdown 文件。

### Action: enter_mode
自主进入计划模式，先规划再动手（写工具将被禁用；会创建计划草稿文件）。命中以下任一情况时主动使用：新功能实现、多文件（>2-3 个）改动、存在多个有效方案、架构决策、需求不清需要先探索。不要用于：单点小修、用户已给出详细逐步指令的任务、纯研究/问答。进入**无需用户确认**——用户的审批门在计划提交时，不在进入时。已在规划中时重复调用幂等。用户通过会话内审批卡（桌面端）或 /plan-approve（TUI）批准提交的计划；全部任务完成后 ` + "`plan close`" + ` apply=true 会把计划标记为 EXECUTED。

### Action: exit_mode
退出计划模式、解除写锁。不修改计划文件（不标记 EXECUTED、不勾选 checkbox）。审批即自动退出，仅在系统未自动退出、或用户明确要求「直接开始写代码」时调用。`,
		InputSchema: objSchemaOrdered([]string{"action", "title", "plan", "options", "file_path", "tasks", "apply", "verifiedCommands", "deliveryState", "note", "updateClosure"}, map[string]any{
			"action": enumPropOrdered("语义见上方各 Action 章节。", []string{"submit", "close", "enter_mode", "exit_mode"}),
			"title":  strProp("[submit] 简短描述性计划标题（用于生成文件 slug）"),
			"plan":   strProp("[submit] 完整计划 Markdown。省略则从 plan mode 活动计划文件读取。"),
			"options": arrPropOrdered("[submit] 计划含 2-3 个不同方案时列出，供用户审批时选择。", objPropMapOrdered(
				[]string{"label", "description"},
				map[string]any{
					"label":       strProp("方案短名（推荐项追加 \"(Recommended)\"）"),
					"description": strProp("取舍简要说明"),
				})),
			"file_path":        strProp("[close] docs/superpowers/plans/ 下的计划 Markdown 路径"),
			"tasks":            strProp("[close] 任务选择，如 1、1-3、1,3-4 或 all"),
			"apply":            boolProp("[close] 写回计划文件（默认 false 预览模式）"),
			"verifiedCommands": arrayPropOrdered("[close] 闭环摘要中包含的验证命令", "string"),
			"deliveryState":    enumPropOrdered("[close] 交付门状态", []string{"GREEN", "YELLOW", "RED"}),
			"note":             strProp("[close] 可选闭环备注"),
			"updateClosure":    boolProp("[close] 是否 upsert 执行状态与闭环（默认 true）"),
		}),
	}
	return t
}

func (t *planTool) Definition() contract.Definition { return t.def }
func (t *planTool) Enabled() bool                   { return t.enabled }
func (t *planTool) ConcurrencySafe() bool           { return false }

// RequiresApproval 对账 TS `PLAN_TOOL.requiresApproval()` —— 恒 false。
//
// 理由（逐字对账 TS 注释）：plan close 只动 plan markdown（checkbox + 闭环段），
// 在 docs/superpowers/plans/ 或 .rivet/plans/ 下、经路径校验且可逆，
// 故跳过审批门以避免打断自动化 close 流程。
func (t *planTool) RequiresApproval(*CallParams) bool { return false }

func (t *planTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *planTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	action, _ := p.Input["action"].(string)
	switch action {
	case "submit":
		return planSubmitExecute(p), nil
	case "close":
		return planCloseExecute(p), nil
	case "enter_mode":
		return planEnterModeExecute(p), nil
	case "exit_mode":
		return planExitModeExecute(p), nil
	}
	return contract.Result{
		Content: fmt.Sprintf("错误：未知 action「%s」。请使用 \"submit\"、\"close\"、\"enter_mode\" 或 \"exit_mode\"。", action),
		IsError: true,
	}, nil
}

// planEnterModeExecute 切入计划模式（第七十九刀接线）。
//
// 对账 TS `planEnterModeExecute`（`plan.ts:324`）。
//
// **接线背景**：此前本 action 走 `planModeUnsupported` 明确报错，理由写
// 「Go 侧尚未移植写工具禁用机制」——**但机制其实早已实现**
// （`agent/planmode.go` 的 `CheckPlanMode`），只是没接线。本刀补上。
//
// **fail-closed**：`EnterPlanMode` 回调为 nil 时明确报错（对账 TS：
// 子代理不能把主代理切入计划模式）。
func planEnterModeExecute(p *CallParams) contract.Result {
	if p.EnterPlanMode == nil {
		return contract.Result{
			Content: "错误：当前上下文不可用 enter_mode（子代理不能把主代理切入计划模式）。",
			IsError: true,
		}
	}
	activePlanFilePath, alreadyPlanning := p.EnterPlanMode()
	if alreadyPlanning {
		msg := "已在计划模式中。"
		if activePlanFilePath != "" {
			msg += " 活动计划草稿：" + activePlanFilePath
		}
		return contract.Result{Content: msg}
	}
	lines := []string{
		"已进入计划模式——写工具已禁用（计划草稿文件除外）。",
	}
	if activePlanFilePath != "" {
		lines = append(lines, "计划草稿: "+activePlanFilePath)
	}
	lines = append(lines,
		"",
		"下一步：",
		"1. 先用 todo 建调研清单（3-6 项：摸清各模块现状、外部调研、设计收敛），最后一项固定为「汇总写计划并用 plan action=submit 提交审批」；逐项勾掉推进。计划正文只写计划文件，不进 todo。",
		"2. 调研：多模块任务用 delegate_batch 一次并行派 2-4 个只读 code_scout（按模块/文件域切分），汇总发现。",
		"3. 用 write_file/edit_file 把计划增量写入草稿——开头（H1 之后）先写「## 需求提炼」：用用户原话提炼需求目标与非目标（submit 门禁）。",
		"4. 瑶光反证（必需章节，submit 门禁）：关键断言在计划期复现——设计定稿后回读引用代码到 file:line、bugfix 跑 run_tests 拿 RED 证据、或派 profile=adversarial_verifier authority=yaoguang。复现不了的推论写为待验证假设，不当结论。",
		"5. 用 plan action=submit 提交（省略 plan 字段即从草稿提交）。提交门禁：标题级「需求提炼」章节、一张 ```mermaid 图、标题级「反证/复现」章节、>8 任务/>15 文件时 ### Wave N 分波——所有未达标项一次驳回列全。提交后用户通过会话内审批卡批准；不要让用户手输 /plan-approve 或任何命令。",
	)
	return contract.Result{Content: strings.Join(lines, "\n")}
}

// planExitModeExecute 退出计划模式（第七十九刀接线）。
//
// 对账 TS `planExitModeExecute`（`plan.ts:354`）：退出并解除写限制，**不修改
// 计划文件**。正常流程审批即自动退出；本 action 是后备。
//
// **fail-closed**：`ExitPlanMode` 回调为 nil 时明确报错。
func planExitModeExecute(p *CallParams) contract.Result {
	if p.ExitPlanMode == nil {
		return contract.Result{
			Content: "错误：当前上下文不可用 exit_mode（子代理不能退出主代理的计划模式）。",
			IsError: true,
		}
	}
	p.ExitPlanMode()
	return contract.Result{Content: "已退出计划模式——写操作限制已解除。"}
}

// ── submit ──

// planSubmitExecute 处理 submit（对账 TS `planSubmitExecute`，plan.ts:418-620）。
func planSubmitExecute(p *CallParams) contract.Result {
	title, _ := p.Input["title"].(string)
	if strings.TrimSpace(title) == "" {
		return contract.Result{Content: "错误：title 必填", IsError: true}
	}

	// ── plan 正文来源：显式传入 或 活动计划草稿回读 ──
	//
	// 对账 TS `planSubmitExecute`（`plan.ts:399-425`）：
	//
	//	let submittedFromDraft: string | null = null
	//	if (typeof planContent !== 'string' || !planContent.trim()) {
	//	  const draftPath = params.activePlanFilePath
	//	  if (!draftPath) return 错误「未设置活动计划文件时 plan 必填」
	//	  draftText = await readFile(join(cwd, draftPath), 'utf-8')   ← 读失败/为空各有分支
	//	  planContent = draftText; submittedFromDraft = draftPath
	//	}
	//
	// **为什么必须有这条**：`plan` 的描述与此前的 plan-mode 指令块
	// （`prompt/modeblocks.go` 的 `<plan-mode>`）都告诉模型「省略 `plan` 字段则
	// 从活动计划文件读取」。缺这条时那句是**假话**——模型照办必失败。它的
	// 上游 `enter_mode`（建草稿）自第七十九刀起已工作，故缺口在 submit 这一环。
	planContent, _ := p.Input["plan"].(string)
	submittedFromDraft := ""
	if strings.TrimSpace(planContent) == "" {
		draftRel := p.ActivePlanFilePath
		if draftRel == "" {
			return contract.Result{
				Content: "错误：未设置活动计划文件时 plan 必填。请先写入计划文件，或直接传入 plan 内容。",
				IsError: true,
			}
		}
		draftAbs := filepath.Join(p.Cwd, filepath.FromSlash(draftRel))
		raw, readErr := os.ReadFile(draftAbs)
		if readErr != nil {
			return contract.Result{
				Content: "读取活动计划文件失败（" + draftRel + "）：" + readErr.Error(),
				IsError: true,
			}
		}
		if strings.TrimSpace(string(raw)) == "" {
			return contract.Result{
				Content: "错误：活动计划文件为空（" + draftRel + "）。请先写入计划，再提交。",
				IsError: true,
			}
		}
		planContent = string(raw)
		submittedFromDraft = draftRel
	}

	submitOptions, optErr := parseSubmitOptions(p.Input["options"])
	if optErr != "" {
		return contract.Result{Content: "错误：" + optErr, IsError: true}
	}

	// ── 指针回传门禁（硬性，先于一切软门禁）──
	//
	// arg post-processor 会把历史里的 plan 字段改写成 "[plan persisted to …]"
	// 显示指针——包括被门禁拒绝、实际并未落盘的提交。模型复用该指针重提时
	// 必须在这里拦下。
	if matched := DetectPointerPlaceholder(planContent); matched != "" {
		// 优雅化解：指针所指计划文件确实在磁盘上 → 幂等成功。
		if rel := parsePlanPointerPath(matched, planContent); rel != "" {
			abs := filepath.Join(p.Cwd, rel)
			if resolved, ok := ResolveIdempotentPointer(IdempotentResolveInput{
				Mode: ResolveModeEdit, FilePath: abs, Value: planContent, MatchedPrefix: matched,
			}); ok {
				return contract.Result{Content: resolved}
			}
		}
		return contract.Result{
			Content: "❌ 提交被拦截：plan 字段的内容是系统显示占位符（\"" + matched + " …\"），不是真实的计划正文。" +
				"原因：对话历史中，提交后的参数会被替换为显示摘要（包括被门禁拒绝、未落盘的提交）。AI 有时会误把摘要当正文复用。" +
				"修复：在 plan 字段写出完整计划正文；或先 read_file 目标计划文件后重提。" +
				"\n[" + PointerGuardErrorMarker + "]",
			IsError: true,
		}
	}

	// 剥离历史 approve/reject 状态标记——驳回修订后整读重提交时，残留的
	// "> **Status: REJECTED**" 会让新提交被误判为 rejected。
	planBody := plan.StripPlanStatusMarkers(planContent)

	slug := plan.Slugify(title)

	fullContent := planBody
	if !strings.HasPrefix(strings.TrimSpace(planBody), "# ") {
		fullContent = "# " + strings.TrimSpace(title) + "\n\n" + strings.TrimSpace(planBody)
	}
	fullContent = strings.TrimSpace(fullContent) + "\n"

	// ── 硬拦（每次命中都拦，非 one-shot）：占位符簇 / 空章节 / 纯省略号段落 ──
	if reason := checkPlanForPlaceholders(fullContent); reason != "" {
		return contract.Result{
			Content: "⚠️ 计划尚未保存 — " + reason + "\n\n" +
				"用 edit_file 完善活动计划文件：根因、每文件 diff/伪代码、取舍表、验证清单等。补完后同 title 重提。",
			IsError: true,
		}
	}

	// ── 软门禁聚合：全部检查一次跑完，一次拒绝列全所有缺口 ──
	var blocks []string

	if !planRequireHeadingRe.MatchString(fullContent) && !planRequireWarnedSlugs[slug] {
		planRequireWarnedSlugs[slug] = true
		blocks = append(blocks, "缺「需求提炼」章节——在计划开头（H1 之后）补一个标题含\"需求提炼\"的 ## 级章节，用用户原话提炼需求：\n"+
			"- **目标**：用户要达成什么（尽量引用/贴近用户原话，不要改写成官方套话）\n"+
			"- **非目标**：明确不做什么（边界外但容易被误并入的事项）")
	}

	if !planMermaidFenceRe.MatchString(planBody) && !planWarnedSlugs[slug] {
		planWarnedSlugs[slug] = true
		blocks = append(blocks, "缺 Mermaid 图——用 edit_file 在计划里补一张架构/数据流图（哪怕核心 3–5 个节点）。骨架：\n\n"+
			planMissingDiagramSkeleton+"\n\n"+
			"图形说明：(圆角)=输入/用户 · [[子程序]]=agent · {{六边形}}=LLM · [(圆柱)]=存储 · {菱形}=决策。")
	}

	if !planFalsifHeadingRe.MatchString(fullContent) && !planFalsifWarnedSlugs[slug] {
		planFalsifWarnedSlugs[slug] = true
		blocks = append(blocks, "缺「瑶光反证」章节——需要一个标题含\"反证\"或\"复现\"的 ## 级章节（正文/列表里提到不算）：\n"+
			"- **关键断言清单**：每条断言 + 计划期证据（read/grep 到 file:line、测试输出摘要）\n"+
			"- **原缺陷复现**（bugfix）：复现结果摘要（RED）\n"+
			"- **待验证假设**：计划期无法复现的推论 + 执行期如何验证")
	}

	// 事实锚点校验（best-effort——守卫自身永不阻塞 submit）。
	var anchorDriftNote string
	if report, err := plan.CheckPlanFactAnchors(fullContent, p.Cwd); err == nil && len(report.Drifts) > 0 {
		if !planAnchorWarnedSlugs[slug] {
			planAnchorWarnedSlugs[slug] = true
			blocks = append(blocks, fmt.Sprintf("%d 个事实锚点与当前项目不符：\n\n%s\n\n用 read/grep 核实后修正引用；确认新建则标注「新增」；根错位的引用补全根前缀。",
				len(report.Drifts), plan.FormatAnchorDrifts(report.Drifts)))
		} else {
			anchorDriftNote = fmt.Sprintf("\n⚠ 锚点残留提示：%d 个引用仍与当前工作区不符（已放行）。执行时以现实为准并在交付报告留痕。", len(report.Drifts))
		}
	}

	// 规模门禁：大计划（任务 > 8 或文件 > 15）必须显式分波 + 每波验证命令。
	var scaleNote string
	scale := checkPlanScale(fullContent)
	if scale.oversized && !scale.hasWaveStructure {
		if !planScaleWarnedSlugs[slug] {
			planScaleWarnedSlugs[slug] = true
			blocks = append(blocks, fmt.Sprintf("计划规模超阈值（任务 %d 个 / 涉及文件 %d 个，阈值 %d/%d），但没有分波结构。补充「分波执行」章节：\n"+
				"- 用 `### Wave 1 / Wave 2 / …` 标题把任务切成 2-4 个可独立验证的波次\n"+
				"- 每波末尾声明验证要点（typecheck / 测试）\n"+
				"- 波的边界放在功能可自证的位置（一波结束 = 可编译可测试）",
				scale.taskCount, scale.fileCount, planScaleTaskThreshold, planScaleFileThreshold))
		} else {
			scaleNote = fmt.Sprintf("\n⚠ 规模留痕：计划超阈值（任务 %d / 文件 %d）且无分波结构（已放行）。执行时建议手动分批 + 阶段性验证。",
				scale.taskCount, scale.fileCount)
		}
	}

	if len(blocks) > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "⚠️ 计划尚未保存 — 共 %d 项缺口（一次列全，免去逐条往返）：\n\n", len(blocks))
		for i, b := range blocks {
			fmt.Fprintf(&sb, "%d. %s\n\n", i+1, b)
		}
		sb.WriteString("逐条补完后同 title 重提（不要在聊天重贴全文）。每项只拦一次——确认某项不适用时原样重提即放行。")
		return contract.Result{Content: sb.String(), IsError: true}
	}

	// ── 产出模型留痕 ──
	//
	// **与 TS 的差异（明示）**：TS 用 `params.sessionModel` 记录本计划由哪个
	// 模型写出（H1 前的 `> **Model: <name> (<tier>)**` 行）。Go 侧 `CallParams`
	// **无 sessionModel 字段**（已核实 registry.go 全字段），且无写入方——
	// 硬造一个恒空字段会产出「声明但永不生效」的静默失效。故此处**不写留痕**，
	// 待会话模型信息透传到 CallParams 后补接。
	contentToPersist := fullContent

	relativePath, err := plan.WritePlan(p.Cwd, slug, contentToPersist, submitOptions)
	if err != nil {
		return contract.Result{Content: "写入计划失败：" + err.Error(), IsError: true}
	}

	// ── 草稿回收 ──
	//
	// 对账 TS `plan.ts:583-590`：
	//
	//	if (submittedFromDraft && isDraftSlug(basename(submittedFromDraft, '.md'))) {
	//	  await rm(join(params.cwd, submittedFromDraft), { force: true }).catch(() => {})
	//	}
	//
	// **两道守卫，缺一不可**：
	//  1. `submittedFromDraft != ""`——只有**真从草稿提交**才删。显式传 plan 的
	//     修订会话不该被动到活动文件。
	//  2. `plan.IsDraftSlug`——只有**草稿形态**（`draft-<n>`）才删。修订会话里
	//     `ActivePlanFilePath` 可能就指向**用户已批准的计划本身**，删它等于
	//     毁掉用户的工作（这是防数据丢失的守卫，有对抗性测试钉住）。
	//
	// **best-effort**：清理失败不阻塞 submit（对账 TS 的 `.catch(() => {})`）
	// ——内容已安全落进规范文件，草稿只是残留物。
	if submittedFromDraft != "" && plan.IsDraftSlug(draftSlugOf(submittedFromDraft)) {
		_ = os.Remove(filepath.Join(p.Cwd, filepath.FromSlash(submittedFromDraft)))
	}

	optionsHint := ""
	if len(submitOptions) >= 2 {
		labels := make([]string, len(submitOptions))
		for i, o := range submitOptions {
			labels[i] = "`" + o.Label + "`"
		}
		optionsHint = fmt.Sprintf("\n已记录选项（%d）。用户可在审批时选择：%s", len(submitOptions), strings.Join(labels, ", "))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ 计划已提交：**%s**\n", strings.TrimSpace(title))
	fmt.Fprintf(&sb, "文件：`%s`\n", relativePath)
	fmt.Fprintf(&sb, "Slug：`%s`\n", slug)
	sb.WriteString(optionsHint)
	sb.WriteString(anchorDriftNote)
	sb.WriteString(scaleNote)
	sb.WriteString("\n\n审批请求已推送给用户——批准即开始执行，也可附修订意见驳回。")
	sb.WriteString("\n\n**请在此等待——在用户批准前不要继续推进。**")

	return contract.Result{Content: sb.String()}
}

// draftSlugOf 从项目相对路径取 slug（basename 去 `.md`）。
//
// 对账 TS `plan.ts:588` 的 `basename(submittedFromDraft, '.md')`。
// 用途：判定活动计划文件是否**草稿形态**（`plan.IsDraftSlug`）——
// 只有草稿才可回收，用户已批准的规范计划文件绝不能删。
func draftSlugOf(relPath string) string {
	base := filepath.Base(filepath.ToSlash(relPath))
	return strings.TrimSuffix(base, ".md")
}

// parsePlanPointerPath 从 plan 指针首行解析出项目相对路径。
//
// 对账 TS `parsePlanPointerPath`（plan.ts:325-333）。格式：
// `[plan persisted to <path> — …]`。
func parsePlanPointerPath(prefix, value string) string {
	firstLine := strings.TrimLeft(strings.TrimSpace(value), " \t\n\r")
	if idx := strings.IndexAny(firstLine, "\r\n"); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	if !strings.HasPrefix(firstLine, prefix) {
		return ""
	}
	after := strings.TrimLeft(firstLine[len(prefix):], " \t")
	if idx := strings.Index(after, " — "); idx >= 0 {
		return strings.TrimSpace(after[:idx])
	}
	if idx := strings.IndexAny(after, " \t"); idx >= 0 {
		return strings.TrimSpace(after[:idx])
	}
	return strings.TrimSpace(after)
}

// parseSubmitOptions 解析 options 字段。
//
// 对账 TS `parseSubmitOptions`（plan.ts:113-141）。返回 (options, 错误文案)。
// 最多 3 个；label 必填且唯一；不得使用保留的审批标签。
func parseSubmitOptions(raw any) ([]plan.PlanOption, string) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, ""
	}
	var out []plan.PlanOption
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label, _ := m["label"].(string)
		desc, _ := m["description"].(string)
		if strings.TrimSpace(label) == "" || desc == "" {
			continue
		}
		out = append(out, plan.PlanOption{Label: strings.TrimSpace(label), Description: strings.TrimSpace(desc)})
	}
	if len(out) == 0 {
		return nil, ""
	}
	if len(out) > 3 {
		return nil, "At most 3 options are allowed"
	}
	seen := map[string]bool{}
	for _, o := range out {
		key := strings.ToLower(strings.TrimSpace(o.Label))
		if seen[key] {
			return nil, "Option labels must be unique"
		}
		if planReservedOptionLabels[key] {
			return nil, "Option labels must not use reserved approval labels"
		}
		seen[key] = true
	}
	return out, ""
}

// planReservedOptionLabels 是对账 TS `RESERVED_OPTION_LABELS`（plan.ts:105-107）。
var planReservedOptionLabels = map[string]bool{
	"approve": true, "reject": true, "reject and exit": true, "revise": true,
}

// ── close ──

// planCloseExecute 处理 close（对账 TS `planCloseExecute`，plan.ts:655-750）。
func planCloseExecute(p *CallParams) contract.Result {
	rawPath, _ := p.Input["file_path"].(string)
	if strings.TrimSpace(rawPath) == "" {
		return contract.Result{Content: "错误：file_path 必填", IsError: true}
	}
	tasks, _ := p.Input["tasks"].(string)
	if strings.TrimSpace(tasks) == "" {
		return contract.Result{Content: "错误：tasks 必填", IsError: true}
	}

	// 路径校验（逃逸拦截复用 pathsafe 的判定）。
	abs, err := resolvePlanClosePath(p.Cwd, rawPath)
	if err != "" {
		return contract.Result{Content: "错误：" + err, IsError: true}
	}

	rel := strings.ReplaceAll(filepath.ToSlash(mustRel(p.Cwd, abs)), "\\", "/")
	inSuperpowers := strings.HasPrefix(rel, "docs/superpowers/plans/") && strings.HasSuffix(rel, ".md")
	inRivet := strings.HasPrefix(rel, ".rivet/plans/") && strings.HasSuffix(rel, ".md")
	if !inSuperpowers && !inRivet {
		return contract.Result{
			Content: fmt.Sprintf("错误：plan close 仅支持 docs/superpowers/plans/ 或 .rivet/plans/：%s", rel),
			IsError: true,
		}
	}

	raw, readErr := os.ReadFile(abs)
	if readErr != nil {
		return contract.Result{Content: "错误：未找到计划文件：" + abs, IsError: true}
	}

	deliveryState, _ := p.Input["deliveryState"].(string)
	if deliveryState != "" && !isDeliveryState(deliveryState) {
		return contract.Result{Content: "错误：deliveryState 必须是 GREEN、YELLOW 或 RED", IsError: true}
	}

	verifiedCommands := asStringSlice(p.Input["verifiedCommands"])

	apply, _ := p.Input["apply"].(bool)
	updateClosure := true
	if v, ok := p.Input["updateClosure"].(bool); ok {
		updateClosure = v
	}

	opts := plan.PlanCloseOptions{
		Tasks:            tasks,
		VerifiedCommands: verifiedCommands,
		DeliveryState:    deliveryState,
		Note:             strOrEmpty(p.Input["note"]),
		UpdateClosure:    &updateClosure,
	}

	result, closeErr := plan.ClosePlanMarkdown(string(raw), opts)
	if closeErr != nil {
		return contract.Result{Content: "错误：" + closeErr.Error(), IsError: true}
	}

	if apply {
		// EXECUTED 标记只在 apply 时写（对账 TS：realGreen 才标，但 Go 侧
		// 无交付门禁回调，故按「显式 deliveryState == GREEN 或未指定」标记）。
		contentToWrite := result.Content
		if deliveryState == "" || deliveryState == "GREEN" {
			contentToWrite = plan.InsertPlanStatusMarker(result.Content, plan.StatusExecuted, planNowISO())
		}
		if err := os.WriteFile(abs, []byte(contentToWrite), 0o644); err != nil {
			return contract.Result{Content: "写入失败：" + err.Error(), IsError: true}
		}
		action := planClosureAction(result)
		var sb strings.Builder
		fmt.Fprintf(&sb, "计划已关闭：%s\n", rel)
		fmt.Fprintf(&sb, "任务：%s\n", tasks)
		fmt.Fprintf(&sb, "已更新复选框：%d\n", result.TotalChangedCheckboxes)
		fmt.Fprintf(&sb, "闭环：%s\n", action)
		if deliveryState != "" {
			fmt.Fprintf(&sb, "交付：%s\n", deliveryState)
		}
		return contract.Result{Content: strings.TrimRight(sb.String(), "\n")}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "计划关闭预览：%s\n", rel)
	fmt.Fprintf(&sb, "任务：%s\n", tasks)
	fmt.Fprintf(&sb, "将更新复选框：%d\n", result.TotalChangedCheckboxes)
	fmt.Fprintf(&sb, "闭环：%s\n", planClosureAction(result))
	if deliveryState != "" {
		fmt.Fprintf(&sb, "交付：%s\n", deliveryState)
	}
	sb.WriteString("\n变更：\n")
	if len(result.Changes) == 0 {
		sb.WriteString("  (none)\n")
	}
	for _, c := range result.Changes {
		fmt.Fprintf(&sb, "  - Task %d: %d/%d checkbox(es) updated\n", c.TaskNumber, c.ChangedCheckboxCount, c.CheckboxCount)
	}
	sb.WriteString("\n未写入任何文件。以 apply=true 重跑以写入计划闭环。")
	return contract.Result{Content: sb.String()}
}

// planClosureAction 对账 TS `closureAction`（plan.ts:199-203）。
func planClosureAction(r *plan.PlanCloseResult) string {
	if r.ClosureInserted {
		return "insert"
	}
	if r.ClosureUpdated {
		return "update"
	}
	return "unchanged"
}

// isDeliveryState 对账 TS `isDeliveryState`（plan.ts:181-183）。
func isDeliveryState(v string) bool {
	return v == "GREEN" || v == "YELLOW" || v == "RED"
}

// asStringSlice 对账 TS `asStringArray`（plan.ts:185-189）。
func asStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func strOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

// ── 计划门禁辅助（对账 TS plan.ts 的纯函数段）──

// planScaleCheck 是计划规模检查结果（对账 TS `PlanScaleCheck`，plan.ts:58-62）。
type planScaleCheck struct {
	taskCount        int
	fileCount        int
	oversized        bool
	hasWaveStructure bool
}

// checkPlanScale 估计计划规模并检测分波声明。
//
// 对账 TS `checkPlanScale`（plan.ts:66-72`）：
//
//	taskCount = (content.match(/^\s*[-*]\s*\[[ xX]\]/gm) ?? []).length
//	fileCount = extractPlanAnchors(content).length
func checkPlanScale(content string) planScaleCheck {
	taskCount := len(planCheckboxLineRe.FindAllString(content, -1))
	fileCount := len(plan.ExtractPlanAnchors(content))
	oversized := taskCount > planScaleTaskThreshold || fileCount > planScaleFileThreshold
	hasWave := planWaveHeadingRe.MatchString(content) && planWaveVerifyRe.MatchString(content)
	return planScaleCheck{taskCount: taskCount, fileCount: fileCount, oversized: oversized, hasWaveStructure: hasWave}
}

// planCheckboxLineRe 匹配 checkbox 行（用于规模统计）。
var planCheckboxLineRe = mustRe(`(?m)^\s*[-*]\s*\[[ xX]\]`)

// mustRe 编译正则，失败即 panic（模式是编译期常量，失败属编码错误）。
func mustRe(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// planNowISO 返回 ISO-8601 时间戳（对账 JS `new Date().toISOString()`）。
//
// 格式必须 24 字符：`2026-09-22T05:12:07.169Z`。
func planNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// resolvePlanClosePath 解析并校验 close 的 file_path。
//
// 返回 (绝对路径, 错误文案)。复用 pathsafe 的逃逸拦截语义。
func resolvePlanClosePath(cwd, rawPath string) (string, string) {
	abs := rawPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, rawPath)
	}
	abs = filepath.Clean(abs)
	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return "", "解析 cwd 失败"
	}
	rel, err := filepath.Rel(cwdAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "路径逃逸出项目目录"
	}
	return abs, ""
}

// mustRel 返回相对路径（失败时返回原绝对路径）。
func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

// checkPlanForPlaceholders 是硬门禁：占位符簇 / 空章节 / 纯省略号段落。
//
// 对账 TS `checkPlanForPlaceholders`（plan.ts:152-179`）。返回错误文案；
// 通过返回空串。
func checkPlanForPlaceholders(content string) string {
	hits := planPlaceholderRe.FindAllString(content, -1)
	if len(hits) >= 3 {
		seen := map[string]bool{}
		var uniq []string
		for _, h := range hits {
			l := strings.ToLower(h)
			if !seen[l] {
				seen[l] = true
				uniq = append(uniq, l)
			}
		}
		return "计划包含过多占位符：" + strings.Join(uniq, ", ") + "。请继续补充具体设计后再提交。"
	}

	if hasEmptyPlanSection(content) {
		return "检测到只有标题、没有正文的空章节。请为每个章节补充具体分析和方案后再提交。"
	}

	if planOnlyDotsRe.MatchString(content) {
		return "检测到仅含省略号的占位段落。请替换为具体设计内容后再提交。"
	}
	return ""
}

// hasEmptyPlanSection 报告是否存在「只有标题、没有正文」的章节。
//
// 对账 TS `hasEmptySection`（plan.ts:84-103`）。
//
// 一个章节「空」的定义：其标题之后（仅跨空行）紧跟着**同级或更浅**的标题。
// 更深的标题说明该章节正文被组织成了子章节——这是正常 markdown 模式
// （`## 实现` → `### 任务 1`）。
func hasEmptyPlanSection(content string) bool {
	openHeadingLevel := -1
	for _, line := range strings.Split(content, "\n") {
		if m := planHeadingRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			if openHeadingLevel != -1 && level <= openHeadingLevel {
				return true
			}
			openHeadingLevel = level
			continue
		}
		if strings.TrimSpace(line) != "" {
			openHeadingLevel = -1
		}
	}
	return false
}
