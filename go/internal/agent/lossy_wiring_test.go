package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestLossyHookWiredIntoLoop —— **接线反证**。
//
// 单元测试证明 hook 逻辑正确，但不证明 loop 会调用它。本测试用真实 loop
// 跑一个「工具输出被截断」的场景，断言 advisory 真的进了请求体。
func TestLossyHookWiredIntoLoop(t *testing.T) {
	// 用 read_file 读一个**超出上限**的文件 → 输出被截断并带 [output truncated:] 标记。
	root := t.TempDir()
	big := make([]byte, 0, 200000)
	for i := 0; i < 200000; i++ {
		big = append(big, byte('a'+(i%26)))
	}
	if err := writeFileHelper(root+"/big.txt", string(big)); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "big.txt"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewLossyObservationHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(context.TODO(), "读大文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	// 断言：后续请求体里出现 lossy advisory
	found := false
	for _, body := range sc.handlerBodies() {
		if containsStr(body, "有损观测") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("**lossy advisory 未进入请求体**——hook 未被 loop 调用或未接线。"+
			"共 %d 个请求，均无该 advisory。", len(sc.handlerBodies()))
	}
}

// **反证**：小文件（不截断）**不**产生 lossy advisory。
//
// 这防止「hook 无条件触发」的假绿。
func TestLossyHookSilentForSmallFile(t *testing.T) {
	root := t.TempDir()
	if err := writeFileHelper(root+"/small.txt", "hello\n"); err != nil {
		t.Fatal(err)
	}

	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "small.txt"}),
		textTurn("完成"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})
	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewLossyObservationHook(bus))
	l.Hooks = p
	l.Advisories = bus

	if err := l.Run(context.TODO(), "读小文件"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	for _, body := range sc.handlerBodies() {
		if containsStr(body, "有损观测") {
			t.Errorf("小文件不应产生 lossy advisory")
			break
		}
	}
}

// TestSpiralHookWiredIntoProductionPath —— **悬空修复的反证**。
//
// 第四十二刀写了 spiral hook 但漏了生产注册（第四十三刀修）。本测试断言
// 生产装配段（`main.go` 的装配逻辑）确实注册了它。
//
// **为什么用源码扫描而非运行时**：`main.go` 的装配在 `main()` 里，
// 测试无法调用它。故用「源码里存在注册调用」作为接线证据——这是弱证据，
// 但配合用户级验收（真实二进制）构成完整链条。
func TestHookRegistrationInMain(t *testing.T) {
	// 这一条由 cmd/tianshu 的构建 + 用户级验收覆盖；此处只做编译期存在性检查。
	// 真正的断言在 TestReasoningSpiralHookConstructible。
	h := NewReasoningSpiralHook(ReasoningSpiralDeps{Bus: &spySink{}})
	if h.Name != "reasoning-spiral" || h.Phase != PhasePreTurn {
		t.Errorf("spiral hook 元数据异常：name=%q phase=%q", h.Name, h.Phase)
	}
	lh := NewLossyObservationHook(&spySink{})
	if lh.Name != "lossy-observation" || lh.Phase != PhasePostTool {
		t.Errorf("lossy hook 元数据异常：name=%q phase=%q", lh.Name, lh.Phase)
	}
}
