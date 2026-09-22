package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// fileinfo.go —— 文件/目录元数据（不读内容）。
//
// 对账 TS `src/tools/file-info.ts`。

// fileInfoTextExts 对账 TS `TEXT_EXTENSIONS`（file-info.ts:135-155）。
var fileInfoTextExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".json": true, ".jsonl": true, ".json5": true,
	".md": true, ".mdx": true, ".txt": true, ".rst": true, ".adoc": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true,
	".py": true, ".rb": true, ".go": true, ".rs": true, ".java": true, ".kt": true,
	".c": true, ".cpp": true, ".h": true, ".hpp": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".css": true, ".scss": true, ".less": true, ".html": true, ".htm": true, ".svg": true,
	".xml": true, ".csv": true, ".tsv": true,
	".sql": true, ".graphql": true, ".proto": true,
	".lock": true, ".log": true, ".patch": true, ".diff": true,
	".env": true, ".gitignore": true, ".editorconfig": true,
}

// fileInfoTextNames 对账 TS `TEXT_FILENAMES`（file-info.ts:157-160）。
var fileInfoTextNames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true, "readme": true, "changelog": true,
	".gitignore": true, ".npmrc": true, ".editorconfig": true, ".env": true,
}

// isLikelyTextFile 对账 TS `isLikelyTextFile`（file-info.ts:162-166）。
func isLikelyTextFile(name, ext string) bool {
	if ext != "" && fileInfoTextExts[strings.ToLower(ext)] {
		return true
	}
	return fileInfoTextNames[strings.ToLower(name)]
}

// formatBytesFileInfo 对账 TS `formatBytes`（file-info.ts:168-174）。
//
// **精度规则**：`i === 0`（即字节）用 0 位小数，其余 1 位。
func formatBytesFileInfo(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB"}
	i := int(math.Floor(math.Log(float64(bytes)) / math.Log(1024)))
	if i > len(units)-1 {
		i = len(units) - 1
	}
	val := float64(bytes) / math.Pow(1024, float64(i))
	if i == 0 {
		return fmt.Sprintf("%.0f %s", val, units[i])
	}
	return fmt.Sprintf("%.1f %s", val, units[i])
}

// FormatPermissions 返回人类可读的权限。
//
// 对账 TS `formatPermissions`（file-info.ts:176-184）。**平台分叉**：
//   - Windows：mode 无 POSIX 位，故只报唯一真实的区分——**是否可写**
//     （`read-write` / `read-only`），而不是误导性的八进制串。
//   - 其他：`0` + 三位八进制。
//
// **`goos` 参数同时接受 `"windows"` 与 `"win32"`**——前者是 Go 的
// `runtime.GOOS` 拼写，后者是 JS 的 `process.platform` 拼写（TS 侧的默认
// 参数用它）。两者都认，使 Go 内部调用（传 `runtime.GOOS`）与差分对账
// （传 TS 的 `process.platform`）走同一分支。
//
// 导出供测试直接验证平台分支。
func FormatPermissions(mode os.FileMode, goos string) string {
	if goos == "windows" || goos == "win32" {
		if mode&0o200 != 0 {
			return "read-write"
		}
		return "read-only"
	}
	return fmt.Sprintf("0%o", mode&0o777)
}

// fileInfoDirScan 是目录扫描结果（对账 TS `DirScanResult`，file-info.ts:186-191）。
type fileInfoDirScan struct {
	fileCount int
	totalSize int64
	// truncated 表示撞到了上限——数字是**下界**而非总数。
	truncated bool
}

// 目录统计的边界（对账 TS `MAX_SCAN_FILES` / `MAX_SCAN_DEPTH`，file-info.ts:193-205）。
//
// **为什么需要**（逐字对账 TS 注释）：这里每个文件都要一次 `stat`，故无界遍历
// 会把一次 `file_info` 调用变成与树同深同宽的 syscall 数——指向本仓库的
// `desktop/src-tauri/target/debug/deps` 意味着 19433 次。摘要不必精确到有用，
// 但**必须能返回**。
const (
	fileInfoMaxScanFiles = 5000
	fileInfoMaxScanDepth = 12
)

// scanFileInfoDirectory 对账 TS `scanDirectory`（file-info.ts:207-243）。
func scanFileInfoDirectory(dir string) fileInfoDirScan {
	var res fileInfoDirScan

	var walk func(current string, depth int)
	walk = func(current string, depth int) {
		entries, err := os.ReadDir(current)
		if err != nil {
			return // 不可读目录——保留已有结果
		}
		for _, e := range entries {
			if res.fileCount >= fileInfoMaxScanFiles {
				res.truncated = true
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if e.IsDir() {
				// 跳过构建/依赖树是「摘要一个项目」与「摘要其编译输出」的区别。
				// 真正想要它们的调用方把它们作为**根**传入——那仍然可用；
				// 这里只在它们出现在**别的树内部**时剪枝。
				if IsScanExcludedDir(name) || depth >= fileInfoMaxScanDepth {
					res.truncated = true
					continue
				}
				walk(filepath.Join(current, name), depth+1)
			} else if e.Type().IsRegular() {
				res.fileCount++
				if info, statErr := os.Stat(filepath.Join(current, name)); statErr == nil {
					res.totalSize += info.Size()
				}
				// 不可读文件——已计数，但大小未知（对账 TS 的 catch）。
			}
		}
	}

	walk(dir, 0)
	return res
}

// fileInfoTool 是 file_info 工具实现。
type fileInfoTool struct {
	def     contract.Definition
	enabled bool
}

// FileInfo 构造 file_info 工具。
func FileInfo() Tool {
	t := &fileInfoTool{enabled: true}
	t.def = contract.Definition{
		Name: "file_info",
		Description: "获取文件或目录的元数据，不读取其内容。" +
			"\n\n返回：exists、type（file/directory/symlink）、size、修改时间、permissions、扩展名。" +
			"\n目录额外返回：文件数量和总大小。" +
			"\n用本工具替代 bash stat/ls/file 来检查路径是否存在、体积多大。" +
			"\n无需审批——只读，不启动子进程。",
		InputSchema: objSchemaOrdered([]string{"path"}, map[string]any{
			"path": strProp("文件或目录路径（绝对路径，或相对 cwd 的路径）"),
		}, "path"),
	}
	return t
}

func (t *fileInfoTool) Definition() contract.Definition { return t.def }
func (t *fileInfoTool) Enabled() bool                   { return t.enabled }
func (t *fileInfoTool) ConcurrencySafe() bool           { return true }
func (t *fileInfoTool) RequiresApproval(*CallParams) bool {
	return false
}
func (t *fileInfoTool) Timeout(*CallParams) time.Duration { return 30 * time.Second }

func (t *fileInfoTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	inputPath, _ := p.Input["path"].(string)
	inputPath = strings.TrimSpace(inputPath)
	if inputPath == "" {
		return contract.Result{Content: "错误：path 参数必填", IsError: true}, nil
	}

	validated := pathsafe.Validate(p.Cwd, inputPath, pathsafe.ModeRead, nil)
	if !validated.OK {
		// 对账 TS：项目外但**存在**的路径给出提示而非硬错（供 import_resource 引入）。
		resolved := inputPath
		if abs, err := filepath.Abs(inputPath); err == nil {
			resolved = abs
		}
		if _, statErr := os.Stat(resolved); statErr == nil {
			rel := relPosix(p.Cwd, resolved)
			return contract.Result{
				Content:   fmt.Sprintf("Path: %s\n注意：位于项目目录外——需要读取时先用 import_resource 引入。", rel),
				UIContent: rel + "（项目外）",
			}, nil
		}
		return contract.Result{Content: "错误：" + validated.Error, IsError: true}, nil
	}

	absPath := validated.Path

	ls, err := os.Lstat(absPath)
	if err != nil {
		rel := relPosix(p.Cwd, absPath)
		return contract.Result{
			Content:   fmt.Sprintf("Path: %s\nExists: false（路径不存在）", rel),
			UIContent: rel + " — 不存在",
		}, nil
	}

	relPath := relPosix(p.Cwd, absPath)
	ext := filepath.Ext(absPath)
	name := filepath.Base(absPath)

	fileType := "file"
	switch {
	case ls.IsDir():
		fileType = "directory"
	case ls.Mode()&os.ModeSymlink != 0:
		fileType = "symlink"
	}

	lines := []string{
		"Path: " + relPath,
		"Exists: true",
		"Type: " + fileType,
	}

	switch {
	case ls.Mode().IsRegular():
		lines = append(lines, "Size: "+formatBytesFileInfo(ls.Size()))
		if ext != "" {
			lines = append(lines, "Extension: "+ext)
		}
		lines = append(lines, "Modified: "+ls.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"))
		lines = append(lines, "Permissions: "+FormatPermissions(ls.Mode(), runtime.GOOS))
		lines = append(lines, "Encoding: "+boolToText(isLikelyTextFile(name, ext), "text", "binary"))

	case ls.IsDir():
		dirInfo := scanFileInfoDirectory(absPath)
		// **对账 TS**：把下界印成精确计数是**错答案**，不是「四舍五入的答案」
		// ——必须说明是哪一种。
		approx := ""
		if dirInfo.truncated {
			approx = "≥"
		}
		lines = append(lines, fmt.Sprintf("Files: %s%d", approx, dirInfo.fileCount))
		lines = append(lines, fmt.Sprintf("Total size: %s%s", approx, formatBytesFileInfo(dirInfo.totalSize)))
		if dirInfo.truncated {
			lines = append(lines, "Note: partial tally — build/dependency subtrees skipped, or the file/depth cap was reached.")
		}
		lines = append(lines, "Modified: "+ls.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"))

	default:
		// 符号链接（对账 TS 的第三个分支）。
		lines = append(lines, "Modified: "+ls.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"))
		if target, statErr := os.Stat(absPath); statErr == nil {
			targetType := "file"
			if target.IsDir() {
				targetType = "directory"
			}
			lines = append(lines, "Target type: "+targetType)
			lines = append(lines, "Target size: "+formatBytesFileInfo(target.Size()))
		} else {
			lines = append(lines, "Target: broken symlink")
		}
	}

	return contract.Result{Content: strings.Join(lines, "\n")}, nil
}

func boolToText(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
