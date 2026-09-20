//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Windows 实现：taskkill /F /T 按父子链回收。
//
// 对账 TS `src/tools/process-kill.ts` 的 win32 分支（issue #144 的实测结论）：
//
//   - Windows 上 `syscall.Kill` / `SIGKILL` / `Setpgid` **不存在**——负 PID 与
//     POSIX 信号语义不适用（编译期就会失败）。
//   - **一律带 `/F`**：不带 `/F` 的 taskkill 对 console 子进程是 no-op，
//     系统会把每个 PID 回成「只能强制终止此进程(带 /F 选项)」。TS 侧历史实现
//     分 SIGTERM→SIGKILL 两级，那 3 秒「优雅期」在 Windows 上纯空转——
//     端口与文件锁白占，子进程一个都没回收。故不再区分信号。
//
// 已知边界（与 TS 一致）：`taskkill /T` 走 Win32 父子链，Git Bash/MSYS 经
// `nohup` 等派生的孙进程其父链会断开，可能逃过回收。这是 issue #144 本体，
// 本实现不声称解决它——只保证与 TS 同等的行为。

// configureProcessGroupPlatform 在 Windows 上是 no-op。
//
// Windows 无进程组语义，组杀由 killProcessTreePlatform 的 taskkill /T 承担。
// 保留函数体（而非删掉调用点）是为了让两平台的调用序列一致。
func configureProcessGroupPlatform(cmd *exec.Cmd) {
	_ = cmd
}

// killProcessTreePlatform 用 taskkill 回收进程树。
//
// 传 pid 而非负 pid（Windows 不支持负 PID）；/T 连带子孙，/F 强制。
// 尽力而为：进程可能已退出，taskkill 会报「没有找到进程」——忽略即可。
func killProcessTreePlatform(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = kill.Run()
}
