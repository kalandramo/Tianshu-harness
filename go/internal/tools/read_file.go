package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kalandramo/tianshu/go/internal/artifact"
	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// readFileTool 实现 read_file。
//
// 语义要点（对账 src/tools/read-file.ts）：
//   - 路径经 pathsafe 校验（fail-closed 逃逸与敏感文件拦截）
//   - 二进制文件拒绝（不是把乱码喂给模型）
//   - 超长内容截断并标记 Lossiness（lossy 观测不能支撑负向结论）
//   - offset/limit 是**已知子区间**的手段，不是长文件的绕行
type readFileTool struct {
	baseTool
	// Cwd 是工作目录（构造时绑定）。
	Cwd string
	// Grants 是越界路径授权判定。
	Grants pathsafe.GrantChecker
}

// ReadFile 构造 read_file 工具。
func ReadFile(cwd string, grants pathsafe.GrantChecker) Tool {
	t := &readFileTool{Cwd: cwd, Grants: grants}
	t.def = contract.Definition{
		Name: "read_file",
		Description: `从文件系统读取文件，支持可选的行范围。

- 约 50,000 行以内的文件完整返回——不要自己切成小片分多次读
- offset/limit 只用于已知子区间（如第 800-900 行），不要拿它当长文件的绕行手段
- 输出可能被截断（标记 [output truncated]）——**截断的观测不能支撑负向结论**
  （「我没看到 X」≠「X 不存在」），需换 grep 或分段读确认`,
		InputSchema: objSchemaOrdered([]string{
			"file_path", "file_paths", "offset", "limit", "focus", "focus_max_matches",
		}, map[string]any{
			"file_path": strProp("文件的绝对路径"),
			"file_paths": arrayPropOrdered(
				"一次调用读取多个文件。用于替代重复的 read_file 调用。每个文件单独成节。最多 5 个文件。",
				"string"),
			"offset":            intProp("起始读取行号（从 1 开始）"),
			"limit":             intProp("最多读取的行数"),
			"focus":             strProp("任务关键词或问题；只返回结构摘要和相关片段"),
			"focus_max_matches": intProp("focus 最多返回的片段数量（默认 8）"),
		}, "file_path"),
	}
	t.enabled = true
	t.concurrent = true
	return t
}

func (t *readFileTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *readFileTool) Execute(ctx context.Context, p *CallParams) (contract.Result, error) {
	// ── 多读分支（对账 TS `read-file.ts:756-762` + `handleMultiRead:1063`）──
	//
	// **修复的缺陷**：`file_paths` 此前只出现在 InputSchema 里（**声明但未实现**
	// ——与 `focus` 同类的静默失效）：传 file_paths 时 `file_path` 为空 → 报
	// 「read_file 需要 path 参数」，而模型以为自己请求了多文件读取。
	//
	// **上限 5**（对账 TS 的 `filePaths.slice(0, 5)`）。
	if raw, ok := p.Input["file_paths"]; ok {
		if paths := toStringSlice(raw); len(paths) > 0 {
			return t.executeMultiRead(ctx, p, paths)
		}
	}

	path, _ := p.Input["file_path"].(string)
	if path == "" {
		return contract.Result{
			Content: "read_file 需要 path 参数",
			IsError: true,
		}, nil
	}

	// 路径校验——fail-closed
	vr := pathsafe.Validate(t.Cwd, path, pathsafe.ModeRead, &pathsafe.Options{Grants: t.Grants})
	if !vr.OK {
		return contract.Result{Content: vr.Error, IsError: true}, nil
	}

	info, err := os.Stat(vr.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return contract.Result{
				Content: fmt.Sprintf("文件不存在：%s", path),
				IsError: true,
			}, nil
		}
		return contract.Result{Content: fmt.Sprintf("无法访问：%v", err), IsError: true}, nil
	}
	if info.IsDir() {
		return contract.Result{
			Content: fmt.Sprintf("%s 是目录，不是文件。用 glob 列出目录内容。", path),
			IsError: true,
		}, nil
	}

	data, err := os.ReadFile(vr.Path)
	if err != nil {
		return contract.Result{Content: fmt.Sprintf("读取失败：%v", err), IsError: true}, nil
	}

	// 二进制检测：不把乱码喂给模型
	if isBinary(data) {
		return contract.Result{
			Content: fmt.Sprintf(
				"%s 看起来是二进制文件（%d 字节）。read_file 只读文本；如需查看请用 bash 的 file/xxd 等工具。",
				path, len(data)),
			IsError: true,
		}, nil
	}

	text := string(data)
	lines := strings.Split(text, "\n")

	// ── focus 分支（对账 TS `read-file.ts:780,931-948`）──
	//
	// **条件**：focus 非空 **且** 未给 offset/limit（TS 的 `focusedRead`）。
	// 有显式范围时 focus 不生效——范围读取是更精确的意图表达。
	//
	// **修复的缺陷**：此前 Go 侧声明了 `focus`/`focus_max_matches` 参数但
	// **完全未实现**——传 focus 时静默返回全文件，模型以为拿到聚焦结果。
	// 静默失效比未移植更糟：模型基于错误前提推理。
	//
	// **不在此 return**（对账 TS）：TS 的 focus 分支只决定 `modelContent`，
	// `rawContent` 仍是**全文**，外层 L0 用全文长度判阈值——故大文件聚焦读取
	// **仍落盘全文 + artifact 标记**。若此处提前 return，会绕过 L0（审查发现
	// 的真实偏差）。
	focus := strings.TrimSpace(strArg(p.Input, "focus"))
	hasExplicitRange := inputProvided(p.Input, "offset") || inputProvided(p.Input, "limit")
	focusedContent := ""
	if focus != "" && !hasExplicitRange {
		maxChars := ComputeModelReadCap(ModelReadCapInput{
			ContextWindow:   p.ContextWindow,
			ProviderProfile: p.ProviderProfile,
		}).MaxChars
		// 多读分支：用均分后的 per-file cap（对账 TS 的 `perFileCap`）。
		if p.perFileCapMax > 0 {
			maxChars = p.perFileCapMax
		}
		res := BuildFocusedReadView(FocusedReadOptions{
			FilePath:     path,
			Content:      text,
			Focus:        focus,
			MaxChars:     maxChars,
			MaxMatches:   intArg(p.Input, "focus_max_matches", defaultMaxMatches),
			ContextLines: defaultContextLines,
		})
		focusedContent = res.Content
	}

	// offset/limit
	offset := intArg(p.Input, "offset", 1)
	limit := intArg(p.Input, "limit", 0)
	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	selected := lines[start:end]
	body := strings.Join(selected, "\n")

	// 截断（标记 lossiness——截断观测不能支撑负向结论）。
	//
	// **对账 TS**：TS 的 `modelContent = truncateContent(content, cap.maxChars,
	// cap.headChars, cap.tailChars)`（`read-file.ts:707`）——**头+尾截断**，
	// 而非只保留头部。cap 来自 `ComputeModelReadCap`（窗口感知 + 提供商策略
	// 系数 + 120K 硬上限），不是静态阈值。
	//
	// **此前的近似**：`MaxBytes` 硬编码 100_000 + 只保留头部。两处偏差：
	//   1. 不随上下文窗口缩放（1M 窗口下 TS 给 120K）
	//   2. 丢弃尾部（TS 保留 head + tail，尾部常含总结/错误）
	var lossiness *contract.Lossiness
	if len(body) > 0 {
		cap := ComputeModelReadCap(ModelReadCapInput{
			ContextWindow:   p.ContextWindow,
			ProviderProfile: p.ProviderProfile,
		})
		// 多读分支的 per-file cap 覆盖（对账 TS 的 `perFileCap`）。
		if p.perFileCapMax > 0 {
			cap.MaxChars = p.perFileCapMax
			cap.HeadChars = p.perFileCapHead
			cap.TailChars = p.perFileCapTail
		}
		if UTF16Len(body) > cap.MaxChars {
			// 对账 TS 的 `truncateContent`：head + 提示 + tail（UTF-16 语义）。
			body = TruncateContent(body, cap.MaxChars, cap.HeadChars, cap.TailChars)
			l := contract.LossinessTruncated
			lossiness = &l
			body += fmt.Sprintf(
				"\n\n[output truncated: 文件共 %d 行 / %d 字节。用 offset/limit 读后续区间。]",
				len(lines), len(data))
		}
	}

	content := body
	if limit > 0 || offset > 1 {
		content = fmt.Sprintf("[%s 第 %d-%d 行，共 %d 行]\n%s",
			path, start+1, end, len(lines), body)
	}
	// focus 生效时，**模型可见内容**换成聚焦视图（对账 TS 的 `modelContent`）。
	// **但 L0 仍用全文 `text` 判阈值并落盘全文**——这正是不在此提前 return 的原因。
	if focusedContent != "" {
		content = focusedContent
	}

	// ── L0 artifact 包装（对账 TS `read-file.ts:992-1042`）──
	//
	// **为什么在这一层**：read_file 在 `l0WrappedTools` 里，L1 会跳过它
	// （防无限嵌套 + double-save）。故大结果必须由**工具自己**落盘。
	//
	// **两处偏差（此前 Go 侧完全无 L0）**：读大文件时既不入 store、也没有
	// structural outline，只能靠截断——模型看到的是截断原文；TS 侧给的是
	// outline + `[artifact:id]`。
	if p.ArtifactStore != nil {
		threshold := artifact.ToolArtifactThreshold("read_file", p.ContextWindow)
		if UTF16Len(text) >= threshold {
			res := artifact.SummarizeFileContent(text, path)
			id, err := p.ArtifactStore.Save(artifact.SaveInput{
				Tool: "read_file", Target: path,
				RawContent: text, // **原文**（不是截断后的 content）
				Summary:    res.Summary,
				Sections:   res.Sections,
			})
			if err == nil {
				// 标记**必须在末尾**——`ArtifactMarkerRegex` 依赖此位置。
				summaryBlock := ""
				if s := strings.TrimSpace(res.Summary); s != "" {
					summaryBlock = "\n\n── Structural outline ──\n" + s
				}
				return contract.Result{
					Content:   content + summaryBlock + "\n[artifact:" + id + "]",
					Lossiness: lossiness,
				}, nil
			}
			// Save 失败 → 优雅降级（返回未包装内容，对账 TS 的 try/catch）。
		}
	}

	return contract.Result{
		Content:   content,
		Lossiness: lossiness,
	}, nil
}

// isBinary 判定内容是否为二进制。
//
// 判据：前 8KB 内出现 NUL 字节，或非 UTF-8 有效序列占比过高。
func isBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	for _, b := range probe {
		if b == 0 {
			return true
		}
	}
	// UTF-8 有效性（允许少量损坏，但大面积无效即为二进制）
	if !utf8.Valid(probe) {
		// 统计无效字节占比
		invalid := 0
		for i := 0; i < len(probe); {
			r, size := utf8.DecodeRune(probe[i:])
			if r == utf8.RuneError && size == 1 {
				invalid++
			}
			i += size
		}
		if invalid*10 > len(probe) {
			return true
		}
	}
	return false
}

// inputProvided 复刻 TS 的 `params.input.X !== undefined` 语义。
//
// **为什么不能直接用 `p.Input[k] != nil`**：JSON 里显式 `null` 会被解码成
// Go 的 `nil`，与「键缺失」不可区分；而 TS 的 `null !== undefined` 为 **true**
// （null 算「提供了」）。故必须查**键是否存在**，而非值是否为 nil。
//
// 探针实测：`{"offset": null}` 在 TS 里使 `hasExplicitRange = true`（focus 不生效），
// 用 `!= nil` 则判为 false（focus 生效）——语义漂移。
func inputProvided(input map[string]any, key string) bool {
	_, ok := input[key]
	return ok
}

// intArg 从入参取整数（容忍 float64 —— JSON 解码的默认形态）。
func intArg(input map[string]any, key string, def int) int {
	v, ok := input[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return def
	}
}

// strArg 从入参取字符串。
func strArg(input map[string]any, key string) string {
	v, _ := input[key].(string)
	return v
}

// boolArg 从入参取布尔。
func boolArg(input map[string]any, key string) bool {
	v, _ := input[key].(bool)
	return v
}

// relLabel 返回相对工作目录的标签（供 UI 与错误信息用）。
func relLabel(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// toStringSlice 把 JSON 数组（解码为 []any）转为 []string。
//
// 非字符串元素跳过（对账 TS 的 `as string[]` 宽松断言——非法元素在 TS 里
// 会在后续 trim() 时抛错，Go 侧跳过更稳）。
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		// 已显式构造为 []string 时（测试/内部调用）。
		if ss, ok2 := v.([]string); ok2 {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// executeMultiRead 逐文件读取并拼接（对账 TS `handleMultiRead`）。
//
// **首版不接 dedup**（`fileReadHistory` 属未移植的 dedup 子系统）——它对账的是
// 「记录」而非模型可见输出，缺它不影响本函数的可见行为。`NoteFileObserved`
// 已移植（`filestate.go:54`），照 TS 调用。
func (t *readFileTool) executeMultiRead(ctx context.Context, p *CallParams, paths []string) (contract.Result, error) {
	// cap 按文件数**均分**（对账 TS 的 `Math.floor(computedCap.maxChars / paths.length)`）。
	n := len(paths)
	capFull := ComputeModelReadCap(ModelReadCapInput{
		ContextWindow:   p.ContextWindow,
		ProviderProfile: p.ProviderProfile,
	})
	perFileMax := capFull.MaxChars / n
	perFileHead := capFull.HeadChars / n
	perFileTail := capFull.TailChars / n

	// 上限 5（对账 TS 的 `slice(0, 5)`）。
	if len(paths) > 5 {
		paths = paths[:5]
	}

	sections := []string{}
	totalBytes := 0
	errors := 0

	for _, rawPath := range paths {
		trimmed := strings.TrimSpace(rawPath)
		if trimmed == "" {
			continue
		}
		// 复用单读逻辑：构造子 CallParams（设 file_path、清 file_paths）。
		sub := *p
		sub.Input = map[string]any{}
		for k, v := range p.Input {
			if k == "file_paths" {
				continue
			}
			sub.Input[k] = v
		}
		sub.Input["file_path"] = trimmed
		// cap 均分：用 ContextWindow 无法精确表达 per-file cap，
		// 故走一个显式的覆盖通道。
		sub.perFileCapMax = perFileMax
		sub.perFileCapHead = perFileHead
		sub.perFileCapTail = perFileTail

		res, err := t.Execute(ctx, &sub)
		canonical := ""
		if vr := pathsafe.Validate(t.Cwd, trimmed, pathsafe.ModeRead, &pathsafe.Options{Grants: t.Grants}); vr.OK {
			canonical = vr.Path
		}

		if err != nil || res.IsError {
			// 错误节（对账 TS 的 catch 分支）。
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = res.Content
			}
			display := trimmed
			// 只有以 cwd 开头时才转相对（对账 TS 注释：避免 relative() 把无关
			// 路径变成一串 `../..`）。
			if strings.HasPrefix(trimmed, t.Cwd) {
				display = tsRelative(t.Cwd, trimmed)
			}
			sections = append(sections, "── "+display+" ──\nError: "+msg)
			errors++
			continue
		}

		relPath := tsRelative(t.Cwd, canonical)
		sections = append(sections, "── "+relPath+" ──\n"+res.Content)
		totalBytes += len(res.Content)

		// 记录文件观察（对账 TS 的 noteFileObserved）。
		if canonical != "" {
			if fi, serr := os.Stat(canonical); serr == nil {
				NoteFileObserved(canonical, fi.ModTime().UnixMilli(), fi.Size(), p.SessionID)
			}
		}
	}

	return contract.Result{
		Content:   strings.Join(sections, "\n\n"),
		UIContent: multiReadUI(len(paths), errors, totalBytes),
	}, nil
}

// tsRelative 对账 TS 的 `relative(cwd, p).replaceAll('\', '/')`。
//
// **与 relLabel 的区别**：relLabel 在 rel 以 `..` 开头时回退原路径；TS 的
// `relative` 不做此判断，且**总是**把反斜杠转正斜杠。
func tsRelative(cwd, p string) string {
	rel, err := filepath.Rel(cwd, p)
	if err != nil {
		rel = p
	}
	return strings.ReplaceAll(rel, "\\", "/")
}

// multiReadUI 对账 TS 的 `Read N/M files (X.X KB total)`。
func multiReadUI(total, errors, bytes int) string {
	kb := float64(bytes) / 1024.0
	return fmt.Sprintf("Read %d/%d files (%.1f KB total)", total-errors, total, kb)
}
