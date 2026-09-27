package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// exportfile.go —— `export_file` 工具（第八十二刀）。
//
// 对账 TS `src/tools/export-file.ts`（112 行）。
//
// # 为什么这是「真接线」而非「造子系统」
//
// 依赖全是**平台能力**：`fs.mkdir/stat/copyFile/writeFile` + `path.dirname/resolve`
// + `expandHome` + `DetectSensitiveFile`——**Go 侧全部已有**（实测：`expandHome`
// 10 处命中、`DetectSensitiveFile` 8 处）。**零回调、零未移植子系统**。
//
// 对比 `ask_image`（依赖 `params.visionAsk` 回调，其实现体 Go 侧零命中）——
// 那种即使写了工具也永远走 fail-closed 分支，属造子系统。
//
// # 安全边界
//
// `source_path` 曾是 TS 侧**唯一不经敏感检测的读路径**（issue #135）——
// 可把 `~/.ssh/id_rsa`、`.env`、凭证文件直接复制出项目。故 copy 之前
// 先跑同一门禁。**这是本工具最重要的边界**：它堵的是「导出 = 外泄」这条路。

// exportMaxBytes 是导出的显式安全上限（对账 TS `MAX_EXPORT_BYTES`）。
//
// **为什么需要**：外部导出是「把数据搬出工作区」的动作，无上界会让
// 一次误操作搬运整个目录树。
const exportMaxBytes = 50 * 1024 * 1024 // 50MB

// ExportFile 创建 `export_file` 工具。
func ExportFile(cwd string) Tool { return &exportFileTool{cwd: cwd} }

type exportFileTool struct{ cwd string }

func (t *exportFileTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "export_file",
		Description: "将文件创建或复制到外部路径，如桌面、下载、挂载盘或 Windows 路径。\n\n" +
			"当用户明确要求将生成的资产放到项目工作区之外时使用。与 write_file 不同，" +
			"此工具面向用户可见的外部输出，且始终需要审批。\n\n" +
			"支持：\n" +
			"- 写文本内容：destination_path + content\n" +
			"- 写二进制内容：destination_path + content + encoding=\"base64\"\n" +
			"- 复制已有文件：destination_path + source_path\n\n" +
			"示例：\n" +
			"Good: export_file(destination_path=\"~/Desktop/tianshu-logo.svg\", content=\"<svg>...</svg>\")\n" +
			"Good: export_file(destination_path=\"H:\\\\zhuomian\\\\白嫖gpt\\\\logo.png\", source_path=\"/tmp/generated.png\")\n" +
			"Bad: 用 bash 重定向或 echo 往外部路径写文件",
		InputSchema: objSchemaOrdered(
			[]string{"destination_path", "content", "source_path", "encoding"},
			map[string]any{
				"destination_path": strProp("绝对路径或 ~ 相对路径。可在项目之外。"),
				"content":          strProp("要写入的文件内容。二进制文件用 encoding=\"base64\"。"),
				"source_path":      strProp("要复制到 destination_path 的已有文件。"),
				"encoding": enumPropOrdered(
					"content 的解码方式。默认 text。",
					[]string{"text", "base64"},
				),
			}, "destination_path"),
	}
}

func (t *exportFileTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	res, err := exportFileRun(p.Input)
	if err != nil {
		return contract.Result{Content: "错误：" + err.Error(), IsError: true}, nil
	}
	suffix := ""
	if res.mode == "copy" {
		suffix = "（已复制）"
	}
	return contract.Result{
		Content: fmt.Sprintf("已导出 %d 字节到 %s%s", res.bytes, res.path, suffix),
	}, nil
}

// RequiresApproval 恒 true（对账 TS `() => true`）。
//
// **为什么**：它把数据搬到工作区之外——必须走审批回合。
func (t *exportFileTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 false（对账 TS `() => false`）——导出是有序的副作用。
func (t *exportFileTool) ConcurrencySafe() bool { return false }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *exportFileTool) Enabled() bool { return true }

// Timeout 用默认（0）——纯本地文件操作。
func (t *exportFileTool) Timeout(_ *CallParams) time.Duration { return 0 }

// exportResult 是导出的结果（对账 TS 的返回结构）。
type exportResult struct {
	path  string
	bytes int64
	// mode 是 'content' | 'copy'——决定文案后缀。
	mode string
}

// exportFileRun 执行导出（提取以便单测，对账 TS 的 `exportFile` 函数）。
func exportFileRun(input map[string]any) (exportResult, error) {
	destinationPath := exportResolvePath(input["destination_path"])
	if destinationPath == "" {
		return exportResult{}, fmt.Errorf("destination_path 为必填项")
	}

	_, hasContent := input["content"].(string)
	hasSource := exportResolvePath(input["source_path"]) != ""
	// 对账 TS：`if (hasContent === hasSource) throw ...`——两者都给或都不给。
	if hasContent == hasSource {
		return exportResult{}, fmt.Errorf("必须且只能提供 content 或 source_path 之一")
	}

	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return exportResult{}, err
	}

	if hasContent {
		content, _ := input["content"].(string)
		encoding, _ := input["encoding"].(string)
		if encoding == "" {
			encoding = "text"
		}
		if encoding != "text" && encoding != "base64" {
			return exportResult{}, fmt.Errorf("encoding 必须为 text 或 base64")
		}
		var buf []byte
		if encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return exportResult{}, fmt.Errorf("base64 解码失败：%v", err)
			}
			buf = decoded
		} else {
			buf = []byte(content)
		}
		if int64(len(buf)) > exportMaxBytes {
			return exportResult{}, fmt.Errorf("导出过大（%.1fMB）；上限为 50MB",
				float64(len(buf))/(1024*1024))
		}
		if err := os.WriteFile(destinationPath, buf, 0o644); err != nil {
			return exportResult{}, err
		}
		return exportResult{path: destinationPath, bytes: int64(len(buf)), mode: "content"}, nil
	}

	sourcePath := exportResolvePath(input["source_path"])
	// **敏感文件硬门**（issue #135）：copy 之前先跑同一门禁。
	if sensitive := DetectSensitiveFile(sourcePath); sensitive.Sensitive {
		return exportResult{}, fmt.Errorf(
			"拒绝导出敏感文件（%s）：%s。敏感文件不允许复制到项目之外。",
			sensitive.PatternName, sourcePath)
	}
	sourceStat, err := os.Stat(sourcePath)
	if err != nil {
		return exportResult{}, err
	}
	if !sourceStat.Mode().IsRegular() {
		return exportResult{}, fmt.Errorf("source_path 必须是文件")
	}
	if sourceStat.Size() > exportMaxBytes {
		return exportResult{}, fmt.Errorf("导出过大（%.1fMB）；上限为 50MB",
			float64(sourceStat.Size())/(1024*1024))
	}
	if err := copyFileContents(sourcePath, destinationPath); err != nil {
		return exportResult{}, err
	}
	destStat, err := os.Stat(destinationPath)
	if err != nil {
		return exportResult{}, err
	}
	return exportResult{path: destinationPath, bytes: destStat.Size(), mode: "copy"}, nil
}

// exportResolvePath 解析输入里的路径：trim + `~` 展开 + 绝对化。
//
// 对账 TS `getDestinationPath` / `getSourcePath`：
// `resolve(expandHome(raw.trim()))`；非字符串或空 → 返回空（TS 返回 null）。
func exportResolvePath(v any) string {
	raw, ok := v.(string)
	if !ok {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	expanded := expandHome(raw)
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return filepath.Clean(expanded)
	}
	return abs
}

// copyFileContents 复制文件内容（对账 TS 的 `copyFile`）。
//
// **为什么不用 os.Link/rename**：跨文件系统时那些会失败（导出目标常在
// 挂载盘/不同卷）。逐字节复制是最稳的语义，与 Node 的 `fs.copyFile` 一致。
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
