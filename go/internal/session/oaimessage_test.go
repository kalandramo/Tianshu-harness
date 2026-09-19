package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// orphanOracle 是 TS SessionPersist.loadOai 的真实产出。
// 生成命令：npx tsx go/testdata/orphan/gen-oracle.ts
type orphanOracle struct {
	Cases map[string]struct {
		Note string `json:"note"`
		// Rows 保留**原始 JSON 形态**（map）——结构体反序列化无法区分
		// 「content 键缺失」与「content 为 null」，而 isOaiMessage 恰恰
		// 依赖这个区分。
		Rows   []map[string]any `json:"rows"`
		Loaded []struct {
			Role          string   `json:"role"`
			Content       *string  `json:"content"`
			ToolCallIDs   []string `json:"toolCallIds"`
			ToolCallNames []string `json:"toolCallNames"`
			ToolCallID    *string  `json:"toolCallId"`
		} `json:"loaded"`
		HasReminder bool    `json:"hasReminder"`
		Reminder    *string `json:"reminder"`
	} `json:"cases"`
}

func loadOrphanOracle(t *testing.T) orphanOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "orphan", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/orphan/gen-oracle.ts", path, err)
	}
	var o orphanOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// toOai 把 oracle 的原始 row（map）转成 Go 结构。
func toOai(r map[string]any) OaiMessage {
	m := OaiMessage{}
	m.Role, _ = r["role"].(string)
	if c, ok := r["content"].(string); ok {
		m.Content = &c
	}
	m.ToolCallID, _ = r["tool_call_id"].(string)
	if tcs, ok := r["tool_calls"].([]any); ok {
		m.ToolCalls = []OaiToolCall{}
		for _, t := range tcs {
			tcm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tcm["id"].(string)
			typ, _ := tcm["type"].(string)
			out := OaiToolCall{ID: id, Type: typ}
			if fn, ok := tcm["function"].(map[string]any); ok {
				name, _ := fn["name"].(string)
				args, _ := fn["arguments"].(string)
				out.Function = &OaiFunction{Name: name, Arguments: args}
			}
			m.ToolCalls = append(m.ToolCalls, out)
		}
	}
	return m
}

// TestRepairOrphanOracleParity —— 逐用例对账 TS 的 loadOai 产出。
func TestRepairOrphanOracleParity(t *testing.T) {
	o := loadOrphanOracle(t)
	checked := 0
	for name, c := range o.Cases {
		checked++
		t.Run(name, func(t *testing.T) {
			// 复刻 loadOai 的**完整**处理链：
			//   isOaiMessage 过滤 → normalize → repair
			//
			// **过滤层必须复刻**：assistant 若缺 content 键（如
			// {role:'assistant',tool_calls:[]}），isOaiMessage 判 false，
			// 整行在进入 normalize 之前就被跳过。漏掉这层会让
			// emptyToolCallsNullContent 用例对不上。
			filtered := make([]OaiMessage, 0, len(c.Rows))
			for _, r := range c.Rows {
				if !IsOaiMessage(r) { // 传原始 map，保留键缺失信息
					continue
				}
				filtered = append(filtered, toOai(r))
			}
			normalized := NormalizeOaiMessages(filtered)
			rep := RepairOrphanToolCalls(normalized)

			// 若有孤儿，TS 会插入 system-reminder 到**首位**
			got := rep.Messages
			if rep.HadOrphans {
				reminder := OrphanReminderGeneric
				if rep.StrippedWriteTool {
					reminder = OrphanReminderWriteTool
				}
				got = append([]OaiMessage{{Role: "system", Content: &reminder}}, got...)
			}

			// 数量
			if len(got) != len(c.Loaded) {
				t.Fatalf("消息数不符：Go=%d TS=%d\n  Go =%+v", len(got), len(c.Loaded), got)
			}
			// 逐条对账
			for i, want := range c.Loaded {
				g := got[i]
				if g.Role != want.Role {
					t.Errorf("[%d] role：Go=%q TS=%q", i, g.Role, want.Role)
				}
				// content 对账（nil vs 空串要区分）
				switch {
				case want.Content == nil && g.Content != nil:
					t.Errorf("[%d] content：Go=%q TS=nil", i, *g.Content)
				case want.Content != nil && g.Content == nil:
					t.Errorf("[%d] content：Go=nil TS=%q", i, *want.Content)
				case want.Content != nil && g.Content != nil && *g.Content != *want.Content:
					t.Errorf("[%d] content：Go=%q TS=%q", i, *g.Content, *want.Content)
				}
				// toolCallIds
				if want.ToolCallIDs == nil {
					if g.ToolCalls != nil {
						t.Errorf("[%d] tool_calls：Go 有 %d 个，TS 为 nil", i, len(g.ToolCalls))
					}
				} else {
					if len(g.ToolCalls) != len(want.ToolCallIDs) {
						t.Errorf("[%d] tool_calls 数：Go=%d TS=%d", i, len(g.ToolCalls), len(want.ToolCallIDs))
					} else {
						for j, id := range want.ToolCallIDs {
							if g.ToolCalls[j].ID != id {
								t.Errorf("[%d] tool_calls[%d].id：Go=%q TS=%q", i, j, g.ToolCalls[j].ID, id)
							}
						}
					}
				}
				// toolCallId
				switch {
				case want.ToolCallID == nil && g.ToolCallID != "":
					t.Errorf("[%d] tool_call_id：Go=%q TS=nil", i, g.ToolCallID)
				case want.ToolCallID != nil && g.ToolCallID != *want.ToolCallID:
					t.Errorf("[%d] tool_call_id：Go=%q TS=%q", i, g.ToolCallID, *want.ToolCallID)
				}
			}
			// 警告文案逐字对账
			if c.Reminder != nil {
				if len(got) == 0 || got[0].Content == nil || *got[0].Content != *c.Reminder {
					var gotR string
					if len(got) > 0 && got[0].Content != nil {
						gotR = *got[0].Content
					}
					t.Errorf("警告文案不符\n  Go =%q\n  TS =%q", gotR, *c.Reminder)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestRepairOrphanWriteToolWarning —— 写类工具走非破坏性文案。
func TestRepairOrphanWriteToolWarning(t *testing.T) {
	empty := ""
	msgs := []OaiMessage{
		{Role: "assistant", Content: &empty, ToolCalls: []OaiToolCall{
			{ID: "w1", Type: "function", Function: &OaiFunction{Name: "write_file"}},
		}},
	}
	rep := RepairOrphanToolCalls(msgs)
	if !rep.HadOrphans {
		t.Fatal("应检出孤儿")
	}
	if !rep.StrippedWriteTool {
		t.Error("write_file 应标记为写类工具")
	}
}

// TestRepairOrphanNonWriteToolWarning —— 非写类工具走通用文案。
func TestRepairOrphanNonWriteToolWarning(t *testing.T) {
	empty := ""
	msgs := []OaiMessage{
		{Role: "assistant", Content: &empty, ToolCalls: []OaiToolCall{
			{ID: "r1", Type: "function", Function: &OaiFunction{Name: "read_file"}},
		}},
	}
	rep := RepairOrphanToolCalls(msgs)
	if !rep.HadOrphans {
		t.Fatal("应检出孤儿")
	}
	if rep.StrippedWriteTool {
		t.Error("read_file 不应标记为写类工具")
	}
}

// TestRepairAllWriteToolNames —— 五个写类工具全部覆盖。
func TestRepairAllWriteToolNames(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "hash_edit", "ast_edit", "apply_patch"} {
		if !WriteToolNames[name] {
			t.Errorf("%s 应在写类工具集合里", name)
		}
	}
	// 反例
	for _, name := range []string{"read_file", "grep", "glob", "bash", "todo"} {
		if WriteToolNames[name] {
			t.Errorf("%s 不应在写类工具集合里", name)
		}
	}
}

// TestNormalizeEmptyToolCallsRemoved —— 空 tool_calls 数组被移除。
func TestNormalizeEmptyToolCallsRemoved(t *testing.T) {
	content := "text only"
	m := OaiMessage{Role: "assistant", Content: &content, ToolCalls: []OaiToolCall{}}
	out, changed := NormalizeOaiMessage(m)
	if !changed {
		t.Error("空 tool_calls 应被标记为改动")
	}
	if out.ToolCalls != nil {
		t.Errorf("空 tool_calls 应被移除，得到 %+v", out.ToolCalls)
	}
	if out.Content == nil || *out.Content != "text only" {
		t.Error("content 应保留")
	}
	// 非 assistant 不受影响
	m2 := OaiMessage{Role: "user", ToolCalls: []OaiToolCall{}}
	if _, ch := NormalizeOaiMessage(m2); ch {
		t.Error("非 assistant 不应被改动")
	}
	// 有 tool_calls 的不受影响
	m3 := OaiMessage{Role: "assistant", ToolCalls: []OaiToolCall{{ID: "x"}}}
	if _, ch := NormalizeOaiMessage(m3); ch {
		t.Error("非空 tool_calls 不应被改动")
	}
}

// TestNormalizeNullContentFilled —— content 缺失时补空串。
func TestNormalizeNullContentFilled(t *testing.T) {
	m := OaiMessage{Role: "assistant", ToolCalls: []OaiToolCall{}}
	out, changed := NormalizeOaiMessage(m)
	if !changed {
		t.Fatal("应标记改动")
	}
	if out.Content == nil || *out.Content != "" {
		t.Errorf("content 应补空串，得到 %v", out.Content)
	}
}

// TestRepairOrphanResultDropped —— 孤儿 tool 结果被丢弃。
func TestRepairOrphanResultDropped(t *testing.T) {
	msgs := []OaiMessage{
		{Role: "user", Content: strPtr("hi")},
		{Role: "tool", ToolCallID: "ghost", Content: strPtr("stale")},
		{Role: "user", Content: strPtr("next")},
	}
	rep := RepairOrphanToolCalls(msgs)
	if !rep.HadOrphans {
		t.Fatal("应检出孤儿结果")
	}
	if len(rep.Messages) != 2 {
		t.Errorf("应剩 2 条，得到 %d", len(rep.Messages))
	}
	for _, m := range rep.Messages {
		if m.Role == "tool" {
			t.Error("孤儿 tool 结果应被丢弃")
		}
	}
}

// TestRepairMixedKeepsValid —— 部分孤儿时保留有效 tool_call。
func TestRepairMixedKeepsValid(t *testing.T) {
	empty := ""
	msgs := []OaiMessage{
		{Role: "assistant", Content: &empty, ToolCalls: []OaiToolCall{
			{ID: "c1", Function: &OaiFunction{Name: "read_file"}},
			{ID: "c2", Function: &OaiFunction{Name: "grep"}},
		}},
		{Role: "tool", ToolCallID: "c1", Content: strPtr("ok")},
	}
	rep := RepairOrphanToolCalls(msgs)
	if !rep.HadOrphans {
		t.Fatal("应检出孤儿")
	}
	if len(rep.Messages) != 2 {
		t.Fatalf("应剩 2 条，得到 %d", len(rep.Messages))
	}
	// assistant 的 tool_calls 应只剩 c1
	if len(rep.Messages[0].ToolCalls) != 1 || rep.Messages[0].ToolCalls[0].ID != "c1" {
		t.Errorf("应只保留 c1，得到 %+v", rep.Messages[0].ToolCalls)
	}
	// 混合场景里 c2（grep，非写类）被剔除
	if rep.StrippedWriteTool {
		t.Error("grep 不是写类工具")
	}
}

// TestRepairNoOrphansUnchanged —— 无孤儿时消息不变、无标记。
func TestRepairNoOrphansUnchanged(t *testing.T) {
	empty := ""
	msgs := []OaiMessage{
		{Role: "assistant", Content: &empty, ToolCalls: []OaiToolCall{
			{ID: "c1", Function: &OaiFunction{Name: "read_file"}},
		}},
		{Role: "tool", ToolCallID: "c1", Content: strPtr("ok")},
	}
	rep := RepairOrphanToolCalls(msgs)
	if rep.HadOrphans {
		t.Error("无孤儿不应标记")
	}
	if len(rep.Messages) != 2 {
		t.Errorf("消息数应不变，得到 %d", len(rep.Messages))
	}
}

// TestRepairWriteToolWithContentKeepsMessage —— 写类工具但 assistant 有文本内容时保留消息。
func TestRepairWriteToolWithContentKeepsMessage(t *testing.T) {
	msgs := []OaiMessage{
		{Role: "assistant", Content: strPtr("writing now"), ToolCalls: []OaiToolCall{
			{ID: "w1", Function: &OaiFunction{Name: "write_file"}},
		}},
	}
	rep := RepairOrphanToolCalls(msgs)
	if !rep.HadOrphans {
		t.Fatal("应检出孤儿")
	}
	if !rep.StrippedWriteTool {
		t.Error("应标记写类工具")
	}
	// content 非空 → 消息保留，只剔除 tool_calls
	if len(rep.Messages) != 1 {
		t.Fatalf("消息应保留，得到 %d 条", len(rep.Messages))
	}
	if rep.Messages[0].ToolCalls != nil {
		t.Errorf("tool_calls 应被剔除，得到 %+v", rep.Messages[0].ToolCalls)
	}
	if rep.Messages[0].Content == nil || *rep.Messages[0].Content != "writing now" {
		t.Error("content 应保留")
	}
}

func strPtr(s string) *string { return &s }
