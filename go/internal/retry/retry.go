// Package retry 实现结构化重试引擎。
//
// 对账 src/api/retry-engine.ts。三个核心机制：
//
//  1. **抖动指数退避**：base = min(baseDelay × 2^(attempt-1), maxDelay)；
//     实际等待 = base + random(0, jitterRatio × base)。
//  2. **服务端指令优先**：Retry-After 指定的等待只加抖动、不做指数增长
//     ——等多久由服务端决定，不由曲线决定。
//  3. **预算护栏**：不仅在每个 attempt 顶部检查总时长预算，还要在**等待前**
//     预判——否则服务端给一个 30min 的 Retry-After，会先睡满 30min 才在下一轮
//     顶部发现超预算（911s 挂死事故）。
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/kalandramo/tianshu/go/internal/apierr"
)

// BudgetExhaustedError 是重试预算耗尽时抛出的终态错误。
//
// 独立类型（而非仅靠消息模式）：引擎自己的 catch 需在分类器之前识别它——
// 它的消息匹配不到任何分类器模式，会被判为 unknown/retryable，从而多烧一轮
// 退避并让用户可见的「across N attempt(s)」计数漂移。
type BudgetExhaustedError struct{ Msg string }

func (e *BudgetExhaustedError) Error() string { return e.Msg }

// BackoffConfig 是指数退避形状。
type BackoffConfig struct {
	// BaseDelayMs 是退避基数（类别自身无延迟时使用）。
	BaseDelayMs int
	// MaxDelayMs 是每次等待的上限（默认 30000）。
	MaxDelayMs int
	// JitterRatio 是抖动比例（0–2）：wait = capped + rand() × ratio × capped。
	//
	// 用指针区分「未配置」与「显式 0」——对齐 TS 的 `?? LEGACY_JITTER_RATIO`
	// 语义：只有 undefined 才回退默认，显式 0 表示「不要抖动」。
	JitterRatio *float64
}

// jitterRatioOr 返回配置的抖动比例，未配置时用历史默认。
func (b *BackoffConfig) jitterRatioOr(def float64) float64 {
	if b == nil || b.JitterRatio == nil {
		return def
	}
	return *b.JitterRatio
}

// CategoryOverride 是逐类别的重试覆盖。
type CategoryOverride struct {
	// MaxRetries 是该类别的重试上限。显式值不再被分类器默认值向下夹取
	//（显式 MaxTotalRetries 仍会封顶）。
	MaxRetries *int
	// RetryDelayMs 是该类别的等待基数（配置了 backoff 时作为指数起点）。
	RetryDelayMs *int
}

// Policy 是从提供商配置带入的重试策略。缺席 = 历史行为。
type Policy struct {
	// Backoff 一旦设置，把所有可重试类别切到可配置的指数曲线；
	// 不设置则保持历史固定延迟路径。
	Backoff   *BackoffConfig
	Overrides map[apierr.Category]CategoryOverride
}

// Options 是重试选项。
type Options struct {
	// MaxTotalRetries 是重试次数上限。nil = 由各分类器的默认值决定
	//（不注入隐式 5——全部可重试类别的默认值本就 ≤5，故默认行为不变，
	// 而显式配置不再被夹取）。
	MaxTotalRetries *int
	// MaxTotalDurationMs 是所有 attempt 的总耗时上限（默认无限制）。
	// 超出时放弃当前 attempt 并抛错，防止在无响应提供商上重试几十分钟。
	MaxTotalDurationMs *int64
	Policy             *Policy
	// OnRetry 在每次重试前回调（诊断用）。
	OnRetry func(info Info)
	// Sleep 是等待实现。nil = 真实等待（生产路径）。
	// 测试注入零延迟实现，避免用真实秒级等待拖慢套件。
	Sleep func(ctx context.Context, d time.Duration) error
}

// sleepFn 返回生效的等待实现。
func (o *Options) sleepFn() func(context.Context, time.Duration) error {
	if o != nil && o.Sleep != nil {
		return o.Sleep
	}
	return sleepCtx
}

// Info 是一次重试的诊断信息。
type Info struct {
	// Attempt 是 1-based 的重试序号（1 = 首次重试，不含初始调用）。
	Attempt     int
	Classified  apierr.Classified
	NextDelayMs int
}

const (
	defaultBackoffBaseMs = 1000
	defaultBackoffMaxMs  = 30000
	legacyJitterRatio    = 0.5
)

// JitteredBackoff 计算带全抖动的指数退避延迟。
//
//	base   = min(baseDelayMs × 2^(attempt-1), maxDelayMs)
//	jitter = rand(0, jitterRatio × base)
//	result = base + jitter
func JitteredBackoff(attempt, baseDelayMs, maxDelayMs int, jitterRatio float64) int {
	if baseDelayMs <= 0 {
		baseDelayMs = defaultBackoffBaseMs
	}
	if maxDelayMs <= 0 {
		maxDelayMs = defaultBackoffMaxMs
	}
	exp := float64(baseDelayMs) * math.Pow(2, float64(attempt-1))
	capped := math.Min(exp, float64(maxDelayMs))
	jitter := rand.Float64() * jitterRatio * capped
	return int(capped + jitter)
}

// delayWithJitter 给固定延迟加抖动。
func delayWithJitter(delayMs int, jitterRatio float64) int {
	return delayMs + int(rand.Float64()*jitterRatio*float64(delayMs))
}

// computeNextDelay 决定下次尝试前的等待时长。
//
// 三种情形，按序判定：
//
//  1. 服务端指定了等待（Retry-After）→ 固定延迟 + 抖动，**不做指数增长**
//     ——由服务端决定，不由曲线决定。
//  2. 未配置 backoff → 历史行为不变：类别固定延迟 + 50% 抖动，
//     或类别自身无延迟（retryDelayMs=0）时用抖动指数退避。
//  3. 配置了 backoff → 所有可重试类别统一走指数曲线，基数为
//     覆盖值/类别延迟（类别无则回退 backoff.BaseDelayMs），受 maxDelayMs 封顶。
func computeNextDelay(attempt int, c apierr.Classified, ov *CategoryOverride, bo *BackoffConfig) int {
	categoryDelay := c.RetryDelayMs

	if c.RetryDelayFromServer && categoryDelay > 0 {
		return delayWithJitter(categoryDelay, bo.jitterRatioOr(legacyJitterRatio))
	}

	if bo == nil {
		if categoryDelay > 0 {
			return delayWithJitter(categoryDelay, legacyJitterRatio)
		}
		return JitteredBackoff(attempt, defaultBackoffBaseMs, defaultBackoffMaxMs, legacyJitterRatio)
	}

	base := 0
	if ov != nil && ov.RetryDelayMs != nil {
		base = *ov.RetryDelayMs
	} else if categoryDelay > 0 {
		base = categoryDelay
	} else if bo.BaseDelayMs > 0 {
		base = bo.BaseDelayMs
	} else {
		base = defaultBackoffBaseMs
	}
	maxDelay := bo.MaxDelayMs
	if maxDelay <= 0 {
		maxDelay = defaultBackoffMaxMs
	}
	return JitteredBackoff(attempt, base, maxDelay, bo.jitterRatioOr(legacyJitterRatio))
}

// Do 以结构化重试执行 fn。
//
//   - 先调一次 fn()，然后最多重试 min(classified.MaxRetries, MaxTotalRetries) 次。
//   - 分类器判 !Retryable 时立即抛出。
//   - 延迟用 classified.RetryDelayMs（>0 时），否则回退抖动指数退避。
//   - 尊重 ctx 取消——等待期间取消则返回 ctx 的错误。
func Do[T any](ctx context.Context, fn func() (T, error), opts *Options) (T, error) {
	var zero T
	if opts == nil {
		opts = &Options{}
	}
	maxTotal := opts.MaxTotalRetries
	var maxDuration int64
	if opts.MaxTotalDurationMs != nil {
		maxDuration = *opts.MaxTotalDurationMs
	}
	policy := opts.Policy

	var startTime int64
	if maxDuration > 0 {
		startTime = time.Now().UnixMilli()
	}

	// attempt 是 1-based 且计的是**重试次数**（不含初始调用）
	for attempt := 0; ; attempt++ {
		// 尝试前检查取消
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		// 检查全局时长预算
		if maxDuration > 0 && time.Now().UnixMilli()-startTime > maxDuration {
			return zero, &BudgetExhaustedError{Msg: fmt.Sprintf(
				"Retry budget exhausted: total retry time exceeded %ds across %d attempt(s). "+
					"Provider may be unavailable — try again later or switch provider.",
				maxDuration/1000, attempt)}
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}

		// 引擎内部哨兵：预算耗尽为终态，在分类前抛回。
		// 交给分类器会被判 unknown/retryable（消息匹配不到模式），
		// 白烧一轮退避并让计数漂移。
		var be *BudgetExhaustedError
		if errors.As(err, &be) {
			return zero, err
		}

		classified := apierr.Classify(err)

		// 不可重试 → 立即传播
		if !classified.Retryable {
			return zero, err
		}

		// 重试上限——显式用户配置优先，分类器是兜底：
		//   1. overrides[category].MaxRetries → 该类别显式值（仍受显式 MaxTotalRetries 夹取）
		//   2. 显式 MaxTotalRetries → 直接生效（抬升分类器默认，消除「调了不生效」）
		//   3. 两者都无 → 分类器默认（历史行为）
		var ov *CategoryOverride
		if policy != nil {
			if o, ok := policy.Overrides[classified.Category]; ok {
				ov = &o
			}
		}

		var effectiveMax int
		switch {
		case ov != nil && ov.MaxRetries != nil:
			if maxTotal != nil {
				effectiveMax = min(*ov.MaxRetries, *maxTotal)
			} else {
				effectiveMax = *ov.MaxRetries
			}
		case maxTotal != nil:
			if classified.StripImages {
				effectiveMax = min(classified.MaxRetries, *maxTotal)
			} else {
				effectiveMax = *maxTotal
			}
		default:
			effectiveMax = classified.MaxRetries
		}

		// +1：attempt 从 0 开始（初始调用是 attempt 0，首次重试是 attempt 1）
		if attempt+1 > effectiveMax {
			return zero, err
		}

		// 延迟决策——服务端指令 > 配置曲线 > 历史路径
		var bo *BackoffConfig
		if policy != nil {
			bo = policy.Backoff
		}
		nextDelayMs := computeNextDelay(attempt+1, classified, ov, bo)

		if opts.OnRetry != nil {
			opts.OnRetry(Info{Attempt: attempt + 1, Classified: classified, NextDelayMs: nextDelayMs})
		}

		// 预算护栏：预算只在每轮顶部检查，故一个长于剩余预算的等待会先睡满
		// ——服务端给 30min 的 Retry-After 会在 10s 预算下挂 30min。
		// 下一轮顶部检查必然失败，故提前预判并在等待前终止。
		if maxDuration > 0 {
			elapsed := time.Now().UnixMilli() - startTime
			if elapsed+int64(nextDelayMs) > maxDuration {
				return zero, &BudgetExhaustedError{Msg: fmt.Sprintf(
					"Retry budget exhausted: total retry time would exceed %ds across %d attempt(s) "+
						"(next wait %ds > remaining %ds). Provider may be unavailable — try again later or switch provider.",
					maxDuration/1000, attempt+1, nextDelayMs/1000, (maxDuration-elapsed)/1000)}
			}
		}

		// 等待（可取消）
		if err := opts.sleepFn()(ctx, time.Duration(nextDelayMs)*time.Millisecond); err != nil {
			return zero, err
		}
	}
}

// sleepCtx 等待 d，期间 ctx 取消则立即返回其错误。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
