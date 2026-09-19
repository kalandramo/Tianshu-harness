package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// oracleEntry 对应 gen-oracle.ts 写出的一项。
type oracleEntry struct {
	Rounds []struct {
		ID                string `json:"id"`
		StartMessageIndex int    `json:"startMessageIndex"`
		EndMessageIndex   int    `json:"endMessageIndex"`
		TurnNumber        int    `json:"turnNumber"`
		HasToolCalls      bool   `json:"hasToolCalls"`
		HasToolResults    bool   `json:"hasToolResults"`
		TokenEstimate     int    `json:"tokenEstimate"`
		ApiInvariant      string `json:"apiInvariant"`
	} `json:"rounds"`
	Count     int `json:"count"`
	Invariant struct {
		TotalRounds      int      `json:"totalRounds"`
		OkRounds         int      `json:"okRounds"`
		RepairedRounds   int      `json:"repairedRounds"`
		BrokenRounds     int      `json:"brokenRounds"`
		OrphanToolUse    []string `json:"orphanToolUse"`
		OrphanToolResult []string `json:"orphanToolResult"`
	} `json:"invariant"`
}

// messageSpec 是 oracle 用例里的消息形态（gen-oracle.ts 的构造同构）。
type messageSpec struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ToolCalls        []map[string]any `json:"tool_calls"`
	ToolCallID       string           `json:"tool_call_id"`
	ReasoningContent string           `json:"reasoning_content"`
}

// loadOracle 读 golden。
func loadOracle(t *testing.T) map[string]oracleEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rounds", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败（先跑 node_modules/.bin/tsx go/testdata/rounds/gen-oracle.ts）：%v", err)
	}
	var out map[string]oracleEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatal("oracle 为空——生成器可能没跑成功")
	}
	return out
}

// loadCases 读 oracle 用例的**输入**（消息序列）。
//
// 输入与 golden 分开维护：gen-oracle.ts 里 cases 是 TS 字面量，这里复刻同一批。
// **风险**：两处漂移会导致「Go 测的是另一组输入」。故用 cases.ts 导出的 JSON
// 作为唯一真源——见 cases.json。
func loadCases(t *testing.T) map[string][]session.OaiMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "rounds", "cases.json"))
	if err != nil {
		t.Fatalf("读 cases 失败：%v", err)
	}
	var specs map[string][]messageSpec
	if err := json.Unmarshal(raw, &specs); err != nil {
		t.Fatalf("解析 cases 失败：%v", err)
	}
	out := map[string][]session.OaiMessage{}
	for name, msgs := range specs {
		out[name] = toOaiMessages(msgs)
	}
	return out
}

// toOaiMessages 把 JSON 形态转成 Go 侧 OaiMessage。
func toOaiMessages(specs []messageSpec) []session.OaiMessage {
	out := make([]session.OaiMessage, 0, len(specs))
	for _, s := range specs {
		m := session.OaiMessage{Role: s.Role, ToolCallID: s.ToolCallID}
		switch c := s.Content.(type) {
		case string:
			m.Content = &c
		case nil:
			// 保持 nil（缺失语义）
		}
		for _, tc := range s.ToolCalls {
			call := session.OaiToolCall{}
			if v, ok := tc["id"].(string); ok {
				call.ID = v
			}
			if v, ok := tc["type"].(string); ok {
				call.Type = v
			}
			if fn, ok := tc["function"].(map[string]any); ok {
				f := &session.OaiFunction{}
				if v, ok := fn["name"].(string); ok {
					f.Name = v
				}
				if v, ok := fn["arguments"].(string); ok {
					f.Arguments = v
				}
				call.Function = f
			}
			// 额外字段透传（TS 的 JSON.stringify 会带上——token 长度对账需要）
			for k, v := range tc {
				if k == "id" || k == "type" || k == "function" {
					continue
				}
				if call.Extra == nil {
					call.Extra = map[string]any{}
				}
				call.Extra[k] = v
			}
			m.ToolCalls = append(m.ToolCalls, call)
		}
		if s.ReasoningContent != "" {
			m.Extra = map[string]any{"reasoning_content": s.ReasoningContent}
		}
		out = append(out, m)
	}
	return out
}

// TestRoundsOracleParity —— **核心对账**：Go 分组结果与 TS 逐字段相同。
func TestRoundsOracleParity(t *testing.T) {
	oracle := loadOracle(t)
	cases := loadCases(t)

	if len(cases) != len(oracle) {
		t.Fatalf("用例数不匹配：cases=%d oracle=%d（两处漂移）", len(cases), len(oracle))
	}

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			msgs, ok := cases[name]
			if !ok {
				t.Fatalf("oracle 有 %q 但 cases 没有——输入不同步", name)
			}

			got := GroupIntoRoundsOai(msgs)

			if len(got) != len(entry.Rounds) {
				t.Fatalf("轮数不符：Go=%d TS=%d\nGo:  %+v", len(got), len(entry.Rounds), got)
			}
			for i, want := range entry.Rounds {
				g := got[i]
				if g.ID != want.ID {
					t.Errorf("[%d] id: Go=%q TS=%q", i, g.ID, want.ID)
				}
				if g.StartMessageIndex != want.StartMessageIndex {
					t.Errorf("[%d] start: Go=%d TS=%d", i, g.StartMessageIndex, want.StartMessageIndex)
				}
				if g.EndMessageIndex != want.EndMessageIndex {
					t.Errorf("[%d] end: Go=%d TS=%d", i, g.EndMessageIndex, want.EndMessageIndex)
				}
				if g.TurnNumber != want.TurnNumber {
					t.Errorf("[%d] turn: Go=%d TS=%d", i, g.TurnNumber, want.TurnNumber)
				}
				if g.HasToolCalls != want.HasToolCalls {
					t.Errorf("[%d] hasToolCalls: Go=%v TS=%v", i, g.HasToolCalls, want.HasToolCalls)
				}
				if g.HasToolResults != want.HasToolResults {
					t.Errorf("[%d] hasToolResults: Go=%v TS=%v", i, g.HasToolResults, want.HasToolResults)
				}
				if string(g.ApiInvariant) != want.ApiInvariant {
					t.Errorf("[%d] invariant: Go=%q TS=%q", i, g.ApiInvariant, want.ApiInvariant)
				}
				if g.TokenEstimate != want.TokenEstimate {
					t.Errorf("[%d] tokenEstimate: Go=%d TS=%d", i, g.TokenEstimate, want.TokenEstimate)
				}
			}
		})
	}
}

// TestCountRoundsOaiParity —— countRoundsOai 与 oracle 对账。
func TestCountRoundsOaiParity(t *testing.T) {
	oracle := loadOracle(t)
	cases := loadCases(t)

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			msgs := cases[name]
			got := CountRoundsOai(msgs)
			if got != entry.Count {
				t.Errorf("轮数：Go=%d TS=%d", got, entry.Count)
			}
			// **parity 不变量**：count 必须等于分组长度
			if g := len(GroupIntoRoundsOai(msgs)); got != g {
				t.Errorf("countRoundsOai(%d) 与 groupIntoRoundsOai(%d) 不一致——TS 侧有 parity 测试钉住这条", got, g)
			}
		})
	}
}

// TestComputeInvariantStatusParity —— 不变量汇总与 oracle 对账。
func TestComputeInvariantStatusParity(t *testing.T) {
	oracle := loadOracle(t)
	cases := loadCases(t)

	for name, entry := range oracle {
		t.Run(name, func(t *testing.T) {
			msgs := cases[name]
			got := ComputeOaiInvariantStatus(GroupIntoRoundsOai(msgs))

			if got.TotalRounds != entry.Invariant.TotalRounds {
				t.Errorf("totalRounds: Go=%d TS=%d", got.TotalRounds, entry.Invariant.TotalRounds)
			}
			if got.OkRounds != entry.Invariant.OkRounds {
				t.Errorf("okRounds: Go=%d TS=%d", got.OkRounds, entry.Invariant.OkRounds)
			}
			if got.RepairedRounds != entry.Invariant.RepairedRounds {
				t.Errorf("repairedRounds: Go=%d TS=%d", got.RepairedRounds, entry.Invariant.RepairedRounds)
			}
			if got.BrokenRounds != entry.Invariant.BrokenRounds {
				t.Errorf("brokenRounds: Go=%d TS=%d", got.BrokenRounds, entry.Invariant.BrokenRounds)
			}
			if !equalStrs(got.OrphanToolUse, entry.Invariant.OrphanToolUse) {
				t.Errorf("orphanToolUse: Go=%v TS=%v", got.OrphanToolUse, entry.Invariant.OrphanToolUse)
			}
			if !equalStrs(got.OrphanToolResult, entry.Invariant.OrphanToolResult) {
				t.Errorf("orphanToolResult: Go=%v TS=%v", got.OrphanToolResult, entry.Invariant.OrphanToolResult)
			}
		})
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
