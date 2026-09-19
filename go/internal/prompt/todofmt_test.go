package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// todofmtOracle 是 TS 侧真实 TodoStore.formatList / formatSummary 的产出。
// 生成命令：npx tsx go/testdata/todofmt/gen-oracle.ts
type todofmtOracle struct {
	EmptyResult string `json:"emptyResult"`
	Cases       map[string]struct {
		Note  string `json:"note"`
		Todos []struct {
			ID         string `json:"id"`
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
		FormatList    string `json:"formatList"`
		FormatSummary string `json:"formatSummary"`
	} `json:"cases"`
}

func loadTodofmtOracle(t *testing.T) todofmtOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "todofmt", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/todofmt/gen-oracle.ts", path, err)
	}
	var o todofmtOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// toItems 把 oracle 的 todos 转成 Go 结构。
func toItems(c []struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}) []TodoItem {
	out := make([]TodoItem, len(c))
	for i, t := range c {
		out[i] = TodoItem{ID: t.ID, Content: t.Content, Status: t.Status, ActiveForm: t.ActiveForm}
	}
	return out
}

// TestEmptyResultConstant —— 空清单文案逐字一致。
func TestEmptyResultConstant(t *testing.T) {
	o := loadTodofmtOracle(t)
	if TodoEmptyResult != o.EmptyResult {
		t.Errorf("空清单文案不符\n  Go =%q\n  TS =%q", TodoEmptyResult, o.EmptyResult)
	}
}

// TestFormatTodoListParity —— 清单渲染与 TS 逐字节相同。
func TestFormatTodoListParity(t *testing.T) {
	o := loadTodofmtOracle(t)
	if len(o.Cases) == 0 {
		t.Fatal("oracle 无用例")
	}
	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			got := FormatTodoList(toItems(c.Todos))
			if got != c.FormatList {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, c.FormatList)
			}
		})
	}
}

// TestFormatTodoSummaryParity —— 摘要渲染与 TS 逐字节相同。
func TestFormatTodoSummaryParity(t *testing.T) {
	o := loadTodofmtOracle(t)
	for name, c := range o.Cases {
		t.Run(name, func(t *testing.T) {
			got := FormatTodoSummary(toItems(c.Todos))
			if got != c.FormatSummary {
				t.Errorf("不等价\n  Go =%q\n  TS =%q", got, c.FormatSummary)
			}
		})
	}
}

// TestTodoIconThreeStates —— 三个状态 icon。
func TestTodoIconThreeStates(t *testing.T) {
	cases := map[string]string{"completed": "✓", "in_progress": "►", "pending": "○"}
	for status, want := range cases {
		got := FormatTodoList([]TodoItem{{ID: "1", Content: "x", Status: status}})
		if !contains(got, want+" [1]") {
			t.Errorf("状态 %s 应渲染 icon %q，实际 %q", status, want, got)
		}
	}
	// 未知状态兜底为 ○
	got := FormatTodoList([]TodoItem{{ID: "1", Content: "x", Status: "unknown_status"}})
	if !contains(got, "○ [1]") {
		t.Errorf("未知状态应兜底为 ○，实际 %q", got)
	}
}

// TestFormatTodoListEmpty —— 空清单返回固定文案（formatList 特判）。
func TestFormatTodoListEmpty(t *testing.T) {
	if got := FormatTodoList(nil); got != TodoEmptyResult {
		t.Errorf("空清单应返回固定文案，实际 %q", got)
	}
}

// TestFormatTodoSummaryEmpty —— 空清单**不特判**（与 formatList 不同）。
//
// formatSummary 会渲染出「已更新：0/0 已完成」加一个空行——这是 TS 的真实
// 行为（oracle 锁定）。
func TestFormatTodoSummaryEmpty(t *testing.T) {
	got := FormatTodoSummary(nil)
	if got != "已更新：0/0 已完成\n" {
		t.Errorf("空清单摘要应为「已更新：0/0 已完成」加换行，实际 %q", got)
	}
}

// TestFormatDifferenceStatusSuffix —— formatList 带 status 后缀、formatSummary 不带。
func TestFormatDifferenceStatusSuffix(t *testing.T) {
	todos := []TodoItem{{ID: "1", Content: "x", Status: "pending"}}
	list := FormatTodoList(todos)
	sum := FormatTodoSummary(todos)
	if !contains(list, "(pending)") {
		t.Errorf("formatList 应带 status 后缀，实际 %q", list)
	}
	if contains(sum, "(pending)") {
		t.Errorf("formatSummary 不应带 status 后缀，实际 %q", sum)
	}
}

// TestActiveFormNotRendered —— activeForm 不参与渲染（真实行为）。
func TestActiveFormNotRendered(t *testing.T) {
	item := TodoItem{ID: "1", Content: "修复认证 bug", Status: "in_progress", ActiveForm: "正在修复认证 bug"}
	got := FormatTodoList([]TodoItem{item})
	if contains(got, "正在修复认证 bug") {
		t.Errorf("formatList 不应渲染 activeForm，实际 %q", got)
	}
	if !contains(got, "修复认证 bug") {
		t.Errorf("应渲染 content，实际 %q", got)
	}
}

// TestTodoFormatOrderPreserved —— 输出顺序 = 输入顺序（不排序）。
func TestTodoFormatOrderPreserved(t *testing.T) {
	todos := []TodoItem{
		{ID: "z", Content: "最后", Status: "pending"},
		{ID: "a", Content: "最先", Status: "completed"},
	}
	got := FormatTodoList(todos)
	idxZ := indexOf(got, "[z]")
	idxA := indexOf(got, "[a]")
	if !(idxZ >= 0 && idxA > idxZ) {
		t.Errorf("顺序应保持输入顺序（z 在 a 前），实际 %q", got)
	}
}
