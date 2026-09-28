package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// appendix_planmode_e2e_test.go —— 第一百一十一刀 W3：plan mode 块的**端到端可达性**。
//
// # 为什么必须端到端（这是本刀最关键的一条防线）
//
// W1/W2 的用例直接调 `BuildDynamicAppendix`——它们只能证明「函数行为对」，
// **证明不了「状态真的流到了函数」**。本刀修的正是「没人来取」这类缺口：
// 函数一直是对的，错的是**调用链**。
//
// 故本文件走 **`Loop.Run` 的完整路径**：真 plan mode 状态 → run → 捕获
// 发往端点的请求体 → 断言附录真的出现。
//
// 本项目已有同类防线（`appendix_wiring_test.go` 的 permission-note 版），
// 本文件是 plan mode 版的对应物。

// TestPlanModeBlockReachesRequestBody —— ★ 主干：进 plan mode 后请求体含 plan 块。
//
// 走 `Loop.Run`：先 `enterPlanMode()`（模拟 plan 工具的 enter action），
// 再 Run，断言 user message 尾部含 `<context-update>` 与 `<plan-mode>`。
func TestPlanModeBlockReachesRequestBody(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("完成")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})

	// 模拟 plan 工具的 enter action（真实入口，非直接赋字段）
	l.enterPlanMode()

	if err := l.Run(context.TODO(), "做点计划"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	bodies := sc.handlerBodies()
	if len(bodies) == 0 {
		t.Fatal("mock 未收到请求")
	}
	body := bodies[0]

	if !strings.Contains(body, "<context-update>") {
		t.Error("★ 请求体缺 <context-update> 信封（W1 的信封未到生产路径？）")
	}
	if !strings.Contains(body, "<plan-mode>") {
		t.Error("★ 请求体缺 <plan-mode> 块——这正是本刀要修的缺口：" +
			"tmpGo 侧 plan mode 曾是「哑的」（门在拦写，模型不知情）")
	}
	if !strings.Contains(body, "你处于规划模式") {
		t.Error("<plan-mode> 块内容不符")
	}
}

// TestPlanModeBlockAbsentWithoutPlanMode —— ★ 配对反例：不进 plan mode 则无该块。
//
// **为什么必须配对**：只测「进了有块」会被「恒出块」的实现蒙混，
// 那会让每个普通轮次都背上 3200 字符的 plan 指令（前缀缓存灾难）。
func TestPlanModeBlockAbsentWithoutPlanMode(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("完成")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	// **不** enterPlanMode

	if err := l.Run(context.TODO(), "普通提问"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	body := sc.handlerBodies()[0]
	if strings.Contains(body, "<plan-mode>") {
		t.Error("★ 非 plan mode 的轮次不该含 <plan-mode> 块")
	}
}

// TestPlanExitReminderSentExactlyOnce —— ★ V7：退出提示恰好**注入**一次。
//
// 走真实 `exitPlanMode()`（Loop 方法，第七十九刀接线的那条路径），
// 再连跑两轮。
//
// # ★ 判据的设计（首版写错，探针实测后已订正——记录纠正过程）
//
// 首版断言「第二轮请求体不含 `<plan-mode-exit>`」，实测**失败**。
// 探针给出决定性事实：
//
//	exit 后 pending=true state=off
//	一轮后 pending=false
//	二轮后 pending=false          ← 状态流转**正确**
//	body[0] 含 exit 提示=true
//	body[1] 含 exit 提示=true     ← 但第二轮请求体仍含
//
// **根因**：`body[1]` 里那一处来自**对话历史**——`Run` 把第一轮的 user
// 消息（含 exit 提示）留在 history 中，第二轮请求体自然带着它。
// 那是**正确行为**（历史必须保留），错的是我的判据：
// 我要验的是「**新注入**的 appendix 只发一次」，不是「请求体全文只出现一次」。
//
// **订正后的判据**：比对两轮的出现**次数**——第二轮不得比第一轮**多**。
// 若 one-shot 失效（pending 未清），第二轮会再注入一次 → 计数 +1 → 红。
func TestPlanExitReminderSentExactlyOnce(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("一"), textTurn("二")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})

	// 先进入再退出（模拟真实的 enter → approve/exit 流程）
	l.enterPlanMode()
	l.exitPlanMode()

	if err := l.Run(context.TODO(), "第一轮"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	if err := l.Run(context.TODO(), "第二轮"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}

	bodies := sc.handlerBodies()
	if len(bodies) < 2 {
		t.Fatalf("应有 2 个请求，实得 %d", len(bodies))
	}

	marker := "<plan-mode-exit>"
	first := strings.Count(bodies[0], marker)
	second := strings.Count(bodies[1], marker)

	if first == 0 {
		t.Fatal("★ 退出后第一轮应注入 <plan-mode-exit> 提示（本刀缺口未修？）")
	}
	if second != first {
		t.Errorf("★ 退出提示只该**注入**一次（one-shot）——第二轮不得比第一轮多。"+
			"first=%d second=%d（第二轮多出的那次即未清除 pending 导致的重复注入）",
			first, second)
	}
	if !strings.Contains(bodies[0], "Plan Mode 已退出") {
		t.Error("exit 提示内容不符")
	}
}

// TestPlanBlockStillAbsentAfterExit —— 退出后 plan 块消失（状态真的回落）。
func TestPlanBlockStillAbsentAfterExit(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("一")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
	})
	l.enterPlanMode()
	l.exitPlanMode()

	if err := l.Run(context.TODO(), "x"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	body := sc.handlerBodies()[0]
	if strings.Contains(body, "你处于规划模式") {
		t.Error("退出后不该再含 plan-mode 指令（否则模型以为还在规划）")
	}
}

// TestAppendixStaysOutOfSystemPrompt —— 架构约束：appendix 只进 user message。
//
// 对账 `prompt/full.go` 的架构说明：appendix 输入（planModeState 等）会
// **会话中途翻转**，若进 system prompt（frozen 前缀），用户切模式就会
// 让整个前缀缓存失效。
func TestAppendixStaysOutOfSystemPrompt(t *testing.T) {
	root := t.TempDir()
	sc := &scriptedServer{responses: []string{textTurn("一")}}
	srv := httptest.NewServer(sc.handler())
	defer srv.Close()

	l := newTestLoop(t, srv, Config{
		Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: "auto-safe",
		SystemPrompt: "SENTINEL-SYSTEM-PROMPT",
	})
	l.enterPlanMode()

	if err := l.Run(context.TODO(), "x"); err != nil {
		t.Fatalf("Run 失败：%v", err)
	}
	body := sc.handlerBodies()[0]

	sysStart := strings.Index(body, "SENTINEL-SYSTEM-PROMPT")
	if sysStart < 0 {
		t.Fatal("system prompt 未进入请求体")
	}
	sysSeg := body[sysStart:]
	if next := strings.Index(sysSeg, `"role"`); next >= 0 {
		sysSeg = sysSeg[:next]
	}
	if strings.Contains(sysSeg, "<plan-mode>") {
		t.Error("★ <plan-mode> 出现在 system 消息里——那会打断前缀缓存" +
			"（appendix 必须进 user 消息尾部）")
	}

	// 正向：plan 块在 user 消息之后
	userStart := strings.Index(body, `"role":"user"`)
	planPos := strings.Index(body, "<plan-mode>")
	if userStart < 0 || planPos < userStart {
		t.Errorf("★ <plan-mode>（位置 %d）应在 user 消息（位置 %d）之后", planPos, userStart)
	}
}
