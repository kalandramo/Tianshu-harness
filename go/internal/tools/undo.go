package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/recovery"
)

// UndoHistory 是 `undo` 工具依赖的文件历史面。
//
// # 为什么是一层接口（而非直接 import internal/filehistory）
//
// 与 `LspNavigator` 同一模式：`internal/tools` 是工具内核，反向依赖
// 具体子系统会让依赖方向倒置。历史实例在**会话建立后**才存在
// （late-bound，对账 TS 的 `createUndoTool(getFileHistory)` 回调），
// 由装配层注入。
//
// 额外收益：测试无需真文件系统（注入假实现即可覆盖七个分支）。
type UndoHistory interface {
	// LatestSnapshotID 返回最近一次快照的 id。
	LatestSnapshotID() (string, bool)
	// GetDiffStats 计算「回滚到该快照」的变化量（预览用）。
	GetDiffStats(targetMessageID string) (*UndoDiffStats, bool)
	// Rewind 回滚到指定快照，返回被改动的文件（**绝对路径**）。
	Rewind(targetMessageID string) ([]string, error)
}

// UndoDiffStats 是 rewind 预览的统计（与 `filehistory.DiffStats` 同形）。
//
// 同 `LspLocation` 的处理：两包各定义同形类型，装配层做字段拷贝，
// 避免 `tools ↔ filehistory` 的命名类型耦合。
type UndoDiffStats struct {
	FilesChanged []string
	Insertions   int
	Deletions    int
}

type undoTool struct{}

// Undo 创建 `undo` 工具。
//
// 对账 TS `createUndoTool`（`src/tools/undo.ts` 96 行）。
func Undo() Tool { return &undoTool{} }

func (t *undoTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "undo",
		// **逐字对账 TS**（进请求体，前缀缓存字节稳定）
		Description: "撤销最近一次文件改动，将其恢复到之前的备份。恢复前先展示将发生的变化。" +
			"该操作以文件为单位——只回退上一次工具调用中修改过的文件。",
		InputSchema: objSchemaOrdered(
			[]string{"confirm"},
			map[string]any{
				"confirm": boolProp("设为 true 才执行撤销。不带 confirm 时只展示预览。"),
			},
			// **required 为空**（对账 TS：input_schema 无 required 字段）
		),
	}
}

// Execute 实现七个分支（逐条对账 TS 的文案）。
func (t *undoTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	history := resolveUndoHistory(p)
	if history == nil {
		return contract.Result{Content: "文件历史不可用。", IsError: true}, nil
	}

	latestID, ok := history.LatestSnapshotID()
	if !ok {
		return contract.Result{Content: "没有可撤销的文件历史快照。"}, nil
	}

	// 对账 TS：`const confirm = params.input.confirm === true`
	// ——**严格**比较，字符串 "true" / 数字 1 都不算（那些走预览）。
	if !undoConfirmIsTrue(p) {
		return undoPreview(p, history, latestID), nil
	}

	restored, err := history.Rewind(latestID)
	if err != nil {
		return contract.Result{Content: "撤销失败：" + err.Error(), IsError: true}, nil
	}
	if len(restored) == 0 {
		return contract.Result{Content: "没有需要恢复的文件。"}, nil
	}

	// 审计记账（best-effort）——见下方说明。
	auditRestores(p, restored)

	// 展示与归属比较都归一化为**相对路径**（见 undoFileLabels 的说明）
	labels := undoFileLabels(p.Cwd, restored)
	var b strings.Builder
	fmt.Fprintf(&b, "已恢复 %d 个文件：\n", len(labels))
	for i, l := range labels {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  - " + l)
	}
	b.WriteString(undoUnownedNoteConfirm(p, restored))
	return contract.Result{Content: b.String()}, nil
}

// RequiresApproval 恒 true（对账 TS `requiresApproval: () => true`）。
//
// **为什么必须审批**：撤销是**破坏性操作**——它把文件改回旧内容，
// 可能丢掉本轮的有意编辑。
func (t *undoTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 false（对账 TS `isConcurrencySafe: () => false`）。
//
// 多文件回滚不是原子操作，与其他写工具并发会互相踩。
func (t *undoTool) ConcurrencySafe() bool { return false }

// Enabled 恒 true（对账 TS `isEnabled: () => true`）。
//
// 注意与 LSP 工具的差异：这里**不**按 history 可用性门控——工具始终可见，
// 调用时才报「文件历史不可用。」。对账 TS 的同一选择。
func (t *undoTool) Enabled() bool { return true }

// Timeout 用默认（0）。
func (t *undoTool) Timeout(_ *CallParams) time.Duration { return 0 }

// ── 辅助 ─────────────────────────────────────────────────────────

// resolveUndoHistory 取本会话的历史（对账 TS 的 `getFileHistory()`）。
func resolveUndoHistory(p *CallParams) UndoHistory {
	if p == nil || p.FileHistory == nil {
		return nil
	}
	return p.FileHistory()
}

// undoConfirmIsTrue 严格判定 confirm（对账 TS 的 `=== true`）。
func undoConfirmIsTrue(p *CallParams) bool {
	if p == nil || p.Input == nil {
		return false
	}
	v, ok := p.Input["confirm"].(bool)
	return ok && v
}

// undoPreview 生成预览（对账 TS 的预览分支）。
func undoPreview(p *CallParams, history UndoHistory, latestID string) contract.Result {
	stats, ok := history.GetDiffStats(latestID)
	if !ok || stats == nil || len(stats.FilesChanged) == 0 {
		return contract.Result{Content: "最近快照中没有可撤销的变更。"}
	}
	labels := undoFileLabels(p.Cwd, stats.FilesChanged)

	var b strings.Builder
	fmt.Fprintf(&b, "预览：将恢复 %d 个文件：\n", len(labels))
	for i, l := range labels {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  - " + l)
	}
	fmt.Fprintf(&b, "\n+%d/-%d 行", stats.Insertions, stats.Deletions)
	b.WriteString(undoUnownedNotePreview(p, stats.FilesChanged))
	b.WriteString("\n\n传入 confirm: true 以执行。")
	return contract.Result{Content: b.String()}
}

// auditRestores 记审计日志（**best-effort**）。
//
// 对账 TS：
//
//	try { trackFileRestore(params.cwd, file, 'undo tool restore', 0, params.sessionId) }
//	catch { /* audit journal unavailable — restore already applied */ }
//
// **为什么必须吞掉错误**：文件此刻**已经恢复**。审计写失败若冒泡成
// 「撤销失败」，模型会以为没撤成 → 重试 → 把刚恢复的旧内容又盖掉（二次伤害）。
// 这是 TS 注释明写的理由，不是随手忽略。
func auditRestores(p *CallParams, restored []string) {
	if p == nil {
		return
	}
	for _, f := range restored {
		_ = recovery.RecordRecovery(p.Cwd, recovery.RecoveryEntry{
			File:      f,
			Action:    "undo tool restore",
			LinesLost: 0,
		}, p.SessionID)
	}
}

// undoFileLabels 把绝对路径归一化为**相对 cwd 的正斜杠路径**（展示与比较共用）。
//
// # 为什么要归一化（两处都要用同一口径）
//
//  1. **展示**：TS 的文件路径本来就是相对的（`trackEdit(filePath)` 收的是
//     `file_path` 入参形态）；Go 的 History 返回绝对路径，直接展示会给出
//     一长串机器路径。
//  2. **归属比较**：`CallParams.OwnedFiles` 是**相对路径**（实测其他工具的
//     用法：`[]string{"src/a.ts"}`）。用绝对路径去比会**全部判为「不属于
//     当前任务」** → 每份预览都带误导性告警。
//
// 归一化在**展示前**做一次，之后展示与比较都用相对形态——避免两处各自转换
// 而口径漂移（那是 bug 温床：一边修了另一边忘）。
func undoFileLabels(cwd string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, relForUndo(cwd, f))
	}
	return out
}

// relForUndo 把绝对路径转成相对 cwd 的正斜杠路径。
//
// 不在 cwd 内（`..` 开头）时**保持原样**——那种文件本就该让用户看到完整路径。
func relForUndo(cwd, abs string) string {
	if cwd == "" || !filepath.IsAbs(abs) {
		return filepath.ToSlash(abs)
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// undoUnownedFiles 返回**不属于当前任务**的文件（相对路径形态）。
//
// 对账 TS：
//
//	const unowned = params.ownedFiles?.length
//	  ? stats.filesChanged.filter(f => !params.ownedFiles!.includes(f))
//	  : []
//
// ★ **`OwnedFiles` 为空 → 返回空**（不做归属检查）。这点至关重要：
// 空表示「无归属信息」（测试直调、非任务上下文），照常比较会把**所有文件**
// 都判成「不属于本任务」，每份预览都带误导性告警。
func undoUnownedFiles(p *CallParams, files []string) []string {
	if p == nil || len(p.OwnedFiles) == 0 {
		return nil
	}
	// 两侧都归一化为相对路径后比较（OwnedFiles 实测是相对形态；
	// 但调用方也可能填绝对路径——故对 owned 侧同样做归一化兜底）。
	owned := make(map[string]bool, len(p.OwnedFiles))
	for _, o := range p.OwnedFiles {
		owned[filepath.ToSlash(o)] = true
		if p.Cwd != "" && filepath.IsAbs(o) {
			owned[relForUndo(p.Cwd, o)] = true
		}
	}

	var unowned []string
	for _, f := range files {
		rel := relForUndo(p.Cwd, f)
		if !owned[rel] && !owned[filepath.ToSlash(f)] {
			unowned = append(unowned, rel)
		}
	}
	return unowned
}

// undoUnownedNotePreview 预览态的归属告警（对账 TS 原文）。
//
// TS：
//
//	`\n\n⚠️  警告：${n} 个文件不属于当前任务，可能属于并行会话：\n${列表}\n确认前请核实归属。`
//
// **注意「⚠️」后是两个空格**（TS 原文如此）——逐字对账，不要"顺手"改成一个。
func undoUnownedNotePreview(p *CallParams, files []string) string {
	unowned := undoUnownedFiles(p, files)
	if len(unowned) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n⚠️  警告：%d 个文件不属于当前任务，可能属于并行会话：\n", len(unowned))
	for i, u := range unowned {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  - " + u)
	}
	b.WriteString("\n确认前请核实归属。")
	return b.String()
}

// undoUnownedNoteConfirm 确认态的归属告警（对账 TS 原文）。
//
// TS：`\n⚠️  ${n} 个文件不属于本任务：${逗号连接}`
//
// **与预览态措辞不同**（更短、单行、逗号连接）。不要为了"统一"合并两者
// ——它们是逐字对账的对象。
func undoUnownedNoteConfirm(p *CallParams, files []string) string {
	unowned := undoUnownedFiles(p, files)
	if len(unowned) == 0 {
		return ""
	}
	return fmt.Sprintf("\n⚠️  %d 个文件不属于本任务：%s",
		len(unowned), strings.Join(unowned, ", "))
}
