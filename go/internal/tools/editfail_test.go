package tools

import (
	"context"
	"strings"
	"testing"
)

// TestEditFailGatePrefixThreshold —— 门禁前缀的阈值语义（<3 空、≥3 非空）。
func TestEditFailGatePrefixThreshold(t *testing.T) {
	resetEditFailCountForTests()
	path := "/tmp/gate-test.go"

	if got := editFailGatePrefix(path, "hash_edit"); got != "" {
		t.Errorf("0 次失败应无前缀，得到 %q", got)
	}
	incrementEditFailCount(path)
	if got := editFailGatePrefix(path, "hash_edit"); got != "" {
		t.Errorf("1 次失败应无前缀，得到 %q", got)
	}
	incrementEditFailCount(path)
	if got := editFailGatePrefix(path, "hash_edit"); got != "" {
		t.Errorf("2 次失败应无前缀，得到 %q", got)
	}
	incrementEditFailCount(path)
	got := editFailGatePrefix(path, "hash_edit")
	if got == "" {
		t.Fatal("3 次失败应有前缀")
	}
	// 文案对账 TS：`此文件已连续 hash_edit 失败 3 次，再次编辑前必须先重新 read_file。\n\n`
	for _, want := range []string{"此文件已连续", "hash_edit", "失败 3 次", "必须先重新 read_file"} {
		if !strings.Contains(got, want) {
			t.Errorf("前缀应含 %q：%q", want, got)
		}
	}
}

// TestEditFailCountReset —— 成功后清零。
func TestEditFailCountReset(t *testing.T) {
	resetEditFailCountForTests()
	path := "/tmp/reset-test.go"

	for i := 0; i < 5; i++ {
		incrementEditFailCount(path)
	}
	if editFailGatePrefix(path, "x") == "" {
		t.Fatal("5 次失败应有前缀")
	}
	resetEditFailCount(path)
	if got := editFailGatePrefix(path, "x"); got != "" {
		t.Errorf("清零后应无前缀，得到 %q", got)
	}
}

// TestEditFailCountPerFile —— 计数按文件独立（不串味）。
func TestEditFailCountPerFile(t *testing.T) {
	resetEditFailCountForTests()
	a, b := "/tmp/a.go", "/tmp/b.go"

	incrementEditFailCount(a)
	incrementEditFailCount(a)
	incrementEditFailCount(a)
	incrementEditFailCount(b)

	if editFailGatePrefix(a, "x") == "" {
		t.Error("a 已 3 次应有前缀")
	}
	if got := editFailGatePrefix(b, "x"); got != "" {
		t.Errorf("b 只 1 次不应有前缀，得到 %q", got)
	}
}

// TestEditFailGateWiredIntoHashEdit —— **接线验证**：hash_edit 语法错误
// 连续 3 次后，第 3 次的报错文案应含门禁前缀。
func TestEditFailGateWiredIntoHashEdit(t *testing.T) {
	resetEditFailCountForTests()
	dir := t.TempDir()
	original := "package main\n\nfunc main() {}\n"

	var lastContent string
	for i := 1; i <= 3; i++ {
		writeRepoFile(t, dir, "g.go", original)
		tool := HashEdit(dir, nil)
		p := &CallParams{
			Input: map[string]any{
				"file_path":  "g.go",
				"anchors":    []any{"L1", "L3"},
				"new_string": "package main\n\nfunc main() {\n", // 缺右花括号
			},
			Cwd:          dir,
			ApprovalMode: "dangerously-skip-permissions",
		}
		res, _ := tool.Execute(context.Background(), p)
		if !res.IsError {
			t.Fatalf("第 %d 次应失败：%s", i, res.Content)
		}
		lastContent = res.Content
		// 前两次不应有门禁前缀
		if i < 3 && strings.Contains(res.Content, "必须先重新 read_file") {
			t.Errorf("第 %d 次不应有门禁前缀：%s", i, res.Content)
		}
	}
	// 第 3 次应有门禁前缀
	if !strings.Contains(lastContent, "必须先重新 read_file") {
		t.Errorf("第 3 次应含门禁前缀：%s", lastContent)
	}
	if !strings.Contains(lastContent, "失败 3 次") {
		t.Errorf("应报告失败次数 3：%s", lastContent)
	}
}

// TestEditFailGateResetOnSuccess —— 成功编辑后门禁解除。
func TestEditFailGateResetOnSuccess(t *testing.T) {
	resetEditFailCountForTests()
	dir := t.TempDir()

	// 先失败 3 次
	for i := 0; i < 3; i++ {
		writeRepoFile(t, dir, "r.go", "package main\n\nfunc main() {}\n")
		tool := HashEdit(dir, nil)
		p := &CallParams{
			Input: map[string]any{
				"file_path":  "r.go",
				"anchors":    []any{"L1", "L3"},
				"new_string": "package main\n\nfunc main() {\n",
			},
			Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		}
		_, _ = tool.Execute(context.Background(), p)
	}

	// 再成功一次
	writeRepoFile(t, dir, "r.go", "package main\n\nvar x = 1\n")
	tool := HashEdit(dir, nil)
	p := &CallParams{
		Input: map[string]any{
			"file_path":  "r.go",
			"anchors":    []any{"L3"},
			"new_string": "var x = 2",
		},
		Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
	}
	res, _ := tool.Execute(context.Background(), p)
	if res.IsError {
		t.Fatalf("合法编辑应成功：%s", res.Content)
	}

	// 门禁应已解除
	if got := editFailGatePrefix(dir+"/r.go", "hash_edit"); got != "" {
		t.Errorf("成功后门禁应解除，得到 %q", got)
	}
}

// TestEditFailGateWiredIntoWriteFile —— write_file 同样接入门禁。
func TestEditFailGateWiredIntoWriteFile(t *testing.T) {
	resetEditFailCountForTests()
	dir := t.TempDir()

	var last string
	for i := 1; i <= 3; i++ {
		tool := WriteFile(dir, nil)
		p := &CallParams{
			Input: map[string]any{
				"file_path": "w.go",
				"content":   "package main\n\nfunc main() {\n", // 缺右花括号
			},
			Cwd: dir, ApprovalMode: "dangerously-skip-permissions",
		}
		res, _ := tool.Execute(context.Background(), p)
		if !res.IsError {
			t.Fatalf("第 %d 次应失败：%s", i, res.Content)
		}
		last = res.Content
	}
	if !strings.Contains(last, "必须先重新 read_file") {
		t.Errorf("write_file 第 3 次应含门禁前缀：%s", last)
	}
}
