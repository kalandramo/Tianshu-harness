package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// readpolicyWiring_test.go —— 策略层**接线**的端到端验证。
//
// 为什么要单独一组：`readpolicy_test.go` 测的是 `DecideReadPolicy` 的**判定**
// （101 例差分 oracle），但判定正确 ≠ 被消费。本组测的是「判定结果是否真的
// 改变了 read_file 的**输出**」——正是第八刀遗漏的那一环（第五例未接线）。

func mkSourceLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("const value = 42; // padding to reach the desired size\n")
	}
	return b.String()
}

// TestPartialBranchWired —— >80KB 源文件无范围读 → **折叠骨架**而非截断全文。
//
// 对账 TS `:658-666`（且 >100KB 走 `:603` 同一分支）。
func TestPartialBranchWired(t *testing.T) {
	root := t.TempDir()
	// 源文件（.ts 归 source），>80KB 触发 partial。
	big := mkSourceLines(1200) // 每行 ~53 字节 → ~64KB；再加函数体撑过 80KB
	for i := 0; i < 400; i++ {
		big += "function handler" + string(rune('a'+i%26)) + "() {\n  const x = 1;\n  return x;\n}\n"
	}
	mustWriteFile(t, filepath.Join(root, "big.ts"), big)
	if len(big) <= sourceLargeBytes {
		t.Fatalf("fixture 应 >%d 字节，实得 %d", sourceLargeBytes, len(big))
	}

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "big.ts"}))
	if r.IsError {
		t.Fatalf("大源文件应放行 partial，实得错误：%.200s", r.Content)
	}
	// 骨架视图的特征：PARTIAL/SKELETON 头 + 导航指引。
	if !strings.Contains(r.Content, "PARTIAL view") && !strings.Contains(r.Content, "SKELETON view") {
		t.Errorf("大源文件应返回骨架视图：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "read_file(file_path=") {
		t.Errorf("骨架应含后续读取指引：%.300s", r.Content)
	}
	// **关键**：骨架应折叠掉函数体（不是原样全量）。
	if strings.Contains(r.Content, "const value = 42;") && len(r.Content) > sourceLargeBytes {
		t.Error("partial 分支应折叠/裁剪，不应原样返回全文")
	}
	// **折叠确实生效**：区分两条路径的**唯一可靠信号是头部标记**——
	// 折叠路径给 `SKELETON view`（`buildPartialView` 收到 skeletonOf），
	// 回退路径给 `PARTIAL view`。长度不足以区分（两条路径都受 cap 约束）。
	//
	// 探针实测（fixture 同此）：WasFolded=true、originalLines=2801、
	// foldedLines=200、ratio=0.071 → 折叠路径 → SKELETON view。
	if !strings.Contains(r.Content, "SKELETON view") {
		t.Errorf("应走折叠路径（SKELETON view）而非回退（PARTIAL view）：%.300s", r.Content)
	}
}

// TestPreviewBranchWired —— 日志文件无范围读 → **有界头尾预览**而非全文。
//
// 对账 TS `:647-656`。
//
// **两个尺寸窗口都有讲究**：
//   - preview 要求 `>16KB`（preview guard）；
//   - `>100KB` 会先撞 `:602` 的 oversize 检查，那里**只有 partial 放行**；
//   - 预览内容本身若超 cap，还会被 `truncateContent` 二次 head/tail 截断
//     （TS 同构），中部的 "N lines omitted" 标记会落到被裁掉的中段。
//
// 故 fixture 取 ~17KB（头部行缩短，使预览整体不触发二次截断）。
func TestPreviewBranchWired(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 600; i++ {
		b.WriteString("2026 INFO log line\n") // 短行：600×19 ≈ 11.4KB
	}
	// 补足到 >16KB。
	for b.Len() <= logPreviewGuardBytes {
		b.WriteString("appended log padding line\n")
	}
	b.WriteString("FINAL_LOG_MARKER\n")
	log := b.String()
	mustWriteFile(t, filepath.Join(root, "app.log"), log)
	if len(log) <= logPreviewGuardBytes {
		t.Fatalf("fixture 应 >%d 字节，实得 %d", logPreviewGuardBytes, len(log))
	}
	if len(log) > maxToolInputBytes {
		t.Fatalf("fixture 应 <=%d 字节，实得 %d", maxToolInputBytes, len(log))
	}

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "app.log"}))
	if r.IsError {
		t.Fatalf("日志应给预览而非报错：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "looks like a log/JSONL output file") {
		t.Errorf("应识别为日志并给预览：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "lines omitted") {
		t.Errorf("预览应标注省略行数：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "Preview boundaries:") {
		t.Errorf("预览应给出后续读取边界：%.300s", r.Content)
	}
	// 预览必须**短于**原文（否则不是"有界"）。
	if len(r.Content) >= len(log) {
		t.Errorf("预览应短于全文：preview=%d raw=%d", len(r.Content), len(log))
	}
}

// TestRejectWithRangeWired —— generated/minified 无范围读 → 报错 + 指引。
//
// 对账 TS `:627-629`。此前 Go 静默返回全文。
func TestRejectWithRangeWired(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "bundle.min.js"),
		"var a=1;"+strings.Repeat("var x=1;", 500))

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "bundle.min.js"}))
	if !r.IsError {
		t.Fatalf("minified 无范围读应报错，实得：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "offset") || !strings.Contains(r.Content, "limit") {
		t.Errorf("应指引用 offset/limit：%.200s", r.Content)
	}
}

// TestRejectWithRangeAllowsExplicitRange —— 给范围后放行（对账 TS 分支顺序）。
func TestRejectWithRangeAllowsExplicitRange(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "bundle.min.js"), "var a=1;\nvar b=2;\nvar c=3;\n")

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{
		"file_path": "bundle.min.js", "offset": 1, "limit": 2}))
	if r.IsError {
		t.Fatalf("显式范围应放行（hasExplicitRange 优先）：%.200s", r.Content)
	}
}

// TestFullWithHintWired —— 中等源文件（20KB-80KB）→ 全文 + 编辑指引。
//
// 对账 TS `:711-713`。
func TestFullWithHintWired(t *testing.T) {
	root := t.TempDir()
	// 目标：>20KB 且 <=80KB。
	mid := mkSourceLines(500) // ~26KB
	mustWriteFile(t, filepath.Join(root, "mid.ts"), mid)
	if len(mid) <= sourceSmallBytes || len(mid) > sourceLargeBytes {
		t.Fatalf("fixture 应在 (20KB,80KB]，实得 %d", len(mid))
	}

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "mid.ts"}))
	if r.IsError {
		t.Fatalf("中等文件应放行：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "── Note: this file is") {
		t.Errorf("中等文件应附编辑指引：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "hash_edit with anchors") {
		t.Errorf("指引应含 hash_edit 建议：%.300s", r.Content)
	}
}

// TestSmallSourceNoHint —— 小源文件（<=20KB）无指引（对账 TS）。
func TestSmallSourceNoHint(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "small.ts"), "const x = 1\n")

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "small.ts"}))
	if strings.Contains(r.Content, "── Note: this file is") {
		t.Errorf("小文件不应有编辑指引：%.200s", r.Content)
	}
}

// TestOffsetExceedsLength —— offset 越界 → 报错文本含总行数（对账 TS `:667-677`）。
func TestOffsetExceedsLength(t *testing.T) {
	root := t.TempDir()
	// 注意：内容以 \n 结尾时 split 会多出一个空尾元素（4 行）。
	mustWriteFile(t, filepath.Join(root, "tiny.txt"), "line1\nline2\nline3\n")

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{
		"file_path": "tiny.txt", "offset": 999}))
	if !strings.Contains(r.Content, "exceeds file length") {
		t.Fatalf("越界应报错并说明：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "4 lines") {
		t.Errorf("应告知总行数（含尾空行共 4）：%.200s", r.Content)
	}
}

// TestFocusRejectsHugeFile —— focus + 超 2MB → 报错（对账 TS `:598-602`）。
func TestFocusRejectsHugeFile(t *testing.T) {
	root := t.TempDir()
	huge := strings.Repeat("const x = 1;\n", 200_000) // ~2.6MB
	mustWriteFile(t, filepath.Join(root, "huge.ts"), huge)
	if len(huge) <= maxFocusScanBytes {
		t.Fatalf("fixture 应 >%d 字节，实得 %d", maxFocusScanBytes, len(huge))
	}

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{
		"file_path": "huge.ts", "focus": "something"}))
	if !r.IsError {
		t.Fatalf("focus 超大文件应报错：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "2MB") {
		t.Errorf("应说明 2MB 上限：%.200s", r.Content)
	}
}

// TestOversizeNonPartialRejected —— >100KB 且非 partial 类 → 报「文件过大」。
//
// 对账 TS `:602-616`：`fileSize > MAX_TOOL_INPUT_BYTES && !hasExplicitRange
// && !hasFocus` 时**只有 `partial` 放行，其余（含 preview）一律 throw**。
//
// **本条是本刀实测发现的边界**：日志 >100KB 时不会给预览，而是报错——
// 我最初的预览测试就撞在这条上（期望错误）。
func TestOversizeNonPartialRejected(t *testing.T) {
	root := t.TempDir()
	hugeLog := strings.Repeat("2026-09-21 INFO payload line here\n", 4000) // >100KB
	mustWriteFile(t, filepath.Join(root, "huge.log"), hugeLog)
	if len(hugeLog) <= maxToolInputBytes {
		t.Fatalf("fixture 应 >%d 字节，实得 %d", maxToolInputBytes, len(hugeLog))
	}

	tool := ReadFile(root, nil)
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "huge.log"}))
	if !r.IsError {
		t.Fatalf(">100KB 的日志应被拒（只有 partial 放行）：%.200s", r.Content)
	}
	if !strings.Contains(r.Content, "文件过大") {
		t.Errorf("应报「文件过大」：%.200s", r.Content)
	}
	// 给显式范围后应放行。
	r2, _ := tool.Execute(t.Context(), call(root, map[string]any{
		"file_path": "huge.log", "offset": 1, "limit": 10}))
	if r2.IsError {
		t.Errorf("显式范围应放行：%.200s", r2.Content)
	}
}

// TestHugeNonSourceRejected —— >100KB 非 source 类（非 partial）→ 报错。
//
// 对账 TS `:616`：partial 放行，其余报「文件过大」。
func TestHugeNonSourceRejected(t *testing.T) {
	root := t.TempDir()
	// .bin 不在 BINARY_EXTENSIONS 也不在 source 列表 → unknown；但 stable text。
	huge := strings.Repeat("some plain text content here\n", 6000) // >100KB
	mustWriteFile(t, filepath.Join(root, "notes.txt"), huge)

	tool := ReadFile(root, nil)
	// 注：.txt 是 unknown，>80KB 会走 partial（放行）——故本测试用 generated
	// 目录下的 minified（reject）验证"非 partial 的过大文件被拒"。
	mustWriteFile(t, filepath.Join(root, "dist", "out.min.css"),
		strings.Repeat("a{b:c}", 20000))
	r, _ := tool.Execute(t.Context(), call(root, map[string]any{"file_path": "dist/out.min.css"}))
	if !r.IsError {
		t.Fatalf("generated/minified 过大文件应被拒：%.200s", r.Content)
	}
}
