package tools

import "testing"

// writeapproval_test.go —— 写工具的 `RequiresApproval` 对账 TS（第七十四刀）。
//
// # 缺口（`.rivet/HANDOFF.md` 的「下一步」第 2 条，文档称「前置③」）
//
// TS 侧 4 个写工具的 `requiresApproval` 是**恒真**：
//
//	src/tools/write-file.ts:349   requiresApproval: () => true
//	src/tools/edit.ts:390         requiresApproval: () => true
//
// 而 Go 侧是 `p.ApprovalMode != "dangerously-skip-permissions"`——**档位驱动**。
//
// # 为什么「行为当前正确」但仍是隐患
//
// `decideApprovalGate`（`agent/approval_gate.go`）**开头**已处理 skip 档：
//
//	if p.ApprovalMode == "dangerously-skip-permissions" {
//		return approvalGateDecision{Block: false}
//	}
//
// 且 `auto-safe` 分支读的是 `isHighRisk`（**不读** `needsApproval`）。
// 故 `RequiresApproval` 的返回值**只在 manual 档被消费**——那里
// `ApprovalMode != skip` 恒为 true，与 TS 的 `() => true` **等价**。
//
// **隐患**：档位语义被**两处**判定（`RequiresApproval` 与 `decideApprovalGate`）。
// 将来任一处改动会导致不一致——这是**结构性**问题，不是当前缺陷。
//
// # 本刀的判据（为什么不能只断言「行为不变」）
//
// 「行为等价」在修前修后都成立——那样的测试**恒绿**，没有判别力。
// 故断言**实现形态**：`RequiresApproval` 必须在**所有档位**下返回 true
// （包括 skip 档）。若它仍读档位，skip 档下会返回 false → 测试红。

// writeToolCases 是 4 个写工具的构造（需 cwd，用空串——本测试不执行工具）。
func writeToolCases() map[string]Tool {
	return map[string]Tool{
		"write_file":  WriteFile("", nil),
		"edit_file":   EditFile("", nil),
		"hash_edit":   HashEdit("", nil),
		"apply_patch": ApplyPatch("", nil),
	}
}

// TestWriteToolsRequiresApprovalIsAlwaysTrue —— **核心**：4 个写工具在
// **任何档位**下 `RequiresApproval` 都返回 true（对账 TS 的 `() => true`）。
//
// 若实现仍是 `ApprovalMode != skip`，skip 档那格会红。
func TestWriteToolsRequiresApprovalIsAlwaysTrue(t *testing.T) {
	modes := []string{
		"manual",
		"auto-safe",
		"dangerously-skip-permissions",
		"",        // 未设
		"unknown", // 未知档位
	}
	for name, tool := range writeToolCases() {
		for _, mode := range modes {
			p := &CallParams{ApprovalMode: mode}
			if !tool.RequiresApproval(p) {
				t.Errorf("%s 在档位 %q 下 RequiresApproval 应恒为 true（对账 TS `() => true`）",
					name, mode)
			}
		}
	}
}

// TestWriteToolsRequiresApprovalIgnoresCallParams —— **实现形态断言**：
// 返回值不得依赖 `CallParams` 的任何字段。
//
// 这是「消除重复判定」的直接检验——若将来有人重新引入档位依赖，本测试红。
func TestWriteToolsRequiresApprovalIgnoresCallParams(t *testing.T) {
	for name, tool := range writeToolCases() {
		var nilParams *CallParams
		// nil params 不应 panic（恒真实现不读 p）。
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s 的 RequiresApproval(nil) panic——恒真实现不该读 p：%v", name, r)
				}
			}()
			if !tool.RequiresApproval(nilParams) {
				t.Errorf("%s 的 RequiresApproval(nil) 应返回 true", name)
			}
		}()
	}
}

// TestReadToolsRequiresApprovalIsFalse —— **反面对照**：只读工具恒为 false。
//
// 防止「统一改成恒真」的过度修正——读工具不该要求批准。
func TestReadToolsRequiresApprovalIsFalse(t *testing.T) {
	p := &CallParams{ApprovalMode: "manual"}
	cases := map[string]Tool{
		"read_file": ReadFile("", nil),
		"grep":      Grep(""),
		"glob":      Glob(""),
	}
	for name, tool := range cases {
		if tool.RequiresApproval(p) {
			t.Errorf("%s 是只读工具，manual 档下不该要求批准", name)
		}
	}
}
