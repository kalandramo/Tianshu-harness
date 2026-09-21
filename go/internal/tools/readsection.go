package tools

// readsection.go —— read_section 工具（artifact 召回路径）。
//
// 对账 TS `src/tools/read-section.ts` 的 `READ_SECTION_TOOL`。
//
// # 作用
//
// 大工具结果被 artifact 拦截后，消息历史里只剩一行摘要引用
// `[artifact:ID] ... Use read_section(...)`。模型需要原文细节时用它按
// **行范围或字符范围**取回。
//
// # 本刀范围（有意收窄）
//
// 只实现 **artifactId 分支**（artifact 召回）。
//
// **未移植 file_path 分支**：TS 还支持 `read_section(file_path=...)` 从
// 磁盘活动文件读——它依赖 `getFileReadMtime`（本会话读过的文件 mtime 表，
// 用于陈旧性告警）与 `computeModelReadCap`（按窗口/提供商算读上限）。
// 这两个模块 Go 侧尚未移植，故 file_path 分支留待后续刀。
//
// **未移植 compact-history 快速路径**：那是 compact 归档（`COMPACT_HISTORY_TOOL`）
// 的专用流式读取 + recall 标记，依赖 `recall-marker.ts`。

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	ctxstore "github.com/kalandramo/tianshu/go/internal/context"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// maxRawBytes 是单个 artifact 原文的内存读取上限（对账 TS 的 2MB）。
//
// **为什么设限**：更大的文件会造成内存压力并在重复 read_section 时卡顿——
// 此时应让模型改用 grep 或 bash + head/tail。
const maxRawBytes = 2 * 1024 * 1024

// legacyMaxSectionChars 是 read_section 输出的硬地板（对账 TS 的 8000）。
//
// **背景**（TS 注释记录的真实回归）：read_section 写就时 read_file 上限是
// 8K，故 8K 对称。后来 read_file 在 1M 窗口上放大到 ~200K，而 read_section
// 仍是硬编码 8K——于是 prune 提示「用 read_section 恢复」时，模型只能从
// 60K 文件里取回 8K，**可见地卡住**（模型自述「结果被截到 8030 字符」）。
const legacyMaxSectionChars = 8000

// lineRangeRe / charRangeRe 对账 TS 的 parseLineRange / parseCharRange。
var (
	lineRangeRe = regexp.MustCompile(`(?i)^L?(\d+)-L?(\d+)$`)
	charRangeRe = regexp.MustCompile(`(?i)^c(\d+)-c(\d+)$`)
)

// parseLineRange 解析 "L100-L200" 或 "100-200"。
//
// 对账 TS：start < 1 或 end < start 时返回 nil（无效）。
func parseLineRange(s string) (start, end int, ok bool) {
	m := lineRangeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	st, _ := strconv.Atoi(m[1])
	en, _ := strconv.Atoi(m[2])
	if st < 1 || en < st {
		return 0, 0, false
	}
	return st, en, true
}

// parseCharRange 解析 "c0-c5000"。
//
// 对账 TS：start < 0 或 end < start 时返回 nil。
func parseCharRange(s string) (start, end int, ok bool) {
	m := charRangeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	st, _ := strconv.Atoi(m[1])
	en, _ := strconv.Atoi(m[2])
	if st < 0 || en < st {
		return 0, 0, false
	}
	return st, en, true
}

// extractSection 按行范围或字符范围切出内容。
//
// 对账 TS `extractSection`：
//   - 行范围：`lines.slice(start-1, min(end, len))`；start 越界返回
//     越界提示（**不是空串**）。
//   - 字符范围：`content.slice(min(start,len), min(end,len))`。
//   - 都不匹配：无效格式提示。
func extractSection(rawContent, sectionID string) string {
	if st, en, ok := parseLineRange(sectionID); ok {
		lines := strings.Split(rawContent, "\n")
		startIdx := st - 1
		if startIdx >= len(lines) {
			return fmt.Sprintf("[区段 %s 超出范围 — 文件共 %d 行]", sectionID, len(lines))
		}
		endIdx := en
		if endIdx > len(lines) {
			endIdx = len(lines)
		}
		return strings.Join(lines[startIdx:endIdx], "\n")
	}
	if st, en, ok := parseCharRange(sectionID); ok {
		// 对账 TS 的 slice 语义：越界即截断。
		start := st
		if start > len(rawContent) {
			start = len(rawContent)
		}
		end := en
		if end > len(rawContent) {
			end = len(rawContent)
		}
		if end < start {
			end = start
		}
		return rawContent[start:end]
	}
	return fmt.Sprintf("[无效的区段格式：%s。行范围用 \"L100-L200\"，字符范围用 \"c0-c5000\"]", sectionID)
}

// readSectionTool 实现 read_section。
type readSectionTool struct {
	def contract.Definition
	// Cwd 是工作目录（file_path 分支的路径校验基准）。
	Cwd string
	// Grants 是越界路径授权判定（对账 TS `validatePath(params.cwd, file_path)`）。
	Grants pathsafe.GrantChecker
	// ContextWindow 是默认上下文窗口（调用方可按次覆盖）。
	//
	// 对账 TS 的 `params.contextWindow`——Go 侧 CallParams 也带该字段，
	// 但工具装配时的默认值在此（供未传参的调用方）。
	ContextWindow int
	// session 是会话 ID（mtime 表按会话隔离）。
	session string
}

// ReadSection 构造 read_section 工具。
//
// 对账 TS `READ_SECTION_TOOL`。cwd/grants 供 `file_path` 分支做路径校验
// （对账 TS `validatePath(params.cwd, file_path)`）。
func ReadSection(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &readSectionTool{Cwd: cwd, Grants: grants}
	t.def = contract.Definition{
		Name: "read_section",
		Description: `从之前保存的 artifact 中读取指定区段。

### 用法
- 用于加载消息历史中被摘要化的 artifact 输出的细节
- 需要 artifactId——区段格式支持行范围（L100-L200）和字符范围（c0-c5000）

### 示例
好：read_section(artifactId="abc123", section="L1-L500")
好：read_section(artifactId="abc123", section="c0-c50000")`,
		InputSchema: objSchemaOrdered([]string{"artifactId", "section"}, map[string]any{
			"artifactId": strProp("先前 tool_result 中的 artifact ID"),
			"section":    strProp("要读取的区段：\"L100-L200\" 表示第 100-200 行，\"c0-c5000\" 表示字符范围"),
		}, "section"),
	}
	return t
}

func (t *readSectionTool) Definition() contract.Definition { return t.def }

func (t *readSectionTool) RequiresApproval(*CallParams) bool { return false }
func (t *readSectionTool) ConcurrencySafe() bool             { return true }
func (t *readSectionTool) Enabled() bool                     { return true }
func (t *readSectionTool) Timeout(*CallParams) time.Duration { return 0 }

// Execute 实现取回逻辑。
//
// 对账 TS 的 artifactId 分支。**错误文案逐字对账**。
func (t *readSectionTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	artifactId, _ := p.Input["artifactId"].(string)
	filePath, _ := p.Input["file_path"].(string)
	section, _ := p.Input["section"].(string)

	if section == "" {
		return contract.Result{Content: "错误：需要提供 section", IsError: true}, nil
	}

	_, _, isLine := parseLineRange(section)
	_, _, isChar := parseCharRange(section)
	if !isLine && !isChar {
		return contract.Result{
			Content: fmt.Sprintf("错误：无效的区段格式：%s。行范围用 \"L100-L200\"，字符范围用 \"c0-c5000\"。", section),
			IsError: true,
		}, nil
	}

	if artifactId == "" && filePath == "" {
		return contract.Result{Content: "错误：需要提供 artifactId 或 file_path", IsError: true}, nil
	}

	// ── file_path 分支：从磁盘活动文件读（对账 TS 的 "B3" 分支）──
	//
	// 用途：配合 read-ref 引用恢复本会话早前读过的文件内容。
	// **必须置于 artifactId 分支之前**（对账 TS 的 `if (file_path && !artifactId)`）。
	if filePath != "" && artifactId == "" {
		return t.readFromDisk(filePath, section)
	}

	store := p.ArtifactStore
	if store == nil {
		return contract.Result{Content: "错误：当前会话未配置 artifactStore", IsError: true}, nil
	}

	a := store.Get(artifactId)
	if a == nil {
		return contract.Result{
			Content: fmt.Sprintf("错误：未找到 Artifact %s——可能已被清理或从未创建。请用原始工具（bash/read_file/grep）重新生成输出。", artifactId),
			IsError: true,
		}, nil
	}

	// ── compact-history 召回快速路径 ──
	//
	// 对账 TS（`read-section.ts` 的 "Compact-history recall fast path"）：
	// **长线程归档常超下面的 2MB 内存上限**——那会让归档自己的目录项**无法
	// 被召回**（存得下、取不回）。故对行范围走**流式**读取：只取请求的行，
	// 不把整个文件载入内存，也**不过 2MB 闸门**。
	//
	// **只对行范围生效**：字符范围需要全文（无法流式定位），故落回下方通用路径。
	//
	// ⚠️ **必须在 2MB 守卫之前**——那正是本分支存在的理由。
	if a.Tool == ctxstore.CompactHistoryTool {
		if start, end, isLine := parseLineRange(section); isLine {
			ranged, err := store.ReadLineRange(artifactId, start, end)
			if err != nil {
				return contract.Result{
					Content: fmt.Sprintf("错误：读取 artifact %s 失败：%v", artifactId, err),
					IsError: true,
				}, nil
			}
			if ranged == nil {
				return contract.Result{
					Content: fmt.Sprintf("错误：未找到 Artifact %s。", artifactId),
					IsError: true,
				}, nil
			}
			// 请求起点越界 → 报总行数（**不是错误**——对账 TS 的 isError: false）。
			if len(ranged.Content) == 0 && start > ranged.TotalLines {
				return contract.Result{
					Content: fmt.Sprintf("[区段 %s 超出范围 — artifact 共 %d 行]", section, ranged.TotalLines),
				}, nil
			}
			body := ranged.Content
			maxChars := readSectionMaxChars(p.ContextWindow)
			if len(body) > maxChars {
				body = body[:maxChars] + fmt.Sprintf("\n... [已截断至 %d 字符]", maxChars)
			}
			if ranged.Capped {
				body += fmt.Sprintf("\n... [范围已限制为 %d 行 — 请缩小范围分页读取]", artifact.MaxRangeLines)
			}
			return contract.Result{
				// 前置召回标记——让**下一次压缩**能把这块折叠回指针
				// （recall-eviction，见 context.RenderArchiveBody 的 tool 分支）。
				Content: ctxstore.BuildRecallMarker(artifactId, section) + "\n" + body,
				RawPath: a.RawPath,
			}, nil
		}
	}

	// 内存读取上限守卫——**先看文件大小再读**，避免把巨型文件载入内存。
	if fi, err := os.Stat(a.RawPath); err == nil && fi.Size() > maxRawBytes {
		return contract.Result{
			Content: fmt.Sprintf(
				"错误：Artifact %s 原始文件过大（%.1fMB > 2MB 上限）。请对原始输出用 grep，或用 bash 配合 head/tail 直接查看。",
				artifactId, float64(fi.Size())/1024/1024),
			IsError: true,
		}, nil
	}

	raw, err := store.ReadRaw(artifactId)
	if err != nil {
		// SHA-256 不符 → 损坏提示（对账 TS 的 ArtifactCorruptionError 分支）。
		if _, ok := err.(*artifact.CorruptionError); ok {
			return contract.Result{
				Content: fmt.Sprintf("错误：Artifact %s 磁盘数据已损坏（SHA-256 不匹配）。请重新读取源内容。", artifactId),
				IsError: true,
			}, nil
		}
		return contract.Result{
			Content: fmt.Sprintf("错误：读取 artifact %s 失败：%v", artifactId, err),
			IsError: true,
		}, nil
	}
	if raw == "" {
		// ReadRaw 对未知 id 返回 ("", nil)——但上面已 Get 过，故这里是文件缺失。
		return contract.Result{
			Content: fmt.Sprintf("错误：Artifact %s 的原始文件在磁盘上缺失（%s）。可能已被清理。请用原始工具重新生成输出。", artifactId, a.RawPath),
			IsError: true,
		}, nil
	}

	sectionContent := extractSection(raw, section)
	maxChars := readSectionMaxChars(p.ContextWindow)
	truncated := sectionContent
	if len(sectionContent) > maxChars {
		truncated = sectionContent[:maxChars] + fmt.Sprintf("\n... [已截断至 %d 字符]", maxChars)
	}

	return contract.Result{Content: truncated, RawPath: a.RawPath}, nil
}

// readFromDisk 实现 file_path 分支：从磁盘活动文件按区段读取。
//
// 对账 TS `read-section.ts` 的 file_path 分支（"B3"）。**顺序敏感**：
//
//  1. 路径校验（fail-closed）
//  2. **陈旧性检查**：mtime 与上次观察不符 → 前置告警
//  3. 大文件守卫（>2MB → 报错并建议 grep/head）
//  4. 读取 + 切区段
//  5. 按 `computeModelReadCap` 截断（**不是** artifact 阈值近似）
func (t *readSectionTool) readFromDisk(filePath, section string) (contract.Result, error) {
	// 1) 路径校验——fail-closed。
	vr := pathsafe.Validate(t.Cwd, filePath, pathsafe.ModeRead, &pathsafe.Options{Grants: t.Grants})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}
	canonical := vr.Path

	// 2) 陈旧性检查（对账 TS）：文件自上次 read_file 后变更过 → 告警。
	//    **先 stat 再判**——stat 失败不报错（文件可能不存在，交给下一步）。
	stalenessNote := ""
	var sizeBytes int64
	if fi, err := os.Stat(canonical); err == nil {
		sizeBytes = fi.Size()
		if lastMtime, ok := GetFileReadMtime(canonical, t.sessionID()); ok {
			if fi.ModTime().UnixMilli() != lastMtime {
				stalenessNote = "\n⚠ 文件自上次 read_file 后已变更（mtime 不匹配）。以下内容为当前磁盘版本，可能与上文不一致。\n"
			}
		}
	}

	// 3) 大文件守卫（对账 TS 的 `_rawSize > MAX_RAW_BYTES`）。
	if sizeBytes > maxRawBytes {
		return contract.Result{
			Content: fmt.Sprintf(
				"错误：文件 %s 过大（%.1fMB > %dMB 上限）。请用 grep 或 bash 配合 head/tail 直接查看。",
				canonical, float64(sizeBytes)/1024/1024, maxRawBytes/1024/1024),
			IsError: true,
		}, nil
	}

	// 4) 读取 + 切区段。
	raw, err := os.ReadFile(canonical)
	if err != nil {
		return contract.Result{
			Content: fmt.Sprintf("错误：读取文件失败：%v", err),
			IsError: true,
		}, nil
	}
	sectionContent := extractSection(string(raw), section)

	// 5) 按模型读上限截断（对账 TS 的 `Math.max(cap.maxChars, LEGACY...)`）。
	maxChars := readSectionMaxChars(t.ContextWindow)
	truncated := sectionContent
	if len(sectionContent) > maxChars {
		truncated = sectionContent[:maxChars] + fmt.Sprintf("\n... [已截断至 %d 字符]", maxChars)
	}

	return contract.Result{
		Content: stalenessNote + truncated,
		RawPath: canonical,
	}, nil
}

// sessionID 返回工具装配时的会话 ID（file_path 分支的 mtime 表按会话隔离）。
//
// **注意**：`CallParams.SessionID` 是**按次调用**的会话 ID，此处用工具装配
// 时的值——两者在正常装配下一致；差异只在测试手工构造 CallParams 时出现。
func (t *readSectionTool) sessionID() string { return t.session }

// readSectionMaxChars 返回 read_section 的单次输出上限（字符）。
//
// 对账 TS：`Math.max(computeModelReadCap(...).maxChars, LEGACY_MAX_SECTION_CHARS)`。
//
// **已消掉此前的近似偏差**：首版用 `artifact.ToolArtifactThreshold("read_file",
// ...)` 近似（注释里标注「移植 computeModelReadCap 后应替换」）——现改用真实
// 的 `ComputeModelReadCap`，含提供商策略系数与 120K 硬上限。
func readSectionMaxChars(contextWindow int) int {
	cap := ComputeModelReadCap(ModelReadCapInput{ContextWindow: contextWindow})
	if cap.MaxChars < legacyMaxSectionChars {
		return legacyMaxSectionChars
	}
	return cap.MaxChars
}
