package agent

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestLossyHookCooldownAcrossRuns —— **跨 Run 冷却回归**（对抗验证抓到的缺陷）。
//
// 缺陷：快照 `Turn` 曾填 **run 局部序号**（`buildRuntimeSnapshot(turn)` 传循环
// 变量）。run 局部序号**每个 Run 从 0 重启**，而 TS 的 `snapshot.turn` 是
// session turn（`session.getTurnCount()`，loop-factory.ts:516）——**单 Run 内
// 恒定、跨 Run 推进**。二者不等价：新 Run 的首轮会与上一 Run 的首轮撞键
// （都为 0），冷却判定 `Snapshot.Turn == lastFiredTurn` 成立 → 新 Run 的 lossy
// advisory 被**误抑制**，用户看不到本该出现的提醒。
//
// **复现手法（踩过坑，勿改回同文件）**：两个 Run 各读一个**不同的**大文件。
//
// 首版用「同一个大文件读两次」——**复现失效**。原因不在冷却键，而在 read-ref
// 会话去重：第二次读同一未变文件时，read_file 返回 `[read-ref] ... 本会话已读
// 且未变`（298 字符），**不是有损输出**，`IsLossyObservation` 为 false，hook
// 正确地不触发。探针实测确认（Turn=2 但 isLossy=false）。
//
// 判据（改用两个不同文件后）：
//   - 修复前（run 局部 Turn）：两个 Run 首轮 Turn 都为 0 → Run2 被抑制 → **红**
//   - 修复后（session Turn）：Run1 Turn=1 / Run2 Turn=2 → 都触发 → 绿
//
// **判别力已实测**：把 `Turn` 改回 `0`（run 局部）→ 本测试转红（Run2 advisory=false）。
func TestLossyHookCooldownAcrossRuns(t *testing.T) {
	root := t.TempDir()
	// 两个**内容不同**的大文件——各读一次，都会产出 [output truncated: 标记，
	// 且不会触发 read-ref 去重（内容不同即非「未变」）。
	big1 := make([]byte, 0, 200000)
	for i := 0; i < 200000; i++ {
		big1 = append(big1, byte('a'+(i%26)))
	}
	if err := writeFileHelper(root+"/big1.txt", string(big1)); err != nil {
		t.Fatal(err)
	}
	big2 := make([]byte, 0, 200000)
	for i := 0; i < 200000; i++ {
		big2 = append(big2, byte('A'+(i%26)))
	}
	if err := writeFileHelper(root+"/big2.txt", string(big2)); err != nil {
		t.Fatal(err)
	}

	// 脚本：Run1 = 读 big1 + 终答；Run2 = 读 big2 + 终答。
	sc := &scriptedServer{responses: []string{
		toolTurnArgs("c1", "read_file", map[string]any{"file_path": "big1.txt"}),
		textTurn("完成1"),
		toolTurnArgs("c2", "read_file", map[string]any{"file_path": "big2.txt"}),
		textTurn("完成2"),
	}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{Model: "m", MaxTokens: 100, Cwd: root})

	bus := NewAdvisoryBus()
	p := NewPipeline(PipelineOptions{})
	p.Register(NewLossyObservationHook(bus))
	l.Hooks = p
	l.Advisories = bus

	// ── Run 1 ──
	if err := l.Run(context.TODO(), "读大文件（第一轮）"); err != nil {
		t.Fatalf("Run1 失败：%v", err)
	}
	run1BodyCount := len(sc.handlerBodies())
	run1HasAdvisory := anyBodyContains(sc.handlerBodies()[:run1BodyCount], "有损观测")
	if !run1HasAdvisory {
		t.Fatalf("Run1 应产生 lossy advisory（前置失败）——共 %d 个请求体",
			run1BodyCount)
	}

	// ── Run 2（同一 loop，session-scoped hook 复用）──
	if err := l.Run(context.TODO(), "读大文件（第二轮）"); err != nil {
		t.Fatalf("Run2 失败：%v", err)
	}

	// 判据：Run2 的请求体里必须**再次**出现 lossy advisory。
	//
	// 若冷却键用 run 局部 Turn，Run2 首轮 Turn 会重置为 0、与 Run1 首轮撞键，
	// 这里恒为 false → 本断言红。
	run2Bodies := sc.handlerBodies()[run1BodyCount:]
	run2HasAdvisory := anyBodyContains(run2Bodies, "有损观测")
	if !run2HasAdvisory {
		t.Errorf("**跨 Run 冷却误抑制**：Run2 未产生 lossy advisory——"+
			"快照 Turn 不是 session turn（run 局部序号每 Run 从 0 重启会撞键）。"+
			"Run1 %d 个请求体 / Run2 %d 个请求体。",
			run1BodyCount, len(run2Bodies))
	}
}

// anyBodyContains 报告任一请求体是否含指定子串。
func anyBodyContains(bodies []string, needle string) bool {
	for _, b := range bodies {
		if containsStr(b, needle) {
			return true
		}
	}
	return false
}
