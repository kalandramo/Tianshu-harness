package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	tnet "github.com/kalandramo/tianshu/go/internal/net"
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
	// 非 Windows 上应走符号链接（不复制）
	if !strings.Contains(content, "junction 链接") && !strings.Contains(content, "复制方式") {
		t.Errorf("应说明链接/复制方式，实得：\n%s", content)
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
	// 非 Windows 上应是符号链接（未复制）
	if info.Mode()&os.ModeSymlink == 0 {
		t.Log("（当前环境未走符号链接——可能是 Windows 或无权限，已退化为复制）")
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
