package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oracleCase 对账 TS `summarize.ts` 的黄金数据（`testdata/summarize/oracle.json`）。
//
// 生成方式：`node_modules/.bin/tsx go/testdata/summarize/gen_oracle.ts`。
type oracleCase struct {
	Kind     string            `json:"kind"`
	Content  string            `json:"content"`
	Arg      string            `json:"arg"`
	Extra    int               `json:"extra"`
	Summary  string            `json:"summary"`
	Sections []ArtifactSection `json:"sections"`
}

// loadSummarizeOracle 读黄金数据（逐例比对，失败即报**具体**差异）。
func loadSummarizeOracle(t *testing.T) []oracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "summarize", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []oracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空——生成脚本可能失败")
	}
	return cases
}

// TestSummarizeOracle 逐例对账 TS 实现。
//
// **summary 与 sections 都比对**（sections 此前无人校验——本刀同时锁定）。
func TestSummarizeOracle(t *testing.T) {
	cases := loadSummarizeOracle(t)
	for i, c := range cases {
		var got SummarizeResult
		switch c.Kind {
		case "file":
			got = SummarizeFileContent(c.Content, c.Arg)
		case "grep":
			got = SummarizeGrepResult(c.Content, c.Arg)
		case "bash":
			got = SummarizeBashOutput(c.Content, c.Arg, c.Extra)
		default:
			t.Fatalf("未知 kind=%q（用例 %d）", c.Kind, i)
		}

		if got.Summary != c.Summary {
			t.Errorf("用例 %d (%s %q) summary 不符：\n期望 %q\n实得 %q",
				i, c.Kind, c.Arg, c.Summary, got.Summary)
		}
		if len(got.Sections) != len(c.Sections) {
			t.Errorf("用例 %d (%s %q) sections 数不符：期望 %d，实得 %d",
				i, c.Kind, c.Arg, len(c.Sections), len(got.Sections))
			continue
		}
		for j := range c.Sections {
			if got.Sections[j] != c.Sections[j] {
				t.Errorf("用例 %d (%s %q) sections[%d] 不符：\n期望 %+v\n实得 %+v",
					i, c.Kind, c.Arg, j, c.Sections[j], got.Sections[j])
			}
		}
	}
}

// TestSummarizeSectionsNeverNil —— 对账 TS 的 `[]`：Go 的 nil 会序列化成
// `null`，与 TS 的 `[]` 字节不等价。
//
// 这条防的是**静默的序列化差异**——`_index.jsonl` 里 `"sections":null`
// 与 `"sections":[]` 会让 TS/Go 产出的 artifact 索引不互通。
func TestSummarizeSectionsNeverNil(t *testing.T) {
	cases := []SummarizeResult{
		SummarizeFileContent("x", "a.go"),    // Go 低细节 → sections 空
		SummarizeFileContent("x", "a.txt"),   // generic → 空
		SummarizeGrepResult("a.ts:1:x", "p"), // grep → 空
		SummarizeBashOutput("out", "cmd", 0), // bash → 空
	}
	for i, r := range cases {
		if r.Sections == nil {
			t.Errorf("用例 %d：Sections 为 nil（应为空切片）", i)
		}
		// 序列化后必须是 `[]` 而非 `null`。
		b, err := json.Marshal(r.Sections)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		if string(b) != "[]" {
			t.Errorf("用例 %d：序列化为 %s（期望 []）", i, b)
		}
	}
}

// TestSummarizeJsonKeyOrderStable —— JSON 顶层键**保序**。
//
// Go 的 `map` 遍历顺序随机 → 若用 map 解析，summary 会随运行变化
// （字节不稳定）。这条测试连跑多次比对同一结果。
func TestSummarizeJsonKeyOrderStable(t *testing.T) {
	content := `{"zeta":1,"alpha":2,"mid":3,"beta":4,"gamma":5,"delta":6,"epsilon":7,"eta":8}`
	first := SummarizeFileContent(content, "j.json").Summary
	for i := 0; i < 20; i++ {
		if got := SummarizeFileContent(content, "j.json").Summary; got != first {
			t.Fatalf("第 %d 次结果不同（键序不稳定）：\n首次 %q\n本次 %q", i, first, got)
		}
	}
	// 期望按出现顺序（不是字典序）。
	want := "json file, 1 lines. Keys: zeta, alpha, mid, beta, gamma, delta, epsilon, eta Nested: "
	_ = want
	if first != "json file, 1 lines. Keys: zeta, alpha, mid, beta, gamma, delta, epsilon, eta" {
		t.Errorf("键序不符：%q", first)
	}
}

// TestSummarizeGoLowDetail —— `.go` **有意低细节**（对账 TS 注释）。
//
// 这条防的是「好心给 Go 加结构解析」——那会偏离 TS 语义。
func TestSummarizeGoLowDetail(t *testing.T) {
	r := SummarizeFileContent("package main\n\nfunc main() {}\n", "g.go")
	if len(r.Sections) != 0 {
		t.Errorf("Go 摘要器应产出空 sections，实得 %d 个", len(r.Sections))
	}
	if !strings.Contains(r.Summary, "low-detail") {
		t.Errorf("Go 摘要应标注 low-detail：%q", r.Summary)
	}
}
