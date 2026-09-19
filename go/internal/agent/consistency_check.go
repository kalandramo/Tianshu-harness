package agent

import (
	"context"
	"strings"
)

// Consistency-Check Hook —— 原则 ⑤「有限规则无限涌现」。
//
// 对账 src/agent/hooks/consistency-check-hook.ts。
//
// **做什么**：当 write_file / edit_file 写入一个文件时，检查是否有
// file_observation claim 引用了该文件的旧状态。若有，调用
// `effects.markClaimStale` 将其标记为过期。
//
// **为什么重要**：这是 cross-store 耦合的第一条信号——
// evidence store（工具结果）→ claim store（知识）。文件被改了，
// 那些「我观察到 a.ts 里有 X」的知识就过期了。
type ConsistencyCheckHook struct {
	getFileObservations func() []FileObservation
}

// FileObservation 是一条 file_observation claim。
//
// 对账 ConsistencyCheckHookDeps 的返回类型
// `Array<{ id, text, evidence: Array<{ path? }> }>`。
type FileObservation struct {
	ID       string
	Text     string
	Evidence []EvidenceRef
}

// EvidenceRef 是 claim 的证据引用。
type EvidenceRef struct {
	// Path 是证据指向的文件路径（可为空——非文件类证据）。
	Path string
}

// NewConsistencyCheckHook 构造 hook。
//
// deps 只含 getFileObservations——由 anchor-registry 或 claim-store 提供。
// **依赖注入而非直接 import**：让 hook 可独立测试（不必拉起整个 claim store）。
func NewConsistencyCheckHook(getFileObservations func() []FileObservation) RuntimeHook {
	h := &ConsistencyCheckHook{getFileObservations: getFileObservations}
	return RuntimeHook{
		Name:  "consistency-check",
		Phase: PhasePostTool,
		Run:   h.run,
	}
}

// run 是 hook 本体。
func (h *ConsistencyCheckHook) run(ctx context.Context, hctx *RuntimeHookContext, tool *RuntimeToolEvent) error {
	// 只在写操作后触发
	if tool == nil {
		return nil
	}
	if tool.Name != "write_file" && tool.Name != "edit_file" {
		return nil
	}
	if tool.Target == "" {
		return nil
	}

	target := tool.Target
	for _, obs := range h.getFileObservations() {
		if observationReferencesFile(obs, target) {
			// **用 *Safe 方法**——effects 未接线时是 no-op（对账 TS 的 ?? noop）
			hctx.Effects.MarkClaimStaleSafe(obs.ID)
		}
	}
	return nil
}

// observationReferencesFile 判断 claim 的证据是否引用了目标文件。
//
// 对账 TS 的**三条匹配分支**：
//
//	e.path === tool.target
//	|| tool.target.endsWith(e.path)
//	|| e.path.endsWith(tool.target)
//
// **为什么三条都要**：claim 里存的可能是相对路径（`a.ts`）而工具 target 是
// `src/a.ts`（或反之）。漏掉任何一条都会漏标 claim。
func observationReferencesFile(obs FileObservation, target string) bool {
	for _, e := range obs.Evidence {
		if e.Path == "" {
			continue
		}
		if e.Path == target ||
			strings.HasSuffix(target, e.Path) ||
			strings.HasSuffix(e.Path, target) {
			return true
		}
	}
	return false
}
