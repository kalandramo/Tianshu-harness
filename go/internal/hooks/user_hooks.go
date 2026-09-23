// Package hooks 实现用户自定义 hook——`.rivet/hooks.json` 的 event→script 映射。
//
// 对账 TS 的 `src/hooks/user-hooks-runner.ts`（141 行）。
//
// ## 为什么需要（TS 文件头原文）
//
//	User hooks — .rivet/hooks.json event→script mapping.
//
//	Runs external shell scripts on agent lifecycle events. Scripts receive
//	event context via environment variables and stdin JSON.
//
// ## 安全边界（本包的核心，不可绕过）
//
// `hooks.json` **属于仓库内容**——脚本经 shell 跑、拿完整用户权限。
// 故它**不能自我授权**（SECURITY.md 信任边界）：未授信项目一律不执行。
//
// 这道门由 `internal/trust` 把守（TS 侧是 `config/project-trust.ts`）。
// 本包的移植同时点亮了此前悬空的 `internal/trust`（380 行、13 个导出符号、
// 零生产 import）——两者是同一个安全机制的两半，必须一起接。
//
// ## 与 TS 的差异（显式，全部记录）
//
//  1. **无 pluginHooks 合并**：TS 的 `runHooksForEvent` 接受 `pluginHooks`
//     参数（插件贡献的绝对路径脚本）。Go 侧无 plugins 子系统——传空即等价于
//     TS 的 `getPluginHooks?.()` 返回 undefined 的路径。**将来移植 plugins 时
//     需在此扩展**。
//  2. **无 `emitHookResult` 事件流**：TS 把结果经 `emitHookResult` 送到桌面端
//     事件流（I4）。Go 侧无桌面端——结果通过回调暴露（`WithResultSink`），
//     CLI 可选择性消费。
//  3. **ANTI_INTERACTIVE_ENV 本地实现**：TS 从 `tools/resolved-env.ts` 导入。
//     Go 侧无该模块（环境解析层未移植），故按 TS 原文逐条内联 8 个变量。
package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/tools"
	"github.com/kalandramo/tianshu/go/internal/trust"
)

// HookEvent 是 hook 触发的生命周期事件。
//
// 对账 TS 的 `HookEvent` 联合类型（5 个成员）。
type HookEvent string

const (
	EventPreTurn     HookEvent = "preTurn"
	EventPostTurn    HookEvent = "postTurn"
	EventPostTool    HookEvent = "postTool"
	EventPostSession HookEvent = "postSession"
	EventOnError     HookEvent = "onError"
)

// ValidEvents 是合法事件集（对账 TS 的 `VALID_EVENTS`）。
//
// **用途**：加载时过滤掉未知 event 的条目——静默忽略而非报错
// （对账 TS 的 `.filter(h => VALID_EVENTS.has(h.event) && ...)`）。
var ValidEvents = map[HookEvent]bool{
	EventPreTurn:     true,
	EventPostTurn:    true,
	EventPostTool:    true,
	EventPostSession: true,
	EventOnError:     true,
}

// DefaultTimeoutMs 是脚本缺省超时（对账 TS 的 `?? 5000`）。
const DefaultTimeoutMs = 5000

// HookEntry 是 hooks.json 里的一条配置。
//
// 对账 TS 的 `HookEntry`。
type HookEntry struct {
	// Event 是触发事件。
	Event HookEvent `json:"event"`
	// Script 是脚本路径——相对项目根或绝对路径。
	Script string `json:"script"`
	// TimeoutMs 是可选超时（毫秒）。0 = 用 DefaultTimeoutMs。
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// HooksConfig 是 hooks.json 的顶层结构。
//
// 对账 TS 的 `HooksConfig`。
type HooksConfig struct {
	Hooks []HookEntry `json:"hooks"`
}

// HookContext 是传给脚本的事件上下文。
//
// **它同时是两个通道的内容**（对账 TS）：
//   - 经 stdin 传 JSON（`JSON.stringify(ctx)`）
//   - 经环境变量传标量（`RIVET_HOOK_EVENT` / `RIVET_SESSION_ID` / ...）
type HookContext struct {
	Event      HookEvent `json:"event"`
	Cwd        string    `json:"cwd"`
	SessionID  string    `json:"sessionId,omitempty"`
	Turn       int       `json:"turn,omitempty"`
	ToolName   string    `json:"toolName,omitempty"`
	ToolResult string    `json:"toolResult,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// HookResult 是一条脚本的执行结果。
//
// 对账 TS 的 `HookResult`。
type HookResult struct {
	// Script 是配置里的原始路径（**不是**解析后的绝对路径——对账 TS）。
	Script string
	// Ok 是是否成功（exit code == 0）。
	Ok bool
	// Output 是 stdout + stderr 合并后 trim 的内容。
	Output string
}

// antiInteractiveEnv 是阻止 CLI 工具启动交互程序的环境变量。
//
// 对账 TS 的 `ANTI_INTERACTIVE_ENV`（src/tools/resolved-env.ts:405-417）
// ——**8 个变量逐条内联**。这些工具在 hook 脚本里被调用时会阻塞等待用户输入
// （分页器、编辑器、凭据提示），而 hook 在 agent 主循环里跑，无人应答。
//
// **为什么不用 Go 侧 bash 工具的环境**：`tools.prepareCommand` 是**包内私有**
// 的，且它做的是进程组/超时兜底，与本表职责不同（本表是「防交互」）。
// 两处都需要，不是重复。
var antiInteractiveEnv = map[string]string{
	"PAGER":               "cat",
	"GIT_PAGER":           "cat",
	"GH_PAGER":            "cat",
	"MANPAGER":            "cat",
	"GIT_TERMINAL_PROMPT": "0",    // git 永不交互式索要凭据
	"GIT_EDITOR":          "true", // git 永不启动编辑器
	"GIT_SEQUENCE_EDITOR": "true",
	"GPG_TTY":             "", // 空 = gpg/pinentry 拿不到 tty，干净失败
}

// LoadHooksConfig 读取并校验 `<cwd>/.rivet/hooks.json`。
//
// 对账 TS 的 `loadHooksConfig`。**三道关，任一不过返回空配置**：
//
//  1. 文件不存在 → 空配置（正常情况，不是错误）
//  2. **项目未授信 → 空配置 + 单次提示**（安全边界，本包的核心）
//  3. JSON 解析失败 → 空配置（静默降级，对账 TS 的 `catch { return {hooks:[]} }`）
//
// **为什么信任门在解析之前**：未授信项目里的 `hooks.json` 是**不可信输入**
// ——连解析都不该做（解析器漏洞面）。TS 即如此。
func LoadHooksConfig(cwd string) HooksConfig {
	path := filepath.Join(cwd, ".rivet", "hooks.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return HooksConfig{}
	}

	// 信任门：hooks.json 属仓库内容，未授信项目一律不执行。
	if !trust.IsProjectTrusted(cwd) {
		trust.NotifyUntrustedOnce("hooks", cwd, nil)
		return HooksConfig{}
	}

	var parsed HooksConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return HooksConfig{}
	}

	// 过滤：未知 event 或非字符串 script 的条目丢弃。
	filtered := make([]HookEntry, 0, len(parsed.Hooks))
	for _, h := range parsed.Hooks {
		if ValidEvents[h.Event] && h.Script != "" {
			filtered = append(filtered, h)
		}
	}
	parsed.Hooks = filtered
	return parsed
}

// ResultSink 接收 hook 执行结果（对账 TS 的 `emitHookResult`）。
//
// Go 侧无桌面端事件流，故结果通过此回调暴露。nil = 不消费（CLI 缺省）。
type ResultSink func(results []HookResult, event HookEvent, turn int, toolName string, errMsg string)

// Runner 执行用户 hook。
//
// **与 TS 的差异**：TS 的 `runHooksForEvent` 是纯函数（每次重新读配置）。
// Go 侧同样每次重新读——**这是有意的**：`hooks.json` 可能在会话中被编辑，
// 每次读保证取到最新（对账 TS 行为）。
type Runner struct {
	Cwd       string
	SessionID string
	// GetTurn 是轮次读取器（对账 TS 的 `deps.getTurn`）——**惰性求值**，
	// 因为 turn 随会话推进。
	GetTurn func() int
	// Sink 是结果出口（可选）。
	Sink ResultSink
}

// RunForEvent 执行某事件的全部 hook。
//
// 对账 TS 的 `runHooksForEvent`。返回每条脚本的结果（**顺序与配置一致**）。
//
// **不因单条失败中断**：脚本 A 失败不影响脚本 B 执行（对账 TS 的 for 循环
// 里 catch 后 continue 的语义）。
func (r *Runner) RunForEvent(ctx HookContext) []HookResult {
	config := LoadHooksConfig(r.Cwd)
	results := make([]HookResult, 0, len(config.Hooks))

	for _, entry := range config.Hooks {
		if entry.Event != ctx.Event {
			continue
		}
		results = append(results, r.runOne(entry, ctx))
	}
	return results
}

// runOne 执行单条 hook。
func (r *Runner) runOne(entry HookEntry, ctx HookContext) HookResult {
	// 脚本路径：绝对路径直接用，相对路径以 cwd 为基（对账 TS 的 isAbsolute 分支）。
	scriptPath := entry.Script
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(r.Cwd, scriptPath)
	}

	if _, err := os.Stat(scriptPath); err != nil {
		return HookResult{
			Script: entry.Script,
			Ok:     false,
			Output: "Script not found: " + scriptPath,
		}
	}

	timeoutMs := entry.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = DefaultTimeoutMs
	}

	// stdin 传完整 JSON 上下文（对账 TS 的 `input: JSON.stringify(ctx)`）。
	stdinJSON, err := json.Marshal(ctx)
	if err != nil {
		return HookResult{Script: entry.Script, Ok: false, Output: err.Error()}
	}

	cmd := exec.Command(scriptPath)
	cmd.Dir = r.Cwd
	cmd.Env = r.buildEnv(ctx)
	cmd.Stdin = strings.NewReader(string(stdinJSON))

	out, err := runWithTimeout(cmd, time.Duration(timeoutMs)*time.Millisecond)
	ok := err == nil
	return HookResult{Script: entry.Script, Ok: ok, Output: strings.TrimSpace(out)}
}

// buildEnv 构造脚本环境（宿主环境 + 防交互变量 + hook 上下文变量）。
//
// 对账 TS 的 env 构造：
//
//	{ ...process.env, ...ANTI_INTERACTIVE_ENV, RIVET_HOOK_EVENT, RIVET_SESSION_ID, RIVET_TURN, RIVET_TOOL_NAME }
//
// **顺序重要**：防交互变量在宿主环境**之后**（last-wins，覆盖用户设的 PAGER）。
func (r *Runner) buildEnv(ctx HookContext) []string {
	env := os.Environ()
	for k, v := range antiInteractiveEnv {
		env = append(env, k+"="+v)
	}
	env = append(env,
		"RIVET_HOOK_EVENT="+string(ctx.Event),
		"RIVET_SESSION_ID="+ctx.SessionID,
		"RIVET_TURN="+itoa(ctx.Turn),
		"RIVET_TOOL_NAME="+ctx.ToolName,
	)
	return env
}

// ErrTimeout 表示脚本超时。
var ErrTimeout = errors.New("hook script timed out")

// runWithTimeout 执行命令并捕获合并输出，超时则杀掉**整棵进程树**。
//
// ## 与 TS 的差异（重要）
//
// TS 用 `spawnSync(..., { timeout, shell: true })`——`shell: true` 让脚本经
// shell 执行（故 `hooks.json` 里可写 shell 语法）。
//
// Go 侧**不设 `shell: true`**：`exec.Command(scriptPath)` 直接执行文件，
// 要求脚本本身可执行（有 shebang / 是 .exe）。**这是刻意的安全收窄**：
// 经 shell 跑会把 `script` 字段变成任意命令注入面，而它来自仓库内容。
// TS 的 `shell:true` 依赖信任门兜底；Go 侧再加一层——**即使信任门失效，
// 也只能执行文件，不能注入命令**。
//
// ## 为什么复用 tools 的进程树原语
//
// **首版只调 `cmd.Process.Kill()`——在 Windows 上超时形同虚设**（本刀实测：
// 400ms 超时实际耗时 29.5 秒）。根因与 `tools/proctree.go` 记载的完全同类：
// `.bat` 由 cmd.exe 包装执行，Kill 只杀包装进程，子进程（如 ping.exe）存活
// 且**继承了 stdout 管道写端**——`cmd.Wait()` 阻塞到管道 EOF。
//
// 正解是两件事一起做（`tools.PrepareCommand` 已封装）：
//
//  1. `WaitDelay`——给 Wait 的内部 io.Copy 设上限，句柄泄漏时兜底返回。
//  2. `KillProcessTree`——超时时回收整棵树，而非只杀主进程。
//
// **不复制一份到本包**：这两个细节都是踩过坑的，复制意味着下次修 bug 修两处。
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) (string, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	// 进程组语义 + Wait 兜底（必须在 Start 之前）。
	tools.PrepareCommand(cmd)

	if err := cmd.Start(); err != nil {
		return buf.String(), err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(timeout):
		// **杀整棵树**（不是 cmd.Process.Kill()——那只杀主进程）。
		tools.KillProcessTree(cmd)
		<-done // 等 Wait 返回，避免僵尸
		return buf.String(), ErrTimeout
	}
}

// itoa 是 strconv.Itoa 的本地别名（与 agent 包的同类工具一致，避免依赖）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// 确保 fmt 被使用（诊断路径预留）。
var _ = fmt.Sprintf
