//go:build !windows

package plan

import "os"

// birthTimeMillis 返回文件的创建时间（Unix 毫秒）。
//
// 对账 TS `stat().birthtime`（plan-store.ts:243 等）——TS 用它作
// PlanDocument.createdAt，且 ListPlans 按它降序排序。
//
// # 平台差异（**已知偏差，如实记录**）
//
// Linux 上 `statx` 的 btime 需要显式请求（Go 的 `os.Stat` 在部分内核/文件
// 系统上返回零值），macOS/BSD 有 `Birthtimespec`。为免在 Linux 上拿到 1970
// 年的时间戳（会把排序彻底打乱），此处**统一回退 `ModTime`**——语义上是
// 「最后修改时间」，与「创建时间」在计划文件场景下差异极小（计划文件写入后
// 基本只被 close/approve 追加标记，ModTime 即最近一次状态变更时间）。
//
// **这是与 TS 的已知偏差**：TS 的 `birthtime` 在 Linux 上同样可能是零值
// （Node 的 `stat().birthtime` 在部分平台返回 epoch），故两侧行为在 Linux
// 上都不理想——Go 侧的 ModTime 回退实际更稳定。Windows 走
// `filetime_windows.go` 的真创建时间。
func birthTimeMillis(info os.FileInfo) int64 {
	return info.ModTime().UnixMilli()
}
