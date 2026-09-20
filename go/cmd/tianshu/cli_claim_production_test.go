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

// TestCLIEndToEndClaimProduced —— **真正的用户级验收（真实二进制）**：
//
// 用户动作：跑真实 CLI（`tianshu -p "..."`），模型调 read_file 读一个源文件。
// 观察到：**`<cwd>/.rivet/claims/<sessionId>.claims.jsonl` 里出现了
// file_observation 类型的 claim**——即 claim 产生端真的工作了。
//
// **这修的是上一轮的失真**：我上轮把「用户在真实会话中 claim 被标记过期」
// 标为 met，但核实发现 `Propose` 无生产调用方——claim store 永远是空的。
// 上轮测试是手工构造装配喂事件，验证的是「接线」，不是「用户路径」。
//
// 这次跑**真实二进制**，观察**磁盘产物**。
func TestCLIEndToEndClaimProduced(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	// 造一个含导出符号的源文件（read_file 提取 file_observation 需要）
	srcFile := filepath.Join(root, "widget.ts")
	if err := os.WriteFile(srcFile, []byte(
		"export function renderWidget() {}\nexport const WIDGET_NAME = 'w';\nexport class Widget {}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	// mock 端点：第 1 轮让模型 read_file，之后终答
	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, sseToolCallArgsCLI2("c1", "read_file", map[string]any{"file_path": srcFile}))
		} else {
			fmt.Fprint(w, sseTextCLI2("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-e2e")

	cmd := exec.Command(bin, "-p", "看看 widget.ts", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// ── 观察 1：请求体里应含 read_file 的工具结果（说明工具真跑了）──
	sawToolResult := false
	for _, b := range bodies {
		if strings.Contains(b, "WIDGET_NAME") || strings.Contains(b, "renderWidget") {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("工具结果未回灌请求——read_file 可能没执行\nCLI 输出：%s", out)
	}

	// ── 观察 2：**磁盘上出现 claim JSONL** ──
	claimsDir := filepath.Join(root, ".rivet", "claims")
	entries, err := os.ReadDir(claimsDir)
	if err != nil {
		t.Fatalf("**claim 目录不存在**（%v）——claim 产生端未工作\nCLI 输出：%s", err, out)
	}

	var jsonlPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".claims.jsonl") {
			jsonlPath = filepath.Join(claimsDir, e.Name())
		}
	}
	if jsonlPath == "" {
		t.Fatalf("**无 claims.jsonl**——claim 未产生\n目录内容：%v", entries)
	}

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("读 claims.jsonl 失败：%v", err)
	}
	content := string(raw)

	// ── 观察 3：JSONL 里有 file_observation claim，且内容含源文件符号 ──
	if !strings.Contains(content, `"type":"claim_proposed"`) {
		t.Errorf("JSONL 里应有 claim_proposed 事件，实际：\n%s", content)
	}
	if !strings.Contains(content, `"kind":"file_observation"`) {
		t.Errorf("应有 file_observation 类型的 claim，实际：\n%s", content)
	}
	// text 应含文件名与符号（提取器的 text 格式：`widget.ts (NL): sym1, sym2`）
	if !strings.Contains(content, "widget.ts") {
		t.Errorf("claim 文本应含文件名 widget.ts，实际：\n%s", content)
	}
	if !strings.Contains(content, "renderWidget") && !strings.Contains(content, "WIDGET_NAME") {
		t.Errorf("claim 文本应含提取出的符号，实际：\n%s", content)
	}

	t.Logf("claim JSONL（%d 字节）：\n%s", len(raw), content)
}

func sseTextCLI2(text string) string {
	return `data: {"choices":[{"delta":{"content":"` + text + `"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

func sseToolCallCLI2(id, name, args string) string {
	return `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id +
		`","type":"function","function":{"name":"` + name + `","arguments":` + jsonStrCLI2(args) + `}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
}

// sseToolCallArgsCLI2 同 sseToolCallCLI2，但参数用 json.Marshal 序列化。
//
// **为什么需要它**：`sseToolCallCLI2(..., `{"file_path":"`+path+`"}`)` 在
// Windows 上产出 `{"file_path":"C:\Users\..."}`——`\U`/`\M` 是**非法 JSON
// 转义**，模型侧参数解析失败（工具报 error），测试于是以「工具结果未回灌」
// 等间接症状失败，掩盖真实根因（夹具坏了，不是链路断了）。
//
// jsonStrCLI2 只转义**外层**嵌入，救不了本来就是非法 JSON 的 args 输入。
// 任何嵌入路径的 args 都必须经这里构造，不要手拼。
func sseToolCallArgsCLI2(id, name string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		panic("sseToolCallArgsCLI2: 参数不可序列化：" + err.Error())
	}
	return sseToolCallCLI2(id, name, string(b))
}

func jsonStrCLI2(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestCLIEndToEndClaimLifecycle —— **完整生命周期验收（真实二进制）**：
//
// 用户动作：跑真实 CLI 两轮——先 read_file 读 widget.ts（产生 claim），
// 再 write_file 改同一个文件。
// 观察到：claims.jsonl 里既有 `claim_proposed`（产生）**也有**
// `claim_status_changed` 到 stale（过期标记）。
//
// **这是从产生到消费的完整链路**——上轮只验证了「装配」，这次验证端到端。
func TestCLIEndToEndClaimLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	srcFile := filepath.Join(root, "lifecycle.ts")
	if err := os.WriteFile(srcFile, []byte(
		"export function alpha() {}\nexport const BETA = 2;\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			// 第一轮：读文件（产生 claim）
			fmt.Fprint(w, sseToolCallArgsCLI2("c1", "read_file", map[string]any{"file_path": srcFile}))
		case 2:
			// 第二轮：写同一文件（应触发 claim 过期）
			fmt.Fprint(w, sseToolCallArgsCLI2("c2", "write_file", map[string]any{
				"file_path": srcFile,
				"content":   "export function alpha() { /* changed */ }\nexport const BETA = 3;\n",
			}))
		default:
			fmt.Fprint(w, sseTextCLI2("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-lifecycle")

	cmd := exec.Command(bin, "-p", "读后改 lifecycle.ts", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// ── 读 claims.jsonl ──
	claimsDir := filepath.Join(root, ".rivet", "claims")
	entries, err := os.ReadDir(claimsDir)
	if err != nil {
		t.Fatalf("claim 目录不存在：%v\nCLI 输出：%s", err, out)
	}
	var content string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".claims.jsonl") {
			raw, _ := os.ReadFile(filepath.Join(claimsDir, e.Name()))
			content += string(raw)
		}
	}
	if content == "" {
		t.Fatal("无 claims.jsonl 内容")
	}

	// ── 观察：产生 + 过期 两个事件都在 ──
	if !strings.Contains(content, `"type":"claim_proposed"`) {
		t.Errorf("缺 claim_proposed（产生端未工作）")
	}
	if !strings.Contains(content, `"type":"claim_status_changed"`) {
		t.Errorf("**缺 claim_status_changed**（过期标记未触发）\n实际内容：\n%s", content)
	}
	if !strings.Contains(content, `"status":"stale"`) {
		t.Errorf("应有 stale 状态变更\n实际内容：\n%s", content)
	}

	t.Logf("生命周期 JSONL：\n%s", content)
}
