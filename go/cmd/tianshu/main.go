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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kalandramo/tianshu/go/internal/agent"
	"github.com/kalandramo/tianshu/go/internal/api"
	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/client"
	ctxstore "github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/retry"
	"github.com/kalandramo/tianshu/go/internal/session"
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
		err := loop.Run(ctx, *prompt)
		// 收尾 flush：确保不留未写尾部（落盘失败不改变退出码）
		loop.FlushSession()
		if err != nil {
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
	// 会话结束：排空落盘缓冲
	loop.FlushSession()
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
	// 会话 ID：启用状态容器 + 持久化（缺省时 Loop.State/Persist 恒为 nil）
	if app.Agent.SessionID == "" {
		app.Agent.SessionID = session.NewID()
	}
	loop := agent.New(app.Agent, cl, reg)

	// ── artifact store 装配 ──
	//
	// 对账 TS `loop.ts:846-847`：
	//   `new ArtifactStore(join(cwd, '.rivet', 'artifacts'), sessionId)`
	//
	// **为什么必须在 CLI 装**：大工具结果的落盘与召回（read_section）走的是
	// 这条真实会话路径。不装的话 `context_collapse.go` 里的 artifact 分支
	// 永远不触发（那是已记录的架构欠账）。
	//
	// 顺带回收超期会话目录（对账 TS `bootstrap.ts:2131` 的
	// `cleanupOldArtifactSessions`）——失败不影响会话（只回收磁盘）。
	artifactDir := filepath.Join(app.Agent.Cwd, ".rivet", "artifacts")
	loop.Artifacts = artifact.NewStore(artifactDir, app.Agent.SessionID, artifact.Options{})
	if cleaned := artifact.CleanupOldSessions(artifactDir, app.Agent.SessionID); cleaned > 0 {
		fmt.Fprintf(os.Stderr, "已回收 %d 个过期 artifact 会话目录\n", cleaned)
	}

	// ── CVM 装配：hook 管线 + 劝导总线 + claim store ──
	//
	// **为什么必须在 CLI 装**：hook 与 advisory 的逻辑再完备，不在这里装配
	// 就完全不生效——真实会话走的是这条路径，不是测试里的手工注入。
	//
	// 装配四件：
	//   1. AdvisoryBus——hook 投递的出口 + 渲染成 <星域-advisory> 块
	//   2. Pipeline——五阶段 hook 管线（当前注册两个真实 hook）
	//   3. ClaimStore——consistency-check 的真实 claim 来源（认知层）
	//   4. Loop.Hooks / Loop.Advisories / Loop.Effects——接进主循环
	//
	// 对账 TS 的 loop-factory / create-runtime-hooks 装配路径
	// （TS 侧默认装配 ~18+ hook；这里只装已移植的两个）。
	bus := agent.NewAdvisoryBus()
	pipeline := agent.NewPipeline(agent.PipelineOptions{})
	pipeline.Register(agent.NewTypecheckReminderHook(bus))

	// claim store：落盘到 <cwd>/.rivet/claims/<sessionId>.claims.jsonl。
	//
	// **构造失败降级为空集**（不阻塞会话）——consistency-check 退化为 no-op，
	// 其余功能不受影响。这是**显式降级**（错误走 stderr 可见）。
	var claimStore *ctxstore.ClaimStore
	claimsDir := filepath.Join(app.Agent.Cwd, ".rivet", "claims")
	if err := os.MkdirAll(claimsDir, 0o755); err == nil {
		cs, csErr := ctxstore.NewClaimStore(claimsDir, app.Agent.SessionID)
		if csErr == nil {
			claimStore = cs
		} else {
			fmt.Fprintf(os.Stderr, "claim store 不可用（consistency-check 降级）：%v\n", csErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "claim 目录不可建（consistency-check 降级）：%v\n", err)
	}

	pipeline.Register(agent.NewConsistencyCheckHook(func() []agent.FileObservation {
		if claimStore == nil {
			return nil // 显式降级：无 store 即无观察
		}
		// 对账 TS loop-factory.ts:691 的
		// `contextClaimStore?.listClaims({ kind: ['file_observation'] })`
		claims := claimStore.ListClaims(nil, []ctxstore.ContextClaimKind{ctxstore.ClaimFileObservation}, nil)
		out := make([]agent.FileObservation, 0, len(claims))
		for _, c := range claims {
			fo := agent.FileObservation{ID: c.ID, Text: c.Text}
			for _, e := range c.Evidence {
				fo.Evidence = append(fo.Evidence, agent.EvidenceRef{Path: e.Path})
			}
			out = append(out, fo)
		}
		return out
	}))

	loop.Hooks = pipeline
	loop.Advisories = bus

	// readback：劝导采纳率台账（核销闭环）。
	//
	// 对账 TS loop-factory 的 `new AdvisoryReadback()` 装配。四个调用点已在
	// loop 里接好（render 后 Track / postTool ObserveTool / postTurn Evaluate /
	// postSession FlushAtSessionEnd）。
	//
	// **pattern_absent 谓词需要文件读取器**——注入 os.ReadFile（读失败返回空串，
	// 对账 TS defaultReadFile 的 null 分支 → 视为「模式消失」满足）。
	readback := agent.NewAdvisoryReadback()
	readback.SetReadFile(func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(b)
	})
	loop.Readback = readback

	// 习惯化对抗接线——**这是 readback 的下游消费者**。
	//
	// 对账 TS loop-factory 的 `bus.setHabituationPolicy(readback)`。
	// bus 据此做两级反应：streak >= 2 升级措辞、streak >= 3 有界静音。
	//
	// **接口精确匹配**——readback 的 `GetIgnoredStreak(key) int` 直接满足
	// `agent.HabituationPolicy`，无需适配器。
	bus.SetHabituationPolicy(readback)

	// lift 消费接线——**成熟 lift 的第二层治理**。
	//
	// 对账 TS loop.ts:803：
	//   `this.advisoryBus.setLiftProvider(key => this.advisoryReadback.getMatureLift(key))`
	//
	// 与习惯化（看「连续被忽略」的行为层信号）不同，lift 看的是**反事实基线**：
	// 投递组采纳率 减 扣留组自发完成率。lift <= 0 = 「没提醒模型也会做」→ 纯噪音
	// → 静音 10 个渲染周期（比习惯化的 4 长——反事实证据更可靠）。
	//
	// **成熟度门在 readback 内部**：decided < 5 或 shadowHeld < 3 返回 nil，
	// bus 视为中性、不静音。这避免了冷启动阶段误杀有效提醒。
	bus.SetLiftProvider(readback.GetMatureLift)

	// holdout 反事实抽样——**shadow 样本的产生端**（对账 loop.ts:773）。
	//
	// 这是 lift 消费的**数据源**：没有它，readback 的 shadowHeld/shadowSatisfied
	// 永远为 0，GetMatureLift 恒返回 nil（成熟度门未过）→ 负 lift 静音永不触发。
	//
	// **为什么需要反事实**：采纳率度量的是**相关性**——「送达后 2 轮内出现验证」
	// 可能只是模型本来就要做。按小概率静默扣留（不渲染但照常核销）得到「没提醒
	// 也会做」的基线，lift = 投递组采纳率 - 扣留组自发完成率，才是**因果**增益。
	//
	// 资格门（HOLDOUT_MIN_DELIVERED=3）：冷 key 先积累投递组基数——没有足够的
	// 投递组样本，差值的分母为零。
	//
	// `RIVET_ADVISORY_HOLDOUT=0` 可关闭抽样（缺省 0.1）。
	// T7 效力排序——**让效力跨 priority 参与预算竞争**（对账 loop.ts:810-820）。
	//
	// **为什么需要**：此前效力只在 priority 完全相等时做 tie-break，而 hook 的
	// priority 高度分散——实测 self-verify 采纳 77% 却因 0.58 < 0.70 恒输给
	// 采纳 12% 的 todo-missing。**「说了有人听」的提醒应该赢得预算**。
	//
	// 信号优先级：成熟 lift（已过样本门，故满置信）→ 回退采纳率（按决出样本数
	// 缩放置信度，避免单样本改写优先级）。
	//
	// `RIVET_ADVISORY_EFFICACY_SPAN=0` 关闭调整（回退纯 priority 排序）。
	bus.SetEfficacySpan(agent.ParseEfficacySpan(os.Getenv("RIVET_ADVISORY_EFFICACY_SPAN")))
	bus.SetEfficacySignalProvider(func(key string) *agent.EfficacySignal {
		if lift := readback.GetMatureLift(key); lift != nil {
			return &agent.EfficacySignal{Score: (*lift + 1) / 2, Confidence: 1}
		}
		rate := readback.GetAdoptionRate(key)
		if rate == nil {
			return nil
		}
		decided := float64(readback.GetDecidedCount(key))
		conf := decided / agent.EfficacyConfidentSamples
		if conf > 1 {
			conf = 1
		}
		return &agent.EfficacySignal{Score: *rate, Confidence: conf}
	})
	// 次级排序键的回退源（priority 完全平手时用采纳率）
	bus.SetAdoptionRateProvider(readback.GetAdoptionRate)

	// W2 efficacy 负反馈环——**发射前回读会话内 delivered/adopted**（对账 loop.ts:792）。
	//
	// 同 key 零采纳连发 3 次后冷却翻倍、6 次后**会话内静默**。
	//
	// **与习惯化静音互补**（TS 注释原文）：习惯化依赖 `ignoredStreak`，而它依赖
	// expect 谓词——**无 expect 的 key（如 convergence 的多数变体）ignored 永远
	// 是 0，只有这条环能拦住它**。
	//
	// **注意口径**：这里用的是**会话内统计**（不含跨会话先验）——负反馈环看的是
	// 「本次会话里说了几次没人听」，与 T7 排序（含先验）不同。
	bus.SetEfficacyStatsProvider(func(key string) *agent.EfficacyStats {
		s, ok := readback.Stats()[key]
		if !ok {
			return nil
		}
		return &agent.EfficacyStats{Delivered: s.Delivered, Adopted: s.Adopted}
	})

	// 跨会话效能信息素——**先验的加载端**（对账 loop.ts:779-790）。
	//
	// 这是 lift / holdout 资格 / 副驾闸门的**冷启动数据源**：readback 的 per-key
	// 统计随会话死亡，没有先验时每个新会话都要从零攒（holdout 资格需送达 >= 3，
	// 成熟 lift 需 decided >= 5 且 shadow >= 3）——真实使用中会话往往没那么长。
	//
	// 加载 → 按 14 天半衰期 EWMA 衰减 → 剔除衰减殆尽的 key → 播种给 readback。
	// **写回**在 loop 内自动进行（每 20 轮 + 会话结束），此处只需装配 store。
	efficacyStore := agent.NewAdvisoryEfficacyStore(app.Agent.Cwd)
	loop.EfficacyStore = efficacyStore
	loop.SeedEfficacyPriors()

	bus.SetHoldoutPolicy(agent.HoldoutPolicy{
		Rate: agent.ParseHoldoutRate(os.Getenv("RIVET_ADVISORY_HOLDOUT")),
		IsEligible: func(key string) bool {
			return readback.GetDeliveredCount(key) >= agent.HoldoutMinDelivered
		},
	})

	// claim 提取器装配——**这是 claim 的产生端**。
	//
	// 对账 TS 的工具执行后提取：`extractClaimsFromToolResult(ctx, meta)` →
	// `claimStore.propose(proposal)`。
	//
	// **去重**：对 read_file，已观察过的路径不再重复提取（对账 TS 的
	// `existingFileObservations` 参数）。
	if claimStore != nil {
		turnCounter := 0
		loop.OnToolResult = func(ev *agent.RuntimeToolEvent, turn int) {
			if turn != turnCounter {
				turnCounter = turn
			}
			// 已有的 file_observation 路径集（去重）
			existing := map[string]bool{}
			for _, c := range claimStore.ListClaims(nil, []ctxstore.ContextClaimKind{ctxstore.ClaimFileObservation}, nil) {
				for _, e := range c.Evidence {
					if e.Path != "" {
						existing[e.Path] = true
					}
				}
			}

			proposals := ctxstore.ExtractClaimsFromToolResult(
				ctxstore.ToolResultContext{
					ToolName: ev.Name,
					Input:    ev.Input,
					Result:   ev.ResultContent,
					IsError:  ev.IsError,
				},
				ctxstore.ClaimExtractionMeta{
					SessionID: app.Agent.SessionID,
					Turn:      turn,
					EventID:   fmt.Sprintf("t%d:%s", turn, ev.Name),
				},
				existing,
				nowMs(),
			)
			for _, p := range proposals {
				if _, err := claimStore.Propose(p); err != nil {
					fmt.Fprintf(os.Stderr, "claim 提取落盘失败：%v\n", err)
				}
			}
		}
	}

	// effects：claim 过期标记的真实落点。
	//
	// 对账 TS tool-execution.ts:719 的 markClaimStale——
	// `updateClaimStatus(claimId, 'stale', \`invalidated by ${tool} on ${target}\`)`。
	// Go 侧 reason 格式由 hook 侧传入（见 consistency_check.go 的 effect 调用）。
	loop.Effects = agent.RuntimeHookEffects{
		MarkClaimStale: func(claimID string) {
			if claimStore == nil {
				return
			}
			if _, err := claimStore.UpdateClaimStatus(
				claimID, ctxstore.StatusStale, "invalidated by file write", nowMs()); err != nil {
				fmt.Fprintf(os.Stderr, "标记 claim 过期失败：%v\n", err)
			}
		},
	}

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

// nowMs 返回当前 epoch 毫秒。
func nowMs() int64 { return time.Now().UnixMilli() }
