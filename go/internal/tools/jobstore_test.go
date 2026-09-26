package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// jobstore_test.go —— SessionJobs 的核心语义（第八十刀）。
//
// 对账 TS `src/tools/job-store.ts`。**TS 侧无该模块的单测**（只有 TUI 层的
// app-job-events/app-job-await-status），故本测试是**语义锁定**而非 oracle 对账——
// 每条断言都对着 TS 源码的对应分支写，注释标出 TS 行号依据。
//
// # 为什么不写「真实后台进程」的重测试
//
// spawn 真进程的测试要处理时序竞态（进程起没起、输出到没到），容易 flaky。
// 本文件用**短命真命令**（`echo`/`sleep`）测端到端，用**纯函数**测派生逻辑——
// 两者分工明确，失败时能立刻定位是时序还是逻辑。

// ── 纯函数：lastNonEmptyLine（对账 TS `lastNonEmptyLine`）──────────────

// TestJobLastNonEmptyLine —— 取最后一个非空行，>200 截断。
//
// 对账 TS `job-store.ts` 末尾的 `lastNonEmptyLine`：从后往前找第一个
// `trim()` 后非空的行；长度 >200 时 `slice(0,200)`。
func TestJobLastNonEmptyLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"单个非空行", "hello", "hello"},
		{"尾部空行被跳过", "a\nb\n\n\n", "b"},
		{"只取最后一行", "first\nsecond\nthird", "third"},
		{"全空白返回空", "\n  \n\t\n", ""},
		{"空串返回空", "", ""},
		{"行内空白被 trim", "a\n   padded   ", "padded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jobLastNonEmptyLine(tc.in); got != tc.want {
				t.Errorf("jobLastNonEmptyLine(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}

	// 长度上限：201 字符 → 截到 200。
	long := strings.Repeat("x", 201)
	got := jobLastNonEmptyLine(long)
	if len(got) != 200 {
		t.Errorf("超长行应截到 200 字符，实得 %d", len(got))
	}
	// 恰好 200 不截断（边界）。
	exact := strings.Repeat("y", 200)
	if len(jobLastNonEmptyLine(exact)) != 200 {
		t.Errorf("恰好 200 字符不应截断")
	}
}

// ── spawn / list / snapshot ──────────────────────────────────────────────

// TestJobSpawnReturnsRunningSnapshot —— spawn 立刻返回 running 快照。
//
// 对账 TS `SessionJobs.spawn`：`job.start(logPath)` 后返回 `job.snapshot()`。
// snapshot 的 `command` 是 **rawCommand**（原始命令，非改写后的）——TS
// `BackgroundJob` 构造器 `this.command = opts.rawCommand`。
func TestJobSpawnReturnsRunningSnapshot(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)

	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "sleep 5",
		RawCommand: "sleep 5 --raw",
		Cwd:        t.TempDir(),
		Env:        os.Environ(),
	})

	if snap.ID == "" {
		t.Fatal("spawn 应返回非空 job id")
	}
	if len(snap.ID) != 8 {
		t.Errorf("job id 应为 8 字符（对账 TS randomUUID().slice(0,8)），实得 %q(%d)", snap.ID, len(snap.ID))
	}
	if snap.Status != JobRunning {
		t.Errorf("刚 spawn 的 job 状态应为 running，实得 %q", snap.Status)
	}
	if snap.Command != "sleep 5 --raw" {
		t.Errorf("snapshot.Command 应为 rawCommand，实得 %q", snap.Command)
	}
	if snap.StartedAt == 0 {
		t.Error("snapshot 应有 StartedAt")
	}
	if snap.PID == 0 {
		t.Error("snapshot 应有 PID（进程已启动）")
	}

	jobs.KillAll() // 清理：别留 sleep 孤儿
}

// TestJobListSortedByStartedAtDesc —— list 按 startedAt 降序（最新在前）。
//
// 对账 TS `list()`：`.sort((a, b) => b.startedAt - a.startedAt)`。
func TestJobListSortedByStartedAtDesc(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	first := jobs.Spawn(JobSpawnOptions{Command: "sleep 5", RawCommand: "first", Cwd: t.TempDir()})
	time.Sleep(15 * time.Millisecond) // 让 startedAt 可区分（毫秒精度）
	second := jobs.Spawn(JobSpawnOptions{Command: "sleep 5", RawCommand: "second", Cwd: t.TempDir()})

	list := jobs.List()
	if len(list) != 2 {
		t.Fatalf("应列出 2 个 job，实得 %d", len(list))
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Errorf("list 应按 startedAt 降序（最新在前），实得 [%s, %s]", list[0].Command, list[1].Command)
	}
}

// TestJobListEmptyReturnsEmptySlice —— 无 job 时返回空切片（非 nil）。
//
// **为什么断言非 nil**：`job` 工具的 `list` action 用 `len(list)==0` 判断，
// 但下游（SSE 序列化）会区分 `null` 与 `[]`。TS 的 `[...map.values()]` 恒为数组。
func TestJobListEmptyReturnsEmptySlice(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	list := jobs.List()
	if list == nil {
		t.Error("无 job 时 List() 应返回空切片而非 nil")
	}
	if len(list) != 0 {
		t.Errorf("无 job 时应为 0 条，实得 %d", len(list))
	}
}

// ── 输出捕获与日志 ────────────────────────────────────────────────────────

// TestJobCapturesOutputAndExits —— 捕获 stdout/stderr，退出后 status=exited。
//
// 对账 TS `BackgroundJob.start`：stdout/stderr 都接 `onData`；
// `child.on('close', code => this.onExit(code ?? 1))` → status='exited'。
func TestJobCapturesOutputAndExits(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "echo hello-from-job; echo err-line >&2",
		RawCommand: "echo hello-from-job; echo err-line >&2",
		Cwd:        t.TempDir(),
	})

	// 等它退出（短命令，通常 <1s）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := jobs.Snapshot(snap.ID)
	if got == nil {
		t.Fatal("job 应仍在表中（终态不立即删除）")
	}
	if got.Status != JobExited {
		t.Fatalf("echo 结束后状态应为 exited，实得 %q", got.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("退出码应为 0，实得 %v", got.ExitCode)
	}
	if got.EndedAt == 0 {
		t.Error("终态 job 应有 EndedAt")
	}

	logs := jobs.Logs(snap.ID)
	if logs == nil {
		t.Fatal("Logs 应返回内容而非 nil")
	}
	if !strings.Contains(*logs, "hello-from-job") {
		t.Errorf("应捕获 stdout，实得 %q", *logs)
	}
	if !strings.Contains(*logs, "err-line") {
		t.Errorf("应捕获 stderr（TS 侧 stderr 也接 onData），实得 %q", *logs)
	}

	// lastLine 应是最后一行（stderr 在 stdout 之后 flush，但不保证顺序；
	// 只断言它非空且是其中一行）。
	if got.LastLine == "" {
		t.Error("终态 job 的 LastLine 应非空")
	}
}

// TestJobLogsUnknownReturnsNil —— 未知 id 的 Logs 返回 nil。
//
// 对账 TS `logs(id)`：`this.jobs.get(id)?.logs() ?? null`。
// **这个 nil 很重要**：`job` 工具据此区分「无此 job」（报错）与「job 无输出」（"(无输出)"）。
func TestJobLogsUnknownReturnsNil(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	if got := jobs.Logs("nosuchid"); got != nil {
		t.Errorf("未知 id 应返回 nil，实得 %q", *got)
	}
	if s := jobs.Snapshot("nosuchid"); s != nil {
		t.Errorf("未知 id 的 Snapshot 应返回 nil，实得 %+v", s)
	}
}

// TestJobLogsWrittenToDisk —— 输出落盘到 logDir/<id>.log。
//
// 对账 TS `spawn`：`mkdirSync(logDir, {recursive:true})` + `join(logDir, id+'.log')`
// + `createWriteStream(logPath, {flags:'a'})`。
func TestJobLogsWrittenToDisk(t *testing.T) {
	logDir := t.TempDir()
	jobs := NewSessionJobs(logDir, nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "echo disk-marker",
		RawCommand: "echo disk-marker",
		Cwd:        t.TempDir(),
	})

	// 等退出 + 日志 flush。
	path := filepath.Join(logDir, snap.ID+".log")
	deadline := time.Now().Add(10 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "disk-marker") {
			content = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if content == nil {
		t.Fatalf("日志应写入 %s 且含 disk-marker", path)
	}
}

// ── await：三态（命中 / 退出 / 超时）─────────────────────────────────────

// TestJobAwaitPatternMatches —— pattern 命中时提前返回 matched=true。
//
// 对账 TS `await`：`onData` 里对每个 waiter 做 `w.regex.test(this.ring)`，
// 命中即 resolve `{matched:true, timedOut:false}`。
func TestJobAwaitPatternMatches(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	// 延迟输出 "Ready"——确保 await 真的在等，而非命中缓冲。
	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "sleep 0.3; echo Ready",
		RawCommand: "sleep 0.3; echo Ready",
		Cwd:        t.TempDir(),
	})

	start := time.Now()
	res, err := jobs.Await(snap.ID, JobAwaitOptions{Pattern: "Ready", TimeoutMs: 10000})
	if err != nil {
		t.Fatalf("Await 不应返回 error：%v", err)
	}
	if res == nil {
		t.Fatal("Await 应返回结果")
	}
	if !res.Matched {
		t.Errorf("pattern 应命中，实得 matched=false tail=%q", res.Tail)
	}
	if res.TimedOut {
		t.Error("命中时不应 timedOut")
	}
	if !strings.Contains(res.Tail, "Ready") {
		t.Errorf("tail 应含命中文本，实得 %q", res.Tail)
	}
	// 应在 0.3s 输出后很快返回，而非等满 10s。
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("命中应提前返回，实得耗时 %v", elapsed)
	}
}

// TestJobAwaitAlreadyExitedReturnsImmediately —— 已终态 job 的 await 立刻返回。
//
// 对账 TS `await` 开头：`if (this.status !== 'running') return Promise.resolve({...})`。
func TestJobAwaitAlreadyExitedReturnsImmediately(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "true", Cwd: t.TempDir()})

	// 先等它退出。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	start := time.Now()
	res, _ := jobs.Await(snap.ID, JobAwaitOptions{TimeoutMs: 60000})
	if res == nil {
		t.Fatal("Await 应返回结果")
	}
	if res.TimedOut {
		t.Error("已终态 job 不应 timedOut（应立即返回）")
	}
	if res.Matched {
		t.Error("无 pattern 时不应 matched")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("已终态 job 的 await 应立即返回，实得 %v", elapsed)
	}
}

// TestJobAwaitTimeoutReportsTimedOut —— 超时返回 timedOut=true 且 job 仍 running。
//
// 对账 TS `await` 的 waiter.timer 分支。
func TestJobAwaitTimeoutReportsTimedOut(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	start := time.Now()
	res, _ := jobs.Await(snap.ID, JobAwaitOptions{TimeoutMs: 300})
	if res == nil {
		t.Fatal("Await 应返回结果")
	}
	if !res.TimedOut {
		t.Error("应 timedOut=true")
	}
	if res.Matched {
		t.Error("超时不应 matched")
	}
	if res.Job.Status != JobRunning {
		t.Errorf("超时后 job 应仍 running，实得 %q", res.Job.Status)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("应按 timeout 返回（约 300ms），实得 %v", elapsed)
	}
}

// TestJobAwaitUnknownReturnsNil —— 未知 id 返回 nil（**不是** error）。
//
// 对账 TS `SessionJobs.await`：`if (!job) return null`。
// `job` 工具据此报「未找到任务 X」。
func TestJobAwaitUnknownReturnsNil(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	res, err := jobs.Await("nosuchid", JobAwaitOptions{TimeoutMs: 100})
	if err != nil {
		t.Fatalf("未知 id 不应返回 error（应返回 nil），实得 %v", err)
	}
	if res != nil {
		t.Errorf("未知 id 应返回 nil，实得 %+v", res)
	}
}

// TestJobAwaitInvalidPatternIgnored —— 非法正则被忽略（不 panic），退化为纯等待。
//
// 对账 TS：`try { regex = new RegExp(opts.pattern) } catch { regex = undefined }`。
func TestJobAwaitInvalidPatternIgnored(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	// `[` 是非法正则；应被忽略 → 纯等待 → 超时。
	res, err := jobs.Await(snap.ID, JobAwaitOptions{Pattern: "[", TimeoutMs: 300})
	if err != nil {
		t.Fatalf("非法正则不应导致 error：%v", err)
	}
	if res == nil {
		t.Fatal("应返回结果")
	}
	if !res.TimedOut {
		t.Error("非法正则被忽略后应纯等待至超时")
	}
}

// ── kill ──────────────────────────────────────────────────────────────────

// TestJobKillRunningReturnsTrueAndMarksKilled —— kill 运行中 job 返回 true 且状态变 killed。
//
// 对账 TS `kill()`：`if (status !== 'running' || !child) return false`；
// 置 `status = 'killed'` 后发 SIGTERM。
func TestJobKillRunningReturnsTrueAndMarksKilled(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	if !jobs.Kill(snap.ID) {
		t.Fatal("kill 运行中 job 应返回 true")
	}
	got := jobs.Snapshot(snap.ID)
	if got == nil {
		t.Fatal("kill 后 job 应仍在表中")
	}
	if got.Status != JobKilled {
		t.Errorf("kill 后状态应为 killed，实得 %q", got.Status)
	}
}

// TestJobKillTerminalReturnsFalse —— 终态 job 的 kill 返回 false（不透传假成功）。
//
// **这条是 TS 注释里点名的语义**：「返回是否真发了信号——终态 job 返回 false，
// 调用方不得据 true 覆盖其真实结局」。
func TestJobKillTerminalReturnsFalse(t *testing.T) {
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

	if jobs.Kill(snap.ID) {
		t.Error("终态 job 的 kill 应返回 false")
	}
	// 且不得把 exited 覆盖成 killed。
	if got := jobs.Snapshot(snap.ID); got.Status != JobExited {
		t.Errorf("kill 终态 job 不得覆盖其状态，实得 %q", got.Status)
	}
}

// TestJobKillUnknownReturnsFalse —— 未知 id 返回 false。
func TestJobKillUnknownReturnsFalse(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	if jobs.Kill("nosuchid") {
		t.Error("未知 id 的 kill 应返回 false")
	}
}

// ── killAll / hasRunning ──────────────────────────────────────────────────

// TestJobKillAllTerminatesEverything —— killAll 终止所有运行中 job。
//
// 对账 TS `killAll()`：`for (const job of this.jobs.values()) job.kill()`。
func TestJobKillAllTerminatesEverything(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)

	a := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "a", Cwd: t.TempDir()})
	b := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "b", Cwd: t.TempDir()})

	if !jobs.HasRunning() {
		t.Fatal("killAll 前应有运行中 job")
	}
	jobs.KillAll()

	for _, id := range []string{a.ID, b.ID} {
		if s := jobs.Snapshot(id); s != nil && s.Status == JobRunning {
			t.Errorf("killAll 后 %s 不应仍为 running", id)
		}
	}
	if jobs.HasRunning() {
		t.Error("killAll 后 HasRunning 应为 false")
	}
}

// ── 终态淘汰（MAX_TERMINAL_JOBS）──────────────────────────────────────────

// TestJobEvictsOldestTerminalBeyondCap —— 终态条目超上限时淘汰最旧的，running 永不淘汰。
//
// 对账 TS `evictTerminals`：终态按 startedAt 升序，删掉超出
// `MAX_TERMINAL_JOBS` 的最旧若干；**running 永不淘汰**。
//
// **为什么用小上限**：TS 的上限是 50，逐个 spawn 50+ 个真进程太慢。
// 本实现的上限是包级常量，测试用 `newSessionJobsWithCap` 注入小值。
func TestJobEvictsOldestTerminalBeyondCap(t *testing.T) {
	jobs := newSessionJobsWithCap(t.TempDir(), 2)

	// 三个短命令依次跑完 → 终态 3 个 → 淘汰后应只剩 2 个。
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		s := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "done", Cwd: t.TempDir()})
		ids = append(ids, s.ID)
		// 等它退出（淘汰在 exit 事件里触发）。
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if snap := jobs.Snapshot(s.ID); snap != nil && snap.Status != JobRunning {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond) // 让淘汰跑完
	}

	if got := len(jobs.List()); got != 2 {
		t.Errorf("终态上限 2 时应有 2 条，实得 %d", got)
	}
	// 最旧的那个应被淘汰。
	if s := jobs.Snapshot(ids[0]); s != nil {
		t.Errorf("最旧的终态 job 应被淘汰，实得 %+v", s)
	}
}

// TestJobRunningNeverEvicted —— running 的 job 不参与淘汰。
//
// 对账 TS `evictTerminals`：`.filter(j => j.status !== 'running')`。
func TestJobRunningNeverEvicted(t *testing.T) {
	jobs := newSessionJobsWithCap(t.TempDir(), 1)
	defer jobs.KillAll()

	// 一个长期 running。
	running := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "running", Cwd: t.TempDir()})

	// 再跑三个短命令（终态上限 1 → 淘汰到只剩 1 个终态）。
	for i := 0; i < 3; i++ {
		s := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "done", Cwd: t.TempDir()})
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if snap := jobs.Snapshot(s.ID); snap != nil && snap.Status != JobRunning {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
	}

	if s := jobs.Snapshot(running.ID); s == nil {
		t.Fatal("running 的 job 永不被淘汰")
	}
}

// ── 生命周期上限（RIVET_JOB_MAX_MS）───────────────────────────────────────

// TestJobMaxLifetimeMsEnvParsing —— 环境变量解析：非正/非法 → 0（不限）。
//
// 对账 TS `jobMaxLifetimeMs`：`Number.isFinite(v) && v > 0 ? v : 0`。
func TestJobMaxLifetimeMsEnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"-1", 0},
		{"abc", 0},
		{"5000", 5000},
		{"  7000  ", 7000}, // parseInt 容忍前后空白
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("RIVET_JOB_MAX_MS", tc.env)
			if got := JobMaxLifetimeMs(); got != tc.want {
				t.Errorf("JobMaxLifetimeMs() = %d，期望 %d", got, tc.want)
			}
		})
	}
}

// TestJobMaxLifetimeKillsRunaway —— 超期 job 被自动终止。
//
// 对账 TS `onLifetimeExceeded`：写一行 `[job killed] exceeded max lifetime`
// 后走正常 kill 路径。
func TestJobMaxLifetimeKillsRunaway(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	// 显式传 maxLifetimeMs（比环境变量更直接，且不污染其他测试）。
	snap := jobs.Spawn(JobSpawnOptions{
		Command:       "sleep 30",
		RawCommand:    "sleep 30",
		Cwd:           t.TempDir(),
		MaxLifetimeMs: 400,
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := jobs.Snapshot(snap.ID)
	if got == nil {
		t.Fatal("job 应仍在表中")
	}
	if got.Status == JobRunning {
		t.Fatal("超过 maxLifetimeMs 的 job 应被自动终止")
	}
	if logs := jobs.Logs(snap.ID); logs != nil && !strings.Contains(*logs, "exceeded max lifetime") {
		t.Errorf("应记录超期原因，实得 %q", *logs)
	}
}

// ── 心跳上报（onAwaitHeartbeat）──────────────────────────────────────────

// TestJobAwaitHeartbeatReportsWhileRunning —— await 期间按心跳上报。
//
// 对账 TS `SessionJobs.await` 的 heartbeat：按 interval 回调
// `job:await:<id>`，**每 tick 复查 status，终态即停**（不是空转 timer）。
func TestJobAwaitHeartbeatReportsWhileRunning(t *testing.T) {
	var mu sync.Mutex
	var reports []string
	jobs := NewSessionJobs(t.TempDir(), func(source string) {
		mu.Lock()
		reports = append(reports, source)
		mu.Unlock()
	}, 80) // 80ms 心跳，便于测试
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})

	// await 500ms → 应有若干次心跳。
	_, _ = jobs.Await(snap.ID, JobAwaitOptions{TimeoutMs: 500})

	mu.Lock()
	n := len(reports)
	mu.Unlock()

	if n == 0 {
		t.Fatal("await 期间应上报心跳")
	}
	if want := "job:await:" + snap.ID; reports[0] != want {
		t.Errorf("心跳 source 应为 %q，实得 %q", want, reports[0])
	}
}

// TestJobAwaitHeartbeatNilSafe —— 无上报器时不 panic（缺省路径）。
func TestJobAwaitHeartbeatNilSafe(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 80)
	defer jobs.KillAll()

	snap := jobs.Spawn(JobSpawnOptions{Command: "sleep 30", RawCommand: "sleep 30", Cwd: t.TempDir()})
	res, err := jobs.Await(snap.ID, JobAwaitOptions{TimeoutMs: 200})
	if err != nil {
		t.Fatalf("无上报器时不应 error：%v", err)
	}
	if res == nil {
		t.Fatal("应返回结果")
	}
}

// ── 输出节流（OUTPUT_THROTTLE_MS）────────────────────────────────────────

// TestJobOutputThrottled —— 突发输出被合并（节流到 ≤1 事件/间隔）。
//
// 对账 TS `onData` + `flushOutput`：`pendingChunk` 累积，
// 首次 data 起一个 timer，到期 flush 成一个 `output` 事件。
//
// **断言方式**：快速产出多行的命令，订阅事件，断言 output 事件数
// **远少于**行数（节流生效）。不断言精确值——那依赖调度时序，会 flaky。
func TestJobOutputThrottled(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	var mu sync.Mutex
	outputEvents := 0
	jobs.OnEvent(func(ev JobEvent) {
		if ev.Kind == "output" {
			mu.Lock()
			outputEvents++
			mu.Unlock()
		}
	})

	// 100 行快速输出（在同一 tick 内基本全到）。
	snap := jobs.Spawn(JobSpawnOptions{
		Command:    "for i in $(seq 1 100); do echo line$i; done",
		RawCommand: "seq",
		Cwd:        t.TempDir(),
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := jobs.Snapshot(snap.ID); s != nil && s.Status != JobRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(700 * time.Millisecond) // 等最后一次 flush

	mu.Lock()
	n := outputEvents
	mu.Unlock()

	if n == 0 {
		t.Fatal("应有 output 事件")
	}
	if n >= 100 {
		t.Errorf("100 行突发输出应被节流成远少于 100 个事件，实得 %d", n)
	}
}

// ── 并发安全 ─────────────────────────────────────────────────────────────

// TestJobConcurrentAccessNoRace —— 并发 spawn/list/snapshot 不触发 -race。
//
// **为什么重要**：TS 侧是单线程 EventEmitter；Go 侧 spawn 的 goroutine
// 会并发回调（onData/onExit）。map 与 ring 必须加锁，否则 `-race` 报错。
func TestJobConcurrentAccessNoRace(t *testing.T) {
	jobs := NewSessionJobs(t.TempDir(), nil, 0)
	defer jobs.KillAll()

	done := make(chan struct{})
	// 读侧：并发 list/snapshot。
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					_ = jobs.List()
					_ = jobs.HasRunning()
				}
			}
		}()
	}
	// 写侧：spawn 短命令。
	for i := 0; i < 10; i++ {
		s := jobs.Spawn(JobSpawnOptions{Command: "true", RawCommand: "x", Cwd: t.TempDir()})
		_ = jobs.Snapshot(s.ID)
		_ = jobs.Logs(s.ID)
	}
	close(done)
	time.Sleep(100 * time.Millisecond)
}
