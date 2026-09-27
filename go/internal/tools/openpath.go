package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// openpath.go —— `open_path` 工具（第八十四刀 · W2-1）。
//
// 对账 TS `src/tools/open-path.ts`（213 行）。
//
// # 为什么这是「接线」
//
// 依赖只有 `os/exec`（对账 `node:child_process`）、`os`（对账 `node:fs`）、
// `path/filepath`（对账 `node:path`）+ `expandHome`（Go 侧已有）。
// **门链早已预留**：`approval_pathgrant.go:125` 的 `open_path` 授权分支在
// 本刀前就存在——本刀让它首次有真实消费者。
//
// # 安全设计（两条，均来自 TS 注释）
//
//  1. **不用 `cmd /c start`**：cmd 对参数做二次解析，路径中的 `& | % ^`
//     会被重新解释（注入面）。改用 PowerShell `Start-Process`，路径作
//     单引号字面串传入（单引号内 `& | % ^ $` 全不解释，`''` 转义内嵌单引号）。
//  2. **Windows 无处理程序时退化为「定位」**（issue #193）：打开一个无关联
//     处理程序的文件会弹「选取应用」对话框，而 spawn 是 detached——调用方
//     既回收不了也察觉不到。定位（`explorer /select,`）必定成功且绝不弹框。

// OpenPath 创建 `open_path` 工具。
func OpenPath(cwd string) Tool { return &openPathTool{cwd: cwd} }

type openPathTool struct{ cwd string }

// openPathCommand 对账 TS `OpenPathCommand`。
type openPathCommand struct {
	cmd  string
	args []string
}

func (t *openPathTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "open_path",
		Description: "在用户操作系统中打开文件或目录。\n\n" +
			"用于用户可见文件，如生成的图片、SVG、PDF 或文件夹。接受外部路径（桌面、下载、挂载盘、Windows 路径），通过直接启动 OS 打开器避免 shell 引用问题。\n\n" +
			"示例：\n" +
			"Good: open_path(path=\"~/Desktop/tianshu-logo.svg\")\n" +
			"Good: open_path(path=\"H:\\\\zhuomian\\\\白嫖gpt\")\n" +
			"Bad: 用 bash explorer/open/start 命令加手写 shell 引号",
		InputSchema: objSchemaOrdered(
			[]string{"path"},
			map[string]any{
				"path": strProp("绝对路径或 ~ 相对路径，要打开的文件/目录。可在项目之外。"),
			}, "path"),
	}
}

func (t *openPathTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	raw, _ := p.Input["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return contract.Result{Content: "错误：path 为必填项", IsError: true}, nil
	}

	platform := hostPlatform()
	target := normalizeOpenTarget(strings.TrimSpace(raw), platform)
	if _, err := os.Stat(target); err != nil {
		return contract.Result{Content: "错误：路径不存在：" + target, IsError: true}, nil
	}

	isDir := isDirectoryPath(target)
	var hasHandler *bool
	if !isDir {
		hasHandler = windowsFileHasHandler(target, platform, defaultRegQuery)
	}
	action := decideOpenAction(openDecision{
		platform:    platform,
		isDirectory: isDir,
		hasHandler:  hasHandler,
	})

	var command openPathCommand
	verb := "已打开"
	if action == "reveal" {
		command = buildRevealCommand(target, platform)
		verb = "已在资源管理器中定位"
	} else {
		command = buildOpenPathCommand(target, platform)
	}

	// detached + 不等待（对账 TS 的 `detached: true, stdio: 'ignore'` + `unref()`）。
	// **不读输出**——OS 打开器是长驻进程，等待会挂住工具。
	cmd := exec.Command(command.cmd, command.args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		verbShort := "打开"
		if action == "reveal" {
			verbShort = "定位"
		}
		return contract.Result{
			Content: fmt.Sprintf("%s %s 时出错：%v", verbShort, target, err),
			IsError: true,
		}, nil
	}
	// 释放子进程（Go 无 unref，用 goroutine 回收避免僵尸）。
	go func() { _ = cmd.Wait() }()

	return contract.Result{Content: verb + "：" + target}, nil
}

// RequiresApproval 恒 true（对账 TS `() => true`）。
func (t *openPathTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 **true**（对账 TS `() => true`）——只读打开，无写副作用。
func (t *openPathTool) ConcurrencySafe() bool { return true }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *openPathTool) Enabled() bool { return true }

// Timeout 用默认（0）——spawn 后立即返回，不等子进程。
func (t *openPathTool) Timeout(_ *CallParams) time.Duration { return 0 }

// hostPlatform 返回当前平台标识（对账 TS `process.platform` 的取值域）。
//
// Go 的 `runtime.GOOS` 取值 `darwin`/`windows`/`linux` 与 Node 的
// `process.platform` 在主流平台一致，故直接映射。
func hostPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	}
	return "linux"
}

// winDriveRe 判断是否已有盘符或 UNC 前缀（对账 TS `/^(?:[a-zA-Z]:[\\/]|\\\\)/`）。
var winDriveRe = regexp.MustCompile(`^(?:[a-zA-Z]:[\\/]|\\\\)`)

// normalizeOpenTarget 对账 TS `normalizeOpenTarget`。
//
// **路径语义由「声明的平台」决定，不用宿主 resolve()**——否则在 macOS 上跑
// windows 用例时语义不自洽。生产调用方传的是宿主平台，行为不变。
func normalizeOpenTarget(path, platform string) string {
	expanded := expandHome(path)
	if platform == "windows" {
		if winDriveRe.MatchString(expanded) {
			return expanded
		}
		return win32Resolve(expanded)
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	if abs, err := filepath.Abs(expanded); err == nil {
		return abs
	}
	return filepath.Clean(expanded)
}

// win32Resolve 模拟 Node `path.win32.resolve` 的核心行为。
//
// **为什么不用 filepath.Abs**：在 macOS 上 `filepath.Abs("a.svg")` 得到
// `/cwd/a.svg`（posix 语义），而 Windows 期望 `C:\cwd\a.svg`。测试需要
// 跨平台自洽，故显式实现。
func win32Resolve(p string) string {
	// 相对路径 → 拼上当前工作目录（模拟 win32.resolve 的盘符逻辑）。
	// 生产路径上调用方传的多是绝对路径，故这里是 best-effort。
	if !winDriveRe.MatchString(p) {
		if cwd, err := os.Getwd(); err == nil {
			p = filepath.Join(cwd, p)
		}
	}
	return strings.ReplaceAll(p, "/", `\`)
}

// buildOpenPathCommand 对账 TS `buildOpenPathCommand`。
func buildOpenPathCommand(path, platform string) openPathCommand {
	target := normalizeOpenTarget(path, platform)

	if platform == "windows" {
		// explorer/Start-Process 对正斜杠路径静默失败（前端可能传 'C:/Users/...'），
		// 统一转反斜杠。**不用 cmd /c start**（元字符二次解析 = 注入面）。
		winTarget := strings.ReplaceAll(target, "/", `\`)
		literal := "'" + strings.ReplaceAll(winTarget, "'", "''") + "'"
		return openPathCommand{
			cmd:  "powershell.exe",
			args: []string{"-NoProfile", "-NonInteractive", "-Command", "Start-Process -FilePath " + literal},
		}
	}
	if platform == "darwin" {
		return openPathCommand{cmd: "open", args: []string{target}}
	}
	return openPathCommand{cmd: "xdg-open", args: []string{target}}
}

// buildRevealCommand 对账 TS `buildRevealCommand`。
func buildRevealCommand(path, platform string) openPathCommand {
	target := normalizeOpenTarget(path, platform)

	if platform == "windows" {
		winTarget := strings.ReplaceAll(target, "/", `\`)
		literal := "'" + strings.ReplaceAll(winTarget, "'", "''") + "'"
		return openPathCommand{
			cmd:  "powershell.exe",
			args: []string{"-NoProfile", "-NonInteractive", "-Command", "explorer /select," + literal},
		}
	}
	if platform == "darwin" {
		return openPathCommand{cmd: "open", args: []string{"-R", target}}
	}
	// Linux：无通用「选中文件」API——打开父目录。
	return openPathCommand{cmd: "xdg-open", args: []string{filepath.Dir(target)}}
}

// openDecision 对账 TS `OpenDecision`。
type openDecision struct {
	platform    string
	isDirectory bool
	// hasHandler：true=有处理程序；false=确认没有；nil=判断不了。
	hasHandler *bool
}

// decideOpenAction 对账 TS `decideOpenAction`（纯判定，platform 可注入）。
func decideOpenAction(in openDecision) string {
	if in.platform != "windows" {
		return "open"
	}
	if in.isDirectory {
		return "open"
	}
	if in.hasHandler != nil && !*in.hasHandler {
		return "reveal"
	}
	return "open"
}

// isDirectoryPath 对账 TS `isDirectoryPath`——stat 失败（竞态删除等）按「不是目录」处理。
func isDirectoryPath(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.IsDir()
}

// regQueryExec 对账 TS `RegQueryExec`——可注入的注册表查询（便于测试）。
type regQueryExec func(key, value string) (string, error)

// defaultRegQuery 对账 TS `defaultRegQuery`。
//
// **SystemRoot 下取绝对路径**，避免依赖 PATH 里 reg.exe 的顺序。
func defaultRegQuery(key, value string) (string, error) {
	reg := "reg.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		reg = filepath.Join(root, "System32", "reg.exe")
	}
	args := []string{"query", key}
	if value == "" {
		args = append(args, "/ve")
	} else {
		args = append(args, "/v", value)
	}
	out, err := exec.Command(reg, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// winExtRe 对账 TS `/^\.[A-Za-z0-9_+-]+$/`。
var winExtRe = regexp.MustCompile(`^\.[A-Za-z0-9_+-]+$`)

// winProgIDRe 对账 TS `/ProgId\s+REG_SZ\s+\S+/`。
var winProgIDRe = regexp.MustCompile(`ProgId\s+REG_SZ\s+\S+`)

// winRegSZRe 对账 TS `/REG_SZ\s+(\S+)/`。
var winRegSZRe = regexp.MustCompile(`REG_SZ\s+(\S+)`)

// windowsFileHasHandler 对账 TS `windowsFileHasHandler`。
//
// Windows：一个**文件**被 shell「打开」时，是否注册了可用的处理程序。
//
// **为什么需要**（issue #193）：`.zzq` 这类没有关联的扩展名交给 shell 打开时
// 没有可用处理程序，Windows 会弹「选取应用」对话框（`OpenWith.exe`）。
// 对话框宿主是 shell，与调用方生命周期无关——既不会被调用方回收，也无法被
// 察觉，只会堆在用户桌面上。
//
// 判定顺序（best-effort，只查注册表最常用的两处）：
//  1. 用户级默认程序：`HKCU\...\FileExts\<ext>\UserChoice` 的 ProgId
//  2. 机器级关联：`HKCR\<ext>` 默认值 → ProgId → `HKCR\<ProgId>\shell\open\command`
//
// 返回 `nil` = 判断不了（非 Windows、无扩展名、查询本身出错）——调用方按
// fail-open 处理，保持既有行为，避免把「本来能打开」的机器误降级成「只定位」。
func windowsFileHasHandler(filePath, platform string, query regQueryExec) *bool {
	if platform != "windows" {
		return nil
	}
	ext := filepath.Ext(filePath)
	if ext == "" || !winExtRe.MatchString(ext) {
		return nil
	}

	// ① 用户级 UserChoice
	if out, err := query(`HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\`+ext+`\UserChoice`, "ProgId"); err == nil {
		if winProgIDRe.MatchString(out) {
			return boolPtr(true)
		}
	}
	// 该扩展名没有用户级覆盖——正常情况，继续查机器级。

	// ② 机器级 HKCR\<ext> 默认值 → ProgId
	out, err := query(`HKEY_CLASSES_ROOT\`+ext, "")
	if err != nil {
		// HKCR 里根本没有该扩展名——「确认无处理程序」的强信号。
		return boolPtr(false)
	}
	m := winRegSZRe.FindStringSubmatch(out)
	if m == nil || len(m) < 2 {
		return nil
	}
	progID := m[1]

	// ③ HKCR\<ProgId>\shell\open\command 存在？
	if _, err := query(`HKEY_CLASSES_ROOT\`+progID+`\shell\open\command`, ""); err != nil {
		return boolPtr(false)
	}
	return boolPtr(true)
}

// boolPtr 返回 *bool（辅助）。
func boolPtr(b bool) *bool { return &b }
