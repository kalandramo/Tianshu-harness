package agent

import (
	"regexp"
	"strings"
)

// ── bash 前缀 allowlist 判定 ──
//
// 对账 TS `src/agent/permissions.ts:180-252` 的 `segmentMatchesAllowEntry` /
// `isBashCommandAllowlisted`。
//
// **与 deny 侧的差别不是"反过来"**：deny 宽松剥离环境赋值不影响边界
// （拒绝方向），而 allow 侧"放宽"意味着**授予**——故 `segmentMatchesAllowEntry`
// 有 5 道 fail-closed 守卫（见下），deny 侧一道都没有。
//
// **为什么必须有它**：TS `shouldAsk` 的 `allowlisted ? false` 分支消费
// `isBashCommandAllowlisted`（经 `bashAllowlisted`）。Go 侧此前只有
// `IsBashCommandDenied`——用户配了 `permissions.bash.allowlist` 也不会生效，
// 每次仍要审批（可用性问题，非安全问题，但同属"配置被静默忽略"）。

// unmodelledShellCharsRe 匹配 `splitShellSegments` **未建模**的 shell 构造。
//
// 对账 TS `const UNMODELLED_SHELL_CHARS = /[<>\\!]/`。
//
// 含这些字符的命令无法逐段推理：
//   - `>` `<` 重定向：`echo x > ~/.zshrc` 会在 `echo` 被放行时**静默写文件**；
//   - `\` 转义改变 shell 眼中的分隔符；
//   - `!` 触发历史展开。
//
// 命中即整体拒绝（回落到显式审批）。
var unmodelledShellCharsRe = regexp.MustCompile(`[<>\\!]`)

// interpreters 是以**代码为参数**的二进制——只匹配二进制名等于授予它执行
// 任意代码的能力（`bash -c "rm -rf /"` 在 `bash` 被放行时仍须审批）。
//
// 对账 TS `INTERPRETERS`。
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "ksh": true, "dash": true, "fish": true,
	"node": true, "deno": true, "bun": true, "python": true, "python3": true,
	"perl": true, "ruby": true, "php": true,
	"osascript": true, "powershell": true, "pwsh": true, "cmd": true,
}

// inlineCodeFlagRe 匹配解释器的内联代码 flag（`-c` / `-e` / `--eval` …）。
//
// 对账 TS `const INLINE_CODE_FLAG = /^-{1,2}(c|e|eval|command|exec)$/i`。
//
// **只对 `interpreters` 查**——大量无害命令用 `-c` 做别的事（`grep -c`、`wc -c`）。
var inlineCodeFlagRe = regexp.MustCompile(`(?i)^-{1,2}(c|e|eval|command|exec)$`)

// inlineCodeClusterRe 匹配携带内联代码开关的短 flag 簇：`bash -lc`、`sh -ec`。
//
// 对账 TS `const INLINE_CODE_CLUSTER = /^-[a-z]*[ce][a-z]*$/i`。
//
// **只认单横杠**：`--check` / `--experimental-*` 这类长 flag 无害，不得被捕获。
// 注意 TS 用的是 `[a-z]`（非 `\w`）——大写字母不匹配（除非 `i` 标志下；
// TS 有 `i` 标志，故 Go 用 `(?i)` 对账）。
var inlineCodeClusterRe = regexp.MustCompile(`(?i)^-[a-z]*[ce][a-z]*$`)

// alwaysPromptBinaries 是"给什么执行什么"的二进制（无需 flag）。
//
// 对账 TS `ALWAYS_PROMPT_BINARIES`。
//
// wrapper 类（`timeout` / `nice` / `nohup` / `env` …）会把参数向量当子进程跑——
// 放行 wrapper 等于**洗白**任意命令。
var alwaysPromptBinaries = map[string]bool{
	"eval": true, "exec": true, "source": true, ".": true, "xargs": true,
	"timeout": true, "nice": true, "nohup": true, "parallel": true, "env": true, "stdbuf": true,
}

// leadingAssignRe 匹配开头的 `\w+=`（用于 allow 侧的严格剥离循环条件）。
//
// 对账 TS `while (/^\w+=/.test(trimmed))`。
var leadingAssignRe = regexp.MustCompile(`^\w+=`)

// assignWithValueRe 捕获 `\w+=(\S*)\s+` 的值部分。
//
// 对账 TS `/^\w+=(\S*)\s+/`。**必须要求尾随空白**——只有赋值无命令时
// （`A=1`）匹配失败 → 返回 false（没有可授予的东西）。
var assignWithValueRe = regexp.MustCompile(`^\w+=(\S*)\s+`)

// inertValueRe 判定赋值值是否"明显惰性"。
//
// 对账 TS `if (/[/:$=]/.test(value)) return false`。
//
// **为什么允许剥惰性值**：环境赋值本身是代码执行向量
// （`PATH=/tmp/evil:`、`LD_PRELOAD=…`、`NODE_OPTIONS=--require=…`），
// 而 allow 侧的"放宽"就是授予。只有不含路径分隔符/冒号/美元/第二个等号的
// 值才可剥——其余回落到显式审批。
var inertValueRe = regexp.MustCompile(`[/:$=]`)

// residualSubstDelimRe 匹配残余的替换符。
//
// 对账 TS `if (/[` + "`" + `()]/.test(trimmed)) return false`。
//
// 含义：`splitShellSegments` 没能完全解析该段（如嵌套 `$( … $( … ) … )`）。
// **deny 侧可以容忍解析失误**（只削弱拒绝），但**在没检视过的文本上授予**
// 是反面——故 fail closed。
var residualSubstDelimRe = regexp.MustCompile("[" + "`" + `()]`)

// whitespaceSplitRe 按空白切分 token（对账 TS `trimmed.split(/\s+/)`）。
var whitespaceSplitRe = regexp.MustCompile(`\s+`)

// segmentMatchesAllowEntry 报告单个段的命令头是否命中 allowlist。
//
// 对账 TS `segmentMatchesAllowEntry`（`permissions.ts:206-235`）。
//
// **与 `segmentMatchesDenyPrefix` 的 token 边界相同**，但多了 5 道
// fail-closed 守卫（allow 决策需要、deny 决策不需要）：
//  1. 环境赋值**严格**剥离（仅惰性值）；
//  2. 残余替换符 → 拒绝；
//  3. 二进制含 `$` → 拒绝（静态不可知）；
//  4. `alwaysPromptBinaries` → 拒绝（wrapper 洗白）；
//  5. `interpreters` + 内联代码 flag → 拒绝。
func segmentMatchesAllowEntry(segment string, allowlist []string) bool {
	trimmed := strings.TrimLeft(segment, " \t\n\v\f\r")

	// ① 严格剥离环境赋值：仅惰性值可剥，其余拒绝。
	for leadingAssignRe.MatchString(trimmed) {
		m := assignWithValueRe.FindStringSubmatch(trimmed)
		if m == nil {
			// 只有赋值、没有命令——没有可授予的东西。
			return false
		}
		if inertValueRe.MatchString(m[1]) {
			return false
		}
		trimmed = trimmed[len(m[0]):]
	}
	if trimmed == "" {
		return false
	}

	// ② 残余替换符 → 分段没解净，不放行。
	if residualSubstDelimRe.MatchString(trimmed) {
		return false
	}

	tokens := whitespaceSplitRe.Split(trimmed, -1)
	binary := ""
	if len(tokens) > 0 {
		binary = tokens[0]
	}

	// ③ 二进制本身是展开（`$CMD args`）→ 静态不可知。
	if strings.Contains(binary, "$") {
		return false
	}

	// 取路径基名（对账 TS `binary.slice(binary.lastIndexOf('/') + 1)`）。
	base := binary
	if idx := strings.LastIndex(binary, "/"); idx >= 0 {
		base = binary[idx+1:]
	}

	// ④ wrapper 类 → 拒绝。
	if alwaysPromptBinaries[base] {
		return false
	}

	// ⑤ 解释器 + 内联代码 flag → 拒绝。
	if interpreters[base] && hasInlineCodeFlag(tokens[1:]) {
		return false
	}

	// token 边界匹配（与 deny 侧相同）。
	for _, entry := range allowlist {
		if entry == "" || !strings.HasPrefix(trimmed, entry) {
			continue
		}
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

// hasInlineCodeFlag 报告 token 列表里是否有内联代码 flag。
//
// 对账 TS `tokens.slice(1).some(t => INLINE_CODE_FLAG.test(t) || INLINE_CODE_CLUSTER.test(t))`。
func hasInlineCodeFlag(tokens []string) bool {
	for _, t := range tokens {
		if inlineCodeFlagRe.MatchString(t) || inlineCodeClusterRe.MatchString(t) {
			return true
		}
	}
	return false
}

// bashAllowlistedFor 报告该次工具调用是否被 `permissions.bash.allowlist` 覆盖。
//
// 对账 TS `tool-pipeline.ts:1146-1148`：
//
//	const bashAllowlisted = tu.name === 'bash' && typeof tu.input.command === 'string'
//	  ? isBashCommandAllowlisted(tu.input.command, bashAllowPrefixes)
//	  : false
//
// **只对 bash 生效**（与 `bashDeniedFor` 对称）：allowlist 是命令前缀语义，
// 其他工具没有 `command` 参数。
func bashAllowlistedFor(perms *PermissionConfig, toolName string, input map[string]any) bool {
	if perms == nil || perms.Bash == nil || toolName != "bash" {
		return false
	}
	cmd, ok := input["command"].(string)
	if !ok {
		return false
	}
	return IsBashCommandAllowlisted(cmd, perms.Bash.Allowlist)
}

// IsBashCommandAllowlisted 报告命令是否被 allowlist 覆盖。
//
// 对账 TS `isBashCommandAllowlisted`（`permissions.ts:243-252`）。
//
// **语义**：shell 会执行的**每一段**都必须被 allowlist 覆盖——同一套分段，
// 故只放行 `npx` 时 `npx && rm -rf /` 仍被拒（`rm` 段未覆盖）。
//
// **整体前置拒绝**：含未建模 shell 字符（`<>\!`）的命令直接 false，
// 不论分段结果如何。
func IsBashCommandAllowlisted(command string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if unmodelledShellCharsRe.MatchString(trimmed) {
		return false
	}
	segments := splitShellSegments(trimmed)
	if len(segments) == 0 {
		return false
	}
	for _, seg := range segments {
		if !segmentMatchesAllowEntry(seg, allowlist) {
			return false
		}
	}
	return true
}
