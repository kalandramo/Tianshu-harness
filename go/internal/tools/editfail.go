package tools

import (
	"runtime"
	"strings"
	"sync"
)

// 编辑失败计数门。
//
// 对账 src/tools/read-file.ts 的 `editFailCount`（Map<string, number>）+
// 三个函数（increment / reset / 测试钩子）。
//
// **语义**：按文件累计**连续**编辑失败次数；≥3 时编辑工具的报错文案前置一句
// 门禁提示（「再次编辑前必须先重新 read_file」）。成功编辑清零。
//
// **注意**：这只是**提示性门禁**（在错误文案里加前缀），不是硬拒绝——
// 对账 TS 的 `gatePrefix` 语义。
var (
	editFailMu    sync.Mutex
	editFailCount = map[string]int{}
)

// canonicalPathKey 归一化路径键（对账 TS 的 canonicalPathKey）。
//
// POSIX 原样返回（大小写敏感文件系统，**不能** lowercase）；
// Windows 上转 POSIX 分隔符并 lowercase。
func canonicalPathKey(filePath string) string {
	if runtime.GOOS != "windows" {
		return filePath
	}
	return strings.ToLower(strings.ReplaceAll(filePath, `\`, "/"))
}

// incrementEditFailCount 递增并返回新的失败次数。
func incrementEditFailCount(path string) int {
	key := canonicalPathKey(path)
	editFailMu.Lock()
	defer editFailMu.Unlock()
	editFailCount[key]++
	return editFailCount[key]
}

// resetEditFailCount 清零失败计数（成功编辑后调用）。
func resetEditFailCount(path string) {
	key := canonicalPathKey(path)
	editFailMu.Lock()
	defer editFailMu.Unlock()
	delete(editFailCount, key)
}

// editFailGatePrefix 生成门禁前缀（≥3 次失败时非空）。
//
// 对账 TS 的 gatePrefix 构造：
//
//	`此文件已连续 hash_edit 失败 ${fails} 次，再次编辑前必须先重新 read_file。\n\n`
//
// `toolName` 是调用方工具名（write_file / hash_edit），用于文案对账。
func editFailGatePrefix(path, toolName string) string {
	key := canonicalPathKey(path)
	editFailMu.Lock()
	fails := editFailCount[key]
	editFailMu.Unlock()
	if fails < 3 {
		return ""
	}
	return "此文件已连续 " + toolName + " 失败 " + itoa(fails) + " 次，再次编辑前必须先重新 read_file。\n\n"
}

// resetEditFailCountForTests 清空全部计数（测试钩子，对账
// __resetEditFailCountForTests）。
func resetEditFailCountForTests() {
	editFailMu.Lock()
	defer editFailMu.Unlock()
	editFailCount = map[string]int{}
}
