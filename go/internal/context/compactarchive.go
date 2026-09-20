package context

// compactarchive.go —— 被丢弃历史的冷存储归档序列化。
//
// 对账 TS `src/agent/compact-archive.ts`。
//
// # 为什么需要它
//
// 存储层压缩（`replaceWithCheckpoint`）丢掉一段消息时，被丢的那段在这里
// 序列化并存为 `compact-history` artifact——模型之后可以 `read_section`
// 逐字取回**任何更早的消息**，而不是只能依赖有损的 LLM 摘要。
//
// # 序列化契约（**必须稳定**——read_section 按行定位）
//
// 每条消息用固定 divider 头渲染：
//
//	--- turn:N role:ROLE ---
//	<body line 1>
//	<body line 2>
//
// sections **按消息切分**（message → 行范围），**不是按轮**：单条 assistant
// 消息可能携带 content + reasoning + 多个 tool_calls 跨几十行，单条 tool
// 结果可能几万字符——轮→行的映射太粗，定位不准。逐消息 divider 保证
// 字节稳定的边界；内嵌的 catalog 再把这些聚合成紧凑的 turn→行目录。

import (
	"strconv"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// SerializedArchive 是归档序列化的结果。
//
// 对账 TS `SerializedArchive`。
type SerializedArchive struct {
	// RawContent 是字节稳定的原文。
	RawContent string
	// Sections 是逐消息的片段（name = `msgN turnT role`）。
	Sections []artifact.ArtifactSection
	// TurnRanges 是聚合后的 turn→行范围（用于构造内嵌 catalog）。
	TurnRanges []TurnRange
}

// TurnRange 是一轮占据的行范围。
//
// 对账 TS 的 `{turn, lineStart, lineEnd}`。
type TurnRange struct {
	Turn      int
	LineStart int
	LineEnd   int
}

// archiveHeader 对账 TS `archiveHeader`——固定 divider。
//
// **必须逐字相同**：read_section 靠它定位。
func archiveHeader(turn int, role string) string {
	return "--- turn:" + strconv.Itoa(turn) + " role:" + role + " ---"
}

// RenderArchiveBody 渲染单条消息的归档正文。
//
// 对账 TS `renderBody`。**四类角色分派**：
//
//   - user：纯文本（多模态取 text 部分）
//   - tool：正文；**recall 标记**折叠为一行指针（见下）
//   - assistant：content + `[reasoning]` + 每个 `[tool_call name] args`
//   - 其余（system 等）：content
//
// # recall-eviction（为什么 tool 分支要折叠）
//
// 被召回的 compact-history 块会**再次**进入历史。若原样重新归档，内容会
// 在 artifact 之间重复累积（压缩被抵消）。故折叠为一行指针——原 artifact
// 仍持有字节，指针保持可召回性而不复制内容。
func RenderArchiveBody(msg session.OaiMessage) string {
	switch msg.Role {
	case "user":
		return oaiMessageTextGo(msg)
	case "tool":
		content := contentOf(msg)
		if recall := ParseRecallMarker(content); recall != nil {
			return "[recalled → " + recall.ArtifactID + " " + recall.Section + " (see original artifact)]"
		}
		return content
	case "assistant":
		parts := []string{}
		if c := contentOf(msg); c != "" {
			parts = append(parts, c)
		}
		if rc, ok := msg.Extra["reasoning_content"].(string); ok && rc != "" {
			parts = append(parts, "[reasoning]\n"+rc)
		}
		for _, call := range msg.ToolCalls {
			name := ""
			args := ""
			if call.Function != nil {
				name = call.Function.Name
				args = call.Function.Arguments
			}
			parts = append(parts, "[tool_call "+name+"] "+args)
		}
		return strings.Join(parts, "\n")
	default:
		return contentOf(msg)
	}
}

// oaiMessageTextGo 对账 TS `oaiMessageText`。
//
// **Go 侧简化**：Go 的 `Content` 是 `*string`（多模态内容在 `Extra` 里透传，
// 未结构化）——故此处直接取字符串。若将来支持多模态数组，需在此补分派。
func oaiMessageTextGo(msg session.OaiMessage) string {
	return contentOf(msg)
}

// SerializeMessagesForArchive 把一段消息序列化为字节稳定的原文 + 逐消息
// sections + 聚合的 turn→行目录。
//
// 对账 TS `serializeMessagesForArchive`。
//
// # turn 计数规则（对账 TS）
//
// `turn` 从 0 起；**每条 user 消息递增一次**（首条 user 让 `seenUser` 变 true
// 但不递增）——即「第 N 轮」由第 N+1 条 user 开始。这保证 assistant/tool
// 消息归属到其所属的 user 轮。
func SerializeMessagesForArchive(messages []session.OaiMessage) SerializedArchive {
	sections := []artifact.ArtifactSection{}
	type agg struct{ lineStart, lineEnd int }
	turnAgg := map[int]*agg{}
	turnOrder := []int{}
	blocks := []string{}

	turn := 0
	seenUser := false
	line := 1

	for idx, msg := range messages {
		if msg.Role == "user" {
			if seenUser {
				turn++
			}
			seenUser = true
		}

		body := RenderArchiveBody(msg)
		block := archiveHeader(turn, msg.Role)
		if body != "" {
			block = block + "\n" + body
		}

		blockLines := strings.Count(block, "\n") + 1
		lineStart := line
		lineEnd := line + blockLines - 1

		sections = append(sections, artifact.ArtifactSection{
			Name:      "msg" + strconv.Itoa(idx) + " turn" + strconv.Itoa(turn) + " " + msg.Role,
			LineStart: lineStart,
			LineEnd:   lineEnd,
			CharCount: len(block),
		})

		if a, ok := turnAgg[turn]; ok {
			a.lineEnd = lineEnd
		} else {
			turnAgg[turn] = &agg{lineStart: lineStart, lineEnd: lineEnd}
			turnOrder = append(turnOrder, turn)
		}

		blocks = append(blocks, block)
		// blocks 之间用 '\n' 连接，故下一块从本块末行的**下一行**开始。
		line = lineEnd + 1
	}

	turnRanges := make([]TurnRange, 0, len(turnOrder))
	for _, t := range turnOrder {
		a := turnAgg[t]
		turnRanges = append(turnRanges, TurnRange{Turn: t, LineStart: a.lineStart, LineEnd: a.lineEnd})
	}
	// 按 turn 升序（对账 TS 的 sort）。
	sortTurnRanges(turnRanges)

	return SerializedArchive{
		RawContent: strings.Join(blocks, "\n"),
		Sections:   sections,
		TurnRanges: turnRanges,
	}
}

// sortTurnRanges 按 turn 升序（简单插入排序——规模小）。
func sortTurnRanges(rs []TurnRange) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Turn < rs[j-1].Turn; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// maxCatalogTurns 对账 TS 的 `MAX_CATALOG_TURNS`。
const maxCatalogTurns = 40

// BuildArchiveCatalog 构造紧凑的 turn→行目录（嵌进摘要消息）。
//
// 对账 TS `buildArchiveCatalog`。超过上限时**只显示前 N 轮 + 一行省略说明**。
func BuildArchiveCatalog(turnRanges []TurnRange, artifactID string) string {
	shown := turnRanges
	if len(shown) > maxCatalogTurns {
		shown = shown[:maxCatalogTurns]
	}
	lines := make([]string, 0, len(shown)+2)
	for _, t := range shown {
		lines = append(lines, "- turn "+strconv.Itoa(t.Turn)+": L"+strconv.Itoa(t.LineStart)+"-L"+strconv.Itoa(t.LineEnd))
	}
	if len(turnRanges) > maxCatalogTurns {
		last := turnRanges[len(turnRanges)-1]
		lines = append(lines, "- … (+"+strconv.Itoa(len(turnRanges)-maxCatalogTurns)+
			" more turns, up to L"+strconv.Itoa(last.LineEnd)+")")
	}
	return strings.Join(append([]string{"压缩历史目录 (artifact:" + artifactID + ", turn→行):"}, lines...), "\n")
}

// BuildRecallRefBlock 构造追加到压缩摘要消息的召回引用块。
//
// 对账 TS `buildRecallRefBlock`。**只会被放进新写的摘要消息**——绝不进
// 冻结锚前缀，故前缀缓存安全。
func BuildRecallRefBlock(artifactID string, count int, catalog string) string {
	return strings.Join([]string{
		"",
		"",
		"---",
		"[已归档 " + strconv.Itoa(count) + " 条更早消息 → artifact:" + artifactID + "]",
		catalog,
		`召回原文: read_section(artifactId="` + artifactID + `", section="L起-L止")`,
	}, "\n")
}
