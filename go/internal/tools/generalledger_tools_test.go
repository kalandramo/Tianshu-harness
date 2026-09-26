package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generalledger_tools_test.go —— 两个将星账本工具的端到端行为（第七十七刀）。
//
// 存储层的纯函数由 `internal/context/generalledger_oracle_test.go` 对账 TS；
// 本文件验**工具层**：参数校验、错误文案、注册、与存储层的接线。

func glCall(t *testing.T, tool Tool, cwd string, input map[string]any) (string, bool) {
	t.Helper()
	res, err := tool.Execute(context.Background(), &CallParams{Input: input, Cwd: cwd})
	if err != nil {
		t.Fatalf("Execute 不该返回 error（用 Result.IsError 表达）：%v", err)
	}
	return res.Content, res.IsError
}

// TestRecordGeneralFindingToolWritesLedger —— 首次写 → 复发写（计数递增）。
func TestRecordGeneralFindingToolWritesLedger(t *testing.T) {
	dir := t.TempDir()
	tool := RecordGeneralFinding(dir)

	content, isErr := glCall(t, tool, dir, map[string]any{
		"star": "天权", "family": "always-true-on-missing-field", "note": "首次发现",
	})
	if isErr {
		t.Fatalf("首次写应成功，实得错误：%s", content)
	}
	if !strings.Contains(content, "recurrenceCount: 1") {
		t.Errorf("首次写文案应含 recurrenceCount: 1，实得 %q", content)
	}

	content, isErr = glCall(t, tool, dir, map[string]any{
		"star": "天权", "family": "always-true-on-missing-field", "note": "复发",
	})
	if isErr {
		t.Fatalf("复发写应成功，实得错误：%s", content)
	}
	if !strings.Contains(content, "recurrenceCount: 2") {
		t.Errorf("复发写文案应含 recurrenceCount: 2，实得 %q", content)
	}

	// 落盘验证
	p := filepath.Join(dir, ".rivet", "generals", "tianquan.md")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("账本应已创建：%v", err)
	}
	if !strings.Contains(string(data), "always-true-on-missing-field") {
		t.Errorf("账本应含族名：\n%s", string(data))
	}
}

// TestRecordGeneralFindingValidatesFamilySlug —— **反面对照**：非 kebab-case 被拒。
//
// TS 描述原文：「同族复发必须复用既有 slug」——自由文本会让同一缺陷裂成多族。
func TestRecordGeneralFindingValidatesFamilySlug(t *testing.T) {
	dir := t.TempDir()
	tool := RecordGeneralFinding(dir)

	bad := []string{
		"含空格的族名",           // 空格
		"UpperCase-Family", // 大写
		"-leading-dash",    // 首字符是连字符
		"",                 // 空
	}
	for _, fam := range bad {
		content, isErr := glCall(t, tool, dir, map[string]any{
			"star": "天权", "family": fam, "note": "x",
		})
		if !isErr {
			t.Errorf("family=%q 应被拒（非 kebab-case），实得通过：%s", fam, content)
		}
	}
	// 不应创建任何文件（全部被拒）
	if _, err := os.Stat(filepath.Join(dir, ".rivet")); err == nil {
		t.Error("被拒的调用不该创建账本目录")
	}
}

// TestRecordGeneralFindingRequiresAllFields —— 三字段缺任一即拒。
func TestRecordGeneralFindingRequiresAllFields(t *testing.T) {
	dir := t.TempDir()
	tool := RecordGeneralFinding(dir)
	cases := []map[string]any{
		{"family": "a", "note": "x"},                // 缺 star
		{"star": "天权", "note": "x"},                 // 缺 family
		{"star": "天权", "family": "a"},               // 缺 note
		{"star": "  ", "family": "a", "note": "x"},  // star 全空白
		{"star": "天权", "family": "a", "note": "  "}, // note 全空白
	}
	for i, in := range cases {
		if _, isErr := glCall(t, tool, dir, in); !isErr {
			t.Errorf("用例 %d 应被拒（字段缺失/空白）：%+v", i, in)
		}
	}
}

// TestRecordGeneralFindingUnknownStar —— 未知星域被拒。
func TestRecordGeneralFindingUnknownStar(t *testing.T) {
	dir := t.TempDir()
	tool := RecordGeneralFinding(dir)
	content, isErr := glCall(t, tool, dir, map[string]any{
		"star": "不存在的星域", "family": "a-b", "note": "x",
	})
	if !isErr {
		t.Error("未知星域应被拒")
	}
	if !strings.Contains(content, "未知星域") {
		t.Errorf("错误文案应指明未知星域，实得 %q", content)
	}
}

// TestRecallGeneralReadsLedger —— 写入后能召回（闭环）。
func TestRecallGeneralReadsLedger(t *testing.T) {
	dir := t.TempDir()
	rec := RecallGeneral(dir)

	// 尚无账本 → 报错并提示创建
	content, isErr := glCall(t, rec, dir, map[string]any{"star": "天权"})
	if !isErr {
		t.Error("无账本时应报错")
	}
	if !strings.Contains(content, "尚无账本") {
		t.Errorf("应提示尚无账本，实得 %q", content)
	}

	// 写一条
	glCall(t, RecordGeneralFinding(dir), dir, map[string]any{
		"star": "天权", "family": "test-family", "note": "召回验证",
	})

	// 再召回 → 返回全文
	content, isErr = glCall(t, rec, dir, map[string]any{"star": "天权"})
	if isErr {
		t.Fatalf("有账本时应成功，实得错误：%s", content)
	}
	if !strings.Contains(content, "test-family") || !strings.Contains(content, "召回验证") {
		t.Errorf("召回内容应含族名与实例行：\n%s", content)
	}
}

// TestRecallGeneralUnknownStar —— 未知星域被拒（并列出磁盘上已有的账本）。
func TestRecallGeneralUnknownStar(t *testing.T) {
	dir := t.TempDir()
	content, isErr := glCall(t, RecallGeneral(dir), dir, map[string]any{"star": "不存在"})
	if !isErr {
		t.Error("未知星域应被拒")
	}
	if !strings.Contains(content, "未知星域") {
		t.Errorf("文案应指明未知星域，实得 %q", content)
	}
	// 空目录应显示「（无）」
	if !strings.Contains(content, "（无）") {
		t.Errorf("空目录应显示「（无）」，实得 %q", content)
	}
}

// TestRecallGeneralEmptyStar —— star 为空被拒。
func TestRecallGeneralEmptyStar(t *testing.T) {
	dir := t.TempDir()
	if _, isErr := glCall(t, RecallGeneral(dir), dir, map[string]any{"star": "  "}); !isErr {
		t.Error("空 star 应被拒")
	}
}

// TestGeneralLedgerToolsRegistered —— **接线断言**：两个工具都在默认注册表里。
//
// 本项目栽过多次「实现正确但零注册」——本测试钉住注册。
func TestGeneralLedgerToolsRegistered(t *testing.T) {
	reg := NewDefaultRegistry(Options{Cwd: t.TempDir()})
	for _, name := range []string{"recall_general", "record_general_finding"} {
		if !reg.Has(name) {
			t.Errorf("默认注册表应含 %q", name)
		}
	}
	// 反面对照：不存在的名字不该被误报为存在
	if reg.Has("nonexistent_tool_xyz") {
		t.Error("Has 对不存在的工具应返回 false")
	}
}
