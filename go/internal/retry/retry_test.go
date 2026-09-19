package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/apierr"
)

func intPtr(i int) *int     { return &i }
func i64Ptr(i int64) *int64 { return &i }

// noSleep 是零延迟等待——测试用，避免真实秒级等待拖慢套件。
func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// opts 构造测试选项（默认注入零延迟 sleep）。
func opts(maxRetries int, extra *Options) *Options {
	o := &Options{MaxTotalRetries: intPtr(maxRetries), Sleep: noSleep}
	if extra != nil {
		if extra.MaxTotalDurationMs != nil {
			o.MaxTotalDurationMs = extra.MaxTotalDurationMs
		}
		if extra.Policy != nil {
			o.Policy = extra.Policy
		}
		if extra.OnRetry != nil {
			o.OnRetry = extra.OnRetry
		}
	}
	return o
}

// 可重试错误（429）——用于驱动重试循环。
func rateLimitErr() error { return &apierr.APIError{Status: 429, Msg: "rate limited"} }

// 不可重试错误（401）。
func authErr() error { return &apierr.APIError{Status: 401, Msg: "unauthorized"} }

// 反证 A：不可重试错误必须立即抛出，绝不进入重试循环。
func TestNonRetryablePropagatesImmediately(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		return 0, authErr()
	}, opts(10, nil))

	if err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 1 {
		t.Fatalf("不可重试错误应只调用 1 次，实际 %d 次", calls)
	}
}

// 重试直到成功。
func TestRetriesUntilSuccess(t *testing.T) {
	calls := 0
	got, err := Do(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, rateLimitErr()
		}
		return 42, nil
	}, opts(5, nil))

	if err != nil {
		t.Fatalf("应最终成功：%v", err)
	}
	if got != 42 {
		t.Errorf("结果 = %d, want 42", got)
	}
	if calls != 3 {
		t.Errorf("调用次数 = %d, want 3", calls)
	}
}

// 重试上限：超过后抛出最后的错误。
func TestRetryCeiling(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		return 0, rateLimitErr()
	}, opts(2, nil))

	if err == nil {
		t.Fatal("应返回错误")
	}
	// 初始调用 1 次 + 重试 2 次 = 3
	if calls != 3 {
		t.Errorf("调用次数 = %d, want 3（初始 1 + 重试 2）", calls)
	}
}

// 反证 B：显式 MaxTotalRetries 必须能抬升分类器默认值（「调了不生效」缺陷）。
//
// rate_limit 的分类器默认 maxRetries=5；显式设 7 应生效为 7。
func TestExplicitMaxTotalRaisesClassifierDefault(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		return 0, rateLimitErr()
	}, opts(7, nil))

	if err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 8 { // 初始 1 + 重试 7
		t.Errorf("调用次数 = %d, want 8（显式 7 次重试应生效，而非被夹到分类器默认 5）", calls)
	}
}

// 类别覆盖优先于全局，但仍受显式全局夹取。
func TestCategoryOverrideWins(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		return 0, rateLimitErr()
	}, opts(10, &Options{Policy: &Policy{Overrides: map[apierr.Category]CategoryOverride{
		apierr.CatRateLimit: {MaxRetries: intPtr(2)},
	}}}))

	if err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 3 { // 初始 1 + 类别覆盖的 2
		t.Errorf("调用次数 = %d, want 3（类别覆盖 2 应优先于全局 10）", calls)
	}
}

// 反证 C：预算护栏必须在**等待前**预判，而非等睡完再检查。
//
// 场景：服务端给一个远超预算的 Retry-After。若只在循环顶部检查，
// 会先睡满那个时长才失败（911s 挂死事故）。
func TestBudgetGuardBeforeSleep(t *testing.T) {
	start := time.Now()
	_, err := Do(context.Background(), func() (int, error) {
		// 服务端指定 10 分钟等待
		return 0, &apierr.APIError{Status: 429, Msg: "rate limited", RetryAfterMs: intPtr(600_000)}
	}, &Options{MaxTotalDurationMs: i64Ptr(1000), Sleep: noSleep}) // 预算仅 1 秒

	elapsed := time.Since(start)

	var be *BudgetExhaustedError
	if !errors.As(err, &be) {
		t.Fatalf("应返回 BudgetExhaustedError，实际 %v", err)
	}
	// 关键断言：必须在预判阶段就失败，而不是睡满 10 分钟
	if elapsed > 5*time.Second {
		t.Fatalf("预算护栏未在等待前生效——耗时 %v（应远小于服务端指定的 10 分钟）", elapsed)
	}
}

// 预算耗尽是终态，不得被分类器判为可重试。
func TestBudgetExhaustedIsTerminal(t *testing.T) {
	// 用一个极小预算 + 快速失败的错误
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		time.Sleep(10 * time.Millisecond)
		return 0, rateLimitErr()
	}, &Options{MaxTotalDurationMs: i64Ptr(50), Sleep: noSleep})

	var be *BudgetExhaustedError
	if !errors.As(err, &be) {
		t.Fatalf("应返回 BudgetExhaustedError，实际 %v", err)
	}
	// 分类器会把这个错误判为 unknown/retryable——引擎必须先识别它
	if c := apierr.Classify(err); c.Retryable && c.Category == apierr.CatUnknown {
		t.Log("（预期：分类器确实会误判它为 unknown/retryable，故引擎需在分类前拦截）")
	}
}

// ctx 取消必须中断等待。
func TestContextCancelInterruptsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Do(ctx, func() (int, error) {
		return 0, rateLimitErr()
	}, &Options{MaxTotalRetries: intPtr(10)}) // 用真实 sleep：本用例验证取消能中断等待
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实际 %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("取消未及时中断等待，耗时 %v", elapsed)
	}
}

// OnRetry 回调携带正确的 attempt 序号与分类结果。
func TestOnRetryCallback(t *testing.T) {
	var infos []Info
	_, err := Do(context.Background(), func() (int, error) {
		return 0, rateLimitErr()
	}, opts(3, &Options{OnRetry: func(i Info) { infos = append(infos, i) }}))
	if err == nil {
		t.Fatal("应返回错误")
	}
	if len(infos) != 3 {
		t.Fatalf("OnRetry 应调用 3 次，实际 %d", len(infos))
	}
	for idx, info := range infos {
		wantAttempt := idx + 1
		if info.Attempt != wantAttempt {
			t.Errorf("第 %d 次回调的 Attempt = %d, want %d", idx, info.Attempt, wantAttempt)
		}
		if info.Classified.Category != apierr.CatRateLimit {
			t.Errorf("分类结果错误：%v", info.Classified.Category)
		}
	}
}

// 退避曲线：单调不减且受 maxDelay 封顶。
func TestJitteredBackoffCurve(t *testing.T) {
	const base, maxDelay = 1000, 8000
	// attempt=1 → base 1000；attempt=2 → 2000；attempt=3 → 4000；attempt=4 → 8000（封顶）
	for attempt := 1; attempt <= 6; attempt++ {
		for run := 0; run < 50; run++ {
			got := JitteredBackoff(attempt, base, maxDelay, 0.5)
			// 下界：capped（jitter ≥ 0）
			capped := base << (attempt - 1)
			if capped > maxDelay {
				capped = maxDelay
			}
			if got < capped {
				t.Fatalf("attempt=%d: %d < capped %d", attempt, got, capped)
			}
			// 上界：capped × (1 + ratio)
			upper := capped + capped/2 + 1
			if got > upper {
				t.Fatalf("attempt=%d: %d > upper %d", attempt, got, upper)
			}
		}
	}
}

// 反证 D：服务端指定的延迟不得被指数放大（只加抖动）。
func TestServerDelayNotExponentiated(t *testing.T) {
	c := apierr.Classified{
		RetryDelayMs:         2000,
		RetryDelayFromServer: true,
	}
	bo := &BackoffConfig{BaseDelayMs: 1000, MaxDelayMs: 60000, JitterRatio: f64Ptr(0.5)}

	// 无论 attempt 多大，服务端延迟都只加抖动，不做指数增长
	for attempt := 1; attempt <= 5; attempt++ {
		got := computeNextDelay(attempt, c, nil, bo)
		if got < 2000 || got > 3000 { // 2000 + 0~50%
			t.Fatalf("attempt=%d: 服务端延迟被放大为 %d（应保持在 2000~3000）", attempt, got)
		}
	}
}

// 未配置 backoff 时保持历史行为（类别固定延迟 + 抖动）。
func TestLegacyPathWithoutBackoff(t *testing.T) {
	c := apierr.Classified{RetryDelayMs: 2000}
	for run := 0; run < 30; run++ {
		got := computeNextDelay(1, c, nil, nil)
		if got < 2000 || got > 3000 {
			t.Fatalf("历史路径延迟 = %d（应在 2000~3000）", got)
		}
	}
}

// 类别无延迟时用抖动指数退避。
func TestZeroCategoryDelayUsesExponential(t *testing.T) {
	c := apierr.Classified{RetryDelayMs: 0}
	// attempt=3 → base 1000×4 = 4000
	got := computeNextDelay(3, c, nil, nil)
	if got < 4000 || got > 6000 {
		t.Fatalf("零延迟类别应用指数退避，got %d（期望 4000~6000）", got)
	}
}

// 配置了 backoff 时统一走指数曲线，基数为类别延迟。
func TestBackoffUsesCategoryDelayAsBase(t *testing.T) {
	c := apierr.Classified{RetryDelayMs: 500}
	bo := &BackoffConfig{MaxDelayMs: 60000, JitterRatio: f64Ptr(0)}
	// attempt=2 → 500×2 = 1000
	got := computeNextDelay(2, c, nil, bo)
	if got != 1000 {
		t.Errorf("got %d, want 1000（类别延迟 500 作为指数基数）", got)
	}
}

// stripImages 类别的显式全局值只允许收紧（一次性语义）。
func TestStripImagesExplicitGlobalOnlyTightens(t *testing.T) {
	// 413 有图 → image_strip, MaxRetries=1
	stripErr := &apierr.APIError{Status: 413, Msg: "too large", PayloadHadImages: boolPtr(true)}
	calls := 0
	_, err := Do(context.Background(), func() (int, error) {
		calls++
		return 0, stripErr
	}, opts(10, nil)) // 全局 10 不得放大 stripImages 的 1

	if err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 2 { // 初始 1 + strip 的 1 次
		t.Errorf("调用次数 = %d, want 2（stripImages 一次性语义不得被全局值放大）", calls)
	}
}

func boolPtr(b bool) *bool      { return &b }
func f64Ptr(f float64) *float64 { return &f }
