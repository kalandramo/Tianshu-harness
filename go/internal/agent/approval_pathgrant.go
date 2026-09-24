// approval_pathgrant.go —— 越界文件路径判定（档位门控的 pathGrantNeed 输入）。
//
// 对账 TS `outOfWorkspaceFilePaths`（`tool-pipeline.ts:259-289`）。
//
// ## 为什么需要它
//
// 审批决策树里 `pathGrantNeed` 是「出界路径 → 必须问」的判据。缺了它，
// 出界读写会走 in-workspace 分支被静默放行——`echo key >> ~/.ssh/authorized_keys`
// 这类越界写在 auto-safe 档下零提示执行。
//
// ## 与 bash 侧的互补
//
// bash 的越界写目标由 `HasOutOfWorkspaceWriteTarget`（Wave 1）判——那是**文本
// 启发式**。文件工具走**真实路径校验**（`pathsafe.Validate`，含符号链接解析、
// 授权查询、敏感文件检测）。两者覆盖不同的工具面，不可互相替代。
package agent

import (
	"os"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// fileToolModes 对账 TS `FILE_TOOL_MODES`（`tool-pipeline.ts:243-249`）。
//
// 决定各文件工具的访问模式（读/写）。写侧是更强的授权面。
var fileToolModes = map[string]pathsafe.Mode{
	"read_file":  pathsafe.ModeRead,
	"write_file": pathsafe.ModeWrite,
	"edit_file":  pathsafe.ModeWrite,
	"hash_edit":  pathsafe.ModeWrite,
}

// PathGrantNeed 是越界路径授权需求。
//
// 对账 TS 的返回结构 `{ mode: 'read' | 'write'; paths: string[] }`。
type PathGrantNeed struct {
	Mode  pathsafe.Mode
	Paths []string
}

// expandHome 展开 `~` 前缀为家目录。
//
// 对账 TS `expandHome`（`platform.ts:512-517`）。**必须在路径校验前做**——
// execute 侧对路径做 trim + expandHome，若此处不归一，`~/Desktop/x.svg`
// 会被词法 resolve 进 cwd 而绕过门禁。
func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return home + p[1:]
		}
	}
	return p
}

// resolvePathForLabel 返回 cwd 基准的解析路径（供调用方生成授权标签）。
//
// 对账 TS 的 `resolvePath`——那是 **Node `path.resolve`**（`tool-pipeline.ts:15`
// 的 `resolve as resolvePath`），**不是** `filepath.Join`+`Clean`。
//
// **为什么必须复刻 Node 语义**（实测差异，见 `nodepath.go`）：
//
//	用例                  filepath.Join(cwd,p)     Node path.resolve(cwd,p)
//	/repo + /etc/passwd   \repo\etc\passwd         D:\etc\passwd  ← 绝对段截断
//	/repo + ../../etc/x   \etc\x                   D:\etc\x       ← 盘符基准
//
// 返回的是**授权标签**（桌面端 `pathGrantHint` 消费）——标签错位会让授权
// 作用到错误路径。故必须与 TS 逐值一致，由 oracle 对账
// （`TestNodeResolveWin32Parity`）。
func resolvePathForLabel(cwd, p string) string {
	return nodeResolve(cwd, p)
}

// OutOfWorkspaceFilePaths 返回文件工具将触及、且当前在工作区外**未被授权**的
// 绝对路径。工作区内或已授权时返回 nil。
//
// 对账 TS `outOfWorkspaceFilePaths`。
//
// **调用方**：审批决策树的 `pathGrantNeed` 分支（Go 侧接线见 loop.go）。
//
// 参数 `grants` 为 nil 时等价于「无任何授权」——所有越界路径都会被报告
// （fail-closed：宁可多问一次）。
func OutOfWorkspaceFilePaths(cwd, toolName string, input map[string]any, grants pathsafe.GrantChecker) *PathGrantNeed {
	opts := &pathsafe.Options{Grants: grants}
	strInput := func(k string) string {
		v, _ := input[k].(string)
		return v
	}

	// ── 外部导出面：destination 写侧优先于 source 读侧 ──
	//
	// 对账 TS：更强的授权面决定整组 mode（返回结构保持单 mode 形态）。
	if toolName == "export_file" || toolName == "create_document" {
		dest := ""
		if s := strInput("destination_path"); s != "" {
			dest = expandHome(strings.TrimSpace(s))
		}
		src := ""
		if toolName == "export_file" {
			if s := strInput("source_path"); s != "" {
				src = expandHome(strings.TrimSpace(s))
			}
		}
		if dest != "" && !pathsafe.Validate(cwd, dest, pathsafe.ModeWrite, opts).OK {
			return &PathGrantNeed{Mode: pathsafe.ModeWrite, Paths: []string{resolvePathForLabel(cwd, dest)}}
		}
		if src != "" && !pathsafe.Validate(cwd, src, pathsafe.ModeRead, opts).OK {
			return &PathGrantNeed{Mode: pathsafe.ModeRead, Paths: []string{resolvePathForLabel(cwd, src)}}
		}
		return nil
	}

	// ── open_path：只读打开（OS opener，不回读内容）──
	if toolName == "open_path" {
		p := ""
		if s := strInput("path"); s != "" {
			p = expandHome(strings.TrimSpace(s))
		}
		if p != "" && !pathsafe.Validate(cwd, p, pathsafe.ModeRead, opts).OK {
			return &PathGrantNeed{Mode: pathsafe.ModeRead, Paths: []string{resolvePathForLabel(cwd, p)}}
		}
		return nil
	}

	mode, ok := fileToolModes[toolName]
	if !ok {
		return nil
	}

	// 候选路径：file_path（单）/ file_paths（数组）/ paths（数组）。
	var candidates []string
	if s, ok := input["file_path"].(string); ok {
		candidates = append(candidates, s)
	}
	for _, key := range []string{"file_paths", "paths"} {
		arr, ok := input[key].([]any)
		if !ok {
			continue
		}
		for _, raw := range arr {
			if s, ok := raw.(string); ok {
				candidates = append(candidates, s)
			}
		}
	}

	var paths []string
	for _, c := range candidates {
		if !pathsafe.Validate(cwd, c, mode, opts).OK {
			paths = append(paths, resolvePathForLabel(cwd, c))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return &PathGrantNeed{Mode: mode, Paths: paths}
}
