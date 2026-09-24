// salience.go —— 动态 appendix 的块显著性评分与 Top-K 选择。
//
// 对账 TS `src/prompt/volatile.ts` 的 `assignSalience`(944) 与
// `selectTopKBlocks`(996)。
//
// **为什么这两个是附录家族的切入点**：TS 侧 volatile 层分两半——
//   - **stable 块**（frozen 前缀）：`buildStableVolatileBlock`，Go 侧已移植
//     且有生产消费路径（`full.go:131` ← `BuildFullSystemPrompt`）
//   - **动态 appendix**：`buildDynamicAppendixParts` → `buildDynamicAppendix`
//     → `buildLatestTurnVolatileBlock`，Go 侧**零移植**
//
// 本文件是动态 appendix 的**地基**：`buildDynamicAppendixParts` 内部调
// `selectTopKBlocks` 做预算裁剪，而它又依赖 `assignSalience` 给每块打分。
// 两者都是**纯函数**（零 IO、零会话状态），故可先行闭合——不受
// 「volatile 依赖会话状态」这一前提的约束（那是对 `buildDynamicAppendixParts`
// 说的，见 HANDOFF 的说明）。
//
// **缓存命中率相关性**：Top-K 决定哪些块进 appendix、哪些被预算丢弃。
// 输出字节直接影响尾部增量 → 影响前缀缓存。故**排序稳定性**是硬语义
// （见 SelectTopKBlocks 的注释）。
package prompt

import (
	"sort"
	"strings"
)

// SalientBlock 是带显著性评分的 appendix 子块。
//
// 对账 TS `SalientBlock`（volatile.ts:875-879）：
//
//	export interface SalientBlock {
//	  content: string
//	  salience: number
//	  source?: CvmInjectionSource
//	}
//
// **`Source` 为什么是 string 而非枚举**：TS 的 `CvmInjectionSource` 是
// 7 个字面量的联合类型（`projection` / `ephemeral` / `tool-context` /
// `advisory-appendix` / `system-reminder` / `runtime-payload` /
// `control-appendix`，见 `pressure-monitor.ts:50-58`）。Go 侧用 string
// 承载——原因有二：① 该值只用于**元数据透传**（TS 注释：「caller-attached
// metadata such as source survives selection」），Go 侧不消费其取值；
// ② 用 string 避免与 `internal/context` 包产生循环依赖（压力监控尚未移植）。
// 空串 = 无 source（对账 TS 的 undefined）。
type SalientBlock struct {
	Content  string
	Salience float64
	// Source 是块的注入来源（透传，不参与判定）。空串 = 无。
	Source string
}

// salienceRule 是一条前缀 → 评分规则。
//
// **顺序即语义**：TS 用一串 `if (startsWith(...)) return X` 实现，先匹配者
// 胜出。当前规则集的前缀互不重叠，但**保持原顺序**是安全的前提——若未来
// TS 加入重叠前缀，顺序就决定了结果。
type salienceRule struct {
	prefix string
	score  float64
}

// salienceRules 对账 TS `assignSalience`（volatile.ts:944-982）的完整分支表。
//
// **来源**：从 TS 源码逐行提取（`awk` 抽取 `startsWith` + `return` 行），
// 非手抄。顺序与 TS 完全一致。
//
// 评分档位含义（对账 TS 注释 volatile.ts:883-891）：
//
//	1.0  identity-critical（star-domain）
//	0.95 治理整个 planning turn（plan-mode / ask-mode）——预算压力下绝不丢弃
//	0.8  直接可行动（repair-hint / historical-lessons / mentions / progress /
//	     active-plan / 星域-advisory）
//	0.75 output-style——极小但治理整个回复的散文风格
//	0.7  任务相关（intent-retrieval-route / task-progress / decisions /
//	     git-status / recent-commits——git 状态是任务地基：被 Top-K 丢弃会
//	     诱发模型用 bash 重新获取，形成 doom-loop）
//	0.6  skill 发现层（模型可按需用 skill 工具重新列出）
//	0.5  默认
//	0.4  session housekeeping（session-state / cross-session）
//	0.3  去重提示（read-file-dedup-hint）
var salienceRules = []salienceRule{
	// ── 1.0：identity-critical。TS 注释标为 "Dead path"——star-domain 现已
	// 折叠进 frozen 前缀，不再走 appendix。保留以对齐 assignSalience 的
	// 测试契约与未来的 appendix 级域渲染。
	{"<star-domain", 1.0},

	// ── 0.95：治理整个 turn
	{"<plan-mode>", 0.95},
	{"<ask-mode>", 0.95},

	// ── 0.8：直接可行动
	{"<repair-hint>", 0.8},
	{"<星域-advisory>", 0.8},
	{"<historical-lessons>", 0.8},
	// 用户显式 @-引用的文件/路径——直接意图信号，必须在预算压力下存活
	// （TS 注释：was silently defaulting to 0.5）
	{"<mentions>", 0.8},
	{"<task-depth", 0.7},
	{"<plan-methodology", 0.7},

	// ── 0.6：skill 发现层
	{"<available-skills", 0.6},

	// ── 0.7：任务相关
	{"<tool-context>", 0.7},
	{"<plan-cache-advisory>", 0.7},
	// U6：plan trace 是任务基线/进度——显式评分以免预算压力丢弃
	{"<plan-execution-trace", 0.7},
	// active-plan 指针是执行关键——预算压力下绝不丢弃
	{"<active-plan", 0.8},
	{"<intent-retrieval-route", 0.7},
	// progress 有两种形态：`<progress>`（无属性）与 `<progress ...`（带属性）
	{"<progress>", 0.8},
	{"<progress ", 0.8},
	{"<task-progress", 0.7},
	{"<decisions>", 0.7},
	{"<worktree-warning", 0.7},
	{"<git-status>", 0.7},
	{"<recent-commits>", 0.7},

	// ── 0.4：session housekeeping
	{"<session-state>", 0.4}, // legacy fallback
	{"<cross-session", 0.4},

	// ── 0.3：去重提示
	{"<read-file-dedup-hint>", 0.3},

	// ── 0.75：output-style（顺序在 TS 侧靠后，保持原样）
	{"<output-style>", 0.75},
}

// defaultSalience 对账 TS `return 0.5`（volatile.ts:982）。
const defaultSalience = 0.5

// AssignSalience 给一个 appendix 子块内容打显著性分。
//
// 对账 TS `assignSalience`（volatile.ts:944）：按**前缀**匹配，返回首个命中
// 规则的分数；无命中返回 0.5。
//
// **startsWith 语义**（非 Contains）：`<git-status>x` 命中 `<git-status>`，
// 而 `  <git-status>x`（前导空白）**不**命中——TS 不 trim。测试用例
// `  <git-status>` 钉住这一点。
func AssignSalience(blockContent string) float64 {
	for _, r := range salienceRules {
		if strings.HasPrefix(blockContent, r.prefix) {
			return r.score
		}
	}
	return defaultSalience
}

// truncatedMarker 对账 TS 的 `'\n[truncated]'`（volatile.ts:1003）。
const truncatedMarker = "\n[truncated]"

// SelectTopKBlocks 按显著性降序选块，直到字符预算耗尽。
//
// 对账 TS `selectTopKBlocks`（volatile.ts:996-1017）：
//
//	const sorted = [...blocks].sort((a, b) => b.salience - a.salience)
//	const selected: T[] = []
//	let used = 0
//	const blockCap = Math.max(Math.floor(maxChars * 0.4), 2_000)
//
//	for (const block of sorted) {
//	  const content = block.content.length > blockCap
//	    ? block.content.slice(0, blockCap) + '\n[truncated]'
//	    : block.content
//	  const overhead = selected.length > 0 ? 2 : 0
//	  if (used + overhead + content.length > maxChars && selected.length > 0) {
//	    continue
//	  }
//	  selected.push(content === block.content ? block : { ...block, content })
//	  used += overhead + content.length
//	}
//	return selected
//
// **★ 排序稳定性（本函数最关键的语义）**：TS 的 `Array.prototype.sort` 在
// V8 是**稳定排序**——同 salience 的块保持输入顺序。Go 的 `sort.Slice`
// **不稳定**（pdqsort），同 salience 块的相对顺序会变 → appendix 字节变化
// → 打断前缀缓存（本刀的目标正是缓存命中率）。
//
// 故**必须**用 `sort.SliceStable`。测试
// `TestSelectTopKBlocksStability/★同salience大批量`（50 个同分块）钉住它。
//
// **两条易错语义**：
//   - **超预算是 `continue` 而非 `break`**：中间块放不下时，后面的小块
//     若放得下仍应入选。测试 `★超预算是continue非break` 钉住。
//   - **至少保留一个块**：`selected.length > 0` 短路——预算为 0 时仍保留
//     最高 salience 的块。测试 `★至少保留一个块` 钉住。
//
// **overhead = 2**：第 2 个块起每个多算 2 字符（块间 `"\n\n"` 分隔符）。
func SelectTopKBlocks(blocks []SalientBlock, maxChars int) []SalientBlock {
	if len(blocks) == 0 {
		return nil
	}

	// ★ SliceStable —— 不可换成 sort.Slice（见函数注释）
	sorted := make([]SalientBlock, len(blocks))
	copy(sorted, blocks)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Salience > sorted[j].Salience
	})

	selected := make([]SalientBlock, 0, len(sorted))
	used := 0

	blockCap := maxChars * 4 / 10 // floor(maxChars * 0.4)
	if blockCap < 2000 {
		blockCap = 2000
	}

	for _, block := range sorted {
		content := block.Content
		if len(content) > blockCap {
			content = content[:blockCap] + truncatedMarker
		}

		overhead := 0
		if len(selected) > 0 {
			overhead = 2
		}

		// 至少保留一个块：selected 非空时才允许因预算跳过
		if used+overhead+len(content) > maxChars && len(selected) > 0 {
			continue
		}

		out := block
		out.Content = content
		selected = append(selected, out)
		used += overhead + len(content)
	}

	return selected
}
