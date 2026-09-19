package compact

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// micro-compact 常量（对账 TS 同名常量）。
const (
	// KeepRecentMessages 是保留的最近消息条数。
	//
	// 对账 `KEEP_RECENT_MESSAGES = 4`。
	KeepRecentMessages = 4
	// CacheAnchorMessages 是开头保留的缓存锚消息条数。
	//
	// 对账 `CACHE_ANCHOR_MESSAGES = 2`——保留前 2 条（初始 user 请求 +
	// assistant 响应）以在压缩后维持前缀结构，让 DeepSeek 的前缀缓存仍能
	// 匹配 `[System][Tools][Volatile][User1][Asst1]`。
	CacheAnchorMessages = 2
	// charsPerToken 是 ASCII 的字符/token 比。
	charsPerToken = 4.0
	// cjkCharsPerToken 是 CJK 的字符/token 比（**不是 4**）。
	cjkCharsPerToken = 1.2
	// collapseMinTurnAge 是启用语义折叠的最低轮龄。
	collapseMinTurnAge = 4
	// truncateMinChars 是截断预览的最小字符数。
	truncateMinChars = 1200
	// tier2TargetRatio 是轮次删除的目标水位（删到窗口的 70% 以下）。
	tier2TargetRatio = 0.7
	// microCompactedTag 是截断桩的标签前缀。
	microCompactedTag = "<microcompacted tool_result"
)

// MicroCompactResult 是 micro-compact 的结果。
//
// 对账 `{ messages, truncated }`。
type MicroCompactResult struct {
	Messages []session.OaiMessage
	// Truncated 是**被改动的消息数**（截断 + 删除，不含未变动的）。
	Truncated int
}

// EstimateOaiMessageTokens 估算单条消息的 token 数。
//
// 对账 `estimateOaiMessageTokens`（**micro.ts 版**）。
//
// ⚠️ **本仓有两份口径不同的估算器，这是 TS 的历史遗留，不是笔误**：
//
//	internal/context/rounds.go  3 个 CJK 区间，cjk÷1.5   ← 用于 OaiRound.TokenEstimate
//	internal/compact/micro.go   6 个 CJK 区间，cjk÷1.2   ← 本函数
//
// 后果：`MicroCompactOai` 的 tier-2 里 `currentTokens`（本函数口径）会减去
// `round.TokenEstimate`（rounds 口径）——**跨口径相减**。TS 同样如此
// （`src/compact/micro.ts` vs `src/context/rounds.ts`），本项目纪律是忠实
// 复刻既有行为，故不「修正」。若未来要统一，必须同时改两侧并重跑 oracle。
//
// **三条路径**：
//
//   - assistant：`content + JSON(tool_calls) + reasoning_content` 三者相加。
//     TS 注释说明了为什么不能互斥：旧实现只要 tool_calls 存在就跳过
//     reasoning_content，而**带工具调用的轮次恰恰是保留 reasoning 的那些**
//     （DeepSeek 需要回显）——导致估算系统性偏低，压缩在工具密集会话里总是触发太晚。
//   - user（多模态）：文本部分 + 每张图的 token（按真实尺寸分块，非按张数）。
//   - 其余：纯文本，按 ASCII / CJK 分档计算。
//
// **CJK 分档**：ASCII 每 4 字符 1 token，CJK 每 1.2 字符 1 token——中文字符
// 的 token 密度远高于英文。
func EstimateOaiMessageTokens(msg session.OaiMessage) int {
	content := ""
	switch msg.Role {
	case "assistant":
		content = derefString(msg.Content)
		if len(msg.ToolCalls) > 0 {
			content += stringifyToolCalls(msg.ToolCalls)
		}
		content += extraString(msg.Extra, "reasoning_content")
	default:
		// user 多模态（content 为数组）在 Go 侧落在 Extra 里；纯文本走这里
		content = derefString(msg.Content)
	}

	// **先拼接再统一分档，不是分段相加**——探针实测：TS 的
	// `content='hi' + reasoning='thinking hard'`（15 字符）返回 4（= ceil(15/4)），
	// 而非 ceil(2/4)+ceil(13/4)=5。分段相加会系统性高估。
	ascii, cjk := countChars(content)
	return int(math.Ceil(float64(ascii)/charsPerToken) + math.Ceil(float64(cjk)/cjkCharsPerToken))
}

// EstimateOaiTokens 估算消息列表的总 token 数。
//
// 对账 `estimateOaiTokens`。
func EstimateOaiTokens(messages []session.OaiMessage) int {
	total := 0
	for _, m := range messages {
		total += EstimateOaiMessageTokens(m)
	}
	return total
}

// countChars 统计 ASCII 与 CJK 字符数。
//
// **CJK 范围**（对账 TS 的判据）：
//
//	U+4E00–U+9FFF   CJK 统一表意文字
//	U+3400–U+4DBF   CJK 扩展 A
//	U+20000–U+2A6DF CJK 扩展 B
//	U+3040–U+309F   平假名
//	U+30A0–U+30FF   片假名
//	U+AC00–U+D7AF   韩文音节
func countChars(s string) (ascii, cjk int) {
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			ascii++
		}
	}
	return ascii, cjk
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x3040 && r <= 0x309F) ||
		(r >= 0x30A0 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7AF)
}

// ComputeTurnAges 计算每条消息的「轮龄」。
//
// 对账 `computeTurnAges`。**语义 = 从末尾往回数遇到的 user 消息数**：
//
//	[user, assistant]           → {1: 0, 0: 1}
//	[user, asst, user, asst]    → {3: 0, 2: 1, 1: 1, 0: 2}
//
// **注意 TS 的注释是错的**：它写「Current turn = 0, previous turn = 1」，
// 但实现里 `turn` 只在遇到 user 时递增，所以**最近的 user 消息轮龄是 1**
// 而非 0，只有末尾的非 user 消息才是 0。探针实测确认了这个偏差
// （`.rivet/scratch` 里复刻 TS 实现逐行跑过），Go 侧以**实现行为**为准。
//
// TS 里另有一处死代码（`turnBoundaries` 收集后从未使用），Go 侧不复刻。
func ComputeTurnAges(messages []session.OaiMessage) map[int]int {
	ages := make(map[int]int, len(messages))
	turn := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			turn++
		}
		ages[i] = turn
	}
	return ages
}

// MicroCompactOai 执行 micro-compact。
//
// 对账 `microCompactOai`。**两阶段**：
//
//   - **阶段 1（逐条）**：tool 消息超过预览上限则截断为 `<microcompacted>` 桩；
//     非近期的 assistant reasoning 走折叠（TS 侧恒不变，见 `compactOaiReasoning`）。
//   - **阶段 2（轮次删除）**：若截断后仍超窗口，删除**中间轮次**——保留前
//     `CacheAnchorMessages` 条锚与后 `KeepRecentMessages` 条近期消息，
//     按轮删到窗口的 70% 以下。
//
// **`recoveryRefs` 参数**：Go 侧本刀未实现边界归档（`recovery-ref`），
// 故 fail-open 分支（`archiveRan`）恒为 false——即「无 ref 时保留原文」的
// 保护不生效，但截断本身仍走 `recoveryRefId` 为空的分支。
func MicroCompactOai(
	messages []session.OaiMessage,
	contextWindow int,
	estimatedTokens int,
) MicroCompactResult {
	recentStart := len(messages) - KeepRecentMessages
	if recentStart < 0 {
		recentStart = 0
	}
	turnAges := ComputeTurnAges(messages)

	compactedCount := 0
	shortened := make([]session.OaiMessage, len(messages))
	for i, msg := range messages {
		isRecent := i >= recentStart
		turnAge := turnAges[i]

		// 阶段 1：tool 消息截断
		if msg.Role == "tool" {
			res := compactToolMessage(msg, contextWindow, turnAge)
			if res.changed {
				compactedCount++
				shortened[i] = res.msg
				continue
			}
		}
		// 非近期的 assistant reasoning 折叠。
		//
		// **本分支恒不生效**（TS 的 `compactOaiReasoning` 硬编码返回
		// `changed: false`）——reasoning_content 永远完整保留：截断收益在
		// 前缀缓存生效后可忽略，而**不完整推理会降低模型质量**（MiMo、DeepSeek
		// 要求回显完整 reasoning）。保留这个空分支是为了对齐 TS 的结构，
		// 未来若引入 reasoning 摘要可直接填充。`isRecent` 因此是结构性占位，
		// 不参与任何判定——变异测试对它的「红 0」是**等价变异**，不是测试缺口。
		if !isRecent && i >= CacheAnchorMessages {
			_ = msg // 占位：TS 侧此处调用 compactOaiReasoning(msg)，恒不变
		}
		shortened[i] = msg
	}

	currentTokens := estimatedTokens
	if compactedCount > 0 {
		currentTokens = EstimateOaiTokens(shortened)
	}

	// 无需轮次删除
	if currentTokens <= contextWindow || len(messages) <= KeepRecentMessages+CacheAnchorMessages {
		return MicroCompactResult{Messages: shortened, Truncated: compactedCount}
	}

	// ── 阶段 2：轮次删除 ──
	anchorEnd := CacheAnchorMessages
	tier2RecentStart := len(shortened) - KeepRecentMessages
	if tier2RecentStart < 0 {
		tier2RecentStart = 0
	}
	rounds := context.GroupIntoRoundsOai(shortened)
	removeIndexes := map[int]bool{}

	for _, round := range rounds {
		// **三重条件**：在锚之后、在近期之前、API 不变量完好
		if round.StartMessageIndex < anchorEnd ||
			round.EndMessageIndex > tier2RecentStart ||
			round.ApiInvariant != context.InvariantOK {
			continue
		}
		// 删掉这轮后仍在 70% 之上则跳过（不够本）
		if float64(currentTokens-round.TokenEstimate) <= float64(contextWindow)*tier2TargetRatio {
			continue
		}
		for idx := round.StartMessageIndex; idx < round.EndMessageIndex; idx++ {
			removeIndexes[idx] = true
		}
		currentTokens -= round.TokenEstimate
		compactedCount += round.EndMessageIndex - round.StartMessageIndex
		if currentTokens <= contextWindow {
			break
		}
	}

	if len(removeIndexes) > 0 {
		result := make([]session.OaiMessage, 0, len(shortened)-len(removeIndexes))
		for i, m := range shortened {
			if !removeIndexes[i] {
				result = append(result, m)
			}
		}
		return MicroCompactResult{Messages: result, Truncated: compactedCount}
	}

	return MicroCompactResult{Messages: shortened, Truncated: compactedCount}
}

// compactToolMessage 压缩单条 tool 消息（对账 `compactToolMessage`）。
//
// **本刀只实现截断路径**——语义折叠（`context-collapse`）留下刀，故
// `turnAge >= collapseMinTurnAge` 的分支恒走截断。
//
// **截断上限的取值**（TS 注释里的 BUG FIX）：原公式是
// `toolResultMaxTokens * CHARS_PER_TOKEN`，对 200K 窗口算出 240K 字符，
// 导致截断**永不触发**。修正为直接用 `toolResultMaxTokens` 作为字符上限。
func compactToolMessage(
	msg session.OaiMessage,
	contextWindow int,
	turnAge int,
) (result struct {
	msg     session.OaiMessage
	changed bool
}) {
	result.msg = msg
	content := derefString(msg.Content)

	// **必须用数字重载**（对账 `compactThresholds(contextWindow)`）——不是
	// Profile 版本。两者在 toolResultMaxTokens 上取值相同，但语义不同：
	// 数字重载的 reactive 是 0.8（历史遗留），Profile 是 0.88。
	previewChars := CompactThresholdsForWindow(contextWindow).ToolResultMaxTokens
	// `truncateMinChars` 下限是**不可达防御**——最小的 `toolResultMaxTokens`
	// 是 100K 窗口下的 30000，远大于 1200。TS 同样写 `Math.max(1_200, ...)`。
	// 变异测试对它的「红 0」是**等价变异**。
	if previewChars < truncateMinChars {
		previewChars = truncateMinChars
	}
	if len(content) <= previewChars {
		return
	}

	// 无 recoveryRef（本刀未实现边界归档）→ 直接构造桩
	stub := microCompactedTag + ` original_chars="` + microItoa(len(content)) + `">` + "\n" +
		content[:previewChars] + "\n</microcompacted tool_result>"
	if len(stub) >= len(content) {
		return
	}
	c := stub
	next := msg
	next.Content = &c
	result.msg = next
	result.changed = true
	return
}

// ── 内部辅助 ──

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	if v, ok := extra[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// stringifyToolCalls 序列化工具调用（对账 TS 的 `JSON.stringify(tool_calls)`）。
//
// **必须与 TS 的键序一致**——TS 的对象字面量序是 `id, type, function`，
// 而 function 内部是 `name, arguments`。Go 侧手工构造以保证字节一致。
func stringifyToolCalls(calls []session.OaiToolCall) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, c := range calls {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":`)
		b.WriteString(mustJSON(c.ID))
		b.WriteString(`,"type":"function","function":{"name":`)
		b.WriteString(mustJSON(c.Function.Name))
		b.WriteString(`,"arguments":`)
		b.WriteString(mustJSON(c.Function.Arguments))
		b.WriteString(`}}`)
	}
	b.WriteByte(']')
	return b.String()
}

func microItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
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

// mustJSON 序列化字符串（对账 JSON.stringify 的转义规则）。
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
