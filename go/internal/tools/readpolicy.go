package tools

// readpolicy.go —— 文件读取策略判定。
//
// 对账 TS `src/tools/read-policy.ts`（62 行，零外部依赖）。
//
// # 消费者
//
// `readFilePayload`（TS `read-file.ts:594`）：
//
//	policy := decideReadPolicy({ filePath, sizeBytes: fileSize, hasExplicitRange })
//
// 策略决定四件事：是否整体返回（`full`）、带编辑提示（`full-with-hint`）、
// 只给首页（`partial`）、只给预览（`preview`）、或要求显式范围
// （`reject-with-range`）。
//
// # Windows 路径的关键行为（探针实测，勿"优化"）
//
// `classifyPath` 的 generated 检测用 `/(?:^|\/)(?:dist|build|coverage|\.next)\//`——
// **只认正斜杠**。探针实测（`go/testdata/readpolicy/probe.ts`）：
//
//	"D:\\proj\\dist\\a.ts" → source（反斜杠不匹配）
//	"D:/proj/dist/a.ts"    → generated（正斜杠匹配）
//
// 这是 TS 的**真实行为**，Go 侧必须原样复刻——**不要**把 `\` 归一化为 `/`，
// 那会让 Windows 上的判定偏离 TS（同样是"偏离 TS 语义的优化是缺陷"）。

import "strings"

// ReadPolicyKind 对账 TS `ReadPolicyKind`。
type ReadPolicyKind string

// ReadPolicyAction 对账 TS `ReadPolicyAction`。
type ReadPolicyAction string

const (
	KindSource    ReadPolicyKind = "source"
	KindLog       ReadPolicyKind = "log"
	KindJSONL     ReadPolicyKind = "jsonl"
	KindGenerated ReadPolicyKind = "generated"
	KindMinified  ReadPolicyKind = "minified"
	KindUnknown   ReadPolicyKind = "unknown"

	ActionFull            ReadPolicyAction = "full"
	ActionFullWithHint    ReadPolicyAction = "full-with-hint"
	ActionPartial         ReadPolicyAction = "partial"
	ActionPreview         ReadPolicyAction = "preview"
	ActionRejectWithRange ReadPolicyAction = "reject-with-range"
)

// 对账 TS 的常量（逐字）。
const (
	logPreviewGuardBytes = 16 * 1024
	defaultPreviewLines  = 80
	defaultMaxRangeLines = 200
	sourceSmallBytes     = 20 * 1024
	sourceLargeBytes     = 80 * 1024
)

// ReadPolicyInput 对账 TS `ReadPolicyInput`。
type ReadPolicyInput struct {
	FilePath         string
	SizeBytes        int
	HasExplicitRange bool
}

// ReadPolicyDecision 对账 TS `ReadPolicyDecision`。
type ReadPolicyDecision struct {
	Kind          ReadPolicyKind
	Action        ReadPolicyAction
	Reason        string
	PreviewLines  int
	MaxRangeLines int
}

// classifyPath 按路径归类（对账 TS `classifyPath`）。
//
// **顺序敏感**：jsonl → log → minified → generated → source → unknown。
// 先匹配者胜（如 `a.jsonl` 是 jsonl 而非 source）。
func classifyPath(filePath string) ReadPolicyKind {
	lower := strings.ToLower(filePath)

	// `\.(?:jsonl|ndjson)(?:\.\d+)?$`
	if hasExtWithOptionalDigits(lower, []string{".jsonl", ".ndjson"}) {
		return KindJSONL
	}
	// `\.(?:log|out|err|trace)(?:\.\d+)?$`
	if hasExtWithOptionalDigits(lower, []string{".log", ".out", ".err", ".trace"}) {
		return KindLog
	}
	// `\.min\.(?:js|css)$`
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") {
		return KindMinified
	}
	// `(?:^|\/)(?:dist|build|coverage|\.next)\/` —— **只认正斜杠**（见文件头）。
	for _, seg := range []string{"dist", "build", "coverage", ".next"} {
		if strings.Contains(lower, "/"+seg+"/") || strings.HasPrefix(lower, seg+"/") {
			return KindGenerated
		}
	}
	// `\.(?:ts|tsx|js|jsx|mjs|cjs|json|md|css|scss|html|yml|yaml)$`
	for _, e := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".json", ".md", ".css", ".scss", ".html", ".yml", ".yaml"} {
		if strings.HasSuffix(lower, e) {
			return KindSource
		}
	}
	return KindUnknown
}

// hasExtWithOptionalDigits 复刻 `\.(?:ext1|ext2)(?:\.\d+)?$`。
//
// `a.jsonl` ✓、`a.jsonl.1` ✓、`a.jsonl.x` ✗。
func hasExtWithOptionalDigits(lower string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
		// 带数字后缀：`<ext>.<digits>`。
		if i := strings.LastIndex(lower, e+"."); i >= 0 && i+len(e)+1 < len(lower) {
			if allDigits(lower[i+len(e)+1:]) {
				return true
			}
		}
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DecideReadPolicy 判定读取策略（对账 TS `decideReadPolicy`）。
//
// 分支顺序**逐条对账**（顺序敏感）：
//  1. `hasExplicitRange` → full（**最高优先**——即使 generated/minified 也放行）
//  2. log/jsonl 且超 preview guard → preview
//  3. generated/minified → reject-with-range
//  4. source/unknown：>80KB → partial；>20KB → full-with-hint
//  5. 兜底 → full
func DecideReadPolicy(input ReadPolicyInput) ReadPolicyDecision {
	kind := classifyPath(input.FilePath)
	base := ReadPolicyDecision{
		Kind:          kind,
		PreviewLines:  defaultPreviewLines,
		MaxRangeLines: defaultMaxRangeLines,
	}

	if input.HasExplicitRange {
		base.Action = ActionFull
		base.Reason = "explicit range requested"
		return base
	}
	if (kind == KindLog || kind == KindJSONL) && input.SizeBytes > logPreviewGuardBytes {
		base.Action = ActionPreview
		base.Reason = "log-like file over preview guard"
		return base
	}
	if kind == KindGenerated || kind == KindMinified {
		base.Action = ActionRejectWithRange
		base.Reason = "generated or minified file requires an explicit range"
		return base
	}
	if kind == KindSource || kind == KindUnknown {
		if input.SizeBytes > sourceLargeBytes {
			base.Action = ActionPartial
			base.Reason = "large source file — returning first page with navigation hints"
			return base
		}
		if input.SizeBytes > sourceSmallBytes {
			base.Action = ActionFullWithHint
			base.Reason = "medium source file — full read with editing hints"
			return base
		}
	}
	base.Action = ActionFull
	base.Reason = "safe default read"
	return base
}
