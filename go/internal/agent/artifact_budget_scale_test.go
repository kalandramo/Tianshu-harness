package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// artifact_budget_scale_test.go —— L1 artifact 阈值的**预算感知缩放**。
//
// 对账 TS `tool-pipeline.ts:575-581`：
//
//	if (remainingBudgetFraction != null) {
//	  if (f > 0.5)      threshold = max(threshold, threshold * 3)
//	  else if (f > 0.3) threshold = max(threshold, threshold * 1.5)
//	}
//
// **为什么需要这组测试**：`BudgetFraction()` 此前**零消费者**——TS 把
// `budgetFraction` 传给 `artifactIntercept` 做阈值缩放，Go 侧漏了这一步
// （第二十三刀 `DecideReadPolicy` 的同型缺陷：实现了但未接线）。

// TestBudgetScaleAmpleInlinesMore —— 余量充裕（>0.5）→ 阈值 ×3。
//
// 构造内容长度**介于 base 与 base×3 之间**——基础阈值会包，缩放后不包。
// 这是使「缩放生效」与「未生效」可区分的**关键输入构造**。
func TestBudgetScaleAmpleInlinesMore(t *testing.T) {
	base := defaultArtifactInterceptThreshold // 2500
	// 长度 4000：> 2500（基础会包），< 7500（×3 后不包）。
	content := strings.Repeat("x", 4000)

	// 无预算信息 → 基础阈值 → 包。
	if !shouldInterceptForArtifact("some_tool", content, false, 0, nil) {
		t.Errorf("无预算信息时 %d 字符应被拦截（> base %d）", len(content), base)
	}

	// 预算充裕（fraction = 1.0，未消费）→ ×3 → 不包。
	ample := CreateTurnBudget(0) // 50k 基线
	if shouldInterceptForArtifact("some_tool", content, false, 0, ample) {
		// fraction = 1 - 0/50000 = 1.0 > 0.5 → threshold = 7500 > 4000 → 不包
		t.Errorf("预算充裕时 %d 字符不应被拦截（×3 后阈值 %d）", len(content), base*3)
	}
}

// TestBudgetScaleModerate —— 中等余量（0.3 < f <= 0.5）→ ×1.5。
//
// 构造 fraction 恰在 (0.3, 0.5]：max=50000，用 30000 → f = 0.4。
// 阈值 2500×1.5 = 3750；内容 3000 字符 → 基础包（3000>2500），缩放后不包。
func TestBudgetScaleModerate(t *testing.T) {
	base := defaultArtifactInterceptThreshold
	content := strings.Repeat("x", 3000) // 2500 < 3000 < 3750

	mod := CreateTurnBudget(0)
	mod.Consume(30000) // f = 1 - 30000/50000 = 0.4

	if got := mod.BudgetFraction(); got <= 0.3 || got > 0.5 {
		t.Fatalf("fraction 应落在 (0.3, 0.5]，实得 %v", got)
	}
	if shouldInterceptForArtifact("some_tool", content, false, 0, mod) {
		t.Errorf("中等余量时 %d 字符不应被拦截（×1.5 后阈值 %d）", len(content), base*3/2)
	}
}

// TestBudgetScaleTightUsesBase —— 余量紧张（<= 0.3）→ 基础阈值。
//
// 反证：确保实现不是「无条件缩放」。
// max=50000，用 40000 → f = 0.2 <= 0.3 → 不缩放。
// 内容 3000 > 2500 → **应包**。
func TestBudgetScaleTightUsesBase(t *testing.T) {
	content := strings.Repeat("x", 3000)

	tight := CreateTurnBudget(0)
	tight.Consume(40000) // f = 0.2

	if got := tight.BudgetFraction(); got > 0.3 {
		t.Fatalf("fraction 应 <= 0.3，实得 %v", got)
	}
	if !shouldInterceptForArtifact("some_tool", content, false, 0, tight) {
		t.Errorf("余量紧张时 %d 字符应被拦截（基础阈值 %d）", len(content), defaultArtifactInterceptThreshold)
	}
}

// TestBudgetScaleMaxZeroFractionIsOne —— `max == 0` 时 fraction 为 1。
//
// 对账 TS：`maxTokensPerTurn > 0 ? ... : 1`——危急档 max=0，
// fraction 取 1（而非除零）。这意味着**危急档下 artifact 阈值反而 ×3**。
// 看似反直觉，但这是 TS 的算式直译：`turnBudget` 耗尽靠的是
// `consume` + `isExhausted`（`<stored>` 包装），与阈值缩放是**两条独立路径**。
func TestBudgetScaleMaxZeroFractionIsOne(t *testing.T) {
	critical := CreateTurnBudget(0.9) // max = 0
	if got := critical.BudgetFraction(); got != 1 {
		t.Errorf("max=0 时 fraction 应为 1，实得 %v", got)
	}
}

// TestInterceptUsesTurnBudget —— **接线验证**：`interceptResultForArtifact`
// 把 `l.turnBudget` 传下去。
//
// 直接调 `shouldInterceptForArtifact` 只能证明函数本身对；这条证明
// **Loop 真的接上了**。构造：内容长度落在「基础包 / ×3 不包」区间，
// 对比有无 budget 时 `interceptResultForArtifact` 的产出。
func TestInterceptUsesTurnBudget(t *testing.T) {
	content := strings.Repeat("x", 4000) // 2500 < 4000 < 7500

	// 无 budget → 包成 artifact 引用。
	l1 := newArtifactLoop(t, 0)
	got1 := l1.interceptResultForArtifact(toolCall{name: "some_tool"}, contract.Result{Content: content})
	if !strings.HasPrefix(got1.Content, "[artifact:") {
		t.Fatalf("无 budget 时应包成 artifact，实得：%.80s", got1.Content)
	}

	// 有充裕 budget → 阈值 ×3 → 不包。
	l2 := newArtifactLoop(t, 0)
	l2.turnBudget = CreateTurnBudget(0)
	got2 := l2.interceptResultForArtifact(toolCall{name: "some_tool"}, contract.Result{Content: content})
	if strings.HasPrefix(got2.Content, "[artifact:") {
		t.Errorf("预算充裕时不应包（阈值 ×3），实得：%.80s", got2.Content)
	}
}
