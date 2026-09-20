package tools

import (
	"regexp"
	"runtime"
	"strings"
)

// isWindowsHost 报告宿主是否为 Windows（本包内的宿主判定）。
func isWindowsHost() bool {
	return runtime.GOOS == "windows"
}

// resolveTestSpawn 把测试运行器命令规范化到宿主 OS 的 spawn 形态。
//
// 对账 src/tools/run-tests.ts 的 `resolveTestSpawn`。
//
// # Windows 的 `.cmd` 问题
//
// Windows 上 `npm` / `npx` / `tsx`（以及经 npx 调用的 vitest/jest）是
// **`.cmd` shim**，不是 `.exe`。TS（Node）拒绝在不带 `shell:true` 时 spawn
// `.cmd`（抛 EINVAL），故 TS 把它们路由到 shell 并对路径/参数加引号。
//
// Go 的处境不同：实测 `exec.Command("npm", ...)` 在 Windows 上**能直接**执行
// `.cmd`（Go 内部经 cmd.exe 解释）。所以 Go **不需要** TS 的 shell 路由来让
// 命令"跑起来"。
//
// # 但安全层必须移植
//
// 既然 Go 执行 `.cmd` 时参数仍经 cmd.exe 解析，就存在注入面。实测（本机
// Windows，`exec.Command(some.cmd, args...)`）：
//
//	参数                结果
//	"a b"（含空格）      Go 自动加引号 → 安全
//	"a & b"             Go 自动加引号 → 安全（& 被引号保护）
//	"a&b"（无空格）      **& 被解释执行** → 注入
//	"a|b"（无空格）      **命令被拆分** → 注入
//	"x%PATH%"           **变量展开** → 信息泄露
//	"a^b"               **^ 被吞** → 数据损坏
//	"x("                被解释
//
// 即：Go 的自动引号**只在含空白时触发**，不含空白的元字符全部裸露。TS 的
// `quote` 正是堵这个面的：先把 `%` `"` 消毒为 `_`（这两者在双引号内仍危险），
// 再对含元字符的 token 整体加引号。Go 侧照做。
//
// 参数注入用 deps（对齐 TS 的 TestSpawnDeps），使本函数可在任意宿主上单测。
type TestSpawnDeps struct {
	IsWindows bool
	Exists    func(string) bool
}

// ResolvedTestSpawn 是规范化后的 spawn 描述。
//
// 对账 TS `ResolvedTestSpawn`。
type ResolvedTestSpawn struct {
	Command string
	Args    []string
	// Shell 为真时该命令须经 shell 执行（Windows `.cmd` shim）。
	// 调用方（buildCmd）负责按平台选择 shell（Windows cmd.exe / Unix sh）。
	Shell bool
}

// cmdSafePattern 匹配「无需加引号即可安全穿过 cmd.exe」的 token。
//
// 对账 TS 的 `CMD_SAFE`。
var cmdSafePattern = regexp.MustCompile(`^[A-Za-z0-9@_+=:,./\\-]+$`)

// cmdUnsafeInQuotes 匹配即便在双引号内仍有危险的字符。
//
// `%`：变量展开（实测引号挡不住）。
// `"`：断引，破坏引号包裹。
// 对账 TS 的 `/[%"]/`。
var cmdUnsafeInQuotes = regexp.MustCompile(`[%"]`)

// quoteCmdArg 为 cmd.exe 语境消毒并（必要时）加引号。
//
// 对账 TS `resolveTestSpawn` 内的 `quote`，逐条对齐：
//  1. 已整体带引号、内部无 `%`/`"` 的 token → 原样返回（不双重加引）
//  2. 否则：`%` 与 `"` 替换为 `_`（fail-closed 方向——宁可改字符也不放行）
//  3. 消毒后若全落在 CMD_SAFE 集合内 → 原样；否则整体加双引号
func quoteCmdArg(raw string) string {
	// 1. 已引号包裹且内部干净——不重复加引。
	if len(raw) >= 2 && strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		inner := raw[1 : len(raw)-1]
		if !cmdUnsafeInQuotes.MatchString(inner) {
			return raw
		}
	}
	// 2. 消毒危险字符。
	s := cmdUnsafeInQuotes.ReplaceAllString(raw, "_")
	// 3. 按需加引号。
	if cmdSafePattern.MatchString(s) {
		return s
	}
	return `"` + s + `"`
}

// quoteCmdArgs 对参数逐个消毒（对账 TS 的 `args.map(quote)`）。
func quoteCmdArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quoteCmdArg(a)
	}
	return out
}

// winJoin 拼接 Windows 路径（对账 TS 的 `winPath.join`）。
//
// 手写而非 filepath.Join：本函数可能在非 Windows 宿主上被单测调用，
// filepath.Join 在那种环境下不认反斜杠，会推导出错误路径。
func winJoin(base string, parts ...string) string {
	out := strings.TrimRight(base, `\/`)
	for _, p := range parts {
		out += `\` + strings.Trim(p, `\/`)
	}
	return out
}

// ResolveTestSpawn 规范化测试运行器的 spawn 形态。
//
// 对账 TS `resolveTestSpawn`。Windows：
//   - `tsx` → 优先项目本地 `node_modules\.bin\tsx.cmd`（让 targeted tsx 无需
//     全局安装即可用），缺失则回落 `npx tsx`
//   - `npm` / `npx` → 裸命令名（cmd.exe 从 PATH 解析到 .cmd）
//   - `node` / `pytest` 等真可执行文件 → 直接 spawn（不经 shell）
//
// 非 Windows：一切直接 spawn，不经 shell。
func ResolveTestSpawn(command string, args []string, cwd string, deps TestSpawnDeps) ResolvedTestSpawn {
	if !deps.IsWindows {
		return ResolvedTestSpawn{Command: command, Args: append([]string{}, args...), Shell: false}
	}

	if command == "tsx" {
		// 优先项目本地 shim，让 targeted tsx 不依赖全局安装。
		// win32 路径运算保证在 POSIX 宿主上单测时结果确定。
		localShim := winJoin(cwd, "node_modules", ".bin", "tsx.cmd")
		if deps.Exists(localShim) {
			// 路径始终加引号——cwd 可能含空格（C:\Users\My Name）。
			// 对账 lsp/client.ts::runTscSubprocess + theta-check.ts::resolveTscCommand。
			return ResolvedTestSpawn{
				Command: `"` + localShim + `"`,
				Args:    quoteCmdArgs(args),
				Shell:   true,
			}
		}
		// 回落：npx tsx —— npx.cmd 在 shell 下从 node_modules 解析 tsx。
		return ResolvedTestSpawn{
			Command: "npx",
			Args:    quoteCmdArgs(append([]string{"tsx"}, args...)),
			Shell:   true,
		}
	}

	if command == "npm" || command == "npx" {
		// 裸命令名；cmd.exe 在 shell 下从 PATH 解析 npm.cmd/npx.cmd。
		return ResolvedTestSpawn{Command: command, Args: quoteCmdArgs(args), Shell: true}
	}

	// node / pytest / 其他真可执行文件：直接 spawn。
	return ResolvedTestSpawn{Command: command, Args: append([]string{}, args...), Shell: false}
}
