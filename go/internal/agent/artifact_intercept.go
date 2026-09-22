package agent

// artifact_intercept.go —— 工具结果的 L1 artifact 拦截。
//
// 对账 TS `src/agent/tool-pipeline.ts` 的 L1 层（`Artifact intercept`）。
//
// # 分层语义（TS 注释的架构）
//
// 四层压缩架构里，artifact 包装分两层：
//
//   - **L0**：工具实现**自己**包装（read_file / grep / bash / read_section），
//     产出**尾部** `[artifact:id]` 标记。
//   - **L1**：本文件——工具管线层的兜底包装，处理**没有** L0 的工具
//     （delegate_batch / sandbox_exec / 第三方 MCP 工具等）。
//
// # 为什么 L1 必须跳过 L0 工具（TS 记录的真实事故）
//
// 1. **无限嵌套**：read_file/read_section 的内容是模型**明确请求**的。
//    若 L1 再包一层，每次恢复都变成 `[artifact:新ID] → read_section(新ID) → ...`
//    （tianshu v4 pro 2026-05-25 事故复盘）。
// 2. **双重保存**：grep/bash 的 L0 标记在**尾部**，而早期 L1 的检查是
//    `startsWith('[artifact:')`——检查不到尾部标记，于是把**已被截断的字符串**
//    又存了一遍（L0→L1 double-save bug）。
//
// **Go 侧现状**：工具尚无 L0 包装，故 `l0WrappedTools` 当前不会命中。
// 保留该集合是**契约完整性**——未来给 read_file/grep/bash 加 L0 时无需改此处。
//
// # 阈值（对账 TS）
//
// 成功结果用**窗口感知的按工具阈值**（`artifact.ToolArtifactThreshold`）——
// TS 注释称其为四层压缩架构的「L1 层」：静态阈值（2500 / 4000）在 1M 窗口上
// 会误伤中等输出（delegate_batch 14K、bash sed 8K）。
//
// **错误结果不受窗口 floor 影响**（对账 TS）：~30K 以下的栈回溯行内更有用，
// 而更大的错误块（如 200K 测试输出）仍应被拦截。

import (
	"strconv"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/compact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// l0WrappedTools 是**自己做 L0 包装**的工具——L1 不得重复包装。
//
// 对账 TS `L0_WRAPPED_TOOLS`。**必须逐字相同**（工具名）。
var l0WrappedTools = map[string]bool{
	"read_file":    true,
	"read_section": true,
	"grep":         true,
	"bash":         true,
}

// defaultArtifactInterceptThreshold 是无窗口信息时的兜底阈值（字符）。
//
// 对账 TS 的静态 fallback 2500——仅在 contextWindow 未知且工具无特定阈值时用。
const defaultArtifactInterceptThreshold = 2500

// artifactMarkerPresent 报告内容里是否已有 artifact 标记（任意位置）。
//
// 对账 TS：早期只查 `startsWith('[artifact:')`（漏掉尾部标记导致 double-save），
// 现在应查**任意位置**。
func artifactMarkerPresent(content string) bool {
	return strings.Contains(content, "[artifact:")
}

// shouldInterceptForArtifact 判定是否应把结果包成 artifact。
//
// 对账 TS L1 的判断链：
//  1. 工具在 L0 集合里 → **不包**（防嵌套/double-save）
//  2. 内容已含 artifact 标记 → **不包**
//  3. 内容长度 <= 阈值 → **不包**
//  4. 否则 → 包
//
// 阈值：成功结果取「窗口感知按工具阈值」与「兜底阈值」的较大值；
// 错误结果**不用窗口 floor**（只与兜底阈值比）。
//
// `budget` 非 nil 时按剩余预算**缩放阈值**（对账 TS `tool-pipeline.ts:575-581`）：
// 预算充裕时更倾向行内保留（少包 artifact），紧张时回到基础阈值。
// nil 表示无预算信息（对账 TS 的 `remainingBudgetFraction != null` 判空）。
func shouldInterceptForArtifact(toolName, content string, isError bool, contextWindow int, budget *TurnBudget) bool {
	if l0WrappedTools[toolName] {
		return false
	}
	if artifactMarkerPresent(content) {
		return false
	}
	threshold := defaultArtifactInterceptThreshold
	if !isError && contextWindow > 0 {
		if floor := artifact.ToolArtifactThreshold(toolName, contextWindow); floor > threshold {
			threshold = floor
		}
	}
	// ── 预算感知缩放（对账 TS `tool-pipeline.ts:575-581`）──
	//
	// 顺序**必须在窗口 floor 之后**——TS 同样是先 floor 再缩放。
	// `< 0.3` 不加分支：用基础阈值（TS 注释：context is getting tight）。
	if budget != nil {
		switch frac := budget.BudgetFraction(); {
		case frac > 0.5:
			threshold = max(threshold, threshold*3) // 余量充裕 → 3 倍
		case frac > 0.3:
			threshold = max(threshold, threshold*3/2) // 中等 → 1.5 倍
		}
	}
	// 对账 TS：`content.length > threshold`（严格大于）。
	return charLenForArtifact(content) > threshold
}

// charLenForArtifact 返回 UTF-16 code unit 数（对账 JS 的 `content.length`）。
//
// **与 artifact 包内的 charLen 同语义**——此处独立实现避免导出内部函数。
// TS 的阈值比较用的是 `content.length`（code unit），不是字节或码点。
func charLenForArtifact(content string) int {
	n := 0
	for _, r := range content {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// interceptResultForArtifact 在需要时把结果包成 artifact 引用。
//
// 返回**可能被替换**的结果。包装失败时**优雅降级**返回原内容
// （对账 TS 的 try/catch：磁盘写失败就交给下游截断处理）。
//
// store 为 nil 时直接返回原结果（增强而非必需）。
func (l *Loop) interceptResultForArtifact(tc toolCall, res contract.Result) contract.Result {
	if l.Artifacts == nil {
		return res
	}
	if !shouldInterceptForArtifact(tc.name, res.Content, res.IsError, l.artifactContextWindow(), l.turnBudget) {
		return res
	}

	summary := generateArtifactSummary(tc.name, res.Content, tc.input)
	id, err := l.Artifacts.Save(artifact.SaveInput{
		Tool:       tc.name,
		Target:     toolTarget(tc.input),
		RawContent: res.Content,
		Summary:    summary,
		// sections 留空——按扩展名提取需 summarize.ts（后续刀）。
		// 对账 TS 的实际调用（tool-pipeline.ts:607 传 `sections: []`）。
	})
	if err != nil {
		return res // 优雅降级
	}

	// 对账 TS 的包装文案（含错误结果的 head 摘录）。
	headExcerpt := ""
	if res.IsError {
		headExcerpt = "\n" + extractErrorHead(res.Content)
	}
	res.Content = "[artifact:" + id + "] " + summary + headExcerpt +
		"\nUse read_section(artifactId=\"" + id + "\", section=\"L1-L200\") to expand."
	return res
}

// artifactContextWindow 返回当前上下文窗口（0 = 未知）。
func (l *Loop) artifactContextWindow() int {
	if l.Compact != nil {
		return l.Compact.ContextWindow
	}
	return 0
}

// providerProfile 返回提供商切片（nil = 无 profile，走 balanced）。
//
// 对账 TS `config.providerProfile`（`tool-pipeline.ts:465`）。
// 消费者：`read_file` 的 `ComputeModelReadCap`（策略系数影响读上限）。
func (l *Loop) providerProfile() *compact.CompactRatioProfile {
	if l.Compact != nil {
		return l.Compact.ProviderProfile
	}
	return nil
}

// buildToolCallParams 构造工具调用参数（**提取以便接线可测**）。
//
// 对账 TS `tool-pipeline.ts` 的 params 组装。提取自 `executeTool` 内联构造
// ——原来内联时无法单测，导致「字段有读取方、无写入方」的缺陷（read_file
// 读 `p.ProviderProfile` 但构造点从未赋值）不会被任何测试抓到。
func (l *Loop) buildToolCallParams(tc toolCall) *tools.CallParams {
	return &tools.CallParams{
		Input:        tc.input,
		ToolUseID:    tc.id,
		Cwd:          l.cfg.Cwd,
		ApprovalMode: l.cfg.ApprovalMode,
		SessionID:    l.cfg.SessionID,
		// artifact 存储注入 read_section（召回路径）。
		ArtifactStore: l.Artifacts,
		ContextWindow: l.artifactContextWindow(),
		// ProviderProfile 注入 read_file（读上限按提供商策略系数缩放）。
		//
		// 对账 TS `tool-pipeline.ts:465,840` 的 `providerProfile: config.providerProfile`。
		// **来源**：CompactBoundary 持有（bootstrap 时按 provider 名 + 窗口算出）。
		// 无 Compact 时为 nil → 走 balanced（系数 1.0），与 TS 默认一致。
		ProviderProfile: l.providerProfile(),
	}
}

// generateArtifactSummary 生成注入历史的启发式摘要。
//
// 对账 TS `generateToolSummary`（`tool-pipeline.ts`）的**核心分支子集**。
// TS 版按工具分派十余个 case；此处实现覆盖主要工具的部分——**未实现的
// 走通用兜底**（行数 + 字符数 + 前几行预览），与 TS 的 default 分支同形。
//
// **这是有意的简化**（记于 HANDOFF）：摘要文本影响注入历史的字节，但
// 不进冻结前缀（它在 tool_result 里）。移植完整版需逐 case 对账。
func generateArtifactSummary(toolName, content string, input map[string]any) string {
	lines := strings.Split(content, "\n")
	lineCount := len(lines)
	charCount := charLenForArtifact(content)

	switch toolName {
	case "read_file":
		target, _ := input["file_path"].(string)
		if target == "" {
			target, _ = input["path"].(string)
		}
		return "[read_file " + target + "] " + strconv.Itoa(lineCount) + " lines, " + strconv.Itoa(charCount) + " chars."
	case "grep", "search":
		pattern, _ := input["pattern"].(string)
		matches := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				matches++
			}
		}
		return "[grep \"" + pattern + "\"] " + strconv.Itoa(matches) + " matching lines."
	case "bash":
		cmd, _ := input["command"].(string)
		if len(cmd) > 80 {
			cmd = cmd[:80]
		}
		return "[bash " + cmd + "] " + strconv.Itoa(lineCount) + " lines, " + strconv.Itoa(charCount) + " chars."
	case "run_tests":
		return "[run_tests] " + strconv.Itoa(lineCount) + " lines of test output."
	case "glob":
		matches := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				matches++
			}
		}
		pattern, _ := input["pattern"].(string)
		return "[glob \"" + pattern + "\"] " + strconv.Itoa(matches) + " files found."
	default:
		preview := ""
		n := 0
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if n > 0 {
				preview += " | "
			}
			preview += strings.TrimSpace(l)
			n++
			if n >= 3 {
				break
			}
		}
		if len(preview) > 160 {
			preview = preview[:160]
		}
		return "[" + toolName + "] " + strconv.Itoa(lineCount) + " lines, " + strconv.Itoa(charCount) + " chars. " + preview
	}
}

// extractErrorHead 从错误输出里挑最有诊断价值的行（对账 TS `extractErrorHead`）。
//
// 对账 TS：优先含 error/fail 关键词的行（**词边界**匹配——避免命中
// `errorHandler` 这类标识符），最多 8 行、每行截 120 字符；无命中则取
// **末 8 行**（通常含总结）。
func extractErrorHead(content string) string {
	lines := strings.Split(content, "\n")
	errorLines := []string{}
	for _, l := range lines {
		if hasErrorKeyword(l) {
			errorLines = append(errorLines, trimTo(l, 120))
		}
	}
	if len(errorLines) > 0 {
		if len(errorLines) > 8 {
			errorLines = errorLines[:8]
		}
		return strings.Join(errorLines, "\n")
	}
	// 兜底：末 8 行。
	start := len(lines) - 8
	if start < 0 {
		start = 0
	}
	tail := make([]string, 0, 8)
	for _, l := range lines[start:] {
		tail = append(tail, trimTo(l, 120))
	}
	return strings.Join(tail, "\n")
}

// hasErrorKeyword 报告该行是否含错误关键词（词边界）。
//
// 对账 TS 的正则：`\b(?:error|Error|FAIL|AssertionError|TypeError|ReferenceError)\b|expect\(`
func hasErrorKeyword(line string) bool {
	if strings.Contains(line, "expect(") {
		return true
	}
	for _, kw := range []string{"error", "Error", "FAIL", "AssertionError", "TypeError", "ReferenceError"} {
		if containsWord(line, kw) {
			return true
		}
	}
	return false
}

// containsWord 报告 s 是否含以词边界包围的 word。
//
// 词边界 = 非 [A-Za-z0-9_] 字符（对账 JS 的 `\b`）。
func containsWord(s, word string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i < 0 {
			return false
		}
		pos := idx + i
		beforeOK := pos == 0 || !isWordChar(rune(s[pos-1]))
		afterPos := pos + len(word)
		afterOK := afterPos >= len(s) || !isWordChar(rune(s[afterPos]))
		if beforeOK && afterOK {
			return true
		}
		idx = pos + 1
		if idx >= len(s) {
			return false
		}
	}
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// trimTo 截断到 n 个**符文**并去空白（对账 TS 的 `.trim().slice(0, 120)`）。
func trimTo(s string, n int) string {
	t := strings.TrimSpace(s)
	r := []rune(t)
	if len(r) > n {
		return string(r[:n])
	}
	return t
}
