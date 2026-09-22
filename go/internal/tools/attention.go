package tools

import (
	"path"
	"regexp"
	"strings"
)

// attention.go —— 按注意力价值分级仓库相对路径。
//
// 对账 TS `src/context/attention-filter.ts`。
//
// # 设计意图（逐字对账 TS 文件头）
//
// 这是一个**纯结构分类器**：不读 git、.gitignore、配置或文件内容。
// **未知路径 fail toward content**——真实的人类文件保持可见，除非它们
// 匹配到已被证实的 runtime/build/foreign 形状。
//
// # 消费者
//
// `repo_map`（跳过 silent 项 + L0_build 目录）、`inspect_project`（跳过
// silent 目录）、`readpolicy` 族。**Go 侧此前无此能力**——本刀新建。

// AttentionTier 是注意力分级。
//
// 对账 TS `AttentionTier`（attention-filter.ts:3）。
type AttentionTier string

const (
	// TierL0Build 是构建产物（最高噪声）。
	TierL0Build AttentionTier = "L0_build"
	// TierL1Fragment 是碎片（日志、锁、运行时数据）。
	TierL1Fragment AttentionTier = "L1_fragment"
	// TierL2Foreign 是外部 agent 目录。
	TierL2Foreign AttentionTier = "L2_foreign"
	// TierL3Content 是真实内容（默认）。
	TierL3Content AttentionTier = "L3_content"
)

// AttentionVerdict 是分级判定结果。
//
// 对账 TS `AttentionVerdict`（attention-filter.ts:5-9）。
type AttentionVerdict struct {
	Tier   AttentionTier
	Silent bool
	Reason string
}

// attentionBuildDirs 对账 TS `BUILD_DIRS`（attention-filter.ts:11-19）。
var attentionBuildDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
	"__pycache__":  true,
	"coverage":     true,
}

// attentionForeignDirs 对账 TS `FOREIGN_DIRS`（attention-filter.ts:21-28）。
var attentionForeignDirs = map[string]bool{
	".agents":    true,
	".codex":     true,
	".obsidian":  true,
	".claude":    true,
	".cursor":    true,
	".od-skills": true,
}

// attentionFragmentExts 对账 TS `FRAGMENT_EXTENSIONS`（attention-filter.ts:30-38）。
var attentionFragmentExts = map[string]bool{
	".log":         true,
	".lock":        true,
	".pid":         true,
	".swp":         true,
	".tmp":         true,
	".map":         true,
	".tsbuildinfo": true,
}

// attentionFragmentNames 对账 TS `FRAGMENT_FILENAMES`（attention-filter.ts:40-43）。
var attentionFragmentNames = map[string]bool{
	".DS_Store": true,
	"Thumbs.db": true,
}

// attentionRivetRuntimeDirs 对账 TS `RIVET_RUNTIME_DIRS`（attention-filter.ts:45-55）。
var attentionRivetRuntimeDirs = map[string]bool{
	"sessions":    true,
	"tasks":       true,
	"plans":       true,
	"cache-log":   true,
	"sensorium":   true,
	"prefix-diag": true,
	"playbook":    true,
	"runtime":     true,
	"tmp":         true,
	"external":    true,
}

var (
	// attentionArchiveRe 对账 TS `hasArchiveExtension`（attention-filter.ts:65-67）。
	attentionArchiveRe = regexp.MustCompile(`(?i)\.(?:zip|tgz|tar\.gz)$`)
	// attentionSQLiteRe 对账 TS `hasSqliteSidecarName`（attention-filter.ts:69-71）。
	//
	// TS 是两条正则的或：`^meridian\.db(?:-.+)?$` 与
	// `^meridian\.db(?:\.shm|\.wal)?$`。合并后等价于 `^meridian\.db(-.+)?$`
	// ——但**保留两条**以免合并时误纳（如 `meridian.db.shm` 在第一条不匹配、
	// 第二条匹配）。语义上两条的并集见下。
	attentionSQLiteRe1 = regexp.MustCompile(`(?i)^meridian\.db(?:-.+)?$`)
	attentionSQLiteRe2 = regexp.MustCompile(`(?i)^meridian\.db(?:\.shm|\.wal)?$`)
)

// normalizeAttentionPath 对账 TS `normalizeRelPath`（attention-filter.ts:57-63）。
//
// 反斜杠→斜杠；去前导 `./`；连续斜杠折叠；去尾斜杠。
func normalizeAttentionPath(relPath string) string {
	s := strings.ReplaceAll(relPath, "\\", "/")
	s = strings.TrimPrefix(s, "./")
	s = multiSlashRe.ReplaceAllString(s, "/")
	s = strings.TrimSuffix(s, "/")
	return s
}

var multiSlashRe = regexp.MustCompile(`/+`)

// splitAttentionPath 对账 TS `splitPath`（attention-filter.ts:65-69）。
//
// 空路径或 `.` 返回空切片。
func splitAttentionPath(relPath string) []string {
	normalized := normalizeAttentionPath(relPath)
	if normalized == "" || normalized == "." {
		return nil
	}
	var out []string
	for _, seg := range strings.Split(normalized, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// attentionVerdict 对账 TS `verdict`（attention-filter.ts:73-75）。
//
// **关键**：`silent = tier != L3_content`——只有真实内容不静音。
func attentionVerdict(tier AttentionTier, reason string) AttentionVerdict {
	return AttentionVerdict{Tier: tier, Silent: tier != TierL3Content, Reason: reason}
}

// ClassifyPath 按注意力价值分类仓库相对路径。
//
// 对账 TS `classifyPath`（attention-filter.ts:91-123`）。判定链**顺序敏感**：
//
//  1. 空路径 → L3_content / empty-path
//  2. 首段命中 BUILD_DIRS → L0_build
//  3. 首段命中 FOREIGN_DIRS → L2_foreign
//  4. 首段 `.rivet`：第二段命中 RIVET_RUNTIME_DIRS → L1_fragment；
//     或「恰两段且末段 .jsonl」→ L1_fragment；或 sqlite sidecar → L1_fragment
//  5. 末段命中 FRAGMENT_FILENAMES → L1_fragment / os-noise
//  6. 扩展名命中 FRAGMENT_EXTENSIONS → L1_fragment / fragment-ext
//  7. `.jsonl` 且首段 `.test-tmp` → L1_fragment / runtime-jsonl
//  8. 归档扩展名 → L1_fragment / archive-artifact
//  9. 首段 `.test-tmp` → L1_fragment / test-runtime
//  10. 否则 → L3_content / content-default
func ClassifyPath(relPath string) AttentionVerdict {
	parts := splitAttentionPath(relPath)
	if len(parts) == 0 {
		return attentionVerdict(TierL3Content, "empty-path")
	}

	first := parts[0]
	fileName := parts[len(parts)-1]
	// 对账 TS 的 `path.posix.extname`——**posix** 语义，故用 path 包而非
	// filepath（Windows 上 filepath.Ext 会把反斜杠当分隔符，但此处已归一化）。
	ext := path.Ext(fileName)

	if attentionBuildDirs[first] {
		return attentionVerdict(TierL0Build, "build-dir")
	}
	if attentionForeignDirs[first] {
		return attentionVerdict(TierL2Foreign, "foreign-agent-dir")
	}

	if first == ".rivet" {
		if len(parts) > 1 && attentionRivetRuntimeDirs[parts[1]] {
			return attentionVerdict(TierL1Fragment, "rivet-runtime")
		}
		if len(parts) == 2 && strings.HasSuffix(fileName, ".jsonl") {
			return attentionVerdict(TierL1Fragment, "rivet-jsonl")
		}
		if attentionSQLiteRe1.MatchString(fileName) || attentionSQLiteRe2.MatchString(fileName) {
			return attentionVerdict(TierL1Fragment, "rivet-db")
		}
	}

	if attentionFragmentNames[fileName] {
		return attentionVerdict(TierL1Fragment, "os-noise")
	}
	if attentionFragmentExts[ext] {
		return attentionVerdict(TierL1Fragment, "fragment-ext")
	}
	if strings.HasSuffix(fileName, ".jsonl") && first == ".test-tmp" {
		return attentionVerdict(TierL1Fragment, "runtime-jsonl")
	}
	if attentionArchiveRe.MatchString(fileName) {
		return attentionVerdict(TierL1Fragment, "archive-artifact")
	}
	if first == ".test-tmp" {
		return attentionVerdict(TierL1Fragment, "test-runtime")
	}

	return attentionVerdict(TierL3Content, "content-default")
}
