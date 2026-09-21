package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bigText 造一段 modelBytes 超过 readRefThreshold(2048) 的文本。
func bigText() string {
	return strings.Repeat("const x = 1 // 中文注释占位\n", 200)
}

func writeBig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReadRefServesReferenceOnRepeat —— **核心**：重复读未变文件返回引用。
//
// TS 的 RIVET_READ_REF **默认开启**——Go 此前总是重发全文，是可观测的行为分歧。
func TestReadRefServesReferenceOnRepeat(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "") // 空 = 未设 = 默认开
	dir := t.TempDir()
	writeBig(t, dir, "a.ts", bigText())
	tool := ReadFile(dir, nil)

	p1 := call(dir, map[string]any{"file_path": "a.ts"})
	p1.SessionID = "s1"
	r1, _ := tool.Execute(t.Context(), p1)
	if r1.IsError {
		t.Fatalf("首读失败：%s", r1.Content)
	}
	if strings.Contains(r1.Content, "[read-ref]") {
		t.Errorf("首读不应有引用：%.200s", r1.Content)
	}

	p2 := call(dir, map[string]any{"file_path": "a.ts"})
	p2.SessionID = "s1"
	r2, _ := tool.Execute(t.Context(), p2)
	if !strings.Contains(r2.Content, "[read-ref]") {
		t.Fatalf("重复读应返回引用，实得：%.300s", r2.Content)
	}
	// 引用应含召回指引（对账 TS）。
	if !strings.Contains(r2.Content, "read_section(file_path=") {
		t.Errorf("引用应含 read_section 指引：%.300s", r2.Content)
	}
	// 引用应显著短于全文。
	if len(r2.Content) >= len(r1.Content)/2 {
		t.Errorf("引用应远短于全文：ref=%d full=%d", len(r2.Content), len(r1.Content))
	}
}

// TestReadRefDegradesAfterServedOnce —— degrade gate：引用发过仍被要 → 返回全文。
//
// 防死循环：引用没起作用（目标可能已被裁剪出请求视图）时降级为真实读取。
func TestReadRefDegradesAfterServedOnce(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "b.ts", bigText())
	tool := ReadFile(dir, nil)

	for i := 0; i < 2; i++ { // 首读 + 一次引用
		p := call(dir, map[string]any{"file_path": "b.ts"})
		p.SessionID = "s1"
		_, _ = tool.Execute(t.Context(), p)
	}

	p3 := call(dir, map[string]any{"file_path": "b.ts"})
	p3.SessionID = "s1"
	r3, _ := tool.Execute(t.Context(), p3)
	if strings.Contains(r3.Content, "[read-ref]") {
		t.Fatalf("第三次应降级为全文，实得：%.300s", r3.Content)
	}
	if !strings.Contains(r3.Content, "const x = 1") {
		t.Errorf("降级后应是真实内容：%.200s", r3.Content)
	}
}

// TestReadRefDisabledByEnv —— RIVET_READ_REF=0 时不返回引用（但仍发提醒）。
func TestReadRefDisabledByEnv(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "0")
	dir := t.TempDir()
	writeBig(t, dir, "c.ts", bigText())
	tool := ReadFile(dir, nil)

	for i := 0; i < 2; i++ {
		p := call(dir, map[string]any{"file_path": "c.ts"})
		p.SessionID = "s1"
		_, _ = tool.Execute(t.Context(), p)
	}
	p := call(dir, map[string]any{"file_path": "c.ts"})
	p.SessionID = "s1"
	r, _ := tool.Execute(t.Context(), p)
	if strings.Contains(r.Content, "[read-ref]") {
		t.Errorf("关闭时不应有引用：%.200s", r.Content)
	}
	// 提醒仍应出现（对账 TS：警告与引用是两个独立分支）。
	if !strings.Contains(r.Content, "read-dedup") {
		t.Errorf("关闭引用时仍应发提醒：%.300s", r.Content)
	}
}

// TestReadRefSkippedForSmallFile —— 小片段（<= 阈值）不引用，避免浪费往返。
func TestReadRefSkippedForSmallFile(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "small.ts", "const x = 1\n")
	tool := ReadFile(dir, nil)

	for i := 0; i < 3; i++ {
		p := call(dir, map[string]any{"file_path": "small.ts"})
		p.SessionID = "s1"
		r, _ := tool.Execute(t.Context(), p)
		if strings.Contains(r.Content, "[read-ref]") {
			t.Fatalf("小文件第 %d 次不应引用：%s", i+1, r.Content)
		}
		if !strings.Contains(r.Content, "const x = 1") {
			t.Errorf("应始终返回内容：%s", r.Content)
		}
	}
}

// TestRepeatReadNotDetectedAfterChange —— 文件变更后不判定为重复读。
func TestRepeatReadNotDetectedAfterChange(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	p := writeBig(t, dir, "d.ts", bigText())
	tool := ReadFile(dir, nil)

	c1 := call(dir, map[string]any{"file_path": "d.ts"})
	c1.SessionID = "s1"
	_, _ = tool.Execute(t.Context(), c1)

	// 改内容（size 变化）。
	if err := os.WriteFile(p, []byte(bigText()+"// extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := call(dir, map[string]any{"file_path": "d.ts"})
	c2.SessionID = "s1"
	r2, _ := tool.Execute(t.Context(), c2)
	if strings.Contains(r2.Content, "[read-ref]") {
		t.Errorf("变更后不应引用：%.200s", r2.Content)
	}
	if strings.Contains(r2.Content, "read-dedup") {
		t.Errorf("变更后不应有 dedup 提醒：%.200s", r2.Content)
	}
}

// TestInvalidateReadHistoryDropsRef —— 编辑后失效：重读返回全文。
func TestInvalidateReadHistoryDropsRef(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "e.ts", bigText())
	tool := ReadFile(dir, nil)

	p1 := call(dir, map[string]any{"file_path": "e.ts"})
	p1.SessionID = "s1"
	_, _ = tool.Execute(t.Context(), p1)

	p2 := call(dir, map[string]any{"file_path": "e.ts"})
	p2.SessionID = "s1"
	r2, _ := tool.Execute(t.Context(), p2)
	if !strings.Contains(r2.Content, "[read-ref]") {
		t.Fatalf("应先返回引用：%.200s", r2.Content)
	}

	// 模拟编辑成功后的失效。
	InvalidateReadHistory(filepath.Join(dir, "e.ts"))

	p3 := call(dir, map[string]any{"file_path": "e.ts"})
	p3.SessionID = "s1"
	r3, _ := tool.Execute(t.Context(), p3)
	if strings.Contains(r3.Content, "[read-ref]") {
		t.Errorf("失效后不应引用：%.200s", r3.Content)
	}
}

// TestSessionIsolation —— 表按 sessionId 隔离：并发会话不交叉污染。
func TestSessionIsolation(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "f.ts", bigText())
	tool := ReadFile(dir, nil)

	pa := call(dir, map[string]any{"file_path": "f.ts"})
	pa.SessionID = "sessA"
	_, _ = tool.Execute(t.Context(), pa)

	// 另一会话首读 → 不应有引用。
	pb := call(dir, map[string]any{"file_path": "f.ts"})
	pb.SessionID = "sessB"
	rb, _ := tool.Execute(t.Context(), pb)
	if strings.Contains(rb.Content, "[read-ref]") {
		t.Errorf("B 会话首读不应引用（表应按会话隔离）：%.200s", rb.Content)
	}
}

// TestInvalidateSessionReadDedup —— 清某会话的全部读去重记录。
func TestInvalidateSessionReadDedup(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "g.ts", bigText())
	tool := ReadFile(dir, nil)

	p1 := call(dir, map[string]any{"file_path": "g.ts"})
	p1.SessionID = "s1"
	_, _ = tool.Execute(t.Context(), p1)

	InvalidateSessionReadDedup("s1")

	p2 := call(dir, map[string]any{"file_path": "g.ts"})
	p2.SessionID = "s1"
	r2, _ := tool.Execute(t.Context(), p2)
	if strings.Contains(r2.Content, "[read-ref]") {
		t.Errorf("会话清空后不应引用：%.200s", r2.Content)
	}
	// 表2 应存活（它跟踪文件状态，不是历史存在性）——对账 TS 注释。
	if _, ok := GetFileReadMtime(filepath.Join(dir, "g.ts"), "s1"); !ok {
		t.Error("表2（lastKnownFileState）应不受 read-dedup 清空影响")
	}
}

// TestFileReadHistoryContainsPath —— **表1b「全文件包含」判定路径**。
//
// 场景：**跨调用形态**的重复读（表1a 键不同）——多读记表1b（文件级，无
// offset/limit），随后单读同文件（offset=1）应命中「全文件包含」。
//
// 缺此测试时，路径 2 永不触发（实测 M58 → 0 红）：我的其他测试都走「同切片」路径。
func TestFileReadHistoryContainsPath(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "j.ts", bigText())
	tool := ReadFile(dir, nil)

	// 1) 多读 → 写表1b（文件级）。
	pm := call(dir, map[string]any{"file_paths": []any{"j.ts"}})
	pm.SessionID = "s1"
	rm, _ := tool.Execute(t.Context(), pm)
	if rm.IsError {
		t.Skipf("多读不可用：%.200s", rm.Content)
	}
	if _, ok := lookupFileReadHistory("s1", filepath.Join(dir, "j.ts")); !ok {
		t.Fatal("多读（整文件读）必须写表1b")
	}

	// 2) 单读（offset=1, 无 limit）→ 表1a 键与多读不同，但表1b 应命中。
	ps := call(dir, map[string]any{"file_path": "j.ts"})
	ps.SessionID = "s1"
	rs, _ := tool.Execute(t.Context(), ps)
	if !strings.Contains(rs.Content, "[read-ref]") {
		t.Fatalf("表1b 全文件包含路径应触发引用，实得：%.300s", rs.Content)
	}
}

// TestFileReadHistoryWrittenForFullRead —— **整文件读必须写表1b**。
//
// 与上一条互补：上条断言分片读**不**写，这条断言整读**要**写。
// 缺这条时「表1b 永不写入」的变异不会被发现（实测 M57 → 0 红）。
func TestFileReadHistoryWrittenForFullRead(t *testing.T) {
	ResetReadDedupForTests()
	dir := t.TempDir()
	writeBig(t, dir, "h2.ts", bigText())
	tool := ReadFile(dir, nil)

	p := call(dir, map[string]any{"file_path": "h2.ts"})
	p.SessionID = "s1"
	_, _ = tool.Execute(t.Context(), p)

	e, ok := lookupFileReadHistory("s1", filepath.Join(dir, "h2.ts"))
	if !ok {
		t.Fatal("整文件读必须写表1b（全文件包含判定的依据）")
	}
	if e.totalLines <= 0 {
		t.Errorf("表1b 应记行数，实得 %d", e.totalLines)
	}
	if e.modelBytes <= 0 || e.rawBytes <= 0 {
		t.Errorf("表1b 应记字节数：model=%d raw=%d", e.modelBytes, e.rawBytes)
	}
}

// TestFileReadHistoryOnlyForFullReads —— 表1b 只记整文件读（分片读不写）。
func TestFileReadHistoryOnlyForFullReads(t *testing.T) {
	ResetReadDedupForTests()
	dir := t.TempDir()
	writeBig(t, dir, "h.ts", bigText())
	tool := ReadFile(dir, nil)

	p := call(dir, map[string]any{"file_path": "h.ts", "offset": 5, "limit": 10})
	p.SessionID = "s1"
	_, _ = tool.Execute(t.Context(), p)

	if _, ok := lookupFileReadHistory("s1", filepath.Join(dir, "h.ts")); ok {
		t.Error("分片读不应写表1b（只有整文件读才写）")
	}
	// 表1a 应写（按切片）。
	key := readHistoryKey(dir, filepath.Join(dir, "h.ts"), 5, "10", "s1")
	if _, ok := lookupReadHistory(key); !ok {
		t.Error("分片读应写表1a")
	}
}

// TestLimitFalsySemantics —— `!limit` 的 JS falsy 语义（负数 truthy）。
func TestLimitFalsySemantics(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  bool
	}{
		{"缺失", map[string]any{}, true},
		{"显式 null", map[string]any{"limit": nil}, true},
		{"显式 0", map[string]any{"limit": 0}, true},
		{"正数", map[string]any{"limit": 5}, false},
		{"负数（JS truthy）", map[string]any{"limit": -5}, false},
	}
	for _, c := range cases {
		if got := limitIsFalsy(c.input); got != c.want {
			t.Errorf("%s：limitIsFalsy=%v 期望 %v（TS 的 !limit 语义）", c.name, got, c.want)
		}
	}
}

// TestLimitKeySemantics —— 键里 `limit ?? 'all'`：显式 0 是 "0" 不是 "all"。
func TestLimitKeySemantics(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"缺失", map[string]any{}, "all"},
		{"显式 null", map[string]any{"limit": nil}, "all"},
		{"显式 0", map[string]any{"limit": 0}, "0"},
		{"正数", map[string]any{"limit": 7}, "7"},
	}
	for _, c := range cases {
		if got := limitKeyOf(c.input); got != c.want {
			t.Errorf("%s：limitKeyOf=%q 期望 %q", c.name, got, c.want)
		}
	}
}

// TestReadRefStatsAccumulates —— telemetry：per-session 优先于进程级兜底。
func TestReadRefStatsAccumulates(t *testing.T) {
	ResetReadDedupForTests()
	t.Setenv("RIVET_READ_REF", "")
	dir := t.TempDir()
	writeBig(t, dir, "i.ts", bigText())
	tool := ReadFile(dir, nil)

	stats := &ReadRefStats{}
	p1 := call(dir, map[string]any{"file_path": "i.ts"})
	p1.SessionID = "s1"
	p1.ReadRefStats = stats
	_, _ = tool.Execute(t.Context(), p1)

	p2 := call(dir, map[string]any{"file_path": "i.ts"})
	p2.SessionID = "s1"
	p2.ReadRefStats = stats
	_, _ = tool.Execute(t.Context(), p2)

	if stats.Count != 1 {
		t.Errorf("per-session count 应为 1，实得 %d", stats.Count)
	}
	if stats.SavedBytes <= 0 {
		t.Errorf("per-session savedBytes 应 > 0，实得 %d", stats.SavedBytes)
	}
	// 进程级兜底应未被写入（注入了 per-session）。
	if saved, count := ReadRefTelemetry(); saved != 0 || count != 0 {
		t.Errorf("注入 per-session 时不应写进程级兜底：saved=%d count=%d", saved, count)
	}
}

// TestTrimDropsOldestTwentyPercent —— 裁剪语义：裁 20%（非裁到上限）。
//
// 对账 TS `trimReadHistory`：`drop = Math.ceil(size * 0.2)`。
func TestTrimDropsOldestTwentyPercent(t *testing.T) {
	ResetReadDedupForTests()
	// 写 501 条触发裁剪。
	for i := 0; i < readHistoryMax+1; i++ {
		RecordRead("k"+string(rune('a'+i%26))+itoa(i), 1, 1, 10, 10, "", "s")
	}
	readHistoryMu.Lock()
	n := len(readHistory)
	readHistoryMu.Unlock()
	// 501 → 裁 ceil(501*0.2)=101 → 剩 400。
	if n != 400 {
		t.Errorf("裁剪后应剩 400 条（501-101），实得 %d", n)
	}
}
