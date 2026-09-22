package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plan_test.go —— plan 工具的行为测试。
//
// 重点覆盖：四 action 分发、硬门禁、软门禁 one-shot、指针回传拦截、
// close 的路径白名单与预览/应用分叉、enter/exit_mode 的诚实报错。

func newPlanParams(cwd string, input map[string]any) *CallParams {
	return &CallParams{Cwd: cwd, Input: input}
}

func runPlan(t *testing.T, p *CallParams) string {
	t.Helper()
	tool := Plan()
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	return res.Content
}

// planToolIsError 执行并返回 (内容, 是否错误)。
func planToolIsError(t *testing.T, p *CallParams) (string, bool) {
	t.Helper()
	res, err := Plan().Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	return res.Content, res.IsError
}

// TestPlanUnknownAction —— 未知 action 报错并列出合法值。
func TestPlanUnknownAction(t *testing.T) {
	got := runPlan(t, newPlanParams(t.TempDir(), map[string]any{"action": "bogus"}))
	if !strings.Contains(got, "未知 action") {
		t.Errorf("应报未知 action：%q", got)
	}
}

// TestPlanEnterExitModeHonestError —— **诚实声明**（Go 侧无 plan mode 状态机）。
//
// 对账本刀的设计决策：不假装成功，明确报错并给替代路径。
func TestPlanEnterExitModeHonestError(t *testing.T) {
	for _, action := range []string{"enter_mode", "exit_mode"} {
		content, isErr := planToolIsError(t, newPlanParams(t.TempDir(), map[string]any{"action": action}))
		if !isErr {
			t.Errorf("%s 应报错（未支持），实得成功：%q", action, content)
		}
		if !strings.Contains(content, "暂不支持") {
			t.Errorf("%s 应说明暂不支持：%q", action, content)
		}
		if !strings.Contains(content, "静默失效") {
			t.Errorf("%s 应说明为何不假装成功：%q", action, content)
		}
	}
}

// TestPlanSubmitRequiresTitle —— title 必填。
func TestPlanSubmitRequiresTitle(t *testing.T) {
	content, isErr := planToolIsError(t, newPlanParams(t.TempDir(), map[string]any{
		"action": "submit", "plan": "# X\n",
	}))
	if !isErr || !strings.Contains(content, "title 必填") {
		t.Errorf("应报 title 必填：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanSubmitRequiresPlan —— plan 必填（Go 侧无草稿路径）。
func TestPlanSubmitRequiresPlan(t *testing.T) {
	content, isErr := planToolIsError(t, newPlanParams(t.TempDir(), map[string]any{
		"action": "submit", "title": "T",
	}))
	if !isErr || !strings.Contains(content, "plan 必填") {
		t.Errorf("应报 plan 必填：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanSubmitPointerGuard —— **指针回传拦截**（plan 折叠指针被当正文复用）。
func TestPlanSubmitPointerGuard(t *testing.T) {
	dir := t.TempDir()
	ptr := "[plan persisted to .rivet/plans/x.md — 已提交] " + PointerInternalTag
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": "T", "plan": ptr,
	}))
	if !isErr {
		t.Fatalf("指针回传应被拦截，实得成功：%q", content)
	}
	if !strings.Contains(content, "提交被拦截") {
		t.Errorf("应说明被拦截：%q", content)
	}
	if !strings.Contains(content, PointerGuardErrorMarker) {
		t.Errorf("应含 hook 标记：%q", content)
	}
}

// TestPlanSubmitPointerGuardIdempotentUnreachable —— **实测确认的 TS 行为**：
// plan 场景下幂等化解**不可达**。
//
// tsx 探针实测（复刻 plan.ts 的调用形态）：
//
//	plan.ts 调用形态（绝对 filePath + 相对指针路径）→ null（不可达）
//	两者都绝对                                  → resolved
//
// 原因：plan 的折叠指针记的是**项目相对路径**（`.rivet/plans/x.md`），而
// `resolveIdempotentPointer` 拿 `join(cwd, relPath)` 的**绝对路径**去比——
// 两者永不相等，故走到 null → 回落硬错误。
//
// Go 侧与 TS 行为一致（同样的路径比较）。**这不是缺陷，是 TS 既有行为**：
// 硬错误文案本身已给出「先 read_file 再重提」的恢复路径。
func TestPlanSubmitPointerGuardIdempotentUnreachable(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/existing.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# Existing\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 指针记的是**相对**路径（与 TS 产出的指针一致）。
	ptr := "[plan persisted to " + rel + " — 已提交] " + PointerInternalTag

	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": "T", "plan": ptr,
	}))
	if !isErr {
		t.Fatalf("对账 TS：相对指针路径 + 绝对 filePath → 应回落硬错误，实得成功：%q", content)
	}
	if !strings.Contains(content, "提交被拦截") {
		t.Errorf("应给出硬错误文案：%q", content)
	}
	// 恢复路径必须在文案里（这是硬错误的实际价值）。
	if !strings.Contains(content, "read_file") {
		t.Errorf("硬错误应给出恢复路径：%q", content)
	}
}

// TestPlanSubmitHardGatePlaceholders —— 硬门禁：占位符簇（每次命中都拦）。
func TestPlanSubmitHardGatePlaceholders(t *testing.T) {
	dir := t.TempDir()
	body := "# T\n\n## 需求提炼\n\nTODO FIXME TBD 待补充\n"
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": "占位测试", "plan": body,
	}))
	if !isErr || !strings.Contains(content, "占位符") {
		t.Errorf("占位符簇应硬拦：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanSubmitSoftGateAggregated —— 软门禁一次列全（需求提炼/mermaid/反证）。
func TestPlanSubmitSoftGateAggregated(t *testing.T) {
	dir := t.TempDir()
	// 只有正文，缺需求提炼、mermaid、反证章节。
	body := "# T\n\n## 背景\n\n一些具体内容，没有占位符。\n"
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": "软门禁测试", "plan": body,
	}))
	if !isErr {
		t.Fatalf("缺门禁项应被拦：%q", content)
	}
	for _, want := range []string{"需求提炼", "Mermaid", "反证"} {
		if !strings.Contains(content, want) {
			t.Errorf("应一次列全缺项，缺 %q：%q", want, content)
		}
	}
}

// TestPlanSubmitSoftGateOneShot —— 软门禁 **one-shot**：同 title 重提放行。
//
// 对账 TS 的 `warnedSlugs` 语义（每项按 slug 只拦一次）。
func TestPlanSubmitSoftGateOneShot(t *testing.T) {
	dir := t.TempDir()
	title := "one-shot 测试"
	body := "# T\n\n## 背景\n\n具体内容，无占位符。\n"

	// 第一次：被拦。
	if _, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": title, "plan": body,
	})); !isErr {
		t.Fatal("首次提交应被软门禁拦下")
	}
	// 第二次（同 title）：放行（每项只拦一次）。
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": title, "plan": body,
	}))
	if isErr {
		t.Errorf("同 title 重提应放行，实得：%q", content)
	}
}

// TestPlanSubmitHappyPath —— 齐全的计划提交成功并落盘。
func TestPlanSubmitHappyPath(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"# 完整计划",
		"",
		"## 需求提炼",
		"目标：做 X。非目标：不做 Y。",
		"",
		"## 设计",
		"",
		"```mermaid",
		"flowchart TD",
		"  A --> B",
		"```",
		"",
		"## 反证",
		"关键断言已核实。",
		"",
	}, "\n")

	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "submit", "title": "完整计划", "plan": body,
	}))
	if isErr {
		t.Fatalf("齐全计划应提交成功，实得：%q", content)
	}
	if !strings.Contains(content, "计划已提交") {
		t.Errorf("应报提交成功：%q", content)
	}
	// 落盘核实。
	if _, err := os.Stat(filepath.Join(dir, ".rivet", "plans", "完整计划.md")); err != nil {
		t.Errorf("计划文件未落盘：%v", err)
	}
}

// TestPlanSubmitOptionsValidation —— options 校验（>3 / 重复 / 保留标签）。
func TestPlanSubmitOptionsValidation(t *testing.T) {
	dir := t.TempDir()
	body := "# T\n\n## 需求提炼\n目标。\n\n```mermaid\nflowchart TD\n A-->B\n```\n\n## 反证\n已核。\n"

	cases := []struct {
		name string
		opts []any
		want string
	}{
		{"超 3 个", []any{
			map[string]any{"label": "a", "description": "d"},
			map[string]any{"label": "b", "description": "d"},
			map[string]any{"label": "c", "description": "d"},
			map[string]any{"label": "e", "description": "d"},
		}, "At most 3"},
		{"重复标签", []any{
			map[string]any{"label": "same", "description": "d"},
			map[string]any{"label": "SAME", "description": "d"},
		}, "must be unique"},
		{"保留标签", []any{
			map[string]any{"label": "Approve", "description": "d"},
			map[string]any{"label": "b", "description": "d"},
		}, "reserved"},
	}
	for _, c := range cases {
		content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
			"action": "submit", "title": "opt-" + c.name, "plan": body, "options": c.opts,
		}))
		if !isErr || !strings.Contains(content, c.want) {
			t.Errorf("[%s] 应报 %q，实得：%q (isErr=%v)", c.name, c.want, content, isErr)
		}
	}
}

// ── close ──

// TestPlanClosePathWhitelist —— 路径白名单：只允许 .rivet/plans/ 与
// docs/superpowers/plans/。
func TestPlanClosePathWhitelist(t *testing.T) {
	dir := t.TempDir()
	// 造一个白名单外的 md。
	other := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(other, []byte("# N\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": "notes.md", "tasks": "all",
	}))
	if !isErr || !strings.Contains(content, "仅支持") {
		t.Errorf("白名单外路径应被拒：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanClosePathEscape —— 路径逃逸被拦。
func TestPlanClosePathEscape(t *testing.T) {
	dir := t.TempDir()
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": "../../../etc/passwd", "tasks": "all",
	}))
	if !isErr {
		t.Fatalf("逃逸路径应被拒：%q", content)
	}
	if !strings.Contains(content, "逃逸") && !strings.Contains(content, "仅支持") {
		t.Errorf("应说明拒绝原因：%q", content)
	}
}

// TestPlanClosePreviewDoesNotWrite —— 预览模式**不写盘**。
func TestPlanClosePreviewDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/p.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "# Plan\n\n### Task 1\n- [ ] step\n"
	if err := os.WriteFile(abs, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": rel, "tasks": "all",
	}))
	if isErr {
		t.Fatalf("预览不应报错：%q", content)
	}
	if !strings.Contains(content, "未写入任何文件") {
		t.Errorf("预览应说明未写入：%q", content)
	}
	after, _ := os.ReadFile(abs)
	if string(after) != original {
		t.Errorf("预览模式不应改文件\n前：%q\n后：%q", original, string(after))
	}
}

// TestPlanCloseApplyWrites —— apply=true **真写盘**（勾选 + EXECUTED 标记）。
func TestPlanCloseApplyWrites(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/p.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# Plan\n\n### Task 1\n- [ ] step\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": rel, "tasks": "all", "apply": true,
		"deliveryState": "GREEN", "verifiedCommands": []any{"go test ./..."},
	}))
	if isErr {
		t.Fatalf("apply 不应报错：%q", content)
	}
	if !strings.Contains(content, "计划已关闭") {
		t.Errorf("应报已关闭：%q", content)
	}

	after, _ := os.ReadFile(abs)
	s := string(after)
	if !strings.Contains(s, "- [x] step") {
		t.Errorf("checkbox 应被勾选：%q", s)
	}
	if !strings.Contains(s, "Status: EXECUTED") {
		t.Errorf("应写 EXECUTED 标记：%q", s)
	}
	if !strings.Contains(s, "go test ./...") {
		t.Errorf("应记录验证命令：%q", s)
	}
}

// TestPlanCloseYELLOWNoExecutedMarker —— 非 GREEN 不标 EXECUTED。
//
// 对账 TS：EXECUTED 标记只在 gate-backed GREEN 时写。
func TestPlanCloseYELLOWNoExecutedMarker(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/p.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# Plan\n\n### Task 1\n- [ ] step\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": rel, "tasks": "all", "apply": true,
		"deliveryState": "YELLOW",
	})); isErr {
		t.Fatal("apply 不应报错")
	}
	after, _ := os.ReadFile(abs)
	if strings.Contains(string(after), "Status: EXECUTED") {
		t.Errorf("YELLOW 不应标 EXECUTED：%q", string(after))
	}
}

// TestPlanCloseInvalidDeliveryState —— 非法 deliveryState 被拒。
func TestPlanCloseInvalidDeliveryState(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/p.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# Plan\n\n### Task 1\n- [ ] x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": rel, "tasks": "all", "deliveryState": "PURPLE",
	}))
	if !isErr || !strings.Contains(content, "GREEN") {
		t.Errorf("非法 deliveryState 应被拒：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanCloseNoMatchingTask —— 无匹配任务块报错。
func TestPlanCloseNoMatchingTask(t *testing.T) {
	dir := t.TempDir()
	rel := ".rivet/plans/p.md"
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("# Plan\n\n### Task 1\n- [ ] x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": rel, "tasks": "99",
	}))
	if !isErr || !strings.Contains(content, "No matching task") {
		t.Errorf("无匹配任务应报错：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanCloseMissingFile —— 文件不存在报错。
func TestPlanCloseMissingFile(t *testing.T) {
	dir := t.TempDir()
	content, isErr := planToolIsError(t, newPlanParams(dir, map[string]any{
		"action": "close", "file_path": ".rivet/plans/nope.md", "tasks": "all",
	}))
	if !isErr || !strings.Contains(content, "未找到") {
		t.Errorf("文件不存在应报错：%q (isErr=%v)", content, isErr)
	}
}

// TestPlanRegisteredInDefaultRegistry —— 工具已注册（接线验证）。
func TestPlanRegisteredInDefaultRegistry(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	if _, ok := reg.Get("plan"); !ok {
		t.Fatal("plan 工具未注册进默认注册表")
	}
}

// TestPlanRequiresApprovalFalse —— 对账 TS：close 只动计划 markdown，跳过审批。
func TestPlanRequiresApprovalFalse(t *testing.T) {
	tool := Plan()
	if tool.RequiresApproval(&CallParams{}) {
		t.Error("plan 工具不应要求审批（对账 TS requiresApproval() = false）")
	}
}

// TestPlanSchemaHasAllActions —— schema 声明四个 action。
func TestPlanSchemaHasAllActions(t *testing.T) {
	def := Plan().Definition()
	if def.Name != "plan" {
		t.Errorf("工具名应为 plan，实得 %q", def.Name)
	}
	s := def.Description
	for _, a := range []string{"submit", "close", "enter_mode", "exit_mode"} {
		if !strings.Contains(s, a) {
			t.Errorf("描述应含 action %q", a)
		}
	}
}
