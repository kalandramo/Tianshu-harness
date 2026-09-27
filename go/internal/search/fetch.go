package search

import "context"

// fetch.go —— 搜索响应的 body 大小上限（第九十七刀 · W3）。
//
// 对账 TS `src/tools/web-search/bounded-fetch.ts`（54 行）。

// DefaultMaxSearchBytes 是搜索响应 body 的默认上限。
//
// 对账 TS `DEFAULT_MAX_SEARCH_BYTES = 8 * 1024 * 1024`。
//
// # 为什么需要
//
// 搜索响应（HTML SERP 或 JSON）都很小；超过此上限的几乎必然是恶意或畸形的，
// **绝不能无上限缓冲进堆**。对账 TS 注释。
const DefaultMaxSearchBytes = 8 * 1024 * 1024

// LimitedFetch 包装一个 Fetch，使其响应 body 受大小限制。
//
// 对账 TS `boundedSearchFetch`：后端直接调 `response.text()` / `response.json()`，
// 故本包装在**返回前**就把 body 读完（读到上限即中止并报错），再交出背靠
// 已缓冲字节的新响应。status 保留（让 `OK()` 与 content-type 检查行为一致）。
//
// **与 TS 的差异**：TS 用流式 reader 边读边计，超限时 `reader.cancel()`。
// Go 侧 `Fetch` 契约已是「返回已读入内存的 Response」，故本包装只能**事后**检查
// 长度——即在 `Fetch` 实现内部就应带上限。此处提供统一的检查点与错误文案，
// 供 `HTTPFetch` 与测试桩复用（保持错误语义一致）。
func LimitedFetch(inner Fetch, maxBytes int) Fetch {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSearchBytes
	}
	return func(ctx context.Context, req *Request) (*Response, error) {
		res, err := inner(ctx, req)
		if err != nil {
			return nil, err
		}
		if res == nil {
			return res, nil
		}
		if len(res.Body) > maxBytes {
			return nil, &BodyTooLargeError{MaxBytes: maxBytes, Actual: len(res.Body)}
		}
		return res, nil
	}
}

// BodyTooLargeError 报告响应超过大小上限。
//
// 文案对账 TS：`search response body exceeded ${maxBytes}-byte cap`。
type BodyTooLargeError struct {
	MaxBytes int
	Actual   int
}

func (e *BodyTooLargeError) Error() string {
	return "search response body exceeded " + itoa(e.MaxBytes) + "-byte cap"
}

// itoa 避免为单个格式化引 strconv（保持依赖最小）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
