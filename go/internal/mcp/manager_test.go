package mcp

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// manager_test.go —— MCP 生命周期端到端（V7 的验证面）。
//
// **为什么必须端到端**：本刀的核心主张是「能连、能发现、能调用」。
// 用 mock transport 只能证明「我按我以为的协议发了帧」；真子进程假 server
// 才能证明「对端按 MCP 规范理解了我的帧，且我理解了对端的响应」。
// 这正是 `<task-depth layer="wiring">` 要求的「实例化真实依赖」。

// fakeServerConfig 组装指向假 server 的 Manager 配置。
func fakeServerConfig(serverID string) Config {
	return Config{
		Enabled: true,
		Servers: map[string]ServerConfig{
			serverID: {
				Command: os.Args[0],
				Args:    []string{"-test.run=TestMain"},
				Env:     map[string]string{fakeServerMode: "1"},
			},
		},
	}
}

// TestManagerInitializeDiscoversTools —— ★ V7 主干：initialize → tools/list → 包装。
func TestManagerInitializeDiscoversTools(t *testing.T) {
	m := NewManager(fakeServerConfig("srv"), "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	all := m.AllTools()
	if len(all) != 2 {
		names := make([]string, 0, len(all))
		for _, tl := range all {
			names = append(names, tl.Definition().Name)
		}
		t.Fatalf("应发现 2 个工具，实得 %d：%v", len(all), names)
	}

	// 工具名必须是 mcp__<id>__<name>（对账 policy.go 的 ToolName）
	got := make(map[string]bool)
	for _, tl := range all {
		got[tl.Definition().Name] = true
	}
	if !got["mcp__srv__echo"] {
		t.Errorf("缺 mcp__srv__echo，实得 %v", got)
	}
	// ★ 双下划线工具名：`write__thing` → 名字里是 `write_thing`（单遍替换）
	if !got["mcp__srv__write_thing"] {
		t.Errorf("缺 mcp__srv__write_thing（双下划线折叠），实得 %v", got)
	}

	// 状态应为 connected
	states := m.States()
	if len(states) != 1 || states[0].Status != StatusConnected {
		t.Fatalf("状态应为 connected，实得 %+v", states)
	}
	if states[0].ToolCount != 2 {
		t.Errorf("ToolCount 应为 2，实得 %d", states[0].ToolCount)
	}
}

// TestManagerCallToolRoundTrip —— ★ 端到端通话（真子进程往返）。
func TestManagerCallToolRoundTrip(t *testing.T) {
	m := NewManager(fakeServerConfig("srv"), "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	var echo tools.Tool
	for _, tl := range m.AllTools() {
		if tl.Definition().Name == "mcp__srv__echo" {
			echo = tl
		}
	}
	if echo == nil {
		t.Fatal("未找到 echo 工具")
	}

	res, err := echo.Execute(context.Background(), &tools.CallParams{
		ToolUseID: "t1",
		Input:     map[string]any{"text": "hi"},
	})
	if err != nil {
		t.Fatalf("Execute 报错：%v", err)
	}
	if res.IsError {
		t.Fatalf("不该是错误：%s", res.Content)
	}
	// 假 server 回 "echo:hi"
	if !strings.Contains(res.Content, "echo:hi") {
		t.Errorf("正文应为 echo:hi，实得 %q", res.Content)
	}
	// 且带 MCP 注解
	if !strings.Contains(res.Content, "[MCP: srv · ") {
		t.Errorf("应含注解，实得 %q", res.Content)
	}
}

// TestManagerServerErrorSurfacesAsResult —— server 端 JSON-RPC error → Result.IsError。
//
// 假 server 的 `boom` 方法回 code -32603。经 `tools/call` 调用某个工具时
// 走的是 tools/call 分支——故这里直接调 RPC 层验证错误传播，再由 wrapper
// 的 error 路径覆盖（见 wrapper_test.go 的 TestWrapToolCallErrorClassified）。
func TestManagerServerErrorSurfacesAsResult(t *testing.T) {
	m := NewManager(fakeServerConfig("srv"), "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	m.mu.Lock()
	conn := m.conns["srv"]
	m.mu.Unlock()
	if conn == nil {
		t.Fatal("无连接")
	}

	// `boom` 是假 server 定义的错误方法——tools/call 之外的直接 RPC
	_, err := conn.rpc.Request("boom", nil, DefaultTimeoutMS)
	if err == nil {
		t.Fatal("server 报错应向上传播")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误信息应含 server 的 message，实得 %q", err.Error())
	}
}

// TestManagerDisconnectedServerErrorsOnCall —— ★ 断连后调用报「明确错误」。
//
// 对账 `manager.ts` 的 perToolCallFn：
//
//	if (!this.connections.has(serverId)) {
//	  throw new Error(`MCP server "${serverId}" is disconnected`)
//	}
//
// **为什么这是关键行为**：不给明确错误而是静默失败，会让模型以为
// 「该工具不存在」而非「连接断了」——两者的修复动作完全不同。
func TestManagerDisconnectedServerErrorsOnCall(t *testing.T) {
	m := NewManager(fakeServerConfig("srv"), "")

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	// 拿到工具引用后关闭连接
	var echo tools.Tool
	for _, tl := range m.AllTools() {
		if tl.Definition().Name == "mcp__srv__echo" {
			echo = tl
		}
	}
	if echo == nil {
		t.Fatal("未找到 echo 工具")
	}

	m.Shutdown() // 连接表已清空

	res, err := echo.Execute(context.Background(), &tools.CallParams{ToolUseID: "t1"})
	if err != nil {
		t.Fatalf("Execute 不该向上抛（应转成 Result）：%v", err)
	}
	if !res.IsError {
		t.Fatal("断连后调用应标记为错误")
	}
	if !strings.Contains(res.Content, "disconnected") {
		t.Errorf("错误信息应明示断连（而非静默/含混），实得 %q", res.Content)
	}
}

// TestManagerDisabledConfigIsNoop —— 配置未启用时不连（对账 TS 的 enabled 门）。
func TestManagerDisabledConfigIsNoop(t *testing.T) {
	m := NewManager(Config{Enabled: false}, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("未启用时 Initialize 不该报错：%v", err)
	}
	if len(m.AllTools()) != 0 {
		t.Errorf("未启用时不该有工具，实得 %d", len(m.AllTools()))
	}
	if len(m.States()) != 0 {
		t.Errorf("未启用时不该有状态，实得 %d", len(m.States()))
	}
}

// TestManagerDisabledServerSkipped —— ServerConfig.Disabled=true 的 server 被跳过。
func TestManagerDisabledServerSkipped(t *testing.T) {
	cfg := fakeServerConfig("srv")
	sc := cfg.Servers["srv"]
	sc.Disabled = true
	cfg.Servers["srv"] = sc

	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}
	if len(m.AllTools()) != 0 {
		t.Errorf("disabled server 不该被连接，实得 %d 工具", len(m.AllTools()))
	}
}

// TestManagerBadCommandRecordsErrorState —— ★ 路径不存在 → 状态记错误（不 panic、不永挂）。
//
// 对账 TS：单 server 失败不阻塞 Initialize（「一个挂了不该让会话起不来」）。
func TestManagerBadCommandRecordsErrorState(t *testing.T) {
	m := NewManager(Config{
		Enabled: true,
		Servers: map[string]ServerConfig{
			"broken": {Command: "/nonexistent/definitely-not-here-12345"},
		},
	}, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("单 server 失败不该让 Initialize 报错：%v", err)
	}
	states := m.States()
	if len(states) != 1 {
		t.Fatalf("应有 1 条状态，实得 %d", len(states))
	}
	if states[0].Status != StatusError {
		t.Errorf("状态应为 error，实得 %q", states[0].Status)
	}
	if states[0].Error == "" {
		t.Error("错误状态应带错误信息")
	}
	if states[0].ErrorHint == "" {
		t.Error("错误状态应带 suggestion（供模型给出可执行下一步）")
	}
}

// TestManagerAllToolsOrderStable —— ★ 工具序稳定（前缀缓存关键）。
//
// 工具定义进请求体的 `tools` 段；顺序不定 → 每次请求字节不同 → 前缀缓存失效。
//
// **必须用 ≥2 个 server**（变异 M5 的分诊产物）：初版只用 1 个 server，
// 而 `conns` 是 map——**单元素的 map 遍历序当然稳定**，于是删掉
// `sort.Strings(ids)` 后测试仍绿（M5 红 0）。测序稳定性必须有多元
// 才能暴露 map 遍历的随机性。
func TestManagerAllToolsOrderStable(t *testing.T) {
	// 三个 server（不同 id、同一个假 server 命令）——多元才暴露序问题
	cfg := Config{
		Enabled: true,
		Servers: map[string]ServerConfig{
			"zeta":  {Command: os.Args[0], Args: []string{"-test.run=TestMain"}, Env: map[string]string{fakeServerMode: "1"}},
			"alpha": {Command: os.Args[0], Args: []string{"-test.run=TestMain"}, Env: map[string]string{fakeServerMode: "1"}},
			"mid":   {Command: os.Args[0], Args: []string{"-test.run=TestMain"}, Env: map[string]string{fakeServerMode: "1"}},
		},
	}
	m := NewManager(cfg, "")
	defer m.Shutdown()

	if err := m.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize 报错：%v", err)
	}

	first := m.AllTools()
	if len(first) != 6 { // 3 server × 2 工具
		t.Fatalf("应有 6 个工具，实得 %d", len(first))
	}

	// 序必须是 serverID 字典序：alpha < mid < zeta
	wantPrefix := []string{"mcp__alpha__", "mcp__mid__", "mcp__zeta__"}
	for i, p := range wantPrefix {
		if !strings.HasPrefix(first[i*2].Definition().Name, p) {
			t.Errorf("位置 %d 应为 %s…，实得 %q", i*2, p, first[i*2].Definition().Name)
		}
	}

	// 重复调用序不变（Go 的 map 遍历序随机 → 无排序时多次调用会变）
	for i := 0; i < 20; i++ {
		again := m.AllTools()
		if len(again) != len(first) {
			t.Fatalf("第 %d 次调用长度变了：%d vs %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Definition().Name != first[j].Definition().Name {
				t.Fatalf("第 %d 次调用序变了：位置 %d 是 %q，原为 %q",
					i, j, again[j].Definition().Name, first[j].Definition().Name)
			}
		}
	}
}
