// pointerguard.go —— 指针回灌守卫（共享检测）。
//
// 对账 TS `src/tools/pointer-guard.ts`。
//
// # TS 侧的机制与动机（逐字对账其文件头注释）
//
// 工具参数后处理器把**消息历史**里的大内容字段替换成指针占位符
// （"[file written to …]" 等）。模型在自己的历史工具调用里看到几十个这种
// 指针，于是——尤其在多轮大写入的长会话里——开始**模仿**该模式，把指针文本
// 当作新写入的实际内容（用户报告 2026-07-06：11 批词表文件教会了模型该模式；
// 第 12 批把字面量 "[hash_edit applied to …]" 写进了文件）。
//
// 每个接受大文本字段的工具都必须拒绝以**任意**指针前缀开头的值——模型可能把
// write_file 的指针回吐到 hash_edit 的 new_string 里，反之亦然。
//
// # Go 侧现状（重要，必须如实记录）
//
// **Go 侧目前没有「参数折叠为指针」的后处理器**——已核实：
//   - `file_tools.go:147` 的 write_file 结果消息是中文 `已写入 %s（%d 行）`，
//     而 TS 的指针前缀是英文 `[file written to`。
//   - 全仓 grep `参数.*折叠|arg.*collapse|CollapseArg` 零命中。
//
// 故 6 个前缀里，`[file written to` / `[edit on` / `[hash_edit applied to` /
// `[patch applied to` / `[new block` **在 Go 侧不会被产生**——它们的触发面
// 目前为零。本模块**仍移植**，理由有二：
//
//  1. **`[plan persisted to` 是本刀要实现的 plan 工具自己会产生的**——
//     plan submit 的参数折叠属本刀范围（Wave 3）。
//  2. **纯函数、可独立对账**——留着它是「契约完整性」，与
//     `artifact_intercept.go` 的 `l0WrappedTools` 保留未命中项同理。
//
// 若未来给 Go 加参数折叠机制，本模块即刻生效、无需改动。
package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 指针前缀常量。
//
// **必须逐字相同**（模型模仿的是字面文本）。前四个对账 TS 的各
// arg-processor，后两个对账 TS pointer-guard.ts:28,30。
const (
	// WriteFilePointerPrefix 对账 TS `WRITE_FILE_POINTER_PREFIX`。
	WriteFilePointerPrefix = "[file written to"
	// EditFilePointerPrefix 对账 TS `EDIT_FILE_POINTER_PREFIX`。
	EditFilePointerPrefix = "[edit on"
	// HashEditPointerPrefix 对账 TS `HASH_EDIT_POINTER_PREFIX`。
	HashEditPointerPrefix = "[hash_edit applied to"
	// ApplyPatchPointerPrefix 对账 TS `APPLY_PATCH_POINTER_PREFIX`。
	//
	// **注意**：Go 侧 `applypatch.go:48` 另有一个私有同名常量
	// `applyPatchPointerPrefix`（字面量相同）。TS 侧同样是双重定义
	// （pointer-guard.ts:30 与 plan-submit-arg-processor.ts:16），一致性仅由
	// 测试锁住——照抄该不对称。
	ApplyPatchPointerPrefix = "[patch applied to"
	// EditNewBlockPointerPrefix 对账 TS `EDIT_NEW_BLOCK_POINTER_PREFIX`（edit_file
	// 的 new_string 折叠标记）。
	EditNewBlockPointerPrefix = "[new block"
	// PlanPointerPrefix 对账 TS `PLAN_POINTER_PREFIX`（plan submit 的 plan 折叠标记）。
	PlanPointerPrefix = "[plan persisted to"
)

// PointerPlaceholderPrefixes 是全部指针前缀。
//
// 顺序对账 TS `POINTER_PLACEHOLDER_PREFIXES`（pointer-guard.ts:32-39）。
var PointerPlaceholderPrefixes = []string{
	WriteFilePointerPrefix,
	EditFilePointerPrefix,
	HashEditPointerPrefix,
	ApplyPatchPointerPrefix,
	EditNewBlockPointerPrefix,
	PlanPointerPrefix,
}

// PointerGuardErrorMarker 是嵌入每条守卫错误的稳定标记。
//
// 对账 TS `POINTER_GUARD_ERROR_MARKER`（pointer-guard.ts:45）。指针回灌
// advisory hook 靠这个子串统计重复违规次数。
const PointerGuardErrorMarker = "pointer placeholder from message history"

// PointerInternalTag 是真实指针里内嵌的机器专用标签。
//
// 对账 TS `POINTER_INTERNAL_TAG`（`pointer-tag.ts:11`）。
const PointerInternalTag = "#RIVET-POINTER-DISPLAY-ONLY#"

// pointerMarkerPhrases 是真实指针必含的标记短语（二级守卫）。
//
// 对账 TS `POINTER_MARKER_PHRASES`（pointer-guard.ts:59-64）。用途：**真实内容
// 恰好以同样的括号前缀开头时不被误拒**。
var pointerMarkerPhrases = []string{
	PointerInternalTag,
	"Display placeholder",
	"never emit as content",
	"Use read_file to review",
}

// DetectPointerPlaceholder 报告 value 是否为指针占位符。
//
// 对账 TS `pointer-guard.ts:73-96`。返回匹配到的前缀；真实内容返回空串。
//
// 判定链（三级）：
//  1. **整值快路径**：`trimStart` 后整值即为指针（最常见的回灌模式）。
//  2. **逐行扫描**：真实指针永远是**单行**；若任一整行匹配则拒绝。
//
// 单行判定（`detectPointerPlaceholderInLine`）三条件同时成立：
//   - 以某个前缀开头
//   - 行内**不含** `\n` / `\r`（含换行说明模型在前缀模仿之后追加了真实内容）
//   - 含 `pointerMarkerPhrases` 之一
func DetectPointerPlaceholder(value string) string {
	if head := detectPointerPlaceholderInLine(trimLeftSpace(value)); head != "" {
		return head
	}

	for _, rawLine := range splitLinesCRLF(value) {
		line := trimLeftSpace(rawLine)
		if line == "" {
			continue
		}
		if matched := detectPointerPlaceholderInLine(line); matched != "" {
			return matched
		}
	}
	return ""
}

// detectPointerPlaceholderInLine 判定单行是否为指针。
//
// 对账 TS `pointer-guard.ts:98-109`。
func detectPointerPlaceholderInLine(line string) string {
	for _, prefix := range PointerPlaceholderPrefixes {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// 真实指针由 arg-processor 渲染为单行；含换行说明模型在前缀之后
		// 追加了真实内容。
		if strings.ContainsAny(line, "\n\r") {
			continue
		}
		hasMarker := false
		for _, phrase := range pointerMarkerPhrases {
			if strings.Contains(line, phrase) {
				hasMarker = true
				break
			}
		}
		if !hasMarker {
			continue
		}
		return prefix
	}
	return ""
}

// PointerPlaceholderError 构造面向模型与用户的拒绝文案。
//
// 对账 TS `pointer-guard.ts:117-132`。先用平实语言说明**发生了什么**，
// 再给模型具体恢复路径；末尾附机器专用标记供 hook 检测。
func PointerPlaceholderError(toolName, field, matchedPrefix, filePath string) string {
	return "❌ 写入被拦截：" + field + " 的内容是历史消息里的显示指针（\"" + matchedPrefix + " …\"），不是真实的文件内容。\n\n" +
		"机制：大内容写入成功后，历史消息中的参数会被替换成这种显示指针（节省上下文 token）——你之前的写入已成功落盘，没有出错；这次是把历史里的指针误当正文传了回来。\n\n" +
		"修复：先 read_file " + filePath + " 看磁盘当前内容——它很可能已经是你要写的完整内容（若是，直接继续下一步，不要重写）；确认需要修改时，再用真实完整内容调用 " + toolName + "。\n\n" +
		"[" + PointerGuardErrorMarker + "]"
}

// ── 幂等化解（共享于 write_file / edit_file / hash_edit / plan）──

// IdempotentResolveMode 是幂等化解的模式。
type IdempotentResolveMode string

const (
	// ResolveModeFull 对账 TS 的 `mode='full'`（write_file）：磁盘行数须与指针
	// 记录精确一致，chars 容 CRLF 偏差。
	ResolveModeFull IdempotentResolveMode = "full"
	// ResolveModeEdit 对账 TS 的 `mode='edit'`（edit_file/hash_edit/plan）：
	// 指针记录的是块大小非整文件——只校验路径一致 + 目标文件存在。
	ResolveModeEdit IdempotentResolveMode = "edit"
)

// IdempotentResolveInput 是幂等化解的输入。
//
// 对账 TS `IdempotentResolveInput`（pointer-guard.ts:155-160）。
type IdempotentResolveInput struct {
	Mode          IdempotentResolveMode
	FilePath      string
	Value         string
	MatchedPrefix string
}

// ResolveIdempotentPointer 尝试把「指针回灌」化解为幂等成功。
//
// 对账 TS `pointer-guard.ts:163-189`。返回 (成功文案, true)；无法化解返回
// ("", false) 让调用方走原硬错误。**绝不把真实错误吞成成功。**
//
// 成功条件链：首行解析出路径 → 指针路径与本次 filePath 规范化后相等 →
// 目标文件可读 → 按 mode 判定。
func ResolveIdempotentPointer(in IdempotentResolveInput) (string, bool) {
	ptrPath := parsePointerPath(in.Value, in.MatchedPrefix)
	if ptrPath == "" {
		return "", false
	}
	if !PathsMatchForCompare(ptrPath, toPosixPath(in.FilePath)) {
		return "", false
	}

	raw, err := os.ReadFile(in.FilePath)
	if err != nil {
		return "", false
	}
	onDisk := strings.ReplaceAll(string(raw), "\r\n", "\n")

	if in.Mode == ResolveModeFull {
		m := fullPointerStatsRe.FindStringSubmatch(in.Value)
		if m == nil {
			return "", false
		}
		wantLines := atoiSafe(m[1])
		wantChars := atoiSafe(m[2])
		lines := len(strings.Split(onDisk, "\n"))
		if lines != wantLines {
			return "", false
		}
		diff := len(onDisk) - wantChars
		if diff < 0 {
			diff = -diff
		}
		if diff > wantLines {
			return "", false
		}
		return "该文件已是指针所指向的内容（磁盘为凭：" + itoa(lines) + " lines，路径一致），本次按幂等成功处理、未做写入。" +
			"那是消息历史里的显示指针被当作内容回传的自动化解——以后如需修改请先 read_file 再编辑；如需整文件重写，请写出完整真实内容。", true
	}

	return "该编辑已应用到磁盘（路径一致，文件存在），本次按幂等无需重复应用。" +
		"你回传的是消息历史里的 \"" + in.MatchedPrefix + " …\" 显示指针（不是真实内容）——如需继续修改请先 read_file " + toPosixPath(in.FilePath) + " 看当前内容。", true
}

// fullPointerStatsRe 从指针文本里抽取行数与字符数。
//
// 对账 TS `/(\d+) lines?, (\d+) chars/`。
var fullPointerStatsRe = regexp.MustCompile(`(\d+) lines?, (\d+) chars`)

// parsePointerPath 从指针首行解析出路径。
//
// 对账 TS `pointer-guard.ts:135-152`。各工具指针格式统一为
// `<prefix> <path> — …` 或 `<prefix> <path>: …`。取**最早**的分隔符
// （两者都可能出现在描述文本里，离前缀最近的那个才是路径分隔符）。
func parsePointerPath(value, prefix string) string {
	firstLine := firstLineOf(value)
	if !strings.HasPrefix(firstLine, prefix) {
		return ""
	}
	after := trimLeftSpace(firstLine[len(prefix):])

	bestIdx := -1
	for _, sep := range []string{" — ", ": "} {
		if idx := strings.Index(after, sep); idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
			bestIdx = idx
		}
	}
	if bestIdx >= 0 {
		return strings.TrimSpace(after[:bestIdx])
	}
	// 无分隔符：取首个空白分隔的 token。
	if idx := strings.IndexAny(after, " \t"); idx >= 0 {
		p := strings.TrimSpace(after[:idx])
		return p
	}
	return strings.TrimSpace(after)
}

// PathsMatchForCompare 归一路径后比较相等。
//
// 对账 TS `plan-mode.ts:126-129` 的 `canonicalizePathForCompare`：
// 反斜杠→斜杠；盘符形路径（Windows）整体小写。
//
// **为什么整体小写**：NTFS 大小写不敏感，且盘符大小写在真实环境里不稳定
// ——VSCode/Git Bash 常给小写盘符（`c:\proj`）而 `process.cwd()` 给大写
// （`C:\proj`）。逐字节比较会误拒活动计划文件的写入 → plan mode 下草稿
// 永远为空（桌面「起草中」断流）。
func PathsMatchForCompare(a, b string) bool {
	return canonicalizePathForCompare(a) == canonicalizePathForCompare(b)
}

var driveLetterRe = regexp.MustCompile(`^[a-zA-Z]:/`)

// canonicalizePathForCompare 对账 TS `canonicalizePathForCompare`。
func canonicalizePathForCompare(p string) string {
	s := strings.ReplaceAll(p, "\\", "/")
	if driveLetterRe.MatchString(s) {
		return strings.ToLower(s)
	}
	return s
}

// toPosixPath 反斜杠转斜杠（对账 TS `toPosixPath`）。
func toPosixPath(p string) string {
	return filepath.ToSlash(p)
}

// ── 小工具 ──

// trimLeftSpace 去掉前导空白（对账 JS 的 `trimStart()`）。
//
// JS 的 `trimStart()` 去的是 Unicode 空白 + 换行；Go 的 `TrimLeft(s, " \t\n\r")`
// 覆盖常用集。此处显式列字符，避免 `strings.TrimSpace` 连尾部一起去掉。
func trimLeftSpace(s string) string {
	return strings.TrimLeft(s, " \t\n\r\v\f\u00a0\u2028\u2029\ufeff")
}

// splitLinesCRLF 按 `\r?\n` 分行（对账 JS 的 `split(/\r?\n/)`）。
func splitLinesCRLF(s string) []string {
	return regexp.MustCompile(`\r?\n`).Split(s, -1)
}

// firstLineOf 取首行（对账 JS 的 `split(/\r?\n/, 1)[0]`）。
func firstLineOf(s string) string {
	s = trimLeftSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
