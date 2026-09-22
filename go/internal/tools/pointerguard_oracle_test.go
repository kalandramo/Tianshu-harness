package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointerguard_oracle_test.go —— 指针守卫的差分对账。
//
// oracle 由 `testdata/plan/gen-oracle.ts` 的 `pointer` 段产出（tsx 真跑
// TS `detectPointerPlaceholder`）。

const pointerOraclePath = "../../testdata/plan/oracle.json"

type pointerOracleCase struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

func loadPointerOracle(t *testing.T) []pointerOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(pointerOraclePath))
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v", err)
	}
	var all struct {
		Pointer []pointerOracleCase `json:"pointer"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(all.Pointer) == 0 {
		t.Fatal("oracle 无 pointer 用例")
	}
	return all.Pointer
}

// TestOracleDetectPointerPlaceholder —— 逐例对账。
func TestOracleDetectPointerPlaceholder(t *testing.T) {
	cases := loadPointerOracle(t)
	for _, c := range cases {
		got := DetectPointerPlaceholder(c.Input)
		if got != c.Output {
			t.Errorf("DetectPointerPlaceholder(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	t.Logf("pointer 对账 %d 例", len(cases))
}

// TestPointerPrefixesExact —— 6 个前缀必须**逐字**相同。
//
// 模型模仿的是字面文本，改一个字符守卫即失效。
func TestPointerPrefixesExact(t *testing.T) {
	want := []string{
		"[file written to",
		"[edit on",
		"[hash_edit applied to",
		"[patch applied to",
		"[new block",
		"[plan persisted to",
	}
	if len(PointerPlaceholderPrefixes) != len(want) {
		t.Fatalf("前缀数不符：want %d got %d", len(want), len(PointerPlaceholderPrefixes))
	}
	for i, w := range want {
		if PointerPlaceholderPrefixes[i] != w {
			t.Errorf("前缀[%d] want %q got %q", i, w, PointerPlaceholderPrefixes[i])
		}
	}
}

// TestPointerMarkerPhrasesGate —— **二级守卫**：仅以前缀开头但无标记短语的
// 真实内容**不得**被拒。
//
// 这是 TS 注释明确的设计意图（"real content which merely happens to start
// with the same bracketed prefix is not rejected"）。
func TestPointerMarkerPhrasesGate(t *testing.T) {
	// 以前缀开头但无标记短语 → 不拒。
	realContent := "[file written to src/a.ts] 这是模型真的想写的内容"
	if got := DetectPointerPlaceholder(realContent); got != "" {
		t.Errorf("无标记短语的真实内容不应被拒，实得 %q", got)
	}
	// 含标记短语 → 拒。
	fake := "[file written to src/a.ts] " + PointerInternalTag
	if got := DetectPointerPlaceholder(fake); got == "" {
		t.Error("含标记短语应被拒")
	}
}

// TestPointerLineScanCatchesMultiLineEcho —— **逐行扫描**会命中「首行是完整
// 指针 + 后面跟真实内容」的形态。
//
// 对账 TS：`detectPointerPlaceholder` 的整值快路径失败后，**逐行**再扫一遍
// ——真实指针永远是单行，若任一整行匹配即拒。
//
// **注意与 `TestPointerSingleLineOnly` 的区别**：那条测的是「同一行内**含换行**」
// （前缀 + 标记 + 同行内还有内容），此条测的是「首行**本身**就是完整指针，
// 换行在标记之后」。TS 对前者放行、对后者拒绝。
func TestPointerLineScanCatchesMultiLineEcho(t *testing.T) {
	// 首行是完整指针（含标记短语，行内无换行），第二行是真实内容 → 拒。
	multi := "[file written to x] Display placeholder\nreal second line"
	if got := DetectPointerPlaceholder(multi); got == "" {
		t.Error("首行为完整指针时应被拒（逐行扫描命中）")
	}
}

// TestPointerSingleLineOnly —— **行内**含换行的前缀模仿不得被拒。
//
// 对账 TS `detectPointerPlaceholderInLine`：单行判定要求 `line` 内**不含**
// `\n`/`\r`——含换行说明模型在前缀模仿之后追加了真实内容。
//
// **注意**：此断言作用于**传给单行判定的那个字符串**。`DetectPointerPlaceholder`
// 会先按 `\r?\n` 切分再逐行判定，故「行内含换行」只在整值快路径上可达。
func TestPointerSingleLineOnly(t *testing.T) {
	// 整值：前缀 + 标记 + 换行 + 真实内容。整值快路径因含换行放弃，
	// 但逐行扫描会命中首行 → 最终仍拒（与 TS 一致）。
	//
	// 故这里直接测单行判定函数本身（包内可见）。
	lineWithNewline := "[file written to src/a.ts] " + PointerInternalTag + "\nreal"
	if got := detectPointerPlaceholderInLine(lineWithNewline); got != "" {
		t.Errorf("行内含换行时单行判定应放弃，实得 %q", got)
	}
	// 同一内容若把换行换成空格 → 单行判定命中。
	lineNoNewline := "[file written to src/a.ts] " + PointerInternalTag + " real"
	if got := detectPointerPlaceholderInLine(lineNoNewline); got == "" {
		t.Error("行内无换行时单行判定应命中")
	}
}

// TestPointerErrorMarkerPresent —— 拒绝文案含稳定标记（hook 靠它计数）。
func TestPointerErrorMarkerPresent(t *testing.T) {
	msg := PointerPlaceholderError("write_file", "content", "[file written to", "src/a.ts")
	if !strings.Contains(msg, PointerGuardErrorMarker) {
		t.Errorf("错误文案应含标记 %q", PointerGuardErrorMarker)
	}
	if !strings.Contains(msg, "[file written to") {
		t.Error("错误文案应含匹配到的前缀")
	}
}

// TestResolveIdempotentPointerFullMode —— write_file 模式的幂等化解。
//
// **关键**：只有磁盘状态与指针记录自洽才化解为成功。
//
// **行数语义**：`lines = split('\n').length`——尾换行会**多算一行**。
// 故 "a\nb\nc\n" 是 **4** 行（不是 3）。这是 TS 的既有语义（对账
// `pointer-guard.ts` 的 `onDisk.split('\n').length`），照抄。
func TestResolveIdempotentPointerFullMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	content := "line1\nline2\nline3\n" // split('\n') → 4 元素
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := len(strings.Split(content, "\n"))
	if lines != 4 {
		t.Fatalf("前置假设错误：split 应得 4，实得 %d", lines)
	}
	ptr := "[file written to " + target + " — 4 lines, 18 chars] " + PointerInternalTag

	got, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode:          ResolveModeFull,
		FilePath:      target,
		Value:         ptr,
		MatchedPrefix: WriteFilePointerPrefix,
	})
	if !ok {
		t.Fatalf("磁盘自洽应化解为幂等成功")
	}
	if !strings.Contains(got, "幂等成功") {
		t.Errorf("文案应说明幂等成功，实得 %q", got)
	}
}

// TestResolveIdempotentPointerCharsTolerance —— chars 容差 = 行数。
//
// 对账 TS：`Math.abs(onDisk.length - wantChars) > wantLines` 才失败——
// 容差恰为行数（容忍 CRLF↔LF 转换的字节差）。
func TestResolveIdempotentPointerCharsTolerance(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	content := "aa\nbb\n" // 6 字节，split → 3 行
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// chars 偏差 = 3（恰等于行数 3）→ 仍在容差内（> 才失败）。
	ptrOK := "[file written to " + target + " — 3 lines, 9 chars] " + PointerInternalTag
	if _, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode: ResolveModeFull, FilePath: target, Value: ptrOK, MatchedPrefix: WriteFilePointerPrefix,
	}); !ok {
		t.Error("偏差 == 行数时应化解（TS 用 `>` 而非 `>=`）")
	}

	// chars 偏差 = 4（> 行数 3）→ 超容差，不化解。
	ptrBad := "[file written to " + target + " — 3 lines, 10 chars] " + PointerInternalTag
	if _, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode: ResolveModeFull, FilePath: target, Value: ptrBad, MatchedPrefix: WriteFilePointerPrefix,
	}); ok {
		t.Error("偏差 > 行数时不得化解")
	}
}

// TestResolveIdempotentPointerFullModeMismatch —— **反证**：磁盘不一致时不化解。
//
// 没有这条无法排除「实现无条件化解为成功」——那会把真实错误吞成成功。
func TestResolveIdempotentPointerFullModeMismatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 指针声称 99 lines —— 与磁盘（1 行）不符。
	ptr := "[file written to " + target + " — 99 lines, 999 chars] " + PointerInternalTag

	if _, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode:          ResolveModeFull,
		FilePath:      target,
		Value:         ptr,
		MatchedPrefix: WriteFilePointerPrefix,
	}); ok {
		t.Error("磁盘行数不符时**不得**化解为成功（会把真实错误吞掉）")
	}
}

// TestResolveIdempotentPointerPathMismatch —— 路径不一致时不化解。
func TestResolveIdempotentPointerPathMismatch(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(b, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ptr := "[file written to " + a + " — 1 lines, 2 chars] " + PointerInternalTag

	if _, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode:          ResolveModeEdit,
		FilePath:      b, // 目标是 b，指针指向 a
		Value:         ptr,
		MatchedPrefix: WriteFilePointerPrefix,
	}); ok {
		t.Error("路径不一致时不得化解")
	}
}

// TestResolveIdempotentPointerFileMissing —— 文件不存在时不化解。
func TestResolveIdempotentPointerFileMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.txt")
	ptr := "[file written to " + missing + " — 1 lines, 2 chars] " + PointerInternalTag

	if _, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode:          ResolveModeEdit,
		FilePath:      missing,
		Value:         ptr,
		MatchedPrefix: WriteFilePointerPrefix,
	}); ok {
		t.Error("文件不存在时不得化解")
	}
}

// TestResolveIdempotentPointerEditMode —— edit 模式只校验路径 + 存在。
func TestResolveIdempotentPointerEditMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.ts")
	if err := os.WriteFile(target, []byte("const a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ptr := "[edit on " + target + ": replaced 3 lines] " + PointerInternalTag

	got, ok := ResolveIdempotentPointer(IdempotentResolveInput{
		Mode:          ResolveModeEdit,
		FilePath:      target,
		Value:         ptr,
		MatchedPrefix: EditFilePointerPrefix,
	})
	if !ok {
		t.Fatal("edit 模式路径一致 + 文件存在应化解")
	}
	if !strings.Contains(got, "幂等") {
		t.Errorf("文案应说明幂等，实得 %q", got)
	}
}

// TestPathsMatchForCompareDriveLetter —— 盘符大小写不敏感（Windows 特化）。
//
// 对账 TS `canonicalizePathForCompare`：盘符形路径整体小写。
// 逐字节比较会误拒活动计划文件的写入（VSCode 给小写盘符、cwd 给大写）。
func TestPathsMatchForCompareDriveLetter(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`C:\proj\a.ts`, `c:/proj/a.ts`, true},
		{`D:\x\y.ts`, `d:\x\y.ts`, true},
		{`/home/u/a.ts`, `/home/u/a.ts`, true},
		{`/home/u/A.ts`, `/home/u/a.ts`, false}, // 非盘符形：大小写敏感
		{`C:\a.ts`, `D:\a.ts`, false},
	}
	for _, c := range cases {
		if got := PathsMatchForCompare(c.a, c.b); got != c.want {
			t.Errorf("PathsMatchForCompare(%q, %q) want %v got %v", c.a, c.b, c.want, got)
		}
	}
}
