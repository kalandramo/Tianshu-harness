package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/trust"
)

// writeHooks 在临时项目里写 .rivet/hooks.json。
func writeHooks(t *testing.T, dir, content string) {
	t.Helper()
	rivetDir := filepath.Join(dir, ".rivet")
	if err := os.MkdirAll(rivetDir, 0o755); err != nil {
		t.Fatalf("建 .rivet 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rivetDir, "hooks.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("写 hooks.json 失败: %v", err)
	}
}

// trustProject 让 trust 认这个目录为已授信（经环境变量覆盖，跨平台稳定）。
func trustProject(t *testing.T) {
	t.Helper()
	t.Setenv("RIVET_TRUST_PROJECT", "1")
}

// TestLoadHooksConfigMissingFile —— 文件不存在返回空配置，不报错。
func TestLoadHooksConfigMissingFile(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)

	cfg := LoadHooksConfig(dir)
	if len(cfg.Hooks) != 0 {
		t.Fatalf("缺文件应返回空配置，实得 %d 条", len(cfg.Hooks))
	}
}

// TestLoadHooksConfigUntrustedProject —— **本包的核心安全不变量**。
//
// 未授信项目里的 hooks.json **不执行**（脚本拿完整用户权限，不能自我授权）。
func TestLoadHooksConfigUntrustedProject(t *testing.T) {
	dir := t.TempDir()
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"a.sh"}]}`)
	t.Setenv("RIVET_TRUST_PROJECT", "0")

	cfg := LoadHooksConfig(dir)
	if len(cfg.Hooks) != 0 {
		t.Fatalf("未授信项目不应加载任何 hook，实得 %d 条", len(cfg.Hooks))
	}
}

// TestLoadHooksConfigTrustedProject —— 已授信项目正常加载。
func TestLoadHooksConfigTrustedProject(t *testing.T) {
	dir := t.TempDir()
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"a.sh"}]}`)
	trustProject(t)

	cfg := LoadHooksConfig(dir)
	if len(cfg.Hooks) != 1 {
		t.Fatalf("已授信应加载 1 条，实得 %d", len(cfg.Hooks))
	}
	if cfg.Hooks[0].Event != EventPreTurn || cfg.Hooks[0].Script != "a.sh" {
		t.Errorf("解析错误: %+v", cfg.Hooks[0])
	}
}

// TestLoadHooksConfigFiltersInvalidEvents —— 未知 event 被过滤。
func TestLoadHooksConfigFiltersInvalidEvents(t *testing.T) {
	dir := t.TempDir()
	writeHooks(t, dir, `{"hooks":[
		{"event":"preTurn","script":"ok.sh"},
		{"event":"bogusEvent","script":"bad.sh"},
		{"event":"postTool","script":""}
	]}`)
	trustProject(t)

	cfg := LoadHooksConfig(dir)
	if len(cfg.Hooks) != 1 {
		t.Fatalf("应只保留 1 条合法 hook，实得 %d: %+v", len(cfg.Hooks), cfg.Hooks)
	}
	if cfg.Hooks[0].Script != "ok.sh" {
		t.Errorf("保留了错误条目: %+v", cfg.Hooks[0])
	}
}

// TestLoadHooksConfigMalformedJSON —— 坏 JSON 静默降级为空配置。
func TestLoadHooksConfigMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeHooks(t, dir, `{not json`)
	trustProject(t)

	cfg := LoadHooksConfig(dir)
	if len(cfg.Hooks) != 0 {
		t.Fatalf("坏 JSON 应降级为空配置，实得 %d 条", len(cfg.Hooks))
	}
}

// TestRunForEventFiltersByEvent —— 只跑匹配事件的脚本。
func TestRunForEventFiltersByEvent(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	writeHooks(t, dir, `{"hooks":[
		{"event":"preTurn","script":"pre.sh"},
		{"event":"postTool","script":"post.sh"}
	]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 1 {
		t.Fatalf("应只跑 1 条 preTurn，实得 %d", len(results))
	}
	if results[0].Script != "pre.sh" {
		t.Errorf("跑错脚本: %+v", results[0])
	}
}

// TestRunForEventMissingScript —— 脚本文件不存在 → ok=false + 说明。
func TestRunForEventMissingScript(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"nope.sh"}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(results))
	}
	if results[0].Ok {
		t.Error("脚本不存在应 ok=false")
	}
	if !strings.Contains(results[0].Output, "Script not found") {
		t.Errorf("应给出 not found 说明，实得: %q", results[0].Output)
	}
}

// TestRunForEventExecutesScript —— 真实执行脚本，捕获输出。
//
// Windows 上不可用（需 shebang / 可执行位）——跳过并说明。
func TestRunForEventExecutesScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang——脚本执行路径由 TestRunForEventScriptArgs 覆盖（用 .bat）")
	}
	dir := t.TempDir()
	trustProject(t)
	script := filepath.Join(dir, "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"event=$RIVET_HOOK_EVENT\"\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"hello.sh"}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(results))
	}
	if !results[0].Ok {
		t.Fatalf("脚本应执行成功，实得: %q", results[0].Output)
	}
	if !strings.Contains(results[0].Output, "event=preTurn") {
		t.Errorf("环境变量未传给脚本，输出: %q", results[0].Output)
	}
}

// TestRunForEventStdinJSON —— 脚本经 stdin 收到完整 JSON 上下文。
func TestRunForEventStdinJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	trustProject(t)
	script := filepath.Join(dir, "dump.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"postTool","script":"dump.sh"}]}`)

	r := &Runner{Cwd: dir, SessionID: "sess-1", GetTurn: func() int { return 7 }}
	results := r.RunForEvent(HookContext{
		Event: EventPostTool, Cwd: dir, SessionID: "sess-1", Turn: 7,
		ToolName: "read_file", ToolResult: "success",
	})

	if len(results) != 1 || !results[0].Ok {
		t.Fatalf("执行失败: %+v", results)
	}
	var got HookContext
	if err := json.Unmarshal([]byte(results[0].Output), &got); err != nil {
		t.Fatalf("stdin 不是合法 JSON: %q (%v)", results[0].Output, err)
	}
	if got.ToolName != "read_file" || got.Turn != 7 || got.SessionID != "sess-1" {
		t.Errorf("stdin 上下文不符: %+v", got)
	}
}

// TestRunForEventTimeout —— 超时脚本被杀，ok=false。
func TestRunForEventTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	trustProject(t)
	script := filepath.Join(dir, "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"slow.sh","timeoutMs":300}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(results))
	}
	if results[0].Ok {
		t.Error("超时脚本应 ok=false")
	}
}

// TestRunForEventContinuesAfterFailure —— **单条失败不中断后续**。
func TestRunForEventContinuesAfterFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	trustProject(t)
	if err := os.WriteFile(filepath.Join(dir, "fail.sh"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.sh"), []byte("#!/bin/sh\necho fine\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[
		{"event":"preTurn","script":"fail.sh"},
		{"event":"preTurn","script":"ok.sh"}
	]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 2 {
		t.Fatalf("两条都应执行，实得 %d", len(results))
	}
	if results[0].Ok {
		t.Error("第一条应失败")
	}
	if !results[1].Ok {
		t.Errorf("第二条应成功（失败不中断），实得: %q", results[1].Output)
	}
}

// TestRunForEventUntrustedDoesNotExecute —— **安全不变量的端到端确认**。
//
// 未授信项目：即使 hooks.json 合法、脚本存在且可执行，也**不得执行**。
// 这是本包存在的理由——脚本拿完整用户权限，不能自我授权。
func TestRunForEventUntrustedDoesNotExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 shebang")
	}
	dir := t.TempDir()
	// 脚本会写一个哨兵文件——若它被执行，哨兵就会出现。
	sentinel := filepath.Join(dir, "EXECUTED")
	script := filepath.Join(dir, "danger.sh")
	body := "#!/bin/sh\ntouch " + sentinel + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"danger.sh"}]}`)
	t.Setenv("RIVET_TRUST_PROJECT", "0")

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	if len(results) != 0 {
		t.Fatalf("未授信不应执行任何脚本，实得 %d 条", len(results))
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("**安全违规**：未授信项目的脚本被执行了（哨兵文件出现）")
	}
}

// TestBuildEnvIncludesAntiInteractive —— 防交互变量注入环境。
//
// 不变量：这 8 个变量必须出现在脚本环境里——否则 hook 里跑 git/npm 会阻塞
// 等用户输入（agent 主循环无人应答）。
//
// ## 为什么断言必须查 map 而非 os.Environ 结果
//
// M258 变异（删掉 `"PAGER": "cat"`）曾 0 红——**两层原因**：
//
//  1. 首版断言用 `strings.Contains(joined, "PAGER=cat")`，被 `GIT_PAGER=cat`
//     这个**子串**满足（弱断言）。
//  2. 改用精确行匹配后**仍然 0 红**——实测发现**宿主环境本来就设了
//     `PAGER=cat`**（本机 env 确认），故 `os.Environ()` 里的那条让断言通过，
//     无法区分「我们注入的」与「宿主自带的」。
//
// 故本测试**直接断言 antiInteractiveEnv 表本身**（不依赖宿主环境），
// 另用 `TestBuildEnvOverridesHostPager` 覆盖「覆盖语义」。
func TestBuildEnvIncludesAntiInteractive(t *testing.T) {
	// ① 表本身：8 个变量逐条精确匹配（对账 TS 的 ANTI_INTERACTIVE_ENV）。
	want := map[string]string{
		"PAGER":               "cat",
		"GIT_PAGER":           "cat",
		"GH_PAGER":            "cat",
		"MANPAGER":            "cat",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_EDITOR":          "true",
		"GIT_SEQUENCE_EDITOR": "true",
		"GPG_TTY":             "",
	}
	if len(antiInteractiveEnv) != len(want) {
		t.Errorf("antiInteractiveEnv 应有 %d 项，实得 %d: %v",
			len(want), len(antiInteractiveEnv), antiInteractiveEnv)
	}
	for k, v := range want {
		got, ok := antiInteractiveEnv[k]
		if !ok {
			t.Errorf("antiInteractiveEnv 缺少 %q", k)
			continue
		}
		if got != v {
			t.Errorf("antiInteractiveEnv[%q] = %q，期望 %q", k, got, v)
		}
	}

	// ② 上下文变量：同样直接断言注入结果里含精确项。
	r := &Runner{Cwd: "/tmp"}
	env := r.buildEnv(HookContext{Event: EventPreTurn, SessionID: "s1", Turn: 3, ToolName: "grep"})
	exact := map[string]int{}
	for _, e := range env {
		exact[e]++
	}
	for _, want := range []string{
		"RIVET_HOOK_EVENT=preTurn", "RIVET_SESSION_ID=s1",
		"RIVET_TURN=3", "RIVET_TOOL_NAME=grep",
	} {
		if exact[want] == 0 {
			t.Errorf("环境缺少精确项 %q", want)
		}
	}
}

// TestBuildEnvOverridesHostPager —— **覆盖语义**：注入的防交互变量在宿主之后。
//
// 不变量（对账 TS 的 `{ ...process.env, ...ANTI_INTERACTIVE_ENV }`）：
// 若宿主设了 `PAGER=less`，我们注入的 `PAGER=cat` 必须**在它之后**出现
// （Unix exec 取最后一个 = 我们的生效）。
//
// **本测试用计数而非存在性**——宿主可能已有 `PAGER=cat`（本机就是），
// 存在性断言无法区分来源。
func TestBuildEnvOverridesHostPager(t *testing.T) {
	t.Setenv("PAGER", "less")
	t.Setenv("GIT_PAGER", "less")

	r := &Runner{Cwd: "/tmp"}
	env := r.buildEnv(HookContext{Event: EventPreTurn})

	// 找到每个键的最后一次出现——那才是生效值。
	lastVal := map[string]string{}
	for _, e := range env {
		if i := indexByte(e, '='); i >= 0 {
			lastVal[e[:i]] = e[i+1:]
		}
	}
	if lastVal["PAGER"] != "cat" {
		t.Errorf("PAGER 生效值应为 cat（覆盖宿主 less），实得 %q", lastVal["PAGER"])
	}
	if lastVal["GIT_PAGER"] != "cat" {
		t.Errorf("GIT_PAGER 生效值应为 cat，实得 %q", lastVal["GIT_PAGER"])
	}
}

// indexByte 是 strings.IndexByte 的本地包装（避免额外 import）。
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// TestDefaultTimeoutAppliesWhenUnset —— **缺省超时生效**（M255 暴露的盲区）。
//
// M255 变异（默认超时改成 60000）曾 0 红——因为超时测试用的是**显式**
// `timeoutMs: 400`，从不走默认分支。本测试用**不设 timeoutMs** 的配置跑一个
// 长脚本，断言它在合理时间内返回（即默认 5000ms 生效，而非无限等待）。
func TestDefaultTimeoutAppliesWhenUnset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 版在 user_hooks_windows_test.go 覆盖")
	}
	dir := t.TempDir()
	trustProject(t)
	// 睡 30 秒——若默认超时失效，测试会挂很久
	script := filepath.Join(dir, "long.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	// **不设 timeoutMs** → 走 DefaultTimeoutMs (5000)
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"long.sh"}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	start := time.Now()
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(results))
	}
	if results[0].Ok {
		t.Error("超长脚本应因默认超时失败")
	}
	// 默认 5s——留余量断言 < 15s（若走 60000 会远超）
	if elapsed > 15*time.Second {
		t.Errorf("默认超时未生效——耗时 %v（应约 5s）", elapsed)
	}
}

// TestDefaultTimeoutConstantValue —— 缺省超时常量值对账 TS 的 `?? 5000`。
func TestDefaultTimeoutConstantValue(t *testing.T) {
	if DefaultTimeoutMs != 5000 {
		t.Errorf("DefaultTimeoutMs = %d，期望 5000（对账 TS 的 ?? 5000）", DefaultTimeoutMs)
	}
}

// TestValidEventsCoversAllFive —— 5 个事件全部合法（对账 TS 的 VALID_EVENTS）。
func TestValidEventsCoversAllFive(t *testing.T) {
	for _, e := range []HookEvent{EventPreTurn, EventPostTurn, EventPostTool, EventPostSession, EventOnError} {
		if !ValidEvents[e] {
			t.Errorf("%q 应在 ValidEvents 中", e)
		}
	}
	if len(ValidEvents) != 5 {
		t.Errorf("ValidEvents 应有 5 个成员，实得 %d", len(ValidEvents))
	}
}

// TestTrustOverrideEnv —— trust 的环境变量覆盖生效（前置条件自检）。
//
// **为什么需要**：本包全部安全测试都依赖 `RIVET_TRUST_PROJECT` 覆盖。
// 若该变量失效，未授信测试会假绿（实际走了「默认未授信」路径）或假红。
// 断言前置条件，避免静默空操作。
func TestTrustOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RIVET_TRUST_PROJECT", "1")
	if !trust.IsProjectTrusted(dir) {
		t.Fatal("RIVET_TRUST_PROJECT=1 应视为已授信——后续测试的前提失效")
	}
	t.Setenv("RIVET_TRUST_PROJECT", "0")
	if trust.IsProjectTrusted(dir) {
		t.Fatal("RIVET_TRUST_PROJECT=0 应视为未授信")
	}
}
