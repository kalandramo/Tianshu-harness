package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// repomap.go —— 精简文件树（标注入口/测试/配置）。
//
// 对账 TS `src/tools/repo-map.ts`。

// repoMapExcludeDirs 是共享基线 + 本工具的额外项。
//
// 对账 TS `EXCLUDE_DIRS`（repo-map.ts:12-15）。**注释逐字对账**：
// 它曾是一份手工维护的副本，丢掉了 `target`——而那是 Rust/Tauri 项目
// 拥有最大树的地方。
var repoMapExcludeDirs = func() map[string]bool {
	m := map[string]bool{}
	for k := range ScanExcludeDirs {
		m[k] = true
	}
	m["coverage"] = true
	m[".turbo"] = true
	m[".cache"] = true
	return m
}()

const (
	repoMapDefaultMaxFiles = 200
	repoMapDefaultDepth    = 4
)

// repoMapEntryFiles 对账 TS `ENTRY_FILES`（repo-map.ts:17-20）。
var repoMapEntryFiles = map[string]bool{
	"main.ts": true, "main.tsx": true, "index.ts": true, "index.tsx": true,
	"app.tsx": true, "server.ts": true, "server.js": true, "main.js": true,
}

// repoMapConfigFiles 对账 TS `CONFIG_FILES`（repo-map.ts:21-25）。
var repoMapConfigFiles = map[string]bool{
	"tsconfig.json": true, "package.json": true, "jsconfig.json": true,
	"vite.config.ts": true, "vite.config.js": true,
	"next.config.js": true, "next.config.ts": true,
	"tailwind.config.ts": true, "tailwind.config.js": true,
}

// isRepoMapTestFile 对账 TS `isTestFile`（repo-map.ts:27-29）。
func isRepoMapTestFile(name string) bool {
	return repoMapTestFileRe.MatchString(name)
}

var repoMapTestFileRe = mustRe(`\.(test|spec)\.(ts|tsx|js|jsx|mjs|cjs)$`)

// isRepoMapConfigFile 对账 TS `isConfigFile`（repo-map.ts:35-40）。
func isRepoMapConfigFile(name string) bool {
	if repoMapConfigFiles[name] {
		return true
	}
	for _, suf := range []string{".config.ts", ".config.js", ".config.mjs", ".config.cjs"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// annotateRepoMapFile 对账 TS `annotateFile`（repo-map.ts:42-48）。
//
// **判定顺序敏感**：入口 > 测试 > 配置 > 文档。
func annotateRepoMapFile(name string) string {
	if repoMapEntryFiles[name] {
		return "入口"
	}
	if isRepoMapTestFile(name) || name == "__tests__" {
		return "测试"
	}
	if isRepoMapConfigFile(name) {
		return "配置"
	}
	if strings.HasSuffix(name, ".md") {
		return "文档"
	}
	return ""
}

// repoMapNode 是树节点（对账 TS `TreeNode`，repo-map.ts:50-56）。
type repoMapNode struct {
	name       string
	isDir      bool
	children   []repoMapNode
	annotation string
	sizeBytes  int64
	hasSize    bool
}

// repoMapCounts 是文件计数（对账 TS 的 `{ n, total }` 对象）。
type repoMapCounts struct {
	n     int
	total int
}

// buildRepoMapTree 递归构建树。
//
// 对账 TS `buildTree`（repo-map.ts:58-110`）。**五个必须复刻的细节**：
//
//  1. `depth > maxDepth` 时返回空（**严格大于**，故 depth=maxDepth 仍会枚举）。
//  2. 点开头目录/文件默认跳过，**但** `.env.example` 与 `.gitignore` 例外。
//  3. `includeSilent=false` 时跳过 silent 项；目录额外跳过 `tier == L0_build`。
//  4. 排序：目录优先，同类按名字**localeCompare**（Go 用字节序，见偏差说明）。
//  5. 文件计数：`total++` 在 `n >= maxFiles` 检查**之前**——故 total 反映
//     真实总数，n 封顶在 maxFiles。
func buildRepoMapTree(dir string, depth int, counts *repoMapCounts, maxFiles, maxDepth int, projectRoot string, includeSilent bool) []repoMapNode {
	if depth > maxDepth {
		return nil
	}
	names, err := readDirNames(dir)
	if err != nil {
		return nil
	}

	type entry struct {
		name      string
		isDir     bool
		sizeBytes int64
	}
	var entries []entry

	for _, name := range names {
		fullPath := filepath.Join(dir, name)
		relPath := relPosix(projectRoot, fullPath)
		verdict := ClassifyPath(relPath)

		if !includeSilent && strings.HasPrefix(name, ".") && name != ".env.example" && name != ".gitignore" {
			continue
		}
		if !includeSilent && verdict.Silent {
			continue
		}

		info, statErr := os.Stat(fullPath)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			if repoMapExcludeDirs[name] {
				continue
			}
			if !includeSilent && verdict.Tier == TierL0Build {
				continue
			}
			entries = append(entries, entry{name: name, isDir: true})
		} else if info.Mode().IsRegular() {
			entries = append(entries, entry{name: name, isDir: false, sizeBytes: info.Size()})
		}
	}

	// 排序：目录优先，同类按名字。
	//
	// **必须复刻 JS 的 `localeCompare`**——它不是字节序。实测差异（同一组名）：
	//
	//	localeCompare: __tests__ .env.example .gitignore .hidden .rivet a.ts
	//	               agent docs m.ts package.json README.md src ...
	//	字节序:        .env.example .gitignore .hidden .rivet README.md
	//	               __tests__ a.ts agent docs m.ts package.json src ...
	//
	// 两处关键差异：
	//  1. `__tests__` 在 localeCompare 下排**最前**（`_` 的排序权重低于 `.`）；
	//  2. `README.md` 在 localeCompare 下排在 `package.json` **之后**
	//     （大小写不敏感：`r` > `p`），字节序则因 `R`(0x52) < `p`(0x70) 排前。
	//
	// 实现：**主键小写**（大小写不敏感），**次键原串**（稳定区分同小写不同大小写），
	// 并对 `_` 做权重调整使其低于 `.`。这覆盖了实测的全部差异点。
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return localeCompareLess(entries[i].name, entries[j].name)
	})

	var nodes []repoMapNode
	for _, e := range entries {
		if e.isDir {
			children := buildRepoMapTree(filepath.Join(dir, e.name), depth+1, counts, maxFiles, maxDepth, projectRoot, includeSilent)
			if len(children) > 0 {
				ann := ""
				if e.name == "__tests__" {
					ann = "测试"
				}
				nodes = append(nodes, repoMapNode{name: e.name, isDir: true, children: children, annotation: ann})
			}
		} else {
			counts.total++
			if counts.n >= maxFiles {
				continue
			}
			counts.n++
			nodes = append(nodes, repoMapNode{
				name:       e.name,
				isDir:      false,
				annotation: annotateRepoMapFile(e.name),
				sizeBytes:  e.sizeBytes,
				hasSize:    true,
			})
		}
	}
	return nodes
}

// formatRepoMapSize 对账 TS `formatSize`（repo-map.ts:112-117）。
func formatRepoMapSize(bytes int64, hasSize bool) string {
	if !hasSize {
		return ""
	}
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%dKB", bytes/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

// formatRepoMapTree 渲染树（对账 TS `formatTree`，repo-map.ts:119-136）。
func formatRepoMapTree(nodes []repoMapNode, prefix string) []string {
	var lines []string
	for i, node := range nodes {
		last := i == len(nodes)-1
		connector := "├── "
		if last {
			connector = "└── "
		}
		annotation := ""
		if node.annotation != "" {
			annotation = " [" + node.annotation + "]"
		}
		size := ""
		if !node.isDir {
			if s := formatRepoMapSize(node.sizeBytes, node.hasSize); s != "" {
				size = " " + s
			}
		}
		lines = append(lines, prefix+connector+node.name+annotation+size)

		if node.isDir && len(node.children) > 0 {
			childPrefix := prefix + "│   "
			if last {
				childPrefix = prefix + "    "
			}
			lines = append(lines, formatRepoMapTree(node.children, childPrefix)...)
		}
	}
	return lines
}

// repoMapTool 是 repo_map 工具实现。
type repoMapTool struct {
	def     contract.Definition
	enabled bool
}

// RepoMap 构造 repo_map 工具。
func RepoMap() Tool {
	t := &repoMapTool{enabled: true}
	t.def = contract.Definition{
		Name: "repo_map",
		Description: `返回精简文件树，标注入口文件、测试文件和配置文件。

看文件树用 repo_map；看结构关系（imports/calls、爆炸半径）用 repo_graph。

先浅后深：repo_map({ depth: 2 })，再下钻到具体区域：repo_map({ path: "src/agent/" })。`,
		InputSchema: objSchemaOrdered([]string{"max_files", "path", "depth"}, map[string]any{
			"max_files": numProp("最多包含的文件数（默认：200）"),
			"path":      strProp("要聚焦的子目录（相对项目根目录）。默认：项目根目录。"),
			"depth":     numProp("最大目录深度（默认：4）。浅层概览用 2。"),
		}),
	}
	return t
}

func (t *repoMapTool) Definition() contract.Definition { return t.def }
func (t *repoMapTool) Enabled() bool                   { return t.enabled }
func (t *repoMapTool) ConcurrencySafe() bool           { return true }
func (t *repoMapTool) RequiresApproval(*CallParams) bool {
	return false
}
func (t *repoMapTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *repoMapTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	// **对账 TS 的 `|| DEFAULT` falsy 语义**——显式传 0 也回退默认。
	maxFiles := intArgFalsy(p.Input, "max_files", repoMapDefaultMaxFiles)
	// **对账 TS 的 `?? DEFAULT` nullish 语义**——只有缺失才回退（0 保留）。
	maxDepth := intArg(p.Input, "depth", repoMapDefaultDepth)
	subPath, _ := p.Input["path"].(string)

	// **统一用绝对路径**：cwd 与 root 都必须是绝对，否则
	// `filepath.Rel(相对cwd, 绝对root)` 会失败并把 header 退化成绝对路径。
	cwdAbs := mustAbs(p.Cwd)
	root := cwdAbs

	if subPath != "" {
		root = mustAbs(filepath.Join(cwdAbs, subPath))
		// 安全：解析后必须仍在 cwd 内（尾部分隔符防前缀注入）。
		//
		// **与 TS 的差异（本刀修正的 TS 缺陷）**：TS 写的是
		// `const safeCwd = resolve(params.cwd) + '/'`——在 Windows 上
		// `resolve` 返回**反斜杠**路径，拼上 `'/'` 得到混合分隔符
		// （`...\repomap/`），而 `root` 是纯反斜杠 → `startsWith` **恒为 false**
		// → `repo_map({path: ...})` 在 Windows 上**完全不可用**（实测确认）。
		//
		// 作者在 macOS/Linux 开发（那里 `resolve` 返回 `/`，`+ '/'` 恰好正确），
		// 故该缺陷从未暴露。Go 侧用 `filepath.Separator` 平台无关地表达
		// 「路径必须在 cwd 子树内」这一**意图**。已在 oracle 登记为已知偏离。
		safeCwd := strings.TrimRight(cwdAbs, "/\\") + string(filepath.Separator)
		if !strings.HasPrefix(root, safeCwd) && root != cwdAbs {
			return contract.Result{Content: "错误：path 必须位于项目目录内", IsError: true}, nil
		}
	}

	info, err := os.Stat(root)
	if err != nil {
		return contract.Result{
			Content: fmt.Sprintf("错误：目录不存在：%s", root),
			IsError: true,
		}, nil
	}
	if !info.IsDir() {
		return contract.Result{Content: fmt.Sprintf("错误：不是目录：%s", root), IsError: true}, nil
	}

	includeSilent := false
	if subPath != "" {
		includeSilent = ClassifyPath(relPosix(cwdAbs, root)).Silent
	}

	counts := &repoMapCounts{}
	tree := buildRepoMapTree(root, 0, counts, maxFiles, maxDepth, cwdAbs, includeSilent)

	displayRoot := filepath.Base(root) + "/"
	if subPath != "" {
		displayRoot = filepath.Base(cwdAbs) + "/" + relPosix(cwdAbs, root) + "/"
	}

	lines := formatRepoMapTree(tree, "")
	dirCount := countRepoMapDirs(tree)

	omitted := 0
	if counts.total > maxFiles {
		omitted = counts.total - maxFiles
	}
	truncated := ""
	if omitted > 0 {
		truncated = fmt.Sprintf("\n...（已截断：省略 %d 个文件；可用 repo_map({path: \"...\"}) 或 glob/grep 做定向查看）", omitted)
	}
	summary := fmt.Sprintf("树中 %d 个文件，%d 个目录", counts.n, dirCount)

	return contract.Result{
		Content: displayRoot + "\n" + strings.Join(lines, "\n") + truncated + "\n" + summary,
	}, nil
}

// countRepoMapDirs 递归统计目录数（对账 TS `countDirs`，repo-map.ts:167-174）。
func countRepoMapDirs(nodes []repoMapNode) int {
	n := 0
	for _, node := range nodes {
		if node.isDir {
			n++
			n += countRepoMapDirs(node.children)
		}
	}
	return n
}

// localeCompareLess 近似 JS 的 `a.localeCompare(b)` 默认行为。
//
// **不是完整实现**——`localeCompare` 依赖 ICU 排序规则，完整复刻需引入
// 区域数据。此处实现**实测差异点**所需的部分：
//
//   - 大小写不敏感（主键小写）：`README.md` > `package.json`
//   - `_` 权重低于 `.`：`__tests__` 排最前
//   - 同小写时按原串（稳定，不依赖比较器的调用顺序）
//
// **已知未覆盖**：非 ASCII 名（中文/重音字母）的排序权重与 ICU 不同。
// 若未来有此类需求，应引入 `golang.org/x/text/collate`。
func localeCompareLess(a, b string) bool {
	ka := localeSortKey(a)
	kb := localeSortKey(b)
	if ka != kb {
		return ka < kb
	}
	return a < b
}

// localeSortKey 生成近似 localeCompare 的排序键。
//
// 把 `_` 映射到 `.` 之前的字节（`\x01`），其余小写化。
// 这样 `__tests__` → `\x01\x01tests\x01\x01` 排在 `.env.example`（`\x2e...`）之前，
// 与实测的 localeCompare 输出一致。
func localeSortKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_':
			b.WriteByte(0x01)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// mustAbs 返回绝对路径（失败返回原值）。
func mustAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// readDirNames 读目录条目名（失败返回 error）。
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// relPosix 返回 POSIX 风格的相对路径（对账 TS `relativePosix`）。
//
// 对账 `path-format.ts:9-11`：`toPosixPath(relative(from, to))`。
//
// **必须复刻的关键语义**：Node 的 `path.relative(x, x)` 返回 **`”`（空串）**，
// 而 Go 的 `filepath.Rel(x, x)` 返回 **`'.'`**。这个差异会在
// `file_info` 的「根目录」用例上暴露（TS 输出 `Path: `，Go 输出 `Path: .`）。
// 故此处把 `'.'` 归一为空串。
//
// **与 `relLabel` 的差异**：`relLabel` 在越界时回退绝对路径，而
// `relativePosix` 直接返回 `relative` 的结果（可能含 `..`）。
func relPosix(from, to string) string {
	rel, err := filepath.Rel(from, to)
	if err != nil {
		return filepath.ToSlash(to)
	}
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// intArgFalsy 对账 TS 的 `(params.input.X as number) || DEFAULT` 语义。
//
// **关键差异**：TS 的 `||` 是 **falsy** 语义——显式传 `0` 也回退默认值。
// 既有的 `intArg(input, key, def)` 是「键存在即返回」（0 会被返回），
// **不等价**。故此处单独实现，避免误用既有 helper 造成语义漂移。
func intArgFalsy(input map[string]any, key string, def int) int {
	v, ok := input[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		if x == 0 {
			return def
		}
		return x
	case int64:
		if x == 0 {
			return def
		}
		return int(x)
	case float64:
		if x == 0 {
			return def
		}
		return int(x)
	default:
		return def
	}
}
