// Package apierr 把原始 API 异常映射为结构化恢复策略。
//
// 对账 src/api/error-classifier.ts。纯函数、无副作用。
//
// 分类优先级：HTTP 状态码 → 错误名 → 消息模式 → 兜底 unknown。
package apierr

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Category 是错误类别。
type Category string

const (
	CatRateLimit           Category = "rate_limit"
	CatOverloaded          Category = "overloaded"
	CatServerError         Category = "server_error"
	CatTimeout             Category = "timeout"
	CatAuthError           Category = "auth_error"
	CatClientError         Category = "client_error"
	CatContextOverflow     Category = "context_overflow"
	CatImageStrip          Category = "image_strip"
	CatStreamParse         Category = "stream_parse"
	CatReasoningRepetition Category = "reasoning_repetition"
	CatUnknown             Category = "unknown"
)

// AllCategories 是全部类别的运行时清单（与 TS 的 ERROR_CATEGORIES 对应）。
//
// 单一真源：配置侧的 retry.overrides 键枚举以此为准，字段拼错在加载期就报错，
// 而非静默失效。
var AllCategories = []Category{
	CatRateLimit, CatOverloaded, CatServerError, CatTimeout, CatAuthError,
	CatClientError, CatContextOverflow, CatImageStrip, CatStreamParse,
	CatReasoningRepetition, CatUnknown,
}

// Classified 是一条结构化恢复策略。
type Classified struct {
	Retryable       bool
	RetryDelayMs    int
	ShouldReconnect bool
	Category        Category
	UserMessage     string
	MaxRetries      int
	// StripImages 为真时，重试引擎应在重试前剥离消息中的图片内容。
	// 不消耗重试预算（只剥离一次）。
	StripImages bool
	// RetryDelayFromServer 为真时 RetryDelayMs 来自服务端 Retry-After 头，
	// 而非类别默认值——重试引擎对此类延迟只加抖动、不做指数增长
	//（等待时长由服务端决定）。
	RetryDelayFromServer bool
}

// APIError 是携带 HTTP 状态的错误。
type APIError struct {
	Status int
	Msg    string
	// PayloadHadImages 是 API 客户端在 413 上打的标记：它知道自己发出了什么。
	// 用来区分「图片过重」与「纯上下文超限」——两者在 wire 层长得一样。
	PayloadHadImages *bool
	// RetryAfterMs 是服务端指定的等待时长。
	RetryAfterMs *int
	// NonSSE 标记「端点返回了非 SSE 的 200 响应」（端点答了，但不是流）。
	NonSSE bool
	// Cause 是底层网络错误（对应 TS 的 err.cause 链）。
	Cause error
}

func (e *APIError) Error() string { return e.Msg }

// ReasoningRepetitionError 表示检测到推理短句持续重复。
type ReasoningRepetitionError struct{ Msg string }

func (e *ReasoningRepetitionError) Error() string { return e.Msg }

// NewReasoningRepetition 构造推理重复错误。
func NewReasoningRepetition(msg string) *ReasoningRepetitionError {
	return &ReasoningRepetitionError{Msg: msg}
}

// ── 模式（与 TS 的正则逐条对应）──

var (
	reStatusCode  = regexp.MustCompile(`\((\d{3})\)`)
	reImageErr    = regexp.MustCompile(`(?i)could not process image|image processing|unsupported image|invalid image format`)
	reNetworkErr  = regexp.MustCompile(`(?i)ECONNRESET|EPIPE|ECONNREFUSED|ENOTFOUND|EAI_AGAIN|EHOSTUNREACH|ENETUNREACH|ECONNABORTED|UND_ERR_CONNECT|UND_ERR_SOCKET|other side closed|fetch failed`)
	reTimeout     = regexp.MustCompile(`(?i)timeout|timed?\s*out`)
	reOverloaded  = regexp.MustCompile(`(?i)service[_\s-]*unavailable|too\s+busy|temporarily\s+unavailable|server\s+overload|overloaded|capacity`)
	reEmptyStream = regexp.MustCompile(`(?i)empty_stream|upstream.*stream.*closed|stream.*closed.*before.*payload`)
	reContextOvf  = regexp.MustCompile(`(?i)prompt is too long|context_length_exceeded|max.*token|context.*overflow`)
	reStreamParse = regexp.MustCompile(`(?i)stream.*parse|parse.*stream|invalid.*sse|unexpected.*event`)
	reAbort       = regexp.MustCompile(`(?i)^context canceled$|^context deadline exceeded$|aborted`)
)

// fetchCauseDetail 提取错误 cause 链的可读细节。
//
// 网络库常把真实失败埋在 cause 里（"fetch failed" 本身匹配不到任何模式），
// 不解包的话分类与用户可见错误都只能看到无用的顶层消息。
//
// 返回 ` ← ` 连接的 cause 链，无 cause 时返回空串。
func fetchCauseDetail(err error) string {
	var parts []string
	cur := err
	for depth := 0; cur != nil && depth < 5; depth++ {
		unwrapped := errors.Unwrap(cur)
		if unwrapped == nil {
			break
		}
		// AggregateError 等价物：Go 用 errors.Join，其 Unwrap 返回 []error。
		if joined, ok := unwrapped.(interface{ Unwrap() []error }); ok {
			for i, sub := range joined.Unwrap() {
				if i >= 3 {
					break
				}
				parts = append(parts, sub.Error())
			}
			break
		}
		parts = append(parts, unwrapped.Error())
		cur = unwrapped
	}
	// 去重（保序）
	seen := map[string]bool{}
	var uniq []string
	for _, p := range parts {
		if p != "" && !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	return strings.Join(uniq, " ← ")
}

// extractStatus 从错误中提取 HTTP 状态码。
func extractStatus(err error) (int, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Status != 0 {
			return ae.Status, true
		}
	}
	// 消息里的 "(429)" 形式
	msg := err.Error()
	if m := reStatusCode.FindStringSubmatch(msg); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	return 0, false
}

// Classify 把错误分类为结构化恢复策略。
func Classify(err error) Classified {
	if err == nil {
		return Classified{
			Retryable: false, Category: CatUnknown,
			UserMessage: "unknown error", MaxRetries: 0,
		}
	}

	// 推理重复：终态，不重试
	var rre *ReasoningRepetitionError
	if errors.As(err, &rre) {
		return Classified{
			Retryable: false, Category: CatReasoningRepetition,
			UserMessage: rre.Msg, MaxRetries: 0,
		}
	}

	var ae *APIError

	// 非 SSE 200：端点答了但不是流——重试只会重复同样的错误配置
	if errors.As(err, &ae) && ae.NonSSE {
		return Classified{
			Retryable: false, Category: CatClientError,
			UserMessage: ae.Msg, MaxRetries: 0,
		}
	}

	status, hasStatus := extractStatus(err)

	// 0. 图片处理错误（400/500 包裹的图片拒绝）——在通用状态分类之前检查
	if hasStatus && (status == 400 || status == 500) && reImageErr.MatchString(err.Error()) {
		return Classified{
			Retryable: true, Category: CatImageStrip,
			UserMessage: "Image processing error — stripping images and retrying.",
			MaxRetries:  1, StripImages: true,
		}
	}

	// 1. 状态码分类
	if hasStatus {
		var payloadHadImages *bool
		if errors.As(err, &ae) {
			payloadHadImages = ae.PayloadHadImages
		}
		if c, ok := classifyByStatus(status, payloadHadImages); ok {
			// 服务端 Retry-After 覆盖类别默认延迟
			var ae2 *APIError
			if errors.As(err, &ae2) && ae2.RetryAfterMs != nil {
				c.RetryDelayMs = *ae2.RetryAfterMs
				c.RetryDelayFromServer = true
			}
			return c
		}
	}

	// 2. 名称 / 消息模式分类
	return classifyByPattern(err)
}

// classifyByStatus 按 HTTP 状态码分类。未识别的状态返回 ok=false。
func classifyByStatus(status int, payloadHadImages *bool) (Classified, bool) {
	switch {
	case status == 429:
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatRateLimit,
			UserMessage: "Rate limited — too many requests. Retrying after back-off.",
			MaxRetries:  5,
		}, true
	case status == 529 || status == 503:
		return Classified{
			Retryable: true, RetryDelayMs: 3000, ShouldReconnect: true,
			Category:    CatOverloaded,
			UserMessage: "Server is overloaded. Retrying after back-off.",
			MaxRetries:  3,
		}, true
	case status == 500 || status == 502:
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatServerError,
			UserMessage: "Server error. Retrying.",
			MaxRetries:  3,
		}, true
	case status == 408:
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatTimeout,
			UserMessage: "Server request timeout. Retrying.",
			MaxRetries:  3,
		}, true
	case status == 425:
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatOverloaded,
			UserMessage: "Server not ready (Too Early). Retrying.",
			MaxRetries:  3,
		}, true
	case status == 413:
		// 两种成因在 wire 层长相一样，区分信息只有 API 客户端有
		//（它知道自己刚发出去的请求体里有没有 image_url）。
		//   true      → 图片过重：剥离后重发一次
		//   false     → 本就没图：纯上下文超限，重发必然再 413，不可重试
		//   nil       → 第三方网关转述：乐观按 image_strip
		if payloadHadImages != nil && !*payloadHadImages {
			return Classified{
				Retryable: false, Category: CatContextOverflow,
				UserMessage: "Payload too large (413) — the request exceeds the provider limit.",
				MaxRetries:  0,
			}, true
		}
		return Classified{
			Retryable: true, ShouldReconnect: false,
			Category:    CatImageStrip,
			UserMessage: "Payload too large — stripping images and retrying.",
			MaxRetries:  1, StripImages: true,
		}, true
	case status == 401 || status == 403:
		return Classified{
			Retryable: false, Category: CatAuthError,
			UserMessage: "Authentication failed. Check your API key.",
			MaxRetries:  0,
		}, true
	case status == 404:
		return Classified{
			Retryable: false, Category: CatClientError,
			UserMessage: "Not found (404) — verify the model id with `rivet provider models <provider>`.",
			MaxRetries:  0,
		}, true
	case status >= 400 && status < 500:
		return Classified{
			Retryable: false, Category: CatClientError,
			UserMessage: "Client error (" + strconv.Itoa(status) + ").",
			MaxRetries:  0,
		}, true
	case status >= 500:
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatServerError,
			UserMessage: "Server error (" + strconv.Itoa(status) + "). Retrying.",
			MaxRetries:  3,
		}, true
	}
	return Classified{}, false
}

// classifyByPattern 按错误名 / 消息模式分类。
func classifyByPattern(err error) Classified {
	msg := err.Error()
	cause := fetchCauseDetail(err)
	searchText := msg
	if cause != "" {
		searchText = msg + " | " + cause
	}

	// 用户主动取消——绝不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Classified{
			Retryable: false, Category: CatClientError,
			UserMessage: "Request was aborted.",
			MaxRetries:  0,
		}
	}

	// 连接重置 / 拒绝 / 不可达——传输层网络失败，重连重试是对的默认
	if reNetworkErr.MatchString(searchText) {
		um := "Connection lost. Reconnecting."
		if cause != "" {
			um = "Connection lost (" + cause + "). Reconnecting."
		}
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category: CatTimeout, UserMessage: um, MaxRetries: 3,
		}
	}

	// 超时
	if reTimeout.MatchString(searchText) {
		return Classified{
			Retryable: true, RetryDelayMs: 3000, ShouldReconnect: true,
			Category:    CatTimeout,
			UserMessage: "Request timed out. Retrying.",
			MaxRetries:  3,
		}
	}

	// 部分 OpenAI 兼容网关把过载做成结构化错误体，但不在抛出的 Error 上保留
	// HTTP 503——保持 overloaded 类别，让 FallbackClient 能切备用提供商。
	if reOverloaded.MatchString(searchText) {
		return Classified{
			Retryable: true, RetryDelayMs: 3000, ShouldReconnect: true,
			Category:    CatOverloaded,
			UserMessage: "Service is busy. Retrying or switching provider.",
			MaxRetries:  3,
		}
	}

	// 上游流在首个负载前关闭
	if reEmptyStream.MatchString(searchText) {
		return Classified{
			Retryable: true, RetryDelayMs: 2000, ShouldReconnect: true,
			Category:    CatServerError,
			UserMessage: "Upstream stream closed. Retrying.",
			MaxRetries:  3,
		}
	}

	// 上下文超限
	if reContextOvf.MatchString(msg) {
		return Classified{
			Retryable: false, Category: CatContextOverflow,
			UserMessage: "Context too long — reduce prompt size.",
			MaxRetries:  0,
		}
	}

	// 流解析错误
	if reStreamParse.MatchString(searchText) {
		return Classified{
			Retryable: true, RetryDelayMs: 1000, ShouldReconnect: true,
			Category:    CatStreamParse,
			UserMessage: "Stream parse error. Reconnecting.",
			MaxRetries:  2,
		}
	}

	// 兜底 unknown——乐观可重试
	return Classified{
		Retryable: true, RetryDelayMs: 2000,
		Category:    CatUnknown,
		UserMessage: "Unexpected error: " + msg,
		MaxRetries:  2,
	}
}

// ParseRetryAfterMs 解析 Retry-After 头值（RFC 7231 §7.1.3）。
//
// 数字字符串 → 秒 × 1000；HTTP-date → 与当前的差值；不可解析 → 无值。
func ParseRetryAfterMs(value string, nowMs int64) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
		if f >= 0 {
			return int(f * 1000), true
		}
	}
	// HTTP-date：Go 的 http.ParseTime 可解析标准格式，但为避免引入 net/http
	// 的传递依赖，这里只做数字形式；日期形式由调用方在使用时补。
	return 0, false
}

// RecoveryGuidance 返回终态恢复指引（重试耗尽后的「下一步」）。
//
// 与 UserMessage 的分工：UserMessage 是重试进行中的过程文案（"Retrying…"），
// 本函数是耗尽后的可行动指引。
func RecoveryGuidance(err error) string {
	c := Classify(err)
	switch c.Category {
	case CatRateLimit:
		return "限流/额度不足：稍等片刻再发，或切轻量档；持续 429 先查余额"
	case CatOverloaded, CatServerError:
		return "服务商暂时性故障：稍后重发，或切换服务商"
	case CatTimeout:
		return "网络超时：检查网络/代理后重发"
	case CatAuthError:
		return "认证失败：检查 API Key"
	case CatContextOverflow:
		return "上下文超限：压缩历史或开新会话"
	case CatClientError:
		return "请求被拒（模型 id 或端点路径错）：确认模型；自定义端点检查 baseUrl 是否缺 /v1"
	case CatImageStrip:
		return "图片负载超限：去掉部分图片后重发"
	case CatStreamParse:
		return "流解析失败：重发一次；反复出现请打包日志提 issue"
	case CatReasoningRepetition:
		return "检测到推理短句持续重复，已停止请求；建议新建会话或切换模型"
	default:
		return "重发一次；持续失败请体检配置并查看日志"
	}
}
