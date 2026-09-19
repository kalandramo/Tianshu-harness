package tools

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/recovery"
	"github.com/kalandramo/tianshu/go/internal/syntaxcheck"
)

// Anchor 是一个编辑锚点。
//
// 对账 src/tools/hash-edit.ts 的 Anchor interface：
// Line 为 1-based；Hash 为 8 位小写 hex，仅位置模式时为 nil。
type Anchor struct {
	Line int
	Hash *string
}

// recoveryNearWindow 是过期锚点在期望行附近的搜索半径。
//
// 对账 hash-edit.ts 的 RECOVERY_NEAR_WINDOW = 200。
const recoveryNearWindow = 200

// shiftedWindow 是「行号整体漂移」时在漂移后位置的搜索半径。
//
// 对账 findShiftedAnchorLine 里硬编码的 50（与 recoveryNearWindow 不同值）。
const shiftedWindow = 50

// 锚点格式：完整 `L<num>:<8hex>` 或仅位置 `L<num>`，两者都容忍行尾内容后缀
// （fresh anchors 现附 " → content"，模型回灌整行时不得解析失败）。
var (
	anchorFullRe = regexp.MustCompile(`^L(\d+):([0-9a-f]{8})(?:\s|$)`)
	anchorPosRe  = regexp.MustCompile(`^L(\d+)(?:\s|$)`)
)

// ParseAnchor 解析 "L<num>:<hex>" 或 "L<num>"；失败返回 nil。
//
// 对账 parseAnchor。注意**完整格式先试**——否则 "L5:a1b2c3d4" 会被
// 仅位置正则截成 L5。
func ParseAnchor(raw string) *Anchor {
	if m := anchorFullRe.FindStringSubmatch(raw); m != nil {
		line, err := strconv.Atoi(m[1])
		if err != nil || line < 1 {
			return nil
		}
		h := m[2]
		return &Anchor{Line: line, Hash: &h}
	}
	if m := anchorPosRe.FindStringSubmatch(raw); m != nil {
		line, err := strconv.Atoi(m[1])
		if err != nil || line < 1 {
			return nil
		}
		return &Anchor{Line: line}
	}
	return nil
}

// RecoverStaleAnchors 在当前文件内容中搜索过期锚点，返回恢复后的锚点。
//
// 对账 recoverStaleAnchors。策略：
//  1. 在每个锚点期望行附近 ±recoveryNearWindow 搜索；
//  2. 找不到时，检查已恢复锚点是否有**一致的行号漂移**，有则按漂移后
//     位置再搜。
//
// 全部恢复且保持严格升序才返回；否则返回 nil（调用方走诊断路径）。
//
// lines 是**按 \n 切分**的行数组（0-based 索引，行号 = 索引 + 1）。
func RecoverStaleAnchors(anchors []Anchor, lines []string) []Anchor {
	recovered := make([]Anchor, len(anchors))
	for i, a := range anchors {
		recovered[i] = a
	}
	used := make(map[int]bool)

	for i, anchor := range anchors {
		if anchor.Hash == nil {
			continue // 仅位置锚点不参与内容恢复
		}
		found := findAnchorLine(*anchor.Hash, anchor.Line, lines, used, recoveryNearWindow)
		if found == 0 {
			found = findShiftedAnchorLine(*anchor.Hash, anchor.Line, i, anchors, recovered, lines, used)
		}
		if found == 0 {
			return nil
		}
		recovered[i] = Anchor{Line: found, Hash: anchor.Hash}
		used[found] = true
	}

	// 必须保持升序——首尾锚点要定义有效区间。
	for i := 1; i < len(recovered); i++ {
		if recovered[i].Line <= recovered[i-1].Line {
			return nil
		}
	}
	return recovered
}

// findAnchorLine 在期望行 ±window 内搜索匹配 hash 的未占用行。
// 返回 0 表示未找到。
func findAnchorLine(hash string, expectedLine int, lines []string, used map[int]bool, window int) int {
	start := expectedLine - window
	if start < 1 {
		start = 1
	}
	end := expectedLine + window
	if end > len(lines) {
		end = len(lines)
	}
	for i := start; i <= end; i++ {
		if used[i] {
			continue
		}
		if prompt.HashLine(lines[i-1]) == hash {
			return i
		}
	}
	return 0
}

// findShiftedAnchorLine 用已恢复锚点的**一致漂移量**推算位置后再搜。
//
// 对账 findShiftedAnchorLine：首个锚点（index 0）无前置参考，直接返回 0；
// 漂移必须**全部一致且非零**，否则放弃。
func findShiftedAnchorLine(
	hash string, expectedLine, originalIndex int,
	originalAnchors, recovered []Anchor, lines []string, used map[int]bool,
) int {
	if originalIndex == 0 {
		return 0
	}
	var shifts []int
	for i := 0; i < originalIndex; i++ {
		if originalAnchors[i].Hash != nil {
			shifts = append(shifts, recovered[i].Line-originalAnchors[i].Line)
		}
	}
	if len(shifts) == 0 {
		return 0
	}
	first := shifts[0]
	if first == 0 {
		return 0
	}
	for _, s := range shifts {
		if s != first {
			return 0
		}
	}
	return findAnchorLine(hash, expectedLine+first, lines, used, shiftedWindow)
}

// AnchorMismatch 是一个锚点失配记录。
type AnchorMismatch struct {
	Anchor     Anchor
	ActualHash string // 实际哈希，或 "<eof>"
	ActualLine string // 该行内容，或 "<行号超出文件长度>"
}

// FormatStaleDiagnostic 渲染过期锚点诊断（模型可见）。
//
// 对账 formatStaleDiagnostic。核心是**给模型一条恢复路径**——诊断里附
// 「可立即重试的锚点」（用当前行哈希替换过期哈希）。没有它模型会反复
// 用已死的锚点重试（read_file 不输出哈希，只有 grep 会）。
//
// 若任一失配是 <eof>（行号越界），则不给重试锚点——越界行没有有效替代。
func FormatStaleDiagnostic(
	filePath string, anchors []Anchor, lines []string, mismatches []AnchorMismatch,
) string {
	var evidence strings.Builder
	for i, m := range mismatches {
		if i > 0 {
			evidence.WriteByte('\n')
		}
		ctx := "<未找到该行>"
		if m.Anchor.Line-1 >= 0 && m.Anchor.Line-1 < len(lines) {
			ctx = truncateRunes(lines[m.Anchor.Line-1], 60)
		}
		evidence.WriteString("  L" + itoa(m.Anchor.Line) + ": expected " + derefHash(m.Anchor.Hash) +
			" | actual " + m.ActualHash + " | content: " + ctx)
	}

	var allAnchors strings.Builder
	for i, a := range anchors {
		if i > 0 {
			allAnchors.WriteByte('\n')
		}
		allAnchors.WriteString("  L" + itoa(a.Line) + ":" + derefHash(a.Hash))
	}

	retryable := true
	for _, m := range mismatches {
		if m.ActualHash == "<eof>" {
			retryable = false
			break
		}
	}

	var tail string
	if retryable {
		parts := make([]string, len(anchors))
		for i, a := range anchors {
			hash := ""
			matched := false
			for _, m := range mismatches {
				if m.Anchor == a {
					hash = m.ActualHash
					matched = true
					break
				}
			}
			if !matched {
				// 未失配的锚点保留原哈希；仅位置锚点用当前行哈希补齐
				if a.Hash != nil {
					hash = *a.Hash
				} else {
					cur := ""
					if a.Line-1 >= 0 && a.Line-1 < len(lines) {
						cur = lines[a.Line-1]
					}
					hash = prompt.HashLine(cur)
				}
			}
			parts[i] = `"L` + itoa(a.Line) + ":" + hash + `"`
		}
		tail = `若上方所示 "content" 正是你要替换的行，请立即用以下锚点重试：anchors: [` +
			strings.Join(parts, ", ") + `]` + "\n" +
			"若不是正确的行，请用 grep 重新定位目标（grep 输出含新鲜的 L<line>:<hash> 锚点提示；read_file 不会输出哈希）。"
	} else {
		tail = "锚点行号超出当前文件长度。请用 grep 重新定位目标（grep 输出含新鲜的 L<line>:<hash> 锚点提示；read_file 不会输出哈希）。"
	}

	return "hash_edit 在 " + filePath + " 上失败：" + itoa(len(mismatches)) + " 个锚点已过期。\n" +
		"自你上次 read_file 以来文件已变化（可能是你自己更早的编辑导致）。\n\n" +
		"期望的锚点：\n" + allAnchors.String() + "\n\n" +
		"过期锚点（该行当前哈希）：\n" + evidence.String() + "\n\n" +
		tail + "\n" +
		"不要再用已经用过的锚点重试——它们是一次性坐标，完全相同的调用还会再次失败。"
}

// derefHash 渲染锚点哈希用于诊断文本。
//
// 对账 JS 的模板插值 `${a.hash}`：hash 为 null 时输出**字面量 "null"**
// （不是空串、不是占位符）。实测确认：仅位置锚点越界时诊断显示
// "L99: expected null | actual <eof>"。
func derefHash(h *string) string {
	if h == nil {
		return "null"
	}
	return *h
}

// truncateRunes 按 **rune 数**截断（对账 JS 的 .slice(0, 60)）。
//
// 注意：JS 的 slice 按 UTF-16 code unit 计。此处用 rune 近似——
// 差异只出现在含代理对（emoji）的行，诊断文本截断处。
// 已知偏差，见 HANDOFF。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// itoa 是 strconv.Itoa 的短别名（本文件内高频使用）。
func itoa(n int) string { return strconv.Itoa(n) }

// ── 工具本体 ──

// hashEditTool 是 hash_edit 工具的实现。
//
// 对账 src/tools/hash-edit.ts 的 HASH_EDIT_TOOL。核心语义：锚点是
// **一次性坐标**，哈希校验保证编辑落在模型以为的那一行。
//
// **已知降级**（见 HANDOFF）：
//   - 指针回灌守卫（pointer-guard）未移植——它依赖 4 个未移植的
//     arg-processor 常量模块。风险：模型可能把历史里的指针文本
//     （"[hash_edit applied to …]"）当 new_string 传回来，被写进文件。
//   - 写入后语法检查（checkSyntax）未移植——TS 侧会在致命语法错误时
//     回滚。Go 侧直接写入，无回滚。
//   - 编辑失败计数门（连续 3 次失败要求重新 read_file）未移植。
//   - dry_run 的 diff 预览（buildFileDiff）未移植——返回简化的变更摘要。
type hashEditTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
	// Stack 是备份栈（写入前备份，供回滚）。
	Stack *recovery.Stack
}

// HashEdit 构造 hash_edit 工具。
func HashEdit(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &hashEditTool{Cwd: cwd, Grants: grants, Stack: recovery.DefaultStack()}
	t.def = contract.Definition{
		Name: "hash_edit",
		Description: `内容哈希锚定的文件编辑。比 edit_file 更安全的替代。

锚点格式为 L<line>:<8-char-hex>（完整哈希校验）或 L<line>
（仅位置快速路径——仅在你刚读过该文件时使用）。提供 1-3 个
锚点：首尾锚点定义含两端在内的替换区间；中间锚点校验区间内部。
单锚点模式替换该行（要插入就把该行内容原样放进 new_string 首/尾）。

哈希：SHA256(line_content_without_trailing_cr)[0:8]。
grep 结果对单文件匹配附带锚点提示。

### 锚点是一次性坐标
任何对该文件的写入都会让此前取得的锚点作废——包括你自己上一次的
hash_edit / edit_file / write_file。编辑点之后的所有行号还会整体漂移
（漂移量 = 新行数 − 旧行数）。拿旧锚点重试同一调用只会再次失败。

同一文件连续编辑：
- 改**刚编辑过的那一块**：用成功回传的「新鲜锚点」（只覆盖编辑点前后
  各一行与新块首尾）。
- 改**该文件的其他位置**：先用 grep 重新取锚点。read_file 的输出不带
  哈希，只有 grep 会给出 L<line>:<hash> 提示。
- 一次要改多处时，**从文件末尾往前改**——这样先改的位置不会让后面
  待改位置的行号漂移。

仅位置模式（L<line> 无哈希）适合首次编辑，且绝不能连续链式使用；
链式编辑一律用带哈希的完整锚点（L<line>:<hash>）。

### 示例
替换 L5-L7：anchors=["L5:a1b2c3d4","L7:e5f6a7b8"], new_string="新5\n新6\n新7"
删除 L10-L12：anchors=["L10:deadbeef","L12:cafebabe"], new_string=""
在 L42 后插入：anchors=["L42:feedface"], new_string="<L42 原内容>\n新增行"

多处改动、超过约 20 行的编辑或结构性重构，优先用
apply_patch 加 unified diff。

注意：new_string 较大时，消息历史只保留短指针
（file_path + 大小）——看到指针说明那次编辑已成功落盘，
不是你写了占位符，不要重做。后续轮次用 read_file 回看当前内容。
new_string 必须是真实文件内容；把历史里的
[hash_edit applied to …] 指针原样传回会被拦截。`,
		InputSchema: objSchemaOrdered([]string{"file_path", "anchors", "new_string", "dry_run"}, map[string]any{
			"file_path": strProp("要编辑文件的绝对路径。先提供此参数。"),
			"anchors": arrayPropOrdered(
				"1-3 个锚点，格式 \"L<line>:<8-char-hex>\"（完整）或 \"L<line>\"（仅位置）。首尾锚点定义含两端在内的替换区间。",
				"string"),
			"new_string": strProp("锚定区间的替换文本。传 \"\" 表示删除。最后提供此参数。"),
			"dry_run":    boolProp("为 true 时，计算并返回将要应用的 diff，但不写盘。"),
		}, "file_path", "anchors", "new_string"),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

func (t *hashEditTool) RequiresApproval(p *CallParams) bool {
	return p.ApprovalMode != "dangerously-skip-permissions"
}

func (t *hashEditTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *hashEditTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	path := strArg(p.Input, "file_path")
	newStr, hasNew := p.Input["new_string"].(string)
	if path == "" || !hasNew {
		return contract.Result{
			Content: "hash_edit 需要 file_path、anchors、new_string 三个参数",
			IsError: true,
		}, nil
	}

	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}

	// 文件存在性（mtime 漂移检测是 TS 侧行为，Go 版未移植，见降级说明）
	if _, err := os.Stat(vr.Path); err != nil {
		return contract.Result{Content: "错误：文件未找到：" + path, IsError: true}, nil
	}

	// 锚点解析与校验
	rawAnchors, _ := p.Input["anchors"].([]any)
	if len(rawAnchors) == 0 || len(rawAnchors) > 3 {
		return contract.Result{
			Content: `错误：anchors 必须是 1-3 个 "L<line>:<hash>" 或 "L<line>" 字符串组成的数组`,
			IsError: true,
		}, nil
	}
	anchors := make([]Anchor, 0, len(rawAnchors))
	for _, raw := range rawAnchors {
		s, _ := raw.(string)
		a := ParseAnchor(s)
		if a == nil {
			return contract.Result{
				Content: `错误：无效锚点格式 "` + s + `"。期望 "L<num>:<8-char-hex>"（如 "L5:a1b2c3d4"）或 "L<num>"（如 "L5"）`,
				IsError: true,
			}, nil
		}
		anchors = append(anchors, *a)
	}
	// 严格升序——反序会导致行重复与静默文件损坏
	for i := 1; i < len(anchors); i++ {
		if anchors[i].Line <= anchors[i-1].Line {
			return contract.Result{
				Content: "错误：anchors 必须严格按行号升序排列。" +
					"锚点 " + itoa(i+1) + "（L" + itoa(anchors[i].Line) + "）没有排在锚点 " +
					itoa(i) + "（L" + itoa(anchors[i-1].Line) + "）之后。",
				IsError: true,
			}, nil
		}
	}

	// 读取并归一化到 LF
	raw, err := os.ReadFile(vr.Path)
	if err != nil {
		return contract.Result{Content: "读取失败：" + err.Error(), IsError: true}, nil
	}
	rawContent := string(raw)
	eol := chooseEOL(vr.Path, detectEOL(rawContent))
	content := toLF(rawContent)
	lines := strings.Split(content, "\n")

	// 校验锚点
	var mismatches []AnchorMismatch
	for _, a := range anchors {
		if a.Line > len(lines) {
			mismatches = append(mismatches, AnchorMismatch{
				Anchor: a, ActualHash: "<eof>", ActualLine: "<行号超出文件长度>",
			})
			continue
		}
		if a.Hash != nil {
			actual := prompt.HashLine(lines[a.Line-1])
			if actual != *a.Hash {
				mismatches = append(mismatches, AnchorMismatch{
					Anchor: a, ActualHash: actual, ActualLine: lines[a.Line-1],
				})
			}
		}
	}

	// 过期恢复：全部锚点都带哈希时才尝试
	if len(mismatches) > 0 {
		allFullHash := true
		for _, m := range mismatches {
			if m.Anchor.Hash == nil {
				allFullHash = false
				break
			}
		}
		if allFullHash {
			if rec := RecoverStaleAnchors(anchors, lines); rec != nil {
				return t.applyEdit(p, vr.Path, lines, rec, newStr, content, eol, true)
			}
		}
		return contract.Result{
			Content: FormatStaleDiagnostic(path, anchors, lines, mismatches),
			IsError: true,
		}, nil
	}

	return t.applyEdit(p, vr.Path, lines, anchors, newStr, content, eol, false)
}

// applyEdit 执行 splice 与写盘。
//
// recovered 为 true 时消息里追加「已自动恢复 N 个过期锚点」。
func (t *hashEditTool) applyEdit(
	p *CallParams, absPath string, lines []string, anchors []Anchor,
	newStr, oldContent string, eol EOL, recovered bool,
) (contract.Result, error) {
	firstLine := anchors[0].Line
	lastLine := anchors[len(anchors)-1].Line

	before := lines[:firstLine-1]
	after := lines[lastLine:]
	var newLines []string
	if newStr != "" {
		newLines = strings.Split(newStr, "\n")
	}
	combined := make([]string, 0, len(before)+len(newLines)+len(after))
	combined = append(combined, before...)
	combined = append(combined, newLines...)
	combined = append(combined, after...)
	newContent := strings.Join(combined, "\n")

	if boolArg(p.Input, "dry_run") {
		return contract.Result{
			Content: "预览（dry_run）" + absPath + " — 未写入任何更改：\n\n" +
				"将 L" + itoa(firstLine) + "-L" + itoa(lastLine) +
				"（" + itoa(lastLine-firstLine+1) + " 行）替换为 " + itoa(len(newLines)) + " 行\n" +
				"（Go 版未移植 diff 预览，见 HANDOFF 降级项）",
		}, nil
	}

	// **写入前备份**（供回滚）。dry_run 分支已在上方提前返回，不会走到这里。
	if _, err := t.Stack.TrackFileChange(t.Cwd, recovery.FileChangeRecord{
		FilePath:   relForRecovery(t.Cwd, absPath),
		Action:     "edit",
		ToolCallID: "hash_edit",
	}); err != nil {
		return contract.Result{Content: "备份失败（写入已中止）：" + err.Error(), IsError: true}, nil
	}

	if err := os.WriteFile(absPath, []byte(applyEOL(newContent, eol)), 0o644); err != nil {
		return contract.Result{Content: "写入失败：" + err.Error(), IsError: true}, nil
	}
	if p.OnFileWrite != nil {
		p.OnFileWrite(absPath)
	}

	// 恢复计数：带哈希且行号相对声明位置变了的锚点数。
	// 注意 anchors 此时已是**恢复后**的锚点，故需与原始声明比较——
	// 但原始声明未传入，这里退化为「有恢复即报恢复数」的近似。
	// TS 侧精确统计（对账 buildFreshAnchors 前的 recoveredCount）。
	recoveredCount := 0
	if recovered {
		recoveredCount = len(anchors)
	}

	// ── 应用后语法检查 + 回滚 ──
	//
	// 对账 TS 的 checkSyntax 分支：致命语法错误时从备份恢复并报错。
	// TS 侧还有「失败计数门」（连续 3 次要求先 read_file）——那是独立欠账，
	// 尚未移植（见 HANDOFF）。
	if chk := syntaxcheck.Check(absPath, applyEOL(newContent, eol)); chk.Fatal != "" {
		rel := relForRecovery(t.Cwd, absPath)
		restored := t.Stack.RestoreLatestBackup(t.Cwd, rel, p.SessionID)
		rollbackMsg := "自动回滚失败。"
		if restored {
			rollbackMsg = "更改已自动回滚。"
		}
		// 失败计数门（≥3 次时前置提示）
		incrementEditFailCount(absPath)
		gate := editFailGatePrefix(absPath, "hash_edit")
		return contract.Result{
			Content: gate + "错误：" + chk.Fatal + "\n\n" + rollbackMsg +
				"\n\n请修复编辑后重试。复杂改动建议优先用 apply_patch 加 unified diff。",
			IsError: true,
		}, nil
	}
	// 成功：清零失败计数
	resetEditFailCount(absPath)

	recoveredInfo := ""
	if recovered {
		recoveredInfo = "（已自动恢复 " + itoa(recoveredCount) + " 个过期锚点）"
	}
	// 成功消息用**绝对路径**（对账 TS；read_file 的 relLabel 语义不适用）
	fresh := prompt.BuildFreshAnchors(strings.Split(newContent, "\n"), len(before), len(newLines))
	return contract.Result{
		Content: "hash_edit" + recoveredInfo + " 已应用到 " + absPath + "：将 L" +
			itoa(firstLine) + "-L" + itoa(lastLine) + "（" + itoa(lastLine-firstLine+1) +
			" 行）替换为 " + itoa(len(newLines)) + " 行" + fresh,
	}, nil
}
