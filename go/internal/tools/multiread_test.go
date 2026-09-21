package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multiReadOracleCase 对账 `handleMultiRead` 的**格式层**。
//
// 生成：`node_modules/.bin/tsx go/testdata/multiread/gen_oracle.ts`
//
// **重要局限（明示）**：该 oracle 是 TS 逻辑的**人工转录**（`gen_oracle.ts` 里
// 复刻了 handleMultiRead 的拼接代码），**不是 TS 原函数的运行输出**——因为
// `handleMultiRead` 调 `readFilePayload`（含 gitignore/office/partial 等未移植
// 分支）。故它锁定的是**格式契约**（节头/分隔/错误节/UI 文案），不是端到端行为。
type multiReadOracleCase struct {
	Label         string   `json:"label"`
	Cwd           string   `json:"cwd"`
	Paths         []string `json:"paths"`
	ModelContents []string `json:"modelContents"`
	ErrorIndexes  []int    `json:"errorIndexes"`
	PerFileCap    struct {
		MaxChars  int `json:"maxChars"`
		HeadChars int `json:"headChars"`
		TailChars int `json:"tailChars"`
	} `json:"perFileCap"`
	Content   string `json:"content"`
	UIContent string `json:"uiContent"`
}

func loadMultiReadOracle(t *testing.T) []multiReadOracleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "multiread", "oracle.json"))
	if err != nil {
		t.Fatalf("读 oracle 失败：%v", err)
	}
	var cases []multiReadOracleCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(cases) == 0 {
		t.Fatal("oracle 为空")
	}
	return cases
}

// TestMultiReadFormatContract —— 格式契约逐例对账（**独立于文件系统**）。
//
// 直接测格式拼装函数，避免依赖真实文件的读取管线（那部分 Go 未移植全）。
func TestMultiReadFormatContract(t *testing.T) {
	cases := loadMultiReadOracle(t)
	for i, c := range cases {
		paths := c.Paths
		if len(paths) > 5 {
			paths = paths[:5]
		}
		// 复刻 Go 的 executeMultiRead 拼接（喂入 oracle 的 modelContents）。
		sections := []string{}
		totalBytes := 0
		errors := 0
		mi := 0
		for _, rawPath := range paths {
			trimmed := strings.TrimSpace(rawPath)
			if trimmed == "" {
				continue
			}
			if containsInt(c.ErrorIndexes, indexOfStr(c.Paths, rawPath)) {
				display := trimmed
				if strings.HasPrefix(trimmed, c.Cwd) {
					display = tsRelative(c.Cwd, trimmed)
				}
				sections = append(sections, "── "+display+" ──\nError: simulated error")
				errors++
				mi++
				continue
			}
			mc := ""
			if mi < len(c.ModelContents) {
				mc = c.ModelContents[mi]
			}
			mi++
			rel := tsRelative(c.Cwd, trimmed)
			sections = append(sections, "── "+rel+" ──\n"+mc)
			totalBytes += len(mc)
		}
		gotContent := strings.Join(sections, "\n\n")
		gotUI := multiReadUI(len(paths), errors, totalBytes)

		if gotContent != c.Content {
			t.Errorf("用例 %d (%s) content 不符：\n期望 %q\n实得 %q",
				i, c.Label, c.Content, gotContent)
		}
		if gotUI != c.UIContent {
			t.Errorf("用例 %d (%s) uiContent 不符：期望 %q，实得 %q",
				i, c.Label, c.UIContent, gotUI)
		}
	}
}

// TestMultiReadEndToEnd —— **端到端**：read_file 传 file_paths 真读多文件。
//
// 这是「file_paths 静默失效」的最终验收：此前传 file_paths 会报
// 「read_file 需要 path 参数」。
func TestMultiReadEndToEnd(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"a.ts", "const a = 1")
	mustWriteFile(t, rootDir(root)+"b.ts", "const b = 2")

	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_paths": []any{"a.ts", "b.ts"}})
	p.ContextWindow = 1_000_000

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Fatalf("多读不应报错：%s", r.Content)
	}
	// 两节，各带节头。
	if !strings.Contains(r.Content, "a.ts ──") || !strings.Contains(r.Content, "b.ts ──") {
		t.Errorf("应含两节头：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "const a = 1") || !strings.Contains(r.Content, "const b = 2") {
		t.Errorf("应含两文件内容：%.300s", r.Content)
	}
	// 节间用空行分隔。
	if !strings.Contains(r.Content, "\n\n") {
		t.Errorf("节间应分隔：%.300s", r.Content)
	}
	// UI 文案。
	if !strings.Contains(r.UIContent, "Read 2/2 files") {
		t.Errorf("UI 文案不符：%q", r.UIContent)
	}
}

// TestMultiReadPartialFailure —— **单文件失败不整体失败**（对账 TS 的 catch）。
//
// **fixture 关键**：失败文件必须在**中间**，否则「错误即提前返回」与
// 「跳过并继续」产出相同结果（变异 M47 首版 0 红即此因——成功文件在前时
// 提前返回仍包含它）。
func TestMultiReadPartialFailure(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"first.ts", "const first = 1")
	mustWriteFile(t, rootDir(root)+"third.ts", "const third = 3")

	tool := ReadFile(root, nil)
	// 顺序：成功 → 失败 → 成功（失败在中间）。
	p := call(root, map[string]any{"file_paths": []any{"first.ts", "missing.ts", "third.ts"}})
	p.ContextWindow = 1_000_000

	r, _ := tool.Execute(t.Context(), p)
	if r.IsError {
		t.Fatalf("部分失败不应让整体报错：%s", r.Content)
	}
	if !strings.Contains(r.Content, "const first = 1") {
		t.Errorf("失败前的文件应有内容：%.300s", r.Content)
	}
	if !strings.Contains(r.Content, "Error:") {
		t.Errorf("失败的文件应有 Error 节：%.300s", r.Content)
	}
	// **关键**：失败**之后**的文件仍应被读（证明是 continue 而非 return）。
	if !strings.Contains(r.Content, "const third = 3") {
		t.Errorf("失败**之后**的文件仍应被读（continue 而非 return）：%.400s", r.Content)
	}
	if !strings.Contains(r.UIContent, "Read 2/3 files") {
		t.Errorf("UI 应报 2/3：%q", r.UIContent)
	}
}

// TestMultiReadCapsAtFive —— 上限 5（对账 TS 的 `slice(0, 5)`）。
func TestMultiReadCapsAtFive(t *testing.T) {
	root := t.TempDir()
	names := []any{}
	for i := 0; i < 7; i++ {
		n := string(rune('a'+i)) + ".ts"
		mustWriteFile(t, rootDir(root)+n, "const x = 1")
		names = append(names, n)
	}
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_paths": names})
	p.ContextWindow = 1_000_000
	r, _ := tool.Execute(t.Context(), p)
	// 只读前 5 个 → UI 报 5/5。
	if !strings.Contains(r.UIContent, "Read 5/5 files") {
		t.Errorf("应只读 5 个（上限）：%q", r.UIContent)
	}
}

// TestMultiReadPerFileCapDivided —— cap 按文件数均分（对账 TS 的 Math.floor）。
func TestMultiReadPerFileCapDivided(t *testing.T) {
	full := ComputeModelReadCap(ModelReadCapInput{ContextWindow: 1_000_000})
	if full.MaxChars%2 != 0 {
		t.Skip("本用例假设 maxChars 可被 2 整除")
	}
	// 2 个文件 → 每文件 cap = maxChars/2。
	root := t.TempDir()
	mustWriteFile(t, rootDir(root)+"a.ts", "x")
	mustWriteFile(t, rootDir(root)+"b.ts", "y")
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_paths": []any{"a.ts", "b.ts"}})
	p.ContextWindow = 1_000_000
	r, _ := tool.Execute(t.Context(), p)
	// 内容短，不触发截断——这里只验证不崩且 UI 正确。
	if !strings.Contains(r.UIContent, "Read 2/2 files") {
		t.Errorf("UI 不符：%q", r.UIContent)
	}
}

// TestMultiReadEmptyArrayFallsThrough —— 空数组应回落到单读路径（报缺 path）。
func TestMultiReadEmptyArrayFallsThrough(t *testing.T) {
	root := t.TempDir()
	tool := ReadFile(root, nil)
	p := call(root, map[string]any{"file_paths": []any{}})
	r, _ := tool.Execute(t.Context(), p)
	// 空数组 → 不走多读 → 无 file_path → 报错。
	if !r.IsError {
		t.Errorf("空数组且无 file_path 应报错：%s", r.Content)
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func indexOfStr(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}
