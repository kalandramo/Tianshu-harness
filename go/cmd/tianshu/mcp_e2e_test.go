package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mcp_e2e_test.go —— MCP 工具的**装配层可达性**（本刀 V10）。
//
// **为什么必须走真实 CLI 二进制**（本项目反复踩的坑）：
// `internal/mcp` 的单测全绿只证明「组件自身对」，不证明**它被装上了**。
// 前几轮已多次遇到「组件内部测试全绿，但 CLI 没装配」——`buildLoop` 是
// 内部函数，测它仍是内部视角。只有真跑二进制才能证明**用户实际路径**通
// （对账 gitscout_e2e_test.go 的同一理由）。
//
// V10 的两条（计划 §验证清单 V10）：
//  1. **无 mcp 配置**时工具数不变（不回归基线）
//  2. **有 mcp 配置**时 MCP 工具出现在发往端点的请求体 `tools` 段

// fakeMcpServerScript 返回一个极简 MCP server 的 shell 脚本内容。
//
// **为什么用 shell 脚本而非 Go 程序**：零编译、可读性最好，且本用例要验的是
// **装配**（工具是否进了请求体），不是协议细节——协议细节有
// `internal/mcp/fakeserver_test.go` 的同系列用例覆盖。
func fakeMcpServerScript() string {
	return `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{}}}\n' "$id"
      ;;
    *'"method":"notifications/initialized"'*)
      : ;;
    *'"method":"tools/list"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"probe_tool","description":"PROBE-TOOL-DESC","inputSchema":{"type":"object","properties":{"x":{"type":"string"}}}}]}}\n' "$id"
      ;;
    *'"method":"tools/call"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"probe-ok"}]}}\n' "$id"
      ;;
  esac
done
`
}

// captureBodyServer 起一个捕获请求体的假端点（SSE 回一个纯文本回复）。
//
// 返回 URL 与「取已捕获请求体」的函数。
func captureBodyServer(t *testing.T) (string, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var bodies []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseText("done"))
	}))
	t.Cleanup(srv.Close)

	return srv.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(bodies))
		copy(out, bodies)
		return out
	}
}

// TestCLIMCPToolsAppearInRequest —— ★ V10 主干：配置了 MCP server 后，
// 其工具出现在真实请求体的 tools 段。
func TestCLIMCPToolsAppearInRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	bin := buildCLIBinary(t, "tianshu-mcp-e2e")

	dir := t.TempDir()
	srvPath := filepath.Join(dir, "fake_mcp.sh")
	if err := os.WriteFile(srvPath, []byte(fakeMcpServerScript()), 0o755); err != nil {
		t.Fatal(err)
	}

	// 配置文件：mcp 为**顶层**键（对账 TS schema.ts:1038）
	cfgPath := filepath.Join(dir, "config.json")
	cfgJSON := `{"mcp":{"enabled":true,"servers":{"probe":{"command":"` + srvPath + `"}}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL, getBodies := captureBodyServer(t)

	cmd := exec.Command(bin, "-p", "hi", "--base-url", baseURL, "--model", "test-model")
	// DEEPSEEK_API_KEY 是 loadConfig 的第一顺位（对账 main.go:firstEnv）；
	// RIVET_CONFIG_PATH 由 rivetpath.UserConfigPath 读取（已核实存在）。
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

	first := bodies[0]
	if !strings.Contains(first, "mcp__probe__probe_tool") {
		t.Errorf("**MCP 工具未进入请求体 tools 段**——组件测试全绿但生产路径未装配。\n"+
			"首个请求体片段：%.2000s", first)
	}
	if !strings.Contains(first, "PROBE-TOOL-DESC") {
		t.Errorf("MCP 工具的 description 未进请求体。\n首个请求体片段：%.1200s", first)
	}
}

// TestCLIMCPNoConfigKeepsToolsUnchanged —— ★ V10 回归面：未配置 MCP 时无 mcp 工具。
//
// **为什么必须有这条**：接线后若 MCP 装配路径无条件注册（哪怕配置为空），
// 会把「无 MCP」用户的请求体也改掉 → 破坏既有前缀缓存。
// 本用例与上一条构成**双向**验证：配了就进、没配就不进。
func TestCLIMCPNoConfigKeepsToolsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	bin := buildCLIBinary(t, "tianshu-mcp-nocfg-e2e")
	dir := t.TempDir()

	// 无 mcp 段的配置
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"agent":{"permissions":{"allow":[]}}}`), 0o644); err != nil {
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
	if strings.Contains(bodies[0], "mcp__") {
		t.Errorf("未配置 MCP 时不该出现 mcp__ 工具。\n请求体片段：%.1200s", bodies[0])
	}
}
