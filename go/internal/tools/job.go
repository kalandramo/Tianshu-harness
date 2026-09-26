package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// job.go —— `job` 工具（第八十刀）。
//
// 对账 TS `src/tools/job-tool.ts`（120 行）。查看/控制由 `bash(run_in_background)`
// 启动的后台任务。
//
// # 为什么是独立工具而非 bash 的 action
//
// 后台任务的生命周期**脱离调用点**（这正是它的意义）——bash 调用早已返回，
// 而任务还在跑。所以控制面必须是独立工具（可跨轮调用），不能塞进 bash 的参数。
//
// # 文案纪律
//
// 本文件的所有输出字符串**逐字对账 TS**——它们进模型上下文，
// 措辞偏差会改变行为（同 `tddgate.go` 的既有纪律）。

// Job 创建 `job` 工具。
func Job() Tool { return &jobTool{} }

type jobTool struct{}

func (t *jobTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "job",
		Description: "查看和控制由 bash(run_in_background) 启动的后台任务。\n\n" +
			"Actions:\n" +
			"- list: 列出本会话所有后台任务（状态、已运行时长、最后一行输出）。\n" +
			"- await: 阻塞直到任务退出、输出命中 `pattern`（正则）或 `timeout` 到时。" +
			"依赖后台结果前先用它（例如等 dev server 输出 \"Ready\" 或 install 完成）。\n" +
			"- logs: 返回任务捕获的输出。\n" +
			"- kill: 终止运行中的任务。",
		// **必须用 objSchemaOrdered**：属性声明序对账 TS（action/id/pattern/timeout），
		// 键序不同会让工具定义变化打掉整个前缀缓存（helpers.go:24-28）。
		InputSchema: objSchemaOrdered([]string{"action", "id", "pattern", "timeout"}, map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []any{"list", "await", "logs", "kill"},
				"description": "要执行的操作",
			},
			"id":      strProp("任务 id（await/logs/kill 必填）"),
			"pattern": strProp("仅 await：对输出匹配的正则；命中即提前返回（如 \"Ready|listening|compiled\"）"),
			"timeout": map[string]any{
				"type":        "integer",
				"description": "仅 await：最长阻塞毫秒数（默认 120000，上限 600000）",
			},
		}, "action"),
	}
}

func (t *jobTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	jobs := p.Jobs
	if jobs == nil {
		// 无会话上下文 → 不是错误，是「换个方式做」。
		return contract.Result{
			Content: "后台任务系统在当前上下文不可用（无会话）。请直接前台运行命令。",
		}, nil
	}

	action, _ := p.Input["action"].(string)
	id := ""
	if v, ok := p.Input["id"]; ok && v != nil {
		id = fmt.Sprintf("%v", v)
	}

	switch action {
	case "list":
		list := jobs.List()
		if len(list) == 0 {
			return contract.Result{
				Content:   "当前没有后台任务。",
				UIContent: "后台任务: 0",
			}, nil
		}
		lines := make([]string, 0, len(list))
		running := 0
		for _, j := range list {
			if j.Status == JobRunning {
				running++
			}
			lines = append(lines, jobFmtLine(j))
		}
		return contract.Result{
			Content:   fmt.Sprintf("后台任务 (%d，运行中 %d):\n%s", len(list), running, strings.Join(lines, "\n")),
			UIContent: fmt.Sprintf("后台任务: %d (运行 %d)", len(list), running),
		}, nil

	case "await":
		if id == "" {
			return contract.Result{Content: "await 需要 id 参数。", IsError: true}, nil
		}
		pattern := ""
		if v, ok := p.Input["pattern"]; ok && v != nil {
			pattern = fmt.Sprintf("%v", v)
		}
		timeoutMs := jobToolAwaitMs(p)
		res, err := jobs.Await(id, JobAwaitOptions{Pattern: pattern, TimeoutMs: timeoutMs})
		if err != nil {
			return contract.Result{Content: "等待任务失败：" + err.Error(), IsError: true}, nil
		}
		if res == nil {
			return contract.Result{
				Content: fmt.Sprintf(`未找到任务 %s。用 job(action="list") 查看。`, id),
				IsError: true,
			}, nil
		}

		var verdict string
		switch {
		case res.Matched:
			verdict = "✓ 输出命中 pattern"
		case res.TimedOut:
			verdict = fmt.Sprintf("⏱ 等待超时（%s），任务仍在运行", jobFmtDuration(timeoutMs))
		default:
			verb := "退出"
			if res.Job.Status == JobKilled {
				verb = "被终止"
			}
			verdict = fmt.Sprintf("● 任务已%s (exit %s)", verb, jobFmtExitCode(res.Job))
		}
		header := fmt.Sprintf("[%s] %s · %s", res.Job.ID, verdict, jobFmtStatus(res.Job))
		content := header
		if res.Tail != "" {
			content = header + "\n── 输出尾部 ──\n" + res.Tail
		}
		ui := fmt.Sprintf("await %s: %s", res.Job.ID, jobAwaitUIVerdict(res))
		return contract.Result{Content: content, UIContent: ui}, nil

	case "logs":
		if id == "" {
			return contract.Result{Content: "logs 需要 id 参数。", IsError: true}, nil
		}
		logs := jobs.Logs(id)
		if logs == nil {
			return contract.Result{Content: fmt.Sprintf("未找到任务 %s。", id), IsError: true}, nil
		}
		body := *logs
		if body == "" {
			body = "(无输出)"
		}
		return contract.Result{Content: body, UIContent: "logs " + id}, nil

	case "kill":
		if id == "" {
			return contract.Result{Content: "kill 需要 id 参数。", IsError: true}, nil
		}
		ok := jobs.Kill(id)
		if ok {
			return contract.Result{
				Content:   fmt.Sprintf("已发送终止信号给任务 %s。", id),
				UIContent: "kill " + id,
			}, nil
		}
		return contract.Result{
			Content:   fmt.Sprintf("任务 %s 不存在或已结束。", id),
			UIContent: "kill " + id,
			IsError:   true,
		}, nil

	default:
		return contract.Result{
			Content: fmt.Sprintf("未知 action: %s。可用: list / await / logs / kill。", action),
			IsError: true,
		}, nil
	}
}

// jobToolAwaitMs 解析并钳制 await 的超时。
//
// 对账 TS：`Math.min(Number(input.timeout) || DEFAULT_AWAIT_MS, MAX_AWAIT_MS)`。
// **钳制是必须的**：pattern 永不命中时，无上限的 await 会把循环挂死。
func jobToolAwaitMs(p *CallParams) int64 {
	v, _ := p.Input["timeout"]
	n := int64(0)
	switch x := v.(type) {
	case float64:
		n = int64(x)
	case int:
		n = int64(x)
	case int64:
		n = x
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			n = parsed
		}
	}
	if n <= 0 {
		n = jobDefaultAwaitMs
	}
	if n > jobMaxAwaitMs {
		n = jobMaxAwaitMs
	}
	return n
}

// Timeout 返回工具级超时。
//
// 对账 TS：await action 返回 `t + 30_000`（超出 await 窗口给管线余量）；
// 其他 120_000。
//
// **为什么 await 要加余量**：await 是阻塞调用，工具级超时若等于 await 窗口，
// 管线会在 await 返回前先掐断——等于 await 永远拿不到结果。
func (t *jobTool) Timeout(p *CallParams) time.Duration {
	if p != nil {
		if action, _ := p.Input["action"].(string); action == "await" {
			return time.Duration(jobToolAwaitMs(p)+30_000) * time.Millisecond
		}
	}
	return 120 * time.Second
}

// RequiresApproval 恒 false——job 控制不产生新副作用（对账 TS `() => false`）。
func (t *jobTool) RequiresApproval(_ *CallParams) bool { return false }

// ConcurrencySafe 恒 true——list/logs 只读，await/kill 按 id 定位
// （对账 TS `isConcurrencySafe: () => true`）。
func (t *jobTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `isEnabled: () => true`）。
func (t *jobTool) Enabled() bool { return true }

// ── 格式化（对账 TS 的 fmtDuration / fmtStatus / fmtLine）─────────────────

// jobFmtDuration 把毫秒格式化成人类可读时长。
//
// 对账 TS `fmtDuration`：<60s → `${s}s`；否则 `${m}m${r}s`（r 为 0 时省略秒）。
func jobFmtDuration(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	r := s % 60
	if r != 0 {
		return fmt.Sprintf("%dm%ds", m, r)
	}
	return fmt.Sprintf("%dm", m)
}

// jobFmtStatus 格式化状态。
//
// 对账 TS `fmtStatus`：running → `running`；killed → `killed (exit N)`（有码时）；
// 否则 `exited (N)`（无码时 `exited (?)`）。
func jobFmtStatus(j JobSnapshot) string {
	switch j.Status {
	case JobRunning:
		return "running"
	case JobKilled:
		if j.ExitCode != nil {
			return fmt.Sprintf("killed (exit %d)", *j.ExitCode)
		}
		return "killed"
	default:
		return fmt.Sprintf("exited (%s)", jobFmtExitCode(j))
	}
}

// jobFmtExitCode 返回退出码的展示形式（无码 → "?"）。
func jobFmtExitCode(j JobSnapshot) string {
	if j.ExitCode == nil {
		return "?"
	}
	return strconv.Itoa(*j.ExitCode)
}

// jobFmtLine 格式化一行列表项。
//
// 对账 TS `fmtLine`：
//
//	`[${id}] ${status} · ${elapsed} · ${command}` + （有 lastLine 时 `\n    └ ${lastLine}`）
func jobFmtLine(j JobSnapshot) string {
	end := j.EndedAt
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	elapsed := jobFmtDuration(end - j.StartedAt)
	head := fmt.Sprintf("[%s] %s · %s · %s", j.ID, jobFmtStatus(j), elapsed, j.Command)
	if j.LastLine != "" {
		return head + "\n    └ " + j.LastLine
	}
	return head
}

// jobAwaitUIVerdict 返回 await 的 UI 摘要（对账 TS 的 uiContent 三元）。
func jobAwaitUIVerdict(res *JobAwaitResult) string {
	switch {
	case res.Matched:
		return "命中"
	case res.TimedOut:
		return "超时"
	default:
		return jobFmtStatus(res.Job)
	}
}
