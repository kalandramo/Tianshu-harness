package tools

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// 进程树管理——平台无关的编排骨架。
//
// 背景（对账 TS `src/tools/process-kill.ts` 与 issue #144）：超时/取消时
// **必须回收整棵进程树**，而不只是主进程。只杀主进程会让 bash 派生的后台
// 子进程成为孤儿，继续占端口、持文件锁、写文件——会话「假死」的经典成因。
//
// 但「整树回收」的机制在两个平台上完全不同，且**不可互换**：
//
//   - Unix：子进程自成进程组（`Setpgid`），`kill(-pid)` 一次回收整组。
//   - Windows：无进程组语义，`syscall.Kill` / `SIGKILL` / `Setpgid` 在
//     `syscall` 包中**根本不存在**（编译期失败）。须走 `taskkill /F /T /PID`，
//     按 Win32 父子链回收。
//
// 故按平台拆分（build tag），语义契约统一由本文件声明：
//
//  1. `configureProcessGroup(cmd)` 在 `Start()` 前调用，建立平台的组语义。
//  2. `killProcessTree(cmd)` 在 ctx 到期时调用，回收整棵树（幂等、尽力而为）。
//
// 调用方（bash / run_tests）只依赖这两个函数，不感知平台差异。

// configureProcessGroup 建立平台特有的进程组语义（须在 cmd.Start() 前调用）。
//
// Unix：Setpgid 让子进程成为新进程组组长，kill(-pid) 才作用得到整组。
// Windows：无进程组概念，此调用是 no-op（组杀由 killProcessTree 的 taskkill
//
//	/T 承担）；保留调用点是为了让两个平台的调用序列一致、便于审读。
func configureProcessGroup(cmd *exec.Cmd) {
	configureProcessGroupPlatform(cmd)
}

// waitDelay 是 cmd.Wait() 在进程退出后、等待 I/O 管道关闭的上限。
//
// **为什么必须有它**（Windows 上是「超时形同虚设」的真缺陷）：
//
// 把 cmd.Stdout/Stderr 指向 bytes.Buffer 时，Go 会创建**管道**并靠 Wait
// 关闭读端。子进程派生的**孙进程继承了写端句柄**——即使孙进程已被 taskkill
// 杀死，只要还有句柄副本未关闭，Wait 内部的 io.Copy 就会阻塞到 EOF。
//
// 实测（本机 Windows，`bash -c "sh -c 'sleep 20' & sleep 20"`，超时 500ms）：
//
//	无 WaitDelay    → cmd.Wait() 阻塞 19–20 秒（等满孙进程的 sleep）
//	WaitDelay=300ms → cmd.Wait() 301ms 返回
//
// 即：没有它，bash 工具的 timeout 参数在 Windows 上**根本不生效**——调用方
// 设 500ms，实际卡 20 秒。这比「杀不掉孙进程」更隐蔽：进程确实死了，但调用
// 被句柄吊住。
//
// 取 2 秒的理由：正常命令的管道在进程退出后毫秒级关闭，2s 给慢速 flush 留足
// 余量（不会误伤）；一旦句柄泄漏则 2s 兜底返回，把最坏延迟从「子进程寿命」
// 压到常数。与 TS 侧 SIGTERM→SIGKILL 的 3s 兜底同量级。
//
// 该字段在所有平台语义一致（Go 1.20+），Unix 上正常路径同样受益于兜底。
const waitDelay = 2 * time.Second

// prepareCommand 做 Start() 前的统一准备：平台组语义 + Wait 兜底。
//
// 两个平台共用——调用方（bash / run_tests）只需调这一个函数。
// PrepareCommand 是 prepareCommand 的导出形式——供 tools 包外的调用方
// （如 internal/hooks 的脚本执行）复用同一套进程树语义。
//
// **为什么导出而非各写一份**：进程组 + WaitDelay 的细节是踩过坑的
// （见 waitDelay 的注释——Windows 上超时形同虚设的真缺陷）。复制一份到
// 别处意味着下次修 bug 要修两处，且极易漏。
func PrepareCommand(cmd *exec.Cmd) { prepareCommand(cmd) }

func prepareCommand(cmd *exec.Cmd) {
	configureProcessGroup(cmd)
	cmd.WaitDelay = waitDelay
}

// killProcessTree 回收 cmd 的整棵进程树（尽力而为，不返回错误）。
//
// 调用时机：ctx 到期（超时/取消）时，**与 cmd.Wait() 并行**——不能等 Wait
// 返回后再杀。主进程一死，其进程组组长身份消失，事后 kill(-pid) 可能命中
// 已回收的 pgid；Windows 上父子链也会因主进程退出而断裂，taskkill /T 再也
// 找不到孙进程。
// KillProcessTree 是 killProcessTree 的导出形式（同 PrepareCommand 的理由）。
func KillProcessTree(cmd *exec.Cmd) { killProcessTree(cmd) }

func killProcessTree(cmd *exec.Cmd) {
	killProcessTreePlatform(cmd)
}

// watchAndKillOnCancel 在 runCtx 到期时回收进程树，与 cmd.Wait() 并行。
//
// 这是 bash / run_tests 共用的编排骨架：返回一个「等待结束」的关闭函数，
// 调用方在 cmd.Wait() 返回后调用它，避免 goroutine 泄漏。
//
// 竞态处理：`done` 用 sync.Once 关闭——Wait 返回与 ctx 到期可能几乎同时发生，
// 双重关闭会 panic。
func watchAndKillOnCancel(runCtx context.Context, cmd *exec.Cmd) (signalDone func()) {
	done := make(chan struct{})
	var once sync.Once
	signalDone = func() { once.Do(func() { close(done) }) }
	go func() {
		select {
		case <-runCtx.Done():
			killProcessTree(cmd)
		case <-done:
			// Wait 已返回，无需清理
		}
	}()
	return signalDone
}
