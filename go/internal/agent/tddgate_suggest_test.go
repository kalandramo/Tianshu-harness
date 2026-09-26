package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// tddgate_suggest_test.go —— TDD gate 的 suggest 提示通道（第七十三刀）。
//
// # 对账 TS（`tool-pipeline.ts:900-905, 1748-1750`）
//
// TS 的 suggest 文案经 `tddSuggestNote` 附加到**工具结果 content 尾部**：
//
//	if (tddSuggestNote && !harnessResult.isError) {
//	  finalContent = `${finalContent}\n\n[TDD] ${tddSuggestNote}`
//	}
//
// **只在工具成功时贴**（失败的编辑结果本身已是反馈）。
// **追加在结果尾部** = 对话历史末尾 → **冻结前缀不变**（缓存友好，TS 注释明说）。
//
// # 附注的触发区域（TS 有意的降噪）
//
//	decision.action === 'suggest' && decision.message
//	  && (gateState.hasFailedTests || gateState.editsSinceLastTest >= threshold)
//
// 即**只在「enforce 会拦」的区域才附注**——探索窗口（<threshold）与
// 测试文件 RED 步骤保持安静，避免每次编辑都贴尾巴。
//
// # 为什么需要「成功才贴」的反面对照
//
// 若实现成「无条件贴」，失败路径也会带 TDD 文案——但 TS 明确排除。
// 反之若实现成「从不贴」，阈值区域的正例会红。两个方向都要能红。

// registerFileTools 把真实文件工具注册进空 registry（让 edit/write 能成功执行）。
func registerFileTools(r *tools.Registry, cwd string) {
	r.Register(tools.WriteFile(cwd, nil))
	r.Register(tools.EditFile(cwd, nil))
}

// writeFileForTest 预置一个文件（工具执行的前置条件）。
func writeFileForTest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("预置 %s 失败: %v", name, err)
	}
}

// tddSuggestResult 跑一次 executeTool，返回结果的 content 与 isError。
func tddSuggestResult(t *testing.T, tddEnv string, setup func(*Loop), toolName, target string) (string, bool) {
	t.Helper()
	l := &Loop{}
	l.cfg.TddGateEnv = tddEnv
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()

	if setup != nil {
		setup(l)
	}
	input := map[string]any{}
	if target != "" {
		input["file_path"] = target
	}
	res := l.executeTool(context.Background(), toolCall{name: toolName, input: input})
	return res.Content, res.IsError
}

// hasTddNote 报告 content 是否带 suggest 附注（`\n\n[TDD] ` 形式）。
func hasTddNote(content string) bool {
	return strings.Contains(content, "\n\n[TDD] ")
}

// TestTddSuggestNoteAppendedAtThreshold —— **核心**：suggest 模式下，达阈值
// 区域的编辑结果尾部应带 `[TDD]` 附注。
//
// 用未注册的工具（放行后报「工具未找到」）——但那条路径 `IsError=true`，
// 而附注**只在成功时贴**。故本测试用一个**真实注册的工具**：`read_file`
// 是读工具（不经 TDD 门），不行；用 `write_file` 会真写文件。
//
// **改用**：直接验证「门链在放行 suggest 时把 note 带到结果上」——
// 用一个可成功执行的 edit 工具。
func TestTddSuggestNoteAppendedAtThreshold(t *testing.T) {
	dir := t.TempDir()
	writeFileForTest(t, dir, "d.ts", "// orig\n")

	l := &Loop{}
	l.cfg.TddGateEnv = "suggest"
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	registerFileTools(l.registry, dir)

	threeEditsReadTest(l)

	res := l.executeTool(context.Background(), toolCall{
		name: "write_file",
		input: map[string]any{
			"file_path": dir + "/d.ts",
			"content":   "// edited\n",
		},
	})
	if res.IsError {
		t.Fatalf("suggest 模式不该拦，实得 IsError=true content=%q", res.Content)
	}
	if !hasTddNote(res.Content) {
		t.Errorf("达阈值区域的成功编辑应带 `[TDD]` 附注，实得 content=%q", res.Content)
	}
	// 附注必须在**尾部**（缓存友好：冻结前缀不变）。
	idx := strings.Index(res.Content, "\n\n[TDD] ")
	if idx < 0 || idx+len("\n\n[TDD] ") >= len(res.Content) {
		t.Errorf("`[TDD]` 附注应在结果尾部且带文案，实得 content=%q", res.Content)
	}
}

// TestTddSuggestNoteSilentInExplorationWindow —— **降噪**：探索窗口
// （edits < threshold）不附注。
//
// TS 注释：「探索窗口（<threshold）与测试文件 RED 步骤保持安静，
// 避免每次编辑都贴尾巴。」
func TestTddSuggestNoteSilentInExplorationWindow(t *testing.T) {
	dir := t.TempDir()
	writeFileForTest(t, dir, "d.ts", "// orig\n")

	l := &Loop{}
	l.cfg.TddGateEnv = "suggest"
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	registerFileTools(l.registry, dir)

	// 只 1 次编辑（< threshold=3）
	l.State.TrackFileModified("src/a.ts")
	l.evidence.TrackFileModified("src/a.ts")
	l.State.TrackFileRead("src/a.test.ts", "")

	res := l.executeTool(context.Background(), toolCall{
		name: "write_file",
		input: map[string]any{
			"file_path": dir + "/d.ts",
			"content":   "// edited\n",
		},
	})
	if hasTddNote(res.Content) {
		t.Errorf("探索窗口（1 次编辑 < 阈值 3）不该附注，实得 content=%q", res.Content)
	}
}

// TestTddSuggestNoteNotOnFailure —— **反面对照**：失败的编辑结果不带附注
// （TS：`!harnessResult.isError`）。
func TestTddSuggestNoteNotOnFailure(t *testing.T) {
	dir := t.TempDir()
	// **不预置** d.ts → write_file 到不存在的父目录？改用 edit_file 改不存在的文件。
	l := &Loop{}
	l.cfg.TddGateEnv = "suggest"
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	registerFileTools(l.registry, dir)

	threeEditsReadTest(l)

	// edit_file 目标不存在 → 工具失败（IsError=true）
	res := l.executeTool(context.Background(), toolCall{
		name: "edit_file",
		input: map[string]any{
			"file_path":  dir + "/nonexistent.ts",
			"old_string": "x",
			"new_string": "y",
		},
	})
	if !res.IsError {
		t.Fatalf("改不存在的文件应失败（本测试前提），实得 IsError=false content=%q", res.Content)
	}
	if hasTddNote(res.Content) {
		t.Errorf("失败的编辑结果不该带 TDD 附注（TS: `!harnessResult.isError`），实得 content=%q", res.Content)
	}
}

// TestTddSuggestNoteNotOnEnforceBlock —— enforce 拦截走原路径（block 文案），
// 不是 suggest 附注形式。
func TestTddSuggestNoteNotOnEnforceBlock(t *testing.T) {
	dir := t.TempDir()
	writeFileForTest(t, dir, "d.ts", "// orig\n")

	l := &Loop{}
	l.cfg.TddGateEnv = "enforce"
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	registerFileTools(l.registry, dir)

	threeEditsReadTest(l)

	res := l.executeTool(context.Background(), toolCall{
		name: "write_file",
		input: map[string]any{
			"file_path": dir + "/d.ts",
			"content":   "// edited\n",
		},
	})
	if !res.IsError {
		t.Fatal("enforce 达阈值应拦（本测试前提）")
	}
	if !isTddBlockMessage(res.Content) {
		t.Errorf("enforce 拦截应是 block 文案，实得 %q", res.Content)
	}
	if hasTddNote(res.Content) {
		t.Error("enforce 拦截不该用 suggest 的附注形式（两者是不同路径）")
	}
}

// TestTddSuggestNoteNotInSuggestUnderThresholdWithFailedTests —— 有失败测试时
// **即使未达阈值也附注**（TS: `hasFailedTests || edits >= threshold`）。
func TestTddSuggestNoteOnFailedTestsUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	writeFileForTest(t, dir, "d.ts", "// orig\n")

	l := &Loop{}
	l.cfg.TddGateEnv = "suggest"
	l.cfg.ApprovalMode = "dangerously-skip-permissions"
	l.cfg.Cwd = dir
	l.State = session.New("tddgate-suggest-test")
	l.evidence = newEvidenceTracker()
	l.registry = tools.NewRegistry()
	registerFileTools(l.registry, dir)

	// 1 次编辑（< 阈值）+ 一次失败验证
	l.State.TrackFileModified("src/a.ts")
	l.evidence.TrackFileModified("src/a.ts")
	l.evidence.TrackVerification("failed")
	l.State.TrackFileRead("src/a.test.ts", "")

	res := l.executeTool(context.Background(), toolCall{
		name: "write_file",
		input: map[string]any{
			"file_path": dir + "/d.ts",
			"content":   "// edited\n",
		},
	})
	if !hasTddNote(res.Content) {
		t.Errorf("有失败测试时应附注（即使未达阈值），实得 content=%q", res.Content)
	}
}
