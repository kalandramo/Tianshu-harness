package agent

import (
	"strings"
	"testing"
)

// appendix_envelope_test.go —— 第一百一十一刀 W1：`<context-update>` 信封。
//
// # 缺口
//
// Go 的 `BuildDynamicAppendix` 返回**裸拼接**（`strings.Join(parts, "\n\n")`），
// 而 TS 侧两种路径**都包信封**（`src/prompt/engine.ts`）：
//
//	:1383  无 delta      `<context-update>\n${parts.join('\n\n')}\n</context-update>`
//	:1395  delta baseline `<context-update seq="${n}">\n…\n</context-update>`
//	:1398  delta 无变化   `<context-update seq="${n}"/>`（自闭合）
//	:1401  delta 有变化   `<context-update seq="${n}" mode="delta">\n…\n</context-update>`
//
// 本刀对齐**无 seq 的 baseline 形态**（`:1383`）——seq/delta 机制是独立子系统
// （需跨轮持久化三个字段 + 与压缩后重发 baseline 耦合），不在本刀范围。
//
// # 为什么信封重要（不只是「好看」）
//
// `<context-update>` 是模型识别「这段是系统注入的上下文」的**唯一标记**，
// 且 TS 侧 `appendix-anatomy` / `payload-diagnostic` 等下游按块名解析该结构。
// 丢了它，模型读到一段无标记的裸文本。

// TestBuildAppendixWrapsInContextUpdate —— ★ V1：有块时必须包信封。
func TestBuildAppendixWrapsInContextUpdate(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{ApprovalMode: "dangerously-skip-permissions"})

	if !strings.HasPrefix(got, "<context-update>\n") {
		t.Errorf("应以 `<context-update>\\n` 开头（对齐 engine.ts:1383），实得：%q",
			truncateRunes(got, 80))
	}
	if !strings.HasSuffix(got, "\n</context-update>") {
		t.Errorf("应以 `\\n</context-update>` 结尾，实得：%q", truncateRunes(got, 80))
	}
	// 内容仍在信封内
	if !strings.Contains(got, "<permission-note>") {
		t.Error("信封内应含子块内容")
	}
}

// TestBuildAppendixEmptyStaysEmpty —— ★ V2：零块时返回**空串**，不是空信封。
//
// **这条是缓存稳定性的守卫**：TS 明确 `if (parts.length === 0) return ”`
// （`engine.ts:1382`/`:1394`）。若改成返回 `<context-update/>`，
// **所有无 appendix 的轮次字节都会变**——那会破坏既有前缀缓存。
func TestBuildAppendixEmptyStaysEmpty(t *testing.T) {
	for _, mode := range []string{"auto-safe", "manual", ""} {
		got := BuildDynamicAppendix(AppendixContext{ApprovalMode: mode})
		if got != "" {
			t.Errorf("★ mode=%q 零块时应返回空串（不是空信封），实得：%q",
				mode, truncateRunes(got, 60))
		}
	}
}

// TestBuildAppendixJoinUsesDoubleNewline —— V3：块间分隔符是 `\n\n`。
//
// 对账 `engine.ts:1383` 的 `join('\n\n')`。
//
// **注意**：`src/prompt/volatile.ts:868` 的 `buildDynamicAppendix` wrapper
// 用的是 `join('\n')`——但**主路径不用它**（`engine.ts:9` 只 import
// `buildDynamicAppendixParts`）。故 `\n\n` 是对的，勿按 wrapper 改。
//
// 本用例用 permission-note + terseness 两块（Go 侧当前**仅有的**两个生产块）
// 来验证分隔符——`PlanModeState` 块在 W2 接入，届时另有用例覆盖三块。
func TestBuildAppendixJoinUsesDoubleNewline(t *testing.T) {
	got := BuildDynamicAppendix(AppendixContext{
		ApprovalMode: "dangerously-skip-permissions",
		TerseEnv:     "1",
	})

	noteEnd := strings.Index(got, "</permission-note>")
	styleStart := strings.Index(got, "<output-style>")
	if noteEnd < 0 || styleStart < 0 {
		t.Fatalf("前置失败：两块应都产出。got=%q", truncateRunes(got, 120))
	}

	between := got[noteEnd+len("</permission-note>") : styleStart]
	if between != "\n\n" {
		t.Errorf("★ 块间分隔符应为 `\\n\\n`（对齐 engine.ts:1383 的 join），实得 %q", between)
	}
}
