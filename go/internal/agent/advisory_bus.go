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

	// delivered 是 render 实际送达的条目（供 readback 追踪采纳）。
	delivered []DeliveredAdvisory

	// renderEpoch 是会话内单调渲染序号。
	//
	// **为什么不用 turn**：render(turn) 的 turn 是 run 内局部序号，每个 run
	// 都从 0 重置——用它做冷却会导致固定在 turn=12 触发的 key 永久静默。
	renderEpoch int
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
