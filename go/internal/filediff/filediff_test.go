package filediff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// diffOracle 是 TS buildFileDiff / computeChangedLineRanges 的真实产出。
type diffOracle struct {
	Note     string `json:"note"`
	Path     string `json:"path"`
	Before   string `json:"before"`
	After    string `json:"after"`
	MaxLines *int   `json:"maxLines"`
	Diff     string `json:"diff"`
	Ranges   []struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"ranges"`
}

func loadDiffOracle(t *testing.T) map[string]diffOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "filediff", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：npx tsx go/testdata/filediff/gen-oracle.ts", path, err)
	}
	var o map[string]diffOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestBuildFileDiffParity —— **逐字节对账** unified diff 输出。
//
// 这是自写 Myers 算法的最大风险点：格式细节（`,1` 是否省略、`-0,0` 起始、
// 尾换行标记）极易与 jsdiff 偏离。
func TestBuildFileDiffParity(t *testing.T) {
	oracle := loadDiffOracle(t)
	checked := 0

	for key, want := range oracle {
		checked++
		t.Run(key, func(t *testing.T) {
			maxLines := 0
			if want.MaxLines != nil {
				maxLines = *want.MaxLines
			}
			got := BuildFileDiff(want.Path, want.Before, want.After, maxLines)
			if got != want.Diff {
				t.Errorf("diff 字节不符：\n  Go=%q\n  TS=%q", got, want.Diff)
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestComputeChangedLineRangesParity —— 对账行范围（零上下文 hunk）。
func TestComputeChangedLineRangesParity(t *testing.T) {
	oracle := loadDiffOracle(t)
	checked := 0

	for key, want := range oracle {
		checked++
		t.Run(key, func(t *testing.T) {
			got := ComputeChangedLineRanges(want.Before, want.After)
			if len(got) != len(want.Ranges) {
				t.Fatalf("区间数不符：Go=%+v TS=%+v", got, want.Ranges)
			}
			for i, w := range want.Ranges {
				if got[i].Start != w.Start || got[i].End != w.End {
					t.Errorf("[%d] 区间不符：Go={%d,%d} TS={%d,%d}",
						i, got[i].Start, got[i].End, w.Start, w.End)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestNoChangeReturnsEmpty —— 无变化返回空串（对账 TS）。
func TestNoChangeReturnsEmpty(t *testing.T) {
	if got := BuildFileDiff("a.ts", "same\n", "same\n", 0); got != "" {
		t.Errorf("无变化应返回空串，得到 %q", got)
	}
	if got := ComputeChangedLineRanges("same\n", "same\n"); got != nil {
		t.Errorf("无变化应返回 nil，得到 %+v", got)
	}
}

// TestTruncationKeepsHeader —— 截断保留 header（对账 TS 的切片语义）。
func TestTruncationKeepsHeader(t *testing.T) {
	before := "a\nb\nc\nd\ne\n"
	after := "A\nB\nC\nD\nE\n"
	got := BuildFileDiff("a.ts", before, after, 3)
	if !contains(got, "@@") {
		t.Errorf("截断后应保留 @@ header：%q", got)
	}
	if !contains(got, "ctrl+o 展开") {
		t.Errorf("截断后应有提示行：%q", got)
	}
}

// TestWindowsPathNormalized —— 反斜杠路径归一化为正斜杠。
func TestWindowsPathNormalized(t *testing.T) {
	got := BuildFileDiff(`src\a.ts`, "x\n", "y\n", 0)
	if !contains(got, "src/a.ts") {
		t.Errorf("路径应归一化：%q", got)
	}
	if contains(got, `\`) {
		t.Errorf("不应含反斜杠：%q", got)
	}
}

// TestHunkHeaderAlwaysHasCount —— hunk 头**总是**含 `,count`（不省略 `,1`）。
//
// 这是与 GNU diff 的关键差异——jsdiff 不省略。
func TestHunkHeaderAlwaysHasCount(t *testing.T) {
	got := BuildFileDiff("a.ts", "x\n", "y\n", 0)
	if !contains(got, "@@ -1,1 +1,1 @@") {
		t.Errorf("hunk 头应含 `,1`（不省略）：%q", got)
	}
}

// TestNewFileHunkHeader —— 全新文件的 hunk 头是 `-0,0`。
func TestNewFileHunkHeader(t *testing.T) {
	got := BuildFileDiff("new.ts", "", "x\ny\n", 0)
	if !contains(got, "@@ -0,0 +1,2 @@") {
		t.Errorf("全新文件 hunk 头应为 `-0,0 +1,2`：%q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
