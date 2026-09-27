package search

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// chain.go —— 有序后端链（第九十七刀 · W3）。
//
// 对账 TS `src/tools/web-search/chain.ts`（89 行）。

// BackendError 是链遍历过程中记录的单个后端失败/空结果。
type BackendError struct {
	Backend string
	Message string
}

// OffTopicBatch 是链序第一个被判跑题的非空批次。
type OffTopicBatch struct {
	Backend string
	Results []Result
}

// ChainResult 是一次链遍历的结果。
type ChainResult struct {
	// Backend 是产出结果的后端名；全部落空时为 ""。
	Backend string
	// Results 是胜出后端的结果（落空时为空）。
	Results []Result
	// Errors 是遍历期间累积的各后端失败/空结果。
	Errors []BackendError
	// OffTopicFallback 是**低置信兜底**：链序第一个跑题批次。
	//
	// 它不参与胜出判定（Results 仍为空），只作降级返回用——避免单后端配置下
	// 把「后端降级返回泛结果」直接变成「什么都搜不到」。
	// 仅当有后端返回过跑题内容时非 nil。
	OffTopicFallback *OffTopicBatch
}

// RunBackendChain 按顺序试各后端。
//
// **首个可用且返回可用非空结果的后端胜出并短路**。
// 不可用（缺 key）静默跳过、不算错误；空结果、跑题结果、抛错都记入 Errors
// 后继续走下一个。
//
// 对账 TS `runBackendChain`：每个后端包一层 `timeoutMs` 超时。
func RunBackendChain(ctx context.Context, backends []Backend, query string, count, timeoutMs int) ChainResult {
	if ctx == nil {
		ctx = context.Background()
	}
	var errs []BackendError
	var offTopicFallback *OffTopicBatch

	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	for _, backend := range backends {
		if !backend.IsAvailable() {
			continue
		}

		results, err := searchWithTimeout(ctx, backend, query, count, timeout)
		if err != nil {
			errs = append(errs, BackendError{Backend: backend.Name(), Message: describeError(err, timeoutMs)})
			continue
		}

		if len(results) > 0 {
			if LooksOffTopic(query, results) {
				errs = append(errs, BackendError{Backend: backend.Name(), Message: OffTopicError})
				// 链序优先：只保留第一个跑题批次，后续批次不得覆盖它。
				if offTopicFallback == nil {
					offTopicFallback = &OffTopicBatch{Backend: backend.Name(), Results: results}
				}
				continue
			}
			return ChainResult{Backend: backend.Name(), Results: results, Errors: errs}
		}
		errs = append(errs, BackendError{Backend: backend.Name(), Message: NoResultsError})
	}

	return ChainResult{Results: []Result{}, Errors: errs, OffTopicFallback: offTopicFallback}
}

// searchWithTimeout 给单次后端搜索套上超时。
//
// 对账 TS：`new AbortController()` + `setTimeout(() => controller.abort(), timeoutMs)`。
func searchWithTimeout(ctx context.Context, backend Backend, query string, count int, timeout time.Duration) ([]Result, error) {
	sub, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return backend.Search(sub, query, count)
}

// describeError 把错误渲染成用户可读的失败原因。
//
// 对账 TS `describeError`：
//   - 超时（Go 的 `context.DeadlineExceeded` ↔ TS 的 `AbortError`）→ `timed out after Ns`
//   - 其余 → 错误原文；**若含网络底层原因则附上**（对账 TS 的 `fetchCauseDetail`）
func describeError(err error, timeoutMs int) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timed out after %gs", float64(timeoutMs)/1000)
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return err.Error()
}
