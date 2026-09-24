package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDynamicAppendixWiring —— **动态 appendix 的端到端接线测试**。
//
// 判据（对账 TS `volatile.ts:825/834/842` 的 appendix 家族）：
//
//   - **skip 档**（`dangerously-skip-permissions`）→ 请求体含 `<permission-note>`
//   - **其他档**（auto-safe / manual）→ **不含**（TS 注释：Returns ” for every
//     mode that still asks, so those turns stay **byte-identical**）
//
// **为什么必须端到端**：单元测试只证明 `RenderPermissionNote` 判定正确；
// 若它没被消费（悬空实现），单测照样全绿而用户看不到任何变化。
// 本项目反复踩过「type-without-consumer」缺口（第五十刀 `NeedsApproval`、
// 第五十五/五十六/五十七刀的 appendix 家族）。
//
// **架构约束（关键）**：appendix 必须注入 **user message 尾部**，**不能**进
// system prompt——后者是 frozen 前缀，`approvalMode` 中途翻转会打断前缀缓存
// （`full.go:90-105` 的架构说明）。
func TestDynamicAppendixWiring(t *testing.T) {
	// 判据：Run 之后，模型收到的请求体里是否含 <permission-note>。
	run := func(t *testing.T, mode string) (body string) {
		t.Helper()
		root := t.TempDir()
		sc := &scriptedServer{responses: []string{textTurn("完成")}}
		srv := httptest.NewServer(sc.handler())
		defer srv.Close()

		l := newTestLoop(t, srv, Config{
			Model: "m", MaxTokens: 100, Cwd: root, ApprovalMode: mode,
		})
		if err := l.Run(context.TODO(), "测试"); err != nil {
			t.Fatalf("Run 失败：%v", err)
		}
		bodies := sc.handlerBodies()
		if len(bodies) == 0 {
			t.Fatal("mock 未收到请求")
		}
		return bodies[0]
	}

	t.Run("★skip档注入permission-note", func(t *testing.T) {
		body := run(t, "dangerously-skip-permissions")
		if !strings.Contains(body, "<permission-note>") {
			t.Error("★ skip 档下请求体应含 <permission-note>（appendix 未接线？）")
		}
		if !strings.Contains(body, "全自动（免审批）") {
			t.Error("permission-note 内容不符")
		}
	})

	t.Run("★auto-safe档不注入（字节稳定）", func(t *testing.T) {
		body := run(t, "auto-safe")
		if strings.Contains(body, "<permission-note>") {
			t.Error("★ auto-safe 档不该含 <permission-note>——" +
				"TS 注释要求这些 turn 保持 byte-identical")
		}
	})

	t.Run("manual档不注入", func(t *testing.T) {
		body := run(t, "manual")
		if strings.Contains(body, "<permission-note>") {
			t.Error("manual 档不该含 <permission-note>")
		}
	})

	t.Run("★appendix进user消息而非system", func(t *testing.T) {
		// 架构约束验证：<permission-note> 必须出现在 **user message** 里，
		// 不能出现在 **system message**（后者是 frozen 前缀）。
		//
		// **必须显式设 SystemPrompt**——否则请求体里没有 system 消息，
		// 这条断言会因「找不到 system」而空转（首次实现在此踩过）。
		root := t.TempDir()
		sc := &scriptedServer{responses: []string{textTurn("完成")}}
		srv := httptest.NewServer(sc.handler())
		defer srv.Close()

		l := newTestLoop(t, srv, Config{
			Model: "m", MaxTokens: 100, Cwd: root,
			ApprovalMode: "dangerously-skip-permissions",
			SystemPrompt: "SENTINEL-SYSTEM-PROMPT",
		})
		if err := l.Run(context.TODO(), "测试"); err != nil {
			t.Fatalf("Run 失败：%v", err)
		}
		body := sc.handlerBodies()[0]

		if !strings.Contains(body, "<permission-note>") {
			t.Fatal("前置失败：未注入 permission-note")
		}
		// system 消息的 content 段（从 SENTINEL 到下一个 role 之前）
		sysStart := strings.Index(body, "SENTINEL-SYSTEM-PROMPT")
		if sysStart < 0 {
			t.Fatal("system prompt 未进入请求体")
		}
		sysSeg := body[sysStart:]
		if next := strings.Index(sysSeg, `"role"`); next >= 0 {
			sysSeg = sysSeg[:next]
		}
		if strings.Contains(sysSeg, "<permission-note>") {
			t.Error("★ <permission-note> 出现在 system 消息里——" +
				"那会打断前缀缓存（appendix 必须进 user 消息尾部）")
		}
		// 正向断言：permission-note 在 user 消息内。
		//
		// **不要用「截到下一个 "role"」的方式切段**——`"role"` 出现在
		// content **之前**（`{"role":"user","content":"..."}`），那种切法
		// 会把段截成空（首次实现在此踩过）。
		//
		// 有效判据：permission-note 的位置在 user 消息起点之后。
		// （反向已被上一断言覆盖：system 段内不含它。故它只能在 user 段内。）
		userStart := strings.Index(body, `"role":"user"`)
		if userStart < 0 {
			t.Fatal(`请求体里找不到 "role":"user"`)
		}
		pnPos := strings.Index(body, "<permission-note>")
		if pnPos < userStart {
			t.Errorf("★ <permission-note>（位置 %d）应在 user 消息（位置 %d）之后",
				pnPos, userStart)
		}
	})
}

// TestBuildDynamicAppendixUnit —— 装配函数的单元测试（无 IO）。
func TestBuildDynamicAppendixUnit(t *testing.T) {
	t.Run("skip档产出permission-note", func(t *testing.T) {
		got := BuildDynamicAppendix(AppendixContext{ApprovalMode: "dangerously-skip-permissions"})
		if !strings.Contains(got, "<permission-note>") {
			t.Errorf("skip 档应产出 permission-note：got=%q", truncateRunes(got, 60))
		}
	})

	t.Run("★其他档产出空串", func(t *testing.T) {
		for _, mode := range []string{"auto-safe", "manual", "auto-accept", ""} {
			got := BuildDynamicAppendix(AppendixContext{ApprovalMode: mode})
			if got != "" {
				t.Errorf("★ mode=%q 应产出空串（字节稳定）：got=%q", mode, truncateRunes(got, 60))
			}
		}
	})
}
