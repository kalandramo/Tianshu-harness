package agent

import (
	"regexp"
	"strings"
)

// ── shell 分段与 bash 前缀 deny 判定 ──
//
// 对账 TS `src/agent/permissions.ts:66-113` 的 `splitShellSegments` /
// `segmentMatchesDenyPrefix` / `isBashCommandDenied`。
//
// **为什么需要它**：TS 的 `shouldAsk` 之前有一条独立守卫
//
//	denied || bashDenied || selfKill
//
// 其中 `bashDenied` 消费 `permissions.bash.denylist`（**独立于 `deny` 规则**）。
// Go 侧此前只接了 `denied`（`IsToolDenied`），而 `PermissionConfig` 连
// `bash` 字段都没有 → 用户在 `permissions.bash.denylist` 里写的规则被
// **静默忽略**（与第五十二刀 deny 规则不生效同族）。
//
// **为什么不能复用 `deny` 规则替代**：两者匹配的东西不同——`deny` 规则匹配
// **工具名 + 参数模式**（`{tool:"bash", params:{command:"rm -rf*"}}`），而
// `denylist` 是**命令前缀**语义（`taskkill` 匹配 `taskkill /f /im x.exe`，
// 且必须穿透 `;` / `&&` / `$( … )` 找到藏在后面的段）。后者需要 shell 分段。

// subshellRe 匹配命令替换体：`$( … )` 或反引号。
//
// 对账 TS `const subshellRe = /\$\(([^()]*)\)|`([^`]*)`/g`。
//
// **`[^()]*` 不处理嵌套**：`$(a $(b) c)` 会匹配到 `$(b)`（最内层非空括号对），
// 留下 `$(a   c)` 这种残缺段——**这是 TS 的真实行为**，Go 侧逐值复刻
// （oracle 用例 `cmdsubst-nested-parens-unparsed` 钉住）。不"顺手修好"：
// 分段结果会喂给 deny 判定，擅自改变切分会让两边判定分歧。
var subshellRe = regexp.MustCompile(`\$\(([^()]*)\)|` + "`" + `([^` + "`" + `]*)` + "`")

// segmentSeparatorRe 是段分隔符：`;` 换行 `|` `&`。
//
// 对账 TS `/[;\n|&]+/`。**重定向不是分隔符**——`>` `<` `2>` 属于同一条命令，
// 故 `echo hi > out.txt` 切出来仍是单段（oracle `redirect-not-separator` 钉住）。
var segmentSeparatorRe = regexp.MustCompile(`[;\n|&]+`)

// splitShellSegments 把 bash 命令切成 shell 实际会执行的各段。
//
// 对账 TS `splitShellSegments`（`permissions.ts:75-88`）。
//
// **两阶段顺序是行为的一部分**（不可调换）：
//  1. 先抽出命令替换体（`$( … )` / 反引号），把它们按分隔符切开追加；
//  2. 再从**剥离了替换体**的顶层字符串切分。
//
// 若不剥离就切分，替换体内部的 `|` / `;` 会污染顶层切分。反过来若不先抽，
// `$(taskkill …)` 里的命令就看不见——**denylist 会被 `foo; $(taskkill)` 绕过**。
func splitShellSegments(command string) []string {
	segments := []string{}

	// ① 抽命令替换体。
	for _, m := range subshellRe.FindAllStringSubmatch(command, -1) {
		inner := m[1]
		if inner == "" {
			inner = m[2]
		}
		if strings.TrimSpace(inner) != "" {
			segments = append(segments, segmentSeparatorRe.Split(inner, -1)...)
		}
	}

	// ② 剥离替换体后切分顶层。
	// TS 用 `replace(re, ' ')`（替换为单个空格）——Go 用 ReplaceAllString 等价。
	stripped := subshellRe.ReplaceAllString(command, " ")
	segments = append(segments, segmentSeparatorRe.Split(stripped, -1)...)

	// ③ trim + 丢弃空段（对账 TS `.map(trim).filter(Boolean)`）。
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// envAssignmentRe 匹配开头的 `VAR=value` 环境赋值。
//
// 对账 TS `segmentMatchesDenyPrefix` 里的 `/^\w+=\S*\s+/`。
// **deny 侧的剥离比 allow 侧宽松**（allow 侧要防 `PATH=/tmp/evil:` 这类注入，
// 见 `segmentMatchesAllowEntry` 的注释）——deny 剥离只影响"是否拒绝"，
// 宽松剥离不会放宽边界。
var envAssignmentRe = regexp.MustCompile(`^\w+=\S*\s+`)

// segmentMatchesDenyPrefix 报告单个段的命令头是否命中 denylist 前缀。
//
// 对账 TS `segmentMatchesDenyPrefix`（`permissions.ts:90-102`）。
//
// **token 边界是核心**：`rm` 匹配 `rm -rf`，但**不匹配** `rmdir`；
// 而参数里含该词（`echo taskkill`）也**不匹配**——因为只比命令头。
func segmentMatchesDenyPrefix(segment string, denylist []string) bool {
	trimmed := strings.TrimLeft(segment, " \t\n\v\f\r")
	// 剥离开头的环境赋值（可多个）。
	for envAssignmentRe.MatchString(trimmed) {
		trimmed = envAssignmentRe.ReplaceAllString(trimmed, "")
	}
	if trimmed == "" {
		return false
	}
	for _, entry := range denylist {
		if entry == "" || !strings.HasPrefix(trimmed, entry) {
			continue
		}
		// token 边界：前缀后必须是结尾、空格或 tab。
		if len(trimmed) == len(entry) {
			return true
		}
		next := trimmed[len(entry)]
		if next == ' ' || next == '\t' {
			return true
		}
	}
	return false
}

// bashDeniedFor 报告该次工具调用是否命中 `permissions.bash.denylist`。
//
// 对账 TS `tool-pipeline.ts:1136-1138`：
//
//	const bashDenied = tu.name === 'bash' && typeof tu.input.command === 'string'
//	  ? isBashCommandDenied(tu.input.command, bashDenyPrefixes)
//	  : false
//
// **只对 bash 生效**：`denylist` 是命令前缀语义，其他工具没有 `command` 参数。
// 非 bash 工具直接返回 false（不误伤）。
//
// **与 `IsToolDenied` 并列**（TS 的 `denied || bashDenied || selfKill`）：
// 两者都放在决策链**最前**、任何档位都不能绕过。
func bashDeniedFor(perms *PermissionConfig, tc toolCall) bool {
	if perms == nil || perms.Bash == nil || tc.name != "bash" {
		return false
	}
	cmd, ok := tc.input["command"].(string)
	if !ok {
		return false
	}
	return IsBashCommandDenied(cmd, perms.Bash.Denylist)
}

// IsBashCommandDenied 报告命令是否被 denylist 前缀拦下。
//
// 对账 TS `isBashCommandDenied`（`permissions.ts:112-115`）。
//
// **fail-closed 语义**：shell 会执行的**任一段**命中前缀 → 整条命令被拒。
// TS 注释特别说明这与 allowlist 逻辑**故意不同**：allowlist 拒绝含 shell
// 操作符的命令（防 `npx && rm -rf /` 蒙混），但 denylist 若照搬那套，
// `taskkill …; rm -rf /` 反而会**穿过** denylist——与 denylist 的职责相反。
func IsBashCommandDenied(command string, denylist []string) bool {
	if len(denylist) == 0 {
		return false
	}
	for _, seg := range splitShellSegments(command) {
		if segmentMatchesDenyPrefix(seg, denylist) {
			return true
		}
	}
	return false
}
