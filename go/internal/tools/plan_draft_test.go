package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plan_draft_test.go —— `submit` 的草稿回读与回收（第一百零七刀）。
//
// # 本文件修的缺口
//
// `plan` 工具的**描述**与 **plan-mode 指令块**（`prompt/modeblocks.go`）都告诉
// 模型「省略 `plan` 字段则从活动计划文件读取」——对账 TS
// `planSubmitExecute`（`plan.ts:399-425`）：
//
//	const draftPath = params.activePlanFilePath
//	if (!draftPath) { return { content: '错误：未设置活动计划文件时 plan 必填…' } }
//	draftText = await readFile(join(params.cwd, draftPath), 'utf-8')
//	planContent = draftText
//
// 而 Go 侧**只读 `Input["plan"]`，从不回读草稿**，且 `CallParams` 连
// `ActivePlanFilePath` 字段都没有（TS `types.ts:301` 有）。
//
// **后果**：模型照 plan-mode 指令块办——`enter_mode`（建草稿）→ 用
// `write_file` 写草稿 → 省略 `plan` 字段 submit → **失败**。
// 那条指令对 Go 是假话，且它此刻正进模型上下文。
//
// # 测试纪律
//
// 每条都走**真 `Plan()` 工具 + 真文件**（不 mock 文件系统）——草稿回读的
// 正确性完全取决于「路径对不对、读得到吗、读回的是不是那份内容」。

// writeDraft 在 cwd 下建一份 plan-mode 草稿并返回相对路径。
//
// 用与生产同一形态的路径（`.rivet/plans/draft-<n>.md`）——**不用临时随机名**：
// 草稿回收分支的判据正是 slug 形态（`plan.IsDraftSlug`）。
func writeDraft(t *testing.T, cwd, content string) string {
	t.Helper()
	rel := ".rivet/plans/draft-1790513827780.md"
	abs := filepath.Join(cwd, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return rel
}

// validPlanBody 是能过全部软门禁的计划正文（需求提炼 + mermaid + 反证）。
func validPlanBody(title string) string {
	return "# " + title + "\n\n" +
		"## 需求提炼\n\n目标：验证草稿回读。非目标：无。\n\n" +
		"```mermaid\nflowchart TD\n    A(输入) --> B([输出])\n```\n\n" +
		"## 反证/复现\n\n断言 1——证据：本文件。\n"
}

// TestPlanSubmitReadsFromDraft —— ★ 核心：省略 plan 字段时回读草稿。
//
// 对账 TS `plan.ts:401-424`。
func TestPlanSubmitReadsFromDraft(t *testing.T) {
	dir := t.TempDir()
	draftRel := writeDraft(t, dir, validPlanBody("从草稿提交的计划"))

	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "从草稿提交的计划"},
		ActivePlanFilePath: draftRel,
	}
	content, isErr := planToolIsError(t, p)
	if isErr {
		t.Fatalf("有草稿时应回读并提交成功，实得错误：%q", content)
	}
	if !strings.Contains(content, "计划已提交") {
		t.Errorf("应报提交成功：%q", content)
	}

	// 落盘的规范文件应含草稿正文（不是空/占位）。
	slug := "从草稿提交的计划"
	written, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", slug+".md"))
	if err != nil {
		t.Fatalf("规范计划文件应存在：%v", err)
	}
	if !strings.Contains(string(written), "验证草稿回读") {
		t.Errorf("落盘内容应来自草稿，实得：%q", string(written))
	}
}

// TestPlanSubmitNoDraftNoPlanFails —— 无草稿且无 plan → 报错（fail-closed）。
//
// **保留 fail-closed**：对账 TS 的 `if (!draftPath)` 分支。修「恒报错」
// 不能修成「静默成功」。
func TestPlanSubmitNoDraftNoPlanFails(t *testing.T) {
	content, isErr := planToolIsError(t, newPlanParams(t.TempDir(), map[string]any{
		"action": "submit", "title": "T",
	}))
	if !isErr {
		t.Fatalf("无草稿无 plan 应报错，实得成功：%q", content)
	}
	// **文案订正**：原文案写「Go 运行时暂无 plan mode 活动计划文件（enter_mode 未支持）」
	// ——enter_mode 早已实现（第七十九刀）。新文案对账 TS「未设置活动计划文件时 plan 必填」。
	if strings.Contains(content, "enter_mode 未支持") {
		t.Errorf("不该再声称「enter_mode 未支持」（早已实现）：%q", content)
	}
	if !strings.Contains(content, "plan 必填") {
		t.Errorf("应说明 plan 必填：%q", content)
	}
}

// TestPlanSubmitDraftEmptyFails —— 草稿为空 → 报错（对账 TS `plan.ts:418-421`）。
func TestPlanSubmitDraftEmptyFails(t *testing.T) {
	dir := t.TempDir()
	draftRel := writeDraft(t, dir, "   \n\n")

	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "T"},
		ActivePlanFilePath: draftRel,
	}
	content, isErr := planToolIsError(t, p)
	if !isErr {
		t.Fatalf("空草稿应报错，实得成功：%q", content)
	}
	if !strings.Contains(content, "活动计划文件为空") {
		t.Errorf("应报草稿为空：%q", content)
	}
}

// TestPlanSubmitDraftMissingFileFails —— 草稿路径存在但文件不在 → 报读失败。
//
// 对账 TS `plan.ts:411-415` 的 `catch` 分支。
func TestPlanSubmitDraftMissingFileFails(t *testing.T) {
	dir := t.TempDir()
	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "T"},
		ActivePlanFilePath: ".rivet/plans/draft-999.md", // 不创建
	}
	content, isErr := planToolIsError(t, p)
	if !isErr {
		t.Fatalf("草稿文件不存在应报错，实得成功：%q", content)
	}
	if !strings.Contains(content, "读取活动计划文件失败") {
		t.Errorf("应报读取失败：%q", content)
	}
}

// TestPlanSubmitRecyclesDraft —— ★ 提交成功后回收草稿（对账 TS `plan.ts:583-590`）。
//
// TS 注释：草稿内容已进规范计划文件，删掉草稿避免它作为孤儿残留
// （也会与已提交的计划重复）。**best-effort**：清理失败不阻塞 submit。
func TestPlanSubmitRecyclesDraft(t *testing.T) {
	dir := t.TempDir()
	draftRel := writeDraft(t, dir, validPlanBody("回收草稿"))

	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "回收草稿"},
		ActivePlanFilePath: draftRel,
	}
	if content, isErr := planToolIsError(t, p); isErr {
		t.Fatalf("提交应成功：%q", content)
	}

	draftAbs := filepath.Join(dir, filepath.FromSlash(draftRel))
	if _, err := os.Stat(draftAbs); !os.IsNotExist(err) {
		t.Errorf("草稿应在提交成功后被回收（对账 TS plan.ts:588），但仍存在：%v", err)
	}
}

// TestPlanSubmitKeepsNonDraftActiveFile —— ★ 活动文件**非草稿形态**时不删。
//
// 对账 TS `plan.ts:588` 的 `isDraftSlug(...)` 守卫：修订会话里
// activePlanFilePath 就是规范文件本身，删它等于**毁掉用户已批准的计划**。
// 这个守卫是防数据丢失的，必须有对抗性用例。
func TestPlanSubmitKeepsNonDraftActiveFile(t *testing.T) {
	dir := t.TempDir()
	// 非草稿形态的 slug（用户已批准的计划文件名）。
	rel := ".rivet/plans/my-existing-plan.md"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(validPlanBody("修订既有计划")), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "修订既有计划"},
		ActivePlanFilePath: rel,
	}
	if content, isErr := planToolIsError(t, p); isErr {
		t.Fatalf("提交应成功：%q", content)
	}

	if _, err := os.Stat(abs); err != nil {
		t.Errorf("非草稿形态的活动文件**不得**被回收（会毁掉既有计划）：%v", err)
	}
}

// TestPlanSubmitExplicitPlanBeatsDraft —— 显式 plan 优先于草稿（对账 TS 的 if 条件）。
//
// TS：`if (typeof planContent !== 'string' || !planContent.trim())` 才走草稿
// ——即显式给了 plan 就**不读**草稿。
func TestPlanSubmitExplicitPlanBeatsDraft(t *testing.T) {
	dir := t.TempDir()
	draftRel := writeDraft(t, dir, validPlanBody("草稿里的标题"))

	explicit := strings.Replace(validPlanBody("显式传入的标题"), "显式传入的标题", "显式传入的标题", 1)
	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "显式传入的标题", "plan": explicit},
		ActivePlanFilePath: draftRel,
	}
	content, isErr := planToolIsError(t, p)
	if isErr {
		t.Fatalf("应成功：%q", content)
	}

	written, err := os.ReadFile(filepath.Join(dir, ".rivet", "plans", "显式传入的标题.md"))
	if err != nil {
		t.Fatalf("规范文件应存在：%v", err)
	}
	if !strings.Contains(string(written), "显式传入的标题") {
		t.Errorf("落盘内容应来自显式 plan：%q", string(written))
	}
	// 未走草稿路径 → 草稿不该被回收（对账 TS：只有 submittedFromDraft 才删）。
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(draftRel))); err != nil {
		t.Errorf("未从草稿提交时不该回收草稿：%v", err)
	}
}

// TestPlanDescriptionDoesNotClaimUnsupported —— 覆盖 **Description 通路**。
//
// # 为什么补这条
//
// 既有的 `TestPlanModeUnsupportedMessageGone`（planmode_tools_test.go）只断言
// **runtime 输出**，而第七十九刀的漏网之处在 **`t.def.Description`**——同一事实
// 的第三条载体，且**进模型上下文**：模型据此认为 enter_mode 无用，永不调用。
//
// 判据：描述不得再称「暂不支持/尚未移植/暂无」，且必须含 enter_mode/exit_mode
// 的实际语义（对账 TS `src/tools/plan.ts:243-249`）。
func TestPlanDescriptionDoesNotClaimUnsupported(t *testing.T) {
	desc := Plan().Definition().Description
	for _, bad := range []string{"暂不支持", "尚未移植", "Go 侧暂无 plan mode 草稿"} {
		if strings.Contains(desc, bad) {
			t.Errorf("plan 描述不该再含 %q（机制已实现，且该描述进模型上下文）：\n%s", bad, desc)
		}
	}
	for _, want := range []string{"自主进入计划模式", "退出计划模式", "写工具将被禁用"} {
		if !strings.Contains(desc, want) {
			t.Errorf("描述应含 enter_mode/exit_mode 的真实语义 %q（对账 TS plan.ts:243-249）", want)
		}
	}
}

// TestPlanSubmitUsesActivePlanFilePathFromLoop —— 端到端：Loop 构造点注入该字段。
//
// 与 `sessionmodfiles_assembly_test.go` 同一模式：单测直调 `Plan()` 证明
// 「给了字段会回读」，但**不证明生产会注入**。本用例走 `buildToolCallParams`
// 的唯一构造点。
//
// **注意**：本测试在 `tools` 包，构造点在 `agent` 包——故此处只断言
// `CallParams` 有该字段且形态正确；跨包注入由 agent 包的装配测试覆盖
// （见 `planmode_wiring_test.go` 的补充）。
func TestPlanSubmitUsesActivePlanFilePathFromLoop(t *testing.T) {
	dir := t.TempDir()
	draftRel := writeDraft(t, dir, validPlanBody("构造点注入"))

	// 模拟构造点会做的事：填 ActivePlanFilePath。
	p := &CallParams{
		Cwd:                dir,
		Input:              map[string]any{"action": "submit", "title": "构造点注入"},
		ActivePlanFilePath: draftRel,
	}
	if content, isErr := planToolIsError(t, p); isErr {
		t.Fatalf("应成功（证明该字段真被消费）：%q", content)
	}
}

var _ = context.Background // 保持 import 稳定（后续用例可能用）
