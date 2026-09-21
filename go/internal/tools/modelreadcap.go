package tools

// modelreadcap.go —— 单次工具调用的输出字符预算。
//
// 对账 TS `src/tools/model-read-cap.ts` 的 `computeModelReadCap`。
//
// # 为什么需要它
//
// read_file 与 grep 历史上无论窗口多大都只回 8000 字符给模型。在 200K 窗口上
// 那是 ~4% 容量；1M 窗口上不足 1%。模型**无声地**只看到任何非平凡源文件的
// 头尾切片、中间被省略——这让代码推理不可靠，且在很多情况下与「文件很短」
// 无法区分。
//
// 本模块按**上下文窗口**与**提供商的缓存策略**算出单次调用的字符预算。

import "github.com/kalandramo/tianshu/go/internal/compact"

// ModelReadCap 是单次工具调用的字符预算。
//
// 对账 TS `ModelReadCap`。
type ModelReadCap struct {
	// MaxChars 是截断后保留的总字符数。
	MaxChars int
	// HeadChars 是保留在**头部**的字符数。
	HeadChars int
	// TailChars 是保留在**尾部**的字符数。
	TailChars int
}

// DefaultModelReadCap 是遗留地板值——对账历史的 8000 / 4000 / 2000 三分。
//
// 对账 TS `DEFAULT_MODEL_READ_CAP`。
var DefaultModelReadCap = ModelReadCap{MaxChars: 8000, HeadChars: 4000, TailChars: 2000}

// absoluteMaxChars 是硬上限：超过 120K 字符的内容应走 artifact store。
//
// 对账 TS `ABSOLUTE_MAX_CHARS`。**从 200K 降到 120K（B1）**：1M 窗口上一次
// 200K 的 read_file 会一次性吃掉 ~50K token（~5%）。120K 既够大源文件
// （loop.ts ~62K），又能封住极端值。
const absoluteMaxChars = 120_000

// charsPerToken 对账 TS 的 `CHARS_PER_TOKEN`。
const charsPerToken = 4

// tokenFractionPerCall 是单次工具结果可占用窗口的比例。
//
// 对账 TS `tokenFractionPerCall`。**断点与 `pruneThresholds` 的窗口档位对齐**，
// 让读上限与 prune/豁免逻辑保持coherent。
func tokenFractionPerCall(contextWindow int) float64 {
	if contextWindow >= 500_000 {
		return 0.05 // ≥500K：5%——空间充裕
	}
	if contextWindow >= 200_000 {
		return 0.03 // 200K–500K：3%
	}
	return 0.02 // <200K：2%
}

// strategyMultiplier 是策略系数。
//
// 对账 TS `STRATEGY_MULTIPLIER`。**cache-preserving 的提供商可以一次多送**——
// 因为那里的压缩更贵，与其让模型稍后重读，不如一次给全貌。
var strategyMultiplier = map[compact.CompactProviderStrategy]float64{
	compact.StrategyCachePreserving: 1.3,
	compact.StrategyBalanced:        1.0,
	compact.StrategyAggressive:      0.65,
}

// ModelReadCapInput 是 `ComputeModelReadCap` 的输入。
//
// 对账 TS `ModelReadCapInput`。
type ModelReadCapInput struct {
	// ContextWindow 是上下文窗口（<=0 表示未知）。
	ContextWindow int
	// ProviderProfile 是提供商切片（nil = 无 profile，走 balanced）。
	ProviderProfile *compact.CompactRatioProfile
}

// ComputeModelReadCap 计算单次工具调用的读上限。
//
// 对账 TS `computeModelReadCap`。**三条边界**（对账 TS 注释）：
//
//   - 无 contextWindow → 返回 `DefaultModelReadCap`（未接配置的调用方的
//     向后兼容路径）
//   - **永不低于 8000**——绝不比遗留行为更紧
//   - 封顶 `absoluteMaxChars`(120K)——超过则调用方应依赖 artifact store
//
// head/tail 按总上限的 **60% / 30%** 切分，留 10% 给截断标记行。
func ComputeModelReadCap(input ModelReadCapInput) ModelReadCap {
	if input.ContextWindow <= 0 {
		return DefaultModelReadCap
	}

	strategy := compact.CompactProviderStrategyFor(input.ProviderProfile)
	multiplier := strategyMultiplier[strategy]

	fraction := tokenFractionPerCall(input.ContextWindow)
	// 对账 TS 的 Math.floor。
	computed := int(float64(input.ContextWindow) * fraction * charsPerToken * multiplier)

	maxChars := computed
	if maxChars < DefaultModelReadCap.MaxChars {
		maxChars = DefaultModelReadCap.MaxChars
	}
	if maxChars > absoluteMaxChars {
		maxChars = absoluteMaxChars
	}

	return ModelReadCap{
		MaxChars:  maxChars,
		HeadChars: int(float64(maxChars) * 0.6),
		TailChars: int(float64(maxChars) * 0.3),
	}
}
