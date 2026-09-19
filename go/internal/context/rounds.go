// Package context 提供认知上下文层。
//
// 对账 src/context/（30+ 文件、约 6000+ 行）。**当前已移植的范围**很小——
// 只有 rounds 分组（本文件）。其余（claim-store / cognitive-ledger /
// stigmergy / task-contract / compact-policy 等）尚未移植。
//
// 这是**有意的分步**：rounds 分组是纯算法、无外部依赖，可独立对账；
// 其他模块依赖会话状态与存储层，需要更多地基。
package context

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// ApiInvariant 是一轮的 API 不变量状态。
//
// 对账 TS 的 ApiInvariant：
//   - ok：tool_calls 与 tool_results 配对完整
//   - repaired：有孤儿 tool_result（结果多于调用）——可修复
//   - broken：有缺失的 tool_result（调用多于结果）——**未配对，会打 API**
type ApiInvariant string

const (
	InvariantOK       ApiInvariant = "ok"
	InvariantRepaired ApiInvariant = "repaired"
	InvariantBroken   ApiInvariant = "broken"
)

// OaiRound 是一次「回合」的消息区间。
//
// 对账 src/context/rounds.ts 的 OaiRound。
type OaiRound struct {
	ID string
	// StartMessageIndex / EndMessageIndex 是**左闭右开**的消息区间。
	StartMessageIndex int
	EndMessageIndex   int
	// TurnNumber 从 1 开始——**只有 user 消息会推进它**。
	TurnNumber     int
	HasToolCalls   bool
	HasToolResults bool
	TokenEstimate  int
	ApiInvariant   ApiInvariant
}

// hasOaiToolCalls 报告消息是否为「带 tool_calls 的 assistant」。
//
// 对账 hasOaiToolCalls：role 为 assistant **且** tool_calls 是非空数组。
func hasOaiToolCalls(m session.OaiMessage) bool {
	return m.Role == "assistant" && len(m.ToolCalls) > 0
}

// extractOaiToolCallIDs 提取消息里的 tool_call id 列表。
func extractOaiToolCallIDs(m session.OaiMessage) []string {
	out := make([]string, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		out = append(out, tc.ID)
	}
	return out
}

// EstimateOaiMessageTokens 估算一条 OAI 消息的 token 数。
//
// 对账 estimateOaiMessageTokens。**算法**（不是精确 tokenizer，是启发式）：
//
//   - assistant：content + reasoning_content + JSON.stringify(tool_calls) 拼起来
//   - user（parts 数组）：文本按 len/4，图片走 estimateImageTokens
//   - 其余：content 字符串
//
// 然后对内容按字符分类统计：
//
//	ascii（含其他非 CJK）→ ceil(ascii / 4)
//	CJK（中日韩）        → ceil(cjk / 1.5)
//
// **CJK 判定区间**（三个，缺一不可）：
//   - 0x4E00–0x9FFF：CJK 统一表意
//   - 0x3040–0x30FF：日文平/片假名
//   - 0xAC00–0xD7AF：韩文音节
//
// **未移植**：user 的 parts 数组分支（含 `estimateImageTokens`，需解析 PNG
// base64 头）——该分支在 Go 侧退化为把 content 当字符串处理。见 HANDOFF。
func EstimateOaiMessageTokens(m session.OaiMessage) int {
	content := ""
	switch m.Role {
	case "assistant":
		if m.Content != nil {
			content += *m.Content
		}
		if rc, ok := m.Extra["reasoning_content"].(string); ok {
			content += rc
		}
		if len(m.ToolCalls) > 0 {
			content += stringifyToolCalls(m.ToolCalls)
		}
	default:
		if m.Content != nil {
			content = *m.Content
		}
	}

	ascii, cjk := 0, 0
	for _, r := range content {
		if isCJK(r) {
			cjk++
		} else {
			ascii++
		}
	}
	return int(math.Ceil(float64(ascii)/4)) + int(math.Ceil(float64(cjk)/1.5))
}

// isCJK 判定 rune 是否落在三个 CJK 区间之一。
//
// **对账 TS 的三个区间**——注意 TS 侧用 `codePointAt(0)` 遍历（按码点），
// Go 的 `range` 也按码点，故语义一致（都正确处理代理对）。
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3040 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7AF)
}

// stringifyToolCalls 把 tool_calls 序列化为 JSON（用于 token 估算）。
//
// 对账 `JSON.stringify(msg.tool_calls)`。**只服务 token 估算，不参与 wire 路径**。
//
// **为什么键序不参与对账**：TS 的 JSON.stringify 保**构造序**（对象字面量序或
// JSON.parse 的文件序）——从 Go 侧无从得知。但 token 估算只看**字符长度**
// （`ceil(len/4)`），长度与键序无关，故只要**字段集合完整**即可对齐。
// 这里对 Extra 用排序键（Go map 无序，排序保证可复现），长度不受影响。
//
// **必须透传 Extra**：TS 侧 `JSON.stringify` 会带上 tool_call 上的所有字段
// （如 `index`）。丢弃它们会让长度偏短——oracle 的 toolcall_extra_fields
// 用例锁住这一点（曾因此红：Go=19 / TS=25）。
func stringifyToolCalls(tcs []session.OaiToolCall) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, tc := range tcs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":`)
		b.WriteString(jsonStr(tc.ID))
		b.WriteString(`,"type":`)
		b.WriteString(jsonStr(tc.Type))
		if tc.Function != nil {
			b.WriteString(`,"function":{"name":`)
			b.WriteString(jsonStr(tc.Function.Name))
			b.WriteString(`,"arguments":`)
			b.WriteString(jsonStr(tc.Function.Arguments))
			b.WriteByte('}')
		}
		// 透传额外字段（TS 的 JSON.stringify 会带上）。**排除已知键**，
		// 避免重复输出。键排序由 encoding/json 保证（map 序列化按序）。
		for _, k := range sortedExtraKeys(tc.Extra) {
			if k == "id" || k == "type" || k == "function" {
				continue
			}
			b.WriteByte(',')
			b.WriteString(jsonStr(k))
			b.WriteByte(':')
			b.WriteString(marshalCompact(tc.Extra[k]))
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.String()
}

// sortedExtraKeys 返回 Extra 的键（排序，保证可复现）。
func sortedExtraKeys(extra map[string]any) []string {
	if len(extra) == 0 {
		return nil
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// marshalCompact 紧凑序列化一个值（对账 JSON.stringify 的紧凑输出）。
//
// 用 encoding/json 的紧凑模式——它**转义 HTML 字符**（`<` → `\u003c`），
// 与 JSON.stringify 不同。但本函数只服务 token 估算的**长度**，且 tool_call
// 的额外字段在实践中是数字/短字符串（如 `index`），不含 HTML 字符。
// 若将来出现 HTML 字符导致长度偏差，oracle 会抓到。
func marshalCompact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// jsonStr 序列化字符串（对账 JSON.stringify 的转义）。
func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[(r>>4)&0xF])
				b.WriteByte(hex[r&0xF])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// GroupIntoRoundsOai 把 OAI 消息序列分成回合。
//
// 对账 groupIntoRoundsOai。**分组规则**（四种形态）：
//
//  1. **user 消息** → 单条成轮，**推进 turnNumber**
//  2. **assistant 无 tool_calls** → 单条成轮
//  3. **assistant 带 tool_calls** → 吸收**紧随其后**的所有 tool 消息
//     （贪心：一旦遇到非 tool 就停）。按 tool_call id 比对算不变量
//  4. **孤儿 tool 消息**（前面没有 assistant 领它）→ 单条成轮，
//     不变量 = `repaired`
//  5. 其余（system 等）→ 单条成轮
//
// **不变量判定**（仅形态 3 会算）：
//   - 有调用无结果（missingResults 非空）→ `broken`
//   - 有结果无调用（orphanResults 非空）→ `repaired`
//   - 否则 → `ok`
func GroupIntoRoundsOai(messages []session.OaiMessage) []OaiRound {
	var rounds []OaiRound
	i := 0
	roundID := 0
	turnNumber := 0

	nextID := func() string {
		id := "round_" + itoa(roundID)
		roundID++
		return id
	}

	for i < len(messages) {
		msg := messages[i]
		startIndex := i

		// 形态 1：user 消息
		if msg.Role == "user" {
			turnNumber++
			rounds = append(rounds, OaiRound{
				ID:                nextID(),
				StartMessageIndex: startIndex,
				EndMessageIndex:   i + 1,
				TurnNumber:        turnNumber,
				TokenEstimate:     EstimateOaiMessageTokens(msg),
				ApiInvariant:      InvariantOK,
			})
			i++
			continue
		}

		// 形态 2：assistant 无 tool_calls
		if msg.Role == "assistant" && !hasOaiToolCalls(msg) {
			rounds = append(rounds, OaiRound{
				ID:                nextID(),
				StartMessageIndex: startIndex,
				EndMessageIndex:   i + 1,
				TurnNumber:        turnNumber,
				TokenEstimate:     EstimateOaiMessageTokens(msg),
				ApiInvariant:      InvariantOK,
			})
			i++
			continue
		}

		// 形态 3：assistant 带 tool_calls → 吸收后续 tool 消息
		if msg.Role == "assistant" && hasOaiToolCalls(msg) {
			toolCallIDs := extractOaiToolCallIDs(msg)
			var resultIDs []string
			j := i + 1
			for j < len(messages) && messages[j].Role == "tool" {
				resultIDs = append(resultIDs, messages[j].ToolCallID)
				j++
			}

			missing := idsNotIn(toolCallIDs, resultIDs)
			orphans := idsNotIn(resultIDs, toolCallIDs)

			invariant := InvariantOK
			if len(missing) > 0 {
				invariant = InvariantBroken
			} else if len(orphans) > 0 {
				invariant = InvariantRepaired
			}

			tokenEst := 0
			for idx := startIndex; idx < j; idx++ {
				tokenEst += EstimateOaiMessageTokens(messages[idx])
			}

			rounds = append(rounds, OaiRound{
				ID:                nextID(),
				StartMessageIndex: startIndex,
				EndMessageIndex:   j,
				TurnNumber:        turnNumber,
				HasToolCalls:      true,
				HasToolResults:    len(resultIDs) > 0,
				TokenEstimate:     tokenEst,
				ApiInvariant:      invariant,
			})
			i = j
			continue
		}

		// 形态 4：孤儿 tool 消息
		if msg.Role == "tool" {
			rounds = append(rounds, OaiRound{
				ID:                nextID(),
				StartMessageIndex: startIndex,
				EndMessageIndex:   i + 1,
				TurnNumber:        turnNumber,
				HasToolResults:    true,
				TokenEstimate:     EstimateOaiMessageTokens(msg),
				ApiInvariant:      InvariantRepaired,
			})
			i++
			continue
		}

		// 形态 5：system 或其他
		rounds = append(rounds, OaiRound{
			ID:                nextID(),
			StartMessageIndex: startIndex,
			EndMessageIndex:   i + 1,
			TurnNumber:        turnNumber,
			TokenEstimate:     EstimateOaiMessageTokens(msg),
			ApiInvariant:      InvariantOK,
		})
		i++
	}

	return rounds
}

// CountRoundsOai 数消息序列里有几轮。
//
// 对账 countRoundsOai。**为什么单独存在**：groupIntoRoundsOai 每次请求构建都
// 跑一遍（400 轮时占 37ms 构建里的 ~21ms），而本函数只要 ~0.2ms。语义必须与
// 分组**完全一致**——分组规则见 GroupIntoRoundsOai（TS 侧有 parity 测试钉住）。
func CountRoundsOai(messages []session.OaiMessage) int {
	count := 0
	i := 0
	for i < len(messages) {
		count++
		msg := messages[i]
		i++
		// assistant 带 tool_calls 会吸收紧随的 tool 结果——整段算一轮
		if msg.Role == "assistant" && hasOaiToolCalls(msg) {
			for i < len(messages) && messages[i].Role == "tool" {
				i++
			}
		}
	}
	return count
}

// ApiInvariantStatus 是轮次不变量的汇总。
//
// 对账 ApiInvariantStatus。
type ApiInvariantStatus struct {
	TotalRounds    int
	OkRounds       int
	RepairedRounds int
	BrokenRounds   int
	// OrphanToolUse 是有调用无结果的轮 id（**会打 API**）。
	OrphanToolUse []string
	// OrphanToolResult 是有结果无调用的轮 id。
	OrphanToolResult []string
}

// ComputeOaiInvariantStatus 汇总轮次的不变量状态。
//
// 对账 computeOaiInvariantStatus。**注意孤儿判定条件**：
//   - repaired 且（有结果 **且** 无调用）→ 记入 orphanToolResult
//   - broken 且（有调用 **且** 无结果）→ 记入 orphanToolUse
func ComputeOaiInvariantStatus(rounds []OaiRound) ApiInvariantStatus {
	status := ApiInvariantStatus{
		TotalRounds:      len(rounds),
		OrphanToolUse:    []string{},
		OrphanToolResult: []string{},
	}

	for _, r := range rounds {
		switch r.ApiInvariant {
		case InvariantOK:
			status.OkRounds++
		case InvariantRepaired:
			status.RepairedRounds++
			if r.HasToolResults && !r.HasToolCalls {
				status.OrphanToolResult = append(status.OrphanToolResult, r.ID)
			}
		case InvariantBroken:
			status.BrokenRounds++
			if r.HasToolCalls && !r.HasToolResults {
				status.OrphanToolUse = append(status.OrphanToolUse, r.ID)
			}
		}
	}
	return status
}

// idsNotIn 返回 a 中不在 b 里的元素（对账 TS 的 `a.filter(x => !b.includes(x))`）。
//
// **注意保序**——TS 的 filter 保留 a 的原序。
func idsNotIn(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if !set[x] {
			out = append(out, x)
		}
	}
	return out
}

// itoa 是整数转字符串的本地实现（避免为单处调用引入 strconv）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
