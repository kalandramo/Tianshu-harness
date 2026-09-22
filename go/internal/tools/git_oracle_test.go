package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// git_oracle_test.go —— git 工具 + stash 安全的差分对账。
//
// oracle 由 `testdata/gittool/gen-oracle.ts` 真跑 TS 产出。**非确定性字段已
// 归一化**（`<HASH>` / `<DATE>` / `stash@{N}` / 绝对路径），故可逐字节比对。

const gitFixturesDir = "../../testdata/gittool/fixtures"

type gitOracle struct {
	Git []struct {
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Content string          `json:"content"`
		IsError bool            `json:"isError"`
	} `json:"git"`
	GitStagedSummary struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitStagedSummary"`
	GitCommitStaged struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitCommitStaged"`
	GitCommitNothing struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitCommitNothing"`
	GitCommitSensitive struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitCommitSensitive"`
	GitCommitScoped struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitCommitScoped"`
	GitCommitTagWarning struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitCommitTagWarning"`
	StashSafetyDifferent struct {
		Blocked   bool            `json:"blocked"`
		Conflicts []StashConflict `json:"conflicts"`
		Reasons   []string        `json:"reasons"`
	} `json:"stashSafetyDifferent"`
	StashSafetySame struct {
		Blocked   bool            `json:"blocked"`
		Conflicts []StashConflict `json:"conflicts"`
		Reasons   []string        `json:"reasons"`
	} `json:"stashSafetySame"`
	StashSafetyMissingRef struct {
		Blocked   bool            `json:"blocked"`
		Conflicts []StashConflict `json:"conflicts"`
		Reasons   []string        `json:"reasons"`
	} `json:"stashSafetyMissingRef"`
	StashSafetyMissingCurrent struct {
		Blocked   bool            `json:"blocked"`
		Conflicts []StashConflict `json:"conflicts"`
		Reasons   []string        `json:"reasons"`
	} `json:"stashSafetyMissingCurrent"`
	GitStashPopBlocked struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"gitStashPopBlocked"`
}

func loadGitOracle(t *testing.T) *gitOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(gittoolOraclePath))
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v", err)
	}
	var o gitOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return &o
}

var (
	gitHashRe   = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	gitDateRe   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[^\s]*`)
	gitStashNRe = regexp.MustCompile(`stash@\{\d+\}`)
)

// normalizeGitOutput 复刻 oracle 的 `normalizeGit`（必须逐条对应）。
func normalizeGitOutput(s string) string {
	s = normalizeDiffOutput(s)
	s = gitHashRe.ReplaceAllString(s, "<HASH>")
	s = gitDateRe.ReplaceAllString(s, "<DATE>")
	s = gitStashNRe.ReplaceAllString(s, "stash@{N}")
	return s
}

// makeGitRepoFixture 建出与 oracle 相同的仓库。
func makeGitRepoFixture(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(filepath.FromSlash(gitFixturesDir), "gitrepo")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "o@t")
	gitRun(t, repo, "config", "user.name", "o")
	gitRun(t, repo, "config", "core.autocrlf", "false")
	gitRun(t, repo, "config", "commit.gpgsign", "false")

	write := func(rel, content string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.ts", "export const a = 1\n")
	write("README.md", "# T\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "init S1")
	return repo
}

// runGitTool 执行 git 工具并归一化输出。
func runGitTool(t *testing.T, cwd string, input map[string]any, owned []string) (string, bool) {
	t.Helper()
	res, err := Git().Execute(context.Background(), &CallParams{
		Cwd: cwd, Input: input, OwnedFiles: owned, ToolUseID: "t",
	})
	if err != nil {
		t.Fatalf("Execute 报错：%v", err)
	}
	return normalizeGitOutput(res.Content), res.IsError
}

// TestOracleGitTool —— git 工具逐例对账（干净仓库上的用例）。
func TestOracleGitTool(t *testing.T) {
	o := loadGitOracle(t)
	if len(o.Git) == 0 {
		t.Fatal("oracle 无 git 用例")
	}
	repo := makeGitRepoFixture(t)

	// **只跑前 11 个用例**（干净仓库状态）——后面的 dirty 用例需要改动，
	// 由 TestOracleGitToolDirty 单独处理。
	for i := 0; i < 11 && i < len(o.Git); i++ {
		c := o.Git[i]
		var input map[string]any
		if err := json.Unmarshal(c.Input, &input); err != nil {
			t.Fatalf("[%s] 解析 input 失败：%v", c.Name, err)
		}
		got, isErr := runGitTool(t, repo, input, nil)
		if got != c.Content {
			t.Errorf("[%s] content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", c.Name, c.Content, got)
		}
		if isErr != c.IsError {
			t.Errorf("[%s] isError TS=%v Go=%v", c.Name, c.IsError, isErr)
		}
	}
	t.Logf("git 对账 %d 例（干净仓库段）", min(11, len(o.Git)))
}

// TestOracleGitToolDirty —— 有改动后的 status / diff_summary 对账。
func TestOracleGitToolDirty(t *testing.T) {
	o := loadGitOracle(t)
	repo := makeGitRepoFixture(t)

	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.ts", "export const a = 2\n")
	write("src/new.ts", "export const n = 1\n")
	// **非 ASCII 文件名**——锁定 `-c core.quotePath=false`（见 oracle 同段注释）。
	write("src/中文文件.ts", "export const zh = 1\n")
	write("说明文档.md", "# 中文\n")

	for _, want := range []string{"status-dirty", "diff_summary-dirty"} {
		var c *struct {
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content string          `json:"content"`
			IsError bool            `json:"isError"`
		}
		for i := range o.Git {
			if o.Git[i].Name == want {
				c = &o.Git[i]
			}
		}
		if c == nil {
			t.Fatalf("oracle 缺用例 %s", want)
		}
		var input map[string]any
		json.Unmarshal(c.Input, &input)
		got, isErr := runGitTool(t, repo, input, nil)
		if got != c.Content {
			t.Errorf("[%s] content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", want, c.Content, got)
		}
		if isErr != c.IsError {
			t.Errorf("[%s] isError TS=%v Go=%v", want, c.IsError, isErr)
		}
	}
}

// TestOracleGitCommitFlows —— commit 的四种路径对账。
//
// **顺序敏感**：oracle 依次跑 staged→nothing→sensitive→scoped→tagWarning，
// 每步都改变仓库状态。测试必须复刻同一序列。
func TestOracleGitCommitFlows(t *testing.T) {
	o := loadGitOracle(t)
	repo := makeGitRepoFixture(t)

	// 制造改动并暂存 a.ts
	if err := os.WriteFile(filepath.Join(repo, "src", "a.ts"), []byte("export const a = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "new.ts"), []byte("export const n = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "src/a.ts")

	// ① staged summary
	got, isErr := runGitTool(t, repo, map[string]any{"action": "diff_summary"}, nil)
	if got != o.GitStagedSummary.Content || isErr != o.GitStagedSummary.IsError {
		t.Errorf("[staged summary]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitStagedSummary.Content, o.GitStagedSummary.IsError, got, isErr)
	}

	// ② commit 已暂存内容
	got, isErr = runGitTool(t, repo, map[string]any{"action": "commit", "message": "feat: staged only S1"}, nil)
	if got != o.GitCommitStaged.Content || isErr != o.GitCommitStaged.IsError {
		t.Errorf("[commit staged]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitCommitStaged.Content, o.GitCommitStaged.IsError, got, isErr)
	}

	// ③ commit 无归属无暂存 → 报错
	got, isErr = runGitTool(t, repo, map[string]any{"action": "commit", "message": "x"}, nil)
	if got != o.GitCommitNothing.Content || isErr != o.GitCommitNothing.IsError {
		t.Errorf("[commit nothing]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitCommitNothing.Content, o.GitCommitNothing.IsError, got, isErr)
	}

	// ④ commit 归属含敏感文件 → 拦截
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, isErr = runGitTool(t, repo, map[string]any{"action": "commit", "message": "x"}, []string{".env"})
	if got != o.GitCommitSensitive.Content || isErr != o.GitCommitSensitive.IsError {
		t.Errorf("[commit sensitive]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitCommitSensitive.Content, o.GitCommitSensitive.IsError, got, isErr)
	}

	// ⑤ commit 归属正常文件 → 成功
	if err := os.WriteFile(filepath.Join(repo, "src", "b.ts"), []byte("export const b = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, isErr = runGitTool(t, repo, map[string]any{"action": "commit", "message": "feat: scoped S1"}, []string{"src/b.ts"})
	if got != o.GitCommitScoped.Content || isErr != o.GitCommitScoped.IsError {
		t.Errorf("[commit scoped]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitCommitScoped.Content, o.GitCommitScoped.IsError, got, isErr)
	}

	// ⑥ commit 多标签少文件 → 审计警告
	if err := os.WriteFile(filepath.Join(repo, "src", "c.ts"), []byte("export const c = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, isErr = runGitTool(t, repo, map[string]any{"action": "commit", "message": "feat: S1 M1 C1"}, []string{"src/c.ts"})
	if got != o.GitCommitTagWarning.Content || isErr != o.GitCommitTagWarning.IsError {
		t.Errorf("[commit tag warning]\nTS: %q (err=%v)\nGo: %q (err=%v)", o.GitCommitTagWarning.Content, o.GitCommitTagWarning.IsError, got, isErr)
	}
	t.Log("commit 四条路径 + 标签审计对账通过")
}

// makeStashFixture 建出与 oracle 相同的 stash 仓库，并推到指定场景。
//
// 返回 (repo, 场景设置函数已执行)。
func makeStashFixture(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(filepath.FromSlash(gitFixturesDir), "stashrepo")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "o@t")
	gitRun(t, repo, "config", "user.name", "o")
	gitRun(t, repo, "config", "core.autocrlf", "false")

	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("stashed version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "stash", "push", "-q", "-m", "s1")
	return repo
}

// TestOracleStashSafety —— stash 安全四场景对账。
func TestOracleStashSafety(t *testing.T) {
	o := loadGitOracle(t)

	// 场景 A：工作树与 stash 不同 → blocked
	repoA := makeStashFixture(t)
	if err := os.WriteFile(filepath.Join(repoA, "f.txt"), []byte("current different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gotA := CheckStashSafety(repoA, "stash@{0}")
	if gotA.Blocked != o.StashSafetyDifferent.Blocked {
		t.Errorf("[A different] blocked TS=%v Go=%v", o.StashSafetyDifferent.Blocked, gotA.Blocked)
	}
	assertConflicts(t, "A", o.StashSafetyDifferent.Conflicts, gotA.Conflicts)

	// 场景 B：工作树与 stash 相同 → 不 blocked
	repoB := makeStashFixture(t)
	if err := os.WriteFile(filepath.Join(repoB, "f.txt"), []byte("stashed version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gotB := CheckStashSafety(repoB, "stash@{0}")
	if gotB.Blocked != o.StashSafetySame.Blocked {
		t.Errorf("[B same] blocked TS=%v Go=%v", o.StashSafetySame.Blocked, gotB.Blocked)
	}
	assertConflicts(t, "B", o.StashSafetySame.Conflicts, gotB.Conflicts)

	// 场景 C：不存在的 ref → blocked
	repoC := makeStashFixture(t)
	gotC := CheckStashSafety(repoC, "stash@{99}")
	if gotC.Blocked != o.StashSafetyMissingRef.Blocked {
		t.Errorf("[C missing ref] blocked TS=%v Go=%v", o.StashSafetyMissingRef.Blocked, gotC.Blocked)
	}
	if len(gotC.Reasons) != len(o.StashSafetyMissingRef.Reasons) {
		t.Errorf("[C] reasons TS=%v Go=%v", o.StashSafetyMissingRef.Reasons, gotC.Reasons)
	}

	// 场景 D：工作树文件缺失 → missing_current（**不 blocked**）
	repoD := makeStashFixture(t)
	if err := os.Remove(filepath.Join(repoD, "f.txt")); err != nil {
		t.Fatal(err)
	}
	gotD := CheckStashSafety(repoD, "stash@{0}")
	if gotD.Blocked != o.StashSafetyMissingCurrent.Blocked {
		t.Errorf("[D missing current] blocked TS=%v Go=%v", o.StashSafetyMissingCurrent.Blocked, gotD.Blocked)
	}
	assertConflicts(t, "D", o.StashSafetyMissingCurrent.Conflicts, gotD.Conflicts)

	t.Log("stash 安全四场景对账通过")
}

// assertConflicts 比对冲突列表（路径 + 状态，忽略 StashRef 的 `stash@{0}` 差异）。
func assertConflicts(t *testing.T, label string, want, got []StashConflict) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("[%s] conflicts 数 TS=%d Go=%d\nTS=%+v\nGo=%+v", label, len(want), len(got), want, got)
		return
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Status != want[i].Status {
			t.Errorf("[%s] conflicts[%d] TS={%s,%s} Go={%s,%s}",
				label, i, want[i].Path, want[i].Status, got[i].Path, got[i].Status)
		}
	}
}

// TestOracleGitStashPopBlocked —— stash_pop 有冲突时拒绝（fail-closed）。
func TestOracleGitStashPopBlocked(t *testing.T) {
	o := loadGitOracle(t)
	repo := makeStashFixture(t)
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("conflicting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, isErr := runGitTool(t, repo, map[string]any{"action": "stash_pop"}, nil)
	if got != o.GitStashPopBlocked.Content {
		t.Errorf("stash_pop blocked\nTS: %q\nGo: %q", o.GitStashPopBlocked.Content, got)
	}
	if isErr != o.GitStashPopBlocked.IsError {
		t.Errorf("isError TS=%v Go=%v", o.GitStashPopBlocked.IsError, isErr)
	}
}

// TestGitRequiresApprovalOnlyForCommit —— 对账 TS：**只有 commit 需审批**。
func TestGitRequiresApprovalOnlyForCommit(t *testing.T) {
	tool := Git()
	if !tool.RequiresApproval(&CallParams{Input: map[string]any{"action": "commit"}}) {
		t.Error("commit 应要求审批")
	}
	for _, a := range []string{"status", "diff_summary", "log", "log_graph", "stash", "stash_pop"} {
		if tool.RequiresApproval(&CallParams{Input: map[string]any{"action": a}}) {
			t.Errorf("%s 不应要求审批", a)
		}
	}
}

// TestGitNotConcurrencySafe —— 对账 TS：`isConcurrencySafe: () => false`。
func TestGitNotConcurrencySafe(t *testing.T) {
	if Git().ConcurrencySafe() {
		t.Error("git 工具不应标记为并发安全（对账 TS）")
	}
}

// TestGitRegistered —— 注册验证。
func TestGitRegistered(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get("git"); !ok {
		t.Fatal("git 未注册进默认注册表")
	}
}

// TestGitMaxCountClamping —— `maxCount` 夹取（log 1-100，log_graph 1-500）。
//
// 用真实仓库验证：maxCount=0 应回退到默认（log 20 / graph 200），
// 而非 0（0 条输出）。
func TestGitMaxCountClamping(t *testing.T) {
	repo := makeGitRepoFixture(t)
	// maxCount=0 → log 的 `intArg` 返回 0 → clamp 到 1 → 仍有 1 条输出。
	got, _ := runGitTool(t, repo, map[string]any{"action": "log", "maxCount": 0}, nil)
	if strings.TrimSpace(got) == "" {
		t.Errorf("maxCount=0 应被夹到 1（对账 TS Math.max(1, ...)），实得空输出")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
