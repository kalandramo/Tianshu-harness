package agent

import (
	"regexp"
	"strings"
)

// tddgate.go —— TDD gate 的纯决策函数（第七十二刀）。
//
// 对账 TS `src/agent/tdd-gate.ts` 的 `evaluateTddGate` / `parseTddGateConfig`。
//
// # 范围声明（**有意不移植的部分**）
//
// TS 的 `tdd-gate.ts` 有三个导出：
//
//	evaluateTddGate   ✅ 本刀移植（纯决策函数，硬拦/建议）
//	parseTddGateConfig ✅ 本刀移植（环境变量解析）
//	checkTddGate      ❌ 不移植——走 `ImmuneContextHint` 通道
//	buildTddGateHint  ❌ 不移植——同上
//
// **为什么不移植后两个**：它们产出 `ImmuneContextHint`（`{level, signalKinds,
// matchedMistakes, suggestion}`），经 `turn-step-producer` 的
// `formatImmuneContext` → `buildCognitiveProjectionParts` 进入 prompt。Go 侧
// **该通道完全不存在**（`signalKinds` / `immune-signal` 在生产代码零出现）——
// 移植它们等于先造一条新提示管道，**超出本刀**。它们的功能是「提示文案」，
// 而 `evaluateTddGate` 的 suggest 分支已覆盖同类价值（且走既有的门链返回）。
//
// # 与 TS 的一处**必须注意的差异**（oracle 当场抓到）
//
// 本文件的 `isTddTestFile` 与 `evidence.go` 的 `testFileRe` 是**不同**正则：
//
//	isTddTestFile（TS `tdd-gate.ts` 的 isTestFile）  `/\.(test|spec)\./` | `__tests__`
//	testFileRe    （TS `evidence.ts` 的 isTestFile）  额外含 `_test.` / `test_`
//
// 后果：`src/foo_test.go` 在 enforce 模式下**会被 block**（不被豁免），
// 因为 TS 的 RED 步骤豁免只认 `.test.`/`.spec.`/`__tests__`。**不要「统一」
// 这两个正则**——那是行为回归（`TestIsTddTestFileRegexDiffersFromEvidence` 钉住）。

// TddGateConfig 是 TDD gate 的配置。
//
// 对账 TS `TddGateConfig`。
type TddGateConfig struct {
	// Enabled 是总开关。false 时 gate 从不 block 或 suggest。
	Enabled bool
	// Mode 是 `"suggest"`（只建议）或 `"enforce"`（达阈值硬拦）。
	Mode string
	// Threshold 是「连续未验证编辑数」达到多少时硬拦（enforce 模式）。
	Threshold int
	// SkipIfNoTests 为 true 时，未读过测试文件则不拦（项目可能无测试设施）。
	SkipIfNoTests bool
}

// DefaultTddGateConfig 是默认配置：启用、**只建议不硬拦**。
//
// 对账 TS `DEFAULT_TDD_GATE_CONFIG`。
//
// **为什么默认 suggest 而非 enforce**（TS 注释的真实事故）：TDD 纪律作为
// **任务起步指引**前置（见 TS 的 `checkTddGate`）比中途硬拦更好——session
// 05e1500e 显示 enforce 在**修复中途**拦截会把 agent 逼进重写循环。硬拦是
// 显式 opt-in（`RIVET_TDD_GATE=enforce`）。
var DefaultTddGateConfig = TddGateConfig{
	Enabled:       true,
	Mode:          "suggest",
	Threshold:     3,
	SkipIfNoTests: true,
}

// TddGateDecision 是决策结果。
//
// 对账 TS `TddGateDecision`。
type TddGateDecision struct {
	// Action 是 `"allow"` / `"suggest"` / `"block"`。
	Action string
	// Message 是给模型的可读指引（suggest/block 时非空）。
	//
	// **逐字对账 TS**——它会进模型的下一轮请求，不是内部日志。
	Message string
}

// editTools 是「会改文件」的工具集合——gate 只干预这些。
//
// 对账 TS `EDIT_TOOLS`。**工具名与 TS 一致**（Go 侧同名工具已存在：
// `file_tools.go` / `hashedit.go` / `applypatch.go`）。
var editTools = map[string]bool{
	"edit_file":   true,
	"write_file":  true,
	"apply_patch": true,
	"hash_edit":   true,
}

// tddTestFileRe 匹配「测试文件」——**仅用于 RED 步骤豁免**。
//
// 对账 TS `tdd-gate.ts` 的 `isTestFile`：
//
//	/\.(test|spec)\./i  ||  /[\\/]__tests__[\\/]/i
//
// **注意**：比 `evidence.go` 的 `testFileRe` **窄**（不含 `_test.`/`test_`）。
// 详见文件头「必须注意的差异」。
var tddTestFileRe = regexp.MustCompile(`(?i)\.(test|spec)\.`)
var tddTestsDirRe = regexp.MustCompile(`(?i)[\\/]__tests__[\\/]`)

// isTddTestFile 报告路径是否是测试文件（RED 步骤豁免用）。
func isTddTestFile(path string) bool {
	return tddTestFileRe.MatchString(path) || tddTestsDirRe.MatchString(path)
}

// EvaluateTddGate 决定一次工具调用应被放行、建议、还是拦截。
//
// 对账 TS `evaluateTddGate`。**纯函数**：无 I/O、无状态、无副作用。
//
// 分支顺序**敏感**（逐条对账 TS，顺序变了语义就变）：
//
//  1. config 关闭 → allow
//  2. 非 edit 工具 → allow
//  3. scratch 路径 → allow（探针纪律豁免）
//  4. 无代码编辑（纯文档）→ allow
//  5. filesModified == 0 → allow
//  6. 已有验证：有失败 → suggest（修测试）；否则 allow
//  7. 达阈值：suggest 模式 → suggest；enforce + 未读测试 + skipIfNoTests →
//     suggest（降级）；enforce + 目标是测试文件 → suggest（RED 豁免）；
//     否则 block
//  8. 未达阈值 → suggest（2 次编辑的探索窗口）
func EvaluateTddGate(gateState tddGateState, toolName string, config TddGateConfig, targetPath string) TddGateDecision {
	// 1. gate 整体关闭 → 从不干预。
	if !config.Enabled {
		return TddGateDecision{Action: "allow"}
	}

	// 2. 只管 edit/write 类工具。读、bash、搜索等一律放行。
	if !editTools[toolName] {
		return TddGateDecision{Action: "allow"}
	}

	// 3. scratch 探针（`.rivet/scratch/`）是行为验证微探针，不是交付物编辑。
	//    拦它们会把 agent 锁在 RED gate 本该鼓励的探针纪律之外——恒放行。
	if targetPath != "" && isScratchPath(targetPath) {
		return TddGateDecision{Action: "allow"}
	}

	// 4. 纯文档编辑（无代码文件被改）→ 放行。gate 的目的是代码的 TDD；
	//    文档、配置、计划文件没有测试可跑。
	if !gateState.HasCodeEdits {
		return TddGateDecision{Action: "allow"}
	}

	// 5. 还没改过文件 → 无可验证，让首次编辑干净通过。
	if gateState.FilesModified == 0 {
		return TddGateDecision{Action: "allow"}
	}

	// 6. 已跑过验证 → 模型在带测试迭代。
	if gateState.Verifications > 0 {
		// 但若测试在失败，提示先修而不是继续堆编辑。
		if gateState.HasFailedTests {
			return TddGateDecision{
				Action:  "suggest",
				Message: tddSuggestFailedMessage(gateState.Verifications),
			}
		}
		return TddGateDecision{Action: "allow"}
	}

	// 从这里起：有文件被改、零验证、且是 edit 工具。应用渐进阈值。
	if gateState.EditsSinceLastTest >= config.Threshold {
		// suggest 模式从不拦截——只建议。
		if config.Mode == "suggest" {
			return TddGateDecision{
				Action:  "suggest",
				Message: tddSuggestMessage(gateState.EditsSinceLastTest),
			}
		}
		// skipIfNoTests：若 agent 没读过任何测试文件，项目可能无测试设施
		//（或这是无测试项目上的快速修复）。降级 block → suggest，
		// 免得 agent 被永久卡住。
		if config.SkipIfNoTests && !gateState.HasReadTestFiles {
			return TddGateDecision{
				Action:  "suggest",
				Message: tddSuggestMessage(gateState.EditsSinceLastTest),
			}
		}
		// 编辑测试文件就是 RED 步骤——永不拦它，否则 agent 无法修复
		// 坏掉的失败测试，会升级成重写循环。
		if targetPath != "" && isTddTestFile(targetPath) {
			return TddGateDecision{
				Action:  "suggest",
				Message: tddTestFileSuggestMessage(gateState.EditsSinceLastTest),
			}
		}
		return TddGateDecision{
			Action:  "block",
			Message: tddBlockMessage(gateState.EditsSinceLastTest),
		}
	}

	// 未达阈值：建议但不拦（2 次编辑的探索窗口）。
	return TddGateDecision{
		Action:  "suggest",
		Message: tddSuggestMessage(gateState.EditsSinceLastTest),
	}
}

// parseTddGateEnv 从原始取值解析配置。
//
// **为什么不经 `os.Getenv` 直接读**（对账 TS 的 `parseTddGateConfig` 有偏离）：
// TS 在会话构造时调一次 `parseTddGateConfig()`（内部读 `process.env`）并持有。
// Go 侧改为**装配层读环境、内核收原始值**——与 `TerseEnv` / `Permissions`
// 同一注入模式。**理由是可测性与变异可见性**：若内核自己读 env，那么
// 「装配层漏填」这个缺陷无法被测出（e2e 设了子进程 env 后内核仍能生效，
// 变异反证会红 0 处——第七十二刀初版实测踩到）。
//
// **语义与 TS 逐值等价**：同一原始取值 → 同一配置（见 `tddgate_oracle_test.go`
// 的解析矩阵）。
func parseTddGateEnv(raw string) TddGateConfig {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "off" || raw == "0" || raw == "false" || raw == "disabled" {
		c := DefaultTddGateConfig
		c.Enabled = false
		return c
	}
	if raw == "enforce" || raw == "on" || raw == "1" || raw == "true" {
		c := DefaultTddGateConfig
		c.Mode = "enforce"
		return c
	}
	// "suggest" / "advisory" / 未设 / 未知 → suggest（默认）。
	c := DefaultTddGateConfig
	c.Mode = "suggest"
	return c
}

// ---------------------------------------------------------------------------
// 文案（**逐字对账 TS**——会进模型的下一轮请求）
// ---------------------------------------------------------------------------

func tddBlockMessage(edits int) string {
	return "TDD Gate: " + itoa(edits) + " edits without a test run. Write a failing test first (run_tests or bash test command should fail = RED), then edit. Run tests (run_tests tool or bash: npm test / pytest / etc.) to clear this gate."
}

func tddSuggestMessage(edits int) string {
	return "TDD discipline: " + itoa(edits) + " edit(s) made, 0 verifications. Consider running tests before more edits."
}

func tddSuggestFailedMessage(count int) string {
	return "TDD discipline: " + itoa(count) + " verification(s) failed. Fix the failing tests before continuing to edit."
}

func tddTestFileSuggestMessage(edits int) string {
	return "TDD discipline: " + itoa(edits) + " edit(s) without a test run, but this edit targets a test file — writing/fixing the test is the RED step, so it is allowed. Run the test after this edit to verify it fails (RED) or passes (GREEN)."
}
