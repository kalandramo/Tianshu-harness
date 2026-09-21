package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// focusedOracleCase 对账 TS `focused-read.ts` 的黄金数据。
//
// 生成：`node_modules/.bin/tsx go/testdata/focused/gen_oracle.ts`
type focusedOracleCase struct {
	Label         string             `json:"label"`
	FilePath      string             `json:"filePath"`
	Content       string             `json:"inputContent"`
	Focus         string             `json:"focus"`
	MaxChars      int                `json:"maxChars"`
	MaxMatches    int                `json:"maxMatches"`
	ContextLines  int                `json:"contextLines"`
	Ranges        []FocusedReadRange `json:"ranges"`
	MatchedLines  int                `json:"matchedLines"`
	OmittedLines  int                `json:"omittedLines"`
	Matched       bool               `json:"matched"`
	ContentOut    string             `json:"outputContent"`
	NoMatchBranch bool               `json:"noMatchBranch"`
}

func loadFocusedOracle(t *testing.T) []focusedOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "focused", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []focusedOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空——生成脚本可能失败")
	}
	return cases
}

// TestFocusedReadOracle 逐例对账。
//
// **对账范围**：ranges / matchedLines / omittedLines / matched（确定性核心）。
// `content` 仅在有匹配时对账——无匹配分支的骨架依赖 `foldCode`（Go 侧未移植，
// 走 TS 自身的回退路径），故形态会不同（已记入文件头注释）。
func TestFocusedReadOracle(t *testing.T) {
	cases := loadFocusedOracle(t)
	for i, c := range cases {
		got := BuildFocusedReadView(FocusedReadOptions{
			FilePath:     c.FilePath,
			Content:      c.Content,
			Focus:        c.Focus,
			MaxChars:     c.MaxChars,
			MaxMatches:   c.MaxMatches,
			ContextLines: c.ContextLines,
		})

		if got.Matched != c.Matched {
			t.Errorf("用例 %d (%s) matched 不符：期望 %v，实得 %v",
				i, c.Label, c.Matched, got.Matched)
		}
		if got.MatchedLines != c.MatchedLines {
			t.Errorf("用例 %d (%s) matchedLines 不符：期望 %d，实得 %d",
				i, c.Label, c.MatchedLines, got.MatchedLines)
		}
		if got.OmittedLines != c.OmittedLines {
			t.Errorf("用例 %d (%s) omittedLines 不符：期望 %d，实得 %d",
				i, c.Label, c.OmittedLines, got.OmittedLines)
		}
		if len(got.Ranges) != len(c.Ranges) {
			t.Errorf("用例 %d (%s) ranges 数不符：期望 %d，实得 %d",
				i, c.Label, len(c.Ranges), len(got.Ranges))
			continue
		}
		for j := range c.Ranges {
			if got.Ranges[j] != c.Ranges[j] {
				t.Errorf("用例 %d (%s) ranges[%d] 不符：\n期望 %+v\n实得 %+v",
					i, c.Label, j, c.Ranges[j], got.Ranges[j])
			}
		}
		// content：仅匹配分支对账（无匹配分支的骨架依赖未移植的 foldCode）。
		if !c.NoMatchBranch && got.Content != c.ContentOut {
			t.Errorf("用例 %d (%s) content 不符：\n期望 %.300q\n实得 %.300q",
				i, c.Label, c.ContentOut, got.Content)
		}
	}
}

// TestFocusedReadFixesSilentFailure —— **核心验收**：focus 不再被静默忽略。
//
// 此前 Go 的 read_file 声明了 focus 参数但未实现——传 focus 时返回全文件，
// 模型以为拿到聚焦结果。这条锁定「传 focus 得到聚焦视图」。
func TestFocusedReadFixesSilentFailure(t *testing.T) {
	content := strings.Join([]string{
		"import { a } from 'a'",
		"const unrelated = 1",
		"export function targetFn() { return 1 }",
		"const another = 2",
	}, "\n")
	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: content, Focus: "targetFn",
		MaxChars: 8000, MaxMatches: 8, ContextLines: 2,
	})
	if !got.Matched {
		t.Fatal("应匹配到 targetFn")
	}
	if !strings.Contains(got.Content, "[focused-read]") {
		t.Errorf("应含聚焦视图头部：%.200s", got.Content)
	}
	if !strings.Contains(got.Content, "targetFn") {
		t.Errorf("应含匹配行：%.200s", got.Content)
	}
	// **关键**：必须明示这不是完整文件（防模型误判）。
	if !strings.Contains(got.Content, "this is not the complete file") {
		t.Errorf("应明示非完整文件：%.300s", got.Content)
	}
}

// TestFocusedReadNoMatchTellsTruth —— 无匹配时**不谎称完整**。
func TestFocusedReadNoMatchTellsTruth(t *testing.T) {
	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: "const a = 1\nconst b = 2",
		Focus: "zzzznope", MaxChars: 8000, MaxMatches: 8, ContextLines: 2,
	})
	if got.Matched {
		t.Fatal("不应匹配")
	}
	if !strings.Contains(got.Content, "No direct focus match") {
		t.Errorf("应明示无直接匹配：%.300s", got.Content)
	}
	// 应指引精确范围重读。
	if !strings.Contains(got.Content, "read_file(offset, limit)") {
		t.Errorf("应指引精确重读：%.300s", got.Content)
	}
}

// TestFocusedReadStopWordsIgnored —— 停用词不产生匹配。
func TestFocusedReadStopWordsIgnored(t *testing.T) {
	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: "the code is here\nplease read the file",
		Focus: "the code", MaxChars: 8000, MaxMatches: 8, ContextLines: 2,
	})
	if got.Matched {
		t.Errorf("停用词不应匹配，实得 ranges=%+v", got.Ranges)
	}
}

// TestFocusedReadChineseBigram —— 中文按 bigram 分词。
func TestFocusedReadChineseBigram(t *testing.T) {
	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: "无关行\n会话管理器实现\n另一行",
		Focus: "会话管理", MaxChars: 8000, MaxMatches: 8, ContextLines: 2,
	})
	if !got.Matched {
		t.Fatalf("中文应匹配，tokens 应含 bigram")
	}
	if !strings.Contains(got.Content, "会话管理器实现") {
		t.Errorf("应选中含目标的中文行：%.300s", got.Content)
	}
}

// TestFocusedReadBudgetTrimsLowestScore —— 预算超限时丢弃**最低分**区间。
//
// **构造关键**：必须有**多个**区间且渲染总长超预算，否则裁剪不发生、断言无区分力
// （变异 M42「丢最高分」首版 0 红即此因）。
func TestFocusedReadBudgetTrimsLowestScore(t *testing.T) {
	var b strings.Builder
	// 三个分散的匹配：高分（结构行 + 长标识符）/ 中分 / 低分。
	// **每段填充长行**，让渲染总长确实超预算（否则裁剪不发生、断言无区分力）。
	long := strings.Repeat("z", 200)
	b.WriteString("export function strongMatchToken() { return '" + long + "' }\n")
	for i := 0; i < 20; i++ {
		b.WriteString("const pad1 = '" + long + "'\n")
	}
	b.WriteString("const mediumToken = '" + long + "'\n")
	for i := 0; i < 20; i++ {
		b.WriteString("const pad2 = '" + long + "'\n")
	}
	b.WriteString("weak = '" + long + "'\n")
	got := BuildFocusedReadView(FocusedReadOptions{
		FilePath: "x.ts", Content: b.String(),
		Focus:    "strongMatchToken mediumToken weak",
		MaxChars: 800, MaxMatches: 8, ContextLines: 2,
	})
	if !got.Matched {
		t.Fatal("应匹配")
	}
	// 高分区间必须保留。
	if !strings.Contains(got.Content, "strongMatchToken") {
		t.Errorf("高分区间应被保留：%.400s", got.Content)
	}
	// **关键**：预算裁剪应**真的发生**（区间数少于全部匹配数 3）。
	if len(got.Ranges) >= 3 {
		t.Errorf("预算应触发裁剪（3 个区间超预算），实得 %d 个区间", len(got.Ranges))
	}
	// 被丢的应是**最低分**——保留的区间里不应有「只剩 weak」的情况。
	keptHigh := false
	for _, r := range got.Ranges {
		if r.Score >= 7 {
			keptHigh = true
		}
	}
	if !keptHigh {
		t.Errorf("应保留至少一个高分区间，实得 %+v", got.Ranges)
	}
}

// TestFocusedReadTokenize —— 单元：分词（含停用词过滤与 bigram）。
func TestFocusedReadTokenize(t *testing.T) {
	// 英文：长度 >= 2 且非停用词。
	got := tokenizeFocus("commitAction the file")
	want := map[string]bool{"commitaction": true}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("不应含 token %q（got=%v）", tok, got)
		}
	}
	if len(got) != 1 {
		t.Errorf("应恰 1 个 token，实得 %v", got)
	}

	// 中文：产 bigram。
	zh := tokenizeFocus("会话管理")
	if len(zh) != 3 { // 会话/话管/管理
		t.Errorf("中文应产 3 个 bigram，实得 %v", zh)
	}
	// 空 focus。
	if n := len(tokenizeFocus("")); n != 0 {
		t.Errorf("空 focus 应无 token，实得 %d", n)
	}
}

// TestFocusedReadNormalizeTruncates —— 单元：focus 截断到 240。
func TestFocusedReadNormalizeTruncates(t *testing.T) {
	long := strings.Repeat("x", 500)
	if n := UTF16Len(normalizeFocus(long)); n != 240 {
		t.Errorf("应截断到 240 code unit，实得 %d", n)
	}
	// 空白折叠。
	if got := normalizeFocus("a  \t\n  b"); got != "a b" {
		t.Errorf("应折叠空白，实得 %q", got)
	}
}

// TestReadFileFocusEndToEnd —— **端到端**：read_file 传 focus 真的走聚焦分支。
//
// 这是「静默失效」的最终验收：此前传 focus 返回全文件；现在应返回聚焦视图。
func TestReadFileFocusEndToEnd(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("const filler = 1\n")
	}
	b.WriteString("export function commitAction() { return 1 }\n")
	for i := 0; i < 300; i++ {
		b.WriteString("const filler2 = 2\n")
	}
	mustWriteFile(t, rootDir(root)+"big.ts", b.String())

	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "big.ts", "focus": "commitAction"})
	p.ContextWindow = 1_000_000

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Fatalf("不应报错：%s", r.Content)
	}
	// 必须是聚焦视图（不是全文件）。
	if !strings.Contains(r.Content, "[focused-read]") {
		t.Fatalf("应走聚焦分支，实得：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "commitAction") {
		t.Errorf("应含目标行：%.300s", r.Content)
	}
	// **关键**：输出应远小于全文件（证明真的聚焦了）。
	if len(r.Content) > 3000 {
		t.Errorf("聚焦输出应远小于全文件（601 行），实得 %d 字节", len(r.Content))
	}
}

// TestReadFileFocusIgnoredWithRange —— focus 与 offset/limit 同传时 focus 不生效。
//
// 对账 TS 的 `focusedRead = focus.length > 0 && offset === undefined && limit === undefined`。
func TestReadFileFocusIgnoredWithRange(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"a.ts", "line1\nline2\nline3\nline4\nline5")

	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "a.ts", "focus": "line2", "offset": 1, "limit": 3})
	r, _ := tool.Execute(t.Context(), p)
	if strings.Contains(r.Content, "[focused-read]") {
		t.Errorf("有显式范围时不应走聚焦分支：%.200s", r.Content)
	}
	// 应走范围读取（含范围头）。
	if !strings.Contains(r.Content, "第 1-3 行") {
		t.Errorf("应走范围读取：%.200s", r.Content)
	}
}

// TestReadFileFocusEmptyStringIgnored —— 空/纯空白 focus 不触发聚焦。
func TestReadFileFocusEmptyStringIgnored(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"a.ts", "const x = 1")
	tool := ReadFile(root, nil)
	for _, f := range []string{"", "   ", "\t"} {
		p := call(root, map[string]any{"file_path": "a.ts", "focus": f})
		r, _ := tool.Execute(t.Context(), p)
		if strings.Contains(r.Content, "[focused-read]") {
			t.Errorf("focus=%q 不应触发聚焦分支", f)
		}
	}
}
