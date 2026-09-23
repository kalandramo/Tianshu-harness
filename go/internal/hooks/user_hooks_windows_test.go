//go:build windows

package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Windows 上脚本执行路径的覆盖。
//
// **为什么单独一个文件**：POSIX 的 shebang 在 Windows 无效，故主测试文件里
// 5 个执行类用例被 skip——那是**真实覆盖缺口**，不是可接受的豁免。
// 本文件用 `.bat` 补上同样的行为断言（执行/stdin/超时/不中断/安全门）。
//
// **注意**：`exec.Command` 在 Windows 上能直接执行 `.bat`（经 CreateProcess
// 的批处理关联）——无需 `shell: true`。

func writeBat(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	// .bat 需要 CRLF 才可靠（某些 cmd.exe 版本对 LF 解析不稳）
	content := strings.ReplaceAll(body, "\n", "\r\n")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写 .bat 失败: %v", err)
	}
	return p
}

// TestRunForEventExecutesScriptWindows —— 真实执行 .bat，捕获输出。
func TestRunForEventExecutesScriptWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	writeBat(t, dir, "hello.bat", "@echo off\necho event=%RIVET_HOOK_EVENT%\n")
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"hello.bat"}]}`)

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

// TestRunForEventStdinJSONWindows —— 脚本经 stdin 收到完整 JSON 上下文。
func TestRunForEventStdinJSONWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	// `more` 把 stdin 原样转 stdout（Windows 内置）
	writeBat(t, dir, "dump.bat", "@echo off\nmore\n")
	writeHooks(t, dir, `{"hooks":[{"event":"postTool","script":"dump.bat"}]}`)

	r := &Runner{Cwd: dir, SessionID: "sess-1", GetTurn: func() int { return 7 }}
	results := r.RunForEvent(HookContext{
		Event: EventPostTool, Cwd: dir, SessionID: "sess-1", Turn: 7,
		ToolName: "read_file", ToolResult: "success",
	})

	if len(results) != 1 || !results[0].Ok {
		t.Fatalf("执行失败: %+v", results)
	}
	out := strings.TrimSpace(results[0].Output)
	var got HookContext
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdin 不是合法 JSON: %q (%v)", out, err)
	}
	if got.ToolName != "read_file" || got.Turn != 7 || got.SessionID != "sess-1" {
		t.Errorf("stdin 上下文不符: %+v", got)
	}
}

// TestRunForEventTimeoutWindows —— 超时脚本被杀。
func TestRunForEventTimeoutWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	// ping 一个不可达地址 = 长等待（比 timeout 命令更可移植）
	writeBat(t, dir, "slow.bat", "@echo off\nping -n 30 127.0.0.1 >nul\n")
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"slow.bat","timeoutMs":400}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	start := time.Now()
	results := r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("应有 1 条结果，实得 %d", len(results))
	}
	if results[0].Ok {
		t.Error("超时脚本应 ok=false")
	}
	// 400ms 超时不应等满 ping 的 ~29 秒
	if elapsed > 10*time.Second {
		t.Errorf("超时未生效——耗时 %v（应 < 10s）", elapsed)
	}
}

// TestRunForEventContinuesAfterFailureWindows —— 单条失败不中断后续。
func TestRunForEventContinuesAfterFailureWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	writeBat(t, dir, "fail.bat", "@echo off\nexit /b 3\n")
	writeBat(t, dir, "ok.bat", "@echo off\necho fine\n")
	writeHooks(t, dir, `{"hooks":[
		{"event":"preTurn","script":"fail.bat"},
		{"event":"preTurn","script":"ok.bat"}
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

// TestRunForEventUntrustedDoesNotExecuteWindows —— **安全不变量的端到端确认**。
func TestRunForEventUntrustedDoesNotExecuteWindows(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "EXECUTED.txt")
	writeBat(t, dir, "danger.bat", "@echo off\necho x > \""+sentinel+"\"\n")
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"danger.bat"}]}`)
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

// TestRunForEventTimeoutKillsProcessWindows —— 超时后进程确实终止（无残留）。
func TestRunForEventTimeoutKillsProcessWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	marker := filepath.Join(dir, "LATE.txt")
	// 若进程未被杀，10 秒后会写出标记
	writeBat(t, dir, "late.bat", "@echo off\nping -n 11 127.0.0.1 >nul\necho late > \""+marker+"\"\n")
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"late.bat","timeoutMs":400}]}`)

	r := &Runner{Cwd: dir, GetTurn: func() int { return 0 }}
	r.RunForEvent(HookContext{Event: EventPreTurn, Cwd: dir})

	// 等足够久让「若未杀」的路径有机会写标记
	time.Sleep(12 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("超时后进程仍在运行（标记文件出现）——Kill 未生效")
	}
}

// TestDefaultTimeoutAppliesWhenUnsetWindows —— **缺省超时生效**（M255 盲区）。
//
// 用**不设 timeoutMs** 的配置跑长脚本，断言默认 5000ms 生效。
func TestDefaultTimeoutAppliesWhenUnsetWindows(t *testing.T) {
	dir := t.TempDir()
	trustProject(t)
	// ping 30 次 ≈ 29 秒——若默认超时失效会挂很久
	writeBat(t, dir, "long.bat", "@echo off\nping -n 30 127.0.0.1 >nul\n")
	writeHooks(t, dir, `{"hooks":[{"event":"preTurn","script":"long.bat"}]}`)

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
	// 默认 5s + WaitDelay 2s 兜底 → 应 < 15s
	if elapsed > 15*time.Second {
		t.Errorf("默认超时未生效——耗时 %v（应约 5-7s）", elapsed)
	}
}
