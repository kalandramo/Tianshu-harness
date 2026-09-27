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
	"github.com/kalandramo/tianshu/go/internal/filehistory"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
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
		// Jobs：后台任务注册表（bash 的 run_in_background 与 job 工具用）。
		//
		// 对账 TS `tool-pipeline.ts:842` 的 `jobs: deps.jobs`——TS 侧由
		// `tool-execution.ts:303` 的 `this.deps.getJobs?.()` 取出 AgentLoop 的
		// `_jobs`。**nil 是合法值**：无会话上下文时 bash 退回前台、job 工具
		// 提示不可用（两者都是 TS 的既有语义，不是错误）。
		// **必须显式判空**：`l.Jobs` 是 `*SessionJobs`，直接赋给 `JobRegistry`
		// 接口会产生 **typed-nil**（接口非 nil、底层指针 nil）——消费侧的
		// `p.Jobs != nil` 会通过，随后 `s.jobs[...]` 解引用 nil 崩溃
		// （实测：无会话的 bash 后台调用 panic 在 jobstore.go 的 `s.mu.Lock()`）。
		Jobs: jobRegistryOrNil(l.Jobs),
		// GrantPath：目录子树授权（request_path_access 用）。
		//
		// **为什么需要转换**：`tools.GrantMode` 与 `agent.GrantMode` 是**独立
		// 定义**（字符串值一致，类型不同——避免 import 环）。此处做一次转换。
		//
		// nil 判定：`l.pathGrants` 是 `*pathGrantStore`——**必须显式判空**，
		// 否则赋给 func 的闭包会捕获 typed-nil 并在调用时 panic（同 Jobs 的坑）。
		GrantPath: l.grantPathFunc(),
		// Grants：**会话级**授权存储（第八十一刀修的既有缺陷）。
		//
		// 写/读工具的 `Grants` 是构造时绑定的（`NewDefaultRegistry` 的参数），
		// 而本存储由 `Loop.New()` 创建——**两个实例**；且 `main.go` 装配
		// registry 时根本没传 `Grants`（零值 nil）→ 工具内部永远看不到授权。
		// 经本字段把工具的判定对齐到会话实例（工具侧 `effectiveGrants` 优先用它）。
		//
		// **typed-nil 防护**：`l.pathGrants` 是 `*pathGrantStore`——nil 时
		// 必须返回真 nil 接口（同 Jobs 的坑）。
		Grants: grantCheckerOrNil(l.pathGrants),
		// FileHistory / TrackFileEdit：文件历史（第一百零二刀）。
		//
		// **两者成对**：`TrackFileEdit` 是写入侧的登记钩子（写工具成功调用），
		// `FileHistory` 是 `undo` 工具的读取面。它们共享同一实例——
		// 否则 undo 会读到一个空历史（写工具写进了另一个实例）。
		//
		// **typed-nil 防护**：`l.FileHistory` 是 `*filehistory.History`，
		// nil 时必须传 nil 回调（否则闭包捕获 typed-nil 并在调用时 panic，
		// 同 Jobs / Grants 的坑）。
		TrackFileEdit: l.trackFileEditFunc(),
		FileHistory:   l.fileHistoryFunc(),
	}
}

// trackFileEditFunc 返回写入侧的历史登记钩子（nil = 不跟踪）。
//
// 闭包里带上**当前调用的 tool_use id**——这正是本刀修的数据源：
// `trackEdit(path, id)` 按 id 分组快照（见 filehistory 包注释）。
func (l *Loop) trackFileEditFunc() func(string, string) {
	if l.FileHistory == nil {
		return nil
	}
	return func(absPath, toolUseID string) {
		// **best-effort**：历史登记失败不影响已完成的写入（对账 TS 的
		// `try { trackEdit(...) } catch {}` 语义——历史是附加能力）。
		//
		// id 为空时用固定哨兵：退化为一轮一个快照（历史仍可用，只是粒度粗）。
		if toolUseID == "" {
			toolUseID = "write"
		}
		_ = l.FileHistory.TrackEdit(absPath, toolUseID)
	}
}

// fileHistoryFunc 返回 undo 工具读取的历史面（nil = 不可用）。
func (l *Loop) fileHistoryFunc() func() tools.UndoHistory {
	if l.FileHistory == nil {
		return nil
	}
	return func() tools.UndoHistory { return undoHistoryAdapter{h: l.FileHistory} }
}

// undoHistoryAdapter 把 `filehistory.History` 适配成 `tools.UndoHistory`。
//
// # 为什么需要适配（而不是让 tools 直接 import filehistory）
//
// 两包各自定义同形但**不同名**的类型（`filehistory.DiffStats` vs
// `tools.UndoDiffStats`），Go 的隐式接口满足要求方法签名逐字一致，
// 故无法直接赋值。这与 LSP 的 `lspNavigatorAdapter` 是同一取舍：
// **收益是依赖方向正确**（工具内核不反向依赖子系统），
// 代价是每次返回一次字段拷贝（预览统计只有几个标量，可忽略）。
type undoHistoryAdapter struct {
	h *filehistory.History
}

func (a undoHistoryAdapter) LatestSnapshotID() (string, bool) {
	return a.h.LatestSnapshotID()
}

func (a undoHistoryAdapter) GetDiffStats(targetMessageID string) (*tools.UndoDiffStats, bool) {
	stats, ok := a.h.GetDiffStats(targetMessageID)
	if !ok || stats == nil {
		return nil, ok
	}
	return &tools.UndoDiffStats{
		FilesChanged: stats.FilesChanged,
		Insertions:   stats.Insertions,
		Deletions:    stats.Deletions,
	}, true
}

func (a undoHistoryAdapter) Rewind(targetMessageID string) ([]string, error) {
	return a.h.Rewind(targetMessageID)
}

// grantCheckerOrNil 把可能为 nil 的 *pathGrantStore 转成**真 nil 接口**。
//
// Go 经典陷阱：`var s *T = nil; var i I = s` → `i != nil` 为 true。
// `pathsafe` 视 nil grants 为「无任何授权」（fail-closed），故必须返回真 nil。
func grantCheckerOrNil(s *pathGrantStore) pathsafe.GrantChecker {
	if s == nil {
		return nil
	}
	return s
}

// grantPathFunc 返回注入给 `request_path_access` 的授权回调。
//
// nil store → nil（工具侧 fail-closed 报错，不假装成功）。
func (l *Loop) grantPathFunc() func(string, tools.GrantMode, string) {
	if l.pathGrants == nil {
		return nil
	}
	return func(root string, mode tools.GrantMode, cwd string) {
		// tools.GrantMode → agent.GrantMode（值一致，类型不同）。
		l.pathGrants.GrantPath(root, GrantMode(mode), cwd)
	}
}

// jobRegistryOrNil 把可能为 nil 的 *SessionJobs 转成**真 nil 接口**。
//
// Go 的经典陷阱：`var s *T = nil; var i I = s` → `i != nil` 为 true。
// 消费侧（bash / job 工具）用 `p.Jobs != nil` 判「有无会话」，
// 故 nil 时必须返回真 nil 而非 typed-nil。
func jobRegistryOrNil(j *tools.SessionJobs) tools.JobRegistry {
	if j == nil {
		return nil
	}
	return j
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
