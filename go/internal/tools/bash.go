package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// bashTool 实现 bash（命令执行）。
//
// 对账 src/tools/bash.ts 的核心语义：
//   - 默认超时 120s（防挂死）
//   - 原始输出上限 8MB，超出截断并标记 Lossiness
//   - 进程组隔离 + 超时时杀整个进程组（防孤儿进程）
//   - 破坏性命令需要批准（硬闸门，不可被会话档位绕过）
//   - 环境错误（command not found）与执行失败分类不同
type bashTool struct {
	baseTool
	Cwd string
	// TimeoutSec 是默认超时（秒），0 = 120。
	TimeoutSec int
	// MaxOutputBytes 是单流输出上限，0 = 8MB。
	MaxOutputBytes int
}

// Bash 构造 bash 工具。
func Bash(cwd string) Tool {
	t := &bashTool{Cwd: cwd}
	t.def = contract.Definition{
		Name: "bash",
		Description: `执行 shell 命令。

- 用 && 串联独立命令；长时间运行的命令用 run_in_background=true 转入后台
- 输出可能被截断（标记 [output truncated]）——**截断的观测不能支撑负向结论**，
  需换更窄的命令确认
- 破坏性/不可逆命令（rm -rf、git reset --hard 等）需要用户明确批准
- 超时默认 120 秒；超时会终止整个进程组（不留孤儿进程）`,
		InputSchema: objSchemaOrdered([]string{"command", "timeout", "run_in_background"}, map[string]any{
			"command":           strProp("要执行的 shell 命令"),
			"timeout":           intPropMin("超时毫秒数（默认 120000；非正数按默认值处理）", 1),
			"run_in_background": boolProp("设为 true 转入后台并返回 job id。自动检测已知长跑命令。"),
		}, "command"),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

// Timeout 返回工具级超时。
func (t *bashTool) Timeout(p *CallParams) time.Duration {
	if p != nil {
		if ms := intArg(p.Input, "timeout", 0); ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	sec := t.TimeoutSec
	if sec <= 0 {
		sec = 120
	}
	return time.Duration(sec) * time.Second
}

// RequiresApproval：破坏性命令**必须**批准，不受会话档位影响（硬闸门）。
//
// 这是 fail-closed 的：宁可多问一次，也不静默执行不可逆操作。
func (t *bashTool) RequiresApproval(p *CallParams) bool {
	cmd := strArg(p.Input, "command")
	if isDestructiveCommand(cmd) {
		return true // 硬闸门：即使 dangerously-skip-permissions 也需确认
	}
	// 非破坏性命令按会话档位
	return p.ApprovalMode != "dangerously-skip-permissions"
}

// destructivePatterns 是破坏性/不可逆命令模式。
//
// 与 src/tools/destructive-patterns.ts 对齐的**核心子集**——完整版含更多
// 变体（Windows 专有、数据库 DROP 等），后续按需扩充。
var destructivePatterns = []struct {
	re   *regexp.Regexp
	desc string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+`), "递归/强制删除"},
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "git reset --hard（丢工作区改动）"},
	{regexp.MustCompile(`\bgit\s+reset\s+--mixed\b`), "git reset --mixed"},
	{regexp.MustCompile(`\bgit\s+checkout\s+--\s`), "git checkout --（丢工作区改动）"},
	{regexp.MustCompile(`\bgit\s+restore\b`), "git restore（丢工作区改动）"},
	{regexp.MustCompile(`\bgit\s+clean\s+-[a-zA-Z]*[fd]`), "git clean（删未跟踪文件）"},
	{regexp.MustCompile(`\bgit\s+stash\s+(drop|clear)\b`), "git stash drop/clear"},
	{regexp.MustCompile(`\bgit\s+push\s+.*--force\b`), "git push --force"},
	{regexp.MustCompile(`\bgit\s+branch\s+-D\b`), "git branch -D（强删分支）"},
	{regexp.MustCompile(`\bDROP\s+(TABLE|DATABASE)\b`), "SQL DROP"},
	{regexp.MustCompile(`\bTRUNCATE\s+TABLE\b`), "SQL TRUNCATE"},
	{regexp.MustCompile(`\bmkfs\b`), "格式化文件系统"},
	{regexp.MustCompile(`\bdd\s+.*of=/dev/`), "dd 写设备"},
	{regexp.MustCompile(`>\s*/dev/sd[a-z]`), "直接写块设备"},
}

// isDestructiveCommand 判定命令是否破坏性/不可逆。
func isDestructiveCommand(cmd string) bool {
	for _, p := range destructivePatterns {
		if p.re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// DestructiveReason 返回命中的破坏性模式描述（供审批提示）。
func DestructiveReason(cmd string) string {
	for _, p := range destructivePatterns {
		if p.re.MatchString(cmd) {
			return p.desc
		}
	}
	return ""
}

// Execute 执行命令。
func (t *bashTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	command := strArg(p.Input, "command")
	if strings.TrimSpace(command) == "" {
		return contract.Result{Content: "bash 需要 command 参数", IsError: true}, nil
	}

	timeout := t.Timeout(p)
	maxOut := t.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 8 << 20 // 8MB
	}

	// 可取消的执行上下文
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 用 exec.Command（非 CommandContext）：CommandContext 的默认取消行为是
	// 只杀主进程，与我们要杀整组的逻辑竞争。这里自行管理取消。
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = t.Cwd
	// 平台组语义 + Wait 兜底（详见 prepareCommand / waitDelay 的说明）
	prepareCommand(cmd)

	var stdout, stderr bytes.Buffer
	stdoutW := &limitedWriter{buf: &stdout, limit: maxOut}
	stderrW := &limitedWriter{buf: &stderr, limit: maxOut}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	start := time.Now()
	err := cmd.Start()
	if err != nil {
		// 启动失败——环境问题（bash 不存在等）
		return contract.Result{
			Content:    fmt.Sprintf("命令启动失败：%v", err),
			IsError:    true,
			ErrorClass: errClassPtr(contract.ErrorClassEnvironment),
		}, nil
	}

	// 超时/取消时**立即**杀整个进程树。
	//
	// 关键：不能等 cmd.Wait() 返回后再杀。exec.CommandContext 的默认行为是在
	// ctx 超时时只杀主进程（bash 本身），而 bash 派生的子进程（`sh -c '...' &`）
	// 会继续存活成为孤儿。且主进程一死，其进程组组长身份消失，事后 kill(-pid)
	// 可能命中已回收的 pgid。
	//
	// 平台差异（进程组 vs taskkill /F /T）封装在 killProcessTree 内。
	signalDone := watchAndKillOnCancel(runCtx, cmd)

	waitErr := cmd.Wait()
	duration := time.Since(start)
	signalDone()

	timedOut := runCtx.Err() == context.DeadlineExceeded

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// ── 构造输出 ──
	var sb strings.Builder
	out := stdout.String()
	errOut := stderr.String()
	if out != "" {
		sb.WriteString(out)
	}
	if errOut != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[stderr]\n")
		sb.WriteString(errOut)
	}

	stdoutTrunc := stdoutW.truncated
	stderrTrunc := stderrW.truncated
	lossiness := contract.LossinessLossless
	if stdoutTrunc || stderrTrunc {
		lossiness = contract.LossinessTruncated
		sb.WriteString(fmt.Sprintf(
			"\n\n[output truncated: 单流上限 %d 字节。**截断的观测不能支撑负向结论**——"+
				"换更窄的命令（grep/head/tail）确认你要找的东西是否真的不存在。]",
			maxOut))
	}

	result := contract.Result{
		Content:    sb.String(),
		Lossiness:  &lossiness,
		ExitCode:   &exitCode,
		Command:    command,
		RawBytes:   int64Ptr(int64(len(out) + len(errOut))),
		ErrorClass: nil,
	}

	if timedOut {
		result.IsError = true
		result.ErrorClass = errClassPtr(contract.ErrorClassTimeout)
		result.Content = fmt.Sprintf(
			"命令超时（%v）已被终止，整个进程组已清理。\n\n部分输出：\n%s",
			timeout.Round(time.Millisecond), result.Content)
		return result, nil
	}

	if exitCode != 0 {
		result.IsError = true
		result.ErrorClass = errClassPtr(contract.ErrorClassExecFailure)
		// 环境错误识别：command not found / 权限不足等
		combined := out + errOut
		if isEnvironmentError(combined) {
			result.ErrorClass = errClassPtr(contract.ErrorClassEnvironment)
		}
		// 退出码附加到内容（模型需要看到它）
		result.Content = fmt.Sprintf("%s\n\n[exit code: %d, 耗时 %v]", result.Content, exitCode, duration.Round(time.Millisecond))
		return result, nil
	}

	_ = duration
	return result, nil
}

// limitedWriter 是带上限的缓冲写入器，超限后丢弃并标记。
type limitedWriter struct {
	buf       *bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil // 丢弃但报告已写，避免调用方报错
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

// isEnvironmentError 识别「环境缺东西」而非「命令执行失败」。
//
// 这个区分很重要：command not found 是环境问题而非模型能力问题，
// 下游（momentum/doom/approval）不得据此惩罚——否则平台差异会让 agent 变胆怯。
func isEnvironmentError(output string) bool {
	patterns := []string{
		"command not found",
		"no such file or directory",
		"permission denied",
		"not found",
		"未找到命令",
	}
	lower := strings.ToLower(output)
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func errClassPtr(c contract.ToolErrorClass) *contract.ToolErrorClass { return &c }
func int64Ptr(i int64) *int64                                        { return &i }
