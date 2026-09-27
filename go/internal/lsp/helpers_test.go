package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempFile 在 dir 下写一个文本文件（必要时建父目录）。
//
// 返回绝对路径。测试用——真实验证「didOpen 发的是磁盘上的真实内容」。
func writeTempFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	return writeTempBytes(t, dir, rel, []byte(content))
}

// writeTempBytes 同上，但写任意字节。
func writeTempBytes(t *testing.T, dir, rel string, content []byte) string {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
	return abs
}
