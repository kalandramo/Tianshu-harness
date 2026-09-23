package agent

import (
	"context"
	"strings"
	"testing"
)

// hashEditEvent 构造一个 hash_edit 的 postTool 事件。
func hashEditEvent(filePath string) *RuntimeToolEvent {
	return &RuntimeToolEvent{
		Name:   "hash_edit",
		Input:  map[string]any{"file_path": filePath, "anchors": []any{"L1:aabbccdd"}},
		Target: filePath,
	}
}

// 同轮同文件第 2 次 hash_edit → 提交 advisory。
func TestEditAdvisoryFiresOnSecondHashEdit(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}

	// 第 1 次：不触发
	h.Run(context.Background(), hctx, hashEditEvent("src/a.ts"))
	if len(sink.entries) != 0 {
		t.Fatalf("第 1 次不应触发，得到 %d 条", len(sink.entries))
	}

	// 第 2 次：触发
	h.Run(context.Background(), hctx, hashEditEvent("src/a.ts"))
	if len(sink.entries) != 1 {
		t.Fatalf("第 2 次应触发，得到 %d 条", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Key != "edit-tool-advisory" {
		t.Errorf("key = %q", e.Key)
	}
	if e.Priority != 0.5 {
		t.Errorf("priority = %v, want 0.5", e.Priority)
	}
	if e.Category != CategoryDiscipline {
		t.Errorf("category = %q", e.Category)
	}
	if e.TTL != 1 {
		t.Errorf("ttl = %d, want 1", e.TTL)
	}
	// 文案含次数与替代方案
	for _, want := range []string{"2 次", "edit_file", "write_file"} {
		if !strings.Contains(e.Content, want) {
			t.Errorf("文案应含 %q：%q", want, e.Content)
		}
	}
}

// **反证**：不同文件各自计数，互不影响。
func TestEditAdvisoryPerFileCounting(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}

	h.Run(context.Background(), hctx, hashEditEvent("a.ts"))
	h.Run(context.Background(), hctx, hashEditEvent("b.ts"))
	if len(sink.entries) != 0 {
		t.Errorf("不同文件各 1 次不应触发，得到 %d 条", len(sink.entries))
	}
}

// **反证**：轮次变化重置计数。
//
// 这是 turn-scoped 语义的核心——跨轮累积会让「上一轮编辑过」也触发。
func TestEditAdvisoryResetsOnTurnChange(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)

	// 第 1 轮编辑 1 次
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 1},
	}, hashEditEvent("a.ts"))

	// 第 2 轮再编辑 1 次 → 不应触发（计数已重置）
	h.Run(context.Background(), &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{Turn: 2},
	}, hashEditEvent("a.ts"))

	if len(sink.entries) != 0 {
		t.Errorf("**计数未按轮重置**——跨轮累积了，得到 %d 条", len(sink.entries))
	}
}

// **反证**：非 hash_edit 工具不计入。
func TestEditAdvisoryIgnoresOtherTools(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}

	for i := 0; i < 5; i++ {
		h.Run(context.Background(), hctx, &RuntimeToolEvent{
			Name: "edit_file", Input: map[string]any{"file_path": "a.ts"},
		})
	}
	if len(sink.entries) != 0 {
		t.Errorf("edit_file 不应计入 hash_edit 计数，得到 %d 条", len(sink.entries))
	}
}

// file_path 缺失时回退 Target。
func TestEditAdvisoryFallsBackToTarget(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}

	ev := &RuntimeToolEvent{Name: "hash_edit", Target: "from-target.ts"}
	h.Run(context.Background(), hctx, ev)
	h.Run(context.Background(), hctx, ev)
	if len(sink.entries) != 1 {
		t.Errorf("回退 Target 后应触发，得到 %d 条", len(sink.entries))
	}
}

// **反证**：file_path 与 Target 都空 → 不触发（不 panic）。
func TestEditAdvisoryNoPathNoFire(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}
	for i := 0; i < 3; i++ {
		h.Run(context.Background(), hctx, &RuntimeToolEvent{Name: "hash_edit"})
	}
	if len(sink.entries) != 0 {
		t.Errorf("无路径不应触发，得到 %d 条", len(sink.entries))
	}
}

// 第 3 次也触发（每次 >= 2 都提交）。
func TestEditAdvisoryFiresEveryTimeAboveThreshold(t *testing.T) {
	sink := &spySink{}
	h := NewEditToolAdvisoryHook(sink)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}
	for i := 0; i < 4; i++ {
		h.Run(context.Background(), hctx, hashEditEvent("a.ts"))
	}
	// 第 2/3/4 次各触发一次 = 3 条
	if len(sink.entries) != 3 {
		t.Errorf("第 2..4 次应各触发，得到 %d 条", len(sink.entries))
	}
	// 最后一条应显示 4 次
	if !strings.Contains(sink.entries[2].Content, "4 次") {
		t.Errorf("末条应显示 4 次：%q", sink.entries[2].Content)
	}
}

// nil 输入不 panic。
func TestEditAdvisoryNilSafe(t *testing.T) {
	h := NewEditToolAdvisoryHook(&spySink{})
	if err := h.Run(context.Background(), nil, nil); err != nil {
		t.Errorf("nil 输入不应报错：%v", err)
	}
	h2 := NewEditToolAdvisoryHook(nil)
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{Turn: 1}}
	for i := 0; i < 3; i++ {
		if err := h2.Run(context.Background(), hctx, hashEditEvent("a.ts")); err != nil {
			t.Errorf("nil bus 不应报错：%v", err)
		}
	}
}

// 元数据对账。
func TestEditAdvisoryMetadata(t *testing.T) {
	h := NewEditToolAdvisoryHook(&spySink{})
	if h.Name != "edit-tool-advisory" {
		t.Errorf("name = %q", h.Name)
	}
	if h.Phase != PhasePostTool {
		t.Errorf("phase = %q, want postTool", h.Phase)
	}
}
