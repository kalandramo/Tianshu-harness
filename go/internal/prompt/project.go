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

// BuildSystemPromptWithProject 渲染 system prompt，并把项目指令按节选取后
// 追加在尾部。
//
// 这是 projinst.go 的**生产消费路径**——没有它，按节选取算法就是悬空代码。
//
// 结构：
//
//	<static 提示词>
//
//	<project-instructions>
//	<按节选取后的项目文档>
//	</project-instructions>
//
// 注意：TS 侧这部分由 buildVolatileBlockInternal 渲染，且 project-instructions
// 经 escapeXml 转义后进 <context> 块。此处是**最小可用路径**——直接追加，
// 不做 XML 转义与 <context> 包裹。后续移植 volatile 层时应替换本函数，
// 而非在其上叠加（否则会出现两处渲染同一内容的双写）。
func BuildSystemPromptWithProject(ctx Context, cwd string, cap int) string {
	base := BuildSystemPrompt(ctx)
	md := LoadProjectInstructions(cwd)
	if md == "" {
		return base
	}
	if cap <= 0 {
		cap = defaultProjectInstructionsCap
	}

	sel := SelectProjectInstructions(md, cap, func(t string) int { return len([]rune(EscapeXML(t))) })
	block := "<project-instructions>\n" + EscapeXML(sel.Text) + "\n</project-instructions>"
	return base + "\n\n" + block
}
