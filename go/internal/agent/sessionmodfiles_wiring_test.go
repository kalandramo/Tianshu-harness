package agent

import (
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// sessionmodfiles_wiring_test.go —— 接线测试：`CallParams.SessionModifiedFiles`。
//
// # 为什么必须有这个文件
//
// 本字段此前**有读取方、零写入方**——`internal/tools/git.go` 两处读它
// （`gitCommit` 的归属范围回退、`gitStash`），但全 go 树无任何生产赋值点。
// 单测测的是「注入后的行为」，不是「生产会不会注入」；本文件锁住后者：
// 每条用例都走 **`buildToolCallParams`（唯一 `CallParams` 构造点）**，
// 而非直接调 helper——`TestBuildToolCallParamsCarriesProfile` 的注释记过
// 这个教训（只验 helper 时，改构造点的变异不会红）。
//
// 对账 TS `src/agent/tool-pipeline.ts:838`：
//
//	sessionModifiedFiles: [...deps.evidence.getState().filesModified],
//
// TS 的 `filesModified` 是 **`Set<string>`**（`src/agent/evidence.ts:33`），
// 插入序。Go 侧取 `session.FileIndex`（「保持插入序的文件记录表」，
// `internal/session/state.go`）的 `ModifiedByMe` 条目——同源同序。

// newSessionLoop 构造一个带会话状态的 Loop（用于接线测试）。
//
// 不用 `newArtifactLoop`：那条路径不装 `State`，而本文件测的正是
// 「State → CallParams」这段接线。
func newSessionLoop(t *testing.T) *Loop {
	t.Helper()
	l := &Loop{State: session.New("sess-modfiles")}
	return l
}

// trackWrite 模拟 `observeToolResult` 的写工具分支（对账
// `internal/agent/loop.go` 的 `case "write_file", "edit_file", "hash_edit"`）。
func trackWrite(l *Loop, path string) {
	l.State.TrackFileModified(path)
}

// TestSessionModifiedFilesCollectsWriteTools —— V1/V7：写工具入列且保插入序。
//
// **顺序是语义的一部分**：`git add -- <files>` 的参数序会进入提交输出。
// TS 的 `Set` 是插入序，FileIndex 也是插入序——排序会偏离。（变异 M3）
func TestSessionModifiedFilesCollectsWriteTools(t *testing.T) {
	l := newSessionLoop(t)
	trackWrite(l, "zz_b.ts")
	trackWrite(l, "zz_a.ts")

	p := l.buildToolCallParams(toolCall{id: "t1", name: "write_file"})
	got := p.SessionModifiedFiles
	want := []string{"zz_b.ts", "zz_a.ts"} // 插入序，**不是**字典序

	if len(got) != len(want) {
		t.Fatalf("SessionModifiedFiles 长度不符：got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SessionModifiedFiles 顺序不符（必须是插入序）：got=%v want=%v", got, want)
		}
	}
}

// TestSessionModifiedFilesExcludesReads —— V2：只读的文件不入列。
//
// `ModifiedByMe` 是判据（对账 `session/state.go` 的 `TrackFileRead`
// 「保留既有的 ModifiedByMe」——读不撤销改的标记）。（变异 M2）
func TestSessionModifiedFilesExcludesReads(t *testing.T) {
	l := newSessionLoop(t)
	l.State.TrackFileRead("zz_read_only.ts", "")
	trackWrite(l, "zz_written.ts")
	// 既读又写 → 应入列（ModifiedByMe 为 true）。
	l.State.TrackFileRead("zz_both.ts", "")
	trackWrite(l, "zz_both.ts")

	p := l.buildToolCallParams(toolCall{id: "t1", name: "write_file"})
	got := p.SessionModifiedFiles

	if listHas(got, "zz_read_only.ts") {
		t.Errorf("只读文件不该入列：%v", got)
	}
	if !listHas(got, "zz_written.ts") {
		t.Errorf("写过的文件应入列：%v", got)
	}
	if !listHas(got, "zz_both.ts") {
		t.Errorf("既读又写的文件应入列（ManagedByMe 为 true）：%v", got)
	}
}

// TestSessionModifiedFilesNilStateSafe —— V4：无会话状态时不 panic。
//
// 对账 Go 的「最小可跑路径」：headless / 部分测试装配下 `State == nil`
// （`loop.go` 的 `State` 字段注释：「nil 时跳过状态更新」）。
func TestSessionModifiedFilesNilStateSafe(t *testing.T) {
	l := &Loop{} // State == nil
	p := l.buildToolCallParams(toolCall{id: "t1", name: "write_file"})
	if len(p.SessionModifiedFiles) != 0 {
		t.Errorf("State 为 nil 时应为空，实得 %v", p.SessionModifiedFiles)
	}
}

// TestSessionModifiedFilesApplyPatchTargets —— V3：apply_patch 的多目标路径入列。
//
// 对账 `observeToolResult` 的 `case "apply_patch"` 分支：目标路径取自
// `prompt.ExtractPatchTargetPaths(diff)`（一个 patch 可改多个文件）。
func TestSessionModifiedFilesApplyPatchTargets(t *testing.T) {
	l := newSessionLoop(t)
	// 模拟 apply_patch 分支对每个目标调 TrackFileModified。
	for _, rel := range []string{"zz_p1.txt", "zz_p2.txt", "zz_p3.txt"} {
		l.State.TrackFileModified(rel)
	}

	p := l.buildToolCallParams(toolCall{id: "t1", name: "apply_patch"})
	for _, want := range []string{"zz_p1.txt", "zz_p2.txt", "zz_p3.txt"} {
		if !listHas(p.SessionModifiedFiles, want) {
			t.Errorf("apply_patch 目标 %s 应入列：%v", want, p.SessionModifiedFiles)
		}
	}
}

func listHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
