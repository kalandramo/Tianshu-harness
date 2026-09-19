package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// persistOracle 是 TS SessionPersist.loadOai 的真实产出。
// 生成命令：npx tsx go/testdata/persist/gen-oracle.ts
type persistOracle struct {
	Cases map[string]struct {
		Note   string   `json:"note"`
		Rows   []string `json:"rows"`
		Loaded []struct {
			Role          string   `json:"role"`
			Content       *string  `json:"content"`
			ToolCallIDs   []string `json:"toolCallIds"`
			ToolCallNames []string `json:"toolCallNames"`
			ToolCallArgs  []string `json:"toolCallArgs"`
			ToolCallID    *string  `json:"toolCallId"`
			Reasoning     *string  `json:"reasoning"`
		} `json:"loaded"`
		Count int `json:"count"`
	} `json:"cases"`
}

func loadPersistOracle(t *testing.T) persistOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "persist", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/persist/gen-oracle.ts", path, err)
	}
	var o persistOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// TestPersistLoadOaiParity —— 逐用例对账 loadOai 的完整链路。
func TestPersistLoadOaiParity(t *testing.T) {
	o := loadPersistOracle(t)
	checked := 0
	for name, c := range o.Cases {
		checked++
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sid := "p-" + name
			sdir := SessionDir(dir)
			if err := os.MkdirAll(sdir, 0o755); err != nil {
				t.Fatal(err)
			}
			// 写会话文件（行 + 校验和）
			var body string
			if len(c.Rows) > 0 {
				lines := make([]string, len(c.Rows))
				for i, r := range c.Rows {
					lines[i] = prompt.AppendChecksum(r)
				}
				body = strings.Join(lines, "\n") + "\n"
			}
			if err := os.WriteFile(filepath.Join(sdir, sid+".jsonl"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			p, err := NewPersist(sid, dir)
			if err != nil {
				t.Fatalf("构造 Persist 失败：%v", err)
			}
			defer p.Close()

			got := p.LoadOai()
			if len(got) != c.Count {
				t.Fatalf("消息数不符：Go=%d TS=%d\n  Go =%+v", len(got), c.Count, got)
			}
			for i, want := range c.Loaded {
				g := got[i]
				if g.Role != want.Role {
					t.Errorf("[%d] role：Go=%q TS=%q", i, g.Role, want.Role)
				}
				switch {
				case want.Content == nil && g.Content != nil:
					t.Errorf("[%d] content：Go=%q TS=nil", i, *g.Content)
				case want.Content != nil && g.Content == nil:
					t.Errorf("[%d] content：Go=nil TS=%q", i, *want.Content)
				case want.Content != nil && g.Content != nil && *g.Content != *want.Content:
					t.Errorf("[%d] content：Go=%q TS=%q", i, *g.Content, *want.Content)
				}
				// tool_calls
				if want.ToolCallIDs == nil {
					if g.ToolCalls != nil {
						t.Errorf("[%d] tool_calls：Go 有 %d 个，TS 为 nil", i, len(g.ToolCalls))
					}
				} else {
					if len(g.ToolCalls) != len(want.ToolCallIDs) {
						t.Errorf("[%d] tool_calls 数：Go=%d TS=%d", i, len(g.ToolCalls), len(want.ToolCallIDs))
					} else {
						for j := range want.ToolCallIDs {
							if g.ToolCalls[j].ID != want.ToolCallIDs[j] {
								t.Errorf("[%d] tool_calls[%d].id：Go=%q TS=%q", i, j, g.ToolCalls[j].ID, want.ToolCallIDs[j])
							}
							if want.ToolCallNames[j] != "" && g.ToolCalls[j].Function != nil &&
								g.ToolCalls[j].Function.Name != want.ToolCallNames[j] {
								t.Errorf("[%d] tool_calls[%d].name：Go=%q TS=%q", i, j,
									g.ToolCalls[j].Function.Name, want.ToolCallNames[j])
							}
							if want.ToolCallArgs[j] != "" && g.ToolCalls[j].Function != nil &&
								g.ToolCalls[j].Function.Arguments != want.ToolCallArgs[j] {
								t.Errorf("[%d] tool_calls[%d].arguments：Go=%q TS=%q", i, j,
									g.ToolCalls[j].Function.Arguments, want.ToolCallArgs[j])
							}
						}
					}
				}
				// tool_call_id
				switch {
				case want.ToolCallID == nil && g.ToolCallID != "":
					t.Errorf("[%d] tool_call_id：Go=%q TS=nil", i, g.ToolCallID)
				case want.ToolCallID != nil && g.ToolCallID != *want.ToolCallID:
					t.Errorf("[%d] tool_call_id：Go=%q TS=%q", i, g.ToolCallID, *want.ToolCallID)
				}
				// reasoning_content
				if want.Reasoning != nil {
					rv, _ := g.Extra["reasoning_content"].(string)
					if rv != *want.Reasoning {
						t.Errorf("[%d] reasoning：Go=%q TS=%q", i, rv, *want.Reasoning)
					}
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestParseSessionLineSkipsAudit —— 审计行被跳过。
func TestParseSessionLineSkipsAudit(t *testing.T) {
	for _, typ := range []string{"compact_start", "compact_end", "model_switch"} {
		_, skip := ParseSessionLine(`{"type":"` + typ + `"}`)
		if !skip {
			t.Errorf("%s 应被跳过", typ)
		}
	}
	// 普通行不跳过
	m, skip := ParseSessionLine(`{"role":"user","content":"hi"}`)
	if skip {
		t.Error("普通行不应跳过")
	}
	if m["role"] != "user" {
		t.Errorf("解析结果不符：%+v", m)
	}
	// 损坏行跳过
	if _, skip := ParseSessionLine("{not json"); !skip {
		t.Error("损坏行应跳过")
	}
}

// TestLegacyUserBlocksSplit —— legacy user 块数组拆分为文本 + tool 结果。
func TestLegacyUserBlocksSplit(t *testing.T) {
	m := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "question"},
			map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "result"},
		},
	}
	got := LegacyMessageToOaiMessages(m)
	if len(got) != 2 {
		t.Fatalf("应拆成 2 条，得到 %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content == nil || *got[0].Content != "question" {
		t.Errorf("第一条应是 user 文本，得到 %+v", got[0])
	}
	if got[1].Role != "tool" || got[1].ToolCallID != "c1" {
		t.Errorf("第二条应是 tool 结果，得到 %+v", got[1])
	}
}

// TestLegacyUserTextOnly —— 只有文本时只产一条。
func TestLegacyUserTextOnly(t *testing.T) {
	m := map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": "just text"}},
	}
	got := LegacyMessageToOaiMessages(m)
	if len(got) != 1 {
		t.Fatalf("应只有 1 条，得到 %d", len(got))
	}
}

// TestLegacyAssistantToolUseStableStringify —— tool_use 的 arguments 用排序键序列化。
func TestLegacyAssistantToolUseStableStringify(t *testing.T) {
	m := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type": "tool_use", "id": "c1", "name": "read_file",
				// 键序故意逆序 → stableStringify 应排序
				"input": map[string]any{"z": 1, "a": 2},
			},
		},
	}
	got := LegacyMessageToOaiMessages(m)
	if len(got) != 1 {
		t.Fatalf("应 1 条，得到 %d", len(got))
	}
	if len(got[0].ToolCalls) != 1 || got[0].ToolCalls[0].Function == nil {
		t.Fatal("应有 1 个 tool_call")
	}
	args := got[0].ToolCalls[0].Function.Arguments
	// stableStringify 排序键 → a 在前
	if !strings.HasPrefix(args, `{"a":2,"z":1}`) {
		t.Errorf("arguments 应用排序键序列化，得到 %q", args)
	}
}

// TestLegacyAssistantContentNullWhenToolOnly —— 只有 tool_use 时 content 为 nil。
func TestLegacyAssistantContentNullWhenToolOnly(t *testing.T) {
	m := map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "tool_use", "id": "c1", "name": "grep", "input": map[string]any{}}},
	}
	got := LegacyMessageToOaiMessages(m)
	if len(got) != 1 {
		t.Fatalf("应 1 条，得到 %d", len(got))
	}
	if got[0].Content != nil {
		t.Errorf("只有 tool_use 时 content 应为 nil，得到 %q", *got[0].Content)
	}
}

// TestLegacyAssistantTextOnlyContentEmpty —— 只有文本且无 tool 时 content 为文本。
func TestLegacyAssistantTextOnlyContentEmpty(t *testing.T) {
	m := map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "hi"}},
	}
	got := LegacyMessageToOaiMessages(m)
	if got[0].Content == nil || *got[0].Content != "hi" {
		t.Errorf("content 应为 hi，得到 %v", got[0].Content)
	}
}

// TestPersistAppendLoadRoundTrip —— 写入后读回（端到端）。
func TestPersistAppendLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("rt", dir)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	defer p.Close()

	c := "hello"
	if err := p.AppendOai(OaiMessage{Role: "user", Content: &c}, true); err != nil {
		t.Fatalf("append 失败：%v", err)
	}
	got := p.LoadOai()
	if len(got) != 1 {
		t.Fatalf("应读回 1 条，得到 %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content == nil || *got[0].Content != "hello" {
		t.Errorf("往返不符：%+v", got[0])
	}
}

// TestPersistEmptyFileLoadsEmpty —— 空文件读出空数组。
func TestPersistEmptyFileLoadsEmpty(t *testing.T) {
	dir := t.TempDir()
	p, err := NewPersist("empty", dir)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	defer p.Close()
	if got := p.LoadOai(); len(got) != 0 {
		t.Errorf("空文件应读出空数组，得到 %d 条", len(got))
	}
}
