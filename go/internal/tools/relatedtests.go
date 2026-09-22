// related_tests 工具：查找与给定源文件相关的测试文件。
//
// 对账 TS 的 `RELATED_TESTS_TOOL`（src/tools/related-tests.ts:222），即
// `createRelatedTestsTool(() => null)` 这个**静态变体**——无 Meridian 索引器，
// 永远走硬编码路径启发式。
//
// ## 为什么不移植 Meridian 分支
//
// TS 的 `createRelatedTestsTool(getIndexer)` 优先用 Meridian SQL 的真实 import
// 图（`db.getTestsFor` + `analyzeImpact`），启发式只是兜底。Go 侧**没有
// Meridian**（全仓 grep 零命中），故 Go 版对应的是 TS 的静态变体——这不是
// 收窄，而是 Go 侧唯一存在的语义。
//
// ## 必须手工复刻的 Node 语义（探针实测，见 testdata/relatedtests/probe*.ts）
//
// **`path.dirname` 保留输入分隔符**（纯字符串操作，不归一化）：
//
//	dirname('src/tools/bash.ts') = 'src/tools'   ← 正斜杠保留
//	dirname('src\\tools\\bash.ts') = 'src\\tools' ← 反斜杠保留
//
// 而 **`path.join` 归一化到平台分隔符**（Windows 上反斜杠）：
//
//	join('src/tools', '__tests__') = 'src\\tools\\__tests__'
//
// 二者的**不对称**正是 TS 的一个平台缺陷：`dir.startsWith('src/')` 只在输入
// 用正斜杠时成立。oracle 的 `win-backslash-*` 用例锁定了这一点
// （`src\tools\bash.ts` 少返回一个结果）。Go 侧若直接用 `filepath.Dir` 会
// 归一化分隔符 → 该分支行为改变 → 对账失败。故此处**手工实现 dirname**，
// 逐字节复刻 Node 的保留语义。
//
// ## 不去重（TS 原样）
//
// `flat.py` 的 dir/parentDir/relDir 都是 `.`，4 个候选撞同一路径，
// TS 返回**重复 4 次**（oracle `py-flat-top` 锁定）。不「优化」成去重。
package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// relatedTestsTool 实现 related_tests。
type relatedTestsTool struct {
	cwd string
}

// RelatedTests 创建 related_tests 工具。
func RelatedTests(cwd string) Tool { return &relatedTestsTool{cwd: cwd} }

func (t *relatedTestsTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "related_tests",
		Description: `查找与给定源文件相关的测试文件。

### 用法
- 编辑源文件后，用它确定需要跑哪些测试
- 编辑前，用它了解该文件已有哪些测试
- 返回匹配的测试文件路径列表

### 示例
Good: related_tests(file="src/tools/bash.ts") — 找 bash 工具的测试
Good: related_tests(file="src/api/client.ts") — 找 API client 的测试`,
		InputSchema: objSchemaOrdered([]string{"file"}, map[string]any{
			"file": strProp("相对 cwd 的源文件路径"),
		}, "file"),
	}
}

func (t *relatedTestsTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	file, _ := p.Input["file"].(string)

	// 绝对路径（含 Windows `C:\`）与 `..` 一律拒绝——只允许 cwd 内的相对路径。
	// 对账 TS：`isAbsolute(file) || file.includes('..')`。
	// 注意 TS 的 `includes('..')` 是**子串**判定——`a..b.ts` 也命中（oracle
	// `err-dotdot-mid` 锁定）。
	if isAbsPath(file) || strings.Contains(file, "..") {
		return contract.Result{
			Content: "错误：file 路径必须相对于项目目录。",
			IsError: true,
		}, nil
	}

	if isTestFile(file) {
		sources := t.findSourceForTest(file)
		if len(sources) == 0 {
			return contract.Result{Content: "未找到相关源文件。"}, nil
		}
		return contract.Result{Content: strings.Join(sources, "\n")}, nil
	}

	tests := t.findTestsForSource(file)
	if len(tests) == 0 {
		return contract.Result{Content: "未找到相关测试。"}, nil
	}
	return contract.Result{Content: strings.Join(tests, "\n")}, nil
}

func (t *relatedTestsTool) RequiresApproval(p *CallParams) bool { return false }
func (t *relatedTestsTool) ConcurrencySafe() bool               { return true }
func (t *relatedTestsTool) Enabled() bool                       { return true }
func (t *relatedTestsTool) Timeout(p *CallParams) time.Duration { return 0 }

// ── Node path 语义的手工复刻 ──

// isAbsPath 复刻 Node 的 `path.isAbsolute`（win32 语义）。
//
// 探针实测：`C:\x\y.ts` 与 `C:/x/y.ts` 均为 true。Go 的 `filepath.IsAbs`
// 在 Windows 上对两者也为 true，但**在非 Windows 上对 `C:\` 为 false**——
// 为跨平台一致，此处显式判定盘符前缀。
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}
	// 盘符：X: 或 X:/
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// jsDirname 复刻 Node 的 `path.dirname`——**保留输入的分隔符**。
//
// 与 Go 的 `filepath.Dir` 的关键差异：后者会 Clean 归一化（`src/tools` →
// `src\tools`），使 TS 的 `startsWith('src/')` 判定行为改变。
//
// Node 的规则（简化到本工具用到的范围）：
//   - 去掉末尾分隔符后，找**最后一个**分隔符，返回其之前的部分
//   - 无分隔符 → `"."`
//   - 结果为空 → `"."`
//
// 分隔符集合：`/` 与 `\`（Node 在 win32 上两者都认，且**保留原字符**）。
func jsDirname(p string) string {
	// 先剥掉末尾分隔符（Node：`a/b/` → dirname 为 `a`）
	end := len(p)
	for end > 1 && isSep(p[end-1]) {
		end--
	}
	p = p[:end]
	if p == "" {
		return "."
	}
	// 从右往左找分隔符
	for i := len(p) - 1; i >= 0; i-- {
		if isSep(p[i]) {
			if i == 0 {
				// 根路径：Node 返回 "/"
				return p[:1]
			}
			return p[:i]
		}
	}
	return "."
}

func isSep(c byte) bool { return c == '/' || c == '\\' }

// jsJoin 复刻 Node 的 `path.join`——**归一化到平台分隔符**（Windows 反斜杠）。
//
// 探针实测：`join('src/tools', '__tests__')` = `'src\\tools\\__tests__'`。
// 与 `jsDirname` 的不对称正是本工具对账的难点。
//
// 归一化规则：所有分隔符统一为平台分隔符；`.` 段被消除（`join('.', 'x')`
// = `'x'`，探针实测）。
func jsJoin(parts ...string) string {
	var segs []string
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		for _, seg := range splitSep(part) {
			if seg == "" || seg == "." {
				continue
			}
			segs = append(segs, seg)
		}
	}
	return strings.Join(segs, string(os.PathSeparator))
}

// splitSep 按 `/` 或 `\` 切分。
func splitSep(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' })
}

// jsExtname 复刻 Node 的 `path.extname`——返回**最后一个** `.` 起（含）的后缀。
//
// 探针实测：`bash.test.ts` → `.ts`（不是 `.test.ts`）。
func jsExtname(p string) string {
	// 只看 basename 部分
	base := p
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		base = p[i+1:]
	}
	idx := strings.LastIndex(base, ".")
	if idx <= 0 { // idx==0 表示以 `.` 开头的隐藏文件 → 无扩展名
		return ""
	}
	return base[idx:]
}

// jsBasename 复刻 Node 的 `path.basename(p, ext)`。
func jsBasename(p, ext string) string {
	base := p
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		base = p[i+1:]
	}
	if ext != "" && strings.HasSuffix(base, ext) {
		base = base[:len(base)-len(ext)]
	}
	return base
}

// ── TS 逻辑的直译 ──

// isTestFile 对账 TS 的 `isTestFile`。
func isTestFile(filePath string) bool {
	base := jsBasename(filePath, "")
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	// Python 约定：test_<name>.py / <name>_test.py
	if strings.HasSuffix(base, ".py") &&
		(strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")) {
		return true
	}
	return false
}

// pythonTestCandidates 对账 TS 的 `pythonTestCandidates`。
func pythonTestCandidates(file string) []string {
	baseName := jsBasename(file, ".py")
	dir := jsDirname(file)
	parentDir := jsDirname(dir)
	relDir := dir
	if strings.HasPrefix(dir, "src/") {
		relDir = dir[4:]
	}

	return []string{
		// Co-located
		jsJoin(dir, "test_"+baseName+".py"),
		jsJoin(dir, baseName+"_test.py"),
		// Sibling tests/ dir
		jsJoin(dir, "tests", "test_"+baseName+".py"),
		jsJoin(parentDir, "tests", "test_"+baseName+".py"),
		// Top-level tests/ flat
		jsJoin("tests", "test_"+baseName+".py"),
		// Top-level tests/ mirroring the source path
		jsJoin("tests", relDir, "test_"+baseName+".py"),
	}
}

// findTestsForSource 对账 TS 的 `findTestsForSource`。
func (t *relatedTestsTool) findTestsForSource(file string) []string {
	ext := jsExtname(file)
	baseName := jsBasename(file, ext)
	dir := jsDirname(file)
	parentDir := jsDirname(dir)

	relDir := dir
	if strings.HasPrefix(dir, "src/") {
		relDir = dir[4:]
	}

	if ext == ".py" {
		cands := pythonTestCandidates(file)
		return t.filterExistingSorted(cands)
	}

	candidates := []string{
		// __tests__ dir colocated
		jsJoin(dir, "__tests__", baseName+".test.ts"),
		jsJoin(dir, "__tests__", baseName+".spec.ts"),
		// Parent __tests__ dir
		jsJoin(parentDir, "__tests__", baseName+".test.ts"),
		jsJoin(parentDir, "__tests__", baseName+".spec.ts"),
		// Co-located test file
		jsJoin(dir, baseName+".test.ts"),
		jsJoin(dir, baseName+".spec.ts"),
		// Top-level __tests__ mirroring src path
		jsJoin("__tests__", relDir, baseName+".test.ts"),
		jsJoin("__tests__", relDir, baseName+".spec.ts"),
		// Top-level tests dir mirroring src path
		jsJoin("tests", relDir, baseName+".test.ts"),
		jsJoin("tests", relDir, baseName+".spec.ts"),
	}
	return t.filterExistingSorted(candidates)
}

// findSourceForTest 对账 TS 的 `findSourceForTest`。
func (t *relatedTestsTool) findSourceForTest(file string) []string {
	ext := jsExtname(file)
	baseNameRaw := jsBasename(file, ext)
	var baseName string
	if ext == ".py" {
		baseName = strings.TrimSuffix(strings.TrimPrefix(baseNameRaw, "test_"), "_test") + ext
	} else {
		// 去掉 .test / .spec 后缀（注意：baseName 已是去 ext 的）
		baseName = strings.TrimSuffix(strings.TrimSuffix(baseNameRaw, ".test"), ".spec") + ext
	}
	dir := jsDirname(file)

	var sourceDirs []string

	// __tests__/foo.test.ts -> same parent dir
	if jsBasename(dir, "") == "__tests__" {
		sourceDirs = append(sourceDirs, jsDirname(dir))
	}

	// Python: <pkg>/tests/test_foo.py -> <pkg>/foo.py; tests/test_foo.py -> ./foo.py
	if ext == ".py" && jsBasename(dir, "") == "tests" {
		sourceDirs = append(sourceDirs, jsDirname(dir))
	}
	if ext == ".py" && strings.HasPrefix(dir, "tests/") {
		sourceDirs = append(sourceDirs, dir[6:])
	}

	// tests/tools/foo.test.ts -> src/tools/
	if strings.HasPrefix(dir, "tests/") {
		sourceDirs = append(sourceDirs, jsJoin("src", dir[6:]))
	}

	// __tests__/tools/foo.test.ts -> src/tools/
	if strings.HasPrefix(dir, "__tests__/") {
		sourceDirs = append(sourceDirs, jsJoin("src", dir[10:]))
	}

	// Co-located: same dir
	sourceDirs = append(sourceDirs, dir)

	candidates := make([]string, 0, len(sourceDirs))
	for _, d := range sourceDirs {
		candidates = append(candidates, jsJoin(d, baseName))
	}
	return t.filterExistingSorted(candidates)
}

// filterExistingSorted 过滤存在的路径并排序（对账 TS 的 filter+map+sort）。
//
// 排序用**字节序**（Go 的 sort.Strings 是字节序，与 JS 的 UTF-16 code unit
// 序在 BMP 内一致——探针实测 JS `sort()` 把 `_`(0x5F) 排在字母前，
// `A.ts` 在 `a.ts` 前，均为 code unit 序）。
//
// **不去重**——TS 原样（oracle `py-flat-top` 锁定 4 次重复）。
func (t *relatedTestsTool) filterExistingSorted(candidates []string) []string {
	var out []string
	for _, c := range candidates {
		abs := t.cwd + string(os.PathSeparator) + filepathFromSlash(c)
		if _, err := os.Stat(abs); err == nil {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// filepathFromSlash 把 jsJoin 产出的平台分隔符路径转成可直接 Stat 的形式。
// Windows 上 jsJoin 已用 `\`，直接可用；此处保留函数以明示意图。
func filepathFromSlash(p string) string { return p }

var _ = fmt.Sprintf
