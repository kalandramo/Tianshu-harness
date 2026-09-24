package agent

import (
	"strings"
	"testing"
)

// TestDetectNegativeFactInLossyResult —— 对账 TS
// `src/agent/__tests__/negative-fact-detector.test.ts`。
//
// **用例集有意收窄（重要，勿照搬 TS）**：TS 的 10 个用例里有 6 个用
// `[storm-collapsed:` / `[tiered-summary:` / `[stdout truncated:` 等标记——
// 这些标记 **Go 侧刻意不移植**（见 `lossy_markers.go` 文件头：Go 侧不产生
// 它们，移植会让检测永不触发，成死模式）。
//
// 故本测试**只覆盖 Go 侧真实产生的 4 条标记**：
//
//	^\[collapsed  /  \[output truncated:  /  PARTIAL view of   /  <microcompacted
//
// 首版照搬 TS 用例 → 6 个红。**根因是用例假设错，不是实现错**——这正印证
// 了「移植前先核实源语言的真实产生点」的纪律。
//
// 判据分两类：
//   - 有损 + 负向断言 → 检出（非 nil）
//   - 无损（无论是否含负向词）→ 不检出（nil）
func TestDetectNegativeFactInLossyResult(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantNil bool
		wantSub string // wantNil=false 时，matched 应含此子串（小写比较）
	}{
		{
			name:    "无损内容（无折叠标记）",
			content: "[ls -la] exit=0 time=0.1s lines=42 — output complete\nfile1\nfile2",
			wantNil: true,
		},
		{
			name:    "有损但无负向断言",
			content: "[output truncated: head 100 + tail 80 of 500 lines shown — 320 lines omitted]\n  ls -la  exit=0  42 lines",
			wantNil: true,
		},
		{
			name:    "[collapsed] + empty",
			content: "[collapsed grep: 14 matches in 3 files]\n  ls -la .rivet/sessions/  exit=0  0 lines\nLast output:\n  (empty)",
			wantSub: "empty",
		},
		{
			name:    "[output truncated] + not found",
			content: "[ls -la] exit=0 time=0.1s lines=500\n...\n[output truncated: head 100 + tail 80 of 500 lines shown — 320 lines omitted]\nnot found: some/file.ts",
			wantSub: "not found",
		},
		{
			name:    "[output truncated] + 0 results",
			content: "[output truncated: 2000 of 50000 bytes shown]\n0 results across 10 files",
			wantSub: "0 results",
		},
		{
			name:    "[collapsed] + 0 files",
			content: "[collapsed bash: 4 calls in 1 file]\n  find . -name pattern  exit=0  0 lines\nLast output:\n  0 files found",
			wantSub: "0 files",
		},
		{
			name:    "无损 + no errors（正常输出，不该检出）",
			content: "[npm test] exit=0 time=1.5s lines=128 — output complete\nTests: 128 passed, 0 failed\nno errors",
			wantNil: true,
		},
		{
			name:    "[collapsed] + no errors",
			content: "[collapsed npm: 4 calls in 1 file]\n  npm test  exit=0  128 lines\nLast output:\n  Tests: 128 passed, 0 failed, no errors",
			wantSub: "no errors",
		},
		{
			name:    "[collapsed] + all passed",
			content: "[collapsed bash: 4 calls in 1 file]\n all passed",
			wantSub: "all passed",
		},
		{
			name:    "PARTIAL view + empty",
			content: "── PARTIAL view of big.txt (1 lines, 200000 chars) ──\nShowing lines 1-1 of 1.\n(empty)",
			wantSub: "empty",
		},
		{
			name:    "<microcompacted> + no changes",
			content: "<microcompacted 12 messages>\nLast output:\n  no changes",
			wantSub: "no changes",
		},
		{
			name:    "无损 + unchanged（不该检出——无损即无风险）",
			content: "[git status] exit=0 time=0.1s lines=3 — output complete\nunchanged: 5 files",
			wantNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectNegativeFactInLossyResult(tc.content)
			if tc.wantNil {
				if got != nil {
					t.Errorf("应不检出，得到 matched=%q reason=%q", got.Matched, got.Reason)
				}
				return
			}
			if got == nil {
				t.Fatalf("应检出但得到 nil（内容：%q）", tc.content)
			}
			if !strings.Contains(strings.ToLower(got.Matched), tc.wantSub) {
				t.Errorf("matched 应含 %q，得到 %q", tc.wantSub, got.Matched)
			}
			// reason 文案对账 TS：必须含 "lossy" 与 matched 原文。
			if !strings.Contains(got.Reason, "lossy") {
				t.Errorf("reason 应含 \"lossy\"，得到 %q", got.Reason)
			}
			if !strings.Contains(got.Reason, got.Matched) {
				t.Errorf("reason 应含 matched 原文，得到 %q", got.Reason)
			}
		})
	}
}

// TestNegativeFactPatternsCoverage —— 锁定负向模式表的**完整 13 条**。
//
// 对账 TS `NEGATIVE_FACT_PATTERNS` 的 13 条。**与标记表不同，这张表必须
// 全量移植**——负向断言是**自然语言**，不依赖 Go 侧哪个子系统产生；
// 只要是有损输出，任何负向词都该被拦。
//
// 逐条给一个最小有损上下文（用 Go 侧真实标记），确认每条都能命中。
func TestNegativeFactPatternsCoverage(t *testing.T) {
	// 13 条模式对应的触发词（逐条对账 TS 声明序）。
	triggers := []string{
		"empty", "not found", "no matches", "0 results", "0 files",
		"nothing to commit", "no tests found", "all passed", "no errors",
		"unchanged", "not modified", "not detected", "no changes",
	}
	if len(triggers) != len(negativeFactPatterns) {
		t.Fatalf("模式表应有 %d 条，实际 %d 条", len(triggers), len(negativeFactPatterns))
	}

	for i, word := range triggers {
		content := "[output truncated: 100 of 5000 lines shown]\n" + word
		got := DetectNegativeFactInLossyResult(content)
		if got == nil {
			t.Errorf("模式[%d] %q 应命中，得到 nil", i, word)
			continue
		}
		if !strings.EqualFold(got.Matched, word) {
			t.Errorf("模式[%d] 期望 matched=%q，得到 %q", i, word, got.Matched)
		}
	}
}

// TestNegativeFactWordBoundary —— 锁定 `\b` 词边界语义（防词内子串误命中）。
//
// 对账 TS 用 `\bempty\b` 而非裸 `empty`。若无边界，`emptyish` / `unchangedness`
// 会误命中——那些是普通词，不构成负向断言。
func TestNegativeFactWordBoundary(t *testing.T) {
	cases := []string{
		"[output truncated: 100 of 5000 lines shown]\nemptyish buffer",
		"[output truncated: 100 of 5000 lines shown]\nunchangedness",
	}
	for _, content := range cases {
		if got := DetectNegativeFactInLossyResult(content); got != nil {
			t.Errorf("词内子串不该命中，得到 matched=%q（内容：%q）", got.Matched, content)
		}
	}
}

// TestGuardLossyToolResult —— 对账 TS 的三个 guard 用例。
func TestGuardLossyToolResult(t *testing.T) {
	t.Run("无检出时原样返回", func(t *testing.T) {
		content := "[ls -la] exit=0 time=0.1s lines=42 — output complete\nfile1\nfile2"
		if got := GuardLossyToolResult(content); got != content {
			t.Errorf("应原样返回，得到 %q", got)
		}
	})

	t.Run("检出时前置 VERIFICATION_REQUIRED 标记", func(t *testing.T) {
		content := "[collapsed bash: 5 calls in 1 file]\n  ls -la .rivet/sessions/  exit=0  0 lines\n(empty)"
		guarded := GuardLossyToolResult(content)
		if !strings.Contains(guarded, verificationRequiredMarker) {
			t.Errorf("应含 %s 标记", verificationRequiredMarker)
		}
		if !strings.Contains(guarded, "Do NOT conclude absence/emptiness") {
			t.Error("应含 TS 的收尾文案")
		}
		if !strings.Contains(guarded, content) {
			t.Error("原文必须保留在警告之后")
		}
		if !strings.HasPrefix(guarded, verificationRequiredMarker) {
			t.Error("标记必须在最前")
		}
	})

	t.Run("已有标记时不重复注入", func(t *testing.T) {
		content := "[collapsed bash: 5 calls in 1 file]\n(empty)"
		guarded1 := GuardLossyToolResult(content)
		if !strings.Contains(guarded1, verificationRequiredMarker) {
			t.Fatal("首次应注入标记")
		}
		guarded2 := GuardLossyToolResult(guarded1)
		if n := strings.Count(guarded2, verificationRequiredMarker); n != 1 {
			t.Errorf("标记不得重复，得到 %d 个", n)
		}
	})
}
