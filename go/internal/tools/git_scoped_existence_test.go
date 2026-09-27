package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git_scoped_existence_test.go —— `getScopedCommitFiles` 的**存在性过滤**。
//
// # 本文件修的缺陷（第 107 刀的副作用 + TS 既有缺陷）
//
// 第 107 刀给 `submit` 加了草稿回收：成功提交后 `os.Remove` 掉
// `.rivet/plans/draft-<ms>.md`。**但该路径仍留在 `sessionModifiedFiles`**
// （它源自 `session.FileIndex` 的 `ModifiedByMe`，删文件不会清这个标记）。
//
// 而 `getScopedCommitFiles` 只做**路径归一**、不校验存在性 →
// 后续任何 `git commit` 会把「已删除的草稿路径」喂给 `git add`。
//
// **实测后果**（探针 + shell 复现，见文件末注释）：
// `git add -- <存在的文件> <已删除的文件>` **整体失败**（exit=128，
// `fatal: pathspec ... did not match any files`），**连正常文件也暂存不了**。
//
// # 与 TS 的关系（诚实标注）
//
// TS `src/tools/git.ts` 的 `runGit(['add','--', ...scopedFiles])` **同样不校验存在性**
// ——这是**忠实移植来的缺陷**。但按项目纪律：当上游行为会让该功能**自身失效**时，
// 应在 Go 侧修正并显式记录差异，而非以「对账 TS」为名复刻它。

// TestGetScopedCommitFilesDropsMissingPaths —— ★ 核心：不存在的路径被过滤。
func TestGetScopedCommitFilesDropsMissingPaths(t *testing.T) {
	dir := t.TempDir()
	// 一个真实存在的文件
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 一个已被删除的路径（模拟回收后的草稿）
	gone := filepath.Join(dir, ".rivet", "plans", "draft-1.md")

	got := getScopedCommitFiles(dir, nil, []string{real, gone})

	if len(got) != 1 {
		t.Fatalf("应只保留存在的路径，实得 %v", got)
	}
	if got[0] != "real.txt" {
		t.Errorf("应保留 real.txt，实得 %q", got[0])
	}
	for _, f := range got {
		if strings.Contains(f, "draft-1.md") {
			t.Errorf("已删除的草稿路径不该进提交范围：%v", got)
		}
	}
}

// TestGetScopedCommitFilesAllMissingReturnsEmpty —— 全不存在 → 空。
//
// 使 `gitCommit` 走到「无归属文件」分支（fail-loud 报错），
// 而不是把死路径喂给 `git add` 撞 fatal。
func TestGetScopedCommitFilesAllMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got := getScopedCommitFiles(dir, nil, []string{
		filepath.Join(dir, "ghost-a.txt"),
		filepath.Join(dir, "ghost-b.txt"),
	})
	if len(got) != 0 {
		t.Errorf("全不存在时应返回空，实得 %v", got)
	}
}

// TestGitCommitAfterDraftRecycleSucceeds —— ★ 端到端：草稿回收后 commit 仍能提交真实文件。
//
// 这是缺陷的**用户可见后果**——修前此用例报「git add 失败」。
func TestGitCommitAfterDraftRecycleSucceeds(t *testing.T) {
	repo := makeGitRepoFixture(t)
	// **必须绝对化**：`makeGitRepoFixture` 返回的是相对路径（`../../testdata/...`），
	// 而本用例要构造的输入也是绝对路径（模拟生产里 `file_path` 的形态）——
	// 相对 cwd + 绝对 file 会被 `mustAbsJoin` 误当相对拼接，`Rel` 出 `..` 而被
	// 全部过滤（实测踩过：scoped 恒为 nil）。生产 cwd 恒为绝对，故此处对齐生产。
	repo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}

	// 本会话改了一个真实文件。
	if err := os.WriteFile(filepath.Join(repo, "src", "b.ts"), []byte("export const b = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 模拟「submit 回收掉的草稿」——路径已删，但仍被当作会话文件传入。
	goneDraft := filepath.Join(repo, ".rivet", "plans", "draft-999.md")

	res, execErr := Git().Execute(context.Background(), &CallParams{
		Cwd:   repo,
		Input: map[string]any{"action": "commit", "message": "feat: S1"},
		// **两者都给**：真实文件 + 已删除草稿（这正是生产时序的形态）。
		SessionModifiedFiles: []string{
			filepath.Join(repo, "src", "b.ts"),
			goneDraft,
		},
		ToolUseID: "t",
	})
	if execErr != nil {
		t.Fatalf("Execute 报错：%v", execErr)
	}
	if res.IsError {
		t.Fatalf("草稿回收后提交真实文件不该失败（修前报「git add 失败」）：%s", res.Content)
	}

	committed := gitRun(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, "src/b.ts") {
		t.Errorf("真实文件应被提交，实得：%q", committed)
	}
}
