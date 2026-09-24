// dynamic_appendix.go —— 动态 appendix 的装配点。
//
// 对账 TS `src/prompt/volatile.ts` 的 `buildDynamicAppendixParts`(601) /
// `buildDynamicAppendix`(866) —— **最小子集**：只接当前 Go 侧**已有状态载体**
// 的块，其余按状态就位情况增量接入。
//
// **为什么需要这个装配点**：第五十五至五十七刀移植了 appendix 家族的一批
// 纯函数（`RenderPlanModeBlock` / `RenderAskModeBlock` / `RenderTersenessNudge`
// / `RenderPermissionNote`），但它们**零生产消费者**——用户看不到任何变化。
// 本文件是它们的**归宿**。
//
// **★ 架构约束（关键，不可违反）**：appendix 必须注入 **user message 尾部**，
// **绝不能**进 system prompt。
//
// 理由（见 `prompt/full.go:90-105` 的架构说明）：
//   - TS 侧 frozen 块是 **trailer-merge 到 user message**（`engine.ts:659`），
//     system prompt **完全冻结**、历史消息尾部也可增量缓存
//   - Go 侧 frozen 块拼在 **system prompt 内**（既有架构选择）——它**同样
//     稳定可缓存**，但 volatile 变化会**打断整个前缀**
//
// 故：appendix 的输入（`approvalMode` / `planModeState` / `askModeState` /
// `tersenessEnabled`）会**会话中途翻转**，若进 system prompt，用户切换模式
// 就会让整个前缀缓存失效（DeepSeek 侧全价重算）。TS 源码反复强调
// 「Cache-safe: dynamic appendix only」正是此意。
//
// **本文件当前接的块**：仅 `RenderPermissionNote`（只依赖 `ApprovalMode`，
// 零新增状态）。其余块的接入见 HANDOFF 的遗留段。
package agent

import (
	"strings"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// AppendixContext 是动态 appendix 的输入。
//
// **有意保持最小**：只含当前 Go 侧**已有载体**的状态。字段随接入进度增长。
//
// 对账 TS `VolatileContext` 的 appendix 相关子集——TS 那个结构有 20+ 字段，
// 但绝大多数在 Go 侧无载体（`toolHistory` / `planModeState` /
// `worktreeReality` / `playbookLessons` / `taskProgress` / `decisions` …）。
// **不虚构字段**：没有载体的状态不设字段，避免产出永远为空的死代码。
type AppendixContext struct {
	// ApprovalMode 是审批档位（对账 TS 的 `config.approval`）。
	//
	// 消费方：`RenderPermissionNote` —— 仅 `dangerously-skip-permissions`
	// 时产出 `<permission-note>`，其余档返回空串（保证那些 turn 字节稳定）。
	ApprovalMode string
}

// BuildDynamicAppendix 装配动态 appendix（user message 尾部的增量块）。
//
// 对账 TS `buildDynamicAppendix`(866) —— 本函数是其**最小子集**：
// 按固定顺序拼接各子块，跳过空块。
//
// **返回空串的语义**：无任何子块产出时返回 `""`——调用方据此**不追加**
// 任何内容，保持请求体字节与未接线时一致（这是「不引入缓存差异」的要求，
// 与 TS 的 `buildLatestTurnVolatileBlock` 里 `if (!appendix) return frozen`
// 同源）。
//
// **块间分隔符**：TS 用 `'\n\n'`（对账 `buildDynamicAppendix` 的 join）。
// 首个块不加前导分隔符。
func BuildDynamicAppendix(ctx AppendixContext) string {
	var parts []string

	// ── <permission-note> ──
	// 对账 TS `volatile.ts:834` 附近的 `renderPermissionNote(approvalMode)`。
	//
	// **为什么先接它**：它是 appendix 家族里**唯一零新增状态**的块——
	// 只依赖 `ApprovalMode`（Go 侧 `Config.ApprovalMode` 早就有）。
	// 其余块（plan/ask/terseness）都需要 Go 侧不存在的状态载体。
	if note := prompt.RenderPermissionNote(ctx.ApprovalMode); note != "" {
		parts = append(parts, note)
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// appendDynamicAppendix 把 appendix 追加到 user message 内容尾部。
//
// **调用点**：`Loop.Run` 里构造 user 消息时（`loop.go:502` 附近）。
//
// **为什么在 user message 而非 system**：见本文件头部的架构约束说明。
//
// **空 appendix 时原样返回**：保证未配置/非 skip 档的请求体与接线前
// **逐字节相同**——不引入任何缓存差异。
func (l *Loop) appendDynamicAppendix(content string) string {
	appendix := BuildDynamicAppendix(AppendixContext{
		ApprovalMode: l.cfg.ApprovalMode,
	})
	if appendix == "" {
		return content
	}
	return content + "\n\n" + appendix
}
