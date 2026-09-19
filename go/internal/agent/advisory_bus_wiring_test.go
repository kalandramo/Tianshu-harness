package agent

import (
	"strings"
	"testing"
)

// TestAdvisoryBusClosesHookLoop —— **用户级验收**：
//
// 用户动作：hook 经真实管线投递 advisory → bus 渲染。
// 观察到：`<星域-advisory>` 块里出现 typecheck-reminder 的提醒文本。
//
// **这是 advisory 通路的最后一环**——此前 hook 投递的条目无处显示。
func TestAdvisoryBusClosesHookLoop(t *testing.T) {
	bus := NewAdvisoryBus()

	// hook 直接投递到 bus（bus 实现 AdvisorySink）
	h := NewTypecheckReminderHook(bus)
	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		SawTypecheck:      false,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	if err := h.Run(t.Context(), ctx, nil); err != nil {
		t.Fatalf("hook 报错：%v", err)
	}

	// 渲染
	out := bus.Render("", 0)
	if out == "" {
		t.Fatal("hook 投递后应渲染出内容（此前投递无处显示）")
	}
	if !strings.Contains(out, "<星域-advisory>") {
		t.Errorf("应是 <星域-advisory> 块，得到 %q", out)
	}
	if !strings.Contains(out, "typecheck-reminder") {
		t.Errorf("应含 typecheck-reminder 的 key，得到 %q", out)
	}
	if !strings.Contains(out, "你改了 TS 文件") {
		t.Errorf("应含提醒正文，得到 %q", out)
	}
	// priority 应格式化为两位小数
	if !strings.Contains(out, `priority="0.60"`) {
		t.Errorf("priority 应格式化为 0.60，得到 %q", out)
	}
}

// TestAdvisoryBusAsPipelineSink —— 经真实 Pipeline 投递到 bus。
//
// 用户动作：装 Pipeline + bus，跑 postTurn。观察到：bus 渲染出提醒。
func TestAdvisoryBusAsPipelineSink(t *testing.T) {
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(bus))

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	p.RunPostTurn(t.Context(), ctx)

	out := bus.Render("", 0)
	if !strings.Contains(out, "typecheck-reminder") {
		t.Errorf("经 Pipeline 投递后应渲染出提醒，得到 %q", out)
	}
}

// TestAdvisoryBusDedupAcrossHooks —— 多个 hook 投递同 key 时去重。
func TestAdvisoryBusDedupAcrossHooks(t *testing.T) {
	bus := NewAdvisoryBus()

	bus.Submit(AdvisoryEntry{Key: "same", Priority: 0.5, Category: CategoryDiscipline, Content: "低"})
	bus.Submit(AdvisoryEntry{Key: "same", Priority: 0.8, Category: CategoryDiscipline, Content: "高"})

	out := bus.Render("", 0)
	if strings.Contains(out, "低") {
		t.Error("同 key 应只留高优先级的那条")
	}
	if !strings.Contains(out, "高") {
		t.Error("应保留高优先级条目")
	}
}

// TestAdvisoryBusTTLPersistsAcrossTurns —— TTL 条目跨轮存活。
func TestAdvisoryBusTTLPersistsAcrossTurns(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.Submit(AdvisoryEntry{
		Key: "persist", Priority: 0.6, Category: CategoryDiscipline, Content: "X", TTL: 2,
	})

	out1 := bus.Render("", 0)
	if !strings.Contains(out1, "persist") {
		t.Fatal("第一轮应渲染")
	}
	out2 := bus.Render("", 1)
	if !strings.Contains(out2, "persist") {
		t.Error("TTL=2 应在第二轮仍渲染")
	}
	out3 := bus.Render("", 2)
	if strings.Contains(out3, "persist") {
		t.Error("TTL 耗尽后不应再渲染")
	}
}

// TestAdvisoryBusBudgetLimits —— 每轮预算限制。
func TestAdvisoryBusBudgetLimits(t *testing.T) {
	bus := NewAdvisoryBus()
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		bus.Submit(AdvisoryEntry{
			Key: k, Priority: 0.9 - float64(i)*0.1,
			Category: AdvisoryCategory([]string{"discipline", "repair", "todo", "dedup", "immune"}[i]),
			Content:  k,
		})
	}

	out := bus.Render("", 0)
	// 预算 3 条
	n := strings.Count(out, "<entry ")
	if n != maxAdvisoriesPerTurn {
		t.Errorf("应渲染 %d 条，得到 %d：%q", maxAdvisoriesPerTurn, n, out)
	}
}

// TestAdvisoryBusLedgerTracksDrops —— 被预算挤掉的记入 ledger。
func TestAdvisoryBusLedgerTracksDrops(t *testing.T) {
	bus := NewAdvisoryBus()
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		bus.Submit(AdvisoryEntry{
			Key: k, Priority: 0.9 - float64(i)*0.1,
			Category: AdvisoryCategory([]string{"discipline", "repair", "todo", "dedup", "immune"}[i]),
			Content:  k,
		})
	}
	bus.Render("", 0)

	ledger := bus.DrainLedger()
	if ledger.Submitted != 5 {
		t.Errorf("submitted 应为 5，得到 %d", ledger.Submitted)
	}
	if ledger.Rendered != 3 {
		t.Errorf("rendered 应为 3，得到 %d", ledger.Rendered)
	}
	if ledger.Dropped != 2 {
		t.Errorf("dropped 应为 2，得到 %d", ledger.Dropped)
	}
}

// TestAdvisoryBusEscapesXML —— XML 转义（防注入破坏块结构）。
func TestAdvisoryBusEscapesXML(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.Submit(AdvisoryEntry{
		Key: "k&<", Priority: 0.6, Category: CategoryDiscipline,
		Content: `<script>alert("x")</script>`,
	})

	out := bus.Render("", 0)
	if strings.Contains(out, "<script>") {
		t.Errorf("原始 < 不应出现在输出里（应转义）：%q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("应转义为 &lt;script&gt;：%q", out)
	}
	if !strings.Contains(out, "k&amp;&lt;") {
		t.Errorf("key 也应转义：%q", out)
	}
}

// TestToFixed2MatchesJS —— priority 格式化对账 JS toFixed(2)。
//
// **期望值全部由 `node -e "v.toFixed(2)"` 实测得出**（不是直觉推断）。
//
// **关键边界值**：2.675 / 0.615 / 1.255 这三个——首版自写「远离零」实现在
// 它们上全错（给 2.68/0.62/1.26），而 JS 给 2.67/0.61/1.25。这几个用例是
// 那次错误的判别器（原用例集只有 0.555，恰好两者一致，故变异反证红 0）。
func TestToFixed2MatchesJS(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.6, "0.60"},
		{0.1, "0.10"},
		{0.9, "0.90"},
		{0.05, "0.05"},
		{0.99, "0.99"},
		{0.0, "0.00"},
		{1.0, "1.00"},
		// ── 边界值：这些是「远离零」实现的判别器 ──
		{0.555, "0.56"}, // 两者一致（巧合）
		{1.005, "1.00"}, // 两者一致（巧合）
		{2.675, "2.67"}, // **远离零实现给 2.68——错**
		{0.615, "0.61"}, // **远离零实现给 0.62——错**
		{1.255, "1.25"}, // **远离零实现给 1.26——错**
		{0.145, "0.14"}, // **远离零实现给 0.15——错**
	}
	for _, c := range cases {
		if got := toFixed2(c.in); got != c.want {
			t.Errorf("toFixed2(%v)：want %q，got %q", c.in, c.want, got)
		}
	}
}
