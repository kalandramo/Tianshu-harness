package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// gitscout_e2e_test.go —— git_scout 的**真实 CLI 二进制**端到端验收。
//
// **为什么需要它**（本项目反复踩过的坑）：工具在 `internal/tools` 的单测里
// 全绿 ≠ 生产路径可用。前几轮已三次遇到「组件内部测试全绿，但 CLI 没装配」
// ——`buildLoop` 是内部函数，测它仍是内部视角。只有真跑二进制才能证明
// **用户实际路径**通。
//
// 本测试验证两件事（单测覆盖不到的）：
//  1. `git_scout` 的 schema **真的进了发往端点的请求体**（tools 段）
//  2. 模型调 `git_scout` 时，工具**真的被执行**且结果回灌

// TestCLIEndToEndGitScoutInRequest —— git_scout 进入真实请求体 + 可被调用。
func TestCLIEndToEndGitScoutInRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	// 造一个真 git 仓库（git_scout 要跑真 git）。
	repo := t.TempDir()
	gitRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "f.txt")
	gitRun("commit", "-q", "-m", "e2e-scout-commit")

	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			// 模型第一轮：调 git_scout 看历史。
			fmt.Fprint(w, sseToolCallArgs("c1", "git_scout", map[string]any{"action": "log"}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-scout-test")

	cmd := exec.Command(bin,
		"-p", "看看这个仓库的提交历史",
		"--base-url", srv.URL,
		"--model", "test-model",
	)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	if len(bodies) == 0 {
		t.Fatalf("端点未收到请求\nCLI 输出：%s", out)
	}

	// ── 观察 1：tools 段含 git_scout 的 schema ──
	first := bodies[0]
	if !strings.Contains(first, `"git_scout"`) {
		t.Errorf("**git_scout 未进入真实请求体的 tools 段**——工具在内部测试全绿但生产路径未装配。\n"+
			"首个请求体片段：%.600s", first)
	}
	// 校验关键 schema 字段（属性名与 required）。
	if !strings.Contains(first, `"maxCount"`) || !strings.Contains(first, `"merge_base"`) {
		t.Errorf("git_scout 的 schema 字段不完整（缺 maxCount 或 merge_base）：%.800s", first)
	}

	// ── 观察 2：工具被执行，结果回灌（第二轮请求含工具结果）──
	if len(bodies) < 2 {
		t.Fatalf("应至少有 2 个请求（工具调用 + 结果回灌），实得 %d", len(bodies))
	}
	second := bodies[1]
	if !strings.Contains(second, "e2e-scout-commit") {
		t.Errorf("**git_scout 的结果未回灌到第二轮请求**——工具未被真正执行。\n"+
			"第二轮请求片段：%.800s", second)
	}
}

// TestCLIEndToEndGitScoutRejectsWrite —— 写 action 在真实 CLI 路径下也被拒。
func TestCLIEndToEndGitScoutRejectsWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	repo := t.TempDir()
	gitRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "f.txt")
	gitRun("commit", "-q", "-m", "base")

	headBefore := func() string {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repo
		o, _ := cmd.Output()
		return strings.TrimSpace(string(o))
	}()

	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			// 模型试图用 git_scout 做写操作——应被拒。
			fmt.Fprint(w, sseToolCallArgs("c1", "git_scout", map[string]any{"action": "commit"}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-scout-write-test")
	cmd := exec.Command(bin, "-p", "提交一下", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察：拒绝文案回灌（第二轮请求里应有「只读侦察工具」）。
	if len(bodies) < 2 {
		t.Fatalf("应至少有 2 个请求，实得 %d", len(bodies))
	}
	if !strings.Contains(bodies[1], "只读侦察工具") {
		t.Errorf("写 action 应被拒且拒绝文案回灌，第二轮请求片段：%.800s", bodies[1])
	}

	// **安全关键**：HEAD 未变（写 action 若真跑了会留提交）。
	cmd2 := exec.Command("git", "rev-parse", "HEAD")
	cmd2.Dir = repo
	o, _ := cmd2.Output()
	if headAfter := strings.TrimSpace(string(o)); headAfter != headBefore {
		t.Errorf("**HEAD 被改动**：%q → %q（写 action 未被拦住）", headBefore, headAfter)
	}
}
