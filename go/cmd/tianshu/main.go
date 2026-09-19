// Command tianshu 是天枢运行时的 Go 版 CLI 入口。
//
// 用法：
//
//	tianshu -p "提示词"            # 单次提示（headless）
//	tianshu -p "提示词" --json     # JSON 事件流输出
//	tianshu                        # 交互式（逐行读 stdin）
//
// 环境变量：
//
//	DEEPSEEK_API_KEY / OPENAI_API_KEY / RIVET_API_KEY   API 密钥
//	TIANSHU_BASE_URL                                    端点（默认 DeepSeek）
//	TIANSHU_MODEL                                       模型名
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kalandramo/tianshu/go/internal/agent"
	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/client"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/retry"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

func main() {
	var (
		prompt       = flag.String("p", "", "单次提示（headless 模式）")
		jsonOut      = flag.Bool("json", false, "以 JSON 事件流输出")
		model        = flag.String("model", "", "模型名（覆盖 TIANSHU_MODEL）")
		baseURL      = flag.String("base-url", "", "API 端点（覆盖 TIANSHU_BASE_URL）")
		approvalMode = flag.String("approval", "auto-safe", "审批档位：auto-safe | dangerously-skip-permissions")
		maxTurns     = flag.Int("max-turns", 50, "单次 run 的最大轮数")
		systemPrompt = flag.String("system", "", "系统提示词（默认按模型家族渲染，见 internal/prompt）")
		showVersion  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("tianshu (go) 0.1.0-dev")
		return
	}

	app, err := loadConfig(*model, *baseURL, *approvalMode, *maxTurns, *systemPrompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误：%v\n", err)
		os.Exit(2)
	}
	cfg := app.Agent

	// 优雅退出：Ctrl+C 取消上下文
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	loop := buildLoop(app, *jsonOut)

	if *prompt != "" {
		// headless 单次提示
		if err := loop.Run(ctx, *prompt); err != nil {
			reportError(err, *jsonOut)
			os.Exit(1)
		}
		return
	}

	// 交互式：逐行读 stdin
	fmt.Fprintf(os.Stderr, "tianshu (go) — 模型 %s，工作目录 %s\n", cfg.Model, cfg.Cwd)
	fmt.Fprintf(os.Stderr, "输入提示词后回车；Ctrl+D 退出。\n\n")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for {
		fmt.Fprint(os.Stderr, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		if err := loop.Run(ctx, line); err != nil {
			reportError(err, *jsonOut)
			// 交互模式下不退出，继续接受输入
		}
		fmt.Fprintln(os.Stderr)
	}
}

// 系统提示词由 internal/prompt 按模型家族渲染（对账 src/prompt/static.ts，
// 与 TS 侧逐字节等价）。`--system` 可显式覆盖。

// appConfig 把 agent 配置与 client 配置捆在一起（凭证不进 agent.Config）。
type appConfig struct {
	Agent  agent.Config
	Client client.Config
}

// loadConfig 从 flag 与环境变量装配配置。
func loadConfig(model, baseURL, approval string, maxTurns int, systemPrompt string) (*appConfig, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取工作目录失败：%w", err)
	}

	apiKey := firstEnv("DEEPSEEK_API_KEY", "OPENAI_API_KEY", "RIVET_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf(
			"未找到 API 密钥。请设置 DEEPSEEK_API_KEY / OPENAI_API_KEY / RIVET_API_KEY 之一")
	}

	if model == "" {
		model = firstEnv("TIANSHU_MODEL")
	}
	if model == "" {
		model = "deepseek-v4-pro"
	}
	if baseURL == "" {
		baseURL = firstEnv("TIANSHU_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}

	// 系统提示词：未显式指定时按模型家族渲染（对账 TS buildSystemPrompt），
	// 并附加 frozen 稳定块（environment/sober/locus/project-instructions/...）。
	// 模型家族决定是否附加 calibration 片段——deepseek/mimo/glm 各有一段。
	if systemPrompt == "" {
		family := prompt.DetectModelFamily(model)
		systemPrompt = prompt.BuildFullSystemPrompt(
			prompt.Context{ModelFamily: family}, cwd, prompt.DetectHostEnv())
	}

	return &appConfig{
		Agent: agent.Config{
			Model:        model,
			MaxTokens:    8192,
			MaxTurns:     maxTurns,
			SystemPrompt: systemPrompt,
			Cwd:          cwd,
			ApprovalMode: approval,
		},
		Client: client.Config{
			BaseURL:  baseURL,
			APIKey:   apiKey,
			Model:    model,
			Provider: providerFromURL(baseURL),
			Retry: &retry.Options{
				// 10 分钟总预算（防无响应提供商上重试几十分钟）
				MaxTotalDurationMs: int64Ptr(10 * 60 * 1000),
			},
			Body: &api.ClientConfig{
				Model:     model,
				MaxTokens: 8192,
			},
		},
	}, nil
}

func buildLoop(app *appConfig, jsonOut bool) *agent.Loop {
	cl := client.New(app.Client)
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: app.Agent.Cwd})
	loop := agent.New(app.Agent, cl, reg)

	loop.Emit = func(e agent.Event) {
		if jsonOut {
			emitJSON(e)
			return
		}
		emitHuman(e)
	}
	return loop
}

// emitJSON 输出 JSON 事件（供脚本消费）。
func emitJSON(e agent.Event) {
	rec := map[string]any{
		"type": e.Kind,
		"turn": e.Turn,
	}
	if e.Text != "" {
		rec["text"] = e.Text
	}
	if e.ToolName != "" {
		rec["tool"] = e.ToolName
	}
	if e.ToolInput != nil {
		rec["input"] = e.ToolInput
	}
	if e.Kind == "tool_result" {
		rec["isError"] = e.IsError
	}
	if e.StopReason != "" {
		rec["stopReason"] = e.StopReason
	}
	if e.Usage != nil {
		rec["usage"] = map[string]any{
			"input_tokens":                e.Usage.InputTokens,
			"output_tokens":               e.Usage.OutputTokens,
			"cache_read_input_tokens":     e.Usage.CacheReadInputTokens,
			"cache_creation_input_tokens": e.Usage.CacheCreationInputTokens,
		}
	}
	// 单行 JSON（NDJSON），便于流式消费
	b, _ := json.Marshal(rec)
	fmt.Println(string(b))
}

// emitHuman 输出人类可读的事件。
func emitHuman(e agent.Event) {
	switch e.Kind {
	case "text":
		fmt.Print(e.Text)
	case "tool_start":
		fmt.Fprintf(os.Stderr, "\n[tool] %s %s\n", e.ToolName, compactJSON(e.ToolInput))
	case "tool_result":
		status := "ok"
		if e.IsError {
			status = "error"
		}
		fmt.Fprintf(os.Stderr, "[tool] %s → %s\n", e.ToolName, status)
	case "turn_end":
		fmt.Fprintln(os.Stderr)
	case "done":
		fmt.Println()
		if e.Usage != nil && e.Usage.InputTokens > 0 {
			hit := 0.0
			if e.Usage.InputTokens > 0 {
				hit = float64(e.Usage.CacheReadInputTokens) / float64(e.Usage.InputTokens) * 100
			}
			fmt.Fprintf(os.Stderr, "[usage] input=%d output=%d cache_read=%d cache_create=%d 命中率=%.1f%%\n",
				e.Usage.InputTokens, e.Usage.OutputTokens,
				e.Usage.CacheReadInputTokens, e.Usage.CacheCreationInputTokens, hit)
		}
	}
}

// compactJSON 把入参压成单行摘要。
func compactJSON(v map[string]any) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// reportError 输出错误（含可行动指引）。
func reportError(err error, jsonOut bool) {
	guidance := client.Guidance(err)
	if jsonOut {
		b, _ := json.Marshal(map[string]any{
			"type":     "error",
			"error":    err.Error(),
			"guidance": guidance,
		})
		fmt.Println(string(b))
		return
	}
	fmt.Fprintf(os.Stderr, "\n错误：%v\n", err)
	if guidance != "" {
		fmt.Fprintf(os.Stderr, "建议：%s\n", guidance)
	}
}

// firstEnv 返回第一个非空的环境变量值。
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// providerFromURL 从端点推断提供商名（用于能力与特化分支）。
func providerFromURL(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.Contains(lower, "deepseek"):
		return "deepseek"
	case strings.Contains(lower, "moonshot") || strings.Contains(lower, "kimi"):
		return "kimi"
	case strings.Contains(lower, "bigmodel") || strings.Contains(lower, "glm"):
		return "glm"
	case strings.Contains(lower, "minimax"):
		return "minimax"
	case strings.Contains(lower, "siliconflow"):
		return "siliconflow"
	case strings.Contains(lower, "openai"):
		return "openai"
	case strings.Contains(lower, "anthropic"):
		return "claude"
	default:
		return "openai" // OpenAI 兼容兜底
	}
}

func int64Ptr(i int64) *int64 { return &i }
