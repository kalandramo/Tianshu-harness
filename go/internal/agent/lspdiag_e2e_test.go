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
// ★ 用**生产同一个**适配器（订正自审查 #8）。
//
// 原先这里有一份与 `cmd/tianshu/main.go` 逐字同构的副本，代价是
// 「生产侧漂移不会被 e2e 覆盖」——测试验证的是测试那份。现共用
// `NavigatorDiagnostics`。
func newLspAdapter(nav *lsp.Navigator) LspDiagnostics {
	return NavigatorDiagnostics(nav)
}

// requireGopls 报告真实 gopls 是否可用。
//
// # ★ 默认**失败**而非跳过（第一百零四刀）
//
// 本组是唯一能暴露「真实 server 行为与假件不同」的用例——上一刀的三个缺陷
// （生产从不 spawn / `openedDocs` 键不一致 / gopls 不重推同内容 didChange）
// **全都只有真实 gopls 能暴露**。
//
// 若在无 gopls 时静默 `t.Skip`，「全量 0 FAIL」在这些用例**全部跳过**时
// 同样成立——那是**假绿**：验收报告说通过，实际最关键的一层根本没跑。
//
// 故：默认 `t.Fatal` 要求装 gopls；确实无法安装的机器用
// `RIVET_LSP_E2E=0` **显式**豁免（豁免要留痕，不能靠环境静默决定）。
func requireGopls(t *testing.T) {
	t.Helper()
	if os.Getenv("RIVET_LSP_E2E") == "0" {
		t.Skip("RIVET_LSP_E2E=0：显式豁免真实 LSP 端到端（需自行确保别处覆盖）")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("★ 需要真实 gopls 才能验证本组用例（PATH 上未找到）。" +
			"装 gopls（`go install golang.org/x/tools/gopls@latest`）" +
			"或显式设 RIVET_LSP_E2E=0 豁免——**不要静默跳过**。")
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
		// ★ 不跳过：`requireGopls` 已确认 gopls 在 PATH 上，
		// 此时子系统未就绪 **是真实故障**（版本/环境问题），
		// 跳过会把它伪装成「环境不满足」而放过。
		t.Fatalf("gopls 在 PATH 上但 LSP 子系统未就绪——这是真实故障，不是环境缺失")
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
	l := &Loop{LspDiagnostics: newLspAdapter(nav)}
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
		t.Fatalf("gopls 在 PATH 上但 LSP 子系统未就绪——真实故障，不跳过")
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

	l := &Loop{LspDiagnostics: newLspAdapter(nav)}
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
func TestE2E_DryRunDoesNotWriteOnDisk(t *testing.T) {
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
func TestE2E_DryRunPredictsSyntaxError(t *testing.T) {
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

// TestE2E_RequireGopls_FailsWithoutGopls —— ★ 防「假绿」的元测试。
//
// # 为什么需要一条测试来测「测试的失败行为」
//
// 上一刀的真实缺陷（生产不 spawn / 键不一致）**全都只有真实 gopls 能暴露**。
// 若 `requireGopls` 在无 gopls 时静默 `t.Skip`，那么 CI 上「全量 0 FAIL」
// 在这些用例**全部跳过**时同样成立——验收报告说通过，最关键的一层却没跑。
//
// 本用例断言「无 gopls 时必须失败」，把该机制本身钉住：
// 将来有人把 `Fatalf` 改回 `Skip`，这里会红。
func TestE2E_RequireGopls_FailsWithoutGopls(t *testing.T) {
	// ★ **无条件执行**（订正自审查 #6）。
	//
	// 本用例是**源码级断言**，不 spawn gopls、不依赖环境——原先加
	// `if gopls 存在 { t.Skip }` 等于让它在**最需要它的机器上**
	// （装了 gopls 的 CI/开发机）静默跳过，正是它要防的假绿形态。
	if _, err := exec.LookPath("gopls"); err == nil {
		t.Log("本机有 gopls——但本用例是源码断言，条件不变，继续执行")
	}
	// 源码级断言：确认存在 Fatalf 分支且保留 RIVET_LSP_E2E 逃生阀。
	src, err := os.ReadFile("lspdiag_e2e_test.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func requireGopls(")
	if start < 0 {
		t.Fatal("找不到 requireGopls")
	}
	rest := body[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end > 0 {
		rest = rest[:end+1]
	}
	if !strings.Contains(rest, "t.Fatalf") {
		t.Errorf("★ requireGopls 必须在无 gopls 时 t.Fatalf（而非静默跳过）")
	}
	if !strings.Contains(rest, `RIVET_LSP_E2E`) {
		t.Errorf("★ 应保留 RIVET_LSP_E2E=0 显式逃生阀（豁免要留痕）")
	}
}
