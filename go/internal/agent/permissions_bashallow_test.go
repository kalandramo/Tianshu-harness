package agent

import (
	"testing"
)

// TestIsBashCommandAllowlistedOracle —— allow 判定与 TS 逐值对账（命令 × allowlist 矩阵）。
//
// 覆盖 `segmentMatchesAllowEntry` 的 5 道 fail-closed 守卫 + 放行路径。
// oracle 由 `go/testdata/shellsplit/gen-oracle.ts` 从真实 TS 导出。
func TestIsBashCommandAllowlistedOracle(t *testing.T) {
	o := loadShellsplitOracle(t)
	if len(o.Allowed) == 0 {
		t.Fatal("oracle 的 allowed 为空——生成器可能未更新（重跑 npx tsx go/testdata/shellsplit/gen-oracle.ts）")
	}
	for _, c := range o.Allowed {
		t.Run(c.Label, func(t *testing.T) {
			got := IsBashCommandAllowlisted(c.Cmd, c.Allowlist)
			if got != c.Allowed {
				t.Errorf("IsBashCommandAllowlisted(%q, %v) = %v, want %v",
					c.Cmd, c.Allowlist, got, c.Allowed)
			}
		})
	}
}

// TestAllowEntryFailClosedGuards —— 5 道守卫的**意图**测试（oracle 保证等价，
// 本测试保证语义可读、失败信号能定位到具体守卫）。
//
// 每道守卫都要有反例——否则"放行过宽"这类缺陷不会被发现。
func TestAllowEntryFailClosedGuards(t *testing.T) {
	// **allowlist 必须含全部 wrapper/解释器名**：否则「去掉守卫」变异下，
	// 命令仍因「未覆盖」而 false，检测不到守卫被删——覆盖缺口。
	allow := []string{
		"ls", "bash", "env", "node", "echo", "cat", "grep",
		"timeout", "nice", "nohup", "xargs", "source", "exec", "stdbuf", "parallel",
	}

	cases := []struct {
		guard string
		cmd   string
		want  bool
		why   string
	}{
		// 放行路径（对照基线）
		{"基线", "ls -la", true, "命中 allowlist"},

		// 守卫 1：环境赋值严格剥离
		{"守卫1", "CI=true ls", true, "惰性值可剥"},
		{"守卫1", "PATH=/tmp/evil: ls", false, "含路径分隔符与冒号——注入向量"},
		{"守卫1", "LD_PRELOAD=/x.so ls", false, "同上"},
		{"守卫1", "A=$HOME ls", false, "含美元展开"},
		{"守卫1", "A=b=c ls", false, "含第二个等号"},
		{"守卫1", "A=1", false, "只有赋值无命令"},

		// 守卫 2：残余替换符
		{"守卫2", "ls $(ls)", true, "替换体已抽净，两段各自覆盖"},
		{"守卫2", "echo $(a $(b) c)", false, "嵌套未解净——段含括号"},

		// 守卫 3：二进制含 $
		{"守卫3", "$CMD ls", false, "二进制是展开，静态不可知"},
		{"守卫3", "${CMD} ls", false, "同上"},

		// 守卫 4：wrapper 洗白
		// **每个 wrapper 都要有反例**：只在 allowlist 含该名时，删守卫才会红。
		{"守卫4", "env ls", false, "wrapper 会把参数当子进程跑"},
		{"守卫4", "timeout 5 ls", false, "同上"},
		{"守卫4", "nice ls", false, "同上"},
		{"守卫4", "nohup ls", false, "同上"},
		{"守卫4", "xargs ls", false, "同上"},
		{"守卫4", "source ls", false, "同上"},
		{"守卫4", "exec ls", false, "同上"},
		{"守卫4", "stdbuf -o0 ls", false, "同上"},
		{"守卫4", "parallel ls", false, "同上"},

		// 守卫 5：解释器 + 内联代码
		{"守卫5", "bash -c \"rm -rf /\"", false, "只匹配二进制名等于授予任意代码"},
		{"守卫5", "bash -lc \"x\"", false, "短 flag 簇"},
		{"守卫5", "node --eval \"x\"", false, "长 flag"},
		{"守卫5", "bash script.sh", true, "无内联 flag——可放行"},
		// **修正记录**：本用例初版写 `grep -c x f` 期望 true，是**测试期望错**——
		// oracle 的 allowlist 里没有 `grep`，TS 也返回 false。实现是对的。
		// 改用 tsx 探针实测过的两条（TS 真实行为）：
		{"守卫5", "grep -c x f", true, "非解释器，-c 无害（探针实测 TS=true）"},
		{"守卫5", "node --check x.js", true, "长 flag 无害——只认内联代码 flag（探针实测 TS=true）"},

		// 未建模字符整体拒绝
		{"未建模", "echo x > f", false, "重定向会静默写文件"},
		{"未建模", "cat < f", false, "输入重定向"},
		{"未建模", "ls \\", false, "反斜杠改变分隔符语义"},
		{"未建模", "ls !x", false, "叹号触发历史展开"},

		// 链式：任一段未覆盖 → 整条拒绝
		{"链式", "ls && ls -la", true, "两段都覆盖"},
		{"链式", "ls && rm -rf /", false, "rm 段未覆盖"},
	}

	for _, c := range cases {
		t.Run(c.guard+"|"+c.cmd, func(t *testing.T) {
			if got := IsBashCommandAllowlisted(c.cmd, allow); got != c.want {
				t.Errorf("IsBashCommandAllowlisted(%q) = %v, want %v（%s）", c.cmd, got, c.want, c.why)
			}
		})
	}
}

// TestAllowlistedEmptyAndTrivial —— 空 allowlist / 空命令的边界。
func TestAllowlistedEmptyAndTrivial(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		allow []string
		want  bool
	}{
		{"空allowlist", "ls", nil, false},
		{"空allowlist非nil", "ls", []string{}, false},
		{"空命令", "", []string{"ls"}, false},
		{"仅空白命令", "   ", []string{"ls"}, false},
		{"allowlist含空条目", "ls", []string{"", "ls"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsBashCommandAllowlisted(c.cmd, c.allow); got != c.want {
				t.Errorf("IsBashCommandAllowlisted(%q, %v) = %v, want %v", c.cmd, c.allow, got, c.want)
			}
		})
	}
}
