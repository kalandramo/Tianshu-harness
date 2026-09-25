package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/platform"
)

// winnoteOracle 是 TS 侧真实 windowsShellNote 的产出 + 两条 note 模板。
// 生成命令：npx tsx go/testdata/winnote/gen-oracle.ts
type winnoteOracle struct {
	ShellNotes           map[string]string `json:"shellNotes"`
	PlatformNoteTemplate string            `json:"platformNoteTemplate"`
	PathStyleNote        string            `json:"pathStyleNote"`
}

func loadWinnoteOracle(t *testing.T) winnoteOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "winnote", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/winnote/gen-oracle.ts", path, err)
	}
	var o winnoteOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestWindowsShellNoteParity —— windowsShellNote 四个分支与 TS 逐字节相同。
func TestWindowsShellNoteParity(t *testing.T) {
	o := loadWinnoteOracle(t)
	if len(o.ShellNotes) == 0 {
		t.Fatal("oracle 无 shellNotes")
	}
	for kind, want := range o.ShellNotes {
		t.Run(kind, func(t *testing.T) {
			if got := WindowsShellNote(kind); got != want {
				t.Errorf("不等价\n  Go 长度=%d\n  TS 长度=%d\n  Go =%q\n  TS =%q",
					len(got), len(want), trunc2(got, 200), trunc2(want, 200))
			}
		})
	}
}

// TestWindowsShellNoteUnixEmpty —— sh 与未知 kind 返回空串（Unix 不注入）。
func TestWindowsShellNoteUnixEmpty(t *testing.T) {
	for _, k := range []string{"sh", "", "unknown-kind", "zsh"} {
		if got := WindowsShellNote(k); got != "" {
			t.Errorf("kind=%q 应返回空串，实际 %q", k, got)
		}
	}
}

// TestPlatformNoteRendering —— platform-note 的插值形态。
//
// 该 note 仅在「目标平台 ≠ 宿主平台」时出现。用 oracle 的模板逐字对账。
func TestPlatformNoteRendering(t *testing.T) {
	o := loadWinnoteOracle(t)
	got := renderPlatformNote("win32", "darwin")
	want := replaceAll(o.PlatformNoteTemplate, "{target}", "win32")
	want = replaceAll(want, "{host}", "darwin")
	if got != want {
		t.Errorf("不等价\n  Go =%q\n  TS 模板 =%q", got, want)
	}
}

// TestPathStyleNoteRendering —— path-style-note 在 win32 目标平台时出现。
func TestPathStyleNoteRendering(t *testing.T) {
	o := loadWinnoteOracle(t)
	if got := pathStyleNote; got != o.PathStyleNote {
		t.Errorf("不等价\n  Go =%q\n  TS =%q", got, o.PathStyleNote)
	}
}

// TestPlatformNotesInBlock —— 三个 note 在 frozen 块中的位置与触发条件。
//
// 对账 volatile.ts 的顺序：environment → platform-note → path-style-note
// → shell-note → runtime-env → sober。
func TestPlatformNotesInBlock(t *testing.T) {
	// 场景 1：目标平台 ≠ 宿主 → 有 platform-note；目标非 win32 → 无 path-style-note
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	got := BuildStableVolatileBlock(VolatileContext{
		Cwd:            "/fixture",
		TargetPlatform: "linux",
	}, host)
	if !contains(got, "<platform-note>") {
		t.Errorf("目标平台≠宿主时应含 platform-note，实际 %q", trunc2(got, 300))
	}
	if contains(got, "<path-style-note>") {
		t.Error("目标非 win32 时不应含 path-style-note")
	}

	// 场景 2：目标平台 = 宿主 → 无 platform-note
	got2 := BuildStableVolatileBlock(VolatileContext{Cwd: "/fixture"}, host)
	if contains(got2, "<platform-note>") {
		t.Error("目标平台=宿主时不应含 platform-note")
	}

	// 场景 3：目标 win32 → 有 path-style-note
	got3 := BuildStableVolatileBlock(VolatileContext{
		Cwd:            "/fixture",
		TargetPlatform: "win32",
	}, host)
	if !contains(got3, "<path-style-note>") {
		t.Errorf("目标 win32 时应含 path-style-note，实际 %q", trunc2(got3, 300))
	}

	// 场景 4：shell kind 非空（宿主属性）→ 有 shell-note
	got4 := BuildStableVolatileBlock(VolatileContext{
		Cwd: "/fixture",
	}, HostEnv{Platform: "win32", OSType: "Windows_NT", OSRelease: "10.0", ShellKind: "powershell"})
	if !contains(got4, "<shell-note>") {
		t.Errorf("ShellKind 非空时应含 shell-note，实际 %q", trunc2(got4, 300))
	}
	if !contains(got4, "PowerShell") {
		t.Error("应含 PowerShell 分支文案")
	}
}

// TestPlatformNoteOrder —— 三个 note 的顺序（对账 volatile.ts:1064-1080）。
func TestPlatformNoteOrder(t *testing.T) {
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0", ShellKind: "bash"}
	got := BuildStableVolatileBlock(VolatileContext{
		Cwd:            "/fixture",
		TargetPlatform: "win32",
		RuntimeEnv:     "<runtime-env>\nx\n</runtime-env>",
	}, host)

	idxEnv := indexOf(got, "<environment")
	idxPlatform := indexOf(got, "<platform-note>")
	idxPath := indexOf(got, "<path-style-note>")
	idxShell := indexOf(got, "<shell-note>")
	idxRuntime := indexOf(got, "<runtime-env>")
	idxSober := indexOf(got, "<sober>")

	if idxPlatform < 0 || idxPath < 0 || idxShell < 0 {
		t.Fatalf("三个 note 都应出现，实际 %q", trunc2(got, 400))
	}
	if !(idxEnv < idxPlatform && idxPlatform < idxPath && idxPath < idxShell &&
		idxShell < idxRuntime && idxRuntime < idxSober) {
		t.Errorf("顺序应为 environment→platform→path-style→shell→runtime-env→sober，实际 %d/%d/%d/%d/%d/%d",
			idxEnv, idxPlatform, idxPath, idxShell, idxRuntime, idxSober)
	}
}

// TestEnvironmentHostAttribute —— 目标平台≠宿主时 environment 行应带 host 属性。
//
// M2 变异（不产生 host 属性）首轮红 0 处——我的用例只测了 note 的出现，
// 没测 environment 行本身的形态变化。这条补上。
//
// 对账 TS volatile.ts:1062-1063：
//
//	const hostAttr = targetPlatform !== process.platform ? ` host="${process.platform}"` : ''
func TestEnvironmentHostAttribute(t *testing.T) {
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}

	// 目标平台≠宿主 → 应带 host="darwin"
	diff := BuildStableVolatileBlock(VolatileContext{Cwd: "/fixture", TargetPlatform: "win32"}, host)
	if !contains(diff, `host="darwin"`) {
		t.Errorf("目标平台≠宿主时应带 host 属性，实际 %q", trunc2(diff, 200))
	}
	if !contains(diff, `platform="win32"`) {
		t.Errorf("platform 应为目标平台 win32，实际 %q", trunc2(diff, 200))
	}

	// 目标平台=宿主 → 不带 host 属性
	same := BuildStableVolatileBlock(VolatileContext{Cwd: "/fixture"}, host)
	if contains(same, `host="`) {
		t.Errorf("目标平台=宿主时不应带 host 属性，实际 %q", trunc2(same, 200))
	}
	if !contains(same, `platform="darwin"`) {
		t.Errorf("platform 应为宿主 darwin，实际 %q", trunc2(same, 200))
	}
}

// TestDetectHostEnvShellKind —— DetectHostEnv 填充的 ShellKind 与真实探测一致。
//
// 对账 TS 的 `getShellCommand().kind`（volatile.ts:1081）。此前该值由
// `DetectShellKind(host.Platform)` 产出，内部偷读 runtime.GOOS，使 win32 渲染
// 不可测；现改为 HostEnv 的注入字段（见 HostEnv.ShellKind 注释）。
//
// 本测试锁**生产路径的一致性**：DetectHostEnv 的 ShellKind 必须等于
// platform 层真实探测的 Kind（不硬编码断言具体值——它依赖本机环境：
// 装了 Git Bash 的 Windows 就是 "bash"）。
func TestDetectHostEnvShellKind(t *testing.T) {
	env := DetectHostEnv()
	want := string(platform.HostShellCommand().Kind)
	if env.ShellKind != want {
		t.Errorf("DetectHostEnv().ShellKind = %q, want %q（须与 platform.HostShellCommand().Kind 一致）",
			env.ShellKind, want)
	}
	// 非 Windows 宿主恒为 "sh"（platform.ResolveShellCommand 的 Unix 分支）。
	if runtime.GOOS != "windows" && env.ShellKind != "sh" {
		t.Errorf("非 Windows 宿主 ShellKind 应为 sh，得到 %q", env.ShellKind)
	}
}

// TestWin32ShellKindRendersNote —— **win32 渲染端到端**（此前不可测）。
//
// 这是注入化的核心收益：喂 `HostEnv{Platform:"win32", ShellKind:"bash"}` 即可
// 在任意平台验证 win32 的 shell-note 渲染，无需真实 Windows 宿主。
// 此前 `DetectShellKind` 偷读 runtime.GOOS，非 Windows 上 win32 分支永不可达。
func TestWin32ShellKindRendersNote(t *testing.T) {
	dir := t.TempDir()
	for _, kind := range []string{"bash", "powershell", "cmd"} {
		host := HostEnv{Platform: "win32", OSType: "Windows_NT", OSRelease: "10.0.22631", ShellKind: kind}
		got := BuildFullSystemPrompt(Context{}, dir, host)
		if !contains(got, "<shell-note>") {
			t.Errorf("win32 + ShellKind=%q 应注入 shell-note，实际 %q", kind, trunc2(got, 300))
		}
	}
	// 对照：Unix shell 族不注入（对账 WindowsShellNote 的 default 分支）。
	hostSh := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0", ShellKind: "sh"}
	if got := BuildFullSystemPrompt(Context{}, dir, hostSh); contains(got, "<shell-note>") {
		t.Errorf("ShellKind=sh 不应注入 shell-note，实际 %q", trunc2(got, 300))
	}
}

// TestFullSystemPromptNoShellNoteOnUnix —— Unix 宿主不注入 shell-note。
func TestFullSystemPromptNoShellNoteOnUnix(t *testing.T) {
	dir := t.TempDir()
	host := HostEnv{Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0"}
	got := BuildFullSystemPrompt(Context{}, dir, host)
	if contains(got, "<shell-note>") {
		t.Errorf("Unix 宿主不应注入 shell-note，实际 %q", trunc2(got, 300))
	}
}
