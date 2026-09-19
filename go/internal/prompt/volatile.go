package prompt

import "strings"

// FrozenBlockCaps 是 frozen 块的字符上限默认值。
// 对账 src/prompt/block-policy.ts 的 FROZEN_BLOCK_CAPS（standard 档）。
type FrozenBlockCaps struct {
	ProjectInstructions int
	ProjectMemory       int
	KnowledgeManifest   int
	SeedCapsule         int
	CodebaseIndex       int
}

// DefaultFrozenBlockCaps 对账 FROZEN_BLOCK_CAPS（block-policy.ts:13）。
var DefaultFrozenBlockCaps = FrozenBlockCaps{
	ProjectInstructions: 8000,
	ProjectMemory:       3000,
	KnowledgeManifest:   2200,
	SeedCapsule:         3000,
	CodebaseIndex:       4000,
}

// HostEnv 是宿主环境信息——`<environment>` 行的输入。
//
// 参数化而非直接读 os：这让 frozen 块可对账（golden 记录宿主值，Go 侧注入
// 同一组值）。生产调用方传真实宿主值。
type HostEnv struct {
	// Platform 是目标平台（Node 的 process.platform / Go 的 runtime.GOOS 映射）。
	Platform string
	// OSType 对应 Node 的 os.type()（如 "Darwin" / "Linux"）。
	OSType string
	// OSRelease 对应 Node 的 os.release()（如 "25.6.0"）。
	OSRelease string
}

// ActiveDomain 是当前星域（session 常量，进 frozen 前缀）。
type ActiveDomain struct {
	Name           string
	Motto          string
	VolatileBlock  string
	KnowledgeBlock string
}

// VolatileContext 是 frozen 稳定块的输入。
//
// 只含**参与 stable 块渲染**的字段。TS 侧 VolatileContext 还有大量
// per-turn 动态字段（toolHistory/taskProgress/decisions/...）——它们被
// buildStableVolatileBlock 显式 strip 掉，故此处不需要。
//
// 未移植的字段（本轮范围外，见 HANDOFF）：
//   - runtimeEnvBlock：detectRuntimeEnvBlock（spawn python3/node 探测版本）
//   - declaredVerify：renderDeclaredVerify（读 .rivet-config.json）
//   - activeDomain.KnowledgeBlock 的 top-K 选择（session 绑定逻辑）
type VolatileContext struct {
	Cwd                    string
	RivetMd                string
	ProjectMemoryBlock     string
	KnowledgeManifestBlock string
	SeedCapsuleBlock       string
	ProjectIndexBlock      string
	WorkingSet             []string
	SessionMemoryBlock     string
	// CwdRelation 取值 "self" / "world" / ""（对应 TS 的 CwdRelation）。
	CwdRelation  string
	ActiveDomain *ActiveDomain
	// RuntimeEnv 是 <runtime-env> 块的内容（对账 TS 的 detectRuntimeEnvBlock）。
	// 空串表示无该块。由调用方用 DetectRuntimeEnvBlock 生成后传入——
	// 这样 BuildStableVolatileBlock 保持纯函数（不 spawn 子进程）。
	RuntimeEnv string
	// DeclaredVerify 是 <verify-commands> 块的内容（对账 TS 的 renderDeclaredVerify）。
	// 空串表示无该块。由调用方用 DetectDeclaredVerifyBlock 生成后传入——
	// 同样为保持纯函数（不读文件系统）。
	DeclaredVerify string
	// BlockCaps 覆盖默认 caps（对齐 TS 的 `{...FROZEN_BLOCK_CAPS, ...ctx.blockCaps}`）。
	BlockCaps map[string]int
}

// 固定块文本（对账 volatile.ts 的字面量）。
const (
	soberBlock = "<sober>天枢在此。证据先行，全貌定向。</sober>"

	locusSelfBlock  = `<locus relation="self">这是你的源码。改动直接影响你自己的运行时行为。每个修改都做三级验证（typecheck → related tests → full suite）。对 prompt/、hooks/、engine.ts 的修改需说明预期的认知影响。</locus>`
	locusWorldBlock = `<locus relation="world">你在一个外部项目中工作。遵循项目自身的约定（AGENTS.md + .rivet.md）。验证深度匹配任务复杂度：简单修改跑 related tests，跨模块改动跑 full suite。</locus>`

	// starDomainSharedDiscipline 对账 volatile.ts:1191 —— 全星域共享执行纪律。
	starDomainSharedDiscipline = "\n执行纪律（全星域共享）：绿非证明，复现即证——宣称已修/已验证前，先用工具复现结论；报告里的每个数字要能指到一条真实验证记录。"
)

// BuildStableVolatileBlock 复刻 volatile.ts:511 的 buildStableVolatileBlock
// —— frozen 前缀的主体。
//
// 拼接顺序（对账 buildVolatileBlockInternal，volatile.ts:1055）：
//
//	<context>
//	<environment ... />            ← 宿主相关，由 host 参数注入
//	[<platform-note>...]           ← 仅当目标平台≠宿主
//	[<path-style-note>...]         ← 仅 win32
//	[<shell-note>...]              ← 仅 Windows shell（Unix 返回空）
//	[<runtime-env>...]             ← 未移植（IO 探测）
//	<sober>...</sober>
//	[<locus relation="...">...]    ← self / world
//	[<project-instructions>...]    ← 按节选取 + 转义 + truncateBlock
//	[<verify-commands ...>]        ← 未移植（读 .rivet-config.json）
//	[<project-memory>...]
//	[<knowledge-manifest>...]
//	[<seed-capsules>...]
//	[<codebase-index>...]
//	[<working-set>...]
//	[<session-memory>...]
//	[<star-domain ...>...]         ← frozen 末尾
//	</context>
//
// 块间分隔符是 "\n\n"；空块跳过。全空时返回 ""（TS 的 `parts.length > 0` 分支）。
//
// **未移植的 IO 探测块**（runtime-env / verify-commands）在 TS 侧依赖宿主
// 环境与项目文件；本轮参数化省略。生产调用方若需要，应在 host 侧补齐后
// 再拼接——但注意这会改变字节，需同步 oracle。
func BuildStableVolatileBlock(ctx VolatileContext, host HostEnv) string {
	ordered := []string{}

	// <environment> —— 宿主相关。目标平台≠宿主时附加 host 属性与提示。
	// 本轮 targetPlatform 恒等于 host.Platform（跨平台目标未移植）。
	ordered = append(ordered, `<environment platform="`+host.Platform+`" cwd="`+
		EscapeXML(ctx.Cwd)+`" os="`+EscapeXML(host.OSType+" "+host.OSRelease)+`" />`)

	// NOTE: TS 的顺序是 environment → platform-note → path-style-note →
	// shell-note → runtime-env → sober → locus → project-instructions → ...。
	// 本轮 platform-note / path-style-note / shell-note / runtime-env /
	// verify-commands 未移植（见函数注释）——在非 Windows 且无 runtime 标记
	// 的项目上，省略它们与 TS 输出一致（oracle 用例覆盖的场景）。

	// runtime-env 在 sober **之前**（对账 volatile.ts:1090 的顺序）。
	if ctx.RuntimeEnv != "" {
		ordered = append(ordered, ctx.RuntimeEnv)
	}

	ordered = append(ordered, soberBlock)
	if ctx.CwdRelation == "self" {
		ordered = append(ordered, locusSelfBlock)
	} else if ctx.CwdRelation == "world" {
		ordered = append(ordered, locusWorldBlock)
	}

	caps := DefaultFrozenBlockCaps
	if ctx.BlockCaps != nil {
		caps = mergeCaps(caps, ctx.BlockCaps)
	}

	if ctx.RivetMd != "" {
		// 对账 volatile.ts:1113 —— codebase-index 已含模块目录表时，剥离
		// project-instructions 里的冗余表（省 ~600-800 字符）。
		md := ctx.RivetMd
		if ctx.ProjectIndexBlock != "" {
			md = StripFirstMarkdownTable(md)
		}
		ordered = append(ordered, RenderProjectInstructionsBlock(md, caps.ProjectInstructions))
	}
	// verify-commands 在 project-instructions **之后**、project-memory 之前
	// （对账 volatile.ts:1129 的位置）。它**不过 truncateBlock**——TS 侧直接
	// push，没有 cap。
	if ctx.DeclaredVerify != "" {
		ordered = append(ordered, ctx.DeclaredVerify)
	}

	if ctx.ProjectMemoryBlock != "" {
		ordered = append(ordered, TruncateBlock(ctx.ProjectMemoryBlock, caps.ProjectMemory, "project-memory"))
	}
	if ctx.KnowledgeManifestBlock != "" {
		ordered = append(ordered, TruncateBlock(ctx.KnowledgeManifestBlock, caps.KnowledgeManifest, "knowledge-manifest"))
	}
	if ctx.SeedCapsuleBlock != "" {
		ordered = append(ordered, TruncateBlock(ctx.SeedCapsuleBlock, caps.SeedCapsule, "seed-capsule"))
	}
	if ctx.ProjectIndexBlock != "" {
		ordered = append(ordered, TruncateBlock(ctx.ProjectIndexBlock, caps.CodebaseIndex, "codebase-index"))
	}
	if len(ctx.WorkingSet) > 0 {
		files := make([]string, len(ctx.WorkingSet))
		for i, f := range ctx.WorkingSet {
			files[i] = "<file>" + EscapeXML(f) + "</file>"
		}
		ordered = append(ordered, "<working-set>\n"+strings.Join(files, "\n")+"\n</working-set>")
	}
	if ctx.SessionMemoryBlock != "" {
		ordered = append(ordered, "<session-memory>\n"+EscapeXML(ctx.SessionMemoryBlock)+"\n</session-memory>")
	}
	if ctx.ActiveDomain != nil {
		d := ctx.ActiveDomain
		knowledge := ""
		if d.KnowledgeBlock != "" {
			knowledge = "\n<domain-knowledge>\n" + EscapeXML(d.KnowledgeBlock) + "\n</domain-knowledge>"
		}
		ordered = append(ordered, `<star-domain name="`+d.Name+`" motto="`+d.Motto+`">`+
			d.VolatileBlock+starDomainSharedDiscipline+knowledge+`</star-domain>`)
	}

	if len(ordered) == 0 {
		return ""
	}
	return "<context>\n" + strings.Join(ordered, "\n\n") + "\n</context>"
}

// mergeCaps 对账 TS 的 `{...FROZEN_BLOCK_CAPS, ...ctx.blockCaps}`。
func mergeCaps(base FrozenBlockCaps, override map[string]int) FrozenBlockCaps {
	out := base
	if v, ok := override["projectInstructions"]; ok {
		out.ProjectInstructions = v
	}
	if v, ok := override["projectMemory"]; ok {
		out.ProjectMemory = v
	}
	if v, ok := override["knowledgeManifest"]; ok {
		out.KnowledgeManifest = v
	}
	if v, ok := override["seedCapsule"]; ok {
		out.SeedCapsule = v
	}
	if v, ok := override["codebaseIndex"]; ok {
		out.CodebaseIndex = v
	}
	return out
}
