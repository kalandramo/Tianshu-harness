package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// projinstOracle 是 TS 侧真实 splitSections / selectSections /
// selectProjectInstructions 的产出。
// 生成命令：npx tsx go/testdata/projinst/gen-oracle.ts
type projinstOracle struct {
	Split map[string]struct {
		MD       string `json:"md"`
		Sections []struct {
			Heading string `json:"heading"`
			Title   string `json:"title"`
			Text    string `json:"text"`
			Tier    int    `json:"tier"`
		} `json:"sections"`
	} `json:"split"`
	Select map[string]struct {
		MD             string   `json:"md"`
		Budget         int      `json:"budget"`
		MeasureEscaped bool     `json:"measureEscaped"`
		Text           string   `json:"text"`
		Omitted        []string `json:"omitted"`
	} `json:"select"`
	Greedy map[string]struct {
		Text    string   `json:"text"`
		Omitted []string `json:"omitted"`
	} `json:"greedy"`
	Escape map[string]string `json:"escape"`
}

func loadProjinstOracle(t *testing.T) projinstOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "projinst", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/projinst/gen-oracle.ts", path, err)
	}
	var o projinstOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestSplitSectionsParity —— 切分结果与 TS 逐字节相同（含 tier 判定）。
func TestSplitSectionsParity(t *testing.T) {
	o := loadProjinstOracle(t)
	if len(o.Split) == 0 {
		t.Fatal("oracle 无 split 用例")
	}
	for name, c := range o.Split {
		t.Run(name, func(t *testing.T) {
			got := SplitSections(c.MD)
			if len(got) != len(c.Sections) {
				t.Fatalf("节数不符：Go=%d TS=%d\n  Go titles=%v", len(got), len(c.Sections), titles(got))
			}
			for i, want := range c.Sections {
				g := got[i]
				if g.Heading != want.Heading {
					t.Errorf("节 %d Heading：Go=%q TS=%q", i, g.Heading, want.Heading)
				}
				if g.Title != want.Title {
					t.Errorf("节 %d Title：Go=%q TS=%q", i, g.Title, want.Title)
				}
				if g.Text != want.Text {
					t.Errorf("节 %d Text：Go=%q TS=%q", i, g.Text, want.Text)
				}
				if int(g.Tier) != want.Tier {
					t.Errorf("节 %d Tier：Go=%d TS=%d", i, g.Tier, want.Tier)
				}
			}
		})
	}
}

// TestSelectSectionsGreedyParity —— 贪心选取在各预算点上与 TS 一致。
// 这条锁定 sep 计费与 tier 顺序。
func TestSelectSectionsGreedyParity(t *testing.T) {
	o := loadProjinstOracle(t)
	if len(o.Greedy) == 0 {
		t.Fatal("oracle 无 greedy 用例")
	}
	// 用与生成器相同的文档重建 sections
	doc := "## 甲\n" + rep("A", 50) + "\n## 乙\n" + rep("B", 50) + "\n## 丙\n" + rep("C", 50)
	sections := SplitSections(doc)

	for budgetStr, want := range o.Greedy {
		t.Run("budget="+budgetStr, func(t *testing.T) {
			budget := atoi(budgetStr)
			got := SelectSections(sections, budget, nil)
			if got.Text != want.Text {
				t.Errorf("Text 不符\n  Go 长度=%d TS 长度=%d\n  Go =%q\n  TS =%q",
					len([]rune(got.Text)), len([]rune(want.Text)), got.Text, want.Text)
			}
			if !eqStrs(got.Omitted, want.Omitted) {
				t.Errorf("Omitted：Go=%v TS=%v", got.Omitted, want.Omitted)
			}
		})
	}
}

// TestSelectProjectInstructionsParity —— 端到端选取与 TS 一致。
// 这条锁定两轮预留、非单调回退、空块回退。
func TestSelectProjectInstructionsParity(t *testing.T) {
	o := loadProjinstOracle(t)
	if len(o.Select) == 0 {
		t.Fatal("oracle 无 select 用例")
	}
	for name, c := range o.Select {
		t.Run(name, func(t *testing.T) {
			var measure func(string) int
			if c.MeasureEscaped {
				measure = func(s string) int { return len([]rune(EscapeXML(s))) }
			}
			got := SelectProjectInstructions(c.MD, c.Budget, measure)
			if got.Text != c.Text {
				t.Errorf("Text 不符（budget=%d measureEscaped=%v）\n  Go 长度=%d TS 长度=%d\n  Go =%q\n  TS =%q",
					c.Budget, c.MeasureEscaped, len([]rune(got.Text)), len([]rune(c.Text)),
					trunc2(got.Text, 200), trunc2(c.Text, 200))
			}
			if !eqStrs(got.Omitted, c.Omitted) {
				t.Errorf("Omitted：Go=%v TS=%v", got.Omitted, c.Omitted)
			}
		})
	}
}

// TestEscapeXMLParity —— 转义语义与 TS 一致。
// 关键锁定：单引号**不**转义；`&` 先转避免二次转义。
func TestEscapeXMLParity(t *testing.T) {
	o := loadProjinstOracle(t)
	if len(o.Escape) == 0 {
		t.Fatal("oracle 无 escape 用例")
	}
	for input, want := range o.Escape {
		t.Run(input, func(t *testing.T) {
			if got := EscapeXML(input); got != want {
				t.Errorf("EscapeXML(%q)：Go=%q TS=%q", input, got, want)
			}
		})
	}
}

// TestEscapeXMLOrderInvariant —— 独立验证 `&` 先转这一不变量。
// 若把 & 放到最后替换，`<` → `&lt;` 会再被转成 `&amp;lt;`。
func TestEscapeXMLOrderInvariant(t *testing.T) {
	if got := EscapeXML("<"); got != "&lt;" {
		t.Errorf("EscapeXML(\"<\") = %q，期望 \"&lt;\"（& 若后转会得到 &amp;lt;）", got)
	}
	if got := EscapeXML("'"); got != "'" {
		t.Errorf("EscapeXML(\"'\") = %q，期望不转义的单引号", got)
	}
}

// TestClassifyTableBeforeGate —— 表格判定必须先于门禁词判定。
// 索引表里偶然出现「必须」不应把整张表提到最高优先级。
func TestClassifyTableBeforeGate(t *testing.T) {
	// 表格占比 >= 0.4，且标题命中门禁词——仍应为 Reference
	tableWithGate := "| a | b |\n| - | - |\n| 必须 | x |\n| c | d |"
	if got := classify("## 安全规范", tableWithGate); got != TierReference {
		t.Errorf("表格优先判定失败：got tier=%d，期望 Reference(%d)", got, TierReference)
	}
	// 无表格但标题命中门禁词 → Gate
	if got := classify("## 安全规范", "普通散文"); got != TierGate {
		t.Errorf("标题门禁判定失败：got tier=%d，期望 Gate(%d)", got, TierGate)
	}
	// 无表格、标题不命中、正文命中 → Gate
	if got := classify("## 说明", "这是必须遵守的内容"); got != TierGate {
		t.Errorf("正文门禁判定失败：got tier=%d，期望 Gate(%d)", got, TierGate)
	}
	// 都不命中 → Prose
	if got := classify("## 说明", "普通散文"); got != TierProse {
		t.Errorf("散文判定失败：got tier=%d，期望 Prose(%d)", got, TierProse)
	}
}

// TestSplitSectionsFenceInvariant —— 围栏内的标题行不得被当作标题。
func TestSplitSectionsFenceInvariant(t *testing.T) {
	md := "## 真标题\n```md\n## 伪标题\n```\n## 后标题"
	got := SplitSections(md)
	if len(got) != 2 {
		t.Fatalf("围栏处理失败：得到 %d 节（期望 2）\n  titles=%v", len(got), titles(got))
	}
	if got[0].Title != "真标题" || got[1].Title != "后标题" {
		t.Errorf("标题切分错误：%v", titles(got))
	}
	if !contains(got[0].Text, "## 伪标题") {
		t.Error("围栏内的伪标题应保留在首节正文中")
	}
}

// TestSplitSectionsHashBoundary —— `#` 也是边界（两份文档拼接的分界）。
func TestSplitSectionsHashBoundary(t *testing.T) {
	md := "# 文档一\n内容\n# 文档二\n内容"
	got := SplitSections(md)
	if len(got) != 2 {
		t.Fatalf("`#` 边界处理失败：得到 %d 节（期望 2）\n  titles=%v", len(got), titles(got))
	}
}

// TestSplitSectionsTripleHashNotBoundary —— `###` 不是边界。
func TestSplitSectionsTripleHashNotBoundary(t *testing.T) {
	md := "## 节\n### 子节\n内容"
	got := SplitSections(md)
	if len(got) != 1 {
		t.Fatalf("`###` 不应拆节：得到 %d 节（期望 1）", len(got))
	}
	if !contains(got[0].Text, "### 子节") {
		t.Error("`### 子节` 应保留在节正文中")
	}
}

// TestSelectImpossibleBudgetReturnsOriginal —— 预算装不下任何节时退回原文。
func TestSelectImpossibleBudgetReturnsOriginal(t *testing.T) {
	md := "## 甲\n内容内容内容"
	got := SelectProjectInstructions(md, 1, nil)
	if got.Text != md {
		t.Errorf("预算不足时应退回原文整块\n  Go =%q\n  期望 =%q", got.Text, md)
	}
	if len(got.Omitted) != 0 {
		t.Errorf("退回时不应有略去标记，got %v", got.Omitted)
	}
}

// TestSelectAmpleBudgetPassthrough —— 预算充足时原样返回。
func TestSelectAmpleBudgetPassthrough(t *testing.T) {
	md := "## 甲\n内容"
	got := SelectProjectInstructions(md, 100000, nil)
	if got.Text != md || len(got.Omitted) != 0 {
		t.Errorf("预算充足应原样返回：Text=%q Omitted=%v", got.Text, got.Omitted)
	}
}

// ── 辅助 ─────────────────────────────────────────────────────────

func titles(ss []DocSection) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Title
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func rep(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func trunc2(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
