package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commandfilter_test.go —— 对账 TS `applyCommandFilter`（六族过滤器）。
//
// oracle 由 `testdata/commandfilter/gen_oracle.ts` **真跑 TS 原实现**产出
// （纯函数，无外部依赖）。生成命令：
//
//	node_modules/.bin/tsx go/testdata/commandfilter/gen_oracle.ts > go/testdata/commandfilter/oracle.json

type cfCase struct {
	Name  string `json:"name"`
	Input struct {
		Command  string `json:"command"`
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exitCode"`
	} `json:"input"`
	// Result 是 TS 的返回：null（不过滤）或字符串。
	Result *string `json:"result"`
}

func loadCFOracle(t *testing.T) []cfCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "commandfilter", "oracle.json"))
	if err != nil {
		t.Skipf("oracle 缺失（需先跑 gen_oracle.ts）：%v", err)
	}
	var cases []cfCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatalf("oracle 解析失败：%v", err)
	}
	return cases
}

// TestApplyCommandFilterOracle —— 逐字节对账 TS 的 21 例。
func TestApplyCommandFilterOracle(t *testing.T) {
	cases := loadCFOracle(t)
	if len(cases) == 0 {
		t.Fatal("oracle 无用例")
	}
	nonNil := 0
	for _, c := range cases {
		got, ok := ApplyCommandFilter(c.Input.Command, c.Input.Stdout, c.Input.ExitCode)
		if c.Result == nil {
			// TS 返回 null → Go 应 ok=false。
			if ok {
				t.Errorf("%s：TS 返回 null（不过滤），Go 却命中并返回：\n%s", c.Name, got)
			}
			continue
		}
		nonNil++
		if !ok {
			t.Errorf("%s：TS 返回了过滤结果（%d 字符），Go 却未命中", c.Name, len(*c.Result))
			continue
		}
		if got != *c.Result {
			t.Errorf("%s 不符\n--- got ---\n%s\n--- want ---\n%s", c.Name, got, *c.Result)
		}
	}
	t.Logf("对账了 %d 例（其中 %d 例有过滤结果）", len(cases), nonNil)
}

// TestCommandFilterPipeNeverFiltered —— 含管道的命令一律不过滤。
//
// 对账 TS 的纪律：管道会洗白 exit code（`tsc --noEmit | head` 的 exit 是 head 的 0），
// 故原始输出比错误摘要安全。
func TestCommandFilterPipeNeverFiltered(t *testing.T) {
	tscFail := "src/a.ts(3,5): error TS2322: bad\n\nFound 1 error in 1 file.\n"
	for _, cmd := range []string{
		"tsc --noEmit | head",
		"git status | cat",
		"npm test | tail -5",
		"git diff | less",
	} {
		if _, ok := ApplyCommandFilter(cmd, tscFail, 0); ok {
			t.Errorf("含管道的命令不应过滤：%q", cmd)
		}
	}
}

// TestCommandFilterContentBeatsExitCode —— **内容优先于 exit code**。
//
// exit 0 但输出含失败签名（`error TS`）→ 按失败处理。
// 防的是 incident 2026-07-19：`tsc --noEmit | head` 的 exit 被洗成 0，
// filterTsc 输出 "✓ typecheck passed" 吞掉真实错误。
func TestCommandFilterContentBeatsExitCode(t *testing.T) {
	tscFail := "src/a.ts(3,5): error TS2322: bad\n\nFound 1 error in 1 file.\n"
	got, ok := ApplyCommandFilter("tsc --noEmit", tscFail, 0) // exit=0 但含 error TS
	if !ok {
		t.Fatal("应命中 tsc 族")
	}
	if strings.Contains(got, "typecheck passed") {
		t.Errorf("exit=0 但含失败签名时应按失败处理，实得：%q", got)
	}
	if !strings.Contains(got, "error TS2322") {
		t.Errorf("应保留 error TS 行：%q", got)
	}
}

// TestCommandFilterZeroFailedNotMisjudged —— `"0 failed"` **不**误判为失败签名。
//
// 对账 TS 的 `FAILURE_SIGNATURE_RE` 用 `[1-9]\d*`（要求显式非零计数）。
func TestCommandFilterZeroFailedNotMisjudged(t *testing.T) {
	if hasFailureSignature("Tests: 5 passed, 0 failed") {
		t.Error("`0 failed` 不应被判为失败签名")
	}
	if hasFailureSignature("ℹ fail 0") {
		t.Error("`fail 0` 不应被判为失败签名")
	}
	// 正例：非零失败计数应命中。
	if !hasFailureSignature("Tests: 1 failed") {
		t.Error("`1 failed` 应判为失败签名")
	}
	if !hasFailureSignature("ℹ fail 2") {
		t.Error("`fail 2` 应判为失败签名")
	}
}

// TestCommandFilterSmallOutputReturnsNil —— 小输出不过滤（无收益零风险）。
func TestCommandFilterSmallOutputReturnsNil(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		out  string
	}{
		{"git log ≤30 行", "git log", strings.Repeat("commit abc\n", 10)},
		{"git diff ≤40 行", "git diff", strings.Repeat("+line\n", 10)},
		{"test run ≤15 行", "npm test", "PASS a\nTests: 1 passed\n"},
	}
	for _, c := range cases {
		if _, ok := ApplyCommandFilter(c.cmd, c.out, 0); ok {
			t.Errorf("%s 应返回 nil（不过滤）", c.name)
		}
	}
}

// TestCommandFilterUnmatchedReturnsNil —— 无匹配族 → nil。
func TestCommandFilterUnmatchedReturnsNil(t *testing.T) {
	for _, cmd := range []string{"echo hi", "ls -la", "cat file.txt", "curl http://x"} {
		if _, ok := ApplyCommandFilter(cmd, strings.Repeat("output\n", 50), 0); ok {
			t.Errorf("未匹配族的命令不应过滤：%q", cmd)
		}
	}
}

// TestFilterTscKeepsFullDiagnosticLine —— 保留**完整**诊断行（含 file:line:col）。
//
// 对账 TS 注释：位置信息是修复的第一素材，剥掉前缀的"美化"曾让 agent
// 拿着错误找不到现场。
func TestFilterTscKeepsFullDiagnosticLine(t *testing.T) {
	out := "src/a.ts(3,5): error TS2322: Type 'string' is not assignable to type 'number'.\n  noise\n\nFound 1 error in 1 file.\n"
	got, _ := ApplyCommandFilter("tsc --noEmit", out, 2)
	if !strings.Contains(got, "src/a.ts(3,5)") {
		t.Errorf("应保留完整位置前缀：%q", got)
	}
	if !strings.Contains(got, "Found 1 error") {
		t.Errorf("应保留汇总 footer：%q", got)
	}
	if strings.Contains(got, "noise") {
		t.Errorf("应丢弃噪声行：%q", got)
	}
}

// TestFilterGitStatusDropsHints —— git status 剔除 hint 行。
//
// **只剔行首**的 hint（对账 TS 的 `^\(use\s+"git\s` 与 `^\(git\s`——两个正则
// 都以 `^` 锚定）。行中出现的 `(use ...)` 是**正文的一部分**，应保留：
// `no changes added to commit (use "git add" ...)` 是 git 的状态陈述，不是提示。
func TestFilterGitStatusDropsHints(t *testing.T) {
	out := "On branch main\n" +
		"  modified: a.ts\n" +
		"no changes added to commit (use \"git add\" and/or \"git commit -a\")\n" + // 行中：保留
		"(use \"git restore <file>...\" to discard changes)\n" + // 行首：剔除
		"(git add <file>... to include in what will be committed)\n" // 行首：剔除
	got, ok := ApplyCommandFilter("git status", out, 0)
	if !ok {
		t.Fatal("应命中 git status 族")
	}
	// 行首 hint 应被剔除。
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "(use ") || strings.HasPrefix(line, "(git ") {
			t.Errorf("行首 hint 应被剔除：%q", line)
		}
	}
	// 正文行（含行中 `(use`）应保留。
	if !strings.Contains(got, "modified: a.ts") {
		t.Errorf("应保留实际变更行：%q", got)
	}
	if !strings.Contains(got, "no changes added to commit") {
		t.Errorf("行中 `(use` 属正文，应保留：%q", got)
	}
}

// TestFilterGitDiffCountsChanges —— 压缩后附 `+A -R` 计数。
func TestFilterGitDiffCountsChanges(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/x.ts b/x.ts\n")
	b.WriteString("index abc..def 100644\n")
	b.WriteString("--- a/x.ts\n+++ b/x.ts\n")
	b.WriteString("@@ -1,60 +1,60 @@\n")
	for i := 0; i < 60; i++ {
		b.WriteString("-old\n")
	}
	for i := 0; i < 60; i++ {
		b.WriteString("+new\n")
	}
	got, ok := ApplyCommandFilter("git diff", b.String(), 0)
	if !ok {
		t.Fatal("应命中 git diff 族")
	}
	if !strings.Contains(got, "+60 -60") {
		t.Errorf("应附 +A -R 计数：%q", got)
	}
	if strings.Contains(got, "index abc..def") {
		t.Errorf("应剥掉 index 行：%q", got)
	}
}
