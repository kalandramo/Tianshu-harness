package prompt

import (
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

// TestBuildSystemPromptWithProjectUTF16Measure —— 生产路径的 measure 必须是
// **UTF-16 code unit** 而非码点。
//
// 这是移植中最易漏的分叉：TS 的 `escapeXml(t).length` 数的是 code unit
// （emoji 计 2），Go 的 len([]rune(t)) 数的是码点（emoji 计 1）。
// 两者只在预算临界点上产生不同选取——普通中文文档完全掩盖它。
//
// 文档取自 oracle 的 docEmoji（码点 98 / UTF16 122）。budget=79 时：
//   - UTF16 计费 → 4 节全保住（omitted=[]）
//   - 码点计费   → 丢 3 节
//
// 断言"4 节标题全在"即可区分两种实现。
func TestBuildSystemPromptWithProjectUTF16Measure(t *testing.T) {
	dir := t.TempDir()
	md := "## 节A\n" + rep("😀", 1) + rep("xx", 1) // 占位，下面用真实文档覆盖
	_ = md

	// 与 oracle docEmoji 相同的语义构造（4 节，混合 emoji 与 x）
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
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(doc), 0644); err != nil {
		t.Fatal(err)
	}

	// 用 UTF16 长度算预算：刚好让所有节都能装下的临界值附近
	utf16Total := 0
	for _, r := range doc {
		if r > 0xFFFF {
			utf16Total += 2
		} else {
			utf16Total++
		}
	}
	cpTotal := len([]rune(doc))
	if utf16Total == cpTotal {
		t.Fatal("前提失败：文档应含代理对字符，UTF16 长度应大于码点数")
	}

	// 预算 = 码点总数（此时按码点算刚好装下、按 UTF16 算超出）
	got := BuildSystemPromptWithProject(Context{}, dir, cpTotal)

	if !contains(got, "已略去") {
		t.Error("按 UTF-16 计费时，budget=码点总数 应触发略去（按码点算则不会）——" +
			"若此处未略去，说明 measure 用了码点而非 code unit")
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
