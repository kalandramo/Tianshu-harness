package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry 是 skill 注册表（进程内）。
//
// 对账 TS 的 `SkillRegistry`。TS 侧是**进程级单例**（`skillRegistry`），
// Go 侧同样导出 `Default` 单例，但工具通过接口取用以便测试注入。
type Registry struct {
	skills map[string]Definition
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{skills: map[string]Definition{}}
}

// Default 是进程级默认注册表（对账 TS 的 `skillRegistry` 单例）。
var Default = NewRegistry()

// Register 注册一个 skill（同名覆盖）。
func (r *Registry) Register(s Definition) {
	r.skills[s.Name] = s
}

// Unregister 移除一个 skill。返回是否存在。
//
// **注意**（对账 TS 注释）：注册表是进程级共享的，unregister 影响所有会话。
// 卸载**不应**调它——当前会话会立刻失效；正确做法是留着不动，下次 bootstrap
// 时消失。本方法为完整性/测试而存在。
func (r *Registry) Unregister(name string) bool {
	if _, ok := r.skills[name]; !ok {
		return false
	}
	delete(r.skills, name)
	return true
}

// Get 按精确名查找。
func (r *Registry) Get(name string) (Definition, bool) {
	s, ok := r.skills[name]
	return s, ok
}

// List 返回全部 skill（顺序不保证——TS 的 Map 插入序，Go 的 map 无序）。
//
// **调用方需自行排序**：发现层渲染与错误提示都要求稳定序（见
// RenderDiscoveryBlock / 工具的错误分支）。
func (r *Registry) List() []Definition {
	out := make([]Definition, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}

// Find 按名查找，带**大小写不敏感回退**。
//
// 对账 TS：`registry.get(name) ?? registry.list().find(s => s.name.toLowerCase() === name.toLowerCase())`
//
// **为什么需要回退**：模型常按记忆写成不同大小写（如 `PDF` vs `pdf`）。
// 精确失败后应回退而非直接报「未找到」。
func (r *Registry) Find(name string) (Definition, bool) {
	if s, ok := r.skills[name]; ok {
		return s, true
	}
	lower := strings.ToLower(name)
	for _, s := range r.skills {
		if strings.ToLower(s.Name) == lower {
			return s, true
		}
	}
	return Definition{}, false
}

// Match 返回 triggers 显式匹配给定文本的 skill。
//
// 对账 TS 的 `match(text)`：`triggers.length === 0 || triggers.some(re => re.test(text))`
//
// **注意 `length === 0` 也算匹配**——无 trigger 的 skill 视为「始终相关」。
func (r *Registry) Match(text string) []Definition {
	var out []Definition
	for _, s := range r.skills {
		if len(s.Triggers) == 0 {
			out = append(out, s)
			continue
		}
		for _, re := range s.Triggers {
			if re.MatchString(text) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// LoadResult 是一次目录加载的结果。
type LoadResult struct {
	Loaded []string
	Errors []string
}

// LoadFromDirectory 从目录加载 skill，支持两种形态并存。
//
// 对账 TS 的 `loadFromDirectory`：
//   - 扁平 `name.md`（天枢原生格式）——无 SkillDir
//   - 目录 `name/SKILL.md`（Claude/agentskills 格式）——保留目录（**不摊平**），
//     以便其子文件（references/、scripts/、assets/）按需读取。设 SkillDir。
//
// **跳过 `_` 前缀条目**（如 `_drafts/`）：自动蒸馏的技能草稿是**仅供审阅**的，
// 绝不能进入发现层或冻结前缀。
func (r *Registry) LoadFromDirectory(dir string, source Source) LoadResult {
	res := LoadResult{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return res // 目录不存在 → 空结果（对账 TS 的 existsSync 早退）
	}

	// 稳定序：TS 用 readdirSync 的目录序，但发现层与错误列表都要求确定性
	// ——按名排序（对账 TS 在别处对 list 排序的做法）。
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "_") {
			continue
		}
		if e.IsDir() {
			skillFile := filepath.Join(dir, name, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			raw, err := os.ReadFile(skillFile)
			if err != nil {
				res.Errors = append(res.Errors, name+": "+err.Error())
				continue
			}
			// 目录型 skill 以**目录名**为回退名（无 frontmatter 的 SKILL.md
			// 以目录为名）。
			def, err := ParseSkillMarkdown(string(raw), name)
			if err != nil {
				res.Errors = append(res.Errors, name+": "+err.Error())
				continue
			}
			def.Source = source
			def.BodyPath = skillFile
			def.SkillDir = filepath.Join(dir, name)
			r.skills[def.Name] = def
			res.Loaded = append(res.Loaded, def.Name)
			continue
		}
		if strings.HasSuffix(name, ".md") {
			skillFile := filepath.Join(dir, name)
			raw, err := os.ReadFile(skillFile)
			if err != nil {
				res.Errors = append(res.Errors, name+": "+err.Error())
				continue
			}
			def, err := ParseSkillMarkdown(string(raw), name)
			if err != nil {
				res.Errors = append(res.Errors, name+": "+err.Error())
				continue
			}
			def.Source = source
			def.BodyPath = skillFile
			r.skills[def.Name] = def
			res.Loaded = append(res.Loaded, def.Name)
		}
	}
	return res
}

// RenderDiscoveryBlock 渲染 Tier-1 发现块（`<available-skills>`）。
//
// 对账 TS 的 `renderDiscoveryBlock(hint, opts)`。
//
// 每个可用 skill 的 name + description——**正文绝不在此层**。triggers 匹配
// `hint` 的排前并标 `relevant="true"`。预算只花在描述上，故溢出罕见；真溢出
// 时丢掉**最不相关**的尾部（正文一个都没有，故无正文损失）。
//
// 返回空串表示无块（对账 TS 的 null）。
func (r *Registry) RenderDiscoveryBlock(hint string, opts DiscoveryOpts) string {
	all := r.List()
	if len(opts.Exclude) > 0 {
		filtered := make([]Definition, 0, len(all))
		for _, s := range all {
			if _, skip := opts.Exclude[s.Name]; skip {
				continue
			}
			filtered = append(filtered, s)
		}
		all = filtered
	}
	if len(all) == 0 {
		return ""
	}

	maxChars := opts.MaxChars
	if maxChars == 0 {
		maxChars = 1500
	}
	maxDescChars := opts.MaxDescChars
	if maxDescChars == 0 {
		maxDescChars = 200
	}

	isRelevant := func(s Definition) bool {
		if hint == "" || len(s.Triggers) == 0 {
			return false
		}
		for _, re := range s.Triggers {
			if re.MatchString(hint) {
				return true
			}
		}
		return false
	}

	// 相关的排前（组内按名稳定序）——预算溢出时保留最有用条目。
	ordered := make([]Definition, len(all))
	copy(ordered, all)
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := isRelevant(ordered[i]), isRelevant(ordered[j])
		if ri != rj {
			return ri // true 在前
		}
		return ordered[i].Name < ordered[j].Name
	})

	var lines []string
	budget := maxChars
	dropped := 0
	for _, s := range ordered {
		// 对账 TS：`(skill.description || '').replace(/\s+/g, ' ').trim().slice(0, maxDescChars)`
		desc := sliceChars(jsTrimSpace(reJSSpacesRun.ReplaceAllString(s.Description, " ")), maxDescChars)
		rel := ""
		if isRelevant(s) {
			rel = ` relevant="true"`
		}
		line := `<skill name="` + s.Name + `"` + rel + `>` + desc + `</skill>`
		// 长度按 UTF-16 code unit 计（对账 JS 的 `.length`）。
		if charLen(line) > budget {
			dropped++
			continue // 试更小的条目，而不是把后面的全砍掉
		}
		lines = append(lines, line)
		budget -= charLen(line)
	}
	if len(lines) == 0 {
		return ""
	}

	out := []string{
		`<available-skills note="Call the skill tool with a name to load its full instructions on demand.">`,
	}
	out = append(out, lines...)
	if dropped > 0 {
		out = append(out, `<more count="`+itoa(dropped)+`" note="More skills available but omitted for space. Refine your request to surface them, or the user can run /skill list."/>`)
	}
	out = append(out, "</available-skills>")
	return strings.Join(out, "\n")
}

// DiscoveryOpts 是发现层渲染选项。
type DiscoveryOpts struct {
	// MaxChars 是块的总字符预算（0 = 默认 1500）。
	MaxChars int
	// MaxDescChars 是单条描述的截断上限（0 = 默认 200）。
	MaxDescChars int
	// Exclude 是本会话禁用的 skill 名（对账 TS 的 `exclude`）。
	Exclude map[string]bool
}

// ListSkillFiles 列出目录型 skill 的子文件（相对 skillDir，**排除 SKILL.md**）。
//
// 对账 TS 的 `listSkillFiles(skillDir, opts)`。
//
// 受深度与条目数约束，防病态目录淹没模型上下文。这是「安全网」树：作者在
// SKILL.md 里手写的链接是主路径，此列表防模型在链接缺失时盲探。
//
// **路径统一正斜杠**：`filepath.Rel` 在 Windows 返回反斜杠，会让
// `<skill-files>` 里给模型看的路径与 glob / repo_map 输出不一致，且跨平台
// 断言不成立（TS 源码有同样的注释）。
func ListSkillFiles(skillDir string, opts FileListOpts) []FileEntry {
	maxDepth := opts.MaxDepth
	if maxDepth == 0 {
		maxDepth = 3
	}
	maxEntries := opts.MaxEntries
	if maxEntries == 0 {
		maxEntries = 50
	}
	out := []FileEntry{}

	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > maxDepth || len(out) >= maxEntries {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if len(out) >= maxEntries {
				return
			}
			abs := filepath.Join(d, e.Name())
			rel, err := filepath.Rel(skillDir, abs)
			if err != nil {
				continue
			}
			rel = strings.ReplaceAll(rel, "\\", "/")
			if rel == "SKILL.md" {
				continue
			}
			if e.IsDir() {
				out = append(out, FileEntry{Path: rel + "/", Kind: "dir"})
				walk(abs, depth+1)
			} else {
				out = append(out, FileEntry{Path: rel, Kind: "file"})
			}
		}
	}
	walk(skillDir, 1)
	return out
}

// FileListOpts 是子文件列举选项。
type FileListOpts struct {
	MaxDepth   int
	MaxEntries int
}

// Assemble 装配一个可用的注册表（内置技能 + 用户级 + 项目级）。
//
// 对账 TS 的 `loadProjectSkills(cwd, opts)`。
//
// ## 五层优先级（后者覆盖前者同名）
//
//  1. builtin（随天枢发布，最低）
//  2. ~/.agents/skills（agentskills.io 跨 agent 标准目录）
//  3. ~/.rivet/skills（跨项目复用）
//  4. 项目 .agents/skills（标准目录项目级）
//  5. 项目 .rivet/skills（项目定制，最高）
//
// **覆盖语义靠调用顺序**：`Registry.Register` 是同名覆盖，故后加载者胜出。
// 顺序即优先级——改动此顺序会静默改变覆盖行为，`TestAssembleLayerPrecedence`
// 锁定它。
//
// `.agents/skills` 槽位低于同层 rivet 原生（生态技能可被原生覆盖）。
//
// ## 未做（明示）
//
// TS 在加载前还会 `seedBundledSkills`（把 app 内置技能种入项目目录）与
// `retireRetiredBundledSkills`（清理退役副本）——两者都是**写盘**操作，
// 属管理面（随桌面端扩展面板 / CLI 管理命令一起做）。
//
// 返回加载结果（loaded / errors 供调用方展示）。目录不存在时**静默跳过**
// （用户级目录常不存在，不应产生噪音）——`LoadFromDirectory` 已保证这点。
func Assemble(cwd string) (*Registry, LoadResult) {
	return AssembleWithHome(cwd, osUserHomeDir())
}

// AssembleWithHome 是 Assemble 的可注入 home 版本（测试用）。
//
// **为什么参数化 home**：用户级目录（层 2/3）在测试里必须可控——否则测试
// 会读到开发者真实 `~/.rivet/skills`，结果随机器而异（非确定性测试）。
func AssembleWithHome(cwd, home string) (*Registry, LoadResult) {
	r := NewRegistry()
	res := LoadResult{}

	load := func(dir string, source Source) {
		if dir == "" {
			return
		}
		lr := r.LoadFromDirectory(dir, source)
		res.Loaded = append(res.Loaded, lr.Loaded...)
		res.Errors = append(res.Errors, lr.Errors...)
	}

	// 1. 内置技能（最低优先级）。
	res.Loaded = append(res.Loaded, RegisterBuiltinSkills(r)...)

	// 2. 用户级 ~/.agents/skills（agentskills.io 跨 agent 标准目录）。
	if home != "" {
		load(filepath.Join(home, ".agents", "skills"), SourceGlobalAgents)
	}
	// 3. 用户级 ~/.rivet/skills（跨项目复用）。
	if home != "" {
		load(filepath.Join(home, ".rivet", "skills"), SourceGlobalRivet)
	}
	// 4. 项目级 .agents/skills（标准目录项目级）。
	if cwd != "" {
		load(filepath.Join(cwd, ".agents", "skills"), SourceProjectAgents)
	}
	// 5. 项目级 .rivet/skills（最高优先级——同名覆盖以上全部）。
	if cwd != "" {
		load(filepath.Join(cwd, ".rivet", "skills"), SourceRivet)
	}

	return r, res
}

// osUserHomeDir 返回用户主目录（失败返回空串）。
//
// **不读 `RIVET_HOME`**：那是 rivet 的**数据根目录**覆盖（见
// internal/trust.RivetHome），而 skill 的用户级路径对账 TS 的 `homedir()`
// ——直接用主目录。混用会让 `RIVET_HOME` 意外改变 skill 搜索路径。
func osUserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// itoa 是 strconv.Itoa 的本地别名（避免为一个调用引入 strconv）。
func itoa(n int) string {
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
