package context

// archiveassembly.go —— `ArchiveDiscarded` 依赖的装配。
//
// 对账 TS `CompactionController.archiveDiscardedHistory`
// （`compaction-controller.ts:1009`）。
//
// # 作用
//
// `replaceWithCheckpoint` 丢掉一段消息时，把它序列化并**存为
// `compact-history` artifact**，返回一段「召回引用块」拼到摘要末尾——
// 模型之后可以 `read_section` 逐字取回被丢的历史。
//
// # fail-soft 契约（对账 TS）
//
// 归档**绝不能阻塞压缩**。TS 的注释与实现都很明确：
// 「Returns null when archiving is unavailable, the zone is empty, or the
// write fails — compaction must never be blocked by archival (fail-soft).」
//
// Go 侧对应：任何失败路径返回空串（调用方 `ReplaceWithCheckpoint` 对空串
// 就是「不追加」——与 TS 的 `archive ? ... : ...` 分支同形）。

import (
	"strconv"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

// CompactHistoryTool 是归档 artifact 的工具名。
//
// 对账 TS 的 `COMPACT_HISTORY_TOOL`。**read_section 靠它走流式快速路径**
// （大归档常超内存读取上限）。
const CompactHistoryTool = "compact-history"

// ArchiveSink 是归档的落盘出口（对账 TS 的 `deps.archiveHistory`）。
//
// 返回 artifact id；空串表示失败（fail-soft）。
type ArchiveSink func(input artifact.SaveInput) (string, error)

// BuildArchiveDiscarded 构造 `CheckpointDeps.ArchiveDiscarded` 的实现。
//
// 对账 TS `archiveDiscardedHistory`。参数：
//   - sink：落盘出口（nil = 不可归档 → 恒返回空串）
//   - turnOf：返回当前轮次（供 summary/target 标注）
//   - onArchive：归档成功后的回调（可 nil）
//
// 返回的函数签名与 `agent.CheckpointDeps.ArchiveDiscarded` 一致：
// 接收丢弃段与原因，返回**追加到摘要末尾的召回引用块**（空串 = 不追加）。
//
// # 四条早退（对账 TS，全部 fail-soft）
//
//  1. sink 为 nil → 不可归档
//  2. 丢弃段为空 → 无内容可归档
//  3. 序列化后正文 trim 后为空 → 不值得归档
//  4. 落盘失败 → 不阻塞压缩
func BuildArchiveDiscarded(
	sink ArchiveSink,
	turnOf func() int,
	onArchive func(artifactID string, turn int),
) func(discarded []session.OaiMessage, reason string) string {
	return func(discarded []session.OaiMessage, reason string) string {
		if sink == nil {
			return ""
		}
		if len(discarded) == 0 {
			return ""
		}

		serialized := SerializeMessagesForArchive(discarded)
		if trimSpace(serialized.RawContent) == "" {
			return ""
		}

		turn := 0
		if turnOf != nil {
			turn = turnOf()
		}

		id, err := sink(artifact.SaveInput{
			Tool:       CompactHistoryTool,
			Target:     "session-history@turn" + strconv.Itoa(turn),
			RawContent: serialized.RawContent,
			Summary: "compacted " + strconv.Itoa(len(discarded)) + " messages at turn " +
				strconv.Itoa(turn) + " (" + reason + ")",
			Sections: serialized.Sections,
		})
		if err != nil || id == "" {
			return "" // fail-soft
		}

		if onArchive != nil {
			onArchive(id, turn)
		}
		catalog := BuildArchiveCatalog(serialized.TurnRanges, id)
		return BuildRecallRefBlock(id, len(discarded), catalog)
	}
}

// trimSpace 是 strings.TrimSpace 的薄包装（保持本文件自足）。
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && isSpaceByte(s[start]) {
		start++
	}
	for end > start && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
