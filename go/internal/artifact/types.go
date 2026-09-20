// Package artifact 实现工具产物的存储层。
//
// 对账 TS `src/artifact/`（`types.ts` + `store.ts`）。
//
// # 为什么需要它
//
// 大工具结果（read_file 读大文件、grep 命中多、bash 长输出）会把消息历史
// 撑爆。TS 的做法是：超过按工具设定的阈值时，把**原文落盘**为 artifact，
// 历史里只留一行摘要引用 `[artifact:ID] ... Use read_section(...)`。
// 模型需要原文时用 `read_section` 按行范围取回。
//
// **Go 侧此前完全没有这一层**——`internal/compact/context_collapse.go` 的
// 注释明确记录了这个欠账（「Go 侧当前无 artifact 生产端，故所有 artifact
// 分支在生产路径上不会触发」）。本包补齐生产端。
//
// # 布局
//
//	<baseDir>/<sessionId>/_index.jsonl   ← append-only 索引（每行一个 Artifact）
//	<baseDir>/<sessionId>/<id>.raw       ← 原始内容
//
// 生产路径的 baseDir 是 `<cwd>/.rivet/artifacts`（对账 TS `loop.ts:846`）。
package artifact

// ArtifactSection 是 artifact 内的一个可寻址片段。
//
// 对账 TS `ArtifactSection`。**Go 侧当前不从内容里提取 sections**（那需要
// `summarize.ts` 的按扩展名分派逻辑，407 行，属后续刀）——保存时传空切片，
// 与 TS 的 `sections: []`（`tool-pipeline.ts` 的实际调用）一致。
type ArtifactSection struct {
	// Name 形如 "imports" / "exports" / "function:commitAction" / "lines:90-125"。
	Name string `json:"name"`
	// LineStart / LineEnd 是 1-based 闭区间。
	LineStart int `json:"lineStart"`
	LineEnd   int `json:"lineEnd"`
	// CharCount 是该片段的字符数。
	CharCount int `json:"charCount"`
}

// Artifact 是一条落盘的产物记录。
//
// 字段顺序对账 TS 接口声明序——JSON 序列化进 `_index.jsonl`，
// 键序影响索引文件的字节（虽不直接进请求前缀，但保持一致便于人工比对）。
type Artifact struct {
	// ID 全局唯一，形如 `<tool>:<shortUuid>`。
	ID string `json:"id"`
	// Tool 是产出该产物的工具名（read_file / grep / bash / run_tests...）。
	Tool string `json:"tool"`
	// Target 是文件路径 / grep 模式 / 命令 / 其他工具目标。
	Target string `json:"target"`
	// SessionID 是所属会话。
	SessionID string `json:"sessionId"`
	// CreatedAt 是毫秒时间戳。
	CreatedAt int64 `json:"createdAt"`
	// Summary 是注入消息历史的启发式摘要。
	Summary string `json:"summary"`
	// Sections 是结构化片段（当前恒为空——见 ArtifactSection 说明）。
	Sections []ArtifactSection `json:"sections"`
	// RawPath 是落盘原文的绝对路径。
	RawPath string `json:"rawPath"`
	// CharCount / LineCount 是原文的字符数与行数。
	CharCount int `json:"charCount"`
	LineCount int `json:"lineCount"`
	// SHA256 是保存时原文的哈希——用于检测 artifact 被篡改/损坏。
	SHA256 string `json:"sha256"`
}

// ArtifactRef 是注入消息历史的紧凑引用。
//
// 对账 TS `ArtifactRef`。
//
// ⚠️ **TS 侧无生产调用方**（grep 确认：只在 `types.ts` 定义 + 测试引用）——
// 实际的包装文案由 `tool-pipeline.ts` 内联拼接（`[artifact:${id}] ${summary}
// ... Use read_section(...)`），**不走** `formatArtifactRef`。
//
// Go 侧保持忠实移植（含 `FormatArtifactRef`），但**同样无生产调用方**——
// 接线的是 `agent/interceptResultForArtifact` 的内联文案（对账 TS 的实际路径）。
// 保留此类型与函数是为了：TS 契约完整性 + 未来若 TS 启用它时可同步。
// **不要**把它当成「artifact 引用格式的唯一来源」。
type ArtifactRef struct {
	ArtifactID string
	Summary    string
	CharCount  int
	LineCount  int
	// Sections 是片段名清单（供快速参考）。
	Sections []string
}

// FormatArtifactRef 生成注入消息历史的紧凑文本。
//
// 对账 TS `formatArtifactRef`：
//
//	`[${charCount} chars, ${lineCount} lines]${sectionList} ${summary} (use read_section to expand)`
//
// 其中 sectionList 为空串或 ` Sections: a, b.`。
func FormatArtifactRef(ref ArtifactRef) string {
	sectionList := ""
	if len(ref.Sections) > 0 {
		sectionList = " Sections: " + joinComma(ref.Sections) + "."
	}
	return "[" + itoa(ref.CharCount) + " chars, " + itoa(ref.LineCount) + " lines]" +
		sectionList + " " + ref.Summary + " (use read_section to expand)"
}

// joinComma 以 ", " 连接（对账 JS 的 Array.prototype.join(', ')）。
func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
