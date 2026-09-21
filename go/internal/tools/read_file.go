package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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
