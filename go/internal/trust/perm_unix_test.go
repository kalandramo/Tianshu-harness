//go:build !windows

package trust

import (
	"os"
	"testing"
)

// assertTrustFilePerm 断言信任文件的权限为 0o600（用户私有）。
//
// Unix：mode 位是**内核强制执行**的安全边界——组/其他用户不可读写。
// 这是真实的加固，必须逐位断言（写错 chmod 值会立刻变红）。
func assertTrustFilePerm(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限应为 0o600，得到 %o", perm)
	}
}
