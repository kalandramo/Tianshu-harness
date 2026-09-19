package agent

import (
	"sort"
	"strconv"
	"strings"
)

// AdvisoryBus 是统一劝导总线——把多条纠偏通道收敛为单一 `<星域-advisory>`
// 汇聚块。
//
// 对账 src/agent/advisory-bus.ts 的 AdvisoryBus **核心路径**：
// submit / 去重 / 排序 / 类别上限 / Top-N 预算 / TTL 存活 / XML 渲染 / ledger。
//
// **未移植的治理子系统**（都是 provider 注入才生效；不注入时行为即核心路径）：
//   - 习惯化对抗（habituation / silenceRemaining / 升级措辞）
//   - efficacy 负反馈环（efficacyStats / 冷却翻倍 / 会话内静默）
//   - lift 消费（liftProvider / 负 lift 静音 / probation）
//   - holdout 反事实抽样（holdout / 影子桶）
//   - SR 通道（systemReminderOut / drainSystemReminders / requeue 携带）
//   - status 通道（statusSink）
//   - 阶段抑制 / 挂起观察（pendingWatch / suppressedCarry / 自愈撤销）
//   - mutex 互斥对（MUTEX_PAIRS）
//   - key 级送达冷却（KEY_COOLDOWN_TURNS）
//   - 星域措辞适配（toneAdapter）
//
// **这是刻意的 Scope Check**：治理子系统共约 800 行，各自依赖跨会话状态
// （readback / efficacy 台账 / 随机抽样）。核心路径是它们的公共基座——
// 先把基座做对并可对账，上层再叠。
type AdvisoryBus struct {
	// entries 是本轮投递的条目（render 后清空）。
	entries []AdvisoryEntry
	// alive 是存活条目——未过期的跨轮条目（TTL > 1）。
	alive []AdvisoryEntry

	// ── 投递账本 ──
	ledgerSubmitted   int
	ledgerRendered    int
	ledgerDropped     int
	ledgerDroppedKeys []string
	// ledgerLiftMuted 是负 lift 静音的累计条目数（对账 ledgerLiftMuted）。
	ledgerLiftMuted int

	// delivered 是 render 实际送达的条目（供 readback 追踪采纳）。
	delivered []DeliveredAdvisory

	// renderEpoch 是会话内单调渲染序号。
	//
	// **为什么不用 turn**：render(turn) 的 turn 是 run 内局部序号，每个 run
	// 都从 0 重置——用它做冷却会导致固定在 turn=12 触发的 key 永久静默。
	renderEpoch int

	// lastDeliveredRenderByKey 是 key → 上次实际送达的单调渲染序号。
	//
	// 对账 lastDeliveredRenderByKey。只记 **KEY_COOLDOWN_TURNS 注册的 key**
	// （对账 recordDeliveredRender 的 `if (KEY_COOLDOWN_TURNS.has(key))`）。
	lastDeliveredRenderByKey map[string]int

	// habituation 是习惯化查询源（注入）。
	//
	// nil 时不做习惯化对抗（对账 TS 的 `if (this.habituation)`）。
	habituation HabituationPolicy
	// silenceRemaining 是 key → 剩余静音渲染周期数。
	silenceRemaining map[string]int
	// lastSilencedStreak 是 key → 上次触发静音时的 ignoredStreak。
	//
	// **防止同一 streak 反复静音，保证 probation 放行**——若不复位，
	// streak 不涨的情况下每轮都会重新触发静音。
	lastSilencedStreak map[string]int

	// liftProvider 是成熟 lift 查询源（注入）。
	//
	// nil 时不做 lift 消费（对账 TS 的 `if (this.liftProvider)`）。
	// 返回 nil = 样本不足 = **中性**，不得据此静音。
	liftProvider func(key string) *float64
	// liftMuteRemaining 是 key → 剩余 lift 静音渲染周期数。
	//
	// **独立于 silenceRemaining**（对账 TS 的 liftMuteRemaining）——两个触发源
	// （习惯化 streak / 负 lift）的静音互不干扰，各自计时。
	liftMuteRemaining map[string]int
	// liftProbation 是「静音期满、下次出现放行一次」的 key 集合。
	//
	// 与习惯化的 probation 机制同构但**独立**：lift 仍 ≤0 才再静音，
	// 避免数据不更新导致永久静音。
	liftProbation map[string]bool
	// ledgerDeferred / ledgerRevoked 等治理账本字段（本移植只保留必要项）。
}

// HabituationPolicy 是习惯化查询源。
//
// 对账 HabituationPolicy——只含 `getIgnoredStreak`（接口隔离：
// bus 只需要这一个事实）。
type HabituationPolicy interface {
	GetIgnoredStreak(key string) int
}

// 习惯化对抗常量。
//
// 对账 HABITUATION_ESCALATE_STREAK / HABITUATION_SILENCE_STREAK /
// HABITUATION_SILENCE_RENDERS。
const (
	// habituationEscalateStreak 是触发升级措辞的最低连续忽略次数。
	habituationEscalateStreak = 2
	// habituationSilenceStreak 是触发静音的最低连续忽略次数。
	habituationSilenceStreak = 3
	// habituationSilenceRenders 是每次静音持续的渲染周期数。
	habituationSilenceRenders = 4

	// liftMuteRenders 是负 lift 静音的渲染周期数。
	//
	// 对账 LIFT_MUTE_RENDERS。**比习惯化静音长**（10 vs 4）——lift 基于反事实
	// 证据，结论更可靠，不需要那么频繁地重新试探。
	liftMuteRenders = 10
	// liftMuteThreshold 是触发静音的 lift 上限。
	//
	// 对账 LIFT_MUTE_THRESHOLD（= 0）。**lift <= 0 都静音**：
	// 正 lift = 提醒有真实增益；lift≈0 = 模型本来就会做（纯噪音）。
	liftMuteThreshold = 0.0
)

// SetHabituationPolicy 注入习惯化查询源。
//
// 对账 setHabituationPolicy。缺省 = 不做习惯化对抗。
func (b *AdvisoryBus) SetHabituationPolicy(policy HabituationPolicy) {
	b.habituation = policy
}

// SetLiftProvider 注入成熟 lift 查询源。
//
// 对账 setLiftProvider（advisory-bus.ts:536）。生产装配：
//
//	bus.SetLiftProvider(readback.GetMatureLift)
//
// 缺省 = 不做 lift 消费。**返回 nil 表示样本不足**（成熟度门未过）——
// 消费端必须视为中性，不得据此静音。
func (b *AdvisoryBus) SetLiftProvider(provider func(key string) *float64) {
	b.liftProvider = provider
}

// isLiftExempt 判断条目是否豁免 lift 静音。
//
// 对账 TS 的豁免集（advisory-bus.ts:958）——**三类**：
// constitutional tier / immediate 条目 / star_domain 类别。
// 与 holdout 资格判定同源（这些条目要么是宪法级约束，要么是即时守护，
// 要么是星域情境提醒，都不该被统计意义上的「无效」判定静音掉）。
func isLiftExempt(e AdvisoryEntry) bool {
	return e.Tier == TierConstitutional || e.Immediate || e.Category == CategoryStarDomain
}

// keyCooldownTurns 是 key 级送达冷却表（轮数）。
//
// 对账 KEY_COOLDOWN_TURNS。**动机**（TS 注释）：virtue-settlement-hook 每次
// 美德结算都提交一次表扬（实测 9-88 次/会话），而表扬的信息量在节奏确认而非
// 重复计数。门禁放 bus 层而非 hook 层——送达历史在 bus 手边，且天然覆盖未来
// 任何调用方。
//
// readonly-spiral / turn-call-limit 的冷却动机（TS 注释）：它们在 advisory
// 洪水期反复弹窗加剧噪音，key 级冷却确保同一提醒不在连续轮次重复注入。
var keyCooldownTurns = map[string]int{
	"virtue-encouragement": 5,
	"readonly-spiral":      3,
	"turn-call-limit":      3,
}

// mutexPair 是一对语义冲突的信号。
type mutexPair struct {
	winner string
	loser  string
}

// mutexPairs 是互斥对表。
//
// 对账 MUTEX_PAIRS。**机制**：同一 render 周期内语义冲突的两个信号同时在场时，
// 确定度低的一方（loser）让位丢弃。
//
// 首个已知冲突（TS 注释）：lossy-observation（观测确实被截断——事实）胜过
// readonly-spiral（"信息可能已足够，开始行动"——启发式）。观测有损时"已足够"
// 不成立，同轮双发会同时催"继续交叉验证"和"停止读取"。
//
// W3 穿透让位（TS 注释）：验证债在场时表扬让位。「你有债」和「干得好」同屏是
// 语义冲突——714c5d9b 里 self-verify 被忽略、同轮表扬照发，验证债类缺陷从提醒
// 眼皮底下逃逸。
//
// **匹配是精确 key 等值，不支持通配**——CCR 验证债 key 增多时逐条补录。
var mutexPairs = []mutexPair{
	{winner: "lossy-observation", loser: "readonly-spiral"},
	{winner: "self-verify", loser: "virtue-encouragement"},
	{winner: "self-verify-scope-mismatch", loser: "virtue-encouragement"},
	{winner: "ccr-天权-P3", loser: "virtue-encouragement"},
	{winner: "ccr-天权-P7", loser: "virtue-encouragement"},
}

// DeliveredAdvisory 是已送达条目的快照（供 readback 跟踪）。
//
// 对账 DeliveredAdvisory。**shadow** 标记 holdout 反事实组（赢得渲染位但被
// 静默扣留）——本移植不含 holdout，故恒为 false，但结构保留以对齐 TS。
type DeliveredAdvisory struct {
	Key      string
	Category AdvisoryCategory
	Tier     AdvisoryTier
	Expect   *AdvisoryExpectation
	Shadow   bool
}

// AdvisoryLedgerDelta 是投递账本快照（自上次 drain 以来的增量）。
//
// 对账 AdvisoryLedgerDelta。**只含本移植覆盖的字段**——治理子系统的计数字段
// （deferred / revoked / heldOut / liftMuted / sr*）恒为 0，结构保留以便对齐。
type AdvisoryLedgerDelta struct {
	Submitted   int
	Rendered    int
	Dropped     int
	DroppedKeys []string
	Deferred    int
	Revoked     int
	HeldOut     int
	LiftMuted   int
}

// 预算常量——对账 TS 同名常量。
const (
	// maxAdvisoriesPerTurn 是每轮最大渲染条数（non-constitutional 上限）。
	maxAdvisoriesPerTurn = 3
	// selfDirectedDomainBudget 是天权/瑶光等自主判断型星域的上限。
	//
	// TS 注释：这些星域有自己的判断力，过度提醒是噪音。
	selfDirectedDomainBudget = 1
	// maxPerCategory 是每 category 最多保留条数，防止单一信号源垄断预算。
	maxPerCategory = 2
	// cvmInjectionBaseBudget 是最终进入 prompt 的总数上限（Wave 2 统一注入预算）。
	cvmInjectionBaseBudget = 3
	// ledgerDroppedKeysCap 是 droppedKeys 的保留上限。
	ledgerDroppedKeysCap = 50
)

// advisoryBudgetForDomain 返回星域感知的轮预算。
//
// 对账 advisoryBudgetForDomain：自主判断型（天权/瑶光）减量，其余全局上限。
func advisoryBudgetForDomain(activeStarDomain string) int {
	if activeStarDomain == "天权" || activeStarDomain == "瑶光" {
		return selfDirectedDomainBudget
	}
	return maxAdvisoriesPerTurn
}

// NewAdvisoryBus 构造总线。
func NewAdvisoryBus() *AdvisoryBus {
	return &AdvisoryBus{}
}

// Submit 投递一条 advisory。
func (b *AdvisoryBus) Submit(entry AdvisoryEntry) {
	b.entries = append(b.entries, entry)
	b.ledgerSubmitted++
}

// SubmitAll 批量投递。
func (b *AdvisoryBus) SubmitAll(entries []AdvisoryEntry) {
	b.entries = append(b.entries, entries...)
	b.ledgerSubmitted += len(entries)
}

// DrainLedger 读取并清零投递账本（自上次 drain 以来的增量）。
func (b *AdvisoryBus) DrainLedger() AdvisoryLedgerDelta {
	delta := AdvisoryLedgerDelta{
		Submitted:   b.ledgerSubmitted,
		Rendered:    b.ledgerRendered,
		Dropped:     b.ledgerDropped,
		DroppedKeys: dedupStrings(b.ledgerDroppedKeys),
		Deferred:    0,
		Revoked:     0,
		HeldOut:     0,
		LiftMuted:   0,
	}
	b.ledgerSubmitted = 0
	b.ledgerRendered = 0
	b.ledgerDropped = 0
	b.ledgerDroppedKeys = nil
	return delta
}

// DrainDelivered 读取并清空「已送达」条目快照。
//
// 调用方（turn-step-producer）在 render 后立刻 drain 并交给 AdvisoryReadback。
func (b *AdvisoryBus) DrainDelivered() []DeliveredAdvisory {
	out := b.delivered
	b.delivered = nil
	return out
}

// Render 渲染本轮劝导为 `<星域-advisory>` XML 块。
//
// 对账 TS 的 render() **核心路径**。**处理顺序**（与 TS 一致）：
//
//  1. 合并 alive + entries 为竞争池
//  2. 按 tier 分流：constitutional 豁免一切上限；其余走去重 + 类别上限
//  3. 去重：同 key 保留**高 priority**（先出现的胜出当且仅当 priority 不更低）
//  4. 类别上限：每 category 最多 2 条
//  5. 分层取用：operational 先、informational 填空（star_domain 豁免预算）
//  6. 排序：constitutional 在前，其余按 priority 降序
//  7. CVM 注入预算：constitutional / immediate 豁免，其余截到 3 条
//  8. TTL 递减：> 1 的条目进 alive 供下轮
//
// **渲染的 priority 是条目自身值（toFixed(2)），不是排序用的有效优先级**——
// TS 注释：有效优先级逐轮变化，写进注入文本会让同一条 ttl>1 的建议在 alive
// 周期内字节抖动，而 advisory 走 appendix 通道（前缀缓存敏感）。
func (b *AdvisoryBus) Render(activeStarDomain string, turn int) string {
	b.renderEpoch++

	// ── 1. 竞争池 ──
	all := make([]AdvisoryEntry, 0, len(b.alive)+len(b.entries))
	all = append(all, b.alive...)
	all = append(all, b.entries...)

	// ── 1a. key 级送达冷却（**在一切竞争逻辑之前**）──
	//
	// 对账 TS 的 W2 key 级送达冷却段。TS 注释：冷却中的条目不该占
	// MUTEX / 预算 / 挂起任何一席。
	//
	// **吞掉 ≠ 永久丢失**：调用方按轮重新 submit，冷却过后自动恢复送达。
	if len(all) > 0 {
		cooled := make([]AdvisoryEntry, 0, len(all))
		var swallowed []string
		for _, e := range all {
			cooldown, registered := keyCooldownTurns[e.Key]
			last, seen := b.lastDeliveredRenderByKey[e.Key]
			if registered && seen && b.renderEpoch-last < cooldown {
				swallowed = append(swallowed, e.Key)
			} else {
				cooled = append(cooled, e)
			}
		}
		if len(swallowed) > 0 {
			b.recordDropped(swallowed)
			all = cooled
		}
	}

	// ── 1b. 互斥对：语义冲突信号同场时 loser 让位（跨通道，在分流前生效）──
	//
	// 对账 TS 的 A4 互斥对段。**注意**：TS 的 winner 判定**并入 pendingWatch**
	// （带 observe 的 winner 在挂起观察窗内不进竞争池 all，只扫 all 会让 loser
	// 照常送达）。**本移植未含挂起观察**，故只扫 all——这是已知偏差，见注释。
	for _, pair := range mutexPairs {
		winnerPresent := false
		for _, e := range all {
			if e.Key == pair.winner {
				winnerPresent = true
				break
			}
		}
		if !winnerPresent {
			continue
		}
		var dropped []string
		kept := make([]AdvisoryEntry, 0, len(all))
		for _, e := range all {
			if e.Key == pair.loser {
				dropped = append(dropped, e.Key)
			} else {
				kept = append(kept, e)
			}
		}
		if len(dropped) > 0 {
			b.recordDropped(dropped)
			all = kept
		}
	}

	// ── 1c. 习惯化对抗（constitutional 豁免）──
	//
	// 对账 TS 的 P1b 习惯化对抗段。**两级反应**（TS 注释）：
	//   streak >= 2 → 升级措辞：在条目前标注「已连续 N 次未见执行」——
	//     被忽略的事实本身是新信息，比原文重复更能穿透注意力习惯化。
	//   streak >= 3 → 有界静音：连续无效的提醒是纯噪音，静音 N 个渲染周期。
	//     期满放行一次（probation）：若那次被采纳则 streak 清零恢复正常；
	//     仍被忽略（streak 增长）才再次静音。**constitutional tier 永不静音**。
	if b.habituation != nil {
		// 静音计时按渲染周期流逝（**无论该 key 本轮是否被投递**）
		for k, v := range b.silenceRemaining {
			if v <= 1 {
				delete(b.silenceRemaining, k)
			} else {
				b.silenceRemaining[k] = v - 1
			}
		}

		var droppedSilenced []string
		kept := make([]AdvisoryEntry, 0, len(all))
		for _, e := range all {
			if e.Tier == TierConstitutional {
				kept = append(kept, e)
				continue
			}
			if _, silenced := b.silenceRemaining[e.Key]; silenced {
				droppedSilenced = append(droppedSilenced, e.Key)
				continue
			}
			streak := b.habituation.GetIgnoredStreak(e.Key)
			// streak 加深才触发新静音——期满 probation 放行一次，采纳则 streak 清零
			if streak >= habituationSilenceStreak && streak > b.lastSilencedStreak[e.Key] {
				if b.lastSilencedStreak == nil {
					b.lastSilencedStreak = map[string]int{}
				}
				b.lastSilencedStreak[e.Key] = streak
				if b.silenceRemaining == nil {
					b.silenceRemaining = map[string]int{}
				}
				b.silenceRemaining[e.Key] = habituationSilenceRenders
				droppedSilenced = append(droppedSilenced, e.Key)
				continue
			}
			if streak >= habituationEscalateStreak {
				// 升级措辞：「被忽略」这个事实本身是新信息
				upgraded := e
				upgraded.Content = "（此提醒已连续 " + strconv.Itoa(streak) +
					" 次未见执行——若你有意跳过请在回复中说明理由）" + e.Content
				kept = append(kept, upgraded)
				continue
			}
			kept = append(kept, e)
		}
		if len(droppedSilenced) > 0 {
			b.recordDropped(droppedSilenced)
		}
		all = kept
	}

	// ── 1d. lift 消费端：负 lift 自动静音 ──
	//
	// 对账 TS 的「Lift 消费端:负 lift 自动静音」（advisory-bus.ts:942-980）。
	//
	// **与习惯化的区别**：习惯化看的是「连续被忽略」（行为层，streak）；
	// lift 看的是**反事实基线**——投递组采纳率 减 扣留组自发完成率。
	// lift ≈ 0 意味着「没提醒模型也会做」→ 提醒是**纯噪音**。
	//
	// **静音时长更长**（10 vs 4）：lift 基于反事实证据，结论更可靠，
	// 不需要那么频繁地重新试探。
	if b.liftProvider != nil {
		// 计时流逝（无论该 key 本轮是否投递）
		for k, v := range b.liftMuteRemaining {
			if v <= 1 {
				delete(b.liftMuteRemaining, k)
				if b.liftProbation == nil {
					b.liftProbation = map[string]bool{}
				}
				b.liftProbation[k] = true // 期满 → 下次出现放行一次
			} else {
				b.liftMuteRemaining[k] = v - 1
			}
		}

		droppedByLift := map[string]bool{}
		kept := make([]AdvisoryEntry, 0, len(all))
		for _, e := range all {
			if isLiftExempt(e) {
				kept = append(kept, e)
				continue
			}
			if _, muted := b.liftMuteRemaining[e.Key]; muted {
				droppedByLift[e.Key] = true
				continue
			}
			if b.liftProbation[e.Key] {
				delete(b.liftProbation, e.Key) // probation 送达，消费一次
				kept = append(kept, e)
				continue
			}
			lift := b.liftProvider(e.Key)
			// nil = 样本不足 → 中性（**不得据此静音**）
			if lift != nil && *lift <= liftMuteThreshold {
				if b.liftMuteRemaining == nil {
					b.liftMuteRemaining = map[string]int{}
				}
				b.liftMuteRemaining[e.Key] = liftMuteRenders
				droppedByLift[e.Key] = true
				continue
			}
			kept = append(kept, e)
		}
		if len(droppedByLift) > 0 {
			keys := make([]string, 0, len(droppedByLift))
			for k := range droppedByLift {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.recordDropped(keys)
			b.ledgerLiftMuted += len(keys)
		}
		all = kept
	}

	// ── 2. 按 tier 分流 ──
	var constitutional, nonConstitutional []AdvisoryEntry
	for _, e := range all {
		if e.Tier == TierConstitutional {
			constitutional = append(constitutional, e)
		} else {
			nonConstitutional = append(nonConstitutional, e)
		}
	}

	// ── 3. 去重（同 key 保留高 priority）──
	//
	// 对账 TS：`if (!existing || entry.priority > existing.priority)`——
	// **严格大于**，故先出现的在平手时胜出（保持插入序）。
	constDeduped := dedupByKeyKeepingHigherPriority(constitutional)
	deduped := dedupByKeyKeepingHigherPriority(nonConstitutional)

	// ── 4. 类别上限 ──
	//
	// **注意顺序**：TS 先对 deduped 排序，再按排序序取前 N 个每 category——
	// 故类别上限保留的是**高优先级**的那些。
	sortedDeduped := make([]AdvisoryEntry, 0, len(deduped))
	for _, e := range deduped {
		sortedDeduped = append(sortedDeduped, e)
	}
	sortEntriesByPriority(sortedDeduped)

	catCounts := map[AdvisoryCategory]int{}
	catFiltered := make([]AdvisoryEntry, 0, len(sortedDeduped))
	for _, e := range sortedDeduped {
		if catCounts[e.Category] < maxPerCategory {
			catCounts[e.Category]++
			catFiltered = append(catFiltered, e)
		}
	}

	// ── 5. 分层取用 ──
	var operational, informational []AdvisoryEntry
	for _, e := range catFiltered {
		if e.Tier == TierInformational {
			informational = append(informational, e)
		} else {
			operational = append(operational, e)
		}
	}

	budget := advisoryBudgetForDomain(activeStarDomain)
	taken := make([]AdvisoryEntry, 0, budget)
	for _, e := range operational {
		// star_domain 类别**豁免预算**（不 break，继续取）
		if len(taken) >= budget && e.Category != CategoryStarDomain {
			break
		}
		taken = append(taken, e)
	}
	for _, e := range informational {
		if len(taken) >= budget && e.Category != CategoryStarDomain {
			break
		}
		taken = append(taken, e)
	}

	// ── 6. 合并：constitutional 在前，其余按 priority ──
	sorted := make([]AdvisoryEntry, 0, len(constDeduped)+len(taken))
	constList := make([]AdvisoryEntry, 0, len(constDeduped))
	for _, e := range constDeduped {
		constList = append(constList, e)
	}
	// constitutional 内部按 key 序输出（TS 用 Map 迭代序 = 插入序）
	sorted = append(sorted, constList...)
	sortEntriesByPriority(taken)
	sorted = append(sorted, taken...)

	// ── 7. CVM 注入预算（constitutional / immediate 豁免）──
	exempt := make([]AdvisoryEntry, 0)
	nonexempt := make([]AdvisoryEntry, 0)
	for _, e := range sorted {
		if e.Tier == TierConstitutional || e.Immediate {
			exempt = append(exempt, e)
		} else {
			nonexempt = append(nonexempt, e)
		}
	}
	if len(nonexempt) > cvmInjectionBaseBudget {
		keep := make([]AdvisoryEntry, 0, len(exempt)+cvmInjectionBaseBudget)
		keep = append(keep, exempt...)
		keep = append(keep, nonexempt[:cvmInjectionBaseBudget]...)
		sorted = keep
	}

	// ── 账本：参与竞争但没拿到渲染位的 key ──
	//
	// **注意**：只统计**非 constitutional** 的 dropped——TS 的 `deduped` 就是
	// nonConstitutional 的去重结果（constitutional 走 constDeduped 独立路径，
	// 永不 dropped）。
	renderedKeys := map[string]bool{}
	for _, e := range sorted {
		renderedKeys[e.Key] = true
	}
	var dropped []string
	for _, e := range deduped {
		if !renderedKeys[e.Key] {
			dropped = append(dropped, e.Key)
		}
	}
	b.recordDropped(dropped)
	b.ledgerRendered += len(sorted)

	// ── P1a 核销闭环：记录实际送达 ──
	for _, e := range sorted {
		b.delivered = append(b.delivered, DeliveredAdvisory{
			Key: e.Key, Category: e.Category, Tier: e.Tier, Expect: e.Expect,
		})
	}

	// ── W2 冷却记账：只记**注册 key** 的送达轮次 ──
	//
	// 对账 recordDeliveredRender 的 `if (KEY_COOLDOWN_TURNS.has(key))`。
	// 非注册 key 不记账——它们的冷却查询恒为 undefined，永远不冷却。
	for _, e := range sorted {
		if _, registered := keyCooldownTurns[e.Key]; registered {
			if b.lastDeliveredRenderByKey == nil {
				b.lastDeliveredRenderByKey = map[string]int{}
			}
			b.lastDeliveredRenderByKey[e.Key] = b.renderEpoch
		}
	}

	if len(sorted) == 0 {
		b.entries = nil
		b.alive = nil
		return ""
	}

	// ── 8. 渲染（priority 用条目自身值，toFixed(2)）──
	lines := make([]string, 0, len(sorted))
	for _, e := range sorted {
		lines = append(lines,
			`  <entry key="`+escapeXML(e.Key)+`" priority="`+formatPriority(e.Priority)+
				`" category="`+string(e.Category)+`">`+escapeXML(e.Content)+`</entry>`)
	}

	// ── TTL 递减：> 1 的条目保留到 alive ──
	b.alive = nil
	for _, e := range sorted {
		ttl := e.TTL
		if ttl == 0 {
			ttl = 1
		}
		if ttl > 1 {
			next := e
			next.TTL = ttl - 1
			b.alive = append(b.alive, next)
		}
	}
	b.entries = nil

	return "<星域-advisory>\n" + strings.Join(lines, "\n") + "\n</星域-advisory>"
}

// Reset 清空所有状态。
func (b *AdvisoryBus) Reset() {
	b.entries = nil
	b.alive = nil
	b.ledgerSubmitted = 0
	b.ledgerRendered = 0
	b.ledgerDropped = 0
	b.ledgerDroppedKeys = nil
	b.delivered = nil
	b.renderEpoch = 0
	// **必须清冷却表**——否则 Reset 后注册 key 仍带着旧送达轮次，
	// 且 renderEpoch 归零会让 `renderEpoch - last` 变成负数（永远 < cooldown），
	// 该 key 被永久静默。对账 TS reset() 的 lastDeliveredRenderByKey.clear()。
	b.lastDeliveredRenderByKey = nil
	// 习惯化状态也要清——否则 Reset 后旧静音/streak 记录残留
	b.silenceRemaining = nil
	b.lastSilencedStreak = nil
	// lift 静音状态也要清（否则 Reset 后旧静音残留）
	b.liftMuteRemaining = nil
	b.liftProbation = nil
	b.ledgerLiftMuted = 0
}

// recordDropped 记 dropped 账本（去重 key，封顶）。
func (b *AdvisoryBus) recordDropped(keys []string) {
	if len(keys) == 0 {
		return
	}
	b.ledgerDropped += len(keys)
	for _, k := range keys {
		b.ledgerDroppedKeys = append(b.ledgerDroppedKeys, k)
	}
	if len(b.ledgerDroppedKeys) > ledgerDroppedKeysCap {
		b.ledgerDroppedKeys = b.ledgerDroppedKeys[len(b.ledgerDroppedKeys)-ledgerDroppedKeysCap:]
	}
}

// dedupByKeyKeepingHigherPriority 按 key 去重，保留高 priority。
//
// 对账 TS 的 Map 去重：`if (!existing || entry.priority > existing.priority)`。
// **严格大于**——平手时先出现的胜出。
//
// **返回有序结果**（按首次出现序）——TS 的 Map 保插入序，后续排序依赖它。
func dedupByKeyKeepingHigherPriority(entries []AdvisoryEntry) []AdvisoryEntry {
	seen := map[string]int{} // key → 在 out 中的下标
	out := make([]AdvisoryEntry, 0, len(entries))
	for _, e := range entries {
		idx, ok := seen[e.Key]
		if !ok {
			seen[e.Key] = len(out)
			out = append(out, e)
			continue
		}
		if e.Priority > out[idx].Priority {
			out[idx] = e
		}
	}
	return out
}

// sortEntriesByPriority 按 priority 降序排序。
//
// **稳定性**：用 sort.SliceStable——TS 的 Array.sort 在现代 V8 里是稳定的，
// 故同 priority 条目保持插入序（oracle 的 same_priority 用例锁住）。
func sortEntriesByPriority(entries []AdvisoryEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Priority > entries[j].Priority
	})
}

// formatPriority 把 priority 格式化为两位小数（对账 TS 的 toFixed(2)）。
func formatPriority(p float64) string {
	return toFixed2(p)
}

// toFixed2 实现 JS 的 Number.prototype.toFixed(2) 语义。
//
// **实测结论**（2026-09-19）：`strconv.FormatFloat(v, 'f', 2, 64)` 与 JS 的
// `toFixed(2)` 逐位一致——覆盖 0.555 / 1.005 / 2.675 / 0.615 / 0.145 / 1.255
// 等边界值全部吻合（12/12）。
//
// **曾走过的弯路（记录以免重蹈）**：首版自写「放大 100 倍 + 远离零舍入」，
// 注释里断言「Go 的 %.2f 用 banker's rounding 会与 JS 分歧」。**实测推翻**：
// 分歧确实存在，但方向相反——自写实现在 4/6 个边界值上错（2.675 → 2.68，
// JS 给 2.67），而 Go 的 `%.2f` 与 `strconv` 都对。
//
// 根因：JS 的 toFixed 与 Go 的格式化都按**浮点二进制实际值**舍入，而非十进制
// 直觉（0.555 的实际二进制值略小于 0.555，故舍向 0.56 是"恰好"；2.675 的实际
// 值略小于 2.675，故给 2.67）。自写实现先做 `v*100` 引入了额外精度损失，
// 反而偏离了两者的共同基准。
//
// **教训**：跨语言数值语义不要凭直觉断言——先写探针实测再落实现。
func toFixed2(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64)
}

// escapeXML 转义 XML 特殊字符。
//
// 对账 TS 的 escapeXml。**顺序重要**：先转 `&` 再转其他——否则已转义的
// 实体（如 `&amp;`）里的 `&` 会被二次转义。
func escapeXML(text string) string {
	s := strings.ReplaceAll(text, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// dedupStrings 去重（保序）——对账 TS 的 `[...new Set(arr)]`。
func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
