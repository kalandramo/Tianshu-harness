package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/filediff"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
	"github.com/kalandramo/tianshu/go/internal/recovery"
	"github.com/kalandramo/tianshu/go/internal/syntaxcheck"
)

// ── write_file ──

type writeFileTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
	// Stack 是备份栈（写入前备份，供回滚）。
	Stack *recovery.Stack
}

// WriteFile 构造 write_file 工具。
func WriteFile(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &writeFileTool{Cwd: cwd, Grants: grants, Stack: recovery.DefaultStack()}
	t.def = contract.Definition{
		Name: "write_file",
		Description: `创建、覆盖或追加一个文件。自动创建父目录。

- 对已有文件的定点修改优先用 edit_file
- write_file 只用于新文件或整文件重写
- overwrite 时 content 是完整文件内容（不是 diff）；append 时 content 是本次追加的块`,
		InputSchema: objSchemaOrdered([]string{"file_path", "content", "mode"}, map[string]any{
			"file_path": strProp("文件的绝对路径。先提供此参数。"),
			"content":   strProp("完整文件内容（append 时为本块内容；不是 diff）。最后提供此参数。"),
			"mode": enumPropOrdered(
				"写入模式。缺省 overwrite 整文件覆盖；append 原样追加到文件末尾（不自动加换行），用于分块写入新文件。",
				[]string{"overwrite", "append"}),
		}, "file_path", "content"),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

// RequiresApproval 写操作**恒需批准**（对账 TS `requiresApproval: () => true`，
// `src/tools/write-file.ts:349`）。
//
// **为什么不判档位**（第七十四刀改）：档位语义由 `agent.decideApprovalGate`
// **单点判定**——它开头就处理 skip 档（`dangerously-skip-permissions` →
// 不拦），且 `auto-safe` 分支读 `isHighRisk`（不读本函数）。故本函数的返回值
// **只在 manual 档被消费**，那里 `!= skip` 恒为 true——与 `() => true` 等价。
//
// **改前是 `p.ApprovalMode != "dangerously-skip-permissions"`**：那让档位语义
// 被**两处**判定（本函数 + `decideApprovalGate`），任一处改动会不一致。
// 这是**结构性隐患**，非行为缺陷（改前后行为等价）。
func (t *writeFileTool) RequiresApproval(*CallParams) bool {
	return true
}

func (t *writeFileTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *writeFileTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	path := strArg(p.Input, "file_path")
	if path == "" {
		return contract.Result{Content: "write_file 需要 file_path 参数", IsError: true}, nil
	}
	content, hasContent := p.Input["content"].(string)
	if !hasContent {
		return contract.Result{Content: "write_file 需要 content 参数", IsError: true}, nil
	}
	mode := strArg(p.Input, "mode")
	if mode == "" {
		mode = "overwrite"
	}

	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeWrite, &pathsafe.Options{Grants: effectiveGrants(p, t.Grants)})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}

	if err := os.MkdirAll(filepath.Dir(vr.Path), 0o755); err != nil {
		return contract.Result{Content: fmt.Sprintf("创建父目录失败：%v", err), IsError: true}, nil
	}

	// **写入前备份**（供回滚）。新文件无备份可做——trackFileChange 只备份
	// 已存在的文件（对账 TS）。
	// 同时记录「写入前是否存在」——语法检查失败时的回滚策略依此分流
	// （新文件删除、已存在文件从备份恢复）。
	_, preStatErr := os.Stat(vr.Path)
	existedBefore := preStatErr == nil

	// **旧内容**（供 W4 的变更行区间计算）。
	//
	// 对账 TS `write-file.ts:307` 的 `haveOldContentForDiff` + `oldContentForDiff`。
	// 读失败/不存在时留空 → `ComputeChangedLineRanges` 会把整个 AFTER 文件
	// 当作改动（安全降级，见 `filediff` 的注释）。
	//
	// 注意 append 模式：命中「追加」语义，但 diff 仍以「拼接后全文」为 after
	// （见下方 afterContent）。
	oldContent, haveOldContent := readFileForDiff(vr.Path)

	if _, err := t.Stack.TrackFileChange(t.Cwd, recovery.FileChangeRecord{
		FilePath:   relForRecovery(t.Cwd, vr.Path),
		Action:     "write",
		ToolCallID: fileChangeToolID(p, "write_file"),
	}); err != nil {
		return contract.Result{Content: fmt.Sprintf("备份失败（写入已中止）：%v", err), IsError: true}, nil
	}

	var writeErr error
	if mode == "append" {
		f, err := os.OpenFile(vr.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			writeErr = err
		} else {
			_, writeErr = f.WriteString(content)
			_ = f.Close()
		}
	} else {
		writeErr = os.WriteFile(vr.Path, []byte(content), 0o644)
	}
	if writeErr != nil {
		return contract.Result{Content: fmt.Sprintf("写入失败：%v", writeErr), IsError: true}, nil
	}

	// ── 应用后语法检查 + 回滚 ──
	//
	// 对账 TS 的 checkSyntax 分支。回滚策略按「写入前是否存在」分流：
	// 新文件**删除**（无备份可恢复，不把语法损坏的残尸留在磁盘上）；
	// 已存在文件从备份恢复。
	if chk := syntaxcheck.Check(vr.Path, content); chk.Fatal != "" {
		rel := relForRecovery(t.Cwd, vr.Path)
		rollbackMsg := "自动回滚失败。"
		if !existedBefore {
			if err := os.Remove(vr.Path); err == nil {
				rollbackMsg = "新文件已自动移除（写入前不存在，无备份可恢复）。"
			}
		} else if t.Stack.RestoreLatestBackup(t.Cwd, rel, p.SessionID) {
			rollbackMsg = "更改已自动回滚。"
		}
		// 失败计数门（≥3 次时前置提示）
		incrementEditFailCount(vr.Path)
		gate := editFailGatePrefix(vr.Path, "写入")
		return contract.Result{
			Content: gate + "错误：" + chk.Fatal + "\n\n" + rollbackMsg + "\n\n请修复内容后重试。",
			IsError: true,
		}, nil
	}
	// 成功：清零失败计数
	resetEditFailCount(vr.Path)

	// 登记文件写入（让证据追踪感知）
	if p.OnFileWrite != nil {
		p.OnFileWrite(vr.Path)
	}

	label := relLabel(t.Cwd, vr.Path)
	lines := strings.Count(content, "\n") + 1
	if mode == "append" {
		return contract.Result{
			Content:       fmt.Sprintf("已追加 %d 行到 %s", lines, label),
			ChangedRanges: computeWriteChangedRanges(oldContent, haveOldContent, content, mode),
		}, nil
	}
	return contract.Result{
		Content:       fmt.Sprintf("已写入 %s（%d 行）", label, lines),
		ChangedRanges: computeWriteChangedRanges(oldContent, haveOldContent, content, mode),
	}, nil
}

// readFileForDiff 读文件内容供 diff 用（**best-effort**，失败即放弃）。
//
// 对账 TS `write-file.ts` 的 `oldContentForDiff` 捕获：读不到就当「新文件」，
// 后果只是变更区间退化为整文件（多显示诊断，不隐藏错误）。
func readFileForDiff(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// computeWriteChangedRanges 算写操作波及的 AFTER 行区间（供 LSP 诊断区域收敛）。
//
// 对账 TS `write-file.ts:320-337`：`computeChangedLineRanges(oldContentForDiff, afterForDiff)`。
//
// **after 语义**：overwrite 时是 content 本身；append 时是「旧内容 + 新内容」
// （因为盘上结果是拼接后的全文，诊断报告的正是它）。
func computeWriteChangedRanges(oldContent string, haveOld bool, content, mode string) []contract.Range {
	after := content
	if mode == "append" && haveOld {
		after = oldContent + content
	}
	// 读不到旧内容 → before 传空串 → filediff 返回「覆盖全文件」区间
	before := ""
	if haveOld {
		before = oldContent
	}
	rs := filediff.ComputeChangedLineRanges(before, after)
	if len(rs) == 0 {
		return nil
	}
	out := make([]contract.Range, 0, len(rs))
	for _, r := range rs {
		out = append(out, contract.Range{Start: r.Start, End: r.End})
	}
	return out
}

// ── edit_file ──

type editFileTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
	// Stack 是备份栈（写入前备份，供回滚）。
	Stack *recovery.Stack
}

// EditFile 构造 edit_file 工具。
func EditFile(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &editFileTool{Cwd: cwd, Grants: grants, Stack: recovery.DefaultStack()}
	t.def = contract.Definition{
		Name: "edit_file",
		Description: `在已有文件中执行精确字符串替换。

- old_string 必须在文件中唯一——必要时带上周边上下文
- 严格保留文件原有的缩进（tabs/spaces）
- replace_all 替换所有出现处
- 大编辑后消息历史只保留短指针——看到指针说明编辑已成功，不要重做`,
		InputSchema: objSchemaOrdered([]string{
			"file_path", "old_string", "new_string", "replace_all", "expected_count", "dry_run",
		}, map[string]any{
			"file_path":      strProp("要编辑文件的绝对路径。先提供此参数。"),
			"old_string":     strProp("要替换的原始文本（必须在文件中唯一）"),
			"new_string":     strProp("替换后的文本"),
			"replace_all":    boolProp("替换 old_string 的所有出现处（默认：false）"),
			"expected_count": numProp("replace_all 为 true 时预期的替换次数。实际次数不符时返回警告，便于你用 grep 核实是否有遗漏（例如缩进差异导致的漏配）。"),
			"dry_run":        boolProp("为 true 时，计算并返回将要应用的 diff，但不写盘。"),
		}, "file_path", "old_string", "new_string"),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

// RequiresApproval 写操作**恒需批准**（对账 TS `requiresApproval: () => true`，
// `src/tools/edit.ts:390`）。档位语义由 `decideApprovalGate` 单点判定——见
// `writeFileTool.RequiresApproval` 的说明。
func (t *editFileTool) RequiresApproval(*CallParams) bool {
	return true
}

func (t *editFileTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *editFileTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	path := strArg(p.Input, "file_path")
	oldStr, hasOld := p.Input["old_string"].(string)
	newStr, hasNew := p.Input["new_string"].(string)
	if path == "" || !hasOld || !hasNew {
		return contract.Result{
			Content: "edit_file 需要 file_path、old_string、new_string 三个参数",
			IsError: true,
		}, nil
	}
	replaceAll := boolArg(p.Input, "replace_all")

	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeWrite, &pathsafe.Options{Grants: effectiveGrants(p, t.Grants)})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}

	data, err := os.ReadFile(vr.Path)
	if err != nil {
		return contract.Result{Content: fmt.Sprintf("读取失败：%v", err), IsError: true}, nil
	}
	text := string(data)

	count := strings.Count(text, oldStr)
	if count == 0 {
		return contract.Result{
			Content: fmt.Sprintf(
				"%s 中未找到 old_string。可能原因：缩进/空白不符、内容已变化、或转义差异。"+
					"建议先 read_file 确认当前内容。",
				relLabel(t.Cwd, vr.Path)),
			IsError: true,
		}, nil
	}
	// 唯一性检查——多处匹配时拒绝（避免误改）
	if count > 1 && !replaceAll {
		return contract.Result{
			Content: fmt.Sprintf(
				"old_string 在 %s 中出现 %d 次，不唯一。请带上更多周边上下文使其唯一，或用 replace_all=true 明确替换全部。",
				relLabel(t.Cwd, vr.Path), count),
			IsError: true,
		}, nil
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(text, oldStr, newStr)
	} else {
		updated = strings.Replace(text, oldStr, newStr, 1)
	}

	// ── dry_run：只预览，**绝不写盘** ──
	//
	// 对账 TS `edit.ts:188-190`（每个分支都有这个早退）：
	//
	//	if (dryRun) {
	//	  return buildDryRunPreview(params.cwd, filePath, freshContent, newContent)
	//	}
	//
	// ★ **必须插在 `TrackFileChange` / `os.WriteFile` 之前**。
	// 此前 Go 侧声明了 `dry_run` 参数却从不读取——`dry_run: true` 时
	// **文件直接落盘**（模型预期预览、实际已改）。这是本刀修的既有缺陷。
	//
	// 早退位置在 `updated` 计算之后：预览需要「应用后」的内容来做
	// diff 与语法检查，但不需要（也不允许）碰磁盘。
	if boolArg(p.Input, "dry_run") {
		return buildDryRunPreview(t.Cwd, vr.Path, text, updated), nil
	}

	// **写入前备份**（供回滚）
	if _, err := t.Stack.TrackFileChange(t.Cwd, recovery.FileChangeRecord{
		FilePath:   relForRecovery(t.Cwd, vr.Path),
		Action:     "edit",
		ToolCallID: fileChangeToolID(p, "edit_file"),
	}); err != nil {
		return contract.Result{Content: fmt.Sprintf("备份失败（写入已中止）：%v", err), IsError: true}, nil
	}

	if err := os.WriteFile(vr.Path, []byte(updated), 0o644); err != nil {
		return contract.Result{Content: fmt.Sprintf("写入失败：%v", err), IsError: true}, nil
	}
	if p.OnFileWrite != nil {
		p.OnFileWrite(vr.Path)
	}
	msg := fmt.Sprintf("已编辑 %s（替换 %d 处）", relLabel(t.Cwd, vr.Path), count)
	if replaceAll && count > 1 {
		msg = fmt.Sprintf("已编辑 %s（replace_all，替换 %d 处）", relLabel(t.Cwd, vr.Path), count)
	}
	// W4：算变更行区间（`data` 是写入前的原始内容，`updated` 是写入后的）
	return contract.Result{
		Content:       msg,
		ChangedRanges: computeEditChangedRanges(string(data), updated),
	}, nil
}

// buildDryRunPreview 构造 dry_run 的预览结果（**不写盘**）。
//
// 对账 TS `edit.ts:454-479` 的 `buildDryRunPreview`：
//
//	① 语法检查（**若应用会否出现错误**——这是 dry_run 的主要价值）
//	② diff（无变化时给占位文案）
//	③ changedRanges
//	④ content 格式：`预览（dry_run）<path> — 未写入任何更改：\n\n<diff>`
//
// # 与已落地路径的差别（重要）
//
// 常规路径的语法检查在**写盘之后**做，失败则回滚。dry_run 不能这么做
// （不能先写再回滚）——它直接在**内存里的 after 内容**上检查。
// 故此处的检查是**预测性**的（「若应用将出现语法错误：…」），
// 不能复用写后检查的文案。
func buildDryRunPreview(cwd, absPath, before, after string) contract.Result {
	var warn string
	chk := syntaxcheck.Check(absPath, after)
	switch {
	case chk.Fatal != "":
		warn = "若应用将出现语法错误：" + chk.Fatal
	case chk.Warning != "":
		warn = chk.Warning
	}

	// diff 与 changedRanges 都是 best-effort（对账 TS 的 try/catch）：
	// 失败不该让预览失败——预览的核心价值是「未写盘 + 语法预警」。
	diff := filediff.BuildFileDiff(relLabel(cwd, absPath), before, after, 0)
	ranges := computeEditChangedRanges(before, after)

	body := diff
	if body == "" {
		body = "（无文本变更）"
	}
	// 路径用 relLabel（与 Go 侧其他消息「已写入 %s」「已编辑 %s」一致）——
	// 对账 TS 用 `filePath` 原样（模型多传绝对路径），relLabel 在本仓库
	// cwd 内会给出更短、更一致的相对形式。
	content := fmt.Sprintf("预览（dry_run）%s — 未写入任何更改：\n\n%s", relLabel(cwd, absPath), body)
	if warn != "" {
		content += "\n\n" + warn
	}

	return contract.Result{
		Content: content,
		// 对账 TS：`uiContent: diff || undefined`——有 diff 才给 UI 覆盖，
		// 否则回落到 Content（`contract.Result.UIContent` 的缺省语义）。
		UIContent:     diff,
		ChangedRanges: ranges,
	}
}

// computeEditChangedRanges 算编辑波及的 AFTER 行区间（供 LSP 诊断区域收敛）。
//
// 对账 TS `hash-edit.ts:128` 的 `computeChangedLineRanges(before, after)`。
func computeEditChangedRanges(before, after string) []contract.Range {
	rs := filediff.ComputeChangedLineRanges(before, after)
	if len(rs) == 0 {
		return nil
	}
	out := make([]contract.Range, 0, len(rs))
	for _, r := range rs {
		out = append(out, contract.Range{Start: r.Start, End: r.End})
	}
	return out
}

// ── glob ──

type globTool struct {
	baseTool
	Cwd string
}

// Glob 构造 glob 工具。
func Glob(cwd string) Tool {
	t := &globTool{Cwd: cwd}
	t.def = contract.Definition{
		Name: "glob",
		Description: `查找匹配 glob 模式的文件。

- 读取文件前，先用 glob 按名称或模式定位文件
- 支持 ** 递归匹配目录、* 通配符、? 单字符、{a,b} 多选一
- 结果排序返回，上限 500 条`,
		InputSchema: objSchemaOrdered([]string{"pattern", "path"}, map[string]any{
			"pattern": strProp("Glob 模式，如 \"src/**/*.ts\" 或 \"*.md\""),
			"path":    strProp("搜索的根目录（默认：cwd）"),
		}, "pattern"),
	}
	t.enabled = true
	t.concurrent = true
	return t
}

func (t *globTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *globTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	pattern := strArg(p.Input, "pattern")
	if pattern == "" {
		return contract.Result{Content: "glob 需要 pattern 参数", IsError: true}, nil
	}
	root := strArg(p.Input, "path")
	if root == "" {
		root = t.Cwd
	} else {
		// 同 fileinfo：接会话授权（第八十一刀订正）。
		vr := pathsafe.Validate(t.Cwd, root, pathsafe.ModeRead, &pathsafe.Options{Grants: effectiveGrants(p, nil)})
		if !vr.OK {
			return contract.Result{Content: vr.Error, IsError: true}, nil
		}
		root = vr.Path
	}

	const limit = 500
	var matches []string

	// 支持 ** 递归：把 pattern 拆为前缀目录与文件名模式
	err := walkGlob(root, pattern, func(rel string) bool {
		matches = append(matches, rel)
		return len(matches) < limit
	})
	if err != nil {
		return contract.Result{Content: fmt.Sprintf("搜索失败：%v", err), IsError: true}, nil
	}

	if len(matches) == 0 {
		return contract.Result{
			Content: fmt.Sprintf("无匹配文件（pattern=%s, root=%s）。", pattern, relLabel(t.Cwd, root)),
		}, nil
	}
	out := strings.Join(matches, "\n")
	truncNote := ""
	if len(matches) >= limit {
		truncNote = fmt.Sprintf("\n\n[已达上限 %d 条——结果被截断，请收窄 pattern] ", limit)
	}
	return contract.Result{Content: out + truncNote}, nil
}

// walkGlob 按 glob 模式遍历。
//
// 用 filepath.WalkDir + 模式匹配：先判断模式是否含 ** 决定匹配策略。
func walkGlob(root, pattern string, yield func(rel string) bool) error {
	recursive := strings.Contains(pattern, "**")
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 权限错误等跳过，不中断整体遍历
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		// 跳过常见的重目录
		if d.IsDir() {
			base := d.Name()
			if base == "node_modules" || base == ".git" || base == "dist" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if matchGlob(rel, pattern, recursive) {
			if !yield(rel) {
				return filepath.SkipAll
			}
		}
		return nil
	})
}

// matchGlob 匹配单个路径。
//
// 非递归模式（不含 **）只匹配 basename 或完整相对路径；
// 递归模式匹配完整相对路径（** 跨目录）。
func matchGlob(rel, pattern string, recursive bool) bool {
	pattern = filepath.ToSlash(pattern)
	if recursive {
		// 把 glob 转为逐段匹配
		return matchSegments(strings.Split(rel, "/"), strings.Split(pattern, "/"))
	}
	// 无斜杠的模式匹配 basename
	if !strings.Contains(pattern, "/") {
		ok, err := filepath.Match(pattern, filepath.Base(rel))
		return err == nil && ok
	}
	ok, err := filepath.Match(pattern, rel)
	return err == nil && ok
}

// matchSegments 逐段匹配（支持 ** 跨目录）。
func matchSegments(pathSegs, patSegs []string) bool {
	if len(patSegs) == 0 {
		return len(pathSegs) == 0
	}
	head := patSegs[0]
	if head == "**" {
		// ** 匹配零个或多个路径段
		for skip := 0; skip <= len(pathSegs); skip++ {
			if matchSegments(pathSegs[skip:], patSegs[1:]) {
				return true
			}
		}
		return false
	}
	if len(pathSegs) == 0 {
		return false
	}
	ok, err := filepath.Match(head, pathSegs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pathSegs[1:], patSegs[1:])
}
