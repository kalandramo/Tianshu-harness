package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	ctxstore "github.com/kalandramo/tianshu/go/internal/context"
)

// newCompactHistoryStore 构造带一条 compact-history artifact 的存储。
func newCompactHistoryStore(t *testing.T, content string) (*artifact.Store, string) {
	t.Helper()
	s := artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "ch" },
	})
	id, err := s.Save(artifact.SaveInput{
		Tool: ctxstore.CompactHistoryTool, Target: "session-history@turn5",
		RawContent: content, Summary: "compacted N messages",
	})
	if err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	return s, id
}

// TestReadSectionCompactHistoryStreamsBeyond2MB —— **本分支存在的理由**。
//
// 长线程归档常超 2MB 内存上限——那会让归档自己的目录项无法被召回
// （存得下、取不回）。流式分支不过 2MB 闸门。
func TestReadSectionCompactHistoryStreamsBeyond2MB(t *testing.T) {
	// 造 3MB 内容（超过 maxRawBytes=2MB）。
	var b strings.Builder
	line := strings.Repeat("x", 1000) + "\n"
	for i := 0; i < 3200; i++ { // 约 3.2MB
		b.WriteString(line)
	}
	big := b.String()
	if len(big) <= maxRawBytes {
		t.Fatalf("测试构造失败：需超 %d 字节，实得 %d", maxRawBytes, len(big))
	}

	store, id := newCompactHistoryStore(t, big)
	tool := ReadSection()

	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L3"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("超 2MB 的 compact-history 应能流式召回，实得错误：%s", res.Content)
	}
	if !strings.Contains(res.Content, "xxxx") {
		t.Errorf("应取回内容，实得 %q", res.Content[:minI2(100, len(res.Content))])
	}
}

// TestReadSectionNonCompactHistoryStillGated —— 普通 artifact 仍受 2MB 闸门。
//
// 流式分支**只对 compact-history 生效**——否则大工具结果会被无声地部分返回。
func TestReadSectionNonCompactHistoryStillGated(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3200; i++ {
		b.WriteString(strings.Repeat("y", 1000) + "\n")
	}
	s := artifact.NewStore(t.TempDir(), "s1", artifact.Options{
		Now: func() int64 { return 1 }, IDGenerator: func() string { return "x" },
	})
	id, _ := s.Save(artifact.SaveInput{Tool: "read_file", Target: "a.ts", RawContent: b.String(), Summary: "s"})

	tool := ReadSection()
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "L1-L3"}, ArtifactStore: s,
	})
	if !res.IsError {
		t.Error("非 compact-history 的大 artifact 应仍被 2MB 闸门拦截")
	}
	if !strings.Contains(res.Content, "过大") {
		t.Errorf("应报过大错误，实得 %q", res.Content)
	}
}

// TestReadSectionCompactHistoryCharRangeFallsThrough —— 字符范围不走流式分支。
//
// 字符范围需全文（无法流式定位），故落回通用路径——大文件会被闸门拦。
func TestReadSectionCompactHistoryCharRangeFallsThrough(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3200; i++ {
		b.WriteString(strings.Repeat("z", 1000) + "\n")
	}
	store, id := newCompactHistoryStore(t, b.String())
	tool := ReadSection()
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"artifactId": id, "section": "c0-c100"}, ArtifactStore: store,
	})
	// 走通用路径 → 撞 2MB 闸门。
	if !res.IsError || !strings.Contains(res.Content, "过大") {
		t.Errorf("字符范围应落回通用路径（受闸门），实得 %q", res.Content)
	}
}

// TestReadSectionCompactHistoryPrefixesRecallMarker —— 结果前置召回标记。
//
// 标记让**下一次压缩**能把这块折叠回指针（recall-eviction）。
func TestReadSectionCompactHistoryPrefixesRecallMarker(t *testing.T) {
	store, id := newCompactHistoryStore(t, "--- turn:0 role:user ---\nhello\n--- turn:0 role:assistant ---\nworld")
	tool := ReadSection()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L2"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if err != nil || res.IsError {
		t.Fatalf("失败：%v / %s", err, res.Content)
	}
	// 首行应是召回标记。
	firstLine := strings.SplitN(res.Content, "\n", 2)[0]
	want := "[recalled " + id + " L1-L2]"
	if firstLine != want {
		t.Errorf("首行应是召回标记：\n期望 %q\n实得 %q", want, firstLine)
	}
	// 标记应能被 context 包解析（回环验证）。
	if p := ctxstore.ParseRecallMarker(res.Content); p == nil {
		t.Error("生成的标记应可被 ParseRecallMarker 解析")
	}
}

// TestReadSectionCompactHistoryCappedNotice —— 超 5000 行时附分页提示。
func TestReadSectionCompactHistoryCappedNotice(t *testing.T) {
	var b strings.Builder
	total := artifact.MaxRangeLines + 500
	for i := 0; i < total; i++ {
		b.WriteString("line\n")
	}
	store, id := newCompactHistoryStore(t, b.String())
	tool := ReadSection()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L" + itoaT(total)},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if err != nil || res.IsError {
		t.Fatalf("失败：%v / %s", err, res.Content)
	}
	if !strings.Contains(res.Content, "范围已限制为") {
		t.Errorf("超上限应附分页提示，实得尾部：%s", tailOf(res.Content, 120))
	}
}

// TestReadSectionCompactHistoryOutOfRange —— 起点越界报总行数（非错误）。
func TestReadSectionCompactHistoryOutOfRange(t *testing.T) {
	store, id := newCompactHistoryStore(t, "a\nb\nc")
	tool := ReadSection()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L100-L200"},
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if err != nil {
		t.Fatalf("不应返回 err：%v", err)
	}
	if res.IsError {
		t.Errorf("越界应是提示而非错误，实得 %q", res.Content)
	}
	if !strings.Contains(res.Content, "超出范围") || !strings.Contains(res.Content, "3 行") {
		t.Errorf("应报总行数，实得 %q", res.Content)
	}
}

// TestReadSectionCompactHistoryTruncatesAtMaxChars —— 超字符上限时截断。
func TestReadSectionCompactHistoryTruncatesAtMaxChars(t *testing.T) {
	// 小窗口 → maxChars 是 8000 地板。
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString(strings.Repeat("q", 200) + "\n") // 每行 200 字符
	}
	store, id := newCompactHistoryStore(t, b.String())
	tool := ReadSection()
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L100"},
		ArtifactStore: store,
		ContextWindow: 64_000, // 小窗口 → 地板 8000
	})
	if err != nil || res.IsError {
		t.Fatalf("失败：%v / %s", err, res.Content)
	}
	if !strings.Contains(res.Content, "已截断至 8000 字符") {
		t.Errorf("应截断到 8000（小窗口地板），实得尾部：%s", tailOf(res.Content, 120))
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func itoaT(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func minI2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
