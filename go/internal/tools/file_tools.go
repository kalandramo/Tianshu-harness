package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// ── write_file ──

type writeFileTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
}

// WriteFile 构造 write_file 工具。
func WriteFile(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &writeFileTool{Cwd: cwd, Grants: grants}
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

// RequiresApproval 写操作需要批准（除非会话档位已放开）。
func (t *writeFileTool) RequiresApproval(p *CallParams) bool {
	return p.ApprovalMode != "dangerously-skip-permissions"
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

	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}

	if err := os.MkdirAll(filepath.Dir(vr.Path), 0o755); err != nil {
		return contract.Result{Content: fmt.Sprintf("创建父目录失败：%v", err), IsError: true}, nil
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

	// 登记文件写入（让证据追踪感知）
	if p.OnFileWrite != nil {
		p.OnFileWrite(vr.Path)
	}

	label := relLabel(t.Cwd, vr.Path)
	lines := strings.Count(content, "\n") + 1
	if mode == "append" {
		return contract.Result{Content: fmt.Sprintf("已追加 %d 行到 %s", lines, label)}, nil
	}
	return contract.Result{Content: fmt.Sprintf("已写入 %s（%d 行）", label, lines)}, nil
}

// ── edit_file ──

type editFileTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
}

// EditFile 构造 edit_file 工具。
func EditFile(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &editFileTool{Cwd: cwd, Grants: grants}
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

func (t *editFileTool) RequiresApproval(p *CallParams) bool {
	return p.ApprovalMode != "dangerously-skip-permissions"
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

	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
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
	return contract.Result{Content: msg}, nil
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
		vr := pathsafe.Validate(t.Cwd, root, pathsafe.ModeRead, nil)
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
