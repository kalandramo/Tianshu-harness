//go:build windows

package prompt

import (
	"fmt"
	"syscall"
	"unsafe"
)

// hostOSType / hostOSRelease：Windows 分支——与 Node 的 os.type()/os.release()
// 同源（Node 在 Windows 上走 Win32 `RtlGetVersion`）。
//
// **为什么不能用 `uname`**（实测踩到）：
//
//	Node  os.type()/os.release()  → "Windows_NT" / "10.0.26200"
//	Git Bash `uname -s`/`-r`      → "MINGW64_NT-10.0-26200" / "3.6.9-b4195d69.x86_64"
//
// 两者不等价，且 uname 的结果**依赖 PATH 上有没有 Git**（没有则命令失败、
// 落到 fallback 得到第三种值）——同一个二进制在不同启动环境下产出不同的
// 冻结前缀，这是最坏的一类不确定性。
//
// 该行进 `<environment>`，属冻结前缀；不等价会让 Go/TS 的缓存 key 分叉。
//
// 用 `syscall.NewLazyDLL` 而非 golang.org/x/sys/windows：前者是标准库，
// 零依赖即可调用 RtlGetVersion（后者会引入一个新模块依赖，收益不抵成本）。
// RtlGetVersion 优于 GetVersionExW——后者在 Win8.1+ 上对未声明 manifest 的
// 进程会撒谎（谎报 6.2）。

// osVersionInfoW 是 RtlGetVersion 的输出结构。
//
// 只需前五个字段取版本号；CSDVersion 按官方结构补齐（sizeof 必须正确，
// 否则 API 会以 STATUS_BUFFER_TOO_SMALL 失败）。
type osVersionInfoW struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
}

// hostOSType 恒返回 "Windows_NT"（对账 Node os.type() 在 Windows 上的定值）。
func hostOSType() string {
	return "Windows_NT"
}

// hostOSRelease 返回 "major.minor.build"（对账 Node os.release()）。
//
// 实测本机：RtlGetVersion → 10.0.26200，Node os.release() → "10.0.26200"，
// 逐字节相同。探测失败时回落到 runtime 常量派生的空串（宁可缺也不编）。
func hostOSRelease() string {
	ntdll := syscall.NewLazyDLL("ntdll.dll")
	rtlGetVersion := ntdll.NewProc("RtlGetVersion")

	var info osVersionInfoW
	info.OSVersionInfoSize = uint32(unsafe.Sizeof(info))
	ret, _, _ := rtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	// NTSTATUS：0 = STATUS_SUCCESS。非零表示调用失败（不该发生）。
	if ret != 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
