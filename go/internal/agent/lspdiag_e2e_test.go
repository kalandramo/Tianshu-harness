package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/lsp"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// 本文件是**真实 gopls 的端到端验收**——与 lspdiag_test.go 的假件测试不同，
// 它 spawn 真语言服务器，验证「用户实际会看到什么」。
//
// 假件测试证明**协议时序**正确；本文件证明**整条链路在真实 server 下通电**。
// 两者缺一不可：假件测不出「真实 server 的推送格式与预期不符」这类问题。

// lspAdapter 把真 Navigator 适配成 agent.LspDiagnostics（与 main.go 同构）。
type lspAdapter struct{ nav *lsp.Navigator }

func (a *lspAdapter) IsReady() bool                  { return a.nav.IsReady() }
func (a *lspAdapter) ChangeFile(p string)            { a.nav.ChangeFile(p) }
func (a *lspAdapter) HasServerForFile(p string) bool { return a.nav.HasServerForFile(p) }
func (a *lspAdapter) GetFileDiagnostics(p string, ms int) []lsp.LspDiagnostic {
	return a.nav.GetFileDiagnostics(p, ms)
}

// requireGopls 报告真实 gopls 是否可用（不可用则跳过，标明原因）。
func requireGopls(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("跳过真实端到端：PATH 上没有 gopls（假件测试仍覆盖协议时序）")
	}
}

// newGoProject 造一个最小 Go 模块（gopls 需要模块上下文才有完整诊断）。
func newGoProject(t *testing.T, fileBody string) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module probe\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte(fileBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

// TestE2E_RealGopls_DiagnosticsAppearInToolResult —— ★★ 用户级验收 A。
//
// # 用户动作 → 可观察结果
//
// 用户对一个含 `undefinedSymbol` 的 Go 文件执行 `edit_file`（把某个标识符
// 改成一个不存在的名字）→ 工具结果正文里应出现 `[LSP Diagnostics]` 段，
// 且含 gopls 报出的**真实**错误文本。
//
// # 这条测试证明什么（假件测试证明不了的）
//
// 假件测试里「推送什么」是我规定的；本测试里推送是 **gopls 真实产出**——
// 它验证 ① 我们的通知/等待时序对真 server 成立 ② 真实推送的 JSON 形状
// 被我们的解析器接住 ③ 整条链路在真实子进程下不 hang。
func TestE2E_RealGopls_DiagnosticsAppearInToolResult(t *testing.T) {
	requireGopls(t)

	// 起点：一个能编译的文件
	const good = `package main

import "fmt"

func main() {
	fmt.Println("hi")
}
`
	dir, file := newGoProject(t, good)

	nav := lsp.NewNavigator(dir)
	if err := nav.Initialize(); err != nil {
		t.Fatalf("LSP 初始化失败：%v", err)
	}
	defer nav.Dispose()

	if !nav.IsReady() {
		t.Skip("gopls 存在但 LSP 子系统未就绪（可能是版本/环境问题）")
	}

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})

	// ★ 真实编辑：把 "hi" 改成对不存在符号的引用 → 引入编译错误
	res, err := reg.Execute(context.Background(), "edit_file", &tools.CallParams{
		Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path":  file,
			"old_string": `fmt.Println("hi")`,
			"new_string": `fmt.Println(undefinedSymbolXyz)`,
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("编辑失败：err=%v %s", err, res.Content)
	}

	// 走生产注入路径
	l := &Loop{LspDiagnostics: &lspAdapter{nav: nav}}
	tc := toolCall{name: "edit_file", input: map[string]any{"file_path": file}}
	// 给 gopls 充分的分析时间（冷启动 + 增量分析）
	out := l.injectLspDiagnostics(tc, res)

	if !strings.Contains(out.Content, "[LSP Diagnostics]") {
		t.Fatalf("★ 真实 gopls 下工具结果应含 [LSP Diagnostics] 段。\n实际正文：\n%s", out.Content)
	}
	// gopls 应报出「undefined」类错误
	if !strings.Contains(strings.ToLower(out.Content), "undefined") {
		t.Errorf("诊断应指出未定义符号（gopls 的真实报错）：\n%s", out.Content)
	}
	t.Logf("验收 A 通过——工具结果正文：\n%s", out.Content)
}

// TestE2E_RealGopls_CleanFileYieldsNoDiagnostics —— 对照：改对了就无诊断段。
//
// 防止「不管有没有问题都贴一段 [LSP Diagnostics]」这种假通过。
func TestE2E_RealGopls_CleanFileYieldsNoDiagnostics(t *testing.T) {
	requireGopls(t)
	dir, file := newGoProject(t, "package main\n\nfunc main() {}\n")

	nav := lsp.NewNavigator(dir)
	if err := nav.Initialize(); err != nil {
		t.Fatalf("LSP 初始化失败：%v", err)
	}
	defer nav.Dispose()
	if !nav.IsReady() {
		t.Skip("LSP 未就绪")
	}

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	// 加一个注释（不引入任何错误）
	res, err := reg.Execute(context.Background(), "edit_file", &tools.CallParams{
		Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": file, "old_string": "func main() {}", "new_string": "func main() {} // ok",
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("编辑失败：%v %s", err, res.Content)
	}

	l := &Loop{LspDiagnostics: &lspAdapter{nav: nav}}
	out := l.injectLspDiagnostics(toolCall{name: "edit_file", input: map[string]any{"file_path": file}}, res)
	if strings.Contains(out.Content, "[LSP Diagnostics]") {
		t.Errorf("干净文件不该贴诊断段（否则是假通过）：\n%s", out.Content)
	}
}

// TestE2E_RealGopls_DryRunDoesNotWriteOnDisk —— ★★ 用户级验收 B。
//
// # 用户动作 → 可观察结果
//
// 用户传 `dry_run: true` 做编辑 → 磁盘文件**字节级不变**，结果以
// 「预览（dry_run）」开头且声明未写入。
//
// 这是本轮修复的核心缺陷的验收：修复前，dry_run=true 会**直接落盘**。
func TestE2E_RealGopls_DryRunDoesNotWriteOnDisk(t *testing.T) {
	dir, file := newGoProject(t, "package main\n\nfunc main() {}\n")

	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})

	res, err := reg.Execute(context.Background(), "edit_file", &tools.CallParams{
		Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": file, "old_string": "func main() {}", "new_string": "func main() { panic(1) }",
			"dry_run": true,
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("dry_run 失败：err=%v %s", err, res.Content)
	}

	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("★ 验收 B 失败：dry_run 后磁盘内容变了\n 改前 %q\n 改后 %q", before, after)
	}
	if !strings.HasPrefix(res.Content, "预览（dry_run）") {
		t.Errorf("★ 结果应以「预览（dry_run）」开头：%q", res.Content)
	}
	if !strings.Contains(res.Content, "未写入任何更改") {
		t.Errorf("★ 应声明未写入：%q", res.Content)
	}
	t.Logf("验收 B 通过——预览正文：\n%s", res.Content)
}

// TestE2E_RealGopls_DryRunPredictsSyntaxError —— ★★ 用户级验收 C。
//
// # 用户动作 → 可观察结果
//
// 用户对**已损坏**的 Go 文件传 `dry_run: true`（编辑会让语法更坏）→
// 结果含「若应用将出现语法错误」预警，且磁盘**仍不变**。
//
// 这条验证 dry_run 的核心价值：**在写盘前知道会坏**。常规路径是
// 「写盘后检查、失败回滚」，dry_run 不能这么做。
func TestE2E_RealGopls_DryRunPredictsSyntaxError(t *testing.T) {
	// 起点是**语法错误**的文件（缺右花括号）
	dir, file := newGoProject(t, "package main\n\nfunc main() {\n")
	before, _ := os.ReadFile(file)

	reg := tools.NewDefaultRegistry(tools.Options{Cwd: dir})
	res, err := reg.Execute(context.Background(), "edit_file", &tools.CallParams{
		Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		Input: map[string]any{
			"file_path": file, "old_string": "func main() {", "new_string": "func main() { var x int",
			"dry_run": true,
		},
	})
	if err != nil {
		t.Fatalf("dry_run 失败：%v", err)
	}

	if !strings.Contains(res.Content, "若应用将出现语法错误") {
		t.Errorf("★ 验收 C 失败：应预告语法错误。实际：%q", res.Content)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Errorf("★ 报了语法错误也不该写盘\n 改前 %q\n 改后 %q", before, after)
	}
	t.Logf("验收 C 通过——预览正文：\n%s", res.Content)
}
