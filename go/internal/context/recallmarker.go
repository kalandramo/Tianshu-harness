package context

// recallmarker.go —— compact-history 召回标记。
//
// 对账 TS `src/compact/recall-marker.ts`。

import "regexp"

// recallMarkerRe 匹配消息开头的召回标记。
//
// 对账 TS 的 `RECALL_MARKER_RE`：
//
//	/^\[recalled (compact-history:[^\s\]]+) (L\d+-L\d+|c\d+-c\d+)\]/
//
// **ASCII-only**：避免 Unicode（↺）的跨平台/存储边界问题，也避免模型输出
// 恰好含该字形时的误匹配。
var recallMarkerRe = regexp.MustCompile(`^\[recalled (compact-history:[^\s\]]+) (L\d+-L\d+|c\d+-c\d+)\]`)

// RecallMarker 是解析出的召回标记。
type RecallMarker struct {
	ArtifactID string
	Section    string
}

// BuildRecallMarker 构造前置在召回内容前的单行标记。
//
// 对账 TS `buildRecallMarker`。
func BuildRecallMarker(artifactID, section string) string {
	return "[recalled " + artifactID + " " + section + "]"
}

// ParseRecallMarker 从消息内容的开头解析召回标记。
//
// 对账 TS `parseRecallMarker`。内容不是召回块时返回 nil。
func ParseRecallMarker(content string) *RecallMarker {
	m := recallMarkerRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	return &RecallMarker{ArtifactID: m[1], Section: m[2]}
}
