package agent

import (
	"path/filepath"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// writeToolNamesForTrack 是「执行前需进文件历史」的写工具集合。
//
// 对账 TS `WRITE_TOOL_NAMES`（`tool-pipeline.ts:1404` 的记账门）：
// write_file / edit_file / hash_edit / ast_edit / apply_patch。
// Go 侧无 ast_edit。
var writeToolNamesForTrack = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"hash_edit":   true,
	"apply_patch": true,
}

// trackEditPaths 从**工具的入参**解析出它将要改写的文件（相对或绝对路径）。
//
// # 为什么从入参解析（而非在工具内部事后挂钩）
//
// 对账 TS `extractWriteFilePaths(tu.name, tu.input)`——TS 在工具**执行前**
// 用它记账。从入参解析是能在执行前拿到路径的**唯一**途径：
// 工具自己的返回里没有「它改了哪些文件」的结构化信息。
//
// 各工具的路径形态（逐条对账 TS）：
//
//	write_file / edit_file / hash_edit → `file_path`
//	apply_patch                        → diff 的 `+++ ` 头（入参无 path 字段）
//
// **apply_patch 的特殊性**（TS 注释逐字）：目标文件只在 diff 的 `+++ ` 头里，
// 「曾按 `input.path ?? input.file` 取，对真实 schema 恒为空，文件级追踪对
// apply_patch **静默失效**」。Go 侧复用 `prompt.ExtractPatchTargetPaths`。
//
// **ast_edit 的 dryRun 边界**（TS 同款）：dryRun 时不记账（不写盘）。
// Go 侧无 ast_edit，但保留该判据的位置说明以便将来移植。
func trackEditPaths(toolName string, input map[string]any) []string {
	if !writeToolNamesForTrack[toolName] || input == nil {
		return nil
	}
	if toolName == "apply_patch" {
		if diff, ok := input["diff"].(string); ok {
			return extractPatchPaths(diff)
		}
		return nil
	}
	if fp, ok := input["file_path"].(string); ok && fp != "" {
		return []string{fp}
	}
	return nil
}

// extractPatchPaths 从补丁 diff 里解析目标文件。
//
// 包装 `prompt.ExtractPatchTargetPaths`（单向依赖：agent → prompt）。
func extractPatchPaths(diff string) []string {
	return prompt.ExtractPatchTargetPaths(diff)
}

// trackEditsBeforeExecution 在工具执行**前**把将改写的文件登进文件历史。
//
// # ★★ 时序是本函数的全部意义
//
// 对账 TS（`src/agent/tool-pipeline.ts:1404` 注释逐字）：
//
//	// E4 记账收口：五件写工具（WRITE_TOOL_NAMES）的编辑都要**在执行前**进
//	// file-history（/undo 与边界回溯的记账源头）。
//
// # 放错时机的后果（第一百零二刀 W3 的实际缺陷，已修）
//
// W3 把记账接到写盘**成功之后**，且注释断言「对账 TS 的 trackEdit 语义——
// 它记录的是编辑后的内容」。**那个断言与 TS 源码相反**。倒置时的后果链：
//
//	备份 = 编辑后内容
//	→ `GetDiffStats` 的 `oldContent == newContent` 恒真 → FilesChanged 恒空
//	→ undo 预览恒返回「最近快照中没有可撤销的变更。」
//	→ `Rewind` 把当前内容原样写回，却仍计入 changed
//	→ 报「已恢复 N 个文件」但文件**零变化**（谎报成功）
//
// 即整个「撤销安全网」**静默失效**——最坏的一类缺陷：不报错、还自称成功。
//
// **best-effort**：历史层是附加能力，其失败不该阻断工具执行
// （对账 TS：记账在 pipeline 里，不参与工具成败判定）。
func (l *Loop) trackEditsBeforeExecution(toolName string, input map[string]any, toolUseID string) {
	if l.FileHistory == nil {
		return
	}
	paths := trackEditPaths(toolName, input)
	if len(paths) == 0 {
		return
	}
	// 空 id（测试/少数据径）→ 哨兵：退化为一轮一快照（历史仍可用，粒度粗）。
	id := toolUseID
	if id == "" {
		id = "write"
	}
	for _, rel := range paths {
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(l.cfg.Cwd, rel)
		}
		_ = l.FileHistory.TrackEdit(abs, id)
	}
}
