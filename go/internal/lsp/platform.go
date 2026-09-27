package lsp

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// isWindows 报告是否运行在 Windows 上。
func isWindows() bool { return runtime.GOOS == "windows" }

// probeWhich 探测可执行名是否在 PATH 上。
//
// 对账 TS `probeWhich`：
//
//	execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin],
//	  { stdio: ['ignore','ignore','ignore'], timeout: 800, windowsHide: true })
//
// Windows 上用 `where`（`which` 在 cmd/PowerShell 下不存在）。
func probeWhich(bin string) bool {
	name := "which"
	if isWindows() {
		name = "where"
	}
	return runQuiet(name, bin, 800*time.Millisecond)
}

// runQuiet 跑一个命令、丢弃输出、按超时杀掉，返回是否成功退出。
//
// **为什么必须真的杀掉**：`exec.Command` + `Wait` 在超时后若不 Kill，
// 子进程会变成孤儿（TS 的 `execFileSync` 由同步语义保证回收，Go 需显式做）。
func runQuiet(name string, arg string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, arg)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err == nil
	case <-ctx.Done():
		// CommandContext 在 ctx 到期会 Kill；此处等回收，避免孤儿。
		<-done
		return false
	}
}
