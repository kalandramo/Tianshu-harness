// Package prompt 复刻 TS 侧的提示词引擎（src/prompt/static.ts）。
//
// 设计要点：
//
//  1. **提示词文本不手抄**——来自 data/prompt.json，该文件由
//     go/testdata/prompt/gen-oracle.ts 从真实 TS 代码路径写出。
//     Wave 1 的假绿事故（手抄字段序，双方同错、测试照绿）是这条纪律的由来。
//  2. **拼接逻辑在 Go 侧实现**（base + separator + calibration 片段），
//     而非直接存渲染结果——这样 TS 侧若改变拼接方式，Go 的对账测试会红。
//  3. **字节等价是硬目标**：system prompt 差一个字节即整条前缀缓存失效。
package prompt

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed data/prompt.json
var embedded []byte

type promptData struct {
	Base         string            `json:"base"`
	BaseBytes    int               `json:"baseBytes"`
	BaseSHA256   string            `json:"baseSha256"`
	Separator    string            `json:"separator"`
	Calibrations map[string]string `json:"calibrations"`
}

var data promptData

// baseBytes / baseSha256 暴露嵌入数据的指纹，供完整性测试对账。
var (
	baseBytes  int
	baseSha256 string
)

func init() {
	if err := json.Unmarshal(embedded, &data); err != nil {
		panic("prompt: 嵌入数据解析失败（data/prompt.json 损坏？）：" + err.Error())
	}
	baseBytes = data.BaseBytes
	baseSha256 = data.BaseSHA256
}

// Context 是 BuildSystemPrompt 的输入，对账 TS 侧的 StaticPromptContext。
//
// 注意：TS 侧还有 Tools 与 Audience 字段。Tools 目前不参与主控提示词渲染
// （buildSystemPrompt 只在 subagent 路径使用它）；Audience='subagent' 走
// 另一条渲染路径（buildSubagentSystemPrompt），尚未移植。
// 这两个字段的缺席是**已知的未移植面**，不是疏漏——见包注释与 HANDOFF。
type Context struct {
	// ModelFamily 决定是否附加 calibration 片段。
	// 空值表示不附加（对应 TS 侧 modelFamily === undefined）。
	ModelFamily string
}

// BuildSystemPrompt 复刻 TS 的 buildSystemPrompt（主控路径）。
//
// TS 逻辑：
//
//	const base = audience === 'subagent' ? subagent(BASE_PROMPT, tools) : BASE_PROMPT
//	const calibration = modelFamily ? MODEL_CALIBRATIONS[modelFamily] : undefined
//	return calibration ? base + '\n\n' + calibration : base
//
// 本函数只实现主控路径（audience 未设）。subagent 路径未移植。
func BuildSystemPrompt(ctx Context) string {
	if ctx.ModelFamily == "" {
		return data.Base
	}
	frag, ok := data.Calibrations[ctx.ModelFamily]
	if !ok {
		return data.Base
	}
	return data.Base + data.Separator + frag
}

// DetectModelFamily 复刻 TS 的 detectModelFamily。
//
// 顺序敏感：TS 按 deepseek → mimo → glm → openai → anthropic 依次子串匹配，
// 先命中者胜。颠倒顺序会改变结果（例如 "claude-gpt-hybrid" 在 TS 下是 openai）。
// 匹配大小写不敏感（TS 侧先 toLowerCase）。
func DetectModelFamily(modelName string) string {
	lower := strings.ToLower(modelName)
	switch {
	case strings.Contains(lower, "deepseek"):
		return "deepseek"
	case strings.Contains(lower, "mimo"):
		return "mimo"
	case strings.Contains(lower, "glm"):
		return "glm"
	case strings.Contains(lower, "gpt"),
		strings.Contains(lower, "o1"),
		strings.Contains(lower, "o3"),
		strings.Contains(lower, "o4"):
		return "openai"
	case strings.Contains(lower, "claude"),
		strings.Contains(lower, "opus"),
		strings.Contains(lower, "sonnet"),
		strings.Contains(lower, "haiku"):
		return "anthropic"
	default:
		return "unknown"
	}
}
