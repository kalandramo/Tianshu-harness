package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// 用户级验收：走**生产装配路径**（NewDefaultRegistry）验证 export_file。
func TestAccExportFileViaProductionRegistry(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	os.MkdirAll(ws, 0o755)

	reg := NewDefaultRegistry(Options{Cwd: ws})

	// ① 工具真的在注册表里（生产装配可见）
	var found bool
	for _, d := range reg.Definitions() {
		if d.Name == "export_file" {
			found = true
		}
	}
	if !found {
		t.Fatal("① export_file 应在生产注册表中")
	}
	t.Log("① 生产注册表含 export_file")

	// ② 通过注册表执行导出（工作区外目标）
	dest := filepath.Join(base, "exported", "note.txt")
	res, err := reg.Execute(nil, "export_file", &CallParams{
		Input: map[string]any{"destination_path": dest, "content": "acc-payload"},
	})
	if err != nil {
		t.Fatalf("② Execute 不应返回 error：%v", err)
	}
	if res.IsError {
		t.Fatalf("② 导出应成功，实得 %q", res.Content)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("② 文件应落盘：%v", err)
	}
	if string(got) != "acc-payload" {
		t.Fatalf("② 内容应为 acc-payload，实得 %q", got)
	}
	t.Logf("② 导出成功且内容正确：%q", got)

	// ③ 敏感文件拒绝（安全边界）
	secret := filepath.Join(base, ".env")
	os.WriteFile(secret, []byte("SECRET=x"), 0o600)
	res3, _ := reg.Execute(nil, "export_file", &CallParams{
		Input: map[string]any{"destination_path": filepath.Join(base, "leak.txt"), "source_path": secret},
	})
	if !res3.IsError {
		t.Fatalf("③ 敏感文件应被拒，实得 %q", res3.Content)
	}
	if _, err := os.Stat(filepath.Join(base, "leak.txt")); err == nil {
		t.Fatal("③ 被拒后不得真的复制")
	}
	t.Logf("③ 敏感文件被拒且未落盘：%s", res3.Content)
}
