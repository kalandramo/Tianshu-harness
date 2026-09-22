package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// spawngit.go —— 统一的 git 子进程启动（环境消毒 + 可执行路径发现）。
//
// 对账 TS `src/tools/spawn-git.ts`。
//
// # TS 侧的动机（逐字对账文件头）
//
// GUI 启动的桌面应用在 Windows 上继承**被截断的 PATH**。`getResolvedEnv`
// 恢复真实 PATH（注册表 / 登录 shell），但调用方还需要解析 **git 可执行文件
// 本身**——当 git 安装目录不在（哪怕已解析的）PATH 上时，裸命令 `git` 会失败
// （罕见但可能）。本模块提供包装，一次性解决两个问题。
//
// # Go 侧的 scope 收窄（**明示偏离**）
//
// **不移植 `getResolvedEnv`**（`resolved-env.ts`，437+ 行）——那是独立子系统
// （Windows 注册表 PATH 恢复、登录 shell 探测、配置化 env 应用、抗交互注入）。
// 本刀只做两件**安全与正确性必需**的事：
//
//  1. `sanitizeGitEnv`——剥离可改变 git 行为指向的攻击者控制位置的环境变量；
//  2. `resolveGitCommand`——解析 git 可执行文件路径。
//
// 环境基线用 `os.Environ()`（继承当前进程）。**与 TS 的差异**：TS 会先经
// `getResolvedEnv` 恢复被 GUI 截断的 PATH；Go 侧不做（该子系统未移植）。
// 对 CLI 场景（PATH 完整）两者等价；对 GUI 启动场景 Go 侧可能找不到 git——
// 已在 HANDOFF 记明。

// unsafeGitEnv 是能改变 git 行为指向攻击者控制位置的**危险子集**。
//
// 对账 TS `UNSAFE_GIT_ENV`（spawn-git.ts:38-46）。
//
// 来源（逐字对账 TS 注释）：codex-security targets.ts 调研
// （`UNSUPPORTED_GIT_ENVIRONMENT`，2026-08）。**只剥危险子集、保留
// `GIT_SSH`/`GIT_EDITOR` 等良性变量**——天枢是开发工具，用户合法的 git 配置
// （如 `GIT_SSH` 自定义）必须保留。
var unsafeGitEnv = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_REPLACE_REF_BASE":             true,
}

// SanitizeGitEnv 返回移除了危险 `GIT_*` 变量的环境拷贝。
//
// 对账 TS `sanitizeGitEnv`（spawn-git.ts:49-56）。
//
// **大小写不敏感比较**（`toUpperCase`）——覆盖 Windows 的环境变量大小写不敏感。
func SanitizeGitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			name = kv[:idx]
		}
		if unsafeGitEnv[strings.ToUpper(name)] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// ResolveGitCommandDeps 是 `ResolveGitCommand` 的注入依赖（纯函数，可单测）。
//
// 对账 TS `ResolveGitCommandDeps`（spawn-git.ts:21-24）。
type ResolveGitCommandDeps struct {
	// GOOS 覆盖平台（默认 `runtime.GOOS`）。
	GOOS string
	// Env 读取环境变量（默认 `os.Getenv`）。
	Env func(string) string
	// Exists 报告路径是否存在（默认 `os.Stat`）。
	Exists func(string) bool
}

// ResolveGitCommand 同步解析 git 可执行文件路径。
//
// 对账 TS `resolveGitCommand`（spawn-git.ts:63-88）。探测顺序：
//
//  1. `RIVET_GIT_PATH` 环境变量覆盖（桌面端从 `env.gitPath` 播种）
//  2. Windows：常见安装位置（Program Files / Program Files (x86) / LOCALAPPDATA）
//  3. 回退字面量 `"git"`——经 PATH 解析（绝大多数安装靠这条）
//
// **`RIVET_GIT_PATH` 与 `env` 的合并语义**（对账 TS）：TS 用
// `{ ...process.env, ...env }`——调用方传的 partial env **不会**掩盖进程级的
// `RIVET_GIT_PATH`。Go 侧同理：`deps.Env` 默认读 `os.Getenv`，即进程环境。
func ResolveGitCommand(deps ResolveGitCommandDeps) string {
	goos := deps.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	getenv := deps.Env
	if getenv == nil {
		getenv = os.Getenv
	}
	exists := deps.Exists
	if exists == nil {
		exists = func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		}
	}

	// 1. 显式覆盖
	if override := getenv("RIVET_GIT_PATH"); override != "" && exists(override) {
		return override
	}

	// 2. Windows：探测常见安装位置
	//
	// **与 TS 的差异**：TS 的 `platform === 'win32'` 是 JS 拼写；
	// Go 的 `runtime.GOOS` 是 `"windows"`。此处两者都认（见 fileinfo.go 的
	// 同款处理）。
	if goos == "windows" || goos == "win32" {
		candidates := []string{
			`C:\Program Files\Git\cmd\git.exe`,
			`C:\Program Files (x86)\Git\cmd\git.exe`,
		}
		if localApp := getenv("LOCALAPPDATA"); localApp != "" {
			candidates = append(candidates, filepath.Join(localApp, "Programs", "Git", "cmd", "git.exe"))
		}
		for _, c := range candidates {
			if exists(c) {
				return c
			}
		}
	}

	// 3. 回退——已解析的 PATH 在绝大多数情况下能找到
	return "git"
}

// GitEnv 返回用于子进程的 git 环境。
//
// 对账 TS `gitEnv`（spawn-git.ts:91-93）：`sanitizeGitEnv(getResolvedEnv(cwd))`。
//
// **Go 侧 scope 收窄**：基线是 `os.Environ()` 而非 `getResolvedEnv(cwd)`
// ——后者（`resolved-env.ts`）未移植，见文件头说明。
func GitEnv() []string {
	return SanitizeGitEnv(os.Environ())
}

// SpawnGit 构造 git 子进程（不启动）。
//
// 对账 TS `spawnGit`（spawn-git.ts:141-149）。返回配置好的 `*exec.Cmd`——
// 调用方负责 `Start`/`Wait`、超时与进程树清理（与 `bash.go` 同款模式）。
//
// `windowsHide: true` 对账：Go 的 `SysProcAttr` 平台差异由
// `prepareCommand` 统一处理（见 proctree.go）。
func SpawnGit(args []string, cwd string) *exec.Cmd {
	cmd := exec.Command(ResolveGitCommand(ResolveGitCommandDeps{}), args...)
	cmd.Dir = cwd
	cmd.Env = GitEnv()
	prepareCommand(cmd)
	return cmd
}
