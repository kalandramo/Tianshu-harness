// planmode.go —— plan 模式的纯函数子集。
//
// 对账 TS `src/agent/plan-mode.ts`（214 行）——**按「消费端先行」筛选后的子集**。
//
// # 移植范围（以及为什么排除其余）
//
// | 符号 | TS 消费方 | 移植 |
// |---|---|---|
// | `checkPlanMode` | `tool-pipeline.ts:1062`（工具执行链） | ✅ |
// | `PLAN_MODE_ALLOWED_TOOLS` | `checkPlanMode` 内部 | ✅ |
// | `canonicalizePathForCompare` / `pathsMatch` / `isUnderScratchDir` | `checkPlanMode` 依赖 | ✅ |
// | `createActivePlanDraftPath` | `loop.ts` 的 `enterPlanMode` | ✅ |
// | `formatActivePlanDraftReceipt` | `tools/edit.ts` / `tools/write-file.ts` | ✅ |
// | `nextShiftTabPlanToggle` / `shiftTabPlanToggleHint` / `approvalModeShortLabel` | **仅 `main.ts`（TUI 层）** | ❌ |
// | `profileIsWriteCapable` / `profileIsPlanModeSafe` | 在 `profile-registry.ts`（独立模块） | ❌ |
//
// **排除理由**（对账跨会话教训「消费端先行」）：TUI 层符号在 Go 侧无渲染层
// 消费方；`profileIs*` 属 profile-registry 模块（独立移植单元）。
//
// # ★ 本文件与「plan 模式机」的关系（重要，勿误读）
//
// 本文件只提供**判定层**。**plan 模式状态机本身未移植**——`enterPlanMode` /
// `exitPlanMode` 依赖 `PromptEngine`（TS 的 1694 行组装引擎，Go 侧架构不同）
// 与写锁机制。故：
//
//   - `CheckPlanMode` 当前**无生产触发方**（`Config` 里没有 planModeState 字段）
//   - 它是**为状态机就位做的准备**，不是「已启用的功能」
//   - `internal/tools/plan.go` 的 `enter_mode`/`exit_mode` 仍是**诚实报错**
//     （见该文件头部「与 TS 的差异」第 1 条）
//
// **为什么仍值得移植**：判定逻辑（白名单 + 路径例外 + 委派 profile 安全）
// 是 plan 模式机里**唯一可独立测试**的部分，且 TS 侧有现成测试可对账。
// 状态机就位后直接接线即可，无需再动本文件。
package agent

import (
	"strconv"
	"strings"
	"time"
)

// PlanModeState 对账 TS `PlanModeState`（`plan-mode.ts:8`）。
//
//	export type PlanModeState = 'off' | 'planning'
//
// **注意与 appendix 的 `planModeState` 不同**：那个是 `'off' | 'planning' |
// 'approved'`（`volatile.ts:379`，多一个 `approved`）。本类型是**工具拦截层**
// 用的两态——`approved` 在拦截层等价于 `off`（计划已批准，写工具解锁）。
type PlanModeState string

const (
	PlanModeOff      PlanModeState = "off"
	PlanModePlanning PlanModeState = "planning"
)

// PlanModeAllowedTools 对账 TS `PLAN_MODE_ALLOWED_TOOLS`（`plan-mode.ts:82-87`）。
//
// 18 个只读/无害工具。**内容由 oracle 逐项对账**（`TestPlanModeAllowedToolsParity`）。
//
// 值得注意的几项：
//   - `run_tests` —— 瑶光反证需要计划期复现
//   - `delegate_task` / `delegate_batch` —— 允许，但**写能力 profile 另拦**
//     （见 `PlanModeCheckContext.DelegatesWriteCapableProfile`）
//   - `ask_user_question` / `plan` / `todo` —— 计划期必需的交互
var PlanModeAllowedTools = map[string]struct{}{
	"read_file": {}, "read_section": {}, "grep": {}, "glob": {}, "repo_map": {},
	"inspect_project": {}, "related_tests": {}, "diff": {}, "todo": {},
	"repo_graph": {}, "web_fetch": {}, "web_search": {}, "memory": {}, "plan": {},
	"ask_user_question": {}, "delegate_task": {}, "delegate_batch": {},
	"run_tests": {},
}

// planModeDelegateTools 对账 TS `DELEGATE_TOOLS`（`plan-mode.ts:94`）——
// 规划模式下仅允许其调度只读侦察 worker 的委派类工具。
var planModeDelegateTools = map[string]struct{}{
	"delegate_task": {}, "delegate_batch": {},
}

// planModeScratchDir 对账 TS `SCRATCH_DIR`（`plan-mode.ts:139`）。
//
// **A5（信号互扰治理 H5）**：plan mode 放行 `.rivet/scratch/` 下的写入——
// 提示词与义务门把 scratch 探针写成验证声称的标准出路，硬拦会形成
// 「系统教你写探针、系统又拦你写探针」的自打架。
const planModeScratchDir = ".rivet/scratch"

// PlanModeCheckContext 对账 TS `PlanModeCheckContext`（`plan-mode.ts:90-104`）。
type PlanModeCheckContext struct {
	// Cwd 是工作目录（用于 resolve 路径）。
	Cwd string
	// TargetFilePath 是 write_file / edit_file 的目标路径（相对或绝对）。
	TargetFilePath string
	// ActivePlanFilePath 是当前活动计划文件（相对 cwd）——**仅允许对该路径写入**。
	// nil = 无活动计划文件。
	ActivePlanFilePath *string
	// DelegatesWriteCapableProfile 表示委派工具请求的 profile 是否超出规划模式
	// 安全集。**由调用方预先算好传入**（对账 TS 注释：保持本函数纯粹可测）。
	//
	// TS 侧调用方（`tool-pipeline.ts:1059`）用 profile registry 算
	// `profileIsPlanModeSafe` 的否定。Go 侧该 registry 未移植，故字段由调用方填。
	DelegatesWriteCapableProfile bool
}

// PlanModeResult 对账 TS `PlanModeResult`（`plan-mode.ts:107-112`）。
type PlanModeResult struct {
	// Allowed 表示是否允许执行。
	Allowed bool
	// Reason 是拒绝原因（Allowed=false 时非空）。
	Reason string
}

// CanonicalizePathForCompare 归一路径用于相等比较。
//
// 对账 TS `canonicalizePathForCompare`（`plan-mode.ts:126-129`）：
//
//	const s = p.replace(/\\/g, '/')
//	return /^[a-zA-Z]:\//.test(s) ? s.toLowerCase() : s
//
// **★ 盘符形路径整体小写，POSIX 路径保持大小写敏感**。
//
// 为什么（TS 注释）：NTFS 大小写不敏感，且盘符大小写在真实环境里不稳定——
// VSCode/Git Bash 常给小写盘符（`c:\proj`）而 `process.cwd()` 给大写
// （`C:\proj`）。逐字节比较会**误拒活动计划文件的写入** → plan mode 下草稿
// 永远为空（桌面「起草中」断流）。
//
// 而 POSIX 文件系统（ext4/APFS）默认区分大小写——折叠会误判不同文件为同一。
func CanonicalizePathForCompare(p string) string {
	s := strings.ReplaceAll(p, `\`, "/")
	if isDriveLetterPath(s) {
		return strings.ToLower(s)
	}
	return s
}

// isDriveLetterPath 对账 TS 正则 `/^[a-zA-Z]:\//`。
func isDriveLetterPath(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
		return false
	}
	return s[1] == ':' && s[2] == '/'
}

// planModePathsMatch 对账 TS `pathsMatch`（`plan-mode.ts:131-133`）：
//
//	canonicalizePathForCompare(resolve(cwd, a)) === canonicalizePathForCompare(resolve(cwd, b))
//
// 用 `nodeResolve`（第五十一刀复刻的 Node `path.resolve`）——**不是**
// `filepath.Join`：两者在「绝对段截断」与「盘符基准」上语义不同
// （见 `nodepath.go` 的实测矩阵）。
func planModePathsMatch(cwd, a, b string) bool {
	return CanonicalizePathForCompare(nodeResolve(cwd, a)) ==
		CanonicalizePathForCompare(nodeResolve(cwd, b))
}

// planModeIsUnderScratchDir 对账 TS `isUnderScratchDir`（`plan-mode.ts:141-145`）：
//
//	const abs = canonicalizePathForCompare(resolve(cwd, target))
//	const scratch = canonicalizePathForCompare(resolve(cwd, SCRATCH_DIR))
//	return abs.startsWith(scratch + '/')
//
// **★ 三条易错语义**（oracle 用例逐个钉住）：
//
//  1. **scratch 目录本身不算**：`.rivet/scratch`（无尾随路径）不匹配
//     `scratch + '/'` → 拦
//  2. **路径穿越被拒绝**：`.rivet/scratch/../../../etc/passwd` resolve 后
//     逃出 scratch → 拦（TS 注释：「resolve 后必须仍在 scratch 目录内」）
//  3. **前缀相似不算**：`.rivet/scratchpad/x.py` 不匹配 `scratch/` → 拦
func planModeIsUnderScratchDir(cwd, target string) bool {
	abs := CanonicalizePathForCompare(nodeResolve(cwd, target))
	scratch := CanonicalizePathForCompare(nodeResolve(cwd, planModeScratchDir))
	return strings.HasPrefix(abs, scratch+"/")
}

// CheckPlanMode 检查工具是否在 plan-mode 下被允许。
//
// 对账 TS `checkPlanMode`（`plan-mode.ts:164-213`）。判定链：
//
//	if (state === 'off') return { allowed: true }                    // ★ 早返回
//	if ((tool === 'write_file' || tool === 'edit_file') && cwd && target) {
//	  if (activePlanFilePath && pathsMatch(...)) return { allowed: true }
//	  if (isUnderScratchDir(cwd, target)) return { allowed: true }
//	}
//	if (DELEGATE_TOOLS.has(tool) && delegatesWriteCapableProfile) return { allowed: false }
//	if (PLAN_MODE_ALLOWED_TOOLS.has(tool)) return { allowed: true }
//	return { allowed: false, reason: ... }
//
// **★ 顺序即语义**（三条易错点）：
//
//  1. **off 早返回**：`off` 状态**忽略** `DelegatesWriteCapableProfile`——
//     oracle 用例 `off-ignores-writecapable-flag` 钉住
//  2. **路径例外只在 write_file/edit_file 上生效**：`bash` 即使 targetFilePath
//     指向活动计划文件也**拦**（oracle 用例 `planning-blocks-bash-even-with-plan-path`）
//  3. **委派检查在白名单之前**：`delegate_task` 在 `PLAN_MODE_ALLOWED_TOOLS` 里，
//     但写能力 profile 的请求**先被拦**（oracle 用例 `planning-blocks-writecapable-delegate_*`）
//
// **Hard-block 的理由**（TS 注释）：提示词说「禁止 patcher」但那是**建议性**的；
// 这里强制，使 plan-mode 会话**永远不能**在批准前派发写代码或跑状态变更命令的
// worker。
func CheckPlanMode(state PlanModeState, toolName string, ctx PlanModeCheckContext) PlanModeResult {
	// ── 1. off 早返回（忽略一切 flag）──
	if state == PlanModeOff {
		return PlanModeResult{Allowed: true}
	}

	// ── 2. 写工具的路径例外 ──
	if (toolName == "write_file" || toolName == "edit_file") &&
		ctx.Cwd != "" && ctx.TargetFilePath != "" {
		if ctx.ActivePlanFilePath != nil && *ctx.ActivePlanFilePath != "" &&
			planModePathsMatch(ctx.Cwd, ctx.TargetFilePath, *ctx.ActivePlanFilePath) {
			return PlanModeResult{Allowed: true}
		}
		// A5：scratch 探针放行——计划期验证声称的标准出路
		if planModeIsUnderScratchDir(ctx.Cwd, ctx.TargetFilePath) {
			return PlanModeResult{Allowed: true}
		}
	}

	// ── 3. 委派写能力 profile：硬拦（在白名单之前）──
	if _, isDelegate := planModeDelegateTools[toolName]; isDelegate && ctx.DelegatesWriteCapableProfile {
		return PlanModeResult{
			Allowed: false,
			Reason: "Plan Mode: only read-only scout profiles (code_scout/doc_scout) and " +
				"test-only verifiers (adversarial_verifier — for 瑶光反证 reproduction) may be " +
				"delegated; write/execute profiles (e.g. patcher) are blocked until the plan is " +
				"approved. Re-run delegation with one of those profiles. Approving the plan exits " +
				"plan mode automatically; to abandon planning and write directly, call plan " +
				"action=exit_mode.",
		}
	}

	// ── 4. 白名单 ──
	if _, ok := PlanModeAllowedTools[toolName]; ok {
		return PlanModeResult{Allowed: true}
	}

	// ── 5. 默认拒绝 ──
	planFileHint := ""
	if ctx.ActivePlanFilePath != nil && *ctx.ActivePlanFilePath != "" {
		planFileHint = " Active plan file (writable): `" + *ctx.ActivePlanFilePath + "`."
	}
	return PlanModeResult{
		Allowed: false,
		Reason: "Plan Mode is active — write operations are blocked." + planFileHint +
			" Scratch probes (writable): `.rivet/scratch/`. " +
			"Allowed tools: read, grep, glob, repo_map, inspect_project, web_search, web_fetch, " +
			"ask_user_question, run_tests (瑶光反证 reproduction), " +
			"delegate_task/delegate_batch (code_scout/doc_scout/adversarial_verifier + authority), todo, plan. " +
			"Plan mode exits automatically once the user approves the plan; to abandon planning " +
			"and write directly, call plan action=exit_mode (no user approval needed).",
	}
}

// CreateActivePlanDraftPath 生成新的活动计划草稿路径（相对 cwd）。
//
// 对账 TS `createActivePlanDraftPath`（`plan-mode.ts:115-117`）：
//
//	return `.rivet/plans/draft-${Date.now()}.md`
//
// **含时间戳，故无法逐值对账**——测试用形态断言（前缀 + 后缀）。
func CreateActivePlanDraftPath() string {
	return ".rivet/plans/draft-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + ".md"
}

// FormatActivePlanDraftReceipt 生成写中活动计划草稿时的工具结果回执。
//
// 对账 TS `formatActivePlanDraftReceipt`（`plan-mode.ts:152-160`）：
//
//	if (!activePlanFilePath) return null
//	if (!pathsMatch(cwd, targetFilePath, activePlanFilePath)) return null
//	return `已写入活动计划文件 \`${activePlanFilePath}\`（${charCount} chars）——…`
//
// **用途**（TS 注释）：CLI 用户常把「沉默」误读为「没写进去」。仅工具结果文本，
// **绝不进冻结提示词**。
//
// **返回空串而非 nil**：Go 侧用 `""` 表达 TS 的 `null`（调用方判空即可）。
// 两者语义一致——都是「无回执」。
func FormatActivePlanDraftReceipt(cwd, targetFilePath string, activePlanFilePath *string, charCount int) string {
	if activePlanFilePath == nil || *activePlanFilePath == "" {
		return ""
	}
	if !planModePathsMatch(cwd, targetFilePath, *activePlanFilePath) {
		return ""
	}
	return "已写入活动计划文件 `" + *activePlanFilePath + "`（" +
		strconv.Itoa(charCount) + " chars）——内容与你提交的一致；历史消息里显示为 " +
		`"[file written to …]"` + " 指针是正常截断（省 token），草稿内容完好，不要重写"
}
