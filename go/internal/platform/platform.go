// Package platform 实现跨平台 shell 探测（宿主平台 → 可执行的 shell 命令）。
//
// 对账 src/platform.ts 的 `resolveShellCommand` / `resolveGitBashPath`。
//
// **为什么需要它**：Windows 上没有「bash」这个默认约定。宿主可能是
// Git Bash（POSIX 可用）、PowerShell（原生）、或 cmd.exe。硬编码
// `exec.Command("bash", ...)` 在没装 Git Bash 的 Windows 上直接失败；
// 而给 PowerShell 环境发 bash 语法会诱导模型反复失败重试。
//
// **为什么做成纯函数 + 可注入依赖**：探测结果依赖 PATH / 文件系统 / 环境变量，
// 直接读进程状态就**无法对账**（测试机与 TS 生成 golden 的机器不同）。
// 参数化后：oracle 记录「给定这组 deps，TS 返回什么」，Go 侧用同一组 deps
// 断言等价——这正是 TS 自己的做法（`ShellProbeDeps` 是注入用的）。
package platform

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// ShellKind 是 shell 族（决定调用方如何包装命令与解码输出）。
//
// 对账 TS `ShellCommand['kind']`。用语义而非对 cmd 字符串做匹配——后者脆弱。
type ShellKind string

const (
	// ShellBash 是 Git Bash（Windows）或 POSIX sh（Unix 复用此族）。
	ShellBash ShellKind = "bash"
	// ShellPowershell 是 pwsh / powershell。
	ShellPowershell ShellKind = "powershell"
	// ShellCmd 是 cmd.exe。
	ShellCmd ShellKind = "cmd"
	// ShellSh 是 Unix 的 POSIX shell（与 bash 族区分：调用方不需要 Windows 包装）。
	ShellSh ShellKind = "sh"
)

// ShellCommand 是探测出的 shell 调用形态。
//
// 对账 TS `ShellCommand`：cmd + args（**不含**用户命令）+ kind。
type ShellCommand struct {
	Cmd  string
	Args []string
	Kind ShellKind
}

// BuildShellArgs 把用户命令追加到 shell 参数后（对账 TS `buildShellArgs`）。
//
// 返回新切片——不得修改 ShellCommand.Args（探测结果会被缓存复用）。
func BuildShellArgs(shell ShellCommand, command string) []string {
	out := make([]string, 0, len(shell.Args)+1)
	out = append(out, shell.Args...)
	out = append(out, command)
	return out
}

// ShellProbeDeps 是 resolveShellCommand 的注入依赖（纯函数，可单测）。
//
// 对账 TS `ShellProbeDeps`。**不直接读进程状态**——那会让对账测试不可复现。
type ShellProbeDeps struct {
	// IsWindows 报告宿主是否为 Windows。
	IsWindows bool
	// Env 是环境变量查询（取 RIVET_USE_POWERSHELL / ComSpec）。
	Env func(string) string
	// GitBashPath 是已解析的 Git Bash 路径（nil = 不存在）。
	GitBashPath *string
	// HasPwsh 报告指定名字的 PowerShell 是否在 PATH 上。
	HasPwsh func(cmd string) bool
}

// ResolveShellCommand 解析要用的 shell（对账 TS `resolveShellCommand`）。
//
// Windows 优先级：Git Bash → PowerShell（pwsh > powershell）→ cmd.exe。
//
//	Git Bash 给可靠的 POSIX 执行（Claude Code 的做法），且避开
//	`powershell -Command` 的参数改写（曾静默吞命令：exit=0 但 stdout 空）。
//	PowerShell 回退用 -NonInteractive，避免等输入而挂死。
//
// Unix：`sh -c`。
func ResolveShellCommand(deps ShellProbeDeps) ShellCommand {
	if deps.IsWindows {
		// 显式开启：即使有 Git Bash 也强制 PowerShell（对齐
		// CLAUDE_CODE_USE_POWERSHELL_TOOL）。默认仍是 Git-Bash 优先——
		// 我们的模型偏 bash；要原生 cmdlet 的用户自己设这个开关。
		forcePwsh := regexp.MustCompile(`(?i)^(1|true|yes)$`).MatchString(deps.Env("RIVET_USE_POWERSHELL"))
		if !forcePwsh && deps.GitBashPath != nil {
			// 纯 `-c`（对齐 Claude Code），**不走 `-l` 登录 shell**。
			// coreutils（ls/cat/grep）已在 Git 的 usr/bin 内、随 bash.exe 自动上
			// PATH，无需 -l。而 `-l` 会源 /etc/profile 重建 PATH，在
			// MSYS2_PATH_TYPE 非 inherit 时会把宿主 Windows PATH 洗掉 → 商店版
			// Python 的 py/python 等原生命令在 Git Bash 里调不动（PowerShell 能用、
			// bash 工具不能用的元凶之一）。去掉 -l 后子进程直接继承 spawn 传入的
			// 完整 Windows PATH，原生命令可正常调用；也更快。
			return ShellCommand{Cmd: *deps.GitBashPath, Args: []string{"-c"}, Kind: ShellBash}
		}
		for _, cmd := range []string{"pwsh.exe", "powershell.exe"} {
			if deps.HasPwsh(cmd) {
				return ShellCommand{
					Cmd:  cmd,
					Args: []string{"-NoProfile", "-NonInteractive", "-Command"},
					Kind: ShellPowershell,
				}
			}
		}
		comSpec := deps.Env("ComSpec")
		if comSpec == "" {
			comSpec = "cmd.exe"
		}
		return ShellCommand{Cmd: comSpec, Args: []string{"/c"}, Kind: ShellCmd}
	}
	return ShellCommand{Cmd: "sh", Args: []string{"-c"}, Kind: ShellSh}
}

// GitBashProbeDeps 是 resolveGitBashPath 的注入依赖（纯函数，可单测）。
//
// 对账 TS `GitBashProbeDeps`。
type GitBashProbeDeps struct {
	IsWindows bool
	Env       func(string) string
	// WhichGit 返回 PATH 上的 git.exe 绝对路径（未找到时 ok=false）。
	WhichGit func() (string, bool)
	// WhichBash 返回 PATH 上的 bash.exe 绝对路径（2.5 路探针，可选）。
	WhichBash func() (string, bool)
	// Exists 报告路径是否存在。
	Exists func(string) bool
}

// wslBashPattern 匹配 WSL 的 System32\bash.exe。
//
// **必须排除**：它的根是 Linux 文件系统，/mnt/c 慢且行尾地狱——拿它当项目
// shell 是事故。对账 TS 的同一正则。
var wslBashPattern = regexp.MustCompile(`(?i)^[A-Za-z]:\\Windows\\(System32|SysWOW64)\\bash\.exe$`)

// ResolveGitBashPath 解析 Windows 上的 Git Bash（bash.exe）路径。
//
// 探测顺序（镜像 Claude Code，让行为跨环境可预测）：
//
//  1. `RIVET_GIT_BASH_PATH` 覆盖
//  2. 从 `where git` 推导（…\Git\cmd\git.exe → …\Git\bin\bash.exe）
//     2.5. `where bash`——bash.exe 在 PATH 但 git.exe 不在（或 Scoop shim 布局，
//     第 2 步的 gitRoot 推导必然失败）时的兜底；**排除 WSL**
//  3. 常见安装位置（Program Files / LocalAppData / Scoop）
//  4. 桌面端随附的 PortableGit（`RIVET_BUNDLED_GIT_DIR`，Tauri 首次运行时
//     解压）——**放最后**，让系统 Git（用户自己的版本与配置）总是优先
//
// 所有路径运算用 Windows 语义（`filepath` 在 Windows 上即是；非 Windows 宿主
// 上调用本函数应直接返回 nil，见 IsWindows 守卫）。
// 非 Windows 或未找到时返回 nil。
func ResolveGitBashPath(deps GitBashProbeDeps) *string {
	if !deps.IsWindows {
		return nil
	}

	if override := deps.Env("RIVET_GIT_BASH_PATH"); override != "" && deps.Exists(override) {
		return &override
	}

	if gitPath, ok := deps.WhichGit(); ok {
		// git.exe 通常在 …\Git\cmd\ 或 …\Git\bin\；bash.exe 在 …\Git\bin\。
		gitRoot := winDir(winDir(gitPath))
		bashPath := winJoin(gitRoot, "bin", "bash.exe")
		if deps.Exists(bashPath) {
			return &bashPath
		}
	}

	// 2.5：bash.exe 自身在 PATH（Scoop/MSYS2/自定义 PATH 布局）。
	if deps.WhichBash != nil {
		if bashOnPath, ok := deps.WhichBash(); ok && !wslBashPattern.MatchString(bashOnPath) && deps.Exists(bashOnPath) {
			return &bashOnPath
		}
	}

	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	}
	if localApp := deps.Env("LOCALAPPDATA"); localApp != "" {
		candidates = append(candidates, winJoin(localApp, "Programs", "Git", "bin", "bash.exe"))
	}
	// Scoop 安装（gitRoot 推导对 ~\scoop\shims 必然失败，常见开发机布局）。
	if scoopRoot := deps.Env("SCOOP"); scoopRoot != "" {
		candidates = append(candidates, winJoin(scoopRoot, "apps", "git", "current", "bin", "bash.exe"))
	}
	if userProfile := deps.Env("USERPROFILE"); userProfile != "" {
		candidates = append(candidates, winJoin(userProfile, "scoop", "apps", "git", "current", "bin", "bash.exe"))
	}
	for _, c := range candidates {
		if deps.Exists(c) {
			hit := c
			return &hit
		}
	}

	// 兜底：随附的 PortableGit（仅桌面端）。真实 Git for Windows——它的
	// bin\bash.exe 包装器会自组装 PATH（usr/bin coreutils 等），下游无需特判。
	// 首次启动时可能尚未解压完——那时这次探测就是未命中。
	if bundledGitDir := deps.Env("RIVET_BUNDLED_GIT_DIR"); bundledGitDir != "" {
		bundledBash := winJoin(bundledGitDir, "bin", "bash.exe")
		if deps.Exists(bundledBash) {
			return &bundledBash
		}
	}

	return nil
}

// winDir / winJoin 用 Windows 路径语义做运算。
//
// **必须显式用 Windows 语义**（而非 filepath）：这些路径是 Windows 形态的
// 字面量，在非 Windows 宿主上跑测试时（CI 的 Linux runner）filepath 会把
// `C:\Program Files\...` 当相对路径处理，推导出错误结果。对账 TS 的
// `winPath.dirname` / `winPath.join`（TS 侧同样显式用 win32 变体）。
func winDir(p string) string {
	// 手写而非 filepath.Dir：后者在非 Windows 上不认反斜杠。
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return "."
	}
	if i == 0 {
		return p[:1]
	}
	return p[:i]
}

// winJoin 拼接 Windows 路径分量。
func winJoin(base string, parts ...string) string {
	out := strings.TrimRight(base, `\/`)
	for _, p := range parts {
		out += `\` + strings.Trim(p, `\/`)
	}
	return out
}

// ── 生产环境探测（真实 IO）────────────────────────────────────

// 缓存：探测要 spawn `where` 子进程，每次工具调用都跑一遍太浪费。
// 对账 TS 的 `_cachedShell` / `_cachedGitBash`。
//
// 注意：缓存的是**宿主真实状态**。用户中途安装 Git Bash 不会被感知——与 TS
// 行为一致（会话内稳定）。测试若要换环境重探，用 ResetProbeCacheForTests。
var (
	shellOnce     sync.Once
	cachedShell   ShellCommand
	gitBashOnce   sync.Once
	cachedGitBash *string
	gitBashProbed bool
)

// HostShellCommand 探测宿主实际的 shell（带缓存，对账 TS 的 getShellCommand）。
//
// 与 ResolveShellCommand 的分工：本函数负责**真实 IO**（where / 文件系统 /
// 环境变量），把结果喂给纯函数。这样探测逻辑本身可对账，IO 层不可对账但简单。
func HostShellCommand() ShellCommand {
	shellOnce.Do(func() {
		cachedShell = ResolveShellCommand(ShellProbeDeps{
			IsWindows:   isWindowsHost(),
			Env:         os.Getenv,
			GitBashPath: HostGitBashPath(),
			HasPwsh:     hasPwshOnPath,
		})
	})
	return cachedShell
}

// HostGitBashPath 探测宿主的 Git Bash 路径（真实 IO，带缓存）。
//
// 返回 nil 表示未找到（Windows）或非 Windows 宿主。
func HostGitBashPath() *string {
	if !isWindowsHost() {
		return nil
	}
	gitBashOnce.Do(func() {
		gitBashProbed = true
		cachedGitBash = ResolveGitBashPath(GitBashProbeDeps{
			IsWindows: true,
			Env:       os.Getenv,
			WhichGit:  func() (string, bool) { return whereOnPath("git") },
			WhichBash: func() (string, bool) { return whereOnPath("bash") },
			Exists:    fileExists,
		})
	})
	return cachedGitBash
}

// ResetProbeCacheForTests 清空探测缓存（对账 TS 的
// __resetShellProbeCacheForTests）。
//
// 仅测试用：让注入不同环境变量的用例能重新探测，而不是拿到上一个用例的缓存。
func ResetProbeCacheForTests() {
	shellOnce = sync.Once{}
	gitBashOnce = sync.Once{}
	gitBashProbed = false
	cachedGitBash = nil
}

// whereOnPath 用 `where` 查 PATH（对账 TS 的 whichGitWindows / whichBashWindows）。
//
// 用 `where` 而非 exec.LookPath：后者会按 PATHEXT 补扩展名，语义与 TS 不一致
// （TS 显式调 `where` 并取首行）。
func whereOnPath(name string) (string, bool) {
	out, err := exec.Command("where", name).Output()
	if err != nil {
		return "", false
	}
	// 取首行（TS 同样取 split('\n')[0]）——多命中时首行是 PATH 序最靠前的。
	line := strings.SplitN(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n", 2)[0]
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	return line, true
}

// hasPwshOnPath 报告 PowerShell 是否在 PATH（对账 TS 的 hasPwshWindows）。
func hasPwshOnPath(cmd string) bool {
	_, ok := whereOnPath(cmd)
	return ok
}

// fileExists 报告路径是否存在（对账 TS 的 existsSync）。
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isWindowsHost 报告宿主是否为 Windows。
//
// 映射 Go 的 runtime.GOOS（"windows"）——本包内部判据用原生命名，对外暴露的
// 平台名映射（"windows" → "win32"）在 prompt 包（nodePlatformName）负责。
func isWindowsHost() bool {
	return runtime.GOOS == "windows"
}
