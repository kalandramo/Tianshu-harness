package compact

// trajectory.go —— 工具调用轨迹记录器。
//
// 对账 TS `src/agent/trajectory.ts` 的 `TrajectoryRecorder`。
//
// # 为什么需要它
//
// session split 的 handoff（`BuildSessionHandoff`）当前只从消息列表提取
// 「近期推理 + 文件清单」——**降级版**。TS 的完整 handoff 有 9 个章节，
// 其中「最近工具轨迹」「错误与修复」依赖本模块提供的结构化轨迹。
//
// # 包位置说明
//
// TS 侧在 `src/agent/trajectory.ts`（agent 目录），Go 侧放在 `internal/compact`
// ——因为消费方 `BuildSessionHandoff` 在此包。若放 `internal/agent` 会造成
// import 循环（agent → compact 已存在）。

// TrajectoryStatus 是一次工具调用的结局。
//
// 对账 TS 的联合类型 `'success' | 'failed' | 'retried-success' | 'retried-failed'`。
type TrajectoryStatus string

const (
	TrajectorySuccess        TrajectoryStatus = "success"
	TrajectoryFailed         TrajectoryStatus = "failed"
	TrajectoryRetriedSuccess TrajectoryStatus = "retried-success"
	TrajectoryRetriedFailed  TrajectoryStatus = "retried-failed"
)

// TrajectoryEntry 是一次工具调用的轨迹记录。
//
// 字段顺序对账 TS 接口声明序——`exportJson` 的字节依赖它（Go 的
// encoding/json 按 struct 字段序输出）。
type TrajectoryEntry struct {
	Turn          int              `json:"turn"`
	Tool          string           `json:"tool"`
	Target        string           `json:"target"`
	DurationMs    int              `json:"durationMs"`
	Status        TrajectoryStatus `json:"status"`
	InputSummary  string           `json:"inputSummary"`
	ResultSummary string           `json:"resultSummary"`
	// ErrorClass 是失败分类（成功时为 ""）。
	//
	// **键序陷阱**：TS 的 `exportJson` 走 `JSON.stringify(entry)`，键序由
	// **对象构造序**决定。TS 调用点（turn-harness.ts:83）把 errorClass 放在
	// status 与 inputSummary 之间，但 `errorClass: undefined` 时该键**不出现**
	// （JSON.stringify 丢弃 undefined）。Go 的 `omitempty` 复刻这个行为。
	//
	// 位置刻意放在**末尾**：TS 的 spread 构造（oracle 生成脚本的 `entry(over)`）
	// 会把 errorClass 推到末尾——oracle 记录的就是这个序。真实调用点的序不同，
	// 但 `exportJson` 无生产消费方（grep 确认），故以 oracle 为准。
	ErrorClass string `json:"errorClass,omitempty"`
}

// IsFailure 报告该结局是否算失败。
//
// 对账 TS 的 `status === 'failed' || status === 'retried-failed'`。
func (e TrajectoryEntry) IsFailure() bool {
	return e.Status == TrajectoryFailed || e.Status == TrajectoryRetriedFailed
}

// TrajectorySummary 是轨迹的聚合统计。
//
// 字段顺序对账 TS 的返回对象字面量序（totalTools → failures → retries →
// avgDurationMs）。
type TrajectorySummary struct {
	TotalTools    int `json:"totalTools"`
	Failures      int `json:"failures"`
	Retries       int `json:"retries"`
	AvgDurationMs int `json:"avgDurationMs"`
}

// DefaultTrajectoryMaxEntries 对账 TS 的 `DEFAULT_MAX_ENTRIES`。
const DefaultTrajectoryMaxEntries = 200

// TrajectoryRecorder 累积工具调用轨迹，超出上限时**保留尾部**。
//
// 对账 TS `TrajectoryRecorder`。零值不可用（maxEntries 为 0 会导致每次记录
// 后立刻清空）——用 `NewTrajectoryRecorder` 构造。
type TrajectoryRecorder struct {
	entries    []TrajectoryEntry
	maxEntries int
}

// NewTrajectoryRecorder 构造记录器。maxEntries <= 0 时用默认值。
//
// **与 TS 的差异**：TS 构造函数默认参数是 `DEFAULT_MAX_ENTRIES`，但显式传 0
// 会得到 maxEntries=0（记录一条即清空）。Go 无默认参数，故把 <=0 视为
// 「用默认」——这是**有意的差异**，避免 Go 调用方零值构造时静默失效。
func NewTrajectoryRecorder(maxEntries int) *TrajectoryRecorder {
	if maxEntries <= 0 {
		maxEntries = DefaultTrajectoryMaxEntries
	}
	return &TrajectoryRecorder{maxEntries: maxEntries}
}

// Record 追加一条记录，超出上限时丢弃最旧的。
//
// 对账 TS：`push` 后 `if (length > max) entries = entries.slice(-max)`。
// 注意是 **>** 而非 >=——恰好等于上限时不裁剪。
func (r *TrajectoryRecorder) Record(entry TrajectoryEntry) {
	r.entries = append(r.entries, entry)
	if len(r.entries) > r.maxEntries {
		r.entries = r.entries[len(r.entries)-r.maxEntries:]
	}
}

// Entries 返回当前轨迹的**副本**（防止调用方改内部状态）。
func (r *TrajectoryRecorder) Entries() []TrajectoryEntry {
	out := make([]TrajectoryEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Summarize 聚合统计。
//
// 对账 TS 的 `summarize()`：
//   - failures = status 为 failed 或 retried-failed 的条数
//   - retries = status **以 "retried" 开头**的条数
//   - avgDurationMs = 总时长 / 条数，四舍五入（**空时为 0，不除零**）
func (r *TrajectoryRecorder) Summarize() TrajectorySummary {
	total := len(r.entries)
	failures := 0
	retries := 0
	sum := 0
	for _, e := range r.entries {
		if e.IsFailure() {
			failures++
		}
		if len(e.Status) >= 7 && e.Status[:7] == "retried" {
			retries++
		}
		sum += e.DurationMs
	}
	avg := 0
	if total > 0 {
		// 对账 TS 的 Math.round(sum / total)。
		// 用整数运算避免浮点：round(a/b) = (a*2 + b) / (b*2) 仅对正数成立。
		avg = (sum*2 + total) / (total * 2)
	}
	return TrajectorySummary{
		TotalTools:    total,
		Failures:      failures,
		Retries:       retries,
		AvgDurationMs: avg,
	}
}

// Reset 清空轨迹。
func (r *TrajectoryRecorder) Reset() {
	r.entries = nil
}
