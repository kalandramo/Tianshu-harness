package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildCLIBinary 构建 CLI 测试二进制并返回**可执行路径**。
//
// **为什么需要平台后缀**：Windows 上 `go build -o tianshu-test` 产出的是
// 无扩展名文件，但 Go 的 `exec.Command` 走 `LookPath` 语义——按 `PATHEXT`
// 补 `.exe` 查找。于是 `exec.Command("<tmp>/tianshu-test")` 报
// 「executable file not found in %PATH%」，而同一个文件用 shell 直接跑是
// 成功的（MSYS/Git-Bash 会自行补扩展名）。这个不一致让 CLI E2E 在 Windows
// 上整片红，且报错指向「找不到可执行文件」而非被测逻辑——是夹具缺陷。
//
// 加 `.exe` 后缀后两个平台都能被 exec 正确解析（Unix 不关心扩展名）。
func buildCLIBinary(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建失败：%v\n%s", err, out)
	}
	return bin
}
