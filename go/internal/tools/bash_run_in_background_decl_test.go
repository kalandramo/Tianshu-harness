package tools

import (
	"strings"
	"testing"
)

// bash_run_in_background_decl_test.go —— `run_in_background` 的**契约**（第八十刀反转）。
//
// # 历史（第二十五刀 → 第八十刀）
//
// 第二十五刀时：Go 侧无 job 子系统（`JobRegistry`/`JobStore` 全库零命中），
// 该参数**被静默忽略**。当时的处理是「把静默变显式」——改描述为诚实声明
// 「Go 侧暂未实现：传 true 仍走前台同步执行」，并在 `schema_parity_test.go`
// 登记一条**含移除条件**的已知偏离（「job 子系统移植后恢复 TS 原文案并从本表删除」）。
//
// **第八十刀：移除条件已满足**——`jobstore.go` + `job.go` 已移植并接线，
// 描述恢复 TS 原文案（`设为 true 转入后台并返回 job id。自动检测已知长跑命令。`），
// 白名单条目已删。故**本测试反转**：从「断言未实现」改为「断言已实现」。
//
// # 为什么反转而非删除
//
// 断言「承诺返回 job id」现在是**真契约**——测试仍有价值：它钉住
// ①描述不被回退成「未实现」声明；②描述里出现 job id 承诺时它处于**肯定**语境
// （第二十五刀那版是**否定**语境，两版必须区分，否则回退会静默通过）。
func TestBashRunInBackgroundDescriptionHonest(t *testing.T) {
	tool := Bash(t.TempDir())
	def := tool.Definition()
	if def.InputSchema == nil {
		t.Fatal("bash 应有 InputSchema")
	}
	raw, ok := def.InputSchema.Properties["run_in_background"]
	if !ok {
		t.Fatal("run_in_background 应在 schema 里")
	}
	desc := propDescription(t, raw)

	// 1) **不得**再出现「未实现/暂未」声明——机制已落地。
	if strings.Contains(desc, "未实现") || strings.Contains(desc, "暂未") {
		t.Errorf("job 子系统已移植，描述不应再有「未实现」声明，实得：%q", desc)
	}
	// 2) 必须**承诺**返回 job id（且是肯定语境）。
	i := strings.Index(desc, "返回 job id")
	if i < 0 {
		t.Fatalf("描述应承诺返回 job id（机制已实现），实得：%q", desc)
	}
	// 否定语境（如「不会返回 job id」）不算承诺——那正是第二十五刀的旧版。
	lo := i - 6
	if lo < 0 {
		lo = 0
	}
	prefix := desc[lo:i]
	if strings.Contains(prefix, "不") || strings.Contains(prefix, "未") {
		t.Errorf("描述里的 job id 承诺处于否定语境（旧版回退？），实得：%q", desc)
	}
	// 3) 必须说明自动检测（第八十刀移植的 isLongRunner）。
	if !strings.Contains(desc, "自动检测") {
		t.Errorf("描述应说明自动检测长跑命令，实得：%q", desc)
	}
	// 4) 与 TS 原文案逐字一致（进前缀，字节稳定是硬约束）。
	want := "设为 true 转入后台并返回 job id。自动检测已知长跑命令。"
	if desc != want {
		t.Errorf("描述应逐字对账 TS：\n实得 %q\n期望 %q", desc, want)
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
