package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// anchors_oracle_test.go —— 锚点提取/格式化的差分对账。
//
// **本文件是 Wave 2 的核心风险面**：TS 用 lookbehind/lookahead 做前后边界，
// RE2 两者都不支持（实测），改为手工字节判定。边界用例（URL 内嵌、粘连
// token、枚举粘连）必须逐例对账，否则语义漂移不会被发现。

type anchorOracleCase struct {
	Line    string `json:"line"`
	Anchors []struct {
		Raw               string `json:"raw"`
		Path              string `json:"path"`
		Line              *int   `json:"line"`
		DeclaredNew       bool   `json:"declaredNew"`
		PlaceholderShaped bool   `json:"placeholderShaped"`
	} `json:"anchors"`
}

type anchorMultiOracle struct {
	Input   string `json:"input"`
	Anchors []struct {
		Raw               string `json:"raw"`
		Path              string `json:"path"`
		Line              *int   `json:"line"`
		DeclaredNew       bool   `json:"declaredNew"`
		PlaceholderShaped bool   `json:"placeholderShaped"`
	} `json:"anchors"`
}

type formatDriftOracle struct {
	Input  int    `json:"input"`
	Output string `json:"output"`
}

func loadAnchorOracle(t *testing.T) ([]anchorOracleCase, anchorMultiOracle, []formatDriftOracle) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(oracleRelPath))
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v", err)
	}
	var all struct {
		ExtractAnchors      []anchorOracleCase  `json:"extractAnchors"`
		ExtractAnchorsMulti anchorMultiOracle   `json:"extractAnchorsMulti"`
		FormatDrifts        []formatDriftOracle `json:"formatDrifts"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(all.ExtractAnchors) == 0 {
		t.Fatal("oracle 无 extractAnchors 用例")
	}
	return all.ExtractAnchors, all.ExtractAnchorsMulti, all.FormatDrifts
}

// TestOracleExtractPlanAnchors —— 逐例对账锚点提取（含 lookbehind 替代边界）。
func TestOracleExtractPlanAnchors(t *testing.T) {
	cases, _, _ := loadAnchorOracle(t)
	for _, c := range cases {
		got := ExtractPlanAnchors(c.Line)
		if len(got) != len(c.Anchors) {
			t.Errorf("ExtractPlanAnchors(%q)\n  TS: %d 条 %v\n  Go: %d 条 %v",
				c.Line, len(c.Anchors), anchorPathsOf(c.Anchors), len(got), anchorPathsOfGo(got))
			continue
		}
		for i, want := range c.Anchors {
			g := got[i]
			if g.raw != want.Raw || g.path != want.Path || g.declaredNew != want.DeclaredNew || g.placeholderShaped != want.PlaceholderShaped {
				t.Errorf("ExtractPlanAnchors(%q)[%d]\n  TS: raw=%q path=%q new=%v ph=%v\n  Go: raw=%q path=%q new=%v ph=%v",
					c.Line, i, want.Raw, want.Path, want.DeclaredNew, want.PlaceholderShaped,
					g.raw, g.path, g.declaredNew, g.placeholderShaped)
			}
			if (g.line == nil) != (want.Line == nil) {
				t.Errorf("ExtractPlanAnchors(%q)[%d] line: TS=%v Go=%v", c.Line, i, want.Line, g.line)
			} else if g.line != nil && *g.line != *want.Line {
				t.Errorf("ExtractPlanAnchors(%q)[%d] line: TS=%d Go=%d", c.Line, i, *want.Line, *g.line)
			}
		}
	}
	t.Logf("extractAnchors 对账 %d 例", len(cases))
}

func anchorPathsOf(in []struct {
	Raw               string `json:"raw"`
	Path              string `json:"path"`
	Line              *int   `json:"line"`
	DeclaredNew       bool   `json:"declaredNew"`
	PlaceholderShaped bool   `json:"placeholderShaped"`
}) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.Path
	}
	return out
}

func anchorPathsOfGo(in []extractedAnchor) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = a.path
	}
	return out
}

// TestOracleExtractPlanAnchorsMultiLine —— 围栏状态跨行对账。
func TestOracleExtractPlanAnchorsMultiLine(t *testing.T) {
	_, multi, _ := loadAnchorOracle(t)
	if multi.Input == "" {
		t.Fatal("oracle 无多行用例")
	}
	got := ExtractPlanAnchors(multi.Input)
	if len(got) != len(multi.Anchors) {
		t.Fatalf("多行提取条数不符\n  TS: %v\n  Go: %v", anchorPathsOf(multi.Anchors), anchorPathsOfGo(got))
	}
	for i, want := range multi.Anchors {
		g := got[i]
		if g.raw != want.Raw || g.path != want.Path || g.declaredNew != want.DeclaredNew {
			t.Errorf("多行[%d]\n  TS: %+v\n  Go: raw=%q path=%q new=%v", i, want, g.raw, g.path, g.declaredNew)
		}
	}
	t.Logf("extractAnchorsMulti 对账 %d 条", len(multi.Anchors))
}

// TestOracleFormatAnchorDrifts —— 漂移格式化对账。
func TestOracleFormatAnchorDrifts(t *testing.T) {
	_, _, cases := loadAnchorOracle(t)
	// oracle 的 input 是条数；用固定 detail 重建。
	details := [][]string{{}, {"详情一"}, {"D1", "D2"}}
	kinds := [][]PlanAnchorDriftKind{{}, {DriftMissingFile}, {DriftMissingFile, DriftRootMismatch}}
	for i, c := range cases {
		var drifts []PlanAnchorDrift
		for j, d := range details[i] {
			drifts = append(drifts, PlanAnchorDrift{Detail: d, Kind: kinds[i][j]})
		}
		got := FormatAnchorDrifts(drifts)
		if got != c.Output {
			t.Errorf("FormatAnchorDrifts(%d 条) TS=%q Go=%q", c.Input, c.Output, got)
		}
	}
	t.Logf("formatDrifts 对账 %d 例", len(cases))
}

// TestExtractAnchorsURLRejected —— **lookbehind 替代的核心**：URL 内嵌路径不提取。
//
// 对账 TS 的 `(?<![\w./\\-])`——`src/a.ts` 在 URL 里前面是 `/`，必须排除。
// RE2 无 lookbehind，Go 侧靠 `isAnchorGlueByte` 手工判定。
func TestExtractAnchorsURLRejected(t *testing.T) {
	urls := []string{
		"见 https://github.com/foo/bar/blob/main/src/a.ts 这个链接",
		"见 http://x.com/a/b/c.ts:5 链接",
		"ftp://host/path/to/file.go 也应排除",
	}
	for _, u := range urls {
		if got := ExtractPlanAnchors(u); len(got) != 0 {
			t.Errorf("URL 内嵌路径不应被提取：%q → %v", u, anchorPathsOfGo(got))
		}
	}
}

// TestExtractAnchorsGluedRejected —— 粘连 token 的前边界。
//
// 对账 TS lookbehind 的 `[\w./\\-]` 排除类——注意 `\w` 在 JS 里是 **ASCII**，
// 故 CJK 字符**不算**粘连（`母x/b.ts` 应提取 `x/b.ts`）。
func TestExtractAnchorsGluedRejected(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"路径 a/b.ts 前面是斜杠/x/b.ts", 1},   // /x/b.ts 被排除
		{"路径 a/b.ts 前面是点./b.ts", 1},     // ./b.ts 被排除
		{"路径 a/b.ts 前面是反斜杠\\x/b.ts", 1}, // \x/b.ts 被排除
		{"路径 a/b.ts 前面是字母x/b.ts", 2},    // x/b.ts 前面是 ASCII 字母 → 排除？见 oracle
		{"见 src/a.ts", 1},
	}
	for _, c := range cases {
		got := ExtractPlanAnchors(c.line)
		if len(got) != c.want {
			t.Errorf("ExtractPlanAnchors(%q) want %d 条 got %d 条 %v", c.line, c.want, len(got), anchorPathsOfGo(got))
		}
	}
}

// TestExtractAnchorsLongestExtensionFirst —— 扩展名最长优先。
//
// 对账 TS：`EXT_ALTERNATION` 按长度降序，否则 `selector.tsx` 会被
// `selector.ts` 抢先匹配。
func TestExtractAnchorsLongestExtensionFirst(t *testing.T) {
	got := ExtractPlanAnchors("见 src/ui/selector.tsx 组件")
	if len(got) != 1 || got[0].path != "src/ui/selector.tsx" {
		t.Errorf("应提取 .tsx 完整路径，实得 %v", anchorPathsOfGo(got))
	}
}

// TestExtractAnchorsEnumerationRejected —— 枚举粘连（README.md/README.zh.md）不提取。
//
// 对账 TS `hasFileShapedIntermediateSegment`——文件不能是目录。
func TestExtractAnchorsEnumerationRejected(t *testing.T) {
	line := "同步 `README.md/README.zh.md/README.i18n.yaml` 三处描述。"
	if got := ExtractPlanAnchors(line); len(got) != 0 {
		t.Errorf("枚举粘连不应被提取，实得 %v", anchorPathsOfGo(got))
	}
}

// TestCheckPlanFactAnchorsMissingFile —— 缺失文件报 missing-file。
func TestCheckPlanFactAnchorsMissingFile(t *testing.T) {
	dir := t.TempDir()
	report, err := CheckPlanFactAnchors("见 src/definitely/missing.ts 的实现", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 1 {
		t.Fatalf("应有 1 条漂移，实得 %d：%+v", len(report.Drifts), report.Drifts)
	}
	if report.Drifts[0].Kind != DriftMissingFile {
		t.Errorf("应为 missing-file，实得 %s", report.Drifts[0].Kind)
	}
	if !strings.Contains(report.Drifts[0].Detail, "不存在") {
		t.Errorf("detail 应说明不存在：%q", report.Drifts[0].Detail)
	}
}

// TestCheckPlanFactAnchorsLineOutOfRange —— 行号越界报 line-out-of-range。
func TestCheckPlanFactAnchorsLineOutOfRange(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src", "a.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// split('\n') → 3 行（尾换行多算 1）。引用第 99 行 → 越界。
	report, err := CheckPlanFactAnchors("见 src/a.ts:99 的实现", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 1 || report.Drifts[0].Kind != DriftLineOutOfRange {
		t.Fatalf("应报 line-out-of-range，实得 %+v", report.Drifts)
	}
}

// TestCheckPlanFactAnchorsLineInRange —— 行号在范围内不报。
//
// **反证**：确保不是「有行号就报」。
func TestCheckPlanFactAnchorsLineInRange(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src", "a.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := CheckPlanFactAnchors("见 src/a.ts:2 的实现", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 0 {
		t.Errorf("行号在范围内不应报漂移，实得 %+v", report.Drifts)
	}
	if report.Checked != 1 {
		t.Errorf("checked 应为 1，实得 %d", report.Checked)
	}
}

// TestCheckPlanFactAnchorsDeclaredNewExempt —— 标「新增」的锚点豁免存在性校验。
func TestCheckPlanFactAnchorsDeclaredNewExempt(t *testing.T) {
	dir := t.TempDir()
	report, err := CheckPlanFactAnchors("新增 src/new/module.ts 文件", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 0 {
		t.Errorf("标新增的文件不应报漂移，实得 %+v", report.Drifts)
	}
}

// TestCheckPlanFactAnchorsPlaceholderExempt —— 占位形态且全根缺失 → 不上报。
func TestCheckPlanFactAnchorsPlaceholderExempt(t *testing.T) {
	dir := t.TempDir()
	report, err := CheckPlanFactAnchors("见 src/foo.ts 示例", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 0 {
		t.Errorf("占位形态不应上报，实得 %+v", report.Drifts)
	}
}

// TestCheckPlanFactAnchorsRerootParent —— 父目录命中报 root-mismatch 附 `../` 标签。
func TestCheckPlanFactAnchorsRerootParent(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "child")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "src", "a.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := CheckPlanFactAnchors("见 src/a.ts 的实现", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drifts) != 1 || report.Drifts[0].Kind != DriftRootMismatch {
		t.Fatalf("应报 root-mismatch，实得 %+v", report.Drifts)
	}
	if !strings.Contains(report.Drifts[0].Detail, "../") {
		t.Errorf("detail 应含 ../ 标签：%q", report.Drifts[0].Detail)
	}
}

// TestCheckPlanFactAnchorsRerootBudgetCap —— **换根探测预算封顶**。
//
// 对账 TS `REROOT_PROBE_BUDGET = 128`：最坏成本与目录规模解耦。
func TestCheckPlanFactAnchorsRerootBudgetCap(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "child")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 造 300 个子目录（超预算）。
	for i := 0; i < 300; i++ {
		if err := os.MkdirAll(filepath.Join(cwd, "d"+itoaPad(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 50 个同首段的 miss 锚点。
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("见 d0/missing" + itoaPad(i) + ".ts 的实现\n")
	}
	report, err := CheckPlanFactAnchors(sb.String(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if report.RerootProbes > rerootProbeBudget {
		t.Errorf("换根探测应封顶 %d，实得 %d", rerootProbeBudget, report.RerootProbes)
	}
	for _, d := range report.Drifts {
		if d.Kind != DriftMissingFile {
			t.Errorf("预算耗尽应 fail-open 降级为 missing-file，实得 %s", d.Kind)
		}
	}
	t.Logf("预算封顶验证：probes=%d (上限 %d)", report.RerootProbes, rerootProbeBudget)
}

// TestCheckPlanFactAnchorsNoRerootWhenAllHit —— 全命中时 rerootProbes = 0。
//
// **反证**：确保不是「总在探测」。
func TestCheckPlanFactAnchorsNoRerootWhenAllHit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src", "a.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := CheckPlanFactAnchors("见 src/a.ts 的实现", dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.RerootProbes != 0 {
		t.Errorf("全命中时不应探测，实得 %d", report.RerootProbes)
	}
}

// TestCheckPlanFactAnchorsMaxAnchors —— 锚点数封顶 200。
func TestCheckPlanFactAnchorsMaxAnchors(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 250; i++ {
		sb.WriteString("见 src/f" + itoaPad(i) + ".ts 的实现\n")
	}
	report, err := CheckPlanFactAnchors(sb.String(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked > maxAnchors {
		t.Errorf("checked 应封顶 %d，实得 %d", maxAnchors, report.Checked)
	}
}

// itoaPad 把整数补零成 4 位（构造唯一文件名用）。
func itoaPad(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
