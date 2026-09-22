package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// store.go —— 计划文件的磁盘存储。
//
// 对账 TS `src/plan/plan-store.ts`（427 行）。

// PlansDir 是计划目录（相对项目根）。
//
// 对账 TS `PLANS_DIR`（plan-store.ts:93）。
const PlansDir = ".rivet/plans"

// planOptionsFrontmatterRe 匹配选项 frontmatter 块。
//
// 对账 TS `PLAN_OPTIONS_FRONTMATTER_RE`（plan-store.ts:44）：
//
//	/^---\r?\nrivet-options:\s*(\[[\s\S]*?\])\s*\r?\n---\r?\n/
//
// **用 `\r?\n` 而非入口归一化**：此 regex 还用于 replace 回写文件，
// 归一化会改动用户文件的换行风格。
var planOptionsFrontmatterRe = regexp.MustCompile(`^---\r?\nrivet-options:\s*(\[[\s\S]*?\])\s*\r?\n---\r?\n`)

// planTitleRe 匹配 H1 标题并捕获标题文本。
var planTitleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// PlanOption 是计划的备选方案。
//
// 对账 TS `PlanOption`（plan-store.ts:37-40）。
type PlanOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// PlanDocument 是读出的计划文档。
//
// 对账 TS `PlanDocument`（plan-store.ts:14-35）。
type PlanDocument struct {
	Slug      string
	Title     string
	Content   string
	Path      string
	CreatedAt int64 // Unix 毫秒（对账 TS 的 Date）
	Status    PlanStatus
	Options   []PlanOption
	Model     string
	ModelTier string
}

// plansRoot 返回计划目录的绝对路径。
func plansRoot(cwd string) string {
	return filepath.Join(cwd, PlansDir)
}

// planFilePath 返回单个计划文件的绝对路径。
func planFilePath(cwd, slug string) string {
	return filepath.Join(plansRoot(cwd), slug+".md")
}

// ensurePlansDir 确保计划目录存在。
//
// 对账 TS `ensurePlansDir`（plan-store.ts:105-111）。
func ensurePlansDir(cwd string) error {
	return os.MkdirAll(plansRoot(cwd), 0o755)
}

// extractTitle 从 markdown 提取 H1 标题。
//
// 对账 TS `extractTitle`（plan-store.ts:131-134）。无 H1 返回 "Untitled Plan"。
func extractTitle(content string) string {
	m := planTitleRe.FindStringSubmatch(content)
	if m == nil {
		return "Untitled Plan"
	}
	return strings.TrimSpace(m[1])
}

// ParsePlanOptions 从计划 frontmatter 解析多方案选项。
//
// 对账 TS `parsePlanOptions`（plan-store.ts:137-158`）。
// 无 frontmatter / 解析失败 / 非数组 → nil；过滤掉缺 label 或 description 的项。
func ParsePlanOptions(content string) []PlanOption {
	m := planOptionsFrontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(m[1]), &parsed); err != nil {
		return nil
	}
	out := make([]PlanOption, 0, len(parsed))
	for _, item := range parsed {
		label, ok1 := item["label"].(string)
		desc, ok2 := item["description"].(string)
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, PlanOption{Label: label, Description: desc})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildPlanFrontmatter 构造选项 frontmatter。
//
// 对账 TS `buildPlanFrontmatter`（plan-store.ts:215-219）。
func buildPlanFrontmatter(options []PlanOption) string {
	if len(options) == 0 {
		return ""
	}
	// **必须用 json.Marshal**——手拼 JSON 会在 Windows 路径上产生 `\U` 非法转义。
	// （本仓库既有教训。）
	raw, err := json.Marshal(options)
	if err != nil {
		return ""
	}
	return "---\nrivet-options: " + string(raw) + "\n---\n\n"
}

// WritePlan 写计划文件，返回相对 cwd 的路径。
//
// 对账 TS `writePlan`（plan-store.ts:221-233`）。
//
// **非原子写**（对账 TS 用 `writeFile`）——与 close 路径的原子写**故意不一致**。
// 这是 TS 既有行为，照抄（原子性差异可能被测试锁定）。
func WritePlan(cwd, slug, content string, options []PlanOption) (string, error) {
	if err := ensurePlansDir(cwd); err != nil {
		return "", err
	}
	filePath := planFilePath(cwd, slug)
	body := buildPlanFrontmatter(options) + planOptionsFrontmatterRe.ReplaceAllString(content, "")
	if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(PlansDir, slug+".md")), nil
}

// ReadPlan 读单个计划。
//
// 对账 TS `readPlan`（plan-store.ts:235-262`）。任何错误 → (nil, false)。
func ReadPlan(cwd, slug string) (*PlanDocument, bool) {
	filePath := planFilePath(cwd, slug)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, false
	}
	content := string(raw)
	doc := &PlanDocument{
		Slug:      slug,
		Title:     extractTitle(content),
		Content:   content,
		Path:      filepath.ToSlash(filepath.Join(PlansDir, slug+".md")),
		CreatedAt: birthTimeMillis(info),
		Status:    ParsePlanStatus(content),
		Options:   ParsePlanOptions(content),
	}
	if p := ParsePlanModel(content); p != nil {
		doc.Model = p.Model
		doc.ModelTier = p.Tier
	}
	return doc, true
}

// ListPlans 列出所有计划（按 createdAt 降序）。
//
// 对账 TS `listPlans`（plan-store.ts:307-327`）。目录不存在 → 空；
// 跳过草稿与不可读项。
func ListPlans(cwd string) []*PlanDocument {
	dir := plansRoot(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var plans []*PlanDocument
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		slug := strings.TrimSuffix(name, ".md")
		if IsDraftSlug(slug) {
			continue
		}
		if doc, ok := ReadPlan(cwd, slug); ok {
			plans = append(plans, doc)
		}
	}
	sort.SliceStable(plans, func(i, j int) bool {
		return plans[i].CreatedAt > plans[j].CreatedAt
	})
	return plans
}

// ApprovePlan 标记计划为已批准。
//
// 对账 TS `approvePlan`（plan-store.ts:362-364`）。
func ApprovePlan(cwd, slug string) (*PlanDocument, bool) {
	return markPlanStatus(cwd, slug, StatusApproved)
}

// RejectPlan 拒绝计划：写 REJECTED 标记而非删除文件，保留原稿供 agent 修订。
//
// 对账 TS `rejectPlan`（plan-store.ts:370-372`）。
func RejectPlan(cwd, slug string) (*PlanDocument, bool) {
	return markPlanStatus(cwd, slug, StatusRejected)
}

// markPlanStatus 在第一个 H1 前插入状态标记并回写。
//
// 对账 TS `markPlanStatus`（plan-store.ts:391-406`）。
//
// **透传 options**——WritePlan 会剥离旧 frontmatter，不传会把多方案记录抹掉，
// 导致 approve 后 selectedApproach 校验永远跳过（见 TS 2026-07-03 缺陷复盘）。
func markPlanStatus(cwd, slug string, status PlanStatus) (*PlanDocument, bool) {
	plan, ok := ReadPlan(cwd, slug)
	if !ok {
		return nil, false
	}
	newContent := InsertPlanStatusMarker(plan.Content, status, nowISO())
	if _, err := WritePlan(cwd, slug, newContent, plan.Options); err != nil {
		return nil, false
	}
	return ReadPlan(cwd, slug)
}

// DeletePlan 删除计划文件。
//
// 对账 TS `deletePlan`（plan-store.ts:408-419`）。不存在返回 false。
func DeletePlan(cwd, slug string) bool {
	filePath := planFilePath(cwd, slug)
	if _, err := os.Stat(filePath); err != nil {
		return false
	}
	return os.Remove(filePath) == nil
}

// StripPlanChrome 剥掉计划文件里的非正文「chrome」行。
//
// 对账 TS `stripPlanChrome`（plan-store.ts:290-305`）。返回正文行数组：
//   - 开头一处 frontmatter（`--- ... ---`，正文中的 `---` 分隔线保留）
//   - `> **Status:` / `> **Model:` 留痕行
func StripPlanChrome(content string) []string {
	rawLines := strings.Split(content, "\n")
	start := 0
	if len(rawLines) > 0 && strings.TrimSpace(rawLines[0]) == "---" {
		closeIdx := -1
		for i := 1; i < len(rawLines); i++ {
			if strings.TrimSpace(rawLines[i]) == "---" {
				closeIdx = i
				break
			}
		}
		if closeIdx > 0 {
			start = closeIdx + 1
		}
	}
	out := make([]string, 0, len(rawLines))
	for _, raw := range rawLines[start:] {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "> **Status:") || strings.HasPrefix(t, "> **Model:") {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// ResolvePlanOptionLabel 在方案列表里解析用户输入的方案名。
//
// 对账 TS `resolvePlanOptionLabel`（plan-store.ts:161-178`）。
// 大小写不敏感、忽略首尾空白，并容忍省略 "(Recommended)" 一类括号后缀。
// 命中返回**规范标签**（options 中的原始 label）。
func ResolvePlanOptionLabel(options []PlanOption, input string) (string, bool) {
	normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	stripSuffix := func(s string) string {
		return optionSuffixRe.ReplaceAllString(normalize(s), "")
	}
	wanted := normalize(input)
	for _, o := range options {
		if normalize(o.Label) == wanted {
			return o.Label, true
		}
	}
	for _, o := range options {
		if stripSuffix(o.Label) == stripSuffix(input) {
			return o.Label, true
		}
	}
	return "", false
}

// optionSuffixRe 剥掉标签尾部的括号后缀（如 "(Recommended)"）。
//
// 对账 TS `/\s*\([^)]*\)\s*$/`。
var optionSuffixRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
