package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTersenessAppendixWiring —— **terse nudge 的端到端接线测试**。
//
// 判据（对账 TS `volatile.ts:838-843`）：
//
//		const { enabled, escalate } = resolveTersenessFlags(ctx)
//		if (enabled) { push(renderTersenessNudge(escalate)) }
//
//	  - `RIVET_TERSE=1`（或 `true`/`on`/`yes`）→ 请求体含 `<output-style>`
//	  - 未设 / `RIVET_TERSE=0` → **不含**（保持字节稳定）
//	  - `escalate`（doom-loop 轮次）→ **降级为 false**（Go 侧无 doom-loop
//	    会话状态载体，见下方「已知降级」）
//
// **为什么必须端到端**：`ResolveTersenessFlags` / `RenderTersenessNudge` 的
// 单元测试早已全绿（第五十七刀 110 子用例），但它们**零生产消费者**——
// 用户设 `RIVET_TERSE=1` 看不到任何变化（第五十七刀验收面 blocked）。
// 本刀是它们的接线。
func TestTersenessAppendixWiring(t *testing.T) {
	// 判据：Run 之后，模型收到的请求体里是否含 <output-style>。
	run := func(t *testing.T, terseEnv string) string {
		t.Helper()
		root := t.TempDir()
		sc := &scriptedServer{responses: []string{textTurn("完成")}}
		srv := httptest.NewServer(sc.handler())
		defer srv.Close()

		l := newTestLoop(t, srv, Config{
			Model: "m", MaxTokens: 100, Cwd: root,
			// TerseEnv 是注入面（nil = 无 env）
			TerseEnv: terseEnv,
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

	t.Run("★RIVET_TERSE=1注入output-style", func(t *testing.T) {
		body := run(t, "1")
		if !strings.Contains(body, "<output-style>") {
			t.Error("★ RIVET_TERSE=1 时请求体应含 <output-style>（terse 未接线？）")
		}
		if !strings.Contains(body, "文字要精炼") {
			t.Error("output-style 内容不符")
		}
	})

	t.Run("★未设env不注入（字节稳定）", func(t *testing.T) {
		body := run(t, "")
		if strings.Contains(body, "<output-style>") {
			t.Error("★ 未设 RIVET_TERSE 时不该含 <output-style>——保持字节稳定")
		}
	})

	t.Run("★RIVET_TERSE=0压制（optOut）", func(t *testing.T) {
		body := run(t, "0")
		if strings.Contains(body, "<output-style>") {
			t.Error("★ RIVET_TERSE=0 应压制 terse 输出")
		}
	})

	t.Run("其他optIn取值", func(t *testing.T) {
		for _, v := range []string{"true", "on", "yes"} {
			body := run(t, v)
			if !strings.Contains(body, "<output-style>") {
				t.Errorf("RIVET_TERSE=%q 应 optIn", v)
			}
		}
	})

	t.Run("★未知值不注入", func(t *testing.T) {
		body := run(t, "maybe")
		if strings.Contains(body, "<output-style>") {
			t.Error("★ 未知取值既非 optIn 也非 optOut，且无 ctx 信号 → 不该注入")
		}
	})
}

// TestBuildDynamicAppendixTerseness —— 装配层的单元测试。
func TestBuildDynamicAppendixTerseness(t *testing.T) {
	t.Run("terse开启产出output-style", func(t *testing.T) {
		got := BuildDynamicAppendix(AppendixContext{TerseEnv: "1"})
		if !strings.Contains(got, "<output-style>") {
			t.Errorf("terse=1 应产出 output-style：got=%q", truncateRunes(got, 60))
		}
	})

	t.Run("★terse关闭产出空串", func(t *testing.T) {
		for _, env := range []string{"", "0", "false", "off", "no", "maybe"} {
			got := BuildDynamicAppendix(AppendixContext{TerseEnv: env})
			if got != "" {
				t.Errorf("★ TerseEnv=%q 应产出空串：got=%q", env, truncateRunes(got, 60))
			}
		}
	})

	t.Run("★escalate降级为false", func(t *testing.T) {
		// Go 侧无 doom-loop 会话状态载体 → escalate 恒 false。
		// 判据：输出的 output-style 块**不含** escalate 专属文案。
		got := BuildDynamicAppendix(AppendixContext{TerseEnv: "1"})
		if strings.Contains(got, "你似乎在重复工作或打转") {
			t.Error("★ escalate 应降级为 false（Go 侧无 doom-loop 载体）——" +
				"不该出现 escalate 专属文案")
		}
	})

	t.Run("permission-note与terse共存", func(t *testing.T) {
		got := BuildDynamicAppendix(AppendixContext{
			ApprovalMode: "dangerously-skip-permissions",
			TerseEnv:     "1",
		})
		if !strings.Contains(got, "<permission-note>") {
			t.Error("应含 permission-note")
		}
		if !strings.Contains(got, "<output-style>") {
			t.Error("应含 output-style")
		}
		// 顺序：permission-note 在前（对账 TS 的 push 顺序）
		if strings.Index(got, "<permission-note>") > strings.Index(got, "<output-style>") {
			t.Error("块顺序应为 permission-note 在前")
		}
	})
}
