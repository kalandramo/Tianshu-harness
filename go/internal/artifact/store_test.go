package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ts 是固定的时间戳（确定性 oracle）。
const ts = int64(1_700_000_000_000)

// newTestStore 构造带确定性时钟与 id 生成器的存储。
//
// **为什么确定性**：`CreatedAt` 与 id 进索引文件——固定它们让测试可断言
// 字节，也避免 flaky。
func newTestStore(t *testing.T, sessionID string) (*Store, string) {
	t.Helper()
	base := t.TempDir()
	n := 0
	s := NewStore(base, sessionID, Options{
		Now: func() int64 { return ts },
		IDGenerator: func() string {
			n++
			return "id" + itoa(n)
		},
	})
	return s, base
}

func TestSaveAndGet(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	id, err := s.Save(SaveInput{
		Tool: "read_file", Target: "src/a.ts", RawContent: "line1\nline2\n", Summary: "[read_file src/a.ts] 2 lines.",
	})
	if err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	if id != "read_file:id1" {
		t.Errorf("id 应为 read_file:id1，实得 %q", id)
	}
	a := s.Get(id)
	if a == nil {
		t.Fatal("Get 应取回记录")
	}
	if a.Tool != "read_file" || a.Target != "src/a.ts" || a.SessionID != "sess1" {
		t.Errorf("字段不符：%+v", a)
	}
	if a.CharCount != 12 {
		t.Errorf("CharCount(对账 TS content.length)：期望 12，实得 %d", a.CharCount)
	}
	// "line1\nline2\n" → split('\n') = ["line1","line2",""] → 3
	if a.LineCount != 3 {
		t.Errorf("LineCount(对账 TS split)：期望 3，实得 %d", a.LineCount)
	}
	if a.Sections == nil {
		t.Error("Sections 应为空切片（非 nil）——nil 序列化成 null")
	}
}

// TestCharCountIsUTF16 —— **关键对账**：TS 的 `content.length` 是 UTF-16 code unit 数。
//
// 实测：`"a😀b"` 在 Node 里 length=4（码点 3、字节 6）。
func TestCharCountIsUTF16(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "x", Target: "t", RawContent: "a😀b", Summary: "s"})
	if a := s.Get(id); a.CharCount != 4 {
		t.Errorf("CharCount 应为 UTF-16 code unit 数 4（码点 3 / 字节 6），实得 %d", a.CharCount)
	}
}

// TestPersistsToAppendOnlyIndex —— 重启后能从索引恢复。
func TestPersistsToAppendOnlyIndex(t *testing.T) {
	s, base := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "grep", Target: "p", RawContent: "hit", Summary: "1 match"})

	indexPath := filepath.Join(base, "sess1", "_index.jsonl")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("索引文件应存在：%v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("索引每行应以换行结尾（append-only 契约）")
	}
	// 逐行可解析。
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("应有 1 行，实得 %d", len(lines))
	}
	var parsed Artifact
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("索引行应可解析：%v", err)
	}
	if parsed.ID != id {
		t.Errorf("索引记录 id 不符：%q vs %q", parsed.ID, id)
	}

	// 新存储（模拟重启）应恢复。
	s2 := NewStore(base, "sess1", Options{Now: func() int64 { return ts }, IDGenerator: func() string { return "x" }})
	if a := s2.Get(id); a == nil {
		t.Error("重启后应从索引恢复 artifact")
	}
}

// TestLoadIndexSkipsMalformedLines —— 一行坏不能让其余不可读。
func TestLoadIndexSkipsMalformedLines(t *testing.T) {
	s, base := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "a", Target: "t", RawContent: "x", Summary: "s"})

	// 在索引尾部追加畸形行 + 另一条好记录。
	good := Artifact{
		ID: "b:idX", Tool: "b", Target: "t2", SessionID: "sess1",
		RawPath: "/tmp/x.raw", Summary: "s2", SHA256: "deadbeef",
	}
	gl, _ := json.Marshal(good)
	indexPath := filepath.Join(base, "sess1", "_index.jsonl")
	f, _ := os.OpenFile(indexPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{not json at all\n")
	f.Write(append(gl, '\n'))
	f.Close()

	s2 := NewStore(base, "sess1", Options{})
	if s2.Get(id) == nil {
		t.Error("畸形行不应影响既有记录")
	}
	if s2.Get("b:idX") == nil {
		t.Error("畸形行之后的合法记录应被恢复")
	}
}

func TestReadRawVerifiesIntegrity(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "x", Target: "t", RawContent: "hello", Summary: "s"})

	got, err := s.ReadRaw(id)
	if err != nil || got != "hello" {
		t.Fatalf("ReadRaw：期望 hello，实得 %q err=%v", got, err)
	}

	// 篡改原文 → 必须抛 CorruptionError。
	a := s.Get(id)
	if err := os.WriteFile(a.RawPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadRaw(id)
	if err == nil {
		t.Fatal("篡改后 ReadRaw 应报错")
	}
	ce, ok := err.(*CorruptionError)
	if !ok {
		t.Fatalf("应为 *CorruptionError，实得 %T", err)
	}
	if ce.ArtifactID != id {
		t.Errorf("错误应携带 artifactID，实得 %q", ce.ArtifactID)
	}
	// 消息逐字对账 TS。
	want := "Artifact " + id + " raw content is corrupted; re-read source"
	if ce.Error() != want {
		t.Errorf("错误文案不符：\n期望 %q\n实得 %q", want, ce.Error())
	}
}

func TestUnknownArtifactReturnsNil(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	if a := s.Get("nope"); a != nil {
		t.Error("未知 id 应返回 nil")
	}
	if raw, err := s.ReadRaw("nope"); raw != "" || err != nil {
		t.Errorf("未知 id 的 ReadRaw 应返回 (\"\", nil)，实得 (%q, %v)", raw, err)
	}
	if r, err := s.ReadLineRange("nope", 1, 10); r != nil || err != nil {
		t.Errorf("未知 id 的 ReadLineRange 应返回 (nil, nil)，实得 (%v, %v)", r, err)
	}
}

func TestListByTarget(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	s.Save(SaveInput{Tool: "read_file", Target: "same.ts", RawContent: "a", Summary: "s1"})
	s.Save(SaveInput{Tool: "read_file", Target: "same.ts", RawContent: "b", Summary: "s2"})
	s.Save(SaveInput{Tool: "bash", Target: "other", RawContent: "c", Summary: "s3"})

	got := s.ListByTarget("same.ts")
	if len(got) != 2 {
		t.Errorf("应命中 2 条，实得 %d", len(got))
	}
	if len(s.List()) != 3 {
		t.Errorf("List 应返回 3 条，实得 %d", len(s.List()))
	}
}

func TestReadLines(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "x", Target: "t", RawContent: "L1\nL2\nL3\nL4\nL5", Summary: "s"})

	got, found, err := s.ReadLines(id, 2, 4)
	if err != nil || !found {
		t.Fatalf("ReadLines 失败：%v found=%v", err, found)
	}
	if got != "L2\nL3\nL4" {
		t.Errorf("期望 L2\\nL3\\nL4，实得 %q", got)
	}
	// 越界 end 被截断（JS slice 语义）。
	got, _, _ = s.ReadLines(id, 3, 100)
	if got != "L3\nL4\nL5" {
		t.Errorf("越界 end 应截断，实得 %q", got)
	}
	// start 越界 → 空。
	got, _, _ = s.ReadLines(id, 100, 200)
	if got != "" {
		t.Errorf("start 越界应返回空，实得 %q", got)
	}
}

func TestReadLineRangeCap(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	// 构造 MaxRangeLines+100 行。
	var b strings.Builder
	for i := 1; i <= MaxRangeLines+100; i++ {
		b.WriteString("L" + itoa(i))
		if i < MaxRangeLines+100 {
			b.WriteString("\n")
		}
	}
	id, _ := s.Save(SaveInput{Tool: "x", Target: "t", RawContent: b.String(), Summary: "s"})

	// 请求超上限 → capped，且只返回 MaxRangeLines 行。
	r, err := s.ReadLineRange(id, 1, MaxRangeLines+50)
	if err != nil {
		t.Fatalf("ReadLineRange 失败：%v", err)
	}
	if !r.Capped {
		t.Error("超上限请求应 capped=true")
	}
	lines := strings.Split(r.Content, "\n")
	if len(lines) != MaxRangeLines {
		t.Errorf("应返回恰好 %d 行，实得 %d", MaxRangeLines, len(lines))
	}
	if lines[0] != "L1" || lines[len(lines)-1] != "L"+itoa(MaxRangeLines) {
		t.Errorf("首尾行不符：%q ... %q", lines[0], lines[len(lines)-1])
	}
}

func TestReadLineRangeUncapped(t *testing.T) {
	s, _ := newTestStore(t, "sess1")
	id, _ := s.Save(SaveInput{Tool: "x", Target: "t", RawContent: "a\nb\nc\nd\ne", Summary: "s"})
	r, err := s.ReadLineRange(id, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Capped {
		t.Error("小窗口不应 capped")
	}
	if r.Content != "b\nc" {
		t.Errorf("期望 b\\nc，实得 %q", r.Content)
	}
}

// ── fallback 会话 ──

func TestFallbackSessionResolves(t *testing.T) {
	s, base := newTestStore(t, "main")
	// worker 会话产出一条。
	worker := NewStore(base, "worker-1", Options{Now: func() int64 { return ts }, IDGenerator: func() string { return "w1" }})
	wide, _ := worker.Save(SaveInput{Tool: "x", Target: "t", RawContent: "from worker", Summary: "s"})

	// 主存储初始解析不到。
	if s.Get(wide) != nil {
		t.Error("未注册 fallback 时不应解析到")
	}
	s.AddFallbackSession("worker-1")
	a := s.Get(wide)
	if a == nil {
		t.Fatal("注册 fallback 后应解析到")
	}
	// rawPath 必须 re-anchor 到 fallback 目录（否则读取指向错误路径）。
	wantPath := filepath.Join(base, "worker-1", safeArtifactFileStem(wide)+".raw")
	if a.RawPath != wantPath {
		t.Errorf("rawPath 应 re-anchor：期望 %q，实得 %q", wantPath, a.RawPath)
	}
	// 能真的读出内容。
	raw, err := s.ReadRaw(wide)
	if err != nil || raw != "from worker" {
		t.Errorf("fallback 读取应有内容：%q err=%v", raw, err)
	}
}

func TestFallbackDedup(t *testing.T) {
	s, _ := newTestStore(t, "main")
	s.AddFallbackSession("w")
	s.AddFallbackSession("w")
	if len(s.fallbackSessionIDs) != 1 {
		t.Errorf("重复注册应去重，实得 %d", len(s.fallbackSessionIDs))
	}
}

func TestFallbackUnknownIgnored(t *testing.T) {
	s, _ := newTestStore(t, "main")
	s.AddFallbackSession("does-not-exist")
	if s.Get("x:y") != nil {
		t.Error("不存在的 fallback 会话不应报错，只返回 nil")
	}
}

// ── cleanup ──

func TestCleanupNeverDeletesActive(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"active", "old1", "old2"} {
		os.MkdirAll(filepath.Join(base, name), 0o755)
	}
	cleaned := CleanupOldSessions(base, "active")
	// 全部都很新（未超 TTL），且无 active 之外的删除。
	if cleaned != 0 {
		t.Errorf("新鲜目录不应被删，实得 %d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(base, "active")); err != nil {
		t.Error("active 会话目录必须保留")
	}
}

func TestCleanupRemovesExcessOldest(t *testing.T) {
	base := t.TempDir()
	// 造 maxArtifactSessions+2 个目录，按 mtime 升序赋值。
	//
	// **mtime 必须落在 TTL 窗口内**——首版用固定的过去时间戳（ts 常量），
	// 结果所有目录都判为过期，删了 51 个而非 2 个。这不是实现 bug，是测试
	// 构造缺陷：TTL 是相对 time.Now() 的，测试必须给相对当前的时间。
	total := maxArtifactSessions + 2
	nowMs := time.Now().UnixMilli()
	for i := 0; i < total; i++ {
		p := filepath.Join(base, "s"+itoa(i))
		os.MkdirAll(p, 0o755)
		// 全部在 1 小时前以内（远新于 7 天 TTL），越早的 i 越旧。
		mod := nowMs - int64(total-i)*1000
		os.Chtimes(p, unixToTime(mod), unixToTime(mod))
	}
	cleaned := CleanupOldSessions(base, "s"+itoa(total-1))
	// **算清期望**：total=52 个目录，排除 active 后剩 **51** 个候选。
	// 条件 `len(dirs)-cleaned > 50`：cleaned=0 时 51>50 成立 → 删第 1 个；
	// cleaned=1 时 50>50 不成立 → 停。故删除数是 **1**（不是 2）。
	// 首版期望写 2 是没算清 active 被排除这一步。
	if cleaned != 1 {
		t.Errorf("应删除 1 个（51 候选 - 50 上限），实得 %d", cleaned)
	}
	if _, err := os.Stat(filepath.Join(base, "s0")); err == nil {
		t.Error("最旧的 s0 应被删")
	}
	if _, err := os.Stat(filepath.Join(base, "s"+itoa(total-1))); err != nil {
		t.Error("active 会话必须保留")
	}
}

// TestCleanupRemovesExpired —— 超 TTL 的目录必须删（即使数量未超上限）。
func TestCleanupRemovesExpired(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, "ancient")
	os.MkdirAll(old, 0o755)
	// 30 天前（远超 7 天 TTL）。
	mod := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(old, mod, mod)

	if cleaned := CleanupOldSessions(base, "active"); cleaned != 1 {
		t.Errorf("超 TTL 的目录应被删，实得 %d", cleaned)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("超期目录应被删除")
	}
}

// ── FormatArtifactRef ──

func TestFormatArtifactRef(t *testing.T) {
	got := FormatArtifactRef(ArtifactRef{
		ArtifactID: "a:1", Summary: "[read_file a.ts] 10 lines.",
		CharCount: 100, LineCount: 10, Sections: []string{"imports", "exports"},
	})
	want := "[100 chars, 10 lines] Sections: imports, exports. [read_file a.ts] 10 lines. (use read_section to expand)"
	if got != want {
		t.Errorf("格式不符：\n期望 %q\n实得 %q", want, got)
	}
	// 无 sections → 无该段。
	got = FormatArtifactRef(ArtifactRef{Summary: "s", CharCount: 1, LineCount: 1})
	want = "[1 chars, 1 lines] s (use read_section to expand)"
	if got != want {
		t.Errorf("无 sections 格式不符：\n期望 %q\n实得 %q", want, got)
	}
}

// unixToTime 把毫秒时间戳转为 time.Time（cleanup 测试设置 mtime 用）。
func unixToTime(ms int64) time.Time {
	return time.UnixMilli(ms)
}
