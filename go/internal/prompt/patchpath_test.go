package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// patchpathOracle 是 TS 侧真实 extractPatchTargetPaths 的产出。
// 生成命令：npx tsx go/testdata/patchpath/gen-oracle.ts
type patchpathOracle struct {
	Cases map[string]struct {
		Diff string   `json:"diff"`
		Out  []string `json:"out"`
		Note string   `json:"note"`
	} `json:"cases"`
}

func loadPatchpathOracle(t *testing.T) patchpathOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "patchpath", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/patchpath/gen-oracle.ts", path, err)
	}
	var o patchpathOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestExtractPatchTargetPathsParity —— 路径提取与 TS 逐字节相同。
func TestExtractPatchTargetPathsParity(t *testing.T) {
	o := loadPatchpathOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}
	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			got := ExtractPatchTargetPaths(c.Diff)
			if len(got) != len(c.Out) {
				t.Fatalf("路径数不符：Go=%v TS=%v", got, c.Out)
			}
			for i := range got {
				if got[i] != c.Out[i] {
					t.Errorf("第 %d 个路径：Go=%q TS=%q（完整 Go=%v TS=%v）",
						i, got[i], c.Out[i], got, c.Out)
				}
			}
		})
	}
}

// TestExtractPatchTargetPathsDedup —— 同一路径重复出现只算一次，且保持首现顺序。
func TestExtractPatchTargetPathsDedup(t *testing.T) {
	diff := "+++ b/x\n+++ b/y\n+++ b/x\n"
	got := ExtractPatchTargetPaths(diff)
	want := []string{"x", "y"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("去重与顺序错误：Go=%v，期望 %v", got, want)
	}
}

// TestExtractPatchTargetPathsSkipsDevNull —— /dev/null 应被跳过（纯删除）。
func TestExtractPatchTargetPathsSkipsDevNull(t *testing.T) {
	diff := "+++ /dev/null\n"
	if got := ExtractPatchTargetPaths(diff); len(got) != 0 {
		t.Errorf("/dev/null 应被跳过，实际 %v", got)
	}
}

// TestExtractPatchTargetPathsStripsPrefixes —— 去 a/ b/ 前缀、去引号、截 tab。
func TestExtractPatchTargetPathsStripsPrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+++ b/src/x.ts\n", "src/x.ts"},
		{"+++ a/src/x.ts\n", "src/x.ts"},
		{"+++ src/x.ts\n", "src/x.ts"},   // 无前缀
		{"+++ \"b/a b.ts\"\n", "a b.ts"}, // 引号 + 前缀
		{"+++ b/x.ts\t2024\n", "x.ts"},   // tab 截断
		{"+++ b/x.ts   \n", "x.ts"},      // 尾随空格
		{"+++ b/x.ts\r\n", "x.ts"},       // CRLF
		{"+++ 中文.ts\n", "中文.ts"},         // 中文
		{"+++ b/\n", ""},                 // 空前缀 → 跳过
	}
	for _, c := range cases {
		got := ExtractPatchTargetPaths(c.in)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("输入 %q 应产出空，实际 %v", c.in, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("输入 %q：Go=%v，期望 [%q]", c.in, got, c.want)
		}
	}
}

// TestExtractPatchTargetPathsBodyLineMatches —— diff 正文里以 `+++ ` 开头的行
// **也会**被当作路径行（TS 只看行首前缀，不区分头部与正文）。
//
// 这是 oracle 锁定的边界行为——看起来像 bug，但是真实语义，需复刻。
func TestExtractPatchTargetPathsBodyLineMatches(t *testing.T) {
	diff := "+++ b/x\n+++not-a-header\n"
	got := ExtractPatchTargetPaths(diff)
	// `+++not-a-header` 无空格前缀，不匹配；只有 `+++ b/x` 匹配
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("Go=%v，期望 [x]", got)
	}

	// 带空格的正文行会被误当路径行
	diff2 := "+++ b/x\n+++ b/fake\n"
	got2 := ExtractPatchTargetPaths(diff2)
	if len(got2) != 2 {
		t.Errorf("正文里带 `+++ ` 前缀的行也会被提取（真实行为），实际 %v", got2)
	}
}
