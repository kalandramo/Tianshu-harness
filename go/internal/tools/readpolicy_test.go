package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readPolicyOracleCase 对账 TS `read-policy.ts` 的黄金数据。
//
// 生成：`node_modules/.bin/tsx go/testdata/readpolicy/gen_oracle.ts`
type readPolicyOracleCase struct {
	FilePath         string `json:"filePath"`
	SizeBytes        int    `json:"sizeBytes"`
	HasExplicitRange bool   `json:"hasExplicitRange"`
	Kind             string `json:"kind"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
	PreviewLines     int    `json:"previewLines"`
	MaxRangeLines    int    `json:"maxRangeLines"`
}

func loadReadPolicyOracle(t *testing.T) []readPolicyOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "readpolicy", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []readPolicyOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空——生成脚本可能失败")
	}
	return cases
}

// TestReadPolicyOracle 逐例对账（**全字段**：kind/action/reason/previewLines/maxRangeLines）。
func TestReadPolicyOracle(t *testing.T) {
	cases := loadReadPolicyOracle(t)
	for i, c := range cases {
		got := DecideReadPolicy(ReadPolicyInput{
			FilePath:         c.FilePath,
			SizeBytes:        c.SizeBytes,
			HasExplicitRange: c.HasExplicitRange,
		})
		if string(got.Kind) != c.Kind {
			t.Errorf("用例 %d (%q, %d) kind 不符：期望 %q，实得 %q",
				i, c.FilePath, c.SizeBytes, c.Kind, got.Kind)
		}
		if string(got.Action) != c.Action {
			t.Errorf("用例 %d (%q, %d) action 不符：期望 %q，实得 %q",
				i, c.FilePath, c.SizeBytes, c.Action, got.Action)
		}
		if got.Reason != c.Reason {
			t.Errorf("用例 %d (%q) reason 不符：\n期望 %q\n实得 %q",
				i, c.FilePath, c.Reason, got.Reason)
		}
		if got.PreviewLines != c.PreviewLines {
			t.Errorf("用例 %d previewLines 不符：期望 %d，实得 %d",
				i, c.PreviewLines, got.PreviewLines)
		}
		if got.MaxRangeLines != c.MaxRangeLines {
			t.Errorf("用例 %d maxRangeLines 不符：期望 %d，实得 %d",
				i, c.MaxRangeLines, got.MaxRangeLines)
		}
	}
}

// TestReadPolicyWindowsBackslashNotGenerated —— **关键平台行为**：反斜杠路径的
// generated 检测**不命中**（TS 的正则只认 `/`）。
//
// 这条防的是「好心归一化路径分隔符」——那会让 Windows 上的判定偏离 TS。
// 探针实测：`D:\proj\dist\a.ts` → source；`D:/proj/dist/a.ts` → generated。
func TestReadPolicyWindowsBackslashNotGenerated(t *testing.T) {
	back := DecideReadPolicy(ReadPolicyInput{FilePath: `D:\proj\dist\a.ts`, SizeBytes: 100})
	if back.Kind != KindSource {
		t.Errorf("反斜杠路径应为 source（对账 TS），实得 %q", back.Kind)
	}
	fwd := DecideReadPolicy(ReadPolicyInput{FilePath: "D:/proj/dist/a.ts", SizeBytes: 100})
	if fwd.Kind != KindGenerated {
		t.Errorf("正斜杠路径应为 generated，实得 %q", fwd.Kind)
	}
	// 两者**必须不同**——若相同说明做了归一化。
	if back.Kind == fwd.Kind {
		t.Error("反斜杠与正斜杠路径的判定被归一化了——偏离 TS")
	}
}

// TestReadPolicyAllActionsReachable —— 五类 action 全可达。
//
// 防的是「某个分支是死代码」——若某 action 不可达，说明条件写错。
func TestReadPolicyAllActionsReachable(t *testing.T) {
	seen := map[ReadPolicyAction]bool{}
	cases := []ReadPolicyInput{
		{FilePath: "a.ts", SizeBytes: 100},      // full
		{FilePath: "a.ts", SizeBytes: 21000},    // full-with-hint
		{FilePath: "a.ts", SizeBytes: 84000},    // partial
		{FilePath: "a.log", SizeBytes: 20000},   // preview
		{FilePath: "dist/a.ts", SizeBytes: 100}, // reject-with-range
	}
	for _, c := range cases {
		seen[DecideReadPolicy(c).Action] = true
	}
	for _, want := range []ReadPolicyAction{
		ActionFull, ActionFullWithHint, ActionPartial, ActionPreview, ActionRejectWithRange,
	} {
		if !seen[want] {
			t.Errorf("action %q 不可达——分支可能是死代码", want)
		}
	}
}

// TestReadPolicyExplicitRangeWins —— `hasExplicitRange` **最高优先**：
// 即使 generated/minified/log 也放行 full。
func TestReadPolicyExplicitRangeWins(t *testing.T) {
	for _, p := range []string{"dist/a.ts", "a.min.js", "a.log", "a.jsonl"} {
		got := DecideReadPolicy(ReadPolicyInput{FilePath: p, SizeBytes: 999999, HasExplicitRange: true})
		if got.Action != ActionFull {
			t.Errorf("%q 有显式范围应放行 full，实得 %q", p, got.Action)
		}
		if got.Reason != "explicit range requested" {
			t.Errorf("%q reason 不符：%q", p, got.Reason)
		}
	}
}

// TestReadPolicyThresholdBoundaries —— 三个常量的**严格大于**语义。
//
// 探针实测：16384 → full（不 > guard）、16385 → preview；
// 20480 → full、20481 → full-with-hint；81920 → full-with-hint、81921 → partial。
func TestReadPolicyThresholdBoundaries(t *testing.T) {
	// log preview guard（16KB）。
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.log", SizeBytes: 16384}); got.Action != ActionFull {
		t.Errorf("16384 应 full（不严格大于 guard），实得 %q", got.Action)
	}
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.log", SizeBytes: 16385}); got.Action != ActionPreview {
		t.Errorf("16385 应 preview，实得 %q", got.Action)
	}
	// source small（20KB）。
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.ts", SizeBytes: 20480}); got.Action != ActionFull {
		t.Errorf("20480 应 full，实得 %q", got.Action)
	}
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.ts", SizeBytes: 20481}); got.Action != ActionFullWithHint {
		t.Errorf("20481 应 full-with-hint，实得 %q", got.Action)
	}
	// source large（80KB）。
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.ts", SizeBytes: 81920}); got.Action != ActionFullWithHint {
		t.Errorf("81920 应 full-with-hint，实得 %q", got.Action)
	}
	if got := DecideReadPolicy(ReadPolicyInput{FilePath: "a.ts", SizeBytes: 81921}); got.Action != ActionPartial {
		t.Errorf("81921 应 partial，实得 %q", got.Action)
	}
}

// TestReadPolicyClassifyOrder —— 归类**顺序敏感**：先匹配者胜。
func TestReadPolicyClassifyOrder(t *testing.T) {
	// `.jsonl` 先于 source 的 `.json`。
	if got := classifyPath("a.jsonl"); got != KindJSONL {
		t.Errorf("a.jsonl 应为 jsonl，实得 %q", got)
	}
	// `.min.js` 先于 source 的 `.js`。
	if got := classifyPath("a.min.js"); got != KindMinified {
		t.Errorf("a.min.js 应为 minified，实得 %q", got)
	}
	// generated 先于 source。
	if got := classifyPath("dist/a.ts"); got != KindGenerated {
		t.Errorf("dist/a.ts 应为 generated，实得 %q", got)
	}
}

// TestReadPolicyExtWithDigits —— `a.jsonl.1` 是 jsonl、`a.jsonl.x` 是 unknown。
func TestReadPolicyExtWithDigits(t *testing.T) {
	if got := classifyPath("a.jsonl.1"); got != KindJSONL {
		t.Errorf("a.jsonl.1 应为 jsonl，实得 %q", got)
	}
	if got := classifyPath("a.log.99"); got != KindLog {
		t.Errorf("a.log.99 应为 log，实得 %q", got)
	}
	// `a.jsonl.x` → 不匹配 jsonl；`.jsonl.x` 也不是 source → unknown。
	if got := classifyPath("a.jsonl.x"); got != KindUnknown {
		t.Errorf("a.jsonl.x 应为 unknown，实得 %q", got)
	}
	// `a.ts.3` → 不是 source（source 正则要求以扩展名**结尾**）→ unknown。
	if got := classifyPath("a.ts.3"); got != KindUnknown {
		t.Errorf("a.ts.3 应为 unknown，实得 %q", got)
	}
}
