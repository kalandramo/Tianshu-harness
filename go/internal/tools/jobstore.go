package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kalandramo/tianshu/go/internal/platform"
)

// jobstore.go —— 后台任务（job）子系统（第八十刀）。
//
// 对账 TS `src/tools/job-store.ts`（411 行）。
//
// # 为什么需要它
//
// Go 侧 `bash` 的 `run_in_background` 参数**一直存在但被静默忽略**
// （`bash.go` 的显式声明，第二十五刀）——因为缺这个子系统。TS 侧
// `AgentLoop` 自己创建 `SessionJobs`（`loop.ts:850`，条件 `if (config.sessionId)`），
// 所以**CLI 交互模式也真的会后台化**：长跑命令（dev server / watcher / install）
// 不阻塞当前轮次，模型用 `job` 工具查看/等待/终止。
//
// 本刀把它接上，并恢复 `bash` 的 TS 原文案。
//
// # 与 TS 的结构对应
//
//	TS BackgroundJob（单 job 的状态机）  →  go backgroundJob
//	TS SessionJobs  （会话级集合 + 事件）→  go SessionJobs
//	TS JobRegistry  （注入工具的窄接口）  →  go JobRegistry（interface）
//
// **差异（Go 惯用法，已声明）**：
//   - TS 用 EventEmitter，Go 用 `OnEvent(func(JobEvent))` 回调列表——语义等价，
//     避免引入 emitter 依赖。
//   - TS 的 `WinStreamDecoder` 在本包已有等价物（`windecode.go` 的
//     `winStreamDecoder`），直接复用。
//   - TS 的 `spawnShell`（Windows 作业持有者 helper，`job-launch.exe`）**未移植**：
//     那是 Windows 专有的 issue #144 修复，依赖一个未随仓库分发的原生二进制。
//     本实现走 `platform.ResolveShellCommand` + 进程组语义（Go 侧已有
//     `configureProcessGroup`/`killProcessTree`），Unix 上等价；Windows 上
//     行为与 Go 侧 bash 工具**一致**（同为无 helper 路径）。

// ── 常量（对账 TS 顶部）─────────────────────────────────────────────────

const (
	// jobRingCap 是每个 job 保留的内存输出环上限（解码后字符数）。
	// 对账 TS `RING_CAP = 64_000`。
	jobRingCap = 64_000
	// jobOutputThrottleMs 是 output 事件的最小间隔（毫秒）。
	// 对账 TS `OUTPUT_THROTTLE_MS = 500`。
	jobOutputThrottleMs = 500
	// jobDefaultAwaitHeartbeatMs 是 await 长等待的心跳间隔。
	// 对账 TS `DEFAULT_AWAIT_HEARTBEAT_MS = 30_000`。
	jobDefaultAwaitHeartbeatMs = 30_000
	// jobDefaultAwaitMs 是 await 的默认超时。
	// 对账 TS job-tool 的 `DEFAULT_AWAIT_MS = 120_000`。
	jobDefaultAwaitMs = 120_000
	// jobMaxAwaitMs 是 await 的超时上限（防 pattern 永不命中时挂死循环）。
	// 对账 TS job-tool 的 `MAX_AWAIT_MS = 600_000`。
	jobMaxAwaitMs = 600_000
	// jobMaxTerminalJobs 是终态条目内存上限：超出淘汰最旧的终态 job。
	// 对账 TS `SessionJobs.MAX_TERMINAL_JOBS = 50`。running 永不淘汰。
	jobMaxTerminalJobs = 50
	// jobKillGraceMs 是 SIGTERM 到 SIGKILL 的宽限窗口。
	// 对账 TS `kill()` 里的 3000ms。
	jobKillGraceMs = 3000
	// jobLastLineMax 是 lastLine 的长度上限（对账 TS `slice(0, 200)`）。
	jobLastLineMax = 200
	// jobTailLimit 是 await 结果里 tail 的长度上限（对账 TS `tail(4000)`）。
	jobTailLimit = 4000
)

// JobStatus 是 job 的生命周期状态。
// 对账 TS `JobStatus = 'running' | 'exited' | 'killed'`。
type JobStatus string

const (
	JobRunning JobStatus = "running"
	JobExited  JobStatus = "exited"
	JobKilled  JobStatus = "killed"
)

// JobSnapshot 是 job 的可序列化快照（安全跨 SSE/REST）。
// 对账 TS `interface JobSnapshot`。
type JobSnapshot struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Status    JobStatus `json:"status"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	StartedAt int64     `json:"startedAt"`
	EndedAt   int64     `json:"endedAt,omitempty"`
	// LastLine 是最后一行非空输出（仪表盘预览）。
	LastLine string `json:"lastLine"`
	PID      int    `json:"pid,omitempty"`
}

// JobEvent 是 job 事件。
// 对账 TS `interface JobEvent`。
type JobEvent struct {
	Kind string `json:"kind"` // started | output | exit
	Job  JobSnapshot
	// Chunk 仅 output 事件有——自上次 emit 以来新追加的文本。
	Chunk string `json:"chunk,omitempty"`
}

// JobSpawnOptions 是 spawn 的参数。
// 对账 TS `interface JobSpawnOptions`。
type JobSpawnOptions struct {
	// Command 是实际执行的命令（后置改写之后）。
	Command string
	// RawCommand 是原始命令（改写之前，用于展示）。
	RawCommand string
	Cwd        string
	// Env 是完全准备好的子进程环境（已 sanitize + mirror 叠加）。
	Env []string
	// MaxLifetimeMs 是墙钟上限，到期自动终止（SIGTERM→SIGKILL）。
	// 0 → 回退到 RIVET_JOB_MAX_MS（0 = 不限）。
	MaxLifetimeMs int64
}

// JobAwaitOptions 是 await 的参数。
// 对账 TS `interface JobAwaitOptions`。
type JobAwaitOptions struct {
	// Pattern 是对累积输出匹配的正则源；命中即提前返回。
	Pattern string
	// TimeoutMs 是最长阻塞毫秒数。0 → 默认 120s。
	TimeoutMs int64
}

// JobAwaitResult 是 await 的结果。
// 对账 TS `interface JobAwaitResult`。
type JobAwaitResult struct {
	Job JobSnapshot
	// Matched 为 true 表示 pattern 在退出/超时前命中输出。
	Matched bool
	// TimedOut 为 true 表示等到超时（job 可能仍在运行）。
	TimedOut bool
	// Tail 是解析时刻的输出尾部。
	Tail string
}

// JobRegistry 是注入给工具的窄接口（隐藏类内部）。
// 对账 TS `interface JobRegistry`。
type JobRegistry interface {
	Spawn(opts JobSpawnOptions) JobSnapshot
	Await(id string, opts JobAwaitOptions) (*JobAwaitResult, error)
	List() []JobSnapshot
	Logs(id string) *string
	Kill(id string) bool
}

// JobMaxLifetimeMs 返回后台 job 的墙钟上限（毫秒）。0/未设 = 不限。
//
// 对账 TS `jobMaxLifetimeMs`：`Number.isFinite(v) && v > 0 ? v : 0`。
//
// **为什么默认不限**：后台 job 本就是长期存在的（dev server、watcher），
// 一刀切的超时会杀掉合法工作。评测/CI 用 RIVET_JOB_MAX_MS 回收
// 永不退出的失控 job（否则会占住端口直到会话关闭）。
func JobMaxLifetimeMs() int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("RIVET_JOB_MAX_MS")), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// ── backgroundJob：单 job 的状态机 ───────────────────────────────────────

type jobWaiter struct {
	matched  chan struct{} // 命中 pattern 时关闭
	done     chan struct{} // 解析时关闭（命中/退出/超时任一）
	resolve  func(JobAwaitResult)
	regex    *regexp.Regexp
	timer    *time.Timer
	resolved bool
}

type backgroundJob struct {
	id        string
	command   string
	startedAt int64

	mu       sync.Mutex
	status   JobStatus
	exitCode *int
	endedAt  int64

	ring    strings.Builder
	ringLen int

	cmd         *exec.Cmd
	pid         int
	logFile     *os.File
	logMu       sync.Mutex
	killTimer   *time.Timer
	lifetimeTmr *time.Timer
	waiters     []*jobWaiter

	// 输出节流——把突发合并成 ≤1 事件/间隔。
	pendingChunk  string
	throttleTimer *time.Timer

	emit func(JobEvent)
}

// newJobID 生成 8 字符 job id。
//
// 对账 TS `randomUUID().slice(0, 8)`——取 UUID 前 8 位（十六进制）。
// Go 侧用 crypto/rand 生成 4 字节 → 8 位 hex，字符集与 TS 一致。
func newJobID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况（熵源不可用）：退回时间戳低位，仍保证 8 位 hex。
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
	}
	return hex.EncodeToString(b[:])
}

func newBackgroundJob(opts JobSpawnOptions, emit func(JobEvent)) *backgroundJob {
	return &backgroundJob{
		id:        newJobID(),
		command:   opts.RawCommand,
		startedAt: time.Now().UnixMilli(),
		status:    JobRunning,
		emit:      emit,
	}
}

// start 启动子进程。
//
// 对账 TS `BackgroundJob.start`。**差异**：TS 按 shell 家族做 Windows 编码前缀
// 改写（rewriteWindowsNullRedirect / PowerShell UTF-8 前缀）；Go 侧 bash 工具的
// 对应逻辑在 `bash.go`（`platform.BuildShellArgs` 承担同职责），此处不再重复——
// 调用方（bash 工具）传入的 `opts.Command` 已是改写后的命令。
func (j *backgroundJob) start(opts JobSpawnOptions, logPath string) error {
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			j.logFile = f
		}
		// 打不开就只丢磁盘日志（尽力而为）——job 仍要跑。
	}

	shellCmd, shellArgs := resolveJobShell(opts.Command)
	cmd := exec.Command(shellCmd, shellArgs...)
	cmd.Dir = opts.Cwd
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}
	// 进程组 + WaitDelay 兜底：与 bash 工具同一套语义（见 proctree.go 的长注释）。
	// 这是「整树回收」的前提——没有 Setpgid，kill(-pid) 会命中调用者自己的进程组。
	prepareCommand(cmd)

	// **为什么用 writer 而非 StdoutPipe**：StdoutPipe 下 `cmd.Wait()` 与读管道的
	// goroutine 存在竞态——Wait 可能先返回并触发 onExit，把 waiters 全部以
	// matched=false 解析，**尽管输出已到、pattern 本该命中**（实测：
	// `sleep 0.3; echo Ready` 的 await 返回 matched=false 但 tail="Ready\n"）。
	//
	// TS 侧无此问题：Node 保证 stdout/stderr 的 `data` 事件先于 `close` 触发。
	// 用 `cmd.Stdout = writer` 时 Go 内部保证「管道排空后才 Wait 返回」——
	// 即 onData 一定在 onExit 之前完成，与 Node 语义对齐。
	//
	// WaitDelay（prepareCommand 已设）兜底孙进程持有写端句柄的情况。
	//
	// **writer 必须在 Start 之前赋值**——Go 在 Start 时建立管道，
	// Start 之后再设 cmd.Stdout 会被忽略（输出全部丢失）。
	decOut := newWinStreamDecoder(isWindowsHost())
	decErr := newWinStreamDecoder(isWindowsHost())
	cmd.Stdout = &jobOutputWriter{job: j, dec: decOut}
	cmd.Stderr = &jobOutputWriter{job: j, dec: decErr}

	if err := cmd.Start(); err != nil {
		return err
	}

	j.mu.Lock()
	j.cmd = cmd
	j.pid = cmd.Process.Pid
	j.mu.Unlock()

	// 绝对墙钟上限（opt-in）：回收永不自行退出的 job。
	maxMs := opts.MaxLifetimeMs
	if maxMs == 0 {
		maxMs = JobMaxLifetimeMs()
	}
	if maxMs > 0 {
		j.mu.Lock()
		j.lifetimeTmr = time.AfterFunc(time.Duration(maxMs)*time.Millisecond, func() {
			j.onLifetimeExceeded(maxMs)
		})
		j.mu.Unlock()
	}

	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		// 排空解码器残留（对账 TS `onExit` 里的 `decoder.end()`）。
		j.onData(decOut.end())
		j.onData(decErr.end())
		j.onExit(code)
	}()

	j.emitEvent(JobEvent{Kind: "started", Job: j.snapshot()})
	return nil
}

// jobOutputWriter 把子进程的 stdout/stderr 直接喂给 onData。
//
// 之所以是 io.Writer 而非独立读 goroutine：见 start() 里关于
// 「Wait 与读管道竞态」的注释——Go 保证 writer 的 Write 全部返回后
// Wait 才返回，从而 onData 严格先于 onExit。
type jobOutputWriter struct {
	job *backgroundJob
	dec *winStreamDecoder
}

func (w *jobOutputWriter) Write(p []byte) (int, error) {
	if text := w.dec.decode(p); text != "" {
		w.job.onData(text)
	}
	return len(p), nil
}

func (j *backgroundJob) onData(text string) {
	if text == "" {
		return
	}
	j.mu.Lock()
	j.appendRingLocked(text)
	// 解析已命中的 pattern waiter。
	if len(j.waiters) > 0 {
		remaining := j.waiters[:0]
		var fired []*jobWaiter
		for _, w := range j.waiters {
			if w.regex != nil && !w.resolved && w.regex.MatchString(j.ring.String()) {
				fired = append(fired, w)
			} else {
				remaining = append(remaining, w)
			}
		}
		j.waiters = remaining
		j.pendingChunk += text
		needThrottle := j.throttleTimer == nil
		if needThrottle {
			j.throttleTimer = time.AfterFunc(jobOutputThrottleMs*time.Millisecond, j.flushOutput)
		}
		j.mu.Unlock()

		for _, w := range fired {
			j.resolveWaiter(w, JobAwaitResult{
				Job: j.snapshot(), Matched: true, TimedOut: false, Tail: j.tail(),
			})
		}
		return
	}
	j.pendingChunk += text
	if j.throttleTimer == nil {
		j.throttleTimer = time.AfterFunc(jobOutputThrottleMs*time.Millisecond, j.flushOutput)
	}
	j.mu.Unlock()
}

// appendRingLocked 追加到输出环并截断到上限。调用方须持锁。
func (j *backgroundJob) appendRingLocked(text string) {
	j.ring.WriteString(text)
	j.ringLen += len(text)
	if j.ringLen > jobRingCap {
		s := j.ring.String()
		if len(s) > jobRingCap {
			s = s[len(s)-jobRingCap:]
		}
		j.ring.Reset()
		j.ring.WriteString(s)
		j.ringLen = len(s)
	}
	if j.logFile != nil {
		j.logMu.Lock()
		_, _ = j.logFile.WriteString(text)
		j.logMu.Unlock()
	}
}

func (j *backgroundJob) flushOutput() {
	j.mu.Lock()
	if j.throttleTimer != nil {
		j.throttleTimer.Stop()
		j.throttleTimer = nil
	}
	chunk := j.pendingChunk
	j.pendingChunk = ""
	snap := j.snapshotLocked()
	j.mu.Unlock()

	if chunk == "" {
		return
	}
	j.emitEvent(JobEvent{Kind: "output", Job: snap, Chunk: chunk})
}

// onLifetimeExceeded 在 job 超过墙钟上限时触发：先记原因（让 logs/await 能看到），
// 再走正常终止路径。对账 TS `onLifetimeExceeded`。
func (j *backgroundJob) onLifetimeExceeded(maxMs int64) {
	j.mu.Lock()
	j.lifetimeTmr = nil
	if j.status != JobRunning {
		j.mu.Unlock()
		return
	}
	j.mu.Unlock()

	secs := (maxMs + 500) / 1000
	j.onData(fmt.Sprintf("\n[job killed] exceeded max lifetime (%ds) — auto-terminated\n", secs))
	j.kill()
}

func (j *backgroundJob) onExit(code int) {
	j.mu.Lock()
	if j.killTimer != nil {
		j.killTimer.Stop()
		j.killTimer = nil
	}
	if j.lifetimeTmr != nil {
		j.lifetimeTmr.Stop()
		j.lifetimeTmr = nil
	}
	if j.status != JobRunning {
		// 已被 kill——保留 killed 状态，只记退出码/时间。
		j.exitCode = &code
		j.endedAt = time.Now().UnixMilli()
	} else {
		j.status = JobExited
		j.exitCode = &code
		j.endedAt = time.Now().UnixMilli()
	}
	waiters := j.waiters
	j.waiters = nil
	j.mu.Unlock()

	j.flushOutput()
	j.logMu.Lock()
	if j.logFile != nil {
		_ = j.logFile.Close()
		j.logFile = nil
	}
	j.logMu.Unlock()

	for _, w := range waiters {
		j.resolveWaiter(w, JobAwaitResult{
			Job: j.snapshot(), Matched: false, TimedOut: false, Tail: j.tail(),
		})
	}
	j.emitEvent(JobEvent{Kind: "exit", Job: j.snapshot()})
}

// resolveWaiter 保证一个 waiter 只被解析一次（命中/退出/超时竞态）。
func (j *backgroundJob) resolveWaiter(w *jobWaiter, res JobAwaitResult) {
	j.mu.Lock()
	if w.resolved {
		j.mu.Unlock()
		return
	}
	w.resolved = true
	if w.timer != nil {
		w.timer.Stop()
	}
	j.mu.Unlock()
	w.resolve(res)
}

// await 阻塞直到 job 退出、输出命中 pattern、或超时。
//
// 对账 TS `BackgroundJob.await`。
func (j *backgroundJob) await(opts JobAwaitOptions) JobAwaitResult {
	j.mu.Lock()
	if j.status != JobRunning {
		snap, tail := j.snapshotLocked(), j.tailLocked()
		j.mu.Unlock()
		return JobAwaitResult{Job: snap, Matched: false, TimedOut: false, Tail: tail}
	}
	var regex *regexp.Regexp
	if opts.Pattern != "" {
		// 非法正则被忽略（不 panic）——对账 TS 的 try/catch。
		if re, err := regexp.Compile(opts.Pattern); err == nil {
			regex = re
		}
	}
	// 快路径：缓冲里已满足 pattern。
	if regex != nil && regex.MatchString(j.ring.String()) {
		snap, tail := j.snapshotLocked(), j.tailLocked()
		j.mu.Unlock()
		return JobAwaitResult{Job: snap, Matched: true, TimedOut: false, Tail: tail}
	}

	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = jobDefaultAwaitMs
	}

	// **regex 必须挂到 waiter 上**：onData 靠 `w.regex` 判断是否命中，
	// 局部变量 `regex` 不挂上去的话 waiter 永远不会匹配（实测：await 只能
	// 等到 onExit 的 matched=false，尽管输出早已含 pattern）。
	//
	// **整个注册（含 timer 创建）必须在同一临界区**：`onData`/`onExit` 在
	// 锁内遍历 `j.waiters`，任何对 waiter 字段或列表的写入若在锁外，
	// 都会与它们构成 data race（`-race` 实测抓到：`onData` 读 `w.regex`
	// 与 await 在锁外 `w.timer = ...` 冲突）。
	//
	// resolve 也在解锁前设好：解锁后再赋值会留下窗口——子进程若在该窗口内
	// 输出，onData 会调到 nil 的 resolve（panic）。
	done := make(chan JobAwaitResult, 1)
	w := &jobWaiter{
		regex:   regex,
		resolve: func(r JobAwaitResult) { done <- r },
	}
	// 超时分支：从 waiter 列表摘除后 resolve timedOut。
	w.timer = time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		j.mu.Lock()
		rest := j.waiters[:0]
		for _, x := range j.waiters {
			if x != w {
				rest = append(rest, x)
			}
		}
		j.waiters = rest
		snap, tail := j.snapshotLocked(), j.tailLocked()
		j.mu.Unlock()
		j.resolveWaiter(w, JobAwaitResult{Job: snap, Matched: false, TimedOut: true, Tail: tail})
	})
	j.waiters = append(j.waiters, w)
	j.mu.Unlock()

	return <-done
}

// kill 终止 job。返回是否真发了信号——终态 job 返回 false，
// 调用方不得据 true 覆盖其真实结局。对账 TS `kill()`。
func (j *backgroundJob) kill() bool {
	j.mu.Lock()
	if j.status != JobRunning || j.cmd == nil {
		j.mu.Unlock()
		return false
	}
	if j.lifetimeTmr != nil {
		j.lifetimeTmr.Stop()
		j.lifetimeTmr = nil
	}
	j.status = JobKilled
	cmd := j.cmd
	j.mu.Unlock()

	killProcessTree(cmd)
	j.mu.Lock()
	j.killTimer = time.AfterFunc(jobKillGraceMs*time.Millisecond, func() {
		j.mu.Lock()
		j.killTimer = nil
		j.mu.Unlock()
		killProcessTree(cmd)
	})
	j.mu.Unlock()
	return true
}

// logs 返回累积输出。
func (j *backgroundJob) logs() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ring.String()
}

// tailLocked 返回输出尾部（调用方须持锁）。
func (j *backgroundJob) tailLocked() string {
	s := j.ring.String()
	if len(s) > jobTailLimit {
		return s[len(s)-jobTailLimit:]
	}
	return s
}

func (j *backgroundJob) tail() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.tailLocked()
}

// snapshotLocked 构造快照（调用方须持锁）。
func (j *backgroundJob) snapshotLocked() JobSnapshot {
	return JobSnapshot{
		ID:        j.id,
		Command:   j.command,
		Status:    j.status,
		ExitCode:  j.exitCode,
		StartedAt: j.startedAt,
		EndedAt:   j.endedAt,
		LastLine:  jobLastNonEmptyLine(j.ring.String()),
		PID:       j.pid,
	}
}

func (j *backgroundJob) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

// emitEvent 触发事件回调。
func (j *backgroundJob) emitEvent(ev JobEvent) {
	if j.emit != nil {
		j.emit(ev)
	}
}

// jobLastNonEmptyLine 取最后一个 trim 后非空的行，超长截断。
//
// 对账 TS `lastNonEmptyLine`：从后往前找；`line.length > 200 ? slice(0,200) : line`。
func jobLastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			if len(line) > jobLastLineMax {
				return line[:jobLastLineMax]
			}
			return line
		}
	}
	return ""
}

// ── SessionJobs：会话级集合 ──────────────────────────────────────────────

// SessionJobs 是一个会话的后台 job 集合，同时是事件源。
// 对账 TS `class SessionJobs extends EventEmitter implements JobRegistry`。
type SessionJobs struct {
	logDir string
	// onAwaitHeartbeat 是长时等待上报（可选）：await 期间按心跳回调
	// `job:await:<id>`，让会话层把「合法长等待」与「卡死」区分开。
	onAwaitHeartbeat func(source string)
	heartbeatMs      int64
	// maxTerminal 是终态条目上限（可注入以便测试，缺省 jobMaxTerminalJobs）。
	maxTerminal int

	mu        sync.Mutex
	jobs      map[string]*backgroundJob
	listeners []func(JobEvent)
}

// NewSessionJobs 构造会话 job 集合。
//
//	onAwaitHeartbeat 为 nil 时不上报心跳；heartbeatMs<=0 时用默认 30s。
func NewSessionJobs(logDir string, onAwaitHeartbeat func(string), heartbeatMs int64) *SessionJobs {
	if heartbeatMs <= 0 {
		heartbeatMs = jobDefaultAwaitHeartbeatMs
	}
	return &SessionJobs{
		logDir:           logDir,
		onAwaitHeartbeat: onAwaitHeartbeat,
		heartbeatMs:      heartbeatMs,
		maxTerminal:      jobMaxTerminalJobs,
		jobs:             make(map[string]*backgroundJob),
	}
}

// newSessionJobsWithCap 是同包测试用的构造器（注入小上限，避免 spawn 50+ 进程）。
func newSessionJobsWithCap(logDir string, cap int) *SessionJobs {
	s := NewSessionJobs(logDir, nil, 0)
	s.maxTerminal = cap
	return s
}

// OnEvent 注册事件监听（对账 TS 的 `on('event', ...)`）。
func (s *SessionJobs) OnEvent(fn func(JobEvent)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

// Spawn 启动一个后台 job 并返回快照。
//
// 对账 TS `SessionJobs.spawn`。
func (s *SessionJobs) Spawn(opts JobSpawnOptions) JobSnapshot {
	var job *backgroundJob
	job = newBackgroundJob(opts, func(ev JobEvent) {
		s.mu.Lock()
		ls := append([]func(JobEvent){}, s.listeners...)
		s.mu.Unlock()
		for _, fn := range ls {
			fn(ev)
		}
		if ev.Kind == "exit" {
			s.evictTerminals()
		}
	})

	s.mu.Lock()
	s.jobs[job.id] = job
	s.mu.Unlock()

	logPath := ""
	if s.logDir != "" {
		if err := os.MkdirAll(s.logDir, 0o755); err == nil {
			logPath = filepath.Join(s.logDir, job.id+".log")
		}
		// 建目录失败就只丢磁盘日志（尽力而为）——job 仍要跑。
	}
	if err := job.start(opts, logPath); err != nil {
		// 启动失败：置终态并返回（TS 侧 spawn 同步抛错，Go 侧不 panic——
		// 工具层要能把它变成一条 isError 结果）。
		job.mu.Lock()
		code := 1
		job.status = JobExited
		job.exitCode = &code
		job.endedAt = time.Now().UnixMilli()
		job.mu.Unlock()
	}
	return job.snapshot()
}

// Await 阻塞直到 job 退出、命中 pattern、或超时。未知 id 返回 (nil, nil)。
//
// 对账 TS `SessionJobs.await`（含心跳上报）。
func (s *SessionJobs) Await(id string, opts JobAwaitOptions) (*JobAwaitResult, error) {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job == nil {
		return nil, nil
	}

	// 长等待心跳：等待期间按间隔上报「job 仍 running」。绑真实状态——
	// 每 tick 复查 status，一进终态就停表（对账 TS 的 heartbeat 实现）。
	var stop chan struct{}
	if s.onAwaitHeartbeat != nil && s.heartbeatMs > 0 {
		stop = make(chan struct{})
		go func() {
			t := time.NewTicker(time.Duration(s.heartbeatMs) * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if job.snapshot().Status == JobRunning {
						s.onAwaitHeartbeat("job:await:" + id)
					} else {
						return
					}
				}
			}
		}()
	}

	res := job.await(opts)
	if stop != nil {
		close(stop)
	}
	return &res, nil
}

// List 列出所有 job，按 startedAt 降序（最新在前）。
//
// 对账 TS `list()`：`.sort((a, b) => b.startedAt - a.startedAt)`。
// **恒返回非 nil 切片**（下游 SSE 序列化区分 null 与 []）。
func (s *SessionJobs) List() []JobSnapshot {
	s.mu.Lock()
	out := make([]JobSnapshot, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.snapshot())
	}
	s.mu.Unlock()

	// 降序：最新在前。用稳定排序保证 startedAt 相同时顺序确定。
	sortSnapshotsDesc(out)
	return out
}

// Logs 返回 job 的累积输出；未知 id 返回 nil。
//
// 对账 TS `logs(id)`：`this.jobs.get(id)?.logs() ?? null`。
func (s *SessionJobs) Logs(id string) *string {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job == nil {
		return nil
	}
	out := job.logs()
	return &out
}

// Snapshot 返回 job 快照；未知 id 返回 nil（测试与 TUI 读模型用）。
func (s *SessionJobs) Snapshot(id string) *JobSnapshot {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job == nil {
		return nil
	}
	snap := job.snapshot()
	return &snap
}

// Kill 终止 job，返回是否真发了信号（透传真实语义：终态 job 返回 false）。
//
// 对账 TS `kill(id)`。
func (s *SessionJobs) Kill(id string) bool {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job == nil {
		return false
	}
	return job.kill()
}

// KillAll 终止所有运行中 job——会话关闭时调用，避免孤儿。
//
// 对账 TS `killAll()`。
func (s *SessionJobs) KillAll() {
	s.mu.Lock()
	all := make([]*backgroundJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		all = append(all, j)
	}
	s.mu.Unlock()
	for _, j := range all {
		j.kill()
	}
}

// HasRunning 报告是否有运行中的 job。
//
// 对账 TS `hasRunning()`。
func (s *SessionJobs) HasRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.mu.Lock()
		running := j.status == JobRunning
		j.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

// evictTerminals 淘汰最旧的终态条目，把终态保有量压回上限。
//
// 对账 TS `evictTerminals`：终态按 startedAt 升序，删掉超出的最旧若干；
// **running 永不淘汰**（磁盘日志保留，淘汰只影响内存态）。
func (s *SessionJobs) evictTerminals() {
	s.mu.Lock()
	defer s.mu.Unlock()

	type entry struct {
		id        string
		startedAt int64
	}
	terminals := make([]entry, 0, len(s.jobs))
	for id, j := range s.jobs {
		j.mu.Lock()
		st, sa := j.status, j.startedAt
		j.mu.Unlock()
		if st != JobRunning {
			terminals = append(terminals, entry{id, sa})
		}
	}
	if len(terminals) <= s.maxTerminal {
		return
	}
	// 升序：最旧的在前。
	for i := 1; i < len(terminals); i++ {
		for k := i; k > 0 && terminals[k].startedAt < terminals[k-1].startedAt; k-- {
			terminals[k], terminals[k-1] = terminals[k-1], terminals[k]
		}
	}
	for i := 0; i < len(terminals)-s.maxTerminal; i++ {
		delete(s.jobs, terminals[i].id)
	}
}

// sortSnapshotsDesc 按 StartedAt 降序（稳定——同值时保持插入序）。
func sortSnapshotsDesc(items []JobSnapshot) {
	for i := 1; i < len(items); i++ {
		for k := i; k > 0 && items[k].StartedAt > items[k-1].StartedAt; k-- {
			items[k], items[k-1] = items[k-1], items[k]
		}
	}
}

// resolveJobShell 返回执行 job 命令的 shell（含 argv 拼装）。
//
// 对账 TS `getShellCommand()` + `[...shell.args, commandToRun]`——Go 侧对应物是
// `platform.HostShellCommand()` + `platform.BuildShellArgs`（与 `bash.go:176-177` 同一套，
// 保证前后台命令的 shell 语义一致）。
func resolveJobShell(command string) (cmd string, args []string) {
	shell := platform.HostShellCommand()
	return shell.Cmd, platform.BuildShellArgs(shell, command)
}
