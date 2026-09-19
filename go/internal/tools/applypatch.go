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
)

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
}

// ApplyPatch 构造 apply_patch 工具。
func ApplyPatch(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &applyPatchTool{Cwd: cwd, Grants: grants}
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

	// 目标路径安全校验（git 自身拒绝绝对路径/..，但预检给出更清楚的错误）
	for _, rel := range prompt.ExtractPatchTargetPaths(normalized) {
		vr := pathsafe.Validate(t.Cwd, rel, pathsafe.ModeWrite, &pathsafe.Options{Grants: t.Grants})
		if !vr.OK {
			return contract.Result{Content: "错误：补丁目标 " + rel + "：" + vr.Error, IsError: true}, nil
		}
	}

	_ = ctx
	ok, errText := applyPatchGitRun(t.Cwd, normalized, checkOnly)
	if !ok {
		return contract.Result{Content: "补丁应用失败：" + errText, IsError: true}, nil
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

// ensure filepath 被使用（预留：POSIX 路径归一化在 Windows 上的补充）
var _ = filepath.ToSlash
