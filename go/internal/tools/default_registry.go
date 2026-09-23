package tools

import (
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// Options 是默认注册表的装配选项。
type Options struct {
	// Cwd 是工作目录。
	Cwd string
	// Grants 是越界路径授权判定。
	Grants pathsafe.GrantChecker
	// Extra 是额外注册的工具（装配注入层）。
	Extra []Tool
}

// NewDefaultRegistry 装配默认工具集。
//
// 对账 src/tools/default-registry.ts 的 kernel 层。Go 版先实现内核子集——
// 完整 44 个工具是分波目标，此处只收「读写检索执行」四类的基础工具。
//
// 装配顺序不影响行为（Definitions() 按名升序输出，保证请求体字节稳定），
// 但保持与 TS 相近的分组便于对账。
func NewDefaultRegistry(opts Options) *Registry {
	r := NewRegistry()
	cwd := opts.Cwd

	// ── 文件读写 ──
	r.Register(ReadFile(cwd, opts.Grants))
	r.Register(WriteFile(cwd, opts.Grants))
	r.Register(EditFile(cwd, opts.Grants))
	r.Register(HashEdit(cwd, opts.Grants))
	r.Register(ApplyPatch(cwd, opts.Grants))

	// ── 检索 ──
	r.Register(Glob(cwd))
	r.Register(Grep(cwd))
	// read_section：artifact 召回路径（大工具结果被拦截后按区段取回）。
	// 对账 TS 的 read-section.ts——属 read 类，minimal preset 也含。
	r.Register(ReadSection(cwd, opts.Grants))

	// ── 项目勘察 ──
	// 三者共用 `classifyPath`（注意力分级）与 `ScanExcludeDirs`（剪枝基线）。
	r.Register(RepoMap())
	r.Register(InspectProject())
	r.Register(FileInfo())
	// related_tests：源文件 ↔ 测试文件的路径推导（纯启发式）。
	// 对账 TS 的 RELATED_TESTS_TOOL——其 Meridian 分支在 Go 侧不存在，
	// 故对应的是 `createRelatedTestsTool(() => null)` 静态变体。
	r.Register(RelatedTests(cwd))
	// leave_mark：会话离别印记（工具本体 + 回调派发）。
	// scope 收窄：印记的真正落盘者（constellation post-session hook）在 Go 侧
	// 不存在——详见 leavemark.go 文件头。
	r.Register(LeaveMark())

	// ── Git ──
	// diff：工作树改动。经 `SpawnGit`（环境消毒 + 可执行路径发现）。
	r.Register(Diff())
	// git：结构化操作（status/diff_summary/commit/log/log_graph/stash/stash_pop）。
	r.Register(Git())

	// ── 执行 ──
	r.Register(Bash(cwd))
	r.Register(RunTests(cwd))

	// ── 任务 ──
	r.Register(Todo())

	// ── 交互 ──
	// ask_user_question：向用户提问并结束回合（EndTurn）。
	// **分层差异（明示）**：TS 侧它在 `src/bootstrap.ts:667` 注册（interactive
	// 层），不在 `createDefaultToolRegistry` 里；Go 侧无对应 bootstrap 分层
	// （工具全在此装配），故注册于此。行为等价——TS 的 bootstrap 也是无条件
	// 注册（仅受 preset 门控）。
	r.Register(AskUserQuestion())
	// skill：按名加载 skill 的完整指令（Tier-2 激活）。
	// **未接线（诚实披露）**：Tier-1 发现层（available-skills 注入）未接
	// prompt——Go 的 frozen 块是会话常量，TS 的是 per-turn 动态 appendix。
	// 工具本体可用（注册表有内容即可加载），但模型「如何知道有哪些 skill」
	// 这一环待补。详见 internal/skills 包注释。
	r.Register(Skill())

	// ── 计划 ──
	// plan：统一计划生命周期（submit / close）。enter_mode/exit_mode 依赖
	// plan mode 状态机（Go 侧未移植）——工具内**诚实报错**而非假装成功。
	r.Register(Plan())

	// ── 装配注入 ──
	for _, t := range opts.Extra {
		r.Register(t)
	}
	return r
}

// Filter 按白名单过滤注册表（对应 TS 的 filterToolRegistry）。
//
// 白名单里出现未注册的工具名时**失败**而非静默忽略——静默会让拼错的
// authority 白名单退化为空集却无人察觉。
func Filter(src *Registry, allowed []string) (*Registry, error) {
	out := NewRegistry()
	for _, name := range allowed {
		t, ok := src.Get(name)
		if !ok {
			return nil, &UnknownAllowlistEntry{Name: name}
		}
		out.Register(t)
	}
	return out, nil
}

// UnknownAllowlistEntry 表示白名单引用了未注册的工具。
type UnknownAllowlistEntry struct{ Name string }

func (e *UnknownAllowlistEntry) Error() string {
	return "Cannot allowlist unknown tool: " + e.Name
}
