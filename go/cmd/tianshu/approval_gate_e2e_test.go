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
	"sync/atomic"
	"testing"
)

// approval_gate_e2e_test.go —— 档位审批门的**真实 CLI 二进制**验收。
//
// 验证单测覆盖不到的一环：**档位真的从 CLI flag 传到工具执行层**。
// 单测直接构造 `Config{ApprovalMode:...}`，而 CLI 路径是
// `--approval <mode>` → `loadConfig` → `Config` → `loop.executeTool`——
// 中间任一处漏传，单测仍全绿但用户行为错误（本项目已多次踩过）。

// TestCLIEndToEndManualBlocksWrite —— manual 档下写操作在真实 CLI 被拦。
func TestCLIEndToEndManualBlocksWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
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
				"file_path": filepath.Join(root, "gated.txt"),
				"content":   "hi",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-gate-manual-test")
	cmd := exec.Command(bin,
		"-p", "写个文件",
		"--base-url", srv.URL,
		"--model", "test-model",
		"--approval", "manual", // ← 关键：真实 CLI flag
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	if len(bodies) < 2 {
		t.Fatalf("应至少 2 个请求，实得 %d\nCLI 输出：%s", len(bodies), out)
	}
	// 观察 1：拒绝文案回灌（第二轮请求含工具结果）。
	if !strings.Contains(bodies[1], "需人工批准") {
		t.Errorf("manual 档下 write_file 应被拦且文案回灌，第二轮请求片段：%.800s", bodies[1])
	}
	// 观察 2：**文件未被创建**（拦截真生效，非仅文案）。
	if _, err := os.Stat(filepath.Join(root, "gated.txt")); err == nil {
		t.Error("**文件被创建了**——manual 档拦截未生效")
	}
}

// TestCLIEndToEndAutoSafeAllowsWrite —— auto-safe 档（默认）下写操作正常执行。
//
// **这是本刀最重要的回归**：若档位分支误用 `needsApproval` 而非 `isHighRisk`，
// 默认档下所有写操作都会被拦——用户装好 CLI 后连写文件都不行。
func TestCLIEndToEndAutoSafeAllowsWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("c1", "write_file", map[string]any{
				"file_path": filepath.Join(root, "allowed.txt"),
				"content":   "hi",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-gate-autosafe-test")
	// 不传 --approval → 默认 auto-safe。
	cmd := exec.Command(bin, "-p", "写个文件", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察：文件**真被写入**（默认档不该拦写操作）。
	data, err := os.ReadFile(filepath.Join(root, "allowed.txt"))
	if err != nil {
		t.Fatalf("auto-safe 档下文件应被写入，实得错误：%v\nCLI 输出：%s", err, out)
	}
	if strings.TrimSpace(string(data)) != "hi" {
		t.Errorf("文件内容应为 hi，实得 %q", string(data))
	}
}
