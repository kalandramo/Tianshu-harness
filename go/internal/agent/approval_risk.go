// approval_risk.go —— 审批风险判定（纯函数层）。
//
// 对账 TS `src/agent/approval-risk.ts` 的导出判定函数。
//
// ## 覆盖范围（有意收窄）
//
// 本文件实现**无外部子系统依赖**的判定：
//
//	NormalizeBashCommand / MatchesDangerousBash / MatchesForegroundOnlyHazard
//	MatchesInputSynthesis / BashCommandMayWrite / IsSafeWriteOnly
//	HasOutOfWorkspaceWriteTarget / BashGitBypassesScope
//	IsDestructiveGitAction / RequiresBashWriteApproval / RequiresUnconditionalApproval
//	AssessToolRisk（sensorium/MCP 分支留空——见下）
//
// **未移植的分支**（Go 侧无对应输入源）：
//
//   - `sensorium` 自适应置信度（`approval-risk.ts:703-714`）——Go 侧无 Sensorium
//   - MCP 策略（`evaluateMcpPolicy`）——Go 侧无 mcp 包
//   - antibodies（`ContextClaim`）——Go 侧有 claims 包但未接线到此
//
// `AssessToolRisk` 接受 nil 的可选输入，行为等价于 TS 省略这些参数
// （oracle 的 riskBaseline 正是这样生成的）。
package agent

import (
	"regexp"
	"strings"
)

// RiskLevel 是风险等级。
//
// 对账 TS `RiskLevel`（`approval-risk.ts:8`）。
type RiskLevel string

const (
	RiskNone   RiskLevel = "none"
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// riskRank 用于等级比较（对账 TS `RISK_RANK`）。
var riskRank = map[RiskLevel]int{RiskNone: 0, RiskLow: 1, RiskMedium: 2, RiskHigh: 3}

// riskMax 返回两者中较高的等级。
func riskMax(a, b RiskLevel) RiskLevel {
	if riskRank[b] > riskRank[a] {
		return b
	}
	return a
}

// RiskAssessment 是风险评估结果。
//
// 对账 TS `RiskAssessment`（`approval-risk.ts:10-14`）。
type RiskAssessment struct {
	Level           RiskLevel `json:"level"`
	Reasons         []string  `json:"reasons"`
	SuggestedAction string    `json:"suggestedAction"`
}

// confidenceThresholds 对账 TS `CONFIDENCE_THRESHOLDS`。
const (
	autoApproveConfidence = 0.8
	escalateConfidence    = 0.3
)

// normalizeBashCommandRe 系列是归一化的四步替换。
//
// 对账 TS `normalizeBashCommand`（`approval-risk.ts:265-271`）：
// 续行 / ${IFS} / 字符级转义 / 引号拼接。
var (
	continuationRe = regexp.MustCompile(`\\\r?\n`)
	ifsRe          = regexp.MustCompile(`(?i)\$\{IFS\}`)
	charEscapeRe   = regexp.MustCompile(`\\(.)`)
	quotesRe       = regexp.MustCompile(`["']`)
)

// NormalizeBashCommand 归一化命令文本——**只服务风险判定，不改变执行**。
//
// 对账 TS `normalizeBashCommand`。审批门在文本层、bash 语义在展开层：反斜杠
// 续行拆散「命令名+旗标」、`${IFS}` 替代空白、字符级转义（`r\m`）与引号拼接
// （`"r"m`）都能让语义不变的命令在文本上认不出。故归一化视图与原始视图**并检**
// （见 `testBoth`）：语义相同、判定必须相同。
func NormalizeBashCommand(command string) string {
	s := continuationRe.ReplaceAllString(command, " ")
	s = ifsRe.ReplaceAllString(s, " ")
	// TS 的 `\\(.)` → `$1`：剥掉转义反斜杠，保留被转义字符。
	s = charEscapeRe.ReplaceAllString(s, "$1")
	return quotesRe.ReplaceAllString(s, "")
}

// testBoth 在原始与归一化两个视图上并检（对账 TS `testBoth`）。
func testBoth(pattern *regexp.Regexp, command string) bool {
	return pattern.MatchString(command) || pattern.MatchString(NormalizeBashCommand(command))
}

// testBothAny 报告任一模式在任一视图上命中（对账 TS 的 `.some(pattern => testBoth(...))`）。
func testBothAny(patterns []*regexp.Regexp, command string) bool {
	for _, p := range patterns {
		if testBoth(p, command) {
			return true
		}
	}
	return false
}

// ── 含 lookaround 的 6 条：结构化改写 ──

var (
	rmWordRe        = regexp.MustCompile(`(?i)\brm\b`)
	rmRecurseFlagRe = regexp.MustCompile(`(?i)\s-{1,2}[a-z]*r`)
	rmForceFlagRe   = regexp.MustCompile(`(?i)\s-{1,2}[a-z]*f`)

	removeItemRe      = regexp.MustCompile(`(?i)\bremove-item\b`)
	removeItemFlagRe  = regexp.MustCompile(`(?i)\s-{1,2}(?:r(?:ecurse)?|fo(?:rce)?)\b`)
	cmdDeleteRe       = regexp.MustCompile(`(?i)\b(?:del|rd|rmdir)\b`)
	cmdDeleteSlashSRe = regexp.MustCompile(`(?i)\s\/s\b`)

	stashWordRe     = regexp.MustCompile(`\bgit\s+stash\b`)
	stashSafeSubRe  = regexp.MustCompile(`\bgit\s+stash\s+(?:pop|list|show|apply|drop|branch)\b`)
	pkgManagerRe    = regexp.MustCompile(`\b(?:npm|pnpm|yarn|bun)\b`)
	pkgGlobalFlagRe = regexp.MustCompile(`\s(?:-g|--global)\b`)
	pkgInstallRe    = regexp.MustCompile(`\b(?:install|i|add)\b`)
	pipRe           = regexp.MustCompile(`\bpip(?:3)?\s+install\b`)
	pipUserFlagRe   = regexp.MustCompile(`\s--user\b`)
	pipVenvPathRe   = regexp.MustCompile(`(?:\.venv|venv)/bin/pip`)
	pipActivateRe   = regexp.MustCompile(`\bactivate\b`)
	brewCargoRe     = regexp.MustCompile(`\b(?:brew|cargo)\s+install\b`)
)

// matchesRmRecursiveForce 对账 TS 的 `\brm\b(?=...r)(?=...f)`。
//
// TS 用双 lookahead 要求「同一窗口内既有递归旗标又有强制旗标」，窗口止于
// 命令分隔符（`rm -r build; ls -f` 不得误报——`ls -f` 在下一段）。
// Go 侧先截窗口再双检。
func matchesRmRecursiveForce(cmd string) bool {
	if !rmWordRe.MatchString(cmd) {
		return false
	}
	win := windowBeforeSeparator(cmd)
	return rmRecurseFlagRe.MatchString(win) && rmForceFlagRe.MatchString(win)
}

// matchesRemoveItemRecurseForce 对账 TS 的 `\bremove-item\b(?=...-r/-fo)`。
func matchesRemoveItemRecurseForce(cmd string) bool {
	if !removeItemRe.MatchString(cmd) {
		return false
	}
	return removeItemFlagRe.MatchString(windowBeforeSeparator(cmd))
}

// matchesCmdDeleteRecurse 对账 TS 的 `\b(?:del|rd|rmdir)\b(?=...\/s)`。
func matchesCmdDeleteRecurse(cmd string) bool {
	if !cmdDeleteRe.MatchString(cmd) {
		return false
	}
	return cmdDeleteSlashSRe.MatchString(windowBeforeSeparator(cmd))
}

// matchesGitStashDestructive 对账 TS 的 `\bgit\s+stash\b(?!safe-subcommand)`。
//
// 裸 `git stash` 是破坏性清空（丢弃工作区改动）；带安全子命令
// （pop/list/show/apply/drop/branch）的不算。
func matchesGitStashDestructive(cmd string) bool {
	if !stashWordRe.MatchString(cmd) {
		return false
	}
	return !stashSafeSubRe.MatchString(cmd)
}

// matchesGlobalInstall 对账 TS 的 GLOBAL_INSTALL_PATTERNS 三条。
//
//  1. 包管理器 + -g/--global + install（lookahead 改写）
//  2. pip 默认全局（排除 --user 与 venv 内 pip）
//  3. brew/cargo install（总是用户全局作用域）
func matchesGlobalInstall(cmd string) bool {
	// ① 包管理器全局安装
	if pkgManagerRe.MatchString(cmd) && pkgGlobalFlagRe.MatchString(cmd) && pkgInstallRe.MatchString(cmd) {
		return true
	}
	// ② pip 默认全局
	if pipRe.MatchString(cmd) && !pipUserFlagRe.MatchString(cmd) &&
		!pipVenvPathRe.MatchString(cmd) && !pipActivateRe.MatchString(cmd) {
		return true
	}
	// ③ brew / cargo install
	return brewCargoRe.MatchString(cmd)
}

// matchesDangerousBashSimple 判定 DANGEROUS 表中无 lookaround 的部分。
func matchesDangerousBashSimple(cmd string) bool {
	return testBothAny(bashDangerousSimplePatterns, cmd)
}

// MatchesDangerousBash 是 manual 档审批门的统一入口。
//
// 对账 TS `matchesDangerousBash`——原始与归一化视图并检 DANGEROUS 清单，
// 含 6 条 lookaround 改写项。
func MatchesDangerousBash(command string) bool {
	if matchesDangerousBashSimple(command) {
		return true
	}
	// lookaround 改写项：同样双视图并检（TS 的 testBoth 对全表生效）。
	for _, view := range []string{command, NormalizeBashCommand(command)} {
		if matchesRmRecursiveForce(view) ||
			matchesRemoveItemRecurseForce(view) ||
			matchesCmdDeleteRecurse(view) ||
			matchesGitStashDestructive(view) ||
			matchesGlobalInstall(view) ||
			testBothAny(availabilityHazardPatterns, view) {
			return true
		}
	}
	return false
}

// MatchesForegroundOnlyHazard 报告是否命中「纯前台抢占」子集。
//
// 对账 TS `matchesForegroundOnlyHazard`。
func MatchesForegroundOnlyHazard(command string) bool {
	return testBothAny(foregroundOnlyHazardPatterns, command)
}

// MatchesInputSynthesis 报告是否命中「会合成键鼠事件」原语。
//
// 对账 TS `matchesInputSynthesis`。
func MatchesInputSynthesis(command string) bool {
	return testBothAny(inputSynthesisPatterns, command)
}

// stripDevNullRedirects 剥离无副作用的 /dev/null 重定向。
//
// 对账 TS `stripDevNullRedirects`（替换为单个空格）。
func stripDevNullRedirects(command string) string {
	return devNullRedirectPattern.ReplaceAllString(command, " ")
}

// BashCommandMayWrite 报告命令是否有写副作用。
//
// 对账 TS `bashCommandMayWrite`：**先剥 /dev/null 再判**（只静音输出的只读
// 命令不该被判为写——误报会让 `grep` 在 reliability 降级模式下被锁死）。
func BashCommandMayWrite(command string) bool {
	normalized := stripDevNullRedirects(command)
	for _, p := range safeWritePatterns {
		if testBoth(p, normalized) {
			return true
		}
	}
	for _, p := range riskyWritePatterns {
		if testBoth(p, normalized) {
			return true
		}
	}
	return matchesGlobalInstall(normalized) // GLOBAL_INSTALL 属于 RISKY 子集
}

// IsSafeWriteOnly 报告命令是否**只**含安全写。
//
// 对账 TS `isSafeWriteOnly`：命中 RISKY 或 DANGEROUS 则 false；否则须命中 SAFE。
func IsSafeWriteOnly(command string) bool {
	normalized := stripDevNullRedirects(command)
	for _, p := range riskyWritePatterns {
		if testBoth(p, normalized) {
			return false
		}
	}
	if matchesGlobalInstall(normalized) {
		return false // GLOBAL_INSTALL 在 RISKY 表内
	}
	if MatchesDangerousBash(normalized) {
		return false
	}
	for _, p := range safeWritePatterns {
		if testBoth(p, normalized) {
			return true
		}
	}
	return false
}

// 越界写目标判定用的锚点模式（对账 TS `hasOutOfWorkspaceWriteTarget`）。
var (
	oowTokenSplitRe   = regexp.MustCompile(`[<>|;&]+`)
	oowWhitespaceRe   = regexp.MustCompile(`\s+`)
	oowQuoteTrimRe    = regexp.MustCompile(`^['"]+|['"]+$`)
	oowWinDriveRe     = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	oowDollarVarRe    = regexp.MustCompile(`^\$(?:\{[^}]+\}|[A-Za-z_][A-Za-z0-9_]*)(?:[\\/].*)?$`)
	oowPercentVarRe   = regexp.MustCompile(`%[^%\s]+%`)
	oowPathSegmentRe  = regexp.MustCompile(`[\\/]`)
	oowDotDotSegments = ".."
)

// HasOutOfWorkspaceWriteTarget 报告命令 token 里是否有指向 cwd 之外的写目标。
//
// 对账 TS `hasOutOfWorkspaceWriteTarget`。这是无沙箱 auto-safe「安全写」自动
// 放行的**第二道闸**：bash 的写目标（重定向、cp/mkdir 参数）不经
// `validatePathSafe`，`echo key >> ~/.ssh/authorized_keys` 若不在此拦下，
// 会在 auto-safe 下零提示执行。
func HasOutOfWorkspaceWriteTarget(command string) bool {
	scan := func(view string) bool {
		for _, token := range oowWhitespaceRe.Split(stripDevNullRedirects(view), -1) {
			for _, raw := range oowTokenSplitRe.Split(token, -1) {
				frag := oowQuoteTrimRe.ReplaceAllString(raw, "")
				if frag == "" {
					continue
				}
				if strings.HasPrefix(frag, "~") {
					return true
				}
				if strings.HasPrefix(frag, "/") || strings.HasPrefix(frag, `\`) {
					return true
				}
				if oowWinDriveRe.MatchString(frag) {
					return true
				}
				if oowDollarVarRe.MatchString(frag) {
					return true
				}
				if oowPercentVarRe.MatchString(frag) {
					return true
				}
				// issue #118：按**路径段**判 `..`，不能只判开头——
				// `foo/../../etc/cron.d/x` 以 `foo` 开头，中段穿越会被漏判。
				for _, seg := range oowPathSegmentRe.Split(frag, -1) {
					if seg == oowDotDotSegments {
						return true
					}
				}
			}
		}
		return false
	}
	// 归一化视图并检：${IFS} 粘成的单 token 让 `^\$` 锚定与空白分词同时失效。
	return scan(command) || scan(NormalizeBashCommand(command))
}

// BashGitBypassesScope 报告 git 命令是否绕过范围限制。
//
// 对账 TS `bashGitBypassesScope`（注意 TS 先 `trim()`）。
func BashGitBypassesScope(command string) bool {
	trimmed := strings.TrimSpace(command)
	for _, p := range gitBypassPatterns {
		if p.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// gitActionDestructiveRe 对账 TS 的 bash 分支正则。
var gitActionDestructiveRe = regexp.MustCompile(`\bgit\s+(?:stash\b|checkout\s|restore\b|reset\b|rm\s)`)

// IsDestructiveGitAction 报告 git 动作是否可抹除工作区改动（panic 目标）。
//
// 对账 TS `isDestructiveGitAction`。
func IsDestructiveGitAction(toolName string, input map[string]any) bool {
	if toolName == "git" {
		action, _ := input["action"].(string)
		return action == "stash" || action == "stash_pop"
	}
	if toolName == "bash" {
		cmd, _ := input["command"].(string)
		return gitActionDestructiveRe.MatchString(cmd)
	}
	return false
}

// RequiresBashWriteApproval 报告 bash 调用是否需要写审批。
//
// 对账 TS `requiresBashWriteApproval`。
func RequiresBashWriteApproval(toolName string, input map[string]any) bool {
	if toolName != "bash" {
		return false
	}
	cmd, _ := input["command"].(string)
	return BashCommandMayWrite(cmd)
}

// RequiresUnconditionalApproval 报告该动作是否**任何授权都不能豁免**。
//
// 对账 TS `requiresUnconditionalApproval`。YOLO（dangerously-skip-permissions）
// 由调用方（loop 的门控）豁免——本函数保持模式无关。
//
// Go 侧当前只有 `request_path_access` 与 `computer_use` 两类；后者工具本身
// 未移植，但判定保留（对账完整 + 将来接入即用）。
func RequiresUnconditionalApproval(toolName string, input map[string]any) bool {
	if toolName == "request_path_access" {
		return true
	}
	if toolName != "computer_use" {
		return false
	}
	action, _ := input["action"].(string)
	if action == "js_eval" || action == "browser_adopt" {
		return true
	}
	if action == "sequence" {
		steps, ok := input["steps"].([]any)
		// 畸形/空 sequence 由 tool 拒绝；审批侧按最严处理（无条件门打开）。
		if !ok || len(steps) == 0 {
			return true
		}
		for _, raw := range steps {
			step, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if RequiresUnconditionalApproval("computer_use", step) {
				return true
			}
		}
		return false
	}
	return false
}
