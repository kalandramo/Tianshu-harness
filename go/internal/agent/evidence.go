// evidence.go —— TDD gate 的编辑计数（补 `session.Manager` 缺的那部分）。
//
// 对账 TS `src/agent/evidence.ts` 的 `EvidenceTracker`（**最小闭合子集**）。
//
// # 与 `session.Manager` 的分工（**关键，避免双重真相源**）
//
// Go 侧**已有**文件改动与验证的追踪，且在 `loop.go` 的 `observeToolResult`
// 里已接线：
//
//	TrackFileModified   → session.Manager.FileIndex（ModifiedByMe 标记）
//	RecordVerification  → session.Manager.Verification（同 target 替换）
//
// 故 `evidenceState` 的 `filesModified` / `verifiedCount` **不必另造**——
// 从 `session.Snapshot()` 派生即可（见 `EvidenceStateFromSession`）。
//
// **本文件只补 `session.Manager` 不提供的**：`editsSinceLastTest` 与
// `hasCodeEdits`——那是「**距上次验证的连续代码编辑数**」，是**相对计数**
// （验证即归零），与会话状态的累计式追踪语义不同。
//
// # 为什么需要 `editsSinceLastTest`
//
// 它驱动 TDD gate：**连续 3 次未验证的代码编辑**应当被约束。TS 注释明确
// 「gate 针对零验证的编辑，**不是**测试通过强制」——故**任何**验证
// （passed/failed/blocked）都归零。
//
// # 本刀范围
//
// 只做 gate 计数 + `evidenceState` 派生。
// **不含**：`buildSummary`（delivery gate 投影）、`deliveryStatus` 全窗口
// 语义、`inferVerificationLevel`/`inferVerifiedFiles`（命令解析）——
// 那些需要 `delivery-gate.ts` 整套，是独立的一刀。
package agent

import (
	"regexp"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// codeExtensions 是计入 TDD gate 的代码文件扩展名。
//
// 对账 TS `CODE_EXTENSIONS`。**配置文件（.yml/.yaml/.toml/.json/.ini/.env）
// 被刻意排除**——它们不需要测试覆盖，计入会造成 gate 误触发。
var codeExtensions = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rs": true, ".go": true, ".java": true, ".kt": true, ".scala": true,
	".c": true, ".cpp": true, ".cc": true, ".h": true, ".hpp": true,
	".rb": true, ".swift": true, ".vue": true, ".svelte": true,
	".sh": true, ".bash": true, ".zsh": true,
	".sql": true, ".graphql": true,
	".css": true, ".scss": true, ".less": true,
}

// isCodeFile 报告路径是否是需要测试覆盖的代码文件。
//
// 对账 TS `isCodeFile`：取**最后一个点**之后的部分作为扩展名。
func isCodeFile(path string) bool {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return false
	}
	return codeExtensions[path[idx:]]
}

// scratchPathRe 匹配 `.rivet/scratch/` 下的一次性探针文件。
//
// 对账 TS `isScratchPath`：`/(?:^|[/\\])\.rivet[/\\]scratch(?:[/\\]|$)/`。
//
// **为什么要排除**：scratch 下的是微探针（throwaway behavior-verification
// scripts），不是交付物。计入会让 RED gate **惩罚它本该鼓励的探针纪律**。
var scratchPathRe = regexp.MustCompile(`(?:^|[/\\])\.rivet[/\\]scratch(?:[/\\]|$)`)

// isScratchPath 报告路径是否在 `.rivet/scratch/` 下。
func isScratchPath(path string) bool {
	return scratchPathRe.MatchString(path)
}

// testFileRe 匹配测试文件路径模式（决定 `hasReadTestFiles`）。
//
// 对账 TS：`/\.test\.|\.spec\.|__tests__|_test\.|test_/`。
var testFileRe = regexp.MustCompile(`\.test\.|\.spec\.|__tests__|_test\.|test_`)

// tddGateState 是 TDD gate 的状态快照（纯值，无引用）。
//
// 对账 TS `TddGateState`。
type tddGateState struct {
	// FilesModified 是自 reset 以来被 edit/write 工具改过的**去重**文件数。
	FilesModified int
	// Verifications 是自 reset 以来记录的验证次数。
	Verifications int
	// EditsSinceLastTest 是距最近一次测试运行的连续代码编辑数
	// （任何 trackVerification 都归零）。
	EditsSinceLastTest int
	// HasFailedTests 是「是否曾有验证以失败告终」——**粘滞**（一旦真则恒真）。
	HasFailedTests bool
	// HasCodeEdits 是「是否有代码文件被改」（纯文档改动时为 false → gate 跳过）。
	HasCodeEdits bool
	// HasReadTestFiles 是「是否读过测试文件」——供 skipIfNoTests：
	// 项目无测试文件时不阻塞。
	HasReadTestFiles bool
}

// evidenceTracker 补 `session.Manager` 不提供的 TDD gate 计数。
//
// 对账 TS `EvidenceTracker` 的 **`editsSinceLastTest` / `hasCodeEdits`** 部分。
//
// **为什么不重复追踪 filesModified/verifications**：那两个事实
// `session.Manager` 已在追踪（`loop.go` 已接线），另存一份会形成双重真相源
// ——任一处漏更新就会漂移。本类型只持有**相对计数**。
type evidenceTracker struct {
	// editsSinceLastTest 是距最近一次测试运行的连续**代码**编辑数。
	//
	// **只对代码文件累加**（文档/配置/scratch 不计）——否则 gate 会拦住
	// 纯文档工作（那没有测试可跑）。
	editsSinceLastTest int
	// hasCodeEdits 是「是否改过代码文件」（与 editsSinceLastTest 同条件置位）。
	hasCodeEdits bool
	// hasFailedTests 是「是否曾有验证失败」——粘滞。
	//
	// **与 session.Manager.Verification 的区别**：那是「同 target 替换」的
	// 当前态（跑绿了就没了）；本字段是**历史粘滞**（曾经红过就一直红）——
	// 对账 TS `getGateState` 的 `verifications.some(v => v.status === 'failed')`
	// 在**全窗口**上的语义。
	hasFailedTests bool
	// verifications 是验证事件的**追加计数**。
	//
	// **为什么不从 session.Manager 派生**（实测踩过）：`RecordVerification`
	// 是**同 target 替换**语义（同一测试命令只留最新状态），而 TS 的
	// `verifications` 是**追加数组**——用前者派生后者不等价
	// （oracle 的 `two-verifies` 期望 2，替换语义下得 1）。
	// 故此处只维护**计数**（不存全量记录，那不在本刀范围）。
	verifications int
}

// newEvidenceTracker 创建空的 gate 计数器。
func newEvidenceTracker() *evidenceTracker {
	return &evidenceTracker{}
}

// TrackFileModified 记录一次文件修改，更新 gate 计数。
//
// 对账 TS `trackFileModified` 的 gate 部分：
//
//	if (isCodeFile(path) && !isScratchPath(path)) { editsSinceLastTest++; hasCodeEdits = true }
//
// **调用点**：`loop.go` 的 `observeToolResult`——与 `l.State.TrackFileModified`
// **并列**（一个记会话索引，一个记 gate 计数，两处事实不同）。
func (e *evidenceTracker) TrackFileModified(path string) {
	if path == "" {
		return
	}
	if isCodeFile(path) && !isScratchPath(path) {
		e.editsSinceLastTest++
		e.hasCodeEdits = true
	}
}

// TrackVerification 记录一次验证事件。
//
// 对账 TS `trackVerification` 的 gate 部分：
//
//	editsSinceLastTest = 0     ← **任何**验证都归零（passed/failed/blocked）
//
// **归零语义是关键**：TDD gate 针对的是「零验证的编辑」，**不是**「测试必须
// 通过」。故失败的验证同样归零——否则「改→跑→红→改」的正常 TDD 节奏会被
// 误判为「未验证」。
func (e *evidenceTracker) TrackVerification(status string) {
	e.editsSinceLastTest = 0
	e.verifications++
	if status == "failed" {
		e.hasFailedTests = true
	}
}

// GateState 返回 TDD gate 的状态快照。
//
// 对账 TS `getGateState`。**`FilesModified` 从会话状态派生**（不重复追踪），
// 其余字段来自本计数器。
//
// **`filesModified` 为何仍从外部传**：那是 `session.Manager` 的既有追踪
// （`FileIndex.ModifiedByMe`），另存会双重真相源。而 `verifications` **不能**
// 同样处理——见字段注释（替换 vs 追加语义）。
func (e *evidenceTracker) GateState(filesModified int, filesRead []string) tddGateState {
	hasReadTest := false
	for _, p := range filesRead {
		if testFileRe.MatchString(p) {
			hasReadTest = true
			break
		}
	}
	return tddGateState{
		FilesModified:      filesModified,
		Verifications:      e.verifications,
		EditsSinceLastTest: e.editsSinceLastTest,
		HasFailedTests:     e.hasFailedTests,
		HasCodeEdits:       e.hasCodeEdits,
		HasReadTestFiles:   hasReadTest,
	}
}

// HasVerificationDebt 报告是否存在验证债。
//
// 对账 TS `hasVerificationDebt`（单一口径，2026-07-25 advisory-ecology W3）：
//
//	deliveryStatus === 'failed' || editsSinceLastTest >= 3
//
// **不用「存在任何未验证编辑」**——正常的编辑→验证节奏会瞬时经过该状态。
//
// **Go 侧代入**：`deliveryStatus === 'failed'` 在全窗口语义上等价于
// `hasFailedTests`（粘滞）——两者都表达「曾失败过」。
func (e *evidenceTracker) HasVerificationDebt() bool {
	return e.hasFailedTests || e.editsSinceLastTest >= 3
}

// EvidenceStateFromSession 从会话快照派生 `filesModified`。
//
// 对账 TS `getState().filesModified.size`。
//
// **为什么不另造计数器**：`session.Manager` 已在追踪该事实
// （`loop.go` 的 `observeToolResult` 接线），派生比复制安全。
//
// **只数 `ModifiedByMe`**（读不撤销改的标记）——对账 TS `filesModified`
// 是「被 edit/write 改过」的集合，不含只读过的文件。
//
// **`verifiedCount` 不在此函数**：`session.Manager.Verification` 是「同
// target 替换」语义，与 TS 的追加数组不等价（实测 oracle `two-verifies`
// 期望 2、替换语义得 1）。故验证计数由 `evidenceTracker.verifications`
// 维护——见 `GateState().Verifications`。
func EvidenceStateFromSession(s session.SessionState) (filesModified int) {
	for _, k := range s.FileIndex.Keys() {
		if v, ok := s.FileIndex.Get(k); ok && v.ModifiedByMe {
			filesModified++
		}
	}
	return filesModified
}
