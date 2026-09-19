package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// TestE2EHooksReachedFromRealLoop —— **用户级验收**：
//
// 用户动作：真实 agent loop 跑一轮，模型调 write_file 写 .ts 文件、再调
// run_tests。观察到：typecheck-reminder hook 经生产路径被触发并投递 advisory。
//
// **这是「hook 不再悬空」的证据**——此前 hook 只在定义文件与测试里出现，
// loop.go 零引用（grep 确认），整条链是断的。
func TestE2EHooksReachedFromRealLoop(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "foo.ts")
	if err := os.WriteFile(target, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 脚本：第 1 轮调 write_file 写 .ts；第 2 轮调 run_tests；第 3 轮终答。
	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const x = 1\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	// ── 装 hook 管线（含真实 hook）──
	sink := &recordingSink{}
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(sink))
	l.Hooks = p

	// ── 跑真实 loop ──
	if err := l.Run(t.Context(), "改一下 foo.ts 并跑测试"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	// ── 观察：hook 被触发（用户级可观察结果）──
	if len(sink.entries) != 1 {
		t.Fatalf("经真实 loop 应触发 typecheck-reminder 并投递 1 条，得到 %d 条\n"+
			"（若为 0：hook 未被生产路径触达，链路仍断）", len(sink.entries))
	}
	if sink.entries[0].Key != "typecheck-reminder" {
		t.Errorf("投递的 key 不符：%q", sink.entries[0].Key)
	}

	// ── 观察：管线统计有记账（证明经过 Pipeline 而非直接调用）──
	stats := p.Stats()
	if len(stats) != 1 || stats[0].ID != "typecheck-reminder" || stats[0].Runs < 1 {
		t.Errorf("管线统计应记录该 hook 执行，得到 %+v", stats)
	}
}

// TestE2EHookNotTriggeredWhenTypecheckRan —— 反向验收：
// 跑了 typecheck 则**不**投递提醒（避免误报）。
func TestE2EHookNotTriggeredWhenTypecheckRan(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bar.ts")
	if err := os.WriteFile(target, []byte("export const y = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const y = 2\n"}`),
		toolTurn("c2", "run_tests", `{}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	sink := &recordingSink{}
	p := NewPipeline(PipelineOptions{})
	// 用**包一层**的 hook 模拟「写文件后又跑了 typecheck」的时序：
	// 真实场景里模型调 write_file 后**再**调 typecheck 工具，故 sawTypecheck
	// 会在写之后被置位。这里用 postTool hook 在 run_tests 后置位。
	p.Register(RuntimeHook{
		Name: "simulate-typecheck", Phase: PhasePostTool,
		Run: func(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
			if tool != nil && tool.Name == "run_tests" {
				// 直接置位**累积状态**（不是快照副本——快照每阶段重建）
				l.hookState.sawTypecheck = true
			}
			return nil
		},
	})
	p.Register(NewTypecheckReminderHook(sink))
	l.Hooks = p

	if err := l.Run(t.Context(), "改 bar.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	if len(sink.entries) != 0 {
		t.Errorf("已跑 typecheck 不应投递，得到 %d 条", len(sink.entries))
	}
}

// TestE2EConsistencyCheckReachedFromRealLoop —— 第二个 hook 的端到端可达性。
//
// 用户动作：真实 loop 里 write_file 写文件。观察到：consistency-check 触发，
// effects.MarkClaimStale 被调用（引用该文件的 claim 被标过期）。
func TestE2EConsistencyCheckReachedFromRealLoop(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "baz.ts")
	if err := os.WriteFile(target, []byte("export const z = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurn("c1", "write_file", `{"file_path":"`+target+`","content":"export const z = 3\n"}`),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: root})
	l := newTestLoopWithRegistry(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root}, reg)

	// 装 consistency-check（claim 引用了目标文件）
	var marked []string
	p := NewPipeline(PipelineOptions{})
	p.Register(NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{
			{ID: "claim_baz", Text: "baz.ts 里有 z", Evidence: []EvidenceRef{{Path: target}}},
		}
	}))
	l.Hooks = p
	l.Effects = RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }}

	if err := l.Run(t.Context(), "改 baz.ts"); err != nil {
		t.Fatalf("loop 运行失败：%v", err)
	}

	if len(marked) != 1 || marked[0] != "claim_baz" {
		t.Errorf("经真实 loop 应标记 claim_baz，得到 %v\n（若为空：hook 未被触达或 effects 未透传）", marked)
	}
}

// TestE2ENilHooksDoesNotBreak —— 未装 hook 时 loop 照常工作（向后兼容）。
func TestE2ENilHooksDoesNotBreak(t *testing.T) {
	sc := &scriptedServer{responses: []string{textTurn("你好")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100})
	// l.Hooks 为 nil
	if err := l.Run(t.Context(), "hi"); err != nil {
		t.Fatalf("未装 hook 时 loop 不应失败：%v", err)
	}
}

// TestHookSnapshotTaskLevelSurvivesWindow —— **任务级字段跨轮存活**。
//
// 对账 TS 注释：`Task-level, not windowed — survives a long turn where the
// edit scrolled out of recentToolHistory`。这是 typecheck-reminder 能在长
// 回合里仍然生效的前提。
func TestHookSnapshotTaskLevelSurvivesWindow(t *testing.T) {
	l := &Loop{}

	// 写一个 TS 文件（置 touchedTSFiles）
	l.recordToolForHooks(&RuntimeToolEvent{Name: "write_file", Success: true, Target: "a.ts"})
	if !l.hookState.touchedTSFiles {
		t.Fatal("写 TS 文件应置 touchedTSFiles")
	}

	// 灌入远超窗口的无关工具调用，把那次编辑挤出窗口
	for i := 0; i < toolHistoryWindow*3; i++ {
		l.recordToolForHooks(&RuntimeToolEvent{Name: "read_file", Success: true, Target: "x.go"})
	}

	// 窗口已滚出，但任务级标志仍在
	snap := l.buildRuntimeSnapshot(1)
	if !snap.TouchedTSFiles {
		t.Error("touchedTSFiles 是任务级——不应因窗口滚出而失效")
	}
	for _, h := range snap.RecentToolHistory {
		if h.Tool == "write_file" {
			t.Error("write_file 应已滚出窗口")
		}
	}
}

// TestHookSnapshotWindowCapped —— 窗口条数上限。
func TestHookSnapshotWindowCapped(t *testing.T) {
	l := &Loop{}
	for i := 0; i < toolHistoryWindow*4; i++ {
		l.recordToolForHooks(&RuntimeToolEvent{Name: "read_file", Success: true})
	}
	if got := len(l.hookState.recentToolHistory); got != toolHistoryWindow {
		t.Errorf("窗口应封顶在 %d，得到 %d", toolHistoryWindow, got)
	}
}

// TestHookSnapshotWriteTsResetsTypecheckFlag —— 写 TS 文件重置 typecheck 标志。
//
// 语义：跑过 typecheck 后**又改了** TS 文件 → 需要重新提醒。
func TestHookSnapshotWriteTsResetsTypecheckFlag(t *testing.T) {
	l := &Loop{}
	l.hookState.sawTypecheck = true

	l.recordToolForHooks(&RuntimeToolEvent{Name: "write_file", Success: true, Target: "a.ts"})

	if l.hookState.sawTypecheck {
		t.Error("写 TS 文件后 sawTypecheck 应重置为 false（需重新检查）")
	}
}

// TestHookSnapshotFailedWriteDoesNotSetFlag —— **失败的工具调用不置任务级标志**。
//
// 写失败的文件没进磁盘，无需 typecheck 提醒。
func TestHookSnapshotFailedWriteDoesNotSetFlag(t *testing.T) {
	l := &Loop{}
	l.recordToolForHooks(&RuntimeToolEvent{Name: "write_file", Success: false, Target: "a.ts"})

	if l.hookState.touchedTSFiles {
		t.Error("失败的写操作不应置 touchedTSFiles")
	}
}

// TestHookSnapshotRunTestsIsNotTypecheck —— **核心语义**：
// run_tests 不算 typecheck（这正是 typecheck-reminder 存在的理由）。
func TestHookSnapshotRunTestsIsNotTypecheck(t *testing.T) {
	l := &Loop{}
	l.recordToolForHooks(&RuntimeToolEvent{Name: "run_tests", Success: true})

	if l.hookState.sawTypecheck {
		t.Error("run_tests 不应被当作 typecheck——esbuild 只转译不查类型")
	}
}

// TestToolTargetUsesFilePathField —— **字段名必须是 file_path**。
//
// 曾误用 `path`（read_file 的 schema 修正过同类问题）——会让 target 静默为空。
func TestToolTargetUsesFilePathField(t *testing.T) {
	if got := toolTarget(map[string]any{"file_path": "a.ts"}); got != "a.ts" {
		t.Errorf("file_path 应被提取，得到 %q", got)
	}
	if got := toolTarget(map[string]any{"path": "a.ts"}); got != "" {
		t.Errorf("`path` 不是工具 schema 的字段名——不应提取，得到 %q", got)
	}
	if got := toolTarget(map[string]any{"command": "ls"}); got != "ls" {
		t.Errorf("bash 的 command 应作为 target，得到 %q", got)
	}
	if got := toolTarget(nil); got != "" {
		t.Errorf("nil input 应返回空，得到 %q", got)
	}
}
