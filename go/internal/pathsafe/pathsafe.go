// Package pathsafe 实现工作区路径校验（fail-closed）。
//
// 对账 src/tools/path-validate.ts。三条不变量：
//
//  1. **敏感文件优先拦截**：在路径逃逸检查**之前**，且覆盖路径的每一种可寻址
//     形态（裸输入 / 词法 resolve / realpath 规范形）。只查裸输入串会让
//     `scripts/../.env` 这类写法绕过。
//  2. **符号链接规范化解在包含性检查之前**：不规范化的话，经符号链接根到达的
//     合法绝对路径会保留未解析前缀，与已 realpath 的 cwd 比较，被误判为逃逸
//     （macOS 的 /var→/private/var 临时目录是最常见触发场景）。
//  3. **不存在的文件解析最近的存在祖先**：让新文件写入可被校验，同时仍能捕获
//     「符号链接父目录逃逸项目」（如 ./evil -> /etc，写 evil/new）。
package pathsafe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Result 是校验结果。
type Result struct {
	OK    bool
	Path  string // 校验通过时的解析路径
	Error string // 失败原因
}

// GrantChecker 判定越界路径是否被显式授权。
//
// 授权存储只经审批流扩宽——本包自身不授予任何权限。
type GrantChecker interface {
	// IsReadGranted 报告 path 是否被授予读权限。
	IsReadGranted(path, cwd string) bool
	// IsWriteGranted 报告 path 是否被授予写权限。
	IsWriteGranted(path, cwd string) bool
}

// Mode 是访问模式。
type Mode int

const (
	ModeRead Mode = iota
	ModeWrite
)

// SensitiveDetector 判定路径是否指向敏感文件（凭据/密钥等）。
type SensitiveDetector interface {
	// Detect 返回是否敏感及命中的模式名。
	Detect(path string) (sensitive bool, patternName string)
}

// Options 是校验选项。
type Options struct {
	// Grants 越界路径的授权判定。nil = 无任何授权（全部越界拒绝）。
	Grants GrantChecker
	// Sensitive 敏感文件检测。nil = 用内置默认规则。
	Sensitive SensitiveDetector
}

// Validate 校验 inputPath 位于工作区内，或被显式授权。
//
// 返回的 Path 保持**原始 cwd 基准**（调用方据此算相对标签），
// 但校验本身对两侧都做规范化。
func Validate(cwd, inputPath string, mode Mode, opts *Options) Result {
	if opts == nil {
		opts = &Options{}
	}

	resolved := resolveUnder(cwd, inputPath)

	// cwd 的 realpath：不解析 cwd 的话，经符号链接到达的 cwd（macOS
	// /var→/private/var、符号链接的 home/挂载/仓库）会让 realpath 后的文件
	// 与未解析的 cwd 比较，把每个合法文件都误判为「符号链接逃逸」。
	realCwd := cwd
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		realCwd = r
	} else {
		realCwd = resolved2(cwd)
	}
	realResolved := resolveUnder(realCwd, inputPath)

	// 在包含性检查**之前**规范化目标（解析其存在祖先中的符号链接）。
	real := ""
	if r, err := filepath.EvalSymlinks(realResolved); err == nil {
		real = r
	} else {
		real = resolveNearestExisting(realResolved, realCwd)
	}

	// ── 敏感文件检查：fail-closed，在逃逸检查之前，覆盖每种形态 ──
	det := opts.Sensitive
	if det == nil {
		det = defaultSensitiveDetector{}
	}
	for _, form := range []string{inputPath, resolved, realResolved, real} {
		if sensitive, pattern := det.Detect(form); sensitive {
			return Result{
				OK: false,
				Error: fmt.Sprintf(
					"Sensitive file blocked: %s matches sensitive pattern %q. "+
						"Reading or committing credential/key files is not permitted. "+
						"If this is a false positive (e.g. a template or fixture), rename the file or move it to a whitelisted path.",
					inputPath, pattern),
			}
		}
	}

	rel, err := filepath.Rel(realCwd, real)
	if err != nil {
		// 不同卷（Windows）——视为越界
		rel = ".." + string(filepath.Separator) + "other"
	}

	if rel == "" || rel == "." {
		return Result{OK: true, Path: resolved}
	}

	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		// 越界——仅当用户已就该子树在请求模式下授权时放行。
		// 授权按本工作区 cwd 作用域：sidecar 在一个进程里承载多个工作区的会话，
		// 故只有本工作区批准（或持久化）的授权 + 用户级授权才能扩宽。
		//
		// 注意：授权比对用**规范化后**的 real 与 realCwd——用未规范化的 resolved
		// 会因 /var vs /private/var 这类符号链接差异让合法授权失效（本文件顶部
		// 警告过的同一陷阱）。
		granted := false
		if opts.Grants != nil {
			if mode == ModeWrite {
				granted = opts.Grants.IsWriteGranted(real, realCwd)
			} else {
				granted = opts.Grants.IsReadGranted(real, realCwd)
			}
		}
		if granted {
			return Result{OK: true, Path: resolved}
		}
		return Result{
			OK: false,
			Error: fmt.Sprintf(
				"Path outside project directory: %s (workspace root: %s). "+
					"If this path is wrong, re-check the workspace root above and use a path under it. "+
					"If the user authorized working there, call request_path_access (or approve the prompt) to grant access.",
				inputPath, realCwd),
		}
	}

	return Result{OK: true, Path: resolved}
}

// MustValidate 在校验失败时返回错误（对应 TS 的 validatePath 抛异常版本）。
func MustValidate(cwd, filePath string, mode Mode, opts *Options) (string, error) {
	r := Validate(cwd, filePath, mode, opts)
	if !r.OK {
		return "", fmt.Errorf("%s", r.Error)
	}
	return r.Path, nil
}

// resolveUnder 以 base 为基准解析 target（target 为绝对路径时按绝对处理）。
//
// 对账 Node 的 `path.resolve(base, target)`。这里有一处**平台语义陷阱**：
// Go 的 `filepath.IsAbs` 与 Node 的 `path.isAbsolute` 对「根相对路径」判定不同。
//
//	路径               Node win32.isAbsolute   Go filepath.IsAbs (Windows)
//	/etc/passwd        true                    false
//	\Windows\System32  true                    false
//	\\server\share     true                    true
//	C:\x               true                    true
//
// Node 把 `/x`（无卷、以分隔符开头）视为**绝对**——`path.win32.resolve('D:\ws',
// '/etc/passwd')` = `D:\etc\passwd`，即**重基到 base 的卷**（不是拼进 base）。
// 若照 Go 的 IsAbs 判定（false）走 Join 分支，`/etc/passwd` 会被静默拼成
// `D:\ws\etc\passwd`——一个位于工作区内的路径，逃逸检查**永不触发**。
// 这是 fail-open 的安全缺口（TS 侧正确拦截，Go 侧放行）。
//
// 故此处显式复刻 Node 语义：先看 IsAbs（覆盖带卷的形态），再看「以分隔符开头」
// （覆盖根相对形态）。后者用 `VolumeName(base)` 取基准卷做重基。
func resolveUnder(base, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	// 根相对路径（/x 或 \x）：Node 视为绝对，重基到 base 的卷。
	if target != "" && os.IsPathSeparator(target[0]) {
		return filepath.Clean(filepath.VolumeName(base) + target)
	}
	return filepath.Join(base, target)
}

// resolved2 是 filepath.Abs 的别名（保留语义清晰的调用点）。
func resolved2(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// resolveNearestExisting 解析 target 不存在时的真实路径：向上走到最近的存在祖先，
// 规范化它，再接回不存在的尾部。
//
// floor（已规范化的 cwd）是上界——爬过它会解析到项目外的祖先（macOS 上 /home
// 是指向 /System/Volumes/Data/home 的合成符号链接），把合法路径误判为逃逸。
func resolveNearestExisting(target, floor string) string {
	var segments []string
	current := target
	for {
		if _, err := os.Lstat(current); err == nil {
			break // 存在
		}
		if current == floor {
			return filepath.Join(append([]string{floor}, segments...)...)
		}
		segments = append([]string{filepath.Base(current)}, segments...)
		parent := filepath.Dir(current)
		if parent == current {
			return target // 到文件系统根仍未找到存在祖先
		}
		current = parent
	}
	if r, err := filepath.EvalSymlinks(current); err == nil {
		return filepath.Join(append([]string{r}, segments...)...)
	}
	return target
}

// ── 默认敏感文件检测 ──

// defaultSensitiveDetector 实现内置的敏感文件名规则。
type defaultSensitiveDetector struct{}

// sensitivePatterns 是敏感文件名模式（小写比对）。
var sensitivePatterns = []struct {
	pattern string
	name    string
}{
	{".env", "env-file"},
	{"credentials", "credentials"},
	{"private_key", "private-key"},
	{"privatekey", "private-key"},
	{"id_rsa", "ssh-private-key"},
	{"id_ed25519", "ssh-private-key"},
	{"secret", "secret"},
	{"token", "token"},
	{".pem", "pem-key"},
	{".p12", "pkcs12"},
	{".pfx", "pkcs12"},
}

// Detect 判定路径是否指向敏感文件。
//
// 归一化后比对：去掉 8.3 短名（由 realpath 展开）、尾部点与斜杠、大小写。
func (defaultSensitiveDetector) Detect(path string) (bool, string) {
	if path == "" {
		return false, ""
	}
	// 归一化：尾部斜杠与点（`.env/` / `.env.` 应等同 `.env`）
	normalized := strings.TrimRight(path, "/\\")
	normalized = strings.TrimRight(normalized, ".")
	lower := strings.ToLower(normalized)

	base := filepath.Base(lower)

	// 子串匹配（对齐 TS detector 的语义）：`my_secret.txt`、`api_token.txt`、
	// `credentials-guide.md` 都应命中——宁可误报也不放过真实凭据。
	for _, p := range sensitivePatterns {
		if strings.Contains(base, p.pattern) {
			return true, p.name
		}
	}
	return false, ""
}
