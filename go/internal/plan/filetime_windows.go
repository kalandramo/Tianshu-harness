//go:build windows

package plan

import (
	"os"
	"syscall"
)

// birthTimeMillis 返回文件的创建时间（Unix 毫秒）。
//
// 对账 TS `stat().birthtime`（plan-store.ts:243 等）——TS 用它作
// PlanDocument.createdAt，且 ListPlans 按它降序排序。
//
// # 平台差异（探针实测）
//
// Windows 上 `info.Sys()` 是 `*syscall.Win32FileAttributeData`，其
// `CreationTime` 是真正的创建时间（NTFS 记录）。实测确认可用：
//
//	CreationTime ms=1790055023812
//
// 取不到时回退 `ModTime`（见 filetime_other.go 的同款回退）。
func birthTimeMillis(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return st.CreationTime.Nanoseconds() / 1e6
	}
	return info.ModTime().UnixMilli()
}
