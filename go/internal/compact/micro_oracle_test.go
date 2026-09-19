package compact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// oracle 的 JSON 形态（与 testdata/pressure/oracle.json 对应）。
type microOracleMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ReasoningContent string `json:"reasoning_content"`
}

type microOracle struct {
	Constants struct {
		KeepRecentMessages  int `json:"keepRecentMessages"`
		CacheAnchorMessages int `json:"cacheAnchorMessages"`
	} `json:"constants"`
	Tokens []struct {
		Name string         `json:"name"`
		Msg  microOracleMsg `json:"msg"`
		Want int            `json:"want"`
	} `json:"tokens"`
	Micro []struct {
		Name            string           `json:"name"`
		ContextWindow   int              `json:"contextWindow"`
		EstimatedTokens int              `json:"estimatedTokens"`
		Msgs            []microOracleMsg `json:"msgs"`
		Want            struct {
			Truncated    int `json:"truncated"`
			MessageCount int `json:"messageCount"`
			Shapes       []struct {
				Role          string `json:"role"`
				Len           int    `json:"len"`
				StartsWithTag bool   `json:"startsWithTag"`
			} `json:"shapes"`
		} `json:"want"`
	} `json:"micro"`
}

func loadMicroOracle(t *testing.T) microOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "pressure", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var out microOracle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return out
}

// buildMsg 从 oracle 字段构造 OaiMessage。
//
// **Content 恒为指针**：oracle 里空串对应显式空串（TS 的 `content: ”`），
// 而非字段缺失——两者在 token 估算里等价（都算 0 字符），但保持形态一致
// 以便未来对账序列化路径。
func buildMsg(m microOracleMsg) session.OaiMessage {
	out := session.OaiMessage{Role: m.Role}
	c := m.Content
	out.Content = &c
	if m.ToolCallID != "" {
		out.ToolCallID = m.ToolCallID
	}
	if m.ReasoningContent != "" {
		out.Extra = map[string]any{"reasoning_content": m.ReasoningContent}
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]session.OaiToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			calls = append(calls, session.OaiToolCall{
				ID:       tc.ID,
				Type:     tc.Type,
				Function: &session.OaiFunction{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		out.ToolCalls = calls
	}
	return out
}

// TestMicroConstantsParity —— **常量对账**。
//
// `CacheAnchorMessages = 2` 是前缀缓存的关键：压缩后保留前 2 条，
// 让 DeepSeek 的前缀仍能匹配 `[System][Tools][Volatile][User1][Asst1]`。
func TestMicroConstantsParity(t *testing.T) {
	o := loadMicroOracle(t)
	if KeepRecentMessages != o.Constants.KeepRecentMessages {
		t.Errorf("KeepRecentMessages：Go=%d TS=%d", KeepRecentMessages, o.Constants.KeepRecentMessages)
	}
	if CacheAnchorMessages != o.Constants.CacheAnchorMessages {
		t.Errorf("CacheAnchorMessages：Go=%d TS=%d", CacheAnchorMessages, o.Constants.CacheAnchorMessages)
	}
}

// TestEstimateOaiMessageTokensParity —— **token 估算逐值对账**（12 个用例）。
//
// 覆盖三条路径：纯文本（ASCII/CJK 分档）、assistant 三部分相加、tool 消息。
//
// **关键用例** `assistant-with-reasoning`（=4）锁定「拼接后统一分档」而非
// 「分段相加」——探针实测 TS 对 `content='hi' + reasoning='thinking hard'`
// （15 字符）返回 4（= ceil(15/4)），分段相加会得 1+4=5。
func TestEstimateOaiMessageTokensParity(t *testing.T) {
	o := loadMicroOracle(t)
	for _, c := range o.Tokens {
		t.Run(c.Name, func(t *testing.T) {
			got := EstimateOaiMessageTokens(buildMsg(c.Msg))
			if got != c.Want {
				t.Errorf("token 估算：Go=%d TS=%d", got, c.Want)
			}
		})
	}
}

// TestMicroCompactParity —— **micro-compact 行为对账**（4 个用例）。
//
// 覆盖四条路径：无需压缩 / **截断** / 短消息不截断 / **轮次删除**。
//
// `tier2-removes-middle-rounds` 锁定阶段 2：10 条 → 6 条（删中间 2 个轮次），
// 保留前 2 锚 + 后 4 近。
func TestMicroCompactParity(t *testing.T) {
	o := loadMicroOracle(t)
	for _, c := range o.Micro {
		t.Run(c.Name, func(t *testing.T) {
			msgs := make([]session.OaiMessage, 0, len(c.Msgs))
			for _, m := range c.Msgs {
				msgs = append(msgs, buildMsg(m))
			}
			got := MicroCompactOai(msgs, c.ContextWindow, c.EstimatedTokens)
			if got.Truncated != c.Want.Truncated {
				t.Errorf("truncated：Go=%d TS=%d", got.Truncated, c.Want.Truncated)
			}
			if len(got.Messages) != c.Want.MessageCount {
				t.Fatalf("消息数：Go=%d TS=%d", len(got.Messages), c.Want.MessageCount)
			}
			for i, w := range c.Want.Shapes {
				if i >= len(got.Messages) {
					break
				}
				m := got.Messages[i]
				if m.Role != w.Role {
					t.Errorf("第 %d 条 role：Go=%q TS=%q", i, m.Role, w.Role)
				}
				s := derefString(m.Content)
				if len(s) != w.Len {
					t.Errorf("第 %d 条内容长度：Go=%d TS=%d", i, len(s), w.Len)
				}
				if starts := len(s) > 0 && s[0] == '<'; starts != w.StartsWithTag {
					t.Errorf("第 %d 条 startsWithTag：Go=%v TS=%v", i, starts, w.StartsWithTag)
				}
			}
		})
	}
}

// TestComputeTurnAges —— **轮龄计算**（TS 未导出该函数，故用探针复刻实现自证）。
//
// 语义（探针实测，非 TS 注释）：**从末尾往回数遇到的 user 消息数**。
// 所以末尾的非 user 消息 = 0，最近的 user = 1——TS 注释写的「当前轮 = 0」
// 与它自己的实现不符。
func TestComputeTurnAges(t *testing.T) {
	c := func(s string) *string { return &s }
	cases := []struct {
		name string
		msgs []session.OaiMessage
		want map[int]int
	}{
		{
			name: "single-user",
			msgs: []session.OaiMessage{{Role: "user", Content: c("a")}},
			want: map[int]int{0: 1},
		},
		{
			// 末尾的 assistant 轮龄 0；最近的 user 是 1
			name: "user-asst",
			msgs: []session.OaiMessage{
				{Role: "user", Content: c("a")},
				{Role: "assistant", Content: c("b")},
			},
			want: map[int]int{0: 1, 1: 0},
		},
		{
			name: "two-turns",
			msgs: []session.OaiMessage{
				{Role: "user", Content: c("a")},
				{Role: "assistant", Content: c("b")},
				{Role: "user", Content: c("c")},
				{Role: "assistant", Content: c("d")},
			},
			want: map[int]int{0: 2, 1: 1, 2: 1, 3: 0},
		},
		{
			// tool 消息不推进轮号——它与其前的 assistant 同轮
			name: "with-tool",
			msgs: []session.OaiMessage{
				{Role: "user", Content: c("a")},
				{Role: "assistant", Content: c("b")},
				{Role: "tool", Content: c("r")},
				{Role: "user", Content: c("c")},
			},
			want: map[int]int{0: 2, 1: 1, 2: 1, 3: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeTurnAges(tc.msgs)
			for idx, want := range tc.want {
				if got[idx] != want {
					t.Errorf("index %d：Go=%d want=%d", idx, got[idx], want)
				}
			}
		})
	}
}
