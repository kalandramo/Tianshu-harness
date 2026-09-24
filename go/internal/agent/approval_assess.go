// approval_assess.go —— AssessToolRisk 主体（对账 TS `assessToolRisk`）。
//
// 与 approval_risk.go 同包：模式表在 approval_patterns.go，判定函数在
// approval_risk.go，风险聚合在本文件。
package agent

import (
	"regexp"
	"strings"
)

// ── computer_use 逐动作风险（对账 TS `COMPUTER_USE_ACTION_RISK`）──

// computerUseActionRisk 对账 TS `COMPUTER_USE_ACTION_RISK`。
//
// 等级上限刻意停在 medium：high 会在 auto-safe 档无条件触发审批，把
// 「始终允许」的免审语义打穿（js_eval/browser_adopt 的无条件接管面已由
// `RequiresUnconditionalApproval` 单独置 high）。
var computerUseActionRisk = map[string]struct {
	Level  RiskLevel
	Reason string
}{
	"check_permissions": {RiskNone, "pure local capability probe"},
	"diagnose":          {RiskNone, "local diagnostics / counters"},
	"wait":              {RiskNone, "plain sleep"},
	"list_apps":         {RiskLow, "enumerates running applications"},
	"snapshot":          {RiskLow, "reads the app accessibility tree and screen"},
	"find":              {RiskLow, "filters the app accessibility tree"},
	"wait_for":          {RiskLow, "polls the app accessibility tree"},
	"focus_app":         {RiskMedium, "steals foreground focus"},
	"launch_app":        {RiskMedium, "launches an app and steals foreground focus"},
	"scroll":            {RiskMedium, "synthesizes scroll input"},
	"navigate":          {RiskMedium, "drives the browser to a new URL"},
	"read_page":         {RiskMedium, "reads arbitrary page content"},
	"tabs":              {RiskMedium, "lists or mutates browser tabs"},
	"click":             {RiskMedium, "synthesizes a click in the target app"},
	"double_click":      {RiskMedium, "synthesizes a double click in the target app"},
	"right_click":       {RiskMedium, "synthesizes a right click in the target app"},
	"drag":              {RiskMedium, "synthesizes drag input in the target app"},
	"type":              {RiskMedium, "types arbitrary text into the target app"},
	"set_value":         {RiskMedium, "writes an arbitrary value into a control"},
	"key":               {RiskMedium, "sends key combos that can trigger shortcuts"},
	"menu_select":       {RiskMedium, "invokes a menu command"},
	"paste_text":        {RiskMedium, "pastes arbitrary text into the target app"},
	"js_eval":           {RiskHigh, "runs arbitrary JS in the user's browser"},
	"browser_adopt":     {RiskHigh, "takes over an external DevTools endpoint"},
}

// pathTraversalDotDotRe 对账 TS 的 `/(^|[\\/])\.\.([\\/]|$)/`。
var pathTraversalDotDotRe = regexp.MustCompile(`(^|[\\/])\.\.([\\/]|$)`)

// absolutePathRe 覆盖 POSIX 绝对路径与 Windows 盘符。
//
// TS 用 Node 的 `isAbsolute`（平台相关）；Go 侧**必须复刻 Node 语义**——
// 项目已记录该坑：`filepath.IsAbs("/etc/passwd")` 在 Windows 返回 false，
// 而 Node 的 `path.win32.isAbsolute` 返回 true。
var absolutePathRe = regexp.MustCompile(`^(?:/|[A-Za-z]:[\\/]|\\\\)`)

// isAbsoluteNodeLike 复刻 Node `isAbsolute` 的双平台语义。
func isAbsoluteNodeLike(p string) bool {
	return absolutePathRe.MatchString(p)
}

// mcpToolNameRe 对账 TS 的 `/^mcp__(.+)__(.+)$/`。
var mcpToolNameRe = regexp.MustCompile(`^mcp__(.+)__(.+)$`)

// urlSchemeRe 从 URL 提取协议（Go 的 net/url 过重，此处只需协议与主机）。
var urlSchemeRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):`)

// ipLiteralRe 识别 IP 字面量（对账 TS 的 `isIP(...) > 0`）。
var ipLiteralRe = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)

// AssessToolRisk 评估工具调用的风险等级。
//
// 对账 TS `assessToolRisk`（`approval-risk.ts:465-736`）。
//
// **可选输入**：TS 的 `doomLoopLevel` / `antibodies` / `sensorium` /
// `declaredCapability` 中，Go 侧只有 `doomLoopLevel` 可完整对应；其余传 nil
// 即等价于 TS 省略（oracle 的 riskBaseline 正是这样生成的）。
//
// **未实现的分支**（Go 侧无输入源，传入 nil 时自然跳过）：
// antibodies 匹配（`:693`）、sensorium 置信度升级（`:703`）、MCP 策略（`:675`）。
// 若将来补齐输入源，在此加对应分支即可——oracle 用例会强制对账。
func AssessToolRisk(toolName string, input map[string]any, doomLoopLevel string) RiskAssessment {
	reasons := []string{}
	level := RiskNone

	strInput := func(key string) string {
		v, _ := input[key].(string)
		return v
	}

	// Arbitrary-JS / endpoint-takeover 面——与管线的无条件门双保险。
	if RequiresUnconditionalApproval(toolName, input) {
		reasons = append(reasons, "arbitrary JS in the user browser / DevTools endpoint takeover")
		level = RiskHigh
	}

	// computer_use 逐动作风险：读屏/交互/接管要有区分度；未知动作 fail-closed。
	if toolName == "computer_use" {
		applyRisk := func(label, riskAction string) {
			entry, ok := computerUseActionRisk[riskAction]
			if !ok {
				reasons = append(reasons, label+`: unknown computer_use action "`+riskAction+`" — fail closed`)
				level = RiskHigh
				return
			}
			reasons = append(reasons, label+": "+entry.Reason)
			level = riskMax(level, entry.Level)
		}
		action := strInput("action")
		if action == "sequence" {
			steps, ok := input["steps"].([]any)
			if !ok || len(steps) == 0 {
				reasons = append(reasons, "computer_use.sequence: empty/malformed steps — fail closed")
				level = RiskHigh
			} else {
				for i, raw := range steps {
					step, _ := raw.(map[string]any)
					stepAction := ""
					if step != nil {
						stepAction, _ = step["action"].(string)
					}
					if stepAction == "" {
						stepAction = "?"
					}
					applyRisk("computer_use.sequence["+itoa(i+1)+"]."+stepAction, stepAction)
				}
			}
		} else {
			applyRisk("computer_use."+action, action)
		}
	}

	// Doom loop：blocked 由管线提前返回，故破坏性 git 保护也须在 warn 窗口触发。
	if doomLoopLevel == "warn" || doomLoopLevel == "blocked" {
		if IsDestructiveGitAction(toolName, input) {
			reasons = append(reasons, "保护模式：工具失败率高，破坏性动作需确认")
			level = RiskHigh
		} else {
			if doomLoopLevel == "blocked" {
				reasons = append(reasons, "Agent is in doom loop (repeated identical tool calls)")
			} else {
				reasons = append(reasons, "Agent may be entering doom loop")
			}
			if level == RiskNone {
				level = RiskMedium
			}
		}
	}

	// 路径穿越
	var targets []string
	for _, k := range []string{"file_path", "path", "target"} {
		if v, ok := input[k].(string); ok {
			targets = append(targets, v)
		}
	}
	for _, tgt := range targets {
		if isAbsoluteNodeLike(tgt) || pathTraversalDotDotRe.MatchString(tgt) {
			reasons = append(reasons, "absolute path target")
			if level != RiskHigh {
				level = RiskMedium
			}
			break
		}
	}

	// 破坏性命令
	if toolName == "bash" {
		cmd := strInput("command")
		// 可用性危害单列（issue #235）：威胁模型是「夺走控制权」而非「破坏数据」。
		if testBothAny(availabilityHazardPatterns, cmd) {
			reasons = append(reasons, "GUI 输入注入：抢占前台 / 合成键鼠事件——执行期间操作者失去本机控制权")
			level = RiskHigh
		}
		for _, p := range bashDangerousSimplePatterns {
			if testBoth(p, cmd) {
				if p == forcePushPattern {
					reasons = append(reasons, "force push can overwrite shared remote history")
				} else {
					reasons = append(reasons, "destructive shell command")
				}
				level = RiskHigh
				break
			}
		}
		// lookaround 改写项（仅当尚未置 high 时才需判定原因文案）。
		if level != RiskHigh {
			for _, view := range []string{cmd, NormalizeBashCommand(cmd)} {
				if matchesRmRecursiveForce(view) || matchesRemoveItemRecurseForce(view) ||
					matchesCmdDeleteRecurse(view) || matchesGitStashDestructive(view) ||
					matchesGlobalInstall(view) {
					reasons = append(reasons, "destructive shell command")
					level = RiskHigh
					break
				}
			}
		}
		if strings.Contains(cmd, "curl") && strings.Contains(cmd, "|") {
			reasons = append(reasons, "Pipe from network")
			if level != RiskHigh {
				level = RiskMedium
			}
		}
		if BashCommandMayWrite(cmd) {
			reasons = append(reasons, "bash command may write to filesystem, package state, or git state")
			if level == RiskNone {
				level = RiskMedium
			}
		}
		if BashGitBypassesScope(cmd) {
			reasons = append(reasons, "unscoped git command bypasses scope — use deliver_task or git tool with ownedFiles instead")
			level = RiskHigh
		}
		// 命令注入
		for _, p := range injectionPatterns {
			if p.MatchString(cmd) {
				reasons = append(reasons, "command injection pattern: "+p.String())
				level = RiskHigh
				break
			}
		}
		// 扩展破坏命令
		for _, p := range destructiveExtendedPatterns {
			if p.MatchString(cmd) {
				reasons = append(reasons, "extended destructive command: "+p.String())
				if level != RiskHigh {
					level = RiskMedium
				}
				break
			}
		}
		// sed 绕过安全关键文件
		for _, p := range sedBypassPatterns {
			if p.MatchString(cmd) {
				reasons = append(reasons, "sed bypass on security-critical file")
				level = RiskHigh
				break
			}
		}
	}

	// sandbox_exec：名义沙箱实为任意代码执行（Node 子进程有完整 fs/net 访问）。
	if toolName == "sandbox_exec" {
		reasons = append(reasons, "arbitrary JavaScript execution — full Node.js process with fs/net/child_process access")
		level = RiskHigh
	}

	// 写操作
	if toolName == "write_file" || toolName == "edit_file" {
		if level == RiskNone {
			level = RiskLow
		}
	}

	// export_file：外部导出面。
	if toolName == "export_file" {
		for _, k := range []string{"destination_path", "source_path"} {
			p, ok := input[k].(string)
			if !ok {
				continue
			}
			if isAbsoluteNodeLike(p) || strings.HasPrefix(p, "~") {
				reasons = append(reasons, "export to out-of-workspace path")
				if level != RiskHigh {
					level = RiskMedium
				}
				break
			}
		}
	}

	// web_fetch URL 风险
	if toolName == "web_fetch" {
		url := strInput("url")
		if url != "" {
			m := urlSchemeRe.FindStringSubmatch(url)
			if m == nil {
				reasons = append(reasons, "malformed URL")
				level = RiskMedium
			} else {
				scheme := strings.ToLower(m[1])
				host := hostFromURL(url)
				switch {
				case scheme != "http" && scheme != "https":
					reasons = append(reasons, "non-http URL protocol")
					level = RiskHigh
				case host == "localhost" || host == "127.0.0.1" || host == "::1":
					reasons = append(reasons, "localhost URL target")
					if level != RiskHigh {
						level = RiskMedium
					}
				case ipLiteralRe.MatchString(host):
					reasons = append(reasons, "IP literal URL target")
					if level != RiskHigh {
						level = RiskMedium
					}
				}
			}
		}
	}

	// rollback / undo 总是高风险
	if toolName == "rollback" || toolName == "undo" {
		reasons = append(reasons, "state rollback changes working tree")
		level = RiskHigh
	}

	// MCP 工具风险（Go 侧无 mcp 包，只保留工具名识别与 low 升级）。
	if m := mcpToolNameRe.FindStringSubmatch(toolName); m != nil {
		reasons = append(reasons, `MCP tool from server "`+m[1]+`"`)
		if level == RiskNone {
			level = RiskLow
		}
	}

	// suggestedAction 文案（对账 TS 的三元链）。
	noApprovalAction := func(v any) bool {
		s, _ := v.(string)
		return s == "check_permissions" || s == "wait" || s == "diagnose"
	}
	computerUseNoApproval := false
	if toolName == "computer_use" {
		action := strInput("action")
		if noApprovalAction(action) {
			computerUseNoApproval = true
		} else if action == "sequence" {
			steps, ok := input["steps"].([]any)
			if ok && len(steps) > 0 {
				all := true
				for _, raw := range steps {
					step, _ := raw.(map[string]any)
					if step == nil || !noApprovalAction(step["action"]) {
						all = false
						break
					}
				}
				computerUseNoApproval = all
			}
		}
	}

	var suggestedAction string
	switch {
	case level == RiskHigh:
		suggestedAction = "Require explicit user approval before execution."
	case computerUseNoApproval:
		suggestedAction = "No approval required for this capability probe."
	case toolName == "computer_use":
		suggestedAction = "Per-app approval: this action prompts unless the target app has an always-allow grant (or the session is YOLO)."
	case level == RiskMedium:
		suggestedAction = "Show risk context and proceed only in auto-safe/manual modes."
	default:
		suggestedAction = "No additional approval required."
	}

	return RiskAssessment{Level: level, Reasons: reasons, SuggestedAction: suggestedAction}
}

// hostFromURL 从 URL 提取主机名（不含端口）。
//
// 只需覆盖 web_fetch 判定的用例形态（`scheme://host[:port]/path`），
// 故不引入 net/url 的重量级解析。
func hostFromURL(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	// 去 userinfo
	if j := strings.LastIndex(rest, "@"); j >= 0 {
		rest = rest[j+1:]
	}
	// 去端口（IPv6 字面量的方括号形态此处不细究——oracle 未覆盖）
	if j := strings.LastIndex(rest, ":"); j >= 0 && !strings.Contains(rest, "]") {
		rest = rest[:j]
	}
	return rest
}

// itoa 是局部整数转字符串（避免为此引入 strconv 到本文件）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
