package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// truncateOracle 是 TS 侧真实 truncateBlock / stripFirstMarkdownTable 的产出。
// 生成命令：npx tsx go/testdata/truncate/gen-oracle.ts
type truncateOracle struct {
	Table map[string]struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"table"`
	Truncate map[string]struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
		Cap  int    `json:"cap"`
		Out  string `json:"out"`
	} `json:"truncate"`
}

func loadTruncateOracle(t *testing.T) truncateOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "truncate", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/truncate/gen-oracle.ts", path, err)
	}
	var o truncateOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestStripFirstMarkdownTableParity —— 表格剥离与 TS 逐字节相同。
func TestStripFirstMarkdownTableParity(t *testing.T) {
	o := loadTruncateOracle(t)
	if len(o.Table) == 0 {
		t.Fatal("oracle 无 table 用例")
	}
	for name, c := range o.Table {
		t.Run(name, func(t *testing.T) {
			got := StripFirstMarkdownTable(c.In)
			if got != c.Out {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, c.Out)
			}
		})
	}
}

// TestTruncateBlockParity —— 块截断与 TS 逐字节相同。
//
// 这是本层最难的断言：truncateBlock 有三重 UTF-16 语义
//  1. `block.length <= maxChars` —— code unit 数比较
//  2. `content.slice(0, N)`     —— 切断代理对 → U+FFFD
//  3. 截断标记嵌入 `block.length` —— code unit 数
func TestTruncateBlockParity(t *testing.T) {
	o := loadTruncateOracle(t)
	if len(o.Truncate) == 0 {
		t.Fatal("oracle 无 truncate 用例")
	}
	for name, c := range o.Truncate {
		t.Run(name, func(t *testing.T) {
			got := TruncateBlock(c.Text, c.Cap, c.Kind)
			if got != c.Out {
				t.Errorf("不等价（kind=%s cap=%d）\n  Go 长度=%d 字节=% x\n  TS 长度=%d 字节=% x\n  Go =%q\n  TS =%q",
					c.Kind, c.Cap, len(got), []byte(got), len(c.Out), []byte(c.Out), got, c.Out)
			}
		})
	}
}

// TestTruncateBlockEmojiSurrogate —— 独立锁定"切断代理对 → U+FFFD"这一不变量。
//
// TS 的 slice 会切断代理对，产生孤立代理；其 UTF-8 编码为 U+FFFD（ef bf bd）。
// Go 无法表示孤立代理，但必须产出同样的字节。
func TestTruncateBlockEmojiSurrogate(t *testing.T) {
	// 30 个 emoji（code unit 60）+ 标签 = 91 code unit，cap=30 会切在代理对中间
	text := "<seed-capsule>\n" + rep("😀", 30) + "\n</seed-capsule>"
	got := TruncateBlock(text, 30, "seed-capsule")

	// 必须含 U+FFFD（孤立代理的 UTF-8 形态）
	if !contains(got, "\uFFFD") {
		t.Errorf("切断代理对处应产生 U+FFFD，实际输出：%q", got)
	}
	// 不得含孤立代理的原始 UTF-8 序列（Go 里不可能，但确认没走别的路径）
	if contains(got, "\xed\xa0\xbd") {
		t.Error("不应出现 CESU-8 形态的孤立代理")
	}
	// 截断标记里的长度必须是 code unit 数（91），不是码点数（61）
	if !contains(got, "91") {
		t.Errorf("截断标记应含 code unit 数 91，实际：%q", got)
	}
}

// TestTruncateBlockPassthrough —— 未超预算时原样返回。
func TestTruncateBlockPassthrough(t *testing.T) {
	s := "short text"
	if got := TruncateBlock(s, 100, "any"); got != s {
		t.Errorf("未超预算应原样返回：got %q", got)
	}
}

// TestTruncateBlockCodebaseIndexBranch —— codebase-index 走特殊分支（不同标记文案）。
func TestTruncateBlockCodebaseIndexBranch(t *testing.T) {
	text := rep("z", 50)
	got := TruncateBlock(text, 20, "codebase-index")
	if !contains(got, "codebase index truncated") {
		t.Errorf("codebase-index 应走特殊标记，实际：%q", got)
	}
	if !contains(got, "use repo_map/repo_graph") {
		t.Errorf("codebase-index 标记应含工具指引，实际：%q", got)
	}
}
