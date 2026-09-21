package tools

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// truncOracleCase 对账 TS `truncation.ts` 的黄金数据。
//
// 生成：`node_modules/.bin/tsx go/testdata/truncation/gen_oracle.ts`
type truncOracleCase struct {
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	FilePath  string `json:"filePath"`
	MaxChars  int    `json:"maxChars"`
	KeepHead  int    `json:"keepHead"`
	KeepTail  int    `json:"keepTail"`
	SkelLines int    `json:"skelLines"`
	SkelChars int    `json:"skelChars"`
	Result    string `json:"result"`
	// ResultBytes 是 TS 产出的 UTF-8 十六进制——用于**字节级**对账
	// （字符串比较可能掩盖孤立代理→U+FFFD 的编码差异）。
	ResultBytes string `json:"resultBytes"`
}

func loadTruncOracle(t *testing.T) []truncOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "truncation", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []truncOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空——生成脚本可能失败")
	}
	return cases
}

// TestTruncationOracle 逐例对账 TS 实现（字符串 + **字节**双重比对）。
func TestTruncationOracle(t *testing.T) {
	cases := loadTruncOracle(t)
	for i, c := range cases {
		var got string
		switch c.Kind {
		case "truncate":
			got = TruncateContent(c.Content, c.MaxChars, c.KeepHead, c.KeepTail)
		case "partial":
			got = BuildPartialView(c.Content, c.FilePath, c.MaxChars, nil)
		case "skeleton":
			got = BuildPartialView(c.Content, c.FilePath, c.MaxChars,
				&SkeletonSource{Lines: c.SkelLines, Chars: c.SkelChars})
		default:
			t.Fatalf("未知 kind=%q（用例 %d）", c.Kind, i)
		}

		if got != c.Result {
			t.Errorf("用例 %d (%s) 字符串不符：\n期望 %q\n实得 %q", i, c.Kind, c.Result, got)
		}
		// 字节级（孤立代理 → U+FFFD 的编码差异只在这里暴露）。
		gotHex := hex.EncodeToString([]byte(got))
		if gotHex != c.ResultBytes {
			t.Errorf("用例 %d (%s) **字节**不符：\n期望 %s\n实得 %s",
				i, c.Kind, c.ResultBytes, gotHex)
		}
	}
}

// TestTruncateContentKeepTailZeroTrap —— `keepTail=0` 时 JS 的 `slice(-0)`
// 返回**整个串**（不是空串）。
//
// 这是本文件最容易写错的一处——`-0 === 0` 的 JS 特性。若实现成
// 「k==0 → 空串」，尾部会静默丢失全部内容。
func TestTruncateContentKeepTailZeroTrap(t *testing.T) {
	got := TruncateContent("abcdefghij", 5, 2, 0)
	want := "ab\n" + truncationNote + "\nabcdefghij"
	if got != want {
		t.Errorf("keepTail=0 应返回全串作尾：\n期望 %q\n实得 %q", want, got)
	}
}

// TestTruncateContentNoOpWhenShort —— 不超限时**原样返回**（零改动）。
func TestTruncateContentNoOpWhenShort(t *testing.T) {
	for _, s := range []string{"", "abc", "😀😀", "你好"} {
		if got := TruncateContent(s, 100, 5, 5); got != s {
			t.Errorf("%q 不应被改动，实得 %q", s, got)
		}
	}
}

// TestTruncateContentLoneSurrogateToFFFD —— 切在代理对中间 → U+FFFD。
//
// 探针实测：`"a😀bc".slice(0,2)` 的 UTF-8 是 `61 ef bf bd`。
func TestTruncateContentLoneSurrogateToFFFD(t *testing.T) {
	got := TruncateContent("a😀bcdefghij", 5, 2, 2)
	// 头 "a" + 孤立高位代理 → "a\uFFFD"
	want := "a\uFFFD\n" + truncationNote + "\nij"
	if got != want {
		t.Errorf("孤立代理应转 U+FFFD：\n期望 %q\n实得 %q", want, got)
	}
	if hex.EncodeToString([]byte(got)) != hex.EncodeToString([]byte(want)) {
		t.Error("字节不符")
	}
}

// TestBuildPartialViewAtLeastOneLine —— 预算为 0 时仍保留 **1 行**。
func TestBuildPartialViewAtLeastOneLine(t *testing.T) {
	got := BuildPartialView("first\nsecond\nthird", "x.ts", 0, nil)
	if !containsSubstr(got, "first") {
		t.Errorf("至少应保留首行，实得 %q", got)
	}
	if containsSubstr(got, "second") {
		t.Errorf("零预算不应含第二行，实得 %q", got)
	}
	// header 必须说 1 行。
	if !containsSubstr(got, "Showing lines 1-1 of 3.") {
		t.Errorf("header 行数不符：%q", got)
	}
}

// TestBuildPartialViewSkeletonHeader —— skeleton 形态的 header 文案。
//
// **防的是**：骨架 header 若报自身尺寸（而非来源文件尺寸），
// "showing lines 1-79 of 79" 会让模型误以为读到了完整文件。
func TestBuildPartialViewSkeletonHeader(t *testing.T) {
	got := BuildPartialView("outline\nlines", "src/a.ts", 800, &SkeletonSource{Lines: 500, Chars: 99999})
	if !containsSubstr(got, "SKELETON view of src/a.ts (500 lines, 99999 chars)") {
		t.Errorf("skeleton header 应报来源尺寸：%q", got)
	}
	if !containsSubstr(got, "This is NOT the file's text") {
		t.Errorf("skeleton header 应有警示：%q", got)
	}
	// 不得出现 PARTIAL 文案。
	if containsSubstr(got, "PARTIAL view") {
		t.Errorf("skeleton 形态不应有 PARTIAL 文案：%q", got)
	}
}

// TestBuildPartialViewNavigationHint —— 导航提示的 offset 是 keptLines+1。
func TestBuildPartialViewNavigationHint(t *testing.T) {
	got := BuildPartialView("l1\nl2\nl3\nl4", "x.ts", 60, nil)
	// 预算 60-300 → 0 → 至少 1 行 → offset 应为 2。
	if !containsSubstr(got, "offset=2, limit=200") {
		t.Errorf("导航 offset 应为 keptLines+1：%q", got)
	}
}

// TestUTF16Len —— 对账 JS 的 `.length`。
func TestUTF16Len(t *testing.T) {
	cases := map[string]int{
		"":      0,
		"abc":   3,
		"😀":     2,
		"a😀bc":  5,
		"你好":    2,
		"😀😀":    4,
		"a😀b你c": 6,
	}
	for s, want := range cases {
		if got := UTF16Len(s); got != want {
			t.Errorf("UTF16Len(%q)：期望 %d，实得 %d", s, want, got)
		}
	}
}

// TestJsSliceTailNegativeZero —— 单元锁定 `slice(-k)` 的三个边界。
func TestJsSliceTailNegativeZero(t *testing.T) {
	s := "a😀bc" // units: 61 d83d de00 62 63
	// k=0 → 全串（-0 语义）。
	if got := jsSliceTail(s, 0); got != s {
		t.Errorf("k=0 应返回全串，实得 %q", got)
	}
	// k >= len → 全串。
	if got := jsSliceTail(s, 5); got != s {
		t.Errorf("k=len 应返回全串，实得 %q", got)
	}
	if got := jsSliceTail(s, 99); got != s {
		t.Errorf("k>len 应返回全串，实得 %q", got)
	}
	// k=2 → "bc"。
	if got := jsSliceTail(s, 2); got != "bc" {
		t.Errorf("k=2 期望 \"bc\"，实得 %q", got)
	}
	// k=3 → 切在代理对中间 → "\uFFFDbc"。
	if got := jsSliceTail(s, 3); got != "\uFFFDbc" {
		t.Errorf("k=3 期望 \"\\uFFFDbc\"，实得 %q", got)
	}
}

// TestJsSliceHeadBoundaries —— 单元锁定 `slice(0,n)`。
func TestJsSliceHeadBoundaries(t *testing.T) {
	s := "a😀bc"
	if got := jsSliceHead(s, 0); got != "" {
		t.Errorf("n=0 应为空，实得 %q", got)
	}
	if got := jsSliceHead(s, -1); got != "" {
		t.Errorf("n<0 应为空，实得 %q", got)
	}
	if got := jsSliceHead(s, 5); got != s {
		t.Errorf("n=len 应返回全串，实得 %q", got)
	}
	if got := jsSliceHead(s, 99); got != s {
		t.Errorf("n>len 应返回全串，实得 %q", got)
	}
	if got := jsSliceHead(s, 2); got != "a\uFFFD" {
		t.Errorf("n=2 期望 \"a\\uFFFD\"，实得 %q", got)
	}
}

// containsSubstr 局部辅助（避免与 summarize_test.go 的同名函数冲突时改名）。
func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
