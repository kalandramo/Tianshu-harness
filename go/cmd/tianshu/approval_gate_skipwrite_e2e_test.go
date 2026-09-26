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

// approval_gate_skipwrite_e2e_test.go —— skip 档下**文件写工具**的 e2e（第七十四刀）。
//
// # 为什么补这一条
//
// 第七十四刀把 4 个写工具的 `RequiresApproval` 从「读档位」改为**恒真**
// （对账 TS `requiresApproval: () => true`）。**档位豁免上移到
// `decideApprovalGate` 单点判定**。
//
// 既有 e2e 覆盖了 skip 档的 **bash** 写命令
// （`TestCLIEndToEndSkipModeAllowsBashWrite`），但**没有 skip 档的 write_file**——
// 而 write_file 正是本次改动的对象。**该路径缺 e2e = 「保护还在」的声称缺直接证据**。
//
// 本测试补上：验证「恒真 + 上移的档位判定」组合后，skip 档下 write_file
// **仍能执行**（即豁免真的发生在 `decideApprovalGate`）。

// TestCLIEndToEndSkipModeAllowsWriteFile —— skip 档下 write_file 真的落盘。
func TestCLIEndToEndSkipModeAllowsWriteFile(t *testing.T) {
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
				"file_path": filepath.Join(root, "skipwrite.txt"),
				"content":   "written-under-skip",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-skipwrite-test")
	cmd := exec.Command(bin,
		"-p", "写个文件",
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

	// 观察：文件**真被写入**。
	//
	// 若 `RequiresApproval` 改成恒真后**没有**在 `decideApprovalGate` 里
	// 正确豁免 skip 档，这里会失败（写操作被拦 → 文件不存在）。
	data, err := os.ReadFile(filepath.Join(root, "skipwrite.txt"))
	if err != nil {
		t.Fatalf("**skip 档下 write_file 被拦**（文件未创建：%v）——「恒真 + 档位判定"+
			"上移」的组合破坏了完全访问档的豁免\nCLI 输出：%s", err, out)
	}
	if strings.TrimSpace(string(data)) != "written-under-skip" {
		t.Errorf("文件内容应为 written-under-skip，实得 %q", string(data))
	}
}

// TestCLIEndToEndManualBlocksWriteFileAfterRequiresApprovalChange ——
// **反面对照**：manual 档下 write_file 仍被拦（恒真不削弱 manual 的保护）。
//
// 与上一条配对：同一次改动，skip 档放行 / manual 档拦截——两个方向都验，
// 才说明「档位判定真的上移了」而不是「判定丢了」。
func TestCLIEndToEndManualBlocksWriteFileAfterRequiresApprovalChange(t *testing.T) {
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
				"file_path": filepath.Join(root, "manualwrite.txt"),
				"content":   "should-not-be-written",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-manualwrite-test")
	cmd := exec.Command(bin,
		"-p", "写个文件",
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

	// 观察 1：拒绝文案回灌。
	if len(bodies) < 2 {
		t.Fatalf("应至少 2 个请求，实得 %d\nCLI 输出：%s", len(bodies), out)
	}
	if !strings.Contains(bodies[1], "需人工批准") {
		t.Errorf("manual 档下 write_file 应被拦且文案回灌，第二轮请求片段：%.600s", bodies[1])
	}
	// 观察 2：**文件未被创建**（拦截真生效）。
	if _, err := os.Stat(filepath.Join(root, "manualwrite.txt")); err == nil {
		t.Error("**文件被创建了**——manual 档拦截未生效（恒真改动削弱了 manual 保护）")
	}
}
