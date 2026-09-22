package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// diff.go —— 工作树改动的 git diff。
//
// 对账 TS `src/tools/diff.ts`。

// diff 的截断常量（对账 TS `MAX_LINES_PER_FILE` / `MAX_TOTAL_CHARS`，
// diff.ts:9-10）。
const (
	diffMaxLinesPerFile = 200
	diffMaxTotalChars   = 8000
	diffTimeout         = 30 * time.Second
)

// SplitByFile 按 `diff --git` 边界切分输出。
//
// 对账 TS `splitByFile`（diff.ts:157-170）。
//
// **边界行归新块**：`diff --git` 行是下一块的首行，不是上一块的末行。
// 首块之前若无 `diff --git`（异常输入），整段作为一块。
func SplitByFile(output string) []string {
	var files []string
	current := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "diff --git") && current != "" {
			files = append(files, current)
			current = line
			continue
		}
		if current != "" {
			current += "\n"
		}
		current += line
	}
	if current != "" {
		files = append(files, current)
	}
	return files
}

// TruncateDiff 按文件行数与总字符数双重截断。
//
// 对账 TS `truncateDiff`（diff.ts:139-155）。**两级截断，顺序敏感**：
//
//  1. 逐文件：超过 200 行的，留前 200 行 + 截断提示
//  2. 整体：超过 8000 字符的，截到 8000 + 截断提示
//
// **注意第二级的语义**：它在**拼接后**的长度上判断，故可能切在文件中间
// ——这是 TS 的行为，照抄。
func TruncateDiff(output string) string {
	files := SplitByFile(output)
	truncated := make([]string, 0, len(files))
	for _, file := range files {
		lines := strings.Split(file, "\n")
		if len(lines) <= diffMaxLinesPerFile {
			truncated = append(truncated, file)
			continue
		}
		head := strings.Join(lines[:diffMaxLinesPerFile], "\n")
		truncated = append(truncated, fmt.Sprintf("%s\n...（已截断，另有 %d 行）", head, len(lines)-diffMaxLinesPerFile))
	}
	result := strings.Join(truncated, "\n")
	if len(result) <= diffMaxTotalChars {
		return result
	}
	// **字节切片**——对账 TS 的 `result.slice(0, MAX_TOTAL_CHARS)`。
	// 注意：TS 的 slice 按 UTF-16 code unit，Go 的切片按字节。对纯 ASCII 的
	// diff 输出两者一致；对含 CJK 的 diff（注释/字符串里的中文）会不同。
	// **这是已知偏差**——见文件尾说明。此处按字节截断并保证不切断 UTF-8 序列。
	return safeByteSlice(result, diffMaxTotalChars) + "\n...（已截断）"
}

// safeByteSlice 按字节截断但**不切断 UTF-8 序列**。
//
// **与 TS 的差异**：TS 的 `String.slice(0, 8000)` 按 UTF-16 code unit，
// 可能切断代理对（产生 U+FFFD）。Go 侧按字节且回退到最近的有效 rune 边界
// ——结果更干净，但**在含非 ASCII 的 diff 上与 TS 字节不同**。
//
// 取舍理由：8000 字符的截断点落在 CJK 中间是低频事件，而产出**无效 UTF-8**
// 会让下游（JSON 序列化、终端渲染）出错。已在 HANDOFF 记明该偏离。
func safeByteSlice(s string, n int) string {
	if n >= len(s) {
		return s
	}
	// 回退到最近的有效 rune 起点（最多回退 3 字节）。
	for i := n; i > n-4 && i > 0; i-- {
		if s[i]&0xC0 != 0x80 { // 非续接字节
			return s[:i]
		}
	}
	return s[:n]
}

// diffTool 是 diff 工具实现。
type diffTool struct {
	def     contract.Definition
	enabled bool
}

// Diff 构造 diff 工具。
func Diff() Tool {
	t := &diffTool{enabled: true}
	t.def = contract.Definition{
		Name: "diff",
		Description: `显示工作树改动的 git diff。

### 用法
- 提交前用 diff 查看哪些文件有改动
- 编辑前用 diff 了解当前状态
- 编辑后用 diff 验证改动是否正确
- 结果按文件截断（每个文件最多 200 行）

### 示例
Good: diff() — 显示所有未暂存改动
Good: diff(staged=true) — 显示已暂存改动
Good: diff(path="src/api/client.ts") — 显示单个文件的 diff`,
		InputSchema: objSchemaOrdered([]string{"staged", "path", "context_lines", "current_task_only"}, map[string]any{
			"staged":            boolProp("显示已暂存改动（--cached）"),
			"path":              strProp("过滤到指定文件或目录"),
			"context_lines":     numProp("上下文行数（默认 3）"),
			"current_task_only": boolProp("只显示当前任务拥有文件的 diff（B1 归属范围）"),
		}),
	}
	return t
}

func (t *diffTool) Definition() contract.Definition { return t.def }
func (t *diffTool) Enabled() bool                   { return t.enabled }
func (t *diffTool) ConcurrencySafe() bool           { return true }
func (t *diffTool) RequiresApproval(*CallParams) bool {
	return false
}
func (t *diffTool) Timeout(*CallParams) time.Duration { return diffTimeout }

func (t *diffTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	// **对账 TS 的 `?? false` / `?? 3`（nullish 语义）**——显式传 0 保留。
	staged := boolArg(p.Input, "staged")
	contextLines := intArg(p.Input, "context_lines", 3)
	currentTaskOnly := boolArg(p.Input, "current_task_only")

	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, fmt.Sprintf("-U%d", contextLines))

	// ── 路径过滤（B1 归属优先）──
	if currentTaskOnly && len(p.OwnedFiles) > 0 {
		var ownedPaths []string
		for _, f := range p.OwnedFiles {
			rel := relPosix(p.Cwd, mustAbsJoin(p.Cwd, f))
			if !strings.HasPrefix(rel, "..") && rel != "" {
				ownedPaths = append(ownedPaths, rel)
			}
		}
		if len(ownedPaths) == 0 {
			return contract.Result{Content: "没有可 diff 的归属文件。"}, nil
		}
		args = append(args, "--")
		args = append(args, ownedPaths...)
	} else if path, ok := p.Input["path"].(string); ok && path != "" {
		validated := pathsafe.Validate(p.Cwd, path, pathsafe.ModeRead, nil)
		if !validated.OK {
			return contract.Result{Content: "错误：" + validated.Error, IsError: true}, nil
		}
		args = append(args, "--", relPosix(p.Cwd, validated.Path))
	}

	startTime := time.Now()

	// ── 执行（复用 bash.go 的进程管理模式）──
	runCtx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()

	cmd := SpawnGit(args, p.Cwd)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		rawPath := PersistRawOutput(p.ToolUseID, err.Error())
		return contract.Result{
			Content: "错误：" + err.Error(),
			RawPath: rawPath,
			IsError: true,
		}, nil
	}

	signalDone := watchAndKillOnCancel(runCtx, cmd)
	waitErr := cmd.Wait()
	signalDone()

	timedOut := runCtx.Err() == context.DeadlineExceeded
	// **用户中止**（对账 TS 的 abortSignal 分支）：协作式取消，返回非错误。
	if ctx.Err() != nil && !timedOut {
		return contract.Result{Content: "用户已中止 diff。"}, nil
	}

	out := stdout.String()
	errOut := strings.TrimSpace(stderr.String())

	if timedOut {
		rawPath := PersistRawOutput(p.ToolUseID, "git diff timed out")
		return contract.Result{
			Content:    "错误：git diff 超时",
			RawPath:    rawPath,
			IsError:    true,
			ErrorClass: errClassPtr(contract.ErrorClassTimeout),
		}, nil
	}

	// **stderr 非空即报错**（对账 TS：`if (stderr.trim())` 分支先于 stdout 检查）。
	if errOut != "" {
		rawPath := PersistRawOutput(p.ToolUseID, errOut)
		return contract.Result{Content: "错误：" + errOut, RawPath: rawPath, IsError: true}, nil
	}

	// **无输出即「无改动」**（对账 TS 的 `if (!stdout.trim())`）。
	if strings.TrimSpace(out) == "" {
		return contract.Result{Content: "无改动。"}, nil
	}

	exitCode := 0
	if waitErr != nil {
		exitCode = exitCodeOf(waitErr)
	}
	durationMs := time.Since(startTime).Milliseconds()

	rawPath := PersistRawOutput(p.ToolUseID, out)
	meta := ToolOutputMeta{
		Command:    "git diff",
		ExitCode:   exitCode,
		DurationMs: durationMs,
		RawPath:    rawPath,
	}

	return contract.Result{
		Content: BuildModelOutput(TruncateDiff(out), meta, nil),
		RawPath: rawPath,
	}, nil
}

// mustAbsJoin 把（可能相对的）路径解析为绝对路径。
func mustAbsJoin(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// exitCodeOf 从 Wait 的错误里提取退出码（非 ExitError 返回 -1）。
//
// 与 `bash.go` 的同款逻辑一致（那里是内联的）。
func exitCodeOf(waitErr error) int {
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
