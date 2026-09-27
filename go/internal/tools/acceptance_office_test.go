package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// acceptance_office_test.go —— 办公文档家族的用户级验收（第八十三刀 · W1）。
//
// **为什么走生产装配路径**（`NewDefaultRegistry`）：单测直接调构造函数会绕过
// 注册表装配——历史上出现过「工具单测绿、但没注册进 registry」的缺口
// （第七十九刀的教训：接线「状态+门+入口」三者缺一不可）。
// 本文件验证**端到端可见性**：模型能看见、能调用、能落盘。

// TestAccOfficeFamilyViaProductionRegistry —— 五个办公工具走生产装配验收。
func TestAccOfficeFamilyViaProductionRegistry(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(Options{Cwd: ws})

	// ① 五个工具都在生产注册表中（模型可见）
	want := []string{
		"create_document", "create_spreadsheet",
		"create_presentation", "create_pdf", "create_image",
	}
	have := map[string]bool{}
	for _, d := range reg.Definitions() {
		have[d.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("① 生产注册表应含 %s", name)
		}
	}
	t.Logf("① 五个办公工具均在生产注册表中（共 %d 个工具）", len(reg.Definitions()))

	// ② 每个工具都能通过注册表调用并落盘（工作区外目标）
	cases := []struct {
		name   string
		file   string
		input  map[string]any
		expect string // 文件应包含的内容
	}{
		{
			name: "create_document", file: "doc.md",
			input:  map[string]any{"title": "T", "content": "正文"},
			expect: "# T\n\n正文",
		},
		{
			name: "create_spreadsheet", file: "sheet.csv",
			input:  map[string]any{"headers": []any{"a"}, "rows": []any{[]any{"b"}}},
			expect: "a\nb\n",
		},
		{
			name: "create_presentation", file: "deck.ppt",
			input:  map[string]any{"title": "D", "slides": []any{map[string]any{"title": "S1", "content": "C"}}},
			expect: "<section",
		},
		{
			name: "create_pdf", file: "rep.html",
			input:  map[string]any{"content": "<h1>H</h1>"},
			expect: "<h1>H</h1>",
		},
		{
			name: "create_image", file: "logo.svg",
			input:  map[string]any{"svg": `<rect x="1"/>`},
			expect: "<svg ",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := filepath.Join(base, "out", c.file)
			in := map[string]any{"destination_path": dst}
			for k, v := range c.input {
				in[k] = v
			}
			res, err := reg.Execute(nil, c.name, &CallParams{Input: in})
			if err != nil {
				t.Fatalf("② Execute 不应返回 error：%v", err)
			}
			if res.IsError {
				t.Fatalf("② 应成功，实得 %q", res.Content)
			}
			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("② 文件应落盘：%v", err)
			}
			if !strings.Contains(string(got), c.expect) {
				t.Errorf("② 文件应含 %q，实得 %q", c.expect, got)
			}
			t.Logf("② %s → 落盘 %d 字节，内容正确", c.name, len(got))
		})
	}
}

// TestAccOfficeFamilyRequireApprovalInProduction —— 五个工具在生产装配下都需审批。
//
// **为什么单独验**：`RequiresApproval` 是安全边界——若装配层把它丢了，
// 工作区外写盘就变成静默执行。这是「字段存在但零调用者」的同型风险。
func TestAccOfficeFamilyRequireApprovalInProduction(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	for _, name := range []string{
		"create_document", "create_spreadsheet",
		"create_presentation", "create_pdf", "create_image",
	} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s 应在注册表中", name)
		}
		if !tool.RequiresApproval(nil) {
			t.Errorf("%s 应恒需审批（面向工作区外输出）", name)
		}
	}
}
