package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// shellProbeOracle 是 TS 侧真实 resolveShellCommand / resolveGitBashPath 的产出。
// 生成命令：node_modules/.bin/tsx go/testdata/shellprobe/gen-oracle.ts
type shellProbeOracle struct {
	GitBash map[string]*string `json:"gitBash"`
	Shell   map[string]struct {
		Cmd  string    `json:"cmd"`
		Args []string  `json:"args"`
		Kind ShellKind `json:"kind"`
	} `json:"shell"`
}

func loadShellProbeOracle(t *testing.T) shellProbeOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "shellprobe", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：node_modules/.bin/tsx go/testdata/shellprobe/gen-oracle.ts",
			path, err)
	}
	var o shellProbeOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// ── deps 构造（与 gen-oracle.ts 的 gitBashDeps / shellDeps 逐字对应）──

type envMap map[string]string

func envGetter(m envMap) func(string) string {
	return func(k string) string { return m[k] }
}

type gitBashOpts struct {
	nonWindows bool // 只有 nonWindows 用例置 true（TS 的 isWindows 默认 true）
	env        envMap
	whichGit   *string
	whichBash  *string
	exists     []string
}

func buildGitBashDeps(o gitBashOpts) GitBashProbeDeps {
	existsSet := make(map[string]bool, len(o.exists))
	for _, p := range o.exists {
		existsSet[p] = true
	}
	return GitBashProbeDeps{
		IsWindows: !o.nonWindows,
		Env:       envGetter(o.env),
		WhichGit: func() (string, bool) {
			if o.whichGit == nil {
				return "", false
			}
			return *o.whichGit, true
		},
		WhichBash: func() (string, bool) {
			if o.whichBash == nil {
				return "", false
			}
			return *o.whichBash, true
		},
		Exists: func(p string) bool { return existsSet[p] },
	}
}

type shellOpts struct {
	nonWindows  bool // 同上：TS 的 isWindows 默认 true
	env         envMap
	gitBashPath *string
	hasPwsh     []string
}

func buildShellDeps(o shellOpts) ShellProbeDeps {
	pwshSet := make(map[string]bool, len(o.hasPwsh))
	for _, p := range o.hasPwsh {
		pwshSet[p] = true
	}
	return ShellProbeDeps{
		IsWindows:   !o.nonWindows,
		Env:         envGetter(o.env),
		GitBashPath: o.gitBashPath,
		HasPwsh:     func(cmd string) bool { return pwshSet[cmd] },
	}
}

func strPtr(s string) *string { return &s }

// ── 用例矩阵（与 gen-oracle.ts 的 gitBashCases 逐条对应）──

func gitBashCases() map[string]gitBashOpts {
	return map[string]gitBashOpts{
		"nonWindows": {nonWindows: true},
		"overrideWins": {
			env:    envMap{"RIVET_GIT_BASH_PATH": `D:\custom\bash.exe`},
			exists: []string{`D:\custom\bash.exe`},
		},
		"overrideIgnoredWhenMissing": {
			env: envMap{"RIVET_GIT_BASH_PATH": `D:\nope\bash.exe`},
		},
		"deriveFromWhereGit": {
			whichGit: strPtr(`C:\Program Files\Git\cmd\git.exe`),
			exists:   []string{`C:\Program Files\Git\bin\bash.exe`},
		},
		"commonInstallProgramFiles": {
			exists: []string{`C:\Program Files\Git\bin\bash.exe`},
		},
		"commonInstallProgramFilesX86": {
			exists: []string{`C:\Program Files (x86)\Git\bin\bash.exe`},
		},
		"localAppDataPortable": {
			env:    envMap{"LOCALAPPDATA": `C:\Users\me\AppData\Local`},
			exists: []string{`C:\Users\me\AppData\Local\Programs\Git\bin\bash.exe`},
		},
		"scoopEnvRoot": {
			env:    envMap{"SCOOP": `C:\Scoop`},
			exists: []string{`C:\Scoop\apps\git\current\bin\bash.exe`},
		},
		"scoopUserProfile": {
			env:    envMap{"USERPROFILE": `C:\Users\me`},
			exists: []string{`C:\Users\me\scoop\apps\git\current\bin\bash.exe`},
		},
		"whereBashFallback": {
			whichBash: strPtr(`C:\tools\msys64\usr\bin\bash.exe`),
			exists:    []string{`C:\tools\msys64\usr\bin\bash.exe`},
		},
		"wslBashExcluded": {
			whichBash: strPtr(`C:\Windows\System32\bash.exe`),
			exists:    []string{`C:\Windows\System32\bash.exe`},
		},
		"wslBashExcludedSysWOW64": {
			whichBash: strPtr(`C:\Windows\SysWOW64\bash.exe`),
			exists:    []string{`C:\Windows\SysWOW64\bash.exe`},
		},
		"whereGitWinsOverWhereBash": {
			whichGit:  strPtr(`C:\Program Files\Git\cmd\git.exe`),
			whichBash: strPtr(`C:\other\bash.exe`),
			exists:    []string{`C:\Program Files\Git\bin\bash.exe`, `C:\other\bash.exe`},
		},
		"bundledIsLastFallback": {
			env:    envMap{"RIVET_BUNDLED_GIT_DIR": `C:\App\portable-git`},
			exists: []string{`C:\App\portable-git\bin\bash.exe`},
		},
		"systemGitWinsOverBundled": {
			env: envMap{"RIVET_BUNDLED_GIT_DIR": `C:\App\portable-git`},
			exists: []string{
				`C:\Program Files\Git\bin\bash.exe`,
				`C:\App\portable-git\bin\bash.exe`,
			},
		},
		"bundledNotExtractedYet": {
			env: envMap{"RIVET_BUNDLED_GIT_DIR": `C:\App\portable-git`},
		},
		"nothingFound": {},
	}
}

func shellCases() map[string]shellOpts {
	return map[string]shellOpts{
		"nonWindows":                  {nonWindows: true},
		"gitBashPreferred":            {gitBashPath: strPtr(`C:\Program Files\Git\bin\bash.exe`)},
		"pwshFallback":                {hasPwsh: []string{"pwsh.exe"}},
		"powershellWhenOnlyItPresent": {hasPwsh: []string{"powershell.exe"}},
		"pwshPreferredOverPowershell": {hasPwsh: []string{"pwsh.exe", "powershell.exe"}},
		"cmdFallbackViaComSpec": {
			env: envMap{"ComSpec": `C:\Windows\system32\cmd.exe`},
		},
		"cmdDefaultWhenComSpecUnset": {},
		"forcePwshOverGitBash": {
			env:         envMap{"RIVET_USE_POWERSHELL": "1"},
			gitBashPath: strPtr(`C:\Program Files\Git\bin\bash.exe`),
			hasPwsh:     []string{"pwsh.exe"},
		},
		"forcePwshTrue": {
			env:     envMap{"RIVET_USE_POWERSHELL": "true"},
			hasPwsh: []string{"powershell.exe"},
		},
		"forcePwshYes": {
			env:     envMap{"RIVET_USE_POWERSHELL": "YES"},
			hasPwsh: []string{"pwsh.exe"},
		},
		"forcePwshNoPowershellFallsToCmd": {
			env:         envMap{"RIVET_USE_POWERSHELL": "1", "ComSpec": `C:\Windows\system32\cmd.exe`},
			gitBashPath: strPtr(`C:\Program Files\Git\bin\bash.exe`),
		},
		"forcePwshZeroKeepsGitBash": {
			env:         envMap{"RIVET_USE_POWERSHELL": "0"},
			gitBashPath: strPtr(`C:\Program Files\Git\bin\bash.exe`),
			hasPwsh:     []string{"pwsh.exe"},
		},
		"forcePwshInvalidValueKeepsGitBash": {
			env:         envMap{"RIVET_USE_POWERSHELL": "maybe"},
			gitBashPath: strPtr(`C:\Program Files\Git\bin\bash.exe`),
		},
	}
}

// TestResolveGitBashPathParity —— Git Bash 路径探测与 TS 逐用例等价。
//
// 覆盖 TS platform-shell.test.ts 的探测顺序矩阵：override → where git 推导 →
// where bash 兜底（**排除 WSL**）→ 常见安装位置 → bundled PortableGit（最后）。
func TestResolveGitBashPathParity(t *testing.T) {
	o := loadShellProbeOracle(t)
	if len(o.GitBash) == 0 {
		t.Fatal("oracle 无 gitBash 用例")
	}
	cases := gitBashCases()
	if len(cases) != len(o.GitBash) {
		t.Errorf("用例数不匹配：Go %d vs TS %d——oracle 生成器或本文件漏了新用例",
			len(cases), len(o.GitBash))
	}
	for name, want := range o.GitBash {
		opts, ok := cases[name]
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应构造——补上再断言", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := ResolveGitBashPath(buildGitBashDeps(opts))
			switch {
			case got == nil && want == nil:
				// 等价：都未找到
			case got == nil || want == nil:
				t.Errorf("不等价\n  Go = %v\n  TS = %v", ptrStr(got), ptrStr(want))
			case *got != *want:
				t.Errorf("不等价\n  Go = %q\n  TS = %q", *got, *want)
			}
		})
	}
}

// TestResolveShellCommandParity —— shell 选择与 TS 逐用例等价。
//
// 覆盖 Windows 优先级（Git Bash > pwsh > powershell > cmd）、
// RIVET_USE_POWERSHELL 的三种真值与两种非真值、Unix 的 sh -c。
func TestResolveShellCommandParity(t *testing.T) {
	o := loadShellProbeOracle(t)
	if len(o.Shell) == 0 {
		t.Fatal("oracle 无 shell 用例")
	}
	cases := shellCases()
	if len(cases) != len(o.Shell) {
		t.Errorf("用例数不匹配：Go %d vs TS %d", len(cases), len(o.Shell))
	}
	for name, want := range o.Shell {
		opts, ok := cases[name]
		if !ok {
			t.Errorf("oracle 有用例 %q 而 Go 侧无对应构造", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			got := ResolveShellCommand(buildShellDeps(opts))
			if got.Cmd != want.Cmd {
				t.Errorf("cmd 不等价：Go=%q TS=%q", got.Cmd, want.Cmd)
			}
			if got.Kind != want.Kind {
				t.Errorf("kind 不等价：Go=%q TS=%q", got.Kind, want.Kind)
			}
			if !equalStrings(got.Args, want.Args) {
				t.Errorf("args 不等价：Go=%v TS=%v", got.Args, want.Args)
			}
		})
	}
}

// TestBuildShellArgs —— 用户命令追加在 shell 参数之后，且不改动原 Args。
func TestBuildShellArgs(t *testing.T) {
	shell := ShellCommand{Cmd: "bash", Args: []string{"-c"}, Kind: ShellBash}
	got := BuildShellArgs(shell, "echo hi")
	want := []string{"-c", "echo hi"}
	if !equalStrings(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
	// 原切片不得被修改（探测结果会被缓存复用）。
	if len(shell.Args) != 1 || shell.Args[0] != "-c" {
		t.Errorf("BuildShellArgs 改动了原 Args：%v", shell.Args)
	}
}

// TestWinPathHelpers —— Windows 路径运算不依赖宿主平台。
//
// 这些路径是 Windows 形态字面量，在 Linux CI 上也必须推导正确（对账 TS 的
// winPath.dirname/join 显式用 win32 变体）。
func TestWinPathHelpers(t *testing.T) {
	// winDir 是**单层** dirname——ResolveGitBashPath 里调两次得到 gitRoot
	// （…\Git\cmd\git.exe → …\Git\cmd → …\Git）。
	cases := []struct{ in, want string }{
		{`C:\Program Files\Git\cmd\git.exe`, `C:\Program Files\Git\cmd`},
		{`C:\Program Files\Git\bin\git.exe`, `C:\Program Files\Git\bin`},
		{`C:\git.exe`, `C:`},
		{`git.exe`, `.`},
	}
	for _, c := range cases {
		if got := winDir(c.in); got != c.want {
			t.Errorf("winDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 两次 dirname 得 gitRoot，再接 bin\bash.exe —— 复刻生产推导链。
	gitExe := `C:\Program Files\Git\cmd\git.exe`
	if got := winJoin(winDir(winDir(gitExe)), "bin", "bash.exe"); got != `C:\Program Files\Git\bin\bash.exe` {
		t.Errorf("gitRoot 推导链 = %q", got)
	}
	if got := winJoin(`C:\root`, "bin", "bash.exe"); got != `C:\root\bin\bash.exe` {
		t.Errorf("winJoin = %q", got)
	}
	// 尾部分隔符不应产生双斜杠。
	if got := winJoin(`C:\root\`, "bin"); got != `C:\root\bin` {
		t.Errorf("winJoin（尾部斜杠）= %q", got)
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func equalStrings(a, b []string) bool {
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
