package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// inspectproject.go —— 分析项目并返回摘要。
//
// 对账 TS `src/tools/inspect-project.ts`。

// inspectPkgJSON 是 package.json 的相关字段。
type inspectPkgJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// inspectFrameworkHint 对账 TS `FRAMEWORK_HINTS`（inspect-project.ts:26-36）。
type inspectFrameworkHint struct {
	deps []string
	name string
}

var inspectFrameworkHints = []inspectFrameworkHint{
	{[]string{"next"}, "Next.js"},
	{[]string{"nuxt"}, "Nuxt"},
	{[]string{"@nestjs/core"}, "NestJS"},
	{[]string{"vue"}, "Vue"},
	{[]string{"react"}, "React"},
	{[]string{"express"}, "Express"},
	{[]string{"fastify"}, "Fastify"},
	{[]string{"hono"}, "Hono"},
	{[]string{"svelte"}, "Svelte"},
}

// inspectTestFrameworks 对账 TS `TEST_FRAMEWORKS`（inspect-project.ts:38-42）。
var inspectTestFrameworks = []inspectFrameworkHint{
	{[]string{"vitest"}, "vitest"},
	{[]string{"jest"}, "jest"},
	{[]string{"mocha"}, "mocha"},
}

// inspectLinter 对账 TS `LINTERS`（inspect-project.ts:44-48）。
type inspectLinter struct {
	files []string
	deps  []string
	name  string
}

var inspectLinters = []inspectLinter{
	{[]string{".eslintrc", ".eslintrc.js", ".eslintrc.json", ".eslintrc.yml", "eslint.config.js", "eslint.config.mjs"}, []string{"eslint"}, "ESLint"},
	{[]string{".prettierrc", ".prettierrc.js", ".prettierrc.json", "prettier.config.js"}, []string{"prettier"}, "Prettier"},
	{[]string{"biome.json"}, []string{"@biomejs/biome"}, "Biome"},
}

// 入口文件探测常量（对账 TS inspect-project.ts:50-52）。
var (
	inspectEntryNames = []string{"main", "index", "app", "server", "cli"}
	inspectEntryExts  = []string{".ts", ".tsx", ".js", ".jsx", ".mjs"}
	inspectEntryDirs  = []string{"src", "lib", "app", "bin", ""}
)

// inspectConfigPatterns 对账 TS `CONFIG_FILE_PATTERNS`（inspect-project.ts:54-69）。
var inspectConfigPatterns = []string{
	"DESIGN.md",
	"tsconfig.json", "tsconfig.*.json",
	"vite.config.ts", "vite.config.js", "vite.config.mjs",
	"next.config.ts", "next.config.js", "next.config.mjs",
	"tailwind.config.ts", "tailwind.config.js", "tailwind.config.mjs",
	"postcss.config.ts", "postcss.config.js", "postcss.config.mjs",
	"tsup.config.ts", "tsup.config.js",
	"webpack.config.ts", "webpack.config.js",
	"rollup.config.ts", "rollup.config.js",
	"esbuild.config.ts", "esbuild.config.js",
	"jest.config.ts", "jest.config.js",
	"vitest.config.ts", "vitest.config.js",
}

// inspectMaxTestFiles 对账 TS `MAX_TEST_FILES`（inspect-project.ts:139）。
const inspectMaxTestFiles = 50

// fileExistsQuick 报告路径是否存在（对账 TS `fileExists`，inspect-project.ts:71-73）。
func fileExistsQuick(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// detectProjectLanguage 对账 TS `detectLanguage`（inspect-project.ts:75-77）。
func detectProjectLanguage(cwd string) string {
	if fileExistsQuick(filepath.Join(cwd, "tsconfig.json")) {
		return "TypeScript"
	}
	return "JavaScript"
}

// detectPackageManager 对账 TS `detectPackageManager`（inspect-project.ts:79-84）。
//
// **顺序敏感**：pnpm > yarn > npm > 未知。
func detectPackageManager(cwd string) string {
	if fileExistsQuick(filepath.Join(cwd, "pnpm-lock.yaml")) {
		return "pnpm"
	}
	if fileExistsQuick(filepath.Join(cwd, "yarn.lock")) {
		return "yarn"
	}
	if fileExistsQuick(filepath.Join(cwd, "package-lock.json")) {
		return "npm"
	}
	return "未知"
}

// allPkgDeps 合并 dependencies 与 devDependencies。
//
// 对账 TS `{ ...pkg.dependencies, ...pkg.devDependencies }`。
func allPkgDeps(pkg inspectPkgJSON) map[string]bool {
	out := map[string]bool{}
	for k := range pkg.Dependencies {
		out[k] = true
	}
	for k := range pkg.DevDependencies {
		out[k] = true
	}
	return out
}

// detectFramework 对账 TS `detectFramework`（inspect-project.ts:86-92）。
func detectFramework(pkg inspectPkgJSON) string {
	deps := allPkgDeps(pkg)
	for _, hint := range inspectFrameworkHints {
		for _, d := range hint.deps {
			if deps[d] {
				return hint.name
			}
		}
	}
	return ""
}

// detectTestFramework 对账 TS `detectTestFramework`（inspect-project.ts:94-104）。
func detectTestFramework(pkg inspectPkgJSON) string {
	deps := allPkgDeps(pkg)
	for _, hint := range inspectTestFrameworks {
		for _, d := range hint.deps {
			if deps[d] {
				return hint.name
			}
		}
	}
	testScript := pkg.Scripts["test"]
	if strings.Contains(testScript, "node:test") || strings.Contains(testScript, "--test") {
		return "node:test"
	}
	return ""
}

// detectLinters 对账 TS `detectLinters`（inspect-project.ts:106-117）。
func detectLinters(cwd string, pkg inspectPkgJSON) []string {
	deps := allPkgDeps(pkg)
	var found []string
	for _, l := range inspectLinters {
		hasFile := false
		for _, f := range l.files {
			if fileExistsQuick(filepath.Join(cwd, f)) {
				hasFile = true
				break
			}
		}
		hasDep := false
		for _, d := range l.deps {
			if deps[d] {
				hasDep = true
				break
			}
		}
		if hasFile || hasDep {
			found = append(found, l.name)
		}
	}
	return found
}

// findEntryFiles 对账 TS `findEntryFiles`（inspect-project.ts:119-136）。
//
// **顺序敏感**：外层 dir、中层 name、内层 ext——故输出顺序由三者决定。
func findEntryFiles(cwd string) []string {
	var entries []string
	for _, dir := range inspectEntryDirs {
		base := cwd
		if dir != "" {
			base = filepath.Join(cwd, dir)
			if !fileExistsQuick(base) {
				continue
			}
		}
		for _, name := range inspectEntryNames {
			for _, ext := range inspectEntryExts {
				full := filepath.Join(base, name+ext)
				if fileExistsQuick(full) {
					entries = append(entries, relPosix(cwd, full))
				}
			}
		}
	}
	return entries
}

// findTestFiles 对账 TS `findTestFiles`（inspect-project.ts:141-171）。
//
// **遍历顺序**：`readdir` 返回的顺序（Go 的 `os.ReadDir` 已按名字排序；
// TS 的 `readdir` 在多数平台返回目录序——**这是已知差异**，见文件尾说明）。
func findTestFiles(cwd string) []string {
	var files []string

	var walk func(dir string)
	walk = func(dir string) {
		if len(files) >= inspectMaxTestFiles {
			return
		}
		names, err := readDirNames(dir)
		if err != nil {
			return
		}
		for _, name := range names {
			fullPath := filepath.Join(dir, name)
			info, statErr := os.Stat(fullPath)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				if shouldSkipBroadDiscoveryDir(cwd, fullPath, name) {
					continue
				}
				walk(fullPath)
			} else if info.Mode().IsRegular() {
				if len(files) >= inspectMaxTestFiles {
					return
				}
				if strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") || name == "__tests__" {
					files = append(files, relPosix(cwd, fullPath))
				}
			}
		}
	}

	walk(cwd)
	return files
}

// shouldSkipBroadDiscoveryDir 对账 TS `shouldSkipBroadDiscoveryDir`
// （inspect-project.ts:19-22）。
func shouldSkipBroadDiscoveryDir(cwd, fullPath, name string) bool {
	if ScanExcludeDirs[name] {
		return true
	}
	return ClassifyPath(relPosix(cwd, fullPath)).Silent
}

// findConfigFiles 对账 TS `findConfigFiles`（inspect-project.ts:173-196）。
func findConfigFiles(cwd string) []string {
	var found []string
	for _, pattern := range inspectConfigPatterns {
		if strings.Contains(pattern, "*") {
			names, err := readDirNames(cwd)
			if err != nil {
				continue
			}
			// 对账 TS：`pattern.replace(/\*/g, '[^.]*')` 后做 `^...$` 全匹配。
			regexStr := strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, `[^.]*`)
			re, err := regexp.Compile("^" + regexStr + "$")
			if err != nil {
				continue
			}
			// **顺序**：TS 按 `readdir` 顺序 push；Go 的 ReadDir 已排序。
			for _, name := range names {
				if re.MatchString(name) {
					found = append(found, name)
				}
			}
		} else {
			if fileExistsQuick(filepath.Join(cwd, pattern)) {
				found = append(found, pattern)
			}
		}
	}
	return found
}

// inspectProjectTool 是 inspect_project 工具实现。
type inspectProjectTool struct {
	def     contract.Definition
	enabled bool
}

// InspectProject 构造 inspect_project 工具。
func InspectProject() Tool {
	t := &inspectProjectTool{enabled: true}
	t.def = contract.Definition{
		Name: "inspect_project",
		Description: `分析当前项目并返回摘要：语言、包管理器、scripts、入口文件、测试结构与框架线索。

### 用法
- 首次进入一个项目时用 inspect_project 了解其结构
- 无需参数——作用于当前工作目录
- 返回结构化摘要，便于规划第一次编辑

### 示例
推荐：inspect_project() — 获取项目概览`,
		InputSchema: objSchemaOrdered(nil, map[string]any{}),
	}
	return t
}

func (t *inspectProjectTool) Definition() contract.Definition { return t.def }
func (t *inspectProjectTool) Enabled() bool                   { return t.enabled }
func (t *inspectProjectTool) ConcurrencySafe() bool           { return true }
func (t *inspectProjectTool) RequiresApproval(*CallParams) bool {
	return false
}
func (t *inspectProjectTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *inspectProjectTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	cwd := p.Cwd

	pkgPath := filepath.Join(cwd, "package.json")
	if !fileExistsQuick(pkgPath) {
		return contract.Result{
			Content: "当前目录未找到 package.json。不是 Node.js 项目。",
			IsError: true,
		}, nil
	}

	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		return contract.Result{Content: "解析 package.json 失败。", IsError: true}, nil
	}
	var pkg inspectPkgJSON
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return contract.Result{Content: "解析 package.json 失败。", IsError: true}, nil
	}

	language := detectProjectLanguage(cwd)
	packageManager := detectPackageManager(cwd)
	linters := detectLinters(cwd, pkg)
	entryFiles := findEntryFiles(cwd)
	testFiles := findTestFiles(cwd)
	configFiles := findConfigFiles(cwd)
	framework := detectFramework(pkg)
	testFramework := detectTestFramework(pkg)

	var lines []string
	lines = append(lines, "## 项目摘要", "")
	lines = append(lines, "语言："+language)
	lines = append(lines, "包管理器："+packageManager)
	if framework != "" {
		lines = append(lines, "框架："+framework+"（从依赖检测）")
	}
	if testFramework != "" {
		lines = append(lines, "测试框架："+testFramework)
	}
	if len(linters) > 0 {
		lines = append(lines, "Linter："+strings.Join(linters, ", "))
	}

	// 关键脚本（对账 TS `keyScripts`，顺序固定）。
	keyScripts := []string{"build", "test", "lint", "dev", "start", "typecheck"}
	var relevantScripts []string
	for _, s := range keyScripts {
		if _, ok := pkg.Scripts[s]; ok {
			relevantScripts = append(relevantScripts, s)
		}
	}
	if len(relevantScripts) > 0 {
		lines = append(lines, "", "### 脚本")
		for _, s := range relevantScripts {
			lines = append(lines, "- "+s+": "+pkg.Scripts[s])
		}
	}

	if len(entryFiles) > 0 {
		lines = append(lines, "", "### 入口文件")
		for _, f := range entryFiles {
			lines = append(lines, "- "+f)
		}
	}
	if len(testFiles) > 0 {
		lines = append(lines, "", "### 测试结构")
		for _, f := range testFiles {
			lines = append(lines, "- "+f)
		}
	}
	if len(configFiles) > 0 {
		lines = append(lines, "", "### 配置文件")
		for _, f := range configFiles {
			lines = append(lines, "- "+f)
		}
	}

	return contract.Result{Content: strings.Join(lines, "\n")}, nil
}

// inspectSortedNames 是测试辅助：把 map 键排序返回（避免 Go map 遍历随机）。
//
// **保留说明**：TS 的对象键顺序在 JS 引擎里是插入序（字符串键），
// 但本工具不遍历依赖 map 输出，故无需复刻。此函数供测试用。
func inspectSortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprintf // 保留 fmt 引用（诊断路径可能用到）
