package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestE2EAdvisoryReachesPrompt —— **用户级验收**：
//
// 用户动作：真实 loop 跑一轮，模型调 write_file 写 .ts + run_tests，
// hook 投递提醒。
// 观察到：**下一轮发给模型的请求体里出现 advisory 块**。
//
// **这是 advisory 通路真正的最后一环**——此前 bus 有渲染能力但没人调用，
// 渲染结果进不了 prompt（与上一轮 hook 悬空同一模式的重演）。
func TestE2EAdvisoryReachesPrompt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "foo.ts")
	if err := os.WriteFile(target, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 三轮：write_file → run_tests → 终答。
	// 第 3 轮的请求体应含 advisory（在第 2 轮 postTurn 触发后渲染）。
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const x = 1\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "改 foo.ts 并跑测试"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// ── 观察：请求体里出现 advisory 块 ──
	found := false
	for i, body := range sc.bodies {
		if strings.Contains(body, "星域-advisory") || strings.Contains(body, "你改了 TS 文件") {
			found = true
			t.Logf("请求 %d 含 advisory 块", i)
		}
	}
	if !found {
		t.Errorf("**advisory 未进入 prompt**——bus 渲染结果没有被注入请求体。\n"+
			"共 %d 个请求，均无 advisory。这说明 bus 未接进 loop 的 prompt 组装路径。", len(sc.bodies))
	}
}

// TestE2EAdvisoryNotDuplicatedWithinRequest —— **单个请求内不重复**。
//
// **注意语义**：hook 每轮 postTurn 都会重新触发（条件持续成立），故 advisory
// 出现在多轮请求里是当前实现的正确行为——抑制跨轮重复属于 TS 的治理子系统
// （expect 核销 / observe 挂起 / key 冷却），本移植未包含。
//
// 这里断言的是**单个请求内**只有 1 份：若实现写回 l.messages，历史会累积，
// 第 N 轮请求里会有 N 份。
func TestE2EAdvisoryNotDuplicatedWithinRequest(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bar.ts")
	if err := os.WriteFile(target, []byte("export const y = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const y = 2\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		toolTurn("c3", "read_file", `{"file_path":"`+target+`"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "改 bar.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	sawAny := false
	for i, body := range sc.bodies {
		n := strings.Count(body, "你改了 TS 文件")
		if n > 0 {
			sawAny = true
		}
		if n > 1 {
			t.Errorf("请求 %d 含 %d 份 advisory——写回了 l.messages（历史累积）", i, n)
		}
	}
	if !sawAny {
		t.Error("应有请求含 advisory")
	}
}

// TestE2EAdvisoryWrappedAsSystemReminder —— **注入消息必须包 system-reminder**。
//
// 对账 TS 的 wrapSystemReminder：不包裹时每次注入都像真实用户边界，
// 触发 prompt engine 重建 appendix，打爆前缀缓存。
func TestE2EAdvisoryWrappedAsSystemReminder(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "w.ts")
	if err := os.WriteFile(target, []byte("export const w = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const w = 1\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "改 w.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	found := false
	for _, body := range sc.bodies {
		if strings.Contains(body, "你改了 TS 文件") {
			found = true
			// 注入的 content 必须以 <system-reminder> 开头
			if !strings.Contains(body, "<system-reminder>") {
				t.Errorf("注入消息未包 system-reminder——会被当成真实用户边界")
			}
		}
	}
	if !found {
		t.Fatal("未找到含 advisory 的请求")
	}
}

// TestE2EAdvisoryEmptyBlockNotInjected —— **空 advisory 不注入空消息**。
//
// 无 advisory 时不应追加多余消息（否则每轮都多一条空 system-reminder，
// 浪费 token 且污染历史形状）。
func TestE2EAdvisoryEmptyBlockNotInjected(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("你好")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	// 装 bus 但**不投递任何 advisory**
	l.Advisories = NewAdvisoryBus()

	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	if len(sc.bodies) == 0 {
		t.Fatal("应有请求")
	}
	body := sc.bodies[0]
	if strings.Contains(body, "星域-advisory") {
		t.Errorf("空 bus 不应注入 advisory 块，但请求体里有：%q", body[:200])
	}
	if strings.Contains(body, "system-reminder") {
		t.Errorf("空 bus 不应注入 system-reminder 消息：%q", body[:200])
	}
}

// TestE2EAdvisoryStarDomainBudget —— **星域预算生效**：天权只 1 条。
//
// 对账 advisoryBudgetForDomain：自主判断型星域减量。
func TestE2EAdvisoryStarDomainBudget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "d.ts")
	if err := os.WriteFile(target, []byte("export const d = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const d = 1\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	// **天权**——预算 1
	l := newTestLoopWithRegistry(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, StarDomain: "天权",
	}, reg)

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	// 投递 3 条不同 category 的 advisory
	p.Register(RuntimeHook{
		Name: "seed-three", Phase: PhasePostTurn,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			bus.Submit(AdvisoryEntry{Key: "a", Priority: 0.9, Category: CategoryDiscipline, Content: "A"})
			bus.Submit(AdvisoryEntry{Key: "b", Priority: 0.8, Category: CategoryRepair, Content: "B"})
			bus.Submit(AdvisoryEntry{Key: "c", Priority: 0.7, Category: CategoryTodo, Content: "C"})
			return nil
		},
	})
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "改 d.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// 找含 advisory 的请求，数 <entry 个数
	for _, body := range sc.bodies {
		if strings.Contains(body, "星域-advisory") {
			n := strings.Count(body, `<entry key=`)
			if n != 1 {
				t.Errorf("天权域应只渲染 1 条 advisory，实际 %d 条", n)
			}
			return
		}
	}
	t.Error("未找到含 advisory 的请求")
}

// TestE2EAdvisoryNotPersistedAfterInjectionTurn —— **请求级注入 vs 持久化**。
//
// **判别力设计**：`TestE2EAdvisoryNotDuplicatedAcrossTurns` 里注入恰好发生在
// **最后一轮**——写回 l.messages 与请求级注入结果相同，无法区分（M4 变异红 0）。
//
// 这里让注入发生在**中间轮**，注入后仍有请求。判据是**消息条数**而非出现次数：
//
//   - 请求级注入：每轮请求的 messages 数 = 历史条数 + 1（advisory 不在历史里）
//   - 写回 l.messages：advisory 进历史，**后续每轮都多累积一条**——
//     历史长度会随轮次单调增长，且下一轮请求里该 advisory 的**条数翻倍**
//     （历史里一份 + 本轮新注入一份）
//
// **注意**：hook 每轮 postTurn 都会重新触发（条件持续成立——touchedTSFiles
// 仍是 true、run_tests 仍在 5 条窗口内、sawTypecheck 仍是 false），故 advisory
// 在**每一轮**都出现是当前实现的正确行为。抑制重复属于 TS 的治理子系统
// （expect 核销 / observe 挂起 / key 冷却），本移植尚未包含。
func TestE2EAdvisoryNotPersistedAfterInjectionTurn(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "p.ts")
	if err := os.WriteFile(target, []byte("export const p = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const p = 1\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		toolTurn("c3", "read_file", `{"file_path":"`+target+`"}`),
		toolTurn("c4", "read_file", `{"file_path":"`+target+`"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(t.Context(), "改 p.ts 再读几次"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	if len(sc.bodies) < 4 {
		t.Fatalf("应有至少 4 个请求，实际 %d", len(sc.bodies))
	}

	// **关键判据**：单个请求里 advisory 的**条数**。
	// 请求级注入 → 每轮恰好 1 条（本轮 render 的那份）。
	// 写回 l.messages → 历史里累积，第 N 轮有 N 份。
	for i, body := range sc.bodies {
		n := strings.Count(body, "你改了 TS 文件")
		if n > 1 {
			t.Errorf("请求 %d 含 %d 份 advisory——**写回了 l.messages**（历史累积）\n"+
				"请求级注入应每轮恰好 1 份", i, n)
		}
	}
}

// TestE2ENilAdvisoriesDoesNotBreak —— 未装 bus 时 loop 照常工作。
func TestE2ENilAdvisoriesDoesNotBreak(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("你好")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	// l.Advisories 为 nil
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("未装 bus 时 loop 不应失败：%v", err)
	}
}
