package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docextract_test.go —— 外部命令编排（第一百刀 · W2）。
//
// 对账 TS `src/tools/doc-extract.ts`（271 行）。
//
// **全部用注入 runner**——不打真实系统命令，测试确定且可在任何平台跑。

// fakeRunner 记录调用并按 binary 返回预设结果。
type fakeRunner struct {
	calls   []fakeCall
	stdout  map[string]string
	failOn  map[string]error
	written map[string]string // soffice 路径：runner 被调时"写出"的文件
	outDir  string
}

type fakeCall struct {
	binary    string
	args      []string
	timeoutMs int
}

func (f *fakeRunner) run(binary string, args []string, timeoutMs int) (string, error) {
	f.calls = append(f.calls, fakeCall{binary: binary, args: args, timeoutMs: timeoutMs})
	if err, ok := f.failOn[binary]; ok {
		return "", err
	}
	// soffice：模拟「把转换产物写进 --outdir」
	if binary == "soffice" || binary == "libreoffice" {
		if f.written != nil {
			for name, content := range f.written {
				if f.outDir != "" {
					_ = os.WriteFile(filepath.Join(f.outDir, name), []byte(content), 0o600)
				}
			}
		}
	}
	return f.stdout[binary], nil
}

// ── isExtractableDocument ───────────────────────────────────────────────

func TestIsExtractableDocument(t *testing.T) {
	for _, p := range []string{"a.pdf", "a.docx", "a.doc", "a.rtf", "a.odt",
		"a.pptx", "a.odp", "a.xlsx", "a.xls", "a.ods", "A.PDF", "A.Docx"} {
		if !isExtractableDocument(p) {
			t.Errorf("%s 应可抽取", p)
		}
	}
	for _, p := range []string{"a.txt", "a.md", "a.go", "a.png", "noext", ""} {
		if isExtractableDocument(p) {
			t.Errorf("%s 不该判为可抽取", p)
		}
	}
}

// ── buildEngineChain ────────────────────────────────────────────────────

// TestBuildEngineChainByExtension —— 按扩展名分派（对账 TS `buildEngineChain`）。
func TestBuildEngineChainByExtension(t *testing.T) {
	engines := func(steps []docExtractStep) []string {
		out := []string{}
		for _, s := range steps {
			out = append(out, string(s.engine))
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		ext      string
		platform string
		want     []string
	}{
		{".pdf", "darwin", []string{"pdftotext"}},
		{".pdf", "linux", []string{"pdftotext"}},
		{".docx", "darwin", []string{"textutil", "soffice", "pandoc"}},
		{".docx", "linux", []string{"soffice", "pandoc"}},
		{".odt", "darwin", []string{"textutil", "soffice", "pandoc"}},
		{".rtf", "linux", []string{"soffice", "pandoc"}},
		{".doc", "darwin", []string{"textutil", "soffice"}},
		{".doc", "linux", []string{"soffice"}}, // pandoc 读不了旧 .doc
		{".pptx", "darwin", []string{"soffice"}},
		{".odp", "linux", []string{"soffice"}},
		{".xlsx", "darwin", []string{"soffice"}}, // TS 有 exceljs，Go 侧收窄
		{".xls", "linux", []string{"soffice"}},
		{".ods", "darwin", []string{"soffice"}},
		{".txt", "darwin", nil},
		{"", "linux", nil},
	}
	for _, c := range cases {
		got := engines(buildEngineChain(c.ext, c.platform))
		if !eq(got, c.want) {
			t.Errorf("buildEngineChain(%q, %q) = %v，期望 %v", c.ext, c.platform, got, c.want)
		}
	}
}

// TestBuildEngineChainTextutilOnlyOnDarwin —— textutil 是 macOS 内置，其他平台不入链。
func TestBuildEngineChainTextutilOnlyOnDarwin(t *testing.T) {
	for _, step := range buildEngineChain(".doc", "linux") {
		if step.engine == engineTextutil {
			t.Error("非 darwin 平台不该含 textutil")
		}
	}
	for _, step := range buildEngineChain(".doc", "windows") {
		if step.engine == engineTextutil {
			t.Error("windows 平台不该含 textutil")
		}
	}
}

// ── extractDocumentText：成功路径 ───────────────────────────────────────

func TestExtractDocumentTextPDFSuccess(t *testing.T) {
	r := &fakeRunner{stdout: map[string]string{"pdftotext": "  抽取的正文  \n"}}
	got := extractDocumentText("/x/a.pdf", docExtractDeps{Runner: r.run, Platform: "darwin"})

	if !got.OK {
		t.Fatalf("应成功，实得 %+v", got)
	}
	if got.Text != "抽取的正文" {
		t.Errorf("应 trim，实得 %q", got.Text)
	}
	if got.Engine != enginePdftotext {
		t.Errorf("引擎应为 pdftotext，实得 %q", got.Engine)
	}
	// 参数对账：`-layout <file> -`
	if len(r.calls) != 1 {
		t.Fatalf("应调 1 次，实得 %d", len(r.calls))
	}
	wantArgs := []string{"-layout", "/x/a.pdf", "-"}
	for i, a := range wantArgs {
		if r.calls[0].args[i] != a {
			t.Errorf("args[%d] 应为 %q，实得 %q", i, a, r.calls[0].args[i])
		}
	}
	if r.calls[0].timeoutMs != 60_000 {
		t.Errorf("超时应 60s，实得 %d", r.calls[0].timeoutMs)
	}
}

// ── fail-open（★ 核心理念）──────────────────────────────────────────────

// TestExtractDocumentTextFailOpenNoEngine —— **全引擎缺失 → ok=false + 建议，不返回 error**。
//
// 对账 TS 文件头：「Fail-open: when no engine is available (or all fail),
// callers keep the raw file and surface an install suggestion —
// extraction never blocks an import.」
func TestExtractDocumentTextFailOpenNoEngine(t *testing.T) {
	r := &fakeRunner{failOn: map[string]error{
		"pdftotext": errors.New("exec: \"pdftotext\": executable file not found in $PATH"),
	}}
	got := extractDocumentText("/x/a.pdf", docExtractDeps{Runner: r.run, Platform: "darwin"})

	if got.OK {
		t.Fatal("无引擎应 ok=false")
	}
	// **建议必须给出安装指引**（对账 TS：`INSTALL_SUGGESTIONS['.pdf']`）
	if !strings.Contains(got.Suggestion, "poppler") {
		t.Errorf("pdf 的建议应含 poppler，实得 %q", got.Suggestion)
	}
	if !strings.Contains(got.Suggestion, "Text extraction unavailable") {
		t.Errorf("应含统一前缀，实得 %q", got.Suggestion)
	}
	// 失败原因应被记录
	if !strings.Contains(got.Suggestion, "pdftotext") {
		t.Errorf("应记录失败的引擎名，实得 %q", got.Suggestion)
	}
}

// TestExtractDocumentTextUnknownExtension —— 链为空 → 「无已知引擎」建议。
func TestExtractDocumentTextUnknownExtension(t *testing.T) {
	r := &fakeRunner{}
	got := extractDocumentText("/x/a.unknown", docExtractDeps{Runner: r.run, Platform: "darwin"})

	if got.OK {
		t.Fatal("未知扩展名应 ok=false")
	}
	if !strings.Contains(got.Suggestion, "No extraction engine known") {
		t.Errorf("实得 %q", got.Suggestion)
	}
	if len(r.calls) != 0 {
		t.Error("链为空时不该调任何命令")
	}
}

// TestExtractDocumentTextDefaultSuggestionForExtWithoutCustom —— 用默认建议。
func TestExtractDocumentTextDefaultSuggestionForExtWithoutCustom(t *testing.T) {
	r := &fakeRunner{failOn: map[string]error{"soffice": errors.New("boom"),
		"libreoffice": errors.New("boom")}}
	got := extractDocumentText("/x/a.pptx", docExtractDeps{Runner: r.run, Platform: "darwin"})

	if got.OK {
		t.Fatal("应失败")
	}
	// .pptx 在 INSTALL_SUGGESTIONS 里有专属条目
	if !strings.Contains(got.Suggestion, "LibreOffice") {
		t.Errorf("实得 %q", got.Suggestion)
	}
}

// ── 引擎链 fallthrough ──────────────────────────────────────────────────

// TestExtractDocumentTextFallsThroughOnEmptyOutput —— **产出空文本 → 推进到下一引擎**。
//
// 对账 TS：`failures.push(\`${step.engine}: produced empty output\`); continue`
func TestExtractDocumentTextFallsThroughOnEmptyOutput(t *testing.T) {
	r := &fakeRunner{
		stdout: map[string]string{"textutil": "   \n  "}, // 只有空白 → trim 后为空
		failOn: map[string]error{"soffice": errors.New("not installed")},
	}
	// 让 pandoc 成功
	r.stdout["pandoc"] = "pandoc 产出"

	got := extractDocumentText("/x/a.docx", docExtractDeps{Runner: r.run, Platform: "darwin"})
	if !got.OK {
		t.Fatalf("应落到 pandoc 成功，实得 %+v", got)
	}
	if got.Engine != enginePandoc {
		t.Errorf("引擎应为 pandoc，实得 %q", got.Engine)
	}
	if got.Text != "pandoc 产出" {
		t.Errorf("实得 %q", got.Text)
	}
	// 三个引擎都该被试过。**注意**：soffice 步骤内部还会试 `libreoffice`，
	// 故实际调用数是 4（textutil + soffice + libreoffice + pandoc）——
	// 对账 TS 同款行为（`runSoffice` 的双二进制循环）。
	if len(r.calls) != 4 {
		t.Errorf("应试 4 次（3 引擎 + soffice 的 libreoffice 回退），实得 %d", len(r.calls))
	}
	// 引擎名序列应证明「每个都试过」
	binaries := []string{}
	for _, c := range r.calls {
		binaries = append(binaries, c.binary)
	}
	want := []string{"textutil", "soffice", "libreoffice", "pandoc"}
	for i := range want {
		if binaries[i] != want[i] {
			t.Errorf("调用序列[%d] 应为 %q，实得 %q（全序列 %v）", i, want[i], binaries[i], binaries)
		}
	}
}

// TestExtractDocumentTextFallsThroughOnError —— 命令报错 → 推进到下一引擎。
func TestExtractDocumentTextFallsThroughOnError(t *testing.T) {
	r := &fakeRunner{
		failOn: map[string]error{"textutil": errors.New("boom"), "soffice": errors.New("boom")},
		stdout: map[string]string{"pandoc": "最终产出"},
	}
	got := extractDocumentText("/x/a.docx", docExtractDeps{Runner: r.run, Platform: "darwin"})
	if !got.OK || got.Engine != enginePandoc {
		t.Errorf("应落到 pandoc，实得 %+v", got)
	}
}

// ── soffice 的特殊路径 ──────────────────────────────────────────────────

// TestRunSofficeTriesBothBinaries —— **soffice 失败后试 libreoffice**。
//
// 对账 TS 注释：「Some distros ship only `libreoffice` (no `soffice` symlink)」
func TestRunSofficeTriesBothBinaries(t *testing.T) {
	tmp := t.TempDir()
	// 预置「转换产物」——readFile 会读它
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("soffice 产出"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{
		failOn: map[string]error{"soffice": errors.New("not found")},
		outDir: tmp,
	}
	// 用固定临时目录：走 TempDir 依赖注入
	deps := docExtractDeps{Runner: r.run, Platform: "linux", TempDir: tmp}

	// 直接构造一个「输出目录 == tmp」的场景不方便（MkdirTemp 会新建子目录）。
	// 改为断言：两个二进制都被试过，且最终因读不到产物而失败。
	got := extractDocumentText("/x/a.pptx", deps)
	if got.OK {
		t.Fatalf("读不到产物应失败，实得 %+v", got)
	}
	// 两个二进制都应被调用
	binaries := []string{}
	for _, c := range r.calls {
		binaries = append(binaries, c.binary)
	}
	if len(binaries) != 2 || binaries[0] != "soffice" || binaries[1] != "libreoffice" {
		t.Errorf("应依次试 soffice → libreoffice，实得 %v", binaries)
	}
	// 参数对账
	args := strings.Join(r.calls[0].args, " ")
	if !strings.Contains(args, "--headless") || !strings.Contains(args, "--convert-to") ||
		!strings.Contains(args, "txt:Text") || !strings.Contains(args, "--outdir") {
		t.Errorf("soffice 参数不符：%v", r.calls[0].args)
	}
	if r.calls[0].timeoutMs != 90_000 {
		t.Errorf("soffice 超时应 90s，实得 %d", r.calls[0].timeoutMs)
	}
}

// TestRunSofficeReadsOutputFile —— soffice 成功后**从 outdir 读产物**。
//
// 这是 soffice 与其余引擎的关键差异：它没有 stdout 模式。
func TestRunSofficeReadsOutputFile(t *testing.T) {
	base := t.TempDir()
	// 让 fakeRunner 在被调时把产物写进「当前 outdir」——
	// 用闭包捕获 outdir 不可行（MkdirTemp 内部创建），改用「写进 base 下所有刚建的目录」。
	runner := func(binary string, args []string, timeoutMs int) (string, error) {
		// args 形如 [... "--outdir", <dir>, filePath]
		outDir := ""
		for i, a := range args {
			if a == "--outdir" && i+1 < len(args) {
				outDir = args[i+1]
			}
		}
		if outDir == "" {
			return "", errors.New("no --outdir")
		}
		// 对账 TS：输出文件名为 `<stem>.txt`
		if err := os.WriteFile(filepath.Join(outDir, "slides.txt"), []byte("幻灯片正文"), 0o600); err != nil {
			return "", err
		}
		return "", nil
	}

	got := extractDocumentText("slides.pptx", docExtractDeps{
		Runner: runner, Platform: "linux", TempDir: base,
	})
	if !got.OK {
		t.Fatalf("应成功，实得 %+v", got)
	}
	if got.Text != "幻灯片正文" {
		t.Errorf("实得 %q", got.Text)
	}
	if got.Engine != engineSoffice {
		t.Errorf("引擎应为 soffice，实得 %q", got.Engine)
	}
}

// TestRunSofficeCleansTempDir —— 临时目录必须被清理（对账 TS 的 finally）。
func TestRunSofficeCleansTempDir(t *testing.T) {
	base := t.TempDir()
	runner := func(binary string, args []string, timeoutMs int) (string, error) {
		return "", errors.New("boom")
	}
	_ = extractDocumentText("a.pptx", docExtractDeps{
		Runner: runner, Platform: "linux", TempDir: base,
	})

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("临时目录应被清空，实得残留：%v", names)
	}
}

// ── stripLastExt ────────────────────────────────────────────────────────

func TestStripLastExt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.pdf", "a"},
		{"a.tar.gz", "a.tar"},
		{"noext", "noext"},
		{"a.docx", "a"},
		{"多字节文档.pdf", "多字节文档"},
	}
	for _, c := range cases {
		if got := stripLastExt(c.in); got != c.want {
			t.Errorf("stripLastExt(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// ── describeExtractErr ──────────────────────────────────────────────────

// TestDescribeExtractErrENOENT —— 对账 TS：`code === 'ENOENT' ? 'not installed' : message`。
func TestDescribeExtractErrENOENT(t *testing.T) {
	// os.ErrNotExist 走「not installed」
	got := describeExtractErr(os.ErrNotExist)
	if got != "not installed" {
		t.Errorf("ENOENT 应渲染为 not installed，实得 %q", got)
	}
	// 包装过的 fs.ErrNotExist 也应识别
	wrapped := &os.PathError{Op: "fork/exec", Path: "pdftotext", Err: os.ErrNotExist}
	if got := describeExtractErr(wrapped); got != "not installed" {
		t.Errorf("包装的 ENOENT 应识别，实得 %q", got)
	}
	// 其他错误保留原文
	if got := describeExtractErr(errors.New("boom")); got != "boom" {
		t.Errorf("实得 %q", got)
	}
}

// ── 常量对账 ────────────────────────────────────────────────────────────

// TestExtractionCaveatMatchesTS —— EXTRACTION_CAVEAT **逐字对账**（进模型上下文）。
func TestExtractionCaveatMatchesTS(t *testing.T) {
	for _, want := range []string{
		"[extracted-text] Converted from a binary document",
		"layout may be lossy (tables, multi-column, figures)",
		`Do not base negative conclusions ("X is not in the document") on this text alone`,
		"consult the original file.",
	} {
		if !strings.Contains(extractionCaveat, want) {
			t.Errorf("EXTRACTION_CAVEAT 应含 %q\n实得：%s", want, extractionCaveat)
		}
	}
}

// TestInstallSuggestionsCoverTSKeys —— 对账 TS `INSTALL_SUGGESTIONS` 的三个键。
func TestInstallSuggestionsCoverTSKeys(t *testing.T) {
	for _, ext := range []string{".pdf", ".pptx", ".odp"} {
		if _, ok := installSuggestions[ext]; !ok {
			t.Errorf("INSTALL_SUGGESTIONS 应含 %q", ext)
		}
	}
	if !strings.Contains(defaultSuggestion, "LibreOffice") {
		t.Errorf("默认建议应介绍 LibreOffice，实得 %q", defaultSuggestion)
	}
}
