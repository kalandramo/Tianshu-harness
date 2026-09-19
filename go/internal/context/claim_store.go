package context

// ClaimEventType 是 claim 事件的类型。
//
// 对账 ContextClaimEvent 的判别联合。
type ClaimEventType string

const (
	EventClaimProposed      ClaimEventType = "claim_proposed"
	EventClaimStatusChanged ClaimEventType = "claim_status_changed"
	EventClaimUsed          ClaimEventType = "claim_used"
	EventClaimBoosted       ClaimEventType = "claim_boosted"
)

// MaxConsumersPerClaim 是单 claim 的消费者记录上限。
//
// 对账 MAX_CONSUMERS_PER_CLAIM。**封顶策略**：保留**最近**的
// （对账 TS 的 `newConsumers.slice(-MAX)`）。
const MaxConsumersPerClaim = 50

// MaxActiveClaims 是活跃 claim 上限。
//
// 对账 MAX_ACTIVE_CLAIMS。**注意**：TS 里该常量定义了但未见使用——
// 移植保留以对齐，不引入未对账的行为。
const MaxActiveClaims = 50

// ClaimEvent 是 claim 事件流的一项。
//
// 对账 ContextClaimEvent。**Go 侧用扁平结构 + Type 字段**（Go 无判别联合），
// 各变体字段共存（未用的为零值）。
type ClaimEvent struct {
	Type      ClaimEventType
	EventID   string
	CreatedAt int64
	// Seq 是单写者序号（0 表示未分配）。
	Seq int64

	// ── claim_proposed ──
	Claim *ContextClaim

	// ── claim_status_changed / claim_used / claim_boosted ──
	ClaimID string

	// ── claim_status_changed ──
	Status ContextClaimStatus
	Reason string

	// ── claim_used ──
	ConsumerID   string
	ConsumerKind string

	// ── claim_boosted ──
	Fitness float64
}

// ProjectClaims 从事件流投影出 claim 状态。
//
// 对账 applyEventsToMap + projectClaims 的「全量重建」路径。
//
// **四种事件的语义**：
//
//	claim_proposed       — **幂等**：已存在同 ID 的 claim 则忽略（不覆盖）
//	claim_status_changed — 改状态；**若新状态非 active，追加一条反证**
//	claim_used           — 追加消费者记录（**封顶 50，保留最近**），更新 lastUsedAt
//	claim_boosted        — 覆盖 fitness（**不是累加**）
//
// **返回顺序**：按 claim 首次出现序（对账 TS 的 Map 插入序）。
func ProjectClaims(events []ClaimEvent) []ContextClaim {
	type slot struct {
		claim ContextClaim
	}
	order := make([]string, 0, len(events))
	index := make(map[string]int, len(events))
	claims := make([]slot, 0, len(events))

	get := func(id string) *slot {
		i, ok := index[id]
		if !ok {
			return nil
		}
		return &claims[i]
	}

	for _, ev := range events {
		switch ev.Type {
		case EventClaimProposed:
			if ev.Claim == nil {
				continue
			}
			// **幂等**：已存在则不覆盖
			if _, ok := index[ev.Claim.ID]; ok {
				continue
			}
			index[ev.Claim.ID] = len(claims)
			order = append(order, ev.Claim.ID)
			claims = append(claims, slot{claim: *ev.Claim})

		case EventClaimStatusChanged:
			s := get(ev.ClaimID)
			if s == nil {
				continue
			}
			// **条件追加反证**：新状态为 active 时**保留**原反证（不追加）
			if ev.Status != StatusActive {
				s.claim.Counterevidence = append(s.claim.Counterevidence, EvidenceRef{
					ID:        ev.EventID,
					Kind:      "tool_result",
					Summary:   ev.Reason,
					CreatedAt: ev.CreatedAt,
				})
			}
			s.claim.Status = ev.Status

		case EventClaimUsed:
			s := get(ev.ClaimID)
			if s == nil {
				continue
			}
			s.claim.Consumers = append(s.claim.Consumers, ConsumerRef{
				ID:     ev.ConsumerID,
				Kind:   ev.ConsumerKind,
				UsedAt: ev.CreatedAt,
			})
			// **封顶：保留最近 N 条**
			if len(s.claim.Consumers) > MaxConsumersPerClaim {
				s.claim.Consumers = s.claim.Consumers[len(s.claim.Consumers)-MaxConsumersPerClaim:]
			}
			s.claim.LastUsedAt = ev.CreatedAt

		case EventClaimBoosted:
			s := get(ev.ClaimID)
			if s == nil {
				continue
			}
			// **覆盖而非累加**（对账 TS 的 `{...claim, fitness: event.fitness}`）
			s.claim.Fitness = ev.Fitness
		}
	}

	out := make([]ContextClaim, 0, len(claims))
	for _, id := range order {
		out = append(out, claims[index[id]].claim)
	}
	return out
}

// FilterClaims 按过滤条件筛选 claim。
//
// 对账 listClaims 的 filter：status / kind / scope 三个**可选**维度，
// 每个维度是「包含即通过」的集合语义。
func FilterClaims(claims []ContextClaim, status []ContextClaimStatus, kind []ContextClaimKind, scope []ContextClaimScope) []ContextClaim {
	out := make([]ContextClaim, 0, len(claims))
	for _, c := range claims {
		if len(status) > 0 && !containsStatus(status, c.Status) {
			continue
		}
		if len(kind) > 0 && !containsKind(kind, c.Kind) {
			continue
		}
		if len(scope) > 0 && !containsScope(scope, c.Scope) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func containsStatus(xs []ContextClaimStatus, v ContextClaimStatus) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsKind(xs []ContextClaimKind, v ContextClaimKind) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsScope(xs []ContextClaimScope, v ContextClaimScope) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
