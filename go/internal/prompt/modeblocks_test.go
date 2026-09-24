package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oracle 结构 —— 对账 go/testdata/modeblocks/oracle.json（真实 TS 实现生成）。
type modeBlocksOracle struct {
	Meta struct {
		Source      string `json:"source"`
		GeneratedBy string `json:"generatedBy"`
	} `json:"meta"`
	PlanMode []struct {
		Name        string  `json:"name"`
		Value       *string `json:"value"`
		IsUndefined bool    `json:"isUndefined"`
		Output      string  `json:"output"`
	} `json:"planMode"`
	AskMode struct {
		Output string `json:"output"`
	} `json:"askMode"`
	PlanExit struct {
		Output string `json:"output"`
	} `json:"planExit"`
}

func loadModeBlocksOracle(t *testing.T) *modeBlocksOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "modeblocks", "oracle.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（%s）：%v\n先跑：npx tsx go/testdata/modeblocks/gen-oracle.ts > go/testdata/modeblocks/oracle.json", p, err)
	}
	var o modeBlocksOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if o.Meta.Source != "src/prompt/volatile.ts" {
		t.Fatalf("oracle 源不符：got=%q（用错 oracle 文件？）", o.Meta.Source)
	}
	if o.Meta.GeneratedBy != "go/testdata/modeblocks/gen-oracle.ts" {
		t.Fatalf("oracle 生成器不符：got=%q", o.Meta.GeneratedBy)
	}
	if len(o.PlanMode) == 0 || o.AskMode.Output == "" || o.PlanExit.Output == "" {
		t.Fatalf("oracle 不完整：planMode=%d askMode=%d planExit=%d",
			len(o.PlanMode), len(o.AskMode.Output), len(o.PlanExit.Output))
	}
	return &o
}

// TestRenderPlanModeBlockParity —— 逐字节对账 TS `renderPlanModeBlock`。
//
// 该块实现体 4229 字符（73 行），手抄必然出错——oracle 是唯一可行来源。
//
// **★ 入参语义**（oracle 实测，非推断）：
// `activePlanFilePath` 用 **truthy 判断**。`undefined` / `null` / `”`
// 三者输出**完全相同**（都不插入「活动计划文件: `path`」行）；只有非空
// 路径才插入该行。
//
// 这与 TS 源码的 `activePlanFilePath ? \`\n活动计划文件: ...\` : ”` 一致。
func TestRenderPlanModeBlockParity(t *testing.T) {
	o := loadModeBlocksOracle(t)

	for _, c := range o.PlanMode {
		t.Run(c.Name, func(t *testing.T) {
			// Go 侧签名用 *string 表达 TS 的 `string | null | undefined`：
			//   - undefined / null → nil（TS 侧两者行为相同）
			//   - empty-string → 指向 "" 的指针（**必须保留**，用于验证
			//     truthy 语义：空串走「无路径」分支）
			//   - 有值 → 指向该值的指针
			var path *string
			if !c.IsUndefined && c.Value != nil {
				v := *c.Value
				path = &v
			}

			got := RenderPlanModeBlock(path)
			if got != c.Output {
				t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
					len(got), truncateForMsg(got), len(c.Output), truncateForMsg(c.Output))
			}
		})
	}
}

// TestRenderPlanModeBlockTruthySemantics —— ★ 单独钉住 truthy 判断。
//
// 三个"无路径"输入（nil / 指向空串的指针）必须产出**同一**结果，
// 且**不含**「活动计划文件: 」那行。
func TestRenderPlanModeBlockTruthySemantics(t *testing.T) {
	empty := ""
	nilCase := RenderPlanModeBlock(nil)
	emptyCase := RenderPlanModeBlock(&empty)

	if nilCase != emptyCase {
		t.Errorf("★ nil 与空串应产出相同结果（truthy 语义）：\n nil(%d)=%q\nempty(%d)=%q",
			len(nilCase), truncateForMsg(nilCase), len(emptyCase), truncateForMsg(emptyCase))
	}

	// 都不得含「活动计划文件: `」这行（那是非空路径才有的）
	marker := "活动计划文件: `"
	if strings.Contains(nilCase, marker) {
		t.Error("★ nil 输入不该含「活动计划文件: `」行")
	}
	if strings.Contains(emptyCase, marker) {
		t.Error("★ 空串输入不该含「活动计划文件: `」行（truthy 判断）")
	}

	// 非空路径必须含该行
	path := ".rivet/plans/x.md"
	withPath := RenderPlanModeBlock(&path)
	if !strings.Contains(withPath, marker) {
		t.Error("非空路径必须插入「活动计划文件: `」行")
	}
	if !strings.Contains(withPath, path) {
		t.Errorf("插入行应含路径本身：%q", path)
	}
}

// TestRenderAskModeBlockParity —— 逐字节对账（288 字符）。
func TestRenderAskModeBlockParity(t *testing.T) {
	o := loadModeBlocksOracle(t)
	got := RenderAskModeBlock()
	if got != o.AskMode.Output {
		t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
			len(got), truncateForMsg(got), len(o.AskMode.Output), truncateForMsg(o.AskMode.Output))
	}
}

// TestRenderPlanExitReminderParity —— 逐字节对账（109 字符）。
func TestRenderPlanExitReminderParity(t *testing.T) {
	o := loadModeBlocksOracle(t)
	got := RenderPlanExitReminder()
	if got != o.PlanExit.Output {
		t.Errorf("输出不符：\n got(%d)=%q\nwant(%d)=%q",
			len(got), truncateForMsg(got), len(o.PlanExit.Output), truncateForMsg(o.PlanExit.Output))
	}
}
