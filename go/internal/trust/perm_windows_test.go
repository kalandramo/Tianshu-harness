//go:build windows

package trust

import (
	"os"
	"testing"
)

// assertTrustFilePerm 在 Windows 上断言信任文件**确实落盘且可读**。
//
// **平台限制（重要，勿误读为测试缺口）**：POSIX 的 0o600 语义在 Windows 上
// **无法用 mode 位表达**——`os.Chmod` 只支持只读位（Go 把任何含写位的 mode
// 一律映射为 0666），而 `os.WriteFile` 的 perm 参数在 Windows 上被内核忽略。
// 实测：写入传 0o600 的文件，`info.Mode().Perm()` 恒为 0666。
//
// Windows 上真正的访问控制边界是 **NTFS ACL**——信任文件落在
// `<用户 profile>\.rivet\` 下，ACL 继承自该目录（仅当前用户 + SYSTEM +
// Administrators）。这由操作系统保证，不是 Go 代码能设置或观测的。
//
// 故此处退化为存在性断言。代价：把 `os.WriteFile` 的 0o600 改成任意值，
// 在本平台**不可判别**（没有可观测差异）——这是平台固有限制，不是测试写错。
// Unix 侧（perm_unix_test.go）保留逐位断言，那里的 mode 位是内核强制的边界。
//
// 不用 `t.Skipf` 整测跳过：存在性断言仍能抓住「写入失败/路径错误」这类回归，
// 而 Skipf 会让它们在本平台静默不可见（项目纪律：Skipf 是变异的隐身衣）。
func assertTrustFilePerm(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("信任文件应已落盘：%v", err)
	}
	if info.IsDir() {
		t.Errorf("信任文件不应是目录：%s", path)
	}
	if info.Size() == 0 {
		t.Errorf("信任文件不应为空：%s", path)
	}
}
