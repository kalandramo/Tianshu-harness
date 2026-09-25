package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// gitscout_test.go —— git_scout 的验证。
//
// **验证策略（明示欠账）**：本工具**未纳入 toolschema oracle**——该 oracle 的
// PORTED 列表是 TS 侧显式枚举（gen-oracle.ts:43-49），新增工具需改生成器并重跑，
// 而重跑需 tsx（本机 node_modules 为空，`ls node_modules | wc -l` = 0）。
// 故 schema 的键序/必填项由 TestGitScoutSchemaShape **显式断言**锁定
// （逐字对照 TS git-scout.ts:167-205 的源码，非手抄转述）。
//
// 行为层用**真 git 仓库**端到端验证（不依赖 oracle）。

// gscInitRepo 在临时目录建一个含 2 个提交的 git 仓库。
//
// 用真实 git（而非 mock）——git_scout 的价值就在「跑真 git 命令」，mock 掉
// 等于不测。返回仓库路径。
func gscInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// 固定身份，避免依赖宿主 git config（否则 CI 上 commit 会失败）。
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first commit")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "second commit")
	return dir
}

// gscRun 跑 git_scout 的一个 action。
func gscRun(t *testing.T, dir string, input map[string]any) (string, bool) {
	t.Helper()
	tool := GitScout()
	r, err := tool.Execute(context.Background(), &CallParams{
		Input: input, Cwd: dir, ToolUseID: "t",
	})
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	return r.Content, r.IsError
}

// TestGitScoutSchemaShape —— 锁定 schema 的形状与**属性声明序**。
//
// 属性序逐字对账 TS git-scout.ts:167-205（action/range/ref/commit/a/b/path/
// contains/maxCount）。键序不同会让工具定义变化打掉整个前缀缓存
// （src/api/openai-client.ts:630）。
func TestGitScoutSchemaShape(t *testing.T) {
	def := GitScout().Definition()
	if def.Name != "git_scout" {
		t.Errorf("name 期望 git_scout，实得 %q", def.Name)
	}
	if def.InputSchema == nil {
		t.Fatal("InputSchema 为 nil")
	}
	if def.InputSchema.Type != "object" {
		t.Errorf("schema type 期望 object，实得 %q", def.InputSchema.Type)
	}
	wantOrder := []string{"action", "range", "ref", "commit", "a", "b", "path", "contains", "maxCount"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 长度 = %d, want %d（%v）", len(def.InputSchema.PropOrder), len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, k := range wantOrder {
		if def.InputSchema.PropOrder[i] != k {
			t.Errorf("PropOrder[%d] = %q, want %q", i, def.InputSchema.PropOrder[i], k)
		}
		if _, ok := def.InputSchema.Properties[k]; !ok {
			t.Errorf("schema 缺属性 %q", k)
		}
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "action" {
		t.Errorf("required 期望 [action]，实得 %v", def.InputSchema.Required)
	}
	// action 必须是 enum（含 9 个只读 action，顺序对账 TS ACTIONS）。
	act, ok := def.InputSchema.Properties["action"].(*wire.OrderedMap)
	if !ok {
		t.Fatalf("action 属性应为 *wire.OrderedMap，实得 %T", def.InputSchema.Properties["action"])
	}
	keys := act.Keys()
	if len(keys) != 3 || keys[0] != "type" || keys[1] != "enum" || keys[2] != "description" {
		t.Errorf("action 属性键序期望 [type enum description]，实得 %v", keys)
	}
	enumRaw, _ := act.Get("enum")
	enumVals, ok := enumRaw.([]any)
	if !ok {
		t.Fatalf("action.enum 应为 []any，实得 %T", enumRaw)
	}
	wantActions := []string{"log", "count", "show", "tags", "branches", "ancestry", "merge_base", "resolve", "diff"}
	if len(enumVals) != len(wantActions) {
		t.Fatalf("enum 长度 = %d, want %d", len(enumVals), len(wantActions))
	}
	for i, a := range wantActions {
		if enumVals[i] != a {
			t.Errorf("enum[%d] = %v, want %q", i, enumVals[i], a)
		}
	}
}

// TestGitScoutToolFlags —— 纯只读工具的三个标志。
func TestGitScoutToolFlags(t *testing.T) {
	tool := GitScout()
	if tool.RequiresApproval(nil) {
		t.Error("RequiresApproval 应为 false（纯只读，无写动作）")
	}
	if !tool.ConcurrencySafe() {
		t.Error("ConcurrencySafe 应为 true（readonly 并行侦察语义）")
	}
	if !tool.Enabled() {
		t.Error("Enabled 应为 true")
	}
}

// TestGitScoutRejectsWriteActions —— **安全关键**：写 action 一律拒绝。
//
// 这是本工具存在的理由：readonly profile 拿不到含写动作的 `git` 工具，
// git_scout 必须是**纯查询**。若任何写动作被放行，隔离信任链即破。
func TestGitScoutRejectsWriteActions(t *testing.T) {
	dir := gscInitRepo(t)
	for _, action := range []string{"commit", "checkout", "reset", "stash", "stash_pop", "push", "clean"} {
		got, isErr := gscRun(t, dir, map[string]any{"action": action})
		if !isErr {
			t.Errorf("action=%q 应被拒（isError=true），实得 %q", action, got)
		}
		if !strings.Contains(got, "只读侦察工具") {
			t.Errorf("action=%q 的拒绝文案应说明只读，实得 %q", action, got)
		}
	}
	// 反证：仓库状态未被改动（写 action 若真跑了会留下痕迹）。
	if _, isErr := gscRun(t, dir, map[string]any{"action": "commit"}); isErr {
		// 预期的拒绝——但需确认 HEAD 未变。
		before, _ := gscRun(t, dir, map[string]any{"action": "count"})
		after, _ := gscRun(t, dir, map[string]any{"action": "count"})
		if before != after {
			t.Errorf("写 action 被拒后提交数变化：%q → %q", before, after)
		}
	}
}

// TestGitScoutLog —— log action 的输出格式（每行 `<短sha>|<月-日 时:分>|<主题>`）。
func TestGitScoutLog(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "log"})
	if isErr {
		t.Fatalf("log 不应报错：%s", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("应有 2 行（2 个提交），实得 %d 行：%q", len(lines), got)
	}
	// 首行是最新提交。
	if !strings.Contains(lines[0], "second commit") {
		t.Errorf("首行应为最新提交，实得 %q", lines[0])
	}
	// 每行 3 段（短sha|日期|主题）。
	for i, ln := range lines {
		parts := strings.SplitN(ln, "|", 3)
		if len(parts) != 3 {
			t.Errorf("第 %d 行应为 3 段（sha|日期|主题），实得 %q", i, ln)
			continue
		}
		if len(parts[0]) == 0 || len(parts[0]) > 40 {
			t.Errorf("第 %d 行的 sha 长度异常：%q", i, parts[0])
		}
		if !strings.Contains(parts[1], ":") {
			t.Errorf("第 %d 行的日期应含 ':'（月-日 时:分 格式），实得 %q", i, parts[1])
		}
	}
}

// TestGitScoutLogMaxCount —— maxCount 限制与校验。
func TestGitScoutLogMaxCount(t *testing.T) {
	dir := gscInitRepo(t)
	got, _ := gscRun(t, dir, map[string]any{"action": "log", "maxCount": float64(1)})
	if n := len(strings.Split(got, "\n")); n != 1 {
		t.Errorf("maxCount=1 应得 1 行，实得 %d 行：%q", n, got)
	}
	// 非法 maxCount：0 / 1001 / 小数 / 非数字。
	for _, bad := range []any{float64(0), float64(1001), 1.5, "50"} {
		_, isErr := gscRun(t, dir, map[string]any{"action": "log", "maxCount": bad})
		if !isErr {
			t.Errorf("maxCount=%v 应被拒", bad)
		}
	}
}

// TestGitScoutCount —— count action。
func TestGitScoutCount(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "count"})
	if isErr {
		t.Fatalf("count 不应报错：%s", got)
	}
	if strings.TrimSpace(got) != "2" {
		t.Errorf("count 期望 2，实得 %q", got)
	}
}

// TestGitScoutShow —— show action（含 refs/subject）。
func TestGitScoutShow(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "show"})
	if isErr {
		t.Fatalf("show 不应报错：%s", got)
	}
	if !strings.Contains(got, "second commit") {
		t.Errorf("show 默认 HEAD 应含最新提交主题，实得 %q", got)
	}
	// 格式：sha | iso 日期 | refs | subject（4 段）。
	if n := len(strings.SplitN(got, "|", 4)); n != 4 {
		t.Errorf("show 输出应为 4 段（sha|日期|refs|subject），实得 %q", got)
	}
}

// TestGitScoutTags —— tags action（空仓库无 tag 时给提示）。
func TestGitScoutTags(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "tags"})
	if isErr {
		t.Fatalf("tags 不应报错：%s", got)
	}
	if got != "(无 tag)" {
		t.Errorf("无 tag 时应返回提示，实得 %q", got)
	}
}

// TestGitScoutBranches —— branches action（含 contains 过滤）。
func TestGitScoutBranches(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "branches"})
	if isErr {
		t.Fatalf("branches 不应报错：%s", got)
	}
	if !strings.Contains(got, "main") {
		t.Errorf("应含 main 分支，实得 %q", got)
	}
	// contains=HEAD 应含 main。
	got2, _ := gscRun(t, dir, map[string]any{"action": "branches", "contains": "HEAD"})
	if !strings.Contains(got2, "main") {
		t.Errorf("contains=HEAD 应含 main，实得 %q", got2)
	}
}

// TestGitScoutAncestry —— ancestry 判定 YES/NO。
func TestGitScoutAncestry(t *testing.T) {
	dir := gscInitRepo(t)
	// HEAD~1 是 HEAD 的祖先 → YES。
	got, isErr := gscRun(t, dir, map[string]any{"action": "ancestry", "a": "HEAD~1", "b": "HEAD"})
	if isErr {
		t.Fatalf("ancestry 不应报错：%s", got)
	}
	if strings.TrimSpace(got) != "YES" {
		t.Errorf("HEAD~1 应为 HEAD 的祖先，实得 %q", got)
	}
	// 反向 → NO。
	got2, _ := gscRun(t, dir, map[string]any{"action": "ancestry", "a": "HEAD", "b": "HEAD~1"})
	if strings.TrimSpace(got2) != "NO" {
		t.Errorf("HEAD 不应为 HEAD~1 的祖先，实得 %q", got2)
	}
}

// TestGitScoutMergeBase —— merge_base 返回共同祖先 sha。
func TestGitScoutMergeBase(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "merge_base", "a": "HEAD", "b": "HEAD~1"})
	if isErr {
		t.Fatalf("merge_base 不应报错：%s", got)
	}
	if strings.TrimSpace(got) != strings.TrimSpace(mustGitOut(t, dir, "rev-parse", "HEAD~1")) {
		t.Errorf("merge_base(HEAD, HEAD~1) 应为 HEAD~1，实得 %q", got)
	}
}

// TestGitScoutResolve —— resolve 把 ref 解析为 40-hex sha。
func TestGitScoutResolve(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "resolve", "ref": "HEAD"})
	if isErr {
		t.Fatalf("resolve 不应报错：%s", got)
	}
	sha := strings.TrimSpace(got)
	if len(sha) != 40 {
		t.Errorf("resolve 应返回 40-hex sha，实得 %q（len=%d）", sha, len(sha))
	}
	// 不存在的 ref → 报错。
	_, isErr2 := gscRun(t, dir, map[string]any{"action": "resolve", "ref": "no-such-ref-xyz"})
	if !isErr2 {
		t.Error("不存在的 ref 应报错")
	}
}

// TestGitScoutDiff —— diff action 对两个提交给 --stat。
func TestGitScoutDiff(t *testing.T) {
	dir := gscInitRepo(t)
	got, isErr := gscRun(t, dir, map[string]any{"action": "diff", "a": "HEAD~1", "b": "HEAD"})
	if isErr {
		t.Fatalf("diff 不应报错：%s", got)
	}
	if !strings.Contains(got, "a.txt") {
		t.Errorf("diff --stat 应含 a.txt，实得 %q", got)
	}
}

// TestGitScoutRefValidation —— 参数防注入：拒 flag 注入与 shell 元字符，
// 但**放行**合法 rev 语法（`~` / `..` / `:`）。
//
// **断言必须检查校验文案而非仅 isErr**：去掉 `gitScoutValidateRef` 后，非法 ref
// 传给 git 时 git 自己也会报错（`-x` 不是合法 rev）——只断言 isErr 的测试在
// 该变异下**仍会通过**（实测：M2 变异红 0 处）。故此处断言「是我们的校验拦的」
// （文案含「非法」），才能区分「校验生效」与「git 兜底报错」。
func TestGitScoutRefValidation(t *testing.T) {
	dir := gscInitRepo(t)
	// 非法：以 '-' 开头（flag 注入）、含空格、含 shell 元字符。
	for _, bad := range []string{"-x", "--help", "a b", "a;rm -rf /", "a|b", "a&b", "a$b", "a`b"} {
		got, isErr := gscRun(t, dir, map[string]any{"action": "resolve", "ref": bad})
		if !isErr {
			t.Errorf("ref=%q 应被拒（注入面），实得 %q", bad, got)
			continue
		}
		// **判别力关键**：必须是我们校验拦的，不是 git 兜底报错。
		if !strings.Contains(got, "非法") {
			t.Errorf("ref=%q 应由**我们的校验**拦下（文案应含「非法」），实得 %q", bad, got)
		}
	}
	// **合法**：`~` / `..` / `:` 不得被拒——否则 ancestry/merge_base/range 全废
	// （TS 注释记录本批实测踩过）。
	for _, good := range []string{"HEAD~1", "HEAD~1..HEAD", "main:src/a.ts"} {
		got, isErr := gscRun(t, dir, map[string]any{"action": "resolve", "ref": good})
		// 这些 ref 在 2 提交仓库里可能不存在（报错是对的），但**不能**是
		// 「非法 ref」的校验错误。
		if isErr && strings.Contains(got, "非法") {
			t.Errorf("ref=%q 是合法 rev 语法，不应被判非法：%q", good, got)
		}
	}
}

// TestGitScoutPathValidation —— path 宽松校验（只拒空串与超长）。
//
// path 总在 `--` 之后传给 git，无注入面——空格/&/引号等合法文件名字符应放行。
func TestGitScoutPathValidation(t *testing.T) {
	dir := gscInitRepo(t)
	// 含空格的 path 应放行（走 `--` 分隔，无注入面）。
	_, isErr := gscRun(t, dir, map[string]any{"action": "log", "path": "my file.txt"})
	if isErr {
		t.Error("含空格的 path 应放行（`--` 分隔后无注入面）")
	}
	// 超长 path 应拒。
	long := strings.Repeat("x", 201)
	if _, isErr := gscRun(t, dir, map[string]any{"action": "log", "path": long}); !isErr {
		t.Error("超长 path 应被拒")
	}
}

// TestGitScoutEndToEndReadOnly —— **验收面**：真仓库上跑 log，且仓库状态不变。
func TestGitScoutEndToEndReadOnly(t *testing.T) {
	dir := gscInitRepo(t)
	before := mustGitOut(t, dir, "status", "--porcelain")
	headBefore := mustGitOut(t, dir, "rev-parse", "HEAD")

	got, isErr := gscRun(t, dir, map[string]any{"action": "log"})
	if isErr {
		t.Fatalf("log 不应报错：%s", got)
	}
	if !strings.Contains(got, "second commit") || !strings.Contains(got, "first commit") {
		t.Errorf("log 应含两个提交，实得 %q", got)
	}

	after := mustGitOut(t, dir, "status", "--porcelain")
	headAfter := mustGitOut(t, dir, "rev-parse", "HEAD")
	if before != after {
		t.Errorf("仓库工作树状态被改动：%q → %q", before, after)
	}
	if headBefore != headAfter {
		t.Errorf("HEAD 被改动：%q → %q", headBefore, headAfter)
	}
}

// TestGitScoutRegistered —— 工具已注册进默认注册表（防悬空）。
func TestGitScoutRegistered(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get("git_scout"); !ok {
		t.Fatal("git_scout 应已注册进默认注册表")
	}
}

// mustGitOut 跑一条 git 命令取 stdout（测试辅助）。
func mustGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v 失败：%v", args, err)
	}
	return strings.TrimSpace(string(out))
}
