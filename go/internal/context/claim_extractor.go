package context

import (
	"regexp"
	"strconv"
	"strings"
)

// ToolResultContext 是工具结果的提取上下文。
//
// 对账 ToolResultContext。
type ToolResultContext struct {
	ToolName string
	Input    map[string]any
	Result   string
	IsError  bool
}

// ClaimExtractionMeta 是提取的元信息。
type ClaimExtractionMeta struct {
	SessionID string
	Turn      int
	EventID   string
}

// claimTTL 是各 claim kind 的存活毫秒数。
//
// 对账 TTL 表。**注意 `Infinity`**——Go 侧用 `ExpiresAt = 0` 表示「永不过期」
// （对账 TS 的 undefined），故这几个 kind 不出 expiresAt。
//
// **Infinity 对应的 kind**：user_constraint / user_preference / decision /
// project_rule。
var claimTTL = map[ContextClaimKind]int64{
	ClaimFileObservation:  30 * 60_000,
	ClaimVerificationFact: 60 * 60_000,
	ClaimFailurePattern:   120 * 60_000,
	ClaimSecurityFinding:  240 * 60_000,
	// 以下为 Infinity（对账 TS）——用 0 表示不过期
	ClaimUserConstraint: 0,
	ClaimUserPreference: 0,
	ClaimDecision:       0,
	ClaimWorkerFinding:  60 * 60_000,
	ClaimProjectRule:    0,
}

// skipTools 是不参与提取的工具。
//
// 对账 SKIP_TOOLS。
var skipTools = map[string]bool{
	"grep": true, "glob": true, "diff": true, "inspect_project": true,
	"repo_map": true, "related_tests": true, "recall": true,
}

// exportRe 匹配 export 声明。
//
// 对账 EXPORT_RE：`^(?:export\s+(?:default\s+)?(?:const|let|var|function|class|interface|type|enum)\s+(\w+)|export\s+\{([^}]+)\})`
//
// **多行模式**（m flag）——`^` 匹配每行开头。
var exportRe = regexp.MustCompile(`(?m)^(?:export\s+(?:default\s+)?(?:const|let|var|function|class|interface|type|enum)\s+(\w+)|export\s+\{([^}]+)\})`)

// commitHashRe 匹配 commit 结果里的 hash。
//
// 对账 COMMIT_HASH_RE：`\[[^\]\n]*?\b([0-9a-f]{7,40})\]`
//
// **TS 注释**：锚定方括号内的 hash——不能取正文里第一个像 hex 的 token。
// commit message 正文常嵌其他 hash（分支名 / stat 数字也像 hex），旧写法
// `/\\b[0-9a-f]{7,40}\\b/` 曾把 ~61% 的 hash 误解析到错误的 commit。
var commitHashRe = regexp.MustCompile(`\[[^\]\n]*?\b([0-9a-f]{7,40})\]`)

// showHashRe 匹配 HEAD 回读格式的 hash。
//
// 对账 SHOW_HASH_RE：`(?:^|\n)([0-9a-f]{7,40}) \(HEAD`
var showHashRe = regexp.MustCompile(`(?m)(?:^|\n)([0-9a-f]{7,40}) \(HEAD`)

// significantCommitRe 匹配显著提交的关键词。
//
// 对账 SIGNIFICANT_COMMIT_RE：`\b(feat|fix|breaking|refactor|perf)\b`（i flag）
var significantCommitRe = regexp.MustCompile(`(?i)\b(feat|fix|breaking|refactor|perf)\b`)

// testResultRe 匹配测试通过数。
//
// 对账 `/(\d+)\s*pass/i`。
var testResultRe = regexp.MustCompile(`(?i)(\d+)\s*pass`)

// securityRe 匹配安全相关内容。
//
// 对账 `/vulnerabilit|CVE-|security|audit/i`。
var securityRe = regexp.MustCompile(`(?i)vulnerabilit|CVE-|security|audit`)

// testCmdRe 匹配 bash 里的测试命令。
//
// 对账 `/test|jest|vitest|pytest/i`。
var testCmdRe = regexp.MustCompile(`(?i)test|jest|vitest|pytest`)

// ExtractClaimsFromToolResult 从工具结果提取 claim 提案。
//
// 对账 extractClaimsFromToolResult。**五种提取路径**：
//
//	read_file（成功）      → file_observation（有 existingFileObservations 去重）
//	run_tests / bash(测试) → 失败 failure_pattern / 通过 verification_fact
//	bash 失败含安全关键词   → security_finding
//	git commit / deliver_task(commit) 且显著 → decision（commit fact）
//	其余                   → 空
//
// **前置门槛**：SKIP_TOOLS 跳过；结果长度 < 10 跳过。
//
// **now 参数**：TS 内部用 `Date.now()`——为可对账与可测，Go 侧显式传入。
func ExtractClaimsFromToolResult(ctx ToolResultContext, meta ClaimExtractionMeta, existingFileObservations map[string]bool, now int64) []ClaimProposal {
	if skipTools[ctx.ToolName] {
		return nil
	}
	if len(ctx.Result) < 10 {
		return nil
	}

	if ctx.ToolName == "read_file" && !ctx.IsError {
		path := stringInput(ctx.Input, "file_path")
		if existingFileObservations[path] {
			return nil
		}
		return []ClaimProposal{fileObservationProposal(ctx, meta, now)}
	}

	isTestRun := ctx.ToolName == "run_tests" ||
		(ctx.ToolName == "bash" && testCmdRe.MatchString(stringInput(ctx.Input, "command")))

	if isTestRun {
		if ctx.IsError {
			return []ClaimProposal{failurePatternProposal(ctx, meta, now)}
		}
		if testResultRe.MatchString(ctx.Result) {
			return []ClaimProposal{verificationFactProposal(ctx, meta, now)}
		}
		return nil
	}

	if ctx.ToolName == "bash" && ctx.IsError && securityRe.MatchString(ctx.Result) {
		return []ClaimProposal{securityFindingProposal(ctx, meta, now)}
	}

	// Commit fact：提取 hash + message 作为 decision claim（decision kind 的
	// TTL 为 Infinity）。**节流**：只有显著提交（feat/fix/breaking/refactor/perf
	// 或 3+ 文件）才持久化——常规单文件提交仅会话内，减少文件膨胀。
	isCommitResult := (ctx.ToolName == "git" && stringInput(ctx.Input, "action") == "commit") ||
		(ctx.ToolName == "deliver_task" && boolInput(ctx.Input, "commit"))
	if isCommitResult && !ctx.IsError && isSignificantCommit(ctx) {
		return []ClaimProposal{commitFactProposal(ctx, meta, now)}
	}

	return nil
}

// stringInput 取字符串入参。
func stringInput(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// boolInput 取布尔入参。
func boolInput(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// fileObservationProposal 构造 file_observation 提案。
//
// 对账 fileObservation。**text 格式**：
//
//	有符号：`<filename> (<N>L): <sym1>, <sym2>, ...`（最多 8 个）
//	无符号：`Read <filename> (<N> lines)`
//
// **N = 行数**（对账 `result.split('\n').length`——注意尾部换行会多算一行）。
func fileObservationProposal(ctx ToolResultContext, meta ClaimExtractionMeta, now int64) ClaimProposal {
	path := stringInput(ctx.Input, "file_path")
	filename := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		filename = path[i+1:]
	}
	lines := strings.Count(ctx.Result, "\n") + 1
	symbols := extractSymbols(ctx.Result)

	text := "Read " + filename + " (" + strconv.Itoa(lines) + " lines)"
	if len(symbols) > 0 {
		n := len(symbols)
		if n > 8 {
			n = 8
		}
		text = filename + " (" + strconv.Itoa(lines) + "L): " + strings.Join(symbols[:n], ", ")
	}

	return ClaimProposal{
		Kind:       ClaimFileObservation,
		Scope:      ScopeSession,
		Text:       text,
		Confidence: 0.6,
		Fitness:    2,
		Source: ClaimSource{
			Actor: "tool", SessionID: meta.SessionID, Turn: meta.Turn, EventID: meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID:        meta.EventID + ":read",
			Kind:      "tool_result",
			Summary:   "read_file " + filename,
			Path:      path,
			CreatedAt: now,
		}},
		CreatedAt: now,
		ExpiresAt: now + claimTTL[ClaimFileObservation],
		Tags:      []string{"tool", "read_file"},
	}
}

// extractSymbols 抽取导出符号（最多 10 个）。
//
// 对账 extractSymbols。**两种形式**：
//
//	export const/let/var/function/class/interface/type/enum <name>
//	export { a, b as c }   → 取 `as` 之后的别名
func extractSymbols(content string) []string {
	var symbols []string
	for _, m := range exportRe.FindAllStringSubmatch(content, -1) {
		if m[1] != "" {
			symbols = append(symbols, m[1])
		} else if m[2] != "" {
			for _, name := range strings.Split(m[2], ",") {
				trimmed := strings.TrimSpace(name)
				// 取 `as` 之后的别名（对账 `split(/\s+as\s+/).pop()`）
				if idx := strings.LastIndex(trimmed, " as "); idx >= 0 {
					trimmed = strings.TrimSpace(trimmed[idx+4:])
				}
				if trimmed != "" {
					symbols = append(symbols, trimmed)
				}
			}
		}
		if len(symbols) >= 10 {
			break
		}
	}
	return symbols
}

// failurePatternProposal 构造 failure_pattern 提案。
//
// 对账 failurePattern。text 取结果**前 200 字符**，换行替换为空格。
func failurePatternProposal(ctx ToolResultContext, meta ClaimExtractionMeta, now int64) ClaimProposal {
	summary := replaceNewlines(sliceFirst(ctx.Result, 200))
	return ClaimProposal{
		Kind:       ClaimFailurePattern,
		Scope:      ScopeSession,
		Text:       summary,
		Confidence: 0.8,
		Fitness:    5,
		Source: ClaimSource{
			Actor: "tool", SessionID: meta.SessionID, Turn: meta.Turn, EventID: meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID: meta.EventID + ":fail", Kind: "test", Summary: summary, CreatedAt: now,
		}},
		CreatedAt: now,
		ExpiresAt: now + claimTTL[ClaimFailurePattern],
		Tags:      []string{"tool", "test_failure"},
	}
}

// verificationFactProposal 构造 verification_fact 提案。
//
// 对账 verificationFact。text：`Tests: <N> pass`（有数字）或 `Tests passing`。
func verificationFactProposal(ctx ToolResultContext, meta ClaimExtractionMeta, now int64) ClaimProposal {
	m := testResultRe.FindStringSubmatch(ctx.Result)
	text := "Tests passing"
	if len(m) > 1 {
		text = "Tests: " + m[1] + " pass"
	}
	return ClaimProposal{
		Kind:       ClaimVerificationFact,
		Scope:      ScopeSession,
		Text:       text,
		Confidence: 0.9,
		Fitness:    3,
		Source: ClaimSource{
			Actor: "tool", SessionID: meta.SessionID, Turn: meta.Turn, EventID: meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID: meta.EventID + ":verify", Kind: "test", Summary: text, CreatedAt: now,
		}},
		CreatedAt: now,
		ExpiresAt: now + claimTTL[ClaimVerificationFact],
		Tags:      []string{"tool", "test_pass"},
	}
}

// securityFindingProposal 构造 security_finding 提案。
func securityFindingProposal(ctx ToolResultContext, meta ClaimExtractionMeta, now int64) ClaimProposal {
	summary := replaceNewlines(sliceFirst(ctx.Result, 200))
	return ClaimProposal{
		Kind:       ClaimSecurityFinding,
		Scope:      ScopeSession,
		Text:       summary,
		Confidence: 0.75,
		Fitness:    6,
		Source: ClaimSource{
			Actor: "tool", SessionID: meta.SessionID, Turn: meta.Turn, EventID: meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID: meta.EventID + ":security", Kind: "tool_result", Summary: summary, CreatedAt: now,
		}},
		CreatedAt: now,
		ExpiresAt: now + claimTTL[ClaimSecurityFinding],
		Tags:      []string{"tool", "security"},
	}
}

// isSignificantCommit 判定提交是否显著。
//
// 对账 isSignificantCommit：message 匹配关键词，**或** stat 行 ≥ 3
// （对账 `result.split('\n').filter(l => l.includes('|')).length >= 3`）。
func isSignificantCommit(ctx ToolResultContext) bool {
	message := stringInput(ctx.Input, "message")
	if significantCommitRe.MatchString(message) {
		return true
	}
	return countLinesContaining(ctx.Result, "|") >= 3
}

// commitFactProposal 构造 commit fact 提案（decision kind）。
//
// 对账 commitFact。**text 格式**：
//
//	`Commit <hash> (turn <N>): "<message 前 80 字符>" (<files>)`
//
// **hash 提取**：先试 COMMIT_HASH_RE（方括号内），失败试 SHOW_HASH_RE
// （HEAD 回读），都失败则 `unknown`。
//
// **files**：从含 `|` 的 stat 行提取第一段，取前 5 个用 `, ` 连接；
// 无 stat 行则 `unknown files`。
func commitFactProposal(ctx ToolResultContext, meta ClaimExtractionMeta, now int64) ClaimProposal {
	m := commitHashRe.FindStringSubmatch(ctx.Result)
	if m == nil {
		m = showHashRe.FindStringSubmatch(ctx.Result)
	}
	hash := "unknown"
	if len(m) > 1 {
		hash = m[1]
	}

	message := sliceFirst(stringInput(ctx.Input, "message"), 80)

	var files []string
	for _, line := range strings.Split(ctx.Result, "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		// 对账 `l.split('|')[0].trim()`
		part := strings.TrimSpace(strings.SplitN(line, "|", 2)[0])
		if part != "" {
			files = append(files, part)
		}
	}
	filesStr := "unknown files"
	if len(files) > 0 {
		n := len(files)
		if n > 5 {
			n = 5
		}
		filesStr = strings.Join(files[:n], ", ")
	}

	text := "Commit " + hash + " (turn " + strconv.Itoa(meta.Turn) + "): \"" + message + "\" (" + filesStr + ")"

	return ClaimProposal{
		Kind:       ClaimDecision,
		Scope:      ScopeProject,
		Text:       text,
		Confidence: 0.95,
		Fitness:    8,
		Source: ClaimSource{
			Actor: "tool", SessionID: meta.SessionID, Turn: meta.Turn, EventID: meta.EventID,
		},
		Evidence: []EvidenceRef{{
			ID: meta.EventID + ":commit", Kind: "tool_result", Summary: text, CreatedAt: now,
		}},
		CreatedAt: now,
		// decision kind 的 TTL 为 Infinity——**不设 expiresAt**（对账 TS 的注释）
		Tags: []string{"tool", "commit", "git", "commit_fact"},
	}
}

// sliceFirst 取字符串前 n 个**字符**（对账 JS 的 `slice(0, n)`）。
//
// **注意**：JS 的 slice 按 UTF-16 code unit 计——对中文（BMP 内）与 ASCII
// 都等同于按字符。超出 BMP 的字符（emoji）JS 会切断代理对，这里按 rune
// 处理（更正确，但**与 TS 在 emoji 边界处可能分歧**——已记入已知边界）。
func sliceFirst(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// replaceNewlines 把换行替换为空格（对账 `replace(/\n/g, ' ')`）。
func replaceNewlines(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}

// countLinesContaining 数含指定子串的行数。
func countLinesContaining(s, sub string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}
