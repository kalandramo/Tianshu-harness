package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBuildFullSystemPrompt —— 完整 system prompt 的组装（static + frozen 块）。
//
// 这是 BuildStableVolatileBlock 的**生产消费路径**——没有它，frozen 块
// 就是悬空代码。
//
// 组装形态（最小可用路径）：
//
//	<static 提示词（含 calibration）>
//
//	<frozen 稳定块（<context>...</context>）>
//
// 注：TS 侧 frozen 块是 trailer-merge 到 user message（engine.ts:659），
// 不是拼进 system prompt。此处拼在后面是**最小可用路径**，与 Go 现有
// 单 SystemPrompt 字段的架构一致；后续移植 trailer-merge 时应替换本函数。
func TestBuildFullSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	host := HostEnv{Platform: "linux", OSType: "Linux", OSRelease: "6.1.0"}

	got := BuildFullSystemPrompt(Context{ModelFamily: "deepseek"}, dir, host)

	// 必须含 static 提示词（含 deepseek calibration）
	if !hasPrefix(got, BuildSystemPrompt(Context{ModelFamily: "deepseek"})) {
		t.Error("应以 static 提示词开头")
	}
	// 必须含 frozen 块的 <context> 外壳
	if !contains(got, "<context>") {
		t.Error("应含 frozen 块的 <context>")
	}
	// 必须含 environment 行（宿主信息）
	if !contains(got, `platform="linux"`) {
		t.Error("应含宿主 platform")
	}
	// 必须含 sober
	if !contains(got, "<sober>") {
		t.Error("应含 sober 块")
	}
}

// TestBuildFullSystemPromptNoProject —— 无项目文件时仍应含 frozen 块。
//
// 注意与 BuildSystemPromptWithProject 的区别：后者无项目文件时返回纯 static；
// 本函数**始终**附加 frozen 块（environment/sober 是会话常量，与项目无关）。
func TestBuildFullSystemPromptNoProject(t *testing.T) {
	dir := t.TempDir()
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.0.0"}
	got := BuildFullSystemPrompt(Context{}, dir, host)

	if !contains(got, "<sober>") {
		t.Error("无项目文件时仍应含 sober（会话常量）")
	}
	if !contains(got, `platform="darwin"`) {
		t.Error("无项目文件时仍应含 environment")
	}
}

// TestBuildFullSystemPromptWithProject —— 有项目文件时 frozen 块含 project-instructions。
func TestBuildFullSystemPromptWithProject(t *testing.T) {
	dir := t.TempDir()
	md := "## 高危命令纪律\n用户让你查看时不要动手，必须等待确认。"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.0.0"}
	got := BuildFullSystemPrompt(Context{}, dir, host)

	if !contains(got, "高危命令纪律") {
		t.Error("应含项目指令内容")
	}
	if !contains(got, "<project-instructions>") {
		t.Error("应含 project-instructions 块")
	}
}

// TestBuildFullSystemPromptHostEnv —— 宿主信息进 environment 行（不硬编码）。
func TestBuildFullSystemPromptHostEnv(t *testing.T) {
	dir := t.TempDir()
	host := HostEnv{Platform: "win32", OSType: "Windows_NT", OSRelease: "10.0.22631"}
	got := BuildFullSystemPrompt(Context{}, dir, host)

	if !contains(got, `platform="win32"`) {
		t.Error("应含注入的 platform")
	}
	if !contains(got, "Windows_NT 10.0.22631") {
		t.Error("应含注入的 os 字符串")
	}
}

// TestDetectHostEnv —— 从 Go runtime 探测宿主（生产路径用）。
//
// 注意 Go 与 Node 的平台名差异：Go 的 runtime.GOOS 是 "darwin"/"linux"/"windows"，
// Node 的 process.platform 是 "darwin"/"linux"/"win32"。**必须映射**，
// 否则 environment 行的字节与 TS 不一致。
func TestDetectHostEnv(t *testing.T) {
	h := DetectHostEnv()
	if h.Platform == "" {
		t.Fatal("Platform 不应为空")
	}
	// windows 必须映射为 win32（Node 的命名）
	if runtime.GOOS == "windows" && h.Platform != "win32" {
		t.Errorf("windows 应映射为 win32，得到 %q", h.Platform)
	}
	if runtime.GOOS != "windows" && h.Platform != runtime.GOOS {
		t.Errorf("非 windows 平台应保持 GOOS 原值，得到 %q（GOOS=%s）", h.Platform, runtime.GOOS)
	}
	if h.OSType == "" {
		t.Error("OSType 不应为空")
	}
}

// TestDetectHostEnvWindowsParity —— Windows 上 OSType/OSRelease 必须与 Node 同源。
//
// **这是回归测试**：修复前 Windows 分支走 Git Bash 的 `uname`，得到
// `MINGW64_NT-10.0-26200` / `3.6.9-...`，而 Node 的 os.type()/os.release()
// 是 `Windows_NT` / `10.0.26200`。该行进 `<environment>`（冻结前缀），
// 不等价会让 Go/TS 的缓存 key 分叉；且 uname 结果还依赖 PATH 上有没有 Git
// ——同一二进制在不同启动环境下产出不同的冻结前缀。
//
// 断言的是**可独立验证的形态**（而非硬编码本机版本号——那会在别的机器上假红）：
//   - OSType 必须是 "Windows_NT"（Node 的定值，不是 MINGW64_NT-*）
//   - OSRelease 必须是三段数字 "X.Y.Z"（Node os.release() 的形态，
//     不是 uname 的 "3.6.9-b4195d69.x86_64"）
func TestDetectHostEnvWindowsParity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 适用")
	}
	h := DetectHostEnv()

	if h.OSType != "Windows_NT" {
		t.Errorf("OSType 应为 %q（Node os.type() 的定值），得到 %q——"+
			"若为 MINGW64_NT-* 说明走了 uname 而非 RtlGetVersion", "Windows_NT", h.OSType)
	}

	// 形态：三个点分十进制段（Node 的 os.release() 在 Windows 上就是 major.minor.build）。
	parts := strings.Split(h.OSRelease, ".")
	if len(parts) != 3 {
		t.Fatalf("OSRelease 应为三段 major.minor.build，得到 %q", h.OSRelease)
	}
	for _, p := range parts {
		if p == "" {
			t.Fatalf("OSRelease 段不应为空：%q", h.OSRelease)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				t.Errorf("OSRelease 应只含数字与点（Node 形态），得到 %q", h.OSRelease)
				break
			}
		}
	}
}

// TestBuildFullSystemPromptRuntimeEnv —— 生产路径必须真的探测 runtime-env。
//
// 这是端到端验证暴露的缺口：BuildStableVolatileBlock 支持 ctx.RuntimeEnv
// 注入，但 BuildFullSystemPrompt（生产入口）**没有调用** DetectRuntimeEnvBlock
// ——字段恒空，块从不出现。Go-only 测试只测了注入位置，测不出这个。
//
// 断言：cwd 含 go.mod 时，输出应含 <runtime-env> 块。
func TestBuildFullSystemPromptRuntimeEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	got := BuildFullSystemPrompt(Context{}, dir, host)

	if !contains(got, "<runtime-env>") {
		t.Errorf("cwd 含 go.mod 时应含 <runtime-env> 块（生产路径必须调 DetectRuntimeEnvBlock）\n实际输出：%q",
			trunc2(got, 400))
	}
	if !contains(got, "go: declared 1.22.0 via go.mod") {
		t.Errorf("应探测到 go 版本声明，实际：%q", trunc2(got, 400))
	}
}

// TestBuildFullSystemPromptNoRuntimeEnv —— 无 marker 文件时不产生该块。
func TestBuildFullSystemPromptNoRuntimeEnv(t *testing.T) {
	dir := t.TempDir()
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	got := BuildFullSystemPrompt(Context{}, dir, host)
	if contains(got, "<runtime-env>") {
		t.Errorf("空目录不应有 <runtime-env> 块，实际：%q", trunc2(got, 400))
	}
}

// TestBuildFullSystemPromptRendersProjectInstructions —— **生产路径端到端**：
// BuildFullSystemPrompt 必须把 cwd 下的项目指令渲染进 <context> 块。
//
// 这条链路容易断：`BuildSystemPromptWithProject`（Deprecated）直接渲染，
// 而生产走 BuildFullSystemPrompt → vctx.RivetMd → volatile 层渲染。若中间
// 某一环漏了，项目指令会**静默消失**（模型看不到 AGENTS.md）。
func TestBuildFullSystemPromptRendersProjectInstructions(t *testing.T) {
	dir := t.TempDir()
	// 写一个可识别的项目指令文件（LoadProjectInstructions 读 AGENTS.md 等）
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# 项目约定\n\n这是测试用的项目指令标记 XYZMARKER。\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := BuildFullSystemPrompt(Context{}, dir, HostEnv{Platform: "linux", OSType: "Linux", OSRelease: "6.0"})

	if !contains(got, "XYZMARKER") {
		t.Errorf("生产路径必须渲染项目指令（模型要能看到 AGENTS.md）：\n%s", got)
	}
	// 应包裹在 <context> 内
	if !contains(got, "<context>") {
		t.Errorf("应含 <context> 外壳：\n%s", got)
	}
	if !contains(got, "</context>") {
		t.Errorf("应含 </context> 闭合：\n%s", got)
	}
}

// TestBuildFullSystemPromptNoProjectInstructions —— 无项目文件时不渲染该块。
func TestBuildFullSystemPromptNoProjectInstructions(t *testing.T) {
	dir := t.TempDir()
	got := BuildFullSystemPrompt(Context{}, dir, HostEnv{Platform: "linux", OSType: "Linux", OSRelease: "6.0"})
	if contains(got, "<project-instructions>") {
		t.Errorf("无项目文件时不应有 project-instructions 块：\n%s", got)
	}
}
