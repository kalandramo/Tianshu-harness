package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// sessionmodfiles_e2e_test.go —— `SessionModifiedFiles` → `git commit` 全链路（W2）。
//
// # 为什么必须有这个文件
//
// W1 的 `sessionmodfiles_wiring_test.go` 只断言「构造点填了字段」。那**不等于**
// 「`git commit` 的行为变了」——中间还隔着 `executeTool` → `registry.Execute`
// → `gitCommit` → `getScopedCommitFiles` → `git add`/`git commit` 多道跳。
// 任一环断了，W1 的测试都不会红。
//
// 这正是本仓库栽过的模式（HANDOFF 第 49 条：装配层是「实现已有但零消费」的
// 最后一道缺口）。
//
// # 真实依赖，不 mock
//
// 走真 `New(...)` + 真 registry + **真临时 git 仓库**（`task-depth=wiring`
// 要求实例化真实依赖）。断言的是**磁盘与 git 历史的可观察结果**，不是
// 「函数被调了」。
//
// # 隔离纪律（HANDOFF 第 ⑦ 条坑）
//
// 用 `t.TempDir()` 建独立仓，**绝不用真实系统路径**——那条坑的记录：
// `pathgrant_wiring_test.go` 用 `/etc/passwd` 做用例，skip 档首触即授后
// 真的去写它，导致全量测试超时 600s。

// e2eRepo 建一个临时 git 仓库供端到端用例使用。
//
// 返回仓库路径与一个 git 命令辅助（t.Helper 包裹，失败即 t.Fatal）。
func e2eRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	run("config", "user.email", "e2e@t")
	run("config", "user.name", "e2e")
	run("config", "commit.gpgsign", "false")
	run("config", "core.autocrlf", "false")

	mustWrite(t, filepath.Join(repo, "seed.txt"), "seed\n")
	run("add", ".")
	run("commit", "-qm", "init")
	return repo, run
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newE2ELoop 构造真 Loop，注册真 git 工具。
func newE2ELoop(t *testing.T, repo string) *Loop {
	t.Helper()
	l := New(Config{Cwd: repo, SessionID: "e2e-modfiles"}, nil, tools.NewRegistry())
	l.State = session.New("e2e-modfiles")
	l.registry.Register(tools.Git())
	return l
}

// TestGitCommitScopesToSessionModifiedFiles —— V5：本会话文件被提交、旁路脏文件留在工作树。
//
// 这是本刀的**用户可见后果**——修前这里会报错（无归属无暂存），
// 修后应按 TS 语义提交本会话文件。
//
// 对账 TS `src/tools/git.ts:564` 的
// `getScopedCommitFiles(cwd, params.ownedFiles, params.sessionModifiedFiles)`。
func TestGitCommitScopesToSessionModifiedFiles(t *testing.T) {
	repo, run := e2eRepo(t)
	l := newE2ELoop(t, repo)

	// ① 一个**事先就脏**的旁路文件——模拟「别的会话/任务留下的改动」。
	//    它不在本会话的 ModifiedByMe 集合里，故不该被本次提交卷入。
	mustWrite(t, filepath.Join(repo, "bystander.txt"), "other session work\n")

	// ② 本会话改的文件——经 observeToolResult 的写工具分支录入。
	//
	//    **走真路径**：直接调 `observeToolResult`（`loop.go` 里写工具分支的入口，
	//    签名 `(name, input, res)`），而不是手写 `l.State.TrackFileModified`
	//    ——HANDOFF 第 56 条坑：「e2e 测试若用手工注入的假数据模拟上游步骤，
	//    会把『我假设的时序』编码成期望」。这里上游就是 observeToolResult。
	mustWrite(t, filepath.Join(repo, "mine.txt"), "my work\n")
	l.observeToolResult("write_file",
		map[string]any{"file_path": "mine.txt"},
		contractResultOK("ok"))

	// ③ 提交——不带 ownedFiles（模拟 ownershipLedger 未移植的生产现状）。
	res := l.executeTool(context.Background(), toolCall{
		name: "git", input: map[string]any{"action": "commit", "message": "feat: mine"},
	})
	if res.IsError {
		t.Fatalf("git commit 不应报错（修前会报「未提供会话归属文件」）：%s", res.Content)
	}

	// ④ 断言 git 历史：本会话文件进了提交。
	committed := run("show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, "mine.txt") {
		t.Errorf("本会话文件应被提交，实得提交内容：%q", committed)
	}
	if strings.Contains(committed, "bystander.txt") {
		t.Errorf("旁路脏文件**不该**被卷入提交（归属收敛失效）：%q", committed)
	}

	// ⑤ 断言工作树：旁路文件仍是脏的（未被提交、也未被丢弃）。
	status := run("status", "--porcelain")
	if !strings.Contains(status, "bystander.txt") {
		t.Errorf("旁路文件应仍留在工作树（未被提交）：%q", status)
	}
	if strings.Contains(status, "mine.txt") {
		t.Errorf("本会话文件应已提交（不该再是脏的）：%q", status)
	}
}

// TestGitCommitFailLoudWhenNothingScoped —— V6：无会话改动 + 无暂存 → 仍 fail-loud。
//
// **为什么保留这条**：修「回退源恒空」不能修成「静默提交一切」——
// TS 在真正无归属时的行为是**报错**（`src/tools/git.ts` 的对应分支），
// 这是多会话工作区里防止误提交的 fail-closed 语义。
func TestGitCommitFailLoudWhenNothingScoped(t *testing.T) {
	repo, _ := e2eRepo(t)
	l := newE2ELoop(t, repo)

	// 无任何会话改动、无暂存。
	res := l.executeTool(context.Background(), toolCall{
		name: "git", input: map[string]any{"action": "commit", "message": "x"},
	})
	if !res.IsError {
		t.Fatalf("无归属无暂存时应报错（fail-loud），实得不报错：%s", res.Content)
	}
	if !strings.Contains(res.Content, "未提供会话归属文件") {
		t.Errorf("应报「未提供会话归属文件…」，实得：%s", res.Content)
	}
}

// TestGitCommitExcludesReadOnlyFiles —— 只读的文件不该被本会话提交卷入。
//
// 这是 `ModifiedByMe` 门在端到端层面的体现：读了别人的文件不等于「我改了它」。
func TestGitCommitExcludesReadOnlyFiles(t *testing.T) {
	repo, run := e2eRepo(t)
	l := newE2ELoop(t, repo)

	// 旁路文件：本会话只**读**了它（observeToolResult 的 read_file 分支）。
	mustWrite(t, filepath.Join(repo, "readonly.txt"), "read only\n")
	l.observeToolResult("read_file",
		map[string]any{"file_path": "readonly.txt"},
		contractResultOK("ok"))

	// 本会话真正改的文件。
	mustWrite(t, filepath.Join(repo, "written.txt"), "written\n")
	l.observeToolResult("write_file",
		map[string]any{"file_path": "written.txt"},
		contractResultOK("ok"))

	res := l.executeTool(context.Background(), toolCall{
		name: "git", input: map[string]any{"action": "commit", "message": "feat: written"},
	})
	if res.IsError {
		t.Fatalf("git commit 不应报错：%s", res.Content)
	}

	committed := run("show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, "written.txt") {
		t.Errorf("写过的文件应被提交：%q", committed)
	}
	if strings.Contains(committed, "readonly.txt") {
		t.Errorf("只读的文件**不该**被卷入提交：%q", committed)
	}
}
