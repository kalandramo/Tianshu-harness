# 交接文档 — Go 重写天枢运行时（macOS 会话 · 审批门 / 认知状态 / hooks）

> 生成时间：2026-09-26 · 设备：macOS（Darwin 25.6.0，作者 moweilong）
> 仓库：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`
> 分支：`go-runtime` · HEAD：`c7f91de8` · 工作树 **clean**
> 本会话共 **20 个提交**（`e866fad8`..`c7f91de8`），全部在 `go-runtime` 分支
> 本文自包含——读者无需本会话任何上下文。

---

## 任务目标

**一句话目标**：把 TypeScript 版天枢运行时（`src/`）**行为等价**地移植成 Go（`go/`），逐刀推进、每刀独立验证。

**本会话实际推进的三条线**（前序会话已完成压缩/session-split 主线，见 `go/HANDOFF.md` 第六十刀）：
1. **审批门接线**——修「配置字段存在但零调用者」导致的 fail-open 静默执行
2. **认知状态子系统称量**——核实「该不该做」，三次判定「不该做」
3. **hooks 隔离缺陷**——修 `internal/hooks` 全量并发时间歇失败

**非目标**：
- 不重写 TS 版（`src/` 下代码一行未动）。
- 不追求「功能更多」——只追求**与 TS 版行为等价**。任何偏离 TS 语义的「优化」都是缺陷。
- 不碰 `go/` 以外的模块。

**硬约束**：前缀缓存工程是核心指标——冻结 system prompt 必须**字节稳定**。任何进入冻结前缀的字符串（如 `<environment os="...">` 行、handoff 文本）差一个字节就会让缓存命中率崩掉。

---

## 拓扑（先读这段，否则会搞错上下文）

`go-runtime` 分支上，**两段会话接力**：

| 会话 | 设备 | 提交区间 | 主题 |
|---|---|---|---|
| 前序 | Windows（`D:\code\Tianshu-harness`） | 至 `516a226`（2026-09-20） | Windows 可移植性 + 压缩/session-split 主线 |
| 中间 | macOS | `516a226`..`6c6904e9`（483 提交，其中 102 触及 `go/`） | 第三十一刀..第六十刀（volatile / 动态 appendix / terse / plan-mode 等） |
| **本会话** | macOS | `e866fad8`..`c7f91de8`（20 提交） | 审批门 / 认知状态称量 / hooks |

**关键数字（实测）**：`git rev-list --count 516a226..HEAD` = **483**；`-- go/` 限定 = **102**。

**权威文档**：
- `go/HANDOFF.md`（**7018 行**，每刀一个专章）——覆盖到**第六十刀**（2026-09-24）。**本会话（第六十一刀起）尚未写入该文件**。
- `go/PLAN.md`——架构欠账清单。

**注意**：本文档路径是 `.rivet/HANDOFF.md`，**被 git 跟踪**（上一版提交 `4ecab8b2`）。本次为覆盖写，旧版（20584 字节，Windows 会话的 16 刀详细记录）仍可从 git 取回：

```
git show 4ecab8b2:.rivet/HANDOFF.md
```

旧版的**技术细节**并非丢失——已核实 `go/HANDOFF.md` 保留了 Windows 可移植性内容（`Windows`/`win32`/`GBK`/`代码页` 关键词命中 77 处，`WaitDelay`/`代码页`/`936` 命中 7 处）。本文档「坑」1–15 是那批记录的**提炼索引**，细节仍以 `go/HANDOFF.md` 为准。

---

## 已完成

### 第 0 段：修测试基线（起点是「非 Windows 上 30 个测试红」）

#### `e866fad8` rawstore grant 路径符号链接规范化（macOS 授权静默失效）
macOS 上 `/var` 是 `/private/var` 的符号链接，`hasPathPrefix` 比较未规范化 → 授权判定失败。修 `internal/…/rawstore.go`。

#### `aa53e594` 修测试基线的 6 族 Windows 平台假设——非 Windows 上从 **30 红到全绿**
根因是测试夹具假设 Windows 语义（路径分隔符、`.exe`、mode 位等），在 macOS 上不成立。

#### `53c4436e` ShellKind 注入化
`DetectShellKind` 偷读 `runtime.GOOS` → win32 渲染路径不可测。归位为 `HostEnv.ShellKind` 字段注入。

#### `da228164` 解除纯函数 win32 语义测试的不必要门控
**教训**：门控（`if runtime.GOOS == "windows" { t.Skip }`）是**覆盖损失**——纯函数在任意平台都可测，不该门控。

#### `00decb97` 移植 `git_scout` 只读 git 史实侦察工具（工具集 21 → 22）
新增 `go/internal/tools/gitscout.go`（457 行）+ `gitscout_test.go`。`gitScoutSafe`（`gitscout.go:189`）是只读白名单守卫。

### 第 1 段：审批门系列（核心成果——修 5 处 fail-open）

**共性根因**：TS 侧有的门，Go 侧「字段/函数存在但零调用者」→ 静默 fail-open。

#### `1c500373` 接线档位审批门——修 4 个写工具在 manual 档静默执行
`Registry.NeedsApproval` **零调用者** → 4 个写工具（write_file / edit_file / …）在 `manual` 档**静默执行**。
**首版实测即错**：`auto-safe` 档必须用 `isHighRisk` 而非 `needsApproval`——否则 `write_file` 在 auto-safe 下不可用（TS 语义是 auto-safe 放行低风险写）。

#### `b94bf8ce` 接线 bash 写命令审批门——修 manual 档下 `mkdir/cp/重定向` 静默执行
新增 `BashCommandMayWrite`（`internal/agent/…`）。**一处错误声称已修正**：硬闸门（`isDestructiveCommand`）与本门（`BashCommandMayWrite`）**命令集只有部分交集**——`mkdir` **不在**硬闸门内（我最初误以为在）。

#### `bcc86951` 修**我上一刀引入的回归** + 补 allowlist 全工具面生效
`skipAllApproval` 短路未复刻 → **skip 档下 bash 写命令全被拦 → headless 死锁**。
**我的纪律错误（记录）**：用 `git checkout` 恢复变异测试**丢掉了本轮全部修改**——项目规则明令禁止。已重新应用，后改**副本文件**做变异。

#### `6d70f6e0` 接线 `permissions.bash.denylist`——修用户黑名单被静默忽略
`PermissionConfig` **连 `bash` 字段都没有** → 用户 denylist 被 `json.Unmarshal` 静默丢弃。
新增 `go/internal/agent/permissions_shellsplit.go`（163 行）：`splitShellSegments:53` + `IsBashCommandDenied:153`。
**平台无关 oracle**：`go/testdata/shellsplit/`。
**发现**：既有 `internal/…/approvalrisk/oracle.json` **不可重现于 macOS**——含 `pathGrant` 平台相关段，是 Windows 基准，故**不重跑**。

#### `9cbf5707` 接线 `permissions.bash.allowlist`
新增 `go/internal/agent/permissions_bashallow.go`（246 行）：`segmentMatchesAllowEntry:116` 有 **5 道 fail-closed 守卫**（环境赋值严格剥离 / wrapper 洗白 / 解释器内联代码等）。
**两处测试期望错**（`grep -c` / `node -c`），用 `npx tsx` 探针实测 TS 真实行为定案。
**M1 首次红 1 = 覆盖缺口**，补 allowlist 语料后红 9。

#### `97b6e67a` 接线 selfKill 自身进程树保护（称量见 `75112524`）
新增 `go/internal/agent/self_preservation.go`（192 行）：`currentProcessTree:91` + `IsSelfDestructiveKill:166`。
**称量修正**：TS 挡两类，Go 侧**只移植 PID 类**。实测进程树 `SELF=72921 PPID=72918`，`72918 COMM=/tmp/ts-probe`——**agent 就是 bash 命令的直接父进程**，`kill <该 pid>` 杀 agent 自己。
**修正**：镜像名类不是「不适用」而是「**收益低**」（Go 确有 `pkill tianshu` 对应物，只是无该习惯性动机）。
**已知盲区（诚实标注）**：`kill $PPID` / `$(echo 1000)` **不拦**——静态字符串分析无法求值（**TS 侧实测同样全 false**，非移植缺陷）。
**RED 反证最强证据**：移除接线 → 测试进程 **`signal: terminated`**（自杀命令真的执行）。
**测试卫生**：`npx kill-port` 耗时 **107.88s**（真去拉包）→ 改本地命令 **0.01s**。

#### `0be170c1` / `1ed6ef11` / `e4a29ae7` / `51a9563c` 状态表对账（4 个 docs 提交）
`go/internal/agent/approval_gate.go` 内的状态表注释：`1ed6ef11` 修 6 处过期声称 + `registry.go` 3 处 + **1 处我自己的事实错误**（`loop.go` 把「Go 实际顺序」写成「TS 顺序」）。
`e4a29ae7` **自我纠错**：我上一刀刚立「不写行号」规矩，同刀内又写了失效锚点（`pathGrant 988→997`）。5 处漂移、3 处准确，**全部改函数名引用**。
`51a9563c` 回答「是否已完善」= **否**，补 4 处缺口（B' 节 `permissionsOverlay` 来源缺失、C 节 `shouldAsk` 之后两段漏列、`yoloBypassesUnconditional`、行号不准确）。

### 第 2 段：认知状态子系统——三次称量，**均判定「不该做」**

这一段的产出是**否决**（记录在 `approval_gate.go` 状态表 D/E 节），价值在于**避免造无消费者的机制**。

#### `d2246fc8` protectionMode 称量——接线后**行为完全不变**，故不做
- 破坏性 git（`git reset --hard` 等）**已在 `destructivePatterns`** → 硬闸门任何档位都拦，接线后该路径行为不变。
- 次要产出（`Level`→`Medium`）**无人消费**（`isHighRiskCall` 只看 `Level == RiskHigh`）。
- 真正的 doom-loop 终止**已有** `wedge_guard.go`（`loop.go` 的 `observeBatch`/`shouldTerminate`）。

#### `62b2d345` pressureResult 链称量——**已完整实现但零实例化**，故不接线
- `compact.PressureMonitor` 已实现（含 `Suggestion` 字段 + oracle 绿 `TestPressureCheckParity`）——**零实例化**。
- 其独立消费者 `turn-intent`（`thrashingSuggestion`）Go 侧**不存在**（`TurnIntent` 标识符在生产代码零出现），且还依赖 `strategy` + `sensorium` + `pheromones` + `recentToolHistory`（**四项皆缺**）。
- **顺带探针实测**：`canAutoApprove` **完全冗余**——256 组穷举（档位 × 风险级 × 置信度 × sensorium 有无 × needsApproval）**0 组差异**。
- **结论**：sensorium 的两个直接消费者**一个冗余、一个无消费者**。

#### `1b9c8ed8` TDD gate 编辑计数（**唯一实际落地的子系统件**）
- **称量修正任务定义**：Go 侧**已有** `session.Manager.TrackFileModified` / `RecordVerification`（`loop.go` 的 `observeToolResult` 已接线），缺的只是 `editsSinceLastTest`（相对计数，验证即归零）。故新增 `evidenceTracker` 为**薄计数器**，避免双重真相源。
- 新增 `go/internal/agent/evidence.go`（237 行）：`TrackFileModified:149` / `TrackVerification:168` / `GateState:184` / `HasVerificationDebt:212`。
- **修的既有缺陷**：`observeToolResult` 开头 `if res.IsError { return }`，但 `run_tests` 失败时 `IsError: exitCode != 0` → 早退把失败验证挡住 → `hasFailedTests` **恒 false**。修法：早退排除 `run_tests`。
- **实测踩坑**：`session.RecordVerification` 是**同 target 替换**语义，TS 的 `verifications` 是**追加数组**（oracle `two-verifies` 期望 2、替换语义得 1）→ tracker 自持追加计数。
- 54 子用例 + 变异反证 3 个 + RED 反证 + 2 条不变量。
- **接线**：`loop.go:321` `l.evidence = newEvidenceTracker()`；写入端 `loop.go:1219/1226`（TrackFileModified）、`loop.go:1250`（TrackVerification）。
- **⚠ 读取端零消费（有意披露）**：`GateState()` / `HasVerificationDebt()` 在**生产代码零引用**（仅测试引用）。`loop.go:196` 的注释**明确写了**「目前**无 gate 拦截消费方**——本刀只建立追踪与派生，拦截是独立的一刀（避免造无消费者的门）」。**这不是缺陷，是显式披露**。

### 第 3 段：`c7f91de8` 修 hooks 间歇失败根因

**症状**：`go test ./...` 时 `internal/hooks` 间歇 FAIL（约 1/3），失败信息「脚本应执行成功，实得 `""`」。

**我上一轮的归因是错的（记录修正）**：曾推测是「测试间通过全局 trust 文件互相污染」。核实后**否定**：
- `t.Setenv` 在包内是**串行安全**的（本包无 `t.Parallel`）
- 全量跑是**每包独立进程**，跨包不共享环境变量
- 失败耗时 **5.01s** = `DefaultTimeoutMs` 的**指纹**
- 单包连跑 18 次**全绿**（负载低时不触发）

**真正的根因（探针实测确证）**，两条：

1. **产品缺陷**：`internal/hooks/user_hooks.go` 的 `runOne` 最后一行是 `Output: strings.TrimSpace(out)`——超时时 `out` 是**空串**（脚本还没输出就被杀），而 `ErrTimeout`（`user_hooks.go:295`）**没写进 `Output`**。后果：调用方看到「hook 失败」但**不知道为什么**，无法区分「脚本不存在」「超时」「退出码非零」。
   **探针证据**：把 `timeoutMs` 降到 1ms，产出 `Ok=false Output=""`——与全量失败的形态**逐字一致**。
2. **测试缺陷**：执行真实脚本的测试**未声明超时**，依赖产品默认值 `DefaultTimeoutMs = 5000`（`user_hooks.go:74`）。`go test ./...` 并行时 CPU/IO 争抢 → 起 `sh` 进程变慢 → 撞超时。

**修法**：
- 产品侧：`err != nil` 时把错误文本并入 `Output`（**保留已有 out**——非零退出码场景下脚本 stderr 是有效诊断信息）。
- 测试侧：给执行真实脚本的测试显式声明 `timeoutMs:30000`（`user_hooks_test.go`）。**不改 `DefaultTimeoutMs`**——那是产品默认值，影响真实用户。
- 新增 `go/internal/hooks/hook_timeout_test.go`（137 行）：`TestHookTimeoutReportsReason:40` / `TestHookNonZeroExitReportsReason:80` / `TestHookTimeoutDoesNotBlockForever:112`。

**验证**：RED 反证（修前 `TestHookTimeoutReportsReason` 红）；变异 M1 回退错误写入 → 红 1；全量 **×6 全绿**（修复前约 1/3 失败）；`-race` hooks 包绿；vet/gofmt 干净；无探针残留。
**诚实标注**：M2 变异（去掉测试的 `timeoutMs` 回到 5s）3 次全量**未复现**——**弱证据**（原复现率约 1/3，3 次不中概率约 30%）。修复正当性不依赖 M2，基于已确证根因。

### 本会话新增文件全表（`git log --diff-filter=A e866fad8^..HEAD`）

```
go/internal/agent/approval_skip_allow_wiring_test.go
go/internal/agent/bash_allowlist_wiring_test.go
go/internal/agent/bash_denylist_wiring_test.go
go/internal/agent/evidence.go                      (237 行)
go/internal/agent/evidence_test.go
go/internal/agent/evidence_wiring_test.go
go/internal/agent/permissions_bashallow.go         (246 行)
go/internal/agent/permissions_bashallow_test.go
go/internal/agent/permissions_shellsplit.go        (163 行)
go/internal/agent/permissions_shellsplit_test.go
go/internal/agent/self_preservation.go             (192 行)
go/internal/agent/self_preservation_test.go
go/internal/agent/selfkill_wiring_test.go
go/internal/config/permissions_bash_test.go
go/internal/hooks/hook_timeout_test.go             (137 行)
go/internal/tools/gitscout.go                      (457 行)
go/internal/tools/gitscout_test.go
go/testdata/evidence/gen-oracle.ts + oracle.json
go/testdata/selfkill/gen-oracle.ts + oracle.json
go/testdata/shellsplit/gen-oracle.ts + oracle.json
```

### 验证基线（本会话末次**真实工具输出**）

| 命令 | 结果 |
|---|---|
| `cd go && go test ./... -count=1` | **26 包 ok、0 FAIL**（`go list ./...` = 27 包，含无测试的） |
| `cd go && go vet ./...` | exit=0 |
| `cd go && gofmt -l .` | 零违规 |
| `cd go && go test -race ./internal/hooks/ -count=1` | ok（7.6s） |
| 工作树 `git status --short` | **clean** |
| 探针残留 `find . -name 'zz_probe*'` | 0 |

**⚠ 已知未复现的失败**：本会话早期曾见 `-race` 3 FAIL，**之后多次重跑未复现，归因未知**。若下个会话遇到，从头查。

---

## 当前卡点

### 卡点 1：`evidenceTracker` 的读取端零消费（TDD gate 拦截未做）

- **状态**：追踪与派生**已就位**（`evidence.go` 全部函数 + 写入端接线），**读取端零生产消费**。
- **已排除**：不是「忘了接线」——`loop.go:196` 注释**显式披露**「无 gate 拦截消费方，拦截是独立的一刀」。这是**有意的范围切分**。
- **待做**：TDD gate 的**拦截**——在 `loop.go` 的 `executeTool`（或合适的门链位置）读 `HasVerificationDebt()`（阈值 3，见 `evidence.go:212`），对「连续 3 次未验证的代码编辑」施加约束。
- **不确定项**：拦截的**具体动作**（拒绝？警告？注入提示？）——需对账 TS 侧 `src/agent/` 的 TDD gate 实现。**未知**，开工前先 grep TS。

### 卡点 2：~~`registry.go` 前置③~~ → **已解决**（第七十四刀）

- **位置**：`go/internal/tools/registry.go` 的「接线条件」注释块，③ 已标 ✅。
- **原问题**：`NeedsApproval` 与 `decideApprovalGate` **各判一次档位**（重复判定）；
  4 处写工具的 `RequiresApproval` 是 `ApprovalMode != skip`，与 TS 的
  `() => true` 不等价。
- **修法**：4 处改为**恒真**（`write_file`/`edit_file`/`hash_edit`/`apply_patch`），
  档位语义**单点**在 `decideApprovalGate`。
- **行为等价性证据**：跨 4 档位的 e2e 全绿（改前改后同结果）；
  新增 2 条 e2e 补上此前缺失的 **skip 档 write_file** 路径
  （`cmd/tianshu/approval_gate_skipwrite_e2e_test.go`）。
- **同时订正**：`internal/tools/tools_test.go` 的 `TestWriteFileRequiresApproval`
  原断言「放开档位下写操作不应需批准」——那是把 Go 侧偏离 TS 的实现固化成了
  断言，已改为「任何档位恒真」。

### 卡点 3：状态表剩余 ⚠️ 项（均判定不做，除非需求出现）

#### ★ 第七十六刀：三条勘探路径的结论（**勿重复勘探**）

「下一步」清空后，用三条独立路径主动找缺口，**全部收敛到「已知且有意」**：

| 路径 | 方法 | 结果 |
|---|---|---|
| ① 未接线标记 | `grep -rnE '未接线\|未注入\|未移植\|悬空' --include='*.go'` | 命中的**全部是有意披露**（如 `askuserquestion.go` 的 `DisplayContent`、`plan.go` 的写工具禁用、`readsection.go` 的 file_path 分支）——每条都写了理由 |
| ② 无测试包 | 逐包查 `*_test.go` | **26/27 有测试**（唯一无测试的 `internal/contract` 是纯类型包） |
| ③ 间歇性失败 | 全量 **×8**（`PLAN.md:1014` 的判据「至少连跑 5 次」） | **8 次全绿**——无时间/浮点/并发类残留偏差 |

**途中被数据纠正的三次误判**（记录，避免下个人重走）：

1. **`CallParams.ReadRefStats` 未注入** → 核实 TS 侧 `params.readRefStats`
   **同样无赋值点**（`types.ts:256` 仅 interface，`read-file.ts` 只读）——
   **两侧一致，忠实移植，非缺口**。TS 的 per-session 遥测实际写在
   `cache-log.jsonl`（`loop-factory.ts:253-257`），而 Go 侧**整个 cache-log
   子系统未移植**（`prefixDiverged` 零命中）——那是子系统级，非同型。
2. **`modelreadcap.go:99` 用 `int()` 而非 `Math.floor`（TS 是 floor）** →
   核实该处 `contextWindow` 已有 `<= 0` 守卫、各乘数恒正——**正数域 `int()` 与
   `Math.floor` 等价，非偏差**。（`profile.go:92` 的注释警告的是**负数域**。）
3. **`default_registry.go:20` 写「完整 51 是分波目标」疑似基准矛盾** →
   `PLAN.md:55` 已准确记录（51 = **full** 基线；minimal 30 / frontend 31），
   **文档无歧义**，是我只读了片段。

**当前工具数**：Go **22**（`grep -cE 'r\.Register\('` 报 23，含循环注册的伪影）。
距 full(51) 的差是**既有分波计划**（`go/HANDOFF.md:6958` 按依赖面从浅到深排了序），
非遗漏。

**结论**：Go 侧当前**无未记录的缺口**。若要继续，从 `go/HANDOFF.md` 的
**「## 建议的第一刀」章节**（用章节名定位，**不要用行号**——该文件持续增长，
行号会漂移；本项目已踩过「同刀内写下的锚点就失效」）取工具清单，
按依赖面从浅到深。

**⚠️ 该章节的推荐已过期**（第七十六刀核实）：它推荐 `related_tests` +
`leave_mark`，而**这两个在第三十七刀（`e6883b66`）就已移植并注册**。
读它时要**逐条核实实际状态**，不要照做。

---

### 卡点 3（原）：状态表剩余 ⚠️ 项（均判定不做，除非需求出现）

| 项 | 称量结论 |
|---|---|
| `protectionMode` | 接线后行为不变 → 不做（`d2246fc8`） |
| `canAutoApprove` | 256 组穷举 0 差异，**完全冗余** → 不做 |
| `unconditionalApproval` | 需先移植 `request_path_access` / `computer_use`，而 Go CLI 无该场景 → 不做 |
| sensorium 链 | 两个直接消费者一个冗余、一个无消费者 → 不做 |
| **`CheckpointDeps.TaskAnchor`** | **造子系统，不是接线** → 不做（第七十五刀称量，详见下） |

#### `TaskAnchor` 的称量（第七十五刀，**勿重复勘探**）

**发现**：`go/PLAN.md:180` 标它是「半落地」的悬空字段。核实后**确认悬空**
（`checkpoint.go` 声明 + 消费点齐全，但 `main.go` 从未注入 → 生产恒 nil）。

**但缺口比文档描述大得多**——它不是「接一个字段」：

- `TaskContract` 在 Go 侧**类型都不存在**（`internal/context/` 无 `taskcontract.go`，
  `grep -riE 'activeContract|taskContract|TurnMode'` 生产代码零命中）。
- TS 侧 `src/context/task-contract.ts` **570 行**（含 `extractTaskContract` 的
  中文启发式正则、`classifyTurnMode`、`advanceContractStatus`、`renderTaskAnchor`）。
- **16+ 个 TS 模块**消费 `TaskContract`（`loop.ts`/`turn-orchestrator.ts`/
  `bootstrap.ts`/`cognitive-ledger.ts`/`plan-task.ts`…）——是 turn 流程的
  **横切基础设施**，不是压缩路径的局部字段。
- **最关键**：它需要**契约生命周期的维护者**（TS 是 `loop.ts` 的
  `this.taskContract`，由 turn 流程维护）——**Go 侧 turn 流程无此概念**。
  **接上去也没有契约可渲染**。

**结论**：强行做就是下一个「声明但无人消费」（`NeedsApproval`/`pressureResult`/
`protectionMode` 的同型）。**判据**：当 Go 侧出现「契约生命周期（task-mode 判定 +
状态推进）」这一需求时，本字段与三个下游一并接。

**已落盘**：`checkpoint.go` 的 `TaskAnchor` 字段注释已标明「有意不接 + 理由 +
何时该做」——下个人读代码即可见，无需再勘探。

### 卡点 4：~~`go/HANDOFF.md` 未记录第六十一刀起~~ → **已解决**（`304f8a51`）

`go/HANDOFF.md` 已回填「第六十一刀起」章节（169 行，覆盖本会话 23 提交）。
**核实方式**：`grep -c '第六十一刀起' go/HANDOFF.md` → 3（标题 + 交叉引用）。

---

## 下一步

**本节已按第七十四刀的核实订正**——原 5 条里 **4 条已失效**（见各条标注）。
仅剩第 4 条仍开放，且文档自己已判「不做」。

1. ~~**TDD gate 拦截**~~ → **已完成**（`abcfc8a7` 第七十二刀接线 + `59c970e6`
   第七十三刀补 suggest 提示通道）。**修正一处原判断**：原文说 suggest 需要
   immune 通道——**错**，TS 的 `tddSuggestNote` 是追加到工具结果尾部
   （`tool-pipeline.ts:1748-1750`），不需新通道。

2. ~~**`registry.go` 前置③**~~ → **已完成**（第七十四刀）。见「卡点 2」。

3. ~~**工具缺口 `related_tests` + `leave_mark`**~~ → **本就已完成**（第三十七刀，
   `e6883b66`）。**本条是过期条目**——原文档引用的 `go/HANDOFF.md:6958` 那段
   自带「过期修正轨迹」（文档自己也警告过）。**核实方式**：
   `ls go/internal/tools/ | grep -iE 'related|leave'` + 确认
   `default_registry.go` 里 `RelatedTests(cwd)` / `LeaveMark()` 已注册。

4. **若要继续认知状态线**（**仍开放，但文档已判「不做」**）：从 `turn-intent`
   **反向切入**（它是唯一有明确外部价值的节点——给用户方向提示），而非从
   sensorium 这个中间层开始造。见 `approval_gate.go` 状态表 E 节。
   **判据**：该需求本身尚未被提出，做出来就是下一个「造了没人用」。

5. ~~**把本会话 20 刀写入 `go/HANDOFF.md`**~~ → **已完成**（`304f8a51`，回填
   第六十一刀起共 23 提交）。

---

## 坑

**绝对不要再踩**——每条一句话说清后果：

### 本会话新增（第十六条起）

16. **批量 sed 替换测试文件会破坏测试意图**——`TestDefaultTimeoutAppliesWhenUnset` 的注释明写「不设 timeoutMs 走默认 5000」，我批量加了 30000 导致它等满 30s 而红。**改测试前逐个看注释里的意图声明**。
17. **hook 超时的 `Output` 曾是空串**（`strings.TrimSpace(out)` 吞掉 `ErrTimeout`）——调用方无法区分失败类型。**任何「失败但 Output 为空」的路径都是缺陷**。
18. **用 `git checkout` 恢复变异测试会丢掉本轮全部修改**——项目规则明令禁止。**改副本文件做变异**（`cp 源 副本` → 改副本 → 测 → 恢复）。
19. **并发负载下 `DefaultTimeoutMs = 5000` 对「起 sh 进程的 hook 脚本」太紧**——`go test ./...` 并行时撞超时（复现率约 1/3）。**超时的指纹是耗时 ≈ 超时值**（5.01s）。
20. **全量失败归因别急着赖「环境污染」**——先看**耗时指纹**与**探针形态**（把超时压到 1ms 复现失败形态，逐字比对）。我上一轮就是误判为 trust 污染。
21. **既有 `approvalrisk/oracle.json` 含平台相关段（Windows 基准）**——在 macOS 上**不可重现**，**不要盲目重跑**。
22. **`session.RecordVerification` 是「同 target 替换」语义**，TS 的 `verifications` 是**追加数组**——直接用会得 1 而非 oracle 期望的 2。**移植前先读语义，别只看签名**。
23. **`observeToolResult` 开头 `if res.IsError { return }` 会挡住 `run_tests` 的失败**（`IsError: exitCode != 0`）——`hasFailedTests` 曾恒 false。**早退守卫要排除「失败也是有效信号」的工具**。
24. **探针必须清理**——本会话用过 `.rivet/scratch/` 与 `zz_probe_*`，交付前 `find . -name 'zz_probe*'` 应返回 0。
25. **「字段/函数存在但零调用者」是本仓库的高频缺陷模式**——`NeedsApproval`、`PermissionConfig.bash`、`TrySessionSplit` 都栽过。**落地新符号后必须 grep 消费方**；反之，声称「已有某能力」前也要 grep 调用点。

### 前序会话的坑（**仍然有效**，Windows 可移植性相关）

1. **手拼 JSON 字符串嵌 Windows 路径 → `\U` 非法转义，JSON 解析失败**。夹具一律走 `json.Marshal`。
2. **`cmd.Wait()` 在孙进程继承管道写端时会阻塞到 EOF → 超时形同虚设**（设 500ms 实测卡 19–20s）。必须设 `cmd.WaitDelay`。
3. **Go `filepath.IsAbs("/etc/passwd")=false` 而 Node `path.win32.isAbsolute`=true → 根相对路径被静默重基进工作区（fail-open 安全洞）**。路径校验必须复刻 Node 语义。
4. **Git Bash 的 `uname` 输出与 Node `os` 模块不一致 → 进冻结前缀的那行字节不等价 → 前缀缓存命中率崩掉**。`<environment>` 行必须走 Win32 API。
5. **Go 自动引号只在含空白时触发 → 不含空白的 `& | ( ^ %` 全裸露**；且**引号挡不住 `%`**。
6. **本机代码页 936（GBK）→ 直读控制台输出乱码**。必须流式解码；`transform.Bytes` 每次重置状态（要持有 `Transformer`）。
7. **TS 的 `String.slice` 按 UTF-16 code unit 计数**——中文场景字节切会截半字符。
8. **Go 的 `json.Unmarshal` 对 `"x": []` 产出非 nil 空切片**（`nil=false len=0`）——判空数组必须用 `len()==0` 且区分 nil。
9. **「红 0 处」有四种成因**，别急着宣布「等价」：①等价变异 ②真测试缺口 ③**编译失败伪装** ④用例集取值点密度不足。
10. **变异反证必须核实变异真的落地**——python 脚本替换可能**静默未生效**。改用 `edit_file`。
11. **断言「不该发生 X」的测试必须验证 X 的可达性**——否则只是恒真断言。
12. **落地新导出符号后必须 grep 消费方**——`TrySessionSplit` 首版是悬空代码，交付报告漏报还错误声称已验收。
13. **实现落地会让「未移植占位」的断言与事实相反**——这类测试必须随之反转或删除，否则 panic 或假红。
14. **交付门禁的「字段无读取方」YELLOW 提示可能是真缺陷**——`ResultSummary` 无消费方暴露了 handoff 失败行漏 summary 段。
15. **`npm install` 会改 `package-lock.json`（3.19.0→3.21.1）→ 不要把它卷进 Go 相关提交**。

---

## 环境事实（供下个会话核对）

- 平台：macOS（Darwin 25.6.0）
- 仓库根：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`
- Go module：`github.com/kalandramo/tianshu/go`（`go/` 子目录）
- Node：24.18.0（`package.json` engines 声明 >=24）
- 分支：`go-runtime` · HEAD：`c7f91de8` · 工作树 clean
- 仓库双 remote：`origin`（私有镜像）、`tianshu`（公开仓库，**绝不直接 push**——历史不同步会被拒；正确流程见项目 `AGENTS.md` 的 `scripts/sync-to-public.sh`）
- `go/internal/` 包列表（27 个，`go list ./...`）：agent api apierr artifact cache client compact config context contract filediff hooks pathsafe platform prompt recovery retry session syntaxcheck tools trust …
- **测试命令**：`cd go && go test ./... -count=1`（本会话基线 26 包 ok / 0 FAIL）

---

## 权威文档索引（按需下钻）

| 想了解 | 读 |
|---|---|
| 每刀的完整技术细节（第一..六十刀） | `go/HANDOFF.md`（**7018 行**，每刀一个专章） |
| 架构欠账清单 | `go/PLAN.md` |
| 审批门的状态表与称量结论 | `go/internal/agent/approval_gate.go` 的 A/B/B'/C/D/E 节（`approval_gate.go:34,108,151,180,204,232`） |
| 本会话的 20 刀 | 本文档 + `git log e866fad8^..c7f91de8` |
| Windows 可移植性的完整记录 | 本文档「坑」1–15 + `go/HANDOFF.md` 前五刀 |
