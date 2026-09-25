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

// TestCLIEndToEndManualBlocksBashWrite —— manual 档下 bash 写命令在真实 CLI 被拦。
//
// 覆盖单测覆盖不到的一环：**档位真的从 CLI flag 传到 bash 写审批门**。
// 上一刀（第六十二刀）的 e2e 只验了文件工具（write_file），本刀补 bash 路径——
// 两条路径的门不同（档位门 vs bash 写门），必须各验一次。
func TestCLIEndToEndManualBlocksBashWrite(t *testing.T) {
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
			fmt.Fprint(w, sseToolCallArgs("c1", "bash", map[string]any{
				"command": "mkdir cli-gate-probe-dir",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-bashgate-manual-test")
	cmd := exec.Command(bin,
		"-p", "建个目录",
		"--base-url", srv.URL,
		"--model", "test-model",
		"--approval", "manual",
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
	// 观察 1：拒绝文案回灌。
	if !strings.Contains(bodies[1], "需人工批准") {
		t.Errorf("manual 档下 bash mkdir 应被拦且文案回灌，第二轮请求片段：%.800s", bodies[1])
	}
	// 观察 2：**目录真未创建**（拦截真生效）。
	if _, err := os.Stat(filepath.Join(root, "cli-gate-probe-dir")); err == nil {
		t.Error("**目录被创建了**——manual 档下 bash 写审批门未生效")
	}
}

// TestCLIEndToEndAutoSafeAllowsBashWrite —— auto-safe 档（默认）下 bash 写命令正常执行。
//
// **本刀最重要的回归**：若把 `safeWriteInNoSandbox` 的档位依赖漏掉
// （即安全写对所有档都放行/都拦），默认档下 `mkdir` 要么被误拦、要么
// 在 manual 档漏过。这里验默认档放行。
func TestCLIEndToEndAutoSafeAllowsBashWrite(t *testing.T) {
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
			fmt.Fprint(w, sseToolCallArgs("c1", "bash", map[string]any{
				"command": "mkdir cli-autosafe-probe-dir",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-bashgate-autosafe-test")
	// 不传 --approval → 默认 auto-safe。
	cmd := exec.Command(bin, "-p", "建个目录", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察：目录**真被创建**（默认档不该拦安全写）。
	if _, err := os.Stat(filepath.Join(root, "cli-autosafe-probe-dir")); err != nil {
		t.Fatalf("auto-safe 档下 `mkdir` 应被执行，实得错误：%v\nCLI 输出：%s", err, out)
	}
}

// TestCLIEndToEndSkipModeAllowsBashWrite —— skip 档下 bash 写命令在真实 CLI 执行。
//
// 覆盖提交后审查发现的回归（第六十四刀）：`bashWriteNeedsApproval` 未复刻
// TS 外层三元的 `skipAllApproval` 短路 → skip 档下所有 bash 写命令被拦。
// 单测（loop 层）已覆盖，此处补**真实 CLI 二进制**——验证档位真从 flag
// 传到 bash 写门，且 headless 下不因无人可批而死锁。
func TestCLIEndToEndSkipModeAllowsBashWrite(t *testing.T) {
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
			fmt.Fprint(w, sseToolCallArgs("c1", "bash", map[string]any{
				"command": "mkdir cli-skip-probe-dir",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-bashgate-skip-test")
	cmd := exec.Command(bin,
		"-p", "建个目录",
		"--base-url", srv.URL,
		"--model", "test-model",
		"--approval", "dangerously-skip-permissions",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察：目录**真被创建**（skip 档 = 完全访问，零审批打扰）。
	if _, err := os.Stat(filepath.Join(root, "cli-skip-probe-dir")); err != nil {
		t.Fatalf("**skip 档下 bash 写命令被拦**（目录未创建：%v）——违反「完全访问档"+
			"零审批打扰承诺」，headless 下无人可批会死锁\nCLI 输出：%s", err, out)
	}
}
