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

// diff_oracle_test.go —— diff / spawn-git 的差分对账。
//
// oracle 由 `testdata/diff/gen-oracle.ts` 真跑 TS 产出。**非确定性字段已
// 归一化**（`time=<T>s`、绝对路径 `<ABS>`/`<ROOT>`），故可逐字节比对。

const diffOraclePath = "../../testdata/diff/oracle.json"
const diffFixturesDir = "../../testdata/diff/fixtures"

// normalizeDiffOutput 复刻 oracle 的归一化，使两侧可比。
//
// **必须与 gen-oracle.ts 的 `normalize` 逐条一致**——否则对账会因归一化
// 差异而假红/假绿。
var (
	diffTimeRe    = regexp.MustCompile(`time=[\d.]+s`)
	diffRootRe    = regexp.MustCompile(`workspace root: [^\s)]+`)
	diffAbsPathRe = regexp.MustCompile(`[A-Za-z]:\\[^\s"]*|[A-Za-z]:/[^\s"]*|/(?:tmp|private|var)/[^\s"]*`)
)

func normalizeDiffOutput(s string) string {
	s = diffTimeRe.ReplaceAllString(s, "time=<T>s")
	s = diffRootRe.ReplaceAllString(s, "workspace root: <ROOT>")
	s = diffAbsPathRe.ReplaceAllString(s, "<ABS>")
	return s
}

type diffOracle struct {
	SanitizeGitEnv []struct {
		Input   map[string]string `json:"input"`
		Output  map[string]string `json:"output"`
		Removed []string          `json:"removed"`
	} `json:"sanitizeGitEnv"`
	ResolveGitCommand []struct {
		Env      map[string]string `json:"env"`
		Platform string            `json:"platform"`
		Out      string            `json:"out"`
	} `json:"resolveGitCommand"`
	Diff []struct {
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Content   string          `json:"content"`
		UIContent *string         `json:"uiContent"`
		IsError   bool            `json:"isError"`
	} `json:"diff"`
	DiffStaged struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffStaged"`
	DiffOwned struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffOwned"`
	DiffOwnedEmpty struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffOwnedEmpty"`
	DiffOwnedOutside struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffOwnedOutside"`
	DiffClean struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffClean"`
	DiffNotGit struct {
		Content string `json:"content"`
		IsError bool   `json:"isError"`
	} `json:"diffNotGit"`
	// 单文件截断的**边界用例**（`MAX_LINES_PER_FILE` 附近）。
	DiffBoundary195 *diffBoundaryCase `json:"diff_boundary195"`
	DiffBoundary196 *diffBoundaryCase `json:"diff_boundary196"`
	DiffBoundary197 *diffBoundaryCase `json:"diff_boundary197"`
}

type diffBoundaryCase struct {
	N       int    `json:"n"`
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

func loadDiffOracle(t *testing.T) *diffOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(diffOraclePath))
	if err != nil {
		t.Fatalf("读取 oracle 失败（需先跑 gen-oracle.ts）：%v", err)
	}
	var o diffOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return &o
}

// ── sanitizeGitEnv ──

// TestOracleSanitizeGitEnv —— 危险 GIT_* 剥离逐例对账。
func TestOracleSanitizeGitEnv(t *testing.T) {
	o := loadDiffOracle(t)
	if len(o.SanitizeGitEnv) == 0 {
		t.Fatal("oracle 无 sanitizeGitEnv 用例")
	}
	for i, c := range o.SanitizeGitEnv {
		// 构造 []string 形式的 env。
		var env []string
		for k, v := range c.Input {
			env = append(env, k+"="+v)
		}
		got := SanitizeGitEnv(env)

		// 转回 map 比对（顺序无关）。
		gotMap := map[string]string{}
		for _, kv := range got {
			if idx := strings.IndexByte(kv, '='); idx >= 0 {
				gotMap[kv[:idx]] = kv[idx+1:]
			}
		}
		if len(gotMap) != len(c.Output) {
			t.Errorf("[%d] 存活项数不符 TS=%d Go=%d\nTS=%v\nGo=%v", i, len(c.Output), len(gotMap), c.Output, gotMap)
			continue
		}
		for k, v := range c.Output {
			if gotMap[k] != v {
				t.Errorf("[%d] %s TS=%q Go=%q", i, k, v, gotMap[k])
			}
		}
		// 被剥离的项必须真的不在结果里。
		for _, k := range c.Removed {
			if _, ok := gotMap[k]; ok {
				t.Errorf("[%d] %q 应被剥离但仍存在", i, k)
			}
		}
	}
	t.Logf("sanitizeGitEnv 对账 %d 例", len(o.SanitizeGitEnv))
}

// TestSanitizeGitEnvKeepsBenign —— **反证**：良性 GIT_* 必须保留。
//
// 对账 TS 注释：「天枢是开发工具，用户合法的 git 配置（如 GIT_SSH 自定义）
// 必须保留」。这条锁定「不要过度剥离」。
func TestSanitizeGitEnvKeepsBenign(t *testing.T) {
	benign := []string{"GIT_SSH", "GIT_EDITOR", "GIT_AUTHOR_NAME", "GIT_PAGER", "PATH", "HOME"}
	env := make([]string, 0, len(benign))
	for _, k := range benign {
		env = append(env, k+"=keep")
	}
	got := SanitizeGitEnv(env)
	if len(got) != len(benign) {
		t.Errorf("良性变量应全部保留：want %d got %d (%v)", len(benign), len(got), got)
	}
}

// TestSanitizeGitEnvCaseInsensitive —— 大小写不敏感（Windows 语义）。
func TestSanitizeGitEnvCaseInsensitive(t *testing.T) {
	cases := [][]string{
		{"git_dir=/evil"},
		{"Git_Work_Tree=/evil"},
		{"GIT_INDEX_FILE=/evil"},
	}
	for _, env := range cases {
		if got := SanitizeGitEnv(env); len(got) != 0 {
			t.Errorf("%v 应被剥离（大小写不敏感），实得 %v", env, got)
		}
	}
}

// TestSanitizeGitEnvNoOverStrip —— **反证**：前缀相似但不同的变量不得被剥。
func TestSanitizeGitEnvNoOverStrip(t *testing.T) {
	env := []string{"GIT_DIRECTORY=keep", "GIT_DIR_X=keep", "XGIT_DIR=keep"}
	if got := SanitizeGitEnv(env); len(got) != 3 {
		t.Errorf("相似名不应被剥离，实得 %v", got)
	}
}

// ── resolveGitCommand ──

// TestOracleResolveGitCommand —— 探测顺序逐例对账。
//
// **注意**：oracle 的用例 3 有**自己的 deps**（`existsSync: () => false`，
// 验「Windows 但都不存在 → 回退 git」）。JSON 只存了 `env`/`platform`/`out`，
// 故此处按用例名/索引复刻该差异——**不能对所有用例用同一套 exists**。
func TestOracleResolveGitCommand(t *testing.T) {
	o := loadDiffOracle(t)
	if len(o.ResolveGitCommand) == 0 {
		t.Fatal("oracle 无 resolveGitCommand 用例")
	}
	// 与 oracle 相同的 exists 集合。
	existsSet := map[string]bool{
		`C:\Program Files\Git\cmd\git.exe`: true,
		"/custom/git":                      true,
	}
	// 用例 3 的 deps：existsSync 恒 false。
	const noExistsCaseIndex = 3

	for i, c := range o.ResolveGitCommand {
		exists := func(p string) bool { return existsSet[p] }
		if i == noExistsCaseIndex {
			exists = func(string) bool { return false }
		}
		deps := ResolveGitCommandDeps{
			GOOS: c.Platform,
			Env: func(k string) string {
				if v, ok := c.Env[k]; ok {
					return v
				}
				return ""
			},
			Exists: exists,
		}
		got := ResolveGitCommand(deps)
		if got != c.Out {
			t.Errorf("[%d] platform=%s env=%v\nTS=%q\nGo=%q", i, c.Platform, c.Env, c.Out, got)
		}
	}
	t.Logf("resolveGitCommand 对账 %d 例", len(o.ResolveGitCommand))
}

// ── diff 工具 ──

// makeDiffRepoFixture 建出与 oracle 相同的 git 仓库夹具。
//
// **必须与 gen-oracle.ts 的建仓序列一致**（init → config → 初始提交 →
// 制造改动）。`core.autocrlf=false` 是关键——否则 Windows 上 git 会把
// LF 转 CRLF，diff 内容与 oracle 不符。
//
// `gitRun` 复用 `applypatch_rollback_test.go` 的既有 helper（同包）。
func makeDiffRepoFixture(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(filepath.FromSlash(diffFixturesDir), "repo")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "oracle@test")
	gitRun(t, repo, "config", "user.name", "oracle")
	gitRun(t, repo, "config", "core.autocrlf", "false")

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
	write("README.md", "# Test\n")
	write("big.txt", numberedLines(300))
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "init")

	// 制造改动（与 oracle 一致）。
	write("src/a.ts", "export const a = 2\nexport const b = 3\n")
	write("big.txt", numberedLines(400))
	write("src/new.ts", "export const n = 1\n")

	return repo
}

// numberedLines 生成 `line 0\nline 1\n...\nline n-1\n`。
//
// **注意与 oracle 的一致性**：oracle 用 “ `line ${i}` “（**无前导零**），
// 故此处不能用 itoaPad。
func numberedLines(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("line ")
		sb.WriteString(itoaPlain(i))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func itoaPlain(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// diffKnownDeviations 登记 diff 的已知偏离用例。
//
// **`path-escape`**：逃逸错误文案来自 `pathsafe`（前刀产物），其尾句是
// **简化版**——TS 说
//
//	...to grant read access; standing grants can also be configured via
//	permissions.additionalReadDirs / additionalWriteDirs.
//
// 而 Go 说 `...to grant access.`（Go 侧无 `permissions.additional*` 配置
// 机制，故不承诺该路径）。**这是 pathsafe 的既有实现**，不在本刀范围——
// 改它会波及其他工具的既有测试。此处只断言**前缀部分一致**（到
// `to grant` 为止），并登记该偏离。
var diffKnownDeviations = map[string]string{
	"path-escape": "尾句文案差异（pathsafe 简化版，见上方说明）",
}

// diffEscapeCommonPrefix 是两侧共有的文案前缀（`path-escape` 用）。
const diffEscapeCommonPrefix = "错误：Path outside project directory: ../outside (workspace root: "

func TestOracleDiff(t *testing.T) {
	o := loadDiffOracle(t)
	if len(o.Diff) == 0 {
		t.Fatal("oracle 无 diff 用例")
	}
	repo := makeDiffRepoFixture(t)

	for _, c := range o.Diff {
		var input map[string]any
		if len(c.Input) > 0 {
			if err := json.Unmarshal(c.Input, &input); err != nil {
				t.Fatalf("[%s] 解析 input 失败：%v", c.Name, err)
			}
		}
		res, err := Diff().Execute(context.Background(), &CallParams{
			Cwd: repo, Input: input, ToolUseID: "t",
		})
		if err != nil {
			t.Fatalf("[%s] Execute 报错：%v", c.Name, err)
		}
		got := normalizeDiffOutput(res.Content)
		want := normalizeDiffOutput(c.Content)

		if _, deviating := diffKnownDeviations[c.Name]; deviating {
			// 登记偏离：只断言共有前缀（到 workspace root 之后的部分不比对）。
			if !strings.HasPrefix(got, diffEscapeCommonPrefix) {
				t.Errorf("[%s] 应含共有前缀 %q，实得：%q", c.Name, diffEscapeCommonPrefix, got)
			}
			if !strings.HasPrefix(want, diffEscapeCommonPrefix) {
				t.Errorf("[%s] oracle 期望应含同一前缀（前置假设错误）", c.Name)
			}
			if res.IsError != c.IsError {
				t.Errorf("[%s] isError TS=%v Go=%v", c.Name, c.IsError, res.IsError)
			}
			continue
		}

		if got != want {
			t.Errorf("[%s] content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", c.Name, want, got)
		}
		if res.IsError != c.IsError {
			t.Errorf("[%s] isError TS=%v Go=%v", c.Name, c.IsError, res.IsError)
		}
	}
	t.Logf("diff 对账 %d 例（其中 %d 例为登记的文案偏离）", len(o.Diff), len(diffKnownDeviations))
}

// TestOracleDiffOwned —— current_task_only 归属过滤对账。
func TestOracleDiffOwned(t *testing.T) {
	o := loadDiffOracle(t)
	repo := makeDiffRepoFixture(t)

	res, err := Diff().Execute(context.Background(), &CallParams{
		Cwd: repo, Input: map[string]any{"current_task_only": true},
		OwnedFiles: []string{"src/a.ts"}, ToolUseID: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := normalizeDiffOutput(res.Content), normalizeDiffOutput(o.DiffOwned.Content); got != want {
		t.Errorf("diffOwned 不一致\nTS:\n%s\nGo:\n%s", want, got)
	}
}

// TestOracleDiffOwnedOutside —— 归属全在项目外 → 固定文案。
func TestOracleDiffOwnedOutside(t *testing.T) {
	o := loadDiffOracle(t)
	repo := makeDiffRepoFixture(t)
	res, err := Diff().Execute(context.Background(), &CallParams{
		Cwd: repo, Input: map[string]any{"current_task_only": true},
		OwnedFiles: []string{"../../outside.ts"}, ToolUseID: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != o.DiffOwnedOutside.Content {
		t.Errorf("文案不符\nTS: %q\nGo: %q", o.DiffOwnedOutside.Content, res.Content)
	}
}

// TestOracleDiffClean —— 干净仓库 → 「无改动。」
func TestOracleDiffClean(t *testing.T) {
	o := loadDiffOracle(t)
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	res, err := Diff().Execute(context.Background(), &CallParams{Cwd: dir, Input: map[string]any{}, ToolUseID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != o.DiffClean.Content {
		t.Errorf("文案不符\nTS: %q\nGo: %q", o.DiffClean.Content, res.Content)
	}
}

// TestOracleDiffNotGit —— 非 git 仓库 → stderr 非空即报错。
func TestOracleDiffNotGit(t *testing.T) {
	o := loadDiffOracle(t)
	dir := t.TempDir()
	res, err := Diff().Execute(context.Background(), &CallParams{Cwd: dir, Input: map[string]any{}, ToolUseID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("非 git 仓库应报错，实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "Not a git repository") {
		t.Errorf("应含 git 的错误信息：%q", res.Content)
	}
	if res.RawPath == "" {
		t.Error("错误路径也应落盘 rawPath（对账 TS 的 persistRawOutput）")
	}
	_ = o
}

// TestOracleDiffBoundary —— **单文件 200 行阈值的边界对账**。
//
// 这是变异 M107（阈值 200 → 201）的**判别力来源**：没有这条，阈值改动
// 不可观测（`big.txt` 的大 diff 会被 `buildModelOutput` 先截到 116 行，
// 永远到不了 200 行阈值）。
//
// oracle 的 `n=196` 用例产出**恰好 201 行**的 diff 块 → 触发截断
// （`...（已截断，另有 2 行）`）。
func TestOracleDiffBoundary(t *testing.T) {
	o := loadDiffOracle(t)
	if o.DiffBoundary196 == nil {
		t.Fatal("oracle 无 diff_boundary196 用例")
	}

	dir := filepath.Join(filepath.FromSlash(diffFixturesDir), "boundary")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "o@t")
	gitRun(t, dir, "config", "user.name", "o")
	gitRun(t, dir, "config", "core.autocrlf", "false")

	// 初始空文件并提交。
	if err := os.WriteFile(filepath.Join(dir, "f.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "init")

	// 写入 n=196 行 → diff 块恰好 201 行。
	var sb strings.Builder
	for i := 0; i < o.DiffBoundary196.N; i++ {
		sb.WriteString("row ")
		sb.WriteString(itoaPlain(i))
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "f.ts"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Diff().Execute(context.Background(), &CallParams{Cwd: dir, Input: map[string]any{}, ToolUseID: "tb"})
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeDiffOutput(res.Content)
	want := normalizeDiffOutput(o.DiffBoundary196.Content)
	if got != want {
		t.Errorf("边界用例 content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", want, got)
	}
	// 显式断言截断提示存在（防两侧都「无截断」而假绿）。
	if !strings.Contains(got, "已截断，另有 2 行") {
		t.Errorf("应触发单文件截断提示，实得：%q", got)
	}
	t.Logf("边界对账：n=%d，块 201 行触发截断", o.DiffBoundary196.N)
}

// ── 纯函数（经行为覆盖）──

// TestSplitByFile —— 按 `diff --git` 边界切分。
func TestSplitByFile(t *testing.T) {
	input := strings.Join([]string{
		"diff --git a/x.ts b/x.ts",
		"--- a/x.ts",
		"+++ b/x.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/y.ts b/y.ts",
		"--- a/y.ts",
		"+++ b/y.ts",
	}, "\n")
	got := SplitByFile(input)
	if len(got) != 2 {
		t.Fatalf("应切成 2 块，实得 %d：%#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "diff --git a/x.ts") {
		t.Errorf("块 0 应以 diff --git 开头：%q", got[0])
	}
	if !strings.HasPrefix(got[1], "diff --git a/y.ts") {
		t.Errorf("块 1 应以 diff --git 开头：%q", got[1])
	}
}

// TestSplitByFileNoBoundary —— 无 `diff --git` 的输入整体成一块。
func TestSplitByFileNoBoundary(t *testing.T) {
	got := SplitByFile("just some text\nmore text")
	if len(got) != 1 {
		t.Fatalf("应成 1 块，实得 %d", len(got))
	}
}

// TestSplitByFileEmpty —— 空输入 → 空切片。
func TestSplitByFileEmpty(t *testing.T) {
	if got := SplitByFile(""); len(got) != 0 {
		t.Errorf("空输入应返回空，实得 %#v", got)
	}
}

// TestTruncateDiffPerFile —— 单文件超 200 行 → 留前 200 行 + 截断提示。
//
// **行数语义**：`MAX_LINES_PER_FILE` 作用在**文件块的全部行**上（含
// `diff --git` 头行），故 251 行的块（1 头 + 250 内容）截断后留 **200 行
// = 头行 + 199 内容行**，提示「另有 51 行」。
//
// 首版测试假设「200 是纯内容行」——**错的**，由实际输出
// `+line 198\n...（已截断，另有 51 行）` 暴露。
func TestTruncateDiffPerFile(t *testing.T) {
	var lines []string
	lines = append(lines, "diff --git a/big.ts b/big.ts")
	for i := 0; i < 250; i++ {
		lines = append(lines, "+line "+itoaPlain(i))
	}
	got := TruncateDiff(strings.Join(lines, "\n"))
	// 251 行 - 200 = 51
	if !strings.Contains(got, "...（已截断，另有 51 行）") {
		t.Errorf("应含截断提示（251-200=51）：%q", got[max(0, len(got)-80):])
	}
	// 前 200 行（头行 + 199 内容行）应完整保留。
	if !strings.Contains(got, "diff --git a/big.ts") {
		t.Error("头行应保留")
	}
	if !strings.Contains(got, "+line 0") || !strings.Contains(got, "+line 198") {
		t.Error("前 199 内容行应保留")
	}
	if strings.Contains(got, "+line 199\n") {
		t.Error("第 200 内容行应被截掉（200 行含头行）")
	}
}

// TestTruncateDiffShortUnchanged —— **反证**：短输入原样返回。
func TestTruncateDiffShortUnchanged(t *testing.T) {
	input := "diff --git a/x.ts b/x.ts\n--- a/x.ts\n+++ b/x.ts\n@@ -1 +1 @@\n-old\n+new"
	if got := TruncateDiff(input); got != input {
		t.Errorf("短输入不应改动\nwant: %q\ngot:  %q", input, got)
	}
}

// TestTruncateDiffTotalChars —— 总长超 8000 字符 → 截断。
func TestTruncateDiffTotalChars(t *testing.T) {
	var files []string
	for f := 0; f < 10; f++ {
		var lines []string
		lines = append(lines, "diff --git a/f"+itoaPlain(f)+".ts b/f"+itoaPlain(f)+".ts")
		for i := 0; i < 150; i++ { // 每文件 150 行（不触发单文件截断）
			lines = append(lines, "+padding line content here "+itoaPlain(i))
		}
		files = append(files, strings.Join(lines, "\n"))
	}
	input := strings.Join(files, "\n")
	if len(input) <= diffMaxTotalChars {
		t.Fatalf("前置假设错误：输入应超 %d 字符，实得 %d", diffMaxTotalChars, len(input))
	}
	got := TruncateDiff(input)
	if !strings.HasSuffix(got, "\n...（已截断）") {
		t.Errorf("应含总长截断提示，尾部：%q", got[max(0, len(got)-40):])
	}
	if len(got) > diffMaxTotalChars+len("\n...（已截断）") {
		t.Errorf("截断后长度应接近上限，实得 %d", len(got))
	}
}

// TestDiffSchemaAndRegistration —— schema 与注册。
func TestDiffSchemaAndRegistration(t *testing.T) {
	def := Diff().Definition()
	if def.Name != "diff" {
		t.Errorf("工具名应为 diff，实得 %q", def.Name)
	}
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get("diff"); !ok {
		t.Fatal("diff 未注册进默认注册表")
	}
}

// TestSpawnGitSanitizesEnv —— **接线验证**：SpawnGit 真的用了消毒后的环境。
//
// 造一个 `GIT_DIR` 污染的环境，断言 SpawnGit 构造的 cmd.Env 里没有它。
func TestSpawnGitSanitizesEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/evil")
	cmd := SpawnGit([]string{"status"}, t.TempDir())
	for _, kv := range cmd.Env {
		if strings.HasPrefix(strings.ToUpper(kv), "GIT_DIR=") {
			t.Errorf("GIT_DIR 应被剥离，实得 %q", kv)
		}
	}
	if cmd.Dir == "" {
		t.Error("Dir 应被设置")
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
