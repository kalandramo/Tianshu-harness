package lsp

import (
	"fmt"
	"strings"
)

// 本文件对账 TS `src/lsp/client.ts` 的 W4 诊断回流部分（`client.ts:276-384`）。
//
// # 为什么这段逻辑值得**逐条对账**而不是"差不多就行"
//
// 它是「编辑后让模型立刻看到自己刚引入的错误」这条反馈回路的**唯一**实现。
// 偏差有两种方向，代价不对称：
//
//   - **多给**（该丢的没丢）：浪费上下文，可接受
//   - **少给**（该留的丢了）：模型看不见自己刚弄坏的东西 → 静默交付缺陷
//
// 故本文件的全部降级路径都朝「多给」一侧倒：算不出区域就整文件给、
// 区域外 error 也要折叠出一个行号 nudge（**绝不静默丢弃 error**）。
// 唯一的"丢弃"是区域外 **warning**——那是有意的降噪（TS 注释明写）。

const (
	// DiagContextLines 是判定「区域内外」时在每个变更区间外扩的行数。
	//
	// 对账 TS `DIAGNOSTIC_CONTEXT_LINES = 3`（`client.ts:280`）。
	// 用途：`changeLine` 本身可能不在诊断报告的行上（诊断常指向邻行），
	// 外扩 3 行足以覆盖这类偏移。
	DiagContextLines = 3

	// ModelInRegionCap 是给模型的「区域内」诊断条数上限。
	//
	// 对账 TS `MODEL_INREGION_CAP = 10`（`client.ts:282`）。
	// **静默截断**（与 TS 一致）——超出的不提示，因为区域内 10 条以上
	// 通常意味着整个文件都坏了，那时后面的诊断也帮不上定位。
	ModelInRegionCap = 10

	// ModelOutRegionLineCap 是折叠 nudge 里列出的行号条数上限。
	//
	// 对账 TS `MODEL_OUTREGION_LINE_CAP = 5`（`client.ts:284`）。
	// 超出时追加 "…"。
	ModelOutRegionLineCap = 5

	// UIDiagnosticCap 是给 UI 的诊断条数上限。
	//
	// 对账 TS `UI_DIAGNOSTIC_CAP = 20`（`client.ts:286`）。
	// 比模型侧宽——人看工具卡时不会因为多几条而迷失，模型会。
	UIDiagnosticCap = 20
)

// LineRange 是**1-based 的行区间**（闭区间，含两端）。
//
// ⚠️ **不要与 `Range` 混淆**：`Range` 是 LSP 协议的 0-based 行列区间
// （`Position{Line, Character}`）。本类型是「编辑触及了哪几行」的业务区间
// （TS `tools/types.ts:447` 的 `changedRanges?: Array<{start, end}>`）。
//
// 只到「行」粒度、不含列、不含字符——因为它唯一的用途是
// `FilterDiagnosticsForEdit` 的 in-region 判定。
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// DiagFilterResult 是 `FilterDiagnosticsForEdit` 的双通道输出。
//
// 对账 TS `EditDiagnosticsResult`（`client.ts:289-294`）。
type DiagFilterResult struct {
	// ModelText 是给模型的收敛后文本（可能为空——空则调用方不加段落）。
	ModelText string
	// UIText 是给 UI 的**全量**列表（区域无关）。
	UIText string
}

// formatDiagnosticLine 格式化单条诊断。
//
// 对账 TS `formatDiagnosticLine`（`client.ts:296-299`）：
//
//	`${kind} L${line + 1}: ${message}`
//
// severity 1 → ERROR，其余（本例只会是 2，因为 3/4 已在入口丢弃）→ WARNING。
func formatDiagnosticLine(d LspDiagnostic) string {
	kind := "WARNING"
	if d.Severity == 1 {
		kind = "ERROR"
	}
	// ★ 行号 +1：LSP 是 0-based，给人看的消息是 1-based
	return fmt.Sprintf("%s L%d: %s", kind, d.Range.Start.Line+1, d.Message)
}

// FilterDiagnosticsForEdit 按编辑触及的行区间把整文件诊断分成
// 「模型该看的」与「人该看的」两份。
//
// 对账 TS `filterDiagnosticsForEdit`（`client.ts:318-384`）。
//
// # 语义（三档）
//
//  1. **区域内**（行 ∈ 任一 range ± `context`）：error + warning **完整消息**
//  2. **区域外 error**：折叠成**一行** nudge，带行号列表——
//     「+N error(s) elsewhere in file (L1, L2, …) — run typecheck before delivery」
//  3. **区域外 warning**：**丢弃**
//
// `ranges` 为空/nil 时**无法定位 → 整文件给模型**（安全降级，不是错误分支）。
//
// uiText 始终是**全量**（errors + warnings，与区域无关，上限 `UIDiagnosticCap`）。
//
// # 为什么区域外 error 不直接丢
//
// 若静默丢掉，模型会以为自己改干净了（工具结果里没有任何错误提示），
// 而实际上文件里还有它引入/遗留的 error。折叠成一行既省上下文，
// 又把「还有 N 个错误在别处」这个事实**明确交出去**。
func FilterDiagnosticsForEdit(
	diagnostics []LspDiagnostic,
	ranges []LineRange,
	context int,
) DiagFilterResult {
	// ── 入口过滤：只留 errors(1) + warnings(2) ──
	//
	// 对账 TS `client.ts:326`：`diagnostics.filter(d => d.severity <= 2)`
	//
	// ⚠️ **必须写成 `<= 2` 而非 `>= 1 && <= 2`**（审查发现，已订正）。
	// 后者对 `severity == 0` 会过滤，而 TS 的 `0 <= 2` 会保留——
	// 方向恰好相反，且与本文件自述的「降级路径朝多给一侧倒」矛盾。
	//
	// # 零值语义（第一百零四刀**实测**，非推测）
	//
	// LSP 的 `severity` 是可选字段。实测两个事实：
	//
	//  1. **gopls 正常推送时总是带 `severity`**（实测其原始 JSON：
	//     `"severity":1`，值类型为数字）。故「缺失」在 gopls 下罕见——
	//     这一点此前是推测，现已验证。
	//  2. 若某 server 真的省略该字段，Go 反序列化得 `severity == 0`
	//     （实测：JSON 无 severity → 结构体字段为 0）→ 本函数**保留**它
	//     → `formatDiagnosticLine` 按「非 1 即 WARNING」标为 `WARNING`。
	//
	// 即：偏离方向是**多给**且**标签可能不准**（未知严重级被显示成 WARNING），
	// 但**不隐藏**任何诊断。符合本文件的降级原则，接受。
	// 代价已明确：一条 severity 未知的诊断会被误标为 WARNING——比静默丢弃好。
	relevant := make([]LspDiagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		if d.Severity <= 2 {
			relevant = append(relevant, d)
		}
	}
	if len(relevant) == 0 {
		return DiagFilterResult{}
	}

	// ── UI 侧：全量列表（区域无关）──
	uiLines := make([]string, 0, min(len(relevant), UIDiagnosticCap))
	for i, d := range relevant {
		if i >= UIDiagnosticCap {
			break
		}
		uiLines = append(uiLines, formatDiagnosticLine(d))
	}
	uiText := strings.Join(uiLines, "\n")

	// ── 无区域信息 → 整文件给模型（安全降级）──
	// 对账 TS `client.ts:333-337`。注意 `!ranges || ranges.length === 0`
	// —— Go 里 nil 与空切片都走这里，语义一致。
	if len(ranges) == 0 {
		modelLines := make([]string, 0, min(len(relevant), ModelInRegionCap))
		for i, d := range relevant {
			if i >= ModelInRegionCap {
				break
			}
			modelLines = append(modelLines, formatDiagnosticLine(d))
		}
		return DiagFilterResult{ModelText: strings.Join(modelLines, "\n"), UIText: uiText}
	}

	// ── 有区域信息 → 分桶 ──
	// 对账 TS `client.ts:339-347`。
	inRegionFn := func(line1 int) bool {
		for _, r := range ranges {
			if line1 >= r.Start-context && line1 <= r.End+context {
				return true
			}
		}
		return false
	}

	var inside []LspDiagnostic
	var outsideErrors []LspDiagnostic
	for _, d := range relevant {
		// ★ 与 `LineRange` 同一坐标系（都是 1-based）
		line1 := d.Range.Start.Line + 1
		switch {
		case inRegionFn(line1):
			inside = append(inside, d)
		case d.Severity == 1:
			outsideErrors = append(outsideErrors, d)
			// 其余情形 = 区域外 warning → 有意丢弃（TS 同）
		}
	}

	// 对账 TS `client.ts:349-362`：两部分用 "\n" 拼接，顺序固定
	//（先区域内、后折叠 nudge）。
	parts := make([]string, 0, 2)
	if len(inside) > 0 {
		n := min(len(inside), ModelInRegionCap)
		lines := make([]string, 0, n)
		for i := 0; i < n; i++ {
			lines = append(lines, formatDiagnosticLine(inside[i]))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(outsideErrors) > 0 {
		n := min(len(outsideErrors), ModelOutRegionLineCap)
		nums := make([]string, 0, n)
		for i := 0; i < n; i++ {
			nums = append(nums, fmt.Sprintf("L%d", outsideErrors[i].Range.Start.Line+1))
		}
		more := ""
		if len(outsideErrors) > ModelOutRegionLineCap {
			more = ", …"
		}
		parts = append(parts, fmt.Sprintf(
			"+%d error(s) elsewhere in file (%s%s) — run typecheck before delivery",
			len(outsideErrors), strings.Join(nums, ", "), more,
		))
	}

	return DiagFilterResult{ModelText: strings.Join(parts, "\n"), UIText: uiText}
}
