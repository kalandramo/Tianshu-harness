package tools

import (
	"strings"
	"testing"
)

// bash_run_in_background_decl_test.go —— `run_in_background` 的**显式未实现声明**。
//
// # 背景
//
// 第二十五刀的用户级验收（实跑探针，2/2 met）证实：传 `run_in_background=true`
// 与不传**行为完全一致**——参数被静默忽略（无 job id、无后台标记、前台同步完成）。
//
// TS 侧该参数是好的（`bash.ts:468` `params.input.run_in_background`），但依赖
// `sessionJobRegistry`（TS 注释明说「Requires a session job registry (server / TUI
// with sessionId); otherwise falls through to normal foreground execution」），
// Go 侧**无该设施**（`JobRegistry`/`jobRegistry`/`JobStore` 全库零命中）。
//
// # 本测试钉住的契约
//
// 既然能力未实现，**描述必须诚实**——不能让模型看到「转入后台并返回 job id」
// 的承诺而实际走前台。把「静默」变「显式」。
//
// # 与 schema_parity_test.go 的关系（**关键**）
//
// `TestToolSchemaByteParity` 逐字节对账 TS oracle（含描述文本）。本刀**有意偏离**
// ——故该测试会红，且**红是对的**（它检测到了真实偏离）。处理方式：**oracle 保持
// TS 原样不动**（它是 TS 的忠实快照），在 `schema_parity_test.go` 里加显式的
// **已知偏离白名单**（含移除条件），而非偷偷改 oracle 掩盖。
func TestBashRunInBackgroundDescriptionHonest(t *testing.T) {
	tool := Bash(t.TempDir())
	def := tool.Definition()
	if def.InputSchema == nil {
		t.Fatal("bash 应有 InputSchema")
	}
	raw, ok := def.InputSchema.Properties["run_in_background"]
	if !ok {
		t.Fatal("run_in_background 应在 schema 里（保留接口语义，不删）")
	}
	desc := propDescription(t, raw)

	// 1) 必须显式说明 Go 侧未实现。
	if !strings.Contains(desc, "未实现") && !strings.Contains(desc, "暂未") {
		t.Errorf("描述应显式说明未实现，实得：%q", desc)
	}
	// 2) 必须说明实际行为（走前台）。
	if !strings.Contains(desc, "前台") {
		t.Errorf("描述应说明实际走前台，实得：%q", desc)
	}
	// 3) **不得**再承诺 job id（那是未实现的能力）。
	//
	// 注意断言写法：新描述里含「**不**返回 job id」——朴素的
	// `Contains(desc, "返回 job id")` 会被这个**否定式**误判（子串命中）。
	// 故改为：若出现「返回 job id」，它**必须**处于否定语境。
	if i := strings.Index(desc, "返回 job id"); i >= 0 {
		// 检查紧邻前缀是否含否定词。
		lo := i - 6
		if lo < 0 {
			lo = 0
		}
		prefix := desc[lo:i]
		if !strings.Contains(prefix, "不") && !strings.Contains(prefix, "未") {
			t.Errorf("描述不应**承诺**返回 job id（未实现），实得：%q", desc)
		}
	}
}

// propDescription 从属性定义里取 description（容忍 wire.OrderedMap 或 map）。
func propDescription(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case interface{ Get(string) (any, bool) }:
		if d, ok := x.Get("description"); ok {
			s, _ := d.(string)
			return s
		}
	case map[string]any:
		s, _ := x["description"].(string)
		return s
	}
	// wire.OrderedMap 走 Marshal 兜底
	if m, ok := v.(interface{ Marshal() string }); ok {
		raw := m.Marshal()
		if i := strings.Index(raw, `"description":"`); i >= 0 {
			rest := raw[i+len(`"description":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				return rest[:j]
			}
		}
	}
	t.Fatalf("无法从属性定义取 description：%T", v)
	return ""
}
