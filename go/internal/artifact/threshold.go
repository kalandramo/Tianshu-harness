package artifact

// threshold.go —— artifact 包装阈值。
//
// 对账两处 TS：
//   - `src/compact/constants.ts` 的 `pruneThresholds(contextWindow)`
//   - `src/tools/artifact-threshold.ts` 的 `getToolArtifactThreshold(...)`
//
// # 为什么放在 artifact 包内（而非 compact）
//
// TS 里 `artifact-threshold.ts` 从 `compact/constants.js` 导入 `pruneThresholds`。
// Go 侧若照搬会产生 `artifact → compact` 依赖，而 `compact` 的压缩路径将来
// 可能要用 artifact（存归档），会形成环。故此处**自包含**复刻阈值阶梯——
// 阶梯本身是纯常量映射，无逻辑耦合。
//
// **若 compact 也需要 `pruneThresholds`**：应把它提到一个无依赖的叶子包
// （如 `internal/compact` 内的独立文件），两边各自引用，而不是让 artifact
// 反向依赖 compact。

// PruneThresholds 是对账 TS `PruneThresholds` 的窗口感知阈值。
type PruneThresholds struct {
	// ProtectRecent 是免于清理的最近消息数。
	ProtectRecent int
	// MinChars 是值得清理的最小内容长度（更短的省不了多少）。
	MinChars int
}

// 小窗口（<200K）的遗留默认值，对账 TS 的两个常量。
const (
	pruneProtectRecentMessages = 8
	pruneMinContentChars       = 1200
)

// PruneThresholdsFor 返回窗口感知的 prune 阈值。
//
// 对账 TS `pruneThresholds`。**阶梯**：
//
//	>= 500_000 → {60, 150_000}
//	>= 200_000 → {30,  40_000}
//	否则        → {8,    1_200}（遗留激进值——小窗口确实需要早清理）
//
// # 为什么大窗口要放大阈值（TS 注释的理由）
//
// 遗留的 8 条 / 1.2KB 默认值来自 64K 窗口时代。在 1M 窗口上沿用会让 prune
// 在**没有真实压力时**就动手——删掉模型刚读进来的工作记忆（一次 read_file），
// 模型只好退回「拆成临时文件」的变通做法（那是为截断上下文训练出来的）。
//
// 150K ≈ 1M 窗口的 4%（字符口径）；典型大源文件（loop.ts ~62K、README ~30K）
// 整个会话都不被动。**同一阈值也给 read_file/bash/grep 的 artifact 包装把关**
// ——低于此尺寸的内容直接返回纯文本，不产生 artifact 引用。
func PruneThresholdsFor(contextWindow int) PruneThresholds {
	if contextWindow >= 500_000 {
		return PruneThresholds{ProtectRecent: 60, MinChars: 150_000}
	}
	if contextWindow >= 200_000 {
		return PruneThresholds{ProtectRecent: 30, MinChars: 40_000}
	}
	return PruneThresholds{ProtectRecent: pruneProtectRecentMessages, MinChars: pruneMinContentChars}
}

// toolArtifactMultipliers 是每工具在基础阈值上的系数。
//
// 对账 TS `TOOL_ARTIFACT_MULTIPLIERS`。**>1 表示阈值更高 → 更多内容留在
// 行内**；<1 表示更低 → 更早包装成 artifact。
//
// # 为什么需要分工具系数（TS 注释的理由）
//
// 各工具的输出特征不同：read_file 是大代码文件、grep 是紧凑搜索结果、
// bash 输出差异极大。单一全局阈值要么**过度包装**（给紧凑输出也造引用），
// 要么**包装不足**（大输出撑爆历史）。
var toolArtifactMultipliers = map[string]float64{
	"read_file":       2.0,  // 1M 窗口上 300K（B1 的 120K 上限更紧）
	"bash":            1.0,  // ~150K on 1M（与 prune minChars 一致）
	"grep":            0.67, // 搜索结果紧凑，~100K 就包
	"run_tests":       1.5,  // ~225K on 1M
	"diff":            1.0,  // ~150K on 1M
	"glob":            0.5,  // 文件清单很紧凑
	"repo_map":        0.5,  // 文件树紧凑
	"inspect_project": 0.5,  // 项目摘要紧凑
	"web_fetch":       1.0,  // 网页内容差异大，默认
	"read_section":    2.0,  // 与 read_file 同
}

// defaultArtifactMultiplier 对账 TS 的 `DEFAULT_MULTIPLIER`。
const defaultArtifactMultiplier = 1.0

// legacyArtifactThreshold 是窗口未知时的遗留默认值。
//
// 对账 TS 的 `: 800`——窗口未知时用固定 800 字符。
const legacyArtifactThreshold = 800

// ToolArtifactThreshold 返回某工具的 artifact 包装阈值（字符数）。
//
// 对账 TS `getToolArtifactThreshold`：基础值 × 工具系数，四舍五入。
// 窗口未知（<=0）时用遗留默认值 800 作基础。
func ToolArtifactThreshold(toolName string, contextWindow int) int {
	base := legacyArtifactThreshold
	if contextWindow > 0 {
		base = PruneThresholdsFor(contextWindow).MinChars
	}
	mul := defaultArtifactMultiplier
	if m, ok := toolArtifactMultipliers[toolName]; ok {
		mul = m
	}
	// 对账 TS 的 Math.round：正数下 +0.5 取整等价。
	return int(float64(base)*mul + 0.5)
}
