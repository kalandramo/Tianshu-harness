package tools

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
)

// importresource_run.go —— `import_resource` 工具本体（第一百刀 · W3）。
//
// 对账 TS `src/tools/import-resource.ts`（418 行）的 `execute` 与三个 handle 分支。
//
// # 三分支（对账 TS `execute`）
//
//  1. `parseGitHubUrl` 命中 → GitHub clone
//  2. `^https?://` → HTTP 下载
//  3. 否则 → 本地路径导入
//
// **`ensureImportDir` 的时机**（对账 TS）：在参数校验**之后**、
// 分支判断**之前**——即无论走哪个分支都会先建目录（即使该分支随后失败）。

// importResource 创建 `import_resource` 工具。
func ImportResource(cwd string) Tool { return &importResourceTool{cwd: cwd} }

// ImportResourceWithDeps 创建带**注入依赖**的 import_resource（测试用）。
//
// **为什么需要注入 fetch**：对账 TS 的 `setHttpFetchForTests`（`:16`）——
// 测试不许打真实网络。
func ImportResourceWithDeps(cwd string, fetch importFetchFn) Tool {
	return &importResourceTool{cwd: cwd, fetch: fetch}
}

// importFetchFn 对账 TS 的 `HttpFetchFn`（`:12`）——可注入的 http 抓取。
type importFetchFn func(ctx context.Context, rawURL string) (*tnet.Result, error)

type importResourceTool struct {
	cwd   string
	fetch importFetchFn
}

func (t *importResourceTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "import_resource",
		Description: "把外部资源导入项目工作区，供其他工具访问。" +
			"\n\n支持：" +
			"\n- 本地文件路径（绝对路径）：/tmp/design.png, ~/Desktop/spec.pdf" +
			"\n- 本地目录：/path/to/external/project" +
			"\n- GitHub 仓库：github.com/user/repo, https://github.com/user/repo" +
			"\n- HTTP/HTTPS URL：下载对应内容" +
			"\n\n导入后，资源位于项目内的 .rivet/external/ 路径下。" +
			"\n其他工具（read_file、grep、glob）即可正常访问。" +
			"\n因会访问项目外部资源，需要审批。",
		InputSchema: objSchemaOrdered(
			[]string{"source", "ref"},
			map[string]any{
				"source": strProp("要导入的资源。可以是本地绝对路径、GitHub URL 或 HTTP/HTTPS URL。"),
				"ref":    strProp("GitHub 仓库专用：要检出的 branch、tag 或 commit。默认使用默认分支。"),
			},
			"source",
		),
	}
}

// RequiresApproval 对账 TS `requiresApproval: () => true`。
func (t *importResourceTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 对账 TS `isConcurrencySafe: () => false`。
//
// **恒 false 的理由**：多个导入可能写同一目标名（同名不同源 → hash 不同，
// 但同一 source 并发导入会竞争同一路径）。
func (t *importResourceTool) ConcurrencySafe() bool { return false }

// Enabled 对账 TS `isEnabled: () => true`。
func (t *importResourceTool) Enabled() bool { return true }

// Timeout 用默认（0 = 未声明）。**对账 TS**：`import-resource.ts` 未声明 `timeoutMs`
// → 走 `DEFAULT_TOOL_TIMEOUT_MS`（120s）。
//
// **诚实标注**：GitHub clone 与文档抽取可能超过 120s——TS 同样如此（未声明即用默认）。
func (t *importResourceTool) Timeout(_ *CallParams) time.Duration { return 0 }

func (t *importResourceTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rawSource, _ := p.Input["source"].(string)
	rawSource = strings.TrimSpace(rawSource)
	if rawSource == "" {
		return contract.Result{Content: "错误：source 为必填项", IsError: true}, nil
	}

	cwd := t.cwd
	if cwd == "" {
		cwd = "."
	}

	// 对账 TS：**在分支判断前**建目录（无论走哪条分支）
	importDir, err := ensureImportDir(cwd)
	if err != nil {
		return contract.Result{
			Content: "错误：无法创建导入目录：" + err.Error(),
			IsError: true,
		}, nil
	}

	if gh, ok := parseGitHubURL(rawSource); ok {
		ref, _ := p.Input["ref"].(string)
		return t.handleGitHubImport(cwd, importDir, gh, strings.TrimSpace(ref)), nil
	}

	if httpSchemeRe.MatchString(rawSource) {
		return t.handleURLImport(ctx, cwd, importDir, rawSource, p.ArtifactStore), nil
	}

	return t.handleLocalImport(cwd, importDir, rawSource, p.ArtifactStore), nil
}

// httpSchemeRe 对账 TS `execute` 里的 `/^https?:\/\//i`（**大小写不敏感**）。
//
// **与 `parseGitHubURL` 内的前缀正则不同**：那个是大小写敏感的
// （对账 TS 的 `/^https?:\/\//`），此处是 `i` 标志。
var httpSchemeRe = regexp.MustCompile(`(?i)^https?://`)

// importURLFetchTimeoutMs 是 URL 分支的抓取超时（对账 TS `timeoutMs: 60_000`）。
//
// **为什么显式常量**：`HTTPFetchGuarded` 的默认是 **15s**（`httpfetch.go:56`）——
// 漏传 `Options` 会让 15~60 秒的下载在 Go 侧超时而 TS 侧成功（提交后审查发现）。
const importURLFetchTimeoutMs = 60_000

// copyNote 是导入方式说明（进模型上下文）。
//
// **为什么是复制而非符号链接**（Go 侧修正，见本文件头的「与 TS 的差异」）：
// 符号链接虽然省磁盘，但 `pathsafe.Validate` 会 `EvalSymlinks` 解析到源路径
// （工作区外）→ `read_file`/`grep` 读它时被判越界拒绝 → 摘要里
// 「该资源现可通过项目内路径访问」的承诺**是假的**。
// 复制则让产物**真的**是工作区内的普通文件。
const copyNote = "（已复制到工作区内——可被 read_file、grep、glob 直接读取）"

// ── 分支 3：本地路径 ────────────────────────────────────────────────────

// handleLocalImport 对账 TS `handleLocalImport`（`:224-262`）。
//
// **敏感门是硬拒**（对账 TS issue #135）：本地导入曾是不经敏感检测的读路径——
// LLM 诱导下可把 `~/.ssh/id_rsa`、`.env`、`~/.aws/credentials` 等
// symlink/复制进 `.rivet/external` 并读入上下文。fail-closed：拒绝并给出原因。
func (t *importResourceTool) handleLocalImport(
	cwd, importDir, source string, store *artifact.Store,
) contract.Result {
	expanded := expandHome(source)
	resolved := resolveAbsPath(expanded)

	// ── 敏感门（在 lstat **之前**，对账 TS 顺序）──
	if sf := DetectSensitiveFile(resolved); sf.Sensitive {
		return contract.Result{
			Content:   "错误：拒绝导入敏感文件（" + sf.PatternName + "）：" + resolved,
			IsError:   true,
			UIContent: "已拦截敏感文件：" + resolved + "（" + sf.PatternName + "）",
		}
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return contract.Result{
			Content:   "错误：路径不存在：" + resolved,
			IsError:   true,
			UIContent: "未找到：" + resolved,
		}
	}

	targetName := importTargetName(resolved)
	targetPath := filepath.Join(importDir, targetName)

	// 对账 TS：先删旧目标（`rm(..., {recursive, force})`）——已存在不报错
	_ = os.RemoveAll(targetPath)

	if info.IsDir() {
		// **复制而非符号链接**（Go 侧修正，见下方 grantImported 的说明）。
		if err := copyDir(resolved, targetPath); err != nil {
			return contract.Result{
				Content: "错误：导入目录失败：" + err.Error(), IsError: true,
			}
		}
		n := countFiles(resolved, 3)
		r := buildImportResult(source, targetPath, cwd,
			importStats{Type: "directory", Files: &n}, store)
		return finalizeImport(r, copyNote)
	}

	// 文件：**复制**（不走 symlink——理由见 grantImported 的注释）
	if err := copyFile(resolved, targetPath); err != nil {
		return contract.Result{
			Content: "错误：导入文件失败：" + err.Error(), IsError: true,
		}
	}
	size := int64(0)
	if st, err := os.Stat(resolved); err == nil {
		size = st.Size()
	}
	return buildImportResult(source, targetPath, cwd, importStats{Type: "file", Size: &size}, store)
}

// ── 分支 1：GitHub ──────────────────────────────────────────────────────

// handleGitHubImport 对账 TS `handleGitHubImport`（`:264-357`）。
//
// # 有意降级（诚实披露）
//
// TS 走 `cloneWithFallback`（镜像回退：gitcode/kkgithub/fastgit）。
// **Go 侧不移植镜像表**——`mirrors` 子系统整体未移植
// （`bash.go:159-160` 已披露），单独为一个工具建它是造无消费者的子系统。
// 故此处**只直连 `github.com`**，clone 失败即返回失败（不试镜像）。
func (t *importResourceTool) handleGitHubImport(
	cwd, importDir string, gh *githubRef, explicitRef string,
) contract.Result {
	ref := explicitRef
	if ref == "" {
		ref = gh.Ref
	}
	// ref 校验（对账 TS：`ref !== undefined && !isSafeGitRef(ref)`）
	if ref != "" && !isSafeGitRef(ref) {
		return contract.Result{
			Content: "错误：无效的 git ref \"" + ref + "\"。branch/tag/commit 不得以 \"-\" 开头，" +
				"也不得包含空白或控制字符。",
			IsError:   true,
			UIContent: "无效 ref：" + ref,
		}
	}

	repoURL := "https://github.com/" + gh.Owner + "/" + gh.Repo + ".git"
	targetName := importTargetName(gh.Owner + "/" + gh.Repo)
	targetPath := filepath.Join(importDir, targetName)

	gitCmd := ResolveGitCommand(ResolveGitCommandDeps{})

	if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
		// 已 clone → `git pull --ff-only`（失败静默，对账 TS 的 catch）
		_ = runGit(gitCmd, []string{"pull", "--ff-only"}, targetPath, 30_000)
	} else {
		_ = os.RemoveAll(targetPath)
		args := []string{"clone", "--depth", "1"}
		if ref != "" {
			args = append(args, "--branch", ref)
		}
		// 对账 TS：`args.push('--', url, targetPath)` —— `--` 分隔防路径被当选项
		args = append(args, "--", repoURL, targetPath)
		if err := runGit(gitCmd, args, "", 120_000); err != nil {
			if isExecNotFound(err) {
				// 对账 TS：`errorKind: 'missing_dep'`（与 webfetch.go:179 同款赋法）
				kind := "missing_dep"
				return contract.Result{
					Content: "错误：未安装 git 或不在 PATH 中——无法 clone " + repoURL +
						"。请安装 git 后重试。",
					IsError:   true,
					UIContent: "未找到 git",
					ErrorKind: &kind,
				}
			}
			return contract.Result{
				Content:   "clone " + repoURL + " 时出错：" + err.Error(),
				IsError:   true,
				UIContent: "克隆失败：" + gh.Owner + "/" + gh.Repo,
			}
		}
	}

	// 浅 clone 后显式 checkout（对账 TS：`--` 消歧）
	// 对账 TS：`if (ref && existsSync(join(targetPath, '.git')))`——缺守卫时
	// 对一个非 git 目录跑 checkout 会产出噪声错误。
	if ref != "" {
		if _, err := os.Stat(filepath.Join(targetPath, ".git")); err == nil {
			_ = runGit(gitCmd, []string{"checkout", ref, "--"}, targetPath, 10_000)
		}
	}

	effectivePath := targetPath
	if gh.Subpath != "" {
		effectivePath = filepath.Join(targetPath, gh.Subpath)
		// **issue #119**：子路径必须仍在仓库容器内
		if subpathEscapesContainer(targetPath, gh.Subpath) {
			return contract.Result{
				Content:   "错误：子路径 '" + gh.Subpath + "' 越出仓库目录，已拒绝。",
				IsError:   true,
				UIContent: "非法子路径：越出仓库目录",
			}
		}
		if _, err := os.Stat(effectivePath); err != nil {
			return contract.Result{
				Content: "错误：在 " + gh.Owner + "/" + gh.Repo + " 中未找到子路径 '" + gh.Subpath + "'",
				IsError: true, UIContent: "未找到子路径：" + gh.Subpath,
			}
		}
	}

	// 对账 TS：**恒传 `files`**，且从无 `size` 字段——
	// `{ type: ls?.isFile() ? 'file' : 'directory', files: await countFiles(targetPath, 3) }`
	// 故 file 情形也输出「文件数：约 N」。此前 Go 侧覆盖成 `{file, size}` 丢了 files。
	files := countFiles(targetPath, 3)
	st := importStats{Type: "directory", Files: &files}
	if info, err := os.Lstat(effectivePath); err == nil && !info.IsDir() {
		st.Type = "file"
	}
	display := "github.com/" + gh.Owner + "/" + gh.Repo
	if gh.Subpath != "" {
		display += "/" + gh.Subpath
	}
	return buildImportResult(display, effectivePath, cwd, st, nil)
}

// timeMillis 把毫秒转 time.Duration（便于对内联字面量保持 TS 同款书写）。
func timeMillis(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

// runGit 执行 git 命令（cwd 为空则用进程当前目录）。
func runGit(gitCmd string, args []string, cwd string, timeoutMs int) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeMillis(timeoutMs))
	defer cancel()
	cmd := exec.CommandContext(ctx, gitCmd, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// isExecNotFound 判定「可执行文件缺失」。
func isExecNotFound(err error) bool {
	if err == nil {
		return false
	}
	return err == exec.ErrNotFound || os.IsNotExist(err) ||
		strings.Contains(err.Error(), "executable file not found") ||
		strings.Contains(err.Error(), "ENOENT")
}

// ── 分支 2：HTTP URL ────────────────────────────────────────────────────

// handleURLImport 对账 TS `handleUrlImport`（`:359-400`）。
func (t *importResourceTool) handleURLImport(
	ctx context.Context, cwd, importDir, rawURL string, store *artifact.Store,
) contract.Result {
	// 对账 TS：`basename(parsed.pathname) || 'downloaded-content'`——
	// JS 的 `basename('/')` 返回 `'/'`（**truthy**，故会被保留）。
	// Go 的 `filepath.Base("/")` 也返回 `"/"`，但**只有空串与 `.` 才算 falsy**。
	filename := "downloaded-content"
	if u, err := url.Parse(rawURL); err == nil {
		if b := filepath.Base(u.Path); b != "" && b != "." {
			filename = b
		}
	}
	targetName := importTargetName(rawURL) + "_" + sanitizeImportName(filename)
	targetPath := filepath.Join(importDir, targetName)

	fetch := t.fetch
	if fetch == nil {
		opts := importURLFetchOptions()
		fetch = func(ctx context.Context, u string) (*tnet.Result, error) {
			return tnet.HTTPFetchGuarded(ctx, u, tnet.Deps{}, opts)
		}
	}
	res, err := fetch(ctx, rawURL)
	if err != nil {
		return contract.Result{
			Content: "下载 " + rawURL + " 时出错：" + err.Error(),
			IsError: true, UIContent: "下载失败：" + rawURL,
		}
	}
	if res.Status >= 400 {
		return contract.Result{
			Content:   fmt.Sprintf("下载 %s 时出错：HTTP %d", rawURL, res.Status),
			IsError:   true,
			UIContent: "下载失败：" + rawURL,
		}
	}
	if len(res.Bytes) == 0 {
		return contract.Result{
			Content: "错误：从 " + rawURL + " 下载得到空文件",
			IsError: true, UIContent: "空下载：" + rawURL,
		}
	}
	if err := os.WriteFile(targetPath, res.Bytes, 0o644); err != nil {
		return contract.Result{
			Content: "错误：写入失败：" + err.Error(), IsError: true,
		}
	}
	n := int64(len(res.Bytes))
	return buildImportResult(rawURL, targetPath, cwd,
		importStats{Type: "file", Size: &n}, store)
}

// ── 结果组装 ────────────────────────────────────────────────────────────

// importStats 对账 TS `buildResult` 的 `stats` 参数。
//
// **为什么用指针**：TS 的判定是 `stats.size !== undefined`（**字段有无**），
// 不是「值非零」——故 `Size *int64` / `Files *int` 才能精确对账：
// 一个**真的为 0 字节**的文件也该输出「大小：0.0 KB」。
type importStats struct {
	Type  string // "file" | "directory"
	Size  *int64
	Files *int
}

// importResult 对账 TS `buildResult` 的返回形状。
type importResult struct {
	Content   string
	UIContent string
}

// buildImportResult 对账 TS `buildResult`（`:120-175`）。
func buildImportResult(
	source, localPath, cwd string, stats importStats, store *artifact.Store,
) contract.Result {
	relPath := relPosix(cwd, localPath)

	header := "已导入：" + source + "\n本地路径：" + relPath + "\n类型：" + stats.Type
	// 对账 TS：`if (stats.size !== undefined)` —— **字段有无**判定，非「值非零」
	if stats.Size != nil {
		header += fmt.Sprintf("\n大小：%.1f KB", float64(*stats.Size)/1024)
	}
	if stats.Files != nil {
		header += fmt.Sprintf("\n文件数：约 %d", *stats.Files)
	}

	var preview string
	switch {
	case stats.Type == "file" && isTextFileByExt(localPath):
		preview = textFilePreview(localPath)
	case stats.Type == "file" && isExtractableDocument(localPath):
		preview = docExtractPreview(localPath, source, store)
	case stats.Type == "file" && isImageFileByExt(localPath):
		preview = "\n\n（图片文件——已导入但无法以文本查看。可用 file_info 查看元数据。）"
	}

	return contract.Result{
		Content: header + preview + "\n\n该资源现可通过项目内路径访问：" + relPath +
			"\n请使用 read_file、grep、glob 配合此路径。",
		UIContent: header + preview,
	}
}

// textFilePreview 对账 TS 的文本预览分支（含 `PREVIEW_BYTES` 截断）。
//
// **截断口径**：TS 用 `content.length` / `content.slice(0, PREVIEW_BYTES)`
// ——**UTF-16 code unit**。Go 侧走 `UTF16Len`/`jsSliceHead`，避免中文下虚假截断提示。
func textFilePreview(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "" // 对账 TS：`catch { /* binary / unreadable */ }`
	}
	content := string(data)
	n := UTF16Len(content)
	if n > previewBytes {
		head := jsSliceHead(content, previewBytes)
		return fmt.Sprintf("\n\n── 预览（前 %d 字符）──\n%s\n...（共 %d 字符）", previewBytes, head, n)
	}
	return "\n\n── 内容 ──\n" + content
}

// docExtractPreview 对账 TS 的文档抽取预览分支（`:139-164`）。
//
// **artifact 接线**：抽取文本落 artifact，注记形如
// `\n完整抽取文本：read_section(artifactId="…")\n[artifact:…]`。
// artifact 持久化失败**静默忽略**（对账 TS：`catch { /* best-effort */ }`）。
func docExtractPreview(path, source string, store *artifact.Store) string {
	extraction := extractDocumentText(path, docExtractDeps{})
	if !extraction.OK {
		return "\n\n（已导入二进制文档。" + extraction.Suggestion + "）"
	}
	marked := extractionCaveat + "\n\n" + extraction.Text

	artifactNote := ""
	if store != nil {
		id, err := store.Save(artifact.SaveInput{
			Tool:       "import_resource",
			Target:     source,
			RawContent: marked,
			Summary: fmt.Sprintf("从 %s 抽取文本（%s）— %d 字符",
				filepath.Base(path), extraction.Engine, UTF16Len(extraction.Text)),
			Sections: []artifact.ArtifactSection{},
		})
		if err == nil {
			artifactNote = "\n完整抽取文本：read_section(artifactId=\"" + id + "\")\n[artifact:" + id + "]"
		}
	}

	n := UTF16Len(extraction.Text)
	body := extraction.Text
	if n > previewBytes {
		body = jsSliceHead(extraction.Text, previewBytes) +
			fmt.Sprintf("\n...（共 %d 字符）", n)
	}
	return "\n\n── 抽取文本（引擎：" + string(extraction.Engine) + "）──\n" +
		extractionCaveat + "\n" + body + artifactNote
}

// finalizeImport 给导入结果追加一条说明（目录链接方式）。
func finalizeImport(r contract.Result, note string) contract.Result {
	r.Content += "\n\n" + note
	r.UIContent += "\n\n" + note
	return r
}

// ── 文件分类辅助（对账 TS 的集合）───────────────────────────────────────

// textExtensions 对账 TS `TEXT_EXTENSIONS`（`:27-45`）。
var textExtensions = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".json": true, ".jsonl": true, ".json5": true,
	".md": true, ".mdx": true, ".txt": true, ".rst": true, ".adoc": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true,
	".py": true, ".rb": true, ".go": true, ".rs": true, ".java": true, ".kt": true,
	".scala": true, ".c": true, ".cpp": true, ".h": true, ".hpp": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".css": true, ".scss": true, ".less": true, ".html": true, ".htm": true, ".svg": true,
	".xml": true, ".csv": true, ".tsv": true,
	".sql": true, ".graphql": true, ".proto": true,
	".lock": true, ".log": true,
	".patch": true, ".diff": true,
}

// textBasenames 对账 TS `isTextFile` 里的文件名白名单。
var textBasenames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true,
	"readme": true, "changelog": true, ".gitignore": true, ".npmrc": true,
}

// isTextFileByExt 对账 TS `isTextFile`（`:47-52`）。
func isTextFileByExt(filePath string) bool {
	if textExtensions[strings.ToLower(filepath.Ext(filePath))] {
		return true
	}
	return textBasenames[strings.ToLower(filepath.Base(filePath))]
}

// imageExtensions 对账 TS `IMAGE_EXTENSIONS`（`:53`）。
var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".bmp": true, ".webp": true, ".ico": true,
}

// isImageFileByExt 对账 TS `isImageFile`。
func isImageFileByExt(filePath string) bool {
	return imageExtensions[strings.ToLower(filepath.Ext(filePath))]
}

// countFiles 对账 TS `countFiles`（`:402-414`）。
//
// **跳过点开头的目录/文件**（对账 TS：`if (entry.name.startsWith('.')) continue`）。
// **失败返回 1**（对账 TS 的 catch）。
func countFiles(dir string, maxDepth int) int {
	if maxDepth <= 0 {
		return 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			count += countFiles(filepath.Join(dir, e.Name()), maxDepth-1)
		} else {
			count++
		}
	}
	return count
}

// ── 复制兜底（symlink 不可用时）──────────────────────────────────────────

// copyFile 复制文件（对账 TS 的 `cp(resolved, targetPath, {force: true})` 兜底）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// copyDir 递归复制目录（symlink 不可用时的兜底，非 TS 原生路径）。
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target)
	})
}

// importURLFetchOptions 构造 URL 分支的抓取选项。
//
// **为什么抽出来**：超时是否真的传到 `HTTPFetchGuarded` 是审查发现的问题
// （曾漏传 → 用 15s 默认 → 15~60s 的下载失败）。抽成函数后测试能直接断言
// **构造出的 Options**，而不是只读常量（后者是恒真断言，抓不到漏传）。
func importURLFetchOptions() tnet.Options {
	return tnet.Options{TimeoutMs: tnet.IntPtr(importURLFetchTimeoutMs)}
}
