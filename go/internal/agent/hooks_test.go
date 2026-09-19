package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestPipeline 构造一个用假时钟的管线（避免依赖真实时间）。
func newTestPipeline(opts PipelineOptions) *Pipeline {
	return NewPipeline(opts)
}

// noopHook 造一个记录调用的 hook。
func recordingHook(name string, phase RuntimeHookPhase, calls *int32, order *[]string, mu *sync.Mutex) RuntimeHook {
	return RuntimeHook{
		Name:  name,
		Phase: phase,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			atomic.AddInt32(calls, 1)
			if order != nil {
				mu.Lock()
				*order = append(*order, name)
				mu.Unlock()
			}
			return nil
		},
	}
}

// TestPipelinePhaseDispatch —— 五阶段各自只跑自己的 hook。
func TestPipelinePhaseDispatch(t *testing.T) {
	var pre, after, postTool, postTurn, postSession int32
	p := newTestPipeline(PipelineOptions{})

	p.Register(recordingHook("h-pre", PhasePreTurn, &pre, nil, nil))
	p.Register(recordingHook("h-after", PhaseAfterPerception, &after, nil, nil))
	p.Register(recordingHook("h-tool", PhasePostTool, &postTool, nil, nil))
	p.Register(recordingHook("h-turn", PhasePostTurn, &postTurn, nil, nil))
	p.Register(recordingHook("h-session", PhasePostSession, &postSession, nil, nil))

	ctx := context.Background()
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}}

	p.RunPreTurn(ctx, hctx)
	if pre != 1 {
		t.Errorf("preTurn 应跑 1 次，得到 %d", pre)
	}
	if after+postTool+postTurn+postSession != 0 {
		t.Error("其他阶段不应被触发")
	}

	p.RunAfterPerception(ctx, hctx)
	if after != 1 {
		t.Errorf("afterPerception 应跑 1 次，得到 %d", after)
	}

	p.RunPostTool(ctx, hctx, &RuntimeToolEvent{Name: "read_file", Success: true})
	if postTool != 1 {
		t.Errorf("postTool 应跑 1 次，得到 %d", postTool)
	}

	p.RunPostTurn(ctx, hctx)
	if postTurn != 1 {
		t.Errorf("postTurn 应跑 1 次，得到 %d", postTurn)
	}

	p.RunPostSession(ctx, hctx)
	if postSession != 1 {
		t.Errorf("postSession 应跑 1 次，得到 %d", postSession)
	}
}

// TestPipelinePhaseOrderPreserved —— **阶段内串行且保序**（hook 有顺序依赖）。
func TestPipelinePhaseOrderPreserved(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var calls int32
	p := newTestPipeline(PipelineOptions{})

	for _, n := range []string{"a", "b", "c", "d"} {
		p.Register(recordingHook(n, PhasePreTurn, &calls, &order, &mu))
	}

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "b", "c", "d"}
	if len(order) != len(want) {
		t.Fatalf("应跑 %d 个，得到 %d", len(want), len(order))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("[%d] 顺序应为 %s，得到 %s（完整：%v）", i, want[i], order[i], order)
		}
	}
}

// TestPipelinePostToolReceivesEvent —— postTool 能拿到工具事件。
func TestPipelinePostToolReceivesEvent(t *testing.T) {
	var gotName, gotTarget, gotClass string
	var gotSuccess bool
	var gotInput map[string]any

	p := newTestPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "capture", Phase: PhasePostTool,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if tool != nil {
				gotName = tool.Name
				gotTarget = tool.Target
				gotSuccess = tool.Success
				gotClass = tool.FailureClass
				gotInput = tool.Input
			}
			return nil
		},
	})

	p.RunPostTool(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}},
		&RuntimeToolEvent{
			Name: "write_file", Target: "a.go", Success: false,
			FailureClass: "type_error",
			Input:        map[string]any{"file_path": "a.go"},
		})

	if gotName != "write_file" || gotTarget != "a.go" || gotSuccess {
		t.Errorf("事件字段不符：name=%q target=%q success=%v", gotName, gotTarget, gotSuccess)
	}
	if gotClass != "type_error" {
		t.Errorf("failureClass 应为 type_error，得到 %q", gotClass)
	}
	if gotInput["file_path"] != "a.go" {
		t.Errorf("input 应透传，得到 %v", gotInput)
	}
}

// TestPipelineNonPostToolGetsNilEvent —— 非 postTool 阶段收到 nil 事件。
func TestPipelineNonPostToolGetsNilEvent(t *testing.T) {
	var gotTool *RuntimeToolEvent = &RuntimeToolEvent{} // 非 nil 初值
	p := newTestPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "check", Phase: PhasePreTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			gotTool = tool
			return nil
		},
	})
	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	if gotTool != nil {
		t.Errorf("preTurn 阶段的 tool 事件应为 nil，得到 %+v", gotTool)
	}
}

// TestPipelineFailureDoesNotStopPhase —— 单 hook 失败**不中断**该阶段。
func TestPipelineFailureDoesNotStopPhase(t *testing.T) {
	var secondRan int32
	var mu sync.Mutex
	var errs []RuntimeHookError

	p := newTestPipeline(PipelineOptions{
		OnError: func(e RuntimeHookError) { mu.Lock(); errs = append(errs, e); mu.Unlock() },
	})
	p.Register(RuntimeHook{
		Name: "boom", Phase: PhasePreTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			return fmt.Errorf("故意失败")
		},
	})
	p.Register(recordingHook("after-boom", PhasePreTurn, &secondRan, nil, nil))

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	if secondRan != 1 {
		t.Error("失败 hook 之后的 hook 仍应执行（阶段不中断）")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 || errs[0].HookName != "boom" {
		t.Errorf("应收到 1 条 boom 的错误，得到 %+v", errs)
	}
}

// TestPipelineTimeoutDoesNotBlock —— **核心**：超时后主流程继续。
func TestPipelineTimeoutDoesNotBlock(t *testing.T) {
	var nextRan int32
	var mu sync.Mutex
	var events []RuntimeHookRunEvent

	p := newTestPipeline(PipelineOptions{
		OnRun: func(e RuntimeHookRunEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
	})
	// 声明 20ms 预算，但实际睡 2 秒
	p.Register(RuntimeHook{
		Name: "slow", Phase: PhasePreTurn, BudgetMs: 20,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			time.Sleep(2 * time.Second)
			return nil
		},
	})
	p.Register(recordingHook("after-slow", PhasePreTurn, &nextRan, nil, nil))

	started := time.Now()
	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	elapsed := time.Since(started)

	// **关键断言**：整体耗时远小于 hook 的实际睡眠时间
	if elapsed > 500*time.Millisecond {
		t.Errorf("超时应快速返回，实际耗时 %v", elapsed)
	}
	if nextRan != 1 {
		t.Error("超时后下一个 hook 仍应执行")
	}

	mu.Lock()
	defer mu.Unlock()
	var slowEvent *RuntimeHookRunEvent
	for i := range events {
		if events[i].ID == "slow" {
			slowEvent = &events[i]
		}
	}
	if slowEvent == nil {
		t.Fatal("应有 slow 的事件")
	}
	if slowEvent.Outcome != OutcomeTimedOut {
		t.Errorf("slow 的 outcome 应为 timed_out，得到 %s", slowEvent.Outcome)
	}
	if slowEvent.BudgetMs != 20 {
		t.Errorf("事件应记录生效预算 20，得到 %d", slowEvent.BudgetMs)
	}
}

// TestPipelineLateFailureReported —— **迟到收尾**：超时后 hook 才失败，
// 仍应送 OnError 保留现场。
func TestPipelineLateFailureReported(t *testing.T) {
	var mu sync.Mutex
	var errs []RuntimeHookError

	p := newTestPipeline(PipelineOptions{
		OnError: func(e RuntimeHookError) { mu.Lock(); errs = append(errs, e); mu.Unlock() },
	})
	p.Register(RuntimeHook{
		Name: "late-boom", Phase: PhasePreTurn, BudgetMs: 20,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			time.Sleep(150 * time.Millisecond)
			return fmt.Errorf("迟到失败")
		},
	})

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	// 等迟到结果落地
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, e := range errs {
			if len(e.Message) > 20 && e.Message[:20] == "late failure after t" {
				found = true
			}
		}
		mu.Unlock()
		if found {
			return // 成功
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Errorf("应收到迟到失败的通知，实际收到 %d 条：%+v", len(errs), errs)
}

// TestPipelineLateFailureDoesNotDoubleCountRuns —— 迟到失败**不重复计 runs**。
func TestPipelineLateFailureDoesNotDoubleCountRuns(t *testing.T) {
	p := newTestPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "late-boom", Phase: PhasePreTurn, BudgetMs: 20,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			time.Sleep(120 * time.Millisecond)
			return fmt.Errorf("迟到失败")
		},
	})

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	time.Sleep(300 * time.Millisecond) // 等迟到结果

	stats := p.Stats()
	if len(stats) != 1 {
		t.Fatalf("应有 1 条统计，得到 %d", len(stats))
	}
	s := stats[0]
	// runs 应只计 1（timed_out），迟到失败不额外计
	if s.Runs != 1 {
		t.Errorf("runs 应为 1（迟到失败不重复计），得到 %d", s.Runs)
	}
	if s.Timeouts != 1 {
		t.Errorf("timeouts 应为 1，得到 %d", s.Timeouts)
	}
}

// TestPipelineStatsKeyedByPhaseAndID —— **stats 按 phase:id 键控**（同名不冲突）。
func TestPipelineStatsKeyedByPhaseAndID(t *testing.T) {
	var c1, c2 int32
	p := newTestPipeline(PipelineOptions{})
	// 两个**同名** hook，不同阶段
	p.Register(recordingHook("same-name", PhasePreTurn, &c1, nil, nil))
	p.Register(recordingHook("same-name", PhasePostTurn, &c2, nil, nil))

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	p.RunPostTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	stats := p.Stats()
	if len(stats) != 2 {
		t.Fatalf("同名不同阶段应产生 2 条统计，得到 %d：%+v", len(stats), stats)
	}
	phases := map[RuntimeHookPhase]bool{}
	for _, s := range stats {
		phases[s.Phase] = true
		if s.ID != "same-name" {
			t.Errorf("ID 应为 same-name，得到 %q", s.ID)
		}
		if s.Runs != 1 {
			t.Errorf("每条应 runs=1，得到 %d", s.Runs)
		}
	}
	if !phases[PhasePreTurn] || !phases[PhasePostTurn] {
		t.Errorf("应含两个阶段，得到 %v", phases)
	}
}

// TestPipelineDisabledHookSkipped —— 禁用集生效，且**计入 skipped**。
func TestPipelineDisabledHookSkipped(t *testing.T) {
	var ran int32
	p := newTestPipeline(PipelineOptions{DisabledHookIDs: []string{"off"}})
	p.Register(recordingHook("off", PhasePreTurn, &ran, nil, nil))

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	if ran != 0 {
		t.Error("被禁用的 hook 不应执行")
	}
	stats := p.Stats()
	if len(stats) != 1 || stats[0].Skipped != 1 || stats[0].Runs != 0 {
		t.Errorf("应记 1 次 skipped / 0 次 runs，得到 %+v", stats)
	}
}

// TestPipelineManifestReflectsDisabled —— manifest 反映禁用状态。
func TestPipelineManifestReflectsDisabled(t *testing.T) {
	var c int32
	p := newTestPipeline(PipelineOptions{})
	p.Register(recordingHook("a", PhasePreTurn, &c, nil, nil))
	p.Register(recordingHook("b", PhasePostTurn, &c, nil, nil))

	m := p.Manifest()
	if len(m) != 2 {
		t.Fatalf("应有 2 项，得到 %d", len(m))
	}
	for _, e := range m {
		if !e.Enabled {
			t.Errorf("%s 初始应启用", e.ID)
		}
	}

	p.SetDisabledHookIDs([]string{"a"})
	m = p.Manifest()
	for _, e := range m {
		if e.ID == "a" && e.Enabled {
			t.Error("a 被禁用后 manifest 应反映")
		}
		if e.ID == "b" && !e.Enabled {
			t.Error("b 不应受影响")
		}
	}
	if p.IsEnabled("a") {
		t.Error("IsEnabled(a) 应为 false")
	}
	if !p.IsEnabled("b") {
		t.Error("IsEnabled(b) 应为 true")
	}
}

// TestPipelineDisabledIsRuntimeOnly —— **禁用只影响运行时，不改变注册集**。
func TestPipelineDisabledIsRuntimeOnly(t *testing.T) {
	var c int32
	p := newTestPipeline(PipelineOptions{DisabledHookIDs: []string{"a"}})
	p.Register(recordingHook("a", PhasePreTurn, &c, nil, nil))

	// manifest 仍应**保留** a（只是 enabled=false）
	m := p.Manifest()
	if len(m) != 1 || m[0].ID != "a" {
		t.Fatalf("注册集不应因禁用而改变，得到 %+v", m)
	}
	if m[0].Enabled {
		t.Error("应标记为未启用")
	}

	// 重新启用后应能执行
	p.SetDisabledHookIDs(nil)
	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	if c != 1 {
		t.Error("重新启用后应执行")
	}
}

// TestPipelinePanicRecovered —— hook panic 不应崩进程（转为 failed）。
func TestPipelinePanicRecovered(t *testing.T) {
	var nextRan int32
	var mu sync.Mutex
	var errs []RuntimeHookError

	p := newTestPipeline(PipelineOptions{
		OnError: func(e RuntimeHookError) { mu.Lock(); errs = append(errs, e); mu.Unlock() },
	})
	p.Register(RuntimeHook{
		Name: "panicky", Phase: PhasePreTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			panic("boom")
		},
	})
	p.Register(recordingHook("after-panic", PhasePreTurn, &nextRan, nil, nil))

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	if nextRan != 1 {
		t.Error("panic 后下一个 hook 仍应执行")
	}
	stats := p.Stats()
	if len(stats) != 2 {
		t.Fatalf("应有 2 条统计，得到 %d", len(stats))
	}
	for _, s := range stats {
		if s.ID == "panicky" && s.Failures != 1 {
			t.Errorf("panicky 应记 1 次失败，得到 %d", s.Failures)
		}
	}
}

// TestPipelineBudgetOverride —— 单 hook budgetMs 覆盖全局。
func TestPipelineBudgetOverride(t *testing.T) {
	var mu sync.Mutex
	var events []RuntimeHookRunEvent

	p := newTestPipeline(PipelineOptions{
		HookTimeoutMs: 5000, // 全局 5s
		OnRun:         func(e RuntimeHookRunEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
	})
	p.Register(RuntimeHook{
		Name: "short-budget", Phase: PhasePreTurn, BudgetMs: 15, // 覆盖为 15ms
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	})
	p.Register(RuntimeHook{
		Name: "uses-global", Phase: PhasePreTurn, // 无覆盖 → 用全局 5000
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			return nil
		},
	})

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		switch e.ID {
		case "short-budget":
			if e.BudgetMs != 15 {
				t.Errorf("short-budget 应用 15，得到 %d", e.BudgetMs)
			}
			if e.Outcome != OutcomeTimedOut {
				t.Errorf("short-budget 应超时，得到 %s", e.Outcome)
			}
		case "uses-global":
			if e.BudgetMs != 5000 {
				t.Errorf("uses-global 应用全局 5000，得到 %d", e.BudgetMs)
			}
		}
	}
}

// TestPipelineSlowFlag —— 超过 slowMs 的完成记 slow。
func TestPipelineSlowFlag(t *testing.T) {
	var mu sync.Mutex
	var events []RuntimeHookRunEvent

	p := newTestPipeline(PipelineOptions{
		HookSlowMs: 10,
		OnRun:      func(e RuntimeHookRunEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
	})
	p.Register(RuntimeHook{
		Name: "slowish", Phase: PhasePreTurn, BudgetMs: 2000,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	})

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		if e.ID == "slowish" && !e.Slow {
			t.Errorf("应标记 slow（耗时 %dms > 阈值 10ms）", e.DurationMs)
		}
	}
	stats := p.Stats()
	if len(stats) != 1 || stats[0].SlowRuns != 1 {
		t.Errorf("stats 应记 1 次 slow，得到 %+v", stats)
	}
}

// TestPipelineOnRunPanicDoesNotBreak —— OnRun 回调 panic 不应打断执行。
func TestPipelineOnRunPanicDoesNotBreak(t *testing.T) {
	var ran int32
	p := newTestPipeline(PipelineOptions{
		OnRun: func(e RuntimeHookRunEvent) { panic("instrumentation boom") },
	})
	p.Register(recordingHook("a", PhasePreTurn, &ran, nil, nil))
	p.Register(recordingHook("b", PhasePreTurn, &ran, nil, nil))

	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	if ran != 2 {
		t.Errorf("插桩 panic 不应打断执行，应跑 2 个，得到 %d", ran)
	}
}

// TestPipelineUnknownPhaseIgnored —— 未知阶段静默忽略（不 panic、不入 manifest）。
func TestPipelineUnknownPhaseIgnored(t *testing.T) {
	var ran int32
	p := newTestPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "weird", Phase: RuntimeHookPhase("not-a-phase"),
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			ran++
			return nil
		},
	})

	// 不应崩
	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})
	if ran != 0 {
		t.Error("未知阶段不应执行")
	}
	// 但**应入 manifest**（对账 TS：registeredHooks 无条件 push）
	if m := p.Manifest(); len(m) != 1 || m[0].ID != "weird" {
		t.Errorf("未知阶段应入 manifest，得到 %+v", m)
	}
}

// TestPipelineStatsSnapshotIsCopy —— Stats 返回副本（调用方改动不影响内部）。
func TestPipelineStatsSnapshotIsCopy(t *testing.T) {
	var c int32
	p := newTestPipeline(PipelineOptions{})
	p.Register(recordingHook("a", PhasePreTurn, &c, nil, nil))
	p.RunPreTurn(context.Background(), &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}})

	s1 := p.Stats()
	s1[0].Runs = 999 // 改副本

	s2 := p.Stats()
	if s2[0].Runs != 1 {
		t.Errorf("Stats 应返回副本，内部值被污染：%d", s2[0].Runs)
	}
}
