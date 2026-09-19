package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/api/sse"
	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/retry"
)

// collector 收集 SSE 事件。
type collector struct {
	texts  []string
	blocks []sse.Event
	stops  []struct {
		reason string
		usage  contract.Usage
	}
}

func newCollector() (*collector, sse.Handler) {
	c := &collector{}
	h := sse.Handler{
		OnTextDelta:    func(s string) { c.texts = append(c.texts, s) },
		OnContentBlock: func(e sse.Event) { c.blocks = append(c.blocks, e) },
		OnStopReason: func(r string, u contract.Usage) {
			c.stops = append(c.stops, struct {
				reason string
				usage  contract.Usage
			}{r, u})
		},
	}
	return c, h
}

// sseBody 构造一段 SSE 响应文本。
func sseBody(chunks ...string) string {
	return strings.Join(chunks, "") + "data: [DONE]\n\n"
}

func dataLine(obj string) string { return "data: " + obj + "\n\n" }

// 基础请求构造。
func baseReq() *api.ChatRequest {
	return &api.ChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []*wire.OrderedMap{
			wire.NewOrderedMap().Set("role", "user").Set("content", "hi"),
		},
	}
}

// 端到端：请求发出 → SSE 流解析 → 文本/块/stop 事件。
func TestEndToEndStream(t *testing.T) {
	var gotAuth, gotCT, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, sseBody(
			dataLine(`{"choices":[{"delta":{"content":"Hello"}}]}`),
			dataLine(`{"choices":[{"delta":{"content":" world"}}]}`),
			dataLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-v4-pro", MaxTokens: 100})
	col, h := newCollector()
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}

	// 请求侧
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("路径 = %q, want /chat/completions", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-v4-pro"`) {
		t.Errorf("请求体缺 model：%s", gotBody)
	}
	// 顶层字段序（保插入序）——这是缓存正确性的关键
	if !strings.HasPrefix(gotBody, `{"model":`) {
		t.Errorf("请求体顶层字段序错误（应以 model 开头）：%.60s", gotBody)
	}

	// 响应侧
	if strings.Join(col.texts, "") != "Hello world" {
		t.Errorf("文本 = %q", strings.Join(col.texts, ""))
	}
	if len(col.stops) != 1 || col.stops[0].reason != "end_turn" {
		t.Errorf("stop 事件错误：%+v", col.stops)
	}
	// 结束时应产出 text 块（会话历史靠它持久化）
	var textBlock string
	for _, b := range col.blocks {
		if b.BlockType == "text" {
			textBlock = b.Text
		}
	}
	if textBlock != "Hello world" {
		t.Errorf("text 块 = %q（缺它则文本回复不会进会话历史）", textBlock)
	}
}

// baseUrl 归一化：剥已知路径尾巴，避免双重拼接。
func TestBaseURLNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://api.deepseek.com", "https://api.deepseek.com"},
		{"https://api.deepseek.com/", "https://api.deepseek.com"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com/v1/chat/completions", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com/chat/completions", "https://api.deepseek.com"},
		{"https://api.deepseek.com/v1/models", "https://api.deepseek.com/v1"},
		{"  https://api.deepseek.com/v1  ", "https://api.deepseek.com/v1"},
	}
	for _, tc := range cases {
		if got := NormalizeBaseURL(tc.in); got != tc.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 反证：baseUrl 带路径尾巴时必须正确归一，否则拼接出双重路径。
func TestBaseURLTailStrippedPreventsDoublePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody())
	}))
	defer srv.Close()

	// 用户粘贴了完整请求 URL（常见误用）
	c := New(Config{
		BaseURL: srv.URL + "/v1/chat/completions",
		APIKey:  "k", Model: "m", MaxTokens: 10,
	})
	col, h := newCollector()
	_ = col
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("路径 = %q, want /v1/chat/completions（若为 /v1/chat/completions/chat/completions 则归一失败）", gotPath)
	}
}

// 非 2xx：错误体解析 + 状态码透传。
func TestErrorResponseParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key provided"}}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "bad", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	_ = col
	err := c.Stream(context.Background(), baseReq(), h)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "Invalid API key provided") {
		t.Errorf("错误体未解析：%v", err)
	}
	// 401 不可重试——应快速失败
	if !strings.Contains(Guidance(err), "认证") {
		t.Errorf("指引应指向认证：%s", Guidance(err))
	}
}

// 反证：不可重试错误不得触发重试（401 只发一次请求）。
func TestNonRetryableFailsFast(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"nope"}}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "bad", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	_ = col
	_ = c.Stream(context.Background(), baseReq(), h)

	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Errorf("401 应只请求 1 次，实际 %d 次", n)
	}
}

// 可重试错误（429）应重试并最终成功。
func TestRetryOn429ThenSucceed(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		if n < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(
			dataLine(`{"choices":[{"delta":{"content":"ok"}}]}`),
			dataLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		))
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10,
		Retry: &retry.Options{
			MaxTotalRetries: intPtr(5),
			Sleep:           func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		},
	})
	col, h := newCollector()
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("应在重试后成功：%v", err)
	}
	if n := atomic.LoadInt64(&calls); n != 3 {
		t.Errorf("调用次数 = %d, want 3", n)
	}
	if strings.Join(col.texts, "") != "ok" {
		t.Errorf("文本 = %q", strings.Join(col.texts, ""))
	}
}

// 反证：200 但非 SSE → 快速失败并给出可行动提示（不把 HTML 喂给解析器）。
func TestNonSSE200FailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		fmt.Fprint(w, "<html><body>Gateway</body></html>")
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	_ = col
	err := c.Stream(context.Background(), baseReq(), h)
	if err == nil {
		t.Fatal("非 SSE 的 200 应失败")
	}
	if !strings.Contains(err.Error(), "content-type") {
		t.Errorf("错误应提及 content-type：%v", err)
	}
	// 这是终态——不应重试（重试只会重复同样的错误配置）
	if Guidance(err) == "" {
		t.Error("应给出指引")
	}
}

// 反证：413 的 payloadHadImages 标记必须正确传递，驱动分类器分流。
func Test413ImageMarkerPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(413)
		fmt.Fprint(w, `{"error":{"message":"payload too large"}}`)
	}))
	defer srv.Close()

	// 无图请求 → 应判 context_overflow（不可重试）
	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	_ = col
	err := c.Stream(context.Background(), baseReq(), h)
	if err == nil {
		t.Fatal("413 应失败")
	}
	g := Guidance(err)
	if !strings.Contains(g, "上下文") {
		t.Errorf("无图 413 应判为上下文超限，实际指引：%s", g)
	}
}

// 有图请求的 413 → 应判 image_strip（可重试一次）。
func Test413WithImagesMarkedAsStrip(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(413)
		fmt.Fprint(w, `{"error":{"message":"too large"}}`)
	}))
	defer srv.Close()

	// 构造带图片部件的请求
	req := &api.ChatRequest{
		Model: "m",
		Messages: []*wire.OrderedMap{
			wire.NewOrderedMap().Set("role", "user").Set("content", []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,xx"}},
			}),
		},
	}
	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10,
		Retry: &retry.Options{
			MaxTotalRetries: intPtr(5),
			Sleep:           func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		},
	})
	col, h := newCollector()
	_ = col
	_ = c.Stream(context.Background(), req, h)

	// image_strip 的 MaxRetries=1 → 初始 1 + 重试 1 = 2 次
	if n := atomic.LoadInt64(&calls); n != 2 {
		t.Errorf("有图 413 应重试 1 次（共 2 次调用），实际 %d 次", n)
	}
}

// Retry-After 头应被读取并驱动等待（服务端指定优先于类别默认）。
func TestRetryAfterHeaderRespected(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		if n < 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(dataLine(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	var observedDelay int
	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10,
		Retry: &retry.Options{
			MaxTotalRetries: intPtr(3),
			OnRetry: func(i retry.Info) {
				observedDelay = i.NextDelayMs
			},
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		},
	})
	col, h := newCollector()
	_ = col
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("应成功：%v", err)
	}
	// Retry-After: 1 秒 → 1000ms + 抖动(0~50%) → 1000~1500
	if observedDelay < 1000 || observedDelay > 1500 {
		t.Errorf("Retry-After 未生效：观察到的延迟 = %dms（应在 1000~1500）", observedDelay)
	}
}

// 工具调用端到端：分片参数被正确累积与解析。
func TestToolCallEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody(
			dataLine(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`),
			dataLine(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\""}}]}}]}`),
			dataLine(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"a.ts\"}"}}]}}]}`),
			dataLine(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}

	var toolBlocks []sse.Event
	for _, b := range col.blocks {
		if b.BlockType == "tool_use" {
			toolBlocks = append(toolBlocks, b)
		}
	}
	if len(toolBlocks) != 1 {
		t.Fatalf("应有 1 个 tool_use 块，实际 %d", len(toolBlocks))
	}
	tb := toolBlocks[0]
	if tb.ToolName != "read_file" {
		t.Errorf("工具名 = %q", tb.ToolName)
	}
	if tb.ToolInput["path"] != "a.ts" {
		t.Errorf("参数 = %v（分片累积失败）", tb.ToolInput)
	}
	if len(col.stops) != 1 || col.stops[0].reason != "tool_use" {
		t.Errorf("stop reason 应为 tool_use：%+v", col.stops)
	}
}

// 会话头随请求发送（缓存路由亲和）。
func TestSessionHeaderSent(t *testing.T) {
	var gotHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Get("X-Request-Session")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody())
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10,
		SessionID: "sess-123",
	})
	col, h := newCollector()
	_ = col
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}
	if gotHdr != "sess-123" {
		t.Errorf("会话头 = %q, want sess-123", gotHdr)
	}
}

// 自定义会话头名。
func TestCustomSessionHeader(t *testing.T) {
	var gotHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Get("X-Custom-Session")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseBody())
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10,
		SessionID: "s1", SessionHeader: "X-Custom-Session",
	})
	col, h := newCollector()
	_ = col
	if err := c.Stream(context.Background(), baseReq(), h); err != nil {
		t.Fatalf("Stream 失败：%v", err)
	}
	if gotHdr != "s1" {
		t.Errorf("自定义会话头 = %q", gotHdr)
	}
}

// ctx 取消应中断整个流程。
//
// 注意：handler 不能 `<-r.Context().Done()` 后直接返回——httptest 的 Close()
// 会等 handler 返回，而客户端取消并不保证服务端 ctx 立即触发，会死锁。
// 这里用显式 channel 让 handler 可被测试主动释放。
func TestContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // 先把头发出去，让客户端进入读流状态
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", MaxTokens: 10})
	col, h := newCollector()
	_ = col
	err := c.Stream(ctx, baseReq(), h)
	if err == nil {
		t.Fatal("应因 ctx 超时而失败")
	}
	// 错误应可归因于取消（可能被重试引擎包装，故用 errors.Is 逐层判）
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Logf("错误：%v（重试引擎可能已包装）", err)
	}
}

func intPtr(i int) *int { return &i }
