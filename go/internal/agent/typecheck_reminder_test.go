package agent

import (
	"strings"
	"testing"
)

// 记录型 sink——捕获 hook 投递的 advisory（测试用）。
type recordingSink struct {
	entries []AdvisoryEntry
}

func (s *recordingSink) Submit(e AdvisoryEntry) {
	s.entries = append(s.entries, e)
}

// TestTypecheckReminderTriggersOnTsEditWithoutTypecheck —— **核心**：
// 改了 TS 文件 + 跑了测试 + 没跑 typecheck → 投递提醒。
//
// 对账 typecheck-reminder-hook.ts 的触发条件（三条全真才触发）。
func TestTypecheckReminderTriggersOnTsEditWithoutTypecheck(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles: true,  // 改了 TS
		SawTypecheck:   false, // 没跑 typecheck
		RecentToolHistory: []ToolHistoryEntry{
			{Tool: "write_file", Status: "ok"},
			{Tool: "run_tests", Status: "ok"}, // 跑了测试
		},
	}}

	if err := h.Run(t.Context(), ctx, nil); err != nil {
		t.Fatalf("hook 不应报错：%v", err)
	}

	if len(sink.entries) != 1 {
		t.Fatalf("应投递 1 条 advisory，得到 %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Key != "typecheck-reminder" {
		t.Errorf("key 应为 typecheck-reminder，得到 %q", e.Key)
	}
	if e.Category != CategoryTypecheck {
		t.Errorf("category 应为 typecheck，得到 %q", e.Category)
	}
	if e.Tier != TierOperational {
		t.Errorf("tier 应为 operational，得到 %q", e.Tier)
	}
	if e.Priority != 0.6 {
		t.Errorf("priority 应为 0.6，得到 %v", e.Priority)
	}
	if e.TTL != 1 {
		t.Errorf("ttl 应为 1，得到 %d", e.TTL)
	}
}

// TestTypecheckReminderContentVerbatim —— **逐字对账 content**。
//
// 用户可见文案必须逐字取自 TS 原文——"意思对"不等于"文本对"。
func TestTypecheckReminderContentVerbatim(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		SawTypecheck:      false,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	if len(sink.entries) != 1 {
		t.Fatalf("应投递 1 条，得到 %d", len(sink.entries))
	}

	want := "【天梁】你改了 TS 文件、跑了测试,但没跑类型检查。esbuild/tsx 只转译不查类型——重复键/重复成员/悬空引用都不会报。交付前跑 `npm run typecheck`(或 `tsc --noEmit`)再声明完成。"
	if sink.entries[0].Content != want {
		t.Errorf("content 必须逐字一致\nwant: %q\ngot:  %q", want, sink.entries[0].Content)
	}
}

// TestTypecheckReminderExpectPredicate —— expect 谓词（核销 + 挂起观察）。
func TestTypecheckReminderExpectPredicate(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	e := sink.entries[0]
	if e.Expect == nil {
		t.Fatal("expect 不应为 nil")
	}
	if e.Expect.Kind != ExpectVerifyAttempted {
		t.Errorf("expect.kind 应为 verify_attempted，得到 %q", e.Expect.Kind)
	}
	if e.Expect.WithinTurns != 2 {
		t.Errorf("expect.withinTurns 应为 2，得到 %d", e.Expect.WithinTurns)
	}
	if e.Observe == nil || e.Observe.Turns != 1 {
		t.Errorf("observe.turns 应为 1，得到 %+v", e.Observe)
	}
}

// TestTypecheckReminderNoTsFiles —— 没改 TS 文件 → 不投递。
func TestTypecheckReminderNoTsFiles(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    false,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	if len(sink.entries) != 0 {
		t.Errorf("没改 TS 文件不应投递，得到 %d 条", len(sink.entries))
	}
}

// TestTypecheckReminderAlreadyTypechecked —— 已跑过 typecheck → 不投递。
func TestTypecheckReminderAlreadyTypechecked(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		SawTypecheck:      true, // 已跑过
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	if len(sink.entries) != 0 {
		t.Errorf("已跑 typecheck 不应投递，得到 %d 条", len(sink.entries))
	}
}

// TestTypecheckReminderNoTestsRun —— 没跑测试 → 不投递（不是"完成"时刻）。
func TestTypecheckReminderNoTestsRun(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		SawTypecheck:      false,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "write_file"}, {Tool: "read_file"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	if len(sink.entries) != 0 {
		t.Errorf("没跑测试不应投递，得到 %d 条", len(sink.entries))
	}
}

// TestTypecheckReminderRunTestsInWindowOnly —— **窗口语义**：
// run_tests 必须在 recentToolHistory 里，任务级标志不算。
func TestTypecheckReminderRunTestsInWindowOnly(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)

	// 窗口里只有 read_file（run_tests 滚出窗口了）→ 不投递
	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		SawTypecheck:      false,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "read_file"}, {Tool: "bash"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	if len(sink.entries) != 0 {
		t.Errorf("run_tests 不在窗口内不应投递，得到 %d 条", len(sink.entries))
	}
}

// TestTypecheckReminderHookMetadata —— 阶段与名字。
func TestTypecheckReminderHookMetadata(t *testing.T) {
	h := NewTypecheckReminderHook(&recordingSink{})
	if h.Name != "typecheck-reminder" {
		t.Errorf("name 应为 typecheck-reminder，得到 %q", h.Name)
	}
	if h.Phase != PhasePostTurn {
		t.Errorf("phase 应为 postTurn，得到 %q", h.Phase)
	}
}

// TestTypecheckReminderIntegratesWithPipeline —— **端到端**：
// 通过 Pipeline 注册并执行，验证 hook 与管线协作。
func TestTypecheckReminderIntegratesWithPipeline(t *testing.T) {
	sink := &recordingSink{}
	p := NewPipeline(PipelineOptions{})
	p.Register(NewTypecheckReminderHook(sink))

	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	p.RunPostTurn(t.Context(), ctx)

	if len(sink.entries) != 1 {
		t.Fatalf("经管线应投递 1 条，得到 %d", len(sink.entries))
	}

	// 统计应记账（hook 名 + 阶段）
	stats := p.Stats()
	if len(stats) != 1 || stats[0].ID != "typecheck-reminder" || stats[0].Runs != 1 {
		t.Errorf("管线统计不符：%+v", stats)
	}
	// manifest 应含该 hook
	m := p.Manifest()
	if len(m) != 1 || m[0].ID != "typecheck-reminder" || !m[0].Enabled {
		t.Errorf("manifest 不符：%+v", m)
	}
}

// TestTypecheckReminderContentNoXMLTags —— content 是单行纯文本（不含 XML 标签）。
//
// 对账 AdvisoryEntry 注释：「渲染内容 — 单行纯文本，不包含 XML 标签」。
func TestTypecheckReminderContentNoXMLTags(t *testing.T) {
	sink := &recordingSink{}
	h := NewTypecheckReminderHook(sink)
	ctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{
		TouchedTSFiles:    true,
		RecentToolHistory: []ToolHistoryEntry{{Tool: "run_tests"}},
	}}
	_ = h.Run(t.Context(), ctx, nil)

	c := sink.entries[0].Content
	if strings.Contains(c, "\n") {
		t.Error("content 应为单行（不含换行）")
	}
	if strings.Contains(c, "<") || strings.Contains(c, ">") {
		t.Errorf("content 不应含 XML 标签：%q", c)
	}
}
