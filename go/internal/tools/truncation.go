package tools

// truncation.go —— 工具输出的截断与 PARTIAL 视图。
//
// 对账 TS `src/tools/truncation.ts`（76 行）。
//
// # 消费者
//
// `readFilePayload`（TS `read-file.ts:555`）用它产出 `modelContent`：
//
//	modelContent = truncateContent(content, cap.maxChars, cap.headChars, cap.tailChars)
//
// 即**原文与模型可见内容的分野**。Go 侧 `read_file` 目前只有一个 `body`
// （无分野），本文件是补上分野的前置。
//
// **与 `internal/prompt/truncate.go` 的区别**（勿混用）：那个是 **frozen 块的
// 预算截断**（对账 `volatile.ts:1209` 的 `truncateBlock`），语义不同源。
//
// # UTF-16 语义（本文件的核心难点）
//
// JS 的 `String.prototype.slice` / `.length` 按 **UTF-16 code unit** 计数，
// 切在代理对中间会产生**孤立代理**——UTF-8 编码时变 U+FFFD（`ef bf bd`）。
// 探针实测（`go/testdata/truncation/probe.ts`）：
//
//	"a😀bc".slice(0,2) → units 61,d83d → utf8 61 ef bf bd
//	"a😀bc".slice(-3)  → units de00,62,63 → utf8 ef bf bd 62 63
//
// Go 的 `[]rune` 切片按码点切、永不切断，直接照搬会产出不同字节。

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// truncationNote 对账 TS 的 `TRUNCATION_NOTE`（逐字）。
const truncationNote = "... (truncated, use offset/limit for more specific ranges)"

// TruncateContent 头尾截断（对账 TS `truncateContent`）。
//
// 语义：
//   - `utf16Len(content) <= maxChars` → 原样返回
//   - 否则 `head + "\n" + TRUNCATION_NOTE + "\n" + tail`
//   - `head = slice(0, keepHead)`、`tail = slice(-keepTail)`
//
// **`keepTail == 0` 的陷阱**：JS 的 `slice(-0)` 等价于 `slice(0)`，返回
// **整个串**（不是空串）——探针实测确认。故 tail 在 keepTail=0 时是全文。
func TruncateContent(content string, maxChars, keepHead, keepTail int) string {
	if UTF16Len(content) <= maxChars {
		return content
	}
	head := jsSliceHead(content, keepHead)
	tail := jsSliceTail(content, keepTail)
	return head + "\n" + truncationNote + "\n" + tail
}

// SkeletonSource 对账 TS `SkeletonSource`：fold 骨架来源文件的真实尺寸。
type SkeletonSource struct {
	Lines int
	Chars int
}

// BuildPartialView 构造大文件的 PARTIAL 视图（对账 TS `buildPartialView`）。
//
// 与头尾截断不同，它返回**从文件开头起的连续内容**——模型看不到拼接碎片。
//
// `skeletonOf` 非 nil 时，content 是 **fold 骨架**而非文件正文：header 必须
// 描述骨架的来源尺寸（否则 "showing lines 1-79 of 79" 读起来像完整文件）。
func BuildPartialView(content, filePath string, maxChars int, skeletonOf *SkeletonSource) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	totalChars := UTF16Len(content)

	headerOverhead := 300
	if skeletonOf != nil {
		headerOverhead = 480
	}
	budget := maxChars - headerOverhead
	if budget < 0 {
		budget = 0
	}

	// 逐行累加直到超预算（每行 +1 计入换行）。
	keptLines := 0
	keptChars := 0
	for keptLines < totalLines && keptChars+UTF16Len(lines[keptLines])+1 <= budget {
		keptChars += UTF16Len(lines[keptLines]) + 1
		keptLines++
	}
	// **至少保留 1 行**（对账 TS 的 `Math.max(1, keptLines)`）——即使预算为 0。
	if keptLines < 1 {
		keptLines = 1
	}

	firstPage := strings.Join(lines[:keptLines], "\n")

	var header []string
	if skeletonOf != nil {
		header = []string{
			"── SKELETON view of " + filePath + " (" + strconv.Itoa(skeletonOf.Lines) +
				" lines, " + strconv.Itoa(skeletonOf.Chars) + " chars) ──",
			"This is NOT the file's text: it is a structural outline (" + strconv.Itoa(keptLines) +
				" of " + strconv.Itoa(totalLines) + " outline lines).",
			"Prose, comments and code bodies have been REMOVED — an empty block here means content was dropped, not that the file is empty.",
			`To read the actual content: read_file(file_path="` + filePath + `", offset=1, limit=200), then page with offset.`,
		}
	} else {
		header = []string{
			"── PARTIAL view of " + filePath + " (" + strconv.Itoa(totalLines) +
				" lines, " + strconv.Itoa(totalChars) + " chars) ──",
			"Showing lines 1-" + strconv.Itoa(keptLines) + " of " + strconv.Itoa(totalLines) + ".",
			`To read more: read_file(file_path="` + filePath + `", offset=` + strconv.Itoa(keptLines+1) + `, limit=200)`,
		}
	}

	out := make([]string, 0, len(header)+4)
	out = append(out, header...)
	out = append(out,
		"To find specific code: use grep first, then read_file with offset/limit.",
		"For editing: use grep to locate the target line, then hash_edit with anchors — no full read needed.",
		"",
		firstPage,
	)
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------
// UTF-16 切片辅助
// ---------------------------------------------------------------------------

// UTF16Len 返回 UTF-16 code unit 数（对账 JS 的 `.length`）。
//
// BMP 外字符（emoji 等）占 2 个 unit。
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// jsSliceHead 对账 JS 的 `s.slice(0, n)`。
//
// `n >= len` → 全串；`n <= 0` → 空串。
func jsSliceHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(s))
	if n >= len(units) {
		return s
	}
	return decodeUnitsWithFFFD(units[:n])
}

// jsSliceTail 对账 JS 的 `s.slice(-k)`。
//
// **关键**：`k == 0` 时 JS 的 `-0 === 0` → `slice(0)` 返回**整个串**
// （不是空串）——探针实测确认。`k >= len` 同样返回全串。
func jsSliceTail(s string, k int) string {
	if k < 0 {
		k = 0
	}
	units := utf16.Encode([]rune(s))
	if k == 0 || k >= len(units) {
		return s // -0 与超界都返回全串（对账 JS）
	}
	return decodeUnitsWithFFFD(units[len(units)-k:])
}

// decodeUnitsWithFFFD 把 UTF-16 units 解码为 string，孤立代理写 U+FFFD。
//
// 对账 JS 的 UTF-8 编码行为：JS 字符串里的孤立代理在编码为 UTF-8 时
// 变成 `ef bf bd`（U+FFFD）——Go 无法表示孤立代理，故显式复刻。
// （与 `internal/prompt/truncate.go` 的 `sliceByUTF16` 同手法，但本包
// 不依赖 prompt 包——避免引入 tools → prompt 的反向依赖。）
func decodeUnitsWithFFFD(units []uint16) string {
	decoded := utf16.Decode(units)
	var b strings.Builder
	b.Grow(len(units))
	for _, r := range decoded {
		if r == utf8.RuneError {
			b.WriteRune('\uFFFD')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
