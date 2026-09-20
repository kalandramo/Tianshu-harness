package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testSpawnOracle 是 TS 侧真实 resolveTestSpawn 的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/testspawn/gen-oracle.ts
type testSpawnOracle map[string]struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Shell   bool     `json:"shell"`
}

func loadTestSpawnOracle(t *testing.T) testSpawnOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "testspawn", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/testspawn/gen-oracle.ts",
			path, err)
	}
	var o testSpawnOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// 用例矩阵（与 gen-oracle.ts 的 cases 逐条对应）。
const (
	spawnCWD  = `C:\proj`
	spawnShim = `C:\proj\node_modules\.bin\tsx.cmd`
)

func spawnCaseDeps(name string) TestSpawnDeps {
	switch name {
	case "nix_npm", "nix_npx", "nix_tsx", "nix_node", "nix_pytest":
		return TestSpawnDeps{IsWindows: false, Exists: func(string) bool { return true }}
	case "win_tsx_local_shim":
		return TestSpawnDeps{IsWindows: true, Exists: func(p string) bool { return p == spawnShim }}
	default:
		return TestSpawnDeps{IsWindows: true, Exists: func(string) bool { return false }}
	}
}

func spawnCaseInput(name string) (string, []string, string) {
	switch name {
	case "nix_npm":
		return "npm", []string{"test"}, "/proj"
	case "nix_npx":
		return "npx", []string{"vitest", "run"}, "/proj"
	case "nix_tsx":
		return "tsx", []string{"--test", "a.test.ts"}, "/proj"
	case "nix_node":
		return "node", []string{"--test", "a.test.ts"}, "/proj"
	case "nix_pytest":
		return "pytest", []string{"-q"}, "/proj"
	case "win_npm":
		return "npm", []string{"test"}, spawnCWD
	case "win_npx":
		return "npx", []string{"vitest", "run"}, spawnCWD
	case "win_tsx_local_shim", "win_tsx_fallback_npx":
		return "tsx", []string{"--test", "a.test.ts"}, spawnCWD
	case "win_node":
		return "node", []string{"--test", "a.test.ts"}, spawnCWD
	case "win_pytest":
		return "pytest", []string{"-q"}, spawnCWD
	case "win_quote_spaces":
		return "npx", []string{"vitest", "run", "tests/My Feature.test.ts"}, spawnCWD
	case "win_no_double_quote":
		return "npx", []string{`"already quoted"`}, spawnCWD
	case "win_pct_expand_filter":
		return "npm", []string{"test", "--", "foo%PATH%"}, spawnCWD
	case "win_pct_userprofile":
		return "npx", []string{"foo%USERPROFILE%"}, spawnCWD
	case "win_amp_injection":
		return "npx", []string{"a&calc&b"}, spawnCWD
	case "win_quote_break":
		return "npx", []string{`foo"&calc&"bar`}, spawnCWD
	case "win_caret_meta":
		return "npx", []string{"a^b"}, spawnCWD
	case "win_pipe_meta":
		return "npx", []string{"a|b"}, spawnCWD
	case "win_lt_gt_meta":
		return "npx", []string{"a<b>c"}, spawnCWD
	case "win_paren_meta":
		return "npx", []string{"a(b)c"}, spawnCWD
	case "win_quoted_with_pct":
		return "npx", []string{`"%PATH%"`}, spawnCWD
	case "win_quoted_clean":
		return "npx", []string{`"clean token"`}, spawnCWD
	}
	return "", nil, ""
}

// TestResolveTestSpawnParity —— 与 TS 逐用例等价（含注入面）。
func TestResolveTestSpawnParity(t *testing.T) {
	o := loadTestSpawnOracle(t)
	if len(o) == 0 {
		t.Fatal("oracle 为空")
	}
	for name, want := range o {
		cmd, args, cwd := spawnCaseInput(name)
		if cmd == "" {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应输入——补上再断言", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := ResolveTestSpawn(cmd, args, cwd, spawnCaseDeps(name))
			if got.Command != want.Command {
				t.Errorf("command 不等价：Go=%q TS=%q", got.Command, want.Command)
			}
			if got.Shell != want.Shell {
				t.Errorf("shell 不等价：Go=%v TS=%v", got.Shell, want.Shell)
			}
			if !equalStrSlice(got.Args, want.Args) {
				t.Errorf("args 不等价：\n  Go=%q\n  TS=%q", got.Args, want.Args)
			}
		})
	}
}

// TestQuoteCmdArgBlocksInjection —— **安全回归**：危险字符必须被消毒。
//
// 这不是"复刻 TS"的装饰性断言——是实测过的真实注入面。探针（本机 Windows，
// `exec.Command(someCmdShim, args...)`）显示：
//
//	"a&b"（无空格）  → & 被解释执行（注入）
//	"a|b"（无空格）  → 命令被拆分（注入）
//	"x%PATH%"       → 变量展开（信息泄露）
//	"a^b"           → ^ 被吞（数据损坏）
//
// Go 的自动引号只在**含空白**时触发，故不含空白的元字符全部裸露——本测试
// 锁定消毒后的形态。
func TestQuoteCmdArgBlocksInjection(t *testing.T) {
	cases := []struct{ in, want string }{
		// % 必须消毒（引号挡不住变量展开——实测确认）。
		{"foo%PATH%", "foo_PATH_"},
		{"%USERPROFILE%", "_USERPROFILE_"},
		// 双引号必须消毒（会断引、破坏包裹）。
		{`foo"bar`, "foo_bar"},
		// 含元字符 → 整体加引号。
		{"a&calc&b", `"a&calc&b"`},
		{"a|b", `"a|b"`},
		{"a<b>c", `"a<b>c"`},
		{"a(b)c", `"a(b)c"`},
		{"a^b", `"a^b"`},
		// 含空白 → 加引号（Go 也会自动加，此处保持一致形态）。
		{"My Feature.test.ts", `"My Feature.test.ts"`},
		// 已引号包裹且内部干净 → 不双重加引。
		{`"already quoted"`, `"already quoted"`},
		// 干净 token → 原样（不加无谓引号）。
		{"vitest", "vitest"},
		{"--test", "--test"},
		{"a.test.ts", "a.test.ts"},
		{`C:\path\to\x`, `C:\path\to\x`},
		// 引号内仍有 % → 需消毒后重新包裹（不能原样放行）。
		// 注意：`"` 本身也属危险字符，一并替换为 `_`——故结果无外层引号
		// （消毒后 `__PATH__` 已落在 CMD_SAFE 集合内，无需再加）。
		// 该期望值取自 TS oracle（win_quoted_with_pct），非自行推断。
		{`"%PATH%"`, `__PATH__`},
		// 已引号包裹且内部干净 → 原样（对账 win_quoted_clean）。
		{`"clean token"`, `"clean token"`},
	}
	for _, c := range cases {
		if got := quoteCmdArg(c.in); got != c.want {
			t.Errorf("quoteCmdArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveTestSpawnDoesNotMutateInput —— 不得改动调用方的 args 切片。
func TestResolveTestSpawnDoesNotMutateInput(t *testing.T) {
	args := []string{"test", "foo"}
	orig := append([]string{}, args...)
	_ = ResolveTestSpawn("npm", args, spawnCWD, TestSpawnDeps{IsWindows: true, Exists: func(string) bool { return false }})
	if !equalStrSlice(args, orig) {
		t.Errorf("输入 args 被改动：%q → %q", orig, args)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
