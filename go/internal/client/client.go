// Package client 把序列化、请求构造、SSE 解析、错误分类、重试串成可用的
// 模型客户端。
//
// 这是集成层——各分片（wire/body/sse/apierr/retry）都有独立的字节级 oracle
// 对账，但**分片正确不等于整体可用**。本包的价值在于让它们协同跑通一次
// 真实的请求-响应循环，并由 mock server 端到端验证。
//
// 对账 src/api/openai-client.ts 的 stream()/sendStream()：
//   - baseUrl 归一化（剥已知请求路径尾巴，避免双重拼接）
//   - 端点 {base}/chat/completions，Authorization: Bearer
//   - 非 2xx → 解析错误体 + 附 status / payloadHadImages（413）/ retryAfterMs
//   - 200 但 content-type 非 SSE → 快速失败并给出可行动提示
//   - 经重试引擎包裹整个尝试
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/api/sse"
	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/apierr"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/retry"
)

// Config 是客户端配置。
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int
	Provider   string
	HTTPClient *http.Client
	// SessionID 非空时随 sessionHeader 发送（缓存路由亲和）。
	SessionID     string
	SessionHeader string
	// Retry 是重试选项。nil = 用默认（10min 总预算）。
	Retry *retry.Options
	// Body 是请求体构造配置（能力、thinking、后缀等）。
	Body *api.ClientConfig
}

// Client 是 OpenAI 兼容的流式客户端。
type Client struct {
	cfg  Config
	http *http.Client
}

// New 创建客户端。
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{cfg: cfg, http: hc}
}

// streamSuffixes 是用户可能粘贴进 baseUrl 的请求路径尾巴（最长优先）。
//
// 不剥的话后续拼接会得到 `…/chat/completions/chat/completions` → 404，
// 而报错只会说 "path may be wrong"。
var streamSuffixes = []string{
	"/images/generations",
	"/chat/completions",
	"/completions",
	"/messages",
	"/models",
	"/embeddings",
}

// NormalizeBaseURL 剥掉尾部斜杠与已知请求路径尾巴。
//
// 版本段（/v1）**保留**——它属于 base。
func NormalizeBaseURL(raw string) string {
	url := strings.TrimRight(strings.TrimSpace(raw), "/")
	lower := strings.ToLower(url)
	for _, suffix := range streamSuffixes {
		if strings.HasSuffix(lower, suffix) {
			url = strings.TrimRight(url[:len(url)-len(suffix)], "/")
			break
		}
	}
	return url
}

// Stream 发送请求并流式解析响应。
//
// 整个尝试（含网络错误）由重试引擎包裹。
func (c *Client) Stream(ctx context.Context, req *api.ChatRequest, h sse.Handler) error {
	opts := c.cfg.Retry
	if opts == nil {
		maxDur := int64(10 * 60 * 1000)
		if c.cfg.Provider == "glm" {
			maxDur = int64(20 * 60 * 1000)
		}
		opts = &retry.Options{MaxTotalDurationMs: &maxDur}
	}

	_, err := retry.Do(ctx, func() (struct{}, error) {
		return struct{}{}, c.attempt(ctx, req, h)
	}, opts)
	return err
}

// attempt 执行一次完整的请求-响应尝试。
func (c *Client) attempt(ctx context.Context, req *api.ChatRequest, h sse.Handler) error {
	bodyCfg := c.cfg.Body
	if bodyCfg == nil {
		bodyCfg = &api.ClientConfig{Model: c.cfg.Model, MaxTokens: c.cfg.MaxTokens}
	}

	// 请求体构造（保插入序——见 internal/api/wire 的说明）
	body := api.BuildWireBody(req, bodyCfg)
	payload := body.Marshal()

	url := NormalizeBaseURL(c.cfg.BaseURL) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connection", "keep-alive")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	if c.cfg.SessionID != "" {
		hdr := c.cfg.SessionHeader
		if hdr == "" {
			hdr = "X-Request-Session"
		}
		httpReq.Header.Set(hdr, c.cfg.SessionID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// 网络层失败——交给分类器判定（含 cause 链解包）
		return &apierr.APIError{Msg: err.Error(), Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// ── 非 2xx：解析错误体并附分类所需标记 ──
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		apiErr := &apierr.APIError{
			Status: resp.StatusCode,
			Msg:    parseErrorBody(resp.StatusCode, string(errBody)),
		}
		// 413 的两种成因在 wire 层同形，只有这里知道刚发出去的体里有没有图。
		// 无图 = 纯上下文超限，重发同一个体必然再 413，不该重试。
		if resp.StatusCode == 413 {
			had := payloadHasImages(req.Messages)
			apiErr.PayloadHadImages = &had
		}
		// Retry-After → 分类器据此用服务端指定的等待
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if ms, ok := apierr.ParseRetryAfterMs(ra, 0); ok {
				apiErr.RetryAfterMs = &ms
			}
		}
		return apiErr
	}

	// ── content-type 门禁：200 但不是 SSE → 端点答了别的东西 ──
	// 常见于路径错（baseUrl 缺 /v1）或网关返回 HTML。快速失败并给可行动提示，
	// 而不是把 HTML 喂给 SSE 解析器。
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "event-stream") && !strings.Contains(ct, "octet-stream") {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &apierr.APIError{
			Status: 200,
			NonSSE: true,
			Msg: fmt.Sprintf(
				"端点返回 200 但 content-type 是 %s（不是 SSE 流）——端点可能不支持流式，或路径错误（baseUrl 是否缺 /v1？）。baseUrl=%s。响应片段：%.200s",
				ct, c.cfg.BaseURL, string(snippet)),
		}
	}

	// ── 流式解析 ──
	parser := sse.NewParser(h)
	scanner := bufio.NewScanner(resp.Body)
	// SSE 单行可能很长（工具参数分片），放宽缓冲上限
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue // 空行与 `:` 注释（心跳）不算数据事件
		}
		payloadLine := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payloadLine == "[DONE]" {
			parser.Finish()
			return nil
		}
		parser.ProcessPayload(payloadLine)
	}
	if err := scanner.Err(); err != nil {
		// 流中途断开——标记为可重试的流错误
		return fmt.Errorf("stream parse: %w", err)
	}
	parser.Finish()
	return nil
}

// parseErrorBody 从错误响应体提取可读消息。
//
// 优先取 OpenAI 兼容的 {"error":{"message":...}}，回退到原始文本。
func parseErrorBody(status int, body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "HTTP " + strconv.Itoa(status)
	}
	// 尝试解析结构化错误体
	if msg := extractErrorMessage(trimmed); msg != "" {
		return msg
	}
	if len(trimmed) > 300 {
		trimmed = trimmed[:300]
	}
	return trimmed
}

// extractErrorMessage 从 JSON 错误体中取 message 字段。
func extractErrorMessage(body string) string {
	// 轻量解析：不引入完整 JSON 解码（错误体可能畸形）
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	if e, ok := m["error"].(map[string]any); ok {
		if msg, ok := e["message"].(string); ok {
			return msg
		}
	}
	if msg, ok := m["message"].(string); ok {
		return msg
	}
	return ""
}

// payloadHasImages 判断消息中是否含图片部件（用于 413 分流）。
func payloadHasImages(msgs []*wire.OrderedMap) bool {
	for _, m := range msgs {
		content, ok := m.Get("content")
		if !ok {
			continue
		}
		arr, ok := content.([]any)
		if !ok {
			continue
		}
		for _, part := range arr {
			pm, ok := part.(map[string]any)
			if !ok {
				// 也可能是 OrderedMap
				if om, ok2 := part.(*wire.OrderedMap); ok2 {
					if t, ok3 := om.Get("type"); ok3 && t == "image_url" {
						return true
					}
				}
				continue
			}
			if t, ok := pm["type"].(string); ok && t == "image_url" {
				return true
			}
		}
	}
	return false
}

// IsBudgetExhausted 报告错误是否为重试预算耗尽。
func IsBudgetExhausted(err error) bool {
	var be *retry.BudgetExhaustedError
	return errors.As(err, &be)
}

// Guidance 返回用户可行动的恢复指引。
func Guidance(err error) string { return apierr.RecoveryGuidance(err) }

// Usage 别名，便于调用方不必额外 import contract。
type Usage = contract.Usage
