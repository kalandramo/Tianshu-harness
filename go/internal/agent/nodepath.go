// nodepath.go —— Node `path.resolve` 的 Go 等价实现。
//
// ## 为什么需要它
//
// Go 的 `filepath.Join` / `filepath.Clean` 与 Node 的 `path.resolve` **语义不同**，
// 差异在安全路径上会致命：
//
//	用例                      Go filepath.Join(cwd,p)     Node path.resolve(cwd,p)
//	/repo + /etc/passwd       \repo\etc\passwd            D:\etc\passwd   ← 绝对段截断
//	/repo + ../../etc/x       \etc\x                     D:\etc\x        ← 盘符基准
//	/repo + ../../../..       \..\..\                →   D:\             ← 不越过驱动器根
//
// 实测来源：`go/testdata/approvalrisk/probe_resolve.ts` 的 `path.win32.resolve`
// 全矩阵（3 cwd × 15 path）。本项目已记录同类坑：`filepath.IsAbs("/etc/passwd")`
// 在 Windows 返回 **false**，而 Node 的 `path.win32.isAbsolute` 返回 **true**
// ——漏了会让根相对路径被静默重基进工作区（fail-open 安全洞）。
//
// ## 覆盖范围
//
// 实现 `path.win32.resolve` 与 `path.posix.resolve` 的**核心语义**：
// 绝对段截断 / 驱动器切换 / 不越过根 / UNC 保留 / 空段与 `.` 跳过 / `..` 上溯。
//
// **未覆盖**：`\\?\` 长路径前缀、驱动器相对路径（`D:x`，无斜杠）的 per-drive cwd
// 语义。这两类在 oracle 用例之外；若将来需要，在此补并加对账。
package agent

import (
	"os"
	"runtime"
	"strings"
)

// isWindowsLike 报告当前平台是否用 Windows 路径语义。
//
// 与 TS 侧一致：Node 在 Windows 上用 `path.win32`，其他平台用 `path.posix`。
func isWindowsLike() bool {
	return runtime.GOOS == "windows"
}

// isAbsWin32 复刻 Node `path.win32.isAbsolute`。
//
// 判据：`/x`、`\x`、`C:/x`、`C:\x`、`\\server\share` 均为绝对。
// **关键**：`/etc/passwd` 在 win32 语义下是**绝对**（Go 的 filepath.IsAbs 说 false）。
func isAbsWin32(p string) bool {
	if p == "" {
		return false
	}
	// UNC：\\server\share 或 //server/share
	if len(p) >= 2 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') {
		return true
	}
	// 根相对：/x 或 \x
	if p[0] == '/' || p[0] == '\\' {
		return true
	}
	// 盘符：C:/ 或 C:\（**必须带分隔符**——`C:x` 是驱动器相对，不是绝对）
	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return true
	}
	return false
}

// isAbsPosix 复刻 Node `path.posix.isAbsolute`（只有前导 `/`）。
func isAbsPosix(p string) bool {
	return len(p) > 0 && p[0] == '/'
}

// isDriveLetter 报告字节是否为盘符。
func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// nodeResolve 复刻 Node `path.resolve`（平台分派）。
//
// 语义（对账 `path.resolve` 文档与实测矩阵）：**从右向左**处理参数，遇到
// 绝对段则丢弃其左侧全部；结果规范化为绝对路径（无 `..`、无重复分隔符）。
func nodeResolve(parts ...string) string {
	if isWindowsLike() {
		return resolveWin32(parts...)
	}
	return resolvePosix(parts...)
}

// splitSegmentsWin32 按 `/` 与 `\` 切分（Windows 两种分隔符等价）。
func splitSegmentsWin32(p string) []string {
	return splitSegmentsBy(p, func(r rune) bool { return r == '/' || r == '\\' })
}

// splitSegmentsPosix 只按 `/` 切分。
//
// **为什么不能复用 win32 版本**：posix 语义下 `\` 是**合法文件名字符**——
// `\\server\share\x` 是一个普通相对路径（Node 的 `posix.resolve` 原样保留），
// 把它当分隔符会给出错误结果。实测 oracle 用例
// `TestNodeResolvePosixParity//repo__\\server\share\x` 抓到该缺陷。
func splitSegmentsPosix(p string) []string {
	return splitSegmentsBy(p, func(r rune) bool { return r == '/' })
}

// splitSegmentsBy 按给定分隔符谓词切分并丢弃空段与 `.`。
func splitSegmentsBy(p string, isSep func(rune) bool) []string {
	var out []string
	for _, seg := range strings.FieldsFunc(p, isSep) {
		if seg == "" || seg == "." {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// resolveWin32 复刻 `path.win32.resolve`。
//
// 算法（与 Node 内部一致）：
//  1. 从右向左找**绝对**参数；找到则它成为基底（其左侧全部丢弃）
//  2. 没找到绝对参数时，基底取**当前工作目录**（此处用 cwd 参数的首段盘符）
//  3. 把基底右侧的段并入，逐段处理 `..`（**不越过根**）
func resolveWin32(parts ...string) string {
	// 找最右的绝对段（Node 从右向左，第一个绝对参数即基底）
	base := ""
	baseIdx := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		if isAbsWin32(parts[i]) {
			base = parts[i]
			baseIdx = i
			break
		}
	}

	if baseIdx < 0 {
		// 无绝对参数：基底是进程 cwd
		wd, err := os.Getwd()
		if err != nil {
			wd = `C:\`
		}
		base = wd
		baseIdx = 0
	}

	// 驱动器前缀（`D:` / `\\server\share` / 空=根相对）
	drive, rest := splitDriveWin32(base)

	// **根相对路径继承 cwd 的驱动器**（Node 行为）：base 是 `/x` 这类无盘符的
	// 绝对路径时，Node 会用**当前工作目录的驱动器**补全——实测
	// `win32.resolve('D:/repo', '/x')` → `D:\x`，而非 `\x`。
	//
	// 取驱动器来源：base 无盘符时，从 parts 中最后一个带盘符的段继承；
	// 都没有则用进程 cwd 的盘符。
	if drive == "" {
		drive = inheritDriveWin32(parts, baseIdx)
	}

	// 基底自身的段
	segs := splitSegmentsWin32(rest)

	// 并入 baseIdx 右侧的参数段
	for _, p := range parts[baseIdx+1:] {
		segs = append(segs, splitSegmentsWin32(p)...)
	}

	// 逐段处理 `..`（不越过根）
	var resolved []string
	for _, seg := range segs {
		if seg == ".." {
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			// 已到根：`..` 被吸收（Node 行为——不越过驱动器根）
			continue
		}
		resolved = append(resolved, seg)
	}

	// 组装：驱动器 + `\` + 段
	if drive == "" {
		// 根相对（base 以 / 或 \ 开头且无盘符）——Node 会补当前驱动器
		if len(resolved) == 0 {
			return `\`
		}
		return `\` + strings.Join(resolved, `\`)
	}
	if len(resolved) == 0 {
		return drive + `\`
	}
	return drive + `\` + strings.Join(resolved, `\`)
}

// inheritDriveWin32 为无盘符的绝对基底（`/x` 形态）确定驱动器。
//
// Node 语义：根相对路径的驱动器来自**当前工作目录**。在本函数的调用语境里，
// 「当前工作目录」的近似来源按优先级：
//
//  1. baseIdx 左侧参数中**最后一个带盘符**的段（调用方通常把 cwd 作为首参传入
//     ——`win32.resolve('D:/repo', '/x')` 的驱动器来自 `D:/repo`）
//  2. baseIdx 右侧参数中带盘符的段（罕见，但 Node 也认）
//  3. 进程真实 cwd 的驱动器
//  4. 兜底 `C:`
//
// **为什么不能只看 base**：base 是 `/x`，自身不含盘符——驱动器信息只存在于
// 其他参数或进程 cwd 里。实测 `win32.resolve('D:/repo','/x')` → `D:\x`。
func inheritDriveWin32(parts []string, baseIdx int) string {
	// ① 左侧（含 base 自身）从右向左找带盘符的段
	for i := baseIdx; i >= 0; i-- {
		if d, _ := splitDriveWin32(parts[i]); d != "" {
			return d
		}
	}
	// ② 右侧
	for i := baseIdx + 1; i < len(parts); i++ {
		if d, _ := splitDriveWin32(parts[i]); d != "" {
			return d
		}
	}
	// ③ 进程 cwd 的驱动器
	if wd, err := os.Getwd(); err == nil {
		if d, _ := splitDriveWin32(wd); d != "" {
			return d
		}
	}
	// ④ 兜底
	return "C:"
}

// splitDriveWin32 切出 Windows 驱动器/UNC 前缀与剩余部分。
//
// 形态：
//
//	`D:\a\b`            → drive=`D:`,      rest=`\a\b`
//	`\\server\share\x`  → drive=`\\server\share`, rest=`\x`
//	`\a\b`              → drive=``,        rest=`\a\b`
func splitDriveWin32(p string) (drive, rest string) {
	if p == "" {
		return "", ""
	}
	// UNC
	if len(p) >= 2 && (p[0] == '\\' || p[0] == '/') && (p[1] == '\\' || p[1] == '/') {
		segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
		if len(segs) >= 2 {
			drive = `\\` + segs[0] + `\` + segs[1]
			rest = `\` + strings.Join(segs[2:], `\`)
			if len(segs) == 2 {
				rest = ""
			}
			return drive, rest
		}
		return p, ""
	}
	// 盘符
	if len(p) >= 2 && isDriveLetter(p[0]) && p[1] == ':' {
		return p[:2], p[2:]
	}
	return "", p
}

// resolvePosix 复刻 `path.posix.resolve`。
func resolvePosix(parts ...string) string {
	base := ""
	baseIdx := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		if isAbsPosix(parts[i]) {
			base = parts[i]
			baseIdx = i
			break
		}
	}
	if baseIdx < 0 {
		wd, err := os.Getwd()
		if err != nil {
			wd = "/"
		}
		base = wd
		baseIdx = 0
	}
	segs := splitSegmentsPosix(base)
	for _, p := range parts[baseIdx+1:] {
		segs = append(segs, splitSegmentsPosix(p)...)
	}
	var resolved []string
	for _, seg := range segs {
		if seg == ".." {
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		resolved = append(resolved, seg)
	}
	return "/" + strings.Join(resolved, "/")
}
