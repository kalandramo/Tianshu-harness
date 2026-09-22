package tools

// scanexcludes.go —— 文件系统遍历工具**不得下探**的目录名基线。
//
// 对账 TS `src/tools/scan-excludes.ts`。
//
// # 为什么必须在**目录级**剪枝（逐字对账 TS 注释）
//
// 逐文件过滤（对每个命中做 gitignore 检查）仍要为每个条目付 `readdir` +
// `lstat`，而那正是成本所在：本仓库的 `desktop/src-tauri/target/debug/deps`
// 单独就有约 19k 文件 / 4.2GB，桌面应用自己的数据目录还嵌在里面。
//
// 这是**共享基线**而非全部——调用方各自并上自己的额外项
// （`repo_map` 还跳过 `.cache`，ast 工具还跳过 `.rivet`）。
// **让基线处处一致才是重点**：它曾被复制粘贴到七个文件里各自漂移，
// 而丢掉 `target` 的那两个正是走进 4.2GB 构建树的那两个。
//
// **故意不含** `.rivet`——计划、skills 与项目知识住在那里，是合法的搜索
// 目标（`read_file` 出于同样理由豁免它）。想跳过它的工具自行添加。
//
// `TianshuData` 在此是**相反**的情形：桌面应用便携模式的数据目录
// （sessions / caches / logs，2402 文件）恰好把自己的 `.rivet` 嵌在里面。
// 直接点名它意味着无论装在何处都会跳过运行时数据，而不是只在它像今天这样
// 落在 `target/` 下时才跳过。

// ScanExcludeDirs 是共享剪枝基线。
//
// 对账 TS `SCAN_EXCLUDE_DIRS`（scan-excludes.ts:24-27）——**顺序逐字相同**。
var ScanExcludeDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	".next":        true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
	"TianshuData":  true,
}

// IsScanExcludedDir 报告目录条目名是否在共享剪枝基线内。
//
// 对账 TS `isScanExcludedDir`（scan-excludes.ts:30-32）。
func IsScanExcludedDir(name string) bool {
	return ScanExcludeDirs[name]
}
