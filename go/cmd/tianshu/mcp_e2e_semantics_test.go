package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mcp_e2e_semantics_test.go —— 第一百一十刀 W3/W4 的端到端验收（V10–V12）。
//
// 复用 mcp_e2e_test.go 的夹具（`fakeMcpServerScript` / `captureBodyServer` /
// `buildCLIBinary`）——同包，不重复定义。

// TestCLIMCPEnabledOmittedStillRegisters —— ★ V10：省略 `enabled` 键仍注册工具。
//
// **为什么这是端到端而非单测**：单测已覆盖 `EnabledOrDefault()` 的取值；
// 但「取值对了却没走到连接」是另一层。本用例证明**用户照 TS 写法配
// （不写 enabled）时，工具真的出现在发往模型的请求体里**。
//
// 对账 TS `src/mcp/config.ts:83` 的 `enabled: z.boolean().default(true)`。
func TestCLIMCPEnabledOmittedStillRegisters(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	bin := buildCLIBinary(t, "tianshu-mcp-noenabled-e2e")
	dir := t.TempDir()

	srvPath := filepath.Join(dir, "fake_mcp.sh")
	if err := os.WriteFile(srvPath, []byte(fakeMcpServerScript()), 0o755); err != nil {
		t.Fatal(err)
	}

	// ★ 配置里**不写 enabled**——这是用户照 TS 文档写配置的自然形态
	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"mcp":{"servers":{"probe":{"command":"` + srvPath + `"}}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, getBodies := captureBodyServer(t)

	cmd := exec.Command(bin, "-p", "hi", "--base-url", baseURL, "--model", "test-model")
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_KEY=test-key",
		"RIVET_CONFIG_PATH="+cfgPath,
	)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI 报错：%v\n%s", err, out)
	}

	bodies := getBodies()
	if len(bodies) == 0 {
		t.Fatalf("没捕获到请求体\nCLI 输出：%s", out)
	}
	if !strings.Contains(bodies[0], "mcp__probe__probe_tool") {
		t.Errorf("★ 省略 enabled 键时 MCP 工具仍应注册（对齐 TS .default(true)）。\n"+
			"首个请求体片段：%.2000s", bodies[0])
	}
}

// TestCLIMCPURLServerPrintsDiagnostic —— ★ V11：url 型 server 给出明确诊断。
//
// 修 finding #2 的端到端面：用户配了 url 型 server，
// **必须**在 stderr 看到「已跳过 + 原因」，且进程不挂、其余 stdio server 正常。
func TestCLIMCPURLServerPrintsDiagnostic(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	bin := buildCLIBinary(t, "tianshu-mcp-url-e2e")
	dir := t.TempDir()

	srvPath := filepath.Join(dir, "fake_mcp.sh")
	if err := os.WriteFile(srvPath, []byte(fakeMcpServerScript()), 0o755); err != nil {
		t.Fatal(err)
	}

	// 一个 url 型（会被拒）+ 一个 stdio 型（应正常工作）
	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"mcp":{"servers":{
	  "remote1":{"url":"https://example.com/mcp"},
	  "probe":{"command":"` + srvPath + `"}
	}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, getBodies := captureBodyServer(t)

	cmd := exec.Command(bin, "-p", "hi", "--base-url", baseURL, "--model", "test-model")
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_KEY=test-key",
		"RIVET_CONFIG_PATH="+cfgPath,
	)
	cmd.Dir = dir
	// CombinedOutput 同时拿 stdout+stderr——诊断走 stderr
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI 报错（url 型 server 不该让进程失败）：%v\n%s", err, out)
	}

	// ★ 诊断必须出现（静默丢弃正是原缺陷）
	if !strings.Contains(string(out), "remote1") {
		t.Errorf("★ stderr 应诊断被拒的 server（含其 ID），实得：\n%s", out)
	}
	if !strings.Contains(string(out), "已跳过") {
		t.Errorf("诊断应说明「已跳过」，实得：\n%s", out)
	}

	// ★ stdio server 不受影响（单点失败不阻塞其余）
	bodies := getBodies()
	if len(bodies) == 0 {
		t.Fatalf("没捕获到请求体\nCLI 输出：%s", out)
	}
	if !strings.Contains(bodies[0], "mcp__probe__probe_tool") {
		t.Errorf("★ 同配置里的 stdio server 应正常注册（一个 url server 不该拖垮其余）。\n"+
			"请求体片段：%.1500s", bodies[0])
	}
}

// TestCLIMCPCtxCancelInterruptsSlowInit —— ★ V12：SIGINT 可中断慢 MCP 初始化。
//
// 修 finding #7 的端到端面（**本刀最严重的一条**）。
//
// # 缺陷原状
//
// `buildLoop` 不收 ctx，`assembleMcpTools` 内部用 `context.Background()` +
// 5s 握手余量 → 用户按 Ctrl+C **完全无效**，进程要等满预算。
//
// # 探针先行（计划的待验证假设 H2，已实测）
//
// 先验证了 SIGINT 能可靠注入 Go 子进程且 `signal.NotifyContext` 秒级响应
// （探针：`kill -INT` 后 0.02s 退出）。故本用例用**真 SIGINT** 而非降级方案。
//
// # 判据
//
// 假 server **永不应答** initialize（`sleep 300`）→ 无 ctx 感知时
// 初始化要等满 `DefaultTimeoutMS+5000 = 65s`。
// 发 SIGINT 后进程应在**秒级**退出；若仍要等满预算，本用例超时失败。
func TestCLIMCPCtxCancelInterruptsSlowInit(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	bin := buildCLIBinary(t, "tianshu-mcp-sigint-e2e")
	dir := t.TempDir()

	// 哑 server：读 stdin 但**永不应答**（模拟卡在启动期的 server）
	dumb := filepath.Join(dir, "dumb.sh")
	if err := os.WriteFile(dumb, []byte("#!/bin/sh\nsleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"mcp":{"servers":{"slow":{"command":"` + dumb + `"}}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, _ := captureBodyServer(t)

	cmd := exec.Command(bin, "-p", "hi", "--base-url", baseURL, "--model", "test-model")
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_KEY=test-key",
		"RIVET_CONFIG_PATH="+cfgPath,
	)
	cmd.Dir = dir
	// 独立进程组，便于清理（哑 server 的子进程）
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// 兜底清理：测试结束（含超时 Fatal）时确保进程树被回收
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})

	// 等进程进入「正在等 MCP 初始化」的状态（假 server 已起、握手未回）
	time.Sleep(1200 * time.Millisecond)

	// ★ 发 SIGINT
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("发 SIGINT 失败：%v", err)
	}

	// 等退出，最多 10s（远小于 65s 的初始化预算）
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// 秒级退出——正确（具体耗时由上面的 Sleep 与调度决定，
		// 关键是**远早于** 65s 预算）
	case <-time.After(10 * time.Second):
		t.Fatalf("★ SIGINT 后 10s 内未退出——说明 signal ctx 未传到 MCP 初始化" +
			"（finding #7 未修：进程会等满 65s 预算）")
	}
}
