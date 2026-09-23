package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHabituationInRealRequests —— **习惯化对抗的端到端验收**。
//
// 用户动作：模型连续 8 轮不执行被提醒的动作（run_tests），提醒每轮重新投递。
// 观察到：**该 advisory 在真实请求体里的出现模式**——
//
//	第 1-3 轮出现（第 3 轮起带升级措辞）→ 第 4-7 轮**消失**（静音）→ 第 8 轮回归。
//
// 这是本刀的用户级可观察标志：重复无效的提醒不再每轮占位。
//
// **接线依赖**：`bus.SetHabituationPolicy(readback)`。若未接，streak 恒为 0，
// 提醒会一直以原文渲染（静音永不触发）。
func TestHabituationInRealRequests(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "read_file", `{"file_path":"missing-1.ts"}`),
		toolTurn("c2", "read_file", `{"file_path":"missing-2.ts"}`),
		toolTurn("c3", "read_file", `{"file_path":"missing-3.ts"}`),
		toolTurn("c4", "read_file", `{"file_path":"missing-4.ts"}`),
		toolTurn("c5", "read_file", `{"file_path":"missing-5.ts"}`),
		toolTurn("c6", "read_file", `{"file_path":"missing-6.ts"}`),
		toolTurn("c7", "read_file", `{"file_path":"missing-7.ts"}`),
		toolTurn("c8", "read_file", `{"file_path":"missing-8.ts"}`),
		textTurn("done"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	rb := NewAdvisoryReadback()
	p := NewPipeline(PipelineOptions{})
	// 每轮 postTurn 投递同一 key、窗口 1 轮、模型永不满足（要求 bash）
	p.Register(RuntimeHook{
		Name: "nudge", Phase: PhasePostTurn,
		Run: func(ctx context.Context, h *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{
				Key: "k", Priority: 0.6, Category: CategoryDiscipline, Content: "跑个 bash",
				Expect: &AdvisoryExpectation{Kind: ExpectToolAppears, Tools: []string{"bash"}, WithinTurns: 1},
			})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus
	l.Readback = rb
	bus.SetHabituationPolicy(rb) // ← 本刀的接线点

	if err := l.Run(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}

	// ── 观察：请求体里该 advisory 的出现模式 ──
	var present []bool
	var escalated []bool
	for _, b := range sc.handlerBodies() {
		present = append(present, strings.Contains(b, `key=\"k\"`))
		escalated = append(escalated, strings.Contains(b, "未见执行"))
	}
	t.Logf("present=%v escalated=%v stats=%+v", present, escalated, rb.Stats()["k"])

	// ① 升级措辞出现过（streak >= 2 生效）
	anyEscalated := false
	for _, e := range escalated {
		if e {
			anyEscalated = true
		}
	}
	if !anyEscalated {
		t.Error("**升级措辞从未出现**——streak 未送达 bus 或阈值判定失效")
	}

	// ② 静音发生过（连续消失 >= 2 个请求）
	maxGap := 0
	gap := 0
	for _, p := range present {
		if p {
			gap = 0
		} else {
			gap++
			if gap > maxGap {
				maxGap = gap
			}
		}
	}
	if maxGap < 2 {
		t.Errorf("**静音未生效**——最长连续缺席仅 %d 个请求（应 >= 2）\n"+
			"present=%v（若全 true 说明 SetHabituationPolicy 未接或静音逻辑失效）", maxGap, present)
	}

	// ③ 静音期间不计入 Delivered（没渲染就不该记送达）
	s := rb.Stats()["k"]
	if s.Delivered >= len(sc.handlerBodies()) {
		t.Errorf("静音轮不应计入 Delivered：Delivered=%d 请求数=%d", s.Delivered, len(sc.handlerBodies()))
	}
	if s.IgnoredStreak < habituationSilenceStreak {
		t.Errorf("ignoredStreak=%d 应 >= %d（静音阈值）", s.IgnoredStreak, habituationSilenceStreak)
	}
}
