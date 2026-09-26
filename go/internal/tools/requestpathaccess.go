package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kalandramo/tianshu/go/internal/contract"
)

// requestpathaccess.go —— `request_path_access` 工具（第八十一刀）。
//
// 对账 TS `src/tools/request-path-access.ts`（104 行）。
//
// # 它补的缺口
//
// Go 侧门链（`loop.go` 的 pathGrant 门）在**非 skip 档**遇到工作区外路径时
// **直接拒绝**——注释写明「无提示通道」。模型没有**主动申请**授权的入口。
// 本工具补上它：`bash` / 多路径 / 目录级授权这三类「内联管线门看不到目标」
// 的场景，统一走它。
//
// # 安全边界（issue #117）
//
// 授权必须有**上界**：`request_path_access(path='/')` 缺省 write 会把本会话
// 写权限放大到全磁盘。两道闸：
//  1. `isForbiddenGrantRoot`——根 / 系统目录黑名单（**target 与其授权根都查**：
//     `/etc/passwd` 的授权根是 `/etc`）。
//  2. `DetectSensitiveFile`——敏感文件（`.env` / `*_rsa` 等）。
//
// # 跨包解耦（为什么是回调）
//
// `GrantMode` / `PathGrant` 定义在 `internal/agent`（`pathgrants.go`），而
// `agent` **已依赖** `tools`——`tools` 反向 import `agent` 会成 **import 环**。
// 故此处按本仓库既有的解耦模式（`CallParams` 的 `EnterPlanMode`/`ExitPlanMode`
// 同款）：**`agent` 注入 `func` 回调**，`tools` 只依赖函数签名。

// GrantMode 是授权模式（**本包定义**，避免 import 环）。
//
// 与 `agent.GrantMode` 字符串值一致（`"read"`/`"write"`）——`agent` 侧在
// 装配回调时做一次转换（两包的类型是独立定义，各有对账锚）。
type GrantMode string

const (
	GrantRead  GrantMode = "read"
	GrantWrite GrantMode = "write"
)

// forbiddenGrantRoots 是不得作为授权根的系统级目录（issue #117）。
//
// 对账 TS `FORBIDDEN_GRANT_ROOTS`——**逐条对账，含大小写形式**。
// 一次批准即把本会话的读写面放大到整个系统区；用户自己的目录树不受影响。
var forbiddenGrantRoots = map[string]struct{}{
	"/etc": {}, "/usr": {}, "/bin": {}, "/sbin": {}, "/lib": {}, "/lib64": {},
	"/var": {}, "/opt": {}, "/root": {}, "/home": {},
	"/users": {}, "/system": {}, "/library": {}, "/private": {},
	"/dev": {}, "/proc": {}, "/sys": {}, "/boot": {},
	`c:\windows`: {}, `c:\program files`: {}, `c:\program files (x86)`: {}, `c:\users`: {},
}

// isForbiddenGrantRoot 报告该路径是否不得作为授权根。
//
// 对账 TS `isForbiddenGrantRoot`：
//
//	norm = target.replace(/[\\/]+$/, '')   // 去尾部斜杠
//	if (norm === '') return true            // POSIX 根 `/`
//	if (/^[A-Za-z]:$/.test(norm)) return true  // Windows 盘根 `C:`
//	return FORBIDDEN_GRANT_ROOTS.has(norm.toLowerCase())
//
// **注意是精确匹配而非前缀**：`/etc-fake` 不得被 `/etc` 误伤
// （TS 用 `Set.has`，同理）。
func isForbiddenGrantRoot(target string) bool {
	// 去尾部斜杠/反斜杠（保留 Windows 盘根形式 `C:`）。
	norm := strings.TrimRight(target, `/\`)
	if norm == "" {
		return true // `/` 或 `///`
	}
	// Windows 盘根：`C:` / `D:`（大小写不敏感）。
	if len(norm) == 2 && norm[1] == ':' {
		c := norm[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return true
		}
	}
	_, bad := forbiddenGrantRoots[strings.ToLower(norm)]
	return bad
}

// expandHome 展开 `~` 前缀为 home 目录（对账 TS `expandHome`）。
//
// 只展开**开头的** `~` 或 `~/`——中间的 `~` 是普通字符。
func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// RequestPathAccess 创建 `request_path_access` 工具。
func RequestPathAccess(cwd string) Tool { return &requestPathAccessTool{cwd: cwd} }

type requestPathAccessTool struct{ cwd string }

func (t *requestPathAccessTool) Definition() contract.Definition {
	return contract.Definition{
		Name: "request_path_access",
		Description: "请求用户授权访问当前工作区之外的路径。\n\n" +
			"用于批量/目录级授权，或基于 bash 的工作区外操作。审批通过后，该目录子树\n" +
			"在本会话内可读/可写（用 remember=true 持久化）。对单个工作区外文件的\n" +
			"读写，直接调用 read_file/write_file 会触发同样的内联提示。",
		InputSchema: objSchemaOrdered([]string{"path", "mode", "remember"}, map[string]any{
			"path": strProp("要授权访问的工作区外路径（文件或目录），绝对路径或 ~ 相对路径。"),
			"mode": map[string]any{
				"type":        "string",
				"enum":        []any{"read", "write"},
				"description": "访问级别。'write' 隐含读取权限。默认 'read'（最小权限；确需写请显式传 'write'）。",
			},
			"remember": boolProp("为当前工作区跨会话持久化此授权。默认 false（仅本会话）。"),
		}, "path"),
	}
}

func (t *requestPathAccessTool) Execute(_ context.Context, p *CallParams) (contract.Result, error) {
	raw, ok := p.Input["path"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return contract.Result{Content: "错误：path 必填", IsError: true}, nil
	}

	// issue #117 —— 缺省 read（最小权限）：缺省 write 叠加无上界授权根时，
	// 一次 `request_path_access(path='/')` 就能把写权限放大到全磁盘。
	mode := GrantRead
	if m, ok := p.Input["mode"].(string); ok && m == "write" {
		mode = GrantWrite
	}
	remember, _ := p.Input["remember"].(bool)

	target := resolveAbs(expandHome(strings.TrimSpace(raw)))

	// 授权**目录子树**：路径本身是（或将是）目录则用它，否则用其父目录
	// ——这样「文件 + 其兄弟」都可达。
	root := target
	if !isExistingDir(target) {
		root = filepath.Dir(target)
	}

	// issue #117 —— 授权必须有上界：**target 与其授权根都查**
	// （`/etc/passwd` 的授权根是 `/etc`）。
	if isForbiddenGrantRoot(target) || isForbiddenGrantRoot(root) {
		return contract.Result{
			Content: "错误：拒绝授权文件系统根/系统目录（" + target + "）——请指定具体的工作子目录。",
			IsError: true,
		}, nil
	}
	if sensitive := DetectSensitiveFile(target); sensitive.Sensitive {
		name := sensitive.PatternName
		if name == "" {
			name = "敏感路径"
		}
		return contract.Result{
			Content: "错误：拒绝授权敏感文件（" + name + "）：" + target,
			IsError: true,
		}, nil
	}

	// 无授权能力 → fail-closed（不得假装成功）。
	if p.GrantPath == nil {
		return contract.Result{
			Content: "错误：路径授权能力在当前上下文不可用（无会话）。请让用户手动操作，或改用工作区内路径。",
			IsError: true,
		}, nil
	}

	// 交互式批准**总是**绑定本会话工作区——缺 cwd 时回退工具自身的 cwd，
	// 绝不产生无作用域（进程级）的授权。
	cwd := p.Cwd
	if cwd == "" {
		cwd = t.cwd
	}
	p.GrantPath(root, mode, cwd)

	// **诚实降级**：TS 支持 `persist` 持久化，Go 侧 `agent.GrantPath`
	// **无 persist 参数**（持久化未移植，见 `pathgrants.go:18-23` 的声明）。
	// 故 remember=true 时**不得**谎称已持久化——明示「仅本会话」。
	lifetime := "仅本会话"
	if remember {
		lifetime = "仅本会话（Go 侧跨会话持久化未移植，remember=true 已降级）"
	}
	verb := "读取"
	if mode == GrantWrite {
		verb = "读写"
	}
	return contract.Result{
		Content: "已授予 " + string(mode) + " 访问：" + root + "\n" +
			"范围：该目录及其下全部路径 — " + lifetime + "。\n" +
			"文件工具与 bash 现在可以在此" + verb + "路径。",
	}, nil
}

// RequiresApproval 恒 true（对账 TS `() => true`）。
//
// **为什么**：它做的正是「扩大权限边界」——必须走审批回合。
func (t *requestPathAccessTool) RequiresApproval(_ *CallParams) bool { return true }

// ConcurrencySafe 恒 false（对账 TS `() => false`）——授权是有序的状态变更。
func (t *requestPathAccessTool) ConcurrencySafe() bool { return false }

// Enabled 恒 true（对账 TS `() => true`）。
func (t *requestPathAccessTool) Enabled() bool { return true }

// Timeout 用默认（0 = 用管线默认）——本工具是纯本地操作，无阻塞等待。
func (t *requestPathAccessTool) Timeout(_ *CallParams) time.Duration { return 0 }

// resolveAbs 把路径解析为绝对路径（对账 TS `resolve`）。
func resolveAbs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// isExistingDir 报告路径存在且是目录（对账 TS `existsSync(t) && statSync(t).isDirectory()`）。
func isExistingDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
