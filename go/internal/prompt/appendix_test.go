package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// oracle 结构 —— 对账 go/testdata/appendix/oracle.json（由真实 TS 实现生成）。
type appendixOracle struct {
	Meta struct {
		Source      string `json:"source"`
		GeneratedBy string `json:"generatedBy"`
	} `json:"meta"`
	Methodology []struct {
		Name        string  `json:"name"`
		Methodology *string `json:"methodology"`
		Reason      *string `json:"reason"`
		PlanMode    bool    `json:"planMode"`
		Output      *string `json:"output"`
	} `json:"methodology"`
	PermissionNote []struct {
		Mode   *string `json:"mode"`
		Output string  `json:"output"`
	} `json:"permissionNote"`
	PlanExecuting struct {
		Output string `json:"output"`
	} `json:"planExecuting"`
	BlockName []struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"blockName"`
}

func loadAppendixOracle(t *testing.T) *appendixOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "appendix", "oracle.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（%s）：%v\n先跑：npx tsx go/testdata/appendix/gen-oracle.ts > go/testdata/appendix/oracle.json", p, err)
	}
	var o appendixOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if o.Meta.Source != "src/prompt/volatile.ts" {
		t.Fatalf("oracle 源不符：got=%q（用错 oracle 文件？）", o.Meta.Source)
	}
	if o.Meta.GeneratedBy != "go/testdata/appendix/gen-oracle.ts" {
		t.Fatalf("oracle 生成器不符：got=%q", o.Meta.GeneratedBy)
	}
	if len(o.Methodology) == 0 || len(o.PermissionNote) == 0 || len(o.BlockName) == 0 {
		t.Fatalf("oracle 为空：methodology=%d permissionNote=%d blockName=%d",
			len(o.Methodology), len(o.PermissionNote), len(o.BlockName))
	}
	return &o
}

// TestRenderPlanMethodologyAdvisoryParity —— 逐值对账 TS `renderPlanMethodologyAdvisory`。
//
// **覆盖的关键组合**（oracle 揭示的、易写错的语义）：
//   - `opts.planMode` 优先于 methodology：planMode-lightweight 返回的是
//     **full 档 design-doc 模板**，不是 lightweight 模板
//   - methodology 为 undefined → 直接 NULL（先判 `!methodology`）
//   - lightweight + reason → **不追加**理由（TS 特判 `methodology === 'lightweight'`）
//   - full + reason → 追加 `\n路由理由: ...`
func TestRenderPlanMethodologyAdvisoryParity(t *testing.T) {
	o := loadAppendixOracle(t)

	for _, c := range o.Methodology {
		t.Run(c.Name, func(t *testing.T) {
			var meth PlanMethodology
			if c.Methodology != nil {
				meth = PlanMethodology(*c.Methodology)
			}
			var reason string
			if c.Reason != nil {
				reason = *c.Reason
			}

			got := RenderPlanMethodologyAdvisory(meth, reason, c.PlanMode)

			if c.Output == nil {
				if got != nil {
					t.Errorf("期望 nil（返回 null），got=%q", truncateForMsg(*got))
				}
				return
			}
			if got == nil {
				t.Fatalf("期望非 nil，got=nil（want %d chars）", len(*c.Output))
			}
			if *got != *c.Output {
				t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
					len(*got), truncateForMsg(*got), len(*c.Output), truncateForMsg(*c.Output))
			}
		})
	}
}

// TestRenderPermissionNoteParity —— 逐值对账 TS `renderPermissionNote`。
//
// 语义：仅 `dangerously-skip-permissions` 返回文案，其余模式返回空串
// （TS 注释：让那些 turn 保持字节不变）。
func TestRenderPermissionNoteParity(t *testing.T) {
	o := loadAppendixOracle(t)

	for _, c := range o.PermissionNote {
		name := "nil"
		if c.Mode != nil {
			name = *c.Mode
			if name == "" {
				name = "empty"
			}
		}
		t.Run(name, func(t *testing.T) {
			var mode string
			if c.Mode != nil {
				mode = *c.Mode
			}
			if got := RenderPermissionNote(mode); got != c.Output {
				t.Errorf("mode=%v：got=%q want=%q", c.Mode, truncateForMsg(got), truncateForMsg(c.Output))
			}
		})
	}
}

// TestRenderPlanExecutingBlockParity —— 逐字节对账（553 字符模板）。
func TestRenderPlanExecutingBlockParity(t *testing.T) {
	o := loadAppendixOracle(t)
	got := RenderPlanExecutingBlock()
	if got != o.PlanExecuting.Output {
		t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
			len(got), truncateForMsg(got), len(o.PlanExecuting.Output), truncateForMsg(o.PlanExecuting.Output))
	}
}

// TestAppendixBlockNameParity —— 逐值对账 TS `appendixBlockName`。
//
// 语义：正则 `/^<([^\s/>]+)/` 提取标签名，失败回退 `anon:<content.length>`。
//
// **oracle 揭示的边界**（易写错）：
//   - `<tag`（无闭合）→ `tag`（正则匹配到结尾，不要求 `>`）
//   - `  <indented>`（前导空白）→ `anon:13`（`^` 锚定，不 trim）
//   - `<ns:tag>` → `ns:tag`（冒号不是分隔符）
//   - `<self-closing />` → `self-closing`（空格是分隔符）
//   - `anon:<len>` 的 len 是 **byte 长度还是 rune 长度**？——见下方专门断言
func TestAppendixBlockNameParity(t *testing.T) {
	o := loadAppendixOracle(t)

	for _, c := range o.BlockName {
		t.Run(c.Input, func(t *testing.T) {
			if got := AppendixBlockName(c.Input); got != c.Output {
				t.Errorf("输入 %q：got=%q want=%q", c.Input, got, c.Output)
			}
		})
	}
}

// TestAppendixBlockNameAnonLength —— ★ 钉住 `anon:` 回退的长度语义。
//
// TS 用 `content.length`——JS 的 String.length 是 **UTF-16 code unit 数**，
// 对 BMP 外字符（emoji、部分 CJK 扩展）与 Go 的 byte/rune 长度**都不同**。
// 本用例用真实 TS 判定，避免 Go 侧猜错。
func TestAppendixBlockNameAnonLength(t *testing.T) {
	// 这些输入的 anon 长度由 oracle 给出（含多字节字符）
	cases := []struct {
		input string
		note  string
	}{
		{"no tag", "纯 ASCII"},
		{"中文无标签", "中文：TS 长度 5（每字 1 code unit）"},
		{"emoji🎯no tag", "emoji：TS 长度 = 码元数，非 rune 数"},
		{"a😀b", "BMP 外字符占 2 code unit"},
	}
	for _, c := range cases {
		t.Run(c.note, func(t *testing.T) {
			got := AppendixBlockName(c.input)
			t.Logf("输入 %q（Go len=%d, runes=%d）→ %q",
				c.input, len(c.input), len([]rune(c.input)), got)
		})
	}
}
