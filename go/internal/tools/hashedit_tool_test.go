package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kalandramo/tianshu/go/internal/prompt"
)

// setupHashEdit 在临时目录建一个目标文件并返回工具与路径。
func setupHashEdit(t *testing.T, content string) (Tool, string, string) {
	t.Helper()
	dir := t.TempDir()
	fp := filepath.Join(dir, "target.txt")
	if content != "" {
		if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
			t.Fatalf("建文件失败：%v", err)
		}
	}
	return HashEdit(dir, nil), fp, dir
}

func execHash(t *testing.T, tool Tool, input map[string]any) (string, bool) {
	t.Helper()
	res, err := tool.Execute(context.Background(), &CallParams{Input: input})
	if err != nil {
		t.Fatalf("Execute 返回 error：%v", err)
	}
	return res.Content, res.IsError
}

// TestHashEditDefinition —— 工具定义与 schema。
func TestHashEditDefinition(t *testing.T) {
	tool, _, _ := setupHashEdit(t, "a\n")
	def := tool.Definition()
	if def.Name != "hash_edit" {
		t.Errorf("工具名应为 hash_edit，得到 %q", def.Name)
	}
	if !tool.Enabled() {
		t.Error("应默认启用")
	}
	if tool.ConcurrencySafe() {
		t.Error("hash_edit 不应标记为并发安全")
	}
	for _, k := range []string{"file_path", "anchors", "new_string", "dry_run"} {
		if _, ok := def.InputSchema.Properties[k]; !ok {
			t.Errorf("schema 缺 %s 字段", k)
		}
	}
	// anchors 必须是数组型（回归：数组型 schema 曾导致序列化 panic）
	anchorsProp, _ := def.InputSchema.Properties["anchors"].(map[string]any)
	if anchorsProp["type"] != "array" {
		t.Errorf("anchors 应为 array 型，得到 %v", anchorsProp["type"])
	}
	if _, ok := anchorsProp["items"]; !ok {
		t.Error("anchors 应有 items 定义")
	}
}

// TestHashEditSingleLineReplace —— 单锚点替换（对账 oracle 成功路径）。
func TestHashEditSingleLineReplace(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "line1\nline2\nline3\n")
	h := prompt.HashLine("line2")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + h}, "new_string": "LINE-TWO",
	})
	if isErr {
		t.Fatalf("不应报错：%s", content)
	}
	// 消息格式：绝对路径 + 行范围 + 新鲜锚点
	if !strings.Contains(content, "hash_edit 已应用到 "+fp) {
		t.Errorf("成功消息应用绝对路径：%q", content)
	}
	if !strings.Contains(content, "将 L2-L2（1 行）替换为 1 行") {
		t.Errorf("成功消息应含行范围：%q", content)
	}
	if !strings.Contains(content, "新鲜锚点（链式安全）：") {
		t.Errorf("成功消息应含新鲜锚点：%q", content)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "line1\nLINE-TWO\nline3\n" {
		t.Errorf("文件内容不符：%q", string(got))
	}
}

// TestHashEditRangeReplace —— 首尾锚点定义区间（含两端）。
func TestHashEditRangeReplace(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\nd\ne\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path":  fp,
		"anchors":    []any{"L2:" + prompt.HashLine("b"), "L4:" + prompt.HashLine("d")},
		"new_string": "X\nY",
	})
	if isErr {
		t.Fatalf("不应报错：%s", content)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nX\nY\ne\n" {
		t.Errorf("区间替换结果不符：%q", string(got))
	}
}

// TestHashEditDelete —— 空 new_string 删除区间。
func TestHashEditDelete(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\nd\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path":  fp,
		"anchors":    []any{"L2:" + prompt.HashLine("b"), "L3:" + prompt.HashLine("c")},
		"new_string": "",
	})
	if isErr {
		t.Fatalf("不应报错：%s", content)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nd\n" {
		t.Errorf("删除结果不符：%q", string(got))
	}
	if !strings.Contains(content, "替换为 0 行") {
		t.Errorf("应报替换为 0 行：%q", content)
	}
}

// TestHashEditInsertAfter —— 单锚点插入（原内容 + 新增）。
func TestHashEditInsertAfter(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")
	_, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + prompt.HashLine("b")},
		"new_string": "b\nNEW",
	})
	if isErr {
		t.Fatal("不应报错")
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nb\nNEW\nc\n" {
		t.Errorf("插入结果不符：%q", string(got))
	}
}

// TestHashEditThreeAnchors —— 三锚点（中间锚点校验区间内部）。
func TestHashEditThreeAnchors(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\nd\ne\n")
	_, isErr := execHash(t, tool, map[string]any{
		"file_path": fp,
		"anchors": []any{
			"L2:" + prompt.HashLine("b"),
			"L3:" + prompt.HashLine("c"),
			"L4:" + prompt.HashLine("d"),
		},
		"new_string": "XYZ",
	})
	if isErr {
		t.Fatal("不应报错")
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nXYZ\ne\n" {
		t.Errorf("三锚点结果不符：%q", string(got))
	}
}

// TestHashEditMiddleAnchorMismatch —— 中间锚点失配必须拒绝（不静默改文件）。
func TestHashEditMiddleAnchorMismatch(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\nd\ne\n")
	before, _ := os.ReadFile(fp)
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp,
		"anchors": []any{
			"L2:" + prompt.HashLine("b"),
			"L3:deadbeef", // 故意错
			"L4:" + prompt.HashLine("d"),
		},
		"new_string": "XYZ",
	})
	if !isErr {
		t.Error("中间锚点失配应报错")
	}
	if !strings.Contains(content, "锚点已过期") {
		t.Errorf("应给过期诊断：%q", content)
	}
	after, _ := os.ReadFile(fp)
	if string(before) != string(after) {
		t.Error("失配时文件不应被修改")
	}
}

// TestHashEditPositionOnly —— 仅位置锚点（无哈希）能编辑。
func TestHashEditPositionOnly(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")
	_, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2"}, "new_string": "B",
	})
	if isErr {
		t.Fatal("不应报错")
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nB\nc\n" {
		t.Errorf("仅位置锚点结果不符：%q", string(got))
	}
}

// TestHashEditAnchorWithSuffix —— 锚点容忍行尾内容后缀。
func TestHashEditAnchorWithSuffix(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")
	_, isErr := execHash(t, tool, map[string]any{
		"file_path":  fp,
		"anchors":    []any{"L2:" + prompt.HashLine("b") + " → b"},
		"new_string": "B",
	})
	if isErr {
		t.Fatal("带内容后缀的锚点应被接受")
	}
}

// TestHashEditDryRun —— dry_run 不写盘。
func TestHashEditDryRun(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + prompt.HashLine("b")},
		"new_string": "B2", "dry_run": true,
	})
	if isErr {
		t.Fatal("dry_run 不应报错")
	}
	if !strings.Contains(content, "预览（dry_run）") {
		t.Errorf("应给预览标记：%q", content)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nb\nc\n" {
		t.Errorf("dry_run 不应改文件：%q", string(got))
	}
}

// TestHashEditErrorPaths —— 各类错误路径。
func TestHashEditErrorPaths(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")

	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"非法锚点格式", map[string]any{"file_path": fp, "anchors": []any{"XYZ"}, "new_string": "x"},
			`无效锚点格式 "XYZ"`},
		{"L0 非法", map[string]any{"file_path": fp, "anchors": []any{"L0:aaaaaaaa"}, "new_string": "x"},
			"无效锚点格式"},
		{"超 3 个锚点", map[string]any{"file_path": fp, "anchors": []any{"L1:a", "L2:b", "L3:c", "L4:d"}, "new_string": "x"},
			"1-3 个"},
		{"空锚点", map[string]any{"file_path": fp, "anchors": []any{}, "new_string": "x"},
			"1-3 个"},
		{"非升序", map[string]any{"file_path": fp,
			"anchors": []any{"L3:" + prompt.HashLine("c"), "L1:" + prompt.HashLine("a")}, "new_string": "x"},
			"严格按行号升序"},
		{"行号重复", map[string]any{"file_path": fp,
			"anchors": []any{"L2:" + prompt.HashLine("b"), "L2:" + prompt.HashLine("b")}, "new_string": "x"},
			"严格按行号升序"},
		{"哈希过期", map[string]any{"file_path": fp, "anchors": []any{"L2:deadbeef"}, "new_string": "x"},
			"锚点已过期"},
		{"eof 越界", map[string]any{"file_path": fp, "anchors": []any{"L99:aaaaaaaa"}, "new_string": "x"},
			"锚点已过期"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, isErr := execHash(t, tool, c.input)
			if !isErr {
				t.Errorf("应报错，得到 %q", content)
			}
			if !strings.Contains(content, c.want) {
				t.Errorf("错误消息应含 %q，实际 %q", c.want, content)
			}
		})
	}

	// 文件不存在
	missing, isErr := execHash(t, tool, map[string]any{
		"file_path": filepath.Join(filepath.Dir(fp), "nope.txt"),
		"anchors":   []any{"L1:aaaaaaaa"}, "new_string": "x",
	})
	if !isErr || !strings.Contains(missing, "文件未找到") {
		t.Errorf("文件不存在应报错：%q", missing)
	}
}

// TestHashEditStaleRecovery —— 行号漂移时自动恢复并编辑。
func TestHashEditStaleRecovery(t *testing.T) {
	// 文件被前面插入了两行；模型仍拿旧锚点 L2 指向 b（现在在 L4）
	tool, fp, _ := setupHashEdit(t, "new1\nnew2\na\nb\nc\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + prompt.HashLine("b")},
		"new_string": "B",
	})
	if isErr {
		t.Fatalf("应能自动恢复：%s", content)
	}
	if !strings.Contains(content, "已自动恢复") {
		t.Errorf("应报已恢复：%q", content)
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "new1\nnew2\na\nB\nc\n" {
		t.Errorf("恢复后编辑结果不符：%q", string(got))
	}
}

// TestHashEditStaleDiagnosticNoRetryOnEOF —— eof 越界诊断不给重试锚点。
func TestHashEditStaleDiagnosticNoRetryOnEOF(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L99:aaaaaaaa"}, "new_string": "x",
	})
	if !isErr {
		t.Fatal("应报错")
	}
	if strings.Contains(content, "请立即用以下锚点重试") {
		t.Errorf("eof 越界不应给重试锚点：%q", content)
	}
}

// TestHashEditCRLFPreserved —— CRLF 文件的 EOL 被保留。
func TestHashEditCRLFPreserved(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\r\nb\r\nc\r\n")
	// hashLine 剥离尾部 \r，故锚点哈希基于 "b"
	_, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + prompt.HashLine("b")},
		"new_string": "B",
	})
	if isErr {
		t.Fatal("CRLF 文件应可编辑")
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\r\nB\r\nc\r\n" {
		t.Errorf("应保留 CRLF：%q", string(got))
	}
}

// TestHashEditFreshAnchorsChainable —— 回传的新鲜锚点可直接用于下一次编辑。
func TestHashEditFreshAnchorsChainable(t *testing.T) {
	tool, fp, _ := setupHashEdit(t, "a\nb\nc\n")
	content, isErr := execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{"L2:" + prompt.HashLine("b")},
		"new_string": "B1\nB2",
	})
	if isErr {
		t.Fatalf("首次编辑失败：%s", content)
	}
	// 从回传中抠出 L3 的锚点（新块末行）
	var anchor string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "L3:") {
			anchor = strings.SplitN(line, " ", 2)[0]
			break
		}
	}
	if anchor == "" {
		t.Fatalf("回传中未找到 L3 锚点：%q", content)
	}
	// 用它做链式编辑——应成功
	_, isErr = execHash(t, tool, map[string]any{
		"file_path": fp, "anchors": []any{anchor}, "new_string": "CHAINED",
	})
	if isErr {
		t.Error("回传的新鲜锚点应可直接链式使用")
	}
	got, _ := os.ReadFile(fp)
	if string(got) != "a\nB1\nCHAINED\nc\n" {
		t.Errorf("链式编辑结果不符：%q", string(got))
	}
}
