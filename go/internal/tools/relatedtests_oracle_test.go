package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/api/wire"
)

// oracleCase 是差分对账的一条用例。
type rtOracleCase struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Content string `json:"content"`
	IsError bool   `json:"isError"`
}

type rtOracle struct {
	Files  []string       `json:"files"`
	Source []rtOracleCase `json:"source"`
	Test   []rtOracleCase `json:"test"`
	Error  []rtOracleCase `json:"error"`
}

// loadRTOracle 读 oracle.json 并在临时目录重建同一份 fixture。
//
// fixture 由 gen-oracle.ts 与本报文各自重建（不进版本库——见 .gitignore），
// 保证两侧文件系统布局一致。
func loadRTOracle(t *testing.T) (*rtOracle, string) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/relatedtests/oracle.json")
	if err != nil {
		t.Fatalf("读 oracle.json 失败: %v", err)
	}
	var o rtOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle.json 失败: %v", err)
	}

	cwd := t.TempDir()
	for _, f := range o.Files {
		abs := filepath.Join(cwd, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(abs, []byte("// "+f+"\n"), 0o644); err != nil {
			t.Fatalf("写 fixture 失败: %v", err)
		}
	}
	return &o, cwd
}

func runRT(t *testing.T, cwd string, file string) (string, bool) {
	t.Helper()
	tool := RelatedTests(cwd)
	res, err := tool.Execute(context.Background(), &CallParams{
		Input:     map[string]any{"file": file},
		Cwd:       cwd,
		ToolUseID: "t",
	})
	if err != nil {
		t.Fatalf("Execute 返回 error: %v", err)
	}
	return res.Content, res.IsError
}

// TestOracleRelatedTestsSource 对账「源文件 → 测试」路径。
func TestOracleRelatedTestsSource(t *testing.T) {
	o, cwd := loadRTOracle(t)
	for _, c := range o.Source {
		t.Run(c.Name, func(t *testing.T) {
			got, gotErr := runRT(t, cwd, c.File)
			if gotErr != c.IsError {
				t.Errorf("isError 不一致：期望 %v，实得 %v（content=%q）", c.IsError, gotErr, got)
			}
			if got != c.Content {
				t.Errorf("content 不一致\n期望: %q\n实得: %q", c.Content, got)
			}
		})
	}
}

// TestOracleRelatedTestsReverse 对账「测试文件 → 源文件」路径。
func TestOracleRelatedTestsReverse(t *testing.T) {
	o, cwd := loadRTOracle(t)
	for _, c := range o.Test {
		t.Run(c.Name, func(t *testing.T) {
			got, gotErr := runRT(t, cwd, c.File)
			if gotErr != c.IsError {
				t.Errorf("isError 不一致：期望 %v，实得 %v（content=%q）", c.IsError, gotErr, got)
			}
			if got != c.Content {
				t.Errorf("content 不一致\n期望: %q\n实得: %q", c.Content, got)
			}
		})
	}
}

// TestOracleRelatedTestsErrors 对账错误分支（绝对路径 / `..` 拒绝）。
func TestOracleRelatedTestsErrors(t *testing.T) {
	o, cwd := loadRTOracle(t)
	for _, c := range o.Error {
		t.Run(c.Name, func(t *testing.T) {
			got, gotErr := runRT(t, cwd, c.File)
			if gotErr != c.IsError {
				t.Errorf("isError 不一致：期望 %v，实得 %v（content=%q）", c.IsError, gotErr, got)
			}
			if got != c.Content {
				t.Errorf("content 不一致\n期望: %q\n实得: %q", c.Content, got)
			}
		})
	}
}

// TestRelatedTestsDedupNotApplied 锁定「TS 不去重」这一行为。
//
// oracle `py-flat-top`：`flat.py` 的 dir/parentDir/relDir 都是 `.`，
// 4 个候选撞同一路径，TS 原样返回 4 次。若实现「优化」成去重，此测试红。
func TestRelatedTestsDedupNotApplied(t *testing.T) {
	o, cwd := loadRTOracle(t)
	for _, c := range o.Source {
		if c.Name != "py-flat-top" {
			continue
		}
		lines := strings.Split(c.Content, "\n")
		if len(lines) != 4 {
			t.Fatalf("oracle 前提失效：py-flat-top 期望 4 行，实得 %d", len(lines))
		}
		got, _ := runRT(t, cwd, c.File)
		if got != c.Content {
			t.Errorf("重复未被保留\n期望: %q\n实得: %q", c.Content, got)
		}
		return
	}
	t.Fatal("oracle 中未找到 py-flat-top 用例")
}

// TestRelatedTestsBackslashStartsWithGone 锁定 `startsWith('src/')` 的分隔符敏感。
//
// oracle `win-backslash-bash`：反斜杠输入 `src\tools\bash.ts` 时，TS 的
// `dirname` 保留反斜杠 → `startsWith('src/')` 为假 → relDir 变成
// `src\tools` → 少一个候选命中。若实现用 filepath.Dir（归一化）或对
// 两种分隔符都判 `src/`，此测试红。
func TestRelatedTestsBackslashStartsWithGone(t *testing.T) {
	o, cwd := loadRTOracle(t)
	for _, c := range o.Source {
		if c.Name != "win-backslash-bash" {
			continue
		}
		got, _ := runRT(t, cwd, c.File)
		if got != c.Content {
			t.Errorf("反斜杠输入行为不一致\n期望: %q\n实得: %q", c.Content, got)
		}
		// 正斜杠输入应有 3 个结果，反斜杠只有 2 个——锁定差异存在
		fwd, _ := runRT(t, cwd, "src/tools/bash.ts")
		if len(strings.Split(fwd, "\n")) <= len(strings.Split(got, "\n")) {
			t.Errorf("前提失效：正斜杠应比反斜杠多一个结果\nfwd=%q\nback=%q", fwd, got)
		}
		return
	}
	t.Fatal("oracle 中未找到 win-backslash-bash 用例")
}

// TestRelatedTestsSchemaShape 锁定工具声明的形状。
func TestRelatedTestsSchemaShape(t *testing.T) {
	def := RelatedTests(".").Definition()
	if def.Name != "related_tests" {
		t.Errorf("name 期望 related_tests，实得 %q", def.Name)
	}
	if def.InputSchema == nil {
		t.Fatal("InputSchema 为 nil")
	}
	if def.InputSchema.Type != "object" {
		t.Errorf("schema type 期望 object，实得 %q", def.InputSchema.Type)
	}
	if _, ok := def.InputSchema.Properties["file"]; !ok {
		t.Errorf("schema 缺 file 属性，实得 %v", def.InputSchema.Properties)
	}
	if len(def.InputSchema.Required) != 1 || def.InputSchema.Required[0] != "file" {
		t.Errorf("required 期望 [file]，实得 %v", def.InputSchema.Required)
	}
	if len(def.InputSchema.PropOrder) != 1 || def.InputSchema.PropOrder[0] != "file" {
		t.Errorf("PropOrder 期望 [file]，实得 %v", def.InputSchema.PropOrder)
	}
	// strProp 必须是有序结构（map 会被序列化器排序键 → 前缀缓存字节不等价）
	prop, ok := def.InputSchema.Properties["file"].(*wire.OrderedMap)
	if !ok {
		t.Fatalf("file 属性应为 *wire.OrderedMap，实得 %T", def.InputSchema.Properties["file"])
	}
	if got := prop.Keys(); len(got) != 2 || got[0] != "type" || got[1] != "description" {
		t.Errorf("file 属性键序期望 [type description]，实得 %v", got)
	}
}

// TestRelatedTestsToolFlags 锁定三个布尔标志。
func TestRelatedTestsToolFlags(t *testing.T) {
	tool := RelatedTests(".")
	if tool.RequiresApproval(nil) {
		t.Error("RequiresApproval 应为 false")
	}
	if !tool.ConcurrencySafe() {
		t.Error("ConcurrencySafe 应为 true")
	}
	if !tool.Enabled() {
		t.Error("Enabled 应为 true")
	}
}
