package lsp

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"sync"
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

// procTransport 把子进程的 stdio 管道适配成 `Transport`。
//
// **为什么需要它**：`Transport` 要求 `io.Reader + io.Writer + io.Closer`，
// 而 `exec.Cmd` 的管道是三段独立对象（stdin 只写、stdout 只读），
// 需一个薄包装把两者接成双向流。
type procTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// closeOnce 保证 Close 幂等（RPC 的 Dispose 与进程死亡回调可能都调）。
	closeOnce sync.Once
}

func (t *procTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *procTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// Close 关闭管道并杀掉子进程。
//
// ★ **必须杀进程**：只关管道的话，语言服务器会变成孤儿进程常驻
// （`gopls` 尤甚——它起一个后台索引进程）。`Dispose` 路径依赖此处回收。
func (t *procTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		// 回收僵尸（Kill 之后仍须 Wait）。
		_ = t.cmd.Wait()
	})
	return nil
}

// defaultLspSpawn 启动一个语言服务器子进程并返回其 stdio 传输。
//
// 对账 TS `defaultLspSpawn`（`multi-manager.ts:62-77`）：
//
//	return spawnFn(resolved.command, resolved.args,
//	  { cwd, stdio: ['pipe','pipe','pipe'], env })
//
// # ★ 为什么这个函数的存在本身就是修复
//
// Go 侧原本**没有任何真实 spawn 实现**——`platform.go` 只有 `probeWhich`
// （探测 PATH），而 `multiManagerOptions.spawnFor` 为 nil 时
// `newEntryLocked` **直接返回 nil transport**，`Initialize` 随即失败：
//
//	"LSP server spawn failed: no stdio pipes (check PATH / npx)"
//
// 后果：**整个 LSP 子系统（含 goto/refs/诊断）在生产中从未通电**——
// `IsReady()` 返回 true 只因为它探测的是「PATH 上有没有 gopls 二进制」，
// 而非「能否真的启动」。这是「探测」与「实际可用」的语义落差。
//
// # 与 TS 的差异（已披露）
//
// TS 还做了两件此处**未做**的事：
//   - Windows 下 npx.cmd 解析（`resolveNpmCliCommand`）——Go 侧暂不支持
//     npx 启动的 server（typescript-language-server 等）
//   - `buildStdioEnvWithNodePath`（为桌面端 bundled node 补 PATH）
//
// Go 侧直接按 `def.Command + def.Args` 启动（gopls/rust-analyzer/clangd
// 等都是原生二进制，无 npx 包装）。需要 npx 系 server 时再补。
func defaultLspSpawn(def *LspServerDef, cwd string) Transport {
	if def == nil || def.Command == "" {
		return nil
	}
	cmd := exec.Command(def.Command, def.Args...)
	cmd.Dir = cwd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil
	}
	// stderr 丢弃：语言服务器会往 stderr 打大量日志，读它需要额外 goroutine
	// 且内容无用（对账 TS 的 `stdio: ['pipe','pipe','pipe']` 中第三项
	// 在 TS 侧也只是被忽略）。
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil
	}
	return &procTransport{cmd: cmd, stdin: stdin, stdout: stdout}
}
