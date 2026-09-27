package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// docextract.go —— 二进制办公文档的文本抽取（第一百刀 · W2）。
//
// 对账 TS `src/tools/doc-extract.ts`（271 行）。
//
// # 做什么
//
// 把导入的二进制办公文档（PDF/DOCX/PPTX/…）**经系统工具链**转成可读文本，
// 使内容能作为上下文使用，而不只是磁盘上的一个 blob。
//
// # 引擎链（按扩展名分派，对账 TS `buildEngineChain`）
//
//	.pdf                → pdftotext
//	.docx/.odt/.rtf     → textutil（仅 macOS）→ soffice → pandoc
//	.doc                → textutil（仅 macOS）→ soffice（pandoc 读不了旧 .doc）
//	.pptx/.odp          → soffice
//	.xlsx / .xls / .ods → soffice
//
// # ★ fail-open（对账 TS 文件头）
//
// 无引擎可用（或全部失败）时**不阻断导入**——返回 `ok=false` + 安装建议，
// 调用方保留原文件并展示建议。**抽取失败永远不冒泡成 error**。
//
// # ★ EXTRACTION_CAVEAT 随文本走
//
// 抽取是**有损**的（表格、多栏、图）。标记必须随文本一起返回，让下游读者
// 看到「勿据此下否定结论」的纪律——对账 TS 注释的「反证 1: 抽取质量」。
//
// # 与 TS 的差异（诚实披露）
//
// **纯 JS 兜底引擎不做**：TS 在 pdftotext 缺失时回退 `pdfjs-dist`（`:89`），
// xlsx 走 `exceljs`（`:143`）。两者都是 npm 包，Go 侧无对应物，
// 且引入它们会与「go.mod 零重依赖」的约束冲突。故 Go 侧只有**系统命令**这一条路
// ——无引擎时按 fail-open 返回建议。

// extractionCaveat 对账 TS `EXTRACTION_CAVEAT`（逐字，随文本进模型上下文）。
const extractionCaveat = "[extracted-text] Converted from a binary document — " +
	"layout may be lossy (tables, multi-column, figures). " +
	`Do not base negative conclusions ("X is not in the document") on this text alone; ` +
	"consult the original file."

// docExtractEngine 是对账 TS `ExtractEngine` 的标识集合。
type docExtractEngine string

const (
	enginePdftotext docExtractEngine = "pdftotext"
	engineTextutil  docExtractEngine = "textutil"
	enginePandoc    docExtractEngine = "pandoc"
	engineSoffice   docExtractEngine = "soffice"
)

// docExtractResult 对账 TS `DocExtractResult`（success / failure 两态合一）。
type docExtractResult struct {
	OK         bool
	Text       string
	Engine     docExtractEngine
	Suggestion string // 仅 OK=false 时有值
}

// extractableExts 对账 TS `EXTRACTABLE`（`:38`）。
//
// **注意**：`.xlsx`/`.xls`/`.ods` 在 TS 侧走 exceljs/soffice，Go 侧只保留 soffice。
var extractableExts = map[string]bool{
	".pdf":  true,
	".docx": true,
	".doc":  true,
	".rtf":  true,
	".odt":  true,
	".pptx": true,
	".odp":  true,
	".xlsx": true,
	".xls":  true,
	".ods":  true,
}

// isExtractableDocument 对账 TS `isExtractableDocument`（`:40`）。
func isExtractableDocument(filePath string) bool {
	return extractableExts[strings.ToLower(filepath.Ext(filePath))]
}

// commandRunner 对账 TS `CommandRunner`——**可注入**，便于测试不打真实命令。
//
// 对账 TS 注释：「Command runner — injectable for tests.」
type commandRunner func(binary string, args []string, timeoutMs int) (stdout string, err error)

// defaultCommandRunner 是生产 runner（对账 TS `defaultRunner`）。
//
// **maxBuffer**：TS 设 32MB 上限。Go 侧用 `cmd.Output()` 收集 stdout，
// 超限场景罕见（文本抽取产物不会那么大），故不再另设上限——
// 但**设了超时**（TS 同样设）。
func defaultCommandRunner(binary string, args []string, timeoutMs int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// docExtractDeps 是抽取的可注入依赖（对账 TS 的 `deps` 参数）。
type docExtractDeps struct {
	Runner commandRunner
	// Platform 对账 TS 的 `deps.platform ?? process.platform`（测试注入用）。
	Platform string
	// TempDir 覆盖临时目录（测试用）；空则用 os.TempDir()。
	TempDir string
}

// buildEngineChain 对账 TS `buildEngineChain`（`:189-218`）。
//
// **返回的是「引擎名 + 执行函数」的有序列表**。Go 侧用闭包承载执行体，
// 与 TS 的 `EngineStep{engine, run}` 同构。
func buildEngineChain(ext string, platform string) []docExtractStep {
	textutilAvailable := platform == "darwin"

	switch ext {
	case ".pdf":
		// 对账 TS：`[pdftotext, pdfjs]`——Go 侧无 pdfjs，只剩 pdftotext。
		return []docExtractStep{{engine: enginePdftotext, run: runPdftotext}}
	case ".docx", ".odt", ".rtf":
		steps := []docExtractStep{}
		if textutilAvailable {
			steps = append(steps, docExtractStep{engine: engineTextutil, run: runTextutil})
		}
		return append(steps,
			docExtractStep{engine: engineSoffice, run: runSoffice},
			docExtractStep{engine: enginePandoc, run: runPandoc})
	case ".doc":
		// 对账 TS 注释：pandoc 读不了旧 .doc
		steps := []docExtractStep{}
		if textutilAvailable {
			steps = append(steps, docExtractStep{engine: engineTextutil, run: runTextutil})
		}
		return append(steps, docExtractStep{engine: engineSoffice, run: runSoffice})
	case ".pptx", ".odp":
		return []docExtractStep{{engine: engineSoffice, run: runSoffice}}
	case ".xlsx", ".xls", ".ods":
		// 对账 TS：`.xlsx` 是 `[exceljs, soffice]`、`.xls`/`.ods` 只有 `[soffice]`。
		// Go 侧无 exceljs → 三者统一走 soffice（**有意收窄**，已在文件头披露）。
		return []docExtractStep{{engine: engineSoffice, run: runSoffice}}
	default:
		return nil
	}
}

// docExtractStep 对账 TS `EngineStep`。
type docExtractStep struct {
	engine docExtractEngine
	run    func(filePath string, runner commandRunner, deps docExtractDeps) (string, error)
}

// runPdftotext 对账 TS `runPdftotext`（`:66-69`）：`pdftotext -layout <file> -`
//
// `-` 表示输出到 stdout。
func runPdftotext(filePath string, runner commandRunner, _ docExtractDeps) (string, error) {
	return runner("pdftotext", []string{"-layout", filePath, "-"}, 60_000)
}

// runTextutil 对账 TS `runTextutil`（`:71-74`）：
// `textutil -convert txt -stdout <file>`（macOS 内置）
func runTextutil(filePath string, runner commandRunner, _ docExtractDeps) (string, error) {
	return runner("textutil", []string{"-convert", "txt", "-stdout", filePath}, 60_000)
}

// runPandoc 对账 TS `runPandoc`（`:76-79`）：`pandoc -t plain <file>`
func runPandoc(filePath string, runner commandRunner, _ docExtractDeps) (string, error) {
	return runner("pandoc", []string{"-t", "plain", filePath}, 60_000)
}

// runSoffice 对账 TS `runSoffice`（`:118-134`）。
//
// # 为什么不能像其他引擎那样读 stdout
//
// 对账 TS 注释：「soffice writes the converted file into an outdir (no stdout mode)」
// ——LibreOffice **没有 stdout 模式**，必须给一个输出目录再读文件。
//
// # 为什么试两个二进制
//
// 对账 TS 注释：「Some distros ship only `libreoffice` (no `soffice` symlink)」
// ——部分发行版只有 `libreoffice` 而没有 `soffice` 别名，故两个都试。
//
// **清理语义**：临时目录在 `finally` 里删（TS 用 `rm(..., {recursive, force})`）。
// Go 侧用 `defer os.RemoveAll`。
func runSoffice(filePath string, runner commandRunner, deps docExtractDeps) (string, error) {
	baseDir := deps.TempDir
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	outDir, err := os.MkdirTemp(baseDir, "rivet-extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(outDir)

	var lastErr error
	for _, binary := range []string{"soffice", "libreoffice"} {
		_, err := runner(binary,
			[]string{"--headless", "--convert-to", "txt:Text", "--outdir", outDir, filePath},
			90_000)
		if err != nil {
			lastErr = err
			continue
		}
		// 对账 TS：`basename(filePath).replace(/\.[^.]+$/, '')`——剥最后一个扩展名
		stem := stripLastExt(filepath.Base(filePath))
		data, err := os.ReadFile(filepath.Join(outDir, stem+".txt"))
		if err != nil {
			lastErr = err
			continue
		}
		return string(data), nil
	}
	if lastErr == nil {
		// 理论上不可达（循环至少跑一次），但不留 nil error 让调用方误判成功
		lastErr = errNoExtractionEngine
	}
	return "", lastErr
}

// stripLastExt 对账 TS 的 `.replace(/\.[^.]+$/, ”)`——剥**最后一个**扩展名。
//
// **与 `filepath.Ext` 的差异**：`filepath.Ext("a.tar.gz")` = `.gz`，
// 而 TS 的正则 `\.[^.]+$` 同样只匹配最后一段——两者一致。
// 但 `filepath.Ext(".bashrc")` = `.bashrc`（Go 视整段为扩展名），
// TS 的 `\.[^.]+$` 也匹配 `.bashrc`——若 `stem` 为空则输出名会是 `.txt`。
// 此处与 TS 行为一致，不做额外防御。
func stripLastExt(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// installSuggestions 对账 TS `INSTALL_SUGGESTIONS`（`:222-226`）+ `DEFAULT_SUGGESTION`（`:228`）。
var installSuggestions = map[string]string{
	".pdf": "Install poppler for PDF extraction (macOS: brew install poppler; " +
		"Linux: apt install poppler-utils; Windows: winget install poppler).",
	".pptx": "Install LibreOffice for slide text extraction (macOS: brew install --cask libreoffice; " +
		"Linux: apt install libreoffice; Windows: winget install LibreOffice.LibreOffice).",
	".odp": "Install LibreOffice for slide text extraction (macOS: brew install --cask libreoffice; " +
		"Linux: apt install libreoffice; Windows: winget install LibreOffice.LibreOffice).",
}

// defaultSuggestion 对账 TS `DEFAULT_SUGGESTION`。
const defaultSuggestion = "Install LibreOffice (soffice) or pandoc for document text extraction " +
	"(macOS: brew install --cask libreoffice; Linux: apt install libreoffice; " +
	"Windows: winget install LibreOffice.LibreOffice)."

// extractDocumentText 从二进制文档抽取纯文本。
//
// 对账 TS `extractDocumentText`（`:236-270`）：
//
//  1. 按扩展名取引擎链；**空链 → 立即返回 ok=false**（含「无已知引擎」建议）
//  2. 依次试各引擎：**ENOENT（二进制缺失）与转换失败都推进到下一个**
//  3. 产出的文本 `trim()` 后为空 → 记 `produced empty output` 并继续
//  4. 全部失败 → `ok=false` + 对应安装建议（**绝不返回 error**）
func extractDocumentText(filePath string, deps docExtractDeps) docExtractResult {
	ext := strings.ToLower(filepath.Ext(filePath))
	platform := deps.Platform
	if platform == "" {
		platform = currentGOOS()
	}
	chain := buildEngineChain(ext, platform)
	if len(chain) == 0 {
		return docExtractResult{
			OK:         false,
			Suggestion: "No extraction engine known for " + ext + " files.",
		}
	}

	runner := deps.Runner
	if runner == nil {
		runner = defaultCommandRunner
	}

	failures := []string{}
	for _, step := range chain {
		text, err := step.run(filePath, runner, deps)
		if err != nil {
			failures = append(failures, string(step.engine)+": "+describeExtractErr(err))
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			failures = append(failures, string(step.engine)+": produced empty output")
			continue
		}
		return docExtractResult{OK: true, Text: trimmed, Engine: step.engine}
	}

	suggestion, ok := installSuggestions[ext]
	if !ok {
		suggestion = defaultSuggestion
	}
	return docExtractResult{
		OK: false,
		Suggestion: "Text extraction unavailable (" + strings.Join(failures, "; ") +
			"). " + suggestion,
	}
}

// describeExtractErr 对账 TS：`code === 'ENOENT' ? 'not installed' : message`。
func describeExtractErr(err error) string {
	if err == nil {
		return ""
	}
	// Go 侧 ENOENT 用 os.IsNotExist 或 errors.Is(err, exec.ErrNotFound)
	if os.IsNotExist(err) || err == exec.ErrNotFound {
		return "not installed"
	}
	return err.Error()
}

// currentGOOS 返回当前平台（对账 TS 的 `process.platform`）。
//
// **注意**：Node 的 `process.platform` 与 Go 的 `runtime.GOOS` 取值一致
// （`darwin`/`windows`/`linux`）——`openpath.go:133` 已有同款说明。
func currentGOOS() string { return runtime.GOOS }

// errNoExtractionEngine 是 soffice 双二进制都失败且 lastErr 为 nil 时的兜底。
var errNoExtractionEngine = os.ErrNotExist
