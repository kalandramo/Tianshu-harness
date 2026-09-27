package tools

// 本文件是 W4（LSP 诊断回流）在**工具层**的判定辅助。
//
// # 边界：本文件只放「不依赖 LSP 子系统」的纯判定
//
// 诊断过滤算法（`FilterDiagnosticsForEdit`）与诊断类型定义在
// `internal/lsp`——那里有 13 条对账测试，且 `agent` 层可直接调用
// （无环：`lsp` 不依赖 `agent`/`tools`）。**故本包不重声明诊断类型**——
// 重声明会造出第二套同形类型，装配层得写字段拷贝，且一旦某侧演化就静默漂移。

// WriteToolNames 是「会改磁盘上的文件」的工具名集合。
//
// # 用途：LSP 的 changeFile 通知（让 server 的诊断基于最新内容）
//
// 对账 TS `tool-pipeline.ts:1579` 的 `WRITE_TOOL_NAMES`——通知必须在
// **取诊断之前**发生（否则 server 看到的是编辑前的文件，诊断滞后一轮）。
//
// ⚠️ **与 `ShouldRunDiagnostics` 的范围不同**：本集合含 5 个写工具，
// 但诊断触发只认 `write_file`/`edit_file`。这不是笔误——changeFile 是全量
// 通知（`apply_patch` 也要通知，让 server 清理已删文件的过期诊断），
// 而诊断只对「单 file_path 参数」的工具跑。
func WriteToolNames() map[string]bool {
	return map[string]bool{
		"write_file":  true,
		"edit_file":   true,
		"apply_patch": true,
		"hash_edit":   true,
		"apply_edit":  true,
	}
}

// ShouldRunDiagnostics 报告某次工具调用是否该触发编辑后诊断。
//
// 对账 TS `shouldRunDiagnostics`（`client.ts:265-277`）逐条：
//
//	if (toolName !== 'write_file' && toolName !== 'edit_file') return false
//	if (!filePath) return false
//	return hasServerForFile(filePath)
//
// `hasServer` 由调用方传入 `lsp.HasServerForFile(filePath)` 的结果——
// 这样本函数保持无依赖，可在本包的测试里独立验证。
//
// # 为什么只认 write_file / edit_file
//
// 它们有**单个 `file_path` 参数**，诊断目标无歧义。`apply_patch` 是多文件
// 补丁、`hash_edit` 走哈希锚点（无显式路径参数），逐个取诊断的收益不抵
// 复杂度——TS 侧也是这么划的线。这**不是**「忘了补」，是有意收窄。
func ShouldRunDiagnostics(toolName, filePath string, hasServer bool) bool {
	if toolName != "write_file" && toolName != "edit_file" {
		return false
	}
	return filePath != "" && hasServer
}
