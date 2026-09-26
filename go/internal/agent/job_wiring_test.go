package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// job_wiring_test.go —— bash → job 全链路端到端（第八十刀）。
//
// # 为什么必须有这个文件
//
// 单测 `jobstore_test.go` 与 `job_test.go` 各自绿，**不等于接线绿**——
// 中间还隔着「Loop 创建 SessionJobs」→「buildToolCallParams 注入」→
// 「bash 走后台分支」三道接线。任一环断了，单测都发现不了。
//
// 这正是本仓库栽过的模式（`NeedsApproval`/`CheckPlanMode`/`evidenceTracker`
// 读取端）：**实现存在但零消费**。
//
// # 真实依赖，不 mock
//
// 用真 `Loop`（`New`）+ 真 registry + 真 shell 子进程——`task-depth=wiring`
// 要求「实例化真实依赖」。mock 的 job registry 会让「注入没接上」这类
// 缺陷静默通过。

// newJobTestLoop 构造带 SessionJobs 的真 Loop。
func newJobTestLoop(t *testing.T, sessionID string) *Loop {
	t.Helper()
	dir := t.TempDir()
	l := New(Config{Cwd: dir, SessionID: sessionID}, nil, tools.NewRegistry())
	l.State = session.New(sessionID)
	return l
}

// TestJobWiringLoopCreatesSessionJobs —— **接线①**：Loop 在 sessionId 非空时创建 SessionJobs。
//
// 对账 TS `loop.ts:850` 的 `if (this.config.sessionId) this._jobs = new SessionJobs(...)`。
func TestJobWiringLoopCreatesSessionJobs(t *testing.T) {
	withSession := newJobTestLoop(t, "job-wiring-1")
	if withSession.Jobs == nil {
		t.Fatal("有 sessionId 时 Loop 应创建 SessionJobs（对账 TS loop.ts:850）")
	}
	// 无 sessionId → nil（bash 退回前台，TS 同语义）。
	without := New(Config{Cwd: t.TempDir()}, nil, tools.NewRegistry())
	if without.Jobs != nil {
		t.Error("无 sessionId 时 Jobs 应为 nil（TS 侧 getJobs 返回 undefined）")
	}
}

// TestJobWiringCallParamsInjectsJobs —— **接线②**：buildToolCallParams 注入 Jobs。
//
// 这是最容易漏的一环——`Artifacts` 就是栽在这里（字段存在但零赋值点）。
func TestJobWiringCallParamsInjectsJobs(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-2")

	p := l.buildToolCallParams(toolCall{name: "bash", input: map[string]any{"command": "true"}})
	if p.Jobs == nil {
		t.Fatal("buildToolCallParams 应注入 Jobs（否则 bash 永远走前台）")
	}
	if p.Jobs != l.Jobs {
		t.Error("注入的应是 Loop 自己的 SessionJobs（同一实例）")
	}
}

// TestJobWiringBashBackgroundEndToEnd —— **接线③ + 全链路**：bash 转后台 → job 工具看到它。
//
// 走真 registry.Execute（真门链 + 真工具），断言：
//  1. bash 返回含 `[job:<id>]` 的后台标记（而非前台输出）
//  2. job 工具能 list 到它
//  3. job 工具能 logs 到它的输出
//  4. job 工具能 kill 它
func TestJobWiringBashBackgroundEndToEnd(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-3")
	l.registry.Register(tools.Bash(l.cfg.Cwd))
	l.registry.Register(tools.Job())

	// ① 显式 run_in_background=true → 应转后台。
	res := l.executeTool(context.Background(), toolCall{
		name: "bash",
		input: map[string]any{
			"command":           "echo bg-marker; sleep 30",
			"run_in_background": true,
		},
	})
	if res.IsError {
		t.Fatalf("bash 后台启动不应报错：%s", res.Content)
	}
	if !strings.Contains(res.Content, "[job:") {
		t.Fatalf("应返回后台标记 [job:<id>]，实得：%q", res.Content)
	}

	// 提取 id。
	i := strings.Index(res.Content, "[job:")
	rest := res.Content[i+len("[job:"):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatalf("后台标记格式异常：%q", res.Content)
	}
	jobID := rest[:j]
	if len(jobID) != 8 {
		t.Errorf("job id 应为 8 字符，实得 %q", jobID)
	}

	// ② job list 应看到它。
	listRes := l.executeTool(context.Background(), toolCall{
		name: "job", input: map[string]any{"action": "list"},
	})
	if listRes.IsError {
		t.Fatalf("job list 不应报错：%s", listRes.Content)
	}
	if !strings.Contains(listRes.Content, jobID) {
		t.Errorf("list 应含刚启动的 job %s，实得：%q", jobID, listRes.Content)
	}
	if !strings.Contains(listRes.Content, "running") {
		t.Errorf("刚启动的 job 应显示 running，实得：%q", listRes.Content)
	}

	// ③ 等输出到达后 logs 应能看到。
	deadline := time.Now().Add(10 * time.Second)
	var logsOK bool
	for time.Now().Before(deadline) {
		lg := l.executeTool(context.Background(), toolCall{
			name: "job", input: map[string]any{"action": "logs", "id": jobID},
		})
		if strings.Contains(lg.Content, "bg-marker") {
			logsOK = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !logsOK {
		t.Error("logs 应能看到后台命令的输出（bg-marker）")
	}

	// ④ kill 应终止它。
	killRes := l.executeTool(context.Background(), toolCall{
		name: "job", input: map[string]any{"action": "kill", "id": jobID},
	})
	if killRes.IsError {
		t.Fatalf("kill 运行中 job 不应报错：%s", killRes.Content)
	}
	if !strings.Contains(killRes.Content, "已发送终止信号") {
		t.Errorf("kill 应返回成功文案，实得：%q", killRes.Content)
	}
	// 终态应为 killed（给 kill 一点时间生效）。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := l.Jobs.Snapshot(jobID); s != nil && s.Status != tools.JobRunning {
			if s.Status != tools.JobKilled {
				t.Errorf("kill 后状态应为 killed，实得 %q", s.Status)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestJobWiringBashAwaitPattern —— 全链路 await：等后台命令输出 "Ready"。
//
// 这是 job 工具最主要的用法（等 dev server 就绪），必须端到端验证。
func TestJobWiringBashAwaitPattern(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-4")
	l.registry.Register(tools.Bash(l.cfg.Cwd))
	l.registry.Register(tools.Job())

	res := l.executeTool(context.Background(), toolCall{
		name: "bash",
		input: map[string]any{
			"command":           "sleep 0.3; echo Ready; sleep 30",
			"run_in_background": true,
		},
	})
	if res.IsError {
		t.Fatalf("后台启动失败：%s", res.Content)
	}
	i := strings.Index(res.Content, "[job:")
	rest := res.Content[i+len("[job:"):]
	jobID := rest[:strings.Index(rest, "]")]

	// await pattern=Ready，超时给足。
	aw := l.executeTool(context.Background(), toolCall{
		name: "job",
		input: map[string]any{
			"action": "await", "id": jobID, "pattern": "Ready", "timeout": 15000,
		},
	})
	if aw.IsError {
		t.Fatalf("await 不应报错：%s", aw.Content)
	}
	if !strings.Contains(aw.Content, "✓ 输出命中 pattern") {
		t.Errorf("await 应报告 pattern 命中，实得：%q", aw.Content)
	}
	if !strings.Contains(aw.Content, "Ready") {
		t.Errorf("await 应含命中输出，实得：%q", aw.Content)
	}

	l.Jobs.KillAll()
}

// TestJobWiringBashExplicitFalseStaysForeground —— 显式 false 强制前台（**即使命令匹配长跑模式**）。
//
// 对账 TS `bash.ts:489-490`：`explicitBg !== false && isLongRunner(...)`
// ——显式 false 时**不**自动后台化。
//
// **这条是「自动检测」与「显式控制」的边界**，最容易被实现成无条件后台化。
//
// **命令必须匹配长跑模式**（本测试曾被变异 M4 打红 0 暴露为覆盖缺口）：
// 若命令不匹配 `isLongRunner`，那么「忽略显式 false」的缺陷不会改变结果
// ——wantBackground 本来就是 false（因为 IsLongRunner=false），测试抓不到。
// 故命令文本含 `vite`（在 shell 注释里，既匹配模式又不真起 dev server）。
func TestJobWiringBashExplicitFalseStaysForeground(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-5")
	l.registry.Register(tools.Bash(l.cfg.Cwd))

	// 先自证前提：该命令**确实**匹配长跑模式（否则本测试退化为恒真）。
	cmd := "echo foreground-marker # vite"
	if !tools.IsLongRunner(cmd) {
		t.Fatalf("测试前提不成立：%q 应匹配长跑模式（否则抓不到「忽略显式 false」的缺陷）", cmd)
	}

	res := l.executeTool(context.Background(), toolCall{
		name: "bash",
		input: map[string]any{
			"command":           cmd,
			"run_in_background": false,
		},
	})
	if strings.Contains(res.Content, "[job:") {
		t.Errorf("显式 run_in_background=false 应前台执行（即使命令匹配长跑模式），实得后台标记：%q", res.Content)
	}
	if !strings.Contains(res.Content, "foreground-marker") {
		t.Errorf("前台执行应返回命令输出，实得：%q", res.Content)
	}
}

// TestJobWiringLongRunnerAutoBackgrounds —— 长跑命令**自动**转后台（无需显式传参）。
//
// 对账 TS `isLongRunner`：`npm run dev` 等模式自动后台化。
func TestJobWiringLongRunnerAutoBackgrounds(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-6")
	l.registry.Register(tools.Bash(l.cfg.Cwd))

	// `npm run dev` 匹配长跑模式。用 `true` 冒充 npm（避免依赖真 npm 与网络）——
	// 关键是**命令文本**匹配模式，进而触发自动后台化。
	res := l.executeTool(context.Background(), toolCall{
		name:  "bash",
		input: map[string]any{"command": "npm run dev --version 2>/dev/null || true"},
	})
	if !strings.Contains(res.Content, "[job:") {
		t.Errorf("长跑命令应自动转后台（对账 TS isLongRunner），实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "已自动转入后台") {
		t.Errorf("自动后台化应标明「已自动转入后台」，实得：%q", res.Content)
	}

	l.Jobs.KillAll()
}

// TestJobWiringNoSessionFallsThroughForeground —— 无会话时 bash 退回前台。
//
// 对账 TS：`if (wantBackground && params.jobs)` —— jobs 为 undefined 时
// **不**进后台分支，走前台同步执行。
func TestJobWiringNoSessionFallsThroughForeground(t *testing.T) {
	l := New(Config{Cwd: t.TempDir()}, nil, tools.NewRegistry())
	l.registry.Register(tools.Bash(l.cfg.Cwd))
	l.registry.Register(tools.Job())

	res := l.executeTool(context.Background(), toolCall{
		name: "bash",
		input: map[string]any{
			"command":           "echo no-session-marker",
			"run_in_background": true, // 显式要后台，但无会话
		},
	})
	if strings.Contains(res.Content, "[job:") {
		t.Errorf("无会话时应退回前台（TS 同语义），实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "no-session-marker") {
		t.Errorf("应前台执行并返回输出，实得：%q", res.Content)
	}
}

// TestJobWiringJobToolWithoutSessionGraceful —— 无会话时 job 工具给引导而非报错。
func TestJobWiringJobToolWithoutSessionGraceful(t *testing.T) {
	l := New(Config{Cwd: t.TempDir()}, nil, tools.NewRegistry())
	l.registry.Register(tools.Job())

	res := l.executeTool(context.Background(), toolCall{
		name: "job", input: map[string]any{"action": "list"},
	})
	if res.IsError {
		t.Errorf("无会话不应报错，实得：%q", res.Content)
	}
	if !strings.Contains(res.Content, "不可用") {
		t.Errorf("应提示后台任务系统不可用，实得：%q", res.Content)
	}
}

// TestJobWiringFlushSessionKillsJobs —— 会话收尾终止所有后台 job（防孤儿）。
//
// 对账 TS `killAll()`——不这么做的话 dev server 会活过会话、占住端口。
func TestJobWiringFlushSessionKillsJobs(t *testing.T) {
	l := newJobTestLoop(t, "job-wiring-7")
	l.registry.Register(tools.Bash(l.cfg.Cwd))

	res := l.executeTool(context.Background(), toolCall{
		name:  "bash",
		input: map[string]any{"command": "sleep 30", "run_in_background": true},
	})
	if !strings.Contains(res.Content, "[job:") {
		t.Fatalf("应转后台，实得：%q", res.Content)
	}

	l.FlushSession()

	if l.Jobs.HasRunning() {
		t.Error("FlushSession 后不应有运行中的 job（防孤儿）")
	}
}

// TestJobWiringJobLogDirUnderRivetArtifacts —— 日志目录落在 .rivet/artifacts/jobs。
//
// 对账 TS `loop.ts:847,850` 的 `join(artifactDir, 'jobs')`。
func TestJobWiringJobLogDirUnderRivetArtifacts(t *testing.T) {
	dir := t.TempDir()
	l := New(Config{Cwd: dir, SessionID: "job-wiring-8"}, nil, tools.NewRegistry())
	l.registry.Register(tools.Bash(dir))

	res := l.executeTool(context.Background(), toolCall{
		name:  "bash",
		input: map[string]any{"command": "echo logdir-marker", "run_in_background": true},
	})
	i := strings.Index(res.Content, "[job:")
	if i < 0 {
		t.Fatalf("应转后台，实得：%q", res.Content)
	}
	rest := res.Content[i+len("[job:"):]
	jobID := rest[:strings.Index(rest, "]")]

	// 等日志落盘。
	wantPath := filepath.Join(dir, ".rivet", "artifacts", "jobs", jobID+".log")
	deadline := time.Now().Add(10 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(wantPath); err == nil && strings.Contains(string(b), "logdir-marker") {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Errorf("日志应落在 %s 且含输出", wantPath)
	}

	l.Jobs.KillAll()
}
