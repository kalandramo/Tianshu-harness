package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
	"github.com/kalandramo/tianshu/go/internal/prompt"
	"github.com/kalandramo/tianshu/go/internal/recovery"
)

// applyPatchVerifyEnabled 报告是否启用应用后校验与失败回滚。
//
// 对账 isApplyPatchVerifyEnabled：`RIVET_APPLY_PATCH_VERIFY=0|false` 时
// 退回「git apply 后信任它」的 legacy 行为（不备份、不回滚）。
//
// **本移植范围**：开关控制**备份 + 失败回滚**；语法检查（firstFatalSyntax）
// 是独立欠账，尚未移植。
func applyPatchVerifyEnabled() bool {
	v := os.Getenv("RIVET_APPLY_PATCH_VERIFY")
	return v != "0" && v != "false"
}

// patchTarget 是一个补丁目标文件。
//
// 对账 PatchTarget。existedBefore 决定回滚策略：存在过 → 从备份恢复；
// 不存在 → 删除（补丁新建的文件）。
type patchTarget struct {
	rel           string
	abs           string
	existedBefore bool
}

// applyPatchMaxUILines 是 UI 展示 diff 的行数上限。
//
// 对账 APPLY_PATCH_MAX_UI_LINES = 600。
const applyPatchMaxUILines = 600

// applyPatchPointerPrefix 是 apply_patch 自己的折叠指针前缀。
//
// 对账 APPLY_PATCH_POINTER_PREFIX（来自 apply-patch-arg-processor）。
const applyPatchPointerPrefix = "[patch applied to"

// normalizeDiffPaths 把 diff 头部行的反斜杠归一化为正斜杠。
//
// 对账 normalizeDiffPaths：**只处理**以 `--- `、`+++ `、`diff --git` 开头的行，
// 其余行原样保留（内容行里的反斜杠是数据，不能动）。
func normalizeDiffPaths(diff string) string {
	lines := strings.Split(diff, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "diff --git") {
			lines[i] = strings.ReplaceAll(line, `\`, "/")
		}
	}
	return strings.Join(lines, "\n")
}

// truncateDiffForUI 截断仅供展示的 diff。
//
// 对账 truncateDiffForUi：超出行数时保留前 maxLines 行并附一行提示。
func truncateDiffForUI(diff string, maxLines int) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= maxLines {
		return diff
	}
	hidden := len(lines) - maxLines
	out := append([]string{}, lines[:maxLines]...)
	out = append(out, "…（另有 "+itoa(hidden)+" 行 diff，Ctrl+O）")
	return strings.Join(out, "\n")
}

// applyPatchGitRun 执行 `git apply --3way [--check] <patchfile>`。
//
// 对账 applyPatch（TS）。返回 (ok, errorText)。错误文本取 stderr 优先、
// stdout 兜底，再兜底一句状态码说明。
func applyPatchGitRun(cwd, diff string, checkOnly bool) (bool, string) {
	tmp, err := os.CreateTemp("", "rivet-patch-*.patch")
	if err != nil {
		return false, "创建临时补丁文件失败：" + err.Error()
	}
	patchFile := tmp.Name()
	defer os.Remove(patchFile)
	if _, err := tmp.WriteString(diff); err != nil {
		tmp.Close()
		return false, "写入临时补丁文件失败：" + err.Error()
	}
	tmp.Close()

	args := []string{"apply", "--3way"}
	if checkOnly {
		args = append(args, "--check")
	}
	args = append(args, patchFile)

	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return true, ""
	}
	// 退出码非 0：优先 stderr，其次 stdout，兜底状态码
	errText := strings.TrimSpace(stderr.String())
	if errText == "" {
		errText = strings.TrimSpace(stdout.String())
	}
	if errText == "" {
		errText = "git apply 以非零状态码退出：" + err.Error()
	}
	return false, errText
}

// applyPatchTool 是 apply_patch 工具的实现。
//
// 对账 src/tools/apply-patch.ts 的 APPLY_PATCH_TOOL。核心是调
// `git apply --3way`——**不自己实现 diff 解析**（行为等价的本质是同一个
// git 实现）。
//
// **已知降级**（见 HANDOFF）：
//   - 补丁前备份 + 失败回滚（rollbackTargets / unstagePatchTargets）未移植。
//     TS 侧 `git apply --3way` 失败时状态已被动过（冲突标记落盘、干净文件
//     已 staged），故主动回滚。Go 侧依赖 git 自身的原子性，失败时可能
//     留下部分改动。
//   - 应用后语法检查回滚（firstFatalSyntax）未移植——同 hash_edit。
//   - 编辑失败计数门、client-delegate（apply_edit 通道）未移植。
//   - 指针守卫仅做 apply_patch 自己的前缀检查（跨工具检测依赖未移植的
//     pointer-guard 模块）。
type applyPatchTool struct {
	baseTool
	Cwd    string
	Grants pathsafe.GrantChecker
	// Stack 是备份栈（补丁前备份 + 失败回滚）。
	Stack *recovery.Stack
}

// ApplyPatch 构造 apply_patch 工具。
func ApplyPatch(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &applyPatchTool{Cwd: cwd, Grants: grants, Stack: recovery.DefaultStack()}
	t.def = contract.Definition{
		Name:        "apply_patch",
		Description: `用 git apply 把 unified diff 应用到当前 git 仓库。支持应用前先做 check-only 校验。用于多文件改动或应用已有 patch；单点定向编辑优先用 edit_file 或 hash_edit。注意：大 patch 应用后，消息历史里只保留摘要指针（改动文件列表 + 大小）而非 diff 原文——用 read_file 或 git diff 查看结果。check_only 校验会保留完整 diff 内联。`,
		InputSchema: objSchema(map[string]any{
			"diff":       strProp("要应用的 unified diff 内容。"),
			"check_only": boolProp("只校验 patch 能否干净应用，不修改文件。"),
		}, "diff"),
	}
	t.enabled = true
	t.concurrent = false
	return t
}

func (t *applyPatchTool) RequiresApproval(p *CallParams) bool {
	return p.ApprovalMode != "dangerously-skip-permissions"
}

func (t *applyPatchTool) Timeout(*CallParams) time.Duration { return 60 * time.Second }

func (t *applyPatchTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	diff, _ := p.Input["diff"].(string)
	if strings.TrimSpace(diff) == "" {
		return contract.Result{Content: `apply_patch 需要非空的 "diff" 字符串。`, IsError: true}, nil
	}

	// 指针回灌守卫（仅本工具前缀；跨工具检测见降级说明）
	if strings.HasPrefix(strings.TrimLeft(diff, " \t\n\r"), applyPatchPointerPrefix) {
		return contract.Result{
			Content: `错误："diff" 是历史消息里的显示指针（"` + applyPatchPointerPrefix +
				` …"），不是真正的 unified diff。该占位符只在大内容写入/应用后的历史消息中出现——从来不是合法输入。` +
				`请提供实际的 unified diff，或先用 read_file / git diff 查看当前状态。` +
				"\n\n[pointer placeholder from message history]",
			IsError: true,
		}, nil
	}

	normalized := normalizeDiffPaths(diff)
	checkOnly := boolArg(p.Input, "check_only")
	verify := applyPatchVerifyEnabled() && !checkOnly

	// ── 目标快照 + **补丁前备份** ──
	//
	// check_only 从不写盘，故跳过（对账 TS 的三元判断）。
	// 备份必须先于 git apply 完成——否则回滚拿到的是补丁后的内容。
	var targets []patchTarget
	if verify {
		for _, rel := range prompt.ExtractPatchTargetPaths(normalized) {
			abs := filepath.Join(t.Cwd, rel)
			_, statErr := os.Stat(abs)
			targets = append(targets, patchTarget{rel: rel, abs: abs, existedBefore: statErr == nil})
		}
	}

	// 目标路径安全校验（git 自身拒绝绝对路径/..，但 verify/备份会在 git 之前
	// 读写这些 join 出来的绝对路径——符号链接目录场景下仍需工作区边界把关）
	for _, rel := range prompt.ExtractPatchTargetPaths(normalized) {
		vr := pathsafe.Validate(t.Cwd, rel, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
		if !vr.OK {
			return contract.Result{Content: "错误：补丁目标 " + rel + "：" + vr.Error, IsError: true}, nil
		}
	}

	for _, tg := range targets {
		if tg.existedBefore {
			if _, err := t.Stack.TrackFileChange(t.Cwd, recovery.FileChangeRecord{
				FilePath:   tg.rel,
				Action:     "edit",
				ToolCallID: "apply_patch",
			}); err != nil {
				return contract.Result{
					Content: "错误：补丁目标 " + tg.rel + " 的备份失败：" + err.Error(),
					IsError: true,
				}, nil
			}
		}
	}

	_ = ctx
	ok, errText := applyPatchGitRun(t.Cwd, normalized, checkOnly)
	if !ok {
		// `git apply --3way` 报冲突是 exit 1，但退出前状态**已被动过**：
		// 冲突标记与干净 hunk 已落盘、干净文件被整体套用并 staged 进索引、
		// 冲突文件留下 UU（unmerged）索引条目。
		//
		// 只报「失败」就返回会让模型按「失败=没发生」重试同一补丁
		// （撞 "does not exist in index" 死循环），UU 条目还会让标准恢复
		// 命令 `git checkout -- <file>` 报 "path is unmerged"。
		//
		// 失败分支复用回滚路线，职责两分：**工作树内容**由 RestoreLatestBackup
		// 从补丁前备份恢复，**索引条目**由 unstagePatchTargets 收回 HEAD
		// （reset 不碰工作树，内容恢复不碰索引，职责不可混）。
		//
		// 文案只说「已回滚到补丁前状态」——那是对结果的保证，不声称「曾发生
		// 半套用」：和索引不匹配一类失败路径上本就什么都没留下。
		rolledBack := len(targets) > 0
		if rolledBack {
			t.rollbackTargets(targets, p.SessionID)
			t.unstagePatchTargets(targets)
		}
		content := "补丁应用失败：" + errText
		if rolledBack {
			content = "补丁应用失败（已回滚到补丁前状态）：" + errText
		}
		return contract.Result{Content: content, IsError: true}, nil
	}

	if checkOnly {
		return contract.Result{Content: "补丁可干净应用（仅校验；未修改文件）。"}, nil
	}

	// 登记写入的文件（让证据追踪感知）
	if p.OnFileWrite != nil {
		for _, rel := range prompt.ExtractPatchTargetPaths(normalized) {
			vr := pathsafe.Validate(t.Cwd, rel, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
			if vr.OK {
				p.OnFileWrite(vr.Path)
			}
		}
	}

	return contract.Result{
		Content:   "补丁应用成功。",
		UIContent: truncateDiffForUI(strings.TrimSpace(normalized), applyPatchMaxUILines),
	}, nil
}

// rollbackTargets 撤销已应用的补丁：存在过的文件从备份恢复，新建的文件删除。
//
// 对账 rollbackTargets。逐文件 best-effort。
func (t *applyPatchTool) rollbackTargets(targets []patchTarget, sessionID string) {
	for _, tg := range targets {
		if tg.existedBefore {
			t.Stack.RestoreLatestBackup(t.Cwd, tg.rel, sessionID)
		} else {
			_ = os.Remove(tg.abs) // 已不存在则忽略
		}
	}
}

// unstagePatchTargets 收回失败补丁留下的索引条目。
//
// 对账 unstagePatchTargets。失败的 `git apply --3way` 可能把干净合并的目标
// staged、冲突目标留在 UU（unmerged）态。`git reset -- <path>` 把索引条目
// 退回 HEAD（**不碰工作树**——内容恢复是 rollbackTargets 的职责），
// 解开 `git checkout -- <file>` 这类恢复命令的 "path is unmerged" 阻塞。
//
// 与 rollbackTargets 的顺序无关（各自只碰工作树/索引之一），但**职责不可混**：
// reset 无法恢复文件内容，内容恢复无法清除 unmerged 条目。
//
// 取舍：这些路径上补丁前已 staged 的改动也会被 unstage——失败路径下可接受，
// 因为那些条目本就是补丁自己 staged 的。非 git 工作区里是 no-op（退出码非 0）。
func (t *applyPatchTool) unstagePatchTargets(targets []patchTarget) {
	if len(targets) == 0 {
		return
	}
	rels := make([]string, len(targets))
	for i, tg := range targets {
		rels[i] = tg.rel
	}
	args := append([]string{"reset", "-q", "--"}, rels...)
	cmd := exec.Command("git", args...)
	cmd.Dir = t.Cwd
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run() // best-effort：失败静默（工作树回滚已在上一步完成）
}

// ensure filepath 被使用（预留：POSIX 路径归一化在 Windows 上的补充）
var _ = filepath.ToSlash
