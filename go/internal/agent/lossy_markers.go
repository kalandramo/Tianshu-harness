// 有损观测标记的**单一真源**（lossy markers）。
//
// 对账 TS 的 `src/agent/lossy-markers.ts`（W1-A4）。
//
// ## 消费者
//
//   - `lossy-observation` hook（预防性 advisory——本文件同目录的
//     `lossy_observation.go`）
//   - 将来移植 `negative-fact-detector` 时复用（TS 侧两者共用此表）
//
// ## 反假阳性规则（TS 原文）
//
//	Anti-false-positive rule: only structural markers match — natural-language
//	words like "truncated" in ordinary command output must NOT trigger.
//
// 故所有模式都锚定**结构化标记**（方括号/尖括号包裹的头部或脚注），
// 不是裸词。
//
// ## scope 收窄（明示，重要）
//
// TS 的 `LOSSY_CONTENT_MARKERS` 有 **15 条**；Go 侧**只移植真实产生的那些**。
//
// 核实——以下 TS 标记在 Go 侧**零命中**：
//
//	[storm-collapsed:     storm 折叠（src/compact/storm*.ts）未移植
//	[tiered-summary:      分层摘要器未移植
//	[budget-evicted:      per-message-budget 未移植
//	[budget-summarized:   同上
//	[truncated: N tokens  同上
//	lines omitted (context pressure / per-call budget  同上（Go 有 turn read budget 变体）
//	<stale-compacted      过期轮次压缩未移植
//	[stdout truncated:    Go 的 bash 工具**不产生**——它把 stdout/stderr 两条流
//	[stderr truncated:    统一成一条 `[output truncated: ...]`（internal/tools/bash.go:226-236）。
//	                      per-stream 标记只有 TS 产生（src/tools/bash.ts:637,640）。
//
// **移植未产生的标记会让 hook 永不触发**（死模式），违反「锚定真实标记」
// 的纪律。这些标记随各自子系统移植时一并加入——届时本表的 oracle 测试
// （`lossy_markers_test.go`）会强制补上。
package agent

import "regexp"

// lossyContentMarkers 是结构化有损标记表。
//
// **每条都锚定 Go 侧真实产生点**（注释标注 file:line）。
var lossyContentMarkers = []*regexp.Regexp{
	// src/compact/context_collapse.go:153,155,191,249,307,317,425,448
	// —— 语义折叠摘要头，形如 `[collapsed grep: 14 matches in ...]`
	regexp.MustCompile(`^\[collapsed `),
	// src/tools/modeloutput.go:122,147 与 src/tools/bash.go:232 —— 统一截断脚注
	// 形如 `[output truncated: last 100 of 5000 lines shown — ...]`
	// 与 `[output truncated: 单流上限 N 字节。...]`
	regexp.MustCompile(`\[output truncated:`),
	// src/tools/read_file.go —— "── PARTIAL view of <file> (N lines, M chars) ──"
	regexp.MustCompile(`PARTIAL view of `),
	// src/compact/micro.go:35 —— micro-compact 截断桩
	regexp.MustCompile(`<microcompacted `),
	// **注意（第四十三刀核实）**：TS 侧还有三条 `lines omitted (...)` 前缀模式
	// （turn read budget / context pressure / per-call budget）。Go 侧**不产生**
	// 这种格式——它用的是 `[output truncated: ... — N lines omitted]`（已被
	// 上方的模式覆盖）。故**不移植**那三条。
	//
	// 首版我曾加了一条宽前缀 `lines omitted \(`，那是**臆造的标记**——
	// 正是本文件顶部警告的「移植未产生的标记会让 hook 永不触发」。
	// 由 oracle 对账抓到（TS 侧该正例判 false）。
}

// IsLossyObservation 报告内容是否带任何结构化有损标记。
//
// 对账 TS 的 `isLossyObservation`。
func IsLossyObservation(content string) bool {
	for _, m := range lossyContentMarkers {
		if m.MatchString(content) {
			return true
		}
	}
	return false
}
