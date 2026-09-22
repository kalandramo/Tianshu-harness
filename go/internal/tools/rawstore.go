package tools

// rawstore.go —— bash 原始输出的落盘与恢复提示支撑。
//
// 对账 TS `src/tools/output-store.ts` 的 `rawOutputDir` / `persistRawOutput` /
// `cleanStaleRawOutputs` / `safeRawFileName` 及两个常量。
//
// # 为什么需要
//
// bash 输出被截断后，模型会尝试用 `sed`/`head`/`tee` 变体重跑同一命令去"看剩下的"
// ——这是 **doom-loop 的根因**（TS 注释引 incident 会话 43443098）。落盘 + 在截断
// footer 里给出恢复路径，让模型**读文件**而不是重跑命令。
//
// # 与 path-grants 的强耦合（**本刀的关键称量**）
//
// `rawOutputDir()` 在**项目目录之外**（`os.TempDir()/rivet-raw`）。而 `read_file`
// 会经 `pathsafe.Validate` 拦越界路径——**除非该目录被显式 grant**。
//
// 故 `PersistRawOutput` **必须与 `GrantRuntimeReadPaths` 配套**：只落盘不授权，
// 模型拿到 recovery 提示却读不到文件——**提示变成误导**（比不提示更糟）。
// TS 侧同一约束写在 `rawOutputDir()` 的注释里（`applyRivetRuntimeReadGrants`
// 必须授予恰好这个目录）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 对账 TS 的常量。
const (
	// staleTTLMs 对账 TS `STALE_TTL_MS`（1 小时）。
	staleTTL = time.Hour
	// cleanInterval 对账 TS `CLEAN_INTERVAL`（每 N 次落盘清理一次）。
	cleanInterval = 10
	// rawHashLen 对账 TS `safeRawFileName` 的 `.slice(0, 24)`。
	rawHashLen = 24
)

// rawPersistMu 保护 persistCount（并发 bash 调用会同时落盘）。
var (
	rawPersistMu sync.Mutex
	persistCount int
)

// RawOutputDir 返回 raw 输出目录（对账 TS `rawOutputDir`）。
//
// **必须在项目目录之外**（临时目录），且**调用方必须确保该目录被 grant**——
// 见文件头的耦合说明。
func RawOutputDir() string {
	return filepath.Join(os.TempDir(), "rivet-raw")
}

// safeRawFileName 对账 TS `safeRawFileName`。
//
// 用 sha256 的**前 24 个 hex 字符**（不是 base64、不是全量）。id 为空时 TS 用
// `randomUUID()`——Go 用时间戳 + 计数器替代（UUID 需额外依赖，而此处只需唯一性）。
func safeRawFileName(id string) string {
	if id == "" {
		id = fmt.Sprintf("%d-%d", time.Now().UnixNano(), nextRawSeq())
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:rawHashLen] + ".raw"
}

var rawSeqMu sync.Mutex
var rawSeq uint64

func nextRawSeq() uint64 {
	rawSeqMu.Lock()
	defer rawSeqMu.Unlock()
	rawSeq++
	return rawSeq
}

// PersistRawOutput 把原始输出落盘，返回文件路径。
//
// 对账 TS `persistRawOutput`。**失败时返回空串**（调用方据此跳过 recovery 提示）——
// TS 侧的 `persistRawSafe` 包了 try/catch 并返回 `undefined`，语义一致。
//
// **每 cleanInterval 次落盘触发一次陈旧清理**（异步，不阻塞返回）。
func PersistRawOutput(id, raw string) string {
	dir := RawOutputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	filePath := filepath.Join(dir, safeRawFileName(id))
	if err := os.WriteFile(filePath, []byte(raw), 0o644); err != nil {
		return ""
	}

	rawPersistMu.Lock()
	persistCount++
	shouldClean := persistCount%cleanInterval == 0
	rawPersistMu.Unlock()

	if shouldClean {
		// TS 用 `.catch(() => {})`——清理失败不影响主流程。
		go CleanStaleRawOutputs(time.Now())
	}
	return filePath
}

// CleanStaleRawOutputs 删除超过 staleTTL 的 raw 文件。
//
// 对账 TS `cleanStaleRawOutputs`。**目录不存在时静默返回**（TS 的 catch 分支）。
// 单个文件 stat/unlink 失败也跳过（TS 的 per-file try/catch）。
//
// now 参数便于测试注入时间（TS 用 `Date.now()`）。
func CleanStaleRawOutputs(now time.Time) {
	dir := RawOutputDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-staleTTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// GrantRuntimeReadPaths 把运行时自有的读路径授予读权限。
//
// 对账 TS `applyRivetRuntimeReadGrants`——**它只授予 `rawOutputDir()` 一个目录**。
//
// **为什么必需**：`read_file` 对越界路径走 `pathsafe.Validate`，无 grant 则被拒。
// 模型拿到 `[output truncated: ... full output: read_file <path>]` 提示后读不到
// 文件，会转而重跑命令——正是要防的 doom-loop。
//
// 返回值是**追加后的**授权路径列表（调用方据此构造 GrantChecker）。
// 纯函数：不改全局状态（Go 侧无 TS 的模块级 `_grants` 数组）。
func GrantRuntimeReadPaths(existing []string) []string {
	dir := RawOutputDir()
	for _, p := range existing {
		if p == dir {
			return existing // 幂等：已授权则不重复追加
		}
	}
	return append(append([]string{}, existing...), dir)
}

// StaticGrantChecker 是 `pathsafe.GrantChecker` 的简单实现：按前缀匹配。
//
// 用于把 `GrantRuntimeReadPaths` 产出的路径列表接进 `pathsafe`。
// **前缀匹配需按路径段边界**——否则 `/tmp/rivet-raw-evil` 会被 `/tmp/rivet-raw`
// 误判为已授权（fail-open 安全洞）。
type StaticGrantChecker struct {
	ReadPaths  []string
	WritePaths []string
}

func (g *StaticGrantChecker) IsReadGranted(path, cwd string) bool {
	return hasPathPrefix(path, g.ReadPaths)
}

func (g *StaticGrantChecker) IsWriteGranted(path, cwd string) bool {
	return hasPathPrefix(path, g.WritePaths)
}

// hasPathPrefix 判定 path 是否位于 prefixes 中某个目录之下（**按段边界**）。
//
// 不能简单用 `strings.HasPrefix`：`/a/b-evil` 会匹配 `/a/b`——fail-open。
func hasPathPrefix(path string, prefixes []string) bool {
	clean := filepath.Clean(path)
	for _, p := range prefixes {
		root := filepath.Clean(p)
		if clean == root {
			return true
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			continue // 跨盘符等 → 不在其下
		}
		// `..` 开头 = 不在 root 之下；否则在之下。
		if len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' {
			continue
		}
		return true
	}
	return false
}

// ResetRawPersistStateForTests 重置落盘计数（测试用，避免跨测试污染）。
func ResetRawPersistStateForTests() {
	rawPersistMu.Lock()
	defer rawPersistMu.Unlock()
	persistCount = 0
}
