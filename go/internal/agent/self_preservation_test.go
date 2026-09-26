package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// selfkillOracle 是 oracle.json 的结构。
//
// 由 `go/testdata/selfkill/gen-oracle.ts` 从**真实 TS 代码路径**导出。
type selfkillOracle struct {
	Tree struct {
		SelfPid      int   `json:"selfPid"`
		AncestorPids []int `json:"ancestorPids"`
	} `json:"tree"`
	PidCases []struct {
		Label  string `json:"label"`
		Cmd    string `json:"cmd"`
		Result bool   `json:"result"`
	} `json:"pidCases"`
	ImageCases []struct {
		Label  string `json:"label"`
		Cmd    string `json:"cmd"`
		Result bool   `json:"result"`
	} `json:"imageCases"`
	GuardTree struct {
		SelfPid      int   `json:"selfPid"`
		AncestorPids []int `json:"ancestorPids"`
	} `json:"guardTree"`
	GuardCases []struct {
		Label  string `json:"label"`
		Cmd    string `json:"cmd"`
		Result bool   `json:"result"`
	} `json:"guardCases"`
}

func loadSelfkillOracle(t *testing.T) selfkillOracle {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "selfkill", "oracle.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 oracle 失败：%v（先跑 npx tsx go/testdata/selfkill/gen-oracle.ts）", err)
	}
	var o selfkillOracle
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatalf("解析 oracle 失败：%v", err)
	}
	if len(o.PidCases) == 0 {
		t.Fatal("oracle 的 pidCases 为空——生成器可能失败")
	}
	return o
}

// TestIsSelfDestructiveKillOracle —— PID 类与 TS 逐值对账。
func TestIsSelfDestructiveKillOracle(t *testing.T) {
	o := loadSelfkillOracle(t)
	tree := selfKillProcessTree{SelfPid: o.Tree.SelfPid, AncestorPids: o.Tree.AncestorPids}

	for _, c := range o.PidCases {
		t.Run(c.Label, func(t *testing.T) {
			got := IsSelfDestructiveKill(c.Cmd, tree)
			if got != c.Result {
				t.Errorf("IsSelfDestructiveKill(%q, tree=%+v) = %v, want %v",
					c.Cmd, tree, got, c.Result)
			}
		})
	}
}

// TestImageKillIntentionallyNotPorted —— **有意差异的显式断言**。
//
// TS 对镜像名类（`pkill node` / `taskkill /IM node.exe`）返回 **true**；
// Go 侧**有意返回 false**（理由：Go 二进制名是 `tianshu`，无「习惯性
// pkill tianshu」动机，收益低——见 `self_preservation.go` 文件头）。
//
// 本测试把差异**钉住**：将来若有人「顺手补齐」镜像名类，此测试会红，
// 迫使他先读文件头的决策理由再改。
func TestImageKillIntentionallyNotPorted(t *testing.T) {
	o := loadSelfkillOracle(t)
	tree := selfKillProcessTree{SelfPid: o.Tree.SelfPid, AncestorPids: o.Tree.AncestorPids}

	if len(o.ImageCases) == 0 {
		t.Fatal("oracle 的 imageCases 为空——生成器可能失败")
	}
	for _, c := range o.ImageCases {
		t.Run(c.Label, func(t *testing.T) {
			// 前提校验：TS 侧确实是 true（否则这条差异断言无意义）。
			if !c.Result {
				t.Fatalf("oracle 前提不成立：TS 对 %q 应为 true，实得 false——"+
					"语料或 TS 语义已变，需重审本测试", c.Cmd)
			}
			if got := IsSelfDestructiveKill(c.Cmd, tree); got {
				t.Errorf("Go 侧对镜像名类 %q 返回 true——但这是**有意不移植**的"+
					"（见 self_preservation.go 文件头「有意差异」）。若确要移植，"+
					"请先更新文件头的决策理由与本测试。", c.Cmd)
			}
		})
	}
}

// TestSelfDestructiveKillGuardCases —— **守卫判别性**（树含 0/负值）。
//
// # 为什么单列一组
//
// `n > 0` 守卫在**真实树**（PID 恒 > 0，内核保证）下**永不可判别**——
// 变异反证 M1（去掉 `n <= 0`）红 0 暴露了这点：`kill 0` 的 `0` 不在正常
// 树里，去掉守卫仍返回 false。
//
// 但守卫对账 TS 的 `Number()` 语义（`0`→非正、`1000.5`→NaN），是**语义
// 保真**的一部分，不该因「真实场景不触发」就留着不测。故用「树含 0/负值」
// 这组现实中不可能但**能判别守卫**的输入。
//
// **这是覆盖缺口的补强**，不是"凑覆盖率"——去掉守卫时它会红（可验证）。
func TestSelfDestructiveKillGuardCases(t *testing.T) {
	o := loadSelfkillOracle(t)
	if len(o.GuardCases) == 0 {
		t.Fatal("oracle 的 guardCases 为空——生成器可能未更新")
	}
	tree := selfKillProcessTree{SelfPid: o.GuardTree.SelfPid, AncestorPids: o.GuardTree.AncestorPids}

	for _, c := range o.GuardCases {
		t.Run(c.Label, func(t *testing.T) {
			got := IsSelfDestructiveKill(c.Cmd, tree)
			if got != c.Result {
				t.Errorf("IsSelfDestructiveKill(%q, guardTree=%+v) = %v, want %v"+
					"（守卫应挡住 0/负值——对账 TS 的 Number() 语义）",
					c.Cmd, tree, got, c.Result)
			}
		})
	}
}

// TestSelfDestructiveKillDynamicPidBlindSpot —— **已知盲区**（TS 同样存在）。
//
// # 端到端验收时实测发现（第六十八刀）
//
// 真实 CLI 跑 `echo BEFORE; kill $PPID; echo AFTER` → 工具 `→ ok`，
// 随后 agent `context canceled`（**真的被杀了**）。探针定位：
//
//	"kill $PPID"          → false   ← 字面量 `$PPID` 不是数字
//	"kill ${PPID}"        → false
//	"kill `echo 1000`"    → false
//	"kill $(echo 1000)"   → false
//	"kill 1000"           → true    ← 写死的数字才拦
//
// **TS 侧实测同样全 false**（用同一组命令跑 TS `isSelfDestructiveKill`）。
// 故这是**两侧共有的固有盲区**，不是移植引入的缺陷：
// 判定是**静态字符串分析**，无法求值 shell 变量/命令替换。
//
// # 为什么仍然值得保留
//
// 它挡住的是**最可能的形态**——TS 的字段报告正是写死镜像名的
// `taskkill //F //IM node.exe`；agent 自发写 `kill $PPID` 是小概率事件。
// 要闭合盲区需要**动态求值**（跑一次 shell 展开再判），代价与风险都高得多。
//
// 本测试**固化盲区**：将来若有人想闭合它，会先看到这条已知边界与理由。
func TestSelfDestructiveKillDynamicPidBlindSpot(t *testing.T) {
	tree := selfKillProcessTree{SelfPid: 1000, AncestorPids: []int{999}}

	// 前提：写死数字**能**拦（否则盲区测试无意义——可能整体失效）。
	if !IsSelfDestructiveKill("kill 1000", tree) {
		t.Fatal("前提不成立：`kill 1000` 应被拦（写死 PID 是主要防护形态）")
	}

	blind := []struct {
		cmd string
		why string
	}{
		{"kill $PPID", "shell 变量——静态分析无法求值"},
		{"kill ${PPID}", "同上（花括号形态）"},
		{"kill `echo 1000`", "命令替换——反引号形态"},
		{"kill $(echo 1000)", "命令替换——$( ) 形态"},
		{"kill $SELF", "任意变量"},
	}
	for _, c := range blind {
		t.Run(c.cmd, func(t *testing.T) {
			if IsSelfDestructiveKill(c.cmd, tree) {
				t.Logf("盲区已被闭合（%s）——若这是有意改动，请更新本测试与"+
					"self_preservation.go 的「已知盲区」注释", c.why)
				return
			}
			// 当前预期：不拦。这是**已知边界**，非缺陷。
			t.Logf("已知盲区：%q 不被拦（%s）——TS 侧同样如此", c.cmd, c.why)
		})
	}
}

// TestSelfDestructiveKillCoreSemantics —— 核心语义的**意图**测试。
//
// oracle 保证与 TS 等价，本测试保证语义可读、失败信号能定位到具体规则。
func TestSelfDestructiveKillCoreSemantics(t *testing.T) {
	// 固定树：self=1000，祖先=[999]。
	tree := selfKillProcessTree{SelfPid: 1000, AncestorPids: []int{999}}

	cases := []struct {
		rule string
		cmd  string
		want bool
		why  string
	}{
		{"自身PID", "kill 1000", true, "杀自身"},
		{"祖先PID", "kill 999", true, "杀父进程——同样中断会话"},
		{"带信号", "kill -9 1000", true, "信号 flag 不是 PID，但后续 PID 命中"},
		{"多个PID任一命中", "kill 12345 1000", true, "任一所含 PID 命中即拦"},
		{"无关PID", "kill 12345", false, "定向杀无关进程——不该拦"},
		{"零", "kill 0", false, "0 不是有效 PID（对账 TS n > 0）"},
		{"负数组", "kill -9 -1000", false, "负数是进程组语法，不是 PID"},
		{"非整数", "kill 1000.5", false, "非整数不匹配"},
		{"混合token", "kill 1000abc", false, "整串必须是数字"},
		{"无参数", "kill", false, "没有 PID"},
		{"token严格等于kill", "killall 1000", false, "killall 是镜像名类，不在此处理"},
		{"前缀相似不命中", "killer 1000", false, "tokens[0] 必须严格等于 kill"},
		{"参数里的kill", "echo kill 1000", false, "不是命令头"},
		{"windows-PID", "taskkill /PID 1000", true, "Windows 等价语法"},
		{"windows-双斜杠", "taskkill //PID 1000", true, "MSYS/Git-Bash 形态"},
		{"windows-带引号", "taskkill /PID \"1000\"", true, "引号包裹仍命中"},
		{"windows-无关PID", "taskkill /PID 12345", false, "无关 PID"},
		{"分段-分号", "echo hi; kill 1000", true, "藏在执行符后仍捕获"},
		{"分段-与", "ls && kill 1000", true, "同上"},
		{"分段-管道", "cat f | kill 1000", true, "同上"},
		{"分段-换行", "ls\nkill 1000", true, "同上"},
		{"分段-命令替换", "echo $(kill 1000)", true, "替换体内仍捕获"},
		{"分段-反引号", "echo `kill 1000`", true, "同上"},
		{"不误伤-killport", "npx kill-port 3000", false, "定向重启服务的正当用法"},
		{"不误伤-echo数字", "echo 1000", false, "数字出现在参数里不是 kill"},
		{"空命令", "", false, "空"},
		{"仅空白", "   ", false, "空白"},
	}

	for _, c := range cases {
		t.Run(c.rule+"|"+c.cmd, func(t *testing.T) {
			if got := IsSelfDestructiveKill(c.cmd, tree); got != c.want {
				t.Errorf("IsSelfDestructiveKill(%q) = %v, want %v（%s）", c.cmd, got, c.want, c.why)
			}
		})
	}
}

// TestCurrentProcessTree —— 运行时进程树解析的合理性。
//
// **不断言具体 PID**（每次运行都变），只断言结构不变量：
// selfPid > 0，且祖先含一个正数 PID。
func TestCurrentProcessTree(t *testing.T) {
	tree := currentProcessTree()
	if tree.SelfPid <= 0 {
		t.Errorf("selfPid 应为正数，实得 %d", tree.SelfPid)
	}
	if len(tree.AncestorPids) == 0 {
		t.Error("应至少含直接父进程 PID")
	}
	for _, p := range tree.AncestorPids {
		if p <= 0 {
			t.Errorf("祖先 PID 应为正数，实得 %d", p)
		}
	}
}

// TestSelfDestructiveKillAgainstRealTree —— 用**真实进程树**验证端到端可用。
//
// 构造 `kill <自身PID>` 与 `kill <父PID>` 两条命令，确认都命中。
// 这是「探针实测的进程树关系」在代码里的固化。
func TestSelfDestructiveKillAgainstRealTree(t *testing.T) {
	tree := currentProcessTree()

	self := "kill " + strconv.Itoa(tree.SelfPid)
	if !IsSelfDestructiveKill(self, tree) {
		t.Errorf("`%s` 应命中（杀自身）", self)
	}
	if len(tree.AncestorPids) > 0 {
		parent := "kill " + strconv.Itoa(tree.AncestorPids[0])
		if !IsSelfDestructiveKill(parent, tree) {
			t.Errorf("`%s` 应命中（杀父进程）", parent)
		}
	}
	// 反例：一个几乎不可能存在的 PID。
	if IsSelfDestructiveKill("kill 2147483646", tree) {
		t.Error("无关 PID 不该命中")
	}
}
