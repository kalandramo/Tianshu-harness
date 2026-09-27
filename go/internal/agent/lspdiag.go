package agent

import (
	"strings"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/lsp"
	"github.com/kalandramo/tianshu/go/internal/tools"
)

// 本文件对账 TS `tool-pipeline.ts:1578-1607` 的 LSP 诊断回流收口。
//
// # 事实流（dataflow）
//
//	工具执行成功
//	  → changeFile（通知 server 文件已改，**必须早于取诊断**）
//	  → shouldRunDiagnostics？（只认 write_file/edit_file + 有 server）
//	  → getFileDiagnostics（清缓存 → didChange → 等推送）
//	  → filterDiagnosticsForEdit（按改动行区间收敛）
//	  → modelText 追加进 Content、uiText 追加进 UIContent
//
// # 两条硬纪律（都来自 TS 注释）
//
//  1. **changeFile 必须早于取诊断**——「Must happen BEFORE diagnostics so the
//     server's view is current」（`tool-pipeline.ts:1577`）。顺序反了会拿到
//     基于**编辑前**内容的诊断（滞后一轮）。
//  2. **best-effort**——「Silent: LSP diagnostics are best-effort, never fail
//     the turn」（`tool-pipeline.ts:1605`）。任何失败都不得让工具调用失败。

// LspDiagnostics 是 agent 侧消费的诊断能力面。
//
// **为什么是接口**：与 `tools.LspNavigator` 同一考虑——LSP 是运行时子系统，
// 测试可注入假实现（本机无 gopls，端到端只能靠假件）。
//
// 生产实现由 `cmd/tianshu` 的适配器提供（转发 `lsp.Navigator`）。
type LspDiagnostics interface {
	// GetFileDiagnostics 取文件级诊断（best-effort，可能空）。
	GetFileDiagnostics(filePath string, timeoutMS int) []lsp.LspDiagnostic
	// ChangeFile 通知 server 文件已在磁盘上被改。
	ChangeFile(filePath string)
	// HasServerForFile 报告该文件是否有注册的语言服务器。
	HasServerForFile(filePath string) bool
	// ⚠️ **本接口刻意不含 `IsReady()`**（订正自审查 #3）。
	//
	// 曾有一个 `IsReady() bool` 成员：它有声明、有生产实现，但
	// `injectLspDiagnostics` **从不调用**——死接口方法。而它的存在会误导
	// 后来者以为「注入前会先检查就绪」，实际判据走的是
	// `HasServerForFile`（PATH 探测）+ `GetFileDiagnostics` 的 best-effort。
	//
	// **若将来要加就绪门**：应加在**调用点**并说明它检查的是哪个条件
	//（`probeWhich` 的 PATH 探测 ≠ 「能真的 spawn」——见 HANDOFF 坑 62）。
}

// injectLspDiagnostics 在写工具执行成功后注入 `[LSP Diagnostics]` 段。
//
// 对账 TS `tool-pipeline.ts:1578-1607`。
//
// # 为什么 modelText 与 uiText 分两个通道
//
// 模型侧要**收敛**（只给改动行附近的，避免整文件诊断淹没上下文）；
// 人侧要**全量**（工具卡里应该能看到文件里所有问题）。
// `contract.Result` 已有 `Content`（给模型）与 `UIContent`（给 UI）双通道，
// 与 TS 的 `finalContent` / `rawToolResult.uiContent` 一一对应。
//
// # 返回
//
// 返回修改后的 `contract.Result`；任何失败都返回**原值**（best-effort）。
func (l *Loop) injectLspDiagnostics(tc toolCall, res contract.Result) contract.Result {
	if l.LspDiagnostics == nil || res.IsError {
		return res
	}

	// ① changeFile 通知（全量写工具，含 apply_patch——让它清理已删文件的过期诊断）。
	//
	// 对账 TS `tool-pipeline.ts:1579-1585`：通知在诊断之前，且路径按工具解析。
	if paths := l.writeToolPaths(tc); len(paths) > 0 {
		for _, p := range paths {
			l.LspDiagnostics.ChangeFile(p)
		}
	}

	// ② 诊断触发判定。
	//
	// 对账 TS `shouldRunDiagnostics(tu.name, tu.input.file_path)`——
	// **只认 write_file / edit_file**（有单个 file_path，目标无歧义）。
	filePath, _ := tc.input["file_path"].(string)
	if !tools.ShouldRunDiagnostics(tc.name, filePath, l.LspDiagnostics.HasServerForFile(filePath)) {
		return res
	}

	// ③ 取诊断（best-effort：超时/不可用都返回空）。
	diags := l.LspDiagnostics.GetFileDiagnostics(filePath, lsp.DefaultDiagnosticTimeoutMS)
	if len(diags) == 0 {
		return res
	}

	// ④ 按改动行区间收敛。
	//
	// `res.ChangedRanges` 是写工具在执行时算好的（对账 TS 的
	// `rawToolResult?.changedRanges`）。为空时过滤器退化为整文件（安全降级）。
	ranges := make([]lsp.LineRange, 0, len(res.ChangedRanges))
	for _, r := range res.ChangedRanges {
		ranges = append(ranges, lsp.LineRange{Start: r.Start, End: r.End})
	}
	filtered := lsp.FilterDiagnosticsForEdit(diags, ranges, lsp.DiagContextLines)

	// ⑤ 双通道注入。
	//
	// 对账 TS `tool-pipeline.ts:1594-1601`：
	//   modelText → finalContent（`\n\n[LSP Diagnostics]\n` + modelText）
	//   uiText    → uiContent（若已有 uiContent 则续接，否则以 finalContent 为底）
	if filtered.ModelText != "" {
		res.Content = res.Content + "\n\n[LSP Diagnostics]\n" + filtered.ModelText
	}
	if filtered.UIText != "" {
		// 对账 TS `tool-pipeline.ts:1609-1610`：
		//
		//	if (uiText && rawToolResult) {
		//	  rawToolResult.uiContent = `${uiBase}\n\n[LSP Diagnostics]\n${uiText}`
		//	}
		//
		// 其中 `uiBase = rawToolResult?.uiContent ?? finalContent`（:1605）。
		//
		// # 与 TS 的差异（已核清，非遗漏）
		//
		// TS 的 `rawToolResult` 判据是**防御性的**：它只在工具成功返回时被赋值
		// （`tool-pipeline.ts:1535` 的 `rawToolResult = r`），而本注入块本身就在
		// `!harnessResult.isError` 分支内——两者几乎重合。故该判据在 TS 下
		// 极少为假。
		//
		// Go 的 `contract.Result` 是**值类型**，没有「结果对象不存在」这个状态，
		// 故无对应判据可加。这里改为按 TS 的 uiBase 语义处理：
		// 有 `UIContent` 就续接（保留工具自己的 UI 呈现），否则以 `Content` 为底。
		base := res.UIContent
		if base == "" {
			base = res.Content
		}
		// ⚠️ **当前无生产消费者**（审查 #4 核实）。
		//
		// `UIContent` 的消费语义在 `internal/agent/turnbudget.go` 的
		// `DisplayContent(uiContent, result)`，但该函数**只被测试调用**——
		// Go 侧尚未移植 TUI/server 工具卡管线（TS 的消费者是
		// `tool-pipeline.ts:1578`）。
		//
		// 故这行写入目前只影响测试与将来的渲染层。保留它是为**语义完整**
		// （诊断确实应同时给模型与 UI 两个视角），而非当前有收益。
		// 与 HANDOFF 已记的「UIContent 待 TUI 管线」同源，不重复登记。
		res.UIContent = base + "\n\n[LSP Diagnostics]\n" + filtered.UIText
	}
	return res
}

// writeToolPaths 解析写工具的受影响路径（供 changeFile 通知）。
//
// 对账 TS `tool-pipeline.ts:1580-1584`：
//
//	apply_patch → patchTargetPaths(tu.input.diff)
//	其余写工具 → extractWriteFilePaths(tu.name, tu.input)
//
// `apply_patch` 走 diff 头（含删除经 `--- ` 回退），以便 server 得知文件消失。
func (l *Loop) writeToolPaths(tc toolCall) []string {
	if !tools.WriteToolNames()[tc.name] {
		return nil
	}
	if tc.name == "apply_patch" {
		if diff, ok := tc.input["diff"].(string); ok {
			return extractPatchTargetPaths(diff)
		}
		return nil
	}
	if p, _ := tc.input["file_path"].(string); p != "" {
		return []string{p}
	}
	return nil
}

// extractPatchTargetPaths 从补丁文本里取目标路径。
//
// 对账 TS `patchTargetPaths`（`src/agent/pre-write-claims.ts:18-35`）：
// 以 `+++ `（新奇侧）为准；**纯删除时回退到前一行的 `--- `（旧侧）**。
//
// # ★ 为什么必须处理删除（第一百零五刀）
//
// 纯删除补丁的新奇侧是 `+++ /dev/null`（文件已不存在）。若只认 `+++ `，
// 被删文件**永远解析不出来** → `ChangeFile` 不通知 → server 与该文件的
// 诊断缓存里，陈旧诊断**永久保留**——用户会持续看到已删文件的错误。
//
// TS 注释逐字：「Deletion (`+++ /dev/null`): the removed file is the
// preceding --- header.」故此处按同样的回退取值。
func extractPatchTargetPaths(diff string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || p == "/dev/null" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "+++ ") {
			continue
		}
		// 新奇侧路径（去掉 git 的 `b/` 前缀）。
		p := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")), "b/")
		if p != "" && p != "/dev/null" {
			add(p)
			continue
		}
		// ★ 删除（`+++ /dev/null`）：被删文件是**前一行**的 `--- ` 头。
		if i > 0 && strings.HasPrefix(lines[i-1], "--- ") {
			add(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(lines[i-1], "--- ")), "a/"))
		}
	}
	return out
}

// NavigatorDiagnostics 把 `internal/lsp.Navigator` 适配成 `LspDiagnostics`。
//
// # 为什么这个适配器在 `internal/agent` 而非 `cmd/tianshu`
//
// 原先生产装配（`cmd/tianshu/main.go`）与 e2e 测试各自写了一份**逐字同构**
// 的适配器。两份的代价是：**生产侧漂移不会被 e2e 覆盖**——测试验证的是
// 「测试那份」，而真实跑的是另一份。收敛到一处即消除该漂移面。
//
// # 为什么不能直接把 `*lsp.Navigator` 赋给 `LspDiagnostics`
//
// `LspDiagnostics` 要求返回 `[]lsp.LspDiagnostic`——`Navigator` 的方法签名
// 恰好如此（本包直接 import `internal/lsp` 的类型，故无需字段拷贝）。
// 但 `Navigator` 的方法集**大于**接口要求（还有 GotoDefinition 等），
// 故理论上可直接赋值；保留显式适配器是为了**收窄可见面**：
// agent 层只看到诊断相关的三个方法，不暴露导航能力。
func NavigatorDiagnostics(nav *lsp.Navigator) LspDiagnostics {
	if nav == nil {
		return nil
	}
	return &navigatorDiagnostics{nav: nav}
}

type navigatorDiagnostics struct{ nav *lsp.Navigator }

func (a *navigatorDiagnostics) ChangeFile(filePath string) { a.nav.ChangeFile(filePath) }

func (a *navigatorDiagnostics) HasServerForFile(filePath string) bool {
	return a.nav.HasServerForFile(filePath)
}

func (a *navigatorDiagnostics) GetFileDiagnostics(filePath string, timeoutMS int) []lsp.LspDiagnostic {
	return a.nav.GetFileDiagnostics(filePath, timeoutMS)
}
