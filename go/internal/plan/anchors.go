package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// anchors.go —— 计划事实锚点校验。
//
// 对账 TS `src/plan/plan-fact-anchors.ts`（380 行）。
//
// # 设计约束（逐字对账 TS 文件头）
//
//   - **通用**：天枢部署到任意用户项目。路径识别纯**形状**驱动（含 `/` +
//     已知扩展名结尾）+ 文件系统 stat——**绝不是本仓库目录布局的硬编码白名单**。
//   - **歧义时 fail-open**：解析到项目外、URL、模块相对导入、无法解析的 token
//     一律**跳过**而非标记。消费者（plan submit）是一次性软阻塞，误报的代价
//     是一次重提，永不形成死路。
//   - **上报前先换根**：cwd 处未命中时探测替代根（cwd 子目录 → 父目录 → 兄弟
//     项目）并报 `root-mismatch` 附实际位置，而不是 `missing-file`。
//     换根命中的文件**只 stat、从不读取**（containment 纪律）。
//   - **占位形态** basename（`foo.ts`、`a.py`…）在所有根下都缺失时视为举例
//     散文并跳过；真名文件仍正常校验。
//
// # RE2 硬约束（**计划期的断言已实测修正**）
//
// TS 的正则用 `(?<![\w./\\-])` 前置断言与 `(?!\w)` 后置断言。**实测确认：
// Go 的 regexp（RE2）两者都不支持**——编译均失败：
//
//	a(?!b)   → invalid or unsupported Perl syntax: `(?!`
//	(?<!x)a  → invalid named capture: `(?<!x)a`
//
// 故**前后两个断言都改为手工边界检查**（用 `FindAllStringSubmatchIndex` 拿
// 字节偏移后判定前后字符）。这一点修正了本刀计划里「`(?!...)` RE2 支持」的
// 错误断言。

// PlanAnchorDriftKind 是漂移类型。
//
// 对账 TS `PlanAnchorDriftKind`（plan-fact-anchors.ts:32）。
type PlanAnchorDriftKind string

const (
	DriftMissingFile    PlanAnchorDriftKind = "missing-file"
	DriftLineOutOfRange PlanAnchorDriftKind = "line-out-of-range"
	DriftRootMismatch   PlanAnchorDriftKind = "root-mismatch"
)

// PlanAnchorDrift 是一条锚点漂移。
//
// 对账 TS `PlanAnchorDrift`（plan-fact-anchors.ts:34-42）。
type PlanAnchorDrift struct {
	Anchor string              `json:"anchor"`
	Path   string              `json:"path"`
	Line   *int                `json:"line,omitempty"`
	Kind   PlanAnchorDriftKind `json:"kind"`
	Detail string              `json:"detail"`
}

// PlanAnchorReport 是校验报告。
//
// 对账 TS `PlanAnchorReport`（plan-fact-anchors.ts:44-51）。
type PlanAnchorReport struct {
	// Checked 是实际校验过的**不同**锚点数。
	Checked int `json:"checked"`
	// Drifts 是漂移列表。
	Drifts []PlanAnchorDrift `json:"drifts"`
	// RerootProbes 是换根探测实际发生的 syscall 数（stat + readdir，
	// 预算 REROOT_PROBE_BUDGET 封顶）。0 = 本次调用无需换根探测。
	RerootProbes int `json:"rerootProbes"`
}

// anchorExtensions 是通用文件扩展名集。
//
// 对账 TS `EXTENSIONS`（plan-fact-anchors.ts:52-67）——**顺序逐字相同**。
// 识别是形状驱动而非项目驱动；扩展此列表是安全的，未知扩展名的锚点
// 只是不被校验（fail-open）。
var anchorExtensions = []string{
	"ts", "tsx", "js", "jsx", "mjs", "cjs", "mts", "cts",
	"json", "jsonc", "md", "mdx", "txt",
	"css", "scss", "less", "html", "vue", "svelte",
	"py", "rs", "go", "java", "kt", "rb", "php", "c", "h", "cpp", "hpp", "cs", "swift",
	"sh", "bash", "zsh", "ps1", "bat",
	"yml", "yaml", "toml", "ini", "env.example", "sql", "graphql", "proto",
}

// extAlternation 是**按长度降序**、点号转义后 `|` 连接的扩展名交替。
//
// 对账 TS `EXT_ALTERNATION`（plan-fact-anchors.ts:73-76）：
//
//	[...EXTENSIONS].sort((a, b) => b.length - a.length)
//	    .map(ext => ext.replace(/\./g, String.raw`\.`)).join('|')
//
// **为什么必须长度降序**：否则 `selector.tsx` 会被 `selector.ts` 抢先匹配
// （短交替先赢）。TS 另有 `(?!\w)` 后置断言兜底；Go 侧无断言，故排序是
// **唯一**保障——这里不能省。
var extAlternation = func() string {
	sorted := append([]string(nil), anchorExtensions...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})
	escaped := make([]string, len(sorted))
	for i, ext := range sorted {
		escaped[i] = strings.ReplaceAll(ext, ".", `\.`)
	}
	return strings.Join(escaped, "|")
}()

// pathTokenRe 匹配「至少一个目录段 + 已知扩展名文件名 + 可选 :line 或 :line-line」。
//
// **与 TS 的差异**：TS 的正则含前置 `(?<![\w./\\-])` 与后置 `(?!\w)` 断言；
// RE2 不支持，故此处**只保留主体**，前后边界由 `findAnchorTokens` 手工判定。
var pathTokenRe = regexp.MustCompile(
	`((?:[\w.@-]+/)+[\w.@-]+\.(?:` + extAlternation + `))(?::(\d+)(?:-\d+)?)?`,
)

// knownExtTailRe 匹配「以已知扩展名结尾」的中间段。
//
// 对账 TS `KNOWN_EXT_TAIL_RE`（plan-fact-anchors.ts:86）。
var knownExtTailRe = regexp.MustCompile(`(?i)\.(?:` + extAlternation + `)$`)

// placeholderBasenames 是举例散文里的占位 basename。
//
// 对账 TS `PLACEHOLDER_BASENAMES`（plan-fact-anchors.ts:95）。
var placeholderBasenames = map[string]bool{
	"foo": true, "bar": true, "baz": true, "qux": true, "quux": true,
	"example": true, "sample": true, "demo": true, "dummy": true, "placeholder": true,
}

// newFileMarkerRe 匹配「声明该引用是有意新建」的标记。
//
// 对账 TS `NEW_FILE_MARKER_RE`（plan-fact-anchors.ts:104）：
// `/新增|新建|创建|\bnew file\b|\bcreate[ds]?\b/i`
//
// **故意收窄**——宽泛标记（如英文 "add"）会豁免英文散文里的多数锚点，
// 把存在性校验架空（fail-open 过头）。
var newFileMarkerRe = regexp.MustCompile(`(?i)新增|新建|创建|\bnew file\b|\bcreate[ds]?\b`)

// checkableFenceLangs 是内容仍可能含可校验项目路径的围栏语言。
//
// 对账 TS `CHECKABLE_FENCE_LANGS`（plan-fact-anchors.ts:107）。
var checkableFenceLangs = map[string]bool{
	"bash": true, "sh": true, "shell": true, "zsh": true, "console": true, "": true,
}

// lineCheckMaxBytes 是行数校验的大小上限（成本护栏）。
//
// 对账 TS `LINE_CHECK_MAX_BYTES`（plan-fact-anchors.ts:110）= 2 MiB。
const lineCheckMaxBytes = 2 * 1024 * 1024

// maxAnchors 限制每份计划的锚点校验总量。
//
// 对账 TS `MAX_ANCHORS`（plan-fact-anchors.ts:113）= 200。
const maxAnchors = 200

// rerootExcludeDirs 是换根探测的排除目录。
//
// 对账 TS `REROOT_EXCLUDE_DIRS`（plan-fact-anchors.ts:172）。
var rerootExcludeDirs = map[string]bool{"node_modules": true, "dist": true}

// rerootProbeBudget 是换根探测的总 syscall 预算（stat + readdir 合计）。
//
// 对账 TS `REROOT_PROBE_BUDGET`（plan-fact-anchors.ts:177）= 128。
//
// 渐进探测的核心承诺：**最坏成本与目录规模解耦**——300 还是 3000 个子目录
// 都在预算内封顶，预算耗尽后剩余锚点 fail-open 降级为 missing-file
// （软阻塞性质不变）。
const rerootProbeBudget = 128

// maxRootEnum 是单个父级目录的枚举截断（极端目录兜底）。
//
// 对账 TS `MAX_ROOT_ENUM`（plan-fact-anchors.ts:179）= 512。
const maxRootEnum = 512

// extractedAnchor 是从计划里提取出的候选锚点。
//
// 对账 TS `ExtractedAnchor`（plan-fact-anchors.ts:115-121，未导出）。
type extractedAnchor struct {
	raw               string
	path              string
	line              *int
	declaredNew       bool
	placeholderShaped bool
}

// fenceLangRe 匹配围栏行并捕获语言。
var fenceLangRe = regexp.MustCompile(`^\s*` + "```" + `([\w-]*)`)

// findAnchorTokens 在单行里找出全部锚点 token（**手工前后边界检查**）。
//
// 替代 TS 正则的 `(?<![\w./\\-])` 与 `(?!\w)` 断言：
//   - 前置：token 起点的前一个字符不得落在 `[\w./\\-]` 内（拒绝 URL/path 前缀
//     粘连，如 https URL 里的 `github.com/...` 前面是 `/`）。
//   - 后置：token 主体结尾的下一个字符不得是 `\w`（防 `selector.tsx` 被
//     `selector.ts` 抢先匹配——虽然长度降序已覆盖，双保险）。
func findAnchorTokens(line string) []anchorToken {
	var out []anchorToken
	for _, loc := range pathTokenRe.FindAllStringSubmatchIndex(line, -1) {
		bodyStart, bodyEnd := loc[2], loc[3]
		// 前置边界：前一个**字节**不得落在 [\w./\\-] 内。
		if bodyStart > 0 && isAnchorGlueByte(line[bodyStart-1]) {
			continue
		}
		// 后置边界：token 主体结尾的下一个字符不得是 \w。
		// **注意**：若有 :line 后缀，loc[0..1] 覆盖到行号；边界应看整段匹配之后。
		wholeEnd := loc[1]
		if wholeEnd < len(line) && isWordByte(line[wholeEnd]) {
			continue
		}
		t := anchorToken{raw: line[loc[0]:loc[1]], path: line[bodyStart:bodyEnd]}
		if loc[4] >= 0 {
			n := atoiDigits(line[loc[4]:loc[5]])
			t.line = &n
		}
		out = append(out, t)
	}
	return out
}

// anchorToken 是单条匹配到的 token。
type anchorToken struct {
	raw  string
	path string
	line *int
}

// isAnchorGlueByte 报告字节是否落在 TS lookbehind 的排除类 `[\w./\\-]` 内。
//
// `\w` 在 JS 里是 `[A-Za-z0-9_]`（ASCII，非 Unicode）。
func isAnchorGlueByte(b byte) bool {
	return isWordByte(b) || b == '.' || b == '/' || b == '\\' || b == '-'
}

// isWordByte 报告字节是否为 JS `\w`（ASCII 字母数字下划线）。
func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// atoiDigits 解析纯数字串（无符号）。
func atoiDigits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// hasFileShapedIntermediateSegment 报告路径的非末段里是否含已知扩展名。
//
// 对账 TS `hasFileShapedIntermediateSegment`（plan-fact-anchors.ts:87-90）。
// 已知扩展名形态的中间段（`README.md/…`）说明该 token 是粘连的散文枚举
// （`README.md/README.zh.md/…`），不是一条真实路径——文件不能是目录。
func hasFileShapedIntermediateSegment(path string) bool {
	segments := strings.Split(path, "/")
	if len(segments) <= 1 {
		return false
	}
	for _, s := range segments[:len(segments)-1] {
		if knownExtTailRe.MatchString(s) {
			return true
		}
	}
	return false
}

// isPlaceholderShaped 报告 basename 是否为占位形态。
//
// 对账 TS `isPlaceholderShaped`（plan-fact-anchors.ts:96-99）：去扩展名后
// 小写，命中占位名集合或单字母（`a`–`z`）。
func isPlaceholderShaped(path string) bool {
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		base = path[idx+1:]
	}
	// 去掉扩展名（对账 TS 的 `.replace(/\.[^.]*$/, '')`——最后一个点起）。
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	base = strings.ToLower(base)
	if placeholderBasenames[base] {
		return true
	}
	return len(base) == 1 && base[0] >= 'a' && base[0] <= 'z'
}

// ExtractPlanAnchors 从计划 markdown 提取候选锚点。
//
// 对账 TS `extractPlanAnchors`（plan-fact-anchors.ts:129-163`）。
//
// 围栏块**跳过**，但 shell 类围栏除外（验证命令块引用真实测试路径）；
// mermaid/diff/代码提案围栏满是模块相对或举例路径，只会产生噪声。
func ExtractPlanAnchors(content string) []extractedAnchor {
	anchors := map[string]extractedAnchor{}
	var order []string
	inFence := false
	fenceLang := ""

	for _, line := range strings.Split(content, "\n") {
		if m := fenceLangRe.FindStringSubmatch(line); m != nil {
			inFence = !inFence
			if inFence {
				fenceLang = strings.ToLower(m[1])
			} else {
				fenceLang = ""
			}
			continue
		}
		if inFence && !checkableFenceLangs[fenceLang] {
			continue
		}

		declaredNew := newFileMarkerRe.MatchString(line)
		for _, tok := range findAnchorTokens(line) {
			rawPath := tok.path
			// 模块相对或越界引用是 import 风格，不是项目锚点——解析基准不可知，跳过。
			if strings.HasPrefix(rawPath, "./") || strings.Contains(rawPath, "..") {
				continue
			}
			if strings.Contains(rawPath, "node_modules/") {
				continue
			}
			if hasFileShapedIntermediateSegment(rawPath) {
				continue
			}

			key := rawPath + ":"
			if tok.line != nil {
				key = fmt.Sprintf("%s:%d", rawPath, *tok.line)
			}
			if existing, ok := anchors[key]; ok {
				// 任一处「新增」标记即胜出——计划声明了创建意图。
				if declaredNew && !existing.declaredNew {
					existing.declaredNew = true
					anchors[key] = existing
				}
				continue
			}
			anchors[key] = extractedAnchor{
				raw:               tok.raw,
				path:              rawPath,
				line:              tok.line,
				declaredNew:       declaredNew,
				placeholderShaped: isPlaceholderShaped(rawPath),
			}
			order = append(order, key)
		}
	}

	out := make([]extractedAnchor, 0, len(order))
	for _, k := range order {
		out = append(out, anchors[k])
	}
	return out
}

// isInsideProject 报告路径是否在项目内（与 pathsafe 同款 containment 判定，
// 但不含 grants / 敏感文件逻辑）。
//
// 对账 TS `isInsideProject`（plan-fact-anchors.ts:166-169）。
func isInsideProject(cwd, inputPath string) bool {
	cwdAbs, err1 := filepath.Abs(cwd)
	if err1 != nil {
		return false
	}
	abs, err2 := filepath.Abs(filepath.Join(cwd, inputPath))
	if err2 != nil {
		return false
	}
	rel, err := filepath.Rel(cwdAbs, abs)
	if err != nil {
		return false
	}
	return rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// rerootProbeState 是换根探测会话（一次调用内共享预算与枚举缓存）。
//
// 对账 TS `RerootProbe`（plan-fact-anchors.ts:213-226）。
type rerootProbeState struct {
	cwd      string
	parent   string
	selfName string
	probes   int
	roots    []altRoot
	rootsSet bool
	firstLvl map[string]map[string]bool
}

// altRoot 是替代根。
//
// 对账 TS `AltRoot`（plan-fact-anchors.ts:190-194）。
type altRoot struct {
	label string
	abs   string
}

func createRerootProbeState(cwd string) *rerootProbeState {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	return &rerootProbeState{
		cwd:      abs,
		parent:   filepath.Dir(abs),
		selfName: filepath.Base(abs),
		firstLvl: map[string]map[string]bool{},
	}
}

// probeSpend 扣减预算；预算耗尽返回 false（调用方停止探测，fail-open）。
func (p *rerootProbeState) probeSpend() bool {
	if p.probes >= rerootProbeBudget {
		return false
	}
	p.probes++
	return true
}

// listSubdirs 列出直接子目录（排除点开头与 REROOT_EXCLUDE_DIRS）。
//
// 对账 TS `listSubdirs`（plan-fact-anchors.ts:186-193）。读失败返回空。
func listSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(name, ".") || rerootExcludeDirs[name] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ensureRerootRoots 惰性构建候选根列表。
//
// 对账 TS `ensureRerootRoots`（plan-fact-anchors.ts:238-255`）。
// 返回 nil 表示预算耗尽无法枚举。
func (p *rerootProbeState) ensureRerootRoots() []altRoot {
	if p.rootsSet {
		return p.roots
	}
	if !p.probeSpend() {
		return nil
	}
	subs := listSubdirs(p.cwd)
	if len(subs) > maxRootEnum {
		subs = subs[:maxRootEnum]
	}
	roots := make([]altRoot, 0, len(subs))
	for _, n := range subs {
		roots = append(roots, altRoot{label: n + "/", abs: filepath.Join(p.cwd, n)})
	}
	if p.parent != p.cwd {
		if !p.probeSpend() {
			p.roots = roots
			p.rootsSet = true
			return roots
		}
		siblings := listSubdirs(p.parent)
		filtered := make([]string, 0, len(siblings))
		for _, n := range siblings {
			if n != p.selfName {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > maxRootEnum {
			filtered = filtered[:maxRootEnum]
		}
		for _, n := range filtered {
			roots = append(roots, altRoot{label: "../" + n + "/", abs: filepath.Join(p.parent, n)})
		}
	}
	p.roots = roots
	p.rootsSet = true
	return roots
}

// rootFirstLevel 返回单个候选根的直接子目录集（readdir 一次缓存，不重复扣预算）。
//
// 对账 TS `rootFirstLevel`（plan-fact-anchors.ts:258-266`）。
func (p *rerootProbeState) rootFirstLevel(rootAbs string) map[string]bool {
	if cached, ok := p.firstLvl[rootAbs]; ok {
		return cached
	}
	if !p.probeSpend() {
		return nil
	}
	set := map[string]bool{}
	for _, n := range listSubdirs(rootAbs) {
		set[n] = true
	}
	p.firstLvl[rootAbs] = set
	return set
}

// probeReroot 渐进换根探测：L0 父目录根 → L1 枚举 + 首段过滤 → L2 候选根 stat。
//
// 对账 TS `probeReroot`（plan-fact-anchors.ts:271-293`）。
//
// 命中返回带展示标签的候选根；预算耗尽或全部未命中返回 nil。
// **只 stat/readdir，从不读取文件内容、不做行号校验**（containment 纪律）。
func (p *rerootProbeState) probeReroot(rel string) *altRoot {
	// L0：父目录根（跨项目计划主场景，常数 stat，不枚举任何目录）
	if !p.probeSpend() {
		return nil
	}
	if info, err := os.Stat(filepath.Join(p.parent, rel)); err == nil && info.Mode().IsRegular() {
		return &altRoot{label: "../", abs: p.parent}
	}

	// L1+L2：候选根逐 root 过滤 + 完整探测（预算内，命中即停）
	roots := p.ensureRerootRoots()
	if roots == nil {
		return nil
	}
	segs := strings.Split(rel, "/")
	s1 := segs[0]
	for i := range roots {
		root := &roots[i]
		first := p.rootFirstLevel(root.abs)
		if first == nil {
			return nil // 预算耗尽
		}
		if !first[s1] {
			continue // 首段不匹配（精确过滤，无假阴性）
		}
		if !p.probeSpend() {
			return nil
		}
		if info, err := os.Stat(filepath.Join(root.abs, rel)); err == nil && info.Mode().IsRegular() {
			return root
		}
	}
	return nil
}

// CheckPlanFactAnchors 校验计划锚点与工作树是否一致。
//
// 对账 TS `checkPlanFactAnchors`（plan-fact-anchors.ts:297-375`）。
//
// 返回不一致的漂移项；被跳过（项目外 / 超上限）的锚点**永不**作为漂移上报。
func CheckPlanFactAnchors(content, cwd string) (*PlanAnchorReport, error) {
	all := ExtractPlanAnchors(content)
	if len(all) > maxAnchors {
		all = all[:maxAnchors]
	}

	var drifts []PlanAnchorDrift
	checked := 0
	var pendingReroot []extractedAnchor
	var probe *rerootProbeState

	// 计划里别处声明为新建的路径，在无标记复用时仍豁免存在性校验
	// （如任务列表 + 验证块）。
	declaredNewPaths := map[string]bool{}
	for _, a := range all {
		if a.declaredNew {
			declaredNewPaths[a.path] = true
		}
	}

	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("解析 cwd 失败：%w", err)
	}

	for _, anchor := range all {
		if !isInsideProject(cwdAbs, anchor.path) {
			continue
		}
		checked++

		if anchor.declaredNew || declaredNewPaths[anchor.path] {
			// 计划明确要新建的文件：不再逐条校验父目录是否存在。
			continue
		}

		absolute := filepath.Join(cwdAbs, anchor.path)
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.Mode().IsRegular() {
			pendingReroot = append(pendingReroot, anchor)
			continue
		}

		if anchor.line != nil && info.Size() <= lineCheckMaxBytes {
			text, readErr := os.ReadFile(absolute)
			if readErr == nil {
				lineCount := len(strings.Split(string(text), "\n"))
				if *anchor.line > lineCount {
					n := *anchor.line
					drifts = append(drifts, PlanAnchorDrift{
						Anchor: anchor.raw,
						Path:   anchor.path,
						Line:   &n,
						Kind:   DriftLineOutOfRange,
						Detail: fmt.Sprintf("计划引用 `%s`，但该文件当前只有 %d 行——行号锚点已漂移，重读文件更新引用。", anchor.raw, lineCount),
					})
				}
			}
		}
	}

	// 第二阶段：渐进换根探测（预算封顶、共享枚举、命中即停）。
	if len(pendingReroot) > 0 {
		probe = createRerootProbeState(cwdAbs)
		for _, anchor := range pendingReroot {
			hit := probe.probeReroot(anchor.path)
			if hit != nil {
				drifts = append(drifts, PlanAnchorDrift{
					Anchor: anchor.raw,
					Path:   anchor.path,
					Kind:   DriftRootMismatch,
					Detail: fmt.Sprintf("计划引用 `%s`，相对会话目录不存在，但实际位于 `%s%s`——把引用补全为带根前缀的路径，或确认执行目录后再动手。", anchor.raw, hit.label, anchor.path),
				})
				continue
			}
			// 占位形态（foo/bar/a.py…）且任何根下都不存在（或预算耗尽无法确认）
			// → 视为举例散文，不上报。
			if anchor.placeholderShaped {
				continue
			}
			drifts = append(drifts, PlanAnchorDrift{
				Anchor: anchor.raw,
				Path:   anchor.path,
				Kind:   DriftMissingFile,
				Detail: fmt.Sprintf("计划引用 `%s`，但该文件在当前项目中不存在——用工具核实真实路径，或如果是有意新建请标注「新增」。", anchor.raw),
			})
		}
	}

	rerootProbes := 0
	if probe != nil {
		rerootProbes = probe.probes
	}
	return &PlanAnchorReport{Checked: checked, Drifts: drifts, RerootProbes: rerootProbes}, nil
}

// FormatAnchorDrifts 把漂移报告渲染为 markdown 项目符号行。
//
// 对账 TS `formatAnchorDrifts`（plan-fact-anchors.ts:378-379`）：
// `drifts.map(d => `- ${d.detail}`).join('\n')`。
func FormatAnchorDrifts(drifts []PlanAnchorDrift) string {
	lines := make([]string, len(drifts))
	for i, d := range drifts {
		lines[i] = "- " + d.Detail
	}
	return strings.Join(lines, "\n")
}
