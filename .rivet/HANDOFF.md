# 交接文档 — Go 重写天枢运行时（macOS 会话 · 审批门 / 认知状态 / hooks / TDD gate / 接线）

> 生成时间：2026-09-26（初版）· 最后更新：2026-09-27 · 设备：macOS（Darwin 25.6.0，作者 moweilong）
> 仓库：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`
> 分支：`go-runtime` · HEAD：`029a289e` · 工作树 **clean**
> 本会话共 **48 个提交**（`e866fad8^..029a289e`，含起点），全部在 `go-runtime` 分支
> **最新段**：第 11 段（undo 快照层 + undo 工具，第一百零二刀，工具数 **41 → 42**）
> 本文自包含——读者无需本会话任何上下文。
>
> **更新轨迹**：`8d5c9853`（初版，20 提交，第七十一刀止）→ `f13a73c1`/`5609187d`/`b78a5b4d`/`903e9855`（增量补刀）→ `f760cb7b`（补齐至第七十九刀 + 修头部元数据 + 消矛盾）→ 第八十刀（job 子系统）→ 第八十一刀（`request_path_access` + 修授权形同虚设）→ **第八十二..九十七刀（工具移植 + web_search 全包，见「第 7 段」）**。

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

#### 第八十一刀审查的**未结项**处理（LOW/MEDIUM 两条）

上条记了审查的 4 项发现（假绿/授权面/状态表/档位语义）。另有 2 条**未结项**，
本轮处理完毕：

**⑤ `mode` 属性的键序偏离 TS（LOW → 实为真缺陷，已修）**：
审查标 LOW（「偏差仅 vs TS，无实际缓存 churn」）。但核实后**判定为真缺陷**——
项目三大支柱之一是「前缀缓存字节稳定」，而工具定义**打的是整个前缀**。
证据（探针实测）：裸 `map[string]any` 经 `wire.writeValue` 排序键 → 产出
`{"description":…,"enum":…,"type":"string"}`，而 TS 是
`type → enum → description`。**全仓其他 enum 属性**（`file_tools`/`git`/
`gitscout`/`plan`）**都用 `enumPropOrdered`**——我是唯一例外。
**修**：改用 `enumPropOrdered`（实测新输出 `{"type":"string","enum":["read","write"],"description":…}`）。
**补断言**：`TestRequestPathAccessDefinitionParity` 此前只校验顶层 `PropOrder`
与 enum **值**，不校验嵌套键序——那是审查指出的「假信心」。已补 `mode` 的
`Marshal()` 前缀断言（变异 M9 红 1 验证有判别力）。

**⑥ `fileToolModes` 不含 `apply_patch`/`read_section`（MEDIUM → **误报**，已核实）**：
审查建议「与 TS FILE_TOOL_MODES 对账确认」。**对账结果：Go 侧与 TS 一致**——
TS 的 `FILE_TOOL_MODES`（`tool-pipeline.ts:243-249`）**也不含**这两个工具。
Go 4 项 vs TS 5 项，差的正是 `ast_edit`（**Go 侧未移植的工具**，缺席合理）。
已在 `approval_pathgrant.go` 补注释说明，避免下个人重复勘探。

#### ⑦ 副作用暴露：`pathgrant_wiring_test.go` 用真实系统路径（已修）

修 ⑤ 后跑全量，**`internal/agent` 包超时 600s**（不是断言失败，是 `panic: test timed out`）。
栈定位：`pathgrant_wiring_test.go` → `loop.go:1279` → `file_tools.go:115`
（`os.WriteFile`）。

**根因**：该测试（第五十一刀引入）用**真实系统路径** `/etc/passwd` 做「出界写」用例。
**skip 档首触即授会对它授权**（对账 TS `tool-pipeline.ts:1241-1249`——TS 同样
**无 forbiddenRoot 上界**），于是 `write_file` **真的尝试写 /etc/passwd**。
此前不暴露是因为 `effectiveGrants` 修复前工具看不到授权（**bug 掩盖了它**）；
本刀的修复让授权真的生效 → 测试的副作用暴露。

**修**：改用「`root` 的兄弟临时目录」——出界判定只需「不在 root 之下」，
等价且无副作用。引入 `outsidePlaceholder` 占位常量，由 `run` 在构造 root 后替换
（**必须在 `toolTurnArgs` 之前**——它把 args 序列化成 JSON，之后改已无效；
首版踩过这个顺序坑）。

**验证**：修复后 PASS（0.02s）；变异 M11（去掉占位替换）红 1。

**诚实标注（M10 等价变异）**：回退成 `/etc/passwd` 后**单独跑该测试是 ok**（0.249s，
未挂）——说明「全量挂」依赖**并发条件**（该测试与其他测试并行时触发）。
**故 M10 红 0 是等价变异**，不是覆盖缺口。
但无论如何，**测试用真实系统路径本身是缺陷**（不可移植、有副作用、时序依赖），
修复消除了整类风险——这与 M10 是否打红无关。

#### ⑧ 一次**被推翻的称量**（第八十一刀四续，记录修正）

本续的起点是：上一轮审查发现 `request_path_access` 在 manual/auto-safe 档被硬拒，
根因指向「Go 无交互审批通道」。我去核实时看到 `registry.go` 写着
「② 审批提示往返通道 —— ✅ 已完成」，**第一反应是「这是错误声称」**，
于是动手订正。

**称量被推翻**：重读同一文件的另一段（「接线条件」②）才发现原文写的是
「（**取后者**：`decideApprovalGate` 的确定性解析，**不造弹窗、不争 stdin**）」
——即 **Go 侧主动选择用确定性解析替代弹窗通道**，这是**有意的架构决策**。
故「✅ 已完成」**是准确的**，引用 `decideApprovalGate` 也正确。

**我的订正本身是错的**——我把「有意选择 A 替代 B」误读为「声称 B 已完成」。
已**回退**，改为 11 行**准确补注**：原文保留，补说明
①「② 由确定性解析满足，是有意决策」②「但确定性解析对『本质需用户决定』的
工具（如 `request_path_access`）无能为力，故非 skip 档硬拒——这是既有架构缺口」。

**教训（写给下个人）**：**判「注释错误」前，先读完该文件的同一主题的所有段落**。
`registry.go` 对同一件事写了两处（「为什么单独提取」与「接线条件」），
**只读一处会得出相反结论**。这与第七十八刀（注释落后于实现）是**镜像问题**——
那次是注释比代码旧，这次是**我的阅读比注释浅**。

**另核实**：该「缺口」本身**不是新发现**——`approval_gate.go`（「Go 无提示通道」）、
`loop.go`（「Go 侧无该决策树与提示通道」）**多处已准确记录**。故本轮无新缺口，
只是把「确定性解析的边界」补写清楚。

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

### 验证基线（末次**真实工具输出**，`de3d805f` 时点）

| 命令 | 结果 |
|---|---|
| `cd go && go test ./... -count=1` | **28 包 ok、0 FAIL**（`go list ./...` = 30 包，含无测试的） |
| `cd go && go vet ./...` | exit=0 |
| `cd go && gofmt -l .` | 零违规 |
| `cd go && go test ./internal/net/ -v` | **199 PASS** |
| `cd go && go test ./internal/search/ -v` | **84 PASS** |
| 工具数（CLI 装配下 `NewDefaultRegistry(...).Definitions()` **实测**） | **42** |
| 工作树 `git status --short` | 仅 plan 文件未跟踪 |
| 探针残留 `find . -name 'zz_*' -o -name '*.good'` | 0 |

**⚠ 工具数口径**：`grep -cE 'r\.Register\('` 报 **39**（含 `r.Register(t)` 循环行）。
**以 `Definitions()` 实测的 38 为准**。旧文档写 25/26/27 是各段时点数。

**⚠ 已知未复现的失败**：会话早期曾见 `-race` 3 FAIL，**之后多次重跑未复现，归因未知**。若下个会话遇到，从头查。


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
- 分支：`go-runtime` · HEAD：`029a289e` · 工作树 clean
- 仓库双 remote：`origin`（私有镜像）、`tianshu`（公开仓库，**绝不直接 push**——历史不同步会被拒；正确流程见项目 `AGENTS.md` 的 `scripts/sync-to-public.sh`）
- `go/internal/` 包（30 个，`go list ./...` 实测）：`cmd/tianshu` + `internal/{agent,api,api/sse,api/stablejson,api/wire,apierr,artifact,cache,client,compact,config,context,contract,filediff,hooks,net,pathsafe,plan,platform,prompt,recovery,retry,rivetpath,search,session,skills,syntaxcheck,tools,trust}`
- **测试命令**：`cd go && go test ./... -count=1`（基线 28 包 ok / 0 FAIL）

---

## 第 9 段：`import_resource`（第一百刀）+ 一处上游缺陷的修正

**区间**：`337223f7..8bba0875`（3 提交）· **工具数 38 → 39**

### 为什么只有这一个工具（三路调研的结论）

| 调研路径 | 结论 |
|---|---|
| ① 工具差集 | TS 70 vs Go 38 = **32 项**差异 |
| ② 零消费扫描 | 9 项零消费，但归因后 **(b) 忘接线 = 0**（6 项消费方未移植、3 项 TS 同样零消费） |
| ③ 重子系统前置 | 4 项**全部**需先建子系统 |

`import_resource` 是**唯一**「依赖已就位」项——7 个依赖组件实测全在位
（`expandHome`/`DetectSensitiveFile`/`HTTPFetchGuarded`/`artifact.Store.Save`/
`ResolveGitCommand`/`relPosix`/`UTF16Len`），且 `net/fetchcore_test.go:253`
**早已断言**错误文案提示该工具（门链在等）。

### ★ 发现并修正一处**上游缺陷**（本段最重要的产出）

**缺陷**：`import_resource` 用 `symlink` 把外部资源「放进」工作区，但
`pathsafe.Validate` 会 `EvalSymlinks` 解析到**源路径（工作区外）** →
`read_file`/`grep` 读它时被拒。

**实测证据**：导入后 `read_file` 读 `.rivet/external/note-xxx.md` 报
「Path outside project directory」——即摘要里「该资源现可通过项目内路径访问……
请使用 read_file、grep、glob 配合此路径」这句承诺**是假的**。

**核实上游**：TS `path-validate.ts:50` 同样 `realpathSync`；
`import-resource.ts` **既无 grantPath 也无豁免名单**（grep 零命中）——
**TS 侧同样有此缺陷**，这是「忠实移植」会把缺陷一起搬过来的典型。

**修正（用户拍板）**：Go 侧改为**复制**而非符号链接。
代价（用户已知情）**：磁盘占用 + 源更新不同步**。

**验收判据**（不给任何授权时）：产物 `symlink=false`；
`read_file` `isError=false` 且读到原文；`grep` 同样命中。
→ **「导入后可用」这句承诺现在是真的**。

### 提交后审查的 4 条 HIGH（逐条核实为真后修复）

1. **URL 分支超时**：Go `Options{}` → 15s；TS 显式 60s → 15~60s 下载在 Go 超时。已修。
2. **GitHub file 情形丢 `files`**：TS 恒传 `files` 且**从无 size**；Go 覆盖成 `{file,size}`。已修。
3. **checkout 缺 `.git` 守卫**：TS 有 `ref && existsSync(.git)`。已补。
4. **尾斜杠 URL 的 filename**：JS `basename('/')` 返回 `'/'`（truthy）故保留；Go 排除 `"/"`。已对齐。

**额外自查发现**：`stats.size !== undefined`（TS）是**字段有无**判定——
Go 用零值会让 **0 字节文件不输出大小**。已改 `Size *int64` / `Files *int`。

### 本段新增的坑（第 41 条起）

41. **「只读常量的测试」是恒真断言**——`func f() int { return constX }` 的测试，
    删掉调用点也不红。要抓「漏传参数」必须断言**构造出的对象**
    （本段 M-B 变异首版红 0 就是这个原因）。
42. **符号链接放进工作区 + realpath 校验天然冲突**：symlink 让路径「看起来」在内，
    realpath 让它「实际」在外。移植时遇到这种组合要**判断上游是否真的可用**，
    而不是照搬（本段的核心发现）。
43. **测「阈值/边界」逻辑的样本必须跨越阈值**（第 35 条的延续）：
    100 个中文（100 UTF-16 unit / 300 字节）远小于 4000，换口径照样绿；
    要 2000 个中文（2000 unit 未超 / **6000 字节已超**）才能区分。

---

## 第 10 段：LSP 导航子系统（第一百零一刀 · W1–W3+装配）

**区间**：`0068e815..82410b69`（5 提交）· **工具数 39 → 41**

### 这是第一个「子系统级」移植（前一刀是工具级）

判据不是「TS 有什么」，而是**「Go 侧已在等的消费者」**——即已移植的治理层
有多少处按名引用该能力。全量扫描结果：

| 候选子系统 | Go 侧消费点 | 数量 | 形态 |
|---|---|---|---|
| **LSP**（goto/refs） | `probe_discipline.go:84,85,97,98` + `advisory_readback.go:143,170` | **5** | 判定集 / 分类映射 |
| delegate 族 | `planmode.go:69,76` + `context_collapse.go:98` + `registry.go:423,424` + `modeblocks.go:24` + `plan.go:209` | 6 | 允许集 / 别名表 / **提示词** |
| 仓库索引 · 语义搜索 | `probe_discipline.go:80,81` | 2 | 判定集 |
| monitor | `advisory.go:44`（常量） | 1 | **零消费方** |
| undo | `approval_assess.go:305`（风险定级） | 1 | 风险表 |

**★ 但数量不是唯一维度——形态决定「不做会不会坏」**：

| 形态 | 不做时的行为 |
|---|---|
| 判定集条目 | **永不匹配**（无害） |
| 允许集 / 别名表 | **永不匹配**（无害）——`CheckPlanMode` 是纯字符串 map 判断，**不校验工具是否注册**（实测 `planmode.go:236`） |
| **提示词引导** | **真实缺口**——模型被引导去调不存在的工具 |

故 **LSP 是「消费点最多 + 零接线 + 自包含」的那个**；delegate 族虽有 1 处真实
缺口，但正解是建整个 worker 派发内核（≈11744 行），应另立计划。

### ★ 本段最重要的产出：三处成本/归属订正（都推翻了文档）

1. **LSP 真实缺口是 1111 行，不是 2064**。`src/lsp/client.ts`（363 行）名字像
   LSP 客户端，**实为 tsc 类型检查执行器**（`runTypeCheck` / `runTscSubprocess`）；
   真实 LSP spawn 在 `multi-manager.ts:65-80`。另 `typecheck-cache.ts`（556）
   与 `diagnostics.ts`（34）属 tsc 链，且 Go 侧 `tools/testspawn.go:139` 注释
   自述**已对账移植**。
   → 修正 `.rivet/plans/go-重写天枢运行时-分波移植计划.md:53` 的「1891 行」
   （实测 2064，且**其中 953 行不该算进来**）。
2. **`lsp_diagnostics` 是幻影条目**。TS 侧从未定义该工具（`grep "name: 'lsp_"`
   只命中两个）；`lsp_diagnostics` 字面量仅在 `advisory-readback.ts:102,114`
   的工具名清单里。**Go 侧 `advisory_readback.go:143,170` 是对齐的，不做才是
   parity**——原先把它算成一个缺口。
3. **`languageIdForFile(def, filePath)` 的真实参数序是 def 在前**（调研报告
   写成 `(filePath, def)`，与源码不符）。

### 分波与产出

| 波 | 提交 | 文件 | 行数 |
|---|---|---|---|
| W1 | `f22393af` | `lsp/rpc.go` + 测试 | 534 / 676 |
| W2 | `5235f049` | `lsp/server_registry.go` + `lsp/manager.go` + `lsp/platform.go` + 测试 | 590 / 1207 |
| W2.5 | `4a2a15ae` | `lsp/multi_manager.go` + 测试 | 322 / 586 |
| W3 | `1633dd68` | `tools/lsptools.go` + 测试 + 注册 2 行 | 257 / 444 |
| 装配 | `82410b69` | `lsp/navigator.go` + `cmd/tianshu/main.go` 接线 | 111 / 73 |

**测试**：lsp 包 78 用例、tools 包 17 用例；`-race` 干净。

### ★ 移植中发现并修的 4 处上游缺陷（显式偏离，均有测试）

1. **`rpc.ts` 静默丢弃 server→client 请求**（`client/registerCapability` 等）。
   TS 的分派只有三分支，而 JSON-RPC 2.0 要求请求必须有响应——真实
   `typescript-language-server` 会挂等。Go 侧回 `MethodNotFound`。
2. **`decodeMessages` 返回 `rest: string`** → `Buffer.from(rest,'utf8')` 在
   **多字节字符中间**处置换为 U+FFFD → `JSON.parse` 失败 → `catch` **静默丢弃**
   该响应（模型拿到空结果或等 45s 超时）。Go 侧全程 `[]byte`。
3. **`initialize()` 重入泄漏旧 RPC**（`rpc = createRpcClient(...)` 直接重新赋值，
   旧 proc 不杀）→ 同一 transport 两个 readLoop 抢字节 → 新握手 45s 超时。
   TS 生产路径靠「dispose + 新建实例」绕过，但其注释把重入当支持路径。
4. **进程死亡需显式置 `ready=false`**（TS 的 `proc.on('exit')`）——不接的话
   崩溃的 server 被**永久当成 ready**，定义跳转静默失效且**永不恢复**。
   Go 侧经 `WithDeathHandler` 接在 readLoop 终止分支。

### 本段新增的坑（第 44 条起）

44. **Go 的 `[]byte(string(b))` 是无损往返**（`string` 是字节容器、不做 UTF-8
    校验），而 JS 的 `Buffer.from(x,'utf8')` 会把无效序列置换为 U+FFFD。
    移植「按字符串缓冲」类缺陷时，**Go 侧要复刻缺陷必须显式 `strings.ToValidUTF8`**
    ——该缺陷在 Go 里天然不存在（本段 M4 变异首版红 0 就是这个原因）。
45. **TS 的 `Promise` 可被多次 await 且恒返回同一值；Go 的 `chan bool` 是一次性
    消费**。移植「可重复读的完成信号」必须用「**关闭的 channel + 独立值字段**」，
    否则重试/重启类路径静默失效（表现为「还没到就跳过分支」）。
46. **并发下「分配 ID」与「写出帧」若不在同一临界区，出站帧序会与 ID 序不一致**
    （实测 1,3,2）。TS 单线程里两者无 await 间隔故恒有序。用独立 `writeMu`
    覆盖整段（**不能用状态锁兼任**——写管道阻塞时会卡死 reader 的 dispatch）。
47. **`Dispose()` 里等 readLoop 退出会永久挂起**——transport 的 `Close` 未必能
    中断阻塞中的 `Read`。挂了比泄漏一个 goroutine 更坏（调用方要等它才返回）。
48. **变异的第三种假红：编译失败伪装**（`declared and not used`）。本段 M9
    红 0 即此——测试根本没跑。用 `_ = v` 保住引用即可。
49. **装配层是「实现已有但零消费」的最后一道缺口**。子系统全部实现 + 测试全绿，
    但若 `cmd/main.go` 没注入，工具**永不出现**在模型可见列表。
    用户级验收（走生产装配路径数工具数）是唯一能抓到它的判据。

---

## 第 11 段：undo 快照层 + undo 工具（第一百零二刀 · W1–W3）

**区间**：`ee9f30c6..029a289e`（3 提交）· **工具数 41 → 42**（CLI 装配下）

### 为什么是这一刀（承第 10 段的形态判据）

第 10 段把候选按**消费点形态**排了序，`undo` 列为首选：
「`internal/recovery/stack.go`（333 行）已就位，缺的只是 FileHistory 快照层」。

**★ 落地时核实的差距**（比原判断更细）：

| 维度 | `recovery.Stack`（已有） | `FileHistory`（本刀新建） |
|---|---|---|
| 键 | `(cwd, relPath)` | **`messageId`（= tool_use id）→ 文件 → 备份** |
| 历史深度 | 每文件**仅最近一次** | **100 个快照**，按 messageId 分组 |
| 方向 | **写前**捕获旧内容（失败回滚用） | **写后**读取当前内容（历史分层） |
| 能力 | `RestoreLatestBackup` | `rewind(id)` 精确回滚 + `getDiffStats` 预览 |
| 哨兵 | 无 | `FileName==""`=当时不存在（unlink）/ `Unreadable`=读不到（**跳过**） |

**方向相反**是关键：本刀之前在文件头误以为 `checkpoint.go` 是同类
（它实为 `compaction-controller` 的会话历史压缩）；真正相近的是 `recovery`，
但**方向相反**（写前 vs 写后）。

### ★★ 落地前发现的既有缺陷：`ToolCallID` 传的是工具名

四处写工具调 `TrackFileChange` 时传的全是**硬编码工具名**：

| 位置 | 原值 |
|---|---|
| `go/internal/tools/file_tools.go:100` | `"write_file"` |
| `go/internal/tools/file_tools.go:260` | `"edit_file"` |
| `go/internal/tools/applypatch.go:217` | `"apply_patch"` |
| `go/internal/tools/hashedit.go:513` | `"hash_edit"` |

而 FileHistory 的分组键正是 tool_use id——**不改就是 undo 撤错范围**
（同一轮两次 `write_file` 归入同一快照）。真实 id 现成：
`artifact_intercept.go:194` 的 `buildToolCallParams` 早已填 `ToolUseID: tc.id`。

**修法**：改传 `fileChangeToolID(p, "write_file")`（空 id 回退工具名，
保持既有行为）。**两条待验证假设在计划期就验掉了**：
① `FileChangeRecord.ToolCallID` **零读取点**（改值无破坏）
② 它**不进 journal 落盘**（`RecoveryEntry` 不含该字段）

### 分波与产出

| 波 | 提交 | 内容 |
|---|---|---|
| W1 | `2fced2b9` | `internal/filehistory/`（462 行 + 19 用例）+ 修四处 `ToolCallID` |
| W2 | `a0713afd` | `internal/tools/undo.go`（300 行 + 18 用例）+ 注册 |
| W3 | `029a289e` | 装配（Loop 持有 + 双回调注入）+ 四写工具接线 + 端到端（225 行） |

**测试**：filehistory 19 + undo 24（含 4 端到端）；`-race` 干净。

### ★ 本段最有价值的三处「不显眼的正确性」

1. **`Unreadable` 哨兵**（TS 注释明写的**数据丢失**防线）：
   「备份读失败」≠「文件当时不存在」。按后者处理会把 undo 变成**删除**。
   测试用「目录占位备份路径」精确构造读失败，验证 rewind 后文件**仍在**。
2. **`OwnedFiles` 的口径陷阱**：它是**相对路径**，而 History 返回**绝对路径**
   → 直接比较会把**所有文件**误判为「不属于当前任务」（每份预览都带误导告警）。
   修法：展示前统一归一化，展示与比较**共用同一形态**（避免两处各自转换而漂移）。
3. **审计 best-effort**：文件此刻已恢复，审计写失败若冒泡成「撤销失败」
   → 模型重试 → 把刚恢复的旧内容又盖掉（**二次伤害**）。

### 一处设计修正（执行中改的）

初版让装配层维护 per-Loop 的「当前调用 id」供 `TrackFileEdit` 回调读取。
**放弃**：并发工具调用下会串号。改为**回调签名带 `toolUseID`**——
写工具本来就持有 `p.ToolUseID`，直接传出，无共享可变状态。

### ★★ 提交后审查发现的时序倒置（第一百零二刀续，已修）

**审查报了 4 条 CRITICAL，逐条核实全部为真**——W3 交付的「撤销安全网」
**实际什么都不做，且自称成功**。

**根因**：我把 `TrackEdit` 接到写盘**成功之后**，并在注释里断言「对账 TS 的
trackEdit 语义——它记录的是编辑后的内容」。**那个断言与 TS 源码相反**。

TS `src/agent/tool-pipeline.ts:1404` 注释逐字：

> // E4 记账收口：五件写工具（WRITE_TOOL_NAMES）的编辑都要**在执行前**进
> // file-history（/undo 与边界回溯的记账源头）。

**后果链**：备份 = 编辑后内容 → `GetDiffStats` 的 `oldContent == newContent`
恒真 → FilesChanged 恒空 → undo 预览恒返回「最近快照中没有可撤销的变更。」
→ `Rewind` 把当前内容原样写回却仍计入 changed → 报「已恢复 N 个文件」但
文件**零变化**。

**四条审查发现**（均已处置）：

| # | 发现 | 处置 |
|---|---|---|
| 1 | 时序倒置（核心） | 记账移到 `executeTool` 里 `registry.Execute` **之前**（`loop.go`），路径从**入参**解析（对账 TS `extractWriteFilePaths`） |
| 2 | **e2e 测试掩盖缺陷** | 原来用包内假实现手工注入「编辑前内容」，把正确时序编码成期望 → 改用**真 `filehistory.History`** |
| 3 | `OwnedFiles` 无生产写入方 | 核实为**既有休眠接线**（依赖未移植的 `ownershipLedger`）；**显式登记**而非硬接错源（见下） |
| 4 | 注释与 TS 相反 | 修正 `filehistory` 包与 `TrackEdit` 的文档；记录后果链 |

**修正后新增的防护**：
- 源码级次序断言 `TestTrackEdit_CallSitePrecedesExecution`
  （记账语句必须在 `Execute` 之前——纯文本事实，失败信息直指问题）
- 变异反证：M35（把记账行移到 `Execute` 后）→ 红；M36（e2e 里登记挪到写入后）→ 红

### 关于 `OwnedFiles`（审查 CRITICAL-3 的处置理由）

核实：其生产源是 TS 的 `deps.ownershipLedger?.getOwnedFiles()`，而
**`ownershipLedger` 子系统 Go 侧未移植**。受影响面至少三处：
`git.go:236,371`（作用域提交）、`diff.go:161`（`current_task_only`）、
`undo.go:240`（归属告警）——前两处会**静默退化为「全量」**，
在本仓库的多会话共享工作区里可能把别的会话的文件卷入提交。

**选择显式登记而非硬接源的理由**：可选源要么语义不符
（`evidence.GateState` 的 `FilesModified` 是**计数**，TS 要的是**集合**），
要么要移植整个子系统。**硬接的危害比恒空更严重**——归属这个安全语义会
「看起来生效而实际错误」：恒空是 fail-loud（告警不输出，用户不误信），
而错误归属会输出**错的**告警把用户引向错误结论。

已加两条测试把该事实钉住（`TestOwnedFilesHasNoProducer_ProductionFact`
将来接线后会红——那是**提醒**而非回归）。

### 本段新增的坑（第 50 条起）

50. **「备份读失败」与「文件当时不存在」是两种世界**，前者绝不能触发删除。
    这类哨兵语义容易在移植时被压扁成一个 `null`——必须用**专门构造读失败**
    的测试钉住（本段用「目录占位备份路径」）。
51. **路径口径必须在一处统一**：绝对 vs 相对混用会让「归属比较」全量误判。
    归一化放在**展示前**一次完成、展示与比较共用，避免两处各自转换而漂移。
52. **「写进私有字段的字面量」行为不可观测**（无导出读取接口）——
    验证它有三条路：给生产 API 加测试专用读取方法（**最差**，为测试改接口）、
    reflect 读私有字段（脆弱）、**源码级断言**（确定性，失败信息直指「忘了接线」）。
    本段取第三条（四处 `ToolCallID` 的接线验证）。
53. **回调签名带上调用标识**（如 `toolUseID`），比让装配层维护「当前调用」
    的共享可变字段安全——后者在并发调用下会串号。
54. **工具数随装配而变**：「未注入 navigator 时 LSP 工具不计入 `Definitions()`」
    是设计（不可用 == 不存在），故验收时不能拿固定数字当基准，
    **先确认注入了什么**（本段实测：不注入 LSP 时 40、CLI 装配下 42）。
55. **★「记账/快照类回调」的时序是语义的一部分，不是实现细节**：读磁盘的
    时刻决定备份内容。移植时**必须从上游源码确认调用点位置**，不能凭「哪个
    方向更合理」推断——本段我按「历史分层」推断成「写后」，与 TS 的
    「执行前」恰好相反，导致安全网静默失效且自称成功。
56. **★ e2e 测试若用手工注入的假数据模拟上游步骤，会把「我假设的时序」
    编码成期望——永远打不红倒置实现**。凡测「A 之后 B」的时序，
    A 必须走**真实现**；实在不能时，用**源码级次序断言**兜底
    （纯文本事实，确定性且失败信息直指问题）。
57. **「字段无生产写入方」的两种处置**：能接对源就接；接不了（依赖未移植
    子系统，或可选源语义不符）就**显式登记为休眠接线**并记录受影响面。
    **硬接一个语义不符的源比恒空更危险**——恒空是 fail-loud（告警不输出，
    用户不误信），错值会输出**错的**告警把用户引向错误结论。

---

## 第一百零三刀：LSP 诊断回流（W4）

> 工具数不变（42）——本刀不新增工具，而是**让已有的诊断能力真正通电**。

### 选刀依据（先称量，未按 HANDOFF 推荐）

HANDOFF 原推荐「改 delegate 提示词文案」。**核实后否决**：

TS `volatile.ts:115` 有**逐字相同**的 delegate 文案，而 TS 有真工具
（`delegate-task.ts:132`）——那段文案在 TS 里**是正确的**。Go 侧删/改它
① 背离 parity（本项目硬目标）② 用户仍可手动调用 → 仍是 `ErrUnknownTool`。
**伪修复只隐藏症状**；真解法是建派发内核（11744 行），应另立计划。

### 本刀真正做的事：把「桩」变成「链路」

核实发现 `GetFileDiagnostics` **是返回空切片的桩**（注释自述「本波不实现
诊断内容（属 W4）」），且 Go 侧**从未注册** `publishDiagnostics` 处理器。
故 W4 不是「4 处接线」而是建整条诊断接收链路。

**新增文件**：
- `lsp/diagnostics_filter.go`（211 行）——`FilterDiagnosticsForEdit`
  （对账 `client.ts:318`）+ 4 个 cap 常量
- `lsp/diagnostics_cache.go`——`diagCache`（**`has` 与 `get` 分离**是关键）
- `lsp/diagnostics_fetch.go`——`getFileDiagnostics` 三件套
  （清缓存 → didChange → 等推送）
- `agent/lspdiag.go`——注入收口（双通道 + changeFile 先于取诊断）
- `tools/lspdiagnostics.go`——`WriteToolNames` / `ShouldRunDiagnostics`

**改的既有文件**：`manager.go`（注册通知 + `diags` 字段）、
`multi_manager.go`（桩 → 真调用）、`navigator.go`（暴露两个方法）、
`file_tools.go`（填 `ChangedRanges`）、`loop.go`（字段 + 注入点）、
`main.go`（装配 + 适配器）。

### ★ 三处关键语义（错一处就静默降级）

1. **`diagCache.has` 必须与 `get` 分离**。LSP server 对**无问题的文件**推送
   **空数组**——那是「已检查、无问题」的**确切答案**。若等待循环用
   `len > 0` 判断，干净文件会每次白等满超时（2s）。变异 M39 验证：改成
   `len>0` 即红。
2. **清缓存必须在 didChange 之前**（对账 TS 注释「avoid racing server
   publishDiagnostics」）。顺序反了会自己清掉刚到的推送 → 空转超时。
3. **changeFile 必须早于取诊断**（对账 TS「Must happen BEFORE diagnostics
   so the server's view is current」）。反了拿到的是**编辑前**的诊断。
   变异 M41 验证。

### ★ 复用发现（避免重复建设）

`filediff.ComputeChangedLineRanges` **已完整实现**，其注释逐字预告了 LSP
用途（「让 LSP 诊断过滤暴露一切而非隐藏错误」）。**原计划的新建等于重复
建设**——改为复用后 w4-3 零成本。

### 有意偏离（已披露）

- **不实现 pull 模型**（TS 的 LSP 3.17+ `textDocument/diagnostic` 优先路径）。
  Go 侧只走 push 路径。理由：本机**无 gopls**，pull 路径无法验证；
  且 `diagnosticProvider` 能力判定的分支会引入当前不可测的代码。
- **`severity == 0` 的处理**：TS 的 `undefined <= 2` 为 false（过滤），
  Go 的 `0 <= 2` 为 true（保留）。差异方向是**多给**（severity 缺失时多显示
  一条），符合「降级朝多给倒」原则，接受并记录。

### 提交后审查（7 条 HIGH，核验后 6 条成立）

| # | 发现 | 处置 |
|---|---|---|
| 1 | `dry_run` 死参数（schema 声明、Execute 无读取） | **成立，属既有缺陷**——入下一步 |
| 2 | `severity` 过滤方向与 TS 相反 | ✅ 已修（改 `<=2`）+ 测试 |
| 3 | `WriteToolNames` 含幽灵条目 `apply_edit` | ✅ 已修（→ `ast_edit`）+ 逐字对账测试 |
| 4 | `ChangedRanges` 零消费者 | 审查已自排（作者已声明 W2 未完成） |
| 5 | append 分支偏离 TS | ✅ 已订正注释（**我的更精确**：TS 对 append 会错算） |
| 6 | 同包内重定义内置 `min` | ✅ 已删 |
| 7 | 作者宣称的验证未经本人复现 | ✅ 本刀已本人复现（见下） |

### 验证（本人复现）

- `lsp` 包：诊断过滤 14 用例 + 诊断链路 5 用例 **全绿**
- `agent` 包：注入 6 用例 **全绿**
- `tools` 包：`ChangedRanges` 2 用例 + 判定 3 用例 **全绿**
- **变异反证 6 处全部抓住**：M37（severity 守卫）/ M38（幽灵名）/
  M39（`len>0` 判据）/ M40（不注册通知）/ M41（通知时序）/ M42（UI 通道）
- `-race` 下修了一处**测试基建**竞态（`fakeServer.kill` 未自持锁——
  其注释要求「调用方须持锁」，既有测试从不调它故未暴露）

### 本段新增的坑（第 58 条起）

58. **「桩函数」比「缺失函数」更危险**：`GetFileDiagnostics` 存在、签名正确、
    注释还说明了未来用途，但**返回空切片**。调用方不会报错、只会静默拿到
    空——排查时「函数在、签名对」会让人排除它，浪费大量时间。
    **判据**：看到「本波不实现」「暂时返回空」这类注释，就当它是**缺失**
    而非**存在**。
59. **`has` 与 `get` 必须分离**（缓存/集合类结构的通用教训）：把「已收到空值」
    与「从未收到」合并成一个判据，会让**等待循环**在空值场景下空转超时。
    TS 的 `Map.has` 天然区分二者，Go 的「读 map 得零值」天然**不**区分——
    移植时这是高频陷阱。
60. **测试基建的隐式契约是地雷**：`fakeServer.kill` 注释写「调用方须持有
    `f.mu`」，但既有测试**从不调用它**故从未暴露。新增测试一调就 `-race`
    报错。**修法是让函数自持锁**（消除契约），而非在每个调用点加锁——
    后者依赖「记得读注释」，前者才是结构性的。

## 下一步（第一百零三刀后）

**LSP 诊断回流已完成**（工具数 42——本刀不新增工具）。剩余候选：

1. **`edit_file` 的 `dry_run` 死参数 —— 最近、最实、且危险**。
   schema 声明了 `dry_run`、Execute 全文无读取，故 `dry_run=true` 时
   **文件直接落盘**（模型预期预览、实际已改）。TS `edit.ts:188-214` 有真实现
   （`buildDryRunPreview`，含语法预检）。**这是既有缺陷**（非本刀引入），
   但危害明确——模型一旦用它会**意外修改文件**。
2. **delegate 派发内核**（≈11744 行）——「改文案」已被证伪为伪修复
   （见本段选刀依据）。要么建内核，要么**显式记录为已知缺口**。
3. **LSP 的 pull 模型**（LSP 3.17+ `textDocument/diagnostic`）——
   本刀有意未做（无 gopls 无法验证）。有 gopls 环境时可补。
4. **monitor** —— 建了会是休眠（唯一消费方是 `advisory.go:44` 的常量）。
5. **仓库索引 / 语义搜索** —— 需 Meridian 图 + embedding（零基础）。
6. **不可做**：`computer_use`（TS 侧开源桩 + `src/pro/` 闭源）、
   `sandbox_exec`（语义前提是「隔离的 Node.js 子进程」）。

**注**：本刀发现 `filediff` 包已具备 hunk 级 diff 能力
（`ComputeChangedLineRanges` / `BuildFileDiff` / Myers 算法），
**将来做 diff 相关能力前应先扫它**——不必重写。

## 下一步（第一百零二刀后）——**已被上方「第一百零三刀后」取代，保留以示修正轨迹**

> ⚠️ 本节已过期（LSP W4 已完成）。保留原文以记录「delegate 文案曾被当作
> 最小修法」这一**已被证伪**的判断。

<details>
<summary>原内容（点击展开）</summary>

**undo 已完成**（工具数 42）。剩余候选（按第 10 段的形态判据重排）：

1. **delegate 族的提示词缺口 —— 现在唯一的「真实缺口」形态**。
   `go/internal/prompt/modeblocks.go:24` 引导模型调用 `delegate_task`/
   `delegate_batch`，而 Go 侧无此工具（`CheckPlanMode` 不校验注册，
   失败在更下游的 `registry.Execute` → `ErrUnknownTool`）。
   **最小修法**：改那处提示词文案（不建内核）；完整解是建 worker 派发内核
   （≈11744 行），应另立计划。**成本量级差两个数量级，值得先做最小修法**。
2. **LSP 的 W4（诊断回流）**：`tool-pipeline.ts:1581-1607` 的 `[LSP Diagnostics]`
   注入（编辑后把诊断拼进工具结果，`modelText`/`uiText` 分离，
   `MODEL_INREGION_CAP=10` / `UI_DIAGNOSTIC_CAP=20`）。**前置已就位**
   （LSP 子系统 + `getFileDiagnostics` 相位已在），但仍触达 `agent/loop.go`。
3. <details>
<summary>（续）</summary>

**monitor —— 建了会是休眠**：唯一消费方是 `advisory.go:44` 的常量；
   `SessionJobs.OnEvent` 零生产订阅者。
4. **仓库索引 / 语义搜索** —— 需 Meridian 图 + embedding（零基础），规模不可控。
5. **不可做**：`computer_use`（TS 侧开源桩 + `src/pro/` 闭源）、
   `sandbox_exec`（语义前提是「隔离的 Node.js 子进程」）。

**注**：本刀发现 `apply_patch` 是**多文件**补丁（逐文件登记历史），
若将来做「按 hunk 的精细回滚」，需在 `filehistory` 里引入 hunk 级标识——
当前粒度是「文件级」（对账 TS 的同粒度）。

## 下一步（第一百零一刀后）——**已被上方「第一百零二刀后」取代，保留以示修正轨迹**

> ⚠️ 本节已过期（undo 已完成）。最新结论见上方「## 下一步（第一百零二刀后）」。

<details>
<summary>原内容（点击展开）</summary>



**LSP 导航已完成**（工具数 41）。剩余候选按「消费点形态」重排（见第 10 段的形态表）：

1. **`undo` 快照 —— 次优候选，成本最低**。`internal/recovery/stack.go`（333 行）
   **已就位**（四写工具都持 `recovery.DefaultStack()`，对账 `recovery-stack.ts` +
   `recovery-journal.ts`）；缺的只是 **FileHistory 快照层**（TS `file-history.ts`
   346 行——按 **tool_use id** keyed、支持按会话边界精确回滚 + diff stats +
   `null` 哨兵语义）。消费点仅 1 处（`approval_assess.go:305` 已按名预置风险定级）。
   **性质**：给模型文件撤销安全网，是真实的缺失能力（非「不做也不坏」）。
2. **delegate 族的提示词缺口 —— 唯一的「真实缺口」形态**。
   `prompt/modeblocks.go:24` 引导模型调用 `delegate_task`/`delegate_batch`，
   而 Go 侧无此工具。**注意**：`CheckPlanMode` 不校验注册（纯字符串判断），
   故失败发生在更下游的 `registry.Execute` → `ErrUnknownTool`。
   最小的修法是**改那处提示词文案**（不建内核）；完整解是建 worker 派发内核
   （≈11744 行），应另立计划。
3. **monitor —— 建了会是休眠**：唯一消费方是 `advisory.go:44` 的常量；
   `SessionJobs.OnEvent` 在 Go 侧**零生产订阅者**（休眠接线）。
4. **仓库索引 / 语义搜索** —— 需 Meridian 图 + embedding（Go 侧零基础），规模不可控。
5. **不可做**：`computer_use`（TS 侧是开源桩 + `src/pro/` 闭源实现）、
   `sandbox_exec`（语义前提是「隔离的 Node.js 子进程」，与 Go 重写冲突）。

**LSP 的 W4（诊断回流）已搁置**：`tool-pipeline.ts:1581-1607` 的 `[LSP Diagnostics]`
注入（编辑后把诊断拼进工具结果，`modelText`/`uiText` 分离，`MODEL_INREGION_CAP=10`/
`UI_DIAGNOSTIC_CAP=20`）**独立于 goto/refs** 且触达 `agent/loop.go`——风险面更大，
待需要时另立计划。

</details>

---

## 下一步（第一百刀后）——**已被上方「第一百零一刀后」取代，保留以示修正轨迹**

> ⚠️ 本节内容已过期（LSP 已完成）。最新结论见上方「## 下一步（第一百零一刀后）」。

<details>
<summary>原内容（点击展开）</summary>



**本节已按第一百刀的三路调研更新**——原「下一步」的候选均已在第 9 段完成归因。

**核心结论：32 项工具差集里，只有 `import_resource` 依赖已就位**（已完成）。
其余按阻塞点分类（详见第 9 段）：

1. **需先建子系统**（缺的是整套设施，非一个工具）：
   `lsp_goto_definition`/`lsp_find_references`（需 `go/internal/lsp/`，TS 侧含
   client+rpc+manager+server-registry+typecheck-cache）、`repo_graph`/`semantic_search`
   （需 Meridian 索引 + embedding）、`deliver_task`/`delegate_task`/`team_orchestrate`
   /`galaxy`/`starflow`（需 coordinator/work-order/plan-executor）、`monitor`
   （需 MonitorRegistry + 投递 hook）、`undo`（需 file-history 快照层）、
   `generate_image`（需 provider 配置体系）、`schedule_*`（需 cron 调度器）、
   `ast_grep`/`ast_edit`（需先决策 cgo/纯 Go AST 路线）、`browser`/`browser_debug`
   （需 Playwright 驱动层）
2. **不可做**：`computer_use`（TS 侧是开源桩 + `src/pro/` 闭源实现）、
   `sandbox_exec`（语义前提是「隔离的 Node.js 子进程」，与 Go 重写冲突）
3. **零消费符号无需清理**：9 项中 **(b) 忘接线 = 0**（见第 9 段的归因表）

**判缺口的方法**（已验证有效）：`grep -rn "函数名" go/internal/ --include="*.go" | grep -v _test`
——排除定义与测试后若零命中，才是真缺口；**不要照文件头注释判**（第七十八刀教训）。

</details>

---

## 下一步（第九十九刀后）——**已被上方「第一百刀后」取代，保留以示修正轨迹**

> ⚠️ 本节内容已过期。最新结论见上方「## 下一步（第一百刀后）」。
> 保留原因：记录「当时认为该做什么」与「实际归因后该做什么」的差异——那次调研推翻了本节的大部分判断。

<details>
<summary>原内容（点击展开）</summary>



**本节已按第九十八/九十九刀的核实更新**——原「下一步」的 2 条已在本节处理。

1. **`loop.go` 的超时文案注入**（TS 在超时时补目标摘要——「哪个文件/命令超时」）——
   独立小刀，见 `tool-pipeline.ts:1524-1529` 的 `catch` 分支。
2. **`semantic_search`**（仍不建议）——依赖 Go 侧零命中的 `semantic-index` + embedding。
3. **`update_goal` / `session_vitals`**（仍不建议）——同前，Go 侧依赖全为 0。
4. **`computer_use`**——`RequiresUnconditionalApproval` 的最后一个不可达分支。需 browser 层。
5. **`ast_edit`**——需先评估 Go 侧的 tree-sitter 等价物（cgo 绑定决策）。

**判缺口的方法**（已验证有效）：`grep -rn "函数名" go/internal/ --include="*.go" | grep -v _test`
——排除定义与测试后若零命中，才是真缺口；**不要照文件头注释判**（第七十八刀教训）。

---

</details>

---

## 第 8 段：遗留清理（第九十八..九十九刀）

第九十七刀收灯后回头清遗留。三项逐一取证：

### `c51a3117` 接线 web_crawl 的 artifact 落盘（第九十八刀）

**性质**：本仓库高频缺陷模式「**实现已有但零消费**」——三处证据：
`buildCrawlArtifact`（`webcrawl.go:239`）**已实现**、`formatCrawlSummary`
**已接受** `artifactNote` 参数、但 `Execute` 恒传 `""`。
而消费端 `CallParams.ArtifactStore`（`registry.go:171`）**早在位**
（`bash.go:425` / `grep.go:181` / `read_file.go:444` 都在用）。

**故只接线，不造机制。** 三种情形严格区分（对账 TS `tool.ts:177-192`）：
- 没配 Store → 注记**空**（TS 的 `&&` 短路）
- 零页 → 注记**空**（TS 的 `&& result.pages.length > 0`）
- **写失败 → 明示降级**（TS 的 `catch`，文案逐字对账）

第 3 条是「失败要大声」——静默省略会让模型以为内容完整。

**顺带去重**：本地 `artifactSection` 与 `artifact.ArtifactSection` 字段完全一致
→ 删本地类型改用共享的。

**变异反证 5 个全红**（M5 首版是编译失败伪装：`sections` 变未使用变量）。

### `1954f0e6` 接通工具级超时（第九十九刀）

**缺口**：`Tool.Timeout(p)` 被 **35 个工具**声明（`registry.go:40`），
但 `loop.go` 全文**零处** `context.WithTimeout`，`registry.go:341` 直接调工具。

**对账 TS**：`tool-pipeline.ts:1501` 的 `toolDef?.timeoutMs?.(params) ?? DEFAULT_TOOL_TIMEOUT_MS`
+ `withToolTimeout`（`:311`）。TS 注释（`:725-728`）写明理由：
「unknown future hang becomes a visible timeout instead of a **wedged turn**」。

**★ 关键语义分叉（先取证再落笔）**：TS 侧只有部分工具声明 `timeoutMs`
（web-crawl / council / browser / delegate / starflow / plan-task）；
**`web_fetch` / `web_map` 没声明** → 走 `?? DEFAULT`。
故 Go 侧 `Timeout() → 0` 是「**未声明** = 用默认 120s」，**不是**「无超时」。
按后者实现会让挂死的 web_fetch **冻住整个回合**。

**★ 接线引入的回归（实测抓到）**：`context.WithTimeout` 对 nil parent **panic**，
而 `Registry.Execute` 的既有调用方**有传 nil 的**（`acceptance_exportfile_test.go:31`），
且各工具自己在 `Execute` 里都做了 `if ctx == nil` 兜底——**nil 在该层是被接受的输入**。
**实测后果**：接线后全量 **0 FAIL → 2 FAIL**。
这是「加一层包装却收窄了上层契约」的形态。已加 nil 防御 + 两条回归测试。

**另**：`ToolTimeoutRecoveryHint` 首版**我自己编了一句中文**，核对 TS 原文后逐字订正。

**变异反证 7 个**（M4 首版红 0 = 真覆盖缺口，M6 首版红 0 = 编译伪装，均已重验）。

### ⚠️ 未处理：历史断裂（待定夺）

`45feaf4c` 与 `7eb85853`（第八十三刀的前两个提交）**不可编译**——
它们用了 `helpers.go` 的三个 schema 构造器，而 `helpers.go` 的补充被排到
`a6441d0a`（第三个提交）。**worktree 实测确认**：头尾可编译，中间两个不可。

**HEAD 完全可编译、全量绿**——只有「中途检出那两个提交」会断（如 bisect）。

**两条路**：
- **rebase 修**（把 `helpers.go` 挪到第一个提交）：历史干净，但**重写已推送历史**，
  且项目规则禁 amend + 仓库多会话共享工作区 → 风险实在。
- **保留 + 文档记录**（本节即是）：不碰历史，下个人知道别检出那两个点。

**倾向后者**——断裂的代价是「bisect 时可能撞到」，rebase 的代价是
「可能打断其他会话 + 违反项目规矩」。但这是**不可逆操作**，须用户明确回话才能动。

---

## 权威文档索引（按需下钻）

| 想了解 | 读 |
|---|---|
| 每刀的完整技术细节（第一..七十六刀） | `go/HANDOFF.md`（**7217 行**，每刀一个专章） |
| 架构欠账清单 | `go/PLAN.md` |
| 审批门的状态表与称量结论 | `go/internal/agent/approval_gate.go` 的 A/B/B'/C/D/E 节（**用节名定位，不用行号**） |
| 本会话的 35 提交 | 本文档 + `git log e866fad8^..de3d805f` |
| 第七十七..**九十七**刀（`go/HANDOFF.md` **未记**） | 本文档「第 4 / 5 / 6 / 7 段」 |
| Windows 可移植性的完整记录 | 本文档「坑」1–15 + `go/HANDOFF.md` 前五刀 |

### 第 7 段：工具移植 W1–W3（办公文档 / open_path / capability / internal/net / web_fetch + web_crawl + web_map / web_search 全包）（第八十二..九十七刀）

**区间**：`fde51827..de3d805f`（**34 提交**，含本段末尾的删除补交）
**工具数**：**27 → 38**（`grep -cE 'r\.Register\('` 报 39，含 1 处循环注册；`Definitions()` 实测 38）
**验证**：全量 **exit=0 / 0 FAIL / 28 包 ok**；net 包 199 PASS、search 包 84 PASS

#### 起点：第八十二刀纠正一次**循环论证**的称量

`export_file` 此前被判「不做」（理由：Go 侧缺依赖）。**这个判据是循环论证**——
我用 `grep 工具名` 在 Go 侧找命中，而**工具本身还没移植、必然零命中**。
**正确判据**：读 TS 的**真实 import**，分三类——平台能力（Go stdlib 有）/
项目内子系统（已移植）/ 项目内子系统（缺失）。

**关键发现：门链早已在等工具**（休眠接线）——
`approval_pathgrant.go:101-129` 已有 `export_file`/`create_document`/`open_path`
三个授权分支；`threshold.go:80` 有 `"web_fetch": 1.0`；
`probe_discipline.go:66-68` 注释明写「判定集里的未知名字行为等价于不存在——
**保留无害且将来移植时自动生效**」。

#### W1：办公文档 5 工具（`45feaf4c`/`7eb85853`/`a6441d0a`/`6d4211bd`）

**推翻 scout 初判**：scout 称依赖 docx/exceljs/pdf-lib。**核实后零第三方库**——
`.doc`/`.xls`/`.ppt` 都是「HTML 伪装」，`.pdf` 是「打印就绪的 HTML + @page」。
全是 `exportFile` 下游。

| 文件 | 行数 | 关键语义 |
|---|---|---|
| `createdocument.go` | 220 | content **不看 trim**（空串合法）；`escapeHTML` 的 `&` 必须最先替换 |
| `createspreadsheet.go` | 288 | `.xls` 与 html 同渲染器；CSV/TSV 引号触发字符不同 |
| `createpresentation.go` | 218 | **嵌套 object 数组**（首个用 `objPropMapOrdered`）；页码从 1 |
| `createpdf.go` | 172 | content 是**原始 HTML 不转义**；且**看 trim**（与 document 相反） |
| `createimage.go` | 161 | width/height 是 `number`；**两个不同文案** |

#### W2：`9bb91a99` open_path + `18784100` capability

- `openpath.go`（343 行）：三平台命令；**不用 `cmd /c start`**（元字符注入面）；
  Windows 无处理程序时退化 reveal（issue #193）。
- `capability.go`（533 行）：种子 registry 6 条；**package 检查器恒 false**
  （Go 无 node_modules 语义，fail-closed）。

#### W3 基础：`8e9cfbc2` ssrf + `63e83ddc` httpfetch + `1db127e9` htmltomd

- `ssrf.go`（210 行）：IPv4 14 段 + IPv6 6 段；**四类内嵌 IPv4 镜像**
  （mapped/translated/NAT64/6to4，issue #116 云元数据）。
- `httpfetch.go`（322 行）：六层防护 + DNS pin（`DialContext`）。
- `htmltomd.go`（689 行）：四层结构。

**★ P0 缺陷（`ee117e40` 修复）**：`httpfetch.go:156` 在 `doer(req)` 返回后
**立即 cancel**，而终态 body 在循环外读 → `http.Transport` 关闭连接 →
**任何非瞬时 body 都失败**。**复现证据**：慢速 200KB body 报 `context canceled`。
**修复**：body 读移进循环内。

**★ 回归测试的教训**：第一版用 `buildClient` 直接往返**抓不到**（cancel 在内核内部）。
正确做法是注入 **ctx-aware 假 body** 走完整 `HTTPFetchGuarded`。

`ee117e40` 另修审查 6 项，含：① 截断口径不一致（`len()` 字节判、`UTF16Len` 裁 →
中文下虚假提示「已按 200 字符截断」，探针复现：100 中文 = 300 字节/100 code unit）
②③ errorKind 静默丢弃（内核算了、工具层零命中，而 `contract.Result.ErrorKind`
**全仓零生产消费者**）④ 加 `WebFetchWithDeps` 注入入口。
**自我修正**：我写过 `TestWebFetchErrorKindPropagated` **同义反复**（只测局部变量），
删掉重写端到端版。

#### W3 工具：`00e5e9c3` fetchcache + `8ceb2592` extractlinks + `54f80846` fetchcore + `1ebeb8a8` webfetch

- `fetchcache.go`（211 行）：maxAge 缓存（best-effort，0=禁读仍写）。
- `extractlinks.go`（183 行）：`DecodeBody` charset 判定顺序：头 > meta > utf-8。
- `fetchcore.go`（339 行）：管线 + Jina 兜底。
- `webfetch.go`（248 行）：三分支（批量 / actions / 单页）。

#### W3 crawl/map：`370fd941` crawl + `9d380cf0` webcrawl + `308eb397` webmap

**crawl 三个真 bug（第九十四刀）**：
1. 收敛判据错 → **只抓 1 页**（探针：fetcher 返 2 链接但 `pages=1`）
2. 页数超发（上限 3 产出 7）→ 需 `len(pages)+inFlight >= maxPages` 双重条件
3. 测试位置索引假设错（goroutine 完成顺序不定）

`web_map` 的三路来源（sitemap / 种子页链接 / `site:` 搜索）中，
**第三路当时不可用**（依赖 web_search）——**如实披露**并预留
`WebMapSearchBackend` 接口 + `WebMapWithBackends` 构造器。
**第九十七刀 W6 已兑现**（见下）。

#### 第九十七刀：web_search **全包移植**（W1–W7，7 提交）

用户明确选择「整个包全移植（含 Brave/Tavily/Bing/Bocha）」。

| 波次 | 提交 | 内容 |
|---|---|---|
| W1 | `f6791d8a` | `internal/net/proxy.go`（308 行）——proxy-resolver 移植 |
| W2 | `1cc93505` | search 配置层 + 密钥四层回退链 |
| W3 | `2b5189cc` | `internal/search` 核心——**跑题守卫** + 后端链 |
| W4 | `96b53ffd` | 五个后端（DDG/Bing/Brave/Tavily/博查） |
| W5 | `500855c1` | 工具本体 + 注册 + **导入环修复** |
| W6 | `bd813707` | web_map 搜索路接通 |
| W7 | `00f69f48` | httpfetch 接入 proxy-resolver |

**★ W3 的认知核心——跑题守卫**（`relevance.go`）：
防「HTTP 200 + 结构完好 + 内容无关」的 SERP 冒充答案（**模型无法自我察觉**）。
判据：单查询词 → 零重叠；多查询词 → **覆盖 ≥2 个不同查询词的结果过半数**。
三道同日防线：`site:` 操作符剔除、**同组多枚 bigram 只计一词**（防「量子计算」
自重叠虚高）、**URL 不参与匹配**。测试用 TS 注释里的真实退化样本
（`美国 AI 实验室 出逃 事件 7月 智能体` → 清一色「美国」页面）。

**★ W4 的两条「写进注释的禁令」**（各有回归测试钉住）：
- Bing **必须用浏览器 UA**（非浏览器 UA 返回降级/空 SERP）
- Bing **绝不可传 `setlang` / 英文 `Accept-Language`**——cn.bing.com 对携带
  任何英文语言标识的请求会**静默错路由**（HTTP 200 + 结构完好 + 内容无关）

**★ W5 撞上并修复的真问题：导入环**
`tools → config → agent → hooks → tools`（`config` 含 `LoadPermissions` 依赖 `agent`）。
**修法**：① 新建叶子包 `internal/rivetpath`（纯路径解析，零项目内依赖）
② `config` 的三个路径函数改薄委托（外部 API 逐字不变）
③ search 配置层移入 `internal/search` ④ `tools` 只依赖 `search`。

**★ 第九十七刀的诚实标注**：`secrets.json` 的**加密格式（AES-256-GCM 信封）不解密**
——Go 侧不实现 `auth/secure-store.ts`（315 行 + OS 密钥库）。
旧**明文**格式正常读；加密信封 → `ReadSecret` 返回未命中 → 回退链落到 env。
**这是符合上游设计的收窄**（`secrets-store.ts` 文件头明写 `Reads are fail-open`），
非静默失败——`SecretsIsEncrypted()` 让该降级**可诊断**。
**代价**：桌面端 UI 存的 key 在 Go 侧读不到；CLI 用 env 不受影响。

#### 本段新增文件（`git log --diff-filter=A fde51827..HEAD`，74 个）

```
go/internal/rivetpath/paths.go
go/internal/net/{ssrf,httpfetch,htmltomd,extractlinks,fetchcache,fetchcore,sitemap,map,proxy,crawl}.go   (+ 各自 _test)
go/internal/search/{types,relevance,chain,fetch,config,build,httpfetch,duckduckgo,bing,api}.go          (+ 各自 _test)
go/internal/tools/{exportfile,createdocument,createspreadsheet,createpresentation,createpdf,createimage,
                    openpath,capability,webfetch,webcrawl,webmap,webmap_search,websearch}.go            (+ 各自 _test)
```

#### 本段新增的坑（第 31 条起）

31. **「工具不存在」不能证明「依赖不存在」**（第八十二刀）——用 `grep 工具名` 判依赖是**循环论证**。读 TS 的**真实 import**。
32. **删除类改动不在测试的观测面内**（第九十七刀末）——W5 迁移后 `internal/config/search.go` 成孤儿重复代码，**全量测试一直绿**（两个不同 package 的同名符号不冲突，HEAD 可编译）。**commit 后必须核验 `git status --short`**，未提交的删除就是「交付不完整」。
33. **Go 的中文 bigram 必须按 `rune` 切**——TS 按 UTF-16 code unit（BMP 内 CJK 与 rune 等价），Go 的 string 索引是 byte，直接切出乱码。
34. **测试依赖「当前机器状态」时必须把探测点做成可注入包变量**（第九十七刀 W1）——OS 系统代理探测在「开发机本来没开代理」的机器上恒返回空，**kill switch 测试失去区分力**（M4 红 0）。修法是改产品代码（提可注入探测点）+ 加**反向对照**子用例。
35. **「红 0」的成因有五类**（本段各踩过）：①等价变异（重写变异让它真改变行为）②真覆盖缺口 ③编译失败伪装（`declared and not used`）④**测试输入不对抗**（信封样本恰好缺 `version` 字段 → 被第二道判据拦住；只测正确算法名 → 判据放松照样过）⑤**环境依赖**（见坑 34）。
36. **测试全覆盖「显式注入依赖」路径时，必须补一条走生产默认构造函数的测试**（第九十七刀 W6）——所有 web_map 测试都走 `WebMapWithBackends(...)`，从不走 `WebMap(cwd)`，于是「默认装配漏接线」不会被任何测试抓到。
37. **变异脚本用 `python -c` 时，脚本内 Go 代码的双引号会与外层 shell 引号冲突** → `SyntaxError` → 表现为「编译=0 失败数=0」的**假红**。改用 **heredoc + `assert old in s`**。
38. **`&T{...}.method` 是 Go 语法歧义**（解析为对 method 取值再取地址）——先赋值给变量再取方法值。
39. **给既有函数加回退逻辑时，让既有优先级保持在最前、只在其为空时才走新路径**——既有的测试天然成为回归钉子（W7 的 `TestBuildClientExplicitProxyURLStillWins`）。
40. **导入环排查要沿完整链条读错误**——把纯路径/纯类型下沉为**叶子包**是标准解法，比重构既有包省事。
