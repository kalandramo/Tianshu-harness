package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadProjectInstructions — AGENTS.md + .rivet.md 拼接语义。
// 对账 src/prompt/volatile.ts 的 readRivetMd：两份文件用 "\n\n" 拼接，
// 缺一则只用另一份，都缺返回空串。
func TestLoadProjectInstructions(t *testing.T) {
	dir := t.TempDir()

	// 都缺 → 空
	if got := LoadProjectInstructions(dir); got != "" {
		t.Errorf("两份都缺时应返回空串，得到 %q", got)
	}

	// 只有 AGENTS.md
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents 内容"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := LoadProjectInstructions(dir); got != "agents 内容" {
		t.Errorf("仅 AGENTS.md：得到 %q", got)
	}

	// 加 .rivet.md → "\n\n" 拼接
	if err := os.WriteFile(filepath.Join(dir, ".rivet.md"), []byte("rivet 内容"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := LoadProjectInstructions(dir); got != "agents 内容\n\nrivet 内容" {
		t.Errorf("拼接语义错误：得到 %q，期望 %q", got, "agents 内容\n\nrivet 内容")
	}
}

// TestLoadProjectInstructionsOnlyRivetMd — 只有 .rivet.md 的路径。
func TestLoadProjectInstructionsOnlyRivetMd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".rivet.md"), []byte("只有 rivet"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := LoadProjectInstructions(dir); got != "只有 rivet" {
		t.Errorf("仅 .rivet.md：得到 %q", got)
	}
}

// TestLoadProjectInstructionsNoPanic — 目录不存在时不得 panic（fail-soft）。
func TestLoadProjectInstructionsNoPanic(t *testing.T) {
	if got := LoadProjectInstructions("/nonexistent-dir-xyz-12345"); got != "" {
		t.Errorf("目录不存在应返回空串，得到 %q", got)
	}
}

// TestBuildSystemPromptWithProject — 项目指令注入 system prompt 的完整路径。
//
// 这是本模块的**生产消费路径**：BuildSystemPromptWithProject 读 cwd 下的
// AGENTS.md/.rivet.md，按节选取后追加到 static 提示词尾部。
func TestBuildSystemPromptWithProject(t *testing.T) {
	dir := t.TempDir()
	// 构造一个超预算的文档：一个 Gate 节 + 大量 Ref 表格节
	md := "# 项目标题\n\n## 高危命令纪律\n用户让你查看时不要动手，必须等待确认。\n\n"
	for i := 0; i < 30; i++ {
		md += "## 参考章节" + string(rune('A'+i%26)) + "\n| a | b |\n| - | - |\n| 1 | 2 |\n\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}

	got := BuildSystemPromptWithProject(Context{}, dir, 800)

	// 必须包含 static 提示词
	if !hasPrefix(got, BuildSystemPrompt(Context{})) {
		t.Error("结果应以 static 提示词开头")
	}
	// 必须包含 project-instructions 包裹
	if !contains(got, "<project-instructions>") {
		t.Error("结果应含 <project-instructions> 块")
	}
	// 必须保住 Gate 章节（纪律优先）
	if !contains(got, "高危命令纪律") {
		t.Error("预算紧张时应保住 Gate 章节——这是算法的核心意图")
	}
	// 必须含略去标记（让 agent 知道有东西没看到）
	if !contains(got, "已略去") {
		t.Error("应有略去标记，让 agent 知道自己能去读原文")
	}
}

// TestBuildSystemPromptWithProjectEscapes —— 项目文档必须经 escapeXml。
// 文档里的 `<` 若原样进入 XML 块会破坏结构（且 TS 侧是转义的，不转义即分叉）。
func TestBuildSystemPromptWithProjectEscapes(t *testing.T) {
	dir := t.TempDir()
	md := "## 章节\n内容含 <tag> 与 a & b"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	got := BuildSystemPromptWithProject(Context{}, dir, 8000)

	if contains(got, "<tag>") {
		t.Error("项目文档中的 <tag> 应被转义为 &lt;tag&gt;")
	}
	if !contains(got, "&lt;tag&gt;") {
		t.Error("应含转义后的 &lt;tag&gt;")
	}
	if contains(got, "a & b") {
		t.Error("裸 & 应被转义为 &amp;")
	}
	if !contains(got, "a &amp; b") {
		t.Error("应含转义后的 a &amp; b")
	}
}

// TestBuildSystemPromptWithProjectMeasuresEscaped —— 预算必须按**转义后**长度计。
//
// 转义会膨胀（本仓库实测 31%）。按原文长度计费会让块超出 cap 后被上层
// 再切一刀——等于白选。用一个"原文不超预算但转义后超"的文档锁定。
func TestBuildSystemPromptWithProjectMeasuresEscaped(t *testing.T) {
	dir := t.TempDir()
	// 构造：一节纯散文（无特殊字符，转义不膨胀）+ 一节大量尖括号（转义膨胀 3x）
	// 预算设为：按原文算装得下两节，按转义算只装得下一节。
	plain := "## 散文节\n" + rep("平", 100)
	escaped := "## 尖括号节\n" + rep("<>", 60) // 原文 120 字符 → 转义后 60*8=480

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(plain+"\n"+escaped), 0644); err != nil {
		t.Fatal(err)
	}

	// 预算 700：原文两节共 ~250 装得下；转义后 散文节 ~110 + 尖括号节 ~490 = 600 也装得下
	// 改用更紧的预算暴露差异：400
	got := BuildSystemPromptWithProject(Context{}, dir, 400)

	// 按转义计费时，尖括号节（转义后 ~490）装不下 → 应被略去
	if contains(got, "尖括号节") && !contains(got, "已略去") {
		t.Error("按转义后长度计费时，膨胀的尖括号节应被略去（或至少留略去标记）")
	}
}

// TestProjectInstructionsMeasureIsUTF16 —— 计费口径必须是 UTF-16 code unit。
//
// 这是移植中最易漏的分叉：TS 的 `escapeXml(t).length` 数的是 code unit
// （emoji 计 2），Go 的 len([]rune(t)) 数的是码点（emoji 计 1）。
//
// 断言方式：**直接对比两种 measure 在 RenderProjectInstructionsBlock 上的
// 输出差异**——比断言"含已略去"稳健得多（后者依赖 wrap 扣减与 truncateBlock
// 的中间环节，任一变化都会让断言失效；实测确实如此）。
//
// 用 oracle 的 emoji 文档（码点 98 / UTF16 122），在临界预算上两种计费
// 给出不同结果。
func TestProjectInstructionsMeasureIsUTF16(t *testing.T) {
	// 与 oracle docEmoji 同构：4 节，混合 emoji 与 x
	doc := ""
	for i := 0; i < 4; i++ {
		body := ""
		for j := 0; j < 20; j++ {
			if j%2 == 0 {
				body += "😀"
			} else {
				body += "x"
			}
		}
		if i > 0 {
			doc += "\n"
		}
		doc += "## 节" + string(rune('A'+i)) + "\n" + body
	}

	utf16Total := UTF16Len(doc)
	cpTotal := len([]rune(doc))
	if utf16Total == cpTotal {
		t.Fatal("前提失败：文档应含代理对字符，UTF16 长度应大于码点数")
	}

	// 扫描预算区间，找出"两种计费结果不同"的点——存在即证明计费口径生效
	foundDiff := false
	for cap := 60; cap <= cpTotal+60; cap++ {
		got := RenderProjectInstructionsBlock(doc, cap)
		// 用码点计费重算作对照
		sel := SelectProjectInstructions(doc, cap-projectInstructionsWrap, func(t string) int { return len([]rune(EscapeXML(t))) })
		alt := TruncateBlock("<project-instructions>\n"+EscapeXML(sel.Text)+"\n</project-instructions>", cap, "project-instructions")
		if got != alt {
			foundDiff = true
			break
		}
	}
	if !foundDiff {
		t.Error("在整个预算区间内，UTF-16 计费与码点计费结果完全相同——" +
			"说明 measure 未使用 code unit（或该文档无法区分两者）")
	}
}

// TestBuildSystemPromptWithProjectNoFiles — 无项目文件时退化为纯 static 提示词。
func TestBuildSystemPromptWithProjectNoFiles(t *testing.T) {
	dir := t.TempDir()
	got := BuildSystemPromptWithProject(Context{}, dir, 8000)
	want := BuildSystemPrompt(Context{})
	if got != want {
		t.Errorf("无项目文件时应等于 static 提示词\n  got 长度=%d want 长度=%d", len(got), len(want))
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestProjectBlockFullPath —— project-instructions 的**完整组合渲染**对账。
//
// 这条锁定两个易漏点（都是我上轮接线时漏掉的）：
//  1. selectProjectInstructions 的预算是 `cap - wrap`（wrap=47）
//  2. 包裹后还要过一次 truncateBlock(block, cap, 'project-instructions')
//
// 并锁定一个反直觉行为：truncateBlock 的结果**可以超出 cap**（它扣标签开销
// maxChars-tag.length*2-10，而包裹加回来的可能更多）。不能"顺手"让它不超。
func TestProjectBlockFullPath(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "projinst", "oracle.json"))
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v", err)
	}
	var o struct {
		ProjBlock map[string]struct {
			MD   string `json:"md"`
			Cap  int    `json:"cap"`
			Wrap int    `json:"wrap"`
			Out  string `json:"out"`
		} `json:"projBlock"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.ProjBlock) == 0 {
		t.Fatal("oracle 无 projBlock 用例")
	}

	for name, c := range o.ProjBlock {
		t.Run(name, func(t *testing.T) {
			got := RenderProjectInstructionsBlock(c.MD, c.Cap)
			if got != c.Out {
				t.Errorf("不等价（cap=%d wrap=%d）\n  Go 长度=%d\n  TS 长度=%d\n  Go =%q\n  TS =%q",
					c.Cap, c.Wrap, len(got), len(c.Out), trunc2(got, 160), trunc2(c.Out, 160))
			}
		})
	}
}
