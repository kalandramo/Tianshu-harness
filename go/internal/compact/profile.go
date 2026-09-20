package compact

// CompactionWindowBand 是上下文窗口档位。
//
// 对账 `CompactionWindowBand`。只影响 **reclaim 地板**（大窗口要求回收更多
// 才值得重建），不影响压缩阶梯。
type CompactionWindowBand string

const (
	BandSmall  CompactionWindowBand = "small"
	BandMedium CompactionWindowBand = "medium"
	BandLarge  CompactionWindowBand = "large"
)

// WindowBandFor 由上下文窗口推导档位。
//
// 对账 `windowBandFor`：
//
//	>= 500_000 → large
//	>= 200_000 → medium
//	其余        → small
func WindowBandFor(contextWindow int) CompactionWindowBand {
	if contextWindow >= 500_000 {
		return BandLarge
	}
	if contextWindow >= 200_000 {
		return BandMedium
	}
	return BandSmall
}

// CompactionProfileInput 是 deriveCompactionProfile 的输入。
//
// 对账 `CompactionProfileInput`。可选字段用指针表达「未设置」
// （TS 用 `undefined` + 条件展开，Go 的零值无法区分）。
type CompactionProfileInput struct {
	ContextWindow             int
	Billing                   CompactionBilling
	Cache                     CompactionCache
	CacheWritePricePerMillion *float64
	CacheReadPricePerMillion  *float64
	OutputReserveTokens       *int
}

// DeriveCompactionProfile 派生完整 profile（含 reclaim 地板）。
//
// 对账 `deriveCompactionProfile`。地板矩阵（TS 注释的 plan §3.2 第一版）：
//
//	| profile                                 | minReclaimTokens              | ratio |
//	|-----------------------------------------|-------------------------------|-------|
//	| small/medium + per-token + exact-prefix | max(8192, floor(window×0.03)) | 0.03  |
//	| large + per-token + exact-prefix        | max(32768, floor(window×0.05))| 0.05  |
//	| subscription 或 cache none/partial      | max(4096, floor(window×0.01)) | 0.01  |
//
// **为什么按这两轴分档**（TS 注释原文）：per-token provider 为 cache-miss
// 重建付真金白银，故重写必须回收够多才划算；订阅制只付延迟。而持久精确前缀
// 缓存（DeepSeek/GLM/MiMo）会被任何历史重写击碎——回收不够就是纯亏。
func DeriveCompactionProfile(input CompactionProfileInput) CompactionProfile {
	windowBand := WindowBandFor(input.ContextWindow)
	protectPaidPrefix := input.Billing == BillingPerToken && input.Cache == CompactionCacheExactPrefix

	var minReclaimTokens int
	var minReclaimRatio float64
	switch {
	case protectPaidPrefix && windowBand == BandLarge:
		minReclaimTokens = maxInt(32_768, floorMul(input.ContextWindow, 0.05))
		minReclaimRatio = 0.05
	case protectPaidPrefix:
		minReclaimTokens = maxInt(8_192, floorMul(input.ContextWindow, 0.03))
		minReclaimRatio = 0.03
	default:
		minReclaimTokens = maxInt(4_096, floorMul(input.ContextWindow, 0.01))
		minReclaimRatio = 0.01
	}

	return CompactionProfile{
		WindowBand:                windowBand,
		ContextWindow:             input.ContextWindow,
		EffectiveInputBudget:      input.ContextWindow,
		OutputReserveTokens:       input.OutputReserveTokens,
		Billing:                   input.Billing,
		Cache:                     input.Cache,
		CacheWritePricePerMillion: input.CacheWritePricePerMillion,
		CacheReadPricePerMillion:  input.CacheReadPricePerMillion,
		MinReclaimTokens:          minReclaimTokens,
		MinReclaimRatio:           minReclaimRatio,
	}
}

// floorMul 计算 floor(n * f)——对账 TS 的 `Math.floor(window * ratio)`。
//
// **不能用 int(float64(n) * f)**：浮点乘法的截断方向与 floor 在负数域不同，
// 且 0.03 这类比值无法精确表示。用整数运算避免精度问题：
// floor(n * 3 / 100) 用 n*3/100 的整数除法（Go 的整数除法向零截断，
// n 与 f 均为正时等价于 floor）。
func floorMul(n int, f float64) int {
	// 比值都是百分数（0.01/0.03/0.05），转成整数分子避免浮点。
	switch f {
	case 0.01:
		return n / 100
	case 0.03:
		return n * 3 / 100
	case 0.05:
		return n * 5 / 100
	default:
		return int(float64(n) * f)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
