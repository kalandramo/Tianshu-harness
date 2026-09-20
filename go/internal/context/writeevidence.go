package context

// writeevidence.go —— 写类工具孤儿恢复的文案与磁盘证据探测。
//
// 对账 TS `src/context/write-evidence-probe.ts`。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kalandramo/tianshu/go/internal/session"
)

// WriteToolNames 对账 TS 的 `WRITE_TOOLS`——孤儿恢复需**非破坏性**的工具。
//
// **必须逐字相同**（工具名）。与 `session.WriteToolNames` 同源但语义不同：
// 后者用于「剔除孤儿时是否需非破坏性警告」，此处用于「合成占位是否走写类分支」。
var writeToolNamesForProbe = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"hash_edit":   true,
	"ast_edit":    true,
	"apply_patch": true,
}

// WriteEvidence 是磁盘证据。
//
// 对账 TS `WriteEvidence`。
type WriteEvidence struct {
	Exists bool
	Bytes  int64
}

// WriteProbe 是 cwd 作用域的磁盘探测函数。
//
// 对账 TS `WriteProbe`。返回 nil 表示跳过（无法判定）。
type WriteProbe func(toolName string, args any) *WriteEvidence

// WriteRecoveryMarker 是每条合成恢复结果的共享标记前缀。
//
// **必须逐字相同**——它同时用于渲染与「历史里已有几次恢复」的计数
// （重复升级的触发依据）。
const WriteRecoveryMarker = "会话中断导致工具结果丢失"

// attribution 是每条合成结果附带的归因行。
//
// 对账 TS 的 `ATTRIBUTION`。**为什么必须有**：没有它，模型只看到「结果丢失」，
// 几次之后就会**理性地**得出结论「写工具本身坏了」（用户报告 2026-07-10），
// 然后弃用它们改用 bash 绕过。
const attribution = "\n【归因】这是宿主进程中断（用户中断/强杀/断电/卡顿）造成的结果回传丢失，是恢复机制的合成占位——" +
	"不是写工具故障，也不是系统架构问题。写工具功能正常，请继续正常使用，不要改用 bash 绕过。"

// repeatEscalation 是同一会话已累积多次合成恢复时的额外段落。
//
// 对账 TS 的 `REPEAT_ESCALATION`——模型必须**如实报告环境问题**给用户，
// 而不是自行发明架构诊断。
const repeatEscalation = "\n【重复发生】本会话已多次出现该恢复消息，说明宿主环境在反复中断" +
	"（常见诱因：超大文件全量重写导致卡顿后被强杀、手动反复中断、旧版本缺陷）。" +
	"请把这一情况如实报告给用户，建议升级到最新版本；写入尽量小步进行（edit_file 局部替换优于整文件重写）。" +
	"不要自行得出\"工具层不可用\"的结论。"

// ExtractTargetPath 从工具参数里解析目标路径。
//
// 对账 TS `extractTargetPath`：参数可能是 **JSON 字符串**（指针坍缩后的形态）
// 或对象；取 `file_path` 优先、其次 `path`。
func ExtractTargetPath(args any) string {
	var obj map[string]any
	switch v := args.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			return ""
		}
	case map[string]any:
		obj = v
	default:
		return ""
	}
	if obj == nil {
		return ""
	}
	if p, ok := obj["file_path"].(string); ok && p != "" {
		return p
	}
	if p, ok := obj["path"].(string); ok && p != "" {
		return p
	}
	return ""
}

// FormatBytes 对账 TS `formatBytes`：B / KB（1 位小数）/ MB（1 位小数）。
func FormatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/1024/1024)
}

// FormatWriteRecoveryContent 构造写类工具孤儿的合成结果正文。
//
// 对账 TS `formatWriteRecoveryContent`。`priorOccurrences` = 本会话历史里
// 已有的合成恢复结果数；**>= 2 时追加重复升级段**。
//
// # 四条分支（顺序敏感）
//
//  1. 非写类工具（或工具名未知）→ 通用文案
//  2. 磁盘证据确认**存在且非空** → 「[auto-recovered] 写入已确认」平静确认
//  3. 磁盘证据确认**不存在** → 「可安全重试」
//  4. 无证据 → 「很可能已成功」+ 先 read_file 确认的指引
func FormatWriteRecoveryContent(toolName, filePath string, evidence *WriteEvidence, priorOccurrences int) string {
	suffix := attribution
	if priorOccurrences >= 2 {
		suffix += repeatEscalation
	}

	if toolName == "" || !writeToolNamesForProbe[toolName] {
		return WriteRecoveryMarker + "——该工具可能已经成功执行。检查文件/缓冲区状态后再决定是否重试。" + suffix
	}

	target := "目标文件"
	if filePath != "" {
		target = "`" + filePath + "`"
	}

	// 分支 2：磁盘证据确认成功 → 平静确认优先（去惊吓化，采纳自公开仓库 PR #4）：
	// 开头给结论而非事故。marker 以括注保留在正文——计数靠包含判断，
	// 证据确认的恢复同样是一次宿主中断，不能漏计。
	if evidence != nil && evidence.Exists && evidence.Bytes > 0 {
		return "[auto-recovered] 写入已确认——磁盘证据：" + target + " 已存在（" + FormatBytes(evidence.Bytes) + "），写入已生效。" +
			"直接继续下一步，切勿重写。" +
			"\n（合成占位：" + WriteRecoveryMarker + "，已由磁盘证据确认写入生效。）" + suffix
	}

	// 分支 3：证据确认不存在 → 可安全重试。
	if evidence != nil && !evidence.Exists {
		return WriteRecoveryMarker + "——磁盘证据：" + target + " 当前不存在，写入未生效。可安全重试该写入。" + suffix
	}

	// 分支 4：无证据 → 保守指引。
	return WriteRecoveryMarker + "——对 " + target + " 的写入很可能已经成功执行，文件已保存到磁盘。" +
		"不要盲目重写：先 read_file " + target + " 确认当前内容，若已包含目标改动直接继续下一步；仅当确实缺失时才补写。" + suffix
}

// CountPriorRecoveries 数历史里已有的合成恢复结果数（任意格式）。
//
// 对账 TS `countPriorRecoveries`——**包含判断**（`includes(marker)`），
// 因为证据确认分支把 marker 放在括注里。
func CountPriorRecoveries(messages []session.OaiMessage) int {
	n := 0
	for _, m := range messages {
		if m.Role != "tool" || m.Content == nil {
			continue
		}
		if strings.Contains(*m.Content, WriteRecoveryMarker) {
			n++
		}
	}
	return n
}

// CreateWriteEvidenceProbe 构造 cwd 作用域的磁盘探测函数。
//
// 对账 TS `createWriteEvidenceProbe`——用 validatePathSafe + stat，**从不抛错**。
//
// **Go 侧简化**：路径校验复用 `ExtractTargetPath` 的产物并直接 stat；
// 越界保护由调用方（写工具自身）负责——此处只做存在性探测，不写盘。
func CreateWriteEvidenceProbe(cwd string) WriteProbe {
	return func(toolName string, args any) *WriteEvidence {
		if !writeToolNamesForProbe[toolName] {
			return nil
		}
		rel := ExtractTargetPath(args)
		if rel == "" {
			return nil
		}
		full := filepath.Join(cwd, rel)
		info, err := os.Stat(full)
		if err != nil {
			// 不存在或不可访问——返回「不存在」证据（分支 3）。
			return &WriteEvidence{Exists: false}
		}
		return &WriteEvidence{Exists: true, Bytes: info.Size()}
	}
}
