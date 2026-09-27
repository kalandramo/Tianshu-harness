package mcp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// stdio.go —— MCP server 的 stdio 传输。
//
// 形态参照 `go/internal/lsp/platform.go` 的 `defaultLspSpawn` +
// `procTransport`（同款三管道接成双向流 + Close 杀进程防孤儿）。
//
// # 为什么不复用 lsp 的那两个类型
//
// 它们是 `lsp` 包的**未导出**类型（`procTransport` 小写），且本包刻意不依赖
// `internal/lsp`（见 rpc.go 的 Transport 注释）。此处按同款语义独立实现
// ——不是「重复建设」而是「依赖方向隔离」：MCP 与 LSP 是**并列**的协议客户端，
// 让 MCP 依赖 LSP 会把两个子系统耦合起来。

// procTransport 把子进程的 stdio 管道适配成 Transport。
type procTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// closeOnce 保证 Close 幂等（Dispose 与进程死亡回调可能都调）。
	closeOnce sync.Once
}

func (t *procTransport) Read(p []byte) (int, error)  { return t.stdout.Read(p) }
func (t *procTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// Close 关闭管道并杀掉子进程树。
//
// ★ **必须回收进程树**：只关管道的话 MCP server 会变成孤儿常驻
// （npx 起的 server 尤其——它下面还挂着真正的 node 进程）。
// 用 `tools.KillProcessTree`（`proctree.go:87`）而非 `Process.Kill()`，
// 后者只杀直接子进程，npx 场景下真正的 server 会活下来。
func (t *procTransport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		_ = t.stdout.Close()
		tools.KillProcessTree(t.cmd)
		// 回收僵尸（Kill 之后仍须 Wait）。
		_ = t.cmd.Wait()
	})
	return nil
}

// SpawnStdio 启动 MCP server 子进程并返回其 stdio 传输。
//
// 对账 TS 的 stdio 分支（`transport-factory.ts` + `@modelcontextprotocol/sdk`
// 的 `StdioClientTransport`）。Go 侧无 SDK，故直接按规范实现。
//
// # 环境变量的处理（与 TS 的差异，已披露）
//
// TS 的 SDK 用 `getDefaultEnvironment()` 起一个**白名单**环境（只透传
// PATH/HOME 等），本实现改为**继承父进程环境 + 叠加 `cfg.Env`**。
// 理由：Go 侧 `os.Environ()` 的透传是 Bash/子进程工具的既有惯例
// （见 `internal/tools/spawngit.go`），且 MCP server 常需要用户自定义的
// `PATH`（npx 位置）、代理变量等。**代价**：`cfg.Env` 无法「删掉」父进程的
// 某个变量——若将来需要该能力，须显式加 `UnsetEnv`。
//
// # cwd 语义
//
// `cfg.Cwd` 为空时用 `baseCwd`（对账 TS：`cwd` 缺省取进程 cwd）。
func SpawnStdio(cfg ServerConfig, baseCwd string) (Transport, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp: server command is empty")
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)

	cwd := cfg.Cwd
	if cwd == "" {
		cwd = baseCwd
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	// 继承父环境 + 叠加 cfg.Env（见上方差异说明）
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	// ★ 平台进程组语义（Windows 需 CreateProcess 标志；Unix 需 setsid）
	//   ——`tools.PrepareCommand` 封装了这个平台差异，并有 WaitDelay 兜底
	//   （`proctree.go:73`）。必须在 Start 之前调用。
	tools.PrepareCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	// stderr 交给父进程（server 的日志按 MCP 规范走 stderr；不读会阻塞管道）
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("mcp: start %q: %w", cfg.Command, err)
	}

	return &procTransport{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}
