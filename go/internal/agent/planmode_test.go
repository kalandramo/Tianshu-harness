package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oracle 结构 —— 对账 go/testdata/planmode/oracle.json（真实 TS 实现生成）。
type planModeOracle struct {
	Meta struct {
		Source      string `json:"source"`
		GeneratedBy string `json:"generatedBy"`
	} `json:"meta"`
	Check []struct {
		Name                         string  `json:"name"`
		State                        string  `json:"state"`
		ToolName                     string  `json:"toolName"`
		Cwd                          *string `json:"cwd"`
		TargetFilePath               *string `json:"targetFilePath"`
		ActivePlanFilePath           *string `json:"activePlanFilePath"`
		DelegatesWriteCapableProfile *bool   `json:"delegatesWriteCapableProfile"`
		Allowed                      bool    `json:"allowed"`
		ReasonMentionsPlanMode       bool    `json:"reasonMentionsPlanMode"`
		HasReason                    bool    `json:"hasReason"`
	} `json:"check"`
	Canonicalize []struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"canonicalize"`
	AllowedTools []string `json:"allowedTools"`
	DraftReceipt struct {
		Hit       string `json:"hit"`
		MissNamed string `json:"missNamed"`
		MissNull  string `json:"missNull"`
	} `json:"draftReceipt"`
}

func loadPlanModeOracle(t *testing.T) *planModeOracle {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "planmode", "oracle.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 oracle 失败（%s）：%v\n先跑：npx tsx go/testdata/planmode/gen-oracle.ts > go/testdata/planmode/oracle.json", p, err)
	}
	var o planModeOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if o.Meta.Source != "src/agent/plan-mode.ts" {
		t.Fatalf("oracle 源不符：got=%q（用错 oracle 文件？）", o.Meta.Source)
	}
	if o.Meta.GeneratedBy != "go/testdata/planmode/gen-oracle.ts" {
		t.Fatalf("oracle 生成器不符：got=%q", o.Meta.GeneratedBy)
	}
	if len(o.Check) == 0 || len(o.Canonicalize) == 0 {
		t.Fatalf("oracle 不完整：check=%d canonicalize=%d", len(o.Check), len(o.Canonicalize))
	}
	return &o
}

// TestCheckPlanModeParity —— 逐值对账 TS `checkPlanMode`。
//
// 判定链（对账 plan-mode.ts:164-213）：
//
//	if (state === 'off') return { allowed: true }                    // ★ 早返回
//	if ((tool === 'write_file' || tool === 'edit_file') && cwd && target) {
//	  if (activePlanFilePath && pathsMatch(...)) return { allowed: true }
//	  if (isUnderScratchDir(cwd, target)) return { allowed: true }
//	}
//	if (DELEGATE_TOOLS.has(tool) && delegatesWriteCapableProfile) return { allowed: false }
//	if (PLAN_MODE_ALLOWED_TOOLS.has(tool)) return { allowed: true }
//	return { allowed: false, reason: ... }
//
// **★ 三条易错语义**（oracle 用例逐个钉住）：
//
//  1. **scratch 目录本身不算其子目录**：`isUnderScratchDir` 用
//     `startsWith(scratch + '/')` —— `.rivet/scratch`（无尾随路径）不匹配
//  2. **路径穿越被 resolve 后拒绝**：`.rivet/scratch/../../../etc/passwd`
//     resolve 后逃出 scratch → 拦
//  3. **off 状态忽略 delegatesWriteCapableProfile**：早返回优先
func TestCheckPlanModeParity(t *testing.T) {
	o := loadPlanModeOracle(t)

	for _, c := range o.Check {
		t.Run(c.Name, func(t *testing.T) {
			ctx := PlanModeCheckContext{}
			if c.Cwd != nil {
				ctx.Cwd = *c.Cwd
			}
			if c.TargetFilePath != nil {
				ctx.TargetFilePath = *c.TargetFilePath
			}
			if c.ActivePlanFilePath != nil {
				v := *c.ActivePlanFilePath
				ctx.ActivePlanFilePath = &v
			}
			if c.DelegatesWriteCapableProfile != nil {
				ctx.DelegatesWriteCapableProfile = *c.DelegatesWriteCapableProfile
			}

			got := CheckPlanMode(PlanModeState(c.State), c.ToolName, ctx)

			if got.Allowed != c.Allowed {
				t.Errorf("allowed：got=%v want=%v（reason=%q）", got.Allowed, c.Allowed, got.Reason)
			}
			if c.HasReason != (got.Reason != "") {
				t.Errorf("hasReason：got=%v want=%v", got.Reason != "", c.HasReason)
			}
			if c.ReasonMentionsPlanMode && !strings.Contains(got.Reason, "Plan Mode") {
				t.Errorf("reason 应含 \"Plan Mode\"：got=%q", truncateRunes(got.Reason, 80))
			}
		})
	}
}

// TestCanonicalizePathForCompareParity —— 逐值对账。
//
// 对账 plan-mode.ts:126-129：
//
//	const s = p.replace(/\\/g, '/')
//	return /^[a-zA-Z]:\//.test(s) ? s.toLowerCase() : s
//
// **★ 关键语义**：仅**盘符形**路径整体小写（NTFS 大小写不敏感）；POSIX 路径
// **保持大小写敏感**（ext4/APFS 默认区分）。
func TestCanonicalizePathForCompareParity(t *testing.T) {
	o := loadPlanModeOracle(t)

	for _, c := range o.Canonicalize {
		t.Run(c.Input, func(t *testing.T) {
			if got := CanonicalizePathForCompare(c.Input); got != c.Output {
				t.Errorf("输入 %q：got=%q want=%q", c.Input, got, c.Output)
			}
		})
	}
}

// TestCanonicalizePathForCompareSemantics —— 单独钉住两条关键语义。
func TestCanonicalizePathForCompareSemantics(t *testing.T) {
	t.Run("★盘符路径大小写折叠", func(t *testing.T) {
		a := CanonicalizePathForCompare(`C:\Proj\.rivet\plans\draft-1.md`)
		b := CanonicalizePathForCompare(`c:/proj/.rivet/plans/draft-1.md`)
		if a != b {
			t.Errorf("★ 盘符路径应折叠大小写：%q != %q", a, b)
		}
	})

	t.Run("★POSIX路径保持大小写敏感", func(t *testing.T) {
		a := CanonicalizePathForCompare("/tmp/Plans/draft-1.md")
		b := CanonicalizePathForCompare("/tmp/plans/draft-1.md")
		if a == b {
			t.Error("★ POSIX 路径不该折叠大小写（ext4/APFS 默认区分）")
		}
	})

	t.Run("反斜杠归一为斜杠", func(t *testing.T) {
		if got := CanonicalizePathForCompare(`a\b\c`); got != "a/b/c" {
			t.Errorf("反斜杠应归一：got=%q", got)
		}
	})
}

// TestPlanModeAllowedToolsParity —— 白名单集合内容对账。
func TestPlanModeAllowedToolsParity(t *testing.T) {
	o := loadPlanModeOracle(t)

	if len(PlanModeAllowedTools) != len(o.AllowedTools) {
		t.Fatalf("白名单大小：got=%d want=%d", len(PlanModeAllowedTools), len(o.AllowedTools))
	}
	for _, name := range o.AllowedTools {
		if _, ok := PlanModeAllowedTools[name]; !ok {
			t.Errorf("白名单缺少 %q", name)
		}
	}
	// 反向：Go 侧不该有 TS 侧没有的
	for name := range PlanModeAllowedTools {
		found := false
		for _, n := range o.AllowedTools {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Go 白名单多出 %q（TS 侧没有）", name)
		}
	}
}

// TestFormatActivePlanDraftReceiptParity —— 逐字节对账。
func TestFormatActivePlanDraftReceiptParity(t *testing.T) {
	o := loadPlanModeOracle(t)
	const cwd = "/tmp/rivet-planmode-probe"

	draft := ".rivet/plans/draft-1.md"
	named := ".rivet/plans/my-plan.md"

	t.Run("命中草稿", func(t *testing.T) {
		if got := FormatActivePlanDraftReceipt(cwd, draft, &draft, 42); got != o.DraftReceipt.Hit {
			t.Errorf("got=%q\nwant=%q", got, o.DraftReceipt.Hit)
		}
	})
	t.Run("命名计划非草稿", func(t *testing.T) {
		if got := FormatActivePlanDraftReceipt(cwd, named, &named, 42); got != o.DraftReceipt.MissNamed {
			t.Errorf("got=%q want=%q", got, o.DraftReceipt.MissNamed)
		}
	})
	t.Run("无activePlanFilePath", func(t *testing.T) {
		if got := FormatActivePlanDraftReceipt(cwd, draft, nil, 42); got != o.DraftReceipt.MissNull {
			t.Errorf("got=%q want=%q", got, o.DraftReceipt.MissNull)
		}
	})
}

// TestCreateActivePlanDraftPathShape —— 形态断言（含 Date.now，非逐值）。
func TestCreateActivePlanDraftPathShape(t *testing.T) {
	got := CreateActivePlanDraftPath()
	if !strings.HasPrefix(got, ".rivet/plans/draft-") {
		t.Errorf("应以 .rivet/plans/draft- 开头：got=%q", got)
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("应以 .md 结尾：got=%q", got)
	}
	// 两次调用应产出不同路径（时间戳）
	if CreateActivePlanDraftPath() == got {
		t.Log("两次调用同路径（同毫秒）——非失败，仅记录")
	}
}
