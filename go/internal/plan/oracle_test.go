package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// oracle_test.go —— 差分对账：Go 实现 vs TS 原实现（真跑产出的黄金数据）。
//
// oracle 由 `testdata/plan/gen-oracle.ts` 生成（tsx 真跑 TS 源码）。
// **不是手抄**——本仓库有过手抄导致假绿的教训。

type oracleFile struct {
	Slugify        []oracleInOut      `json:"slugify"`
	StripStatus    []oracleInOut      `json:"stripStatus"`
	InsertStatus   []oracleInsert     `json:"insertStatus"`
	InsertApproved []oracleInsert     `json:"insertStatusApproved"`
	InsertModel    []oracleInOut      `json:"insertModel"`
	InsertModelNoT []oracleInOut      `json:"insertModelNoTier"`
	ParseModel     []oracleParseModel `json:"parseModel"`
	IsDraft        []oracleBool       `json:"isDraft"`
	ParseSelection []oracleSelection  `json:"parseSelection"`
	Close          []oracleClose      `json:"close"`
	Tier           []oracleTier       `json:"tier"`
}

type oracleInOut struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type oracleInsert struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type oracleParseModel struct {
	Input  string               `json:"input"`
	Output *PlanModelProvenance `json:"output"`
}

type oracleBool struct {
	Input  string `json:"input"`
	Output bool   `json:"output"`
}

type oracleSelection struct {
	Input  string `json:"input"`
	Output []int  `json:"output"`
	Error  string `json:"error"`
}

type oracleClose struct {
	Name    string          `json:"name"`
	Input   string          `json:"input"`
	Options json.RawMessage `json:"options"`
	Result  *struct {
		Content                string            `json:"content"`
		TotalChangedCheckboxes int               `json:"totalChangedCheckboxes"`
		AlreadyClosed          bool              `json:"alreadyClosed"`
		ClosureInserted        bool              `json:"closureInserted"`
		ClosureUpdated         bool              `json:"closureUpdated"`
		Changes                []PlanCloseChange `json:"changes"`
	} `json:"result"`
	Error string `json:"error"`
}

type oracleTier struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

// oracleRelPath 是 oracle 相对**包目录**的路径。
//
// go test 的工作目录是包目录（`internal/plan/`），而 testdata 在模块根
// （`go/testdata/`）——故需上溯两级。与 `testdata/` 约定位置一致（Go 工具链
// 忽略名为 testdata 的目录，不会把它当包）。
const oracleRelPath = "../../testdata/plan/oracle.json"

func loadOracle(t *testing.T) *oracleFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(oracleRelPath))
	if err != nil {
		t.Fatalf("读取 oracle 失败（需先跑 gen-oracle.ts）：%v", err)
	}
	var o oracleFile
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return &o
}

// TestOracleSlugify —— Slugify 逐例对账（含 UTF-16 截断与 emoji）。
func TestOracleSlugify(t *testing.T) {
	o := loadOracle(t)
	if len(o.Slugify) == 0 {
		t.Fatal("oracle 无 slugify 用例")
	}
	for _, c := range o.Slugify {
		got := Slugify(c.Input)
		if got != c.Output {
			t.Errorf("Slugify(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	t.Logf("slugify 对账 %d 例", len(o.Slugify))
}

// TestOracleStripStatus —— StripPlanStatusMarkers 逐例对账。
func TestOracleStripStatus(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.StripStatus {
		got := StripPlanStatusMarkers(c.Input)
		if got != c.Output {
			t.Errorf("StripPlanStatusMarkers(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	t.Logf("stripStatus 对账 %d 例", len(o.StripStatus))
}

// TestOracleInsertStatus —— InsertPlanStatusMarker 逐例对账。
//
// **时间戳由 oracle 固定**——TS 侧用 `new Date().toISOString()`，Go 侧由
// 调用方传入。此处比对时把 Go 侧的固定戳对齐 oracle 生成时的戳。
func TestOracleInsertStatus(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.InsertStatus {
		got := InsertPlanStatusMarker(c.Input, StatusExecuted, oracleTimestampFrom(c.Output))
		if got != c.Output {
			t.Errorf("InsertPlanStatusMarker(EXECUTED)(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	for _, c := range o.InsertApproved {
		got := InsertPlanStatusMarker(c.Input, StatusApproved, oracleTimestampFrom(c.Output))
		if got != c.Output {
			t.Errorf("InsertPlanStatusMarker(APPROVED)(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	t.Logf("insertStatus 对账 %d+%d 例", len(o.InsertStatus), len(o.InsertApproved))
}

// oracleTimestampFrom 从 oracle 输出里抽出时间戳（`> **Status: X** — <ts>`）。
//
// 避免在 Go 侧硬编码 oracle 生成时刻——那是运行时值，不是常量。
func oracleTimestampFrom(output string) string {
	const marker = "** — "
	idx := indexOf(output, marker)
	if idx < 0 {
		return ""
	}
	rest := output[idx+len(marker):]
	end := indexOf(rest, "\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestOracleInsertModel —— InsertPlanModelMarker 逐例对账（含幂等剥旧标记）。
func TestOracleInsertModel(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.InsertModel {
		got := InsertPlanModelMarker(c.Input, "deepseek-v4.1-flash", "cheap")
		if got != c.Output {
			t.Errorf("InsertPlanModelMarker(cheap)(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	for _, c := range o.InsertModelNoT {
		got := InsertPlanModelMarker(c.Input, "unknown-model", "")
		if got != c.Output {
			t.Errorf("InsertPlanModelMarker(no tier)(%q)\n  TS: %q\n  Go: %q", c.Input, c.Output, got)
		}
	}
	t.Logf("insertModel 对账 %d+%d 例", len(o.InsertModel), len(o.InsertModelNoT))
}

// TestOracleParseModel —— ParsePlanModel 逐例对账。
func TestOracleParseModel(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.ParseModel {
		got := ParsePlanModel(c.Input)
		switch {
		case c.Output == nil && got != nil:
			t.Errorf("ParsePlanModel(%q) TS=nil Go=%+v", c.Input, got)
		case c.Output != nil && got == nil:
			t.Errorf("ParsePlanModel(%q) TS=%+v Go=nil", c.Input, c.Output)
		case c.Output != nil && got != nil && *got != *c.Output:
			t.Errorf("ParsePlanModel(%q) TS=%+v Go=%+v", c.Input, *c.Output, *got)
		}
	}
	t.Logf("parseModel 对账 %d 例", len(o.ParseModel))
}

// TestOracleIsDraft —— IsDraftSlug 逐例对账。
func TestOracleIsDraft(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.IsDraft {
		if got := IsDraftSlug(c.Input); got != c.Output {
			t.Errorf("IsDraftSlug(%q) TS=%v Go=%v", c.Input, c.Output, got)
		}
	}
	t.Logf("isDraft 对账 %d 例", len(o.IsDraft))
}

// TestOracleParseSelection —— ParseTaskSelection 逐例对账（含错误路径）。
func TestOracleParseSelection(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.ParseSelection {
		got, err := ParseTaskSelection(c.Input)
		if c.Error != "" {
			if err == nil {
				t.Errorf("ParseTaskSelection(%q) 应报错，实得 %v", c.Input, got)
			} else if err.Error() != c.Error {
				t.Errorf("ParseTaskSelection(%q) 错误消息不符\n  TS: %q\n  Go: %q", c.Input, c.Error, err.Error())
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTaskSelection(%q) 不应报错：%v", c.Input, err)
			continue
		}
		if len(got) != len(c.Output) {
			t.Errorf("ParseTaskSelection(%q) TS=%v Go=%v", c.Input, c.Output, got)
			continue
		}
		for i := range got {
			if got[i] != c.Output[i] {
				t.Errorf("ParseTaskSelection(%q) TS=%v Go=%v", c.Input, c.Output, got)
				break
			}
		}
	}
	t.Logf("parseSelection 对账 %d 例", len(o.ParseSelection))
}

// TestOracleClose —— ClosePlanMarkdown 逐例对账（**逐字节比对 content**）。
func TestOracleClose(t *testing.T) {
	o := loadOracle(t)
	if len(o.Close) == 0 {
		t.Fatal("oracle 无 close 用例")
	}
	for _, c := range o.Close {
		opts := decodeCloseOptions(t, c.Options)
		got, err := ClosePlanMarkdown(c.Input, opts)

		if c.Error != "" {
			if err == nil {
				t.Errorf("[%s] 应报错，实得结果", c.Name)
			} else if err.Error() != c.Error {
				t.Errorf("[%s] 错误消息不符\n  TS: %q\n  Go: %q", c.Name, c.Error, err.Error())
			}
			continue
		}
		if err != nil {
			t.Errorf("[%s] 不应报错：%v", c.Name, err)
			continue
		}
		if c.Result == nil {
			t.Errorf("[%s] oracle 既无 result 也无 error", c.Name)
			continue
		}

		if got.Content != c.Result.Content {
			t.Errorf("[%s] content 不一致\n--- TS ---\n%s\n--- Go ---\n%s", c.Name, c.Result.Content, got.Content)
		}
		if got.TotalChangedCheckboxes != c.Result.TotalChangedCheckboxes {
			t.Errorf("[%s] totalChanged TS=%d Go=%d", c.Name, c.Result.TotalChangedCheckboxes, got.TotalChangedCheckboxes)
		}
		if got.AlreadyClosed != c.Result.AlreadyClosed {
			t.Errorf("[%s] alreadyClosed TS=%v Go=%v", c.Name, c.Result.AlreadyClosed, got.AlreadyClosed)
		}
		if got.ClosureInserted != c.Result.ClosureInserted {
			t.Errorf("[%s] closureInserted TS=%v Go=%v", c.Name, c.Result.ClosureInserted, got.ClosureInserted)
		}
		if got.ClosureUpdated != c.Result.ClosureUpdated {
			t.Errorf("[%s] closureUpdated TS=%v Go=%v", c.Name, c.Result.ClosureUpdated, got.ClosureUpdated)
		}
		if len(got.Changes) != len(c.Result.Changes) {
			t.Errorf("[%s] changes 长度 TS=%d Go=%d", c.Name, len(c.Result.Changes), len(got.Changes))
		} else {
			for i := range got.Changes {
				if got.Changes[i] != c.Result.Changes[i] {
					t.Errorf("[%s] changes[%d] TS=%+v Go=%+v", c.Name, i, c.Result.Changes[i], got.Changes[i])
				}
			}
		}
	}
	t.Logf("close 对账 %d 例", len(o.Close))
}

// decodeCloseOptions 把 oracle 的 options JSON 解成 PlanCloseOptions。
func decodeCloseOptions(t *testing.T, raw json.RawMessage) PlanCloseOptions {
	t.Helper()
	var wire struct {
		Tasks            string   `json:"tasks"`
		VerifiedCommands []string `json:"verifiedCommands"`
		DeliveryState    string   `json:"deliveryState"`
		Note             string   `json:"note"`
		UpdateClosure    *bool    `json:"updateClosure"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("解析 close options 失败：%v", err)
	}
	return PlanCloseOptions{
		Tasks:            wire.Tasks,
		VerifiedCommands: wire.VerifiedCommands,
		DeliveryState:    wire.DeliveryState,
		Note:             wire.Note,
		UpdateClosure:    wire.UpdateClosure,
	}
}

// TestOracleTier —— InferModelTierFromName 逐例对账。
func TestOracleTier(t *testing.T) {
	o := loadOracle(t)
	for _, c := range o.Tier {
		got := string(InferModelTierFromName(c.Input))
		if got != c.Output {
			t.Errorf("InferModelTierFromName(%q) TS=%q Go=%q", c.Input, c.Output, got)
		}
	}
	t.Logf("tier 对账 %d 例", len(o.Tier))
}
