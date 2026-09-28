package prompt

import (
	"strings"
	"testing"
)

// self_recognition_e2e_test.go —— 第一百一十二刀 W2/W3：`<locus>` 块**端到端可达性**。
//
// # 为什么必须端到端（本刀的核心防线）
//
// W1 的用例直调 `DetectCwdRelation`——只证明「判定函数对」，
// **证明不了「判定结果真的流到了渲染层」**。而本刀修的正是这类缺口：
// 消费分支（`volatile.go` 的两个 locus 分支）与常量块**一直都在**，
// 缺的是**没人算过 `CwdRelation`**（字段恒空 → 两分支不可达）。
//
// 故本文件走 `BuildFullSystemPrompt` 的完整装配路径：
// 真目录 → 判定 → 填入 VolatileContext → 渲染 → 断言输出含 locus 块。

// fullPromptWithMarker 造一个带（或不带）`.rivet/SELF` 的目录，
// 在该目录下走完整装配，返回 system prompt。
//
// **为什么 sink 用 `Context{}` + 显式 HostEnv**：本用例只关心 locus 块，
// 其他 frozen 块（runtime-env / verify-commands 等）用最小输入即可——
// 它们的存在与否不影响 locus 断言（且它们的注入已有各自用例覆盖）。
func fullPromptWithMarker(t *testing.T, withMarker bool) string {
	t.Helper()
	dir := t.TempDir()
	if withMarker {
		writeSelfMarker(t, dir)
	}
	return BuildFullSystemPrompt(Context{}, dir, HostEnv{
		Platform: "darwin", OSType: "Darwin", OSRelease: "25.6.0", ShellKind: "sh",
	})
}

// tailForMsg 截取字符串尾部若干**字节**用于失败信息（避免把整个 system prompt
// 打进日志）。刻意用尾部——locus 块位于 frozen 块的中后段。
//
// **不用 `truncateForMsg`**（salience_test.go 的那个）：它只留 40 字符，
// 看不到 locus 文本；且跨测试文件依赖脆弱。
func tailForMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// TestLocusSelfRendersInFullPrompt —— ★ V5：真身场景渲染 self 块。
func TestLocusSelfRendersInFullPrompt(t *testing.T) {
	got := fullPromptWithMarker(t, true)

	if !strings.Contains(got, `<locus relation="self">`) {
		t.Errorf("★ cwd 含 .rivet/SELF 时应渲染 <locus relation=\"self\">——"+
			"这正是本刀所修缺口（CwdRelation 恒空导致两分支不可达）。实得输出尾部：%q",
			tailForMsg(got, 300))
	}
	if strings.Contains(got, `<locus relation="world">`) {
		t.Error("self 场景不该同时出现 world 块（两分支应互斥）")
	}
	if !strings.Contains(got, "这是你的源码") {
		t.Error("self 块内容不符（应为 locusSelfBlock 的文本）")
	}
}

// TestLocusWorldRendersInFullPrompt —— ★ V6：外部项目场景渲染 world 块。
//
// **配对反例**：只测「有标记 → self」会被一个「恒出 self」的实现蒙混，
// 那会让模型在别人的项目里以为可以改天枢自己的源码。
func TestLocusWorldRendersInFullPrompt(t *testing.T) {
	got := fullPromptWithMarker(t, false)

	if !strings.Contains(got, `<locus relation="world">`) {
		t.Errorf("★ cwd 无 .rivet/SELF 时应渲染 <locus relation=\"world\">。实得输出尾部：%q",
			tailForMsg(got, 300))
	}
	if strings.Contains(got, `<locus relation="self">`) {
		t.Error("★ world 场景不得出现 self 块（会让模型误以为可改天枢源码）")
	}
	if !strings.Contains(got, "你在一个外部项目中工作") {
		t.Error("world 块内容不符（应为 locusWorldBlock 的文本）")
	}
}

// TestLocusTextByteEqual —— ★ V7：渲染文本与常量**逐字节一致**。
//
// **为什么要这条**：若将来有人改了 `volatile.go` 的常量文本但测试仍只做
// `Contains` 断言，两处会静默漂移。逐字节比对把它钉住。
func TestLocusTextByteEqual(t *testing.T) {
	selfPrompt := fullPromptWithMarker(t, true)
	if !strings.Contains(selfPrompt, locusSelfBlock) {
		t.Errorf("渲染出的 self 块与 locusSelfBlock 常量**不逐字节一致**。\n"+
			"期望含：%q\n实得尾部：%q", locusSelfBlock, tailForMsg(selfPrompt, 400))
	}

	worldPrompt := fullPromptWithMarker(t, false)
	if !strings.Contains(worldPrompt, locusWorldBlock) {
		t.Errorf("渲染出的 world 块与 locusWorldBlock 常量不逐字节一致。\n"+
			"期望含：%q\n实得尾部：%q", locusWorldBlock, tailForMsg(worldPrompt, 400))
	}
}

// TestLocusAppearsExactlyOnce —— ★ V8：`<locus` 恰好出现一次。
//
// 防「两分支都命中」（如误写成两个独立 if 且 CwdRelation 取了一个
// 同时满足两者的值）或重复注入。
func TestLocusAppearsExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withMarker bool
	}{
		{"self 场景", true},
		{"world 场景", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fullPromptWithMarker(t, tc.withMarker)
			if n := strings.Count(got, "<locus "); n != 1 {
				t.Errorf("★ `<locus ` 应恰好出现 1 次，实得 %d 次", n)
			}
		})
	}
}
