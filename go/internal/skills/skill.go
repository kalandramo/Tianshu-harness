// Package skills 实现 skill 的加载、注册与发现（渐进式披露三层模型）。
//
// 对账 TS 的 src/skills/skill-loader.ts（1,040 行）的**工具依赖子集**。
//
// ## 三层模型（TS 文件头原文）
//
//   - Tier 1（发现）：每个 skill 的 name + description 注入动态 appendix 的
//     available-skills 块。**正文不在此层**。
//   - Tier 2（激活）：完整 SKILL.md 正文**按需**加载（模型经 skill 工具，
//     或用户经 /skill <name>）。零截断——超大正文由工具管线的 artifact
//     拦截处理（append-only）。
//   - Tier 3（子文件）：目录型 skill 的子文件树随正文附带，用到才读。
//
// ## scope 收窄（明示，见 HANDOFF）
//
// TS 的 skill-loader 还含**安装/管理面**（importSkillsIntoRivet /
// listInstallableSkills / writeSkill / uninstallSkill / seedBundledSkills /
// retireRetiredBundledSkills / countInstalledSkills）——那是**桌面端扩展面板
// 与 CLI 管理命令**的路径。Go 侧无对应 UI/命令，故不移植。
//
// 本包只移植 skill 工具**运行**所需的部分：frontmatter 解析、注册表、
// 目录扫描、发现层渲染、内置技能、退役表。
//
// ## 未接线（诚实披露）
//
// RenderDiscoveryBlock 是**纯函数**，当前**无生产消费方**——TS 侧它经
// promptEngine.setSkillAdvisoryBlock 注入 per-turn 动态 appendix，而 Go 的
// frozen 块是会话常量（prompt.VolatileContext 无该字段）。接入需要先决定
// Go 的 per-turn 注入机制（方向性改动，另刀处理）。
//
// 工具仍可端到端工作：注册表有内容 → 模型可调 skill(name=...) 加载。
// 只是模型「如何知道有哪些 skill」这一环待补。
package skills

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Source 是 skill 的来源层级（决定覆盖优先级，后者覆盖前者同名）。
//
// 对账 TS 的 SkillSource。Go 侧当前只用到 builtin / rivet 两档——
// 其余档位的扫描（~/.agents、~/.claude、plugin）属管理面，未移植。
type Source string

const (
	SourceBuiltin       Source = "builtin"
	SourceRivet         Source = "rivet"
	SourceGlobalRivet   Source = "global-rivet"
	SourceProjectAgents Source = "project-agents"
	SourceGlobalAgents  Source = "global-agents"
)

// Definition 是一个已加载的 skill。
//
// 对账 TS 的 SkillDefinition。
type Definition struct {
	// Name 是 skill 名（frontmatter 的 name，缺省取文件名去 .md）。
	Name string
	// Description 是简述（进发现层，故必须短）。
	Description string
	// Triggers 是相关性正则——任一匹配即标记该 skill 与本轮相关。
	Triggers []*regexp.Regexp
	// Body 是 SKILL.md 正文（frontmatter 之后的内容，已 trim）。
	Body string
	// TierLock 是可选的模型档位锁定。
	TierLock string
	// BuiltIn 标记内置技能。
	BuiltIn bool
	// Source 是加载来源（由 loader 设置，非 parser）。
	Source Source
	// BodyPath 是后备文件的绝对路径。
	BodyPath string
	// SkillDir 是 skill 根目录（仅目录型有；扁平 .md 为空）。
	SkillDir string
}

// FileEntry 是目录型 skill 的一个子文件。
//
// 对账 TS 的 SkillFileEntry。
type FileEntry struct {
	Path string
	Kind string // "file" | "dir"
}

// reFrontmatter 对账 TS 的 FRONTMATTER_RE = /^---\n([\s\S]*?)\n---\n([\s\S]*)$/
//
// 注意 Go 的 `.` 默认不匹配换行，故用 (?s) 让 [\s\S] 语义等价。
var reFrontmatter = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n(.*)$`)

// jsSpaceClass 是 JS 正则 `\s` 的字符类（用于手写正则）。
//
// Go 的 `\s` 是 [\t\n\f\r ]，**不含** `\v`、NBSP、U+2000-200A、U+FEFF 等。
const jsSpaceClass = `[\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`

// reFrontmatterLine 对账 TS 的 /^(\w+):\s*(.*)$/
//
// JS 的 `\w` 是 [A-Za-z0-9_]（ASCII），Go 的 `\w` 默认也是 ASCII——等价。
var reFrontmatterLine = regexp.MustCompile(`^(\w+):` + jsSpaceClass + `*(.*)$`)

// reIndent 对账 TS 的 /^(\s+)/（行首空白）。
var reIndent = regexp.MustCompile(`^` + jsSpaceClass + `+`)

// reJSSpacesRun 对账 TS 的 /\s+/g（把描述里的空白折叠为单空格）。
var reJSSpacesRun = regexp.MustCompile(jsSpaceClass + `+`)

// normalizeFrontmatterSource 归一化 markdown 源（对账 TS 的
// normalizeFrontmatterSource，src/utils/frontmatter.ts）。
//
// **为什么必须做**：仓库所有 frontmatter 解析器都假设 LF-only
// （/^---\n…\n---\n/）。Windows 上同一文件会带 CRLF（git autocrlf /
// 桌面编辑器）与 UTF-8 BOM（记事本），导致每个 .rivet/skills/ 技能
// 在桌面端构建里都报 "missing YAML frontmatter"。
//
// 在解析入口**一次性**剥 BOM + 折叠 CRLF / 孤立 CR 为 LF，而不是在每个
// 正则里撒 \r?。
func normalizeFrontmatterSource(content string) string {
	content = strings.TrimPrefix(content, "\uFEFF")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return content
}

// jsTrimSpace 对账 JS 的 String.prototype.trim()（含 U+FEFF 等，
// Go 的 strings.TrimSpace 不含）。
//
// 与 internal/tools、internal/artifact 的同名函数同语义——各包独立
// 实现是本仓库的既有模式（无共享的 js 语义包）。
func jsTrimSpace(s string) string {
	return strings.TrimFunc(s, isJSSpace)
}

// isJSSpace 对账 JS 正则 `\s` 的字符集。
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0x1680, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return (r >= 0x2000 && r <= 0x200A) || r == 0x2028 || r == 0x2029
}

// sliceChars 对账 JS 的 s.slice(0, n)——按 **UTF-16 code unit** 截断。
func sliceChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := 0; i < len(s); {
		if count >= n {
			return s[:i]
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r > 0xFFFF {
			if count+2 > n {
				return s[:i]
			}
			count += 2
		} else {
			count++
		}
		i += size
	}
	return s
}

// charLen 对账 JS 的 s.length（UTF-16 code unit 计数）。
func charLen(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// ParseSkillMarkdown 解析 SKILL.md 内容为 Definition。
//
// 对账 TS 的 parseSkillMarkdown(content, fileName)。
//
// fileName 是**回退名**：frontmatter 无 name 时取文件名去 .md
// （目录型 skill 传目录名，故无 frontmatter 的 SKILL.md 以目录为名）。
//
// 无 frontmatter 时**报错**（对账 TS 的 throw）。
func ParseSkillMarkdown(content, fileName string) (Definition, error) {
	content = normalizeFrontmatterSource(content)
	m := reFrontmatter.FindStringSubmatch(content)
	if m == nil {
		return Definition{}, &MissingFrontmatterError{File: fileName}
	}

	fm := parseFrontmatter(m[1])
	body := jsTrimSpace(m[2])

	name := ""
	if v, ok := fm["name"]; ok {
		if s, ok := v.(string); ok && s != "" {
			name = s
		}
	}
	if name == "" {
		name = strings.TrimSuffix(fileName, ".md")
	}

	def := Definition{
		Name:    name,
		Body:    body,
		BuiltIn: false,
	}
	if v, ok := fm["description"]; ok {
		if s, ok := v.(string); ok {
			def.Description = s
		}
	}
	if v, ok := fm["tierLock"]; ok {
		if s, ok := v.(string); ok && (s == "cheap" || s == "balanced" || s == "strong") {
			def.TierLock = s
		}
	}

	// triggers / trigger 二选一（TS：fm.triggers ?? fm.trigger）。
	raw, ok := fm["triggers"]
	if !ok {
		raw, ok = fm["trigger"]
	}
	if ok {
		def.Triggers = compileTriggers(raw)
	}

	return def, nil
}

// MissingFrontmatterError 表示 skill 文件缺 YAML frontmatter。
//
// 文案逐字对账 TS 的 `Skill ${fileName}: missing YAML frontmatter`。
type MissingFrontmatterError struct{ File string }

func (e *MissingFrontmatterError) Error() string {
	return "Skill " + e.File + ": missing YAML frontmatter"
}

// compileTriggers 把 frontmatter 的 triggers 值编译为正则列表。
//
// 对账 TS：
//
//	Array.isArray(raw) → 每个 new RegExp(String(t), 'i')
//	string 且非空      → 单个 new RegExp(raw, 'i')
//
// **大小写不敏感**（JS 的 'i' 标志）——Go 侧加 (?i) 前缀。
// 编译失败的正则**跳过**（TS 会抛，但那是配置错误；Go 侧选择静默降级
// 并在 loader 层记入 errors——见 LoadFromDirectory 的注释）。
func compileTriggers(raw any) []*regexp.Regexp {
	compile := func(s string) *regexp.Regexp {
		re, err := regexp.Compile("(?i)" + s)
		if err != nil {
			return nil
		}
		return re
	}
	switch v := raw.(type) {
	case []any:
		out := make([]*regexp.Regexp, 0, len(v))
		for _, t := range v {
			s, ok := t.(string)
			if !ok {
				continue
			}
			if re := compile(s); re != nil {
				out = append(out, re)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		if re := compile(v); re != nil {
			return []*regexp.Regexp{re}
		}
	}
	return nil
}
