package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashEditSyntaxErrorRollsBack —— **核心**：编辑引入语法错误时自动回滚。
//
// 用 .go 文件（Go 版有原生解析器，判定最权威）。
func TestHashEditSyntaxErrorRollsBack(t *testing.T) {
	dir := t.TempDir()
	original := "package main\n\nfunc main() {}\n"
	writeRepoFile(t, dir, "m.go", original)

	tool := HashEdit(dir, nil)
	// 整体替换成缺右花括号的内容
	p := &CallParams{
		Input: map[string]any{
			"file_path":  "m.go",
			"anchors":    []any{"L1", "L3"},
			"new_string": "package main\n\nfunc main() {\n",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if !res.IsError {
		t.Fatalf("语法错误的编辑应失败（若这里失败说明检查未触发）：%s", res.Content)
	}
	// 错误文案应说明回滚
	if !strings.Contains(res.Content, "回滚") {
		t.Errorf("错误应说明回滚状态：%s", res.Content)
	}
	// **关键断言**：文件恢复原样
	if got := readRepoFile(t, dir, "m.go"); got != original {
		t.Errorf("应回滚到编辑前内容：\n  期望=%q\n  实际=%q", original, got)
	}
}

// TestHashEditValidGoNoRollback —— 合法 Go 编辑不回滚。
func TestHashEditValidGoNoRollback(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "ok.go", "package main\n\nvar x = 1\n")

	tool := HashEdit(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path":  "ok.go",
			"anchors":    []any{"L3"},
			"new_string": "var x = 42",
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if res.IsError {
		t.Fatalf("合法编辑不应失败：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "ok.go"); !strings.Contains(got, "42") {
		t.Errorf("合法编辑应生效，得到 %q", got)
	}
}

// TestApplyPatchSyntaxErrorRollsBack —— **核心**：补丁引入语法错误时整个补丁回滚。
func TestApplyPatchSyntaxErrorRollsBack(t *testing.T) {
	dir := gitRepoForTest(t)
	original := "package main\n\nfunc main() {}\n"
	writeRepoFile(t, dir, "p.go", original)
	gitCommitAll(t, dir, "init")

	// 补丁：删掉右花括号（语法错误但 diff 本身合法）
	diff := `--- a/p.go
+++ b/p.go
@@ -1,3 +1,3 @@
 package main
 
-func main() {}
+func main() {
`
	p, tool := applyPatchCall(dir, diff)
	res, err := tool.Execute(context.Background(), p)
	if err != nil {
		t.Fatalf("Execute 错误：%v", err)
	}
	if !res.IsError {
		t.Fatal("引入语法错误的补丁应失败")
	}
	if !strings.Contains(res.Content, "致命错误") || !strings.Contains(res.Content, "已自动回滚") {
		t.Errorf("错误文案应说明语法错误与回滚：%s", res.Content)
	}
	// **关键断言**：文件回滚
	if got := readRepoFile(t, dir, "p.go"); got != original {
		t.Errorf("应回滚到补丁前内容：\n  期望=%q\n  实际=%q", original, got)
	}
}

// TestApplyPatchValidGoNoRollback —— 合法 Go 补丁不回滚。
func TestApplyPatchValidGoNoRollback(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "v.go", "package main\n\nvar x = 1\n")
	gitCommitAll(t, dir, "init")

	diff := `--- a/v.go
+++ b/v.go
@@ -1,3 +1,3 @@
 package main
 
-var x = 1
+var x = 99
`
	p, tool := applyPatchCall(dir, diff)
	res, _ := tool.Execute(context.Background(), p)
	if res.IsError {
		t.Fatalf("合法补丁应成功：%s", res.Content)
	}
	if got := readRepoFile(t, dir, "v.go"); !strings.Contains(got, "99") {
		t.Errorf("补丁应生效，得到 %q", got)
	}
}

// TestApplyPatchConflictMarkersCaughtBySyntax —— 冲突标记被语法检查捕获。
//
// `git apply --3way` 真走合并路径时会留下 `<<<<<<<` 标记——对 .go 文件
// 这是致命语法错误。但注意：该路径下 applyPatchGitRun 会返回失败（exit 1），
// 故走的是**失败回滚**分支而非语法检查分支。此测试锁的是「无论如何都回滚」。
func TestApplyPatchConflictMarkersCaughtBySyntax(t *testing.T) {
	dir := gitRepoForTest(t)
	writeRepoFile(t, dir, "c.go", "package main\n\nvar a = 1\n")
	gitCommitAll(t, dir, "v1")

	// 生成合法 patch
	writeRepoFile(t, dir, "c.go", "package main\n\nvar a = 1\nvar b = 2\n")
	diff := gitRun(t, dir, "diff")
	gitRun(t, dir, "checkout", "--", "c.go")

	// 制造冲突
	writeRepoFile(t, dir, "c.go", "package main\n\nvar a = 999\n")
	gitCommitAll(t, dir, "v2")

	p, tool := applyPatchCall(dir, diff)
	res, _ := tool.Execute(context.Background(), p)
	if !res.IsError {
		t.Fatal("冲突补丁应失败")
	}
	// 文件不得残留冲突标记
	got := readRepoFile(t, dir, "c.go")
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Errorf("冲突标记应被清除，得到：\n%s", got)
	}
}

// TestWriteFileSyntaxCheckNotWired —— 记录现状：write_file 未接语法检查。
//
// TS 侧 write_file **也**调 checkSyntax，但 Go 侧本轮只接了 hash_edit 与
// apply_patch。此测试是**已知边界**的显式记录——若将来接入，此测试应改为
// 断言回滚行为。
func TestWriteFileSyntaxCheckNotWired(t *testing.T) {
	dir := t.TempDir()
	tool := WriteFile(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path": "bad.go",
			"content":   "package main\n\nfunc main() {\n", // 缺右花括号
		},
		Cwd:          dir,
		ApprovalMode: "dangerously-skip-permissions",
	}
	res, _ := tool.Execute(context.Background(), p)
	if res.IsError {
		t.Errorf("当前 write_file 未接语法检查（已知边界）——若已接入请更新此测试：%s", res.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.go")); err != nil {
		t.Error("write_file 应已写入（未回滚）")
	}
}
