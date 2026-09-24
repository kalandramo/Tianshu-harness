// terseness.go —— terse 输出风格的判定与渲染。
//
// 对账 TS `src/prompt/volatile.ts`：
//   - `resolveTersenessFlags`(227)
//   - `renderTersenessNudge`(215)
//
// **为什么做这两个**：第五十七刀的前置评估发现 `buildDynamicAppendixParts`
// 依赖 20+ 个 ctx 字段 + 8 个未移植函数（含 `resolveTersenessFlags` 与
// `renderTersenessNudge`）。这两个是那条依赖链里**唯一零会话状态依赖**的
// 一环——纯函数，可先行闭合，为后续移植铺路。
//
// **缓存安全**：terse nudge 只进**动态 appendix**（`tersenessEnabled` 会
// 中途翻转），绝不进 frozen 前缀。
package prompt

import "strings"

// TersenessContext 是 terseness 判定的 ctx 输入。
//
// 对账 TS `resolveTersenessFlags` 的 ctx 参数（volatile.ts:228-231）：
//
//	ctx: { tersenessEnabled?: boolean; tersenessEscalate?: boolean }
//
// **用 `*bool` 表达 TS 的 `boolean | undefined`**：TS 侧
// `ctx.tersenessEnabled === true` 是**严格比较**——undefined 与 false 都不
// 触发 optIn。Go 侧 nil 表达 undefined，非 nil 表达显式布尔值。虽然本函数
// 的两处判断（`== true` 与 `Boolean(...)`）对 nil 与 false 的结果相同，
// 但保留三态语义使移植逐字对账、且未来若 TS 改用 `!== undefined` 也不会
// 静默偏离。
type TersenessContext struct {
	Enabled  *bool
	Escalate *bool
}

// TersenessFlags 是 terseness 判定的输出。
//
// 对账 TS 的返回类型 `{ enabled: boolean; escalate: boolean }`。
type TersenessFlags struct {
	Enabled  bool
	Escalate bool
}

// ResolveTersenessFlags 判定本轮 appendix 是否启用 terse 输出风格。
//
// 对账 TS `resolveTersenessFlags`（volatile.ts:227-238）：
//
//	const raw = env['RIVET_TERSE']
//	const v = raw?.trim().toLowerCase()
//	const optOut = v === '0' || v === 'false' || v === 'off' || v === 'no'
//	if (optOut) return { enabled: false, escalate: false }
//	const optIn = v === '1' || v === 'true' || v === 'on' || v === 'yes'
//	              || ctx.tersenessEnabled === true
//	const escalate = Boolean(ctx.tersenessEscalate)
//	return { enabled: optIn || escalate, escalate }
//
// **★ optOut 的早返回是关键语义**：`RIVET_TERSE=0`（或 false/off/no）时
// **连 escalate 也被压制**——返回 `{false, false}` 而非 `{true, true}`。
// 即**环境变量的显式关闭优先于 ctx 的升级信号**。这不是笔误：oracle 用例
// `zero__ctx-escalate` 与 `zero__ctx-both` 实测均为 `{false, false}`。
//
// **三态逻辑**（oracle 102 个用例覆盖）：
//   - optOut（4 个字面量）→ 早返回 {false, false}
//   - optIn（4 个字面量）或 ctx.Enabled == true → enabled 为真
//   - **未识别值**（'maybe' / '2' / ” / 缺键）→ 既非 optOut 也非 optIn，
//     enabled 只取决于 ctx
//
// **大小写与空白**：TS 做 `trim().toLowerCase()`，故 `' TRUE '` 是 optIn、
// `' 0 '` 是 optOut。Go 侧同样处理。
func ResolveTersenessFlags(ctx TersenessContext, env map[string]string) TersenessFlags {
	raw, ok := env["RIVET_TERSE"]
	v := ""
	if ok {
		v = strings.ToLower(strings.TrimSpace(raw))
	}

	optOut := v == "0" || v == "false" || v == "off" || v == "no"
	if optOut {
		// ★ 早返回：压制 escalate（环境变量的显式关闭优先）
		return TersenessFlags{Enabled: false, Escalate: false}
	}

	optIn := v == "1" || v == "true" || v == "on" || v == "yes" ||
		(ctx.Enabled != nil && *ctx.Enabled)

	escalate := ctx.Escalate != nil && *ctx.Escalate

	return TersenessFlags{Enabled: optIn || escalate, Escalate: escalate}
}

// RenderTersenessNudge 渲染 terse 输出风格指令块。
//
// 对账 TS `renderTersenessNudge`（volatile.ts:215-223）：
//
//	const strict = escalate
//	  ? ' 你似乎在重复工作或打转——本轮尤其简洁：一段短文，不复述上下文。'
//	  : ''
//	return `<output-style>文字要精炼。跳过开场白、自我陈述和收尾总结。不要复述已展示的代码、文件内容或上下文——引用即可。直接给答案或动作。${strict} 本指令只约束输出文字——绝不因此削减验证、测试、取证或交付报告的严谨度。</output-style>`
//
// **escalate 的语义**：true 时追加一段「你似乎在重复工作或打转」的措辞——
// 针对模型陷入循环的场景（与 doom-loop 检测联动）。
//
// **★ 末句是防漂移的关键**：「本指令只约束输出文字——绝不因此削减验证、
// 测试、取证或交付报告的严谨度」。这句话防止模型把「精炼」误读为「跳过
// 验证步骤」——**移掉它会引入服从性漂移**（本项目 CVM 的核心防线之一）。
func RenderTersenessNudge(escalate bool) string {
	strict := ""
	if escalate {
		strict = " 你似乎在重复工作或打转——本轮尤其简洁：一段短文，不复述上下文。"
	}
	return "<output-style>文字要精炼。跳过开场白、自我陈述和收尾总结。不要复述已展示的代码、文件内容或上下文——引用即可。直接给答案或动作。" +
		strict +
		" 本指令只约束输出文字——绝不因此削减验证、测试、取证或交付报告的严谨度。</output-style>"
}
