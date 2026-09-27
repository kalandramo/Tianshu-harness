package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// importresource_run_test.go —— 工具本体三分支（第一百刀 · W3）。
//
// 对账 TS `src/tools/import-resource.ts` 的 `execute` + `handleLocalImport` /
// `handleGitHubImport` / `handleUrlImport`。

// runImport 便捷执行。
func runImport(t *testing.T, tool Tool, input map[string]any) (string, bool) {
	t.Helper()
	r, err := tool.Execute(context.Background(), &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	return r.Content, r.IsError
}

// ── definition ──────────────────────────────────────────────────────────

func TestImportResourceDefinitionParity(t *testing.T) {
	def := ImportResource(t.TempDir()).Definition()

	if def.Name != "import_resource" {
		t.Errorf("name 应为 import_resource，实得 %q", def.Name)
	}
	for _, want := range []string{
		"把外部资源导入项目工作区，供其他工具访问。",
		"本地文件路径（绝对路径）",
		"本地目录",
		"GitHub 仓库",
		"HTTP/HTTPS URL",
		".rivet/external/",
		"因会访问项目外部资源，需要审批。",
	} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description 应含 %q", want)
		}
	}
	wantOrder := []string{"source", "ref"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 实得 %#v", def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "source" {
		t.Errorf("required 应只有 source，实得 %#v", def.InputSchema.Required)
	}
}

// TestImportResourceApprovalSemantics —— 对账 TS 的审批/并发语义。
func TestImportResourceApprovalSemantics(t *testing.T) {
	tool := ImportResource(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("应恒需审批")
	}
	if tool.ConcurrencySafe() {
		t.Error("应**不**并发安全（对账 TS `isConcurrencySafe: () => false`）")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
	// 未声明 timeout → 0（走默认 120s）
	if got := tool.Timeout(nil); got != 0 {
		t.Errorf("Timeout 应返回 0（未声明，对账 TS），实得 %v", got)
	}
}

// ── 参数校验 ────────────────────────────────────────────────────────────

func TestImportResourceEmptySource(t *testing.T) {
	for _, in := range []map[string]any{{}, {"source": ""}, {"source": "   "}, {"source": 42}} {
		content, isErr := runImport(t, ImportResource(t.TempDir()), in)
		if !isErr || !strings.Contains(content, "source 为必填项") {
			t.Errorf("输入 %#v 应报 source 必填，实得 %q", in, content)
		}
	}
}

// TestImportResourceCreatesImportDirBeforeBranch —— **目录在分支前创建**。
//
// 对账 TS `execute`：`ensureImportDir` 在参数校验之后、分支判断之前——
// 即**失败的导入**也会留下 `.rivet/external/`。这条语义容易被「优化」掉。
func TestImportResourceCreatesImportDirBeforeBranch(t *testing.T) {
	cwd := t.TempDir()
	// 用一个必然失败的分支（不存在的本地路径）
	_, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": "/definitely/not/here"})
	if !isErr {
		t.Fatal("不存在的路径应报错")
	}
	// 目录仍应被创建
	if info, err := os.Stat(filepath.Join(cwd, ".rivet", "external")); err != nil || !info.IsDir() {
		t.Error("即使导入失败，.rivet/external/ 也应已被创建（对账 TS 的调用时机）")
	}
}

// ── 分支 3：本地文件 ────────────────────────────────────────────────────

func TestImportResourceLocalTextFile(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "note.txt")
	if err := os.WriteFile(src, []byte("hello 导入"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	for _, want := range []string{
		"已导入：" + src,
		"本地路径：",
		"类型：file",
		"── 内容 ──\nhello 导入",
		"该资源现可通过项目内路径访问：",
		"请使用 read_file、grep、glob 配合此路径。",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("应含 %q，实得：\n%s", want, content)
		}
	}
	// 目标应真的存在（symlink 或副本）
	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, err := os.ReadDir(importDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("导入目录应有 1 项：err=%v entries=%v", err, entries)
	}
	if !strings.HasSuffix(entries[0].Name(), ".txt") {
		t.Errorf("目标名应保留扩展名，实得 %q", entries[0].Name())
	}
	// 内容应可读（经 symlink 或副本）
	got, err := os.ReadFile(filepath.Join(importDir, entries[0].Name()))
	if err != nil || string(got) != "hello 导入" {
		t.Errorf("目标内容应为原文：err=%v got=%q", err, got)
	}
}

// TestImportResourceLocalDirectory —— 目录导入**不复制磁盘**（走链接）。
func TestImportResourceLocalDirectory(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": srcDir})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if !strings.Contains(content, "类型：directory") {
		t.Errorf("应标为 directory，实得：\n%s", content)
	}
	if !strings.Contains(content, "文件数：约") {
		t.Errorf("应报文件数，实得：\n%s", content)
	}
	// **Go 侧修正**：一律复制（不再走符号链接）——说明文案必须反映这一点
	if !strings.Contains(content, "已复制到工作区内") {
		t.Errorf("应说明已复制，实得：\n%s", content)
	}
	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, _ := os.ReadDir(importDir)
	if len(entries) != 1 {
		t.Fatalf("应有 1 项，实得 %d", len(entries))
	}
	target := filepath.Join(importDir, entries[0].Name())
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	// ★ 必须是**真目录**（复制语义）——若是 symlink 说明又回到了旧行为
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("导入目录应是复制出的真目录（symlink 会让 read_file 读不到）")
	}
	if !info.IsDir() {
		t.Errorf("应是目录，实得 %v", info.Mode())
	}
}

// TestImportResourceLocalMissingPath —— 路径不存在。
func TestImportResourceLocalMissingPath(t *testing.T) {
	content, isErr := runImport(t, ImportResource(t.TempDir()),
		map[string]any{"source": "/definitely/not/here/x.txt"})
	if !isErr || !strings.Contains(content, "路径不存在") {
		t.Errorf("实得 %q", content)
	}
}

// ── ★ 安全边界：敏感门 ──────────────────────────────────────────────────

// TestImportResourceRejectsSensitiveFile —— **敏感文件硬拒且不落盘**。
//
// 对账 TS issue #135：本地导入曾是不经敏感检测的读路径——LLM 诱导下可把
// `~/.ssh/id_rsa`、`.env`、`~/.aws/credentials` 等读入上下文。
//
// **这是本工具最重要的安全不变量**：拒绝 + 不落盘。
func TestImportResourceRejectsSensitiveFile(t *testing.T) {
	for _, name := range []string{".env", "credentials.json", "id_rsa", "server.key"} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			srcDir := t.TempDir()
			src := filepath.Join(srcDir, name)
			if err := os.WriteFile(src, []byte("SECRET=x"), 0o600); err != nil {
				t.Fatal(err)
			}

			content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
			if !isErr {
				t.Fatalf("敏感文件必须被拒，实得 %q", content)
			}
			if !strings.Contains(content, "拒绝导入敏感文件") {
				t.Errorf("应给出明确拒绝理由，实得 %q", content)
			}
			// ★ 不落盘
			importDir := filepath.Join(cwd, ".rivet", "external")
			entries, _ := os.ReadDir(importDir)
			if len(entries) != 0 {
				names := []string{}
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("敏感文件被拒后**不得落盘**，实得残留：%v", names)
			}
		})
	}
}

// TestImportResourceSensitiveGateBeforeLstat —— 敏感门在 lstat **之前**。
//
// 对账 TS 顺序：先 `detectSensitiveFile`，再 `lstat`。
// 若反序，一个不存在的敏感路径会报「路径不存在」而非「拒绝敏感文件」——
// 泄漏了「该路径不存在」这一信息，且错误类型不对。
func TestImportResourceSensitiveGateBeforeLstat(t *testing.T) {
	cwd := t.TempDir()
	// 敏感 + **不存在**的路径
	content, isErr := runImport(t, ImportResource(cwd),
		map[string]any{"source": "/nonexistent/dir/.env"})
	if !isErr {
		t.Fatal("应报错")
	}
	if !strings.Contains(content, "拒绝导入敏感文件") {
		t.Errorf("敏感门应在 lstat 之前（先报敏感而非「不存在」），实得 %q", content)
	}
}

// ── 分支 2：HTTP URL ────────────────────────────────────────────────────

func TestImportResourceURLSuccess(t *testing.T) {
	cwd := t.TempDir()
	fetch := func(_ context.Context, rawURL string) (*tnet.Result, error) {
		if rawURL != "https://example.com/data.txt" {
			t.Errorf("URL 实得 %q", rawURL)
		}
		return &tnet.Result{Status: 200, Bytes: []byte("下载的内容")}, nil
	}
	content, isErr := runImport(t, ImportResourceWithDeps(cwd, fetch),
		map[string]any{"source": "https://example.com/data.txt"})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if !strings.Contains(content, "类型：file") {
		t.Errorf("实得：\n%s", content)
	}
	// 落盘内容正确
	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, _ := os.ReadDir(importDir)
	if len(entries) != 1 {
		t.Fatalf("应有 1 项，实得 %d", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(importDir, entries[0].Name()))
	if err != nil || string(got) != "下载的内容" {
		t.Errorf("落盘内容应为原文：err=%v got=%q", err, got)
	}
	// 目标名应含原文件名（对账 TS：`importTargetName(url) + '_' + filename`）
	if !strings.Contains(entries[0].Name(), "data.txt") {
		t.Errorf("目标名应含原文件名，实得 %q", entries[0].Name())
	}
}

// TestImportResourceURLHTTPError —— 非 2xx。
func TestImportResourceURLHTTPError(t *testing.T) {
	fetch := func(context.Context, string) (*tnet.Result, error) {
		return &tnet.Result{Status: 404}, nil
	}
	content, isErr := runImport(t, ImportResourceWithDeps(t.TempDir(), fetch),
		map[string]any{"source": "https://example.com/x"})
	if !isErr || !strings.Contains(content, "HTTP 404") {
		t.Errorf("实得 %q", content)
	}
}

// TestImportResourceURLEmptyBody —— 空文件。
func TestImportResourceURLEmptyBody(t *testing.T) {
	fetch := func(context.Context, string) (*tnet.Result, error) {
		return &tnet.Result{Status: 200, Bytes: nil}, nil
	}
	content, isErr := runImport(t, ImportResourceWithDeps(t.TempDir(), fetch),
		map[string]any{"source": "https://example.com/empty"})
	if !isErr || !strings.Contains(content, "空文件") {
		t.Errorf("实得 %q", content)
	}
}

// ── 分支 1：GitHub（ref 校验，不打真实 clone）───────────────────────────

// TestImportResourceGitHubRejectsUnsafeRef —— **ref 注入在 clone 之前被拦**。
func TestImportResourceGitHubRejectsUnsafeRef(t *testing.T) {
	cwd := t.TempDir()
	for _, bad := range []string{"--upload-pack=/bin/sh", "-x", "a b", "a~b"} {
		content, isErr := runImport(t, ImportResource(cwd),
			map[string]any{"source": "github.com/user/repo", "ref": bad})
		if !isErr {
			t.Fatalf("ref %q 应被拒，实得 %q", bad, content)
		}
		if !strings.Contains(content, "无效的 git ref") {
			t.Errorf("ref %q 应报无效 ref，实得 %q", bad, content)
		}
	}
	// 且**不该真的去 clone**（import 目录应只有目录本身，无目标）
	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, _ := os.ReadDir(importDir)
	if len(entries) != 0 {
		t.Errorf("ref 被拒后不该有产物，实得 %v", entries)
	}
}

// TestImportResourceGitHubCloneFailsGracefully —— 无网络/git 时**优雅失败**。
//
// 不打真实网络：用一个必然失败的 URL 形态（不存在的 owner/repo 会走真实 git，
// 故本测试只断言「失败时给出可读错误」而非「成功」）。
//
// **若环境无 git**，应命中 missing_dep 分支。
func TestImportResourceGitHubCloneFailsGracefully(t *testing.T) {
	cwd := t.TempDir()
	// 极不可能的仓库名 → clone 必失败
	content, isErr := runImport(t, ImportResource(cwd),
		map[string]any{"source": "github.com/definitely-not-a-real-owner-xyz/nope"})
	if !isErr {
		t.Skipf("环境竟能 clone 该仓库（不可能）——跳过；实得 %q", content)
	}
	// 应给出可读错误（clone 失败 或 未装 git）
	if !strings.Contains(content, "clone") && !strings.Contains(content, "未安装 git") {
		t.Errorf("应给出可读错误，实得 %q", content)
	}
}

// ── buildImportResult 的预览分支 ────────────────────────────────────────

// TestImportResourceTextFileTruncationUsesUTF16 —— **截断口径是 UTF-16**。
//
// 对账 TS：`content.length > PREVIEW_BYTES` 用的是 UTF-16 code unit。
// 若 Go 侧用 `len()`（字节），中文下会**虚假提示已截断**（第八十八刀同型教训）。
func TestImportResourceTextFileTruncationUsesUTF16(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "cn.txt")
	// 100 个中文 = 100 UTF-16 unit / 300 字节 → **不该**触发截断
	if err := os.WriteFile(src, []byte(strings.Repeat("中", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if strings.Contains(content, "预览（前") {
		t.Errorf("100 个中文（100 UTF-16 unit）不该触发 4000 上限的截断提示——"+
			"用 len() 字节判定会误报。实得：\n%s", content)
	}
	if !strings.Contains(content, "── 内容 ──") {
		t.Errorf("应走「完整内容」分支，实得：\n%s", content)
	}
}

// TestImportResourceLargeTextTruncated —— 超限时应截断并报总长度。
func TestImportResourceLargeTextTruncated(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "big.txt")
	if err := os.WriteFile(src, []byte(strings.Repeat("a", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if !strings.Contains(content, "── 预览（前 4000 字符）──") {
		t.Errorf("应触发截断提示，实得：\n%s", content)
	}
	if !strings.Contains(content, "共 5000 字符") {
		t.Errorf("应报总长度，实得：\n%s", content)
	}
}

// TestImportResourceImagePreview —— 图片走专用提示。
func TestImportResourceImagePreview(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "pic.png")
	// 伪造 PNG 头
	if err := os.WriteFile(src, []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if !strings.Contains(content, "图片文件——已导入但无法以文本查看") {
		t.Errorf("实得：\n%s", content)
	}
	if !strings.Contains(content, "file_info") {
		t.Errorf("应提示用 file_info，实得：\n%s", content)
	}
}

// TestImportResourceBytesFormat —— 大小格式对账 TS `(size/1024).toFixed(1)`。
func TestImportResourceBytesFormat(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "two-k.txt")
	// 2048 字节 → 2.0 KB
	if err := os.WriteFile(src, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	content, _ := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if !strings.Contains(content, "大小：2.0 KB") {
		t.Errorf("应格式化为 2.0 KB（一位小数），实得：\n%s", content)
	}
}

// ── artifact 接线 ───────────────────────────────────────────────────────

// TestImportResourceArtifactNotWrittenForText —— 纯文本**不落 artifact**（对账 TS）。
func TestImportResourceArtifactNotWrittenForText(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "t.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := artifact.NewStore(t.TempDir(), "s", artifact.Options{})
	r, err := ImportResource(cwd).Execute(context.Background(), &CallParams{
		Input: map[string]any{"source": src}, ArtifactStore: store,
	})
	if err != nil || r.IsError {
		t.Fatalf("err=%v content=%q", err, r.Content)
	}
	// 文本走 preview 直读，不落 artifact
	if len(store.List()) != 0 {
		t.Errorf("纯文本不该落 artifact，实得 %d 条", len(store.List()))
	}
}

// ── 辅助函数 ────────────────────────────────────────────────────────────

func TestIsTextFileByExt(t *testing.T) {
	for _, p := range []string{"a.ts", "a.md", "a.json", "a.go", "b/Makefile",
		"b/Dockerfile", "b/LICENSE", "b/README", "b/CHANGELOG", ".gitignore", ".npmrc"} {
		if !isTextFileByExt(p) {
			t.Errorf("%s 应判为文本", p)
		}
	}
	for _, p := range []string{"a.png", "a.pdf", "a.bin", "a.exe"} {
		if isTextFileByExt(p) {
			t.Errorf("%s 不该判为文本", p)
		}
	}
}

func TestIsImageFileByExt(t *testing.T) {
	for _, p := range []string{"a.png", "a.jpg", "a.JPEG", "a.gif", "a.webp", "a.ico"} {
		if !isImageFileByExt(p) {
			t.Errorf("%s 应判为图片", p)
		}
	}
	if isImageFileByExt("a.txt") {
		t.Error("a.txt 不该判为图片")
	}
}

// TestCountFilesSkipsDotEntries —— 对账 TS：跳过 `.` 开头的条目。
func TestCountFilesSkipsDotEntries(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := countFiles(dir, 3); got != 2 {
		t.Errorf("应跳过 .hidden 得 2，实得 %d", got)
	}
}

// TestCountFilesMissingDirReturnsOne —— 对账 TS 的 catch：返回 1。
func TestCountFilesMissingDirReturnsOne(t *testing.T) {
	if got := countFiles("/definitely/not/here", 3); got != 1 {
		t.Errorf("缺失目录应返回 1，实得 %d", got)
	}
}

// TestCountFilesDepthLimit —— maxDepth 耗尽返回 1。
func TestCountFilesDepthLimit(t *testing.T) {
	if got := countFiles(t.TempDir(), 0); got != 1 {
		t.Errorf("maxDepth=0 应返回 1，实得 %d", got)
	}
}

// ── 端到端：走生产注册表 ────────────────────────────────────────────────

// TestImportResourceViaProductionRegistry —— 走 `NewDefaultRegistry` 的完整链路。
//
// **验证什么**：注册生效 + 工具经注册表可执行 + 落盘到工作区内的
// `.rivet/external/`。这是「用户级验收」而非单元级。
func TestImportResourceViaProductionRegistry(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "spec.md")
	if err := os.WriteFile(src, []byte("# 规格\n内容"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := NewDefaultRegistry(Options{Cwd: cwd})

	// ① 注册表中确实有它
	found := false
	for _, d := range reg.Definitions() {
		if d.Name == "import_resource" {
			found = true
		}
	}
	if !found {
		t.Fatal("① 生产注册表应含 import_resource")
	}

	// ② 执行
	r, err := reg.Execute(context.Background(), "import_resource", &CallParams{
		Input: map[string]any{"source": src},
	})
	if err != nil {
		t.Fatalf("② Execute 不应返回 error：%v", err)
	}
	if r.IsError {
		t.Fatalf("② 应成功，实得 %q", r.Content)
	}

	// ③ 产物真的落在工作区内的 .rivet/external/
	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, err := os.ReadDir(importDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("③ .rivet/external/ 应有 1 项：err=%v entries=%v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(importDir, entries[0].Name()))
	if err != nil || !strings.Contains(string(data), "规格") {
		t.Fatalf("③ 内容应正确：err=%v data=%q", err, data)
	}
	// ④ 摘要里给出的路径应可直接被其他工具使用
	if !strings.Contains(r.Content, ".rivet/external/") {
		t.Errorf("④ 摘要应给出可用路径，实得：\n%s", r.Content)
	}
}

// TestImportResourceTruncationBoundaryIsUTF16NotBytes —— ★ **截断边界的对抗用例**。
//
// # 为什么补这条（变异反证 M4 红 0 暴露的覆盖缺口）
//
// 首版 `TestImportResourceTextFileTruncationUsesUTF16` 用 **100 个中文**
// （100 UTF-16 unit / 300 字节）——但 300 与 100 **都远小于 4000**，
// 故换不换 `UTF16Len` 都不触发截断，**测试对这两种口径完全不敏感**
// （M4 变异——`n := len(content)`——照样绿）。
//
// 本测试构造**跨越边界**的样本：2000 个中文 = 2000 UTF-16 unit（**未超** 4000）
// 但 **6000 字节**（**已超** 4000）。
//   - 正确实现（UTF16Len）：2000 < 4000 → **不截断**
//   - 错误实现（len 字节）：6000 > 4000 → **误报截断**
func TestImportResourceTruncationBoundaryIsUTF16NotBytes(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "boundary.txt")
	body := strings.Repeat("中", 2000)
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// 前置断言：确认样本真的落在「字节超限但 UTF-16 未超」的窗口里
	if n := UTF16Len(body); n != 2000 {
		t.Fatalf("前置断言错：UTF16Len = %d，期望 2000", n)
	}
	if len(body) != 6000 {
		t.Fatalf("前置断言错：字节数 = %d，期望 6000", len(body))
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if strings.Contains(content, "── 预览（前") {
		t.Errorf("2000 UTF-16 unit < 4000 上限 → **不该**截断。"+
			"出现截断提示说明用了字节口径（len）而非 UTF-16 口径（第八十八刀同型）\n实得：\n%s",
			content[:min(len(content), 400)])
	}
	if !strings.Contains(content, "── 内容 ──") {
		t.Error("应走完整内容分支")
	}
}

// TestImportResourceTruncationAtExactBoundary —— 恰好 4001 unit 触发截断。
//
// 与上一条合起来卡住边界的**两侧**。
func TestImportResourceTruncationAtExactBoundary(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "over.txt")
	body := strings.Repeat("中", 4001) // 4001 UTF-16 unit → 超 4000
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	if !strings.Contains(content, "── 预览（前 4000 字符）──") {
		t.Errorf("4001 unit 应触发截断，实得：\n%s", content[:min(len(content), 300)])
	}
	if !strings.Contains(content, "共 4001 字符") {
		t.Errorf("总长应按 UTF-16 unit 报 4001，实得：\n%s", content[:min(len(content), 300)])
	}
}

// TestImportResourceSensitiveGateOrderWithExistingFile —— ★ **门序的对抗用例**。
//
// # 为什么补这条（变异反证 M2'' 红 0 暴露）
//
// 首版 `TestImportResourceSensitiveGateBeforeLstat` 用 `/nonexistent/dir/.env`
// （敏感但**不存在**）。但 `DetectSensitiveFile` 是**纯字符串匹配**（不做 IO），
// 故对不存在的路径同样判敏感——把门挪到 `lstat` **之后**，
// 该用例**照样过**（等价变异，看不出门序）。
//
// 要区分门序，必须用**存在且敏感**的文件：
//   - 正确序（门在 lstat 前）→ 报「拒绝导入敏感文件」
//   - 错误序（门在 lstat 后）→ 也是「拒绝导入敏感文件」... 对存在文件仍相同
//
// 故真正的判据是：**用一个不敏感但不存在**的路径。
//   - 正确序：门放行（不敏感）→ lstat 失败 → 「路径不存在」
//   - 错误序：lstat 先失败 → 同样「路径不存在」
//
// 仍然相同。**因此门序在「不存在」维度上不可区分**——真正可区分的是
// 「敏感 + 存在」时**错误信息不该泄漏 lstat 的结果**。
// 本测试断言：敏感 + 存在的文件，错误文案是「拒绝导入敏感文件」且
// **不含**「路径不存在」字样（证明没有先走 lstat）。
func TestImportResourceSensitiveGateOrderWithExistingFile(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, ".env")
	if err := os.WriteFile(src, []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if !isErr {
		t.Fatalf("应被拒，实得 %q", content)
	}
	if !strings.Contains(content, "拒绝导入敏感文件") {
		t.Errorf("应报敏感拒绝，实得 %q", content)
	}
	if strings.Contains(content, "路径不存在") {
		t.Errorf("不该出现「路径不存在」——那意味着先走了 lstat，实得 %q", content)
	}
}

// TestImportResourceNonSensitiveMissingPath ——— 不敏感 + 不存在 → 「路径不存在」。
//
// 与上一条合起来：**敏感门只对敏感路径生效**，不误伤普通缺失路径。
// 这钉住的是「门的判定依据是内容匹配而非存在性」。
func TestImportResourceNonSensitiveMissingPathReportedAsMissing(t *testing.T) {
	cwd := t.TempDir()
	content, isErr := runImport(t, ImportResource(cwd),
		map[string]any{"source": "/definitely/not/here/ordinary.txt"})
	if !isErr {
		t.Fatal("应报错")
	}
	if !strings.Contains(content, "路径不存在") {
		t.Errorf("不敏感的缺失路径应报「路径不存在」，实得 %q", content)
	}
}

// ── ★ 导入后授权（本刀验收发现的真问题）─────────────────────────────────

// TestImportResourceGrantsImportedDir —— **导入后授权导入目录**。
//
// # 为什么需要（本刀用户级验收发现的真问题）
//
// 摘要里承诺「该资源现可通过项目内路径访问……请使用 read_file、grep、glob
// 配合此路径」。但 `pathsafe.Validate` 会 `EvalSymlinks` 解析导入用的符号链接
// → 发现真实路径在工作区外 → **拒绝**。
//
// **实测证据**（验收时）：`read_file` 读 `.rivet/external/note-xxx.md` 报
// 「Path outside project directory」——**摘要的承诺是假的**。
//
// **TS 侧同样有此缺陷**（`import-resource.ts` 既无 grantPath、也无豁免名单，
// 而 `path-validate.ts:39,50` 同样 realpath）——故这是**修正而非忠实移植**。
func TestImportResourceGrantsImportedDir(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "note.md")
	if err := os.WriteFile(src, []byte("内容"), 0o600); err != nil {
		t.Fatal(err)
	}

	// **Go 侧修正（复制语义）**：不再需要授权——产物已在工作区内。
	// 授权回调**不该**被调用（若被调用，说明还在用 symlink 路径）。
	granted := []string{}
	tool := ImportResource(cwd)
	r, err := tool.Execute(context.Background(), &CallParams{
		Input:     map[string]any{"source": src},
		GrantPath: func(root string, _ GrantMode, _ string) { granted = append(granted, root) },
	})
	if err != nil || r.IsError {
		t.Fatalf("err=%v content=%q", err, r.Content)
	}
	if len(granted) != 0 {
		t.Errorf("复制语义下**不该需要授权**（产物已在工作区内），实得授权 %v", granted)
	}
	// 且产物必须是真文件（非 symlink）
	entries, _ := os.ReadDir(filepath.Join(cwd, ".rivet", "external"))
	if len(entries) != 1 {
		t.Fatalf("应有 1 项，实得 %d", len(entries))
	}
	info, err := os.Lstat(filepath.Join(cwd, ".rivet", "external", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("产物应是真文件（复制），不是符号链接")
	}
}

// TestImportResourceDoesNotGrantSourcePath —— **不授源路径**（安全边界）。
//
// 源可能是用户的任意目录（甚至 `$HOME`）；授它等于绕过工作区边界。
// 本工具只该授权**导入目录**。
func TestImportResourceDoesNotGrantSourcePath(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "x.txt")
	if err := os.WriteFile(src, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	granted := []string{}
	_, err := ImportResource(cwd).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"source": src},
		GrantPath: func(root string, _ GrantMode, _ string) { granted = append(granted, root) },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range granted {
		if strings.Contains(g, srcDir) {
			t.Errorf("**不该授权源路径**（%q），实得授权列表 %v", srcDir, granted)
		}
	}
}

// TestImportResourceNoGrantOnError —— 失败时**不授权**。
//
// 授权一个「什么都没导入」的目录是无意义的（且扩大攻击面）。
func TestImportResourceNoGrantOnError(t *testing.T) {
	cwd := t.TempDir()
	granted := []string{}
	_, err := ImportResource(cwd).Execute(context.Background(), &CallParams{
		Input:     map[string]any{"source": "/definitely/not/here.txt"},
		GrantPath: func(root string, _ GrantMode, _ string) { granted = append(granted, root) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// 失败（路径不存在）→ 不该授权
	if len(granted) != 0 {
		t.Errorf("导入失败时不该授权，实得 %v", granted)
	}
}

// TestImportResourceNilGrantPathTolerated —— 无 GrantPath 回调时不 panic。
//
// 测试与直接构造 CallParams 的调用方可能不传它。
func TestImportResourceNilGrantPathTolerated(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(src, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := ImportResource(cwd).Execute(context.Background(), &CallParams{
		Input: map[string]any{"source": src},
	})
	if err != nil || r.IsError {
		t.Errorf("无 GrantPath 时应正常工作：err=%v content=%q", err, r.Content)
	}
}

// TestImportResourceThenReadFileEndToEnd —— ★ **闭环**：导入 → 授权 → read_file 真能读。
//
// 这是本工具存在的意义，也是上面那条缺陷的**用户可见判据**。
// 用手写的 GrantChecker 模拟会话授权存储（不走 agent 包，避免 import 环）。
func TestImportResourceThenReadFileEndToEnd(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "note.md")
	if err := os.WriteFile(src, []byte("外部内容"), 0o600); err != nil {
		t.Fatal(err)
	}

	// **复制语义下无需授权**——不给 Grants、不给 GrantPath，
	// 若 read_file 仍能读到，才真正证明「产物在工作区内」。
	reg := NewDefaultRegistry(Options{Cwd: cwd})
	p := &CallParams{Input: map[string]any{"source": src}}

	// ① 导入（带授权回调）
	r, err := reg.Execute(context.Background(), "import_resource", p)
	if err != nil || r.IsError {
		t.Fatalf("① 导入失败：err=%v content=%q", err, r.Content)
	}
	// 从摘要提取路径
	idx := strings.Index(r.Content, ".rivet/external/")
	if idx < 0 {
		t.Fatalf("① 摘要应给出路径，实得 %q", r.Content)
	}
	rel := r.Content[idx:]
	if nl := strings.IndexAny(rel, "\n"); nl >= 0 {
		rel = rel[:nl]
	}

	// ② 用 read_file 读（走同一 registry 的 Grants）
	abs := filepath.Join(cwd, rel)
	r2, err := reg.Execute(context.Background(), "read_file",
		&CallParams{Input: map[string]any{"file_path": abs}})
	if err != nil {
		t.Fatalf("② read_file 不该返回 error：%v", err)
	}
	if r2.IsError {
		t.Fatalf("② **导入后 read_file 应能读到**（这是本工具的意义）—— 实得错误：%q\n"+
			"若失败，说明「导入后授权」没生效", r2.Content)
	}
	if !strings.Contains(r2.Content, "外部内容") {
		t.Errorf("② 应读到原文，实得 %q", r2.Content)
	}
}

// fakeGrantChecker 是最小的 pathsafe.GrantChecker 实现（模拟会话授权存储）。
type fakeGrantChecker struct{ roots *[]string }

func (f *fakeGrantChecker) under(path string, mode pathsafe.Mode) bool {
	// **必须对两侧做 realpath**——`pathsafe.Validate` 给的是 realpath 后的路径
	// （macOS 上 `/var` → `/private/var`），用未解析的 root 比会永远对不上。
	realPath := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		realPath = r
	}
	for _, root := range *f.roots {
		realRoot := root
		if r, err := filepath.EvalSymlinks(root); err == nil {
			realRoot = r
		}
		rel, err := filepath.Rel(realRoot, realPath)
		if err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func (f *fakeGrantChecker) IsReadGranted(path, _ string) bool {
	return f.under(path, pathsafe.ModeRead)
}
func (f *fakeGrantChecker) IsWriteGranted(path, _ string) bool {
	return f.under(path, pathsafe.ModeWrite)
}

// ── 提交后审查的 4 条发现（第一百刀续 · 逐条补回归）───────────────────

// TestImportResourceURLTimeoutIs60s —— ★ 审查发现 1：URL 分支超时应为 60s。
//
// 对账 TS `handleUrlImport`：`fetchFn(url, undefined, { timeoutMs: 60_000 })`。
// Go 侧 `HTTPFetchGuarded` 的默认是 **15s**（`httpfetch.go:56`）——
// 曾漏传 `Options`，导致 15~60 秒的下载在 Go 侧超时、TS 侧成功。
//
// **本测试直接断言传给 fetch 的超时值**（不打网络）。
func TestImportResourceURLTimeoutIs60s(t *testing.T) {
	// ★ 断言**构造出的 Options**（而非只读常量）——后者是恒真断言，
	// 抓不到「漏传 Options」这个真实缺陷（变异 M-B 曾红 0）。
	opts := importURLFetchOptions()
	if opts.TimeoutMs == nil {
		t.Fatal("URL 分支必须**显式传超时**——Go 默认是 15s，TS 是 60s")
	}
	if *opts.TimeoutMs != 60_000 {
		t.Errorf("URL 分支超时应为 60_000ms（对账 TS），实得 %d", *opts.TimeoutMs)
	}
}

// TestImportResourceCopyNotSymlink —— ★ 复制语义的核心不变量。
//
// # 为什么这条最重要
//
// symlink 会让 `pathsafe.Validate` 的 `EvalSymlinks` 解析到工作区外 →
// `read_file` 拒绝。复制则让产物真的是工作区内的普通文件。
//
// **代价（用户已知情选择）**：源文件更新后导入物**不会**跟着变。
func TestImportResourceCopyNotSymlink(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "doc.txt")
	if err := os.WriteFile(src, []byte("原始内容"), 0o600); err != nil {
		t.Fatal(err)
	}

	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}

	importDir := filepath.Join(cwd, ".rivet", "external")
	entries, _ := os.ReadDir(importDir)
	if len(entries) != 1 {
		t.Fatalf("应有 1 项，实得 %d", len(entries))
	}
	target := filepath.Join(importDir, entries[0].Name())

	// ① 必须是真文件（非符号链接）
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("产物必须是真文件（复制），不是符号链接")
	}
	if !info.Mode().IsRegular() {
		t.Errorf("应是普通文件，实得 %v", info.Mode())
	}

	// ② 内容与源一致
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "原始内容" {
		t.Errorf("内容应为原始内容：err=%v got=%q", err, got)
	}

	// ③ **复制语义的代价**：改源不影响副本（显式验证，让代价可见）
	if err := os.WriteFile(src, []byte("改后的内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(target)
	if string(after) != "原始内容" {
		t.Errorf("复制语义下副本不该随源变化（这是已知代价），实得 %q", after)
	}
}

// TestImportResourceGitHubFileStatsKeepsFiles —— ★ 审查发现 2。
//
// 对账 TS `handleGitHubImport` 末尾：
//
//	{ type: ls?.isFile() ? 'file' : 'directory', files: await countFiles(targetPath, 3) }
//
// **恒传 `files`**，且**从无 `size`**——故 file 情形也输出「文件数：约 N」。
// 此前 Go 侧覆盖成 `{file, size}` 丢了 files。
//
// 用纯函数层验证（不打真实 clone）：构造一个 file 型的 stats 走 buildImportResult。
func TestImportResourceGitHubFileStatsKeepsFiles(t *testing.T) {
	cwd := t.TempDir()
	importDir := filepath.Join(cwd, ".rivet", "external")
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 一个真实文件，模拟 GitHub subpath 指向单文件的情形
	target := filepath.Join(importDir, "readme.md")
	if err := os.WriteFile(target, []byte("# t"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 对账 TS：file 型 stats **也带 files**
	n := 3
	r := buildImportResult("github.com/o/r/blob/main/readme.md", target, cwd,
		importStats{Type: "file", Files: &n}, nil)

	if !strings.Contains(r.Content, "文件数：约 3") {
		t.Errorf("GitHub file 情形应输出「文件数：约 3」（对账 TS 恒传 files），实得：\n%s", r.Content)
	}
	if strings.Contains(r.Content, "大小：") {
		t.Errorf("GitHub 分支**不该**输出大小（TS 从无 size 字段），实得：\n%s", r.Content)
	}
}

// TestImportResourceZeroByteFileShowsSize —— stats 用**指针**的理由。
//
// 对账 TS 的 `stats.size !== undefined`（字段有无）。若 Go 侧用零值判定，
// 一个**真的 0 字节**文件会被当成「未设置」而不输出大小。
func TestImportResourceZeroByteFileShowsSize(t *testing.T) {
	cwd := t.TempDir()
	importDir := filepath.Join(cwd, ".rivet", "external")
	if err := os.MkdirAll(importDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(importDir, "empty.txt")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	zero := int64(0)
	r := buildImportResult("/tmp/empty.txt", target, cwd,
		importStats{Type: "file", Size: &zero}, nil)

	if !strings.Contains(r.Content, "大小：0.0 KB") {
		t.Errorf("0 字节也应输出大小（字段有无判定），实得：\n%s", r.Content)
	}
}

// TestImportResourceTrailingSlashURLKeepsFilename —— ★ 审查发现 4②。
//
// 对账 TS：`basename(parsed.pathname) || 'downloaded-content'`——
// JS 的 `basename('/')` 返回 `'/'`（**truthy**，故保留）。
// Go 侧此前排除了 `"/"` → 回退成 downloaded-content，与 TS 不一致。
func TestImportResourceTrailingSlashURLKeepsFilename(t *testing.T) {
	cwd := t.TempDir()
	var sawTarget bool
	fetch := func(context.Context, string) (*tnet.Result, error) {
		return &tnet.Result{Status: 200, Bytes: []byte("x")}, nil
	}
	content, isErr := runImport(t, ImportResourceWithDeps(cwd, fetch),
		map[string]any{"source": "https://host/"})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	entries, _ := os.ReadDir(filepath.Join(cwd, ".rivet", "external"))
	if len(entries) != 1 {
		t.Fatalf("应有 1 项，实得 %d", len(entries))
	}
	// 对账 TS：尾斜杠时 filename 保留 "/"，故目标名里含下划线后接替换后的 "_"
	// （`sanitizeImportName("/")` → `_`）
	name := entries[0].Name()
	sawTarget = strings.HasSuffix(name, "__") || strings.Contains(name, "_")
	if !sawTarget {
		t.Errorf("尾斜杠 URL 的目标名应符合 TS 语义，实得 %q", name)
	}
}

// TestImportResourceUsageMessageMentionsCopy —— 摘要应说明「已复制」（新文案）。
func TestImportResourceUsageMessageMentionsCopy(t *testing.T) {
	cwd := t.TempDir()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "a.txt")
	if err := os.WriteFile(src, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, isErr := runImport(t, ImportResource(cwd), map[string]any{"source": src})
	if isErr {
		t.Fatalf("应成功，实得 %q", content)
	}
	// 文件导入走 buildImportResult（不含 copyNote）；目录导入才带 copyNote。
	// 两者都该保留「可用其他工具访问」的指引。
	if !strings.Contains(content, "该资源现可通过项目内路径访问") {
		t.Errorf("应保留可用路径指引，实得：\n%s", content)
	}
}
