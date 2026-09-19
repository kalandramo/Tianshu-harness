package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// hashlineOracle 是 TS 侧真实 hashLine / buildFreshAnchors 的产出。
// 生成命令：npx tsx go/testdata/hashline/gen-oracle.ts
type hashlineOracle struct {
	Hashes map[string]string `json:"hashes"`
	CRLF   struct {
		Plain  string `json:"plain"`
		WithCR string `json:"withCR"`
		Equal  bool   `json:"equal"`
	} `json:"crlfInvariant"`
	Fresh map[string]struct {
		Lines  []string `json:"lines"`
		Start0 int      `json:"start0"`
		Count  int      `json:"count"`
		Out    string   `json:"out"`
		Note   string   `json:"note"`
	} `json:"fresh"`
}

func loadHashlineOracle(t *testing.T) hashlineOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "hashline", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/hashline/gen-oracle.ts", path, err)
	}
	var o hashlineOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestHashLineParity —— 行哈希与 TS 逐字节相同。
func TestHashLineParity(t *testing.T) {
	o := loadHashlineOracle(t)
	if len(o.Hashes) == 0 {
		t.Fatal("oracle 无 hashes")
	}
	for line, want := range o.Hashes {
		t.Run(line, func(t *testing.T) {
			if got := HashLine(line); got != want {
				t.Errorf("HashLine(%q)：Go=%s TS=%s", line, got, want)
			}
		})
	}
}

// TestHashLineCRLFNormalization —— 行尾 \r 应被剥离（CRLF 归一化）。
//
// 这是 hashLine 最易漏的语义：不剥 \r 会让同一文件在 LF/CRLF 检出的锚点
// 不一致，跨平台编辑时锚点失配。
func TestHashLineCRLFNormalization(t *testing.T) {
	o := loadHashlineOracle(t)
	if !o.CRLF.Equal {
		t.Fatal("前提失败：TS 侧 'abc' 与 'abc\\r' 的哈希应相同")
	}
	if HashLine("abc") != HashLine("abc\r") {
		t.Error("'abc' 与 'abc\\r' 应哈希相同（行尾 \\r 应被剥离）")
	}
	if HashLine("abc") != o.CRLF.Plain {
		t.Errorf("HashLine(\"abc\") = %s，TS 为 %s", HashLine("abc"), o.CRLF.Plain)
	}
	// 中间的 \r 不剥（只剥行尾）
	if HashLine("a\rb") == HashLine("ab") {
		t.Error("行中间的 \\r 不应被剥离")
	}
}

// TestHashLineDeterministic —— 哈希确定且长度固定（8 位十六进制）。
func TestHashLineDeterministic(t *testing.T) {
	h := HashLine("some line")
	if len(h) != 8 {
		t.Errorf("哈希应为 8 位，得到 %d 位：%q", len(h), h)
	}
	if HashLine("some line") != h {
		t.Error("同一输入应产出同一哈希")
	}
}

// TestBuildFreshAnchorsParity —— 新鲜锚点与 TS 逐字节相同。
func TestBuildFreshAnchorsParity(t *testing.T) {
	o := loadHashlineOracle(t)
	if len(o.Fresh) == 0 {
		t.Fatal("oracle 无 fresh 用例")
	}
	for name, c := range o.Fresh {
		t.Run(name, func(t *testing.T) {
			got := BuildFreshAnchors(c.Lines, c.Start0, c.Count)
			if got != c.Out {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, c.Out)
			}
		})
	}
}

// TestBuildFreshAnchorsStructure —— 锚点结构（4 个条件分支）。
func TestBuildFreshAnchorsStructure(t *testing.T) {
	// 中间插入：应有 4 个锚点（前文/首行/末行/后文）
	got := BuildFreshAnchors([]string{"a", "b", "n1", "n2", "c", "d"}, 2, 2)
	for _, want := range []string{"L2:", "L3:", "L4:", "L5:"} {
		if !contains(got, want) {
			t.Errorf("中间插入应含 %s，实际 %q", want, got)
		}
	}
	// 单行替换：应只有 3 个（前文/首行/后文，无「末行」因 count=1）
	got2 := BuildFreshAnchors([]string{"a", "new", "c"}, 1, 1)
	if contains(got2, "L2:\n") {
		t.Error("单行替换不应有重复的末行锚点")
	}
	// 空文件：无锚点
	if got3 := BuildFreshAnchors([]string{}, 0, 0); got3 != "" {
		t.Errorf("空文件应无锚点，实际 %q", got3)
	}
}

// TestBuildFreshAnchorsTruncation —— 超 80 字符的内容应截断加省略号。
func TestBuildFreshAnchorsTruncation(t *testing.T) {
	long := rep("y", 200)
	got := BuildFreshAnchors([]string{"a", long, "c"}, 1, 1)
	if contains(got, long) {
		t.Error("超长内容应被截断")
	}
	if !contains(got, "…") {
		t.Errorf("截断后应加省略号，实际 %q", got)
	}
}

// TestBuildFreshAnchorsTrimsTrailingWhitespace —— 内容展示应去行尾空白，
// 但**哈希用原始行**。
func TestBuildFreshAnchorsTrimsTrailingWhitespace(t *testing.T) {
	// 行尾有空格与 \r
	got := BuildFreshAnchors([]string{"a", "content   \r", "c"}, 1, 1)
	if contains(got, "content   ") {
		t.Errorf("内容展示应去行尾空白，实际 %q", got)
	}
	if !contains(got, "content") {
		t.Errorf("应保留内容主体，实际 %q", got)
	}
	// 哈希应等于原始行的哈希（含 \r 被归一化）
	wantHash := HashLine("content   \r")
	if !contains(got, wantHash) {
		t.Errorf("哈希应为原始行的哈希 %s，实际 %q", wantHash, got)
	}
}
