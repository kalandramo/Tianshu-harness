package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// job_test.go —— `job` 工具（第八十刀）。
//
// 对账 TS `src/tools/job-tool.ts`。**文案逐字对账**——工具输出进模型上下文，
// 措辞偏差会改变行为（本仓库的既定纪律：用户可见字符串必须逐字取自源）。

// runJobTool 执行一次 job 工具调用。
func runJobTool(t *testing.T, jobs JobRegistry, input map[string]any) (content string, isErr bool) {
	t.Helper()
	res, err := Job().Execute(context.Background(), &CallParams{
		Input: input,
		Jobs:  jobs,
	})
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	return res.Content, res.IsError
}

// TestJobToolNoRegistryGraceful —— 无 registry 时不报错，提示前台运行。
//
// 对账 TS：`if (!jobs) return { content: '后台任务系统在当前上下文不可用（无会话）。
// 请直接前台运行命令。', isError: false }`。
//
// **为什么 isError=false**：这不是错误，是「当前上下文不支持」——
// 报 error 会让模型以为工具坏了，而非「换个方式做」。
func TestJobToolNoRegistryGraceful(t *testing.T) {
	content, isErr := runJobTool(t, nil, map[string]any{"action": "list"})
	if isErr {
		t.Errorf("无 registry 不应 isError=true，实得 content=%q", content)
	}
	want := "后台任务系统在当前上下文不可用（无会话）。请直接前台运行命令。"
	if content != want {
		t.Errorf("文案应逐字对账 TS：\n实得 %q\n期望 %q", content, want)
	}
}

// TestJobToolListEmpty —— 无任务时的文案。
//
// 对账 TS：`'当前没有后台任务。'` + `uiContent: '后台任务: 0'`。
func TestJobToolListEmpty(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "list"})
	if isErr {
		t.Errorf("空列表不应 isError，实得 %q", content)
	}
	want := "当前没有后台任务。"
	if content != want {
		t.Errorf("文案应逐字对账：实得 %q 期望 %q", content, want)
	}
}

// TestJobToolListFormat —— 列表格式（含计数与状态行）。
//
// 对账 TS：`后台任务 (${list.length}，运行中 ${running}):\n${body}`；
// 每行 `[${id}] ${status} · ${elapsed} · ${command}`（有 lastLine 时附 `    └ `）。
func TestJobToolListFormat(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	content, isErr := runJobTool(t, jobs, map[string]any{"action": "list"})
	if isErr {
		t.Fatalf("不应 isError，实得 %q", content)
	}
	if !strings.HasPrefix(content, "后台任务 (1，运行中 1):") {
		t.Errorf("表头应为「后台任务 (1，运行中 1):」，实得 %q", content)
	}
	if !strings.Contains(content, "["+snap.ID+"]") {
		t.Errorf("应含 job id，实得 %q", content)
	}
	if !strings.Contains(content, "running") {
		t.Errorf("应含 running 状态，实得 %q", content)
	}
	if !strings.Contains(content, "sleep 30") {
		t.Errorf("应含命令，实得 %q", content)
	}
}

// TestJobToolAwaitNeedsID —— await 缺 id 报错。
//
// 对账 TS：`'await 需要 id 参数。'`。
func TestJobToolAwaitNeedsID(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "await"})
	if !isErr {
		t.Error("缺 id 应 isError")
	}
	if content != "await 需要 id 参数。" {
		t.Errorf("文案应逐字对账：实得 %q", content)
	}
}

// TestJobToolAwaitUnknownID —— 未知 id 的文案含「未找到任务」+ 引导。
//
// 对账 TS：`未找到任务 ${id}。用 job(action="list") 查看。`。
func TestJobToolAwaitUnknownID(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "await", "id": "nope"})
	if !isErr {
		t.Error("未知 id 应 isError")
	}
	want := `未找到任务 nope。用 job(action="list") 查看。`
	if content != want {
		t.Errorf("文案应逐字对账：\n实得 %q\n期望 %q", content, want)
	}
}

// TestJobToolAwaitMatchedVerdict —— pattern 命中的判定行。
//
// 对账 TS：matched → `✓ 输出命中 pattern`；header = `[${id}] ${verdict} · ${status}`。
func TestJobToolAwaitMatchedVerdict(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "sleep 0.2; echo Ready",
		RawCommand: "serve",
		Cwd:        t.TempDir(),
	})

	content, isErr := runJobTool(t, jobs, map[string]any{
		"action": "await", "id": snap.ID, "pattern": "Ready", "timeout": 10000,
	})
	if isErr {
		t.Fatalf("不应 isError，实得 %q", content)
	}
	if !strings.Contains(content, "✓ 输出命中 pattern") {
		t.Errorf("应含命中判定行，实得 %q", content)
	}
	if !strings.Contains(content, "── 输出尾部 ──") {
		t.Errorf("有 tail 时应含分隔行，实得 %q", content)
	}
	if !strings.Contains(content, "Ready") {
		t.Errorf("应含输出内容，实得 %q", content)
	}
}

// TestJobToolAwaitTimeoutVerdict —— 超时判定行含时长。
//
// 对账 TS：timedOut → `⏱ 等待超时（${fmtDuration(timeoutMs)}），任务仍在运行`。
func TestJobToolAwaitTimeoutVerdict(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	content, isErr := runJobTool(t, jobs, map[string]any{
		"action": "await", "id": snap.ID, "timeout": 300,
	})
	if isErr {
		t.Fatalf("超时不应 isError（job 仍在跑是正常状态），实得 %q", content)
	}
	if !strings.Contains(content, "⏱ 等待超时（") {
		t.Errorf("应含超时判定行，实得 %q", content)
	}
	if !strings.Contains(content, "任务仍在运行") {
		t.Errorf("应说明任务仍在运行，实得 %q", content)
	}
}

// TestJobToolAwaitExitVerdict —— job 已退出的判定行含 exit code。
//
// 对账 TS：`● 任务已${killed?'被终止':'退出'} (exit ${code})`。
func TestJobToolAwaitExitVerdict(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "exit 3", RawCommand: "exit 3", Cwd: t.TempDir()})

	content, _ := runJobTool(t, jobs, map[string]any{
		"action": "await", "id": snap.ID, "timeout": 10000,
	})
	if !strings.Contains(content, "● 任务已退出 (exit 3)") {
		t.Errorf("应含退出判定行（exit 3），实得 %q", content)
	}
}

// TestJobToolLogsNeedsID —— logs 缺 id 报错。
func TestJobToolLogsNeedsID(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "logs"})
	if !isErr {
		t.Error("缺 id 应 isError")
	}
	if content != "logs 需要 id 参数。" {
		t.Errorf("文案应逐字对账：实得 %q", content)
	}
}

// TestJobToolLogsUnknownID —— 未知 id 文案。
//
// 对账 TS：`未找到任务 ${id}。`（注意：logs 分支**不带** await 那句引导）。
func TestJobToolLogsUnknownID(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "logs", "id": "nope"})
	if !isErr {
		t.Error("未知 id 应 isError")
	}
	if content != "未找到任务 nope。" {
		t.Errorf("文案应逐字对账：实得 %q", content)
	}
}

// TestJobToolLogsReturnsOutput —— logs 返回捕获的输出。
func TestJobToolLogsReturnsOutput(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "echo marker-line", RawCommand: "x", Cwd: t.TempDir()})
	// 等它跑完。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	content, isErr := runJobTool(t, jobs, map[string]any{"action": "logs", "id": snap.ID})
	if isErr {
		t.Fatalf("不应 isError，实得 %q", content)
	}
	if !strings.Contains(content, "marker-line") {
		t.Errorf("应返回捕获的输出，实得 %q", content)
	}
}

// TestJobToolLogsEmptyOutputPlaceholder —— 无输出时给占位符而非空串。
//
// 对账 TS：`logs || '(无输出)'`——空串会让模型以为工具失败。
func TestJobToolLogsEmptyOutputPlaceholder(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "true", Cwd: t.TempDir()})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	content, _ := runJobTool(t, jobs, map[string]any{"action": "logs", "id": snap.ID})
	if content != "(无输出)" {
		t.Errorf("无输出时应给占位符，实得 %q", content)
	}
}

// TestJobToolKillNeedsID —— kill 缺 id 报错。
func TestJobToolKillNeedsID(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "kill"})
	if !isErr {
		t.Error("缺 id 应 isError")
	}
	if content != "kill 需要 id 参数。" {
		t.Errorf("文案应逐字对账：实得 %q", content)
	}
}

// TestJobToolKillSuccess —— kill 成功的文案。
//
// 对账 TS：`已发送终止信号给任务 ${id}。`。
func TestJobToolKillSuccess(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	content, isErr := runJobTool(t, jobs, map[string]any{"action": "kill", "id": snap.ID})
	if isErr {
		t.Fatalf("kill 运行中 job 不应 isError，实得 %q", content)
	}
	want := "已发送终止信号给任务 " + snap.ID + "。"
	if content != want {
		t.Errorf("文案应逐字对账：实得 %q 期望 %q", content, want)
	}
}

// TestJobToolKillTerminalIsError —— kill 终态 job 报错（透传真实语义）。
//
// 对账 TS：`ok ? 成功文案 : '任务 ${id} 不存在或已结束。'` + `isError: !ok`。
func TestJobToolKillTerminalIsError(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "true", Cwd: t.TempDir()})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	content, isErr := runJobTool(t, jobs, map[string]any{"action": "kill", "id": snap.ID})
	if !isErr {
		t.Error("kill 终态 job 应 isError（不得假报成功）")
	}
	want := "任务 " + snap.ID + " 不存在或已结束。"
	if content != want {
		t.Errorf("文案应逐字对账：实得 %q 期望 %q", content, want)
	}
}

// TestJobToolUnknownAction —— 未知 action 报错并列出合法值。
//
// 对账 TS：`未知 action: ${action}。可用: list / await / logs / kill。`。
func TestJobToolUnknownAction(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	content, isErr := runJobTool(t, jobs, map[string]any{"action": "bogus"})
	if !isErr {
		t.Error("未知 action 应 isError")
	}
	want := "未知 action: bogus。可用: list / await / logs / kill。"
	if content != want {
		t.Errorf("文案应逐字对账：\n实得 %q\n期望 %q", content, want)
	}
}

// TestJobToolDefinitionParity —— definition 的 name/enum/required 对账 TS。
//
// 工具 definition 进系统提示词前缀——**必须逐字一致**，否则前缀缓存失效。
func TestJobToolDefinitionParity(t *testing.T) {
	def := Job().Definition()

	if def.Name != "job" {
		t.Errorf("name 应为 job，实得 %q", def.Name)
	}
	// description 首行对账 TS。
	wantDescPrefix := "查看和控制由 bash(run_in_background) 启动的后台任务。"
	if !strings.HasPrefix(def.Description, wantDescPrefix) {
		t.Errorf("description 首行应逐字对账：\n实得 %q", def.Description)
	}

	if def.InputSchema == nil {
		t.Fatalf("应有 InputSchema")
	}
	props := def.InputSchema.Properties
	if props == nil {
		t.Fatalf("InputSchema 应有 Properties，实得 %#v", def.InputSchema)
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatal("应有 action 属性")
	}
	enum, ok := action["enum"].([]any)
	if !ok {
		t.Fatalf("action 应有 enum，实得 %#v", action)
	}
	if len(enum) != 4 {
		t.Fatalf("enum 应有 4 项，实得 %#v", enum)
	}
	for i, want := range []string{"list", "await", "logs", "kill"} {
		if enum[i] != want {
			t.Errorf("enum[%d] 应为 %q，实得 %v", i, want, enum[i])
		}
	}

	// required 只有 action。
	req := def.InputSchema.Required
	if len(req) != 1 || req[0] != "action" {
		t.Errorf("required 应只有 action，实得 %#v", req)
	}

	// 属性声明序必须对账 TS（前缀缓存依赖键序）。
	wantOrder := []string{"action", "id", "pattern", "timeout"}
	if len(def.InputSchema.PropOrder) != len(wantOrder) {
		t.Fatalf("PropOrder 应有 %d 项，实得 %#v", len(wantOrder), def.InputSchema.PropOrder)
	}
	for i, w := range wantOrder {
		if def.InputSchema.PropOrder[i] != w {
			t.Errorf("PropOrder[%d] 应为 %q，实得 %q", i, w, def.InputSchema.PropOrder[i])
		}
	}
}

// TestJobToolTimeoutHeadroom —— await 的 timeoutMs 给足余量（超出 await 窗口）。
//
// 对账 TS：`await` action 返回 `t + 30_000`；其他 120_000。
// **为什么重要**：await 是阻塞调用，工具级超时若等于 await 窗口，
// 管线会在 await 返回前先掐断——等于 await 永远拿不到结果。
func TestJobToolTimeoutHeadroom(t *testing.T) {
	tool := Job()

	got := tool.Timeout(&CallParams{Input: map[string]any{"action": "await", "timeout": 5000}})
	if want := 35 * time.Second; got != want {
		t.Errorf("await 的 timeoutMs 应为 5000+30000=35s，实得 %v", got)
	}
	// 未给 timeout → 默认 120s + 30s。
	got = tool.Timeout(&CallParams{Input: map[string]any{"action": "await"}})
	if want := 150 * time.Second; got != want {
		t.Errorf("await 默认应为 120s+30s=150s，实得 %v", got)
	}
	// 其他 action → 120s。
	got = tool.Timeout(&CallParams{Input: map[string]any{"action": "list"}})
	if want := 120 * time.Second; got != want {
		t.Errorf("list 的 timeoutMs 应为 120s，实得 %v", got)
	}
}

// TestJobToolAwaitTimeoutClampedToMax —— await 的 timeout 被钳到 600s 上限。
//
// 对账 TS：`Math.min(Number(input.timeout) || DEFAULT, MAX_AWAIT_MS)`。
// **为什么需要**：pattern 永不命中时，无上限的 await 会把循环挂死。
func TestJobToolAwaitTimeoutClampedToMax(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	// 传一个巨大 timeout——应被钳到 600s，实际行为是「等到 600s」，
	// 测试不能真等，故只断言工具级超时反映钳制后的值。
	tool := Job()
	got := tool.Timeout(&CallParams{Input: map[string]any{"action": "await", "timeout": 99999999}})
	if want := 600*time.Second + 30*time.Second; got != want {
		t.Errorf("超大 timeout 应钳到 600s（+30s 余量=%v），实得 %v", want, got)
	}
	_ = snap
}
