package agent

import (
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/lsp"
)

// fakeDiag 是 `LspDiagnostics` 的测试实现（本机无 gopls，端到端只能靠假件）。
type fakeDiag struct {
	ready      bool
	hasServer  bool
	diags      []lsp.LspDiagnostic
	changeFile []string
	getCalls   int
}

func (f *fakeDiag) IsReady() bool { return f.ready }
func (f *fakeDiag) GetFileDiagnostics(string, int) []lsp.LspDiagnostic {
	f.getCalls++
	return f.diags
}
func (f *fakeDiag) ChangeFile(p string)          { f.changeFile = append(f.changeFile, p) }
func (f *fakeDiag) HasServerForFile(string) bool { return f.hasServer }

// TestInjectLspDiagnostics_ModelAndUIChannels —— ★ 双通道注入。
//
// 对账 TS `tool-pipeline.ts:1594-1601`：
//
//	finalContent += `\n\n[LSP Diagnostics]\n${modelText}`
//	rawToolResult.uiContent = `${uiBase}\n\n[LSP Diagnostics]\n${uiText}`
//
// 判别力：① 只填一个通道 ② 漏掉 `[LSP Diagnostics]` 头 ③ uiText 用了收敛版
// （而非全量）——三种错都会红。
func TestInjectLspDiagnostics_ModelAndUIChannels(t *testing.T) {
	f := &fakeDiag{
		ready: true, hasServer: true,
		diags: []lsp.LspDiagnostic{
			{Range: lsp.Range{Start: lsp.Position{Line: 9}}, Severity: 1, Message: "near"},
			{Range: lsp.Range{Start: lsp.Position{Line: 99}}, Severity: 2, Message: "far warn"},
		},
	}
	l := &Loop{LspDiagnostics: f}
	tc := toolCall{name: "edit_file", input: map[string]any{"file_path": "a.go"}}
	res := contract.Result{
		Content:       "已编辑 a.go",
		ChangedRanges: []contract.Range{{Start: 10, End: 10}},
	}

	got := l.injectLspDiagnostics(tc, res)

	if !strings.Contains(got.Content, "[LSP Diagnostics]") {
		t.Fatalf("Content 应有 [LSP Diagnostics] 段：%q", got.Content)
	}
	if !strings.Contains(got.Content, "ERROR L10: near") {
		t.Errorf("区域内 error 应完整出现在 Content：%q", got.Content)
	}
	if strings.Contains(got.Content, "far warn") {
		t.Errorf("区域外 warning 不该进 Content（模型侧收敛）：%q", got.Content)
	}
	// ★ UI 侧应含**全量**（含区域外 warning）
	if !strings.Contains(got.UIContent, "far warn") {
		t.Errorf("UIContent 应是全量（含区域外 warning）：%q", got.UIContent)
	}
}

// TestInjectLspDiagnostics_ChangeFileBeforeFetch —— ★ 通知必须在取诊断之前。
//
// 对账 TS `tool-pipeline.ts:1576` 注释逐字：
// 「Must happen BEFORE diagnostics so the server's view is current」。
//
// 判别力：把 changeFile 挪到 GetFileDiagnostics 之后（或去掉）即红。
func TestInjectLspDiagnostics_ChangeFileBeforeFetch(t *testing.T) {
	f := &fakeDiag{ready: true, hasServer: false} // hasServer=false → 不取诊断
	l := &Loop{LspDiagnostics: f}
	tc := toolCall{name: "edit_file", input: map[string]any{"file_path": "a.go"}}

	l.injectLspDiagnostics(tc, contract.Result{Content: "ok"})

	if len(f.changeFile) != 1 || f.changeFile[0] != "a.go" {
		t.Errorf("changeFile 应被调用一次且带路径：%+v", f.changeFile)
	}
	if f.getCalls != 0 {
		t.Errorf("无 server 时不该取诊断（ShouldRunDiagnostics 应拦），实得 %d 次", f.getCalls)
	}
}

// TestInjectLspDiagnostics_OnlyWriteToolsNotify —— changeFile 通知面 vs 诊断面。
//
// 断言：① 非写工具（read_file）完全不触发
//
//	② apply_patch 会通知（从 diff 头解析路径）但**不取诊断**
func TestInjectLspDiagnostics_OnlyWriteToolsNotify(t *testing.T) {
	// ① read_file：既无通知也无诊断
	f := &fakeDiag{ready: true, hasServer: true}
	l := &Loop{LspDiagnostics: f}
	l.injectLspDiagnostics(toolCall{name: "read_file", input: map[string]any{"file_path": "a.go"}}, contract.Result{})
	if len(f.changeFile) != 0 || f.getCalls != 0 {
		t.Errorf("read_file 不该触发任何 LSP 动作：notify=%v gets=%d", f.changeFile, f.getCalls)
	}

	// ② apply_patch：通知（解析 diff 头）但不取诊断
	f2 := &fakeDiag{ready: true, hasServer: true}
	l2 := &Loop{LspDiagnostics: f2}
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"
	l2.injectLspDiagnostics(toolCall{name: "apply_patch", input: map[string]any{"diff": diff}}, contract.Result{})
	if len(f2.changeFile) != 1 || f2.changeFile[0] != "x.go" {
		t.Errorf("apply_patch 应从 diff 头解析路径通知：%+v", f2.changeFile)
	}
	if f2.getCalls != 0 {
		t.Errorf("apply_patch 不该取诊断（TS 只认 write_file/edit_file），实得 %d", f2.getCalls)
	}
}

// TestInjectLspDiagnostics_BestEffort —— ★ nil / 错误结果 / 无诊断都不改结果。
//
// 对账 TS：「Silent: LSP diagnostics are best-effort, never fail the turn」。
func TestInjectLspDiagnostics_BestEffort(t *testing.T) {
	base := contract.Result{Content: "原始内容"}

	// ① nil 接口
	l := &Loop{}
	if got := l.injectLspDiagnostics(toolCall{name: "edit_file"}, base); got.Content != "原始内容" {
		t.Errorf("nil 接口应原样返回：%q", got.Content)
	}

	// ② 工具失败 → 不注入（对账 TS `!harnessResult.isError` 前置）
	f := &fakeDiag{ready: true, hasServer: true, diags: []lsp.LspDiagnostic{{Severity: 1, Message: "e"}}}
	l2 := &Loop{LspDiagnostics: f}
	errRes := contract.Result{Content: "失败了", IsError: true}
	if got := l2.injectLspDiagnostics(toolCall{name: "edit_file", input: map[string]any{"file_path": "a.go"}}, errRes); got.Content != "失败了" {
		t.Errorf("失败结果不该被注入：%q", got.Content)
	}

	// ③ 无诊断 → 不加空段
	f3 := &fakeDiag{ready: true, hasServer: true}
	l3 := &Loop{LspDiagnostics: f3}
	if got := l3.injectLspDiagnostics(toolCall{name: "edit_file", input: map[string]any{"file_path": "a.go"}}, base); strings.Contains(got.Content, "LSP Diagnostics") {
		t.Errorf("无诊断不该加空段：%q", got.Content)
	}
}

// TestInjectLspDiagnostics_NoRangesFallbackWholeFile —— 无 ChangedRanges 时整文件。
//
// 对账 TS：`changedRanges` 缺失 → 安全降级为整文件列表（不隐藏错误）。
func TestInjectLspDiagnostics_NoRangesFallbackWholeFile(t *testing.T) {
	f := &fakeDiag{
		ready: true, hasServer: true,
		diags: []lsp.LspDiagnostic{
			{Range: lsp.Range{Start: lsp.Position{Line: 41}}, Severity: 1, Message: "far-away-error"},
		},
	}
	l := &Loop{LspDiagnostics: f}
	tc := toolCall{name: "write_file", input: map[string]any{"file_path": "a.go"}}
	// 注意：ChangedRanges 为空
	got := l.injectLspDiagnostics(tc, contract.Result{Content: "已写入"})

	if !strings.Contains(got.Content, "far-away-error") {
		t.Errorf("无 ranges 时应退化为整文件（不隐藏错误）：%q", got.Content)
	}
}

// TestExtractPatchTargetPaths —— diff 头解析（`+++ ` 为主，删除回退 `--- `）。
//
// 对账 TS `patchTargetPaths`（`src/agent/pre-write-claims.ts:18-35`）。
func TestExtractPatchTargetPaths(t *testing.T) {
	diff := "--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-x\n+y\n"
	got := extractPatchTargetPaths(diff)
	if len(got) != 1 || got[0] != "new.go" {
		t.Errorf("应解析出 new.go（去掉 b/ 前缀）：%+v", got)
	}

	// 多文件 + 去重
	diff2 := "+++ b/a.go\n+++ b/b.go\n+++ b/a.go\n"
	got2 := extractPatchTargetPaths(diff2)
	if len(got2) != 2 || got2[0] != "a.go" || got2[1] != "b.go" {
		t.Errorf("应得 [a.go b.go]（去重）：%+v", got2)
	}

	if len(extractPatchTargetPaths("没有补丁头的文本")) != 0 {
		t.Error("无 +++ 头应得空")
	}
}

// TestExtractPatchTargetPaths_DeletionFallsBackToMinusHeader —— ★ V2（第一百零五刀）。
//
// # 为什么必须回退取 `--- ` 头
//
// 纯删除补丁的新奇侧是 `+++ /dev/null`（文件没了）。若只认 `+++ `，
// **被删文件永远解析不出来** → `ChangeFile` 不通知 → server 与缓存里
// 该文件的陈旧诊断**永久保留**（用户会持续看到已删文件的错误）。
//
// TS 在 `src/agent/pre-write-claims.ts:29-32` 明确处理这个回退：
//
//	// Deletion (`+++ /dev/null`): the removed file is the preceding --- header.
//	const minus = /^--- (?:a\/)?(.+)$/.exec(lines[i - 1] ?? '')
//
// 判别力：去掉回退分支 → 本例红（返回空）。
func TestExtractPatchTargetPaths_DeletionFallsBackToMinusHeader(t *testing.T) {
	// 纯删除：`+++ /dev/null`，被删文件在前一行的 `--- a/x.go`
	del := "--- a/x.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-package x\n"
	got := extractPatchTargetPaths(del)
	if len(got) != 1 || got[0] != "x.go" {
		t.Errorf("★ 纯删除应回退取 `--- ` 头得 x.go，实得 %+v（失去通知 = 陈旧诊断永久保留）", got)
	}

	// 混合：一处修改 + 一处删除
	mixed := "--- a/keep.go\n+++ b/keep.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-g\n"
	got2 := extractPatchTargetPaths(mixed)
	if len(got2) != 2 {
		t.Fatalf("混合补丁应得 2 个路径，实得 %+v", got2)
	}
	seen := map[string]bool{}
	for _, p := range got2 {
		seen[p] = true
	}
	if !seen["keep.go"] || !seen["gone.go"] {
		t.Errorf("应同时含 keep.go 与 gone.go，实得 %+v", got2)
	}

	// `/dev/null` 本身绝不作为路径出现
	for _, p := range got2 {
		if p == "/dev/null" || p == "" {
			t.Errorf("不该出现占位路径：%+v", got2)
		}
	}
}
