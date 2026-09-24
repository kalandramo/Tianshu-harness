// pathgrants.go —— 运行时路径授权存储。
//
// 对账 TS `src/tools/path-grants.ts`（506 行）的**核心授予/查询语义**。
//
// ## 为什么需要它
//
// 审批决策树的 `pathGrantNeed` 分支有两种处理，都需要运行时授权：
//
//   - **skip 档**（`dangerously-skip-permissions`）→ 出界路径**首触即授**
//     （对账 TS `tool-pipeline.ts:1240-1249`）
//   - 其他档 → 弹审批；批准后 `grantPath` 记录授权，后续同路径不再问
//
// Go 侧此前只有 `StaticGrantChecker`（静态、构造期固定）——无法在运行时
// 授予，故 skip 档的"零打断"语义无法实现。
//
// ## scope 收窄（明示）
//
// TS 的完整能力含**持久化**（`persistGrants` / `loadPersistedGrants`，
// 写 `<rivetHome>/path-grants-<slug>.json`）。本文件**只实现进程内存储**——
// 持久化留待需要时补（当前 skip 档的会话级授予足够；TS 注释也说
// 「会话级，不出会话」）。
//
// 未移植：`listPersistedGrants` / `loadPersistedGrants` / `persistGrants` /
// `revokeGrant` / `applyConfiguredPathGrants` / `applyDefaultDependencyReadGrants` /
// `applyRivetRuntimeReadGrants` / `probeConfiguredDirExists`。这些服务于
// config 装配与依赖缓存读取面，各自有独立消费者。
//
// ## 安全语义（逐条对账 TS，不可简化）
//
//  1. **canonicalize**：解析符号链接（最近存在祖先），防符号链接绕过
//  2. **case folding**：Windows 文件系统大小写不敏感，`F:\` 与 `f:\` 必须等价
//  3. **段边界**：`/a/b-evil` 不得匹配 `/a/b`（防 fail-open）
//  4. **scope 隔离**：授权绑定 session cwd——A 工作区的授权不得泄漏给 B
//  5. **写覆盖读**：同 root 的 write 授予升级 read 授予，**永不降级**
package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/kalandramo/tianshu/go/internal/pathsafe"
)

// GrantMode 是授权模式（对账 TS `GrantMode`）。
type GrantMode string

const (
	GrantRead  GrantMode = "read"
	GrantWrite GrantMode = "write"
)

// PathGrant 是一条路径授权（对账 TS `PathGrant`）。
//
// **未含 TS 的 `persisted` / `grantedAt` 字段**——持久化未移植，
// `grantedAt` 无消费者。需要时补。
type PathGrant struct {
	// Root 是规范化（realpath）后的绝对目录根。
	Root string
	// Mode 是授权模式。
	Mode GrantMode
	// Scope 是会话作用域（规范化后的 session cwd）。
	//
	// **为什么必须有**：sidecar 在一个进程里承载多个会话（可能跨不同
	// 工作区），无作用域的交互式授权会让工作区 A 的批准静默授权工作区 B
	// 的写操作。空串 = 进程级（配置/依赖缓存类，由用户或工具级授予）。
	Scope string
}

// grantModeFromPathsafe 把 pathsafe 的访问模式转为授权模式。
//
// **为什么需要转换**：两个包的 Mode 是独立类型（`pathsafe.Mode` 用 iota 枚举，
// `GrantMode` 用字符串常量——后者对账 TS 的 `'read' | 'write'` 字面量类型）。
// 合并成一个会破坏各自的对账锚（TS 侧本就是两套）。
func grantModeFromPathsafe(m pathsafe.Mode) GrantMode {
	if m == pathsafe.ModeWrite {
		return GrantWrite
	}
	return GrantRead
}

// caseInsensitiveFS 报告当前平台文件系统是否大小写不敏感。
//
// 对账 TS `CASE_INSENSITIVE_FS`（Windows / macOS 默认不敏感）。
func caseInsensitiveFS() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// foldCase 按平台语义折叠大小写（对账 TS `foldCase`）。
func foldCase(p string) string {
	if caseInsensitiveFS() {
		return strings.ToLower(p)
	}
	return p
}

// isPathUnder 报告 child 是否位于 root 之下（**按路径段边界**）。
//
// 对账 TS `isPathUnder`。两处语义不可简化：
//
//   - **段边界**：`/a/b-evil` 不得匹配 `/a/b`——`strings.HasPrefix` 会
//     fail-open（项目已记录该坑，`StaticGrantChecker` 的 `hasPathPrefix`
//     同源）。
//   - **分隔符归一**：Windows 上 `/` 与 `\` 等价。
func isPathUnder(root, child string) bool {
	norm := func(p string) string {
		if runtime.GOOS == "windows" {
			p = strings.ReplaceAll(p, "/", `\`)
		}
		return foldCase(p)
	}
	r := norm(root)
	c := norm(child)
	if c == r {
		return true
	}
	sep := string(filepath.Separator)
	prefix := r
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(c, prefix)
}

// canonicalizePath 规范化路径：解析符号链接（或最近存在祖先），使包含性
// 检查比较的是**真实路径**。
//
// 对账 TS `canonicalize`。**安全关键**：不解析符号链接的话，攻击者可经
// 符号链接把"工作区内"指向工作区外。
//
// 目标不存在时向上走到最近的存在祖先，规范化它再接回尾部——这样
// 「将要创建的文件」也能得到正确判定。
func canonicalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// 向上找最近存在祖先
	current := abs
	var tail []string
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs // 到根仍未找到
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
	if real, err := filepath.EvalSymlinks(current); err == nil {
		parts := append([]string{real}, tail...)
		return filepath.Join(parts...)
	}
	return abs
}

// pathGrantStore 是进程内授权存储。
//
// 对账 TS 的包级 `_grants` 数组 + 各查询函数。用 mutex 保护——sidecar
// 多会话并发访问。
type pathGrantStore struct {
	mu     sync.RWMutex
	grants []PathGrant
}

// newPathGrantStore 构造空的授权存储。
func newPathGrantStore() *pathGrantStore {
	return &pathGrantStore{}
}

// scopeVisible 报告授权 g 对 cwd 会话是否可见（对账 TS `scopeVisible`）。
//
// 空 cwd 视为「看得到全部」（legacy 调用方）；无 scope 的授权是进程级的。
func (s *pathGrantStore) scopeVisible(g PathGrant, cwd string) bool {
	if cwd == "" {
		return true
	}
	if g.Scope == "" {
		return true
	}
	return g.Scope == canonicalizePath(cwd)
}

// GrantPath 授予目录子树的访问权（对账 TS `grantPath`）。
//
// `root` 被规范化。**写授权覆盖先前的读授权**（永不降级）。`cwd` 非空时
// 授权**绑定该工作区**——交互式批准必须传 cwd，否则会跨会话泄漏。
func (s *pathGrantStore) GrantPath(root string, mode GrantMode, cwd string) PathGrant {
	canonical := canonicalizePath(root)
	scope := ""
	if cwd != "" {
		scope = canonicalizePath(cwd)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.grants {
		g := &s.grants[i]
		if foldCase(g.Root) == foldCase(canonical) && g.Scope == scope {
			// 升级 read → write，永不降级。
			if mode == GrantWrite {
				g.Mode = GrantWrite
			}
			return *g
		}
	}
	grant := PathGrant{Root: canonical, Mode: mode, Scope: scope}
	s.grants = append(s.grants, grant)
	return grant
}

// IsReadGranted 报告路径是否在任一可见授权根之下（对账 TS `isReadGranted`）。
//
// 读授权或写授权都满足读（写蕴含读）。
func (s *pathGrantStore) IsReadGranted(absPath, cwd string) bool {
	target := canonicalizePath(absPath)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grants {
		if s.scopeVisible(g, cwd) && isPathUnder(g.Root, target) {
			return true
		}
	}
	return false
}

// IsWriteGranted 报告路径是否在任一可见的**写**授权根之下（对账 TS `isWriteGranted`）。
func (s *pathGrantStore) IsWriteGranted(absPath, cwd string) bool {
	target := canonicalizePath(absPath)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.grants {
		if g.Mode == GrantWrite && s.scopeVisible(g, cwd) && isPathUnder(g.Root, target) {
			return true
		}
	}
	return false
}

// ListGrants 返回当前授权的快照（对账 TS `listGrants`）。
func (s *pathGrantStore) ListGrants() []PathGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PathGrant, len(s.grants))
	copy(out, s.grants)
	return out
}

// ResetGrantsForTest 清空授权（测试用，对账 TS `_resetGrantsForTest`）。
func (s *pathGrantStore) ResetGrantsForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants = nil
}
