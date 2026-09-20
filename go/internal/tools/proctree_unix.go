//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// Unix 实现：进程组隔离 + 负 PID 组杀。
//
// 对账 TS `src/tools/process-kill.ts` 的 Unix 分支：
//
//	try { kill(-child.pid, signal) } catch { child.kill(signal) }
//
// 即：优先组杀，失败则退回杀主进程（例如 Setpgid 未生效时 pid != pgid）。

// configureProcessGroupPlatform 让子进程自成进程组。
//
// 关键：没有这一步，`kill(-pid)` 会命中调用者自己的进程组——那会杀掉
// 整个天枢进程（含 TUI 与所有并行 worker）。这是 fail-closed 的边界。
func configureProcessGroupPlatform(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTreePlatform 回收整棵进程树。
//
// 顺序：先杀整个进程组（负 pid = 整组），再兜底直接杀主进程——
// Setpgid 若未生效（pid != pgid），组杀会落空，兜底才是唯一回收手段。
func killProcessTreePlatform(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
