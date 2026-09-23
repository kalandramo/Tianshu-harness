package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/skills"
)

// newSkillRegistry 建一个含固定夹具的注册表。
//
// **每个测试独立建**——TS 的测试共用进程级单例 + beforeEach 重注册，
// 有跨测试污染风险；Go 侧参数化注入避免该问题。
func newSkillRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	r := skills.NewRegistry()
	r.Register(skills.Definition{Name: "small", Description: "small one", Body: "do the thing"})
	r.Register(skills.Definition{Name: "jumbo", Description: "big one", Body: strings.Repeat("y", 20000)})
	return r
}

func callSkill(t *testing.T, reg *skills.Registry, input map[string]any) (contract.Result, error) {
	t.Helper()
	p := &CallParams{Input: input, Cwd: "/tmp", ToolUseID: "t", SkillRegistry: reg}
	return Skill().Execute(context.Background(), p)
}

// ── 行为对账：TS `__tests__/skill-tool.test.ts` 的 11 个用例 ──

// 用例 1：返回**完整正文，零截断**——首尾字符都在。
func TestSkillReturnsFullBody(t *testing.T) {
	reg := newSkillRegistry(t)
	res, err := callSkill(t, reg, map[string]any{"name": "jumbo"})
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.IsError {
		t.Error("不应报错")
	}
	big := strings.Repeat("y", 20000)
	if !strings.Contains(res.Content, big) {
		t.Error("20KB 正文未完整保留")
	}
	if len(res.Content) < 20000 {
		t.Errorf("content 长度 = %d, want >= 20000", len(res.Content))
	}
}

// 用例 2：正文包在 skill 标签里。
func TestSkillWrapsBody(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": "small"})
	want := "<skill name=\"small\">\ndo the thing\n</skill>"
	if res.Content != want {
		t.Errorf("content = %q, want %q", res.Content, want)
	}
}

// 用例 3：未知 skill → 友好错误含可用列表，不抛。
func TestSkillUnknownFriendlyError(t *testing.T) {
	reg := newSkillRegistry(t)
	res, err := callSkill(t, reg, map[string]any{"name": "does-not-exist"})
	if err != nil {
		t.Fatalf("不应抛错：%v", err)
	}
	if !res.IsError {
		t.Error("应标记 isError")
	}
	for _, want := range []string{"未找到 skill", "可用 skill：", "small"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content 应含 %q：%q", want, res.Content)
		}
	}
}

// 用例 4：退役 skill 名 → 映射到原生流程，**不是错误**。
func TestSkillRetiredMapsToNativeFlow(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": "executing-plans"})
	if res.IsError {
		t.Error("退役名不应报错")
	}
	if !strings.Contains(res.Content, "已退役并内置为原生流程") {
		t.Errorf("应含退役提示：%q", res.Content)
	}
	if !strings.Contains(res.Content, "<plan-executing>") {
		t.Errorf("应指向 <plan-executing>：%q", res.Content)
	}
	if strings.Contains(res.Content, "未找到 skill") {
		t.Errorf("不应含「未找到」：%q", res.Content)
	}
}

// 用例 5：缺 name → 报错。
func TestSkillMissingName(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": ""})
	if !res.IsError {
		t.Error("应报错")
	}
	if !strings.Contains(res.Content, "name 必填") {
		t.Errorf("应含 name 必填：%q", res.Content)
	}
}

// 用例 6：**缓存安全**——工具定义不嵌入任何具体 skill 名。
func TestSkillDefinitionIsCacheSafe(t *testing.T) {
	def := Skill().Definition()
	if def.Name != "skill" {
		t.Errorf("名 = %q", def.Name)
	}
	for _, leak := range []string{"jumbo", "small"} {
		if strings.Contains(def.Description, leak) {
			t.Errorf("描述泄漏了具体 skill 名 %q——会破坏前缀缓存", leak)
		}
	}
	if Skill().RequiresApproval(nil) {
		t.Error("不需要批准")
	}
	if !Skill().ConcurrencySafe() {
		t.Error("应可并发")
	}
}

// 用例 7：空正文也优雅处理（仍产出合法包裹结果）。
func TestSkillEmptyBody(t *testing.T) {
	reg := newSkillRegistry(t)
	reg.Register(skills.Definition{Name: "empty", Description: "no body", Body: ""})
	res, _ := callSkill(t, reg, map[string]any{"name": "empty"})
	if res.IsError {
		t.Error("不应报错")
	}
	want := "<skill name=\"empty\">\n\n</skill>"
	if res.Content != want {
		t.Errorf("content = %q, want %q", res.Content, want)
	}
}

// 用例 8：扁平 skill（无 SkillDir）→ **无** skill-files 块。
func TestSkillFlatHasNoFilesBlock(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": "small"})
	if strings.Contains(res.Content, "<skill-files") {
		t.Errorf("扁平 skill 不应有 skill-files：%q", res.Content)
	}
}

// 用例 9：目录型 skill → 正文后附加 skill-files 树。
func TestSkillDirectoryAppendsFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pdf")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "api.md"), []byte("api"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extract.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := skills.NewRegistry()
	reg.Register(skills.Definition{
		Name: "pdf", Description: "pdf skill", Body: "ROUTER", SkillDir: dir,
	})

	res, _ := callSkill(t, reg, map[string]any{"name": "pdf"})
	if res.IsError {
		t.Error("不应报错")
	}
	if !strings.Contains(res.Content, "<skill name=\"pdf\">\nROUTER\n</skill>") {
		t.Errorf("正文未保留：%q", res.Content)
	}
	if !strings.Contains(res.Content, `<skill-files dir="`+dir+`"`) {
		t.Errorf("应含 skill-files 块：%q", res.Content)
	}
	if !strings.Contains(res.Content, "references/api.md") {
		t.Errorf("应含子文件路径：%q", res.Content)
	}
	if !strings.Contains(res.Content, "extract.py") {
		t.Errorf("应含顶层文件：%q", res.Content)
	}
	if strings.Contains(res.Content, "SKILL.md") {
		t.Errorf("不应含 SKILL.md 自身：%q", res.Content)
	}
}

// 用例 10：加载 skill 时触发 onSkillInvoked。
func TestSkillFiresOnInvoked(t *testing.T) {
	reg := newSkillRegistry(t)
	var invoked []string
	p := &CallParams{
		Input:          map[string]any{"name": "small"},
		Cwd:            "/tmp",
		ToolUseID:      "t",
		SkillRegistry:  reg,
		OnSkillInvoked: func(n string) { invoked = append(invoked, n) },
	}
	if _, err := Skill().Execute(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(invoked) != 1 || invoked[0] != "small" {
		t.Errorf("invoked = %v, want [small]", invoked)
	}
}

// 用例 11：complete=true 标记完成。
func TestSkillCompleteMarksFinished(t *testing.T) {
	reg := newSkillRegistry(t)
	var completed []string
	p := &CallParams{
		Input:            map[string]any{"name": "small", "complete": true},
		Cwd:              "/tmp",
		ToolUseID:        "t",
		SkillRegistry:    reg,
		OnSkillCompleted: func(n string) { completed = append(completed, n) },
	}
	res, _ := Skill().Execute(context.Background(), p)
	if res.IsError {
		t.Error("不应报错")
	}
	if !strings.Contains(res.Content, "已标记为完成") {
		t.Errorf("应含完成提示：%q", res.Content)
	}
	if len(completed) != 1 || completed[0] != "small" {
		t.Errorf("completed = %v, want [small]", completed)
	}
}

// ── 补充反证（TS 未覆盖但语义重要）──

// **反证**：complete=false 时**不**触发完成回调，且正常加载。
//
// 若实现把 `complete` 当 truthy 判定（如 `p.Input["complete"] != nil`），
// 此测试转红——模型显式传 false 会被误判为「标记完成」。
func TestSkillCompleteFalseStillLoads(t *testing.T) {
	reg := newSkillRegistry(t)
	completedCalled := false
	invokedCalled := false
	p := &CallParams{
		Input:            map[string]any{"name": "small", "complete": false},
		Cwd:              "/tmp",
		ToolUseID:        "t",
		SkillRegistry:    reg,
		OnSkillCompleted: func(string) { completedCalled = true },
		OnSkillInvoked:   func(string) { invokedCalled = true },
	}
	res, _ := Skill().Execute(context.Background(), p)
	if completedCalled {
		t.Error("complete=false 不应触发完成回调")
	}
	if !invokedCalled {
		t.Error("complete=false 应走加载路径")
	}
	if !strings.Contains(res.Content, "do the thing") {
		t.Errorf("应加载正文：%q", res.Content)
	}
}

// **反证**：名字查找大小写不敏感（对账 TS 的 Find 回退）。
func TestSkillCaseInsensitiveLookup(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": "SMALL"})
	if res.IsError {
		t.Fatalf("大小写不敏感查找失败：%q", res.Content)
	}
	// 回退命中后应使用**注册表里的名字**（small），不是用户输入。
	if !strings.Contains(res.Content, `<skill name="small">`) {
		t.Errorf("应用注册名：%q", res.Content)
	}
}

// **反证**：退役名映射用**表里的名字**（大小写不敏感匹配）。
func TestSkillRetiredCaseInsensitive(t *testing.T) {
	reg := newSkillRegistry(t)
	res, _ := callSkill(t, reg, map[string]any{"name": "EXECUTING-PLANS"})
	if res.IsError {
		t.Error("大写退役名不应报错")
	}
	if !strings.Contains(res.Content, "executing-plans") {
		t.Errorf("应用表里登记的名字：%q", res.Content)
	}
}

// **反证**：注册表为 nil 时回退到包级 Default（不 panic）。
func TestSkillNilRegistryFallsBackToDefault(t *testing.T) {
	p := &CallParams{Input: map[string]any{"name": "nonexistent"}, Cwd: "/tmp"}
	res, err := Skill().Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("不应抛错：%v", err)
	}
	if !res.IsError {
		t.Error("未知名应报错（Default 里无该 skill）")
	}
}

// 空注册表的错误提示用「（未加载任何 skill）」占位。
func TestSkillEmptyRegistryMessage(t *testing.T) {
	reg := skills.NewRegistry()
	res, _ := callSkill(t, reg, map[string]any{"name": "anything"})
	if !strings.Contains(res.Content, "（未加载任何 skill）") {
		t.Errorf("空注册表应有占位提示：%q", res.Content)
	}
}

// uiContent 逐字对账（用户可见字符串）。
func TestSkillUIContentTexts(t *testing.T) {
	reg := newSkillRegistry(t)

	t.Run("扁平", func(t *testing.T) {
		res, _ := callSkill(t, reg, map[string]any{"name": "small"})
		if res.UIContent != "已加载 skill：small" {
			t.Errorf("uiContent = %q", res.UIContent)
		}
	})
	t.Run("完成", func(t *testing.T) {
		res, _ := callSkill(t, reg, map[string]any{"name": "small", "complete": true})
		if res.UIContent != "已完成 skill：small" {
			t.Errorf("uiContent = %q", res.UIContent)
		}
	})
	t.Run("退役", func(t *testing.T) {
		res, _ := callSkill(t, reg, map[string]any{"name": "executing-plans"})
		if res.UIContent != "已映射到原生流程：executing-plans" {
			t.Errorf("uiContent = %q", res.UIContent)
		}
	})
}
