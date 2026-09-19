package agent

// Advisory 通道的最小接口层。
//
// 对账 src/agent/advisory-bus.ts（1277 行）。**这里只做 hook 需要的最小面**——
// TS 侧的 hook 依赖声明本身就是收窄的（`Pick<AdvisoryBus, 'submit'>`），
// 见 typecheck-reminder-hook.ts 的 TypecheckReminderHookDeps。
//
// **未移植**：bus 本体的渲染 / 优先级排序 / 去重 / 挂起观察生命周期 /
// readback 核销 / holdout 分组。那些是独立模块，见 HANDOFF。

// AdvisoryTier 是优先级层。
//
// 对账 AdvisoryTier。constitutional 永不被截断，operational 参与 Top-N，
// informational 填空。
type AdvisoryTier string

const (
	TierConstitutional AdvisoryTier = "constitutional"
	TierOperational    AdvisoryTier = "operational"
	TierInformational  AdvisoryTier = "informational"
)

// AdvisoryCategory 是分类标签。
//
// 对账 AdvisoryCategory（TS 侧是 15 个值的联合类型；这里列 hook 实际用到的，
// 其余按需补——Go 用 string 别名，未列出的值也能承载）。
type AdvisoryCategory string

const (
	CategoryImmune         AdvisoryCategory = "immune"
	CategoryRepair         AdvisoryCategory = "repair"
	CategoryMistake        AdvisoryCategory = "mistake"
	CategoryDedup          AdvisoryCategory = "dedup"
	CategoryDeadEnd        AdvisoryCategory = "dead_end"
	CategoryCerebellar     AdvisoryCategory = "cerebellar"
	CategoryDiscipline     AdvisoryCategory = "discipline"
	CategoryEncouragement  AdvisoryCategory = "encouragement"
	CategoryConstitutional AdvisoryCategory = "constitutional"
	CategoryDelegation     AdvisoryCategory = "delegation"
	CategoryTypecheck      AdvisoryCategory = "typecheck"
	CategoryTodo           AdvisoryCategory = "todo"
	CategoryBackground     AdvisoryCategory = "background"
	CategoryMonitor        AdvisoryCategory = "monitor"
	CategoryStarDomain     AdvisoryCategory = "star_domain"
)

// ExpectKind 是采纳核销谓词的种类。
type ExpectKind string

const (
	// ExpectToolAppears 是「工具出现」——观察窗内出现指定工具即采纳。
	ExpectToolAppears ExpectKind = "tool_appears"
	// ExpectVerifyAttempted 是「验证尝试」——跑过 typecheck 之类即采纳。
	ExpectVerifyAttempted ExpectKind = "verify_attempted"
	// ExpectFileTouched 是「文件被触碰」。
	ExpectFileTouched ExpectKind = "file_touched"
	// ExpectPatternAbsent 是「模式消失」——指定文件里不再有这些串。
	ExpectPatternAbsent ExpectKind = "pattern_absent"
	// ExpectCourseChanged 是「改道」——出现前置对照窗未见过的签名。
	ExpectCourseChanged ExpectKind = "course_changed"
)

// AdvisoryExpectation 是采纳核销谓词。
//
// 对账 AdvisoryExpectation（TS 侧是五个变体的判别联合）。**Go 侧用扁平结构**
// ——因为 Go 无判别联合，用 Kind 字段区分变体，各变体字段共存（未用的为零值）。
// 这与 TS 的语义等价：消费方按 Kind 分支读对应字段。
type AdvisoryExpectation struct {
	Kind ExpectKind
	// Tools 用于 tool_appears。
	Tools []string
	// TargetIncludes 是 tool_appears 的可选 target 约束。
	TargetIncludes string
	// Paths 用于 file_touched。
	Paths []string
	// Path / Needles 用于 pattern_absent。
	Path    string
	Needles []string
	// WithinTurns 是观察窗（轮次）。
	WithinTurns int
}

// AdvisoryObserve 是挂起观察配置。
//
// 对账 `observe?: { turns: number }`。窗口内 expect 谓词被自发满足 → 自愈撤销；
// 被其他条目 corroborates 指认 → 提前确认；到期 → 强制送达。
type AdvisoryObserve struct {
	Turns int
}

// AdvisoryEntry 是一条待投递的 advisory。
//
// 对账 AdvisoryEntry。
type AdvisoryEntry struct {
	// Key 是去重键——同 key 在同轮只保留优先级最高的一条。
	Key string
	// Priority 是优先级（0-1 归一化，越高越靠前）。
	Priority float64
	// Category 是分类标签。
	Category AdvisoryCategory
	// Content 是渲染内容——**单行纯文本，不含 XML 标签**。
	Content string
	// Tier 是优先级层。
	Tier AdvisoryTier
	// TTL 是存活轮次（缺省 1 = 仅本轮）。
	TTL int
	// Expect 是采纳核销谓词（nil = 不参与采纳率统计）。
	Expect *AdvisoryExpectation
	// Immediate 跳过挂起观察与阶段抑制，直达投递。
	Immediate bool
	// Observe 是挂起观察（constitutional / immediate 条目忽略）。
	Observe *AdvisoryObserve
	// Corroborates 是多信号确认——本条目可提前确认这些 key 的挂起条目。
	Corroborates []string
	// Channel 是投递通道（缺省 bus）。
	Channel string
}

// AdvisorySink 是 hook 投递 advisory 的出口。
//
// 对账 TS 的 `Pick<AdvisoryBus, 'submit'>`——**接口收窄是刻意的**：
// hook 只该投递，不该读 bus 内部状态（接口隔离）。
type AdvisorySink interface {
	Submit(entry AdvisoryEntry)
}
