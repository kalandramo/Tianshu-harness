// wedge-loop guard：检测「同一工具批次反复全错」并终止 run。
//
// 对账 TS 的 wedge-loop guard（src/agent/turn-orchestrator.ts:1044-1073）。
//
// ## 为什么需要（TS 源码原文）
//
//	Wedged-loop guard: a model that re-emits the SAME tool batch and gets
//	an all-error result every time (the classic "requires user approval"
//	denial loop — see the boundary-stall screenshot) would otherwise spin
//	to maxTurns, ballooning context until the sidecar OOMs. Detect an
//	identical, fully-errored batch repeating and end the run instead.
//
// ## 与 `maxTurns` 的关系（**不是替代**）
//
// Go 侧此前只有 `maxTurns`（50 轮硬上限）。那是**计数**上限——模型陷入
// 「同批次反复被拒」时仍会烧满 50 轮，每轮都往历史里灌一份完整工具结果，
// 上下文线性膨胀。本守卫是**语义**检测：识别「同一批次反复全错」这一
// 特定形态，提前终止。
//
// ## 触发条件（两个都必须成立）
//
//  1. `allErrored`：本轮有工具调用，且**全部**失败
//  2. `batchFingerprint` 与上一轮**完全相同**
//
// 连续满足 `maxWedgeRepeats` 次 → 终止。
package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// maxWedgeRepeats 是触发终止的连续重复次数。
//
// 对账 TS 的 `MAX_WEDGE_REPEATS = 3`（turn-orchestrator.ts:359）。
const maxWedgeRepeats = 3

// toolBatchFingerprint 生成工具批次的**保序指纹**（name + input）。
//
// 对账 TS 的 `toolBatchFingerprint`：
//
//	JSON.stringify(toolUses.map(tu => [tu.name, tu.input]))
//
// 两个批次若工具与参数相同则产出同一字符串，故「模型重新发出相同的失败调用」
// 可跨轮检测。
//
// **与 TS 的差异（关键）**：TS 的 `JSON.stringify` 对对象的键序**敏感**
// （按插入序序列化）。Go 的 `encoding/json` 对 `map[string]any` 会**排序键**
// ——两者对同一输入产出的字符串不同，但**跨轮比较的用途等价**：只要同一
// 批次的两次调用走同一序列化路径，指纹就相同。故此处用标准 json.Marshal
// （排序键）而非复刻 JS 插入序——**这是有意的等价简化**，不影响守卫语义。
//
// 序列化失败（不可编码的值）时退回「名字拼接」——对账 TS 的 catch 分支。
func toolBatchFingerprint(calls []toolCall) string {
	type pair struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	pairs := make([]pair, 0, len(calls))
	for _, c := range calls {
		pairs = append(pairs, pair{Name: c.name, Input: c.input})
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		names := make([]string, 0, len(calls))
		for _, c := range calls {
			names = append(names, c.name)
		}
		return strings.Join(names, "|")
	}
	return string(b)
}

// wedgeState 是跨轮的 wedge 检测状态。
//
// 对账 TS 的 `deps.state.wedgeToolFingerprint` / `wedgeRepeatCount`。
type wedgeState struct {
	fingerprint string
	repeatCount int
}

// observeBatch 用本轮结果更新 wedge 状态，返回**是否应终止**。
//
// 对账 TS turn-orchestrator.ts:1050-1056：
//
//	allErrored := r.toolCount > 0 && r.errorCount === r.toolCount
//	fp := allErrored ? toolBatchFingerprint(toolUses) : ''
//	if allErrored && fp === state.wedgeToolFingerprint {
//	    state.wedgeRepeatCount++
//	} else {
//	    state.wedgeRepeatCount = allErrored ? 1 : 0
//	    state.wedgeToolFingerprint = fp
//	}
//
// **注意两处易错点**：
//   - 非全错时 `fp` 为空串——空串**不参与**比较（否则「连续两轮空」会误判）
//   - `else` 分支里 `repeatCount` 置 `allErrored ? 1 : 0`——**不是**恒置 0
//     （全错但指纹不同 → 重置为新序列的第 1 次）
func (s *wedgeState) observeBatch(allErrored bool, fingerprint string) {
	if allErrored && fingerprint == s.fingerprint {
		s.repeatCount++
		return
	}
	s.repeatCount = 0
	if allErrored {
		s.repeatCount = 1
	}
	s.fingerprint = fingerprint
}

// shouldTerminate 报告是否已达终止阈值。
func (s *wedgeState) shouldTerminate() bool {
	return s.repeatCount >= maxWedgeRepeats
}

// wedgeDetail 渲染终止详情（对账 TS 的 `detail` 字段）。
//
//	`${toolUses.map(tu => tu.name).join(',')} ×${repeatCount}`
func (s *wedgeState) wedgeDetail(calls []toolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.name)
	}
	return strings.Join(names, ",") + " ×" + strconv.Itoa(s.repeatCount)
}
