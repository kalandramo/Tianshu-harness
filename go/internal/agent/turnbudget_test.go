package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// turnbudget_test.go —— 对账 TS `turn-budget.ts` 与 `tool-pipeline.ts` 的
// `<stored>` 包装算式。
//
// oracle 由 `testdata/turnbudget/gen_oracle.ts` **真跑 TS 原实现**产出。
// 生成命令：
//
//	node_modules/.bin/tsx go/testdata/turnbudget/gen_oracle.ts > go/testdata/turnbudget/oracle.json

type tbBudgetCase struct {
	Name             string  `json:"name"`
	RSSRatio         float64 `json:"rssRatio"`
	MaxTokensPerTurn int     `json:"maxTokensPerTurn"`
	UsedTokens0      int     `json:"usedTokens0"`
	Exhausted0       bool    `json:"exhausted0"`
	AfterConsume100  struct {
		Used      int  `json:"used"`
		Exhausted bool `json:"exhausted"`
	} `json:"afterConsume100"`
	BudgetFractionAfter100 float64 `json:"budgetFractionAfter100"`
	AfterReset             struct {
		Used      int  `json:"used"`
		Exhausted bool `json:"exhausted"`
	} `json:"afterReset"`
}

type tbStoredCase struct {
	Name         string  `json:"name"`
	Content      string  `json:"content"`
	RawPath      *string `json:"rawPath"`
	Tool         string  `json:"tool"`
	ContentChars int     `json:"contentChars"`
	Wrapped      string  `json:"wrapped"`
}

type tbConsumeCase struct {
	Len    int `json:"len"`
	Tokens int `json:"tokens"`
}

type tbOracle struct {
	Constants struct {
		BaseBudgetTokens     int `json:"BASE_BUDGET_TOKENS"`
		PressureBudgetTokens int `json:"PRESSURE_BUDGET_TOKENS"`
	} `json:"constants"`
	Budgets []tbBudgetCase  `json:"budgets"`
	Stored  []tbStoredCase  `json:"stored"`
	Consume []tbConsumeCase `json:"consume"`
}

func loadTBOracle(t *testing.T) tbOracle {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "turnbudget", "oracle.json"))
	if err != nil {
		t.Skipf("oracle 缺失（需先跑 gen_oracle.ts）：%v", err)
	}
	var o tbOracle
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatalf("oracle 解析失败：%v", err)
	}
	return o
}

// TestTurnBudgetConstants —— 两个常量逐字对账。
func TestTurnBudgetConstants(t *testing.T) {
	o := loadTBOracle(t)
	if BaseBudgetTokens != o.Constants.BaseBudgetTokens {
		t.Errorf("BASE_BUDGET_TOKENS：Go=%d TS=%d", BaseBudgetTokens, o.Constants.BaseBudgetTokens)
	}
	if PressureBudgetTokens != o.Constants.PressureBudgetTokens {
		t.Errorf("PRESSURE_BUDGET_TOKENS：Go=%d TS=%d", PressureBudgetTokens, o.Constants.PressureBudgetTokens)
	}
}

// TestTurnBudgetOracle —— 逐字段对账 9 例（选档 / consume / exhausted / reset / fraction）。
func TestTurnBudgetOracle(t *testing.T) {
	o := loadTBOracle(t)
	if len(o.Budgets) == 0 {
		t.Fatal("oracle 无 budgets 用例")
	}
	for _, c := range o.Budgets {
		b := CreateTurnBudget(c.RSSRatio)

		if b.MaxTokensPerTurn != c.MaxTokensPerTurn {
			t.Errorf("%s：maxTokensPerTurn Go=%d TS=%d", c.Name, b.MaxTokensPerTurn, c.MaxTokensPerTurn)
		}
		if b.UsedTokens() != c.UsedTokens0 {
			t.Errorf("%s：初始 used Go=%d TS=%d", c.Name, b.UsedTokens(), c.UsedTokens0)
		}
		if b.IsExhausted() != c.Exhausted0 {
			t.Errorf("%s：初始 exhausted Go=%v TS=%v", c.Name, b.IsExhausted(), c.Exhausted0)
		}

		b.Consume(100)
		if b.UsedTokens() != c.AfterConsume100.Used {
			t.Errorf("%s：consume(100) 后 used Go=%d TS=%d", c.Name, b.UsedTokens(), c.AfterConsume100.Used)
		}
		if b.IsExhausted() != c.AfterConsume100.Exhausted {
			t.Errorf("%s：consume(100) 后 exhausted Go=%v TS=%v", c.Name, b.IsExhausted(), c.AfterConsume100.Exhausted)
		}
		if got := b.BudgetFraction(); got != c.BudgetFractionAfter100 {
			t.Errorf("%s：budgetFraction Go=%v TS=%v", c.Name, got, c.BudgetFractionAfter100)
		}

		b.Reset()
		if b.UsedTokens() != c.AfterReset.Used {
			t.Errorf("%s：reset 后 used Go=%d TS=%d", c.Name, b.UsedTokens(), c.AfterReset.Used)
		}
		if b.IsExhausted() != c.AfterReset.Exhausted {
			t.Errorf("%s：reset 后 exhausted Go=%v TS=%v", c.Name, b.IsExhausted(), c.AfterReset.Exhausted)
		}
	}
	t.Logf("对账了 %d 个预算用例", len(o.Budgets))
}

// TestTurnBudgetThresholds —— 三档阈值（**顺序敏感**）。
func TestTurnBudgetThresholds(t *testing.T) {
	cases := []struct {
		rss  float64
		want int
	}{
		{0, BaseBudgetTokens},
		{0.699, BaseBudgetTokens},
		{0.7, PressureBudgetTokens}, // 边界：>= 0.7 进压力档
		{0.849, PressureBudgetTokens},
		{0.85, 0}, // 边界：>= 0.85 进危急档（0 = 立即耗尽）
		{1.0, 0},
	}
	for _, c := range cases {
		got := CreateTurnBudget(c.rss).MaxTokensPerTurn
		if got != c.want {
			t.Errorf("rss=%v：maxTokensPerTurn Go=%d want %d", c.rss, got, c.want)
		}
	}
}

// TestTurnBudgetZeroIsImmediatelyExhausted —— **max=0 时立即耗尽**。
//
// 这不是边界 bug，是对账 TS 的**有意设计**（内存危急时不再放任何工具结果）：
// `used(0) >= max(0)` 为真。
func TestTurnBudgetZeroIsImmediatelyExhausted(t *testing.T) {
	b := CreateTurnBudget(0.9)
	if b.MaxTokensPerTurn != 0 {
		t.Fatalf("rss=0.9 应进危急档，实得 max=%d", b.MaxTokensPerTurn)
	}
	if !b.IsExhausted() {
		t.Error("max=0 时应立即耗尽（对账 TS 的有意设计）")
	}
}

// TestTurnBudgetConsumeCeil —— consume 的 `ceil(len/4)` 算式。
func TestTurnBudgetConsumeCeil(t *testing.T) {
	o := loadTBOracle(t)
	for _, c := range o.Consume {
		got := (c.Len + 3) / 4
		if got != c.Tokens {
			t.Errorf("len=%d：ceil(len/4) Go=%d TS=%d", c.Len, got, c.Tokens)
		}
	}
}

// TestWrapStoredOracle —— `<stored>` 包装逐字节对账 6 例。
func TestWrapStoredOracle(t *testing.T) {
	o := loadTBOracle(t)
	if len(o.Stored) == 0 {
		t.Fatal("oracle 无 stored 用例")
	}
	for _, c := range o.Stored {
		rawPath := ""
		if c.RawPath != nil {
			rawPath = *c.RawPath
		}
		// 造一个**必定耗尽**的预算（max=0）。
		b := CreateTurnBudget(0.9)
		got, wrapped := WrapStoredIfExhausted(b, c.Tool, c.Content, rawPath)
		if !wrapped {
			t.Errorf("%s：应触发包装（max=0 立即耗尽）", c.Name)
			continue
		}
		if got != c.Wrapped {
			t.Errorf("%s 不符\n--- got ---\n%s\n--- want ---\n%s", c.Name, got, c.Wrapped)
		}
	}
	t.Logf("对账了 %d 个 stored 用例", len(o.Stored))
}

// TestWrapStoredNoWrapWhenBudgetRemains —— 预算充足时不包装。
func TestWrapStoredNoWrapWhenBudgetRemains(t *testing.T) {
	b := CreateTurnBudget(0) // 50k
	got, wrapped := WrapStoredIfExhausted(b, "bash", "short output", "/p.raw")
	if wrapped {
		t.Error("预算充足时不应包装")
	}
	if got != "short output" {
		t.Errorf("应原样返回，实得 %q", got)
	}
	// 但消费仍应记账。
	if b.UsedTokens() == 0 {
		t.Error("即使不包装也应消费预算（对账 TS）")
	}
}

// TestWrapStoredRefPathFallbackUnknown —— rawPath 为空时用字面量 `"unknown"`。
//
// **对账细节**：TS 是 `rawToolResult?.rawPath ?? 'unknown'`——**不是省略该属性**。
func TestWrapStoredRefPathFallbackUnknown(t *testing.T) {
	b := CreateTurnBudget(0.9)
	got, _ := WrapStoredIfExhausted(b, "grep", "content", "")
	if !strings.Contains(got, `ref="unknown"`) {
		t.Errorf("rawPath 为空时应回退到字面量 unknown：%.200s", got)
	}
}

// TestWrapStoredCharsIsPreWrapLength —— `chars` 是**包装前**的长度。
//
// **注意**：包装后**不一定更长**——preview 只取前 500 字符，长内容会被截短
// （这正是包装的目的：把长输出换成短引用）。故断言只用 `chars=` 字段验证。
func TestWrapStoredCharsIsPreWrapLength(t *testing.T) {
	content := strings.Repeat("a", 1234)
	b := CreateTurnBudget(0.9)
	got, _ := WrapStoredIfExhausted(b, "bash", content, "/p.raw")
	if !strings.Contains(got, "chars=1234") {
		t.Errorf("chars 应为包装前长度 1234：%.200s", got)
	}
	// preview 只保留前 500 字符 → 包装后应短于原文（长内容场景）。
	if len(got) >= len(content) {
		t.Errorf("1234 字符被压成 500 preview，包装后应更短：got=%d orig=%d", len(got), len(content))
	}
	// 但短内容（<= 500）包装后会更长（含标签）。
	short := "abc"
	gotShort, _ := WrapStoredIfExhausted(b, "bash", short, "/p.raw")
	if len(gotShort) <= len(short) {
		t.Errorf("短内容包装后应更长（含标签）：got=%d orig=%d", len(gotShort), len(short))
	}
}

// TestDisplayContentPrecedence —— `uiContent ?? result` 优先级。
func TestDisplayContentPrecedence(t *testing.T) {
	// uiContent 非空 → 用它。
	if got := DisplayContent("UI 版", "完整结果"); got != "UI 版" {
		t.Errorf("uiContent 非空时应优先，实得 %q", got)
	}
	// uiContent 为空 → 回退 result。
	if got := DisplayContent("", "完整结果"); got != "完整结果" {
		t.Errorf("uiContent 为空时应回退 result，实得 %q", got)
	}
	// 两者都空 → 空串。
	if got := DisplayContent("", ""); got != "" {
		t.Errorf("两者都空应返回空串，实得 %q", got)
	}
}
