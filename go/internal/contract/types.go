// Package contract 是跨层中立契约包。
//
// 为什么需要它：TS 版存在大量跨层 `import type`（如 src/tools/types.ts 反向
// 引用 src/agent/delivery-gate-v2.ts），而 Go 禁止循环 import。把「被多层共同
// 依赖的类型」下沉到本包，各层单向依赖 contract，即可消除循环依赖。
//
// 类型翻译原则（与 TS 版 src/api/types.ts、src/tools/types.ts 对账）：
//   - TS 的 `field?: T` 三态（缺席 / undefined / 显式值）→ Go 用指针 *T
//   - TS 的 `field: T` 必有字段 → Go 用值类型
//   - TS 的 `string | ContentBlock[]` 联合 → Go 用接口 + 显式 tag（见 Content）
package contract

// Usage 是一次模型调用的 token 计量。
//
// 关键约定（与 TS 版 src/api/types.ts 一致）：InputTokens 是 cache-INCLUSIVE
// 的总量——InputTokens = uncached + CacheRead + CacheCreation。这是
// DeepSeek/OpenAI 原生语义（prompt_tokens = hit + miss）。Anthropic 等
// cache-EXCLUSIVE 的提供商必须在边界处归一化后再产出 Usage——下游
// （命中率计算、成本核算、meta tokenUsage）全部假设此约定。
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// ReasoningTokens 是 OutputTokens 的子集（**不叠加**）。仅支持思考的
	// 提供商会上报（DeepSeek V4 经 completion_tokens_details.reasoning_tokens）。
	// nil = 提供商未提供该拆分。文本 token = OutputTokens - ReasoningTokens。
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
}

// InputSchema 是工具入参的 JSON Schema。
type InputSchema struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required,omitempty"`
	AdditionalProperties *bool          `json:"additionalProperties,omitempty"`
}

// Definition 是工具的对外声明（模型可见的接口）。
type Definition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Capability 是 MCP 服务器策略声明的能力（read/write/execute）。
	// 非 MCP 工具留空。被 assessToolRisk 消费以给出准确的风险标注，
	// 而非硬编码 'unknown'。
	Capability string `json:"capability,omitempty"`
	// InputSchema 是 JSON Schema 形态的入参定义。
	// 注意：TS 版字段名为 input_schema（snake_case），此处用 Go 命名，
	// 序列化时经 json tag 对齐。
	InputSchema *InputSchema `json:"input_schema,omitempty"`
	// ProviderFormat 是提供商特定的格式覆盖（如 Anthropic 的 tool 定义形态）。
	ProviderFormat map[string]any `json:"providerFormat,omitempty"`
}

// Lossiness 描述工具结果的观测保真度。
//
// TS 版用 `lossiness?: 'lossless' | ...`，缺省（undefined）= lossless。
// Go 用指针保持三态——这是字节等价的必要条件：「缺席」与「显式声明 lossless」
// 在序列化时不同。
type Lossiness string

const (
	LossinessLossless    Lossiness = "lossless"
	LossinessTruncated   Lossiness = "truncated"
	LossinessCollapsed   Lossiness = "collapsed"
	LossinessPreviewOnly Lossiness = "preview_only"
)

// ToolErrorClass 是 shell 结果的三态分类。
//
// 'environment' = 命令因主机缺少它而无法运行（command-not-found）——这是
// 环境问题而非模型能力问题，下游（momentum/doom/approval）不得据此惩罚，
// 否则 Windows 的命令名差异会让 agent 变得胆怯。
// 'timeout' = 超出预算——慢 ≠ 死路，故 dead-end 信息素沉积也必须排除。
type ToolErrorClass string

const (
	ErrorClassEnvironment ToolErrorClass = "environment"
	ErrorClassExecFailure ToolErrorClass = "exec-failure"
	ErrorClassTimeout     ToolErrorClass = "timeout"
)

// Range 是文件行区间（1-based，闭区间）。
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// VerificationStatus 是验证命令的终态。
type VerificationStatus string

const (
	VerificationPassed  VerificationStatus = "passed"
	VerificationFailed  VerificationStatus = "failed"
	VerificationBlocked VerificationStatus = "blocked"
)

// VerificationMetadata 是一条验证记录。
//
// 数值字段全部用指针：TS 版在 2026-08-01 把这些降为可选（run_tests 等真实
// 执行始终填充；worker 自报的 verification 已降为交叉校验口径，系统补录的
// 元数据不含计数）。消费方**不得**假定它们存在。
type VerificationMetadata struct {
	Command    string             `json:"command"`
	Status     VerificationStatus `json:"status"`
	Scope      string             `json:"scope"` // full | targeted
	ExitCode   *int               `json:"exitCode,omitempty"`
	Passed     *int               `json:"passed,omitempty"`
	Failed     *int               `json:"failed,omitempty"`
	Skipped    *int               `json:"skipped,omitempty"`
	DurationMs *int64             `json:"durationMs,omitempty"`
	// BlockedReason 说明为何 blocked，使下游能给出场景化指引而非统一的
	// 「tests blocked」文案。
	BlockedReason     string   `json:"blockedReason,omitempty"`
	UserGuidance      string   `json:"userGuidance,omitempty"`
	TargetFiles       []string `json:"targetFiles,omitempty"`
	ResolvedCommand   string   `json:"resolvedCommand,omitempty"`
	RecommendedCmd    string   `json:"recommendedCommand,omitempty"`
	SnapshotRef       string   `json:"snapshotRef,omitempty"`
	VerificationPhase string   `json:"verificationPhase,omitempty"` // isolated | integration
	Timestamp         *int64   `json:"timestamp,omitempty"`
}

// Result 是工具执行的结果。
//
// 指针字段的语义：**缺席 ≠ 零值**。例如 ExitCode 缺席表示「该工具不适用退出码」
// （如 read_file），而 ExitCode=0 表示「命令成功执行」。把两者混为一谈会让
// 下游（证据门禁、失败分类、UI 展示）产生误判。
type Result struct {
	// Content 是发给模型的 tool_result 正文。
	Content string `json:"content"`
	// UIContent 是 UI 展示的摘要覆盖；缺省回退到 Content。
	UIContent string `json:"uiContent,omitempty"`
	// RawPath 是持久化的原始输出文件路径。
	RawPath string `json:"rawPath,omitempty"`
	// Lossiness 为 nil 时等价于 lossless（向后兼容的缺省）。
	Lossiness *Lossiness `json:"lossiness,omitempty"`
	// Images 是可选图片附件（data URL，如 computer_use 截图）。
	// 工具消息在协议层是纯文本；由工具管线决定是否作为后续多模态
	// user 消息转发（仅当活跃模型声明 supportsVision）。
	Images  []string `json:"images,omitempty"`
	IsError bool     `json:"isError,omitempty"`
	// Verification 是主验证记录。
	Verification *VerificationMetadata `json:"verification,omitempty"`
	// ExtraVerifications 是主记录之外需追加的验证事件。VSW 用它从一次
	// run_tests 调用中同时记录 Phase A（隔离）与 Phase B（集成）。
	ExtraVerifications []VerificationMetadata `json:"extraVerifications,omitempty"`
	// RawBytes / RawLines 是截断前的原始输出量（bash stdout+stderr）。
	RawBytes *int64 `json:"rawBytes,omitempty"`
	RawLines *int64 `json:"rawLines,omitempty"`
	// ExitCode 是 shell 命令的退出码（bash 专用）。
	ExitCode *int `json:"exitCode,omitempty"`
	// ErrorClass 是失败 shell 结果的三态分类，驱动 vigor 豁免。
	ErrorClass *ToolErrorClass `json:"errorClass,omitempty"`
	// ErrorKind 是结构化失败分类学（全谱），喂给 repair-hint / antibody /
	// doom-loop 指纹 / blocked 通知等下游。nil = 回退文本正则。
	//
	// 为何需要它：消息文案中文化后英文正则匹配会失灵，凡自家消息会被
	// classifyFailure 正则命中的工具必须优先打此字段。
	ErrorKind *string `json:"errorKind,omitempty"`
	// Command 是已执行的命令（bash），用于 ToolAccumulator 的按命令折叠摘要。
	Command string `json:"command,omitempty"`
	// EndTurn 指示回合循环在本结果后结束（如 ask_user_question 需要用户
	// 下一条消息作为答案）。true 时编排器把本回合收为 final 而非继续工具循环。
	EndTurn bool `json:"endTurn,omitempty"`
	// ChangedRanges 是写族工具（edit_file/write_file）报告的被触及行区间
	// （1-based，闭区间）。工具管线据此收窄 LSP 诊断的注入范围：
	// 区间内的诊断完整呈现，区间外的错误折叠为一行提示，区间外的警告丢弃。
	// 缺席 → 注入点保持整文件行为。
	ChangedRanges []Range `json:"changedRanges,omitempty"`
}
