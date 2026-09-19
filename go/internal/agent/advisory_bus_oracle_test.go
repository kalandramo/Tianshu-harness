package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// busOracleEntry 对应 gen-oracle.ts 写出的一项。
type busOracleEntry struct {
	Renders       []string   `json:"renders"`
	Ledger        busLedger  `json:"ledger"`
	DeliveredKeys [][]string `json:"deliveredKeys"`
}

// busLedger 是 oracle 里的 ledger 形态（只取本移植覆盖的字段）。
type busLedger struct {
	Submitted   int      `json:"submitted"`
	Rendered    int      `json:"rendered"`
	Dropped     int      `json:"dropped"`
	DroppedKeys []string `json:"droppedKeys"`
	Deferred    int      `json:"deferred"`
	Revoked     int      `json:"revoked"`
	HeldOut     int      `json:"heldOut"`
	LiftMuted   int      `json:"liftMuted"`
}

// busCaseSpec 是 cases.json 里的一项。
type busCaseSpec struct {
	Batches []struct {
		Entries []busEntrySpec `json:"entries"`
		Domain  string         `json:"domain"`
	} `json:"batches"`
	Renders int `json:"renders"`
	// Streaks 是习惯化：key → 连续忽略次数（模拟 readback 的 getIgnoredStreak）
	Streaks map[string]int `json:"streaks"`
	// Lifts 是 lift 消费：key → 成熟 lift（null 模拟样本不足 = 中性）
	Lifts map[string]*float64 `json:"lifts"`
}

// fakeHabituation 是测试用的习惯化策略。
type fakeHabituation struct {
	streaks map[string]int
}

func (f *fakeHabituation) GetIgnoredStreak(key string) int { return f.streaks[key] }

// busEntrySpec 是 oracle 用例里的一条 advisory。
type busEntrySpec struct {
	Key       string  `json:"key"`
	Priority  float64 `json:"priority"`
	Category  string  `json:"category"`
	Content   string  `json:"content"`
	Tier      string  `json:"tier"`
	TTL       *int    `json:"ttl"`
	Immediate bool    `json:"immediate"`
}

func loadBusOracle(t *testing.T) map[string]busOracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "advisorybus", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/advisorybus/gen-oracle.ts）：%v", err)
	}
	var out map[string]busOracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空")
	}
	return out
}

func loadBusCases(t *testing.T) map[string]busCaseSpec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "advisorybus", "cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var out map[string]busCaseSpec
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	return out
}

// toEntry 把 JSON 形态转成 Go 侧 AdvisoryEntry。
func (s busEntrySpec) toEntry() AdvisoryEntry {
	e := AdvisoryEntry{
		Key:       s.Key,
		Priority:  s.Priority,
		Category:  AdvisoryCategory(s.Category),
		Content:   s.Content,
		Immediate: s.Immediate,
	}
	if s.Tier != "" {
		e.Tier = AdvisoryTier(s.Tier)
	}
	if s.TTL != nil {
		e.TTL = *s.TTL
	}
	return e
}

// TestAdvisoryBusOracleParity —— **核心对账**：Go bus 的渲染输出与 TS 逐字节一致。
func TestAdvisoryBusOracleParity(t *testing.T) {
	oracle := loadBusOracle(t)
	cases := loadBusCases(t)

	if len(cases) != len(oracle) {
		t.Fatalf("用例数不匹配：cases=%d oracle=%d", len(cases), len(oracle))
	}

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			spec, ok := cases[name]
			if !ok {
				t.Fatalf("oracle 有 %q 但 cases 没有", name)
			}

			bus := NewAdvisoryBus()
			if len(spec.Streaks) > 0 {
				bus.SetHabituationPolicy(&fakeHabituation{streaks: spec.Streaks})
			}
			if len(spec.Lifts) > 0 {
				bus.SetLiftProvider(func(key string) *float64 {
					if v, ok := spec.Lifts[key]; ok {
						return v
					}
					return nil
				})
			}
			for i := 0; i < spec.Renders; i++ {
				if i < len(spec.Batches) {
					b := spec.Batches[i]
					for _, es := range b.Entries {
						bus.Submit(es.toEntry())
					}
				}
				domain := ""
				if i < len(spec.Batches) {
					domain = spec.Batches[i].Domain
				}

				got := bus.Render(domain, i)
				if got != entry.Renders[i] {
					t.Errorf("render[%d] 不一致\nwant: %q\ngot:  %q", i, entry.Renders[i], got)
				}

				// 送达 key 序列对账
				gotKeys := make([]string, 0)
				for _, d := range bus.DrainDelivered() {
					gotKeys = append(gotKeys, d.Key)
				}
				if len(gotKeys) != len(entry.DeliveredKeys[i]) {
					t.Errorf("delivered[%d] 长度：Go=%v TS=%v", i, gotKeys, entry.DeliveredKeys[i])
				} else {
					for j := range gotKeys {
						if gotKeys[j] != entry.DeliveredKeys[i][j] {
							t.Errorf("delivered[%d][%d]：Go=%q TS=%q", i, j, gotKeys[j], entry.DeliveredKeys[i][j])
						}
					}
				}
			}
		})
	}
}

// TestAdvisoryBusLedgerParity —— ledger 计数对账。
func TestAdvisoryBusLedgerParity(t *testing.T) {
	oracle := loadBusOracle(t)
	cases := loadBusCases(t)

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			spec := cases[name]
			bus := NewAdvisoryBus()
			if len(spec.Streaks) > 0 {
				bus.SetHabituationPolicy(&fakeHabituation{streaks: spec.Streaks})
			}
			if len(spec.Lifts) > 0 {
				bus.SetLiftProvider(func(key string) *float64 {
					if v, ok := spec.Lifts[key]; ok {
						return v
					}
					return nil
				})
			}
			for i := 0; i < spec.Renders; i++ {
				if i < len(spec.Batches) {
					for _, es := range spec.Batches[i].Entries {
						bus.Submit(es.toEntry())
					}
				}
				domain := ""
				if i < len(spec.Batches) {
					domain = spec.Batches[i].Domain
				}
				bus.Render(domain, i)
				bus.DrainDelivered()
			}

			got := bus.DrainLedger()
			want := entry.Ledger
			if got.Submitted != want.Submitted {
				t.Errorf("submitted：Go=%d TS=%d", got.Submitted, want.Submitted)
			}
			if got.Rendered != want.Rendered {
				t.Errorf("rendered：Go=%d TS=%d", got.Rendered, want.Rendered)
			}
			if got.Dropped != want.Dropped {
				t.Errorf("dropped：Go=%d TS=%d", got.Dropped, want.Dropped)
			}
			if !equalStringSets(got.DroppedKeys, want.DroppedKeys) {
				t.Errorf("droppedKeys：Go=%v TS=%v", got.DroppedKeys, want.DroppedKeys)
			}
		})
	}
}

// equalStringSets 比较两个字符串集合（忽略顺序）。
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[string]int{}
	for _, s := range a {
		ma[s]++
	}
	for _, s := range b {
		ma[s]--
	}
	for _, v := range ma {
		if v != 0 {
			return false
		}
	}
	return true
}
