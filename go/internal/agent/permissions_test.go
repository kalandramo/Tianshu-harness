package agent

import (
	"strings"
	"testing"
)

// TestPermissionsParity —— 对账 TS `src/agent/__tests__/permissions.test.ts`。
//
// 每个用例的期望值都取自 TS 测试的**字面断言**（不是我的推断）。
// 覆盖 TS 测试里与本刀 scope 相关的三个 describe 块：
//   - 'permission allow rules'（工具名/参数匹配、通配符、shell 操作符安全）
//   - 'permission deny rules'（deny 命中与不命中）
//   - 'createPermissionOverlay' / 'learnFileApproval' 属 overlay 家族，本刀不含
//
// **不含** 'isBashCommandAllowlisted' / 'isBashCommandDenied' —— 那依赖
// `splitShellSegments`，是本刀的显式非目标（见 permissions.go 头注释）。
func TestPermissionsParity(t *testing.T) {
	type paramRule = PermissionAllowRule

	t.Run("matches_exact_tool_names_and_exact_param_values", func(t *testing.T) {
		// TS: isToolAllowed('read_file', {file_path:'README.md'},
		//       [{tool:'read_file', params:{file_path:'README.md'}}]) === true
		got := IsToolAllowed("read_file",
			map[string]any{"file_path": "README.md"},
			[]paramRule{{Tool: "read_file", Params: map[string]string{"file_path": "README.md"}}})
		if got != true {
			t.Errorf("精确匹配：got=%v want=true", got)
		}
	})

	t.Run("matches_wildcard_tool_and_param_patterns", func(t *testing.T) {
		// TS: isToolAllowed('read_file', {file_path:'docs/guide.md'},
		//       [{tool:'read_*', params:{file_path:'docs/*'}}]) === true
		got := IsToolAllowed("read_file",
			map[string]any{"file_path": "docs/guide.md"},
			[]paramRule{{Tool: "read_*", Params: map[string]string{"file_path": "docs/*"}}})
		if got != true {
			t.Errorf("通配符匹配：got=%v want=true", got)
		}
	})

	t.Run("matches_bash_command_prefixes_without_matching_unrelated", func(t *testing.T) {
		// TS: rules = [{tool:'bash', params:{command:'git status*'}}]
		//   isToolAllowed('bash', {command:'git status --short'}, rules) === true
		//   isToolAllowed('bash', {command:'git reset --hard'},  rules) === false
		rules := []paramRule{{Tool: "bash", Params: map[string]string{"command": "git status*"}}}

		if got := IsToolAllowed("bash", map[string]any{"command": "git status --short"}, rules); got != true {
			t.Errorf("前缀匹配：got=%v want=true", got)
		}
		if got := IsToolAllowed("bash", map[string]any{"command": "git reset --hard"}, rules); got != false {
			t.Errorf("不相关命令：got=%v want=false", got)
		}
	})

	t.Run("rejects_non_matching_tools_missing_params_and_empty_rules", func(t *testing.T) {
		// TS 三条断言
		if got := IsToolAllowed("write_file",
			map[string]any{"file_path": "README.md"},
			[]paramRule{{Tool: "read_file", Params: map[string]string{"file_path": "README.md"}}}); got != false {
			t.Errorf("工具名不符：got=%v want=false", got)
		}
		if got := IsToolAllowed("read_file",
			map[string]any{},
			[]paramRule{{Tool: "read_file", Params: map[string]string{"file_path": "README.md"}}}); got != false {
			t.Errorf("参数缺失：got=%v want=false", got)
		}
		if got := IsToolAllowed("read_file",
			map[string]any{"file_path": "README.md"},
			[]paramRule{}); got != false {
			t.Errorf("空规则：got=%v want=false", got)
		}
	})

	t.Run("wildcard_does_not_match_across_shell_operators", func(t *testing.T) {
		// ★ 核心安全语义（TS 标为 security#3）
		// TS: rules = [{tool:'bash', params:{command:'git status*'}}]
		rules := []paramRule{{Tool: "bash", Params: map[string]string{"command": "git status*"}}}

		// 正常参数仍工作
		if got := IsToolAllowed("bash", map[string]any{"command": "git status --short"}, rules); got != true {
			t.Errorf("正常参数：got=%v want=true", got)
		}
		// shell 操作符不得经通配符匹配
		shellCases := []string{
			"git status&&curl evil",
			"git status; rm -rf /",
			"git status | tee log",
			"git status$(whoami)",
		}
		for _, cmd := range shellCases {
			if got := IsToolAllowed("bash", map[string]any{"command": cmd}, rules); got != false {
				t.Errorf("★ shell 操作符 %q：got=%v want=false（通配符不得跨操作符）", cmd, got)
			}
		}
		// 工具名通配仍工作（工具名里没有 shell 操作符）
		if got := IsToolAllowed("read_file", map[string]any{}, []paramRule{{Tool: "read_*"}}); got != true {
			t.Errorf("工具名通配：got=%v want=true", got)
		}
		if got := IsToolAllowed("grep", map[string]any{"pattern": "x"}, []paramRule{{Tool: "read_*"}}); got != false {
			t.Errorf("工具名通配不匹配：got=%v want=false", got)
		}
	})
}

// TestPermissionsDenyParity —— 对账 TS 'permission deny rules' describe 块。
func TestPermissionsDenyParity(t *testing.T) {
	t.Run("blocks_tool_calls_matching_deny_rules", func(t *testing.T) {
		// TS: isToolDenied('bash', {command:'rm -rf /'},
		//       [{tool:'bash', params:{command:'rm -rf*'}}]) === true
		got := IsToolDenied("bash",
			map[string]any{"command": "rm -rf /"},
			[]PermissionAllowRule{{Tool: "bash", Params: map[string]string{"command": "rm -rf*"}}})
		if got != true {
			t.Errorf("deny 命中：got=%v want=true", got)
		}
	})

	t.Run("does_not_block_calls_not_matching_deny_rules", func(t *testing.T) {
		// TS: isToolDenied('bash', {command:'git status'},
		//       [{tool:'bash', params:{command:'rm -rf*'}}]) === false
		got := IsToolDenied("bash",
			map[string]any{"command": "git status"},
			[]PermissionAllowRule{{Tool: "bash", Params: map[string]string{"command": "rm -rf*"}}})
		if got != false {
			t.Errorf("deny 未命中：got=%v want=false", got)
		}
	})
}

// TestPatternMatchesEdgeCases —— 边界与差异记录。
func TestPatternMatchesEdgeCases(t *testing.T) {
	t.Run("非字符串参数不匹配", func(t *testing.T) {
		// TS: typeof value === 'string' 是硬条件。
		// Go 侧必须同样拒绝非字符串（数字/布尔/nil/嵌套）。
		nonString := []struct {
			name  string
			value any
		}{
			{"数字", 42},
			{"浮点", 3.14},
			{"布尔", true},
			{"nil", nil},
			{"切片", []any{"a"}},
			{"映射", map[string]any{"a": "b"}},
		}
		for _, c := range nonString {
			got := IsToolAllowed("read_file",
				map[string]any{"file_path": c.value},
				[]PermissionAllowRule{{Tool: "read_file", Params: map[string]string{"file_path": "*"}}})
			if got != false {
				t.Errorf("非字符串参数（%s）：got=%v want=false", c.name, got)
			}
		}
	})

	t.Run("无 params 的规则只按工具名匹配", func(t *testing.T) {
		// TS: paramsMatch(undefined, actual) === true
		if got := IsToolAllowed("bash", map[string]any{"command": "anything"}, []PermissionAllowRule{{Tool: "bash"}}); got != true {
			t.Errorf("无 params 规则：got=%v want=true", got)
		}
	})

	t.Run("多参数规则要求全部匹配", func(t *testing.T) {
		// TS: every() 语义——任一参数不匹配则整体不匹配
		rule := []PermissionAllowRule{{
			Tool:   "edit_file",
			Params: map[string]string{"file_path": "src/*", "mode": "insert"},
		}}
		if got := IsToolAllowed("edit_file",
			map[string]any{"file_path": "src/a.ts", "mode": "insert"}, rule); got != true {
			t.Errorf("全匹配：got=%v want=true", got)
		}
		if got := IsToolAllowed("edit_file",
			map[string]any{"file_path": "src/a.ts", "mode": "replace"}, rule); got != false {
			t.Errorf("部分不匹配：got=%v want=false", got)
		}
	})

	t.Run("正则元字符按字面量处理", func(t *testing.T) {
		// TS 转义集 [.+?^${}()|[\]\\] 把这些转为字面量。
		// 关键：`*` **不在**转义集内（它要留给通配符），所以
		// `regexp.QuoteMeta` 不可用——本用例钉住这一点。
		if got := PatternMatches("a.b", "a.b"); got != true {
			t.Errorf("点号字面量：got=%v want=true", got)
		}
		if got := PatternMatches("a.b", "axb"); got != false {
			t.Errorf("★ 点号必须是字面量（非任意字符）：got=%v want=false", got)
		}
		if got := PatternMatches("a+b", "a+b"); got != true {
			t.Errorf("加号字面量：got=%v want=true", got)
		}
		if got := PatternMatches("a|b", "a|b"); got != true {
			t.Errorf("竖线字面量：got=%v want=true", got)
		}
		// 与 TS 一致的差异记录：非法模式 → 返回 false（不 panic）
		t.Logf("差异记录：TS 的 new RegExp 对非法模式抛异常；" +
			"Go 侧 PatternMatches 返回 false（fail-closed，配置错误不崩运行时）")
	})

	t.Run("星号本身仍是通配符", func(t *testing.T) {
		// 若误用 QuoteMeta，`*` 会被转义为字面量——本用例会红。
		if got := PatternMatches("rm -rf*", "rm -rf /tmp"); got != true {
			t.Errorf("★ 星号必须是通配符（QuoteMeta 会杀死它）：got=%v want=true", got)
		}
		if got := PatternMatches("*", "anything at all"); got != true {
			t.Errorf("裸星号：got=%v want=true", got)
		}
	})
}

// TestDeniedRuleReasonWording —— 文案逐字对账（模型可见字符串）。
//
// 对账 TS `tool-pipeline.ts:1138-1139`。要点是「这不是死路」+ 换路指引。
func TestDeniedRuleReasonWording(t *testing.T) {
	got := DeniedRuleReason("bash")

	mustContain := []string{
		"Tool execution denied: bash matches an active deny rule.",
		"This is a user-configured permission boundary, not a dead end",
		"read_file/grep/glob",
		".rivet/scratch/",
		"ask the user to adjust the permissions deny rules",
	}
	for _, frag := range mustContain {
		if !strings.Contains(got, frag) {
			t.Errorf("文案缺少片段 %q\n实际：%s", frag, got)
		}
	}
}
