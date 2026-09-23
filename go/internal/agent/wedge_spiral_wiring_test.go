package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// ── 接线验证：两条链路真的被 loop 调用 ──

// TestWedgeGuardWiredIntoLoop —— **接线反证**。
//
// 单元测试证明 `wedgeState.observeBatch` 逻辑正确，但**不证明 loop 调了它**。
// 本测试用真实 loop 跑「连续同批次全错」，断言 run 提前终止。
//
// **判据**：脚本给 5 条相同的失败工具调用响应 + 1 条文本终答。
// 若守卫生效，模型调用次数应远小于 6（在第 3 次重复时终止）。
func TestWedgeGuardWiredIntoLoop(t *testing.T) {
	// 同一个不存在的文件 → read_file 每次失败，且批次指纹相同。
	badTurn := toolTurnArgs("c1", "read_file", map[string]any{"file_path": "does-not-exist.txt"})

	sc := &scriptedServer{responses: []string{
		badTurn, badTurn, badTurn, badTurn, badTurn, textTurn("结束"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, MaxTurns: 20})
	var events []Event
	l.Emit = func(e Event) { events = append(events, e) }

	if err := l.Run(context.Background(), "反复读不存在的文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 守卫应在第 3 次重复后终止——模型调用数应为 3（而非 6）。
	if sc.calls >= 5 {
		t.Errorf("模型调用 %d 次——wedge 守卫未生效（应约 3 次）", sc.calls)
	}
	// 应有错误事件说明死循环
	found := false
	for _, e := range events {
		if e.Kind == "error" && containsStr(e.Text, "死循环") {
			found = true
		}
	}
	if !found {
		t.Errorf("应发出死循环错误事件：%+v", events)
	}
}

// **反证**：不同批次的失败**不**触发守卫（避免误伤正常重试）。
func TestWedgeGuardNotTriggeredByDifferentBatches(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "a.txt"}),
		toolTurnArgs("c2", "read_file", map[string]any{"file_path": "b.txt"}),
		toolTurnArgs("c3", "read_file", map[string]any{"file_path": "c.txt"}),
		toolTurnArgs("c4", "read_file", map[string]any{"file_path": "d.txt"}),
		textTurn("结束"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, MaxTurns: 20})
	if err := l.Run(context.Background(), "读四个不同文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	// 四次不同批次后应正常走到第 5 条文本终答。
	if sc.calls != 5 {
		t.Errorf("模型调用 = %d, want 5（不同批次不应触发守卫）", sc.calls)
	}
}

// TestSpiralHookWiredIntoLoop —— **接线反证**（快照产生端）。
//
// 断言 `recordTurnOutcome` 真的把上一轮结果写进了快照——即 hook 在下一轮
// 看到的是**上一轮**的 thinking 长度与工具使用情况。
func TestSpiralHookWiredIntoLoop(t *testing.T) {
	// 第 1 轮：长思考 + 工具调用（read_file）
	// 第 2 轮：文本终答
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "nope.txt"}),
		textTurn("结束"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})

	// 捕获 preTurn 阶段的快照
	var seen []*RuntimeHookSnapshot
	sink := &spySink{}
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: sink})
	p := NewPipeline(PipelineOptions{})
	// 包一层记录快照
	inner := h.Run
	h.Run = func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
		if hctx != nil && hctx.Snapshot != nil {
			cp := *hctx.Snapshot
			seen = append(seen, &cp)
		}
		return inner(ctx, hctx, tool)
	}
	p.Register(h)
	l.Hooks = p

	if err := l.Run(context.Background(), "读文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	if len(seen) < 2 {
		t.Fatalf("preTurn 应至少调用 2 次，得到 %d", len(seen))
	}
	// 第 2 轮的快照应反映**第 1 轮**（有工具调用）。
	if !seen[1].LastTurnHadTools {
		t.Errorf("第 2 轮快照的 LastTurnHadTools 应为 true（第 1 轮调了 read_file）")
	}
	// 第 1 轮尚无上一轮数据（-1）。
	if seen[0].LastThinkingLength != -1 {
		t.Errorf("第 1 轮 LastThinkingLength 应为 -1（无上一轮），得到 %d", seen[0].LastThinkingLength)
	}
}

// **反证**：第 2 轮无工具时，第 3 轮快照的 LastTurnHadTools 应为 false。
func TestSpiralSnapshotReflectsToolUsage(t *testing.T) {
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "nope.txt"}),
		textTurn("结束"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})

	var seen []*RuntimeHookSnapshot
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: &spySink{}})
	inner := h.Run
	h.Run = func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
		if hctx != nil && hctx.Snapshot != nil {
			cp := *hctx.Snapshot
			seen = append(seen, &cp)
		}
		return inner(ctx, hctx, tool)
	}
	p := NewPipeline(PipelineOptions{})
	p.Register(h)
	l.Hooks = p

	if err := l.Run(context.Background(), "读文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	// 第 1 轮快照：无上一轮
	if seen[0].LastThinkingLength != -1 || seen[0].LastTurnHadTools {
		t.Errorf("第 1 轮应无上一轮数据：len=%d hadTools=%v",
			seen[0].LastThinkingLength, seen[0].LastTurnHadTools)
	}
}
