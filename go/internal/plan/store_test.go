package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// store_test.go —— 计划文件存储的行为测试。
//
// 差分对账不易覆盖文件系统层（oracle 需要真实目录），故此处用行为断言 +
// 关键边界的显式用例。

// TestWritePlanCreatesDirAndFile —— 写计划创建目录并落盘。
func TestWritePlanCreatesDirAndFile(t *testing.T) {
	dir := t.TempDir()
	rel, err := WritePlan(dir, "my-plan", "# My Plan\n\nbody\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel != ".rivet/plans/my-plan.md" {
		t.Errorf("返回路径应为 .rivet/plans/my-plan.md，实得 %q", rel)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "my-plan.md"))
	if err != nil {
		t.Fatalf("文件未落盘：%v", err)
	}
	if string(got) != "# My Plan\n\nbody\n" {
		t.Errorf("内容不符：%q", string(got))
	}
}

// TestWritePlanFrontmatterRoundTrip —— **frontmatter 幂等**：重复写入不叠加。
//
// 这条锁定 `planOptionsFrontmatterRe.ReplaceAllString(content, "")` 那一步
// ——漏掉它会让每次 approve 都叠一层 frontmatter（多方案记录被抹掉的同源缺陷）。
func TestWritePlanFrontmatterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	opts := []PlanOption{{Label: "方案A", Description: "说明A"}}
	content := "# Plan\n\nbody\n"

	if _, err := WritePlan(dir, "p", content, opts); err != nil {
		t.Fatal(err)
	}
	// 第二次：用**已带 frontmatter** 的内容再写（模拟 markPlanStatus 的读改写）。
	raw, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "p.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WritePlan(dir, "p", string(raw), opts); err != nil {
		t.Fatal(err)
	}
	final, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "p.md"))
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(final), "rivet-options:")
	if count != 1 {
		t.Errorf("frontmatter 应只出现 1 次（幂等），实得 %d 次\n%s", count, string(final))
	}
}

// TestReadPlanParsesFields —— 读计划解析各字段。
func TestReadPlanParsesFields(t *testing.T) {
	dir := t.TempDir()
	content := "> **Status: APPROVED** — 2026-01-01T00:00:00.000Z\n\n# My Title\n\nbody\n"
	if _, err := WritePlan(dir, "p", content, nil); err != nil {
		t.Fatal(err)
	}
	doc, ok := ReadPlan(dir, "p")
	if !ok {
		t.Fatal("读取失败")
	}
	if doc.Title != "My Title" {
		t.Errorf("Title 应为 My Title，实得 %q", doc.Title)
	}
	if doc.Status != StatusApproved {
		t.Errorf("Status 应为 APPROVED，实得 %q", doc.Status)
	}
	if doc.Path != ".rivet/plans/p.md" {
		t.Errorf("Path 不符：%q", doc.Path)
	}
	if doc.CreatedAt <= 0 {
		t.Errorf("CreatedAt 应为正，实得 %d", doc.CreatedAt)
	}
}

// TestReadPlanMissing —— 不存在的计划返回 false。
func TestReadPlanMissing(t *testing.T) {
	if _, ok := ReadPlan(t.TempDir(), "nope"); ok {
		t.Error("不存在的计划应返回 false")
	}
}

// TestListPlansSkipsDrafts —— ListPlans 过滤草稿。
func TestListPlansSkipsDrafts(t *testing.T) {
	dir := t.TempDir()
	for _, slug := range []string{"real-plan", "draft-1751600000000", "another"} {
		if _, err := WritePlan(dir, slug, "# "+slug+"\n\nbody\n", nil); err != nil {
			t.Fatal(err)
		}
	}
	plans := ListPlans(dir)
	if len(plans) != 2 {
		t.Fatalf("应列出 2 个（排除草稿），实得 %d", len(plans))
	}
	for _, p := range plans {
		if IsDraftSlug(p.Slug) {
			t.Errorf("草稿不应出现在列表：%s", p.Slug)
		}
	}
}

// TestListPlansMissingDir —— 目录不存在返回空（非报错）。
func TestListPlansMissingDir(t *testing.T) {
	if got := ListPlans(t.TempDir()); len(got) != 0 {
		t.Errorf("无目录应返回空，实得 %d", len(got))
	}
}

// TestApproveRejectPlan —— 审批/拒绝写状态标记且**保留 options**。
//
// **注意 approve → reject 的语义**（TS 行为，探针实测确认）：
// `insertPlanStatusMarker` **不幂等**（不剥旧标记），故 reject 会在文件头
// **叠加**第二个标记；`parsePlanStatus` 有**固定优先级** EXECUTED > APPROVED >
// REJECTED，所以叠加后仍解析为 `approved`。
//
// 探针（tsx 真跑 TS）确认：
//
//	> **Status: APPROVED** — ...
//	> **Status: REJECTED** — ...
//	parsePlanStatus = approved
//
// 这不是 bug，是 TS 既有行为——照抄。故本测试分开验证两个方向，
// 不假设「后写的状态胜出」。
func TestApproveRejectPlan(t *testing.T) {
	dir := t.TempDir()
	opts := []PlanOption{{Label: "A", Description: "da"}, {Label: "B", Description: "db"}}
	if _, err := WritePlan(dir, "p", "# Plan\n\nbody\n", opts); err != nil {
		t.Fatal(err)
	}

	doc, ok := ApprovePlan(dir, "p")
	if !ok {
		t.Fatal("approve 失败")
	}
	if doc.Status != StatusApproved {
		t.Errorf("Status 应为 approved，实得 %q", doc.Status)
	}
	// **关键**：options 必须保留——WritePlan 会剥旧 frontmatter，不透传就丢了。
	if len(doc.Options) != 2 {
		t.Errorf("approve 后 options 应保留 2 个，实得 %d：%+v", len(doc.Options), doc.Options)
	}
}

// TestRejectPlan —— 拒绝写 REJECTED 标记（独立于 approve 验证）。
func TestRejectPlan(t *testing.T) {
	dir := t.TempDir()
	opts := []PlanOption{{Label: "A", Description: "da"}, {Label: "B", Description: "db"}}
	if _, err := WritePlan(dir, "p", "# Plan\n\nbody\n", opts); err != nil {
		t.Fatal(err)
	}
	doc, ok := RejectPlan(dir, "p")
	if !ok {
		t.Fatal("reject 失败")
	}
	if doc.Status != StatusRejected {
		t.Errorf("Status 应为 rejected，实得 %q", doc.Status)
	}
	if len(doc.Options) != 2 {
		t.Errorf("reject 后 options 应保留 2 个，实得 %d", len(doc.Options))
	}
}

// TestParsePlanStatusPriority —— **优先级固定** EXECUTED > APPROVED > REJECTED。
//
// 对账 TS `parsePlanStatus`（plan-store.ts:422-427）。这条锁定「取第一个匹配」
// 这个错误实现——标记叠加时两者给出不同答案。
func TestParsePlanStatusPriority(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    PlanStatus
	}{
		{"无标记", "# Plan\n\nbody\n", StatusSubmitted},
		{"仅 APPROVED", "> **Status: APPROVED** — x\n\n# P\n", StatusApproved},
		{"仅 REJECTED", "> **Status: REJECTED** — x\n\n# P\n", StatusRejected},
		{"仅 EXECUTED", "> **Status: EXECUTED** — x\n\n# P\n", StatusExecuted},
		// **叠加**：REJECTED 在前、APPROVED 在后 → 优先级让 APPROVED 胜出。
		{"REJECTED 先 APPROVED 后", "> **Status: REJECTED** — x\n\n> **Status: APPROVED** — y\n\n# P\n", StatusApproved},
		// APPROVED 先、REJECTED 后 → 仍是 APPROVED（不是「后者胜」）。
		{"APPROVED 先 REJECTED 后", "> **Status: APPROVED** — x\n\n> **Status: REJECTED** — y\n\n# P\n", StatusApproved},
		// EXECUTED 优先级最高。
		{"EXECUTED 与其他并存", "> **Status: APPROVED** — x\n\n> **Status: EXECUTED** — y\n\n# P\n", StatusExecuted},
		// 大小写不敏感。
		{"小写标记", "> **status: approved** — x\n\n# P\n", StatusApproved},
	}
	for _, c := range cases {
		if got := ParsePlanStatus(c.content); got != c.want {
			t.Errorf("[%s] want %q got %q", c.name, c.want, got)
		}
	}
}

// TestDeletePlan —— 删除存在/不存在的计划。
func TestDeletePlan(t *testing.T) {
	dir := t.TempDir()
	if _, err := WritePlan(dir, "p", "# Plan\n", nil); err != nil {
		t.Fatal(err)
	}
	if !DeletePlan(dir, "p") {
		t.Error("删除存在的计划应返回 true")
	}
	if DeletePlan(dir, "p") {
		t.Error("删除不存在的计划应返回 false")
	}
}

// TestParsePlanOptions —— frontmatter 选项解析（含过滤非法项）。
func TestParsePlanOptions(t *testing.T) {
	dir := t.TempDir()
	opts := []PlanOption{{Label: "推荐", Description: "推荐说明"}, {Label: "备选", Description: "备选说明"}}
	if _, err := WritePlan(dir, "p", "# Plan\n\nbody\n", opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "p.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := ParsePlanOptions(string(raw))
	if len(got) != 2 {
		t.Fatalf("应解析 2 个选项，实得 %d：%+v", len(got), got)
	}
	if got[0].Label != "推荐" || got[0].Description != "推荐说明" {
		t.Errorf("选项 0 不符：%+v", got[0])
	}

	// 无 frontmatter → nil。
	if got := ParsePlanOptions("# Plain\n\nbody\n"); got != nil {
		t.Errorf("无 frontmatter 应返回 nil，实得 %+v", got)
	}
	// 非法 JSON → nil（不 panic）。
	if got := ParsePlanOptions("---\nrivet-options: not-json\n---\n\n# P\n"); got != nil {
		t.Errorf("非法 JSON 应返回 nil，实得 %+v", got)
	}
}

// TestStripPlanChrome —— 剥离 frontmatter 与 Status/Model 行。
func TestStripPlanChrome(t *testing.T) {
	content := "---\nrivet-options: []\n---\n\n> **Status: APPROVED** — x\n\n# Title\n\n> **Model: m (cheap)**\n\nbody\n"
	lines := StripPlanChrome(content)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "rivet-options") {
		t.Error("frontmatter 应被剥离")
	}
	if strings.Contains(joined, "> **Status:") || strings.Contains(joined, "> **Model:") {
		t.Error("Status/Model 行应被剥离")
	}
	if !strings.Contains(joined, "# Title") || !strings.Contains(joined, "body") {
		t.Errorf("正文应保留：%q", joined)
	}
}

// TestStripPlanChromeKeepsBodySeparator —— 正文中的 `---` 分隔线保留。
//
// **反证**：确保不是「剥掉所有 ---」。
func TestStripPlanChromeKeepsBodySeparator(t *testing.T) {
	content := "# Title\n\ntext\n\n---\n\nmore\n"
	lines := StripPlanChrome(content)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "---") {
		t.Errorf("正文分隔线应保留：%q", joined)
	}
}

// TestResolvePlanOptionLabel —— 方案名解析（大小写/空白/括号后缀容忍）。
func TestResolvePlanOptionLabel(t *testing.T) {
	opts := []PlanOption{
		{Label: "方案 A (Recommended)", Description: "d1"},
		{Label: "方案 B", Description: "d2"},
	}
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"方案 A (Recommended)", "方案 A (Recommended)", true},
		{"方案 a (recommended)", "方案 A (Recommended)", true},
		{"  方案 A  ", "方案 A (Recommended)", true}, // 括号后缀容忍 + trim
		{"方案 B", "方案 B", true},
		{"不存在", "", false},
	}
	for _, c := range cases {
		got, ok := ResolvePlanOptionLabel(opts, c.input)
		if ok != c.ok || got != c.want {
			t.Errorf("ResolvePlanOptionLabel(%q) want (%q,%v) got (%q,%v)", c.input, c.want, c.ok, got, ok)
		}
	}
}

// TestNowISOFormat —— ISO 格式与 JS `toISOString()` 逐字一致。
//
// 该字符串进计划文件内容，格式漂移会破坏前缀缓存稳定性。
func TestNowISOFormat(t *testing.T) {
	got := nowISO()
	// 形如 2026-09-22T05:12:07.169Z
	if len(got) != 24 {
		t.Fatalf("长度应为 24，实得 %d：%q", len(got), got)
	}
	if got[4] != '-' || got[7] != '-' || got[10] != 'T' || got[19] != '.' || got[23] != 'Z' {
		t.Errorf("格式不符 JS toISOString：%q", got)
	}
}
