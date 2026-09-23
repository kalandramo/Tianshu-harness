package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestEditAdvisoryWiredIntoLoop —— **接线反证**。
//
// 单元测试证明 hook 逻辑正确，但不证明 loop 会调用它。本测试用真实 loop
// 跑「同轮两次 hash_edit 同一文件」，断言 advisory 进了请求体。
func TestEditAdvisoryWiredIntoLoop(t *testing.T) {
	root := t.TempDir()
	target := root + "/h.ts"
	// 写一个足够长的文件，供 hash_edit 锚定
	if err := writeFileHelper(target, "line1\nline2\nline3\nline4\nline5\n"); err != nil {
		t.Fatal(err)
	}

	// 两轮：第 1 轮 hash_edit 两次（同一文件），第 2 轮终答。
	twoEdits := sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{
			map[string]any{"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": "hash_edit",
					"arguments": `{"file_path":"h.ts","anchors":["L1:aaaaaaaa"],"new_string":"line1x"}`}},
			map[string]any{"index": 1, "id": "c2", "type": "function",
				"function": map[string]any{"name": "hash_edit",
					"arguments": `{"file_path":"h.ts","anchors":["L2:bbbbbbbb"],"new_string":"line2x"}`}},
		}},
	}}}) + sseData(map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "tool_calls",
	}}}) + "data: [DONE]\n\n"

	sc := &scriptedServer{responses: []string{twoEdits, textTurn("完成")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewEditToolAdvisoryHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(context.TODO(), "改两次"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	found := false
	for _, body := range sc.handlerBodies() {
		if containsStr(body, "hash_edit") && containsStr(body, "stale") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("**edit-tool advisory 未进入请求体**——hook 未被 loop 调用或未接线。"+
			"共 %d 个请求。", len(sc.handlerBodies()))
	}
}

// **反证**：同轮只编辑**一次** → 不触发。
func TestEditAdvisorySilentOnSingleEdit(t *testing.T) {
	root := t.TempDir()
	target := root + "/one.ts"
	if err := writeFileHelper(target, "line1\nline2\nline3\n"); err != nil {
		t.Fatal(err)
	}

	oneEdit := toolTurnArgs("c1", "hash_edit", map[string]any{
		"file_path": "one.ts", "anchors": []any{"L1:aaaaaaaa"}, "new_string": "line1x",
	})
	sc := &scriptedServer{responses: []string{oneEdit, textTurn("完成")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewEditToolAdvisoryHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(context.TODO(), "改一次"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	for _, body := range sc.handlerBodies() {
		if containsStr(body, "stale") {
			t.Error("单次编辑不应触发 edit-tool advisory")
			break
		}
	}
}
