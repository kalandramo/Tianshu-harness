package lsp

import (
	"strings"
	"testing"
)

// mkDiag 构造一条诊断。**注意**：TS 的 `LspDiagnostic.range.start.line` 是
// 0-based，故入参 line1 是「人看的 1-based 行号」，内部减 1。
func mkDiag(line1, severity int, msg string) LspDiagnostic {
	return LspDiagnostic{
		Range:    Range{Start: Position{Line: line1 - 1}, End: Position{Line: line1 - 1}},
		Severity: severity,
		Message:  msg,
	}
}

// TestFilterDiagnosticsForEdit_NoDiagnostics —— 无诊断 → 双空。
func TestFilterDiagnosticsForEdit_NoDiagnostics(t *testing.T) {
	got := FilterDiagnosticsForEdit(nil, []LineRange{{Start: 1, End: 3}}, DiagContextLines)
	if got.ModelText != "" || got.UIText != "" {
		t.Errorf("无诊断应返回双空，实得 %+v", got)
	}
}

// TestFilterDiagnosticsForEdit_InfoHintDropped —— severity 3/4（info/hint）
// 全部丢弃（TS `d.severity <= 2` 过滤）。
func TestFilterDiagnosticsForEdit_InfoHintDropped(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(1, 3, "an info"),
		mkDiag(2, 4, "a hint"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 5}}, DiagContextLines)
	if got.ModelText != "" || got.UIText != "" {
		t.Errorf("info/hint 应被过滤，实得 %+v", got)
	}
}

// TestFilterDiagnosticsForEdit_NoRangesFallbackWholeFile —— ★ 无 changedRanges
// 时退化为**整文件**列表（安全降级：宁可多给，不可隐藏）。
//
// 对账 TS `client.ts:349-352`：「When `changedRanges` is absent/empty we
// cannot localize, so the model gets the whole-file list — a safe fallback.」
func TestFilterDiagnosticsForEdit_NoRangesFallbackWholeFile(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(100, 1, "far away error"),
		mkDiag(200, 2, "far away warning"),
	}
	got := FilterDiagnosticsForEdit(diags, nil, DiagContextLines)
	want := "ERROR L100: far away error\nWARNING L200: far away warning"
	if got.ModelText != want {
		t.Errorf("无 ranges 应整文件输出\n want %q\n  got %q", want, got.ModelText)
	}
	if got.UIText != want {
		t.Errorf("uiText 同应为全量，实得 %q", got.UIText)
	}

	// 空切片（非 nil）同样走 fallback（TS `!changedRanges || length === 0`）
	got2 := FilterDiagnosticsForEdit(diags, []LineRange{}, DiagContextLines)
	if got2.ModelText != want {
		t.Errorf("空 ranges 也应整文件输出，实得 %q", got2.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_InRegionFullMessages —— 区域内给**完整消息**。
func TestFilterDiagnosticsForEdit_InRegionFullMessages(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(10, 1, "boom"),
		mkDiag(11, 2, "sus"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 10, End: 10}}, DiagContextLines)
	want := "ERROR L10: boom\nWARNING L11: sus"
	if got.ModelText != want {
		t.Errorf("\n want %q\n  got %q", want, got.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_ContextBoundary —— ±context 的边界是**闭区间**。
//
// range [10,10] + context 3 → 行 7..13 属区域内；行 6、14 属区域外。
func TestFilterDiagnosticsForEdit_ContextBoundary(t *testing.T) {
	ranges := []LineRange{{Start: 10, End: 10}}
	for _, tc := range []struct {
		line1 int
		in    bool
	}{
		{7, true}, {13, true}, // 边界内
		{6, false}, {14, false}, // 边界外
	} {
		diags := []LspDiagnostic{mkDiag(tc.line1, 1, "e")}
		got := FilterDiagnosticsForEdit(diags, ranges, DiagContextLines)
		hasFull := strings.Contains(got.ModelText, "ERROR L")
		if hasFull != tc.in {
			t.Errorf("行 %d：期望 in-region=%v，实得 %q", tc.line1, tc.in, got.ModelText)
		}
	}
}

// TestFilterDiagnosticsForEdit_OutRegionErrorCollapsed —— 区域外 error 折叠成
// 一行 nudge（带行号列表），**不丢错误**。
func TestFilterDiagnosticsForEdit_OutRegionErrorCollapsed(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(1, 1, "near"),
		mkDiag(50, 1, "far1"),
		mkDiag(51, 1, "far2"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 1}}, DiagContextLines)
	if !strings.Contains(got.ModelText, "ERROR L1: near") {
		t.Errorf("区域内应完整输出，实得 %q", got.ModelText)
	}
	if !strings.Contains(got.ModelText, "+2 error(s) elsewhere in file (L50, L51) — run typecheck before delivery") {
		t.Errorf("区域外 error 应折叠为 nudge，实得 %q", got.ModelText)
	}
	// ★ 关键：**不隐藏**——错误数量与行号都在
	if strings.Count(got.ModelText, "far1") != 0 {
		t.Errorf("折叠行不该含完整消息（只留行号），实得 %q", got.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_OutRegionWarningDropped —— 区域外 warning
// **完全丢弃**（TS 注释：`// out-of-region warnings are dropped`）。
func TestFilterDiagnosticsForEdit_OutRegionWarningDropped(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(1, 2, "near warn"),
		mkDiag(50, 2, "far warn"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 1}}, DiagContextLines)
	if strings.Contains(got.ModelText, "far warn") {
		t.Errorf("区域外 warning 应丢弃，实得 %q", got.ModelText)
	}
	if strings.Contains(got.ModelText, "elsewhere") {
		t.Errorf("只有 error 才产生 nudge，实得 %q", got.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_OutRegionLineCap —— 折叠行的行号列表上限 5 条，
// 超出加 "…" 后缀。
func TestFilterDiagnosticsForEdit_OutRegionLineCap(t *testing.T) {
	diags := []LspDiagnostic{mkDiag(1, 1, "near")}
	for i := 0; i < 8; i++ {
		diags = append(diags, mkDiag(100+i, 1, "far"))
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 1}}, DiagContextLines)
	want := "+8 error(s) elsewhere in file (L100, L101, L102, L103, L104, …) — run typecheck before delivery"
	if !strings.Contains(got.ModelText, want) {
		t.Errorf("\n want 含 %q\n  got %q", want, got.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_UIRegionAgnostic —— ★ uiText 是**全量**列表
// （区域无关），让 TUI/桌面工具卡仍能看到全部。
func TestFilterDiagnosticsForEdit_UIRegionAgnostic(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(1, 1, "near"),
		mkDiag(50, 2, "far warn"),
		mkDiag(51, 1, "far err"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 1}}, DiagContextLines)
	// 模型侧：近处全量 + 远处 error 折叠，远处 warning 丢弃
	if strings.Contains(got.ModelText, "far warn") {
		t.Errorf("模型侧应丢弃远处 warning，实得 %q", got.ModelText)
	}
	// UI 侧：全都在
	for _, want := range []string{"ERROR L1: near", "WARNING L50: far warn", "ERROR L51: far err"} {
		if !strings.Contains(got.UIText, want) {
			t.Errorf("uiText 应含 %q，实得 %q", want, got.UIText)
		}
	}
}

// TestFilterDiagnosticsForEdit_UICap —— uiText 上限 20 条（UI_DIAGNOSTIC_CAP）。
func TestFilterDiagnosticsForEdit_UICap(t *testing.T) {
	var diags []LspDiagnostic
	for i := 1; i <= 30; i++ {
		diags = append(diags, mkDiag(i, 1, "e"))
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 30}}, DiagContextLines)
	if n := strings.Count(got.UIText, "\n") + 1; n != 20 {
		t.Errorf("uiText 应为 20 条，实得 %d 条", n)
	}
}

// TestFilterDiagnosticsForEdit_ModelInRegionCap —— 区域内上限 10 条
// （MODEL_INREGION_CAP）。第 11 条起不出现（**静默截断，不提示**——与 TS 一致）。
func TestFilterDiagnosticsForEdit_ModelInRegionCap(t *testing.T) {
	var diags []LspDiagnostic
	for i := 1; i <= 15; i++ {
		diags = append(diags, mkDiag(i, 1, "e"))
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 1, End: 15}}, DiagContextLines)
	if n := strings.Count(got.ModelText, "\n") + 1; n != 10 {
		t.Errorf("modelText 区域内应截断到 10 条，实得 %d", n)
	}
}

// TestFilterDiagnosticsForEdit_CustomContext —— context 参数可覆盖（TS 第三参
// `context = DIAGNOSTIC_CONTEXT_LINES`）。
func TestFilterDiagnosticsForEdit_CustomContext(t *testing.T) {
	diags := []LspDiagnostic{mkDiag(20, 1, "e")}
	// context=0 → 行 20 不在 [10,10] 内
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 10, End: 10}}, 0)
	if !strings.Contains(got.ModelText, "elsewhere") {
		t.Errorf("context=0 时行 20 应在区域外，实得 %q", got.ModelText)
	}
	// context=10 → 行 20 = 10+10 在边界内
	got2 := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 10, End: 10}}, 10)
	if !strings.Contains(got2.ModelText, "ERROR L20") {
		t.Errorf("context=10 时行 20 应在区域内，实得 %q", got2.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_MultiRange —— 多个 range 取**并集**
// （TS `changedRanges.some(...)`）。
func TestFilterDiagnosticsForEdit_MultiRange(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(10, 1, "in-first"),
		mkDiag(100, 1, "in-second"),
		mkDiag(55, 1, "between"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 10, End: 10}, {Start: 100, End: 100}}, DiagContextLines)
	if !strings.Contains(got.ModelText, "in-first") || !strings.Contains(got.ModelText, "in-second") {
		t.Errorf("两个 range 内的都应完整输出，实得 %q", got.ModelText)
	}
	if !strings.Contains(got.ModelText, "+1 error(s) elsewhere in file (L55)") {
		t.Errorf("夹在两 range 之间的应为区域外，实得 %q", got.ModelText)
	}
}

// TestFilterDiagnosticsForEdit_SeverityZeroKept —— ★ 审查发现的边界订正。
//
// 原实现写 `severity >= 1 && severity <= 2`，对 `severity == 0` 会**过滤**；
// 而 TS 是 `d.severity <= 2`，`0 <= 2` 为 true → **保留**。方向相反。
//
// # 为什么保留 0 是正确的（而非"照抄 TS 的 bug"）
//
// LSP 的 `severity` 是**可选字段**——缺失时 Go 的零值恰是 0。
// 一条 severity 未知的诊断宁可多显示（本文件的降级原则：朝"多给"倒），
// 不可静默吞掉。判别力：改回 `>= 1 &&` 本用例即红。
func TestFilterDiagnosticsForEdit_SeverityZeroKept(t *testing.T) {
	diags := []LspDiagnostic{
		mkDiag(3, 0, "unknown severity"),
		mkDiag(4, 3, "info — 应丢弃"),
	}
	got := FilterDiagnosticsForEdit(diags, []LineRange{{Start: 3, End: 4}}, DiagContextLines)
	if !strings.Contains(got.ModelText, "unknown severity") {
		t.Errorf("severity=0 应保留（TS 的 0<=2 为 true），实得 %q", got.ModelText)
	}
	if strings.Contains(got.ModelText, "info — 应丢弃") {
		t.Errorf("severity=3 应丢弃（3<=2 为 false），实得 %q", got.ModelText)
	}
}
