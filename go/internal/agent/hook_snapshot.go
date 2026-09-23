package agent

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
	"github.com/kalandramo/tianshu/go/internal/skills"
)

// hookSnapshotState 是跨轮累积的 hook 快照状态。
//
// **为什么单独存**：部分快照字段是**任务级**（整个会话）而非**窗口级**
// （近 N 条工具历史）。TS 侧的 RuntimeHookSnapshot 注释明确区分：
//
//	Component C (typecheck-reminder): a .ts/.tsx file was written this session.
//	Task-level, not windowed — survives a long turn where the edit scrolled out
//	of recentToolHistory.
//
// 若这些字段按窗口计算，长回合里编辑滚出窗口后提醒就失效了——这正是
// TS 注释强调的坑。Go 侧在此显式累积。
type hookSnapshotState struct {
	// touchedTSFiles：本会话写过 .ts/.tsx。
	touchedTSFiles bool
	// sawTypecheck：自上次 TS 编辑后跑过真 typecheck。
	sawTypecheck bool
	// touchedUIFiles：本会话写过 UI 文件（.tsx/.jsx/.vue/.svelte/.css/.html）。
	touchedUIFiles bool
	// sawVisualVerify：本会话用过视觉验证工具。
	sawVisualVerify bool
	// recentToolHistory 是**窗口**（近 N 条），非任务级。
	recentToolHistory []ToolHistoryEntry
}

// toolHistoryWindow 是 recentToolHistory 的保留条数。
//
// 对账 TS：窗口是 5 条（注释多处提及 "5-entry window"）。
const toolHistoryWindow = 5

// tsExtensions 是需要 typecheck 的扩展名。
var tsExtensions = map[string]bool{".ts": true, ".tsx": true}

// uiExtensions 是需要渲染验证的扩展名。
var uiExtensions = map[string]bool{
	".tsx": true, ".jsx": true, ".vue": true, ".svelte": true,
	".css": true, ".html": true,
}

// visualVerifyTools 是视觉验证类工具。
var visualVerifyTools = map[string]bool{
	"browser_debug": true, "computer_use": true, "browser": true,
}

// typecheckTools 是真正的类型检查工具。
var typecheckTools = map[string]bool{"typecheck": true}

// recordToolForHooks 把一次工具调用记进 hook 快照状态。
//
// **任务级标志只在成功时更新**——失败的工具调用不该让"写过 TS 文件"成立
// （写失败的文件没进磁盘，无需 typecheck 提醒）。
func (l *Loop) recordToolForHooks(tool *RuntimeToolEvent) {
	if tool == nil {
		return
	}

	// 窗口：无条件推进（含失败——窗口反映"刚发生了什么"）
	l.hookState.recentToolHistory = append(l.hookState.recentToolHistory, ToolHistoryEntry{
		Tool:   tool.Name,
		Status: statusOf(tool.Success),
		Target: tool.Target,
	})
	if len(l.hookState.recentToolHistory) > toolHistoryWindow {
		l.hookState.recentToolHistory =
			l.hookState.recentToolHistory[len(l.hookState.recentToolHistory)-toolHistoryWindow:]
	}

	// 任务级：只在成功时置位
	if !tool.Success {
		return
	}

	switch tool.Name {
	case "write_file", "edit_file", "hash_edit", "apply_patch":
		ext := strings.ToLower(filepath.Ext(tool.Target))
		if tsExtensions[ext] {
			l.hookState.touchedTSFiles = true
			// **写 TS 文件会重置 typecheck 标志**——自那次编辑后还没跑过。
			l.hookState.sawTypecheck = false
		}
		if uiExtensions[ext] {
			l.hookState.touchedUIFiles = true
			l.hookState.sawVisualVerify = false
		}
	case "run_tests":
		// run_tests **不算** typecheck——这是整个 hook 存在的理由：
		// 测试运行器只转译不查类型。
	default:
		if typecheckTools[tool.Name] {
			l.hookState.sawTypecheck = true
		}
		if visualVerifyTools[tool.Name] {
			l.hookState.sawVisualVerify = true
		}
	}
}

// statusOf 把成功标志转成状态串。
func statusOf(success bool) string {
	if success {
		return "ok"
	}
	return "error"
}

// toolTarget 从工具入参提取展示用的 target。
//
// 对账 TS 侧：target 来自工具结果的 `input.target`（工具自己产出）。
// **Go 侧 contract.Result 没有 target 概念**（只有 RawPath = 原始输出文件路径，
// 语义不同），故这里从入参推导——这是最小可用实现，不是精确对账。
//
// **字段名必须是 `file_path`**：工具 schema 用的是 `file_path`（见
// internal/tools 的 schema 对账）。此处曾用 `path` 是个已知的同类缺陷形态
// （read_file 的 schema 修正过），故显式列出并加注释。
func toolTarget(input map[string]any) string {
	if input == nil {
		return ""
	}
	// 文件类工具：file_path
	if p, ok := input["file_path"].(string); ok && p != "" {
		return p
	}
	// bash：command（作为 target 便于「同一命令重复 3 次」检测）
	if c, ok := input["command"].(string); ok && c != "" {
		return c
	}
	// apply_patch：diff（真实 target 需解析 diff，此处留空——由
	// consistency-check 的路径后缀匹配兜住）
	return ""
}

// buildRuntimeSnapshot 构建 hook 快照。
//
// 对账 TS 的 buildRuntimeSnapshot。**当前只填 Go 侧已有数据的字段**——
// sensorium / strategy / vigor / season 等认知状态依赖尚未移植的模块，
// 留待后续（nil 时 hook 应自行跳过相关判断）。
func (l *Loop) buildRuntimeSnapshot(turn int) *RuntimeHookSnapshot {
	return &RuntimeHookSnapshot{
		Cwd:               l.cfg.Cwd,
		Turn:              turn,
		RecentToolHistory: append([]ToolHistoryEntry(nil), l.hookState.recentToolHistory...),
		TouchedTSFiles:    l.hookState.touchedTSFiles,
		SawTypecheck:      l.hookState.sawTypecheck,
		TouchedUIFiles:    l.hookState.touchedUIFiles,
		SawVisualVerify:   l.hookState.sawVisualVerify,
	}
}

// runHookPhase 执行某个 hook 阶段（Hooks 为 nil 时 no-op）。
func (l *Loop) runHookPhase(ctx context.Context, phase RuntimeHookPhase, turn int, tool *RuntimeToolEvent) {
	if l.Hooks == nil {
		return
	}
	hctx := &RuntimeHookContext{
		Snapshot: l.buildRuntimeSnapshot(turn),
		Effects:  l.Effects,
	}
	switch phase {
	case PhasePreTurn:
		l.Hooks.RunPreTurn(ctx, hctx)
	case PhaseAfterPerception:
		l.Hooks.RunAfterPerception(ctx, hctx)
	case PhasePostTool:
		l.Hooks.RunPostTool(ctx, hctx, tool)
	case PhasePostTurn:
		l.Hooks.RunPostTurn(ctx, hctx)
	case PhasePostSession:
		l.Hooks.RunPostSession(ctx, hctx)
	}
}

// systemReminderOpen / Close 是注入消息的标记（对账 src/prompt/system-reminder.ts）。
//
// **为什么需要**：hook 注入的引导以 `role:user` 消息送达。没有标记时，每次
// 注入看起来都像真实的用户边界——触发 prompt engine 重建 appendix + 换
// volatileBlock，在任务中途打爆前缀缓存（TS 注释引 cache-log #10/#12/#35）。
//
// 约定：每条注入消息都用 `<system-reminder>` 包裹。prompt engine 对这类消息
// 原样透传（不做尾部合并、不做边界检测）；会话持久化也把它们排除在轮次计数
// 与历史回放之外。
const (
	systemReminderOpen  = "<system-reminder>"
	systemReminderClose = "</system-reminder>"
)

// wrapSystemReminder 包裹注入文本（幂等）。
//
// 对账 wrapSystemReminder：已带前缀则原样返回。
func wrapSystemReminder(text string) string {
	if strings.HasPrefix(text, systemReminderOpen) {
		return text
	}
	return systemReminderOpen + "\n" + text + "\n" + systemReminderClose
}

// buildRequestMessages 构建本轮发给模型的消息列表。
//
// **核心职责**：在持久化的 `l.messages` 之上叠加**本轮专属**的注入块
// （advisory / skill 发现层），而**不写回** `l.messages`。
//
// **为什么请求级而非持久化**：
//   - TTL=1 的 advisory 自然只出现在一轮（下轮 render 时 bus 已清空）
//   - 缓存安全：不改写历史，只在尾部追加（对账 TS 的 append-only 细断点通道）
//
// 对账 TS 的 `promptEngine.setHarnessAdvisoryBlock(advisoryBus.render(...))`
// 与 `setSkillAdvisoryBlock(skillRegistry.renderDiscoveryBlock(...))`
// （turn-step-producer.ts:456,654）——**Go 侧的最小实现**：把各块作为
// `<system-reminder>` 包裹的 user 消息追加在尾部。
//
// **与 TS 的差异（有意）**：TS 把这些块注入 system prompt 的 **dynamic
// appendix 区**（`buildDynamicAppendixParts`），Go 无该机制，故走尾部 user
// 消息。两者**缓存语义等价**——都是「不改写历史、只在尾部追加、不破前缀」。
// 详见 HANDOFF 第三十九/四十刀。
func (l *Loop) buildRequestMessages() []*wire.OrderedMap {
	blocks := l.collectInjectionBlocks()
	if len(blocks) == 0 {
		return l.messages
	}

	out := make([]*wire.OrderedMap, 0, len(l.messages)+len(blocks))
	out = append(out, l.messages...)
	for _, b := range blocks {
		out = append(out, wire.NewOrderedMap().
			Set("role", "user").
			Set("content", wrapSystemReminder(b)))
	}
	return out
}

// collectInjectionBlocks 收集本轮要注入的块（按稳定顺序）。
//
// **为什么拆出来**：原实现在 `l.Advisories == nil` 时直接 `return l.messages`
// ——那是**早退吞注入**的缺陷：一旦新增第二个注入源（本刀的 skill 发现层），
// 未装 advisory bus 的会话就永远拿不到它。拆成「各源独立贡献 → 统一拼接」
// 后，任一源缺席不影响其他源。
//
// **顺序**：advisory 在前、skill 发现层在后。两者都是尾部追加，顺序只影响
// 字节位置（不影响语义）——固定顺序保证**同输入同输出**（缓存可预测）。
func (l *Loop) collectInjectionBlocks() []string {
	var blocks []string
	if b := l.renderAdvisoryBlock(); b != "" {
		blocks = append(blocks, b)
	}
	if b := l.renderSkillDiscoveryBlock(); b != "" {
		blocks = append(blocks, b)
	}
	return blocks
}

// renderAdvisoryBlock 渲染本轮 advisory 块（无 bus 时返回空串）。
//
// **副作用（必须无条件执行）**：`DrainDelivered` + `Readback.Track` 在
// `Render` 之后**无条件**调用——见下方注释。
func (l *Loop) renderAdvisoryBlock() string {
	if l.Advisories == nil {
		return ""
	}

	block := l.Advisories.Render(l.cfg.StarDomain, 0)

	// 送达快照交给 readback（核销闭环的起点）。
	//
	// **必须无条件执行**——不能在 `block == ""` 时提前 return。
	// 对账 TS turn-step-producer.ts:690：`drainDelivered()` + `track()` 在
	// `render()` 之后**无条件**调用。
	//
	// **为什么关键**：holdout 把条目扣留时 block 为空，但那些 shadow 条目
	// **照常进 delivered**（核销闭环）。若此处提前 return，shadow 样本全部丢失
	// → GetMatureLift 恒 nil → 负 lift 静音永不触发。这是既有缺陷，勿回退。
	//
	// **必须 drain**——不 drain 会让 delivered 无限累积。drain 出的快照同时
	// 喂给 Track（送达跟踪）与未来的 control adapter（TS 的控制面 tee 模式：
	// 单次 drain → 不可变快照 → 多路分发）。
	delivered := l.Advisories.DrainDelivered()
	if l.Readback != nil {
		// 对账 TS turn-step-producer.ts:690：
		//   `readback.track(deliveredSnapshot, this.self.session.getTurnCount())`
		//
		// **必须用 session turn**——TS 在同文件 682-688 行明确警告过：此处若用
		// run 局部序号，会与 postTool/postTurn 的 session turn 错位，
		// course_changed 永远无法核销。见 Loop.SessionTurn 的说明。
		l.Readback.Track(delivered, l.SessionTurn())
	}

	return block
}

// renderSkillDiscoveryBlock 渲染 skill 的 Tier-1 发现块。
//
// 对账 TS 的 `skillRegistry.renderDiscoveryBlock(userInput, { exclude })`
// （turn-step-producer.ts:457）。
//
// **hint 的来源**：TS 用**本轮的 userInput**（当前用户消息）做 trigger 匹配
// ——相关的 skill 排前并标 `relevant="true"`。Go 侧取 `l.messages` 里
// **最后一条 user 消息**的 content（语义等价：那正是本轮的用户输入）。
//
// **为什么这是 per-turn 而非会话常量**：hint 每轮不同 → 排序结果不同。
// 这正是它**不能**进 frozen 块的原因（会把整个前缀缓存打掉）。
func (l *Loop) renderSkillDiscoveryBlock() string {
	if l.cfg.SkillRegistry == nil {
		return ""
	}
	return l.cfg.SkillRegistry.RenderDiscoveryBlock(l.lastUserInput(), skills.DiscoveryOpts{})
}

// lastUserInput 返回历史里**最后一条 user 消息**的文本内容。
//
// 用于 skill 发现层的 trigger 匹配（对账 TS 的 `userInput` 参数）。
// 无 user 消息时返回空串（发现层退化为纯字母序，不标 relevant）。
func (l *Loop) lastUserInput() string {
	for i := len(l.messages) - 1; i >= 0; i-- {
		role, ok := l.messages[i].Get("role")
		if !ok {
			continue
		}
		if rs, ok := role.(string); ok && rs == "user" {
			if c, ok := l.messages[i].Get("content"); ok {
				if s, ok := c.(string); ok {
					return s
				}
			}
			return ""
		}
	}
	return ""
}
