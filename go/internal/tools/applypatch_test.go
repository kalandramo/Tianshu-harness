package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// applypatchOracle 是 TS APPLY_PATCH_TOOL.execute 的真实产出。
// 生成命令：npx tsx go/testdata/applypatch/gen-oracle.ts
type applypatchOracle struct {
	Cases map[string]struct {
		Note      string `json:"note"`
		Diff      string `json:"diff"`
		CheckOnly bool   `json:"checkOnly"`
		Result    struct {
			Content string `json:"content"`
			IsError bool   `json:"isError"`
			OK      bool   `json:"ok"`
			Error   string `json:"error"`
		} `json:"result"`
		Finals map[string]string `json:"finals"`
	} `json:"cases"`
}

func loadApplypatchOracle(t *testing.T) applypatchOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "applypatch", "oracle.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败（%s）：%v\n生成命令：npx tsx go/testdata/applypatch/gen-oracle.ts", path, err)
	}
	var o applypatchOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	return o
}

// gitAvailable 报告 git 是否可用（不可用时跳过对账）。
func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// initRepo 在 dir 建一个 git 仓库并提交初始文件。
func initRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败：%v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败：%v", err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v 失败：%v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if len(files) > 0 {
		run("add", "-A")
		run("commit", "-m", "init")
	}
}

// TestApplyPatchDefinition —— 工具定义与 schema。
func TestApplyPatchDefinition(t *testing.T) {
	tool := ApplyPatch(t.TempDir(), nil)
	def := tool.Definition()
	if def.Name != "apply_patch" {
		t.Errorf("工具名应为 apply_patch，得到 %q", def.Name)
	}
	if !tool.Enabled() {
		t.Error("应默认启用")
	}
	if tool.ConcurrencySafe() {
		t.Error("apply_patch 不应标记为并发安全")
	}
	if _, ok := def.InputSchema.Properties["diff"]; !ok {
		t.Error("schema 缺 diff")
	}
	if _, ok := def.InputSchema.Properties["check_only"]; !ok {
		t.Error("schema 缺 check_only")
	}
}

// TestApplyPatchOracleParity —— 与 TS oracle 逐用例对账。
//
// 需要 git 可执行文件；不可用时跳过（标注）。
func TestApplyPatchOracleParity(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git 不可用，跳过 apply_patch 对账")
	}
	o := loadApplypatchOracle(t)

	// 各用例的初始文件（从生成器的 cases 表复刻）
	initialFiles := map[string]map[string]string{
		"cleanApply":          {"a.txt": "line1\nline2\nline3\n"},
		"cleanApplyCheckOnly": {"a.txt": "line1\nline2\nline3\n"},
		"badContext":          {"a.txt": "line1\nline2\nline3\n"},
		"newFile":             {},
		"deleteFile":          {"a.txt": "line1\nline2\nline3\n"},
		"multiFile":           {"a.txt": "line1\nline2\nline3\n", "b.txt": "old\n"},
		"emptyDiff":           {},
		"pointerDiff":         {},
	}

	checked := 0
	for name, c := range o.Cases {
		if name == "notARepo" {
			continue // 单独测（需要不 init 的目录）
		}
		files, ok := initialFiles[name]
		if !ok {
			t.Errorf("[%s] 缺初始文件表", name)
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir, files)
			tool := ApplyPatch(dir, nil)

			input := map[string]any{"diff": c.Diff}
			if c.CheckOnly {
				input["check_only"] = true
			}
			res, err := tool.Execute(context.Background(), &CallParams{Input: input})
			if err != nil {
				t.Fatalf("Execute 返回 error：%v", err)
			}
			if res.IsError != c.Result.IsError {
				t.Errorf("isError 不符：Go=%v TS=%v\n  Go content: %s\n  TS content: %s",
					res.IsError, c.Result.IsError, res.Content, c.Result.Content)
			}
			if res.Content != c.Result.Content {
				// **已声明的降级差异**：TS 在 git apply 失败时主动回滚（备份 +
				// unstage），故文案含「已回滚到补丁前状态」；Go 侧未移植回滚，
				// 不加这句（不虚假声称已回滚）。差异仅在此前缀。
				goNorm := strings.Replace(res.Content, "补丁应用失败：", "补丁应用失败（已回滚到补丁前状态）：", 1)
				if goNorm == c.Result.Content {
					t.Logf("已声明降级：回滚文案（Go 无回滚故不含该前缀）")
				} else {
					t.Errorf("content 不符\n  Go =%q\n  TS =%q", res.Content, c.Result.Content)
				}
			}
			// 文件终态对账
			for rel, want := range c.Finals {
				p := filepath.Join(dir, rel)
				got, err := os.ReadFile(p)
				if err != nil {
					if want != "<ABSENT>" {
						t.Errorf("%s 应存在且为 %q，但读取失败：%v", rel, want, err)
					}
					continue
				}
				if want == "<ABSENT>" {
					t.Errorf("%s 应不存在，实际内容 %q", rel, string(got))
					continue
				}
				if string(got) != want {
					t.Errorf("%s 内容不符\n  Go =%q\n  TS =%q", rel, string(got), want)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("oracle 无用例")
	}
	t.Logf("对账了 %d 个用例", checked)
}

// TestApplyPatchNotARepo —— 非 git 仓库时 git apply 报错。
func TestApplyPatchNotARepo(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git 不可用")
	}
	o := loadApplypatchOracle(t)
	c, ok := o.Cases["notARepo"]
	if !ok {
		t.Fatal("oracle 缺 notARepo")
	}
	dir := t.TempDir()
	// **不** git init
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
	tool := ApplyPatch(dir, nil)
	res, err := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"diff": c.Diff}})
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	if !res.IsError {
		t.Error("非 git 仓库应报错")
	}
	if !strings.Contains(res.Content, "补丁应用失败") {
		t.Errorf("应含失败前缀：%q", res.Content)
	}
}

// TestApplyPatchEmptyDiff —— 空 diff 参数错误。
func TestApplyPatchEmptyDiff(t *testing.T) {
	tool := ApplyPatch(t.TempDir(), nil)
	for _, d := range []string{"", "   ", "\n\t "} {
		res, _ := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"diff": d}})
		if !res.IsError || !strings.Contains(res.Content, `需要非空的 "diff"`) {
			t.Errorf("空 diff %q 应报参数错误，得到 %q", d, res.Content)
		}
	}
}

// TestApplyPatchPointerGuard —— 指针占位符被拦截。
func TestApplyPatchPointerGuard(t *testing.T) {
	tool := ApplyPatch(t.TempDir(), nil)
	res, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"diff": "[patch applied to …]"},
	})
	if !res.IsError {
		t.Error("指针占位符应被拦截")
	}
	if !strings.Contains(res.Content, "pointer placeholder from message history") {
		t.Errorf("应含守卫 marker：%q", res.Content)
	}
	// 前置空白也应被拦（trimLeft）
	res2, _ := tool.Execute(context.Background(), &CallParams{
		Input: map[string]any{"diff": "  \n[patch applied to x]"},
	})
	if !res2.IsError {
		t.Error("前置空白的指针也应被拦截")
	}
}

// TestApplyPatchPathEscape —— 补丁目标逃逸出工作区时拒绝。
func TestApplyPatchPathEscape(t *testing.T) {
	tool := ApplyPatch(t.TempDir(), nil)
	diff := "--- a/../../etc/passwd\n+++ b/../../etc/passwd\n@@ -1 +1 @@\n-x\n+y\n"
	res, _ := tool.Execute(context.Background(), &CallParams{Input: map[string]any{"diff": diff}})
	if !res.IsError {
		t.Error("路径逃逸应被拒绝")
	}
	// **断言具体错误来源**——仅断言 IsError 没有判别力：去掉预检后
	// git apply 自身也会失败（但报的是「outside a repository」这类
	// 与路径无关的模糊错误）。预检的价值在于给出**指向路径**的明确错误。
	if !strings.Contains(res.Content, "补丁目标") {
		t.Errorf("应给「补丁目标 X：…」形式的路径预检错误，实际 %q", res.Content)
	}
	if !strings.Contains(res.Content, "outside project directory") {
		t.Errorf("应含路径越界说明，实际 %q", res.Content)
	}
}

// TestNormalizeDiffPaths —— 只归一化头部行。
func TestNormalizeDiffPaths(t *testing.T) {
	in := "diff --git a\\x.txt b\\x.txt\n--- a\\x.txt\n+++ b\\x.txt\n@@ -1 +1 @@\n-a\\b\n+c\\d\n"
	got := normalizeDiffPaths(in)
	// 头部行反斜杠→正斜杠
	if !strings.Contains(got, "--- a/x.txt") {
		t.Errorf("--- 行应归一化：%q", got)
	}
	if !strings.Contains(got, "+++ b/x.txt") {
		t.Errorf("+++ 行应归一化：%q", got)
	}
	if !strings.Contains(got, "diff --git a/x.txt b/x.txt") {
		t.Errorf("diff --git 行应归一化：%q", got)
	}
	// **内容行不动**（数据里的反斜杠必须保留）
	if !strings.Contains(got, "-a\\b") {
		t.Errorf("内容行不应被改动：%q", got)
	}
	if !strings.Contains(got, "+c\\d") {
		t.Errorf("内容行不应被改动：%q", got)
	}
}

// TestTruncateDiffForUI —— 超限截断并附提示。
func TestTruncateDiffForUI(t *testing.T) {
	short := "a\nb\nc"
	if got := truncateDiffForUI(short, 10); got != short {
		t.Errorf("未超限应原样返回：%q", got)
	}
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("line" + itoa(i) + "\n")
	}
	long := strings.TrimSuffix(sb.String(), "\n")
	got := truncateDiffForUI(long, 5)
	lines := strings.Split(got, "\n")
	if len(lines) != 6 {
		t.Errorf("应为 5 行 + 1 行提示，得到 %d 行", len(lines))
	}
	if !strings.Contains(lines[5], "另有 15 行 diff") {
		t.Errorf("提示行应含隐藏行数：%q", lines[5])
	}
}
