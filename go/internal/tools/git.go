package tools

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// git.go —— 结构化 git 操作。
//
// 对账 TS `src/tools/git.ts`（684 行）。

// gitActions 是支持的 action 列表（对账 TS `ACTIONS`，git.ts:12）。
var gitActions = []string{"status", "diff_summary", "commit", "log", "log_graph", "stash", "stash_pop"}

const (
	// gitMaxOutput 是输出截断上限（对账 TS `MAX_OUTPUT`，git.ts:14）。
	gitMaxOutput = 50_000
	// gitTimeout 是单条 git 命令的超时（对账 TS `GIT_TIMEOUT`，git.ts:15）。
	gitTimeout = 10 * time.Second
)

// gitRun 跑 git 命令并返回 stdout。
//
// 对账 TS `runGit`（git.ts:30-135）。**三个必须复刻的细节**：
//
//  1. **`-c core.quotePath=false` 前缀**：让 diff/status/log 输出里的非 ASCII
//     文件路径**原样输出**，而不是被八进制转义（`"\346\226\207"`）。
//  2. **强制 UTF-8 解码**：git 输出字节总是镜像源文件（现代 git 默认 UTF-8）。
//     用 `WinStreamDecoder` 的 GBK 探测会在首块切在多字节字符中间时误判整个流，
//     把 diff 正文变成乱码——故**不做探测**，直接按 UTF-8 解。
//  3. **非零退出时仍保留 stdout**（对账 TS 的 `gitErr.stdout = stdout`）：
//     `git diff --no-index` 在文件不同时退出 1 但 stdout 有 diff。
func gitExec(cwd string, args ...string) (string, error) {
	fullArgs := append([]string{"-c", "core.quotePath=false"}, args...)
	cmd := SpawnGit(fullArgs, cwd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	signalDone := watchAndKillOnCancel(runCtx, cmd)

	waitErr := cmd.Wait()
	signalDone()

	if runCtx.Err() == context.DeadlineExceeded {
		return "", errors.New("git 命令超时")
	}

	out := stdout.String()
	if waitErr != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "git 以状态码 " + strconv.Itoa(exitCode) + " 退出"
		}
		return out, &gitExitError{msg: msg, exitCode: exitCode, stdout: out}
	}

	if len(out) > gitMaxOutput {
		total := len(out)
		out = out[:gitMaxOutput] + "\n\n[... 已截断至 " + strconv.Itoa(gitMaxOutput) +
			" 字符，共 " + strconv.Itoa(total) + "]"
	}
	return out, nil
}

// gitExitError 携带退出码与 stdout（对账 TS `GitExitError`，git.ts:137）。
type gitExitError struct {
	msg      string
	exitCode int
	stdout   string
}

func (e *gitExitError) Error() string { return e.msg }

// gitTool 是 git 工具实现。
type gitTool struct {
	def     contract.Definition
	enabled bool
}

// Git 构造 git 工具。
func Git() Tool {
	t := &gitTool{enabled: true}
	t.def = contract.Definition{
		Name: "git",
		Description: `结构化 git 操作。Actions:
- status: 显示工作树状态、当前分支和文件改动
- diff_summary: 显示已暂存与未暂存改动的 diff 统计
- commit: 有本会话修改的文件时只提交这些文件；否则只提交已暂存的改动
- log: 显示最近提交历史（默认 20 条，可用 maxCount 配置）
- log_graph: 显示覆盖所有本地与远程引用的 ASCII 分支/合并图
- stash: 暂存当前工作目录改动

复杂 git 操作（branch、merge、rebase、push、pull）改用 bash 工具。`,
		InputSchema: objSchemaOrdered([]string{"action", "message", "maxCount"}, map[string]any{
			"action":   enumPropOrdered("要执行的 git 操作", gitActions),
			"message":  strProp("提交信息（commit action 必填）"),
			"maxCount": numProp("log 条目最大数量（默认 20，用于 log action）"),
		}, "action"),
	}
	return t
}

func (t *gitTool) Definition() contract.Definition { return t.def }
func (t *gitTool) Enabled() bool                   { return t.enabled }

// ConcurrencySafe 对账 TS `isConcurrencySafe: () => false`（git.ts:681）。
func (t *gitTool) ConcurrencySafe() bool { return false }

// RequiresApproval 对账 TS：**只有 commit 需要审批**（git.ts:677-679）。
func (t *gitTool) RequiresApproval(p *CallParams) bool {
	action, _ := p.Input["action"].(string)
	return action == "commit"
}

func (t *gitTool) Timeout(*CallParams) time.Duration { return gitTimeout + 5*time.Second }

func (t *gitTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	action, _ := p.Input["action"].(string)
	cwd := p.Cwd

	if !containsString(gitActions, action) {
		return contract.Result{
			Content: "未知 action：" + action + "。支持：" + strings.Join(gitActions, ", "),
			IsError: true,
		}, nil
	}

	switch action {
	case "status":
		return gitStatus(cwd)
	case "diff_summary":
		return gitDiffSummary(cwd)
	case "commit":
		return gitCommit(p)
	case "log":
		return gitLog(p)
	case "log_graph":
		return gitLogGraph(p)
	case "stash":
		return gitStash(p)
	case "stash_pop":
		return gitStashPop(p)
	}
	return contract.Result{Content: "未知 action：" + action, IsError: true}, nil
}

// gitStatus 对账 TS 的 `status` 分支（git.ts:504-520）。
func gitStatus(cwd string) (contract.Result, error) {
	branch, err := gitExec(cwd, "branch", "--show-current")
	if err != nil {
		return contract.Result{Content: "git status 失败：" + err.Error(), IsError: true}, nil
	}
	porcelain, err := gitExec(cwd, "status", "--porcelain")
	if err != nil {
		return contract.Result{Content: "git status 失败：" + err.Error(), IsError: true}, nil
	}
	untracked, err := gitExec(cwd, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return contract.Result{Content: "git status 失败：" + err.Error(), IsError: true}, nil
	}

	lines := []string{"分支：" + strings.TrimSpace(branch)}
	porcelainTrimmed := strings.TrimSpace(porcelain)
	if porcelainTrimmed == "" {
		lines = append(lines, "状态：干净")
	} else {
		lines = append(lines, "变更：", porcelainTrimmed)
	}
	untrackedTrimmed := strings.TrimSpace(untracked)
	if untrackedTrimmed != "" {
		lines = append(lines, "未跟踪：", untrackedTrimmed)
	}
	return contract.Result{Content: strings.Join(lines, "\n")}, nil
}

// gitDiffSummary 对账 TS 的 `diff_summary` 分支（git.ts:522-536）。
func gitDiffSummary(cwd string) (contract.Result, error) {
	staged, err1 := gitExec(cwd, "diff", "--cached", "--stat")
	unstaged, err2 := gitExec(cwd, "diff", "--stat")
	if err1 != nil {
		return contract.Result{Content: "git diff_summary 失败：" + err1.Error(), IsError: true}, nil
	}
	if err2 != nil {
		return contract.Result{Content: "git diff_summary 失败：" + err2.Error(), IsError: true}, nil
	}

	var lines []string
	stagedTrimmed := strings.TrimSpace(staged)
	unstagedTrimmed := strings.TrimSpace(unstaged)
	if stagedTrimmed != "" {
		lines = append(lines, "已暂存：", stagedTrimmed)
	}
	if unstagedTrimmed != "" {
		lines = append(lines, "未暂存：", unstagedTrimmed)
	}
	if stagedTrimmed == "" && unstagedTrimmed == "" {
		lines = append(lines, "无变更。")
	}
	return contract.Result{Content: strings.Join(lines, "\n")}, nil
}

// gitCommit 对账 TS 的 `commit` 分支（git.ts:538-600）。
//
// **两道敏感文件硬门**（fail-closed）：
//  1. 归属范围内的文件
//  2. 无归属时，已暂存的文件
func gitCommit(p *CallParams) (contract.Result, error) {
	cwd := p.Cwd
	message, _ := p.Input["message"].(string)
	if message == "" {
		return contract.Result{Content: `commit 需要 "message" 参数。`, IsError: true}, nil
	}

	scopedFiles := getScopedCommitFiles(cwd, p.OwnedFiles, p.SessionModifiedFiles)

	// 敏感文件硬门（归属范围）。
	var sensitiveScoped []string
	for _, f := range scopedFiles {
		if DetectSensitiveFile(f).Sensitive {
			sensitiveScoped = append(sensitiveScoped, f)
		}
	}
	if len(sensitiveScoped) > 0 {
		return contract.Result{
			Content: "敏感文件拦截：commit 范围含凭据/密钥文件（" + strings.Join(sensitiveScoped, ", ") + "），已中止。" +
				"阅读或提交凭据/密钥文件不被允许；如确为模板/样例，请改用 .example 后缀或移入 fixtures/ 白名单目录。",
			IsError: true,
		}, nil
	}

	commitArgs := []string{"commit", "-m", message}
	if len(scopedFiles) > 0 {
		if _, err := gitExec(cwd, append([]string{"add", "--"}, scopedFiles...)...); err != nil {
			return contract.Result{Content: "git add 失败：" + err.Error(), IsError: true}, nil
		}
		commitArgs = append(commitArgs, "--only", "--")
		commitArgs = append(commitArgs, scopedFiles...)
	} else if !hasStagedChanges(cwd) {
		return contract.Result{
			Content: "未提供会话归属文件给 git commit，且不存在已暂存变更。请使用 deliver_task 并设 commit=true 做归属范围内交付，或在你有意手动管理 git 时显式暂存文件。",
			IsError: true,
		}, nil
	} else {
		// 无归属时提交「已暂存」内容——暂存名单同样过敏感门。
		stagedOut, err := gitExec(cwd, "diff", "--cached", "--name-only")
		if err != nil {
			stagedOut = ""
		}
		var sensitiveStaged []string
		for _, l := range strings.Split(stagedOut, "\n") {
			f := strings.TrimSpace(l)
			if f != "" && DetectSensitiveFile(f).Sensitive {
				sensitiveStaged = append(sensitiveStaged, f)
			}
		}
		if len(sensitiveStaged) > 0 {
			return contract.Result{
				Content: "敏感文件拦截：已暂存内容含凭据/密钥文件（" + strings.Join(sensitiveStaged, ", ") +
					"），已中止提交。请先 git restore --staged 排除这些文件。",
				IsError: true,
			}, nil
		}
	}

	commitOut, err := gitExec(cwd, commitArgs...)
	if err != nil {
		content := "git commit 失败：" + err.Error()
		res := contract.Result{Content: content, IsError: true}
		if strings.Contains(err.Error(), "超时") {
			res.ErrorClass = errClassPtr(contract.ErrorClassTimeout)
		}
		return res, nil
	}

	// 提交后的真实回读：实际落地的改动 + 标签范围审计。
	//
	// **`changed` 必须 trim**（对账 TS `git.ts:591` 的 `.trim()`）——否则
	// 输出末尾会多一个换行，与 TS 逐字节不符（对账时抓到）。
	changedRaw, err := gitExec(cwd, "show", "--stat", "--format=%h%d", "HEAD")
	if err != nil {
		changedRaw = ""
	}
	changed := strings.TrimSpace(changedRaw)
	var changedFiles []string
	for _, l := range strings.Split(changed, "\n") {
		if !strings.Contains(l, "|") {
			continue
		}
		f := strings.TrimSpace(strings.SplitN(l, "|", 2)[0])
		if f != "" {
			changedFiles = append(changedFiles, f)
		}
	}
	audit := AuditCommitTagScope(message, changedFiles)
	body := strings.TrimSpace(commitOut) + "\n\n--- 实际变更（git show --stat）---\n" + changed
	if !audit.OK {
		body += "\n\n" + audit.Message
	}
	return contract.Result{Content: body}, nil
}

// gitLog 对账 TS 的 `log` 分支（git.ts:602-606）。
//
// **`maxCount` 被夹到 [1, 100]**（对账 TS `Math.max(1, Math.min(x ?? 20, 100))`）。
func gitLog(p *CallParams) (contract.Result, error) {
	maxCount := clampInt(intArg(p.Input, "maxCount", 20), 1, 100)
	out, err := gitExec(p.Cwd, "log", "--max-count="+strconv.Itoa(maxCount), "--oneline", "--decorate")
	if err != nil {
		return contract.Result{Content: "git log 失败：" + err.Error(), IsError: true}, nil
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return contract.Result{Content: "尚无提交。"}, nil
	}
	return contract.Result{Content: trimmed}, nil
}

// gitLogGraph 对账 TS 的 `log_graph` 分支（git.ts:608-626）。
//
// **`maxCount` 被夹到 [1, 500]**（默认 200）。
// 输出用 `TrimEnd`（对账 TS 的 `.trimEnd()`）——保留行尾之外的空行结构。
func gitLogGraph(p *CallParams) (contract.Result, error) {
	maxCount := clampInt(intArg(p.Input, "maxCount", 200), 1, 500)
	out, err := gitExec(p.Cwd,
		"log", "--max-count="+strconv.Itoa(maxCount), "--graph", "--all",
		"--oneline", "--decorate", "--branches", "--remotes")
	if err != nil {
		return contract.Result{Content: "git log_graph 失败：" + err.Error(), IsError: true}, nil
	}
	trimmed := trimEndJS(out)
	if trimmed == "" {
		return contract.Result{Content: "尚无提交。"}, nil
	}
	return contract.Result{Content: trimmed}, nil
}

// gitStash 对账 TS 的 `stash` 分支（git.ts:628-652）。
func gitStash(p *CallParams) (contract.Result, error) {
	cwd := p.Cwd
	status, err := gitExec(cwd, "status", "--porcelain")
	if err != nil {
		return contract.Result{Content: "git stash 失败：" + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(status) == "" {
		return contract.Result{Content: "没有可 stash 的变更。"}, nil
	}

	// B1：有归属文件时把 stash 限定在归属范围。
	if len(p.OwnedFiles) > 0 {
		scoped := getScopedCommitFiles(cwd, p.OwnedFiles, p.SessionModifiedFiles)
		if len(scoped) == 0 {
			return contract.Result{
				Content: "没有可 stash 的归属文件。外部脏文件存在，但已排除在 stash 范围外。",
				IsError: true,
			}, nil
		}
		CreateSafetyRef(cwd)
		if _, err := gitExec(cwd, append([]string{"stash", "push", "--"}, scoped...)...); err != nil {
			return contract.Result{Content: "git stash 失败：" + err.Error(), IsError: true}, nil
		}
		return contract.Result{
			Content: "已 stash " + strconv.Itoa(len(scoped)) + " 个归属文件：" + strings.Join(scoped, ", "),
		}, nil
	}

	CreateSafetyRef(cwd)
	if _, err := gitExec(cwd, "stash"); err != nil {
		return contract.Result{Content: "git stash 失败：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{Content: "已保存工作区与索引状态。"}, nil
}

// gitStashPop 对账 TS 的 `stash_pop` 分支（git.ts:654-662）。
//
// **先做安全检查**：有覆盖冲突则拒绝（fail-closed）。
func gitStashPop(p *CallParams) (contract.Result, error) {
	cwd := p.Cwd
	stashRef, _ := p.Input["stashRef"].(string)
	if stashRef == "" {
		stashRef = "stash@{0}"
	}

	safety := CheckStashSafety(cwd, stashRef)
	if safety.Blocked {
		return contract.Result{Content: strings.Join(safety.Reasons, "\n"), IsError: true}, nil
	}
	if _, err := gitExec(cwd, "stash", "pop", stashRef); err != nil {
		return contract.Result{Content: "git stash_pop 失败：" + err.Error(), IsError: true}, nil
	}
	return contract.Result{Content: "已弹出 " + stashRef + "（已做安全检查：无覆盖冲突）。"}, nil
}

// ── 辅助 ──

// getScopedCommitFiles 取提交范围的归属文件。
//
// 对账 TS `getScopedCommitFiles`（git.ts:158-166）。**优先 `ownedFiles`
// （基线后）而非 `sessionModifiedFiles`（基线前）**。
func getScopedCommitFiles(cwd string, ownedFiles, sessionModifiedFiles []string) []string {
	source := ownedFiles
	if len(source) == 0 {
		source = sessionModifiedFiles
	}
	var out []string
	for _, f := range source {
		if rel := normalizeProjectRelativePath(cwd, f); rel != nil {
			out = append(out, *rel)
		}
	}
	return out
}

// normalizeProjectRelativePath 把路径归一为项目相对路径（越界返回 nil）。
//
// 对账 TS `normalizeProjectRelativePath`（git.ts:150-156）。
func normalizeProjectRelativePath(cwd, filePath string) *string {
	resolved := mustAbsJoin(cwd, filePath)
	rel, err := filepath.Rel(cwd, resolved)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil
	}
	rel = filepath.ToSlash(rel)
	return &rel
}

// hasStagedChanges 报告是否存在已暂存改动。
//
// 对账 TS `hasStagedChanges`（git.ts:168-176）：`git diff --cached --quiet`
// **退出码 1 表示有改动**。
func hasStagedChanges(cwd string) bool {
	_, err := gitExec(cwd, "diff", "--cached", "--quiet")
	if err == nil {
		return false // 退出码 0 → 无改动
	}
	var ge *gitExitError
	if errors.As(err, &ge) {
		return ge.exitCode == 1
	}
	return false
}

// containsString 报告切片是否含目标字符串。
func containsString(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

// clampInt 把 v 夹到 [lo, hi]。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// trimEndJS 复刻 JS 的 `String.prototype.trimEnd()`。
func trimEndJS(s string) string {
	end := len(s)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:end])
		if !isJSWhitespace(r) {
			break
		}
		end -= size
	}
	return s[:end]
}
