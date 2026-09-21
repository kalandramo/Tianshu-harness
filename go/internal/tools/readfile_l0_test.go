package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/artifact"
)

// artifactMarkerTailRe 匹配**任意**尾随 artifact 标记（用于取 id）。
//
// **为什么不用 `compact.ArtifactMarkerRegex`**：那个正则的字符类是
// `[A-Za-z0-9_-]+`（**不含冒号**），而 store 产出的 id 是
// `<tool>:<suffix>`（含冒号，如 `read_file:rf1`）——正则匹配不到自家 id。
//
// 这是 TS 侧的既有不一致（`recovery-ref.ts:18` 的正则 vs
// `store.ts:229` 的 id 格式），TS 测试用无冒号 id 掩盖了它。
// Go 侧照抄 TS 行为（不擅自"修"），故测试用宽松正则取 id。
var artifactMarkerTailRe = regexp.MustCompile(`\[artifact:([^\]]+)\]\s*$`)

func newReadFileStore(t *testing.T) *artifact.Store {
	t.Helper()
	return artifact.NewStore(t.TempDir(), "sess1", artifact.Options{
		Now:         func() int64 { return 1_700_000_000_000 },
		IDGenerator: func() string { return "rf1" },
	})
}

// bigTSContent 造一个可辨识的大 .ts 内容（超 read_file 阈值）。
func bigTSContent(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("export function fnHere() { return 1 }\n")
	}
	return b.String()
}

// TestReadFileL0WrapsLargeContent —— **核心**：大内容包成 artifact。
//
// 此前 Go 侧 read_file 既无 L0（被 L1 的 l0WrappedTools 跳过），读大文件时
// 模型只看到截断原文；TS 侧给 outline + `[artifact:id]`。
func TestReadFileL0WrapsLargeContent(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"big.ts", bigTSContent(2000))

	store := newReadFileStore(t)
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "big.ts"})
	p.ArtifactStore = store
	p.ContextWindow = 0 // 阈值 1600（800 × read_file 乘数 2.0）

	r, _ := tool.Execute(t.Context(), p)
	if !strings.Contains(r.Content, "[artifact:") {
		t.Fatalf("大内容应被包装为 artifact，实得：%.200s", r.Content)
	}
	// 标记**必须在末尾**（prune/stale-round 的正则依赖）。
	if !artifactMarkerTailRe.MatchString(r.Content) {
		t.Errorf("标记必须在 content **末尾**：%.200s", r.Content)
	}
	// 应有 structural outline（.ts 文件）。
	if !strings.Contains(r.Content, "Structural outline") {
		t.Errorf("应含 structural outline：%.300s", r.Content)
	}
}

// TestReadFileL0SkipsSmallContent —— 小内容**不包**（零标记）。
//
// 对账 TS 注释：低于 prune 阈值的内容不会被 prune 替换，artifact 备份无意义，
// 而 `[artifact:X]` 标记本身会让模型误以为内容被隐藏（TS 复盘的真实模式）。
func TestReadFileL0SkipsSmallContent(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"small.ts", "export const x = 1\n")

	store := newReadFileStore(t)
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "small.ts"})
	p.ArtifactStore = store
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if strings.Contains(r.Content, "[artifact:") {
		t.Errorf("小内容不应包 artifact（标记会误导模型）：%q", r.Content)
	}
}

// TestReadFileL0StoresRawNotTruncated —— 落盘的必须是**原文**。
//
// 对账 TS：`store.save({ rawContent: payload.rawContent })`——原文，
// 不是截断后的 modelContent。若存错，read_section 取回的是残缺内容。
func TestReadFileL0StoresRawNotTruncated(t *testing.T) {
	root := t.TempDir()
	full := bigTSContent(5000)
	mustWriteFile(t, rootDir(root)+"big.ts", full)

	store := newReadFileStore(t)
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "big.ts"})
	p.ArtifactStore = store
	p.ContextWindow = 0 // cap 8000 → 会截断

	r, _ := tool.Execute(t.Context(), p)
	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.200s", r.Content)
	}
	id := m[1]

	raw, err := store.ReadRaw(id)
	if err != nil {
		t.Fatalf("ReadRaw 失败：%v", err)
	}
	if raw != full {
		t.Errorf("落盘应是原文（%d 字符），实得 %d 字符——存了截断版？",
			len(full), len(raw))
	}
}

// TestReadFileL0RecallByReadSection —— **端到端**：落盘后可用 read_section 取回。
//
// 防的是「存得下、取不回」——包装产出的 id 必须能被 read_section 消费。
func TestReadFileL0RecallByReadSection(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"big.ts", bigTSContent(3000))

	store := newReadFileStore(t)
	rf := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "big.ts"})
	p.ArtifactStore = store
	p.ContextWindow = 0
	r, _ := rf.Execute(t.Context(), p)

	m := artifactMarkerTailRe.FindStringSubmatch(r.Content)
	if m == nil {
		t.Fatalf("应被包装：%.200s", r.Content)
	}
	id := m[1]

	rs := ReadSection("", nil)
	r2, _ := rs.Execute(t.Context(), &CallParams{
		Input:         map[string]any{"artifactId": id, "section": "L1-L10"},
		ArtifactStore: store, ContextWindow: 1_000_000,
	})
	if r2.IsError {
		t.Fatalf("read_section 取回失败：%s", r2.Content)
	}
	if !strings.Contains(r2.Content, "export function fnHere") {
		t.Errorf("取回内容不符：%.200s", r2.Content)
	}
}

// TestReadFileL0GracefulWhenSaveFails —— Save 失败时**优雅降级**。
func TestReadFileL0GracefulWhenSaveFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"big.ts", bigTSContent(3000))

	// 指向一个**不可能创建**的目录（NUL 字节在 Windows 非法）。
	bad := artifact.NewStore("bad\x00dir", "sess1", artifact.Options{})
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_path": "big.ts"})
	p.ArtifactStore = bad
	p.ContextWindow = 0

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Errorf("Save 失败不应让 read_file 报错（优雅降级）：%s", r.Content)
	}
	if strings.Contains(r.Content, "[artifact:") {
		t.Error("Save 失败时不应出现 artifact 标记")
	}
	if !strings.Contains(r.Content, "export function fnHere") {
		t.Errorf("降级后应返回原内容：%.200s", r.Content)
	}
}

// rootDir 返回带尾分隔符的临时目录（Windows 用反斜杠）。
func rootDir(root string) string {
	if strings.HasSuffix(root, "/") || strings.HasSuffix(root, "\\") {
		return root
	}
	return root + string([]byte{92})
}
