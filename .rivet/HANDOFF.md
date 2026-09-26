# 交接文档 — Go 重写天枢运行时（macOS 会话 · 审批门 / 认知状态 / hooks / TDD gate / 接线）

> 生成时间：2026-09-26（初版）· 最后更新：2026-09-27 · 设备：macOS（Darwin 25.6.0，作者 moweilong）
> 仓库：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`
> 分支：`go-runtime` · HEAD：`f760cb7b`（+ 第八十刀未提交）· 工作树 **clean**（第八十刀前）
> 本会话共 **33 个提交**（`e866fad8^..f760cb7b`，含起点；第八十刀待提交后为 34），全部在 `go-runtime` 分支
> 本文自包含——读者无需本会话任何上下文。
>
> **更新轨迹**：`8d5c9853`（初版，20 提交，第七十一刀止）→ `f13a73c1`/`5609187d`/`b78a5b4d`/`903e9855`（增量补刀）→ `f760cb7b`（补齐至第七十九刀 + 修头部元数据 + 消矛盾）→ 第八十刀（job 子系统，见「第 5 段」）。

---

## 任务目标

**一句话目标**：把 TypeScript 版天枢运行时（`src/`）**行为等价**地移植成 Go（`go/`），逐刀推进、每刀独立验证。

**本会话实际推进的四条线**（前序会话已完成压缩/session-split 主线，见 `go/HANDOFF.md` 第六十刀）：
1. **审批门接线**——修「配置字段存在但零调用者」导致的 fail-open 静默执行
2. **认知状态子系统称量**——核实「该不该做」，三次判定「不该做」后，第四次找到**真接线缺口**（TDD gate）
3. **hooks 隔离缺陷**——修 `internal/hooks` 全量并发时间歇失败
4. **「实现已有但零消费」接线系列**——TDD gate 拦截 + suggest 通道、Plan Mode（第七十二/七十三/七十九刀）

**非目标**：
- 不重写 TS 版（`src/` 下代码一行未动）。
- 不追求「功能更多」——只追求**与 TS 版行为等价**。任何偏离 TS 语义的「优化」都是缺陷。
- 不碰 `go/` 以外的模块。

**硬约束**：前缀缓存工程是核心指标——冻结 system prompt 必须**字节稳定**。任何进入冻结前缀的字符串（如 `<environment os="...">` 行、handoff 文本）差一个字节就会让缓存命中率崩掉。

---

## 拓扑（先读这段，否则会搞错上下文）

`go-runtime` 分支上，**四段会话接力**：

| 会话 | 设备 | 提交区间 | 主题 |
|---|---|---|---|
| 前序① | Windows（`D:\code\Tianshu-harness`） | 至 `516a226`（2026-09-20） | Windows 可移植性 + 压缩/session-split 主线 |
| 前序② | macOS | `516a226`..`6c6904e9`（483 提交，其中 102 触及 `go/`） | 第三十一刀..第六十刀（volatile / 动态 appendix / terse / plan-mode 等） |
| **本会话** | macOS | `e866fad8^..fde51827`（**32 提交**） | 审批门 / 认知状态称量 / hooks / TDD gate / Plan Mode 接线 |

**关键数字（实测）**：`git rev-list --count 516a226..HEAD` = **483**；`-- go/` 限定 = **102**。

**权威文档**：
- `go/HANDOFF.md`（**7217 行**，每刀一个专章）——覆盖到**第七十六刀**（`b78a5b4d` 回填「第六十一刀起」章节，169 行）。**第七十七..七十九刀尚未写入**（那三刀只写进了本文档）。
- `go/PLAN.md`——架构欠账清单。

**注意**：本文档路径是 `.rivet/HANDOFF.md`，**被 git 跟踪**。旧版（20584 字节，Windows 会话的 16 刀详细记录）可从 git 取回：

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
- **⚠ 读取端零消费（有意披露）——当时状态**：`GateState()` / `HasVerificationDebt()` 在**生产代码零引用**（仅测试引用）。`loop.go:231` 的注释**当时**写了「目前**无 gate 拦截消费方**——本刀只建立追踪与派生，拦截是独立的一刀（避免造无消费者的门）」。**这不是缺陷，是显式披露**。
  > **后续**：第七十二刀（`abcfc8a7`）已接线拦截、第七十三刀（`59c970e6`）已补 suggest 通道，`loop.go:231` 的注释也已随之订正。**读本节请配合「第 4 段」**——本节是当时的快照，不是现状。

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

### 第 4 段：接线系列——修「实现已有但零消费」（第七十二..七十九刀）

**共性根因**（与第 1 段同型，但更隐蔽）：机制**已完整实现且有 oracle 测试**，
但**生产代码零消费**——不是「忘了写」，而是「写了没接」。判缺口**不能只看文件存在**，
要 grep **消费点**（排除定义文件与测试）。

#### `abcfc8a7` 接线 TDD gate——修「追踪有、拦截无」（第七十二刀）

- **缺口**：`evidence.go`（第 2 段 `1b9c8ed8` 建立）的 `GateState()`/`HasVerificationDebt()`
  **生产零引用**（`loop.go:231` 注释显式披露「无 gate 拦截消费方」）。
- **新增** `go/internal/agent/tddgate.go`（**257 行**）：`EvaluateTddGate`（8 分支顺序敏感）
  + `parseTddGateEnv` + 4 段文案**逐字对账** TS。
- **接线**：`loop.go` 门链末尾 + `main.go` 装配层（`cmd/tianshu/main.go`）。
- **oracle**：`go/testdata/tddgate/oracle.json`（**33 用例**：allow 13 / suggest 10 / block 10），
  从 TS 真实执行生成。**抓到手写必错的差异**：`src/foo_test.go` 在 enforce 下是 **block**
  而非 suggest——TS 的 `isTestFile`（`tdd-gate.ts`）与 `evidence.go` 的 `testFileRe`
  是**两个不同正则**。
- **失误记录（重要教训）**：M1 变异首版**红 0**——只断言 `IsError`，而空 registry 下
  放行也返回 `IsError=true`（「工具未找到」）。修法：断言**命中文案** + 负面用例加**阳性对照**。
  M7 红 0 暴露**注入设计缺陷**：初版 `TddGateEnv` 为空时回退读 `os.Getenv`，让环境变量
  **绕过注入层**（「装配层漏填」测不出）→ 改为 Config 唯一来源。
- **提交信息里的数字错了**：写了「41 用例」，实测 33——下个提交 `6ca114da` 纠正。

#### `59c970e6` 补 TDD gate 的 suggest 提示通道（第七十三刀）

- **推翻上一刀的误判**：TS 的 `tddSuggestNote` **不走 immune 通道**，而是**追加到工具结果尾部**
  （`tool-pipeline.ts:1748-1750`）。immune 通道只属 `buildTddGateHint`（每轮边界提示）。两者互补。
- **实现要点**：只在「enforce 会拦」区域贴（`hasFailedTests || edits >= threshold`）、
  只在成功时贴、格式 `\n\n[TDD] `、时机在内部记录**之后**（内部用原始 content）、
  用**编辑前**快照。**缓存友好**：追加尾部 = 冻结前缀不变。
- **新教训**：M1 变异首版红 0 是**编译失败伪装**——删代码块让局部变量「声明未使用」
  → build failed → `grep -c '^--- FAIL'` 返回 0（测试没跑）→ 误判为覆盖缺口。
  修法：用 `_ = 变量` 保留声明。

#### `f13a73c1` 写工具 `RequiresApproval` 恒真（第七十四刀）

- **称量**：TS 是 `() => true`（4 处），Go 是 `ApprovalMode != skip`。`decideApprovalGate`
  开头已处理 skip、auto-safe 读 `isHighRisk`——故只在 manual 档被消费，**行为等价**
  但**档位语义两处判定**（结构隐患）。
- **实现**：4 处改恒真（`write_file`/`edit_file`/`hash_edit`/`apply_patch`），
  单点收敛到 `decideApprovalGate`。
- **订正既有测试**：`tools_test.go` 的 `TestWriteFileRequiresApproval` 断言旧行为
  （把 Go 侧偏离 TS 的实现固化成断言）→ 改为「任何档位恒真」。
- **诚实标注**：M3（移除 skip 短路）红 0 = **等价变异**（skip 档不在 `switch` 里，
  走末尾兜底，行为不变）。**顺带发现既有隐患**：末尾兜底是「**未知档位放行**」。
- 新增 `cmd/tianshu/approval_gate_skipwrite_e2e_test.go`（139 行，补此前缺失的 skip 档 write_file 路径）。

#### `5609187d` TaskAnchor 称量——**判定不做**（第七十五刀）

`go/PLAN.md:180` 标它「半落地」。核实后**缺口比文档大得多**：`TaskContract` 在 Go 侧
**类型都不存在**；TS 的 `task-contract.ts` **570 行**；**16+ 个模块**消费它；
**决定性判据**——它需要**契约生命周期的维护者**（TS 是 `loop.ts` 的 `this.taskContract`），
**Go 侧 turn 流程无此概念**。接上去也没契约可渲染 → 下一个「声明但无人消费」。
落盘到 `checkpoint.go` 字段注释 + 本文档。

#### `b78a5b4d` 三条勘探路径（第七十六刀）

用户第三次要求「推进」。**换方法**——三条独立路径找缺口：
① grep 未接线标记（命中的**全是有意披露**）② 逐包查测试（**26/27 有测试**）
③ 全量 **×8**（**8 次全绿**）。**全部收敛到「已知且有意」**。
**途中三次误判被数据纠正**：`ReadRefStats` 未注入（TS 侧同样无赋值点，忠实移植）、
`modelreadcap.go` 用 `int()` 而非 `Math.floor`（正数域等价）、
`default_registry.go:20` 疑似基准矛盾（`PLAN.md:55` 已准确记录）。
**订正**：`go/HANDOFF.md` 的「建议的第一刀」推荐 `related_tests`+`leave_mark`——
**第三十七刀就已做**（逐个核实 6 项全已注册）。

#### `d4227ba5` 移植将星账本（第七十七刀）——**本段唯一新增工具**

- **筛选方式改变**：不看文档推荐，按**依赖面**找。`recall_general` + `record_general_finding`
  依赖只有 `node:fs` + 星域表。
- **新增** `go/internal/context/generalledger.go`（**344 行**，对账 TS 12 个导出）
  + `go/internal/tools/generalledger.go`（**178 行**）。
- **星域表最小化**：TS 的 `star-domain-data.ts` 600+ 行，但 `starToGeneralSlug`
  **只需 id + name 两列**——只提取 **16 条**映射，不拖入无关认知资产（漂移风险已声明）。
- **注册位置核实**：TS 侧不在 `default-registry.ts` 而在 `bootstrap.ts:682-683`
  （受 preset 门控，minimal 档不含）——**Go 侧无 preset 机制**，故无条件注册（差异已注释）。
- **验证**：oracle **42 slug + 4 path + 8 parse** 逐值；工具级 8 条；**变异反证 5 个全红**；
  工具数 **22 → 24**（显式工具）。
- 后续 `289aedf5` 补 `generatedBy` 断言——交付门禁 YELLOW（**0 读取方**）是**真缺口**：
  声明了字段却从不断言，而既有 `planmode_test.go:55` **确实断言**它（防 oracle 被覆盖）。

#### `903e9855` 订正 5 处过期注释（第七十八刀）

**系统性发现**：连续**四次**按文件头注释判缺口，核实后**全是注释过期**——
`readsection.go` 的 file_path 分支与 compact-history 快速路径（都已实现）、
`codefold.go` 的 `applyFoldThenPartial`（`readpayload.go:36`）、
`modeloutput.go` 的 `persistRawOutput`（`bash.go:311`/`diff.go:247` 都传了）、
`context_collapse.go` 的 artifact store（`internal/artifact/` 已存在）。
**同时核实三处注释准确**（避免夸大）：`plan.go` 写工具禁用、`bash.go` 的 `runInBackground`、
`readdedup.go` 的 `sliceFromArtifact`。
**根因**：注释写于「本刀」，后续刀补齐时不会回头改前人注释——「未移植」断言**天然短时效**。

#### `fde51827` 接线 Plan Mode（第七十九刀）——**本段最后一刀**

- **缺口**：`internal/agent/planmode.go` 的 `CheckPlanMode`（5 段分支，oracle 对账过）
  **生产零消费**——`Loop` 无状态字段、门链不调它。后果是 `plan` 工具的
  `enter_mode`/`exit_mode` 只能走 `planModeUnsupported` **明确报错**，文案写
  「Go 侧**尚未移植**写工具禁用机制」——**但机制其实早已实现**。
- **接线三者缺一不可**：
  1. **状态**：`Loop.PlanModeState` + `ActivePlanFilePath`（对账 TS `loop.ts:261`）。
     **放 Loop 而非 Config**——会话可变状态（由工具运行中改写）。
  2. **门链**：`executeTool` 最前（deny 门**之前**，对账 TS 顺序 plan-mode(1062) → deny(1137)）。
  3. **入口**：`CallParams.EnterPlanMode`/`ExitPlanMode` 回调 + `Loop.enterPlanMode`/
     `exitPlanMode`（真建草稿文件 + 置状态）+ `plan` 工具接线（替换原报错）。
- **为什么入口不可省**：只接门链而入口不置状态 → `PlanModeState` 永远 `off`
  → **门是死代码**。这是本仓库栽过的「造了没人用」。
- **验证**：门链 5 条 + 工具 6 条 + **端到端闭环 1 条**（`enter_mode` → 门拦写工具
  → 写活动计划文件放行 → `exit_mode` → 写工具恢复放行，含草稿文件真被创建）。
  **变异反证 4 个全红**（M1 移除门链红 2 / M2 enter 不置状态红 1 / M3 工具回退成报错红 4 /
  M4 exit 不清状态红 1）。
- **必须记录的判断：订正了一条既有测试**。`plan_test.go` 的
  `TestPlanEnterExitModeHonestError` 断言 `enter_mode`/`exit_mode` **恒报错**且文案含
  「暂不支持」——它把「机制未移植」的**临时状态固化成了断言**。判为**过期断言**而非有效保护：
  新实现**保留了 fail-closed**（无回调时仍报错，对账 TS「子代理不能切主代理的计划模式」），
  只是文案从「暂不支持」变为「上下文不可用」。已订正断言（保留 fail-closed 检查）。
- **诚实标注（Go 侧的有意收窄）**：TS 的 `enterPlanMode` 还做三件事，Go 侧**未做**
  （子系统不存在或未接线）：① Ask Mode 互斥；② `promptEngine` 同步；③ 调研 advisory 注入。
  TS 的 `delegatesWriteCapableProfile` 依赖 profile registry（`profileIsPlanModeSafe`）
  ——Go 侧未移植，故恒 false（该分支不触发）。

### 第 5 段：job 子系统——修「参数存在但静默忽略」（第八十刀）

**背景**：Go 侧 `bash` 的 `run_in_background` 参数**一直存在但被静默忽略**。
第二十五刀（`go/HANDOFF.md:1973-2028`）把它降级为**显式声明**，并登记了一条
**含移除条件**的 schema 偏离（「job 子系统移植后恢复 TS 原文案并从白名单删除」）。
**本刀满足该移除条件**。

**为什么是真缺口（不是忠实移植）**：TS 的 `AgentLoop` **自己创建** `SessionJobs`
（`loop.ts:850`，条件 `if (config.sessionId)`）——所以 **CLI 交互模式也真的会后台化**。
TS `bash.ts:487-488` 的注释「Requires a session job registry (server / TUI with
sessionId)」容易被误读为「只有 server/TUI 才有」，但 `loop.ts` 的创建点证明 CLI 也有。

**新增**：
- `internal/tools/jobstore.go`（约 900 行）——`SessionJobs` + `backgroundJob`，
  对账 TS `job-store.ts`（411 行）。含输出环（64KB）/ 节流（500ms）/ 三态 await
  （命中/退出/超时）/ 终态淘汰（上限 50，running 永不淘汰）/ 墙钟上限
  （`RIVET_JOB_MAX_MS`）/ await 心跳。
- `internal/tools/job.go`（约 300 行）——`job` 工具（list/await/logs/kill），
  对账 TS `job-tool.ts`（120 行）。**所有输出文案逐字对账**（进模型上下文）。
- `internal/tools/jobstore_test.go`（22 条）+ `job_test.go`（20 条）
  + `internal/agent/job_wiring_test.go`（9 条端到端）。

**接线（三处，缺一不可）**：
1. `Loop.Jobs` 字段 + `New()` 在 `cfg.SessionID != ""` 时创建（对账 TS 的创建条件）。
2. `CallParams.Jobs` + `buildToolCallParams` 注入（**必须经 `jobRegistryOrNil`**，见下）。
3. `bash.go` 后台分支（含 `IsLongRunner` 自动检测，对账 TS `bash.ts:452-466`）
   + `FlushSession` 调 `KillAll`（防孤儿）。

**踩的坑（三条，都有实测证据）**：
1. **`cmd.Stdout = writer` 必须在 `cmd.Start()` 之前**——Go 在 Start 时建立管道，
   Start 之后再赋值**被忽略**（实测：`tail=""`，输出全丢）。
2. **`await` 的 regex 忘了挂到 waiter 上**——局部变量 `regex` 不赋给 `w.regex`，
   `onData` 的 `w.regex != nil` 恒 false，**永不匹配**（实测：`matched=false` 但
   `tail="Ready\n"`——输出到了却不命中）。**测试正确抓住了这个真 bug**。
3. **`-race` 抓到真 data race**：`await` 在**锁外**写 `w.timer` 与 `w.waiters`，
   而 `onData`/`onExit` 在锁内遍历它们。修法：整个 waiter 注册（含 timer 创建）
   纳入同一临界区。**这是 `-race` 的价值——全量测试（无 -race）是绿的**。

**typed-nil 陷阱（值得单独记）**：`Jobs: l.Jobs` 当 `l.Jobs == nil` 时产生
**typed-nil**（接口非 nil、底层指针 nil）——消费侧 `p.Jobs != nil` 通过，
随后 `s.mu.Lock()` 解引用 nil **panic**（实测：无会话的 bash 后台调用崩溃）。
修法：`jobRegistryOrNil()` 显式返回真 nil 接口。

**订正既有测试（反转而非删除）**：`bash_run_in_background_decl_test.go` 断言
「描述应说明未实现」——它把「机制未移植」的**临时状态固化成了断言**。本刀反转
为「断言已实现」，并**保留否定语境的检查**（旧版是「**不**返回 job id」，
新版是「返回 job id」——两版必须区分，否则回退会静默通过）。

**验证**：42 条单测 + 9 条端到端；**变异反证 6 个全红**（M1 移除后台分支红 5 /
M2 typed-nil 红 3 / M3 kill 不透传红 2 / M4 忽略显式 false 红 1 / M5 isLongRunner
恒 false 红 1 / M6 schema 回退红 2）；全量 ×2 绿（26 包）；`-race` 两包绿；
vet/gofmt 干净；工具数 **25 → 26**。

**诚实标注**：
- TS 的 `spawnShell`（Windows 作业持有者 `job-launch.exe`，issue #144 修复）**未移植**
  ——依赖一个未随仓库分发的原生二进制。Unix 上等价（进程组语义）；Windows 上
  与 Go 侧 bash 工具**一致**（同为无 helper 路径）。
- TS 后台分支的 `tryAcquireAdhocLock`（typecheck 串行锁）与 `buildMirrorEnv`
  （mirror 环境叠加）**未做**——Go 侧无这两套子系统（`getResolvedEnv` 在
  `spawngit.go` 已声明未移植）。属**有意收窄**，不影响 job 语义。
- await 心跳上报（`touchActivity`）**传 nil**——Go 侧无 stall-observer 对应物。

**已排除的候选（勿重复勘探）**：文档「下一步」列的 `update_goal` / `session_vitals` /
`semantic_search` 三个工具，我逐个核实其依赖在 Go 侧的命中数——**全部为 0**
（`GoalTracker` 331 行 + 12 模块消费 / `RuntimeSelfModel` 231 行 /
`semantic-index` 464 行 + embedding provider）。**确为造子系统**，文档判断正确。
另核实 `diff` / `plan_close` / `did-you-mean` / `syntax-check` **均已实现**
（前三者是别名或内部库，非独立工具）。

### 第 6 段：`request_path_access` + 修「授权形同虚设」的既有缺陷（第八十一刀）

**称量订正**：文档此前判它「不做」（理由：「需先移植 `request_path_access` /
`computer_use`，而 Go CLI 无该场景」）。**这条把两件事混在一起，且后半句是错的**：
Go CLI **有**工作区外读写场景——`pathgrants.go` 存在、门链的 pathGrant 门
**已接线**（`approval_gate.go` 状态表标 ✅）。缺的只是**主动申请**的入口。

**缺口**：门链在**非 skip 档**遇到工作区外路径时**直接拒绝**（注释写明「无提示通道」）。
模型没有主动申请授权的入口 → 无法做目录级 / 批量 / bash 场景的授权。

**新增**：
- `internal/tools/requestpathaccess.go`（235 行）——`request_path_access` 工具，
  对账 TS `request-path-access.ts`（104 行）。含 `isForbiddenGrantRoot`
  （issue #117 的系统目录黑名单，**纯函数**）+ 敏感文件检测 + `~` 展开。
- `internal/tools/requestpathaccess_test.go`（24 条）+ `internal/agent/requestpathaccess_wiring_test.go`（7 条端到端）。

**称量的收益不止于新工具**：`RequiresUnconditionalApproval`（`approval_risk.go:380`）
**早已实现且已接线**，且**已含 `request_path_access` 分支**——但此前**没有这个工具**，
故那分支是**死代码**（`approval_gate.go` 的注释明写「分支实际不可达」）。
**本刀让那条已接线的判定第一次有真实消费者**。

#### ★ 途中发现的**既有跨模块缺陷**：授权形同虚设

修端到端测试时发现：**授权后写工具仍拒绝**。根因（三层）：

1. **两个实例**：写/读工具的 `Grants` 是**构造时**绑定的
   （`NewDefaultRegistry` 的参数），而会话的授权存储由 `Loop.New()` 创建。
2. **装配层根本没传**：`cmd/tianshu/main.go` 的
   `tools.NewDefaultRegistry(tools.Options{Cwd: ...})` **没传 `Grants`**（零值 nil）。
3. **`Loop.pathGrants` 私有无访问器**：装配层拿不到它对不齐。

后果：门链（用 `l.pathGrants`）授了权、放行了，但工具内部的 `pathsafe.Validate`
用 nil grants → **永远拒绝**。**skip 档的「首触即授」授了权也没用**——那是死路径。

**对账 TS**：TS 的 `isWriteGranted`（`path-grants.ts:181`）读**包级单例** `_grants`
——所有工具共享同一状态、**无构造时注入**。Go 侧做成了构造时字段，这是**移植偏差**。

**修法（最小化，向后兼容）**：
- `CallParams.Grants` 新字段（会话级，随会话变化）。
- `Loop.buildToolCallParams` 注入 `l.pathGrants`（经 `grantCheckerOrNil` 防 typed-nil）。
- `effectiveGrants(p, fallback)` helper：**优先 `p.Grants`**、回退构造时字段。
- 8 处 `pathsafe.Validate` 调用点改用 helper（`file_tools.go`×2 / `applypatch.go`×2 /
  `hashedit.go`×1 / `read_file.go`×2 / `readsection.go`×1——后者需把 `p` 传进
  `readFromDisk`）。

**验证**：24 条单测 + 7 条端到端；**变异反证见下**；全量 ×2 绿（26 包）；
`-race` 两包绿；vet/gofmt 干净；工具数 **26 → 27**。

**诚实标注**：
- **持久化未移植**：TS 的 `grantPath(..., {persist: remember})` 支持跨会话持久化，
  Go 侧 `agent.GrantPath` **无 persist 参数**（`pathgrants.go:18-23` 已声明）。
  故 `remember=true` 时**明示降级**为「仅本会话」——**不得**谎称已持久化。
- **跨包类型独立**：`tools.GrantMode` 与 `agent.GrantMode` 是**独立定义**
  （字符串值一致）——因为 `agent` 已依赖 `tools`，反向 import 会成环。装配层做一次转换。
- **`computer_use` 仍未移植**——`RequiresUnconditionalApproval` 的另一分支
  （`js_eval`/`browser_adopt`/`sequence`）仍不可达。

#### ★ 第八十一刀的**独立审查**（自动审查超时未跑，故补做）

`e315e126` 的自动审查**超时未运行**（advisory 记录）。这是本会话最大的一刀
（13 文件 1140 行），故用对抗式 verifier 补审。**它推翻了/补充了以下四点**：

**① 假绿（HIGH，已修）**：`requestpathaccess_wiring_test.go` 与
`acceptance_requestpathaccess_test.go` 都在 **skip 档**验证「授权后落盘」。
但 skip 档门链对 `write_file` **首触即授** → **即使工具是空操作也会落盘**。
**独立复现**：skip 档下**完全不调** `request_path_access`，写工作区外文件仍落盘。
→ 我上一轮声称的「验收 PASS」含**假绿成分**（`effectiveGrants` 修复部分有效，
`request_path_access` 工具本身的贡献**不可归因**）。
**修**：新增 `grant_attribution_test.go` —— 用 **auto-safe 档**（无路径自动授予、
写工具放行）做归因：B1 未授权被拦 → B2 授权 → B3 落盘，**唯一归因于该授权**。

**② 授权面不完整（MEDIUM，已修）**：`fileinfo.go` / `diff.go` /
`file_tools.go`(glob) / `grep.go` 四处 `pathsafe.Validate(..., nil)` **硬传 nil**
——已授权的工作区外目录对它们仍被拒，与 `request_path_access` 的成功文案
「文件工具与 bash 现在可以在此读写路径」**不符（过度承诺）**。
**修**：四处接 `effectiveGrants(p, nil)`（这 4 个工具无构造时 grants）。
→ 全仓 `pathsafe.Validate` 调用点 **12 处全部接上**。
**验证**：`TestGrantAttributionGlobGrepSeeGrants`（①未授权拦 → ②授 → ③放行命中）。

**③ 状态表被证伪（MEDIUM，已订正）**：`approval_gate.go` 的状态表仍称
`request_path_access` 未注册、`unconditionalApproval` 分支「实际不可达」。
本刀注册后该分支**已可达**（manual 经 `needsApproval`、auto-safe 经 `isHighRisk`）。
**修**：订正该表 + 补「Go 侧与 TS 的行为差异」说明（见 ④）。

**④ 档位语义差异（HIGH，**判定为既有架构缺口，非本刀缺陷**）**：
`request_path_access` 在 manual/auto-safe 档被**硬拒**，只在 skip 档可用。
核实 TS 侧（`tool-pipeline.ts:1213-1343`）：`shouldAsk=true` 后走
`onApprovalRequired` **弹审批**，用户批准后 `grantPath(...)` 授权。
**Go 侧无交互审批通道** → 硬拒。语义是「不可用」而非「待批准」。
**这是既有的架构缺口**（Go CLI 无弹窗），非本工具引入；
且 `RequiresUnconditionalApproval` 对它的恒 true 判定**与 TS 一致**（忠实移植）。
**诚实标注**：该工具在 Go 侧**实际只在 skip 档可用**，而 skip 档本身已对文件工具
首触即授——**它的独立价值被削平**。已记入下条「遗留」。

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

**第 4 段（接线系列）新增**：

```
go/internal/agent/tddgate.go                       (257 行)
go/internal/agent/tddgate_oracle_test.go
go/internal/agent/tddgate_wiring_test.go
go/internal/agent/tddgate_suggest_test.go
go/cmd/tianshu/tddgate_e2e_test.go                 (250 行)
go/testdata/tddgate/gen-oracle.ts + oracle.json    (33 用例)
go/internal/context/generalledger.go               (344 行)
go/internal/context/generalledger_oracle_test.go
go/internal/tools/generalledger.go                 (178 行)
go/internal/tools/generalledger_tools_test.go
go/testdata/generalledger/gen-oracle.ts + oracle.json  (42 slug + 4 path + 8 parse)
go/cmd/tianshu/approval_gate_skipwrite_e2e_test.go (139 行)
go/internal/tools/writeapproval_test.go            (106 行)
go/internal/agent/planmode_wiring_test.go          (227 行)
go/internal/tools/planmode_tools_test.go           (139 行)
```

**第 5 段（job 子系统）新增**：

```
go/internal/tools/jobstore.go                      (~900 行，SessionJobs + backgroundJob)
go/internal/tools/jobstore_test.go                 (22 条)
go/internal/tools/job.go                           (~300 行，job 工具)
go/internal/tools/job_test.go                      (20 条)
go/internal/agent/job_wiring_test.go               (9 条端到端)
```

### 验证基线（末次**真实工具输出**，`fde51827` 时点）

| 命令 | 结果 |
|---|---|
| `cd go && go test ./... -count=1` | **26 包 ok、0 FAIL**（`go list ./...` = 27 包，含无测试的） |
| `cd go && go vet ./...` | exit=0 |
| `cd go && gofmt -l .` | 零违规 |
| `cd go && go test -race ./internal/agent/ ./internal/tools/ -count=1` | 两包均 ok（3.5s / 15.4s） |
| 工具数（`internal/tools/default_registry.go` 显式 `Register`） | **25**（+1 处循环注册 `r.Register(t)`；第八十刀后为 26） |
| 工作树 `git status --short` | **clean** |
| 探针残留 `find . -name 'zz_probe*'` | 0 |

**⚠ 已知未复现的失败**：本会话早期曾见 `-race` 3 FAIL，**之后多次重跑未复现，归因未知**。若下个会话遇到，从头查。

**⚠ 工具数口径**：`grep -cE 'r\.Register\('` 会数到 **25**（含 `r.Register(t)` 循环行）。
**24 个显式工具** + 1 处循环注册（`opts.Extra` 遍历）。旧文档写 22 是第七十七刀之前的数。

---

## 当前卡点

### 卡点 1：~~`evidenceTracker` 的读取端零消费~~ → **已解决**（第七十二/七十三刀）

> **⚠ 本节原文已废，保留以示修正轨迹。** 原写于 `8d5c9853`（第七十一刀时点），
> 当时 `evidenceTracker` 的读取端确实零消费。**第七十二刀（`abcfc8a7`）已接线拦截**，
> **第七十三刀（`59c970e6`）已补 suggest 提示通道**。本节与「下一步」第 1 条
> **曾经互相矛盾**——那是增量补刀时只改了一处留下的疤痕，本次订正。

**原记录（历史）**：`evidence.go` 的 `GateState()`/`HasVerificationDebt()` 生产零引用，
`loop.go:231` 注释显式披露「无 gate 拦截消费方」——那是**有意的范围切分**（避免造无消费者的门）。

**现状**：`internal/agent/tddgate.go` 的 `EvaluateTddGate` 在 `loop.go` 门链末尾消费它
（allow / suggest / block 三态）。阈值 3、文案逐字对账 TS、oracle 33 用例。详见「第 4 段」。

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

### ★ 第七十八刀：**文件头注释落后于实现**（系统性发现，勿按注释判缺口）

本会话连续**四次**按 Go 侧文件头注释判定「缺口」，grep 核实后发现**注释过期**：

| 注释声称 | 实测 |
|---|---|
| `readsection.go` 头：「未移植 file_path 分支」 | **已实现**——`readFromDisk`（L323），含 mtime 告警/大文件守卫/截断 |
| `readsection.go` 头：「未移植 compact-history 快速路径」 | **已实现**——L235-247 + 专属测试 `readsection_compacthistory_test.go` |
| `codefold.go:18`：「`applyFoldThenPartial` 未移植」 | **已实现**——`readpayload.go:36` 的 `ApplyFoldThenPartial` |
| `modeloutput.go:53`：「`persistRawOutput` 未移植，调用方传空串」 | **已实现**——`bash.go:311` / `diff.go:247` 都传了 `RawPath` |
| `compact/context_collapse.go:33`：「artifact store 未移植」 | **已存在**——`internal/artifact/` 全套 + 生产端已接线 |

**但也核实了三处注释是准确的**（不是普遍现象）：
`plan.go` 的写工具禁用机制、`bash.go` 的 `runInBackground`（`jobRegistry` 全库零命中）、
`tools/readdedup.go` 的 `sliceFromArtifact`。

**教训（写给下个人）**：**判缺口前先 grep 实现，别照注释**。
文件头注释写于「本刀」，而后续刀补齐实现时**不会回头改前人的注释**——
故注释的「未移植」断言**天然短时效**。核实方式：
```
grep -rn "函数名" go/internal/ --include="*.go" | grep -v _test
```

**已订正**：上述 5 处注释均加了「第七十八刀订正」段（保留原文以示修正轨迹）。

---

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
| `unconditionalApproval` | ~~需先移植 `request_path_access` / `computer_use`，而 Go CLI 无该场景~~ → **第八十一刀订正：判断有误**。`request_path_access` 已移植（本刀），该判定**第一次有真实消费者**；「Go CLI 无该场景」是错的——工作区外读写场景存在（`pathgrants.go` 与门链的 pathGrant 门都已接线）。`computer_use` 仍未移植 |
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
   第六十一刀起共 23 提交）。**注**：`go/HANDOFF.md` 现覆盖到**第七十六刀**
   （`b78a5b4d` 追加 34 行）——**第七十七..七十九刀只在本文档**（`go/HANDOFF.md`
   未记）。若要两边齐平，需把第七十七..七十九刀补进 `go/HANDOFF.md`。

6. **接线系列的候选（第七十九刀后）**：本会话已修完**四条**「实现已有但零消费」
   （`NeedsApproval` → 审批门、`CheckPlanMode` → Plan Mode、`evidenceTracker` 读取端
   → TDD gate、`RequiresApproval` 重复判定 → 单点）。**剩余的候选工具**
   （`update_goal` / `session_vitals` / `semantic_search`）经第七十五刀的判据
   （「Go 侧有无该状态的**维护者**」）**全部是造子系统**，不适合推进。
   **判缺口的方法**（已验证有效）：`grep -rn "函数名" go/internal/ --include="*.go" | grep -v _test`
   ——排除定义文件与测试后若零命中，才是真缺口；**不要照文件头注释判**（第七十八刀教训）。

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

### 本会话第 4 段新增（第 26 条起）

26. **「红 0」有三种假象**（第七十二/七十三刀各踩一次）：①**等价变异**（`skip` 档不在 `switch` 里走兜底，行为不变）②**真覆盖缺口**（只断言 `IsError`，而空 registry 下放行也返回 `IsError=true`）③**编译失败伪装**（删代码块 → 局部变量「声明未使用」→ build failed → `grep -c '^--- FAIL'` 得 0，**测试根本没跑**）。**修法**：负面用例加**阳性对照**；核实变异**落地且编译通过**；删代码时用 `_ = 变量` 保留声明。
27. **注入层若回退读 `os.Getenv`，环境变量会绕过注入**——第七十二刀 `TddGateEnv` 初版如此，「装配层漏填」测不出。**Config 必须是唯一来源**。
28. **接线「状态 + 门 + 入口」三者缺一不可**（第七十九刀）——只接门链而入口不置状态 → 状态永远 `off` → **门是死代码**。验证必须走**端到端**（enter → 门拦 → exit → 放行），单测门函数绿不等于接线绿。
29. **实现落地会让「未移植占位」的断言与事实相反**（第七十九刀）——`TestPlanEnterExitModeHonestError` 断言 `enter_mode` **恒报错**，那是把「机制未移植」的**临时状态固化成断言**。**改测试前先判**：它是有效保护（fail-closed 语义要保留）还是过期断言（临时状态要撤销）？第七十九刀的答案是**两者都占**——保留 fail-closed 检查、撤销「暂不支持」文案断言。
30. **本会话两处自相矛盾都是「增量补刀只改一处」留下的疤痕**——①文档内卡点 1 说「TDD gate 未做」vs 下一步 1 说「已完成」；②`loop.go:231` 注释说「目前无 gate 拦截消费方」vs `loop.go:1191` 说「第七十二刀补了拦截」。**同一事实写两处时，改一处必须 grep 另一处**（`grep -rn "无 gate 拦截消费方" go/`）。

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
13. **实现落地会让「未移植占位」的断言与事实相反**——这类测试必须随之反转或删除，否则 panic 或假红。**（第七十九刀给了具体判据，见坑 29）**
14. **交付门禁的「字段无读取方」YELLOW 提示可能是真缺陷**——`ResultSummary` 无消费方暴露了 handoff 失败行漏 summary 段。
15. **`npm install` 会改 `package-lock.json`（3.19.0→3.21.1）→ 不要把它卷进 Go 相关提交**。

---

## 环境事实（供下个会话核对）

- 平台：macOS（Darwin 25.6.0）
- 仓库根：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`
- Go module：`github.com/kalandramo/tianshu/go`（`go/` 子目录）
- Node：24.18.0（`package.json` engines 声明 >=24）
- 分支：`go-runtime` · HEAD：`fde51827` · 工作树 clean
- 仓库双 remote：`origin`（私有镜像）、`tianshu`（公开仓库，**绝不直接 push**——历史不同步会被拒；正确流程见项目 `AGENTS.md` 的 `scripts/sync-to-public.sh`）
- `go/internal/` 包（27 个，`go list ./...` 实测）：`cmd/tianshu` + `internal/{agent,api,api/sse,api/stablejson,api/wire,apierr,artifact,cache,client,compact,config,context,contract,filediff,hooks,pathsafe,plan,platform,prompt,recovery,retry,session,skills,syntaxcheck,tools,trust}`
- **测试命令**：`cd go && go test ./... -count=1`（基线 26 包 ok / 0 FAIL）

---

## 权威文档索引（按需下钻）

| 想了解 | 读 |
|---|---|
| 每刀的完整技术细节（第一..七十六刀） | `go/HANDOFF.md`（**7217 行**，每刀一个专章） |
| 架构欠账清单 | `go/PLAN.md` |
| 审批门的状态表与称量结论 | `go/internal/agent/approval_gate.go` 的 A/B/B'/C/D/E 节（`approval_gate.go:34,108,151,180,204,232`） |
| 本会话的 32 提交 | 本文档 + `git log e866fad8^..fde51827` |
| 第七十七..七十九刀（`go/HANDOFF.md` **未记**） | 本文档「第 4 段」 |
| Windows 可移植性的完整记录 | 本文档「坑」1–15 + `go/HANDOFF.md` 前五刀 |
