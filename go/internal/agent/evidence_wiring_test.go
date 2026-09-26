package agent

import (
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// TestObserveToolResultRecordsFailedVerification —— **RED 复现既有缺陷**。
//
// # 缺陷（本刀核实）
//
// `observeToolResult` 开头有 `if res.IsError { return }`（「只在工具成功时
// 记账」）。但 `run_tests` 在**测试失败**时返回 `IsError: exitCode != 0`
// ——故那个早退把失败验证挡在门外，`run_tests` 分支里的
// `if res.IsError { status = "failed" }` **永不可达**。
//
// 后果：`hasFailedTests` 恒 false，`HasVerificationDebt()` 的「曾失败」
// 分支永不触发。
//
// # 为什么早退不能简单去掉
//
// 「失败的工具调用不该污染状态」对**读/写类**工具是对的（读失败不该记
// `TrackFileRead`）。但 `run_tests` 的失败**正是要记的事实**——它是
// 「验证发生过且红了」，不是「工具坏了」。
func TestObserveToolResultRecordsFailedVerification(t *testing.T) {
	l := newTestLoopForObserve(t)

	// 模拟 run_tests 失败：IsError=true（对账 run_tests.go 的 `exitCode != 0`）。
	failed := contract.Result{
		Content:      "FAIL",
		IsError:      true,
		Verification: &contract.VerificationMetadata{Status: contract.VerificationFailed},
	}
	l.observeToolResult("run_tests", map[string]any{"filter": "x"}, failed)

	if got := l.evidence.GateState(0, nil); got.Verifications != 1 {
		t.Errorf("失败的 run_tests 应记 1 次验证，实得 %d——早退把失败验证挡掉了", got.Verifications)
	}
	if !l.evidence.HasVerificationDebt() {
		t.Error("失败的验证应产生验证债（hasFailedTests 应为真）")
	}
}

// TestObserveToolResultRecordsPassedVerification —— 通过的验证仍照常记录。
func TestObserveToolResultRecordsPassedVerification(t *testing.T) {
	l := newTestLoopForObserve(t)

	passed := contract.Result{
		Content:      "ok",
		Verification: &contract.VerificationMetadata{Status: contract.VerificationPassed},
	}
	l.observeToolResult("run_tests", map[string]any{"filter": "x"}, passed)

	if got := l.evidence.GateState(0, nil); got.Verifications != 1 {
		t.Errorf("通过的 run_tests 应记 1 次验证，实得 %d", got.Verifications)
	}
	if l.evidence.HasVerificationDebt() {
		t.Error("通过的验证不该产生债")
	}
}

// TestObserveToolResultFailedReadDoesNotPollute —— **反面对照**：
// 失败的文件工具调用仍不该污染状态（早退对它们是正确的）。
//
// 这是「修 run_tests 不能顺手放开早退」的不变量。
func TestObserveToolResultFailedReadDoesNotPollute(t *testing.T) {
	l := newTestLoopForObserve(t)

	failedRead := contract.Result{Content: "not found", IsError: true}
	l.observeToolResult("read_file", map[string]any{"file_path": "src/a.ts"}, failedRead)

	if got := l.evidence.GateState(0, nil); got.HasReadTestFiles {
		t.Error("失败的 read_file 不该记入 filesRead")
	}
	if l.State != nil {
		if _, ok := l.State.Snapshot().FileIndex.Get("src/a.ts"); ok {
			t.Error("失败的 read_file 不该记入会话 FileIndex")
		}
	}
}

// TestObserveToolResultFailedWriteDoesNotPollute —— 同上，写工具。
func TestObserveToolResultFailedWriteDoesNotPollute(t *testing.T) {
	l := newTestLoopForObserve(t)

	failedWrite := contract.Result{Content: "permission denied", IsError: true}
	l.observeToolResult("write_file", map[string]any{"file_path": "src/a.ts"}, failedWrite)

	if got := l.evidence.GateState(0, nil); got.EditsSinceLastTest != 0 {
		t.Errorf("失败的 write_file 不该计入 gate 编辑数，实得 %d", got.EditsSinceLastTest)
	}
}

// newTestLoopForObserve 造一个最小 Loop（只为 observeToolResult 用）。
//
// **不建 HTTP server**：本测试只调 observeToolResult，不走 Run——故直接
// 构造 Loop 并手工初始化它依赖的两个字段。
func newTestLoopForObserve(t *testing.T) *Loop {
	t.Helper()
	l := &Loop{}
	l.State = session.New("evidence-wiring-test")
	l.evidence = newEvidenceTracker()
	return l
}
