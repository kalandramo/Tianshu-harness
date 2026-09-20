//go:build !windows

package prompt

// hostOSType / hostOSRelease：Unix 分支——与 Node 的 os.type()/os.release()
// 同源（Node 在 Unix 上用 uname(2)，输出与 `uname -s`/`-r` 一致，实测 Darwin
// 上均为 "Darwin" / "25.6.0"）。

func hostOSType() string {
	return unameField("-s", fallbackOSType())
}

func hostOSRelease() string {
	return unameField("-r", "")
}
