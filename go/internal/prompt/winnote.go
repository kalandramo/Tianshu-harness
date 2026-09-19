package prompt

import "strings"

// WindowsShellNote 复刻 volatile.ts 的 windowsShellNote —— Windows shell 原生指引。
//
// 四分支（对账 TS 的 switch）：
//   - bash（Git Bash）：POSIX 可用但宿主是 Windows
//   - powershell / cmd：各自的语法速查
//   - sh / 其他（含空串）：返回 ""（Unix 不注入）
//
// 为什么值得逐字节复刻：这些文案直接决定模型在 Windows 上发什么命令。
// 给 PowerShell 环境发 bash 语法会诱导模型反复失败重试。
func WindowsShellNote(kind string) string {
	switch kind {
	case "bash":
		return "<shell-note>shell 是 Git Bash（POSIX）：`ls`/`cat`/`grep`/`&&`/管道/`2>/dev/null` 均可用。运行在 Windows 宿主——路径用正斜杠或加引号（别用反斜杠，会被转义）；Python 可能是 `python` 或 `py`。非零退出码≠必然失败（很多工具用它表达正常结果）。</shell-note>"
	case "powershell":
		return "<shell-note>shell 是 PowerShell。语法速查：环境变量 `$env:NAME`（不是 `$NAME`）；丢弃错误输出 `2>$null`（不是 `2>nul`/`2>/dev/null`）；PS 5.1 不支持 `&&` 串联——用 `;` 分隔，或上一条后判 `$LASTEXITCODE`；命令替换 `$(...)`；删除 `Remove-Item -Recurse -Force`；存在判断 `Test-Path`；列目录/读文件优先 cmdlet（`Get-ChildItem`/`Get-Content -Tail 20`），`ls`/`cat`/`rm`/`pwd` 是别名可用但参数走 cmdlet 风格；Python 用 `py`。非零退出码≠必然失败。命令报「is not recognized as ... cmdlet」= 此环境没有这个命令，应换用可用工具，不要重试同一条——这不是你的错，也不影响判断。</shell-note>"
	case "cmd":
		return "<shell-note>shell 是 cmd.exe：列目录 `dir`、看文件 `type`、环境变量 `%VAR%`；`ls`/`cat` 不存在（用 `dir`/`type`）；现代 cmd 支持 `&&` 串联；丢弃输出 `2>nul`；Python 用 `py`。非零退出码≠必然失败。命令报「is not recognized」= 此环境没有这个命令，换用可用工具，不要重试同一条。</shell-note>"
	default:
		return ""
	}
}

// pathStyleNote 对账 volatile.ts:1072 的字面量。
// 触发条件：targetPlatform === "win32"。
const pathStyleNote = "<path-style-note>Windows 环境：在回复和文档中书写文件路径时用反斜杠（如 src\\tui\\app.ts、D:\\proj\\file.md），与用户的资源管理器/终端习惯一致。工具参数两种分隔符都接受；shell 命令内的路径写法以 shell-note 为准（Git Bash 用正斜杠）。</path-style-note>"

// renderPlatformNote 对账 volatile.ts:1065 的模板。
// 触发条件：targetPlatform !== hostPlatform。
//
// 模板（逐字取自 TS 源码）：
//
//	<platform-note>文件约定（换行/路径风格）按 {target} 生成；但 shell 命令在宿主 {host} 上执行——优先使用跨平台命令，避免目标平台专属语法在宿主机执行失败。</platform-note>
func renderPlatformNote(target, host string) string {
	return "<platform-note>文件约定（换行/路径风格）按 " + target +
		" 生成；但 shell 命令在宿主 " + host +
		" 上执行——优先使用跨平台命令，避免目标平台专属语法在宿主机执行失败。</platform-note>"
}

// replaceAll 是本包内的小工具（避免为单处调用引入 strings 依赖噪音）。
func replaceAll(s, old, new string) string { return strings.ReplaceAll(s, old, new) }
