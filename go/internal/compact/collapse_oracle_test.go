package compact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type collapseOracle struct {
	Collapse []struct {
		Name    string `json:"name"`
		Tool    string `json:"tool"`
		Content string `json:"content"`
		TurnAge int    `json:"turnAge"`
		Want    *struct {
			ToolName        string `json:"toolName"`
			Summary         string `json:"summary"`
			OriginalTokens  int    `json:"originalTokens"`
			CollapsedTokens int    `json:"collapsedTokens"`
		} `json:"want"`
	} `json:"collapse"`
}

func loadCollapseOracle(t *testing.T) collapseOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pressure", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var out collapseOracle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

// TestCollapseToolResultParity —— **语义折叠逐字节对账**（16 个用例）。
//
// 覆盖：两个前置条件（太小 / 太新）、grep 两条路径（普通 / artifact）、
// read_file 两条路径（有函数 / 仅导出）、bash 失败行判定（含成功行排除）、
// write/edit、run_tests（中文口径）、delegate、generic、artifact 保留。
//
// **注意 `at-boundary-200`**：恰好 200 字符**通过**前置条件（`< 200` 才是
// 拒绝），但纯 `x` 内容无 `file:` 行 → `0 matches in 0 files`。
func TestCollapseToolResultParity(t *testing.T) {
	o := loadCollapseOracle(t)
	if len(o.Collapse) == 0 {
		t.Fatal("oracle 里没有 collapse 用例——生成器未产出")
	}
	for _, c := range o.Collapse {
		t.Run(c.Name, func(t *testing.T) {
			got := CollapseToolResult(c.Tool, c.Content, c.TurnAge)
			if c.Want == nil {
				if got != nil {
					t.Fatalf("应为 nil（前置条件不满足），实得 %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("不应为 nil，期望 summary=%q", c.Want.Summary)
			}
			if got.ToolName != c.Want.ToolName {
				t.Errorf("toolName：Go=%q TS=%q", got.ToolName, c.Want.ToolName)
			}
			if got.Summary != c.Want.Summary {
				t.Errorf("summary 不一致：\n  Go=%q\n  TS=%q", got.Summary, c.Want.Summary)
			}
			if got.OriginalTokens != c.Want.OriginalTokens {
				t.Errorf("originalTokens：Go=%d TS=%d", got.OriginalTokens, c.Want.OriginalTokens)
			}
			if got.CollapsedTokens != c.Want.CollapsedTokens {
				t.Errorf("collapsedTokens：Go=%d TS=%d", got.CollapsedTokens, c.Want.CollapsedTokens)
			}
		})
	}
}

// TestArtifactMarkerRegex —— **artifact 标记契约**（只认行尾标记）。
func TestArtifactMarkerRegex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"result\n[artifact:abc_123]", "abc_123"},
		{"result\n[artifact:abc-123]", "abc-123"},
		{"[artifact:x]", "x"},
		{"result [artifact:x]", "x"},  // 尾随空白 + 行尾
		{"[artifact:x] trailing", ""}, // 不在行尾 → 不匹配
		{"no marker here", ""},
		{"[artifact:]", ""},    // 空 id → 不匹配
		{"[artifact:a b]", ""}, // 空格非法
	}
	for _, c := range cases {
		m := ArtifactMarkerRegex.FindStringSubmatch(c.in)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("输入 %q：Go=%q want=%q", c.in, got, c.want)
		}
	}
}
