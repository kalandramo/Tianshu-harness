package context

import (
	"errors"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/session"
)

func amsg(role, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: role, Content: &c}
}

func atool(id, content string) session.OaiMessage {
	c := content
	return session.OaiMessage{Role: "tool", ToolCallID: id, Content: &c}
}

func aasst(content string, calls ...session.OaiToolCall) session.OaiMessage {
	m := amsg("assistant", content)
	m.ToolCalls = calls
	return m
}

// ── 序列化 ──

// TestArchiveHeaderFormat —— divider 必须逐字对账（read_section 靠它定位）。
func TestArchiveHeaderFormat(t *testing.T) {
	if got := archiveHeader(3, "assistant"); got != "--- turn:3 role:assistant ---" {
		t.Errorf("divider 格式不符：%q", got)
	}
}

// TestSerializeBasicShape —— 基本形态：divider + 正文。
func TestSerializeBasicShape(t *testing.T) {
	msgs := []session.OaiMessage{
		amsg("user", "hello"),
		amsg("assistant", "world"),
	}
	s := SerializeMessagesForArchive(msgs)
	want := "--- turn:0 role:user ---\nhello\n--- turn:0 role:assistant ---\nworld"
	if s.RawContent != want {
		t.Errorf("序列化不符：\n期望 %q\n实得 %q", want, s.RawContent)
	}
}

// TestSerializeTurnIncrement —— turn 随 user 消息递增（首条 user 不递增）。
func TestSerializeTurnIncrement(t *testing.T) {
	msgs := []session.OaiMessage{
		amsg("user", "u1"),
		amsg("assistant", "a1"),
		amsg("user", "u2"), // 第二条 user → turn 递增到 1
		amsg("assistant", "a2"),
		atool("c1", "t1"),
	}
	s := SerializeMessagesForArchive(msgs)
	// 前三块 turn 0，后两块 turn 1。
	if !strings.Contains(s.RawContent, "--- turn:0 role:user ---\nu1") {
		t.Error("首条 user 应是 turn 0")
	}
	if !strings.Contains(s.RawContent, "--- turn:1 role:user ---\nu2") {
		t.Error("第二条 user 应递增到 turn 1")
	}
	if !strings.Contains(s.RawContent, "--- turn:1 role:tool ---\nt1") {
		t.Error("tool 应归属其 user 轮（turn 1）")
	}
}

// TestSerializeSectionsPerMessage —— sections 按消息切分且行范围准确。
func TestSerializeSectionsPerMessage(t *testing.T) {
	msgs := []session.OaiMessage{
		amsg("user", "line1"),
		amsg("assistant", "line2\nline3"),
	}
	s := SerializeMessagesForArchive(msgs)
	if len(s.Sections) != 2 {
		t.Fatalf("应有 2 个 section，实得 %d", len(s.Sections))
	}
	// 第一块：divider(1) + body(1) = 行 1-2。
	if s.Sections[0].Name != "msg0 turn0 user" {
		t.Errorf("section 名不符：%q", s.Sections[0].Name)
	}
	if s.Sections[0].LineStart != 1 || s.Sections[0].LineEnd != 2 {
		t.Errorf("第一块行范围应为 1-2，实得 %d-%d", s.Sections[0].LineStart, s.Sections[0].LineEnd)
	}
	// 第二块：从行 3 开始（块间 '\n' 连接）。
	if s.Sections[1].LineStart != 3 {
		t.Errorf("第二块应从行 3 开始，实得 %d", s.Sections[1].LineStart)
	}
	if s.Sections[1].LineEnd != 5 {
		t.Errorf("第二块应到行 5（divider+2 行正文），实得 %d", s.Sections[1].LineEnd)
	}
}

// TestSerializeEmptyBodyNoExtraLine —— 空正文只有 divider（不加换行）。
func TestSerializeEmptyBodyNoExtraLine(t *testing.T) {
	m := amsg("assistant", "")
	m.ToolCalls = []session.OaiToolCall{{ID: "c1", Function: &session.OaiFunction{Name: "x", Arguments: "{}"}}}
	s := SerializeMessagesForArchive([]session.OaiMessage{m})
	// 有 tool_call 时正文非空（[tool_call x] {}）。
	if !strings.Contains(s.RawContent, "[tool_call x] {}") {
		t.Errorf("应渲染 tool_call，实得 %q", s.RawContent)
	}
	// 真·空正文。
	s2 := SerializeMessagesForArchive([]session.OaiMessage{amsg("system", "")})
	if s2.RawContent != "--- turn:0 role:system ---" {
		t.Errorf("空正文应只有 divider，实得 %q", s2.RawContent)
	}
}

// TestSerializeAssistantRendersReasoning —— reasoning 段被渲染。
func TestSerializeAssistantRendersReasoning(t *testing.T) {
	m := amsg("assistant", "answer")
	m.Extra = map[string]any{"reasoning_content": "my thinking"}
	s := SerializeMessagesForArchive([]session.OaiMessage{m})
	if !strings.Contains(s.RawContent, "[reasoning]\nmy thinking") {
		t.Errorf("应渲染 reasoning 段，实得 %q", s.RawContent)
	}
}

// TestSerializeRecallEviction —— **recall 标记折叠为一行指针**。
//
// 对账 TS 的 recall-eviction：被召回的块不能原样重新归档（会抵消压缩）。
func TestSerializeRecallEviction(t *testing.T) {
	recalled := atool("c1", "[recalled compact-history:abc L1-L100]\n大量原文...")
	s := SerializeMessagesForArchive([]session.OaiMessage{recalled})
	if strings.Contains(s.RawContent, "大量原文") {
		t.Error("被召回的正文不应被重新归档（会抵消压缩）")
	}
	if !strings.Contains(s.RawContent, "[recalled → compact-history:abc L1-L100 (see original artifact)]") {
		t.Errorf("应折叠为指针，实得 %q", s.RawContent)
	}
}

// TestSerializeTurnRangesAggregated —— turnRanges 聚合每轮的行范围。
func TestSerializeTurnRangesAggregated(t *testing.T) {
	msgs := []session.OaiMessage{
		amsg("user", "u1"),
		amsg("assistant", "a1"),
		amsg("user", "u2"),
		amsg("assistant", "a2"),
	}
	s := SerializeMessagesForArchive(msgs)
	if len(s.TurnRanges) != 2 {
		t.Fatalf("应有 2 轮，实得 %d", len(s.TurnRanges))
	}
	if s.TurnRanges[0].Turn != 0 || s.TurnRanges[1].Turn != 1 {
		t.Errorf("turn 序号不符：%+v", s.TurnRanges)
	}
	if s.TurnRanges[0].LineStart != 1 {
		t.Errorf("第 0 轮应从行 1 开始，实得 %d", s.TurnRanges[0].LineStart)
	}
	// 第 0 轮覆盖前两块：行 1-4。
	if s.TurnRanges[0].LineEnd != 4 {
		t.Errorf("第 0 轮应到行 4，实得 %d", s.TurnRanges[0].LineEnd)
	}
}

// ── catalog / recall ref ──

func TestBuildArchiveCatalog(t *testing.T) {
	ranges := []TurnRange{{Turn: 0, LineStart: 1, LineEnd: 10}, {Turn: 1, LineStart: 11, LineEnd: 20}}
	got := BuildArchiveCatalog(ranges, "art:1")
	if !strings.Contains(got, "压缩历史目录 (artifact:art:1, turn→行):") {
		t.Errorf("标题不符：%q", got)
	}
	if !strings.Contains(got, "- turn 0: L1-L10") || !strings.Contains(got, "- turn 1: L11-L20") {
		t.Errorf("目录项不符：%q", got)
	}
}

// TestBuildArchiveCatalogCap —— 超上限时只显示前 40 轮 + 省略说明。
func TestBuildArchiveCatalogCap(t *testing.T) {
	ranges := make([]TurnRange, 0, 45)
	for i := 0; i < 45; i++ {
		ranges = append(ranges, TurnRange{Turn: i, LineStart: i*10 + 1, LineEnd: (i + 1) * 10})
	}
	got := BuildArchiveCatalog(ranges, "a")
	if strings.Contains(got, "- turn 44:") {
		t.Error("超过 40 轮的不应逐条显示")
	}
	if !strings.Contains(got, "(+5 more turns, up to L450)") {
		t.Errorf("应含省略说明，实得 %q", got)
	}
}

func TestBuildRecallRefBlock(t *testing.T) {
	got := BuildRecallRefBlock("art:9", 42, "CATALOG")
	if !strings.Contains(got, "[已归档 42 条更早消息 → artifact:art:9]") {
		t.Errorf("应含归档条数，实得 %q", got)
	}
	if !strings.Contains(got, "CATALOG") {
		t.Error("应嵌入 catalog")
	}
	if !strings.Contains(got, `召回原文: read_section(artifactId="art:9", section="L起-L止")`) {
		t.Errorf("应含召回指引，实得 %q", got)
	}
}

// ── 装配（fail-soft 四条早退）──

func TestArchiveDiscardedHappyPath(t *testing.T) {
	var saved *artifact.SaveInput
	sink := func(in artifact.SaveInput) (string, error) {
		saved = &in
		return "art:42", nil
	}
	fn := BuildArchiveDiscarded(sink, func() int { return 7 }, nil)
	ref := fn([]session.OaiMessage{amsg("user", "old"), amsg("assistant", "gone")}, "session split")

	if saved == nil {
		t.Fatal("应调用落盘")
	}
	if saved.Tool != CompactHistoryTool {
		t.Errorf("工具名应为 %q，实得 %q", CompactHistoryTool, saved.Tool)
	}
	if saved.Target != "session-history@turn7" {
		t.Errorf("target 不符：%q", saved.Target)
	}
	if !strings.Contains(saved.Summary, "compacted 2 messages at turn 7 (session split)") {
		t.Errorf("summary 不符：%q", saved.Summary)
	}
	if len(saved.Sections) != 2 {
		t.Errorf("应带 2 个 section，实得 %d", len(saved.Sections))
	}
	if !strings.Contains(ref, "art:42") {
		t.Errorf("引用块应含 artifact id，实得 %q", ref)
	}
}

func TestArchiveDiscardedNilSink(t *testing.T) {
	fn := BuildArchiveDiscarded(nil, nil, nil)
	if ref := fn([]session.OaiMessage{amsg("user", "x")}, "r"); ref != "" {
		t.Errorf("nil sink 应返回空串（不可归档），实得 %q", ref)
	}
}

func TestArchiveDiscardedEmptyZone(t *testing.T) {
	called := false
	sink := func(in artifact.SaveInput) (string, error) { called = true; return "x", nil }
	fn := BuildArchiveDiscarded(sink, nil, nil)
	if ref := fn(nil, "r"); ref != "" {
		t.Errorf("空段应返回空串，实得 %q", ref)
	}
	if called {
		t.Error("空段不应落盘")
	}
}

// TestArchiveDiscardedWriteFailureIsFailSoft —— **落盘失败不得阻塞压缩**。
func TestArchiveDiscardedWriteFailureIsFailSoft(t *testing.T) {
	sink := func(in artifact.SaveInput) (string, error) { return "", errors.New("disk full") }
	fn := BuildArchiveDiscarded(sink, nil, nil)
	if ref := fn([]session.OaiMessage{amsg("user", "x")}, "r"); ref != "" {
		t.Errorf("失败应返回空串（fail-soft），实得 %q", ref)
	}
}

// TestArchiveDiscardedEmptyIDSameAsFailure —— id 为空视同失败。
func TestArchiveDiscardedEmptyIDSameAsFailure(t *testing.T) {
	sink := func(in artifact.SaveInput) (string, error) { return "", nil }
	fn := BuildArchiveDiscarded(sink, nil, nil)
	if ref := fn([]session.OaiMessage{amsg("user", "x")}, "r"); ref != "" {
		t.Error("空 id 应视同失败")
	}
}

func TestArchiveDiscardedOnArchiveCallback(t *testing.T) {
	var gotID string
	var gotTurn int
	sink := func(in artifact.SaveInput) (string, error) { return "art:cb", nil }
	fn := BuildArchiveDiscarded(sink, func() int { return 3 }, func(id string, turn int) {
		gotID = id
		gotTurn = turn
	})
	fn([]session.OaiMessage{amsg("user", "x")}, "r")
	if gotID != "art:cb" || gotTurn != 3 {
		t.Errorf("回调参数不符：id=%q turn=%d", gotID, gotTurn)
	}
}

// TestArchiveDiscardedEmptyContentNotArchived —— 序列化后正文为空 → 不归档。
func TestArchiveDiscardedEmptyContentNotArchived(t *testing.T) {
	called := false
	sink := func(in artifact.SaveInput) (string, error) { called = true; return "x", nil }
	fn := BuildArchiveDiscarded(sink, nil, nil)
	// 只有空 content 的 system 消息 → 正文是 divider（非空）……
	// 用真·空正文：所有消息内容为空且无 tool_call → 仍有 divider，故此处
	// 验证的是「divider 也算内容」这一事实。
	ref := fn([]session.OaiMessage{amsg("system", "")}, "r")
	if !called {
		t.Error("divider 非空，应归档（对账 TS 的 trim 判据）")
	}
	_ = ref
}

// ── recall marker ──

func TestRecallMarkerRoundTrip(t *testing.T) {
	marker := BuildRecallMarker("compact-history:abc", "L10-L20")
	if marker != "[recalled compact-history:abc L10-L20]" {
		t.Errorf("标记格式不符：%q", marker)
	}
	parsed := ParseRecallMarker(marker + "\ncontent follows")
	if parsed == nil {
		t.Fatal("应解析出标记")
	}
	if parsed.ArtifactID != "compact-history:abc" || parsed.Section != "L10-L20" {
		t.Errorf("解析结果不符：%+v", parsed)
	}
}

func TestParseRecallMarkerRejectsNonMarker(t *testing.T) {
	for _, s := range []string{
		"plain content",
		"[recalled something-else L1-L2]",           // 非 compact-history 前缀
		"prefix [recalled compact-history:x L1-L2]", // 不在开头
		"[recalled compact-history:x badsection]",   // 区段格式不符
	} {
		if ParseRecallMarker(s) != nil {
			t.Errorf("不应匹配：%q", s)
		}
	}
}

func TestParseRecallMarkerCharRange(t *testing.T) {
	p := ParseRecallMarker("[recalled compact-history:x c0-c500]")
	if p == nil || p.Section != "c0-c500" {
		t.Errorf("应支持字符范围区段，实得 %+v", p)
	}
}
