package compact

import (
	"math"
	"sort"
)

// PressureResult 是一次压力检查的结果。
//
// 对账 TS 的 `PressureResult`——**八个字段 + 可选的 suggestion / pressureRelative**。
type PressureResult struct {
	Tier          CompactTier
	ShouldCompact bool
	Thrashing     bool
	FastGrowth    bool
	// Suggestion 目前只有 'task_decomposition' 一种取值（thrashing 且需压缩时）。
	Suggestion string
	Ratio      float64
	GrowthRate float64
	// CvmOverheadRatio 是 CVM 注入占上下文的比例（0-1）。
	CvmOverheadRatio float64
	// ShouldThrottleCvm 表示 CVM 是否该节流注入以降低开销。
	ShouldThrottleCvm bool
	// PressureRelative 是相对压力——当前 ratio 超出近期基线（历史 p90）的幅度，
	// log2 压缩到 0-1：持平基线为 0，涨到基线 2 倍为 1.0。
	//
	// **tokenHistory < 5 条时为 nil**（对账 TS 的 `undefined`）。
	// 与绝对占用互补，**不可单独替代它**——消费侧取两者较大。
	PressureRelative *float64
}

// 压力检查常量（对账 TS 同名常量）。
const (
	// fastGrowthThreshold 是判定「快速增长」的最小 ratio 增量。
	fastGrowthThreshold = 0.15
	// cvmOverheadThreshold 是 CVM 节流阈值（注入超上下文 5% 即节流）。
	cvmOverheadThreshold = 0.05
	// cvmOverheadCeiling 是 CVM 硬天花板（8%——跳过所有非必需注入）。
	cvmOverheadCeiling = 0.08
	// tokenHistoryCap 是 token 历史保留条数。
	tokenHistoryCap = 20
	// compactionTurnsCap 是压缩轮次记录保留条数。
	compactionTurnsCap = 10
	// thrashingWindow 是 thrashing 检测的回看窗口（轮）。
	thrashingWindow = 4
	// thrashingMinCompactions 是判定 thrashing 的最小压缩次数。
	thrashingMinCompactions = 3
	// relativePressureMinHistory 是计算相对压力的最少历史条数。
	relativePressureMinHistory = 5
	// relativePressureFloor 是 p90 的下限（防除零）。
	relativePressureFloor = 0.01
)

// CvmInjectionSource 是 CVM 注入的记账来源。
//
// 对账 TS 的 `CvmInjectionSource`。**每个注入字节只在一个出口记账**
// （不重复计数）。
type CvmInjectionSource string

const (
	// SourceProjection 是动态 appendix 块（由 PromptEngine 在写进
	// <context-update> 时记账——未变的块、复用缓存 appendix 的工具轮、
	// Top-K 淘汰都是免费的）。
	SourceProjection CvmInjectionSource = "projection"
	// SourceEphemeral 是临时块。
	SourceEphemeral CvmInjectionSource = "ephemeral"
	// SourceToolContext 是工具上下文块。
	SourceToolContext CvmInjectionSource = "tool-context"
	// SourceAdvisoryAppendix 是 advisory appendix 块。
	SourceAdvisoryAppendix CvmInjectionSource = "advisory-appendix"
	// SourceSystemReminder 是 bus 排空的 SR（K1 append-only：每次 append 记一次）。
	SourceSystemReminder CvmInjectionSource = "system-reminder"
	// SourceRuntimePayload 是 runtime hook 的 injectUserMessage 载荷。
	SourceRuntimePayload CvmInjectionSource = "runtime-payload"
	// SourceControlAppendix 是控制面 appendix 块。
	SourceControlAppendix CvmInjectionSource = "control-appendix"
)

// tokenSample 是一次 token 采样（对账 `{ turn, tokens }`）。
type tokenSample struct {
	Turn   int
	Tokens int
}

// PressureMonitor 监控上下文压力并给出压缩建议。
//
// 对账 TS 的 `PressureMonitor`。
type PressureMonitor struct {
	contextWindow int

	compactionTurns []int
	tokenHistory    []tokenSample
	// cvmTokenAccumulator 是本会话累计的 CVM 注入 token 估算。
	//
	// **语义是「当前历史里驻留的 CVM 字节」**——由 ResetCvmOverhead 在历史
	// 重写时归零。不要在别处加独立的复位调用，两套复位会漂移。
	cvmTokenAccumulator int
	// cvmBySource 是分来源累计（不变量：各来源之和 == cvmTokenAccumulator）。
	cvmBySource map[CvmInjectionSource]int
}

// NewPressureMonitor 构造监控器。
func NewPressureMonitor(contextWindow int) *PressureMonitor {
	return &PressureMonitor{
		contextWindow: contextWindow,
		cvmBySource:   map[CvmInjectionSource]int{},
	}
}

// Check 执行一次压力检查。
//
// 对账 `check(estimatedTokens, currentTurn)`。
func (m *PressureMonitor) Check(estimatedTokens, currentTurn int) PressureResult {
	ratio := 1.0
	if m.contextWindow > 0 {
		ratio = float64(estimatedTokens) / float64(m.contextWindow)
	}
	tier := m.tierFor(ratio)
	thrashing := m.detectThrashing(currentTurn)

	// ── 增长率：与上次检查的 ratio 差 ──
	prevRatio := ratio
	if n := len(m.tokenHistory); n > 0 {
		prevRatio = float64(m.tokenHistory[n-1].Tokens) / float64(m.contextWindow)
	}
	growthRate := ratio - prevRatio
	fastGrowth := growthRate >= fastGrowthThreshold

	// ── 相对压力：当前 ratio 超出近期基线（历史 p90）的幅度 ──
	//
	// **不能用 min(1, ratio / p90)**（TS 注释原文）：上下文单调增长时当前 ratio
	// 几乎必然是尾部 20 轮的最大值、p90 约等于次大值，比值恒 ≥1 而被钉死在 1.0。
	// 该写法上线后实测 901 轮里 662 轮（73.5%）pressure 恰为 0.50，这一维不再
	// 携带信息。
	//
	// 改为对「超出倍数」取 log2：持平基线记 0，涨到基线的 2 倍记 1.0。
	var pressureRelative *float64
	historyRatios := make([]float64, 0, len(m.tokenHistory))
	for _, h := range m.tokenHistory {
		historyRatios = append(historyRatios, float64(h.Tokens)/float64(m.contextWindow))
	}
	if len(historyRatios) >= relativePressureMinHistory {
		baseline := math.Max(p90(historyRatios), relativePressureFloor)
		excess := math.Max(ratio/baseline, 1)
		v := math.Max(0, math.Min(1, math.Log2(excess)))
		pressureRelative = &v
	}

	// 记录供下次比较（保留尾部 20 条）
	m.tokenHistory = append(m.tokenHistory, tokenSample{Turn: currentTurn, Tokens: estimatedTokens})
	if len(m.tokenHistory) > tokenHistoryCap {
		m.tokenHistory = m.tokenHistory[len(m.tokenHistory)-tokenHistoryCap:]
	}

	// ── CVM 开销 ──
	cvmOverheadRatio := 0.0
	if m.contextWindow > 0 {
		cvmOverheadRatio = float64(m.cvmTokenAccumulator) / float64(m.contextWindow)
	}

	shouldCompact := tier > 0
	res := PressureResult{
		Tier:              tier,
		ShouldCompact:     shouldCompact,
		Thrashing:         thrashing,
		FastGrowth:        fastGrowth,
		Ratio:             ratio,
		GrowthRate:        growthRate,
		CvmOverheadRatio:  cvmOverheadRatio,
		ShouldThrottleCvm: cvmOverheadRatio >= cvmOverheadThreshold,
		PressureRelative:  pressureRelative,
	}
	if thrashing && shouldCompact {
		res.Suggestion = "task_decomposition"
	}
	return res
}

// tierFor 是内部档位判定。
//
// **注意**：`PressureMonitor` 走的是**不带 provider profile / 精度天花板**的
// 简化路径（对账 TS 的 `tierForRatio(ratio)` 单参调用）——即 balanced 基准比值。
// 带 profile 的完整判定见 `TierForRatio`。
func (m *PressureMonitor) tierFor(ratio float64) CompactTier {
	return TierForRatio(ratio, nil, nil, nil)
}

// RecordCvmInjection 记录一次 CVM 注入的 token 估算。
//
// 对账 `recordCvmInjection`。**计费口径 = 真实上线字节**。
func (m *PressureMonitor) RecordCvmInjection(estimatedTokens int, source CvmInjectionSource) {
	m.cvmTokenAccumulator += estimatedTokens
	m.cvmBySource[source] += estimatedTokens
}

// CvmInjectionBySource 返回分来源的累计注入 token（遥测拆解用）。
func (m *PressureMonitor) CvmInjectionBySource() map[CvmInjectionSource]int {
	out := make(map[CvmInjectionSource]int, len(m.cvmBySource))
	for k, v := range m.cvmBySource {
		out[k] = v
	}
	return out
}

// ResetCvmOverhead 归零 CVM 开销计数（如 checkpoint-resume 后）。
func (m *PressureMonitor) ResetCvmOverhead() {
	m.cvmTokenAccumulator = 0
	m.cvmBySource = map[CvmInjectionSource]int{}
}

// CvmOverheadRatio 返回 CVM 注入占上下文的比例。
func (m *PressureMonitor) CvmOverheadRatio() float64 {
	if m.contextWindow <= 0 {
		return 0
	}
	return float64(m.cvmTokenAccumulator) / float64(m.contextWindow)
}

// IsCvmThrottling 判断是否达到节流阈值（5%）。
func (m *PressureMonitor) IsCvmThrottling() bool {
	return m.CvmOverheadRatio() >= cvmOverheadThreshold
}

// IsCvmThrottlingCeiling 判断是否达到硬天花板（8%）。
func (m *PressureMonitor) IsCvmThrottlingCeiling() bool {
	return m.CvmOverheadRatio() >= cvmOverheadCeiling
}

// RecordCompaction 记录一次压缩发生的轮次。
func (m *PressureMonitor) RecordCompaction(turn int) {
	m.compactionTurns = append(m.compactionTurns, turn)
	if len(m.compactionTurns) > compactionTurnsCap {
		m.compactionTurns = m.compactionTurns[len(m.compactionTurns)-compactionTurnsCap:]
	}
}

// CompactionTurns 返回压缩轮次记录（副本）。
func (m *PressureMonitor) CompactionTurns() []int {
	out := make([]int, len(m.compactionTurns))
	copy(out, m.compactionTurns)
	return out
}

// detectThrashing 判断是否在抖动（近 4 轮内压缩 ≥ 3 次）。
//
// 对账 `detectThrashing`：`compactionTurns.filter(turn => currentTurn - turn <= 4).length >= 3`。
func (m *PressureMonitor) detectThrashing(currentTurn int) bool {
	n := 0
	for _, t := range m.compactionTurns {
		if currentTurn-t <= thrashingWindow {
			n++
		}
	}
	return n >= thrashingMinCompactions
}

// p90 返回非空数组的 90 分位。
//
// 对账 TS 的 `p90`：先升序排序，取 `floor(len * 0.9)` 处（越界则取末位）。
//
// **注意不是插值分位**——TS 用的是「排序后按下标取」，Go 侧必须一致。
func p90(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	idx := int(math.Floor(float64(len(sorted)) * 0.9))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
