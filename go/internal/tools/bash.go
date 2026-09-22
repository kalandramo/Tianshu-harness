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
	"unicode/utf8"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/platform"
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
			"command": strProp("要执行的 shell 命令"),
			"timeout": intPropMin("超时毫秒数（默认 120000；非正数按默认值处理）", 1),
			// **显式未实现声明**（第二十五刀）：Go 侧无 sessionJobRegistry 设施
			// （`JobRegistry`/`JobStore` 全库零命中），故此参数**被忽略**，命令
			// 仍走前台同步执行。用户级验收已实测证实（传 true/false/不传，输出
			// 逐字一致、无 job id）。
			//
			// 保留参数（而非删除）是为了不丢接口语义——TS 侧该参数正确，待 job
			// 子系统移植后恢复原文案。**与 TS 的偏离是有意的**，已在
			// schema_parity_test.go 的已知偏离白名单里登记（含移除条件）。
			"run_in_background": boolProp("（Go 侧暂未实现：传 true 仍走前台同步执行，不会转入后台、不返回 job id。job 子系统移植后恢复。）"),
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
	//
	// **shell 由探测决定，不是硬编码 bash**：Windows 上可能是 Git Bash /
	// PowerShell / cmd.exe（对账 TS bash.ts 的 getShellCommand()）。
	// 硬编码 "bash" 在没装 Git Bash 的 Windows 上直接启动失败。
	shell := platform.HostShellCommand()
	cmd := exec.Command(shell.Cmd, platform.BuildShellArgs(shell, command)...)
	cmd.Dir = t.Cwd
	// 平台组语义 + Wait 兜底（详见 prepareCommand / waitDelay 的说明）
	prepareCommand(cmd)

	var stdout, stderr bytes.Buffer
	// 解码器：Windows 中文控制台（代码页 936）输出 GBK 字节，直读会乱码。
	// 每流一个独立解码器（stdout/stderr 编码可能不同，且各自首块探测）。
	stdoutW := &limitedWriter{buf: &stdout, limit: maxOut, dec: newWinStreamDecoder(isWindowsHost())}
	stderrW := &limitedWriter{buf: &stderr, limit: maxOut, dec: newWinStreamDecoder(isWindowsHost())}
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

	// ── 模型可见内容整形（对账 TS `bash.ts:751,786` → `buildModelOutput`）──
	//
	// TS 的 bash 把原始输出交给 `buildModelOutput` 做有损但信息密度高的整形：
	// 成功折叠留**末尾 20 行**、失败走 error-aware 精选、超长走 head+tail。
	//
	// **本刀修的真实偏差**：第二十一刀我实现 successFold 时折叠成**单行提示**，
	// 而 TS 保留末尾 20 行——单行提示让模型失去"最后发生了什么"的观测。
	//
	// **恢复提示的落盘**（对账 TS `bash.ts:748-751`）。
	//
	// **条件对账 TS**：只在**不包装 artifact** 时落盘——artifact 模式由
	// `ArtifactStore` 负责持久化，再落一份是重复（TS 注释：
	// "Skip persistRawOutput in artifact mode — ArtifactStore owns raw persistence"）。
	// 故此处先算阈值，包装与否决定落盘。
	rawOutput := result.Content
	artifactThreshold := artifact.ToolArtifactThreshold("bash", p.ContextWindow)
	willWrapArtifact := p.ArtifactStore != nil && UTF16Len(rawOutput) >= artifactThreshold

	meta := ToolOutputMeta{
		Command:    command,
		ExitCode:   exitCode,
		DurationMs: duration.Milliseconds(),
	}
	if !willWrapArtifact {
		// 落盘失败返回空串 → recovery 提示缺席（不误导）。
		meta.RawPath = PersistRawOutput(p.ToolUseID, rawOutput)
	}
	result.Content = BuildModelOutput(rawOutput, meta, ApplyCommandFilter)
	result.RawPath = meta.RawPath

	// ── L0 artifact 包装（对账 TS `bash.ts:744-800`）──
	//
	// **为什么在这一层**：bash 在 `l0WrappedTools` 里，L1 跳过它（防无限嵌套 +
	// double-save）。故大结果必须由工具自己落盘。
	//
	// **落盘的是 `rawOutput`（整形前）而非 `result.Content`（整形后）**——
	// 对账 TS：artifact 存 `filtered`（过滤后的原文），模型看 `buildModelOutput`
	// 的整形结果。若存整形版，模型经 read_section 召回时会拿到已经截断的内容。
	//
	// **阈值复用 `willWrapArtifact`**（上方已算）——避免两处独立判断漂移。
	if willWrapArtifact {
		res := artifact.SummarizeBashOutput(rawOutput, command, exitCode)
		id, err := p.ArtifactStore.Save(artifact.SaveInput{
			Tool: "bash", Target: command,
			RawContent: rawOutput, // **原文**（整形前）
			Summary:    res.Summary,
			Sections:   res.Sections,
		})
		if err == nil {
			result.Content = result.Content + "\n\nUse read_section(artifactId=\"" + id +
				"\", section=\"L1-L500\") to load full output if the head/tail above is not enough.\n[artifact:" + id + "]"
		}
		// Save 失败 → 优雅降级（保持原 content）。
	}

	return result, nil
}

// limitedWriter 是带上限的缓冲写入器，超限后丢弃并标记。
//
// 同时负责**解码**：Windows 中文控制台输出 GBK 字节，直读会乱码（实测
// `cmd /c "echo 中文"` → `d6 d0 ce c4...`）。解码在写入时进行，以保持跨块
// 状态（多字节字符可能跨 chunk 边界）。
//
// 限流按**原始字节**计（对账 TS 的 OutputStreamBudget），但截断前先解码——
// 否则可能切断多字节字符产出乱码。
type limitedWriter struct {
	buf       *bytes.Buffer
	limit     int
	truncated bool
	dec       *winStreamDecoder
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil // 丢弃但报告已写，避免调用方报错
	}
	// 先解码再按剩余额度写入：解码后的字节数可能与原始不同（GBK 2 字节 →
	// UTF-8 3 字节），故用解码结果的长度重新计算额度。
	s := string(p)
	if w.dec != nil {
		s = w.dec.write(p)
	}
	if len(s) > remaining {
		// 按 rune 边界截断，避免切断 UTF-8 字符。
		w.buf.WriteString(truncateAtRuneBoundary(s, remaining))
		w.truncated = true
		return len(p), nil
	}
	w.buf.WriteString(s)
	return len(p), nil
}

// truncateAtRuneBoundary 在不超过 limit 字节的前提下，取尽量多的完整字符。
func truncateAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// 回退到不切断 UTF-8 序列的位置。
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
