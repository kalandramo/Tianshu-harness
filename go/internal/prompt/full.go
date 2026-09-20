package prompt

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/platform"
)

// DetectShellKind 探测宿主的 shell 族。
//
// 对账 TS 的 `getShellCommand().kind`（volatile.ts:1077）。
//
// Windows 上用 platform.HostShellCommand() 真实探测（Git Bash → PowerShell →
// cmd，含 RIVET_USE_POWERSHELL 覆盖）——探测逻辑与 oracle 对账在
// internal/platform 完成，本函数只做「宿主平台名 → 是否走探测」的分派。
//
// **hostPlatform 参数保留**（而非直接用 runtime.GOOS）：让 shell-note 的渲染
// 可测——测试可喂 "win32" 而不依赖真实宿主。真实调用处传 DetectHostEnv() 的
// Platform（已映射为 Node 命名）。
func DetectShellKind(hostPlatform string) string {
	if hostPlatform != "win32" {
		// Unix：TS 的 resolveShellCommand 返回 kind "sh"。
		return "sh"
	}
	// Windows：真实探测。返回 "bash" / "powershell" / "cmd"，
	// 供 WindowsShellNote 选对应文案。
	return string(platform.HostShellCommand().Kind)
}

// DetectHostEnv 探测宿主环境信息，供 `<environment>` 行使用。
//
// 关键：**Go 与 Node 的命名与来源都不同**，必须映射/对齐才能字节等价：
//   - 平台名：Go 的 runtime.GOOS 是 "windows"，Node 的 process.platform 是 "win32"
//     （darwin/linux 两者相同，仅 windows 有差异）
//   - OS 名/版本：Node 的 os.type()/os.release() —— Unix 上与 `uname -s`/`-r` 同源
//     （实测 Darwin 上均为 "Darwin" / "25.6.0"）；**Windows 上不是**，见下。
//
// Windows 的陷阱（实测）：Node 的 os.type()/os.release() 走 Win32
// `RtlGetVersion`，返回 `Windows_NT` / `10.0.26200`。而 Git for Windows 的
// `uname` 返回 `MINGW64_NT-10.0-26200` / `3.6.9-...`——**两者不等价**，且
// uname 的结果还依赖 PATH 上有没有 Git（没有就落到 fallback，得到第三种值）。
// 该行进 `<environment>`，是**冻结前缀**的一部分，不等价会让 Go/TS 的缓存
// key 分叉。故 Windows 分支单独探测（hostEnvWindows），不走 uname。
func DetectHostEnv() HostEnv {
	return HostEnv{
		Platform:  nodePlatformName(runtime.GOOS),
		OSType:    hostOSType(),
		OSRelease: hostOSRelease(),
	}
}

// nodePlatformName 把 Go 的 GOOS 映射为 Node 的 process.platform 命名。
func nodePlatformName(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// fallbackOSType 在 uname 不可用时给出 OS 名。
func fallbackOSType() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows_NT"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

// unameField 取 `uname <flag>` 的输出（trim 空白）。失败时返回 fallback。
func unameField(flag, fallback string) string {
	out, err := exec.Command("uname", flag).Output()
	if err != nil {
		return fallback
	}
	return strings.TrimSpace(string(out))
}

// BuildFullSystemPrompt 组装完整的 system prompt（static + frozen 稳定块）。
//
// 这是 BuildStableVolatileBlock 的**生产消费路径**。
//
// 形态（最小可用路径）：
//
//	<static 提示词（含 calibration）>
//
//	<frozen 稳定块 <context>...</context>>
//
// 注：TS 侧 frozen 块是 **trailer-merge 到 user message**（engine.ts:659），
// 不是拼进 system prompt。此处拼在后面是**有意的架构选择**（见 HANDOFF #7）：
//
//   - TS 把 volatile 内容移到 user message 尾部，使 system prompt **完全冻结**、
//     历史消息尾部也可增量缓存
//   - Go 拼在 system prompt 内——**同样稳定可缓存**（缓存 system+tools 前缀），
//     但 volatile 变化会打断整个前缀
//
// 移植 trailer-merge 需重构 prompt 组装架构（TS 的 frozen 体系是 engine.ts
// 1700+ 行的核心：frozenUserMerged / frozenFetchIndex / eviction clamp /
// 重复消息各占独立快照）。这是**方向性改动**，收益需实测缓存命中率支撑——
// 在拿到数据前**不硬推**。
//
// 若将来移植：应**替换**本函数而非叠加（否则两处渲染同一内容造成双写）。
//
// 与 BuildSystemPromptWithProject 的分工：
//   - 后者：static + project-instructions（无项目文件时返回纯 static）
//   - 本函数：static + **完整 frozen 块**（environment/sober/locus/... 是会话
//     常量，与项目无关，故始终附加）
//
// 两者并存是因为前者被 11 个测试锁定；本函数是新的生产入口。
func BuildFullSystemPrompt(ctx Context, cwd string, host HostEnv) string {
	base := BuildSystemPrompt(ctx)
	vctx := VolatileContext{
		Cwd:     cwd,
		RivetMd: LoadProjectInstructions(cwd),
		// runtime-env：对账 TS 的 detectRuntimeEnvBlock(ctx.cwd) 内部探测。
		// **必须在这里调**——BuildStableVolatileBlock 只负责把它插入正确位置，
		// 不自己探测（保持纯函数）。漏了这一步则字段恒空、块从不出现。
		RuntimeEnv: DetectRuntimeEnvBlock(RealRuntimeEnvDeps(cwd)),
		// verify-commands：对账 TS 的 renderDeclaredVerify(cwd)。
		// 同 RuntimeEnv，**必须在这里调**——BuildStableVolatileBlock 只插位置。
		DeclaredVerify: DetectDeclaredVerifyBlock(cwd),
		// shell 族：决定是否注入 <shell-note>（非 Windows 恒为 "sh" → 不注入）。
		ShellKind: DetectShellKind(host.Platform),
	}
	frozen := BuildStableVolatileBlock(vctx, host)
	if frozen == "" {
		return base
	}
	return base + "\n\n" + frozen
}
