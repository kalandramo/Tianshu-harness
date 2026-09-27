package tools

import (
	"context"
	"strings"
	"testing"
)

// ── 假 navigator ─────────────────────────────────────────────────

// fakeNav 是一个可编程的 LspNavigator。
type fakeNav struct {
	ready       bool
	supportsDef bool
	supportsRef bool

	gotoResult []LspLocation
	refsResult []LspLocation

	gotFilePath string
	gotLine     int
	gotColumn   int
	gotoCalls   int
	refsCalls   int
}

func (f *fakeNav) IsReady() bool            { return f.ready }
func (f *fakeNav) SupportsDefinition() bool { return f.supportsDef }
func (f *fakeNav) SupportsReferences() bool { return f.supportsRef }

func (f *fakeNav) GotoDefinition(filePath string, line, column int) ([]LspLocation, error) {
	f.gotoCalls++
	f.gotFilePath, f.gotLine, f.gotColumn = filePath, line, column
	return f.gotoResult, nil
}

func (f *fakeNav) FindReferences(filePath string, line, column int) ([]LspLocation, error) {
	f.refsCalls++
	f.gotFilePath, f.gotLine, f.gotColumn = filePath, line, column
	return f.refsResult, nil
}

func loc(uri string, line, char int) LspLocation {
	return LspLocation{
		URI: uri,
		Range: LspRange{
			Start: LspPosition{Line: line, Character: char},
			End:   LspPosition{Line: line, Character: char},
		},
	}
}

// ── definition 逐字对账（前缀缓存字节稳定）─────────────────────────

// TestLspToolDefinitions_NamesAndDescriptionsExact —— ★ 两个工具的 name /
// description **逐字**对账 TS。
//
// 理由：definitions 进请求体，任何字符差异都会让**整个前缀缓存失效**
// （system + tools 段）。
func TestLspToolDefinitions_NamesAndDescriptionsExact(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, supportsRef: true}

	gotoDef := GotoDefinition(nav).Definition()
	if gotoDef.Name != "lsp_goto_definition" {
		t.Errorf("name 不符：%q", gotoDef.Name)
	}
	if gotoDef.Description != "跳转到给定文件位置符号的定义。返回定义的文件路径、行号和列号。用于理解函数、类、变量或类型的定义位置。" {
		t.Errorf("description 逐字不符：\n%q", gotoDef.Description)
	}

	refsDef := FindReferences(nav).Definition()
	if refsDef.Name != "lsp_find_references" {
		t.Errorf("name 不符：%q", refsDef.Name)
	}
	if refsDef.Description != "查找给定文件位置符号的所有引用。返回符号被使用的文件路径、行号和列号列表。用于理解修改函数、类或变量的影响范围。" {
		t.Errorf("description 逐字不符：\n%q", refsDef.Description)
	}
}

// TestLspToolDefinitions_SchemaShape —— schema 的 propOrder / required /
// 属性描述逐字对齐。
//
// 对账 TS：
//
//	input_schema: { type:'object', properties: { file_path, line, column }, required: ['file_path','line','column'] }
//	  file_path: '包含该符号的源文件路径'
//	  line:      '符号所在行号（从 1 开始）'
//	  column:    '符号所在列号（从 0 开始）'
func TestLspToolDefinitions_SchemaShape(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, supportsRef: true}
	for _, name := range []string{"lsp_goto_definition", "lsp_find_references"} {
		var d = GotoDefinition(nav).Definition()
		if name == "lsp_find_references" {
			d = FindReferences(nav).Definition()
		}
		if d.InputSchema == nil {
			t.Fatalf("%s 缺 InputSchema", name)
		}
		if d.InputSchema.Type != "object" {
			t.Errorf("%s 的 type 应为 object", name)
		}
		// ★ PropOrder 必须显式给出且序为 file_path → line → column
		want := []string{"file_path", "line", "column"}
		if strings.Join(d.InputSchema.PropOrder, ",") != strings.Join(want, ",") {
			t.Errorf("%s 的 PropOrder 不符\n want %v\n  got %v", name, want, d.InputSchema.PropOrder)
		}
		if strings.Join(d.InputSchema.Required, ",") != strings.Join(want, ",") {
			t.Errorf("%s 的 Required 不符\n want %v\n  got %v", name, want, d.InputSchema.Required)
		}
		if len(d.InputSchema.Properties) != 3 {
			t.Errorf("%s 应有 3 个属性，实得 %d", name, len(d.InputSchema.Properties))
		}
	}
}

// ── 入参校验（三处文案逐字）──────────────────────────────────────

// TestLspResolveParams_MissingFilePath —— 缺 file_path 的文案逐字。
func TestLspResolveParams_MissingFilePath(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true}
	res, _ := GotoDefinition(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"line": 1, "column": 0},
	})
	if !res.IsError {
		t.Fatal("缺 file_path 应报错")
	}
	if res.Content != "Missing required parameter: file_path" {
		t.Errorf("文案逐字不符：%q", res.Content)
	}
}

// TestLspResolveParams_LineMustBeAtLeastOne —— line < 1 的文案逐字。
//
// 判别力：`line: 0` 若不判（当作合法），会静默发出 line=-1 的请求。
func TestLspResolveParams_LineMustBeAtLeastOne(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true}
	for _, bad := range []any{0, -1, "1", nil} {
		res, _ := GotoDefinition(nav).Execute(context.Background(), &CallParams{
			Input: map[string]any{"file_path": "a.go", "line": bad, "column": 0},
		})
		if !res.IsError {
			t.Errorf("line=%v 应报错", bad)
			continue
		}
		want := "Missing or invalid parameter: line (must be >= 1)"
		if res.Content != want {
			t.Errorf("line=%v 的文案逐字不符\n want %q\n  got %q", bad, want, res.Content)
		}
	}
	if nav.gotoCalls != 0 {
		t.Errorf("校验失败的调用不该触达 navigator，实得 %d 次", nav.gotoCalls)
	}
}

// TestLspResolveParams_ColumnMustBeNonNegative —— column < 0 的文案逐字。
//
// 注意与 line 的差异：column 允许 0（LSP 0-based），line 不允许 0（1-based）。
func TestLspResolveParams_ColumnMustBeNonNegative(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true}
	for _, bad := range []any{-1, "0", nil} {
		res, _ := GotoDefinition(nav).Execute(context.Background(), &CallParams{
			Input: map[string]any{"file_path": "a.go", "line": 1, "column": bad},
		})
		if !res.IsError {
			t.Errorf("column=%v 应报错", bad)
			continue
		}
		want := "Missing or invalid parameter: column (must be >= 0)"
		if res.Content != want {
			t.Errorf("column=%v 的文案逐字不符\n want %q\n  got %q", bad, want, res.Content)
		}
	}

	// column: 0 合法
	nav2 := &fakeNav{ready: true, supportsDef: true}
	res, _ := GotoDefinition(nav2).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "a.go", "line": 1, "column": 0},
	})
	if res.IsError {
		t.Errorf("column=0 应合法，实得错误：%q", res.Content)
	}
}

// ── 输出格式 ─────────────────────────────────────────────────────

// TestLspGoto_ZeroResultUsesInputValues —— ★ 零结果文案用**入参原值**。
//
// TS：`No definition found for symbol at ${filePath}:${line}:${column}`
// ——line/column 是**入参**（1-based line），不是转换后的值。
//
// 判别力：若实现里用了 `line-1` 渲染 → line=5 会显示 `:4:` → 必红。
func TestLspGoto_ZeroResultUsesInputValues(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, gotoResult: nil}
	res, _ := GotoDefinition(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "src/a.go", "line": 5, "column": 7},
	})
	if res.IsError {
		t.Fatalf("零结果不该是 error：%q", res.Content)
	}
	want := "No definition found for symbol at src/a.go:5:7"
	if res.Content != want {
		t.Errorf("文案不符（应用入参原值）\n want %q\n  got %q", want, res.Content)
	}
}

// TestLspRefs_ZeroResultTextUsesNoun 差异化 —— find_references 的零结果用
// `No references found`（不是 definition）。两者措辞不同。
func TestLspRefs_ZeroResultTextUsesNoun(t *testing.T) {
	nav := &fakeNav{ready: true, supportsRef: true}
	res, _ := FindReferences(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "src/a.go", "line": 2, "column": 0},
	})
	want := "No references found for symbol at src/a.go:2:0"
	if res.Content != want {
		t.Errorf("文案不符\n want %q\n  got %q", want, res.Content)
	}
}

// TestLspGoto_FormatsWithLinePlusOne —— ★ 结果行号 **+1**（0-based → 1-based），
// 列号**不加**。
//
// TS：`${loc.uri}:${loc.range.start.line + 1}:${loc.range.start.character}`
//
// 判别力：若行号不加 1 → 显示比真实小 1（用户按行号跳转会偏一行）。
func TestLspGoto_FormatsWithLinePlusOne(t *testing.T) {
	nav := &fakeNav{
		ready: true, supportsDef: true,
		gotoResult: []LspLocation{loc("src/b.go", 9, 4), loc("src/c.go", 0, 0)},
	}
	res, _ := GotoDefinition(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "src/a.go", "line": 1, "column": 0},
	})
	want := "2 definition(s) found:\nsrc/b.go:10:4\nsrc/c.go:1:0"
	if res.Content != want {
		t.Errorf("格式化不符\n want %q\n  got %q", want, res.Content)
	}
}

// TestLspRefs_FormatsWithSingularNounAndCount —— `(s)` 是**字面量**（不复数变形），
// 且计数前置。
func TestLspRefs_FormatsWithSingularNounAndCount(t *testing.T) {
	nav := &fakeNav{
		ready: true, supportsRef: true,
		refsResult: []LspLocation{loc("x.go", 0, 0)},
	}
	res, _ := FindReferences(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "a.go", "line": 1, "column": 0},
	})
	// 1 个结果也用 "reference(s)"（字面 (s)，不写 "reference"）
	want := "1 reference(s) found:\nx.go:1:0"
	if res.Content != want {
		t.Errorf("格式化不符\n want %q\n  got %q", want, res.Content)
	}
}

// TestLspGoto_PassesParamsThroughUnchanged —— 入参原样传给 navigator
// （工具层**不做**行号转换——转换在 manager 层）。
//
// 判别力：若工具层误做 `line-1`，manager 再做一次 → 偏两行。
func TestLspGoto_PassesParamsThroughUnchanged(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true}
	_, _ = GotoDefinition(nav).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "src/a.go", "line": 42, "column": 3},
	})
	if nav.gotLine != 42 {
		t.Errorf("工具层不该转换行号（应原样传 42），实得 %d", nav.gotLine)
	}
	if nav.gotColumn != 3 {
		t.Errorf("列号应原样传 3，实得 %d", nav.gotColumn)
	}
	if nav.gotFilePath != "src/a.go" {
		t.Errorf("路径应原样传，实得 %q", nav.gotFilePath)
	}
}

// ── Enabled 门控（决定工具是否出现在模型可见列表）─────────────────

// TestLspTools_EnabledGating —— ★ `Enabled()` 是「不做也不会坏」的关键：
// server 不可用时工具**不出现在**模型可见列表（行为等价于工具不存在）。
//
// 对账 TS：`isEnabled(): manager.isReady() && manager.supportsDefinition()`。
func TestLspTools_EnabledGating(t *testing.T) {
	cases := []struct {
		name        string
		ready       bool
		supportsDef bool
		supportsRef bool
		wantGoto    bool
		wantRefs    bool
	}{
		{"全不可用", false, false, false, false, false},
		{"ready 但无 capability", true, false, false, false, false},
		{"只支持定义", true, true, false, true, false},
		{"只支持引用", true, false, true, false, true},
		{"全支持", true, true, true, true, true},
	}

	for _, c := range cases {
		nav := &fakeNav{ready: c.ready, supportsDef: c.supportsDef, supportsRef: c.supportsRef}
		if got := GotoDefinition(nav).Enabled(); got != c.wantGoto {
			t.Errorf("%s：goto Enabled 应 %v，实得 %v", c.name, c.wantGoto, got)
		}
		if got := FindReferences(nav).Enabled(); got != c.wantRefs {
			t.Errorf("%s：refs Enabled 应 %v，实得 %v", c.name, c.wantRefs, got)
		}
	}
}

// TestLspTools_NilNavigatorIsDisabled —— nil navigator 时**不可用**且不 panic。
//
// **为什么这条重要**：装配层若忘了注入（或 LSP 初始化失败置 nil），
// 工具必须静默消失而非崩掉整个会话。
func TestLspTools_NilNavigatorIsDisabled(t *testing.T) {
	if GotoDefinition(nil).Enabled() {
		t.Error("nil navigator 应不可用")
	}
	if FindReferences(nil).Enabled() {
		t.Error("nil navigator 应不可用")
	}
	// 直调（绕过 Enabled 短路）应给明确说明，而非 panic
	res, err := GotoDefinition(nil).Execute(context.Background(), &CallParams{
		Input: map[string]any{"file_path": "a.go", "line": 1, "column": 0},
	})
	if err != nil {
		t.Fatalf("不该返回 error：%v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "不可用") {
		t.Errorf("应给明确不可用说明，实得 %q", res.Content)
	}
}

// TestLspTools_ReadOnlyMetadata —— 只读导航：不审批、可并发、无工具级超时。
//
// 对账 TS：`requiresApproval(): false` / `isConcurrencySafe(): true`。
//
// **注意 RequiresApproval=false 的含义**：它进 `approval_pathgrant.go` 的
// 判定面——只读工具不该弹审批（否则每次跳定义都要用户点头）。
func TestLspTools_ReadOnlyMetadata(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, supportsRef: true}
	for _, tl := range []Tool{GotoDefinition(nav), FindReferences(nav)} {
		if tl.RequiresApproval(&CallParams{}) {
			t.Errorf("%s 不该需要审批（只读导航）", tl.Definition().Name)
		}
		if !tl.ConcurrencySafe() {
			t.Errorf("%s 应可并发（只读）", tl.Definition().Name)
		}
		if d := tl.Timeout(&CallParams{}); d != 0 {
			t.Errorf("%s 应用默认超时（0），实得 %v", tl.Definition().Name, d)
		}
	}
}

// ── 端到端：走生产注册表 + 门链生效回归 ──────────────────────────

// TestLspTools_ViaProductionRegistry —— ★ 端到端：注册后能被注册表执行，
// 且 `Definitions()` 含它们（= 真的进了模型可见列表）。
func TestLspTools_ViaProductionRegistry(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, supportsRef: true,
		gotoResult: []LspLocation{loc("main.go", 3, 1)}}

	r := NewRegistry()
	r.Register(GotoDefinition(nav))
	r.Register(FindReferences(nav))

	// ① 进 Definitions（可见）
	names := map[string]bool{}
	for _, d := range r.Definitions() {
		names[d.Name] = true
	}
	if !names["lsp_goto_definition"] || !names["lsp_find_references"] {
		t.Fatalf("两工具应在 Definitions 里，实得 %v", names)
	}

	// ② 可执行
	res, err := r.Execute(context.Background(), "lsp_goto_definition", &CallParams{
		Cwd:   t.TempDir(),
		Input: map[string]any{"file_path": "main.go", "line": 4, "column": 1},
	})
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if res.IsError {
		t.Fatalf("不该报错：%q", res.Content)
	}
	if !strings.Contains(res.Content, "main.go:4:1") {
		t.Errorf("输出应含 main.go:4:1（行 +1 后的展示值），实得 %q", res.Content)
	}
}

// TestLspTools_NotInDefinitionsWhenDisabled —— server 不可用时**不进**
// Definitions（模型看不见）。
//
// 这是「先移植不会坏」的机制保证。
func TestLspTools_NotInDefinitionsWhenDisabled(t *testing.T) {
	nav := &fakeNav{ready: false} // 全不可用
	r := NewRegistry()
	r.Register(GotoDefinition(nav))
	r.Register(FindReferences(nav))

	for _, d := range r.Definitions() {
		if strings.HasPrefix(d.Name, "lsp_") {
			t.Errorf("不可用时不该出现 %s", d.Name)
		}
	}
}

// TestLspTools_RegisteredInDefaultRegistry —— ★ 生产注册表（`NewDefaultRegistry`）
// 能拿到它们（装配缺口会导致「写了但没接上」）。
//
// **本用例是「休眠接线被激活」的验收**：`probe_discipline.go:84,85,97,98`
// 与 `advisory_readback.go:170` 早已按名引用这两个工具，此前永不匹配。
func TestLspTools_RegisteredInDefaultRegistry(t *testing.T) {
	nav := &fakeNav{ready: true, supportsDef: true, supportsRef: true}
	cwd := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: cwd, LspNavigator: nav})

	found := map[string]bool{}
	for _, d := range r.Definitions() {
		found[d.Name] = true
	}
	if !found["lsp_goto_definition"] {
		t.Error("默认注册表应含 lsp_goto_definition")
	}
	if !found["lsp_find_references"] {
		t.Error("默认注册表应含 lsp_find_references")
	}
}

// TestLspTools_DefaultRegistryWithoutNavigator —— 未注入 navigator 时
// 注册表仍可用（工具不出现），**不 panic**。
func TestLspTools_DefaultRegistryWithoutNavigator(t *testing.T) {
	cwd := t.TempDir()
	r := NewDefaultRegistry(Options{Cwd: cwd})

	for _, d := range r.Definitions() {
		if strings.HasPrefix(d.Name, "lsp_") {
			t.Errorf("未注入 navigator 时不该出现 %s（否则模型会看到调不通的工具）", d.Name)
		}
	}
	// 既有工具不受影响
	if len(r.Definitions()) == 0 {
		t.Error("其他工具应正常注册")
	}
}
