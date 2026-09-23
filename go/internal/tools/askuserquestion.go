// ask_user_question 工具：向用户提问并结束当前回合。
//
// 对账 TS 的 `ASK_USER_QUESTION_TOOL`（src/tools/ask-user-question.ts:169）。
//
// ## 回合语义（本工具存在的理由）
//
// 工具**不阻塞等待输入**——它返回占位符并置 `EndTurn: true`，回合循环据此
// 把本回合收为 final 并退出；用户的下一条消息就是答案。这与 TS 一致
// （turn-orchestrator.ts:1033 的 `if (r.endTurn)` → `completeTurn({isFinal:true})`
// → `break`）。
//
// ## scope 收窄（明示）
//
// TS 侧同文件还导出 `AskAnswerDraft` / `draftToAnswer` / `composeAnswers`
// ——那是**桌面端 QuestionCard 的答案组装路径**（用户点选 → 组串 → 作为下一条
// user 消息）。Go 侧无 TUI / 桌面端（`go/internal/tui` 不存在），该路径**无
// 消费方**，故不移植。未来移植 UI 时需一并补上（TS 对账基准见
// `__tests__/ask-compose-answers.test.ts`）。
//
// 同理 `uiContent` 的渲染（`renderAskUserQuestionText`）在 Go 侧当前只被
// `DisplayContent`（turnbudget.go:137）消费——纯文本 TUI 未移植，故它是
// 「已接线但展示层缺席」的状态，非悬空。
package tools

import (
	"context"
	"strconv"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// AskUserQuestionItem 是一个结构化问题。
//
// 对账 TS 的 `AskUserQuestionItem`（ask-user-question.ts:33）——
// 与 `AskUserQuestionInfo.questions[]` 的元素同形（types.ts:128）。
type AskUserQuestionItem struct {
	// ID 是卡片 UI 的稳定标识（省略时自动分配 q1..qN）。
	ID string
	// Prompt 是问题文本。
	Prompt string
	// Options 是可选选项（已规整为字符串，无效项已剔除）。
	Options []string
	// AllowMultiple 表示允许多选。
	AllowMultiple bool
}

// AskUserQuestionInfo 是派发给 UI 的结构化提问载荷。
//
// 对账 TS 的 `AskUserQuestionInfo`（src/tools/types.ts:128）。
type AskUserQuestionInfo struct {
	Questions []AskUserQuestionItem
}

// parseAskUserQuestions 把原始入参规整为结构化问题列表。
//
// 对账 TS 的 `parseAskUserQuestions`（ask-user-question.ts:44）。
// 同时接受单问题形式（question / options / allow_multiple）与
// 多问题形式（questions: [...]）。无有效问题时返回空切片。
func parseAskUserQuestions(input map[string]any) []AskUserQuestionItem {
	// 多问题形式优先（对账 TS：`Array.isArray(input.questions) && length > 0`）。
	if rawQuestions, ok := input["questions"].([]any); ok && len(rawQuestions) > 0 {
		items := []AskUserQuestionItem{}
		for _, raw := range rawQuestions {
			// 非对象（含 null、标量）跳过。TS 用 `typeof raw !== 'object' || raw === null`
			// ——注意 JS 里数组也是 'object'，但数组没有 prompt 字段，结果同样被跳过。
			q, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			prompt := ""
			// 对账 TS：prompt 非空（trim 后）则用它，否则回退 question 字段。
			if s, ok := q["prompt"].(string); ok {
				if t := jsTrimSpace(s); t != "" {
					prompt = t
				}
			}
			if prompt == "" {
				if s, ok := q["question"].(string); ok {
					prompt = jsTrimSpace(s)
				}
			}
			if prompt == "" {
				continue
			}
			// options 字段名兼容：标准 'options' + 别名 'choices'。
			rawOptions := askRawOptions(q)
			// 多选字段名兼容：snake_case 'allow_multiple' + camelCase 'multiSelect'。
			// **严格 === true**（对账 TS——truthy 值不算）。
			allowMultiple := q["allow_multiple"] == true || q["multiSelect"] == true

			id := ""
			if s, ok := q["id"].(string); ok {
				id = jsTrimSpace(s)
			}
			if id == "" {
				id = "q" + strconv.Itoa(len(items)+1)
			}
			items = append(items, AskUserQuestionItem{
				ID:            id,
				Prompt:        prompt,
				Options:       cleanAskOptions(rawOptions),
				AllowMultiple: allowMultiple,
			})
		}
		return items
	}

	// 单问题形式（legacy）。
	if s, ok := input["question"].(string); ok {
		if prompt := jsTrimSpace(s); prompt != "" {
			return []AskUserQuestionItem{{
				ID:            "q1",
				Prompt:        prompt,
				Options:       cleanAskOptions(askRawOptions(input)),
				AllowMultiple: input["allow_multiple"] == true || input["multiSelect"] == true,
			}}
		}
	}

	return []AskUserQuestionItem{}
}

// askRawOptions 取 options / choices 的原始数组（对账 TS 的字段名兼容链）。
func askRawOptions(m map[string]any) any {
	if v, ok := m["options"].([]any); ok {
		return v
	}
	if v, ok := m["choices"].([]any); ok {
		return v
	}
	return nil
}

// cleanAskOptions 把 LLM 输入规整成 string[]。
//
// 对账 TS 的 `cleanOptions`（ask-user-question.ts:56）。三类形态：
//   - string：trim 后非空保留
//   - { label, description }：只取 label（Anthropic 原生 schema）
//   - null / 无 label 对象：跳过（静默降级，不抛错）
func cleanAskOptions(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, item := range arr {
		if s, ok := item.(string); ok {
			if t := jsTrimSpace(s); t != "" {
				out = append(out, t)
			}
			continue
		}
		if m, ok := item.(map[string]any); ok {
			if label, ok := m["label"].(string); ok {
				if t := jsTrimSpace(label); t != "" {
					out = append(out, t)
				}
			}
			// 缺 label 的对象跳过——保持静默降级（UI 侧有兜底）。
		}
		// null / 非对象非字符串：跳过。
	}
	return out
}

// renderAskUserQuestionText 把问题渲染为纯文本（供 TUI / uiContent 通道）。
//
// 对账 TS 的 `renderAskUserQuestionText`（ask-user-question.ts:103）。
// 多选题在末尾追加 "(You can pick more than one.)" 提示。
func renderAskUserQuestionText(questions []AskUserQuestionItem) string {
	blocks := make([]string, 0, len(questions))
	for qi, q := range questions {
		heading := q.Prompt
		if len(questions) > 1 {
			heading = strconv.Itoa(qi+1) + ". " + q.Prompt
		}
		if len(q.Options) == 0 {
			blocks = append(blocks, heading)
			continue
		}
		numbered := ""
		for i, opt := range q.Options {
			if i > 0 {
				numbered += "\n"
			}
			numbered += "  " + strconv.Itoa(i+1) + ". " + opt
		}
		hint := ""
		if q.AllowMultiple {
			hint = "\n\n(You can pick more than one.)"
		}
		blocks = append(blocks, heading+"\n\n"+numbered+hint)
	}
	out := ""
	for i, b := range blocks {
		if i > 0 {
			out += "\n\n"
		}
		out += b
	}
	return out
}

type askUserQuestionTool struct{}

// AskUserQuestion 创建 ask_user_question 工具。
func AskUserQuestion() Tool { return &askUserQuestionTool{} }

func (t *askUserQuestionTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "ask_user_question",
		Description: "向用户提出一个或多个问题，并等待其输入回答。当你需要澄清信息、了解偏好，或需要一个无法从上下文推断的决定时使用。\n\n" +
			"单个问题：传 `question`（+ 可选 `options`）。多个相关问题（最多 4 个）：传 `questions`——桌面端会把它们渲染成一张结构化卡片，用户逐页作答。\n\n" +
			"为一小组互斥选项提供 `options`（UI 渲染为编号列表，用户可直接按编号回答；桌面端卡片还提供自由输入的 \"Other\" 项）。开放式问题省略 `options`。\n\n" +
			"当用户要的是你的分析、建议或观点时，禁止用这个工具把决定推回给用户——那种情况直接回答。优先只问一个问题；先处理你能确定的部分。",
		InputSchema: objSchemaOrdered([]string{"question", "options", "allow_multiple", "questions"}, map[string]any{
			"question":       strProp("要问用户的问题。清晰、具体。"),
			"options":        arrayPropOrdered("可选的 2-4 个简短互斥选项。开放式问题、或用户要的是你的分析而不是菜单时省略。", "string"),
			"allow_multiple": boolProp("允许选择多个选项（默认：false）。"),
			"questions": arrPropItemsFirst("多问题形式（最多 4 个）。存在时优先于单问题字段。",
				objPropMapOrdered([]string{"id", "prompt", "options", "allow_multiple"}, map[string]any{
					"id":             strProp("该问题的可选稳定 id（省略时自动分配）。"),
					"prompt":         strProp("问题文本。"),
					"options":        arrayPropOrdered("可选的 2-4 个简短互斥选项。", "string"),
					"allow_multiple": boolProp("允许选择多个选项（默认：false）。"),
				}, "prompt")),
		}),
	}
}

func (t *askUserQuestionTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	questions := parseAskUserQuestions(p.Input)
	if len(questions) == 0 {
		return contract.Result{Content: "错误：question（或 questions[]）必填", IsError: true}, nil
	}

	rendered := renderAskUserQuestionText(questions)

	// content 是模型看到的：有选项时**必须**包含与用户所见相同的编号渲染
	// ——否则编号只存在于 uiContent，模型看到裸 "1" 只能猜映射
	// （TS 源码记录的真实事故：session 91840816，用户答 1 = plan mode，
	// 模型读成选项 2 = 直接执行）。
	hasOptions := false
	for _, q := range questions {
		if len(q.Options) > 0 {
			hasOptions = true
			break
		}
	}

	// 把可选问题派发给 TUI，使其能开箭头选择器（含多选 / 多问题形态）。
	// TS 侧此处有两个同义变量（hasOptions / hasSelectable），条件完全相同
	// ——Go 侧合并为一个，行为等价。
	if hasOptions && p.OnAskUserQuestion != nil {
		p.OnAskUserQuestion(AskUserQuestionInfo{Questions: questions})
	}

	content := "[等待你的回复…]"
	if hasOptions {
		content = "[等待你的回复…]\n\n已向用户展示以下编号选项：\n" + rendered +
			"\n\n回复中的裸数字对应这里的编号。"
	}

	return contract.Result{
		Content:   content,
		UIContent: rendered,
		// EndTurn 让回合循环把本回合收为 final 并退出——用户的下一轮消息
		// 才是答案。**这是本工具的语义核心**，不是可选装饰。
		EndTurn: true,
	}, nil
}

func (t *askUserQuestionTool) RequiresApproval(*CallParams) bool { return false }
func (t *askUserQuestionTool) ConcurrencySafe() bool             { return true }
func (t *askUserQuestionTool) Enabled() bool                     { return true }
func (t *askUserQuestionTool) Timeout(*CallParams) time.Duration { return 0 }
