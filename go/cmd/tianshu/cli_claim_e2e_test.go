package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/agent"
	"github.com/kalandramo/tianshu/go/internal/context"
)

// TestCLIClaimStoreWiring —— **装配级验收**：
//
// 用户动作：CLI 装配的 hook 管线收到 write_file 的 postTool 事件，且该文件
// 被一条 claim 引用。
// 观察到：**claim 在 store 里被标记为 stale**（落盘事件 + 投影状态变化）。
//
// **这消除第九刀留下的 blocked 验收项**——此前 `getFileObservations` 注入空集，
// hook 无副作用。
//
// **为什么不是真实二进制**：CLI 没有「预置一条 claim」的入口（claim 由
// session 运行期产生，当前 Go 侧还没有 claim 提取器）。故这里直接构造 CLI
// 所用的**同一装配**（buildLoop 的 hook 装配 + 真实 claim store），喂 postTool
// 事件。**限制如实说明**：这验证的是装配正确性，不是「端到端从会话产生 claim」。
func TestCLIClaimStoreWiring(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "foo.ts")
	if err := os.WriteFile(target, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ── 建 claim store 并预置一条引用 foo.ts 的 claim ──
	claimsDir := filepath.Join(root, ".rivet", "claims")
	if err := os.MkdirAll(claimsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := context.NewClaimStore(claimsDir, "test-sess")
	if err != nil {
		t.Fatalf("构造 claim store 失败：%v", err)
	}

	claim, err := store.Propose(context.ClaimProposal{
		Kind:       context.ClaimFileObservation,
		Scope:      context.ScopeSession,
		Text:       "foo.ts 里有函数 X",
		Confidence: 0.7,
		Fitness:    0.5,
		Source:     context.ClaimSource{Actor: "tool", SessionID: "test-sess", Turn: 1, EventID: "e1"},
		Evidence: []context.EvidenceRef{
			{ID: "ev1", Kind: "tool_result", Summary: "读到 foo.ts", Path: target, CreatedAt: 1700000000000},
		},
		CreatedAt: 1700000000000,
		Tags:      []string{"obs"},
	})
	if err != nil {
		t.Fatalf("propose 失败：%v", err)
	}

	// ── 用 CLI 的**同一装配**建 hook 管线 ──
	bus := agent.NewAdvisoryBus()
	pipeline := agent.NewPipeline(agent.PipelineOptions{})
	pipeline.Register(agent.NewTypecheckReminderHook(bus))
	// **真实 claim 来源**（此前这里是空集）
	pipeline.Register(agent.NewConsistencyCheckHook(func() []agent.FileObservation {
		var out []agent.FileObservation
		for _, c := range store.ListClaims([]context.ContextClaimStatus{context.StatusActive}, nil, nil) {
			fo := agent.FileObservation{ID: c.ID, Text: c.Text}
			for _, e := range c.Evidence {
				fo.Evidence = append(fo.Evidence, agent.EvidenceRef{Path: e.Path})
			}
			out = append(out, fo)
		}
		return out
	}))

	// ── 装配 effects（对账 TS 的 markClaimStale 实现）──
	// TS：`updateClaimStatus(claimId, 'stale', 'invalidated by <tool> on <target>')`
	effects := agent.RuntimeHookEffects{
		MarkClaimStale: func(claimID string) {
			_, err := store.UpdateClaimStatus(
				claimID, context.StatusStale,
				"invalidated by write_file on "+target, 1700000001000)
			if err != nil {
				t.Errorf("标记 stale 失败：%v", err)
			}
		},
	}

	// ── 喂 postTool 事件（模拟 write_file 写 foo.ts）──
	ctx := &agent.RuntimeHookContext{
		Snapshot: &agent.RuntimeHookSnapshot{},
		Effects:  effects,
	}
	pipeline.RunPostTool(t.Context(), ctx, &agent.RuntimeToolEvent{
		Name: "write_file", Target: target, Success: true,
		Input: map[string]any{"file_path": target},
	})

	// ── 观察：claim 被标记 stale ──
	all := store.ListClaims(nil, nil, nil)
	if len(all) != 1 {
		t.Fatalf("应有 1 条 claim，得到 %d", len(all))
	}
	if all[0].ID != claim.ID {
		t.Fatalf("claim ID 不符：%q vs %q", all[0].ID, claim.ID)
	}
	if all[0].Status != context.StatusStale {
		t.Errorf("**claim 应被标记 stale**，实际 %q\n"+
			"（hook 未产生副作用 → getFileObservations 或 effects 接线有问题）", all[0].Status)
	}
	// 反证应被追加（status_changed 非 active 时）
	if len(all[0].Counterevidence) != 1 {
		t.Errorf("标记 stale 应追加 1 条反证，得到 %d", len(all[0].Counterevidence))
	}
	if len(all[0].Counterevidence) > 0 {
		want := "invalidated by write_file on " + target
		if all[0].Counterevidence[0].Summary != want {
			t.Errorf("反证 reason 格式不符\nwant: %q\ngot:  %q", want, all[0].Counterevidence[0].Summary)
		}
	}

	// ── 观察：落盘（事件已写入 JSONL）──
	entries, err := os.ReadDir(claimsDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			found = true
		}
	}
	if !found {
		t.Error("claim 变更应落盘到 JSONL")
	}
}

// TestCLIClaimStoreWiringNonWriteToolIgnored —— 反向：非写工具不触发。
func TestCLIClaimStoreWiringNonWriteToolIgnored(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bar.ts")
	os.WriteFile(target, []byte("x"), 0o644)

	claimsDir := filepath.Join(root, ".rivet", "claims")
	os.MkdirAll(claimsDir, 0o755)
	store, _ := context.NewClaimStore(claimsDir, "s2")
	store.Propose(context.ClaimProposal{
		Kind: context.ClaimFileObservation, Scope: context.ScopeSession, Text: "bar.ts 有 Y",
		Confidence: 0.7, Fitness: 0.5,
		Source:    context.ClaimSource{Actor: "tool", SessionID: "s2", Turn: 1, EventID: "e1"},
		Evidence:  []context.EvidenceRef{{ID: "ev", Kind: "tool_result", Summary: "s", Path: target, CreatedAt: 1}},
		CreatedAt: 1,
	})

	bus := agent.NewAdvisoryBus()
	pipeline := agent.NewPipeline(agent.PipelineOptions{})
	pipeline.Register(agent.NewConsistencyCheckHook(func() []agent.FileObservation {
		var out []agent.FileObservation
		for _, c := range store.ListClaims(nil, nil, nil) {
			fo := agent.FileObservation{ID: c.ID, Text: c.Text}
			for _, e := range c.Evidence {
				fo.Evidence = append(fo.Evidence, agent.EvidenceRef{Path: e.Path})
			}
			out = append(out, fo)
		}
		return out
	}))
	_ = bus

	marked := false
	ctx := &agent.RuntimeHookContext{
		Snapshot: &agent.RuntimeHookSnapshot{},
		Effects:  agent.RuntimeHookEffects{MarkClaimStale: func(string) { marked = true }},
	}
	// read_file 不是写工具
	pipeline.RunPostTool(t.Context(), ctx, &agent.RuntimeToolEvent{
		Name: "read_file", Target: target, Success: true,
	})

	if marked {
		t.Error("read_file 不应触发 claim 过期标记")
	}
}
