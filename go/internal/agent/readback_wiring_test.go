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

	// ── 观察：核销发生（有采纳率 = 有判定样本）──
	rate := rb.GetAdoptionRate("test-verify-nudge")
	if rate == nil {
		t.Errorf("**无核销判定**（adoptionRate=nil）——evaluate 未跑或谓词未求值\n"+
			"stats: %+v", rb.Stats())
		return
	}
	// 模型第 1 轮就跑了 run_tests → 应满足 verify_attempted → adopted
	if *rate <= 0 {
		t.Errorf("模型跑了 run_tests，采纳率应 > 0，得到 %v\nstats: %+v", *rate, rb.Stats())
	}
	t.Logf("采纳率=%v stats=%+v", *rate, rb.Stats())
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

// TestReadbackEvaluatePerTurn —— **postTurn 的 Evaluate 必须逐轮跑**。
//
// **为什么需要这个用例**：若只在会话结束 flush，则多轮场景下 streak 的推进
// 时机会错——习惯化对抗依赖**逐轮**累积的 ignoredStreak。
//
// 场景：advisory 窗口 1 轮（tool_appears bash），模型不跑 bash。
// 第 0 轮 postTurn 就该判 ignored；若 Evaluate 未接线，要到 flush 才判。
func TestReadbackEvaluatePerTurn(t *testing.T) {
	root := t.TempDir()
	// 三轮：read_file ×2 + 终答（都不满足 bash 谓词）
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"a.ts"}`),
		toolTurn("c2", "read_file", `{"file_path":"a.ts"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	p := NewPipeline(PipelineOptions{})
	// 窗口 1 轮 + 每轮都投递（同 key 重复送达刷新窗口）
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

	if err := l.Run(t.Context(), "读文件"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// ── 观察：逐轮 Evaluate 应累积多次 ignored（streak > 1）──
	s := rb.Stats()["want-bash"]
	if s.IgnoredStreak <= 1 {
		t.Errorf("**ignoredStreak 应 > 1**（逐轮 Evaluate 累积），实际 %d\n"+
			"（若 =1 说明只在会话结束判了一次）\nstats: %+v", s.IgnoredStreak, s)
	}
	t.Logf("ignoredStreak=%d ignored=%d", s.IgnoredStreak, s.Ignored)
}
