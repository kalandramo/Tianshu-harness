package agent

import (
	"strings"
	"testing"
)

// TestGovernanceCooldownSuppressesRepeat —— **key 冷却抑制重复送达**。
//
// 用注册 key（readonly-spiral，3 轮冷却）验证：
// 第 0 轮送达 → 第 1/2 轮被吞掉（空渲染）→ 第 3 轮恢复。
func TestGovernanceCooldownSuppressesRepeat(t *testing.T) {
	bus := NewAdvisoryBus()

	entry := func() AdvisoryEntry {
		return AdvisoryEntry{
			Key: "readonly-spiral", Priority: 0.6,
			Category: CategoryDiscipline, Content: "开始行动",
		}
	}

	// 第 0 轮：送达
	bus.Submit(entry())
	out0 := bus.Render("", 0)
	if !strings.Contains(out0, "readonly-spiral") {
		t.Fatal("第 0 轮应送达")
	}

	// 第 1、2 轮：冷却中，被吞掉
	for i := 1; i <= 2; i++ {
		bus.Submit(entry())
		out := bus.Render("", i)
		if out != "" {
			t.Errorf("第 %d 轮应在冷却中被吞掉（空渲染），得到 %q", i, out)
		}
	}

	// 第 3 轮：冷却期满（3 轮），恢复送达
	bus.Submit(entry())
	out3 := bus.Render("", 3)
	if !strings.Contains(out3, "readonly-spiral") {
		t.Errorf("第 3 轮冷却期满应恢复送达，得到 %q", out3)
	}
}

// TestGovernanceCooldownOnlyRegisteredKeys —— **冷却只影响注册 key**。
//
// 未注册的 key 每轮都送达（无冷却）——对照组。
func TestGovernanceCooldownOnlyRegisteredKeys(t *testing.T) {
	bus := NewAdvisoryBus()

	for i := 0; i < 3; i++ {
		bus.Submit(AdvisoryEntry{
			Key: "unregistered-key", Priority: 0.6,
			Category: CategoryDiscipline, Content: "普通提醒",
		})
		out := bus.Render("", i)
		if !strings.Contains(out, "unregistered-key") {
			t.Errorf("未注册 key 第 %d 轮应送达（无冷却），得到 %q", i, out)
		}
	}
}

// TestGovernanceCooldownCountsDeliveredOnly —— **只算实际送达的轮次**。
//
// 若某轮没送达（如被预算挤掉），冷却时钟不该前进。
func TestGovernanceCooldownCountsDeliveredOnly(t *testing.T) {
	bus := NewAdvisoryBus()

	// 第 0 轮：readonly-spiral 被更高优先级的 constitutional 挤掉？
	// 不会——constitutional 不占预算。改用：第 0 轮不投递该 key。
	bus.Submit(AdvisoryEntry{Key: "other", Priority: 0.9, Category: CategoryRepair, Content: "别的"})
	bus.Render("", 0) // readonly-spiral 未投递

	// 第 1 轮首次投递 readonly-spiral → 应送达（此前从未送达，无冷却）
	bus.Submit(AdvisoryEntry{
		Key: "readonly-spiral", Priority: 0.6,
		Category: CategoryDiscipline, Content: "行动",
	})
	out1 := bus.Render("", 1)
	if !strings.Contains(out1, "readonly-spiral") {
		t.Errorf("首次投递应送达（此前未送达过，无冷却），得到 %q", out1)
	}
}

// TestGovernanceMutexLoserYields —— **互斥对 loser 让位**。
//
// self-verify（验证债）在场时，virtue-encouragement（表扬）应被丢弃。
func TestGovernanceMutexLoserYields(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.Submit(AdvisoryEntry{Key: "self-verify", Priority: 0.58, Category: CategoryDiscipline, Content: "有验证债"})
	bus.Submit(AdvisoryEntry{Key: "virtue-encouragement", Priority: 0.4, Category: CategoryEncouragement, Content: "干得好"})

	out := bus.Render("", 0)
	if !strings.Contains(out, "self-verify") {
		t.Error("winner（self-verify）应渲染")
	}
	if strings.Contains(out, "virtue-encouragement") {
		t.Error("「有债仍表扬」是语义冲突——loser 应让位")
	}

	ledger := bus.DrainLedger()
	found := false
	for _, k := range ledger.DroppedKeys {
		if k == "virtue-encouragement" {
			found = true
		}
	}
	if !found {
		t.Errorf("让位的 loser 应记入 dropped，得到 %v", ledger.DroppedKeys)
	}
}

// TestGovernanceMutexWinnerAbsent —— **winner 不在场时 loser 正常渲染**。
func TestGovernanceMutexWinnerAbsent(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.Submit(AdvisoryEntry{Key: "virtue-encouragement", Priority: 0.4, Category: CategoryEncouragement, Content: "干得好"})

	out := bus.Render("", 0)
	if !strings.Contains(out, "virtue-encouragement") {
		t.Errorf("winner 不在场时 loser 应正常渲染，得到 %q", out)
	}
}

// TestGovernanceMutexAllPairs —— 覆盖 mutex 表的每一对。
func TestGovernanceMutexAllPairs(t *testing.T) {
	for _, pair := range mutexPairs {
		t.Run(pair.winner+"_wins", func(t *testing.T) {
			bus := NewAdvisoryBus()
			bus.Submit(AdvisoryEntry{Key: pair.winner, Priority: 0.6, Category: CategoryDiscipline, Content: "W"})
			bus.Submit(AdvisoryEntry{Key: pair.loser, Priority: 0.5, Category: CategoryEncouragement, Content: "L"})

			out := bus.Render("", 0)
			if !strings.Contains(out, pair.winner) {
				t.Errorf("winner %q 应渲染", pair.winner)
			}
			if strings.Contains(out, `key="`+pair.loser+`"`) {
				t.Errorf("loser %q 应让位", pair.loser)
			}
		})
	}
}

// TestGovernanceCooldownAndMutexInteraction —— **顺序**：冷却先于 mutex。
//
// 若冷却先把 loser 吞掉，mutex 段不会重复计 dropped。
func TestGovernanceCooldownAndMutexInteraction(t *testing.T) {
	bus := NewAdvisoryBus()

	// 第 0 轮：readonly-spiral 送达（进入 3 轮冷却）
	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "行动"})
	bus.Render("", 0)

	// 第 1 轮：readonly-spiral 被冷却吞掉 + lossy-observation 在场
	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "行动"})
	bus.Submit(AdvisoryEntry{Key: "lossy-observation", Priority: 0.6, Category: CategoryDiscipline, Content: "有损"})
	out := bus.Render("", 1)

	if !strings.Contains(out, "lossy-observation") {
		t.Error("lossy-observation 应渲染")
	}
	if strings.Contains(out, "readonly-spiral") {
		t.Error("readonly-spiral 应在冷却中被吞掉")
	}
}

// TestGovernanceCooldownLedgerAccounting —— 被吞掉的计入 dropped 账本。
func TestGovernanceCooldownLedgerAccounting(t *testing.T) {
	bus := NewAdvisoryBus()

	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	bus.Render("", 0)
	bus.DrainLedger() // 清账

	// 第 1 轮被吞
	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	bus.Render("", 1)

	ledger := bus.DrainLedger()
	if ledger.Dropped != 1 {
		t.Errorf("被吞掉的应计 1 次 dropped，得到 %d", ledger.Dropped)
	}
	if ledger.Rendered != 0 {
		t.Errorf("被吞掉的不应计 rendered，得到 %d", ledger.Rendered)
	}
}

// TestGovernanceCooldownTableOnlyRegistered —— **冷却表只记注册 key**。
//
// 对账 recordDeliveredRender 的 `if (KEY_COOLDOWN_TURNS.has(key))`。
//
// **为什么单独钉住**：放宽写入范围（非注册 key 也记）在**行为上**是等价变异
// （冷却查询同样要求 registered，多余表项不影响渲染），只有直接检查内部状态
// 才能发现。这条不变量保障表不会无界增长——未注册 key 数量无上限。
func TestGovernanceCooldownTableOnlyRegistered(t *testing.T) {
	bus := NewAdvisoryBus()

	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "注册"})
	bus.Submit(AdvisoryEntry{Key: "totally-unregistered", Priority: 0.5, Category: CategoryRepair, Content: "未注册"})
	bus.Render("", 0)

	if _, ok := bus.lastDeliveredRenderByKey["readonly-spiral"]; !ok {
		t.Error("注册 key 应记入冷却表")
	}
	if _, ok := bus.lastDeliveredRenderByKey["totally-unregistered"]; ok {
		t.Error("**未注册 key 不该记入冷却表**——表会无界增长")
	}
}

// TestGovernanceResetClearsCooldown —— Reset 清空冷却状态。
func TestGovernanceResetClearsCooldown(t *testing.T) {
	bus := NewAdvisoryBus()
	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	bus.Render("", 0)

	bus.Reset()

	// Reset 后应无冷却——立即可送达
	bus.Submit(AdvisoryEntry{Key: "readonly-spiral", Priority: 0.6, Category: CategoryDiscipline, Content: "X"})
	out := bus.Render("", 1)
	if !strings.Contains(out, "readonly-spiral") {
		t.Errorf("Reset 后冷却应清空，得到 %q", out)
	}
}
