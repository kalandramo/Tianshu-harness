package tools

// commandfilter.go —— 命令感知输出过滤（六族）。
//
// 对账 TS `src/tools/command-filters.ts` 的 `applyCommandFilter` 及其全部内部
// 函数与常量。
//
// # 为什么需要
//
// tsc / test / git 的输出噪声大但语义简单：`tsc --noEmit` 的 200 行里可能只有
// 3 行是 `error TS`。过滤让模型看到**信号**而非**墙**——TS 注释称之为
// 「有损但信息密度高」。
//
// # 三条纪律（对账 TS 文件头注释）
//
//  1. **小输出返回 nil**（无收益零风险）。
//  2. **只删不编**（除合成摘要/截断标记外不改写原文行），丢内容必留 `[+N omitted]`。
//  3. **内容优先于 exit code**：exit 0 但输出含失败签名（`error TS` / `not ok` /
//     `FAIL` / `N failed`）时**按失败处理**——管道会洗白 exit code
//     （incident 2026-07-19：`tsc --noEmit | head` 的 exit 是 head 的 0，
//     filterTsc 曾因此输出 "✓ typecheck passed" 吞掉 10 个真实错误）。
//  4. **含管道的命令一律不过滤**：exit code 不可信，原始输出比错误摘要安全。

import (
	"regexp"
	"strconv"
	"strings"
)

// ApplyCommandFilter 是命令感知过滤器入口。
//
// 对账 TS `applyCommandFilter`。返回 (过滤结果, 是否命中)。
// **未命中时返回 ("", false)** —— 调用方回退到原始输出（TS 的 `null` 语义）。
func ApplyCommandFilter(command, stdout string, exitCode int) (string, bool) {
	cmd := strings.TrimSpace(command)

	// 管道命令 exit code 是最后一环的，对 tsc/test 不可信——放行原始输出。
	if strings.Contains(cmd, "|") {
		return "", false
	}

	// exit 0 但内容含失败签名 → 按失败处理（**内容优先于 exit code**）。
	effectiveExit := exitCode
	if exitCode == 0 && hasFailureSignature(stdout) {
		effectiveExit = 1
	}

	// 族 1：tsc --noEmit
	if tscRe.MatchString(cmd) && strings.Contains(cmd, "--noEmit") {
		return filterTsc(stdout, effectiveExit), true
	}

	// 族 2：node:test / tsx --test
	if nodeTestRe.MatchString(cmd) && strings.Contains(cmd, "--test") {
		return filterNodeTest(stdout, effectiveExit), true
	}

	// 族 3：git status
	if gitStatusRe.MatchString(cmd) {
		return filterGitStatus(stdout), true
	}

	// 族 4：git log（-p/--patch 走 diff 过滤器）
	if gitLogRe.MatchString(cmd) {
		if out, ok := filterGitLog(cmd, stdout); ok {
			return out, true
		}
		return "", false // TS 的 `filterGitLog` 可返回 null（小输出）
	}

	// 族 5：git diff / git show
	if gitDiffRe.MatchString(cmd) {
		if out, ok := filterGitDiff(stdout); ok {
			return out, true
		}
		return "", false
	}

	// 族 6：npm/pnpm/yarn/bun test、vitest/jest 直跑
	if isTestRunCommand(cmd) {
		if out, ok := filterTestRun(stdout, effectiveExit); ok {
			return out, true
		}
		return "", false
	}

	return "", false
}

// ── 命令匹配正则（对账 TS）──

var (
	tscRe       = regexp.MustCompile(`\btsc\b`)
	nodeTestRe  = regexp.MustCompile(`\b(node|tsx|npx\s+tsx)\b`)
	gitStatusRe = regexp.MustCompile(`^git\s+status\b`)
	gitLogRe    = regexp.MustCompile(`^git\s+log\b`)
	gitDiffRe   = regexp.MustCompile(`^git\s+(diff|show)\b`)
	// gitLogPatchRe 对账 TS 的 `/(?:^|\s)-p\b|--patch/`。
	gitLogPatchRe = regexp.MustCompile(`(?:^|\s)-p\b|--patch`)
	// gitLogUserFormatRe 对账 TS 的 `/--oneline|--pretty|--format/`。
	gitLogUserFormatRe = regexp.MustCompile(`--oneline|--pretty|--format`)
)

// failureSignatureRe 对账 TS 的 `FAILURE_SIGNATURE_RE`。
//
// **要求显式非零失败计数**——`"0 failed"` / `"fail 0"` **不**误判（故用
// `[1-9]\d*` 而非 `\d+`）。
//
// TS 用了 `m` 标志（`^`/`$` 逐行）；Go 默认 `^`/`$` 只匹配**文本**首尾，故显式加 `(?m)`。
var failureSignatureRe = regexp.MustCompile(
	`(?m)\berror\s+TS\d+:|^not ok\b|\bAssertionError\b|^FAIL\s|^\s*[×✖✗]\s|\b[1-9]\d*\s+failed\b|ℹ\s*fail\s+[1-9]`)

func hasFailureSignature(stdout string) bool {
	return failureSignatureRe.MatchString(stdout)
}

// ── 共用辅助 ──

// sgrRe 对账 TS 的 `SGR_RE`（ANSI 颜色码）。
var sgrRe = regexp.MustCompile(`\x1B\[[0-9;]*m`)

func stripSgr(text string) string {
	if !strings.Contains(text, "\x1B") {
		return text
	}
	return sgrRe.ReplaceAllString(text, "")
}

// truncateLine 对账 TS `truncateLine`：超宽则截断 + `...`。
//
// **量纲**：TS 的 `line.length` 是 UTF-16 code unit，用 `UTF16Len`。
func truncateLine(line string, width int) string {
	if UTF16Len(line) <= width {
		return line
	}
	return jsSliceHead(line, width-3) + "..."
}

// ── 族 1：tsc --noEmit ──

// tscErrorRe 对账 TS 的 `/\berror\s+TS\d+:/i`。
var tscErrorRe = regexp.MustCompile(`(?i)\berror\s+TS\d+:`)

// tscFoundRe 对账 TS 的 `/^Found\s+\d+\s+error/i`。
var tscFoundRe = regexp.MustCompile(`(?i)^Found\s+\d+\s+error`)

// tscZeroRe 对账 TS 的 `/Found\s+0\s+errors?\.?/i`。
var tscZeroRe = regexp.MustCompile(`(?i)Found\s+0\s+errors?\.?`)

func filterTsc(stdout string, exitCode int) string {
	if exitCode == 0 {
		// 保留 "Found 0 errors" 汇总行（若有），否则合成。
		if m := tscZeroRe.FindString(stdout); m != "" {
			return m
		}
		return "✓ typecheck passed"
	}

	var kept []string
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		// 保留**完整**诊断行（file:line:col + error TS…）——位置信息是修复的
		// 第一素材（TS 注释：剥掉前缀的"美化"曾让 agent 拿着错误找不到现场）。
		if tscErrorRe.MatchString(trimmed) {
			kept = append(kept, trimmed)
			continue
		}
		// 保留汇总 footer："Found N error(s)."
		if tscFoundRe.MatchString(trimmed) {
			kept = append(kept, trimmed)
		}
	}
	if len(kept) > 0 {
		return strings.Join(kept, "\n")
	}
	return strings.TrimSpace(stdout)
}

// ── 族 2：node:test ──

// nodeTestPassedRe 对账 TS 的 `/\d+\s+passed/`。
var nodeTestPassedRe = regexp.MustCompile(`\d+\s+passed`)

// nodeTestNotOkRe / nodeTestAssertRe / nodeTestFailedRe 对账 TS filterNodeTest 的三个判据。
var (
	nodeTestNotOkRe  = regexp.MustCompile(`^not ok\b`)
	nodeTestAssertRe = regexp.MustCompile(`\bAssertionError\b`)
	nodeTestFailedRe = regexp.MustCompile(`\d+\s+failed`)
)

func filterNodeTest(stdout string, exitCode int) string {
	lines := strings.Split(stdout, "\n")

	if exitCode == 0 {
		var summary []string
		for _, l := range lines {
			if nodeTestPassedRe.MatchString(l) {
				summary = append(summary, l)
			}
		}
		if len(summary) > 0 {
			return strings.Join(summary, "\n")
		}
		return strings.TrimSpace(stdout)
	}

	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if nodeTestNotOkRe.MatchString(trimmed) ||
			nodeTestAssertRe.MatchString(trimmed) ||
			nodeTestPassedRe.MatchString(trimmed) ||
			nodeTestFailedRe.MatchString(trimmed) {
			kept = append(kept, trimmed)
		}
	}
	if len(kept) > 0 {
		return strings.Join(kept, "\n")
	}
	return strings.TrimSpace(stdout)
}

// ── 族 3：git status ──

// gitHintRe1/2 对账 TS 的 `/^\(use\s+"git\s/` 与 `/^\(git\s/`。
var (
	gitHintRe1 = regexp.MustCompile(`^\(use\s+"git\s`)
	gitHintRe2 = regexp.MustCompile(`^\(git\s`)
)

func filterGitStatus(stdout string) string {
	var filtered []string
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if gitHintRe1.MatchString(trimmed) || gitHintRe2.MatchString(trimmed) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

// ── 族 4：git log ──

// 对账 TS 的常量。
const (
	gitLogMaxCommits = 15
	gitLogLineWidth  = 120
)

func filterGitLog(cmd, stdout string) (string, bool) {
	// -p/--patch 走 diff 过滤器。
	if gitLogPatchRe.MatchString(cmd) {
		return filterGitDiff(stdout)
	}
	lines := strings.Split(stripSgr(stdout), "\n")
	if len(lines) <= 30 {
		return "", false
	}

	// 自定义格式（--oneline/--pretty/--format，或没有 "commit " 开头的行）。
	hasCommitPrefix := false
	for _, l := range lines {
		if strings.HasPrefix(l, "commit ") {
			hasCommitPrefix = true
			break
		}
	}
	userFormat := gitLogUserFormatRe.MatchString(cmd) || !hasCommitPrefix

	if userFormat {
		shown := make([]string, 0, 41)
		n := len(lines)
		if n > 40 {
			n = 40
		}
		for i := 0; i < n; i++ {
			shown = append(shown, truncateLine(lines[i], gitLogLineWidth))
		}
		omitted := len(lines) - len(shown)
		if omitted > 0 {
			shown = append(shown, "[+"+strconv.Itoa(omitted)+" commits omitted]")
		}
		return strings.Join(shown, "\n"), true
	}

	// 默认格式：保 commit/Date 行 + 最多 3 行 message，剥 Author/空行/trailer。
	var out []string
	commits, omittedCommits := 0, 0
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "commit ") {
			i++
			continue
		}
		if commits >= gitLogMaxCommits {
			omittedCommits++
			i++
			continue
		}
		commits++
		out = append(out, lines[i])
		i++
		messageKept := 0
		for i < len(lines) && !strings.HasPrefix(lines[i], "commit ") {
			t := strings.TrimSpace(lines[i])
			i++
			if strings.HasPrefix(t, "Date:") {
				out = append(out, t)
				continue
			}
			if strings.HasPrefix(t, "Author:") {
				continue
			}
			if t == "" {
				continue
			}
			if strings.HasPrefix(t, "Signed-off-by:") || strings.HasPrefix(t, "Co-authored-by:") {
				continue
			}
			if messageKept < 3 {
				out = append(out, "    "+truncateLine(t, gitLogLineWidth))
				messageKept++
			}
		}
	}
	if omittedCommits > 0 {
		out = append(out, "[+"+strconv.Itoa(omittedCommits)+" commits omitted]")
	}
	return strings.Join(out, "\n"), true
}

// ── 族 5：git diff / show ──

// 对账 TS 的常量。
const (
	diffHunkMaxLines = 60
	diffMaxLines     = 300
)

func filterGitDiff(stdout string) (string, bool) {
	lines := strings.Split(stripSgr(stdout), "\n")
	if len(lines) <= 40 {
		return "", false
	}

	var out []string
	currentFile := ""
	added, removed := 0, 0
	inHunk := false
	hunkShown, hunkSkipped := 0, 0
	preambleKept := 0

	flushFile := func() {
		if hunkSkipped > 0 {
			out = append(out, "  ... ("+strconv.Itoa(hunkSkipped)+" lines truncated)")
			hunkSkipped = 0
		}
		if currentFile != "" && (added > 0 || removed > 0) {
			out = append(out, "  +"+strconv.Itoa(added)+" -"+strconv.Itoa(removed))
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			flushFile()
			currentFile = line
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				currentFile = line[i+3:]
			} else {
				currentFile = "unknown"
			}
			out = append(out, "\n"+currentFile)
			added, removed = 0, 0
			inHunk = false
			hunkShown = 0
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if hunkSkipped > 0 {
				out = append(out, "  ... ("+strconv.Itoa(hunkSkipped)+" lines truncated)")
				hunkSkipped = 0
			}
			inHunk = true
			hunkShown = 0
			out = append(out, "  "+line)
			continue
		}
		if inHunk {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				added++
				if hunkShown < diffHunkMaxLines {
					out = append(out, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
				continue
			}
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				removed++
				if hunkShown < diffHunkMaxLines {
					out = append(out, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
				continue
			}
			if strings.HasPrefix(line, "\\") {
				continue // "\ No newline at end of file"
			}
			// 上下文行：hunk 内首条变更之前的纯上下文不保留（rtk 同款）。
			if hunkShown > 0 && hunkShown < diffHunkMaxLines {
				out = append(out, "  "+line)
				hunkShown++
			}
			continue
		}
		// hunk 外：preamble（git show 的 commit 头 / --stat 块）只保留首个文件
		// 之前的 4 行非空行；文件头之后的 index/mode/---/+++ 行一律丢弃。
		if currentFile == "" && preambleKept < 4 && strings.TrimSpace(line) != "" {
			out = append(out, line)
			preambleKept++
		}
		if len(out) >= diffMaxLines {
			out = append(out, "\n... (more changes truncated)")
			return strings.Join(out, "\n"), true
		}
	}
	flushFile()
	return strings.Join(out, "\n"), true
}

// ── 族 6：test runners ──

// 对账 TS 的四个正则。
var (
	testSummaryRe       = regexp.MustCompile(`Test Files|Test Suites|Tests\s+\d|Duration|^\s*ℹ\s+(tests|suites|pass|fail|skipped|todo)|\d+\s+passed|\d+\s+failed|Ran all test suites`)
	testFailureStartRe  = regexp.MustCompile(`^\s*(FAIL\b|✕|×|✖|✗|not ok\b|●\s|ERR_ASSERTION|AssertionError)`)
	testFailureDetailRe = regexp.MustCompile(
		`(AssertionError|Expected|Actual|Difference|error:\s|Error:\s|\bat\s+\S+\s*\(|\bpassed\b|\bfailed\b)`)
	testPassLineRe = regexp.MustCompile(`^\s*(✓|✔|ok\b|PASS\b)`)
	testNoiseRe    = regexp.MustCompile(`^>\s+\S+@[\w.-]+\s+\S|^\s*npm\s+(WARN|notice)\b|^\s*pnpm\s+WARN\b`)
)

// 对账 TS 的命令匹配正则。
var (
	testRunRe1 = regexp.MustCompile(`^(npm|pnpm|yarn|bun)\s+(run\s+)?[\w:-]*test[\w:-]*(\s|$)`)
	testRunRe2 = regexp.MustCompile(`^(npx|pnpm\s+exec|pnpm\s+dlx|bunx)\s+(vitest|jest)\b`)
	testRunRe3 = regexp.MustCompile(`^(vitest|jest)\b`)
)

func isTestRunCommand(cmd string) bool {
	if testRunRe1.MatchString(cmd) {
		return true
	}
	if testRunRe2.MatchString(cmd) {
		return true
	}
	return testRunRe3.MatchString(cmd)
}

// testPassedCountRe / testPassedCount2Re 对账 TS 里两个 passed 计数的匹配。
var (
	testPassedCountRe  = regexp.MustCompile(`(\d+)\s+passed`)
	testPassedCount2Re = regexp.MustCompile(`ℹ\s+pass\s+(\d+)`)
)

func filterTestRun(stdout string, exitCode int) (string, bool) {
	rawLines := strings.Split(stripSgr(stdout), "\n")
	if len(rawLines) <= 15 {
		return "", false
	}

	// 剥噪声行。
	var lines []string
	for _, l := range rawLines {
		if !testNoiseRe.MatchString(l) {
			lines = append(lines, l)
		}
	}

	if exitCode == 0 {
		var summary []string
		for _, l := range lines {
			if testSummaryRe.MatchString(l) {
				summary = append(summary, l)
			}
		}
		if len(summary) == 0 {
			// 回退：末 10 行。
			n := len(lines)
			start := n - 10
			if start < 0 {
				start = 0
			}
			s := strings.TrimSpace(strings.Join(lines[start:], "\n"))
			if s == "" {
				return "", false
			}
			return s, true
		}
		// 合成 `✓ N passed` 头。
		head := "✓ tests passed"
		for _, l := range summary {
			if m := testPassedCountRe.FindStringSubmatch(l); m != nil {
				head = "✓ " + m[1] + " passed"
				break
			}
			if m := testPassedCount2Re.FindStringSubmatch(l); m != nil {
				head = "✓ " + m[1] + " passed"
				break
			}
		}
		return strings.Join(append([]string{head}, summary...), "\n"), true
	}

	// 失败：保失败块（失败名 + 断言详情窗口）+ 统计行，丢通过项。
	var kept []string
	window := 0
	for _, line := range lines {
		if testSummaryRe.MatchString(line) {
			kept = append(kept, line)
			if window < 0 {
				window = 0
			}
			continue
		}
		if testFailureStartRe.MatchString(line) {
			kept = append(kept, line)
			window = 5
			continue
		}
		if testFailureDetailRe.MatchString(line) && !testPassLineRe.MatchString(line) {
			kept = append(kept, line)
			if window < 3 {
				window = 3
			}
			continue
		}
		if window > 0 && !testPassLineRe.MatchString(line) {
			kept = append(kept, line)
			window--
			continue
		}
		if window > 0 {
			window--
		}
		// 通过项、coverage 行、npm ERR! 前言全部丢弃。
	}
	if len(kept) == 0 {
		n := len(lines)
		start := n - 15
		if start < 0 {
			start = 0
		}
		return strings.Join(lines[start:], "\n"), true
	}
	const maxKept = 120
	if len(kept) > maxKept {
		return strings.Join(append(kept[:maxKept],
			"[+"+strconv.Itoa(len(kept)-maxKept)+" lines omitted]"), "\n"), true
	}
	return strings.Join(kept, "\n"), true
}
