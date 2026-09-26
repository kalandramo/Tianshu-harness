package agent

import (
	"context"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// tddgate_wiring_test.go —— TDD gate 接线到 `executeTool` 门链（第七十二刀）。
//
// # 这测的是什么（与 tddgate_oracle_test.go 的分工）
//
//   - `tddgate_oracle_test.go`：**纯函数**对账——`EvaluateTddGate` 的决策矩阵
//     与 TS 逐值等价。
//   - 本文件：**接线**——门真的在 `executeTool` 里被调用、block 真的阻止执行。
//     （纯函数绿 ≠ 接线绿：本项目栽过「函数正确但零调用者」多次。）
//
// # 为什么能测到「阻止执行」
//
// TDD 门在 `l.registry.Execute` **之前**返回——故用一个**未注册任何工具**的
// registry 也能验证：若门拦下，`executeTool` 提前返回（带 TDD 文案）；若门
// 放行，则会走到 registry 并报「工具未找到」。
//
// # 断言纪律（变异反证 M1 逼出来的）
//
// **不能只断言 `IsError`**：空 registry 下放行也返回 `IsError=true`
// （「工具未找到」）。必须断言**命中的是 TDD 文案**。
//
// **反面对照必须带阳性对照**：只断言「不该出现 TDD 文案」在接线被移除时
// **恒真**（门没了，文案当然不出现）——那是恒真断言（坑 #11）。故每个
// 负面用例先跑一次 enforce 阳性对照，确认「同状态下门确实会拦」。

// tddProbe 跑一次 `executeTool`，返回是否命中 TDD gate 的 block 文案。
//
// setup 用于造状态（改文件 / 读测试文件 / 记验证）。
func tddProbe(t *testing.T, tddEnv string, setup func(*Loop), toolName, target string) bool {
	t.Helper()
	l := &Loop{}
	l.cfg.TddGateEnv = tddEnv
	l.cfg.ApprovalMode = "dangerously-skip-permissions" // 跳过审批门，隔离 TDD 门
	l.State = session.New("tddgate-wiring-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry() // 空 registry：放行时会报「工具未找到」

	if setup != nil {
		setup(l)
	}
	input := map[string]any{}
	if target != "" {
		input["file_path"] = target
	}
	res := l.executeTool(context.Background(), toolCall{name: toolName, input: input})
	return isTddBlockMessage(res.Content)
}

// threeEditsReadTest 是「达阈值 + 读过测试文件」的共享状态。
//
// 这是 gate 会真正 block 的**唯一组合**（enforce + 3 次未验证编辑 + 读过测试）。
func threeEditsReadTest(l *Loop) {
	for _, p := range []string{"src/a.ts", "src/b.ts", "src/c.ts"} {
		l.State.TrackFileModified(p)
		l.evidence.TrackFileModified(p)
	}
	l.State.TrackFileRead("src/a.test.ts", "")
}

// threeEditsNoTestRead 同上但不读测试文件（触发 skipIfNoTests 降级）。
func threeEditsNoTestRead(l *Loop) {
	for _, p := range []string{"src/a.ts", "src/b.ts", "src/c.ts"} {
		l.State.TrackFileModified(p)
		l.evidence.TrackFileModified(p)
	}
}

// TestTddGateBlocksAtThresholdEnforce —— **核心接线断言**：enforce 模式下
// 达阈值时，edit 工具被真的拦下，且文案来自 TDD gate。
func TestTddGateBlocksAtThresholdEnforce(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Error("enforce 模式达阈值应拦下 edit_file 并返回 TDD 文案")
	}
}

// TestTddGateSuggestModeDoesNotBlock —— suggest 模式（默认）永不拦截。
//
// **带阳性对照**：同状态在 enforce 下必须拦（否则本测试恒真）。
func TestTddGateSuggestModeDoesNotBlock(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：同状态下 enforce 应拦——否则本测试恒真，接线可能已失效")
	}
	if tddProbe(t, "suggest", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Error("suggest 模式不该拦（默认行为：防修复中途被逼进重写循环）")
	}
}

// TestTddGateDisabledEnvNeverBlocks —— `RIVET_TDD_GATE=off` 时门完全关闭。
func TestTddGateDisabledEnvNeverBlocks(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：enforce 应拦")
	}
	if tddProbe(t, "off", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Error("门关闭时不该拦")
	}
}

// TestTddGateNotAppliedToNonEditTools —— 读类工具不经本门。
func TestTddGateNotAppliedToNonEditTools(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：enforce 应拦 edit_file")
	}
	if tddProbe(t, "enforce", threeEditsReadTest, "read_file", "src/d.ts") {
		t.Error("read_file 不该被 TDD gate 拦（门只管 edit/write 类）")
	}
}

// TestTddGateScratchPathExempt —— scratch 探针豁免（否则锁死探针纪律）。
func TestTddGateScratchPathExempt(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：enforce 应拦普通源码路径")
	}
	if tddProbe(t, "enforce", threeEditsReadTest, "write_file", ".rivet/scratch/probe.ts") {
		t.Error("scratch 路径应豁免")
	}
}

// TestTddGateSkipIfNoTestsDowngrades —— 未读过测试文件时 enforce 也降级放行。
func TestTddGateSkipIfNoTestsDowngrades(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：读过测试文件时 enforce 应拦")
	}
	if tddProbe(t, "enforce", threeEditsNoTestRead, "edit_file", "src/d.ts") {
		t.Error("未读测试文件时应降级放行（skipIfNoTests：项目可能无测试设施）")
	}
}

// TestTddGateClearedByVerification —— 跑过测试后门放行。
//
// 这是「拦截不是永久锁」的不变量——否则 agent 被锁死无法推进。
func TestTddGateClearedByVerification(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：零验证时 enforce 应拦")
	}
	afterVerify := func(l *Loop) {
		threeEditsReadTest(l)
		l.evidence.TrackVerification("passed")
	}
	if tddProbe(t, "enforce", afterVerify, "edit_file", "src/d.ts") {
		t.Error("跑过验证后应放行（编辑计数归零）")
	}
}

// TestTddGateTestTargetNotBlocked —— 目标是测试文件时降级（RED 步骤豁免）。
func TestTddGateTestTargetNotBlocked(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：非测试目标是 enforce 应拦")
	}
	if tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/agent/__tests__/foo.test.ts") {
		t.Error("改测试文件是 RED 步骤，不该拦")
	}
}

// TestTddGateGoTestFileNotExempt —— **钉住正则差异的行为后果**。
//
// `foo_test.go` 只匹配 `evidence.go` 的 `testFileRe`（含 `_test.`），
// **不**匹配 tdd-gate 的 `isTddTestFile`。故 enforce 下改 Go 测试文件
// **仍会被拦**——这与 TS 一致（TS 的 isTestFile 同样不认 `_test.go`）。
//
// 若有人「统一」这两个正则，本测试会红——那是**行为回归**。
func TestTddGateGoTestFileNotExempt(t *testing.T) {
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/d.ts") {
		t.Fatal("阳性对照失败：enforce 应拦")
	}
	if !tddProbe(t, "enforce", threeEditsReadTest, "edit_file", "src/foo_test.go") {
		t.Error("`foo_test.go` 应**仍被拦**——tdd-gate 的 isTestFile 只认 " +
			"`.test.`/`.spec.`/`__tests__`（与 TS 一致）。若这里放行，" +
			"说明两个正则被误统一了")
	}
}

// isTddBlockMessage 判定文案是否来自 TDD gate 的 block 分支。
func isTddBlockMessage(content string) bool {
	const prefix = "TDD Gate: "
	return len(content) >= len(prefix) && content[:len(prefix)] == prefix
}
