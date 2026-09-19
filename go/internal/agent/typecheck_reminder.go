package agent

import "context"

// Typecheck-Reminder Hook —— postTurn 提醒，补 self-verify 的盲区。
//
// 对账 src/agent/hooks/typecheck-reminder-hook.ts。
//
// **为什么存在**：self-verify 把 `run_tests` 当作「已验证」。但测试运行器（tsx）
// 与编辑后的 syntaxCheck 都用 esbuild——esbuild 只**转译**，从不查类型。所以
// 重复对象键、重复接口成员、不可能的比较、误删造成的悬空引用都会**测试全绿却
// 打爆 tsc**。这正是「测试全绿但 typecheck 已坏」那个事故的失效模式。
//
// **触发条件**（三条全真，任务级而非 5 条窗口）：
//
//	touchedTsFiles          — 本会话写过 .ts/.tsx 文件
//	∧ !sawTypecheckThisTask — 自那次编辑后没跑过真 tsc/typecheck
//	∧ run_tests 在窗口内     — agent 刚用测试「验证」过（一个「完成」时刻）
//
// 触发时投递一条单轮 operational advisory。权威保证是 review-gate 兜底
// （TS 侧的 Component B）；本 hook 是**廉价的每轮提醒**（Component C），
// 自己不跑 tsc。
type TypecheckReminderHook struct {
	sink AdvisorySink
}

// NewTypecheckReminderHook 构造 hook。
//
// 对账 createTypecheckReminderHook。deps 只含 sink——与 TS 的接口隔离一致
// （TypecheckReminderHookDeps 只有 advisoryBus）。
func NewTypecheckReminderHook(sink AdvisorySink) RuntimeHook {
	h := &TypecheckReminderHook{sink: sink}
	return RuntimeHook{
		Name:  "typecheck-reminder",
		Phase: PhasePostTurn,
		Run:   h.run,
	}
}

// run 是 hook 本体。
func (h *TypecheckReminderHook) run(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
	snapshot := hctx.Snapshot

	// 三条触发条件——顺序与 TS 一致（早退，省判断）。
	if !snapshot.TouchedTSFiles {
		return nil
	}
	if snapshot.SawTypecheck {
		return nil
	}

	ranTests := false
	for _, entry := range snapshot.RecentToolHistory {
		if entry.Tool == "run_tests" {
			ranTests = true
			break
		}
	}
	if !ranTests {
		return nil
	}

	h.sink.Submit(AdvisoryEntry{
		Key:      "typecheck-reminder",
		Priority: 0.6,
		Category: CategoryTypecheck,
		Tier:     TierOperational,
		// **逐字对账 TS 原文**——用户可见文案不可转述。
		Content: "【天梁】你改了 TS 文件、跑了测试,但没跑类型检查。esbuild/tsx 只转译不查类型——重复键/重复成员/悬空引用都不会报。交付前跑 `npm run typecheck`(或 `tsc --noEmit`)再声明完成。",
		TTL:     1,
		// P1a 核销 + Phase 2 挂起观察：下一轮自发跑 typecheck → 自愈撤销
		Expect:  &AdvisoryExpectation{Kind: ExpectVerifyAttempted, WithinTurns: 2},
		Observe: &AdvisoryObserve{Turns: 1},
	})
	return nil
}
