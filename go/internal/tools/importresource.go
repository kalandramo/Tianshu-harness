package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// importresource.go —— `import_resource` 工具（第一百刀 · Wave 1 纯函数部分）。
//
// 对账 TS `src/tools/import-resource.ts`（418 行）。
//
// # 工具做什么
//
// 把**工作区外**的资源导入项目内 `.rivet/external/`，供 `read_file`/`grep`/`glob`
// 正常访问。三类来源：本地路径（文件/目录）、GitHub 仓库、http(s) URL。
//
// # 为什么需要（它不是可选功能）
//
// `read_file`/`grep` 受 `pathsafe` 约束只能读工作区内。模型想看用户桌面的
// 设计稿、`/tmp` 的日志、别人的 GitHub 仓库时，没有入口——只能让用户手动
// 复制。本工具补上这条通道，且**全程过敏感门**（对账 TS issue #135：
// 本地导入曾是不经敏感检测的读路径，可被诱导把 `~/.ssh/id_rsa` 读入上下文）。
//
// # 门链已在等它（本刀不是「造功能」）
//
// `go/internal/net/fetchcore.go` 在遇到二进制内容时，错误文案明写
// 「请用 import_resource」——`go/internal/net/fetchcore_test.go:253` 断言了这一点。
// 该断言在本刀之前就存在，即**门链早已为这个未移植的工具预留了位置**。
//
// # ★ Go 侧对 TS 的一处修正：导入物**复制**而非符号链接
//
// TS 用 `symlink(resolved, targetPath, 'file')` 把外部资源「放进」工作区
// （省磁盘）。但 `pathsafe.Validate`（TS 的 `path-validate.ts:50`）会
// `realpath` 解析符号链接 → 得到**工作区外**的真实路径 → **拒绝访问**。
//
// **实测**（本刀验收）：导入后用 `read_file` 读 `.rivet/external/note-xxx.md`
// 报「Path outside project directory」——即摘要里
// 「该资源现可通过项目内路径访问……请使用 read_file、grep、glob 配合此路径」
// 这句承诺**在 TS 侧是假的**（核实：TS 既无 grantPath 也无豁免名单）。
//
// **Go 侧改为复制**：产物是工作区内的**普通文件**，读得到。
// 代价（用户已知情选择）：磁盘占用 + 源更新不同步。
//
// **不这么做的话，本工具的存在意义为零**——「导入」的全部价值就是「导入后能用」。
//
// # 本文件范围（Wave 1）
//
// 只含**纯函数**（无 IO）：`parseGitHubURL` / `subpathEscapesContainer` /
// `isSafeGitRef` / `importTargetName` / `simpleHash`。
// 工具本体与 IO 分支在后续 Wave。

// importDirRel 对账 TS `IMPORT_DIR`（`import-resource.ts:23`）。
//
// 导入物落到 `<cwd>/.rivet/external/`。
//
// **为什么在 tools 包内而非 rivetpath**：TS 侧它是本文件的私有常量，
// 且带 `cwd` 语义（项目级，非用户级）——`rivetpath` 只管用户级 `RIVET_HOME`。
const importDirRel = ".rivet/external"

// previewBytes 对账 TS `PREVIEW_BYTES`（`:24`）——预览截断长度。
//
// **口径**：TS 用 `content.length`，即 **UTF-16 code unit**。
// Go 侧必须走 `UTF16Len`/`jsSliceHead`（`truncation.go:135,150`），
// 不能直接用 `len()`——否则中文下会虚假提示「已截断」（第八十八刀的同型教训）。
const previewBytes = 4000

// githubRef 对账 TS `parseGitHubUrl` 的返回形状。
type githubRef struct {
	Owner   string
	Repo    string
	Ref     string // 可选：tree/blob 段
	Subpath string // 可选：ref 之后的路径
}

// githubURLRe 对账 TS 的正则（`import-resource.ts:56`）：
//
//	/^github\.com\/([^/]+)\/([^/]+?)(?:\/(tree|blob)\/([^/]+)(?:\/(.*))?)?$/
//
// **Go 的差异**：TS 的 `([^/]+?)` 是非贪婪——在 `$` 锚定下与贪婪等价
// （都已排除 `/`），故 Go 用 `[^/]+` 是行为等价的。
//
// `(tree|blob)` 段只用于**识别形态**，其值本身不参与结果（TS 的解构里
// 第 4 项被跳过：`const [, owner, repo, , ref, subpath] = match`）。
var githubURLRe = regexp.MustCompile(`^github\.com/([^/]+)/([^/]+?)(?:/(?:tree|blob)/([^/]+)(?:/(.*))?)?$`)

// parseGitHubURL 解析 GitHub URL 为 owner/repo/ref/subpath。
//
// 对账 TS `parseGitHubUrl`（`:54-62`）：
//
//	const cleaned = url.replace(/^https?:\/\//, '').replace(/\.git$/, '')
//	const match = cleaned.match(/^github\.com\/([^/]+)\/([^/]+?)(?:\/(tree|blob)\/([^/]+)(?:\/(.*))?)?$/)
//	if (!match) return null
//	…
//	return { owner, repo, ref: ref ?? undefined, subpath: subpath || undefined }
//
// 第二个返回值等价于 TS 的 `null`。
//
// **注意 `.git` 后缀只在末尾剥一次**（`ReplaceAll` 会误伤 `x.git/…`，
// 故用一次前缀式剥离）。
func parseGitHubURL(raw string) (*githubRef, bool) {
	cleaned := raw
	// 对账 TS：`replace(/^https?:\/\//, '')` —— **只剥开头一次**
	if m := httpSchemePrefixRe.FindStringIndex(cleaned); m != nil && m[0] == 0 {
		cleaned = cleaned[m[1]:]
	}
	// 对账 TS：`replace(/\.git$/, '')` —— **只剥末尾一次**
	cleaned = strings.TrimSuffix(cleaned, ".git")

	m := githubURLRe.FindStringSubmatch(cleaned)
	if m == nil {
		return nil, false
	}
	owner, repo := m[1], m[2]
	if owner == "" || repo == "" {
		return nil, false
	}
	// 对账 TS：`ref ?? undefined` / `subpath || undefined`——
	// 空串视为「无」（TS 的 `||` 把 '' 归为 falsy）
	return &githubRef{
		Owner:   owner,
		Repo:    repo,
		Ref:     m[3],
		Subpath: m[4],
	}, true
}

// httpSchemePrefixRe 对账 TS `/^https?:\/\//`（大小写敏感，与 TS 字面量一致）。
var httpSchemePrefixRe = regexp.MustCompile(`^https?://`)

// subpathEscapesContainer 判定 subpath 是否逃出仓库容器。
//
// 对账 TS `subpathEscapesContainer`（`:67-79`）+ `isInsideContainer`（`:81-85`）。
//
// # issue #119：为什么需要两层
//
// subpath 由**模型或 URL 构造**，拼接后必须仍在仓库容器内，否则
// `blob/main/../../../../etc/passwd` 会越过 `.rivet/external/` 读到工作区外
// 任意文件（审批 UI 只显示原始 URL，越界难以察觉）。
//
//   - **词法层**：拦 `..` 穿越（纯字符串 resolve 即可）
//   - **真实层**：clone 下来的仓库**可自带 symlink** 指向容器外——纯字符串
//     resolve 拦不住，必须 realpath 解析后比对
//
// # 空 subpath 快速放行
//
// 对账 TS：`if (subpath.length === 0) return false`——没有子路径就无从逃逸。
func subpathEscapesContainer(container, subpath string) bool {
	if len(subpath) == 0 {
		return false
	}
	root := resolveAbsPath(container)
	lexical := resolveAbsPath(filepath.Join(root, subpath))
	if !isInsideContainer(root, lexical) {
		return true
	}
	// 真实层：两侧都 realpath 后再比。
	// **任一侧不存在则返回 false**（对账 TS 的 catch 分支）：路径尚未 clone 或
	// 子路径缺失时，词法结论已足够，上层会另行报「未找到子路径」。
	realRoot, err1 := filepath.EvalSymlinks(root)
	realLexical, err2 := filepath.EvalSymlinks(lexical)
	if err1 != nil || err2 != nil {
		return false
	}
	return !isInsideContainer(realRoot, realLexical)
}

// isInsideContainer 对账 TS `isInsideContainer`（`:81-85`）：
//
//	if (child === root) return true
//	const prefix = root.endsWith(sep) ? root : root + sep
//	return child.startsWith(prefix)
//
// **前缀必须带分隔符**——否则 `/a/bc` 会被误判为在 `/a/b` 之内。
func isInsideContainer(root, child string) bool {
	if child == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(child, prefix)
}

// unsafeGitRefRe 对账 TS `/[\s\x00-\x1f\x7f~^:?*[\\]/`。
//
// TS 字符类里的 `[` 不需转义（类内不构成嵌套）；Go 的 RE2 同样如此。
var unsafeGitRefRe = regexp.MustCompile(`[\s\x00-\x1f\x7f~^:?*\[\\]`)

// isSafeGitRef 校验 git ref（branch/tag/commit）是否安全。
//
// 对账 TS `isSafeGitRef`（`:92-97`）：
//
//	if (ref.length === 0 || ref.length > 255) return false
//	if (ref.startsWith('-')) return false
//	if (/[\s\x00-\x1f\x7f~^:?*[\\]/.test(ref)) return false
//	return true
//
// # 防的是什么：git 选项注入
//
// ref 经 `execFile` 传参（**不经 shell**），故真正的风险不是命令注入，而是
// **git 选项注入**——`--upload-pack=…` 这样的 ref 会被 git 当成选项解析。
// 故拒绝 `-` 开头。
//
// **长度用 UTF-16 口径**（对账 TS 的 `ref.length`）。
func isSafeGitRef(ref string) bool {
	n := UTF16Len(ref)
	if n == 0 || n > 255 {
		return false
	}
	if strings.HasPrefix(ref, "-") {
		return false
	}
	if unsafeGitRefRe.MatchString(ref) {
		return false
	}
	// **额外防御（Go 侧新增）**：控制字符类已由上面的正则覆盖，
	// 但无效 UTF-8 字节序列不含任何可匹配的 rune——TS 的字符串恒为
	// UTF-16，故此情形在 TS 侧不存在。Go 侧显式拒绝，保持 fail-closed。
	if !utf8.ValidString(ref) {
		return false
	}
	return true
}

// simpleHash 对账 TS `simpleHash`（`:103-107`）：
//
//	let h = 0
//	for (let i = 0; i < str.length; i++) h = ((h << 5) - h + str.charCodeAt(i)) | 0
//	return Math.abs(h)
//
// **`charCodeAt` 是 UTF-16 code unit**——故 Go 侧须按 UTF-16 编码遍历，
// 而非按 rune（BMP 外字符会给出不同的 code unit 序列）。
//
// **`| 0` 是 32 位有符号截断**——Go 侧用 int32 回环。
func simpleHash(s string) int32 {
	var h int32
	for _, u := range utf16Encode(s) {
		// 对账 TS：`((h << 5) - h + code) | 0`
		// `<<` 与加法在 Go 的 int32 上同样回环（Go 的有符号整数溢出是定义良好的）
		h = (h<<5 - h + int32(u))
	}
	if h < 0 {
		return -h
	}
	return h
}

// importTargetName 对账 TS `importTargetName`（`:109-116`）。
//
// 生成保留扩展名的目标名：`name-hash.ext`。
//
//	raw  = basename(source).replace(/[^a-zA-Z0-9._-]/g, '_').slice(0, 40)
//	ext  = extname(raw)
//	base = ext ? raw.slice(0, -ext.length) : raw
//	hash = simpleHash(source).toString(36)
//	return ext ? `${base}-${hash}${ext}` : `${raw}-${hash}`
//
// **注意**：`slice(0, 40)` 是 UTF-16 code unit 截断（对账 TS）。
// **注意**：`toString(36)` 是**无符号**视角——TS 的 `Math.abs` 已保证非负，
// 但 Go 的 int32 转 36 进制须按无符号处理（否则负数会出 `-`）。
func importTargetName(source string) string {
	raw := sanitizeImportName(filepath.Base(source))
	raw = jsSliceHead(raw, 40)
	ext := filepath.Ext(raw)
	base := strings.TrimSuffix(raw, ext)
	hash := base36(uint32(simpleHash(source)))
	if ext != "" {
		return base + "-" + hash + ext
	}
	return raw + "-" + hash
}

// unsafeNameRe 对账 TS `/[^a-zA-Z0-9._-]/g`。
var unsafeNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeImportName 对账 TS 的 `replace(/[^a-zA-Z0-9._-]/g, '_')`。
func sanitizeImportName(name string) string {
	return unsafeNameRe.ReplaceAllString(name, "_")
}

// base36 对账 JS 的 `Number.toString(36)`（限于 32 位无符号域）。
//
// JS 的 `toString(36)` 用 0-9a-z。
func base36(n uint32) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [7]byte // uint32 在 36 进制下最多 7 位（36^7 > 2^32）
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%36]
		n /= 36
	}
	return string(buf[i:])
}

// utf16Encode 对账 JS 的 UTF-16 code unit 序列。
//
// `unicode/utf16` 的 `Encode` 对 BMP 外字符产出代理对——与 JS 一致。
// 已在 truncation.go 用过同款（`jsSliceHead`）。
func utf16Encode(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		if r <= 0xFFFF {
			out = append(out, uint16(r))
			continue
		}
		r -= 0x10000
		out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
	}
	return out
}

// resolveAbsPath 对账 Node 的 `path.resolve`（单参数形态）。
//
// `filepath.Abs` 在解析失败时会返回 err；此处退化返回入参（fail-open 到
// 词法层——后续的容器包含性判断仍会拦住明显越界）。
func resolveAbsPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// ensureImportDir 确保 `<cwd>/.rivet/external/` 存在并返回其路径。
//
// 对账 TS `ensureImportDir`（`:99-103`）：
//
//	const dir = join(cwd, IMPORT_DIR)
//	await mkdir(dir, { recursive: true })
//	return dir
//
// **调用时机对账 TS**：`execute` 里在**参数校验之后**、**分支判断之前**调用
// ——即无论走哪个分支都会先建目录（即使该分支随后失败）。
func ensureImportDir(cwd string) (string, error) {
	dir := filepath.Join(cwd, importDirRel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
