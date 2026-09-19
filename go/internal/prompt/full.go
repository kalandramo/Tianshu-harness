package prompt

import (
	"os/exec"
	"runtime"
	"strings"
)

// DetectHostEnv 探测宿主环境信息，供 `<environment>` 行使用。
//
// 关键：**Go 与 Node 的命名不同**，必须映射才能字节等价：
//   - 平台名：Go 的 runtime.GOOS 是 "windows"，Node 的 process.platform 是 "win32"
//     （darwin/linux 两者相同，仅 windows 有差异）
//   - OS 名/版本：Node 的 os.type()/os.release() 与 `uname -s`/`uname -r` 同源
//     （实测 Darwin 上均为 "Darwin" / "25.6.0"）
//
// 用 `uname` 而非 golang.org/x/sys/unix：后者需引入依赖，而 uname 是 POSIX
// 标准且与 Node 的实现同源。Windows 无 uname，退回 runtime 常量。
func DetectHostEnv() HostEnv {
	return HostEnv{
		Platform:  nodePlatformName(runtime.GOOS),
		OSType:    unameField("-s", fallbackOSType()),
		OSRelease: unameField("-r", ""),
	}
}

// nodePlatformName 把 Go 的 GOOS 映射为 Node 的 process.platform 命名。
func nodePlatformName(goos string) string {
	if goos == "windows" {
		return "win32"
	}
	return goos
}

// fallbackOSType 在 uname 不可用时给出 OS 名（Windows 路径）。
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
// 不是拼进 system prompt。此处拼在后面是**最小可用路径**——与 Go 现有
// 单 SystemPrompt 字段的架构一致；后续移植 trailer-merge 时应**替换**本函数，
// 而非在其上叠加（否则会出现两处渲染同一内容的双写）。
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
	}
	frozen := BuildStableVolatileBlock(vctx, host)
	if frozen == "" {
		return base
	}
	return base + "\n\n" + frozen
}
