package agent

import (
	"testing"
)

// TestConsistencyCheckMarksStaleOnWrite —— **核心**：写入某文件后，
// 引用该文件旧状态的 claim 被标记过期。
func TestConsistencyCheckMarksStaleOnWrite(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{
			{ID: "claim_a", Text: "a.ts 里有函数 X", Evidence: []EvidenceRef{{Path: "src/a.ts"}}},
			{ID: "claim_b", Text: "b.ts 里有类 Y", Evidence: []EvidenceRef{{Path: "src/b.ts"}}},
		}
	})

	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects: RuntimeHookEffects{
			MarkClaimStale: func(id string) { marked = append(marked, id) },
		},
	}

	// 写 a.ts → claim_a 应被标过期（claim_b 不该）
	err := h.Run(t.Context(), hctx, &RuntimeToolEvent{
		Name: "write_file", Target: "src/a.ts", Success: true,
	})
	if err != nil {
		t.Fatalf("hook 不应报错：%v", err)
	}

	if len(marked) != 1 || marked[0] != "claim_a" {
		t.Errorf("应只标记 claim_a，得到 %v", marked)
	}
}

// TestConsistencyCheckPathSuffixMatching —— 路径**双向后缀**匹配。
//
// 对账 TS：`e.path === target || target.endsWith(e.path) || e.path.endsWith(target)`。
// 三条分支都要覆盖——漏掉任何一条都会漏标。
//
// **注意最后两个用例**：TS 的 endsWith 是**裸字符串后缀匹配**，不是路径段匹配。
// 故 `src/nota.ts` 会被 `a.ts` 的 claim 命中（误报）。这是 TS 的既有行为——
// 移植**忠实复刻**，不顺手"修好"（改了会让 Go 侧标记结果与 TS 分叉）。
// 已用 `node -e` 实测 TS 行为确认（`target.endsWith(path)` → true）。
func TestConsistencyCheckPathSuffixMatching(t *testing.T) {
	cases := []struct {
		name   string
		claim  string
		target string
		want   bool
	}{
		{"完全相同", "src/a.ts", "src/a.ts", true},
		{"target 以 claim 路径结尾", "a.ts", "src/a.ts", true},
		{"claim 路径以 target 结尾", "src/a.ts", "a.ts", true},
		{"无关路径", "src/b.ts", "src/a.ts", false},
		// **TS 的误报**：裸后缀匹配不认路径段边界。忠实复刻。
		{"后缀巧合也被命中（TS 既有行为）", "a.ts", "src/nota.ts", true},
		{"完全无关的后缀", "zzz.ts", "src/a.ts", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var marked []string
			h := NewConsistencyCheckHook(func() []FileObservation {
				return []FileObservation{
					{ID: "obs1", Evidence: []EvidenceRef{{Path: c.claim}}},
				}
			})
			hctx := &RuntimeHookContext{
				Snapshot: &RuntimeHookSnapshot{},
				Effects: RuntimeHookEffects{
					MarkClaimStale: func(id string) { marked = append(marked, id) },
				},
			}
			_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{
				Name: "write_file", Target: c.target, Success: true,
			})

			got := len(marked) > 0
			if got != c.want {
				t.Errorf("claim=%q target=%q：want marked=%v，got %v", c.claim, c.target, c.want, got)
			}
		})
	}
}

// TestConsistencyCheckEditFileAlsoTriggers —— edit_file 与 write_file 同等对待。
func TestConsistencyCheckEditFileAlsoTriggers(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}}}
	})
	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}

	_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: "edit_file", Target: "a.ts", Success: true})
	if len(marked) != 1 {
		t.Errorf("edit_file 应触发，得到 %v", marked)
	}
}

// TestConsistencyCheckNonWriteToolIgnored —— 非写工具不触发。
func TestConsistencyCheckNonWriteToolIgnored(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}}}
	})
	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}

	for _, tool := range []string{"read_file", "bash", "grep", "run_tests"} {
		_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: tool, Target: "a.ts", Success: true})
	}
	if len(marked) != 0 {
		t.Errorf("非写工具不应触发，得到 %v", marked)
	}
}

// TestConsistencyCheckNoTargetIgnored —— 无 target 不触发。
func TestConsistencyCheckNoTargetIgnored(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}}}
	})
	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}

	_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: "write_file", Target: "", Success: true})
	if len(marked) != 0 {
		t.Errorf("无 target 不应触发，得到 %v", marked)
	}
}

// TestConsistencyCheckEvidenceWithoutPathIgnored —— 证据项无 path 时跳过。
func TestConsistencyCheckEvidenceWithoutPathIgnored(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{
			{ID: "obs1", Evidence: []EvidenceRef{{Path: ""}}}, // 无 path
		}
	})
	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}

	_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: "write_file", Target: "a.ts", Success: true})
	if len(marked) != 0 {
		t.Errorf("无 path 的证据不应触发，得到 %v", marked)
	}
}

// TestConsistencyCheckMultipleClaimsSameFile —— 多个 claim 引用同一文件时全标。
func TestConsistencyCheckMultipleClaimsSameFile(t *testing.T) {
	var marked []string
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{
			{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}},
			{ID: "obs2", Evidence: []EvidenceRef{{Path: "a.ts"}}},
			{ID: "obs3", Evidence: []EvidenceRef{{Path: "other.ts"}}},
		}
	})
	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}

	_ = h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: "write_file", Target: "a.ts", Success: true})
	if len(marked) != 2 {
		t.Errorf("应标记 2 个 claim，得到 %v", marked)
	}
}

// TestConsistencyCheckHookMetadata —— 阶段与名字。
func TestConsistencyCheckHookMetadata(t *testing.T) {
	h := NewConsistencyCheckHook(func() []FileObservation { return nil })
	if h.Name != "consistency-check" {
		t.Errorf("name 应为 consistency-check，得到 %q", h.Name)
	}
	if h.Phase != PhasePostTool {
		t.Errorf("phase 应为 postTool，得到 %q", h.Phase)
	}
}

// TestConsistencyCheckEffectsNilDoesNotPanic —— **关键**：
// effects 未接线时 hook 不应 panic。
//
// 对账 TS 的 `createRuntimeHookContext`——它把未提供的 effect 填成 noop
// （`effects.markClaimStale ?? noop`）。Go 侧 struct 零值是 nil func，
// 直接调用会 panic。**这是 Go 移植的特有陷阱**：TS 的 undefined 调用在
// 类型层就被 `??` 兜住了，Go 没有对应机制。
func TestConsistencyCheckEffectsNilDoesNotPanic(t *testing.T) {
	h := NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}}}
	})

	// Effects 全零值（所有 func 为 nil）
	hctx := &RuntimeHookContext{Snapshot: &RuntimeHookSnapshot{}}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("effects 未接线时不应 panic，但 panic 了：%v", r)
		}
	}()

	err := h.Run(t.Context(), hctx, &RuntimeToolEvent{Name: "write_file", Target: "a.ts", Success: true})
	if err != nil {
		t.Errorf("hook 不应报错：%v", err)
	}
}

// TestConsistencyCheckIntegratesWithPipeline —— 端到端经管线。
func TestConsistencyCheckIntegratesWithPipeline(t *testing.T) {
	var marked []string
	p := NewPipeline(PipelineOptions{})
	p.Register(NewConsistencyCheckHook(func() []FileObservation {
		return []FileObservation{{ID: "obs1", Evidence: []EvidenceRef{{Path: "a.ts"}}}}
	}))

	hctx := &RuntimeHookContext{
		Snapshot: &RuntimeHookSnapshot{},
		Effects:  RuntimeHookEffects{MarkClaimStale: func(id string) { marked = append(marked, id) }},
	}
	p.RunPostTool(t.Context(), hctx, &RuntimeToolEvent{Name: "write_file", Target: "a.ts", Success: true})

	if len(marked) != 1 {
		t.Fatalf("经管线应标记 1 个，得到 %v", marked)
	}
	stats := p.Stats()
	if len(stats) != 1 || stats[0].ID != "consistency-check" {
		t.Errorf("管线统计不符：%+v", stats)
	}
}
