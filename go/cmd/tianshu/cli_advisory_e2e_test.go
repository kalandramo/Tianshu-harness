package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCLIEndToEndAdvisoryReachesRequest —— **真实 CLI 二进制验收**：
//
// 用户动作：构建并运行 `tianshu -p "..."`（真实二进制，非内部函数调用），
// 模型（mock SSE 端点）调 write_file 写 .ts + run_tests。
// 观察到：**发往端点的请求体里出现 <星域-advisory> 块**。
//
// **为什么必须跑真实二进制**：前四轮我三次踩同一坑——组件在内部测试里全绿，
// 但生产入口（CLI）根本没装配它们。`buildLoop` 是内部函数，测它仍是「内部
// 视角」；只有真跑二进制才能证明**用户实际路径**通。
func TestCLIEndToEndAdvisoryReachesRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	target := filepath.Join(root, "foo.ts")
	if err := os.WriteFile(target, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// mock 端点：按脚本返回 SSE，并记录收到的请求体
	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("c1", "write_file", map[string]any{
				"file_path": target,
				"content":   "export const x = 1\n",
			}))
		case 2:
			fmt.Fprint(w, sseToolCall("c2", "run_tests", `{}`))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	// 构建真实二进制
	bin := buildCLIBinary(t, "tianshu-test")

	cmd := exec.Command(bin,
		"-p", "改一下 foo.ts 并跑测试",
		"--base-url", srv.URL,
		"--model", "test-model",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	if len(bodies) == 0 {
		t.Fatalf("端点未收到请求\nCLI 输出：%s", out)
	}

	// ── 观察：请求体里出现 advisory 块 ──
	found := false
	for i, b := range bodies {
		if strings.Contains(b, "星域-advisory") || strings.Contains(b, "你改了 TS 文件") {
			found = true
			t.Logf("请求 %d 含 advisory 块", i)
		}
	}
	if !found {
		t.Errorf("**advisory 未进入真实 CLI 的请求体**（共 %d 个请求）。\n"+
			"这说明 CLI 未装配 AdvisoryBus——组件在内部测试全绿但生产路径未生效。\n"+
			"CLI 输出：%s", len(bodies), out)
	}
}

// TestCLIEndToEndHooksReached —— 真实 CLI 路径下 hook 被触发。
//
// 通过 advisory 间接验证：typecheck-reminder 触发了才会投递 advisory。
func TestCLIEndToEndHooksReached(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	target := filepath.Join(root, "bar.ts")
	if err := os.WriteFile(target, []byte("export const y = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("c1", "write_file", map[string]any{
				"file_path": target,
				"content":   "export const y = 2\n",
			}))
		case 2:
			fmt.Fprint(w, sseToolCall("c2", "run_tests", `{}`))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-test")

	cmd := exec.Command(bin, "-p", "改 bar.ts", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	found := false
	for _, b := range bodies {
		if strings.Contains(b, "你改了 TS 文件") {
			found = true
		}
	}
	if !found {
		t.Errorf("hook 未在真实 CLI 路径触发（typecheck-reminder 未投递）")
	}
}

// ── SSE 构造辅助（与 agent 包测试同构，此处独立以免跨包依赖）──

func sseText(text string) string {
	return `data: {"choices":[{"delta":{"content":` + jsonStr(text) + `}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

func sseToolCall(id, name, args string) string {
	payload := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":` + jsonStr(id) +
		`,"type":"function","function":{"name":` + jsonStr(name) +
		`,"arguments":` + jsonStr(args) + `}}]}}]}`
	return "data: " + payload + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

// sseToolCallArgs 同 sseToolCall，但参数用 json.Marshal 序列化。
//
// **为什么需要它**：`sseToolCall(..., `{"file_path":"`+path+`"}`)` 在 Windows
// 上产出 `{"file_path":"C:\Users\..."}`——`\U` 是**非法 JSON 转义**，模型侧
// 参数解析失败（工具报 error），测试于是以「hook 未触发」等间接症状失败，
// 掩盖真实根因（夹具坏了，不是链路断了）。
//
// jsonStr 只转义**外层**嵌入，救不了本来就是非法 JSON 的 args 输入。
// 任何嵌入路径的 args 都必须经这里构造，不要手拼。
func sseToolCallArgs(id, name string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		panic("sseToolCallArgs: 参数不可序列化：" + err.Error())
	}
	return sseToolCall(id, name, string(b))
}

func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
