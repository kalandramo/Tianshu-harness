// probe-discipline hook：postTool 检测连续只读工具而无探针。
//
// 对账 TS 的 `createProbeDisciplineHook`
// （src/agent/hooks/probe-discipline-hook.ts）。
//
// ## 为什么需要（TS 文件头原文）
//
//	诊断轮连续 ≥3 个只读工具而零探针 → 提示「30 秒探针能否杀死当前假设」。
//	探针是推理的校验器：推理生成假设，探针验证或杀死它——红灯比继续推演
//	更快逼近根因（symlink 越界 / C→R 折叠均靠探针红灯定位）。
//	冷却：同源 advisory 注入后 COOLDOWN_CALLS 次工具调用内不重复。
//
//	取证补充：探针杀假设，但杀不了编出来的假设。连续只读若全是「层层源码
//	推断」——引用无行号、read 无区间、grep 无上下文——推断链再长也没有骨头。
//	此时先催「找直接观察」，而非直接催探针。
//
// ## 阈值与冷却（TS 注释逐条对账）
//
//	PROBE_THRESHOLD = 5——正常定位 1-2 轮读就够，3 轮以内的深潜仍在取证自然
//	节奏；只有持续 5 轮只读仍无进展才值得提醒（2026-09-07 实机调参：原 3 轮
//	在诊断长链中过于频繁，噪音大于信号）。
//	COOLDOWN_CALLS = 12。
//
// ## 锚点判定的真相（Go 侧探针实测，2026-09-23）
//
// TS 的锚点判定有**两个**来源：
//
//	ANCHORED_READONLY_TOOLS.has(tool.name) || ANCHOR_HINT.test(argString)
//
// 其中 `argString = JSON.stringify(input)`。**实测（npx tsx 直接调真实 hook）**：
//
//	ANCHOR_HINT = /(?:offset|limit|focus|context_lines|start|end|line)\s*[=:]/
//
// 对 JSON 形态**恒不匹配**——`"context_lines":3` 的 `context_lines` 与 `:`
// 之间隔着闭引号，`\s*[=:]` 吃不到。实测 `{"pattern":"x","context_lines":3}`
// 与 `{"file_path":"a.ts","offset":10}` 全部 match: false；只有字符串形态
// `context_lines=3` 才 true。
//
// **故 TS 的 ANCHOR_HINT 是死代码**——锚点判定实际只靠三个工具名
// （read_section / lsp_goto_definition / lsp_find_references）。TS 的测试之所以
// 绿，是因为它的锚点用例首行恰好是 `read_section`，`context_lines` 那行是装饰。
//
// **Go 侧的决定**：不移植失效的正则（移植死代码 = 搬运缺陷），只保留工具名
// 判定。**行为与 TS 完全等价**（TS 的实际行为就是工具名判定）。这是显式偏差，
// 记录于此与 HANDOFF。若 TS 侧将来修正该正则，Go 侧需同步——届时此处即为锚点。
//
// ## key 为何用单调计数器（与 TS 的 Date.now() 同构）
//
// TS 用 `probe-discipline-${Date.now()}`——**故意让每次注入的 key 不同**。
// 若 Go 用固定 key，新条目会与 `alive` 池里 TTL=2 的旧条目撞键，去重时
// （同优先级保留先出现者）**新内容被静默丢弃**。计数器比时间戳更优：
// 确定性、可测。
package agent

import (
	"context"
	"strconv"
)

// probeReadonlyTools 是只读取证工具判定集——探针分级的「只读」判定。
// 写/跑测试/执行类**不算**。
//
// **逐字对账** TS 的 `READONLY_TOOLS`（15 项）。
//
// **前瞻项**（Go 侧尚无对应工具，保留以对齐 TS）：ast_grep / repo_graph /
// semantic_search / recall / memory / lsp_goto_definition /
// lsp_find_references / web_fetch / web_search。判定集里的未知名字
// **行为等价于不存在**（永不匹配）——保留无害且将来移植时自动生效。
//
// **Go 侧已有但 TS 未列入的只读工具**（inspect_project / file_info /
// related_tests / diff）**不擅自加入**——TS 的列表是权威 oracle，
// 差异由 TS 侧决定，不由移植方发明。
var probeReadonlyTools = map[string]bool{
	"read_file":           true,
	"read_section":        true,
	"grep":                true,
	"glob":                true,
	"ast_grep":            true,
	"repo_map":            true,
	"repo_graph":          true,
	"semantic_search":     true,
	"recall":              true,
	"memory":              true,
	"lsp_goto_definition": true,
	"lsp_find_references": true,
	"web_fetch":           true,
	"web_search":          true,
	"git":                 true,
}

// probeAnchoredReadonlyTools 是「带观察锚点的只读」工具集——取证优先级高于裸推断。
//
// 对账 TS 的 `ANCHORED_READONLY_TOOLS`。**这是 TS 锚点判定的全部有效来源**
// （TS 的 ANCHOR_HINT 正则是死代码，见文件头「锚点判定的真相」）。
var probeAnchoredReadonlyTools = map[string]bool{
	"read_section":        true,
	"lsp_goto_definition": true,
	"lsp_find_references": true,
}

// probeDisciplinePriority 对账 TS 的 `priority: 0.5`。
const probeDisciplinePriority = 0.5

const (
	// probeThreshold 是连续只读触发阈值。
	//
	// 对账 TS 的 `PROBE_THRESHOLD = 5`。
	probeThreshold = 5
	// probeCooldownCalls 是注入冷却（工具调用次数）。
	//
	// 对账 TS 的 `COOLDOWN_CALLS = 12`。
	probeCooldownCalls = 12
)

// 两条注入文案——**逐字对账** TS 的模板字符串（中间插入 readStreak）。
//
// **注意「探针」一词在取证文案里也出现**（末句「探针杀假设，但杀不了编出来的
// 假设」）——故不能用 `strings.Contains(content, "探针")` 区分两条分支。
// 区分锚点用「取证」/「锚点」（仅取证文案含）。
const (
	probeEvidencePrefix = "【太一·取证】已连续 "
	probeEvidenceSuffix = " 轮只读，但零个带观察锚点（行号区间 / context_lines / 时间戳对齐 / 磁盘证据）。" +
		"推断链再长也没有骨头——先找一行可复核的观察：相邻行、L→L 的 gap、落盘文件，再让因果从里面长出来。" +
		"探针杀假设，但杀不了编出来的假设。"
	probeNudgePrefix = "【太一·探针】已连续 "
	probeNudgeSuffix = " 轮只读取证。30 秒探针（最小复现 / 一行 tsx -e）能杀死当前最可能的假设吗？" +
		"红灯比继续推演更快逼近根因；假设不可测（缺 key/缺环境）才降级纯推理并标注未验证。"
)

// NewProbeDisciplineHook 构造 probe-discipline hook。
//
// **session-scoped 状态**（对账 TS 的闭包变量）：readStreak /
// callsSinceLastInject / anchoredReads 跨轮累积。故构造一次、跨轮复用。
//
// **投递通道 = system-reminder**（对账 TS 的 `channel: 'system-reminder'`）
// ——Go 侧该通道的语义是「绕过 CVM 注入预算」（见 advisory.go 的
// AdvisoryChannel 说明），对账 TS 的「细断点单独注入」意图。
func NewProbeDisciplineHook(bus AdvisorySink) RuntimeHook {
	readStreak := 0
	// **哨兵 = COOLDOWN_CALLS + 1**（对账 TS）——保证首次触发不被冷却拦住。
	// Go 零值是 0（对冷却判定而言是**合法值**），必须显式初始化。
	callsSinceLastInject := probeCooldownCalls + 1
	// 取证锚点计数：连续只读里带行号/区间/上下文的次数。全裸读（零锚点）→
	// 先催取证；有锚点仍推不动 → 才轮到探针。
	anchoredReads := 0
	seq := 0

	return RuntimeHook{
		Name:  "probe-discipline",
		Phase: PhasePostTool,
		Run: func(_ context.Context, _ *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if tool == nil {
				return nil
			}

			callsSinceLastInject++

			if probeReadonlyTools[tool.Name] {
				readStreak++
				if probeAnchoredReadonlyTools[tool.Name] {
					anchoredReads++
				}
			} else {
				// 写/验证/执行类动作打破只读串——不是取证停滞。
				readStreak = 0
				anchoredReads = 0
			}

			if readStreak < probeThreshold || callsSinceLastInject < probeCooldownCalls {
				return nil
			}

			// **先消费触发，再检查出口**（对账 TS：重置在 submit 之前；
			// 与既有 Go hook 的「先记账后判 bus」约定一致）。
			callsSinceLastInject = 0
			if bus == nil {
				return nil
			}
			seq++

			content := ""
			if anchoredReads == 0 {
				// 连续只读但零观察锚点——推断叠推断，没有骨头。
				// 先补取证，探针还排不上。
				content = probeEvidencePrefix + strconv.Itoa(readStreak) + probeEvidenceSuffix
			} else {
				content = probeNudgePrefix + strconv.Itoa(readStreak) + probeNudgeSuffix
			}

			bus.Submit(AdvisoryEntry{
				Key:      "probe-discipline-" + strconv.Itoa(seq),
				Priority: probeDisciplinePriority,
				Category: CategoryDiscipline,
				Tier:     TierOperational,
				Content:  content,
				TTL:      2,
				Channel:  ChannelSystemReminder,
			})
			return nil
		},
	}
}
