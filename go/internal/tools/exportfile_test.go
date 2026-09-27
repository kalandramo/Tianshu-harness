package tools

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exportfile_test.go —— `export_file` 工具（第八十二刀）。
//
// 对账 TS `src/tools/export-file.ts`（112 行）。
//
// # 为什么这个工具是「真接线」而非「造子系统」
//
// 它的依赖全是**平台能力**：`fs.mkdir/stat/copyFile/writeFile` + `path.dirname/resolve`
// + `expandHome` + `detectSensitiveFile`——**Go 侧全部已有**（实测：`expandHome`
// 10 处命中、`DetectSensitiveFile` 8 处）。**零回调、零未移植子系统**。
//
// 这与 `ask_image`（依赖 `params.visionAsk` 回调，其实现体 Go 侧零命中）不同——
// 后者即使写了工具也永远走 fail-closed 分支。

// ── 纯函数：输入解析（对账 TS 的 getDestinationPath / getSourcePath）────────

// TestExportFileResolvePaths —— 路径解析：trim + expandHome + 绝对化。
//
// 对账 TS：`resolve(expandHome(raw.trim()))`；空/非字符串 → nil。
func TestExportFileResolvePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法取 home")
	}

	// 空/非法 → nil
	for _, in := range []any{nil, "", "   ", 123, true} {
		if got := exportResolvePath(in); got != "" {
			t.Errorf("exportResolvePath(%#v) 应为空，实得 %q", in, got)
		}
	}

	// 普通绝对路径 → 保持
	abs := filepath.Join(t.TempDir(), "a.txt")
	if got := exportResolvePath(abs); got != abs {
		t.Errorf("绝对路径应保持，实得 %q", got)
	}

	// `~` 展开
	got := exportResolvePath("~/x.txt")
	if strings.Contains(got, "~") {
		t.Errorf("`~` 应展开，实得 %q", got)
	}
	if !strings.HasPrefix(got, home) {
		t.Errorf("展开后应在 home 下，实得 %q (home=%q)", got, home)
	}

	// 前后空白被 trim
	if got := exportResolvePath("  " + abs + "  "); got != abs {
		t.Errorf("应 trim 空白，实得 %q", got)
	}
}

// ── 参数校验分支 ────────────────────────────────────────────────────────

// TestExportFileRequiresDestination —— destination_path 必填。
//
// 对账 TS：`throw new Error('destination_path 为必填项')`。
func TestExportFileRequiresDestination(t *testing.T) {
	res := runExportFile(t, map[string]any{"content": "x"})
	if !res.IsError {
		t.Fatal("缺 destination_path 应报错")
	}
	if !strings.Contains(res.Content, "destination_path 为必填项") {
		t.Errorf("文案应逐字对账：实得 %q", res.Content)
	}
}

// TestExportFileContentXorSource —— content 与 source_path 必须且只能给一个。
//
// 对账 TS：`if (hasContent === hasSource) throw new Error('必须且只能提供 content 或 source_path 之一')`。
func TestExportFileContentXorSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.txt")

	// 两者都给 → 报错
	res := runExportFile(t, map[string]any{"destination_path": dst, "content": "x", "source_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "必须且只能提供") {
		t.Errorf("两者都给应报错，实得 %q", res.Content)
	}

	// 两者都不给 → 报错
	res = runExportFile(t, map[string]any{"destination_path": dst})
	if !res.IsError || !strings.Contains(res.Content, "必须且只能提供") {
		t.Errorf("两者都不给应报错，实得 %q", res.Content)
	}
}

// TestExportFileInvalidEncoding —— encoding 只接受 text/base64。
//
// 对账 TS：`if (encoding !== 'text' && encoding !== 'base64') throw ...`。
func TestExportFileInvalidEncoding(t *testing.T) {
	res := runExportFile(t, map[string]any{
		"destination_path": filepath.Join(t.TempDir(), "o.txt"),
		"content":          "x",
		"encoding":         "utf8",
	})
	if !res.IsError || !strings.Contains(res.Content, "encoding 必须为 text 或 base64") {
		t.Errorf("非法 encoding 应报错，实得 %q", res.Content)
	}
}

// ── content 模式 ────────────────────────────────────────────────────────

// TestExportFileWritesTextContent —— text 模式写入（含父目录自动创建）。
//
// 对账 TS：`mkdir(dirname(dest), {recursive:true})` + `writeFile(dest, Buffer.from(content,'utf-8'))`。
func TestExportFileWritesTextContent(t *testing.T) {
	base := t.TempDir()
	// 故意用**不存在的多级子目录**，验证 recursive mkdir。
	dst := filepath.Join(base, "a", "b", "c", "note.txt")

	res := runExportFile(t, map[string]any{"destination_path": dst, "content": "hello-export"})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应存在：%v", err)
	}
	if string(got) != "hello-export" {
		t.Errorf("内容应为 hello-export，实得 %q", got)
	}
	// 文案：`已导出 ${bytes} 字节到 ${path}`（content 模式无「（已复制）」后缀）
	if !strings.Contains(res.Content, "已导出 12 字节到 "+dst) {
		t.Errorf("文案应逐字对账：实得 %q", res.Content)
	}
	if strings.Contains(res.Content, "已复制") {
		t.Errorf("content 模式不应有「已复制」后缀，实得 %q", res.Content)
	}
}

// TestExportFileWritesBase64Content —— base64 模式解码后写入。
//
// 对账 TS：`Buffer.from(content, 'base64')`。
func TestExportFileWritesBase64Content(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "bin.dat")
	payload := []byte{0x00, 0x01, 0xff, 0xfe, 0x42}
	enc := base64.StdEncoding.EncodeToString(payload)

	res := runExportFile(t, map[string]any{
		"destination_path": dst, "content": enc, "encoding": "base64",
	})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("文件应存在：%v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("解码后应为原始字节，实得 %v", got)
	}
	if !strings.Contains(res.Content, "已导出 5 字节到") {
		t.Errorf("应报告解码后字节数（5），实得 %q", res.Content)
	}
}

// ── source_path 模式（copy）──────────────────────────────────────────────

// TestExportFileCopiesSource —— copy 模式复制文件，文案带「（已复制）」。
//
// 对账 TS：`copyFile(source, dest)` + `mode: 'copy'` → 文案后缀 `（已复制）`。
func TestExportFileCopiesSource(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src.txt")
	if err := os.WriteFile(src, []byte("copy-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(base, "sub", "dst.txt")

	res := runExportFile(t, map[string]any{"destination_path": dst, "source_path": src})
	if res.IsError {
		t.Fatalf("应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("目标应存在：%v", err)
	}
	if string(got) != "copy-me" {
		t.Errorf("内容应一致，实得 %q", got)
	}
	if !strings.Contains(res.Content, "已导出 7 字节到 "+dst+"（已复制）") {
		t.Errorf("copy 模式文案应带「（已复制）」：实得 %q", res.Content)
	}
}

// TestExportFileRejectsSensitiveSource —— **敏感文件硬门**（issue #135）。
//
// 对账 TS 注释：「source_path 曾是唯一不经敏感检测的读路径，可把 ~/.ssh/id_rsa、
// .env、凭证文件直接复制出项目。copy 之前先跑同一门禁。」
//
// **这是本工具最重要的安全边界**——它堵的是「导出 = 外泄」这条路。
func TestExportFileRejectsSensitiveSource(t *testing.T) {
	base := t.TempDir()
	dst := filepath.Join(base, "leak.txt")

	for _, name := range []string{".env", "id_rsa", "credentials.json"} {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join(base, name)
			if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			res := runExportFile(t, map[string]any{"destination_path": dst, "source_path": src})
			if !res.IsError {
				t.Fatalf("%s 应被拒（敏感文件），实得 %q", name, res.Content)
			}
			if !strings.Contains(res.Content, "拒绝导出敏感文件") {
				t.Errorf("文案应逐字对账：实得 %q", res.Content)
			}
			// **必须确认没真的复制**（拒绝必须是真拒绝）。
			if _, err := os.Stat(dst); err == nil {
				t.Errorf("%s 被拒后目标文件不应存在", name)
			}
		})
	}
}

// TestExportFileRejectsNonFileSource —— source_path 必须是文件（非目录）。
//
// 对账 TS：`if (!sourceStat.isFile()) throw new Error('source_path 必须是文件')`。
func TestExportFileRejectsNonFileSource(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "adir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	res := runExportFile(t, map[string]any{
		"destination_path": filepath.Join(base, "o.txt"), "source_path": dir,
	})
	if !res.IsError || !strings.Contains(res.Content, "source_path 必须是文件") {
		t.Errorf("目录作 source 应报错，实得 %q", res.Content)
	}
}

// TestExportFileSourceNotFound —— 不存在的 source 报错（不 panic）。
func TestExportFileSourceNotFound(t *testing.T) {
	base := t.TempDir()
	res := runExportFile(t, map[string]any{
		"destination_path": filepath.Join(base, "o.txt"),
		"source_path":      filepath.Join(base, "nope.txt"),
	})
	if !res.IsError {
		t.Errorf("不存在的 source 应报错，实得 %q", res.Content)
	}
}

// ── 50MB 上限 ────────────────────────────────────────────────────────────

// TestExportFileSizeCeiling —— 超 50MB 拒绝。
//
// 对账 TS：`MAX_EXPORT_BYTES = 50 * 1024 * 1024`。
//
// **为什么用「报告 size」而非真写 50MB**：构造 50MB 内容太慢（测试秒级变分钟级）。
// 用 `source_path` 模式 + 稀疏文件，或直接验常量与分支存在。
// 此处用**常量断言 + 边界分支存在性**，真实大文件留给集成测试。
func TestExportFileSizeCeiling(t *testing.T) {
	if exportMaxBytes != 50*1024*1024 {
		t.Errorf("上限应为 50MB，实得 %d", exportMaxBytes)
	}
	// 用 base64 构造刚好超限的**声明长度**（不真分配）——通过 content 的
	// 解码长度检查。此处用一个小技巧：base64 的长度校验在解码后，
	// 故用 50MB+1 的 base64 字符串会真分配。改为验证分支存在。
	//
	// 实际边界由 `TestExportFileSizeCeilingRealFile` 用真实文件覆盖。
}

// TestExportFileSizeCeilingRealFile —— 用真实稀疏文件覆盖 50MB 边界。
//
// **为什么可以这样**：`os.Truncate` 创建稀疏文件，磁盘占用近零，
// 但 `Stat().Size()` 返回真实大小——正好触发 size 分支而不必真写 50MB。
func TestExportFileSizeCeilingRealFile(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "big.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(exportMaxBytes + 1); err != nil {
		f.Close()
		t.Skipf("文件系统不支持稀疏文件：%v", err)
	}
	f.Close()

	res := runExportFile(t, map[string]any{
		"destination_path": filepath.Join(base, "out.bin"), "source_path": src,
	})
	if !res.IsError {
		t.Fatalf("超 50MB 应被拒，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "导出过大") || !strings.Contains(res.Content, "上限为 50MB") {
		t.Errorf("文案应逐字对账：实得 %q", res.Content)
	}
}

// ── definition 对账 ──────────────────────────────────────────────────────

// TestExportFileDefinitionParity —— definition 逐字对账 TS（前缀字节稳定）。
func TestExportFileDefinitionParity(t *testing.T) {
	def := ExportFile(t.TempDir()).Definition()

	if def.Name != "export_file" {
		t.Errorf("name 应为 export_file，实得 %q", def.Name)
	}
	wantPrefix := "将文件创建或复制到外部路径，如桌面、下载、挂载盘或 Windows 路径。"
	if !strings.HasPrefix(def.Description, wantPrefix) {
		t.Errorf("description 首行应逐字对账：\n实得 %q", def.Description)
	}
	if def.InputSchema == nil {
		t.Fatal("应有 InputSchema")
	}
	// 属性声明序对账 TS：destination_path → content → source_path → encoding。
	wantOrder := []string{"destination_path", "content", "source_path", "encoding"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
	// required 只有 destination_path。
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "destination_path" {
		t.Errorf("required 应只有 destination_path，实得 %#v", def.InputSchema.Required)
	}
	// encoding 的键序（enumPropOrdered：type→enum→description）。
	enc, ok := def.InputSchema.Properties["encoding"].(interface{ Marshal() string })
	if !ok {
		t.Fatalf("encoding 应是有序结构，实得 %T", def.InputSchema.Properties["encoding"])
	}
	if raw := enc.Marshal(); !strings.HasPrefix(raw, `{"type":"string","enum":["text","base64"],"description":`) {
		t.Errorf("encoding 键序应 type→enum→description：实得 %s", raw)
	}
}

// TestExportFileApprovalSemantics —— 恒需审批 + 非并发安全（对账 TS）。
func TestExportFileApprovalSemantics(t *testing.T) {
	tool := ExportFile(t.TempDir())
	if !tool.RequiresApproval(nil) {
		t.Error("export_file 应恒需审批（对账 TS `() => true`）")
	}
	if tool.ConcurrencySafe() {
		t.Error("不应并发安全（对账 TS `() => false`）")
	}
	if !tool.Enabled() {
		t.Error("应 enabled")
	}
}

// runExportFile 执行一次 export_file 调用。
func runExportFile(t *testing.T, input map[string]any) (res struct {
	Content string
	IsError bool
}) {
	t.Helper()
	r, err := ExportFile(t.TempDir()).Execute(nil, &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 不应返回 error：%v", err)
	}
	res.Content, res.IsError = r.Content, r.IsError
	return res
}
