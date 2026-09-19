// Package filediff 提供 unified diff 生成（展示用）。
//
// 对账 src/tools/edit-diff.ts（185 行）+ src/workers/cpu-tasks.ts 的
// `diffUnifiedRaw` / `diffStructuredRaw`（底层是 jsdiff 的
// `createTwoFilesPatch` / `structuredPatch`）。
//
// ## 用途与风险边界
//
// 结果是给 **uiContent** 通道的（TUI/桌面工具卡片），**绝不进模型面向的
// `content`**——故不进对话历史、无前缀缓存成本。
//
// ## 与 TS 的架构差异
//
// TS 版把 diff 计算放进 **worker 线程**（cpu-pool），因为 jsdiff 的 Myers 是
// **同步 CPU**：8K 行文件全量重写约 7 秒、50K 行文件可能跑几分钟，会冻住整个
// 事件循环（TUI 卡死、Esc 失效、超时定时器无法触发）——这是 2026-07-08
// 「write 卡住 → 丢工具返回」事故的根因。
//
// Go 版**不需要 worker**：每个工具调用在自己的 goroutine 里，同步 diff 不会
// 阻塞其他协程。但**仍保留超时保护**——超长输入下 diff 是 O(N·D) 的，
// 让工具调用无限期挂着同样不可接受。
package filediff

import (
	"strings"
	"time"
)

// defaultMaxDiffLines 是渲染行数上限（对账 DEFAULT_MAX_DIFF_LINES = 600）。
const defaultMaxDiffLines = 600

// defaultTimeout 是单次 diff 的时间上限。
//
// 对账 TS 的 DIFF_TIMEOUT_MS（1000ms，内联兜底）。Go 侧无 worker 池，
// 故直接用这一个值——但语义相同：**超时返回空串**（展示用，丢失只降级
// 工具卡片，不影响正确性）。
const defaultTimeout = 1 * time.Second

// BuildFileDiff 生成 `before` → `after` 的 unified diff（供 relPath）。
//
// 对账 buildFileDiff：
//   - 无变化（内容相同或 hunk 集为空）→ 返回空串
//   - 丢弃 jsdiff 的 `Index:` + `===` 前导（只留 `--- ` 起，对账 TS 的
//     headerIdx 切片）
//   - **无 `@@` 头 → 返回空串**（如仅尾换行差异）
//   - 超过 maxLines → 截断并附一行提示
func BuildFileDiff(relPath, before, after string, maxLines int) string {
	if before == after {
		return ""
	}
	if maxLines <= 0 {
		maxLines = defaultMaxDiffLines
	}
	posixPath := strings.ReplaceAll(relPath, `\`, "/")

	oldLines := splitLines(before)
	newLines := splitLines(after)
	hunks := diffHunks(oldLines, newLines, 3, defaultTimeout)
	if len(hunks) == 0 {
		// 行内容相同但**尾换行不同**（如 "x\n" → "x"）——jsdiff 仍产出
		// 一个「删除+插入同一行」的 hunk 并附尾换行标记（oracle 锁定）。
		if len(oldLines) > 0 && len(newLines) > 0 &&
			hasTrailingNewline(before) != hasTrailingNewline(after) {
			hunks = []hunk{{
				oldStart: 1, oldLines: len(oldLines),
				newStart: 1, newLines: len(newLines),
				lines: trailingOnlyHunkLines(oldLines, newLines),
			}}
		} else {
			return ""
		}
	}

	// 尾换行标记：**仅当双方都非空**且任一侧缺尾换行时输出。
	// oracle 锁定：全新文件（before=""）与清空文件（after=""）**不加**该标记
	// ——jsdiff 只在两侧都有内容时才比较尾换行。
	trailingNLMissing := len(oldLines) > 0 && len(newLines) > 0 &&
		(!hasTrailingNewline(before) || !hasTrailingNewline(after))
	body := renderUnified(posixPath, hunks, len(oldLines) == 0, len(newLines) == 0, trailingNLMissing)
	// 无 @@ 头 → 无真实变化（对账 TS 的 `if (!body.some(l => l.startsWith('@@')))`）
	hasHunkHeader := false
	for _, l := range body {
		if strings.HasPrefix(l, "@@") {
			hasHunkHeader = true
			break
		}
	}
	if !hasHunkHeader {
		return ""
	}

	if len(body) > maxLines {
		hidden := len(body) - maxLines
		out := append([]string{}, body[:maxLines]...)
		out = append(out, "… +"+itoa(hidden)+" 行 diff · ctrl+o 展开")
		return strings.Join(out, "\n")
	}
	return strings.Join(body, "\n")
}

// LineRange 是 AFTER 文件里一个 1-based 闭区间。
//
// 对账 edit-diff.ts 的 LineRange。
type LineRange struct {
	Start int
	End   int
}

// ComputeChangedLineRanges 计算编辑波及的 AFTER 行范围。
//
// 对账 computeChangedLineRanges：用**零上下文**的 hunk，使每个 hunk 精确
// 对应改动的 after 行。
//   - 纯删除（newLines == 0）塌缩为删除边界的**单行点**
//   - 全新文件（before == ""）→ 一个覆盖全文件的区间
//   - 无变化 → nil
//   - diff 太贵（超时）→ **保守地把整个 AFTER 文件当作改动**（与全新文件
//     同形），让 LSP 诊断过滤暴露一切而非隐藏错误
func ComputeChangedLineRanges(before, after string) []LineRange {
	if before == after {
		return nil
	}
	oldLines := splitLines(before)
	newLines := splitLines(after)
	hunks := diffHunks(oldLines, newLines, 0, defaultTimeout)

	if len(hunks) == 0 {
		// 无 hunk（或超时）→ 保守全文件
		n := len(newLines)
		if n == 0 {
			n = 1
		}
		return []LineRange{{Start: 1, End: n}}
	}

	ranges := make([]LineRange, 0, len(hunks))
	for _, h := range hunks {
		start := h.newStart
		if start < 1 {
			start = 1
		}
		end := start
		if h.newLines > 0 {
			end = start + h.newLines - 1
		}
		ranges = append(ranges, LineRange{Start: start, End: end})
	}
	return ranges
}

// trailingOnlyHunkLines 构造「仅尾换行差异」的 hunk 行。
//
// 对账 jsdiff：内容相同的行全部输出为 `-` 然后 `+`（因为整体被视为一个
// 变更块），例如 `"x\n"` → `"x"` 产出 `["-x","+x"]`。
func trailingOnlyHunkLines(oldLines, newLines []string) []string {
	var out []string
	for _, l := range oldLines {
		out = append(out, "-"+l)
	}
	for _, l := range newLines {
		out = append(out, "+"+l)
	}
	return out
}

// hunk 是一个 diff 块（对账 RawHunk）。
type hunk struct {
	oldStart int
	oldLines int
	newStart int
	newLines int
	lines    []string // 前缀 ' ' / '-' / '+'
}

// splitLines 按 \n 切分并**剥离尾换行**。
//
// 对账 jsdiff 的行为（探针实测）：`"a\nb\n"` → `["a","b"]`（不是
// `["a","b",""]`）。尾换行的**存在性**另行记录（决定是否输出
// `\ No newline at end of file` 标记）。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// hasTrailingNewline 报告内容是否以 \n 结尾。
func hasTrailingNewline(s string) bool {
	return strings.HasSuffix(s, "\n")
}

// diffHunks 用 Myers 算法求 hunk（带上下文行数）。
//
// 超时返回 nil（调用方按「无变化」或「保守全文件」处理）。
func diffHunks(a, b []string, context int, timeout time.Duration) []hunk {
	deadline := time.Now().Add(timeout)
	ops := myersDiff(a, b, deadline)
	if ops == nil {
		return nil
	}
	return buildHunks(a, b, ops, context)
}

// op 是一次编辑操作。
type op struct {
	kind byte // ' ' 相等 / '-' 删除 / '+' 插入
	line string
}

// myersDiff 求 a → b 的最短编辑脚本（Myers 差分算法）。
//
// 超时（deadline 到期）返回 nil。
func myersDiff(a, b []string, deadline time.Time) []op {
	n, m := len(a), len(b)
	max := n + m

	// V 以 k（对角线）为索引，偏移 max 使其可为负。
	v := make([]int, 2*max+1)
	var trace [][]int

	for d := 0; d <= max; d++ {
		if time.Now().After(deadline) {
			return nil
		}
		// 保存本轮的 V（回溯用）
		vc := make([]int, len(v))
		copy(vc, v)
		trace = append(trace, vc)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				x = v[k+1+max]
			} else {
				x = v[k-1+max] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+max] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, max)
			}
		}
	}
	return nil
}

// backtrack 从 trace 回溯出编辑脚本。
func backtrack(trace [][]int, a, b []string, offset int) []op {
	var ops []op
	x, y := len(a), len(b)

	for d := len(trace) - 1; d > 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[prevK+offset]
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			ops = append(ops, op{kind: ' ', line: a[x-1]})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, op{kind: '+', line: b[prevY]})
				y = prevY
			} else {
				ops = append(ops, op{kind: '-', line: a[prevX]})
				x = prevX
			}
		}
	}
	for x > 0 && y > 0 {
		ops = append(ops, op{kind: ' ', line: a[x-1]})
		x--
		y--
	}
	for x > 0 {
		ops = append(ops, op{kind: '-', line: a[x-1]})
		x--
	}
	for y > 0 {
		ops = append(ops, op{kind: '+', line: b[y-1]})
		y--
	}
	// 反转
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// buildHunks 把编辑脚本切成带上下文的 hunk。
func buildHunks(a, b []string, ops []op, context int) []hunk {
	_ = a
	_ = b
	if len(ops) == 0 {
		return nil
	}

	// 找出所有变更位置
	var changes []int
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return nil
	}

	// 按 context 合并成组
	type span struct{ lo, hi int }
	var spans []span
	for _, idx := range changes {
		lo := idx - context
		if lo < 0 {
			lo = 0
		}
		hi := idx + context
		if hi > len(ops)-1 {
			hi = len(ops) - 1
		}
		if len(spans) > 0 && lo <= spans[len(spans)-1].hi+1 {
			if hi > spans[len(spans)-1].hi {
				spans[len(spans)-1].hi = hi
			}
		} else {
			spans = append(spans, span{lo: lo, hi: hi})
		}
	}

	var hunks []hunk
	for _, sp := range spans {
		h := hunk{}
		// 计算 oldStart / newStart：数到 span 起点为止的 old/new 行数
		oldCount, newCount := 0, 0
		for i := 0; i < sp.lo; i++ {
			switch ops[i].kind {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			}
		}
		// unified diff 的起始行是 1-based；空侧约定为 0
		h.oldStart = oldCount + 1
		h.newStart = newCount + 1

		for i := sp.lo; i <= sp.hi; i++ {
			o := ops[i]
			h.lines = append(h.lines, string(o.kind)+o.line)
			switch o.kind {
			case ' ':
				h.oldLines++
				h.newLines++
			case '-':
				h.oldLines++
			case '+':
				h.newLines++
			}
		}
		// 纯插入/纯删除的起始行约定——**探针实测**（对账 jsdiff）：
		// 空侧的行号**取非空侧的起始行号**，不是「前一行号」。
		//
		//   删中间行 a,b,c → a,c      → old(2,1) new(2,0)  ← newStart = oldStart
		//   删首行   a,b,c → b,c      → old(1,1) new(1,0)
		//   删末行   a,b,c → a,b      → old(3,1) new(3,0)
		//   纯插入   a,c   → a,b,c    → old(2,0) new(2,1)  ← oldStart = newStart
		//
		// 这条语义直接决定 ComputeChangedLineRanges 的输出（纯删除时
		// 区间落在**删除边界**而非前一行）。
		if h.oldLines == 0 {
			h.oldStart = h.newStart
		}
		if h.newLines == 0 {
			h.newStart = h.oldStart
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// renderUnified 渲染 unified diff 主体（对账 buildFileDiff 的切片行为）。
//
// **丢弃** `Index:` + `===` 前导（TS 侧从第一个 `--- ` 开始切）。
//
// `beforeEmpty` / `afterEmpty` 决定空侧渲染为 `0,0`——oracle 锁定：
// 全新文件是 `@@ -0,0 +1,2 @@`，清空文件是 `@@ -1,2 +0,0 @@`。
func renderUnified(posixPath string, hunks []hunk, beforeEmpty, afterEmpty, trailingNLMissing bool) []string {
	var out []string
	out = append(out, "--- "+posixPath)
	out = append(out, "+++ "+posixPath)
	for _, h := range hunks {
		out = append(out, "@@ -"+rangeSpec(h.oldStart, h.oldLines, beforeEmpty)+
			" +"+rangeSpec(h.newStart, h.newLines, afterEmpty)+" @@")
		out = append(out, h.lines...)
	}
	if trailingNLMissing {
		out = append(out, "\\ No newline at end of file")
	}
	return out
}

// rangeSpec 渲染 hunk 的行号范围。
//
// **jsdiff 总是输出 `start,count` 形式**（即使 count == 1 也不省略）——
// oracle 锁定：`@@ -1,1 +1,1 @@`。这与 GNU diff 的省略约定不同，照 GNU
// 实现会字节不等价。
//
// 空侧（empty=true 且 count==0）渲染为 `0,0`——对账 oracle 的
// `@@ -0,0 +1,2 @@`。
func rangeSpec(start, count int, empty bool) string {
	if empty && count == 0 {
		return "0,0"
	}
	return itoa(start) + "," + itoa(count)
}

// itoa 是 strconv.Itoa 的本地别名。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
