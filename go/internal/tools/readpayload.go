package tools

// readpayload.go —— readFilePayload 的模型可见内容构造。
//
// 对账 TS `src/tools/read-file.ts:398-508` 的 `applyFoldThenPartial` /
// `buildLogPreviewContent` / `buildFileUiOutput` 及三个阈值常量。
//
// # 为什么单独成文件
//
// 这三个函数是 `ReadPolicyDecision` 四个 action 的**执行侧**——第八刀的
// `readpolicy.go` 只做了判定（`DecideReadPolicy`），执行从未接线。
// 本文件补上执行，使判定层真正生效（详见 HANDOFF）。

import (
	"fmt"
	"strings"
)

// 对账 TS 的三个阈值常量（逐字）。
const (
	// maxToolInputBytes 对账 TS `MAX_TOOL_INPUT_BYTES`。
	maxToolInputBytes = 100 * 1024
	// maxFocusScanBytes 对账 TS `MAX_FOCUS_SCAN_BYTES`。
	maxFocusScanBytes = 2 * 1024 * 1024
	// logPreviewLines 对账 TS `LOG_PREVIEW_LINES`。
	logPreviewLines = 80
)

// ApplyFoldThenPartial 先把代码折叠成签名骨架，再套字节级 partial 视图。
//
// 对账 TS `applyFoldThenPartial`。**折叠无益时回退**：未知语言 / 文件短 /
// 缩减不足 30% → 用原内容的 partial 视图。
//
// **整数比较**：TS 写 `foldedLines < originalLines * 0.7`（浮点）。Go 用
// `foldedLines*10 < originalLines*7`——精确等价且无浮点误差。
func ApplyFoldThenPartial(content, filePath string, cap ModelReadCap) string {
	fold := FoldCode(content, FoldOptions{FilePath: filePath, MaxLines: 200, MaxLinesSet: true})
	if fold.WasFolded && fold.FoldedLines*10 < fold.OriginalLines*7 {
		return BuildPartialView(fold.Folded, filePath, cap.MaxChars,
			&SkeletonSource{Lines: fold.OriginalLines, Chars: UTF16Len(content)})
	}
	return BuildPartialView(content, filePath, cap.MaxChars, nil)
}

// BuildLogPreviewContent 构造日志/JSONL 的**有界预览**（头 N 行 + 尾 N 行 + 指引）。
//
// 对账 TS `buildLogPreviewContent`。**为什么**：日志文件的首次全读会浪费上下文，
// 故只回头尾并给出精确的后续读取指引（offset/limit 边界 + grep 建议）。
//
// **量纲**：`content.length` 是 UTF-16 code unit（JS 字符串语义）——用 UTF16Len。
func BuildLogPreviewContent(filePath, content string) string {
	lines := strings.Split(content, "\n")
	headCount := logPreviewLines
	if len(lines) < headCount {
		headCount = len(lines)
	}
	tailCount := logPreviewLines
	remaining := len(lines) - headCount
	if remaining < 0 {
		remaining = 0
	}
	if remaining < tailCount {
		tailCount = remaining
	}
	head := lines[:headCount]
	var tail []string
	if tailCount > 0 {
		tail = lines[len(lines)-tailCount:]
	}
	omitted := len(lines) - len(head) - len(tail)
	tailStart := 1
	if len(tail) > 0 {
		tailStart = len(lines) - len(tail) + 1
	}

	parts := []string{
		fmt.Sprintf("read_file: %s looks like a log/JSONL output file (%d chars, %d lines).",
			filePath, UTF16Len(content), len(lines)),
		"Full first reads of log files waste context; returning a bounded preview only.",
	}
	tailHint := ""
	if len(tail) > 0 {
		tailHint = fmt.Sprintf("; tail offset=%d limit=%d", tailStart, len(tail))
	}
	parts = append(parts,
		fmt.Sprintf("Preview boundaries: head offset=1 limit=%d%s.", len(head), tailHint),
		"Next step: use read_file(file_path=..., offset=<known line>, limit<=200) for a specific range; use grep on this file for keywords/timestamps before reading middle ranges. Do not scan the whole project for this log.",
		"",
		fmt.Sprintf("── head (L1-L%d) ──", len(head)),
	)
	parts = append(parts, head...)
	if omitted > 0 {
		parts = append(parts, "", fmt.Sprintf("... %d lines omitted ...", omitted), "",
			fmt.Sprintf("── tail (L%d-L%d) ──", tailStart, len(lines)))
		parts = append(parts, tail...)
	}
	return strings.Join(parts, "\n")
}

// BuildFileUiOutput 构造 TUI 展示内容：带行号的头 + 尾（大文件压缩）。
//
// 对账 TS `buildFileUiOutput`。行号格式 `${padStart(4,' ')}│ ${line}`。
func BuildFileUiOutput(raw string, maxLines int) string {
	lines := strings.Split(raw, "\n")
	totalLines := len(lines)
	if totalLines <= maxLines {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = fmt.Sprintf("%4d│ %s", i+1, l)
		}
		return strings.Join(out, "\n")
	}

	// TS：`Math.ceil(maxLines * 0.6)` / `Math.floor(maxLines * 0.4)`。
	// 用整数运算避免浮点：(3n+4)/5 与 2n/5。
	headLines := (maxLines*3 + 4) / 5
	tailLines := (maxLines * 2) / 5
	omitted := totalLines - headLines - tailLines

	out := make([]string, 0, headLines+tailLines+1)
	for i := 0; i < headLines; i++ {
		out = append(out, fmt.Sprintf("%4d│ %s", i+1, lines[i]))
	}
	out = append(out, fmt.Sprintf("  ... %d lines omitted ...", omitted))
	for i := 0; i < tailLines; i++ {
		lineNo := totalLines - tailLines + i + 1
		out = append(out, fmt.Sprintf("%4d│ %s", lineNo, lines[totalLines-tailLines+i]))
	}
	return strings.Join(out, "\n")
}
