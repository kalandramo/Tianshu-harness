package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// ── 运行器探测（RED 先行）──

// Go 项目应探测为 go test。
func TestRunTestsDetectGo(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	mustWriteFile(t, filepath.Join(root, "x_test.go"), "package x\n")

	tool := RunTests(root)
	r, err := tool.Execute(context.Background(), call(root, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("Go 项目应可跑测试：%s", r.Content)
	}
	if r.Verification == nil {
		t.Fatal("必须产出 Verification 元数据（结构化结果是本工具的核心价值）")
	}
	if r.Verification.Status != contract.VerificationPassed {
		t.Errorf("status = %q, want passed", r.Verification.Status)
	}
}

// node 项目（package.json 有 test script）应探测并执行。
func TestRunTestsDetectNode(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "package.json"), `{"name":"x","scripts":{"test":"node --test"}}`)
	mustWriteFile(t, filepath.Join(root, "x.test.js"), "// test\n")

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	// 无测试文件时 node --test 会失败，但应能跑到执行阶段（不是 blocked）
	if r.Verification.BlockedReason == "no_test_framework" {
		t.Errorf("有 package.json 不应报 no_test_framework：%s", r.Content)
	}
}

// Python 项目（pytest 标记）应探测为 pytest。
func TestRunTestsDetectPytest(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pytest.ini"), "[pytest]\n")
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "tests", "test_x.py"), "def test_x(): pass\n")

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.BlockedReason == "no_test_framework" {
		t.Errorf("有 pytest.ini 不应报 no_test_framework")
	}
}

// 反证 A：无任何测试框架时必须 blocked 且给出 blockedReason。
//
// 这个分类很重要：blocked 是中性信号（不是失败），下游据此给场景化指引
// 而非统一的「测试失败」文案。
func TestRunTestsNoFrameworkBlocked(t *testing.T) {
	root := t.TempDir() // 空目录

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))

	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Status != contract.VerificationBlocked {
		t.Errorf("无框架应 blocked，实际 %q", r.Verification.Status)
	}
	if r.Verification.BlockedReason != "no_test_framework" {
		t.Errorf("blockedReason = %q, want no_test_framework", r.Verification.BlockedReason)
	}
	// 必须给用户可行动的指引
	if r.Verification.UserGuidance == "" {
		t.Error("blocked 必须给 userGuidance（否则用户不知道下一步）")
	}
	// blocked 不应标记为错误（中性信号）
	if r.IsError {
		t.Error("blocked 不应是 IsError——它是中性门禁信号")
	}
}

// 反证 B：Python 项目但无测试文件 → no_tests_found（区别于 no_test_framework）。
func TestRunTestsPytestNoTestsFound(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "pytest.ini"), "[pytest]\n")
	// 不建 tests/ 目录

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Status != contract.VerificationBlocked {
		t.Errorf("无测试文件应 blocked，实际 %q", r.Verification.Status)
	}
	if r.Verification.BlockedReason != "no_tests_found" {
		t.Errorf("blockedReason = %q, want no_tests_found（应与 no_test_framework 区分）",
			r.Verification.BlockedReason)
	}
}

// 反证 C：失败的测试必须解析出 passed/failed 计数。
func TestRunTestsParseFailureCounts(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	// 一个会失败的测试
	mustWriteFile(t, filepath.Join(root, "x_test.go"), `package x

import "testing"

func TestFail(t *testing.T) { t.Fatal("boom") }
`)

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Status != contract.VerificationFailed {
		t.Errorf("失败测试应 status=failed，实际 %q", r.Verification.Status)
	}
	if r.Verification.ExitCode == nil || *r.Verification.ExitCode == 0 {
		t.Error("失败时应有非零退出码")
	}
	// 关键：必须能看出失败（不是静默的 0 passed 0 failed）
	if r.Verification.Failed == nil || *r.Verification.Failed == 0 {
		t.Errorf("应解析出失败数，实际 %v", r.Verification.Failed)
	}
}

// 反证 D：通过的测试应给出 passed 计数。
func TestRunTestsParsePassCounts(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	mustWriteFile(t, filepath.Join(root, "x_test.go"), `package x

import "testing"

func TestOK(t *testing.T) {}
func TestOK2(t *testing.T) {}
`)

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Status != contract.VerificationPassed {
		t.Fatalf("应 passed，实际 %q：%s", r.Verification.Status, r.Content)
	}
	if r.Verification.Passed == nil || *r.Verification.Passed == 0 {
		t.Errorf("应解析出通过数，实际 %v", r.Verification.Passed)
	}
}

// 反证 E：blocked 必须是中性信号——不可被当作「测试失败」惩罚。
//
// 这是本工具最重要的语义：blocked（无框架）≠ failed（测试红了）。
func TestRunTestsBlockedIsNeutral(t *testing.T) {
	root := t.TempDir()
	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))

	if r.Verification.Status == contract.VerificationFailed {
		t.Fatal("无框架不应判为 failed——那会让下游误以为代码有问题")
	}
	// 内容应说明原因与出路
	if !strings.Contains(r.Content, "测试") {
		t.Errorf("内容应说明测试相关原因：%s", r.Content)
	}
}

// scope：无 filter 是 full，有 filter 是 targeted。
func TestRunTestsScope(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	mustWriteFile(t, filepath.Join(root, "x_test.go"), "package x\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n")

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification.Scope != "full" {
		t.Errorf("无 filter 应为 full，实际 %q", r.Verification.Scope)
	}
}

// 超时应被识别为 timeout（而非 invocation_failure）。
func TestRunTestsTimeout(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	mustWriteFile(t, filepath.Join(root, "x_test.go"), `package x

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) { time.Sleep(30 * time.Second) }
`)

	tool := RunTests(root)
	p := call(root, map[string]any{"timeout_ms": 1500})
	r, _ := tool.Execute(context.Background(), p)

	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Status != contract.VerificationBlocked {
		t.Errorf("超时应 blocked，实际 %q", r.Verification.Status)
	}
	if r.Verification.BlockedReason != "timeout" {
		t.Errorf("blockedReason = %q, want timeout", r.Verification.BlockedReason)
	}
}

// 反证 F：Verification 的 command 字段必须非空（下游据此核销验证命令）。
func TestRunTestsVerificationCommandNonEmpty(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.27\n")
	mustWriteFile(t, filepath.Join(root, "x_test.go"), "package x\n")

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if r.Verification.Command == "" {
		t.Error("command 不可为空——报告里的数字要能指到一条真实验证记录")
	}
}

// 工具声明与审批。
func TestRunTestsDefinitionAndApproval(t *testing.T) {
	root := t.TempDir()
	tool := RunTests(root)
	if tool.Definition().Name != "run_tests" {
		t.Errorf("工具名 = %q", tool.Definition().Name)
	}
	// 跑测试是读操作（不写文件），但仍会执行任意命令——保守起见按档位
	p := &CallParams{Input: map[string]any{}, ApprovalMode: "dangerously-skip-permissions"}
	if tool.RequiresApproval(p) {
		t.Error("放开档位下不应需批准")
	}
}

// blocked 结果不得被标记 IsError（中性信号语义）。
func TestRunTestsBlockedNotError(t *testing.T) {
	root := t.TempDir()
	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.IsError {
		t.Error("blocked 结果不应 IsError——门禁已重置，不是失败")
	}
}

// 已声明的 verify 命令（.rivet-config.json）应优先使用。
func TestRunTestsDeclaredCommand(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".rivet-config.json"),
		`{"verify":{"test":"echo 'declared-ran'"}}`)

	tool := RunTests(root)
	r, _ := tool.Execute(context.Background(), call(root, map[string]any{}))
	if r.Verification == nil {
		t.Fatal("应产出 Verification")
	}
	if !strings.Contains(r.Content, "declared-ran") {
		t.Errorf("应执行声明的命令：%s", r.Content)
	}
}
