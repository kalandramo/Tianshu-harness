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
// 完整 51 个工具（TS full preset 基线，`tool-preset.test.ts:100` 的
// totalCount('full') 断言；桌面端有调度器时 +3 = 54）是分波目标，
// 此处只收「读写检索执行」四类的基础工具。
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
	// recall_general / record_general_finding：将星账本（跨会话战绩累积）。
	//
	// 对账 TS `bootstrap.ts:682-683`（**不是** `default-registry.ts`——那两个
	// 工具在 TS 侧由 bootstrap 层注册，且受 preset 门控：
	// `tool-preset.ts:139-140` 把它们列在 `MINIMAL_EXCLUDES`，即 **minimal
	// 档不含**，属 full 档）。
	//
	// **Go 侧的差异（有意）**：Go 侧**没有 preset 机制**（全仓无
	// `presetIncludes`/`ToolPreset`）——所有工具无条件注册。故此处直接注册，
	// 与 Go 侧现状一致。移植 preset 门控是独立的一刀。
	//
	// 存储层在 `internal/context/generalledger.go`（纯文件 I/O）。
	r.Register(RecallGeneral(cwd))
	r.Register(RecordGeneralFinding(cwd))

	// ── Git ──
	// diff：工作树改动。经 `SpawnGit`（环境消毒 + 可执行路径发现）。
	r.Register(Diff())
	// git：结构化操作（status/diff_summary/commit/log/log_graph/stash/stash_pop）。
	r.Register(Git())
	// git_scout：**只读** git 史实侦察（9 个查询 action，无写动作）。
	//
	// 对账 TS `GIT_SCOUT_TOOL`（default-registry.ts:129，preset !== 'taiyi'
	// 时注册）。**为什么与 git 并存**：`git` 含写动作，readonly profile
	// （code_scout 等）拿不到它——侦察 worker 因此无法做 git 史实取证。
	// git_scout 是纯查询子集，`RequiresApproval: false`，可安全交给 readonly。
	r.Register(GitScout())

	// ── 执行 ──
	r.Register(Bash(cwd))
	// job：后台任务控制面（第八十刀）。
	//
	// **为什么紧随 bash**：它管的正是 `bash(run_in_background)` 起的任务——
	// 后台任务的生命周期脱离调用点，控制面必须是独立工具（可跨轮调用）。
	// TS 侧同样紧跟 BASH_TOOL 注册（`default-registry.ts`）。
	r.Register(Job())
	// request_path_access：工作区外路径的**主动申请**入口（第八十一刀）。
	//
	// **为什么需要它**：门链在非 skip 档遇到工作区外路径时直接拒绝
	// （「无提示通道」）。没有本工具，模型无法做目录级/批量/bash 场景的授权。
	r.Register(RequestPathAccess(cwd))
	// export_file：把资产导出到工作区之外（第八十二刀）。
	//
	// **为什么需要它**：write_file 面向工作区内，而用户常要求「放到桌面/下载」。
	// TS 侧注释明说它「面向用户可见的外部输出」——bash 重定向是反面做法。
	// 依赖全是平台能力（fs/path/expandHome/DetectSensitiveFile），Go 侧全有。
	r.Register(ExportFile(cwd))
	// create_document：办公文档家族之一（第八十三刀 · W1-1）。
	//
	// **为什么**：TS 侧它属 desktopTools 层（用户要求「放到桌面/下载」的文档）。
	// 依赖只有  + 纯字符串渲染——是 export_file 的**下游**，
	// 复用其敏感门与 50MB 上限。**门链早已预留**（approval_pathgrant.go:101
	// 的  分支），本刀让它首次有真实消费者。
	r.Register(CreateDocument(cwd))
	// create_spreadsheet：办公文档家族之二（W1-2）。
	//  是「以 .xls 扩展名保存的 HTML 表格」（TS 注释明说），与 html 同渲染器。
	r.Register(CreateSpreadsheet(cwd))
	// create_presentation / create_pdf / create_image：办公文档家族之三/四/五（W1-3/4/5）。
	// 三者同为 export_file 的下游——`.ppt`/`.xls`/`.doc` 都是「HTML 伪装」格式
	// （TS 注释明说），`.pdf` 是「打印就绪的 HTML + @page」靠浏览器打印。
	// 零第三方库。
	r.Register(CreatePresentation(cwd))
	r.Register(CreatePdf(cwd))
	r.Register(CreateImage(cwd))
	// open_path：在 OS 中打开文件/目录（第八十四刀 · W2-1）。
	//
	// **为什么需要**：办公文档家族产出的文件要能被用户看到—— 是
	// 「产出 → 查看」闭环的最后一环。依赖  + （已有）。
	// **门链早已预留**： 的 open_path 授权分支
	// 在本刀前就存在，本刀让它首次有真实消费者。
	r.Register(OpenPath(cwd))
	// capability：只读能力索引（第八十五刀 · W2-2）。
	//
	// **为什么需要**：模型需要知道本机有哪些外部 CLI 可用（ffmpeg/jq/rg...）
	// 以及缺失时怎么装——否则会盲目尝试不存在的命令。
	// 依赖 （which）+ （env）+ 读 。
	r.Register(Capability(cwd))
	// web_fetch：抓取 URL 转文本（第九十三刀 · W3-4d）。
	//
	// **为什么需要**：模型读文档/API 参考/issue 页面的主通道。
	// 它是  三层（SSRF / HTTP 抓取 / HTML→MD）+ 缓存 +
	// fetch-core 的**消费者**——那些层此前零生产消费者，本刀让它们全部接线。
	r.Register(WebFetch(cwd))
	// web_crawl：从种子 URL 整站爬取（第九十五刀 · W3-5b）。
	//
	// **为什么需要**：模型「把这个文档站读完」的场景——批量获取各页正文。
	// 复用 crawl BFS 内核（第九十四刀）+ sitemap 阶梯 + fetch 内核。
	// **artifact 落盘未接**（见 webcrawl.go 文件头披露）。
	r.Register(WebCrawl(cwd))
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
