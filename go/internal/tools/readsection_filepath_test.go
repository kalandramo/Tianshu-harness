package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这些测试锁定 read_section 的 file_path 分支（对账 TS 的 "B3" 分支）。

func newDiskTool(t *testing.T, cwd string) Tool {
	t.Helper()
	return ReadSection(cwd, nil)
}

// TestReadSectionFilePathReadsDisk —— 从磁盘按区段读。
func TestReadSectionFilePathReadsDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("L1\nL2\nL3\nL4\nL5"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newDiskTool(t, dir)
	res, err := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "a.txt", "section": "L2-L4"},
		Cwd:   dir,
	})
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if res.Content != "L2\nL3\nL4" {
		t.Errorf("期望 L2\\nL3\\nL4，实得 %q", res.Content)
	}
	if !strings.HasSuffix(res.RawPath, "a.txt") {
		t.Errorf("应回填 rawPath，实得 %q", res.RawPath)
	}
}

// TestReadSectionFilePathCharRange —— 字符范围也可用。
func TestReadSectionFilePathCharRange(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("abcdefghij"), 0o644)
	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "b.txt", "section": "c2-c5"}, Cwd: dir,
	})
	if res.IsError || res.Content != "cde" {
		t.Errorf("期望 cde，实得 %q（err=%v）", res.Content, res.IsError)
	}
}

// TestReadSectionFilePathStalenessWarning —— **核心语义**：mtime 不符时前置告警。
//
// 对账 TS：文件自上次 read_file 后变更过，内容可能与上文不一致——必须告警，
// 否则模型会把当前磁盘版本误当成「上文读到的版本」做推断。
func TestReadSectionFilePathStalenessWarning(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.txt")
	os.WriteFile(p, []byte("content"), 0o644)

	// 记录一个**过期的** mtime（比真实值早 1 小时）。
	canonical := p
	ResetFileStateForTests()
	NoteFileObserved(canonical, time.Now().Add(-time.Hour).UnixMilli(), 7, "")

	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "s.txt", "section": "L1-L1"}, Cwd: dir,
	})
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if !strings.Contains(res.Content, "自上次 read_file 后已变更") {
		t.Errorf("mtime 不符应告警，实得 %q", res.Content)
	}
}

// TestReadSectionFilePathNoWarningWhenFresh —— mtime 相符时**不**告警。
func TestReadSectionFilePathNoWarningWhenFresh(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fresh.txt")
	os.WriteFile(p, []byte("content"), 0o644)
	fi, _ := os.Stat(p)

	ResetFileStateForTests()
	NoteFileObserved(p, fi.ModTime().UnixMilli(), fi.Size(), "")

	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "fresh.txt", "section": "L1-L1"}, Cwd: dir,
	})
	if strings.Contains(res.Content, "已变更") {
		t.Errorf("mtime 相符不应告警，实得 %q", res.Content)
	}
}

// TestReadSectionFilePathNoWarningWhenNeverObserved —— 从未观察过 → 不告警。
//
// 对账 TS：`lastMtime !== null` 才检查——首次读文件没有基准，不该报「变更」。
func TestReadSectionFilePathNoWarningWhenNeverObserved(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	ResetFileStateForTests()

	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "new.txt", "section": "L1-L1"}, Cwd: dir,
	})
	if strings.Contains(res.Content, "已变更") {
		t.Errorf("首次读不该报变更，实得 %q", res.Content)
	}
}

// TestReadSectionFilePathTooLarge —— 超 2MB 报错并建议替代手段。
func TestReadSectionFilePathTooLarge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	f, _ := os.Create(p)
	// 造 2.1MB。
	chunk := strings.Repeat("x", 1024*1024)
	f.WriteString(chunk)
	f.WriteString(chunk)
	f.WriteString(strings.Repeat("y", 100*1024))
	f.Close()

	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "big.txt", "section": "L1-L10"}, Cwd: dir,
	})
	if !res.IsError {
		t.Fatal("超 2MB 应报错")
	}
	if !strings.Contains(res.Content, "过大") || !strings.Contains(res.Content, "head/tail") {
		t.Errorf("应报过大并建议替代手段，实得 %q", res.Content)
	}
}

// TestReadSectionFilePathMissingFile —— 文件不存在时给出可读错误。
func TestReadSectionFilePathMissingFile(t *testing.T) {
	dir := t.TempDir()
	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "nope.txt", "section": "L1-L1"}, Cwd: dir,
	})
	if !res.IsError {
		t.Fatal("不存在的文件应报错")
	}
}

// TestReadSectionFilePathTruncates —— 超上限时截断并标注。
func TestReadSectionFilePathTruncates(t *testing.T) {
	dir := t.TempDir()
	// 小窗口 → 地板 8000；造 100 行 × 200 字符 = 20000 字符。
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString(strings.Repeat("q", 200) + "\n")
	}
	os.WriteFile(filepath.Join(dir, "long.txt"), []byte(b.String()), 0o644)

	tool := newDiskTool(t, dir)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input:         map[string]any{"file_path": "long.txt", "section": "L1-L100"},
		Cwd:           dir,
		ContextWindow: 64_000, // 小窗口 → 地板 8000
	})
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if !strings.Contains(res.Content, "已截断至 8000 字符") {
		t.Errorf("应截断到 8000，实得尾部：%s", tailOf(res.Content, 120))
	}
}

// TestReadSectionBothProvidedUsesArtifact —— 两者都给时走 artifactId 分支。
//
// 对账 TS：`if (file_path && !artifactId)`——file_path 只在**没有** artifactId
// 时生效。这条锁定优先级，防止 file_path 静默劫持 artifact 召回。
func TestReadSectionBothProvidedUsesArtifact(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("SHOULD-NOT-APPEAR"), 0o644)

	store, id := newCompactHistoryStore(t, "--- turn:0 role:user ---\nfrom-artifact")
	tool := ReadSection(dir, nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		// **取 L1-L2**：L1 是 divider、L2 才是正文——只取 L1 拿不到内容
		// （首版写 L1-L1 却断言含正文，是测试自身的错）。
		Input:         map[string]any{"artifactId": id, "file_path": "ignored.txt", "section": "L1-L2"},
		Cwd:           dir,
		ArtifactStore: store,
		ContextWindow: 1_000_000,
	})
	if res.IsError {
		t.Fatalf("不应报错：%s", res.Content)
	}
	if strings.Contains(res.Content, "SHOULD-NOT-APPEAR") {
		t.Error("同时提供时应收 artifactId 分支——file_path 不得劫持")
	}
	if !strings.Contains(res.Content, "from-artifact") {
		t.Errorf("应返回 artifact 内容，实得 %q", res.Content)
	}
}

// ── 文件状态表 ──

func TestFileStateRoundTrip(t *testing.T) {
	ResetFileStateForTests()
	if _, ok := GetFileReadMtime("/x/a.ts", "s1"); ok {
		t.Error("未观察过应返回 false")
	}
	NoteFileObserved("/x/a.ts", 12345, 100, "s1")
	got, ok := GetFileReadMtime("/x/a.ts", "s1")
	if !ok || got != 12345 {
		t.Errorf("应取回 12345，实得 %d ok=%v", got, ok)
	}
}

// TestFileStateSessionIsolation —— 不同会话的观察互不干扰。
func TestFileStateSessionIsolation(t *testing.T) {
	ResetFileStateForTests()
	NoteFileObserved("/x/a.ts", 111, 1, "s1")
	if _, ok := GetFileReadMtime("/x/a.ts", "s2"); ok {
		t.Error("s2 未观察过，不应命中 s1 的记录")
	}
}

func TestFileStateTrim(t *testing.T) {
	ResetFileStateForTests()
	for i := 0; i < lastKnownMax+50; i++ {
		NoteFileObserved(filepath.Join("/x", itoaT(i)), int64(i), 1, "s")
	}
	// 最旧的应被裁掉，最新的应还在。
	if _, ok := GetFileReadMtime("/x/0", "s"); ok {
		t.Error("最旧条目应被裁剪")
	}
	if _, ok := GetFileReadMtime(filepath.Join("/x", itoaT(lastKnownMax+49)), "s"); !ok {
		t.Error("最新条目应保留")
	}
}

// ── computeModelReadCap ──

func TestComputeModelReadCapDefaults(t *testing.T) {
	// 窗口未知 → 遗留地板。
	got := ComputeModelReadCap(ModelReadCapInput{})
	if got.MaxChars != 8000 || got.HeadChars != 4000 || got.TailChars != 2000 {
		t.Errorf("窗口未知应返回遗留地板，实得 %+v", got)
	}
}

func TestComputeModelReadCapTiers(t *testing.T) {
	cases := []struct {
		window int
		want   int
	}{
		{64_000, 8000},    // 0.02×64K×4 = 5120 → 地板 8000
		{200_000, 24_000}, // 0.03×200K×4 = 24000
		{300_000, 36_000}, // 0.03×300K×4 = 36000
		{500_000, 80_000}, // 0.05×500K×4 = 100000 → 未封顶？算：100000 < 120000 → 100000
	}
	for _, c := range cases {
		got := ComputeModelReadCap(ModelReadCapInput{ContextWindow: c.window})
		if c.window == 500_000 {
			// 单独核：0.05 × 500000 × 4 × 1.0 = 100000
			if got.MaxChars != 100_000 {
				t.Errorf("500K 窗口应为 100000，实得 %d", got.MaxChars)
			}
			continue
		}
		if got.MaxChars != c.want {
			t.Errorf("%d 窗口：期望 %d，实得 %d", c.window, c.want, got.MaxChars)
		}
	}
}

func TestComputeModelReadCapCeiling(t *testing.T) {
	// 1M：0.05×1M×4 = 200000 → 封顶 120000。
	got := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 1_000_000})
	if got.MaxChars != absoluteMaxChars {
		t.Errorf("1M 应封顶 %d，实得 %d", absoluteMaxChars, got.MaxChars)
	}
	// head/tail 是 60% / 30%。
	if got.HeadChars != absoluteMaxChars*60/100 {
		t.Errorf("head 应为 60%%，实得 %d", got.HeadChars)
	}
	if got.TailChars != absoluteMaxChars*30/100 {
		t.Errorf("tail 应为 30%%，实得 %d", got.TailChars)
	}
}
