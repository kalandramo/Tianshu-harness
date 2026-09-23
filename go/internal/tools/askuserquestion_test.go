package tools

import (
	"context"
	"strings"
	"testing"
)

// newAskParams 构造 ask_user_question 的调用参数。
func newAskParams(input map[string]any) *CallParams {
	return &CallParams{Input: input, Cwd: "/tmp", ToolUseID: "test-id"}
}

// ── 行为对账：TS `__tests__/ask-user-question.test.ts` 的 10 个用例逐条 ──

// 用例 1：向模型返回占位符、向 UI 返回问题文本。
func TestAskReturnsPlaceholderAndUIContent(t *testing.T) {
	tool := AskUserQuestion()
	res, err := tool.Execute(context.Background(), newAskParams(map[string]any{
		"question": "Which approach?",
	}))
	if err != nil {
		t.Fatalf("Execute 失败：%v", err)
	}
	if res.Content != "[等待你的回复…]" {
		t.Errorf("content = %q, want %q", res.Content, "[等待你的回复…]")
	}
	if res.UIContent != "Which approach?" {
		t.Errorf("uiContent = %q, want %q", res.UIContent, "Which approach?")
	}
}

// 用例 2：有选项时 content 与 uiContent 都含编号列表（模型必须看到与用户
// 相同的编号，否则裸 "1" 回复有歧义）。
func TestAskRendersNumberedOptions(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{
		"question": "Which database?",
		"options":  []any{"Postgres", "SQLite", "MySQL"},
	}))

	if !strings.HasPrefix(res.Content, "[等待你的回复…]") {
		t.Errorf("content 应以占位符开头：%q", res.Content)
	}
	for _, want := range []string{"1. Postgres", "2. SQLite", "裸数字"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content 应含 %q：%q", want, res.Content)
		}
	}
	for _, want := range []string{"Which database?", "1. Postgres", "2. SQLite", "3. MySQL"} {
		if !strings.Contains(res.UIContent, want) {
			t.Errorf("uiContent 应含 %q：%q", want, res.UIContent)
		}
	}
	if strings.Contains(res.UIContent, "pick more than one") {
		t.Errorf("单选不应有多选提示：%q", res.UIContent)
	}
}

// 用例 3：allow_multiple 为真时追加多选提示。
func TestAskMultiSelectHint(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{
		"question":       "Which features?",
		"options":        []any{"Auth", "Billing"},
		"allow_multiple": true,
	}))
	if !strings.Contains(res.UIContent, "pick more than one") {
		t.Errorf("应有 m选提示：%q", res.UIContent)
	}
}

// 用例 4：非字符串与空选项被忽略。
func TestAskIgnoresInvalidOptions(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{
		"question": "Pick one",
		"options":  []any{"Valid", "", "   ", 42, nil},
	}))
	if !strings.Contains(res.UIContent, "1. Valid") {
		t.Errorf("应保留 Valid：%q", res.UIContent)
	}
	if strings.Contains(res.UIContent, "2.") {
		t.Errorf("无效项不应产生编号：%q", res.UIContent)
	}
}

// 用例 5：options 为空时回退纯问题文本。
func TestAskFallsBackToPlainQuestion(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{
		"question": "Open ended?",
		"options":  []any{},
	}))
	if res.UIContent != "Open ended?" {
		t.Errorf("uiContent = %q, want %q", res.UIContent, "Open ended?")
	}
}

// 用例 6：question 与 questions 都缺时**报错**。
func TestAskErrorsWithoutQuestion(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{}))
	if !res.IsError {
		t.Error("应报错")
	}
	if !strings.Contains(res.Content, "question") {
		t.Errorf("错误文案应含 question：%q", res.Content)
	}
}

// 用例 7：多问题形式带每题编号，且 endTurn 为真。
func TestAskMultiQuestionForm(t *testing.T) {
	tool := AskUserQuestion()
	res, _ := tool.Execute(context.Background(), newAskParams(map[string]any{
		"questions": []any{
			map[string]any{"prompt": "Enter plan mode?", "options": []any{"Yes", "No"}},
			map[string]any{"prompt": "Which scope?", "options": []any{"Frontend", "Backend"}, "allow_multiple": true},
		},
	}))
	if !strings.HasPrefix(res.Content, "[等待你的回复…]") {
		t.Errorf("content 应以占位符开头：%q", res.Content)
	}
	if !strings.Contains(res.Content, "1. Yes") {
		t.Errorf("content 应含编号选项：%q", res.Content)
	}
	if !res.EndTurn {
		t.Error("endTurn 应为 true")
	}
	for _, want := range []string{"1. Enter plan mode?", "2. Which scope?", "pick more than one"} {
		if !strings.Contains(res.UIContent, want) {
			t.Errorf("uiContent 应含 %q：%q", want, res.UIContent)
		}
	}
}

// 用例 8：单选选项时派发回调，载荷逐字段对账。
func TestAskCallbackSingleSelect(t *testing.T) {
	var got *AskUserQuestionInfo
	p := newAskParams(map[string]any{
		"question": "Which provider?",
		"options":  []any{"OpenAI", "Anthropic"},
	})
	p.OnAskUserQuestion = func(info AskUserQuestionInfo) { got = &info }

	tool := AskUserQuestion()
	_, _ = tool.Execute(context.Background(), p)

	if got == nil {
		t.Fatal("回调未被调用")
	}
	if len(got.Questions) != 1 {
		t.Fatalf("问题数 = %d, want 1", len(got.Questions))
	}
	q := got.Questions[0]
	if q.Prompt != "Which provider?" {
		t.Errorf("prompt = %q", q.Prompt)
	}
	if len(q.Options) != 2 || q.Options[0] != "OpenAI" || q.Options[1] != "Anthropic" {
		t.Errorf("options = %v", q.Options)
	}
	if q.AllowMultiple {
		t.Error("allowMultiple 应为 false")
	}
}

// 用例 9：多选选项也派发回调（TUI 据此渲染选择器）。
func TestAskCallbackMultiSelect(t *testing.T) {
	called := false
	p := newAskParams(map[string]any{
		"question":       "Which features?",
		"options":        []any{"Auth", "Billing"},
		"allow_multiple": true,
	})
	p.OnAskUserQuestion = func(AskUserQuestionInfo) { called = true }

	tool := AskUserQuestion()
	_, _ = tool.Execute(context.Background(), p)
	if !called {
		t.Error("回调未被调用")
	}
}

// 用例 10：开放式问题（无选项）**不**派发回调。
func TestAskNoCallbackWithoutOptions(t *testing.T) {
	called := false
	p := newAskParams(map[string]any{"question": "Open ended?"})
	p.OnAskUserQuestion = func(AskUserQuestionInfo) { called = true }

	tool := AskUserQuestion()
	_, _ = tool.Execute(context.Background(), p)
	if called {
		t.Error("无选项时不应派发回调")
	}
}

// ── parseAskUserQuestions 对账 ──

func TestParseLegacySingleQuestion(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"question": "Which DB?", "options": []any{"A", "B"}, "allow_multiple": true,
	})
	if len(items) != 1 {
		t.Fatalf("问题数 = %d, want 1", len(items))
	}
	if items[0].ID != "q1" {
		t.Errorf("id = %q, want q1", items[0].ID)
	}
	if items[0].Prompt != "Which DB?" {
		t.Errorf("prompt = %q", items[0].Prompt)
	}
	if len(items[0].Options) != 2 {
		t.Errorf("options = %v", items[0].Options)
	}
	if !items[0].AllowMultiple {
		t.Error("allowMultiple 应为 true")
	}
}

func TestParseMultiQuestionAutoIDs(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{
			map[string]any{"prompt": "First?", "options": []any{"X"}},
			map[string]any{"id": "custom", "prompt": "Second?"},
		},
	})
	if len(items) != 2 {
		t.Fatalf("问题数 = %d, want 2", len(items))
	}
	if items[0].ID != "q1" {
		t.Errorf("items[0].id = %q, want q1", items[0].ID)
	}
	if items[1].ID != "custom" {
		t.Errorf("items[1].id = %q, want custom", items[1].ID)
	}
	if len(items[1].Options) != 0 {
		t.Errorf("items[1].options = %v, want 空", items[1].Options)
	}
}

// questions[] 优先于单问题字段。
func TestParseQuestionsTakesPrecedence(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"question":  "legacy",
		"questions": []any{map[string]any{"prompt": "structured"}},
	})
	if len(items) != 1 {
		t.Fatalf("问题数 = %d, want 1", len(items))
	}
	if items[0].Prompt != "structured" {
		t.Errorf("prompt = %q, want structured", items[0].Prompt)
	}
}

// 畸形项被跳过；全无效时返回空切片。
func TestParseSkipsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"null/无prompt/标量", map[string]any{"questions": []any{
			nil, map[string]any{"options": []any{"a"}}, 42,
		}}},
		{"空输入", map[string]any{}},
		{"空白 question", map[string]any{"question": "   "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := parseAskUserQuestions(c.input)
			if len(items) != 0 {
				t.Errorf("应返回空切片，得到 %+v", items)
			}
		})
	}
}

// ── LLM schema 兼容（TS 记录的三种非标准形态）──

// Anthropic 原生 schema：options 是 [{label, description}]。
func TestParseAnthropicObjectOptions(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{
			"prompt": "选一个",
			"options": []any{
				map[string]any{"label": "A", "description": "aaa"},
				map[string]any{"label": "B", "description": "bbb"},
				map[string]any{"label": "C", "description": "ccc"},
			},
		}},
	})
	if len(items) != 1 {
		t.Fatalf("问题数 = %d", len(items))
	}
	want := []string{"A", "B", "C"}
	if len(items[0].Options) != 3 {
		t.Fatalf("options = %v", items[0].Options)
	}
	for i, w := range want {
		if items[0].Options[i] != w {
			t.Errorf("options[%d] = %q, want %q", i, items[0].Options[i], w)
		}
	}
}

// 只取 label；label 前后空白清掉（与 string 路径一致）。
func TestParseObjectOptionsTrimLabel(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{
			"prompt": "选",
			"options": []any{
				map[string]any{"label": "  苹果  ", "description": "apple"},
				map[string]any{"label": "香蕉", "preview": "yellow"},
			},
		}},
	})
	if len(items[0].Options) != 2 || items[0].Options[0] != "苹果" || items[0].Options[1] != "香蕉" {
		t.Errorf("options = %v, want [苹果 香蕉]", items[0].Options)
	}
}

// 无 label 的对象跳过；纯字符串保留；null 跳过。
func TestParseObjectOptionsMixed(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{
			"prompt": "选",
			"options": []any{
				map[string]any{"description": "no label"},
				map[string]any{"label": "B"},
				nil,
				"plain-string",
			},
		}},
	})
	want := []string{"B", "plain-string"}
	if len(items[0].Options) != len(want) {
		t.Fatalf("options = %v, want %v", items[0].Options, want)
	}
	for i, w := range want {
		if items[0].Options[i] != w {
			t.Errorf("options[%d] = %q, want %q", i, items[0].Options[i], w)
		}
	}
}

// choices 别名等价于 options。
func TestParseChoicesAlias(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{"prompt": "选", "choices": []any{"X", "Y"}}},
	})
	if len(items[0].Options) != 2 || items[0].Options[0] != "X" || items[0].Options[1] != "Y" {
		t.Errorf("options = %v, want [X Y]", items[0].Options)
	}
}

// multiSelect (camelCase) 等价于 allow_multiple (snake_case)。
func TestParseMultiSelectAlias(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{
			"prompt": "多选", "options": []any{"A", "B"}, "multiSelect": true,
		}},
	})
	if !items[0].AllowMultiple {
		t.Error("multiSelect 应等价于 allow_multiple")
	}
}

// 混合：对象数组 + multiSelect 同时存在。
func TestParseMixedSchema(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"questions": []any{map[string]any{
			"prompt": "多选",
			"options": []any{
				map[string]any{"label": "A"},
				map[string]any{"label": "B"},
				map[string]any{"label": "C"},
			},
			"multiSelect": true,
		}},
	})
	if len(items[0].Options) != 3 {
		t.Errorf("options = %v", items[0].Options)
	}
	if !items[0].AllowMultiple {
		t.Error("allowMultiple 应为 true")
	}
}

// **反证**：allow_multiple 用非布尔真值（字符串 "true" / 数字 1）时**不**生效。
//
// 对账 TS 的严格 `=== true`。若 Go 侧误用 truthy 判定，此测试转红。
//
// **两条路径都要覆盖**：单问题与多问题形式各有一套独立的 allowMultiple
// 判定（TS 亦然），只测一条会让另一条的变异逃逸（M4 变异实测发现）。
func TestParseAllowMultipleStrictBool(t *testing.T) {
	nonBool := []any{"true", 1, "yes"}

	t.Run("单问题形式", func(t *testing.T) {
		for _, v := range nonBool {
			items := parseAskUserQuestions(map[string]any{
				"question": "q", "options": []any{"A"}, "allow_multiple": v,
			})
			if len(items) != 1 {
				t.Fatalf("问题数 = %d", len(items))
			}
			if items[0].AllowMultiple {
				t.Errorf("allow_multiple=%v（非布尔 true）不应生效", v)
			}
		}
	})

	t.Run("多问题形式", func(t *testing.T) {
		for _, v := range nonBool {
			items := parseAskUserQuestions(map[string]any{
				"questions": []any{map[string]any{
					"prompt": "q", "options": []any{"A"}, "allow_multiple": v,
				}},
			})
			if len(items) != 1 {
				t.Fatalf("问题数 = %d", len(items))
			}
			if items[0].AllowMultiple {
				t.Errorf("questions[].allow_multiple=%v（非布尔 true）不应生效", v)
			}
		}
	})

	t.Run("多问题形式 multiSelect 别名", func(t *testing.T) {
		for _, v := range nonBool {
			items := parseAskUserQuestions(map[string]any{
				"questions": []any{map[string]any{
					"prompt": "q", "options": []any{"A"}, "multiSelect": v,
				}},
			})
			if len(items) != 1 {
				t.Fatalf("问题数 = %d", len(items))
			}
			if items[0].AllowMultiple {
				t.Errorf("questions[].multiSelect=%v（非布尔 true）不应生效", v)
			}
		}
	})
}

// **反证**：空白规整必须用 JS 语义（`jsTrimSpace`），不是 `strings.TrimSpace`。
//
// U+FEFF（零宽不换行空格）在 JS 的 `String.prototype.trim()` 里算空白，
// Go 的 `strings.TrimSpace` **不算**。若误用后者，带 BOM 的输入（模型从
// 文件里复制文本时常带）会留下不可见前缀，导致 prompt 非空判定与渲染
// 都和 TS 不一致。
func TestParseUsesJsTrimSemantics(t *testing.T) {
	// U+FEFF 包裹的问题文本——JS 侧 trim 后应为裸 "Q"。
	items := parseAskUserQuestions(map[string]any{
		"question": "\uFEFF Q \uFEFF",
		"options":  []any{"\uFEFF A \uFEFF"},
	})
	if len(items) != 1 {
		t.Fatalf("问题数 = %d, want 1", len(items))
	}
	if items[0].Prompt != "Q" {
		t.Errorf("prompt = %q, want %q（U+FEFF 未被 JS 语义 trim）", items[0].Prompt, "Q")
	}
	if len(items[0].Options) != 1 || items[0].Options[0] != "A" {
		t.Errorf("options = %q, want [A]（U+FEFF 未被 JS 语义 trim）", items[0].Options)
	}
}

// 纯 U+FEFF 的 question 应被判为**空**（JS 语义下 trim 后为空）→ 报错。
func TestParseFeffOnlyIsEmpty(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{"question": "\uFEFF\uFEFF"})
	if len(items) != 0 {
		t.Errorf("纯 U+FEFF 应视为空，得到 %+v", items)
	}
}

// ── renderAskUserQuestionText 对账 ──

func TestRenderLegacySingleQuestion(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{
		"question": "Pick", "options": []any{"A", "B"},
	})
	text := renderAskUserQuestionText(items)
	if !strings.HasPrefix(text, "Pick") {
		t.Errorf("应以 Pick 开头：%q", text)
	}
	// 选项缩进两个空格（对账 TS 的 `  ${i+1}. ${opt}`）。
	if !strings.Contains(text, "  1. A") {
		t.Errorf("应含 '  1. A'：%q", text)
	}
	if !strings.Contains(text, "  2. B") {
		t.Errorf("应含 '  2. B'：%q", text)
	}
}

// 单问题无选项时渲染就是问题文本本身（无缩进、无提示）。
func TestRenderOpenEnded(t *testing.T) {
	items := parseAskUserQuestions(map[string]any{"question": "Open?"})
	text := renderAskUserQuestionText(items)
	if text != "Open?" {
		t.Errorf("text = %q, want %q", text, "Open?")
	}
}

// ── 工具元数据 ──

func TestAskToolMetadata(t *testing.T) {
	tool := AskUserQuestion()
	if tool.RequiresApproval(nil) {
		t.Error("不需要批准")
	}
	if !tool.ConcurrencySafe() {
		t.Error("应可并发")
	}
	if !tool.Enabled() {
		t.Error("应启用")
	}
	if tool.Timeout(nil) != 0 {
		t.Error("无超时")
	}
	if tool.Definition().Name != "ask_user_question" {
		t.Errorf("名 = %q", tool.Definition().Name)
	}
}
