package agent

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ── 自身进程树保护（selfKill）──
//
// 对账 TS `src/agent/self-preservation.ts` 的 `isSelfDestructiveKill`
// （**仅 PID 类，见下**）。
//
// # 为什么需要它
//
// bash 工具用 `exec.Command` 启动 shell，故 **agent 就是命令的直接父进程**。
// 实测进程树（`echo $PPID; ps -p $PPID`）：
//
//	SELF=72921 PPID=72918
//	  PID  PPID COMM
//	72918 72916 /tmp/ts-probe      ← Go agent 自身
//
// 故 shell 里 `kill $PPID` 会杀掉 agent 自己——**当前 turn 中断**。
// 这与语言无关：TS 的 `killsOwnPidInSegment` 逻辑在 Go 下同样成立。
//
// # 有意差异：只移植 PID 类，不移植镜像名类
//
// TS 的 `isSelfDestructiveKill` 挡**两类**：
//
//	A. 镜像名类（`pkill node` / `taskkill /IM node.exe` / `killall node`）
//	B. PID 类（`kill <自身/祖先 pid>` / `taskkill /PID <n>`）
//
// **Go 侧只做 B**。理由**不是**「A 不适用」——Go 侧确有对应物
// （`pkill tianshu` 能杀 agent，二进制名可经 `os.Executable()` 取到）。
// 真实理由是**收益低**：
//
//   - TS 做 A 是因为「node」是**极常见的**进程名，用户/agent 常有
//     `pkill node`「重启本地服务」的习惯（TS 注释记录的字段报告正是
//     `taskkill //F //IM node.exe`）。误伤概率高。
//   - Go 侧二进制名是 `tianshu`（或用户自定名），**没有**「习惯性
//     `pkill tianshu`」这一动机——要做也是做 `pkill <自己名字>`，
//     而那需要先解析二进制名，收益与复杂度不成比例。
//
// **严重性低于 TS**（结构差异）：TS 的动机是「杀 sidecar → API 认证上下文
// 丢失 → 401 级联」；Go 侧无 `auth` 包，API key 来自环境变量
// （`main.go:128`）→ 进程重启**无鉴权损失**。Go 会话又**增量落盘**
// （`.rivet/sessions/<id>.jsonl`，batchwriter 首行同步 flush）→ 被杀丢的是
// **当轮未 flush 的部分**，不是全部历史。故真实损失 = 当前 turn 中断 +
// 少量未落盘上下文。
//
// oracle 把两类分开导出（`pidCases` 对账 / `imageCases` 断言差异），
// 防将来有人「顺手补齐 A」时不知这是决策。
//
// # 已知盲区：动态求值的 PID（TS 同样存在，端到端实测发现）
//
// 判定是**静态字符串分析**，无法求值 shell 变量/命令替换。实测（探针）：
//
//	"kill $PPID"          → false   ← 字面量 `$PPID` 不是数字
//	"kill ${PPID}"        → false
//	"kill `echo 1000`"    → false
//	"kill $(echo 1000)"   → false
//	"kill 1000"           → true    ← 写死的数字才拦
//
// **TS 侧用同一组命令实测同样全 false**——故这是两侧共有的固有盲区，
// 不是移植引入的缺陷。真实 CLI 端到端验证过：`echo BEFORE; kill $PPID;
// echo AFTER` 会让 agent `context canceled`（真的被杀）。
//
// **为什么仍然保留**：它挡住最可能的形态（TS 的字段报告是写死镜像名的
// `taskkill //F //IM node.exe`）；agent 自发写 `kill $PPID` 是小概率事件。
// 闭合盲区需要**动态求值**（先跑一次 shell 展开再判），代价与风险高得多。
// 测试 `TestSelfDestructiveKillDynamicPidBlindSpot` 固化了这条边界。

// selfKillProcessTree 是判定所需的进程树（自身 + 祖先 PID）。
//
// 对账 TS `SelfProcessTree`。**显式传参而非全局读**：让判定是纯函数、
// 可测（TS 用带缓存的 `selfProcessTree()` 默认参数，Go 侧由调用方注入）。
type selfKillProcessTree struct {
	SelfPid      int
	AncestorPids []int
}

// currentProcessTree 解析当前进程的自身 + 祖先 PID。
//
// 对账 TS `getSelfProcessTree`。`os.Getpid`/`os.Getppid` 是标准库，
// **无需平台分叉**（TS 要处理 `process.ppid` 可能缺失）。
//
// **只取直接父进程**：TS 也只取 `process.ppid`（注释：best-effort，至少
// 立即父进程）。取完整祖先链需要平台特定的 `ps`/`/proc` 解析，收益不抵
// 复杂度——agent 的父进程（shell/终端）被杀同样会中断会话，这是主要场景。
func currentProcessTree() selfKillProcessTree {
	t := selfKillProcessTree{SelfPid: os.Getpid()}
	if ppid := os.Getppid(); ppid > 0 {
		t.AncestorPids = append(t.AncestorPids, ppid)
	}
	return t
}

// taskkillPidRe 匹配 `taskkill /PID <n>`（含 MSYS/Git-Bash 的 `//PID`）。
//
// 对账 TS `const pidRe = /\/{1,2}pid\s+["']?(\d+)/gi`。
var taskkillPidRe = regexp.MustCompile(`(?i)/{1,2}pid\s+["']?(\d+)`)

// taskkillWordRe 判定段里是否含 `taskkill`。
//
// 对账 TS `/\btaskkill\b/i.test(trimmed)`。**先判词再抽 PID**：避免
// `echo "taskkill /PID 1000"` 这类非命令文本被当命令解析——不过 TS 也
// 只看子串，故此处逐值对账（不"顺手收紧"）。
var taskkillWordRe = regexp.MustCompile(`(?i)\btaskkill\b`)

// tokenWhitespaceRe 按空白切 token（对账 TS `trimmed.split(/\s+/)`）。
var tokenWhitespaceRe = regexp.MustCompile(`\s+`)

// killsOwnPidInSegment 报告单段是否杀了自身进程树里的 PID。
//
// 对账 TS `killsOwnPidInSegment`（`self-preservation.ts:54-76`）。
//
// **只判 PID 类**——镜像名类（`pkill node` 等）不在 Go 侧范围内（见文件头）。
func killsOwnPidInSegment(segment string, pids map[int]bool) bool {
	trimmed := strings.TrimLeft(segment, " \t\n\v\f\r")

	// Windows：`taskkill /PID <n>`（含 `//PID`）。
	if taskkillWordRe.MatchString(trimmed) {
		for _, m := range taskkillPidRe.FindAllStringSubmatch(trimmed, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && pids[n] {
				return true
			}
		}
	}

	// Unix：`kill [-SIG] <pid> …`。
	//
	// **`tokens[0]` 必须严格等于 `kill`**（对账 TS）：`pkill`/`killall` 是
	// 镜像名类，不在此处理；`killer`/`echo kill` 也不得命中。
	tokens := tokenWhitespaceRe.Split(trimmed, -1)
	if len(tokens) > 0 && tokens[0] == "kill" {
		for _, tok := range tokens[1:] {
			if strings.HasPrefix(tok, "-") {
				continue // 信号 flag，不是 PID
			}
			// 对账 TS `Number(tok)` + `Number.isInteger(n) && n > 0`：
			// **必须整串是数字**——`1000abc`/`1000.5` 不算（TS 的 Number()
			// 对它们返回 NaN 或非整数）。Go 的 Atoi 语义一致。
			n, err := strconv.Atoi(tok)
			if err != nil || n <= 0 {
				continue
			}
			if pids[n] {
				return true
			}
		}
	}

	return false
}

// IsSelfDestructiveKill 报告命令是否会终止 agent 自身的进程树。
//
// 对账 TS `isSelfDestructiveKill`（`self-preservation.ts:81-94`）的 **PID 类**
// 分支——镜像名类有意不移植（见文件头「有意差异」）。
//
// **分段后逐段判**：`foo; kill $PPID` 藏在执行符后仍被捕获
// （复用第六十五刀移植的 `splitShellSegments`）。
//
// **不误伤**：指向无关 PID 的定向 kill、`npx kill-port <port>` 都不匹配。
func IsSelfDestructiveKill(command string, tree selfKillProcessTree) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	pids := map[int]bool{tree.SelfPid: true}
	for _, p := range tree.AncestorPids {
		pids[p] = true
	}
	for _, seg := range splitShellSegments(command) {
		if killsOwnPidInSegment(seg, pids) {
			return true
		}
	}
	return false
}

// selfKillBlockReason 是 selfKill 命中时的拒绝文案。
//
// **与 `DeniedRuleReason` 不同**：那条讲「用户配的边界」，这条讲「自杀」——
// 模型需要的指引也不同（不是「问用户放宽」，而是「别杀自己，换方式重启」）。
func selfKillBlockReason() string {
	return "Tool execution blocked: this command would terminate the agent's own " +
		"runtime (the process this session runs in). That aborts the session " +
		"mid-turn. To restart a local dev server, kill a specific non-agent PID " +
		"or use a targeted command like `npx kill-port <port>`; otherwise ask the " +
		"user to restart it manually."
}
