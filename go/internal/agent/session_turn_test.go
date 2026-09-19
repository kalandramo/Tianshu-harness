package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestSessionTurnSemantics —— **锁定 SessionTurn 的语义**（对账 TS）。
//
// TS context.ts:360：`turnCount = messages.filter(m => m.role === 'user').length`
// TS context.ts:202：`turnCount++` 在 addUserMessage 内（每个 user 消息 +1）
//
// 用户动作：连跑 3 个 Run（每个 Run 一条 user 消息）。
// 观察到：SessionTurn() == 3，且**同一 Run 内多次调用恒定**。
func TestSessionTurnSemantics(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r1"),
		textTurn("r2"),
		textTurn("r3"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	// 初始：无 user 消息
	if got := l.SessionTurn(); got != 0 {
		t.Errorf("初始 SessionTurn 应为 0，得到 %d", got)
	}

	for i := 1; i <= 3; i++ {
		if err := l.Run(t.Context(), "msg"); err != nil {
			t.Fatal(err)
		}
		if got := l.SessionTurn(); got != i {
			t.Errorf("第 %d 个 Run 后 SessionTurn 应为 %d，得到 %d", i, i, got)
		}
	}
}

// TestSessionTurnConstantWithinRun —— **单 Run 内恒定**（这是关键语义）。
//
// TS 的 session turn 在 Run 内不推进——窗口靠**跨 user 轮**推进（探针实测：
// session turn 恒定时 evaluate 恒不判定）。
//
// 用户动作：一个 Run 内模型跑 3 轮工具调用。
// 观察到：advisory 的送达 turn 与观察 turn **相同**（都是会话级）。
func TestSessionTurnConstantWithinRun(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		toolTurn("c2", "read_file", `{"file_path":"b.ts"}`),
		toolTurn("c3", "read_file", `{"file_path":"c.ts"}`),
		textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 记录每次 ObserveTool 看到的 turn——同一 Run 内应全部相同
	var seen []int
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "watch", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			seen = append(seen, l.SessionTurn())
			return nil
		},
	})
	l.Hooks = p

	if err := l.Run(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(seen) < 2 {
		t.Fatalf("应有 >= 2 次 postTurn 观察，得到 %d", len(seen))
	}
	for i, v := range seen {
		if v != seen[0] {
			t.Errorf("**单 Run 内 SessionTurn 应恒定**：第 0 次=%d，第 %d 次=%d\n"+
				"（TS 语义：Run 内恒定，跨 user 轮才推进）", seen[0], i, v)
		}
	}
	if seen[0] != 1 {
		t.Errorf("第 1 个 Run 的 SessionTurn 应为 1，得到 %d", seen[0])
	}
	t.Logf("Run 内各轮观察到的 SessionTurn: %v", seen)
}

// TestClockUnifiedInProductionPath —— **生产路径的时钟统一验收**。
//
// 用户动作：两个 user 轮，第 1 轮送达 advisory，第 2 轮模型执行满足谓词的动作。
// 观察到：advisory 被**正确核销**（adopted），而非因时钟错位永久 pending。
//
// **接线依赖**：hook_snapshot 的 `Track(delivered, l.SessionTurn())` 与
// loop 的 `ObserveTool{Turn: l.SessionTurn()}`。若两者不一致，窗口判定错位。
func TestClockUnifiedInProductionPath(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		// 第 1 个 user 轮：读文件
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r1"),
		// 第 2 个 user 轮：跑测试（满足 verify_attempted）
		toolTurn("c2", "run_tests", `{}`),
		textTurn("r2"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb
	bus.SetHabituationPolicy(rb)

	// 第 1 个 Run 期间投递带 expect 的 advisory（窗口 2 轮）
	deliveredOnce := false
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if !deliveredOnce {
				deliveredOnce = true
				bus.Submit(AdvisoryEntry{
					Key: "want-verify", Priority: 0.6, Category: CategoryDiscipline,
					Content: "跑测试",
					Expect:  &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
				})
			}
			return nil
		},
	})
	l.Hooks = p

	// 第 1 个 user 轮
	if err := l.Run(t.Context(), "第一轮"); err != nil {
		t.Fatal(err)
	}
	// 第 2 个 user 轮
	if err := l.Run(t.Context(), "第二轮"); err != nil {
		t.Fatal(err)
	}

	s := rb.Stats()["want-verify"]
	t.Logf("stats=%+v SessionTurn=%d", s, l.SessionTurn())

	if s.Delivered == 0 {
		t.Fatal("送达未被跟踪——Track 未接线")
	}
	if s.Adopted == 0 {
		t.Errorf("**跨轮核销失败**：模型第 2 轮跑了 run_tests，应判 adopted\n"+
			"stats=%+v\n（时钟不统一会让窗口落在错误区间）", s)
	}
}

// TestTrackUsesSessionTurnNotZero —— **判别 Track 的 turn 参数**（探针驱动）。
//
// 探针实测（`TestProbeWindow` 已确认）：
//
//	trackTurn=0 → 送达前的 run_tests 被算进窗口 → **误判 adopted=1**
//	trackTurn=2 → 窗口从 2 起 → 不误判（ignored=1，因为窗口内确实没验证）
//
// 场景：advisory 在第 2 个 user 轮末投递（第 3 轮 render 时送达），窗口 2 轮；
// 模型在第 1 个 user 轮跑过 run_tests（**送达之前**，不该算数）。
//
// **构造要点**：最后一轮必须带工具调用——终答轮会提前 return，不经过 Evaluate。
func TestTrackUsesSessionTurnNotZero(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		// 第 1 轮：跑 run_tests（在 advisory 送达之前）
		toolTurn("c1", "run_tests", `{}`),
		textTurn("r1"),
		// 第 2 轮：读文件
		toolTurn("c2", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r2"),
		// 第 3 轮：读文件（带工具调用 → 会经过 Evaluate）
		toolTurn("c3", "read_file", `{"file_path":"b.ts"}`),
		textTurn("r3"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 第 1 轮：跑 run_tests（session turn 变 1，事件记 Turn=1）
	if err := l.Run(t.Context(), "第一轮"); err != nil {
		t.Fatal(err)
	}

	// 第 2 轮末投递 → 第 3 轮 render 时送达（Track 记当时的 session turn=3）
	p := NewPipeline(PipelineOptions{})
	p.Register(RuntimeHook{
		Name: "late", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if l.SessionTurn() == 2 {
				bus.Submit(AdvisoryEntry{
					Key: "late-key", Priority: 0.6, Category: CategoryDiscipline,
					Content: "跑测试",
					Expect:  &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
				})
			}
			return nil
		},
	})
	l.Hooks = p

	if err := l.Run(t.Context(), "第二轮"); err != nil {
		t.Fatal(err)
	}
	if err := l.Run(t.Context(), "第三轮"); err != nil {
		t.Fatal(err)
	}

	s := rb.Stats()["late-key"]
	t.Logf("stats=%+v SessionTurn=%d", s, l.SessionTurn())
	if s.Delivered == 0 {
		t.Fatal("advisory 未送达——测试构造有误")
	}
	// 第 1 轮的 run_tests 发生在送达之前——不该被算作采纳
	if s.Adopted > 0 {
		t.Errorf("**窗口起点算错**：送达前（第 1 轮）的 run_tests 被误判为采纳\n"+
			"stats=%+v（Track 传了 0，窗口向左扩张到 turn=0）", s)
	}
}

// TestFlushSessionRedeemsAtSessionEnd —— **FlushSession 的核销行为**。
//
// **判别构造**（探针已确认）：flush 前/后 `evaluate` 返回值都是 0（未到期），
// 无法区分。判别信号是 `FlushAtSessionEnd` 的**返回值**——unresolved 列表。
//
// 用户动作：advisory 送达后会话结束（未走完窗口）。
// 观察到：会话级核销把未到期的作为 unresolved 报出，**不误判 ignored**
// （TS advisory-readback.ts:250-260 的原始约定）。
func TestFlushSessionRedeemsAtSessionEnd(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`), textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	rb := NewAdvisoryReadback()
	l.Readback = rb
	l.Advisories = NewAdvisoryBus()

	// 预置一条未到期的 pending（窗口 5 轮，送达于 turn=1）
	rb.Track([]DeliveredAdvisory{{
		Key:    "pending",
		Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 5},
	}}, 1)

	// ── Run 内不该 flush ──
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatal(err)
	}
	if s := rb.Stats()["pending"]; s.Ignored != 0 {
		t.Errorf("Run 内不该判 ignored，实际 %d", s.Ignored)
	}

	// ── 会话结束：走 loop.FlushSession（这是生产路径）──
	unresolved := l.FlushSession()

	if len(unresolved) != 1 {
		t.Errorf("**FlushSession 应把未到期的作 unresolved 报出**（观测价值），得到 %d 条\n"+
			"（若为 0：flush 未接线或 pending 被误判）", len(unresolved))
	} else if unresolved[0].Key != "pending" {
		t.Errorf("unresolved key 应为 pending，得到 %q", unresolved[0].Key)
	}

	// flush 后 pending 已清空
	if n := rb.Evaluate(l.SessionTurn()); n != 0 {
		t.Errorf("flush 后 pending 应已清空，evaluate 判定数应为 0，得到 %d", n)
	}
	if s := rb.Stats()["pending"]; s.Ignored != 0 {
		t.Errorf("flush 不该产生 ignored，实际 %d", s.Ignored)
	}
}
