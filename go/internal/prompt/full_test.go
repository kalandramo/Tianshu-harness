package prompt

import (
	"os"
	"path/filepath"
	"runtime"
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
