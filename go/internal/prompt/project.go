package prompt

import (
	"os"
	"path/filepath"
)

// defaultProjectInstructionsCap 是 project-instructions 的默认字符预算。
//
// 对齐 TS 侧 FROZEN_BLOCK_CAPS.projectInstructions = 8000（block-policy.ts）。
// 之所以要 cap：超预算的项目文档会挤占前缀缓存预算，且按文档顺序硬切会
// 切掉末尾的纪律章节——这正是按节选取要解决的问题。
const defaultProjectInstructionsCap = 8000

// LoadProjectInstructions 读取 cwd 下的项目指令。
//
// 对账 src/prompt/volatile.ts 的 readRivetMd：
//   - AGENTS.md 是「地图」（项目结构与能力索引）
//   - .rivet.md 是「规程」（流程纪律）
//   - 两者用 "\n\n" 拼接；缺一则只用另一份；都缺返回空串
//
// fail-soft：读失败（不存在/无权限）按缺省处理，不 panic——提示词构造
// 不应因文件系统状态而中断。
func LoadProjectInstructions(cwd string) string {
	parts := []string{}
	for _, name := range []string{"AGENTS.md", ".rivet.md"} {
		raw, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		parts = append(parts, string(raw))
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[0] + joinRest(parts)
}

func joinRest(parts []string) string {
	out := ""
	for _, p := range parts[1:] {
		out += "\n\n" + p
	}
	return out
}

// projectInstructionsWrap 是 <project-instructions> 包裹标签的固定开销。
// 对账 TS volatile.ts:1118 的 `const wrap = '<project-instructions>\n\n</project-instructions>'.length`
// ——注意数的是 `\n\n`（两个换行），而实际渲染用单个 `\n`，这是 TS 的原样行为。
const projectInstructionsWrap = len("<project-instructions>\n\n</project-instructions>")

// RenderProjectInstructionsBlock 渲染 <project-instructions> 块。
//
// 对账 TS volatile.ts:1118-1124 的完整路径：
//
//	const wrap = '<project-instructions>\n\n</project-instructions>'.length
//	const selected = selectProjectInstructions(stripped, caps.projectInstructions - wrap, t => escapeXml(t).length)
//	parts.push(truncateBlock(`<project-instructions>\n${escapeXml(selected.text)}\n</project-instructions>`, caps.projectInstructions, 'project-instructions'))
//
// 两个易漏点（首版接线时都漏了）：
//  1. 选取预算要**先扣掉 wrap**（47 字符）
//  2. 包裹后还要**再过一次 truncateBlock**
//
// 反直觉但必须复刻：truncateBlock 的结果**可以超出 cap**。它内部扣的是
// 标签开销（maxChars - tag.length*2 - 10），而包裹标签加回来的可能更多
// （实测 cap=200 → 233 字符）。TS 就是这样，不能"顺手"让它不超。
func RenderProjectInstructionsBlock(md string, cap int) string {
	if cap <= 0 {
		cap = defaultProjectInstructionsCap
	}
	// measure 按**转义后**的 UTF-16 code unit 数计费——对账 TS 的
	// `t => escapeXml(t).length`。注意是 code unit 而非码点。
	measure := func(t string) int { return UTF16Len(EscapeXML(t)) }
	sel := SelectProjectInstructions(md, cap-projectInstructionsWrap, measure)
	block := "<project-instructions>\n" + EscapeXML(sel.Text) + "\n</project-instructions>"
	return TruncateBlock(block, cap, "project-instructions")
}

// BuildSystemPromptWithProject 渲染 system prompt，并把项目指令按节选取后
// 追加在尾部。
//
// Deprecated: **生产路径已改用 `BuildFullSystemPrompt`**（`cmd/tianshu/main.go`
// 唯一消费方）。后者通过 `RivetMd: LoadProjectInstructions(cwd)` 把项目指令
// 交给 volatile 层，渲染进 `<context>` 块内的 `project-instructions`。
//
// 本函数**保留**的原因：`project_test.go` 的 5 个测试通过它覆盖
// 「加载 → 按节选取 → 渲染 → 截断」的**集成路径**（`projinst_test.go` 只覆盖
// 各环节的单元行为）。删除函数会连带丢失这层集成保障，而保留一个无消费方的
// 导出函数成本极低。
//
// 注意：TS 侧这部分由 buildVolatileBlockInternal 渲染进 <context> 块。本函数
// 直接追加、不做 <context> 包裹——**不要**在其上叠加 volatile 渲染（会造成
// 两处渲染同一内容的双写）。
func BuildSystemPromptWithProject(ctx Context, cwd string, cap int) string {
	base := BuildSystemPrompt(ctx)
	md := LoadProjectInstructions(cwd)
	if md == "" {
		return base
	}
	return base + "\n\n" + RenderProjectInstructionsBlock(md, cap)
}
