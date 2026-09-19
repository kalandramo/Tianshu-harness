package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ── 破坏性命令闸门（硬闸门，不可被会话档位绕过）──

func TestDestructiveCommandRequiresApproval(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)

	destructive := []string{
		"rm -rf /tmp/x",
		"rm -rf ./build",
		"git reset --hard HEAD",
		"git checkout -- .",
		"git clean -fd",
		"git stash drop",
		"git push --force origin main",
		"git branch -D feature",
		"DROP TABLE users",
		"TRUNCATE TABLE logs",
	}
	for _, cmd := range destructive {
		// 关键：即使 dangerously-skip-permissions 也必须批准（硬闸门）
		p := &CallParams{
			Input:        map[string]any{"command": cmd},
			ApprovalMode: "dangerously-skip-permissions",
		}
		if !tool.RequiresApproval(p) {
			t.Errorf("破坏性命令 %q 必须需要批准（硬闸门不可被档位绕过）", cmd)
		}
		if reason := DestructiveReason(cmd); reason == "" {
			t.Errorf("应给出 %q 的破坏性原因", cmd)
		}
	}
}

// 反证 A：非破坏性命令在放开档位下不需要批准。
func TestBenignCommandNoApprovalWhenOpen(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	benign := []string{"ls -la", "echo hi", "go test ./...", "cat file.txt", "git status"}
	for _, cmd := range benign {
		p := &CallParams{
			Input:        map[string]any{"command": cmd},
			ApprovalMode: "dangerously-skip-permissions",
		}
		if tool.RequiresApproval(p) {
			t.Errorf("非破坏性命令 %q 在放开档位下不应需批准", cmd)
		}
	}
	// 但在 auto-safe 档位下需要
	p := &CallParams{Input: map[string]any{"command": "echo hi"}, ApprovalMode: "auto-safe"}
	if !tool.RequiresApproval(p) {
		t.Error("auto-safe 档位下命令应需批准")
	}
}

// ── 执行 ──

func TestBashExecuteSuccess(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, err := tool.Execute(context.Background(), call(root, map[string]any{"command": "echo hello"}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("不应报错：%s", r.Content)
	}
	if !strings.Contains(r.Content, "hello") {
		t.Errorf("输出应含 hello：%s", r.Content)
	}
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("退出码应为 0：%v", r.ExitCode)
	}
}

// 工作目录应为 cwd。
func TestBashRunsInCwd(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"command": "pwd"}))
	// macOS 的 /var 是 /private/var 的符号链接，用 Contains 容错
	if !strings.Contains(r.Content, filepath.Base(root)) {
		t.Errorf("应在 %s 下执行：%s", root, r.Content)
	}
}

// 非零退出码应标记错误并附退出码。
func TestBashNonZeroExit(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"command": "exit 3"}))
	if !r.IsError {
		t.Error("非零退出应标记错误")
	}
	if r.ExitCode == nil || *r.ExitCode != 3 {
		t.Errorf("退出码应为 3：%v", r.ExitCode)
	}
	if !strings.Contains(r.Content, "exit code") {
		t.Errorf("内容应含退出码：%s", r.Content)
	}
}

// 反证 B：环境错误（command not found）必须与执行失败分类不同。
//
// 这个区分很重要：环境缺东西不是模型能力问题，下游不得据此惩罚——
// 否则平台差异会让 agent 变胆怯。
func TestBashEnvironmentErrorClassified(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "definitely_not_a_real_command_xyz",
	}))
	if !r.IsError {
		t.Fatal("应报错")
	}
	if r.ErrorClass == nil {
		t.Fatal("应标记 ErrorClass")
	}
	if *r.ErrorClass != "environment" {
		t.Errorf("command not found 应归为 environment，实际 %q", *r.ErrorClass)
	}
}

// 普通执行失败应归为 exec-failure。
func TestBashExecFailureClassified(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"command": "false"}))
	if r.ErrorClass == nil || *r.ErrorClass != "exec-failure" {
		t.Errorf("普通失败应归为 exec-failure，实际 %v", r.ErrorClass)
	}
}

// stderr 应被收集并标记。
func TestBashStderrCaptured(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "echo err >&2",
	}))
	if !strings.Contains(r.Content, "stderr") || !strings.Contains(r.Content, "err") {
		t.Errorf("stderr 应被收集：%s", r.Content)
	}
}

// ── 超时与进程组清理 ──

// 反证 C：超时必须终止命令并标记 timeout。
func TestBashTimeoutKills(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)

	start := time.Now()
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "sleep 30",
		"timeout": 500,
	}))
	elapsed := time.Since(start)

	if !r.IsError {
		t.Fatal("超时应报错")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("超时未生效——耗时 %v（应远小于 sleep 的 30s）", elapsed)
	}
	if r.ErrorClass == nil || *r.ErrorClass != "timeout" {
		t.Errorf("应归为 timeout，实际 %v", r.ErrorClass)
	}
	if !strings.Contains(r.Content, "超时") {
		t.Errorf("应说明超时：%s", r.Content)
	}
}

// 反证 D：超时必须清理**整个进程组**，不留孤儿进程。
//
// 若去掉 Setpgid + kill(-pid)，子进程会成为孤儿继续运行。
func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)

	// 启动一个会派生子进程的命令；超时后两者都应被清理
	marker := filepath.Join(root, "still-alive.txt")
	cmd := "sh -c 'sleep 20 && touch " + marker + "' & sleep 20"
	_, _ = tool.Execute(context.Background(), call(root, map[string]any{
		"command": cmd,
		"timeout": 500,
	}))

	// 等一段时间，确认后台子进程没有继续运行到写标记
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("超时后子进程仍在运行（孤儿进程未被清理）")
	}
}

// ctx 取消应中断执行。
func TestBashContextCancel(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	r, _ := tool.Execute(ctx, call(root, map[string]any{"command": "sleep 30"}))
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("ctx 取消未中断执行——耗时 %v", elapsed)
	}
	if !r.IsError {
		t.Error("取消应标记错误")
	}
}

// ── 输出截断 ──

// 反证 E：输出超限必须截断并标记 Lossiness。
//
// 截断的观测不能支撑负向结论——标记是这条纪律的技术保障。
func TestBashOutputTruncationMarked(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	// 用一个小的上限
	if bt, ok := tool.(*bashTool); ok {
		bt.MaxOutputBytes = 1024
	}

	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "for i in $(seq 1 5000); do echo 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'; done",
	}))

	if r.Lossiness == nil {
		t.Fatal("超限输出必须标记 Lossiness")
	}
	if *r.Lossiness != "truncated" {
		t.Errorf("Lossiness = %q, want truncated", *r.Lossiness)
	}
	if !strings.Contains(r.Content, "truncated") {
		t.Errorf("内容应有截断提示：%.300s", r.Content)
	}
	// 关键提示：截断不能支撑负向结论
	if !strings.Contains(r.Content, "不能支撑负向结论") {
		t.Errorf("截断提示应说明「不能支撑负向结论」：%.400s", r.Content)
	}
}

// 未截断时 Lossiness 应为 lossless（bash 总是显式声明）。
func TestBashSmallOutputLossless(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"command": "echo tiny"}))
	if r.Lossiness == nil {
		t.Fatal("bash 应显式声明 Lossiness")
	}
	if *r.Lossiness != "lossless" {
		t.Errorf("小输出应为 lossless，实际 %q", *r.Lossiness)
	}
}

// ── 边界 ──

func TestBashEmptyCommand(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{"command": "   "}))
	if !r.IsError {
		t.Error("空命令应报错")
	}
}

// 超时参数应可覆盖默认值。
func TestBashCustomTimeout(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	p := call(root, map[string]any{"command": "echo x", "timeout": 5000})
	if got := tool.Timeout(p); got != 5*time.Second {
		t.Errorf("超时应为 5s，实际 %v", got)
	}
	// 默认
	if got := tool.Timeout(call(root, map[string]any{"command": "echo x"})); got != 120*time.Second {
		t.Errorf("默认超时应为 120s，实际 %v", got)
	}
}

// 多行输出应完整保留。
func TestBashMultilineOutput(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "printf 'line1\\nline2\\nline3\\n'",
	}))
	for _, want := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(r.Content, want) {
			t.Errorf("缺少 %s：%s", want, r.Content)
		}
	}
}

// 管道与 && 串联应正常工作。
func TestBashPipelines(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "echo 'b\na\nc' | sort | head -2",
	}))
	if !strings.Contains(r.Content, "a") || !strings.Contains(r.Content, "b") {
		t.Errorf("管道结果错误：%s", r.Content)
	}
}

// 退出码与信号：被杀时应反映。
func TestBashKilledBySignal(t *testing.T) {
	root := t.TempDir()
	tool := Bash(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{
		"command": "kill -TERM $$",
	}))
	if !r.IsError {
		t.Error("被信号终止应标记错误")
	}
}

var _ = syscall.SIGKILL
