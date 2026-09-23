package agent

import (
	"strings"
	"testing"
)

// mkEntry 构造一条普通（非豁免）advisory。
//
// **注意 category**：`maxPerCategory = 2` 会先把同 category 的条目截到 2 条
// ——若测试条目同 category，`cvmInjectionBaseBudget` 根本轮不到生效。
// 故调用方需用**不同 category** 越过 per-category 限制，才能真正测到 CVM 预算。
func mkEntry(key string, priority float64) AdvisoryEntry {
	return mkEntryCat(key, priority, CategoryDiscipline)
}

func mkEntryCat(key string, priority float64, cat AdvisoryCategory) AdvisoryEntry {
	return AdvisoryEntry{
		Key:      key,
		Priority: priority,
		Category: cat,
		Content:  "content-" + key,
		TTL:      1,
	}
}

// distinctCats 是 5 个不同 category（越过 maxPerCategory=2）。
var distinctCats = []AdvisoryCategory{
	CategoryDiscipline, CategoryTypecheck, CategoryTodo, CategoryBackground, CategoryMonitor,
}

// TestSRChannelBypassesBudget —— **本刀的核心不变量**。
//
// `cvmInjectionBaseBudget = 3` 会把最终进 prompt 的条目截到 3 条。SR 通道的
// 语义是**绕过该预算**（对账 TS 的「在 bus 竞争前分走，不占 Top-N 预算」）。
//
// 构造：1 条 SR + 5 条普通（高优先级）→ SR 必须出现，且普通条目仍受 3 条限制。
func TestSRChannelBypassesBudget(t *testing.T) {
	b := NewAdvisoryBus()

	// SR 条目：优先级**最低**——若走普通路径必被截掉。
	sr := mkEntry("sr-key", 0.1)
	sr.Channel = ChannelSystemReminder
	b.Submit(sr)

	// 5 条普通条目，优先级都高于 SR
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		b.Submit(mkEntryCat(k, 0.9-float64(i)*0.01, distinctCats[i]))
	}

	out := b.Render("", 1)

	if !strings.Contains(out, "content-sr-key") {
		t.Errorf("**SR 条目被预算截掉了**——未绕过 cvmInjectionBaseBudget。\n输出：%s", out)
	}
	// 普通条目应仍受 3 条限制（a/b/c 胜出）
	present := 0
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if strings.Contains(out, "content-"+k) {
			present++
		}
	}
	if present != cvmInjectionBaseBudget {
		t.Errorf("普通条目应恰有 %d 条（预算），得到 %d\n输出：%s", cvmInjectionBaseBudget, present, out)
	}
}

// **反证**：不设 Channel 的条目**照常受预算限制**（默认行为不变）。
func TestDefaultChannelStillBudgeted(t *testing.T) {
	b := NewAdvisoryBus()
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		b.Submit(mkEntryCat(k, 0.9-float64(i)*0.01, distinctCats[i]))
	}
	out := b.Render("", 1)

	present := 0
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if strings.Contains(out, "content-"+k) {
			present++
		}
	}
	if present != cvmInjectionBaseBudget {
		t.Errorf("默认通道应受预算限制（%d 条），得到 %d", cvmInjectionBaseBudget, present)
	}
}

// **反证**：显式设 ChannelBus 与不设行为一致。
func TestExplicitBusChannelSameAsDefault(t *testing.T) {
	withDefault := NewAdvisoryBus()
	withExplicit := NewAdvisoryBus()
	for i, k := range []string{"a", "b", "c", "d"} {
		withDefault.Submit(mkEntry(k, 0.9-float64(i)*0.01))
		e := mkEntry(k, 0.9-float64(i)*0.01)
		e.Channel = ChannelBus
		withExplicit.Submit(e)
	}
	if withDefault.Render("", 1) != withExplicit.Render("", 1) {
		t.Error("显式 ChannelBus 应与缺省行为一致")
	}
}

// **反证**：多条 SR 条目都绕过预算。
func TestMultipleSRChannelsAllBypass(t *testing.T) {
	b := NewAdvisoryBus()
	for i, k := range []string{"sr1", "sr2", "sr3"} {
		e := mkEntry(k, 0.1+float64(i)*0.01)
		e.Channel = ChannelSystemReminder
		b.Submit(e)
	}
	// 再加 5 条普通（占满预算）
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		b.Submit(mkEntryCat(k, 0.9-float64(i)*0.01, distinctCats[i]))
	}
	out := b.Render("", 1)

	for _, k := range []string{"sr1", "sr2", "sr3"} {
		if !strings.Contains(out, "content-"+k) {
			t.Errorf("SR 条目 %q 应绕过预算", k)
		}
	}
}

// **反证**：`status` 通道**不**绕过预算（Go 无 TUI sink，按 bus 处理）。
//
// 对账 TS 的「宁可占预算不静默消失」。
func TestStatusChannelDoesNotBypass(t *testing.T) {
	b := NewAdvisoryBus()
	// status 条目优先级最低——若绕过预算会挤掉普通条目
	st := mkEntry("st-key", 0.1)
	st.Channel = ChannelStatus
	b.Submit(st)
	for i, k := range []string{"a", "b", "c", "d", "e"} {
		b.Submit(mkEntryCat(k, 0.9-float64(i)*0.01, distinctCats[i]))
	}
	out := b.Render("", 1)

	// status 条目应被截掉（它不豁免，且优先级最低）
	if strings.Contains(out, "content-st-key") {
		t.Errorf("status 通道不应绕过预算（Go 无 TUI sink）：%s", out)
	}
	// 普通条目仍应 3 条
	present := 0
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if strings.Contains(out, "content-"+k) {
			present++
		}
	}
	if present != cvmInjectionBaseBudget {
		t.Errorf("普通条目应 %d 条，得到 %d", cvmInjectionBaseBudget, present)
	}
}

// SR 条目照常计入 delivered（核销闭环不因通道分流而丢失）。
func TestSRChannelStillDelivered(t *testing.T) {
	b := NewAdvisoryBus()
	sr := mkEntry("sr-key", 0.1)
	sr.Channel = ChannelSystemReminder
	b.Submit(sr)
	b.Render("", 1)

	delivered := b.DrainDelivered()
	found := false
	for _, d := range delivered {
		if d.Key == "sr-key" {
			found = true
		}
	}
	if !found {
		t.Errorf("SR 条目应照常进 delivered（核销闭环）：%+v", delivered)
	}
}

// **SR 在两条合并路径上都要保留**。
//
// `Render` 有两条产出 `sorted` 的路径：
//   - **不触发 CVM 预算重建**（`len(nonexempt) <= 3`）——走第 6 步的 append
//   - **触发重建**（`len(nonexempt) > 3`）——走第 7 步的 `keep` 重建
//
// 两个路径都必须保留 `srEntries`。只测其中一条会漏掉另一条的缺陷
// （M224 变异实测：仅测重建路径时 0 红）。
func TestSRChannelSurvivesBothMergePaths(t *testing.T) {
	// 路径 A：普通条目 ≤ 预算（不触发重建）
	t.Run("不触发重建", func(t *testing.T) {
		b := NewAdvisoryBus()
		sr := mkEntry("sr-a", 0.1)
		sr.Channel = ChannelSystemReminder
		b.Submit(sr)
		b.Submit(mkEntryCat("n1", 0.9, CategoryTypecheck)) // 仅 1 条普通

		out := b.Render("", 1)
		if !strings.Contains(out, "content-sr-a") {
			t.Errorf("不触发重建时 SR 应保留：%s", out)
		}
	})

	// 路径 B：普通条目 > 预算（触发重建）
	t.Run("触发重建", func(t *testing.T) {
		b := NewAdvisoryBus()
		sr := mkEntry("sr-b", 0.1)
		sr.Channel = ChannelSystemReminder
		b.Submit(sr)
		for i, k := range []string{"a", "b", "c", "d"} {
			b.Submit(mkEntryCat(k, 0.9-float64(i)*0.01, distinctCats[i]))
		}

		out := b.Render("", 1)
		if !strings.Contains(out, "content-sr-b") {
			t.Errorf("触发重建时 SR 应保留：%s", out)
		}
	})
}

// 常量值对账 TS。
func TestAdvisoryChannelConstants(t *testing.T) {
	cases := map[AdvisoryChannel]string{
		ChannelBus:            "bus",
		ChannelSystemReminder: "system-reminder",
		ChannelStatus:         "status",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("常量值不符：got %q, want %q", got, want)
		}
	}
}
