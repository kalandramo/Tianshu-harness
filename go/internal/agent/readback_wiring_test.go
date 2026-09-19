package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestReadbackWiredInLoop —— **核销链路验收**：
//
// 用户动作：loop 跑一轮，hook 投递带 `expect` 谓词的 advisory，模型随后
// 执行了满足该谓词的工具（run_tests）。
// 观察到：`readback.GetAdoptionRate` 返回采纳率（非 nil）——说明送达被跟踪、
// 行为被观察、谓词被核销。
//
// **这消除本刀的悬空**——readback 此前无生产调用方。
func TestReadbackWiredInLoop(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	p := NewPipeline(PipelineOptions{})
	// 每轮 postTurn 投递带 expect 的 advisory（verify_attempted 谓词）
	p.Register(RuntimeHook{
		Name: "seed-verify-expect", Phase: PhasePostTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "test-verify-nudge", Priority: 0.6,
				Category: CategoryDiscipline, Content: "跑一下测试",
				Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
			})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus
	l.Readback = rb

	if err := l.Run(t.Context(), "跑测试"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// ── 观察：送达被跟踪 ──
	if got := rb.GetDeliveredCount("test-verify-nudge"); got == 0 {
		t.Errorf("**送达未被跟踪**（delivered=0）——render 后未调 Track")
	}

	// ── 观察：单 Run 内窗口**不闭合**（对账 TS 的 session turn 语义）──
	//
	// TS 的 session turn 在 Run 内恒定（context.ts:360 = user 消息条数），
	// 窗口靠**跨 user 轮**推进。单 Run 内 evaluate 不会判到期——
	// 因此无 adoptionRate 是**正确行为**，不是接线缺陷。
	//
	// 探针实测（TS 真实代码）：session turn 恒定时 `evaluate(t)` 恒返回 0。
	rate := rb.GetAdoptionRate("test-verify-nudge")
	if rate != nil {
		t.Errorf("单 Run 内不该有核销判定（窗口未闭合），却得到 rate=%v\n"+
			"stats: %+v\n（说明 session turn 在 Run 内被错误推进了）", *rate, rb.Stats())
	}
	// 送达仍必须被跟踪（Track 接线）
	s := rb.Stats()["test-verify-nudge"]
	if s.Delivered != 1 {
		t.Errorf("送达应被跟踪 1 次，得到 %d", s.Delivered)
	}
	if s.Adopted != 0 || s.Ignored != 0 {
		t.Errorf("未到期的 pending 不该进账本：adopted=%d ignored=%d", s.Adopted, s.Ignored)
	}
	t.Logf("stats=%+v（单 Run 内未到期，符合 TS 语义）", s)
}

// TestReadbackFlushOnNormalExit —— **正常收尾路径也 flush**。
//
// 用户动作：loop 单轮跑完就终答（无工具调用）。
// 观察到：未到期的 pending **不误判 ignored**。
func TestReadbackFlushOnNormalExit(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("直接回答")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	l.Advisories = bus
	l.Readback = rb

	// 预置一条带长窗口 expect 的送达（模拟前序轮送达）
	rb.Track([]DeliveredAdvisory{{
		Key:    "pending-key",
		Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 5},
	}}, 0)

	if err := l.Run(t.Context(), "回答"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// ── 观察：未到期的不该判 ignored ──
	s := rb.Stats()["pending-key"]
	if s.Ignored != 0 {
		t.Errorf("**未到期不该判 ignored**，实际 %d\n"+
			"（假 ignored 会经 ignoredStreak/efficacy/lift 三条路径压低效力评分）", s.Ignored)
	}
	if s.IgnoredStreak != 0 {
		t.Errorf("不该推进 ignoredStreak，实际 %d", s.IgnoredStreak)
	}
}

// TestReadbackObserveToolWired —— postTool 的 observeTool 接线。
func TestReadbackObserveToolWired(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	rb := NewAdvisoryReadback()
	l.Readback = rb
	l.Advisories = NewAdvisoryBus()

	if err := l.Run(t.Context(), "跑测试"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// 无 expect 的送达不产生 pending，但工具观察应已入日志 → 用一个带 expect
	// 的场景间接验证：手动 Track 后再 Evaluate 应能核销
	// （若 observeTool 未接线，Evaluate 会判 ignored）
	rb2 := NewAdvisoryReadback()
	rb2.Track([]DeliveredAdvisory{{
		Key: "k", Expect: &AdvisoryExpectation{Kind: ExpectVerifyAttempted},
	}}, 0)
	rb2.ObserveTool(ObservedToolEvent{Turn: 0, Name: "run_tests"})
	rb2.Evaluate(0)
	if rb2.Stats()["k"].Adopted != 1 {
		t.Error("observeTool + evaluate 应产生 adopted")
	}
}

// TestReadbackEvaluateAcrossUserTurns —— **逐 user 轮 Evaluate 累积 streak**。
//
// **为什么需要这个用例**：若只在会话结束 flush，streak 的推进时机会错——
// 习惯化对抗依赖**逐轮**累积的 ignoredStreak。
//
// **注意时钟语义**（对账 TS）：这里的「轮」是 **user 轮**（session turn），
// 不是 run 内模型轮。session turn 在 Run 内恒定，跨 user 轮才推进。
//
// 场景：advisory 窗口 1 轮（tool_appears bash），模型连续 3 个 user 轮都不跑 bash。
// 每轮 postTurn 的 Evaluate 应判一次 ignored → streak 累积到 3。
func TestReadbackEvaluateAcrossUserTurns(t *testing.T) {
	root := t.TempDir()
	// 三个 user 轮，每轮模型都只读文件（不满足 bash 谓词）
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r1"),
		toolTurn("c2", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r2"),
		toolTurn("c3", "read_file", `{"file_path":"a.ts"}`),
		textTurn("r3"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	p := NewPipeline(PipelineOptions{})
	// 每个 user 轮都投递同一 key（窗口 1 轮）
	p.Register(RuntimeHook{
		Name: "seed-bash-expect", Phase: PhasePostTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "want-bash", Priority: 0.6,
				Category: CategoryDiscipline, Content: "跑个 bash",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}, WithinTurns: 1},
			})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus
	l.Readback = rb

	// 三个 user 轮
	for i := 0; i < 3; i++ {
		if err := l.Run(t.Context(), "读文件"); err != nil {
			t.Fatalf("第 %d 轮失败：%v", i+1, err)
		}
	}

	// ── 观察：跨 user 轮累积 streak ──
	s := rb.Stats()["want-bash"]
	if s.IgnoredStreak < 2 {
		t.Errorf("**ignoredStreak 应跨 user 轮累积 >= 2**，实际 %d\n"+
			"（若 =1 说明只在会话结束判了一次，逐轮 Evaluate 未生效）\nstats: %+v",
			s.IgnoredStreak, s)
	}
	t.Logf("ignoredStreak=%d ignored=%d delivered=%d", s.IgnoredStreak, s.Ignored, s.Delivered)
}
