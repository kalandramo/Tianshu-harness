package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

func msg(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}

func toolMsg(id, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: "tool", ToolCallID: id, Content: &c}
}

func asstWithCalls(content string, calls ...session.OaiToolCall) session.OaiMessage {
	m := msg("assistant", content)
	m.ToolCalls = calls
	return m
}

func call(id, name, args string) session.OaiToolCall {
	return session.OaiToolCall{ID: id, Type: "function", Function: &session.OaiFunction{Name: name, Arguments: args}}
}

// ── 邻接判定 ──

func TestAdjacencyCleanWhenProperlyPaired(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("system", "s"),
		msg("user", "u"),
		asstWithCalls("", call("c1", "read_file", `{"file_path":"a.ts"}`)),
		toolMsg("c1", "ok"),
	}
	if !isToolAdjacencyCleanOai(msgs) {
		t.Error("正确配对应判定为干净")
	}
}

func TestAdjacencyRejectsStrayTool(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("user", "u"),
		toolMsg("c1", "orphan"), // 游离 tool
	}
	if isToolAdjacencyCleanOai(msgs) {
		t.Error("游离 tool 应判定为不干净")
	}
}

func TestAdjacencyRejectsMissingResult(t *testing.T) {
	msgs := []session.OaiMessage{
		asstWithCalls("", call("c1", "x", "{}"), call("c2", "y", "{}")),
		toolMsg("c1", "only one"),
	}
	if isToolAdjacencyCleanOai(msgs) {
		t.Error("缺结果应判定为不干净")
	}
}

func TestAdjacencyRejectsForeignID(t *testing.T) {
	msgs := []session.OaiMessage{
		asstWithCalls("", call("c1", "x", "{}")),
		toolMsg("FOREIGN", "not mine"),
	}
	if isToolAdjacencyCleanOai(msgs) {
		t.Error("外来 id 应判定为不干净")
	}
}

func TestAdjacencyRejectsDuplicateID(t *testing.T) {
	msgs := []session.OaiMessage{
		asstWithCalls("", call("c1", "x", "{}")),
		toolMsg("c1", "first"),
		toolMsg("c1", "dup"),
	}
	if isToolAdjacencyCleanOai(msgs) {
		t.Error("重复 id 应判定为不干净")
	}
}

// TestAdjacencyRejectsLateResult —— **关键场景**：结果存在但位置错（隔了 user）。
//
// 对账 TS 注释：id 存在性检查必要但不充分——迟到的结果有匹配 id 但邻接破坏。
func TestAdjacencyRejectsLateResult(t *testing.T) {
	msgs := []session.OaiMessage{
		asstWithCalls("", call("c1", "x", "{}")),
		msg("user", "intervening"),
		toolMsg("c1", "late result"),
	}
	if isToolAdjacencyCleanOai(msgs) {
		t.Error("错位结果应判定为不干净（供应商真实要求是邻接，不只是 id 存在）")
	}
}

// ── 修复路径 ──

// TestPreflightNoopWhenClean —— 干净时不得改动（前缀缓存不被动）。
func TestPreflightNoopWhenClean(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("system", "s"),
		msg("user", "u"),
		asstWithCalls("", call("c1", "read_file", `{"file_path":"a.ts"}`)),
		toolMsg("c1", "ok"),
	}
	rep := RunResumePreflightOai(msgs, nil)
	if rep.Repaired {
		t.Error("干净序列不应被标记为已修复")
	}
	if rep.SyntheticResultsInserted != 0 {
		t.Error("干净序列不应合成")
	}
}

// TestPreflightSynthesizesMissingResult —— 缺结果 → 合成占位。
func TestPreflightSynthesizesMissingResult(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("user", "u"),
		asstWithCalls("", call("c1", "read_file", `{"file_path":"a.ts"}`)),
		// 无 tool 结果
	}
	rep := RunResumePreflightOai(msgs, nil)
	if !rep.Repaired {
		t.Fatal("应被修复")
	}
	if rep.SyntheticResultsInserted != 1 {
		t.Errorf("应合成 1 条，实得 %d", rep.SyntheticResultsInserted)
	}
	if !isToolAdjacencyCleanOai(rep.Messages) {
		t.Error("修复后应邻接干净")
	}
	// 合成内容含归因文案（防模型误判工具坏了）。
	last := rep.Messages[len(rep.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Fatalf("末尾应是 c1 的 tool 结果，实得 %+v", last)
	}
	if !strings.Contains(*last.Content, WriteRecoveryMarker) {
		t.Error("合成内容应含恢复标记")
	}
	if !strings.Contains(*last.Content, "【归因】") {
		t.Error("合成内容应含归因段（防模型误判工具故障）")
	}
}

// TestPreflightPullsLateResultBack —— 错位结果被**拉回**（不丢弃）。
func TestPreflightPullsLateResultBack(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("user", "u"),
		asstWithCalls("", call("c1", "read_file", `{"file_path":"a.ts"}`)),
		msg("user", "intervening"),
		toolMsg("c1", "LATE-CONTENT"),
	}
	rep := RunResumePreflightOai(msgs, nil)
	if !rep.Repaired {
		t.Fatal("应被修复")
	}
	// 结果应被拉回 assistant 之后，且**原文保留**（不是合成）。
	if !isToolAdjacencyCleanOai(rep.Messages) {
		t.Fatal("修复后应邻接干净")
	}
	found := false
	for i, m := range rep.Messages {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			found = true
			if *m.Content != "LATE-CONTENT" {
				t.Errorf("应保留原文而非合成，实得 %q", *m.Content)
			}
			// 必须紧跟 assistant。
			if i == 0 || rep.Messages[i-1].Role != "assistant" {
				t.Error("拉回后应紧跟 assistant")
			}
		}
	}
	if !found {
		t.Error("结果应被保留（拉回而非丢弃）")
	}
	if rep.SyntheticResultsInserted != 0 {
		t.Errorf("拉回不应算合成，实得 %d", rep.SyntheticResultsInserted)
	}
}

// TestPreflightDropsDuplicateResult —— 多余的同 id 结果被丢弃。
func TestPreflightDropsDuplicateResult(t *testing.T) {
	msgs := []session.OaiMessage{
		msg("user", "u"),
		asstWithCalls("", call("c1", "x", "{}")),
		toolMsg("c1", "first"),
		toolMsg("c1", "dup"),
	}
	rep := RunResumePreflightOai(msgs, nil)
	if !isToolAdjacencyCleanOai(rep.Messages) {
		t.Fatal("修复后应邻接干净")
	}
	count := 0
	for _, m := range rep.Messages {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("重复结果应被丢弃到只剩 1 条，实得 %d", count)
	}
}

// TestPreflightNormalizesEmptyToolCalls —— `tool_calls: []` 被清除。
//
// 对账 TS：恢复的会话若含空数组，会被原样送回，供应商在生成开始前拒绝。
func TestPreflightNormalizesEmptyToolCalls(t *testing.T) {
	m := msg("assistant", "text")
	m.ToolCalls = []session.OaiToolCall{} // 空数组
	msgs := []session.OaiMessage{msg("user", "u"), m}
	rep := RunResumePreflightOai(msgs, nil)
	if !rep.Repaired {
		t.Error("空 tool_calls 数组应触发规范化")
	}
	for _, mm := range rep.Messages {
		if mm.Role == "assistant" && len(mm.ToolCalls) != 0 {
			t.Errorf("空数组应被清除，实得 %d", len(mm.ToolCalls))
		}
	}
}

// ── 恢复文案分支 ──

func TestRecoveryContentWriteToolNoEvidence(t *testing.T) {
	got := FormatWriteRecoveryContent("write_file", "src/a.ts", nil, 0)
	if !strings.HasPrefix(got, WriteRecoveryMarker) {
		t.Errorf("应以标记开头，实得 %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, "src/a.ts") || !strings.Contains(got, "read_file") {
		t.Error("应含目标路径与 read_file 指引")
	}
}

func TestRecoveryContentEvidenceExists(t *testing.T) {
	got := FormatWriteRecoveryContent("write_file", "src/a.ts", &WriteEvidence{Exists: true, Bytes: 2048}, 0)
	if !strings.HasPrefix(got, "[auto-recovered]") {
		t.Errorf("证据确认成功应平静确认优先，实得 %q", got[:min(40, len(got))])
	}
	if !strings.Contains(got, "2.0KB") {
		t.Error("应含格式化后的大小")
	}
	// marker 以括注保留（计数不能漏）。
	if !strings.Contains(got, WriteRecoveryMarker) {
		t.Error("marker 必须保留（计数依赖它）")
	}
}

func TestRecoveryContentEvidenceMissing(t *testing.T) {
	got := FormatWriteRecoveryContent("edit_file", "src/a.ts", &WriteEvidence{Exists: false}, 0)
	if !strings.Contains(got, "可安全重试") {
		t.Errorf("证据确认不存在应提示可安全重试，实得 %q", got)
	}
}

func TestRecoveryContentNonWriteTool(t *testing.T) {
	got := FormatWriteRecoveryContent("read_file", "src/a.ts", nil, 0)
	if !strings.Contains(got, "该工具可能已经成功执行") {
		t.Errorf("非写类工具应走通用文案，实得 %q", got)
	}
}

func TestRecoveryContentRepeatEscalation(t *testing.T) {
	base := FormatWriteRecoveryContent("write_file", "a.ts", nil, 0)
	if strings.Contains(base, "【重复发生】") {
		t.Error("首次恢复不应含重复段")
	}
	repeated := FormatWriteRecoveryContent("write_file", "a.ts", nil, 2)
	if !strings.Contains(repeated, "【重复发生】") {
		t.Error(">=2 次应含重复升级段")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		512:             "512B",
		2048:            "2.0KB",
		1024 * 1024:     "1.0MB",
		3 * 1024 * 1024: "3.0MB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d)：期望 %q，实得 %q", in, want, got)
		}
	}
}

func TestExtractTargetPath(t *testing.T) {
	// 对象形态。
	if got := ExtractTargetPath(map[string]any{"file_path": "a.ts"}); got != "a.ts" {
		t.Errorf("对象 file_path：实得 %q", got)
	}
	if got := ExtractTargetPath(map[string]any{"path": "b.ts"}); got != "b.ts" {
		t.Errorf("对象 path：实得 %q", got)
	}
	// JSON 字符串形态（指针坍缩后）。
	if got := ExtractTargetPath(`{"file_path":"c.ts"}`); got != "c.ts" {
		t.Errorf("字符串形态：实得 %q", got)
	}
	// 非法输入。
	if got := ExtractTargetPath("not json"); got != "" {
		t.Errorf("非法 JSON 应返回空，实得 %q", got)
	}
	if got := ExtractTargetPath(42); got != "" {
		t.Errorf("非字符串/对象应返回空，实得 %q", got)
	}
}

func TestCountPriorRecoveries(t *testing.T) {
	msgs := []session.OaiMessage{
		toolMsg("c1", "normal result"),
		toolMsg("c2", WriteRecoveryMarker+"——blah"),
		toolMsg("c3", "（合成占位："+WriteRecoveryMarker+"，已由磁盘证据确认写入生效。）"),
		msg("user", "not a tool msg"),
	}
	if got := CountPriorRecoveries(msgs); got != 2 {
		t.Errorf("应数出 2 条（含括注形态），实得 %d", got)
	}
}

// ── 磁盘探测 ──

func TestWriteEvidenceProbe(t *testing.T) {
	dir := t.TempDir()
	// 存在的文件。
	existing := filepath.Join(dir, "exists.ts")
	if err := os.WriteFile(existing, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := CreateWriteEvidenceProbe(dir)

	ev := probe("write_file", map[string]any{"file_path": "exists.ts"})
	if ev == nil || !ev.Exists || ev.Bytes != 7 {
		t.Errorf("存在文件应返回存在证据，实得 %+v", ev)
	}
	ev = probe("write_file", map[string]any{"file_path": "missing.ts"})
	if ev == nil || ev.Exists {
		t.Errorf("缺失文件应返回不存在证据，实得 %+v", ev)
	}
	// 非写类工具 → nil（不探测）。
	if ev := probe("read_file", map[string]any{"file_path": "exists.ts"}); ev != nil {
		t.Errorf("非写类工具应返回 nil，实得 %+v", ev)
	}
}

// TestPreflightWithProbeUsesEvidence —— 探测到的证据进入合成文案。
func TestPreflightWithProbeUsesEvidence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "written.ts"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := CreateWriteEvidenceProbe(dir)
	msgs := []session.OaiMessage{
		msg("user", "u"),
		asstWithCalls("", call("c1", "write_file", `{"file_path":"written.ts"}`)),
	}
	rep := RunResumePreflightOai(msgs, probe)
	last := rep.Messages[len(rep.Messages)-1]
	if !strings.Contains(*last.Content, "[auto-recovered]") {
		t.Errorf("有磁盘证据时应走平静确认分支，实得 %q", *last.Content)
	}
}
