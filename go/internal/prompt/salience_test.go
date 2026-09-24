package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// oracle 结构 —— 对账 go/testdata/salience/oracle.json（由真实 TS 实现生成）。
type salienceOracle struct {
	Meta struct {
		Source      string `json:"source"`
		GeneratedBy string `json:"generatedBy"`
	} `json:"meta"`
	Salience []struct {
		Input    string  `json:"input"`
		Salience float64 `json:"salience"`
	} `json:"salience"`
	TopK []struct {
		Name          string    `json:"name"`
		MaxChars      int       `json:"maxChars"`
		InputOrder    []float64 `json:"inputOrder"`
		InputContents []string  `json:"inputContents"`
		InputSources  []*string `json:"inputSources"`
		Selected      []struct {
			Content  string  `json:"content"`
			Salience float64 `json:"salience"`
			Source   *string `json:"source"`
		} `json:"selected"`
	} `json:"topK"`
}

func loadSalienceOracle(t *testing.T) *salienceOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "salience", "oracle.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（%s）：%v\n先跑：npx tsx go/testdata/salience/gen-oracle.ts > go/testdata/salience/oracle.json", p, err)
	}
	var o salienceOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.Salience) == 0 || len(o.TopK) == 0 {
		t.Fatalf("oracle 为空：salience=%d topK=%d", len(o.Salience), len(o.TopK))
	}
	return &o
}

// TestAssignSalienceParity —— 逐值对账 TS `assignSalience`。
//
// 判据来源：**真实 TS 实现**生成的 oracle（非手抄）。规则表有 30+ 条
// startsWith 分支，手抄必漏——oracle 把 TS 实现本身当唯一真相源。
func TestAssignSalienceParity(t *testing.T) {
	o := loadSalienceOracle(t)

	passed := 0
	for _, c := range o.Salience {
		got := AssignSalience(c.Input)
		if got != c.Salience {
			t.Errorf("输入 %q：got=%v want=%v", truncateForMsg(c.Input), got, c.Salience)
			continue
		}
		passed++
	}
	t.Logf("salience 对账：%d/%d 通过", passed, len(o.Salience))
}

// TestSelectTopKBlocksParity —— 逐值对账 TS `selectTopKBlocks`。
//
// 覆盖：降序排列、稳定排序、预算裁剪（含 continue 语义）、blockCap 截断、
// source 元数据保留、边界（空输入/预算 0/单块）。
func TestSelectTopKBlocksParity(t *testing.T) {
	o := loadSalienceOracle(t)

	for _, c := range o.TopK {
		t.Run(c.Name, func(t *testing.T) {
			blocks := make([]SalientBlock, len(c.InputContents))
			for i, content := range c.InputContents {
				b := SalientBlock{Content: content, Salience: c.InputOrder[i]}
				// 还原输入块的 source（否则 source 透传无法验证）
				if i < len(c.InputSources) && c.InputSources[i] != nil {
					b.Source = *c.InputSources[i]
				}
				blocks[i] = b
			}

			got := SelectTopKBlocks(blocks, c.MaxChars)

			if len(got) != len(c.Selected) {
				t.Fatalf("选中块数：got=%d want=%d", len(got), len(c.Selected))
			}
			for i, want := range c.Selected {
				if got[i].Content != want.Content {
					t.Errorf("第 %d 块内容：got=%q want=%q",
						i, truncateForMsg(got[i].Content), truncateForMsg(want.Content))
				}
				if got[i].Salience != want.Salience {
					t.Errorf("第 %d 块 salience：got=%v want=%v", i, got[i].Salience, want.Salience)
				}
				if want.Source != nil && got[i].Source != *want.Source {
					t.Errorf("第 %d 块 source：got=%q want=%q", i, got[i].Source, *want.Source)
				}
			}
		})
	}
}

// TestSelectTopKBlocksStability —— ★ 排序稳定性（本刀最关键的对账点）。
//
// **为什么单独测**：TS 用 `[...blocks].sort((a,b) => b.salience - a.salience)`，
// JS 的 Array.sort 在 V8 是**稳定排序**（同 salience 保持输入顺序）。
// Go 的 `sort.Slice` **不稳定**——同 salience 块的输出顺序会不同，进而改变
// appendix 字节 → 打断前缀缓存（本刀的目标正是缓存命中率）。
//
// 判据：必须用 `sort.SliceStable`。
func TestSelectTopKBlocksStability(t *testing.T) {
	t.Run("同salience保持输入顺序", func(t *testing.T) {
		// 8 个同 salience 块，内容可辨识
		var blocks []SalientBlock
		want := []string{}
		for i := 0; i < 8; i++ {
			name := string(rune('A' + i))
			blocks = append(blocks, SalientBlock{Content: name, Salience: 0.7})
			want = append(want, name)
		}

		got := SelectTopKBlocks(blocks, 1000)
		if len(got) != len(want) {
			t.Fatalf("块数：got=%d want=%d", len(got), len(want))
		}
		for i := range want {
			if got[i].Content != want[i] {
				t.Errorf("★ 稳定排序被破坏：第 %d 位 got=%q want=%q（顺序 %v）",
					i, got[i].Content, want[i], contentsOf(got))
			}
		}
	})

	t.Run("★同salience大批量", func(t *testing.T) {
		// 50 个同 salience 块——Go 的不稳定排序（pdqsort）在大切片上
		// 更容易暴露顺序错乱；小块数可能偶然保序。
		var blocks []SalientBlock
		for i := 0; i < 50; i++ {
			blocks = append(blocks, SalientBlock{
				Content:  string(rune('a'+i%26)) + "-" + strconv.Itoa(i),
				Salience: 0.5,
			})
		}
		got := SelectTopKBlocks(blocks, 100000)
		if len(got) != 50 {
			t.Fatalf("块数：got=%d want=50", len(got))
		}
		for i := 0; i < 50; i++ {
			want := string(rune('a'+i%26)) + "-" + strconv.Itoa(i)
			if got[i].Content != want {
				t.Fatalf("★ 稳定排序被破坏（第 %d 位）：got=%q want=%q", i, got[i].Content, want)
			}
		}
	})

	t.Run("★散布高分块", func(t *testing.T) {
		// ★ 这是**真正能打红 sort.Slice** 的用例。
		//
		// 探针实测（200 块，i%37==0 处插 0.9）：
		//   sort.Slice       逆序对数 = 6   ← 乱序
		//   sort.SliceStable 逆序对数 = 0   ← 保序
		//
		// 为什么前面的用例打不红：全等值输入下 pdqsort 恰好不换位；只有
		// **少数高分块散布在大量同分块中**时，分区/交换才会打乱同分块的
		// 相对顺序。这是本刀最关键的判别力来源。
		const n = 200
		var blocks []SalientBlock
		origIdx := map[string]int{}
		for i := 0; i < n; i++ {
			s := 0.5
			if i%37 == 0 {
				s = 0.9
			}
			c := "c" + strconv.Itoa(i)
			blocks = append(blocks, SalientBlock{Content: c, Salience: s})
			origIdx[c] = i
		}

		got := SelectTopKBlocks(blocks, 1000000)
		if len(got) != n {
			t.Fatalf("块数：got=%d want=%d", len(got), n)
		}

		// 验证：① 降序 ② 同 salience 组内按原始索引递增
		for i := 1; i < len(got); i++ {
			if got[i].Salience > got[i-1].Salience {
				t.Fatalf("★ 降序被破坏：第 %d 位 salience %v > 前一位 %v",
					i, got[i].Salience, got[i-1].Salience)
			}
			if got[i].Salience == got[i-1].Salience {
				if origIdx[got[i].Content] < origIdx[got[i-1].Content] {
					t.Fatalf("★ 稳定排序被破坏（同 salience 逆序）："+
						"第 %d 位 %q(orig=%d) 在第 %d 位 %q(orig=%d) 之后",
						i-1, got[i-1].Content, origIdx[got[i-1].Content],
						i, got[i].Content, origIdx[got[i].Content])
				}
			}
		}
	})

	t.Run("跨档保序", func(t *testing.T) {
		// 同档内保序 + 跨档降序：0.9 组在 0.5 组之前，组内保持输入顺序
		blocks := []SalientBlock{
			{Content: "low1", Salience: 0.3},
			{Content: "high1", Salience: 0.9},
			{Content: "low2", Salience: 0.3},
			{Content: "high2", Salience: 0.9},
			{Content: "mid", Salience: 0.5},
		}
		got := SelectTopKBlocks(blocks, 1000)
		want := []string{"high1", "high2", "mid", "low1", "low2"}
		for i := range want {
			if got[i].Content != want[i] {
				t.Errorf("第 %d 位：got=%q want=%q（全序列 %v）", i, got[i].Content, want[i], contentsOf(got))
			}
		}
	})
}

// TestSelectTopKBlocksSemantics —— 关键语义单独钉住（不依赖 oracle）。
func TestSelectTopKBlocksSemantics(t *testing.T) {
	t.Run("★至少保留一个块", func(t *testing.T) {
		// 预算 0 仍应保留最高 salience 的块（TS 的 selected.length > 0 短路）
		blocks := []SalientBlock{
			{Content: "0123456789", Salience: 0.9},
			{Content: "0123456789", Salience: 0.8},
		}
		got := SelectTopKBlocks(blocks, 0)
		if len(got) != 1 {
			t.Fatalf("预算 0 应保留 1 块：got=%d", len(got))
		}
		if got[0].Salience != 0.9 {
			t.Errorf("应保留最高 salience：got=%v", got[0].Salience)
		}
	})

	t.Run("★超预算是continue非break", func(t *testing.T) {
		// 中间块放不下时，后面的小块若放得下仍应入选。
		// 若误用 break，c 会丢失。
		blocks := []SalientBlock{
			{Content: strings.Repeat("a", 100), Salience: 0.9},
			{Content: strings.Repeat("b", 100), Salience: 0.8}, // 100+2+100=202 > 150 → 跳过
			{Content: "c", Salience: 0.7},                      // 100+2+1=103 <= 150 → 入选
		}
		got := SelectTopKBlocks(blocks, 150)
		if len(got) != 2 {
			t.Fatalf("应选中 2 块（跳过 b 但继续收 c）：got=%d（%v）", len(got), contentsOf(got))
		}
		if got[1].Content != "c" {
			t.Errorf("★ continue 语义被破坏（误用 break？）：第 2 块 got=%q want=c", got[1].Content)
		}
	})

	t.Run("★blockCap截断加truncated标记", func(t *testing.T) {
		// maxChars=10000 → blockCap = max(floor(10000*0.4), 2000) = 4000
		// 5000 字符内容 → 截断到 4000 + "\n[truncated]" = 4012
		blocks := []SalientBlock{{Content: strings.Repeat("x", 5000), Salience: 0.9}}
		got := SelectTopKBlocks(blocks, 10000)
		if len(got) != 1 {
			t.Fatalf("应 1 块：got=%d", len(got))
		}
		if len(got[0].Content) != 4012 {
			t.Errorf("截断后长度：got=%d want=4012", len(got[0].Content))
		}
		if !hasSuffix(got[0].Content, "\n[truncated]") {
			t.Errorf("截断标记缺失：尾部 %q", tailOf(got[0].Content, 20))
		}
	})

	t.Run("★blockCap下限2000", func(t *testing.T) {
		// maxChars=3000 → floor(3000*0.4)=1200 → max(1200,2000)=2000
		// 2500 字符 → 截断到 2000 + 12 = 2012
		blocks := []SalientBlock{{Content: strings.Repeat("y", 2500), Salience: 0.9}}
		got := SelectTopKBlocks(blocks, 3000)
		if len(got[0].Content) != 2012 {
			t.Errorf("blockCap 下限应为 2000：got len=%d want=2012", len(got[0].Content))
		}
	})

	t.Run("未超blockCap不截断", func(t *testing.T) {
		blocks := []SalientBlock{{Content: strings.Repeat("x", 3999), Salience: 0.9}}
		got := SelectTopKBlocks(blocks, 10000)
		if len(got[0].Content) != 3999 {
			t.Errorf("3999 <= 4000 不该截断：got=%d", len(got[0].Content))
		}
	})

	t.Run("空输入返回空", func(t *testing.T) {
		got := SelectTopKBlocks(nil, 1000)
		if len(got) != 0 {
			t.Errorf("空输入应返回空：got=%d", len(got))
		}
	})

	t.Run("★overhead第2块起加2", func(t *testing.T) {
		// 每块 10 字符：10 + (2+10) + (2+10) = 34 恰好放 3 块
		blocks := []SalientBlock{
			{Content: strings.Repeat("0", 10), Salience: 0.9},
			{Content: strings.Repeat("1", 10), Salience: 0.8},
			{Content: strings.Repeat("2", 10), Salience: 0.7},
		}
		if got := SelectTopKBlocks(blocks, 34); len(got) != 3 {
			t.Errorf("预算 34 应放 3 块：got=%d", len(got))
		}
		if got := SelectTopKBlocks(blocks, 33); len(got) != 2 {
			t.Errorf("预算 33 应放 2 块：got=%d", len(got))
		}
	})
}

// ── 测试辅助 ──

func truncateForMsg(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "…"
}

func contentsOf(bs []SalientBlock) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = truncateForMsg(b.Content)
	}
	return out
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
