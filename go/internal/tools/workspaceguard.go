package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// workspaceguard.go —— 工作区安全检查（**只移植 `git.ts` 实际消费的部分**）。
//
// 对账 TS `src/agent/workspace-guard.ts`（453 行）。
//
// # scope 收窄（**明示**）
//
// TS 的 `WorkspaceGuard` 有 4 个方法：`checkRuntimeArtifacts` /
// `checkStashSafety` / `checkMergeSafety` / `fullReport`。**`git.ts` 只用
// `checkStashSafety`**（已核实：`grep createWorkspaceGuard src/tools/git.ts`
// 仅命中 656 行的 `stash_pop` 分支）。
//
// 其余三个**不移植**——它们没有生产消费者（`fullReport` 只在 TS 的
// `/doctor` 与 TUI 里用，那两个在 Go 侧不存在）。移植无消费者的代码会产出
// 「实现了但零接线」的悬空模块——本系列已抓到五例同型缺陷，不新增。

// StashConflictStatus 是 stash 冲突状态。
//
// 对账 TS `WorkspaceGuardReport['stashConflicts'][number]['status']`。
type StashConflictStatus string

const (
	// StashConflictSame 是工作树内容与 stash 版本**相同**。
	StashConflictSame StashConflictStatus = "same"
	// StashConflictDifferent 是内容**不同**——应用 stash 会覆盖。
	StashConflictDifferent StashConflictStatus = "different"
	// StashConflictMissingStash 是文件不在 stash 树里。
	StashConflictMissingStash StashConflictStatus = "missing_stash"
	// StashConflictMissingCurrent 是文件在 stash 里但**不在工作树**。
	StashConflictMissingCurrent StashConflictStatus = "missing_current"
)

// StashConflict 是单文件的 stash 冲突项。
type StashConflict struct {
	StashRef string              `json:"stashRef"`
	Path     string              `json:"path"`
	Status   StashConflictStatus `json:"status"`
}

// StashSafetyCheck 是 stash 安全检查结果。
//
// 对账 TS `StashSafetyCheck`（workspace-guard.ts:65-69）。
type StashSafetyCheck struct {
	Conflicts []StashConflict
	Blocked   bool
	Reasons   []string
}

// CheckStashSafety 检查应用 stash 是否会覆盖工作树里不同的内容。
//
// 对账 TS `checkStashSafety`（workspace-guard.ts:212-303）。
//
// **判定链**（顺序敏感）：
//  1. `git stash show --name-only <ref>` 失败 → blocked（ref 不存在）
//  2. 无文件 → 不 blocked，理由「Stash has no files to compare.」
//  3. 逐文件：取 stash 版本内容 → 内容为空时再查 `ls-tree` 确认存在性 →
//     工作树文件缺失 → `missing_current`；内容 hash 相同 → `same`；
//     不同 → `different`
//  4. **`different` 存在即 blocked**；`missing_current` 只警告
func CheckStashSafety(cwd, stashRef string) StashSafetyCheck {
	var reasons []string
	var conflicts []StashConflict

	absCwd := mustAbs(cwd)

	// 1. stash 里的文件列表（ref 不存在则 git 报错）
	stashFiles, err := gitLines(absCwd, "stash", "show", "--name-only", stashRef)
	if err != nil {
		return StashSafetyCheck{
			Conflicts: []StashConflict{},
			Blocked:   true,
			Reasons:   []string{"BLOCKED: stash ref " + stashRef + " does not exist or git error."},
		}
	}

	if len(stashFiles) == 0 {
		return StashSafetyCheck{
			Conflicts: []StashConflict{},
			Blocked:   false,
			Reasons:   []string{"Stash has no files to compare."},
		}
	}

	for _, file := range stashFiles {
		absPath := filepath.Join(absCwd, filepath.FromSlash(file))

		// 2. 取 stash 版本内容
		stashContent, err := gitString(absCwd, "show", stashRef+":"+file)
		if err != nil {
			// 文件不在 stash 树里
			conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictMissingStash})
			continue
		}
		// 空内容是合法的（空文件），但要确认文件确实存在于 stash 树。
		if stashContent == "" {
			lsResult, lsErr := gitString(absCwd, "ls-tree", stashRef, "--", file)
			if lsErr != nil || strings.TrimSpace(lsResult) == "" {
				conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictMissingStash})
				continue
			}
		}

		// 3. 工作树文件是否存在
		if !isRegularFile(absPath) {
			conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictMissingCurrent})
			continue
		}

		// 4. hash 比对
		raw, readErr := os.ReadFile(absPath)
		if readErr != nil {
			conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictMissingCurrent})
			continue
		}
		currentHash := sha256Hex(string(raw))
		stashHash := sha256Hex(stashContent)
		if currentHash == stashHash {
			conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictSame})
		} else {
			conflicts = append(conflicts, StashConflict{StashRef: stashRef, Path: file, Status: StashConflictDifferent})
		}
	}

	var differentFiles, missingCurrent []StashConflict
	for _, c := range conflicts {
		switch c.Status {
		case StashConflictDifferent:
			differentFiles = append(differentFiles, c)
		case StashConflictMissingCurrent:
			missingCurrent = append(missingCurrent, c)
		}
	}

	if len(differentFiles) > 0 {
		paths := make([]string, len(differentFiles))
		for i, f := range differentFiles {
			paths[i] = f.Path
		}
		reasons = append(reasons,
			"BLOCKED: "+strconv.Itoa(len(differentFiles))+" file(s) in working tree have different content from stash "+stashRef+": "+
				strings.Join(paths, ", ")+". "+
				"Applying stash would overwrite current content. Compare manually before proceeding.")
	}

	if len(missingCurrent) > 0 {
		paths := make([]string, len(missingCurrent))
		for i, f := range missingCurrent {
			paths[i] = f.Path
		}
		reasons = append(reasons,
			"WARNING: "+strconv.Itoa(len(missingCurrent))+" file(s) exist in stash but missing from working tree: "+
				strings.Join(paths, ", ")+".")
	}

	return StashSafetyCheck{
		Conflicts: conflicts,
		Blocked:   len(differentFiles) > 0,
		Reasons:   reasons,
	}
}

// CreateSafetyRef 在 stash 前创建安全引用，使改动可恢复。
//
// 对账 TS `createSafetyRef`（git.ts:178-185）。**best-effort，永不阻塞
// stash**——任何失败都静默吞掉（对账 TS 的空 catch）。
func CreateSafetyRef(cwd string) {
	sha, err := gitExec(cwd, "stash", "create")
	if err != nil {
		return
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return
	}
	_, _ = gitExec(cwd, "update-ref", "refs/kiro-safety/last-stash", sha)
}

// isRegularFile 报告路径是否存在且为常规文件。
//
// 对账 TS `fileExists`（workspace-guard.ts:113-119）。
func isRegularFile(absPath string) bool {
	info, err := os.Stat(absPath)
	return err == nil && info.Mode().IsRegular()
}

// sha256Hex 返回内容的 sha256 十六进制摘要。
//
// 对账 TS `sha256`（workspace-guard.ts:121-123）。
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ── git 调用辅助（内部，供本文件与 git.go 共用）──

// gitLines 跑 git 并把 stdout 按行切分（去空行）。
//
// 对账 TS `gitLines`（workspace-guard.ts:97-101）：
// `stdout.trim().split('\n').filter(Boolean)`。git 失败返回 error。
func gitLines(cwd string, args ...string) ([]string, error) {
	out, err := gitExec(cwd, args...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	var lines []string
	for _, l := range strings.Split(trimmed, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// gitString 跑 git 并返回原始 stdout（不 trim）。
//
// 对账 TS `gitString`（workspace-guard.ts:107-110）。
func gitString(cwd string, args ...string) (string, error) {
	return gitExec(cwd, args...)
}
