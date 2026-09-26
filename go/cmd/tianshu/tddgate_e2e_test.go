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

// tddgate_e2e_test.go —— TDD gate 的**真实 CLI 二进制**验收（第七十二刀）。
//
// 验证单测覆盖不到的一环：**`RIVET_TDD_GATE` 真的从环境变量传到工具执行层**。
// 单测直接构造 `Config{TddGateEnv:...}`，而 CLI 路径是
// `os.Getenv("RIVET_TDD_GATE")` → `Config` → `loop.executeTool`——
// 中间任一处漏传，单测仍全绿但用户设的 env 不生效（本项目已多次踩过
// 「字段存在但零调用者」）。
//
// # 怎么让 gate 拦下
//
// gate 需要「连续 3 次代码编辑 + 零验证」。故 mock server 按轮返回 4 次
// `edit_file`，目标是**已存在的源文件**（保证前 3 次成功、计数累加）。

// TestCLIEndToEndTddGateEnforceBlocks —— enforce 档下第 4 次编辑被拦。
func TestCLIEndToEndTddGateEnforceBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	// 预置 4 个源文件（每次编辑改一个，避免「文件不存在」的干扰）。
	for _, n := range []string{"a.ts", "b.ts", "c.ts", "d.ts"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("// orig\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// **读一个测试文件**——否则 skipIfNoTests 会把 block 降级为 suggest。
	if err := os.WriteFile(filepath.Join(root, "a.test.ts"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := []string{"a.ts", "b.ts", "c.ts", "d.ts"}
	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		// 轮 1：读测试文件（让 hasReadTestFiles=true）
		// 轮 2-5：四次 edit_file（第 4 次应被 TDD gate 拦）
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("r1", "read_file", map[string]any{
				"file_path": filepath.Join(root, "a.test.ts"),
			}))
		case 2, 3, 4, 5:
			idx := int(n) - 2
			fmt.Fprint(w, sseToolCallArgs(fmt.Sprintf("e%d", idx), "edit_file", map[string]any{
				"file_path":  filepath.Join(root, targets[idx]),
				"old_string": "// orig",
				"new_string": "// edited",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-tddgate-enforce-test")
	cmd := exec.Command(bin, "-p", "改几个文件", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_KEY=test-key",
		"RIVET_TDD_GATE=enforce", // ← 关键：真实环境变量
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察 1：某一轮请求里出现了 TDD gate 的 block 文案（回灌给模型）。
	found := false
	for _, b := range bodies {
		if strings.Contains(b, "TDD Gate:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("enforce 档下应出现 TDD gate 拦截文案，实得 %d 轮请求均无\nCLI 输出：%s",
			len(bodies), out)
	}
}

// TestCLIEndToEndTddGateDefaultDoesNotBlock —— **默认（suggest）不拦**。
//
// **本刀最重要的回归**：若默认值误设为 enforce，用户装好 CLI 后做正常
// 多文件重构会被中途拦下（TS 记录的真实事故：session 05e1500e 显示
// enforce 在修复中途把 agent 逼进重写循环）。
func TestCLIEndToEndTddGateDefaultDoesNotBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	for _, n := range []string{"a.ts", "b.ts", "c.ts", "d.ts"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("// orig\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.test.ts"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := []string{"a.ts", "b.ts", "c.ts", "d.ts"}
	var bodies []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("r1", "read_file", map[string]any{
				"file_path": filepath.Join(root, "a.test.ts"),
			}))
		case 2, 3, 4, 5:
			idx := int(n) - 2
			fmt.Fprint(w, sseToolCallArgs(fmt.Sprintf("e%d", idx), "edit_file", map[string]any{
				"file_path":  filepath.Join(root, targets[idx]),
				"old_string": "// orig",
				"new_string": "// edited",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-tddgate-default-test")
	cmd := exec.Command(bin, "-p", "改几个文件", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY=test-key") // **不设** RIVET_TDD_GATE
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	// 观察 1：**不该出现** block 文案。
	for _, b := range bodies {
		if strings.Contains(b, "TDD Gate:") {
			t.Errorf("默认（suggest）档不该硬拦，但出现 TDD Gate 文案\nCLI 输出：%s", out)
			break
		}
	}
	// 观察 2：**第 4 个文件真被改了**（拦截未生效的正面证据）。
	data, err := os.ReadFile(filepath.Join(root, "d.ts"))
	if err != nil {
		t.Fatalf("读 d.ts 失败：%v", err)
	}
	if !strings.Contains(string(data), "edited") {
		t.Errorf("默认档下第 4 次编辑应生效（d.ts 应含 edited），实得 %q", string(data))
	}
}

// TestCLIEndToEndTddGateOffNeverBlocks —— `RIVET_TDD_GATE=off` 完全关闭。
func TestCLIEndToEndTddGateOffNeverBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("需要构建二进制，short 模式跳过")
	}

	root := t.TempDir()
	for _, n := range []string{"a.ts", "b.ts", "c.ts", "d.ts"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("// orig\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.test.ts"), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := []string{"a.ts", "b.ts", "c.ts", "d.ts"}
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n {
		case 1:
			fmt.Fprint(w, sseToolCallArgs("r1", "read_file", map[string]any{
				"file_path": filepath.Join(root, "a.test.ts"),
			}))
		case 2, 3, 4, 5:
			idx := int(n) - 2
			fmt.Fprint(w, sseToolCallArgs(fmt.Sprintf("e%d", idx), "edit_file", map[string]any{
				"file_path":  filepath.Join(root, targets[idx]),
				"old_string": "// orig",
				"new_string": "// edited",
			}))
		default:
			fmt.Fprint(w, sseText("完成"))
		}
	}))
	defer srv.Close()

	bin := buildCLIBinary(t, "tianshu-tddgate-off-test")
	cmd := exec.Command(bin, "-p", "改几个文件", "--base-url", srv.URL, "--model", "test-model")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"DEEPSEEK_API_KEY=test-key",
		"RIVET_TDD_GATE=off",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("CLI 输出：%s", out)
		t.Fatalf("CLI 运行失败：%v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "d.ts"))
	if err != nil {
		t.Fatalf("读 d.ts 失败：%v", err)
	}
	if !strings.Contains(string(data), "edited") {
		t.Errorf("off 档下第 4 次编辑应生效，实得 %q\nCLI 输出：%s", string(data), out)
	}
}
