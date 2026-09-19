package apierr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type oracleEntry struct {
	Retryable            bool     `json:"retryable"`
	RetryDelayMs         int      `json:"retryDelayMs"`
	ShouldReconnect      bool     `json:"shouldReconnect"`
	Category             string   `json:"category"`
	MaxRetries           int      `json:"maxRetries"`
	StripImages          bool     `json:"stripImages"`
	RetryDelayFromServer bool     `json:"retryDelayFromServer"`
	_                    struct{} `json:"-"`
}

func loadOracle(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "apierr", "oracle.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成：node_modules/.bin/tsx go/testdata/apierr/gen-oracle.ts", p, err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// buildError 按用例名构造对应错误（与 gen-oracle.ts 的 cases 逐字对应）。
func buildError(name string) error {
	switch name {
	// 状态码分支
	case "status_429":
		return &APIError{Status: 429, Msg: "Too many requests"}
	case "status_529":
		return &APIError{Status: 529, Msg: "Overloaded"}
	case "status_503":
		return &APIError{Status: 503, Msg: "Service Unavailable"}
	case "status_500":
		return &APIError{Status: 500, Msg: "Internal error"}
	case "status_502":
		return &APIError{Status: 502, Msg: "Bad gateway"}
	case "status_408":
		return &APIError{Status: 408, Msg: "Request timeout"}
	case "status_425":
		return &APIError{Status: 425, Msg: "Too Early"}
	case "status_401":
		return &APIError{Status: 401, Msg: "Unauthorized"}
	case "status_403":
		return &APIError{Status: 403, Msg: "Forbidden"}
	case "status_404":
		return &APIError{Status: 404, Msg: "Not found"}
	case "status_400":
		return &APIError{Status: 400, Msg: "Bad request"}
	case "status_501":
		return &APIError{Status: 501, Msg: "Not implemented"}
	// 413 三分支
	case "status_413_had_images":
		return &APIError{Status: 413, Msg: "Payload too large", PayloadHadImages: boolPtr(true)}
	case "status_413_no_images":
		return &APIError{Status: 413, Msg: "Payload too large", PayloadHadImages: boolPtr(false)}
	case "status_413_unknown":
		return &APIError{Status: 413, Msg: "Payload too large"}
	// Retry-After 覆盖
	case "retry_after_override":
		return &APIError{Status: 429, Msg: "Rate limited", RetryAfterMs: intPtr(7000)}
	// 图片处理错误
	case "image_process_400":
		return &APIError{Status: 400, Msg: "Could not process image: too large"}
	case "image_process_500":
		return &APIError{Status: 500, Msg: "upstream: 400 invalid image format"}
	// 非 SSE
	case "non_sse":
		return &APIError{Msg: "Endpoint returned a non-SSE 200 response.", NonSSE: true}
	// 模式分支
	case "network_econnrefused":
		return errors.New("fetch failed: ECONNREFUSED")
	case "network_econnreset":
		return errors.New("ECONNRESET")
	case "network_enotfound":
		return errors.New("getaddrinfo ENOTFOUND api.example.com")
	case "timeout_msg":
		return errors.New("Request timeout after 30000ms")
	case "overloaded_msg":
		return errors.New("Service temporarily unavailable")
	case "too_busy":
		return errors.New("Server too busy")
	case "empty_stream":
		return errors.New("empty_stream: upstream closed")
	case "context_overflow":
		return errors.New("prompt is too long: 200000 tokens")
	case "context_length_exceeded":
		return errors.New("context_length_exceeded")
	case "stream_parse":
		return errors.New("invalid SSE event received")
	case "unknown_fallback":
		return errors.New("something completely unexpected")
	case "status_in_message":
		return errors.New("Codex API error (429): rate limited")
	case "status_in_message_503":
		return errors.New("upstream returned (503)")
	}
	return nil
}

// TestClassifierGoldenParity 用真实 TS 分类器的输出对账 Go 实现。
func TestClassifierGoldenParity(t *testing.T) {
	oracle := loadOracle(t)

	var checked int
	for name, raw := range oracle {
		if len(name) > 2 && name[:2] == "__" {
			continue // 元数据段
		}
		err := buildError(name)
		if err == nil {
			t.Errorf("用例 %q 未在 Go 侧定义——gen-oracle.ts 与测试不同步", name)
			continue
		}
		var want oracleEntry
		if e := json.Unmarshal(raw, &want); e != nil {
			t.Fatalf("解析 %s 失败：%v", name, e)
		}
		t.Run(name, func(t *testing.T) {
			got := Classify(err)
			if got.Retryable != want.Retryable {
				t.Errorf("Retryable = %v, want %v", got.Retryable, want.Retryable)
			}
			if string(got.Category) != want.Category {
				t.Errorf("Category = %q, want %q", got.Category, want.Category)
			}
			if got.MaxRetries != want.MaxRetries {
				t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, want.MaxRetries)
			}
			if got.ShouldReconnect != want.ShouldReconnect {
				t.Errorf("ShouldReconnect = %v, want %v", got.ShouldReconnect, want.ShouldReconnect)
			}
			if got.StripImages != want.StripImages {
				t.Errorf("StripImages = %v, want %v", got.StripImages, want.StripImages)
			}
			if got.RetryDelayFromServer != want.RetryDelayFromServer {
				t.Errorf("RetryDelayFromServer = %v, want %v", got.RetryDelayFromServer, want.RetryDelayFromServer)
			}
			// 延迟：仅在非抖动路径上精确比对（抖动由 retry 层负责）
			if !want.RetryDelayFromServer && got.RetryDelayMs != want.RetryDelayMs {
				t.Errorf("RetryDelayMs = %d, want %d", got.RetryDelayMs, want.RetryDelayMs)
			}
			if want.RetryDelayFromServer && got.RetryDelayMs != want.RetryDelayMs {
				t.Errorf("服务端延迟 = %d, want %d", got.RetryDelayMs, want.RetryDelayMs)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("未比对任何用例——oracle 格式可能已变")
	}
	t.Logf("比对 %d 个用例", checked)
}

// 413 三分支必须区分（这是 classifier 里最微妙的逻辑）。
func Test413ThreeWayBranch(t *testing.T) {
	// 有图 → image_strip，可重试，stripImages=true
	withImg := Classify(&APIError{Status: 413, Msg: "too large", PayloadHadImages: boolPtr(true)})
	if withImg.Category != CatImageStrip || !withImg.Retryable || !withImg.StripImages {
		t.Errorf("有图 413 应为 image_strip/可重试/strip：%+v", withImg)
	}
	// 无图 → context_overflow，不可重试（重发必然再 413）
	noImg := Classify(&APIError{Status: 413, Msg: "too large", PayloadHadImages: boolPtr(false)})
	if noImg.Category != CatContextOverflow || noImg.Retryable {
		t.Errorf("无图 413 应为 context_overflow/不可重试：%+v", noImg)
	}
	// 未知 → 乐观按 image_strip
	unknown := Classify(&APIError{Status: 413, Msg: "too large"})
	if unknown.Category != CatImageStrip || !unknown.Retryable {
		t.Errorf("未知 413 应乐观按 image_strip：%+v", unknown)
	}
}

// 反证：413 的「无图」分支若被去掉，本测试变红。
func Test413NoImagesNotRetryable(t *testing.T) {
	c := Classify(&APIError{Status: 413, Msg: "payload too large", PayloadHadImages: boolPtr(false)})
	if c.Retryable {
		t.Fatal("无图 413 若可重试，会在同一请求体上无限循环（每次必然再 413）")
	}
	if c.MaxRetries != 0 {
		t.Errorf("无图 413 的 MaxRetries 应为 0，实际 %d", c.MaxRetries)
	}
}

// Retry-After 解析。
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"5", 5000, true},
		{"0", 0, true},
		{"1.5", 1500, true},
		{"not-a-number", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfterMs(tc.in, 0)
		if ok != tc.ok {
			t.Errorf("ParseRetryAfterMs(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseRetryAfterMs(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// 用户取消绝不重试。
func TestAbortNotRetryable(t *testing.T) {
	c := Classify(context.Canceled)
	if c.Retryable {
		t.Error("context.Canceled 不应可重试")
	}
	c = Classify(context.DeadlineExceeded)
	if c.Retryable {
		t.Error("context.DeadlineExceeded 不应可重试")
	}
}

// 推理重复是终态。
func TestReasoningRepetitionTerminal(t *testing.T) {
	c := Classify(NewReasoningRepetition("推理短句持续重复"))
	if c.Retryable || c.Category != CatReasoningRepetition {
		t.Errorf("推理重复应为终态：%+v", c)
	}
}

// 全部类别都在清单中（防止新增类别漏登记）。
func TestAllCategoriesListed(t *testing.T) {
	seen := map[Category]bool{}
	for _, c := range AllCategories {
		seen[c] = true
	}
	// 每个常量都必须在清单中
	all := []Category{
		CatRateLimit, CatOverloaded, CatServerError, CatTimeout, CatAuthError,
		CatClientError, CatContextOverflow, CatImageStrip, CatStreamParse,
		CatReasoningRepetition, CatUnknown,
	}
	for _, c := range all {
		if !seen[c] {
			t.Errorf("类别 %q 未在 AllCategories 中登记", c)
		}
	}
	if len(AllCategories) != len(all) {
		t.Errorf("AllCategories 长度 %d, want %d", len(AllCategories), len(all))
	}
}

// 每个类别都有非空恢复指引。
func TestRecoveryGuidanceNonEmpty(t *testing.T) {
	for _, c := range AllCategories {
		var err error
		switch c {
		case CatRateLimit:
			err = &APIError{Status: 429, Msg: "x"}
		case CatAuthError:
			err = &APIError{Status: 401, Msg: "x"}
		case CatClientError:
			err = &APIError{Status: 404, Msg: "x"}
		case CatContextOverflow:
			err = errors.New("prompt is too long")
		case CatReasoningRepetition:
			err = NewReasoningRepetition("x")
		case CatImageStrip:
			err = &APIError{Status: 413, Msg: "x", PayloadHadImages: boolPtr(true)}
		case CatStreamParse:
			err = errors.New("invalid SSE event")
		default:
			err = fmt.Errorf("some %s error", c)
		}
		if g := RecoveryGuidance(err); g == "" {
			t.Errorf("类别 %q 的恢复指引为空", c)
		}
	}
}
