package agent

import (
	"regexp"
	"strings"
)

// AdvisoryReadback 是劝导采纳率台账——核销闭环的核心。
//
// 对账 src/agent/advisory-readback.ts 的 AdvisoryReadback **核心路径**：
// track（送达跟踪）/ observeTool（行为观察）/ evaluate（核销评估）/
// 谓词求值 / 查询方法。
//
// **未移植**：
//   - 跨会话 lift 持久化（`readFile` 注入 + JSONL 读写）——依赖跨会话存储层
//   - `seedPriors`（先验播种）——同上
//
// 这两个都是**注入式增强**：不注入时行为即核心路径（本文件）。
//
// **为什么它重要**：四个治理子系统（习惯化对抗 / efficacy 负反馈环 /
// lift 消费 / holdout 反抽样）都以它为前置——它们都需要「这条提醒被采纳了吗」
// 的事实。
type AdvisoryReadback struct {
	// stats 是 per-key 统计。
	stats map[string]*AdvisoryKeyStats
	// pending 是待核销的谓词（已送达、窗口未到）。
	pending []pendingExpectation
	// events 是工具观察日志（按轮保留）。
	events []ObservedToolEvent
	// outcomes 是核销判定记录（供遥测）。
	outcomes []AdvisoryOutcomeEvent
	// priors 是跨会话先验（seedPriors 注入；本移植保留字段但无持久化）。
	priors map[string]EfficacyPriorCounts
	// readFile 是 pattern_absent 谓词的文件读取器（注入）。
	//
	// nil 时视为「文件不可读」→ 谓词满足（对账 TS 的 defaultReadFile 返回
	// null 分支）。与 TS 的 `constructor(readFile = defaultReadFile)` 同模式。
	readFile func(path string) string
}

// AdvisoryKeyStats 是单 key 的统计。
//
// 对账 AdvisoryKeyStats。
type AdvisoryKeyStats struct {
	Delivered int
	Adopted   int
	Ignored   int
	// IgnoredStreak 是连续 ignored 次数——adopted 时清零。
	// **P1b 习惯化对抗的触发信号**。
	IgnoredStreak int
	// ShadowHeld 是 holdout 反事实组的扣留次数（不计入 Delivered）。
	ShadowHeld int
	// ShadowSatisfied 是扣留期内谓词仍被自发满足的次数——「没提醒也会做」的基线。
	ShadowSatisfied int
}

// ObservedToolEvent 是单条工具观察（postTool 喂入，核销评估的证据源）。
//
// 对账 ObservedToolEvent。
type ObservedToolEvent struct {
	Turn int
	Name string
	// Target：bash → command；写/读类 → file_path；其余 → target 字段。
	Target  string
	IsError bool
}

// AdvisoryOutcome 是核销判定结果。
type AdvisoryOutcome string

const (
	OutcomeAdopted AdvisoryOutcome = "adopted"
	OutcomeIgnored AdvisoryOutcome = "ignored"
)

// AdvisoryOutcomeEvent 是单次核销判定（供遥测落盘）。
//
// 对账 AdvisoryOutcomeEvent。
type AdvisoryOutcomeEvent struct {
	Key           string
	Outcome       AdvisoryOutcome
	ExpectKind    ExpectKind
	DeliveredTurn int
	EvaluatedTurn int
	Shadow        bool
}

// pendingExpectation 是待核销的谓词（私有——仅内部 pending 队列用）。
type pendingExpectation struct {
	Key           string
	Expect        AdvisoryExpectation
	DeliveredTurn int
	Shadow        bool
}

// UnresolvedExpectation 是会话结束时仍未到期的谓词（观测用，不记账）。
type UnresolvedExpectation struct {
	Key           string
	ExpectKind    ExpectKind
	DeliveredTurn int
}

// EfficacyPriorCounts 是跨会话效能先验。
//
// 对账 EfficacyPriorCounts（EWMA 衰减后可为小数）。
type EfficacyPriorCounts struct {
	Adopted float64
	Ignored float64
}

// defaultWindow 是各谓词的缺省观察窗口（轮，含送达轮）。
//
// 对账 DEFAULT_WINDOW。**注意 pattern_absent 是 4**——探针清理合法地可以晚
// 几轮（修完再清），窗口放宽。
var defaultWindow = map[ExpectKind]int{
	ExpectToolAppears:     1,
	ExpectVerifyAttempted: 2,
	ExpectFileTouched:     1,
	ExpectPatternAbsent:   4,
	ExpectCourseChanged:   2,
}

// verifyToolNames 是 verify_attempted 认可的工具。
//
// 对账 VERIFY_TOOL_NAMES（与 self-verify/CCR 的 VERIFY 家族同源）。
var verifyToolNames = map[string]bool{
	"run_tests": true, "typecheck": true, "lsp_diagnostics": true,
}

// verifyBashRe 匹配 bash 中的验证类命令。
//
// 对账 VERIFY_BASH_RE：
// `\b(test|vitest|jest|pytest|mocha|tsx\s+--test|npm\s+(run\s+)?(test|typecheck)|tsc\b)`（i）
var verifyBashRe = regexp.MustCompile(`(?i)\b(test|vitest|jest|pytest|mocha|tsx\s+--test|npm\s+(run\s+)?(test|typecheck)|tsc\b)`)

// eventRetentionTurns 是观察日志保留的最大轮跨度。
//
// 对账 EVENT_RETENTION_TURNS=8（pattern_absent 最长窗口 4 + 余量）。
const eventRetentionTurns = 8

// coursePreWindowTurns 是 course_changed 的前置对照窗（轮）。
//
// 对账 COURSE_PRE_WINDOW_TURNS=3。
const coursePreWindowTurns = 3

// toolFamily 是工具族映射。
//
// 对账 TOOL_FAMILY。
var toolFamily = map[string]string{
	"edit_file": "edit", "write_file": "edit", "hash_edit": "edit",
	"apply_patch": "edit", "ast_edit": "edit",
	"read_file": "read", "read_section": "read", "grep": "read",
	"glob": "read", "list_dir": "read", "file_info": "read",
	"run_tests": "verify", "typecheck": "verify", "lsp_diagnostics": "verify",
}

// courseSignature 计算粗签名。
//
// 对账 courseSignature：read/edit 族的 target 是文件路径 → 文件面进签名；
// 其余只看族——命令文本逐字比对太细，会把「换个参数重跑同一条死路」误判为改道。
func courseSignature(e ObservedToolEvent) string {
	family, ok := toolFamily[e.Name]
	if !ok {
		family = e.Name
	}
	if family == "read" || family == "edit" {
		return family + ":" + e.Target
	}
	return family
}

// NewAdvisoryReadback 构造 readback。
func NewAdvisoryReadback() *AdvisoryReadback {
	return &AdvisoryReadback{
		stats:  map[string]*AdvisoryKeyStats{},
		priors: map[string]EfficacyPriorCounts{},
	}
}

// statsFor 取（或建）key 的统计。
func (r *AdvisoryReadback) statsFor(key string) *AdvisoryKeyStats {
	s, ok := r.stats[key]
	if !ok {
		s = &AdvisoryKeyStats{}
		r.stats[key] = s
	}
	return s
}

// SeedPriors 播种跨会话先验。
func (r *AdvisoryReadback) SeedPriors(priors map[string]EfficacyPriorCounts) {
	r.priors = priors
}

// Track 跟踪送达（render 后调用）。
//
// 对账 track。**同 key 重复送达时重置观察窗口（不叠加 pending）**。
//
// **shadow 状态翻转 = 反事实 trial 被污染，作废 shadow 一侧**：
//   - 已有真实 pending + 新扣留 → 模型近期已见过提醒，扣留无对照价值
//   - 已有 shadow pending + 新真实送达 → 扣留期被打断，基线测不成
func (r *AdvisoryReadback) Track(delivered []DeliveredAdvisory, turn int) {
	for _, d := range delivered {
		s := r.statsFor(d.Key)
		shadow := d.Shadow
		if shadow {
			s.ShadowHeld++
		} else {
			s.Delivered++
		}
		if d.Expect == nil {
			continue
		}

		existing := r.findPending(d.Key)
		if existing != nil {
			if existing.Shadow == shadow {
				// 同组重复送达：刷新观察窗口
				existing.Expect = *d.Expect
				existing.DeliveredTurn = turn
				continue
			}
			if shadow {
				s.ShadowHeld = maxInt(0, s.ShadowHeld-1)
				continue // 保留真实 pending
			}
			s.ShadowHeld = maxInt(0, s.ShadowHeld-1)
			existing.Expect = *d.Expect
			existing.DeliveredTurn = turn
			existing.Shadow = false
			continue
		}
		r.pending = append(r.pending, pendingExpectation{
			Key: d.Key, Expect: *d.Expect, DeliveredTurn: turn, Shadow: shadow,
		})
	}
}

// findPending 查找 pending 项（返回指针以支持原地修改）。
func (r *AdvisoryReadback) findPending(key string) *pendingExpectation {
	for i := range r.pending {
		if r.pending[i].Key == key {
			return &r.pending[i]
		}
	}
	return nil
}

// ObserveTool 记录行为观察（postTool 喂入）。
//
// 对账 observeTool。**按轮跨度修剪**（不按条数——重轮次 20+ 工具调用不能把
// 窗口内证据挤掉）。
func (r *AdvisoryReadback) ObserveTool(event ObservedToolEvent) {
	r.events = append(r.events, event)
	cutoff := event.Turn - eventRetentionTurns
	if len(r.events) > 0 && r.events[0].Turn < cutoff {
		kept := r.events[:0]
		for _, e := range r.events {
			if e.Turn >= cutoff {
				kept = append(kept, e)
			}
		}
		r.events = kept
	}
}

// Evaluate 核销评估（postTurn 调用），返回本轮判定数。
//
// 对账 evaluate。**两种判定时机**：
//   - `pattern_absent`（负向）：**只在到期时判**——过早读文件会把「还没来得及清」
//     误判为忽略
//   - 其余（正向）：窗口内满足即 adopted，到期未满足则 ignored
func (r *AdvisoryReadback) Evaluate(turn int) int {
	if len(r.pending) == 0 {
		return 0
	}

	var still []pendingExpectation
	decided := 0

	for _, p := range r.pending {
		window := p.Expect.WithinTurns
		if window == 0 {
			window = defaultWindow[p.Expect.Kind]
		}
		deadline := p.DeliveredTurn + window - 1

		var outcome AdvisoryOutcome
		hasOutcome := false

		if p.Expect.Kind == ExpectPatternAbsent {
			if turn >= deadline {
				if r.checkPatternAbsent(p.Expect) {
					outcome = OutcomeAdopted
				} else {
					outcome = OutcomeIgnored
				}
				hasOutcome = true
			}
		} else {
			if r.checkPositive(p.Expect, p.DeliveredTurn, turn) {
				outcome = OutcomeAdopted
				hasOutcome = true
			} else if turn >= deadline {
				outcome = OutcomeIgnored
				hasOutcome = true
			}
		}

		if !hasOutcome {
			still = append(still, p)
			continue
		}

		decided++
		s := r.statsFor(p.Key)
		if p.Shadow {
			// 反事实组：只进 shadow 桶，不动 adopted/ignored/streak
			// （不污染副驾闸门与习惯化）
			if outcome == OutcomeAdopted {
				s.ShadowSatisfied++
			}
		} else if outcome == OutcomeAdopted {
			s.Adopted++
			s.IgnoredStreak = 0
		} else {
			s.Ignored++
			s.IgnoredStreak++
		}

		r.outcomes = append(r.outcomes, AdvisoryOutcomeEvent{
			Key: p.Key, Outcome: outcome, ExpectKind: p.Expect.Kind,
			DeliveredTurn: p.DeliveredTurn, EvaluatedTurn: turn, Shadow: p.Shadow,
		})
	}

	r.pending = still
	return decided
}

// FlushAtSessionEnd 是会话结束核销（postSession 调用）。
//
// 对账 flushAtSessionEnd。**先按当前证据跑一次正常 evaluate，再把仍未到期的
// pending 清空并如实报告**。
//
// **未到期的不判 ignored**（TS 注释）：advisory 在末轮送达时，模型根本没走完
// 观察窗口的机会，判忽略会把「没机会响应」记成「听了不做」。这类假 ignored 会
// 经 ignoredStreak（习惯化静音）、efficacy 负反馈、跨会话 lift 先验三条路径压低
// 该 key 的效力评分，最终静音掉本可能有效的提醒。
//
// worker 尤其吃这一刀——中位只跑 2 轮，而 verify_attempted 窗口 2 轮、
// pattern_absent 4 轮，几乎所有 pending 在会话结束时都未到期。
func (r *AdvisoryReadback) FlushAtSessionEnd(turn int) (int, []UnresolvedExpectation) {
	decided := r.Evaluate(turn)
	unresolved := make([]UnresolvedExpectation, 0, len(r.pending))
	for _, p := range r.pending {
		unresolved = append(unresolved, UnresolvedExpectation{
			Key: p.Key, ExpectKind: p.Expect.Kind, DeliveredTurn: p.DeliveredTurn,
		})
	}
	r.pending = nil
	return decided, unresolved
}

// DrainOutcomes 读取并清空核销判定记录。
func (r *AdvisoryReadback) DrainOutcomes() []AdvisoryOutcomeEvent {
	out := r.outcomes
	r.outcomes = nil
	return out
}

// Stats 返回统计快照（副本）。
func (r *AdvisoryReadback) Stats() map[string]AdvisoryKeyStats {
	out := make(map[string]AdvisoryKeyStats, len(r.stats))
	for k, v := range r.stats {
		out[k] = *v
	}
	return out
}

// GetIgnoredStreak 返回 key 的连续忽略次数。
func (r *AdvisoryReadback) GetIgnoredStreak(key string) int {
	if s, ok := r.stats[key]; ok {
		return s.IgnoredStreak
	}
	return 0
}

// GetDeliveredCount 返回 key 的送达次数。
func (r *AdvisoryReadback) GetDeliveredCount(key string) int {
	if s, ok := r.stats[key]; ok {
		return s.Delivered
	}
	return 0
}

// GetAdoptionRate 返回采纳率（nil 表示无判定样本）。
//
// 对账 getAdoptionRate：**会话统计 + 先验合并**（避免「率含先验、样本数不含」
// 的错配）。
func (r *AdvisoryReadback) GetAdoptionRate(key string) *float64 {
	s := r.stats[key]
	p := r.priors[key]

	adopted := float64(0)
	ignored := float64(0)
	if s != nil {
		adopted += float64(s.Adopted)
		ignored += float64(s.Ignored)
	}
	adopted += p.Adopted
	ignored += p.Ignored

	decided := adopted + ignored
	if decided <= 0 {
		return nil
	}
	v := adopted / decided
	return &v
}

// GetDecidedCount 返回判定样本数（含先验，与 GetAdoptionRate 同口径）。
func (r *AdvisoryReadback) GetDecidedCount(key string) int {
	s := r.stats[key]
	p := r.priors[key]
	n := 0
	if s != nil {
		n += s.Adopted + s.Ignored
	}
	// 先验是浮点（EWMA 衰减后）——这里取整（对账 TS 的加法直接得 number）
	n += int(p.Adopted + p.Ignored)
	return n
}

// GetLift 返回 lift（nil 表示样本不足）。
//
// 对账 getLift：`adopted/decided - shadowSatisfied/shadowHeld`。
// **decided 为 0 或 shadowHeld 为 0 时返回 nil**。
func (r *AdvisoryReadback) GetLift(key string) *float64 {
	s, ok := r.stats[key]
	if !ok {
		return nil
	}
	decided := s.Adopted + s.Ignored
	if decided == 0 || s.ShadowHeld == 0 {
		return nil
	}
	v := float64(s.Adopted)/float64(decided) - float64(s.ShadowSatisfied)/float64(s.ShadowHeld)
	return &v
}

// checkPositive 求值正向谓词。
//
// 对账 checkPositive。**五种 kind**：
//
//	tool_appears     — 窗口内出现指定工具（可选 targetIncludes 约束）
//	verify_attempted — 窗口内跑了验证工具或验证类 bash 命令
//	file_touched     — 窗口内触碰了指定路径（**子串匹配**）
//	course_changed   — 窗口内出现前置对照窗未见过的粗签名
//	（pattern_absent 是负向，不走这里）
func (r *AdvisoryReadback) checkPositive(expect AdvisoryExpectation, from, to int) bool {
	windowEvents := r.eventsInWindow(from, to)

	switch expect.Kind {
	case ExpectToolAppears:
		for _, e := range windowEvents {
			if !containsString(expect.Tools, e.Name) {
				continue
			}
			if expect.TargetIncludes != "" && !strings.Contains(e.Target, expect.TargetIncludes) {
				continue
			}
			return true
		}
		return false

	case ExpectVerifyAttempted:
		for _, e := range windowEvents {
			if verifyToolNames[e.Name] {
				return true
			}
			if e.Name == "bash" && verifyBashRe.MatchString(e.Target) {
				return true
			}
		}
		return false

	case ExpectFileTouched:
		for _, e := range windowEvents {
			for _, p := range expect.Paths {
				if strings.Contains(e.Target, p) {
					return true
				}
			}
		}
		return false

	case ExpectCourseChanged:
		// 改道 = 观察窗内出现前置对照窗未见过的 (工具族×文件面) 粗签名。
		// 前置窗空（no-tool 僵局）→ 任意工具事件即改道。
		var preEvents []ObservedToolEvent
		for _, e := range r.events {
			if e.Turn >= from-coursePreWindowTurns && e.Turn < from {
				preEvents = append(preEvents, e)
			}
		}
		if len(preEvents) == 0 {
			return len(windowEvents) > 0
		}
		preSigs := map[string]bool{}
		for _, e := range preEvents {
			preSigs[courseSignature(e)] = true
		}
		for _, e := range windowEvents {
			if !preSigs[courseSignature(e)] {
				return true
			}
		}
		return false
	}
	return false
}

// checkPatternAbsent 求值负向谓词。
//
// 对账 checkPatternAbsent。**注意**：TS 读文件检查 needles 是否消失——
// 文件不存在（readFile 返回 null）时**视为满足**（对账 TS 的 `if (!content) return true`）。
//
// **Go 侧注入 readFile**——nil 时视为「文件不可读 → 满足」（与 TS 缺省行为一致）。
func (r *AdvisoryReadback) checkPatternAbsent(expect AdvisoryExpectation) bool {
	if r.readFile == nil {
		return true // 无读取器 → 视为满足（对账 TS 的 null 分支）
	}
	content := r.readFile(expect.Path)
	if content == "" {
		return true
	}
	for _, needle := range expect.Needles {
		if strings.Contains(content, needle) {
			return false
		}
	}
	return true
}

// eventsInWindow 取 [from, to] 轮内的观察事件。
//
// 对账 `this.events.filter(e => e.turn >= from && e.turn <= to)`。
func (r *AdvisoryReadback) eventsInWindow(from, to int) []ObservedToolEvent {
	var out []ObservedToolEvent
	for _, e := range r.events {
		if e.Turn >= from && e.Turn <= to {
			out = append(out, e)
		}
	}
	return out
}

// maxInt 返回较大值。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// containsString 判定切片是否含指定值。
func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// SetReadFile 注入 pattern_absent 谓词的文件读取器。
func (r *AdvisoryReadback) SetReadFile(fn func(path string) string) {
	r.readFile = fn
}
