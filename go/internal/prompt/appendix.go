// appendix.go —— 动态 appendix 的纯函数子集。
//
// 对账 TS `src/prompt/volatile.ts`：
//   - `renderPlanMethodologyAdvisory`(47)
//   - `renderPermissionNote`(88)
//   - `renderPlanExecutingBlock`(167)
//   - `appendixBlockName`(584)
//
// **为什么是这四个**：它们是动态 appendix 家族中**不依赖会话状态**的部分
// （对比 `buildDynamicAppendixParts` 需要 toolHistory / taskProgress 等
// per-turn 字段）。与 `salience.go`（第五十四刀）同层，可独立闭合。
//
// **缓存命中率相关性**：四个函数都只产出 appendix 片段。TS 注释反复强调
// 「Cache-safe: dynamic appendix only」——即这些块的输入（approvalMode /
// planModeState）会在会话中途翻转，故**绝不能进 frozen 前缀**，否则打断
// 前缀缓存。Go 侧移植时必须保持同一分层。
package prompt

import (
	"strings"
	"unicode/utf16"
)

// PlanMethodology 对账 TS `PlanMethodology`（`context/task-contract.ts:456`）：
//
//	export type PlanMethodology = 'lightweight' | 'full'
type PlanMethodology string

const (
	MethodologyLightweight PlanMethodology = "lightweight"
	MethodologyFull        PlanMethodology = "full"
)

// methodologyAdvisoryTemplates 对账 TS `METHODOLOGY_ADVISORY_TEMPLATES`
// （volatile.ts:38-45）—— 键为 PlanMethodology，值为完整 `<plan-methodology>` 块。
//
// **来源**：从 oracle（真实 TS 实现输出）逐字节对账，非手抄。
// 模板长达数百字符，手抄必然逐字出错。
var methodologyAdvisoryTemplates = map[PlanMethodology]string{
	MethodologyLightweight: `<plan-methodology route="lightweight">推荐使用轻量版计划模板（5阶段），路径: docs/superpowers/plans/2026-06-14-plan-methodology-lightweight.md。本任务 scope 内聚，单模块边界内变更，轻量版足以覆盖。至少画一张架构或数据流图（Mermaid），哪怕只画核心 3-5 个节点。开工前先用 todo 列出有序步骤（即为执行计划基线）。</plan-methodology>`,
	MethodologyFull:        `<plan-methodology route="full">推荐使用基础计划模板（writing-plans 已内置为原生 <plan-mode> 流程），路径: docs/superpowers/plans/2026-06-28-plan-methodology-base.md。这是所有计划的默认基础：零上下文假设、任务粒度 2-5 分钟、禁止占位符、TDD、探针先行、瑶光反证、频繁提交。强制要求：① 至少一张 Mermaid 图（架构/数据流/状态图）；② 每个任务 RED→GREEN；③ 复杂实现前先打 30 秒探针；④ 用真实输入复现原问题再验修复，不取信声称取 exit code；⑤ 计划含「瑶光反证」章节（submit 门禁）——设计定稿后回读关键断言到 file:line、bugfix 先跑 run_tests 拿 RED 复现、跑不了的派 adversarial_verifier，复现不了的降级为待验证假设；⑥ 大计划（checkbox 任务 >8 或引用文件 >15）必须显式分波——` + "`### Wave N`" + ` 标题 + 每波验证命令（submit 门禁）。如果任务涉及安全/权限/沙箱/多 enforcement gate，在基础模板之上追加安全附录（安全不变量、触发路径清单、双门对齐数据流图）。开工前先用 todo 列出有序步骤（即为执行计划基线）。</plan-methodology>`,
}

// planModeMethodologyAdvisory 对账 TS `PLAN_MODE_METHODOLOGY_ADVISORY`
// （volatile.ts:46）—— Plan Mode 专用：设计文档口径，不注入可执行
// TDD/bash「执行计划基线」。
const planModeMethodologyAdvisory = `<plan-methodology route="full" mode="design-doc">Plan Mode 产出完整设计文档（写入活动计划文件），不是逐步 bash/commit 菜谱。章节要求：① 需求提炼（用户原话提炼目标与非目标，H1 之后，submit 门禁）；② 问题与根因；③ 至少一张 Mermaid 架构/数据流图；④ 方案取舍表（有决策时）；⑤ 文件:行锚点 + 提议 diff/伪代码；⑥ 验证清单（测什么/看什么，不写逐步 shell 块）；⑦ 「瑶光反证」——关键断言 + file:line 或 run_tests 证据摘要，复现不了的标待验证假设；⑧ 重构类须含「回归清单」；⑨ 大计划（任务 >8 或引用文件 >15）须显式分波——` + "`### Wave N`" + ` 标题 + 每波验证命令（submit 门禁）。逐步命令与 git commit 留给批准后的执行阶段。</plan-methodology>`

// RenderPlanMethodologyAdvisory 渲染计划方法学建议块。
//
// 对账 TS `renderPlanMethodologyAdvisory`（volatile.ts:47-66）：
//
//	if (!methodology) return null
//	if (opts?.planMode) {
//	  if (!reason) return PLAN_MODE_METHODOLOGY_ADVISORY
//	  return PLAN_MODE_METHODOLOGY_ADVISORY.replace(
//	    '</plan-methodology>', `\n路由理由: ${reason}</plan-methodology>`)
//	}
//	const base = METHODOLOGY_ADVISORY_TEMPLATES[methodology]
//	if (!reason || methodology === 'lightweight') return base
//	return base.replace('</plan-methodology>', `\n路由理由: ${reason}</plan-methodology>`)
//
// **返回 nil 的语义**（Go 用 nil 表达 TS 的 null）：methodology 为空时
// 不产出该块——调用方据此跳过拼接。
//
// **★ 三条易错语义**（均由 oracle 用例钉住）：
//
//  1. **`planMode` 优先于 methodology**：`planMode=true` 时无论 methodology
//     是什么，都返回**同一份** design-doc 模板。oracle 用例
//     `planMode-lightweight` 实测返回的是 full 档模板（419 字符）——
//     若按 methodology 分派就会错。
//  2. **`!methodology` 先判**：`planMode=true` + methodology 为空 →
//     **仍返回 nil**（oracle 用例 `planMode-undefined` 实测 NULL）。
//  3. **lightweight 不追加理由**：TS 特判 `methodology === 'lightweight'`，
//     即使传了 reason 也不追加（oracle 用例 `lightweight-with-reason`
//     实测无「路由理由」）。只有 full 档才追加。
func RenderPlanMethodologyAdvisory(methodology PlanMethodology, reason string, planMode bool) *string {
	if methodology == "" {
		return nil
	}

	appendReason := func(base, r string) string {
		return strings.Replace(base, "</plan-methodology>",
			"\n路由理由: "+r+"</plan-methodology>", 1)
	}

	if planMode {
		if reason == "" {
			out := planModeMethodologyAdvisory
			return &out
		}
		out := appendReason(planModeMethodologyAdvisory, reason)
		return &out
	}

	base := methodologyAdvisoryTemplates[methodology]
	// 未知 methodology（非 lightweight/full）时 TS 的查表返回 undefined，
	// 后续 .replace 会抛异常。Go 侧 map 查不到返回空串——同样语义不良，
	// 故显式返回 nil（fail-safe：不产出该块，而非产出畸形块）。
	if base == "" {
		return nil
	}
	if reason == "" || methodology == MethodologyLightweight {
		out := base
		return &out
	}
	out := appendReason(base, reason)
	return &out
}

// RenderPermissionNote 渲染权限提示块。
//
// 对账 TS `renderPermissionNote`（volatile.ts:88-91）：
//
//	if (mode !== 'dangerously-skip-permissions') return ''
//	return '<permission-note>当前权限档：全自动（免审批）。…</permission-note>'
//
// **为什么只有 skip 档才产出**：TS 注释——「Under the unattended level tool
// calls no longer round-trip through approval, and tool-pipeline auto-grants
// out-of-workspace paths on first touch. Without this note the model only sees
// the static instruction『工作区外路径…用 request_path_access 申请』and
// dutifully asks — a round-trip the runtime would have auto-approved anyway.」
//
// **空串是刻意的**（非「无块」）：让其余档位的 turn 与改动前**逐字节相同**，
// 不产生缓存差异。
//
// **缓存安全**：只进动态 appendix（approvalMode 会中途翻转）。
func RenderPermissionNote(mode string) string {
	if mode != "dangerously-skip-permissions" {
		return ""
	}
	return "<permission-note>当前权限档：全自动（免审批）。工具调用不再需要用户批准——不要停下来等授权。工作区外路径的读写会在首次触达时自动授予本会话访问权（无需调用 request_path_access，也不要先申请再动手）。仍会拦截的只有已配置的 deny 规则与危险命令确认。</permission-note>"
}

// renderPlanExecutingText 对账 TS `renderPlanExecutingBlock`（volatile.ts:167）
// 返回的固定文案（553 字符）。**从 oracle 逐字节取，非手抄**。
const renderPlanExecutingText = `<plan-executing>
已批准计划进入执行期。执行纪律（原 executing-plans 技能已内置为原生流程，直接照此执行）：
1. **执行前三查** — 批判性审查计划：步骤间依赖顺序有无颠倒；验证条件是否明确可跑（"确认可用"不算，"run_tests 全绿"才算）；有无隐含环境假设（Node 版本、数据库连接、API Key）。
2. **逐波执行** — 按 Wave 顺序推进，每波结束跑该波验证命令再进下一波；每完成一个逻辑单元用 deliver_task 提交，不攒批。
3. **测试失败三分** — 实现 bug / 测试 bug / 计划误三条不同路径：改实现 / 改测试并记录理由 / 停下修订计划再继续，不混为一谈。
4. **阶段检查点** — 跨阶段计划（诊断→修复→验证）或 context 使用率 >60% 时暂停输出 handoff 摘要（进度、已完成项、待执行项、计划文档路径）交接，防单 session 注意力衰减。
5. **偏离即记录** — 与计划不一致的临时决策记入执行报告，不静默改道。
6. **完成报告四要素** — 完成的任务 / 验证结果（数字，来自本轮工具输出）/ 偏离计划的地方 / 遗留项。
</plan-executing>`

// RenderPlanExecutingBlock 渲染 Plan Mode 执行中的提示块。
//
// 对账 TS `renderPlanExecutingBlock`（volatile.ts:167）——无参、无状态，
// 返回固定文案。逐字节对账（553 字符）。
func RenderPlanExecutingBlock() string {
	return renderPlanExecutingText
}

// AppendixBlockName 提取 appendix 子块的 XML 标签名，供跨轮 diff 使用。
//
// 对账 TS `appendixBlockName`（volatile.ts:584-587）：
//
//	const m = /^<([^\s/>]+)/.exec(content)
//	return m ? m[1]! : `anon:${content.length}`
//
// **★ 正则的精确语义**（oracle 用例逐个钉住）：
//
//   - `^` **锚定开头，不 trim**：`  <indented>` → `anon:13`（前导空白不匹配）
//   - `[^\s/>]+` 排除空白、`/`、`>`：`<self-closing />` → `self-closing`
//   - **不要求闭合 `>`**：`<tag`（无闭合）→ `tag`
//   - **冒号不是分隔符**：`<ns:tag>` → `ns:tag`（命名空间标签整体保留）
//   - `< >` 中的空格使其不匹配：`<>` → `anon:2`
//
// **★ `anon:` 的长度是 UTF-16 code unit 数**（TS 的 `String.length`），
// **不是** Go 的 byte 长度、也不是 rune 数。判别证据（oracle 实测）：
//
//	输入           byte  rune  UTF-16   TS 实际
//	中文无标签      15     5      5       5
//	emoji+标记      10     7      8       8   ← 只有 UTF-16 匹配
//	a😀b            6     3      4       4   ← 只有 UTF-16 匹配
//
// 故用 `utf16Len` 而非 `len()` 或 `len([]rune())`。
// **这是纯 ASCII 用例无法发现的差异**——必须有多字节判别输入。
func AppendixBlockName(content string) string {
	name, ok := matchLeadingTag(content)
	if ok {
		return name
	}
	return "anon:" + itoaUTF16Len(content)
}

// matchLeadingTag 对账 TS 的正则 `/^<([^\s/>]+)/`。
//
// 手写而非用 regexp：Go 的 `regexp` 用 RE2，`[^\s/>]` 中的 `\s` 在字符类内
// 需写成 `\s`（RE2 支持），但 `^` 的锚定语义与「匹配到首个非法字符为止」
// 用索引扫描表达更直接，且避免每次调用编译正则的开销。
func matchLeadingTag(content string) (string, bool) {
	if !strings.HasPrefix(content, "<") {
		return "", false
	}
	rest := content[1:]
	end := 0
	for end < len(rest) {
		c := rest[end]
		// 排除空白 / `/` / `>`（对账 `[^\s/>]`）
		if c == '/' || c == '>' || isASCIISpace(c) {
			break
		}
		// 多字节 UTF-8 序列：整体纳入（TS 的 `[^\s/>]` 按 code unit 匹配，
		// 但非 ASCII 字符不会是 `\s` `/` `>`，故行为一致）
		end++
	}
	if end == 0 {
		// 首字符就是 `/` 或 `>` 或空白——`+` 要求至少一个，故不匹配。
		// 注意 `<>`：rest[0]='>' → end=0 → anon（对账 oracle `<>` → anon:2）。
		return "", false
	}
	return rest[:end], true
}

// isASCIISpace 对账 JS 正则 `\s` 在本场景的实际行为。
//
// **有意收窄**：JS 的 `\s` 匹配更多 Unicode 空白（\u00a0 / \u2028 等）。
// Go 侧只处理 ASCII 空白——因为标签名出现 Unicode 空白的场景不存在，
// 且 TS 侧的实际用例（oracle 覆盖）只用 ASCII 空格。
// **这是显式偏差**：若未来出现 Unicode 空白分隔的标签，两侧会不同。
// 记在此处以便后续需要时补全。
func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// itoaUTF16Len 返回 s 的 UTF-16 code unit 长度（对账 JS 的 String.length）。
//
// BMP 内字符计 1，BMP 外（emoji 等，U+10000 以上）计 2——与 Go 的
// `len([]rune(s))`（一律计 1）和 `len(s)`（UTF-8 byte 数）都不同。
func itoaUTF16Len(s string) string {
	n := len(utf16.Encode([]rune(s)))
	// 手写十进制转换（避免引入 strconv 只为这一个用途）
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
