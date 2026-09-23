// skill 工具：按名加载 skill 的完整指令。
//
// 对账 TS 的 `SKILL_TOOL`（src/tools/skill.ts:19）。
//
// ## Tier-2 激活
//
// 发现块（volatile appendix 的 available-skills）列出每个可用 skill 的
// name + description；本工具**按需**加载其中一个的**完整正文**。正文作为
// 普通工具结果返回——append-only 进历史——故整个会话都能看到。
//
// **零截断**：超大正文由工具管线的 artifact 拦截处理，与其他大工具输出一致。
//
// ## 缓存安全（关键设计）
//
// 静态定义**刻意不嵌入任何具体 skill 名**——故工具描述跨会话字节稳定，
// 前缀缓存得以保持。可加载的 skill 集合只存在于 volatile 发现块。
//
// 本工具的 `TestSkillDefinitionIsCacheSafe` 锁定这一点。
package tools

import (
	"context"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/skills"
)

type skillTool struct{}

// Skill 创建 skill 工具。
func Skill() Tool { return &skillTool{} }

func (t *skillTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "skill",
		Description: "按名称加载某个 skill 的完整指令，然后照做。\n\n" +
			"skill 是可复用的工作流 playbook。available-skills 区块列出了每个 skill 的名称和简述。当某个 skill 的简述与你正在做的事匹配时，用它的确切名称调用本工具，按需加载完整指令，然后执行。\n\n" +
			"执行完已加载的 skill 后，调用 skill(name=\"<name>\", complete=true) 释放它。这样工作流结束后，该 skill 的指令不会再被重新注入上下文。\n\n" +
			"示例：skill(name=\"brainstorming\")",
		InputSchema: objSchemaOrdered([]string{"name", "complete"}, map[string]any{
			"name":     strProp("要加载或标记完成的 skill 的确切名称（见 available-skills 区块）。"),
			"complete": boolProp("为 true 时，将该 skill 标记为已完成而不是加载它。该 skill 的指令将不再被重新注入上下文。"),
		}, "name"),
	}
}

func (t *skillTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	raw, _ := p.Input["name"].(string)
	// 对账 TS：`typeof raw !== 'string' || raw.trim().length === 0`
	// ——非字符串或 trim 后为空都报错。
	if raw == "" || jsTrimSpace(raw) == "" {
		return contract.Result{Content: "错误：name 必填", IsError: true}, nil
	}
	name := jsTrimSpace(raw)

	reg := p.SkillRegistry
	if reg == nil {
		reg = skills.Default
	}

	skill, found := reg.Find(name)
	if !found {
		// 退役的 skill 映射到原生流程而非硬报错——旧计划文本仍写着
		// 「使用 executing-plans」，模型可能条件反射地调本工具。
		if retired, ok := findRetired(name); ok {
			return contract.Result{
				Content: "Skill「" + retired + "」已退役并内置为原生流程，无需加载技能文件" +
					"——规划期直接按系统提示的 <plan-mode> 纪律执行，执行期按 <plan-executing> 纪律执行。",
				UIContent: "已映射到原生流程：" + retired,
			}, nil
		}
		available := []string{}
		for _, s := range reg.List() {
			available = append(available, s.Name)
		}
		sortStrings(available)
		list := "（未加载任何 skill）"
		if len(available) > 0 {
			list = strings.Join(available, ", ")
		}
		return contract.Result{
			Content: "未找到 skill：「" + name + "」。\n可用 skill：" + list,
			IsError: true,
		}, nil
	}

	if complete, _ := p.Input["complete"].(bool); complete {
		if p.OnSkillCompleted != nil {
			p.OnSkillCompleted(skill.Name)
		}
		return contract.Result{
			Content:   "Skill「" + skill.Name + "」已标记为完成。",
			UIContent: "已完成 skill：" + skill.Name,
		}, nil
	}

	if p.OnSkillInvoked != nil {
		p.OnSkillInvoked(skill.Name)
	}

	body := "<skill name=\"" + skill.Name + "\">\n" + skill.Body + "\n</skill>"
	// 扁平（无 SkillDir）skill 没有子文件——正文原样返回。
	if skill.SkillDir == "" {
		return contract.Result{
			Content:   body,
			UIContent: "已加载 skill：" + skill.Name,
		}, nil
	}
	files := skills.ListSkillFiles(skill.SkillDir, skills.FileListOpts{})
	if len(files) == 0 {
		return contract.Result{
			Content:   body,
			UIContent: "已加载 skill：" + skill.Name,
		}, nil
	}
	// 目录型 skill：附加子文件树，让模型知道它可按需（Tier-3）读什么。
	// 正文本身**永不截断**。
	tree := make([]string, 0, len(files))
	for _, f := range files {
		tree = append(tree, "  "+f.Path)
	}
	filesBlock := strings.Join([]string{
		`<skill-files dir="` + skill.SkillDir + `" note="按上方指令需要时再用 read_file/grep/glob 按需读取。不要预先全部加载。大文件先用 read_file(focus=...) 提取关键片段，缺口再用 offset/limit 精读；切勿把摘要当成完整文件。">`,
		strings.Join(tree, "\n"),
		"</skill-files>",
	}, "\n")

	return contract.Result{
		Content:   body + "\n" + filesBlock,
		UIContent: "已加载 skill：" + skill.Name + "（+" + itoaTool(len(files)) + " 个文件）",
	}, nil
}

// findRetired 在退役表里按大小写不敏感查找，返回**表里登记的名字**。
//
// 对账 TS：`RETIRED_BUNDLED_SKILLS.find(e => e.name.toLowerCase() === name.toLowerCase())`
// ——返回的是条目本身（故用 `retired.name` 渲染，不是用户输入的 name）。
func findRetired(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, e := range skills.RetiredBundledSkills {
		if strings.ToLower(e.Name) == lower {
			return e.Name, true
		}
	}
	return "", false
}

// sortStrings 是简单插入排序（避免为工具引入 sort 依赖的语义歧义）。
//
// **为什么不用 sort.Strings**：语义相同（字节序），但此处需要与 TS 的
// `Array.prototype.sort()` 默认序（UTF-16 code unit 序）对账。对 ASCII
// 名两者一致——skill 名实践中均为 ASCII。
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// itoaTool 是 strconv.Itoa 的本地别名。
func itoaTool(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (t *skillTool) RequiresApproval(*CallParams) bool { return false }
func (t *skillTool) ConcurrencySafe() bool             { return true }
func (t *skillTool) Enabled() bool                     { return true }
func (t *skillTool) Timeout(*CallParams) time.Duration { return 0 }
