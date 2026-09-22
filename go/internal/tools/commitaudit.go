package tools

import (
	"regexp"
	"strconv"
)

// commitaudit.go —— 提交信息的任务标签审计。
//
// 对账 TS `src/tools/commit-audit.ts`（32 行）。

// CommitAuditResult 是审计结果。
//
// 对账 TS `CommitAuditResult`（commit-audit.ts:1-8）。
type CommitAuditResult struct {
	OK      bool
	Tags    []string
	Message string
}

// taskTagRe 匹配任务标签（`S14` / `M1` / `C2a` 等）。
//
// 对账 TS `TASK_TAG_RE`（commit-audit.ts:10）：`/\b([SBCML]\d+[a-z]?)\b/g`
//
// **`\b` 是 ASCII 词边界**——Go 的 `\b` 语义一致（都基于 `\w`）。
var taskTagRe = regexp.MustCompile(`\b([SBCML]\d+[a-z]?)\b`)

// ExtractTaskTags 从提交信息里提取任务标签。
//
// 对账 TS `extractTaskTags`（commit-audit.ts:13-15）。
func ExtractTaskTags(message string) []string {
	matches := taskTagRe.FindAllStringSubmatch(message, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// AuditCommitTagScope 校验提交信息的任务标签与实际变更文件是否匹配。
//
// 对账 TS `auditCommitTagScope`（commit-audit.ts:20-32）。**只在信息含标签时
// 审计**（无标签的提交直接通过）：
//
//   - 有标签但 0 文件 → 警告（空提交/误标，如 933887d S14）
//   - 标签数 > 1 且变更文件数 < 标签数 → 警告（多任务范围蔓延信号，如 1adcf6c）
func AuditCommitTagScope(message string, changedFiles []string) CommitAuditResult {
	tags := ExtractTaskTags(message)
	if len(tags) == 0 {
		return CommitAuditResult{OK: true, Tags: tags}
	}
	if len(changedFiles) == 0 {
		return CommitAuditResult{
			OK:      false,
			Tags:    tags,
			Message: "⚠️ Commit tagged " + joinTags(tags) + " but changed 0 files — possible mislabel or empty commit.",
		}
	}
	if len(tags) > 1 && len(changedFiles) < len(tags) {
		return CommitAuditResult{
			OK:   false,
			Tags: tags,
			Message: "⚠️ Commit claims " + strconv.Itoa(len(tags)) + " task tags (" + joinTags(tags) +
				") but changed only " + strconv.Itoa(len(changedFiles)) + " file(s) — possible multiple unrelated tasks in one commit.",
		}
	}
	return CommitAuditResult{OK: true, Tags: tags}
}

// joinTags 用 `,` 连接标签（对账 TS 的 `tags.join(',')`）。
func joinTags(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += ","
		}
		out += t
	}
	return out
}
