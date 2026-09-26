package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// generalledger_oracle_test.go —— 对账 TS 真实输出（第七十七刀）。
//
// oracle 生成：`cd <仓库根> && npx tsx go/testdata/generalledger/gen-oracle.ts`

type glOracle struct {
	GeneratedBy string `json:"generatedBy"`
	SlugCases   []struct {
		Input  string  `json:"input"`
		Output *string `json:"output"`
	} `json:"slugCases"`
	PathCases []struct {
		Input struct {
			Cwd  string `json:"cwd"`
			Slug string `json:"slug"`
		} `json:"input"`
		Output string `json:"output"`
	} `json:"pathCases"`
	ParseCases []struct {
		Name   string         `json:"name"`
		Input  string         `json:"input"`
		Output []LedgerFamily `json:"output"`
	} `json:"parseCases"`
}

func loadGLOracle(t *testing.T) glOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "generalledger", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 npx tsx go/testdata/generalledger/gen-oracle.ts）: %v", err)
	}
	var doc glOracle
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 oracle 失败: %v", err)
	}
	if len(doc.SlugCases) == 0 || len(doc.PathCases) == 0 || len(doc.ParseCases) == 0 {
		t.Fatal("oracle 用例为空——生成器没跑成功")
	}
	// **断言生成器来源**（对账 `planmode_test.go:55` 的既有模式）：
	// 防「oracle.json 被别的生成器覆盖」——那会让本测试对账**另一组输入**，
	// 静默假绿。这是交付门禁的「wrote-but-never-read」提示指向的真实缺口。
	if doc.GeneratedBy != "go/testdata/generalledger/gen-oracle.ts" {
		t.Fatalf("oracle 生成器不符：got=%q want=%q（oracle.json 可能被覆盖）",
			doc.GeneratedBy, "go/testdata/generalledger/gen-oracle.ts")
	}
	return doc
}

// TestStarToGeneralSlugParity —— 逐用例对账 TS。
func TestStarToGeneralSlugParity(t *testing.T) {
	doc := loadGLOracle(t)
	t.Logf("oracle：slug %d / path %d / parse %d",
		len(doc.SlugCases), len(doc.PathCases), len(doc.ParseCases))

	// 覆盖断言：确保 oracle 同时含命中与未命中（防生成器退化）
	hits, misses := 0, 0
	for _, c := range doc.SlugCases {
		if c.Output == nil {
			misses++
		} else {
			hits++
		}
	}
	if hits == 0 || misses == 0 {
		t.Errorf("oracle 覆盖不足：命中 %d / 未命中 %d", hits, misses)
	}

	for _, c := range doc.SlugCases {
		got, ok := StarToGeneralSlug(c.Input)
		if c.Output == nil {
			if ok {
				t.Errorf("%q：TS 返回 null，Go 返回 (%q, true)", c.Input, got)
			}
			continue
		}
		if !ok {
			t.Errorf("%q：TS 返回 %q，Go 返回未命中", c.Input, *c.Output)
			continue
		}
		if got != *c.Output {
			t.Errorf("%q：TS=%q Go=%q", c.Input, *c.Output, got)
		}
	}
}

// TestGeneralLedgerPathParity —— 路径推导逐用例对账。
func TestGeneralLedgerPathParity(t *testing.T) {
	doc := loadGLOracle(t)
	for _, c := range doc.PathCases {
		got := GeneralLedgerPath(c.Input.Cwd, c.Input.Slug)
		// TS 用 `join()` 产出 POSIX 分隔符；Go 的 filepath.Join 在 macOS 上同形。
		// **不做分隔符归一化**——平台差异应在移植时就暴露（Windows 上会不同，
		// 但 Go 侧路径处理走 filepath 是正确的平台行为）。
		if got != c.Output {
			t.Errorf("cwd=%q slug=%q：TS=%q Go=%q", c.Input.Cwd, c.Input.Slug, c.Output, got)
		}
	}
}

// TestParseLedgerFamiliesParity —— markdown 解析逐用例对账。
func TestParseLedgerFamiliesParity(t *testing.T) {
	doc := loadGLOracle(t)
	for _, c := range doc.ParseCases {
		got := ParseLedgerFamilies(c.Input)
		if len(got) != len(c.Output) {
			t.Errorf("%s：族数 TS=%d Go=%d（TS=%v Go=%v）",
				c.Name, len(c.Output), len(got), c.Output, got)
			continue
		}
		for i := range got {
			if got[i] != c.Output[i] {
				t.Errorf("%s[%d]：TS=%+v Go=%+v", c.Name, i, c.Output[i], got[i])
			}
		}
	}
}

// TestAppendGeneralFindingCreatesAndRecurs —— 文件 I/O 行为（oracle 不覆盖）。
//
// 验两态：首次创建（count=1）→ 同族复发（count=2、实例行追加）。
func TestAppendGeneralFindingCreatesAndRecurs(t *testing.T) {
	dir := t.TempDir()

	// ① 首次
	r1, ok := AppendGeneralFinding(dir, GeneralFindingInput{
		Star: "天权", Family: "类型断言失败", Note: "首次",
	})
	if !ok {
		t.Fatal("首次追加应成功")
	}
	if !r1.Created || r1.RecurrenceCount != 1 {
		t.Errorf("首次应为 Created=true count=1，实得 %+v", r1)
	}

	// ② 同族复发
	r2, ok := AppendGeneralFinding(dir, GeneralFindingInput{
		Star: "天权", Family: "类型断言失败", Note: "复发",
	})
	if !ok {
		t.Fatal("复发追加应成功")
	}
	if r2.Created || r2.RecurrenceCount != 2 {
		t.Errorf("复发应为 Created=false count=2，实得 %+v", r2)
	}

	// ③ 解析验证（计数与实例行都落盘）
	_, content, ok := ReadGeneralLedger(dir, "天权")
	if !ok {
		t.Fatal("账本应可读")
	}
	fams := ParseLedgerFamilies(content)
	if len(fams) != 1 || fams[0].RecurrenceCount != 2 {
		t.Errorf("应有 1 族 count=2，实得 %+v", fams)
	}
	if !contains(content, "首次") || !contains(content, "复发") {
		t.Errorf("两个实例行都应在账本里：\n%s", content)
	}

	// ④ 另一族（不复发路径）
	r3, ok := AppendGeneralFinding(dir, GeneralFindingInput{
		Star: "天权", Family: "另一个族", Note: "新族",
	})
	if !ok || r3.Created || r3.RecurrenceCount != 1 {
		t.Errorf("新族应为 Created=false count=1（文件已存在），实得 %+v", r3)
	}
}

// TestAppendGeneralFindingUnknownStar —— 未知星域返回 false（不建文件）。
func TestAppendGeneralFindingUnknownStar(t *testing.T) {
	dir := t.TempDir()
	if _, ok := AppendGeneralFinding(dir, GeneralFindingInput{
		Star: "不存在的星域", Family: "x", Note: "y",
	}); ok {
		t.Error("未知星域应返回 false")
	}
	// 不应创建任何文件
	if _, err := os.Stat(filepath.Join(dir, ".rivet")); err == nil {
		t.Error("未知星域不该创建目录")
	}
}

// TestListGeneralsAndTopFamilies —— 列账本 + Top-N 排序。
func TestListGeneralsAndTopFamilies(t *testing.T) {
	dir := t.TempDir()
	if got := ListGenerals(dir); len(got) != 0 {
		t.Errorf("空目录应返回空，实得 %v", got)
	}

	// 建三个族（计数递增，验排序）
	for _, f := range []struct {
		fam string
		n   int
	}{{"低频", 1}, {"高频", 3}, {"中频", 2}} {
		for i := 0; i < f.n; i++ {
			AppendGeneralFinding(dir, GeneralFindingInput{Star: "天权", Family: f.fam, Note: "x"})
		}
	}

	slugs := ListGenerals(dir)
	if len(slugs) != 1 || slugs[0] != "tianquan" {
		t.Errorf("应列出 [tianquan]，实得 %v", slugs)
	}

	top := TopGeneralFamilies(dir, "天权", 2)
	if len(top) != 2 {
		t.Fatalf("Top-2 应返回 2 项，实得 %d", len(top))
	}
	if top[0].Family != "高频" || top[1].Family != "中频" {
		t.Errorf("应按 count 降序 [高频, 中频]，实得 %v", top)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
