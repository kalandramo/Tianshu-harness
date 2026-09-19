package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// storeOracleEntry 是 oracle 里一项。
type storeOracleEntry struct {
	Claims   []ContextClaim `json:"claims"`
	Filtered []ContextClaim `json:"filtered"`
}

// storeOp 是 cases 里一个操作。
type storeOp struct {
	Op           string         `json:"op"`
	Proposal     *ClaimProposal `json:"proposal"`
	ClaimID      string         `json:"claimId"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason"`
	ConsumerID   string         `json:"consumerId"`
	ConsumerKind string         `json:"consumerKind"`
	UsedAt       int64          `json:"usedAt"`
	Delta        float64        `json:"delta"`
	Cap          float64        `json:"cap"`
}

// storeCase 是 cases 里一项。
type storeCase struct {
	Ops    []storeOp       `json:"ops"`
	Filter *storeFilterRaw `json:"filter"`
}

// storeFilterRaw 是原始过滤条件。
type storeFilterRaw struct {
	Status []string `json:"status"`
	Kind   []string `json:"kind"`
	Scope  []string `json:"scope"`
}

func loadStoreOracle(t *testing.T) map[string]storeOracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claimstore", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/claimstore/gen-oracle.ts）：%v", err)
	}
	var out map[string]storeOracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空")
	}
	return out
}

func loadStoreCases(t *testing.T) map[string]storeCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claimstore", "cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var out map[string]storeCase
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	return out
}

// runOps 把操作脚本转成事件流，再投影出 claim。
//
// **对账策略**：TS 侧通过 store 的公开方法（propose/updateClaimStatus/...）
// 间接驱动 private 的 applyEventsToMap。Go 侧直接构造等价事件流喂给
// ProjectClaims——两者的输入语义相同。
func runOps(t *testing.T, ops []storeOp) []ContextClaim {
	t.Helper()

	var events []ClaimEvent
	// firstClaimID 对账 TS 生成器的 `op.claimId || firstClaimId`
	firstClaimID := ""

	for i, op := range ops {
		id := op.ClaimID
		if id == "" {
			id = firstClaimID
		}

		switch op.Op {
		case "propose":
			if op.Proposal == nil {
				t.Fatalf("op[%d] propose 缺 proposal", i)
			}
			claim := CreateClaimFromProposal(*op.Proposal)
			if firstClaimID == "" {
				firstClaimID = claim.ID
			}
			events = append(events, ClaimEvent{
				Type:      EventClaimProposed,
				EventID:   op.Proposal.Source.EventID,
				CreatedAt: op.Proposal.CreatedAt,
				Claim:     &claim,
			})

		case "status":
			events = append(events, ClaimEvent{
				Type:      EventClaimStatusChanged,
				EventID:   "status:" + id + ":" + op.Status,
				CreatedAt: T0,
				ClaimID:   id,
				Status:    ContextClaimStatus(op.Status),
				Reason:    op.Reason,
			})

		case "use":
			events = append(events, ClaimEvent{
				Type:         EventClaimUsed,
				EventID:      "use:" + id + ":" + op.ConsumerID,
				CreatedAt:    op.UsedAt,
				ClaimID:      id,
				ConsumerID:   op.ConsumerID,
				ConsumerKind: op.ConsumerKind,
			})

		case "boost":
			// **boostFitness 是「取当前 fitness + delta，封顶 cap」**——
			// 事件里带的是**结果值**，故需先算出当前值
			current := 0.0
			for _, c := range ProjectClaims(events) {
				if c.ID == id {
					current = c.Fitness
					break
				}
			}
			newFitness := current + op.Delta
			if newFitness > op.Cap {
				newFitness = op.Cap
			}
			events = append(events, ClaimEvent{
				Type:      EventClaimBoosted,
				EventID:   "boost:" + id,
				CreatedAt: T0,
				ClaimID:   id,
				Fitness:   newFitness,
			})

		default:
			t.Fatalf("op[%d] 未知操作 %q", i, op.Op)
		}
	}

	return ProjectClaims(events)
}

// TestClaimStoreProjectionParity —— **投影层对账**（4 种事件 + 幂等 + 封顶）。
func TestClaimStoreProjectionParity(t *testing.T) {
	oracle := loadStoreOracle(t)
	cases := loadStoreCases(t)

	if len(cases) != len(oracle) {
		t.Fatalf("用例数不匹配：cases=%d oracle=%d", len(cases), len(oracle))
	}

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			spec, ok := cases[name]
			if !ok {
				t.Fatalf("oracle 有 %q 但 cases 没有", name)
			}

			got := runOps(t, spec.Ops)

			if len(got) != len(entry.Claims) {
				t.Fatalf("claim 数：Go=%d TS=%d", len(got), len(entry.Claims))
			}
			for i := range got {
				want := entry.Claims[i]
				g := got[i]

				if g.ID != want.ID {
					t.Errorf("[%d] ID：Go=%q TS=%q", i, g.ID, want.ID)
				}
				if g.Status != want.Status {
					t.Errorf("[%d] status：Go=%q TS=%q", i, g.Status, want.Status)
				}
				if g.Fitness != want.Fitness {
					t.Errorf("[%d] fitness：Go=%v TS=%v", i, g.Fitness, want.Fitness)
				}
				if g.LastUsedAt != want.LastUsedAt {
					t.Errorf("[%d] lastUsedAt：Go=%d TS=%d", i, g.LastUsedAt, want.LastUsedAt)
				}
				if len(g.Counterevidence) != len(want.Counterevidence) {
					t.Errorf("[%d] 反证数：Go=%d TS=%d", i, len(g.Counterevidence), len(want.Counterevidence))
				} else {
					for j := range g.Counterevidence {
						if g.Counterevidence[j].Summary != want.Counterevidence[j].Summary {
							t.Errorf("[%d] 反证[%d] summary：Go=%q TS=%q",
								i, j, g.Counterevidence[j].Summary, want.Counterevidence[j].Summary)
						}
					}
				}
				if len(g.Consumers) != len(want.Consumers) {
					t.Errorf("[%d] 消费者数：Go=%d TS=%d", i, len(g.Consumers), len(want.Consumers))
				} else {
					for j := range g.Consumers {
						if g.Consumers[j].ID != want.Consumers[j].ID {
							t.Errorf("[%d] 消费者[%d] id：Go=%q TS=%q",
								i, j, g.Consumers[j].ID, want.Consumers[j].ID)
						}
					}
				}
			}
		})
	}
}

// TestClaimStoreFilterParity —— 过滤语义对账。
func TestClaimStoreFilterParity(t *testing.T) {
	oracle := loadStoreOracle(t)
	cases := loadStoreCases(t)

	for name, entry := range oracle {
		if entry.Filtered == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			spec := cases[name]
			claims := runOps(t, spec.Ops)

			var status []ContextClaimStatus
			var kind []ContextClaimKind
			var scope []ContextClaimScope
			if spec.Filter != nil {
				for _, s := range spec.Filter.Status {
					status = append(status, ContextClaimStatus(s))
				}
				for _, k := range spec.Filter.Kind {
					kind = append(kind, ContextClaimKind(k))
				}
				for _, sc := range spec.Filter.Scope {
					scope = append(scope, ContextClaimScope(sc))
				}
			}

			got := FilterClaims(claims, status, kind, scope)
			if len(got) != len(entry.Filtered) {
				t.Fatalf("过滤结果数：Go=%d TS=%d", len(got), len(entry.Filtered))
			}
			for i := range got {
				if got[i].ID != entry.Filtered[i].ID {
					t.Errorf("[%d] ID：Go=%q TS=%q", i, got[i].ID, entry.Filtered[i].ID)
				}
			}
		})
	}
}
