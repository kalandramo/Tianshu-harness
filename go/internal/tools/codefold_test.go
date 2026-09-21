package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// codeFoldOracleCase 对账 TS `code-fold.ts`（428 行，五分支）的黄金数据。
//
// 生成：`node_modules/.bin/tsx go/testdata/codefold/gen_oracle.ts`
// **真跑 TS 原实现**（foldCode 无外部依赖）。
type codeFoldOracleCase struct {
	Label         string   `json:"label"`
	FilePath      string   `json:"filePath"`
	Content       string   `json:"content"`
	MaxLines      int      `json:"maxLines"`
	Folded        string   `json:"folded"`
	OriginalLines int      `json:"originalLines"`
	FoldedLines   int      `json:"foldedLines"`
	Signatures    []string `json:"signatures"`
	WasFolded     bool     `json:"wasFolded"`
}

func loadCodeFoldOracle(t *testing.T) []codeFoldOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "codefold", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []codeFoldOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空")
	}
	return cases
}

// TestCodeFoldOracle 逐例对账（**全字段**：folded/originalLines/foldedLines/
// signatures/wasFolded）。
func TestCodeFoldOracle(t *testing.T) {
	cases := loadCodeFoldOracle(t)
	for i, c := range cases {
		got := FoldCode(c.Content, FoldOptions{FilePath: c.FilePath, MaxLines: c.MaxLines, MaxLinesSet: true})

		if got.Folded != c.Folded {
			t.Errorf("用例 %d (%s) folded 不符：\n期望 %.400q\n实得 %.400q",
				i, c.Label, c.Folded, got.Folded)
		}
		if got.OriginalLines != c.OriginalLines {
			t.Errorf("用例 %d (%s) originalLines 不符：期望 %d，实得 %d",
				i, c.Label, c.OriginalLines, got.OriginalLines)
		}
		if got.FoldedLines != c.FoldedLines {
			t.Errorf("用例 %d (%s) foldedLines 不符：期望 %d，实得 %d",
				i, c.Label, c.FoldedLines, got.FoldedLines)
		}
		if got.WasFolded != c.WasFolded {
			t.Errorf("用例 %d (%s) wasFolded 不符：期望 %v，实得 %v",
				i, c.Label, c.WasFolded, got.WasFolded)
		}
		if len(got.Signatures) != len(c.Signatures) {
			t.Errorf("用例 %d (%s) signatures 数不符：期望 %d，实得 %d",
				i, c.Label, len(c.Signatures), len(got.Signatures))
			continue
		}
		for j := range c.Signatures {
			if got.Signatures[j] != c.Signatures[j] {
				t.Errorf("用例 %d (%s) signatures[%d] 不符：\n期望 %q\n实得 %q",
					i, c.Label, j, c.Signatures[j], got.Signatures[j])
			}
		}
	}
}

// TestCodeFoldShortFileNotFolded —— 低于 MIN_LINES_TO_FOLD（50）不折叠。
func TestCodeFoldShortFileNotFolded(t *testing.T) {
	short := "export function a() {\n  return 1\n}"
	got := FoldCode(short, FoldOptions{FilePath: "a.ts"})
	if got.WasFolded {
		t.Error("短文件不应折叠")
	}
	if got.Folded != short {
		t.Error("短文件应原样返回")
	}
	// 49 行（差 1）仍不折叠。
	lines := make([]string, 49)
	for i := range lines {
		lines[i] = "const x = 1"
	}
	if FoldCode(joinLines(lines), FoldOptions{FilePath: "a.ts"}).WasFolded {
		t.Error("49 行不应折叠")
	}
	// 50 行应尝试折叠（但不保证 wasFolded——取决于是否有可折叠块）。
	lines50 := make([]string, 50)
	for i := range lines50 {
		lines50[i] = "const x = 1"
	}
	// 纯 `const x = 1` 无可折叠块 → wasFolded=false（对账 oracle 的 ts-50行 用例）。
	_ = FoldCode(joinLines(lines50), FoldOptions{FilePath: "a.ts"})
}

// TestCodeFoldUnknownLanguageNotFolded —— 未知扩展名不折叠。
func TestCodeFoldUnknownLanguageNotFolded(t *testing.T) {
	content := make([]string, 60)
	for i := range content {
		content[i] = "some text"
	}
	got := FoldCode(joinLines(content), FoldOptions{FilePath: "a.txt"})
	if got.WasFolded {
		t.Error("未知扩展名不应折叠")
	}
}

// TestCodeFoldTsFoldsBodies —— ts-like 把函数体折叠为 `  { … }`。
func TestCodeFoldTsFoldsBodies(t *testing.T) {
	var b []string
	b = append(b, "export function alpha() {", "  const x = 1", "  return x", "}")
	for i := 0; i < 55; i++ {
		b = append(b, "const filler = 1")
	}
	got := FoldCode(joinLines(b), FoldOptions{FilePath: "a.ts"})
	if !got.WasFolded {
		t.Fatal("应折叠")
	}
	if !containsSubstr(got.Folded, "{ … }") {
		t.Errorf("应含折叠标记：%.200q", got.Folded)
	}
	if len(got.Signatures) == 0 {
		t.Error("应提取到签名")
	}
}

// TestCodeFoldMarkdownKeepsHeadings —— markdown 保留标题与首个主题句。
func TestCodeFoldMarkdownKeepsHeadings(t *testing.T) {
	var b []string
	b = append(b, "# Title", "topic", "body one", "body two", "## Sec", "sec topic", "body three")
	for i := 0; i < 60; i++ {
		b = append(b, "filler body line")
	}
	got := FoldCode(joinLines(b), FoldOptions{FilePath: "a.md"})
	if !got.WasFolded {
		t.Fatal("应折叠")
	}
	if !containsSubstr(got.Folded, "# Title") || !containsSubstr(got.Folded, "## Sec") {
		t.Errorf("应保留标题：%.300q", got.Folded)
	}
	if !containsSubstr(got.Folded, "topic") {
		t.Errorf("应保留标题后首个主题句：%.300q", got.Folded)
	}
}

// TestCodeFoldJsonSkeleton —— JSON 产键结构骨架。
func TestCodeFoldJsonSkeleton(t *testing.T) {
	// 需 > 50 行才进入分派。
	obj := map[string]any{}
	for i := 0; i < 60; i++ {
		obj["key"+string(rune('a'+i%26))+string(rune('0'+i/26))] = i
	}
	content := mustJSON(t, obj)
	got := FoldCode(content, FoldOptions{FilePath: "a.json"})
	// 键值被替换为类型名。
	if !containsSubstr(got.Folded, "number") {
		t.Errorf("JSON 骨架应含类型名：%.300q", got.Folded)
	}
}

// TestCodeFoldPythonIndent —— python 按缩进折叠 def/class 体。
func TestCodeFoldPythonIndent(t *testing.T) {
	var b []string
	b = append(b, "class Foo:", "    def bar(self):", "        pass", "", "def top():", "    return 1")
	for i := 0; i < 55; i++ {
		b = append(b, "x = 1")
	}
	got := FoldCode(joinLines(b), FoldOptions{FilePath: "a.py"})
	if !got.WasFolded {
		t.Fatal("应折叠")
	}
	if !containsSubstr(got.Folded, "{ … }") {
		t.Errorf("应含折叠标记：%.200q", got.Folded)
	}
	if len(got.Signatures) < 2 {
		t.Errorf("应提取 class 与 def 签名，实得 %d", len(got.Signatures))
	}
}

func joinLines(ls []string) string {
	out := ""
	for i, l := range ls {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFocusedReadNoMatchUsesFoldCode —— **接线验证**：focused-read 的无匹配分支
// 现在用 `foldCode` 产骨架（此前是 `STRUCTURAL_LINE` 回退）。
//
// 对账 TS `structuralSkeleton`：`folded.wasFolded ? folded.folded : 回退`。
func TestFocusedReadNoMatchUsesFoldCode(t *testing.T) {
	// 造一个可折叠的 .ts 内容（> 50 行 + 有函数体），且 focus 无匹配。
	var b []string
	b = append(b, "export function targetAlpha() {", "  const x = 1", "  return x", "}")
	for i := 0; i < 60; i++ {
		b = append(b, "const filler = 1")
	}
	b = append(b, "export function targetBeta() {", "  return 2", "}")
	content := joinLines(b)

	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: content, Focus: "zzzznomatch",
		MaxChars: 8000, MaxMatches: 8, ContextLines: 2,
	})
	if got.Matched {
		t.Fatal("不应匹配")
	}
	if !containsSubstr(got.Content, "No direct focus match") {
		t.Fatalf("应走无匹配分支：%.300s", got.Content)
	}
	// **关键**：骨架应来自 foldCode —— 含 `{ … }` 折叠标记。
	if !containsSubstr(got.Content, "{ … }") {
		t.Errorf("骨架应来自 foldCode（含折叠标记）：%.400s", got.Content)
	}
	// 且应含签名（函数声明行）。
	if !containsSubstr(got.Content, "targetAlpha") {
		t.Errorf("骨架应含函数签名：%.400s", got.Content)
	}
}
