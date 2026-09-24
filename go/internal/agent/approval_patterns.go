// approval_patterns.go —— 审批风险的模式表（纯数据层）。
//
// 对账 TS `src/agent/approval-risk.ts` 的 9 张模式表（`approval-risk.ts:26-252`）。
//
// ## RE2 改写（本文件的核心工程问题）
//
// TS 用了 6 条含 lookahead/lookbehind 的正则，**Go RE2 不支持**（实测 5/5
// 编译失败）。这些条目改为**结构化判定**——匹配主体后做二次条件检查，而非
// 降级语义：
//
//	TS                                     Go
//	\brm\b(?=...r)(?=...f)           →     rmRe + 窗口内 recurseRe/forceRe
//	\bgit\s+stash\b(?!safe-subcmd)   →     stashRe && !safeStashRe
//	\npm...\b(?=...-g)[^\n]*install  →     npmRe && globalRe && installRe
//	\bpip(?<!venv)(?:3)?\s+install   →     pipRe + 前置上下文排除
//	\bremove-item\b(?=...-r/-fo)     →     removeItemRe + 窗口内旗标检查
//	\b(?:del|rd|rmdir)\b(?=.../s)    →     delRe + 窗口内 /s 检查
//
// 关键反例（`rm -r build; ls -f` 不得误报）由 oracle 用例
// `rm-cross-sep-no-false-positive` 钉住——窗口止于命令分隔符 `[\n;&|]`。
//
// **为什么必须对账而非手抄**：40+ 条正则手抄会引入自洽假绿（Go 与手抄 golden
// 双方同错）。oracle 从真实 TS 路径导出（`go/testdata/approvalrisk/`），
// 改写正确性由 `approval_risk_oracle_test.go` 强制。
package agent

import "regexp"

// ── 单条模式（无 lookaround，可直接使用）──

// forcePushPattern 用于在 assessToolRisk 里给出更清晰的 reason 文案
// （对账 TS 的 `FORCE_PUSH_PATTERN`，在 DANGEROUS_BASH_PATTERNS 中按引用比较）。
var forcePushPattern = regexp.MustCompile(`(?i)\bgit\s+push\b[^\n]*\s--force(?:-with-lease)?\b`)

// availabilityHazardPatterns —— 可用性危害（GUI 输入注入）。
//
// 对账 TS `AVAILABILITY_HAZARD_PATTERNS`。威胁模型是「夺走操作者对本机的
// 控制权」，不是「破坏数据」——决策理由必须不同（见 assessToolRisk）。
var availabilityHazardPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bDllImport\s*\([^)\n]*\buser32`),
	regexp.MustCompile(`(?i)\b(?:SetForegroundWindow|SetCursorPos|BringWindowToTop|mouse_event|keybd_event|SendInput|BlockInput)\s*\(`),
	regexp.MustCompile(`(?i)\bSendKeys\b[^\n]*(?:\(|::)`),
	regexp.MustCompile(`(?i)\bAppActivate\s*\(`),
	regexp.MustCompile(`(?i)\b(?:pyautogui|pynput|pywinauto|AutoIt|AutoHotkey)\b`),
	regexp.MustCompile(`(?i)\bctypes\b[^\n]*\b(?:windll|user32)\b`),
	regexp.MustCompile(`(?i)\bosascript\b[^\n]*\b(?:keystroke|key\s*code)\b`),
	regexp.MustCompile(`(?i)\bosascript\b[^\n]*\bSystem\s+Events\b[^\n]*\b(?:click|perform\s+action|set\s+value)\b`),
	regexp.MustCompile(`(?i)\bCGEventPost\s*\(`),
	regexp.MustCompile(`(?i)\bxdotool\b`),
}

// foregroundOnlyHazardPatterns —— 「前台抢占但不合成键鼠事件」子集。
//
// 对账 TS `FOREGROUND_ONLY_HAZARD_PATTERNS`。执行期让出监控只对它启用
// （合成键鼠会重置系统 idle 计时器，导致误杀正常命令）。
var foregroundOnlyHazardPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bSetForegroundWindow\s*\(`),
	regexp.MustCompile(`(?i)\bBringWindowToTop\s*\(`),
	regexp.MustCompile(`(?i)\bAppActivate\s*\(`),
}

// inputSynthesisPatterns —— 「会合成键鼠事件」原语。
//
// 对账 TS `INPUT_SYNTHESIS_PATTERNS`。刻意不含 SetForegroundWindow /
// BringWindowToTop（窗口层级操作不合成输入）。
var inputSynthesisPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:SetCursorPos|mouse_event|keybd_event|SendInput|BlockInput)\s*\(`),
	regexp.MustCompile(`(?i)\bSendKeys\b[^\n]*(?:\(|::)`),
	regexp.MustCompile(`(?i)\b(?:pyautogui|pynput|pywinauto|AutoIt|AutoHotkey)\b`),
	regexp.MustCompile(`(?i)\bctypes\b[^\n]*\b(?:windll|user32)\b`),
	regexp.MustCompile(`(?i)\bosascript\b[^\n]*\b(?:keystroke|key\s*code)\b`),
	regexp.MustCompile(`(?i)\bosascript\b[^\n]*\bSystem\s+Events\b[^\n]*\b(?:click|perform\s+action|set\s+value)\b`),
	regexp.MustCompile(`(?i)\bCGEventPost\s*\(`),
	regexp.MustCompile(`(?i)\bxdotool\b`),
}

// safeWritePatterns —— 低风险写命令（无沙箱 auto-safe 可自动放行）。
//
// 对账 TS `SAFE_WRITE_PATTERNS`。**注意**：bash 的写目标不经文件工具的路径
// 校验——放行必须叠加 `HasOutOfWorkspaceWriteTarget`（见 assessToolRisk）。
var safeWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:mkdir|touch|cp)\b`),
	regexp.MustCompile(`(^|[^<])>>?\s*[^&\s]`),
	regexp.MustCompile(`\|\s*tee\b`),
	regexp.MustCompile(`\bsed\b[^\n]*\s-i(?:\b|\s|['"])`),
	regexp.MustCompile(`\bperl\b[^\n]*\s-pi(?:\b|\s|['"])`),
	regexp.MustCompile(`\b(?:npm|pnpm|yarn|bun)\s+(?:install|i|add)\b`),
	regexp.MustCompile(`<<[-']?\w*['"]?`),
}

// riskyWritePatterns —— 风险写命令（即使无沙箱也需审批）。
//
// 对账 TS `RISKY_WRITE_PATTERNS`。含 GLOBAL_INSTALL 三态（见
// globalInstallCheck）。
var riskyWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:rm|mv|truncate|dd)\b`),
	regexp.MustCompile(`\b(?:chmod|chown|chgrp)\b`),
	regexp.MustCompile(`\bgit\s+(?:add|commit|checkout|switch|restore|reset|clean|merge|rebase|cherry-pick|push|pull)\b`),
	regexp.MustCompile(`\b(?:npm|pnpm|yarn|bun)\s+(?:remove|rm|update|upgrade|dedupe)\b`),
	regexp.MustCompile(`(?i)\brsync\b[^\n]*\s--delete\b`),
}

// injectionPatterns —— 命令注入（heredoc 滥用 / 进程替换 / shell 提权）。
//
// 对账 TS `INJECTION_PATTERNS`。
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[<>]\s*\(`),
	regexp.MustCompile(`\bzmodload\b`),
	regexp.MustCompile(`\bsysopen\b`),
	regexp.MustCompile(`(?i)\bpowershell\s+-enc`),
	regexp.MustCompile(`\beval\b.*\bexec\b`),
	regexp.MustCompile(`\bsource\b.*\/etc\/|^\.\s+\/etc\//`),
	regexp.MustCompile(`\benv\b.*\b(?:SHELL|PATH|HOME|LD_PRELOAD|DYLD_INSERT_LIBRARIES)=`),
	regexp.MustCompile(`\b(?:python[\d.]*|perl|ruby|node|osascript)\s+-[ec]\s`),
	regexp.MustCompile(`\bcrontab\b`),
	regexp.MustCompile(`\bsystemctl\b.*\b(?:enable|start|stop|restart|mask)\b`),
}

// destructiveExtendedPatterns —— 基础 DANGEROUS 之外的扩展破坏命令。
//
// 对账 TS `DESTRUCTIVE_EXTENDED_PATTERNS`。
var destructiveExtendedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bdocker\s+(?:rm|rmi)\b`),
	regexp.MustCompile(`\bdocker\s+system\s+prune\b`),
	regexp.MustCompile(`\bkubectl\s+delete\b`),
	regexp.MustCompile(`\btruncate\s+-s\s+0\b`),
	regexp.MustCompile(`\bdd\s+if=.*of=\/dev\/`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`(?i)\bformat-volume\b`),
}

// sedBypassPatterns —— sed 修改安全关键文件。
//
// 对账 TS `SED_BYPASS_PATTERNS`。
var sedBypassPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsed\b.*\b(?:\/etc\/|\.ssh\/|authorized_keys|shadow|passwd)\b`),
}

// gitBypassPatterns —— 绕过范围限制的 git 命令（未限定范围的 add/commit/stash）。
//
// 对账 TS `GIT_BYPASS_PATTERNS`。
var gitBypassPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgit\s+add\s+(?:-A\b|--all\b|\.(?:\s|$))`),
	regexp.MustCompile(`\bgit\s+commit\s+[^\n]*-[a-z]*a`),
	regexp.MustCompile(`\bgit\s+stash\s*$`),
	regexp.MustCompile(`\bgit\s+stash\s+(?:push\s*)?$`),
}

// devNullRedirectPattern —— 无副作用的 /dev/null 重定向。
//
// 对账 TS `DEV_NULL_REDIRECT_PATTERN`。在写检测前剥离——只静音输出的只读
// 命令不该被判为写（误报会让 `grep` 被 reliability 降级模式锁死）。
var devNullRedirectPattern = regexp.MustCompile(`(?:^|\s)(?:\d+|&)?>>?\s*/dev/null\b`)

// ── 含 lookaround 的 6 条：结构化改写 ──

// bashDangerousSimplePatterns 是 DANGEROUS_BASH_PATTERNS 中**无 lookaround**
// 的部分（可直接逐条匹配）。
//
// 含 lookaround 的 6 条由专用判定函数处理（见下方）。拆开是为了让
// `MatchesDangerousBash` 的组合逻辑显式、可审计。
var bashDangerousSimplePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bshred\b`),
	regexp.MustCompile(`\btruncate\s+table\b`),
	regexp.MustCompile(`\bgit\s+reset\s+--hard\b`),
	regexp.MustCompile(`\bgit\s+clean\s+-[a-zA-Z]*f\b`),
	regexp.MustCompile(`\bgit\s+checkout\s+--(?:\s|$)`),
	regexp.MustCompile(`\bgit\s+restore\b`),
	regexp.MustCompile(`\bkillall\b`),
	regexp.MustCompile(`\bpkill\s+-[9Kf]\b`),
	regexp.MustCompile(`(?i)\bdrop\s+table\b`),
	regexp.MustCompile(`\bsudo\s+(?:rm|chmod|chown|dd|mkfs|mount|umount|systemctl|shutdown|reboot|passwd|user(?:add|del|mod))\b`),
	regexp.MustCompile(`\bchmod\s+(?:777|[0-7]*7[0-7]*7)\b`),
	regexp.MustCompile(`\bwget\b.*\|\s*(?:\S*\/)?(?:sh|bash|zsh|fish)\b`),
	regexp.MustCompile(`\bcurl\b.*\|\s*(?:\S*\/)?(?:sh|bash|zsh|fish)\b`),
	regexp.MustCompile(`\b(?:sh|bash|zsh|dash)\s+-c\s+["']?\$\(`),
	regexp.MustCompile(`\beval\b.*\$[({]`),
	forcePushPattern,
	regexp.MustCompile(`\b(?:shutdown|reboot|halt|poweroff)\b`),
	regexp.MustCompile(`\bnpm\s+(?:publish|unpublish)\b`),
	regexp.MustCompile(`\bxargs\b.*\brm\b`),
	regexp.MustCompile(`\bbase64\b[^\n]*\|\s*(?:\S*\/)?(?:sh|bash|zsh|fish)\b`),
	regexp.MustCompile(`\bfind\b[^\n|;&]*\s-delete\b`),
}

// commandSeparatorRe 是命令分隔符——lookaround 改写时的「检查窗口」边界。
//
// TS 的 `(?=[^\n;&|]*...)` 用字符类排除分隔符；Go 侧改为**先截取窗口再判**，
// 语义等价且更直观。
var commandSeparatorRe = regexp.MustCompile(`[\n;&|]`)

// windowBeforeSeparator 返回首个命令分隔符之前的部分。
//
// 对账 TS lookahead 里的 `[^\n;&|]*`——那是「不跨越命令边界」的检查窗口。
func windowBeforeSeparator(s string) string {
	if loc := commandSeparatorRe.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}
