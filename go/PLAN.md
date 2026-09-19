# 天枢 Go 重写 — 跨设备续跑计划

> **本文档的定位**：给「在另一台设备上接手」的人（包括几个月后遗忘细节的自己）。
> 读完本文应能在 **15 分钟内**把环境跑起来、知道下一步做什么、以及怎么判断做对了。
>
> 姊妹文档：`HANDOFF.md`（71KB，**详细欠账与踩坑记录**——本文引用它而不复制它）。
> 原计划：`.rivet/plans/go-重写天枢运行时-分波移植计划.md`（**注意：其 checkbox 已与真实进度脱节**，见 §5）。

---

## 1. 一句话目标与当前真实进度

**目标**：用 Go 重写天枢运行时，**字节等价**——Go 渲染的 prompt / 请求体 / 会话行与 TS 逐字节相同。不等价则前缀缓存失效，这是硬约束不是优化项。

**进度（2026-09-19 核实）**：

| 维度 | 数值 |
|------|------|
| Go 生产代码 | 14,114 行 |
| Go 测试代码 | 15,303 行 |
| TS 源码（对照） | 270,600 行 |
| **完成度（按行数粗估）** | **约 5%** |
| Go 包 | 18 个（其中 16 个有测试） |
| oracle 数据集 | 31 个（30 个用 `oracle.json`，`zstd` 用 `frames.json`） |
| 测试函数 | 516 个 |
| 工具 | **10 / 21** |

**必须建立的认知**：这不是"快完成了"，而是**地基已夯实、主体未动**。已完成的深度（字节对账、变异反证、oracle 纪律）很扎实，但广度上 Wave 3/4/5 的核心目录**一个都不存在**。

---

## 2. 环境重建（跨设备第一步）

### 2.1 前置要求

| 依赖 | 版本 | 说明 |
|------|------|------|
| Go | **1.27** | 由 `go/go.mod` 的 `go` 指令钉住，CI 用 `go-version-file` 跟随 |
| Node.js | 24.18.0 | **必须**——oracle 生成器是 TS 脚本，需要跑父仓库源码 |
| git | 任意 | 工具实现依赖 `git apply`、`git diff` 等 |

### 2.2 克隆与位置约束（**关键**）

```bash
git clone <repo> Tianshu-harness
cd Tianshu-harness
git checkout go-runtime        # 工作分支，不是 main
npm install                    # **必需**——node_modules 不在 git 里
```

**`npm install` 是必需步骤，不是可选**：`node_modules/` 不在版本控制内，
干净 clone 后**不存在**。没有它：

- ✅ Go 侧**照常可用**（`go test ./...` 全绿，16 包 ok）
- ❌ **oracle 生成器跑不了**（`node_modules/.bin/tsx: No such file or directory`）

即：**只做 Go 开发可以不装**；**要重新生成 oracle 必须装**。

> **实测记录（2026-09-19，干净 clone 验证）**：`npm install` exit 0 →
> `node_modules/.bin/tsx` 可用 → `tsx go/testdata/recovery/gen-oracle.ts` 输出
> `sha256 efee0f5aaf65594c`，**与开发机一致**（跨设备可复现）。

**目录约束**：Go 代码在 `go/` 子目录，但它**不是独立仓库**——`go/testdata/*/gen-oracle.ts` 用 `../../../src/...` **相对路径 import 父仓库 TS 源码**。

这意味着：

- ❌ 不能只 clone `go/` 子目录
- ❌ 不能把 `go/` 移出父仓库（oracle 生成器会断）
- ✅ 必须整仓 clone，在 `go/` 下跑 Go 命令

**未来拆分独立仓库时的约束**：生成器（`gen-oracle.ts`）应**留在 TS 仓库**——它们是"对账工具"，本就该跟被对账对象在一起。Go 仓库只保留生成好的 `oracle.json`。

### 2.3 验证环境正确（三条命令）

```bash
cd go
go mod verify                                    # 依赖完整性（CI 同款）
GOFLAGS=-mod=readonly go build ./...             # 构建（拒绝隐式改 go.mod）
go test ./... -count=1                           # 全量测试
```

**期望**：`16 个包 ok`、`2 个包无测试（? 标记）`、`0 FAIL`。若失败，先看 §6 排障。

### 2.4 生成 oracle（仅当需要重新对账时）

```bash
cd <repo-root>                                   # **必须回父仓库根**（不是 go/）
node_modules/.bin/tsx go/testdata/<name>/gen-oracle.ts
```

**路径注意**：`node_modules/.bin/tsx` 在**仓库根**，不在 `go/` 下。在 `go/` 里跑
会报 `No such file or directory`。

每个生成器会打印 `sha256 <前16位>`——**同一输入下应可复现**（若不一致，说明生成器引入了时间戳等非确定性，见 §6）。

---

## 3. 开发纪律（**这是本项目最重要的资产，比代码本身重要**）

### 3.1 oracle 纪律（字节等价的唯一保障）

**规则**：文本资产跨语言移植，**一律用生成器从真实 TS 代码路径导出 golden + Go 对账测试**，绝不手抄。

**为什么**：手抄的常量（字段序、默认值、枚举）会引入**自洽假绿**——Go 实现与手抄 golden 双方同错，测试照绿。本项目已因此出过一次事故：

> **事故记录（2026-09-19）**：wire oracle 的字段序是手抄的，写成 `messages→model→stream`，真实顺序是 `model→messages→stream`。Go 与 golden 双方同错，报告的"795 字节逐字节一致"是与自造 golden 自洽的假绿。修复：生成器改为 mock `globalThis.fetch` 捕获**真实 `OpenAIClient.stream()` 实际发送的字节**。

**推广**：oracle 生成器必须调用被测的**真实代码路径**。

### 3.2 变异反证（假绿的唯一解药）

**规则**：写完测试后，**故意把实现改错**，确认测试变红。红不了 = 测试无效。

**本项目已积累的"红 0"成因清单**（踩过的坑）：

| 成因 | 判据 | 规避 |
|------|------|------|
| **编译失败伪装** | 输出含 `build failed` | 先 `go vet` 确认编译通过再数红 |
| 删掉变量后未使用 | `declared and not used` | 加 `_ = x` 保留引用 |
| 等价变异 | 改的是冗余赋值/性能优化 | 检查该分支前置条件是否已保证变量值 |
| 真测试缺口 | 需补测试才红 | 补测试 |
| 不可达分支 | 该路径永不执行 | 换可达路径 |
| 变异未生效 | python heredoc 转义搞坏文件 | 改用独立脚本文件 |
| **`t.Skipf` 隐身衣** | 短路逻辑后测试从 FAIL 变 SKIP | **变异反证前先 `grep t.Skipf`** |
| 走了另一条路径 | 测试场景与被测分支不符 | 先确认测试真的触发了被测分支 |

**实测次数**：编译失败伪装成"红 0"累计遇到 **14 次**——这是最高频的陷阱。

### 3.3 分波交付

每个逻辑单元：**改 → typecheck → 相关测试 → 全量测试 → 变异反证 → commit**。不积攒不相关改动。

---

## 4. 下一步做什么（**按依赖排序，不按难度**）

### 第一刀（进行中）：Wave 4 的 hook 管线 + 认知层

**进度：管线本体已完成（2026-09-19）**

✅ `internal/agent/hooks.go`——五阶段管线本体（472 行 + 575 行测试，18 个测试）
- 五阶段分派（preTurn / afterPerception / postTool / postTurn / postSession）
- 阶段内**串行保序**（hook 有顺序依赖）
- 单 hook 失败/超时**不中断**该阶段
- 单 hook `BudgetMs` 覆盖全局预算
- **迟到收尾**：超时后 hook 仍会 settle，迟到失败送 `OnError` 但不重复计 runs
- `stats` 按 `phase:id` 键控（跨阶段同名不冲突）
- panic 恢复（hook panic / OnRun 回调 panic 都不崩主流程）
- 禁用集热更（只影响运行时，不改变注册集）

变异反证 7 个：**全部有判别力**。

**下一步**：具体 hook 移植（依赖 `internal/context` 认知层）。

### 第二刀（已完成）：`internal/context` 的 rounds 分组（2026-09-19）

✅ `internal/context/rounds.go`——OAI 轮次分组（352 行 + 237 行测试）

对账 `src/context/rounds.ts` 的三个导出函数：

- `GroupIntoRoundsOai`——五种分组形态（user 单条成轮 / assistant 无 tool_calls
  单条成轮 / assistant 带 tool_calls **贪心吸收**后续 tool 消息 / 孤儿 tool /
  其余）+ 不变量三态判定（ok / repaired / broken）
- `CountRoundsOai`——快路径计数（TS 注释：400 轮时占 37ms 构建里的 21ms，
  本函数只要 0.2ms）。**与分组必须 parity**，测试钉住
- `ComputeOaiInvariantStatus`——汇总（含孤儿 use/result 的轮 id 列表）
- `EstimateOaiMessageTokens`——token 启发式（CJK 三区间 + `ceil(n/4)` / `ceil(n/1.5)`）

**oracle 对账**：`testdata/rounds/` 14 个用例（覆盖五种形态 + 不变量三态 +
token 边界），**42 个子测试全绿**。生成器**同时导出 `cases.json`**（输入的唯一
真源）——避免「Go 测的是另一组输入」的漂移。

**oracle 抓到的真实缺陷**（自洽假绿的典型）：首版 `stringifyToolCalls` 丢弃
tool_call 的额外字段（如 `index`），而 TS 的 `JSON.stringify` 会带上——
`toolcall_extra_fields` 用例红（Go=19 / TS=25）。已修：透传 `Extra`。
**若没做 oracle 对账，此缺陷会静默留存**。

变异反证 9 个：**全部有判别力**。

**未移植**：`EstimateOaiMessageTokens` 的 user parts 分支（多模态，含
`estimateImageTokens`——需解析 PNG base64 头的 IHDR 宽高）。Go 侧该分支退化
为把 content 当字符串。见 HANDOFF。

**下一步**：`internal/context` 其余模块（claim-store / cognitive-ledger /
stigmergy / task-contract），或具体 hook 移植。

### 第三刀（已完成）：第一个真实 hook 接入管线（2026-09-19）

✅ `internal/agent/typecheck_reminder.go` + `advisory.go`（77 + 127 行 +
238 行测试）——**证明 hook 管线地基可用**（管线本体不再空转）。

对账 `src/agent/hooks/typecheck-reminder-hook.ts`（53 行）：

- **触发条件**（三条全真，任务级而非 5 条窗口）：
  `touchedTsFiles` ∧ `!sawTypecheckThisTask` ∧ `run_tests` 在窗口内
- 投递一条 operational advisory（key / priority 0.6 / category typecheck /
  ttl 1 / expect verify_attempted withinTurns 2 / observe turns 1）
- **content 逐字对账**——用户可见文案不可转述

**接口收窄是刻意的**：Go 侧 `AdvisorySink` 只含 `Submit`，对账 TS 的
`Pick<AdvisoryBus, 'submit'>`。`advisory-bus.ts` 有 1277 行，**不做完整移植**
——hook 只该投递，不该读 bus 内部状态。bus 本体（渲染 / 排序 / 去重 / 挂起
观察生命周期 / readback 核销 / holdout）见 HANDOFF。

**测试 10 个**（含端到端经 Pipeline 注册执行 + 统计/manifest 对账）。
变异反证 10 个：**全部有判别力**（去掉三条守卫各 1-2 红 / 窗口语义错 1 红 /
content 改一字 1 红 / expect 值 1 红 / category 1 红 / phase 2 红 / priority 1 红 /
ttl 1 红），零编译失败。

**下一步**：接更多 hook（`todo-reminder` / `vigor` / `theta` / `lossy-observation`），
或补 `advisory-bus` 本体。

### 第四刀（已完成）：第二个 hook + effects 通道验证（2026-09-19）

✅ `internal/agent/consistency_check.go`（98 行 + 249 行测试）

对账 `src/agent/hooks/consistency-check-hook.ts`（39 行）——**原则 ⑤
「有限规则无限涌现」**。这是 cross-store 耦合的第一条信号：
evidence store（工具结果）→ claim store（知识）。

- 只在 `write_file` / `edit_file` 后触发，且需有 target
- 路径**双向后缀**匹配（三条分支：相等 / target 后缀 / claim 后缀）
- 命中则调 `effects.markClaimStale`

**顺带修了一个真实缺陷**：`RuntimeHookEffects` 的 Go struct 零值是 nil func，
hook 直接调字段会 panic。TS 侧 `createRuntimeHookContext` 有 `?? noop` 兜底
（未接线 → 安全 no-op），Go 无对应机制。**这是 Go 移植的特有陷阱**——
TS 的 undefined 调用在类型层就被兜住了。已加 `*Safe` 方法族（nil-safe 包装），
并写测试钉住（`TestConsistencyCheckEffectsNilDoesNotPanic`）。

**发现 TS 的一处既有行为**（非缺陷，是事实）：`endsWith` 是**裸字符串后缀
匹配**，不是路径段匹配——`src/nota.ts` 会被 `a.ts` 的 claim 误标。首轮我的
测试期望写错了（以为应该是 false），用 `node -e` 实测 TS 后确认应为 true。
**移植忠实复刻**，不顺手「修好」（改了会让 Go 与 TS 的标记结果分叉）。

**测试 16 个**（含路径匹配 6 个分支 + effects 未接线不崩 + 端到端经管线）。
变异反证 9 个：**全部有判别力**（去掉写工具守卫 1 红 / edit_file 不认 1 红 /
无 target 也触发 1 红 / 漏后缀分支 4 红 / 前后缀搞反 4 红 / 空 path 参与匹配 1 红 /
用裸字段 panic 1 红 / 只标第一个 1 红 / phase 错标 2 红）。

### 第五刀（已完成）：Pipeline 接入 agent loop —— 消除悬空（2026-09-19）

⚠️ **发现真实缺口**：前两刀的 hook 只在定义文件与测试里出现，`loop.go` **零引用**
（grep 确认）——Pipeline 没接进 loop，整条链是断的。「接 hook」当时只完成了
「hook 接进 Pipeline」，没完成「Pipeline 接进 loop」。

✅ `internal/agent/hook_snapshot.go`（152 行）+ loop.go 接线（+10 行调用点）

- `Loop.Hooks *Pipeline` + `Loop.Effects RuntimeHookEffects`（nil = 跳过，增强而非必需）
- 三个调用点：`Run` 循环开头（preTurn）/ 工具执行后（postTool，携带工具事件）/
  轮末（postTurn）
- `hookSnapshotState` 累积**任务级**标志（对账 TS 注释：`Task-level, not
  windowed — survives a long turn where the edit scrolled out of
  recentToolHistory`）；`recentToolHistory` 是 **5 条窗口**
- 语义要点：写 TS 文件**重置** `sawTypecheck`（改了就要重新查）；失败的工具
  调用**不置**任务级标志（没进磁盘）；`run_tests` **不算** typecheck（这正是
  该 hook 存在的理由）

**顺带修了一个既有缺陷**：`observeToolResult` 的 `read_file` 分支用
`input["path"]`，但工具 schema 的字段名是 **`file_path`**——read 追踪静默失效。
与早前 tool schema 对账时修正的是同一根因形态。

**用户级验收（已执行）**：
- `TestE2EHooksReachedFromRealLoop`——真实 `Loop.Run`（mock SSE server）跑一轮，
  模型调 write_file 写 .ts + run_tests → typecheck-reminder 经 Pipeline 触发并
  投递（sink 收到 1 条 + 管线 stats 记账）
- `TestE2EConsistencyCheckReachedFromRealLoop`——真实 loop 里 write_file →
  consistency-check 触发，`effects.MarkClaimStale` 回调被调用
- 反向用例：已跑 typecheck 则不投递（不误报）
- 生产可达性单独复核：`grep runHookPhase` 现出现于 `loop.go`

**测试 10 个**（含 3 个端到端 + 窗口/任务级语义 6 个 + toolTarget 字段名）。
变异反证 9 个：**全部有判别力**（postTurn 不接线 1 红 / postTool 不接线 2 红 /
Hooks nil 检查去掉 1 红 / Effects 不透传 1 红 / 窗口不封顶 2 红 / 写 TS 不重置
标志 1 红 / 失败写也置标志 1 红 / run_tests 当 typecheck 2 红 / toolTarget 字段名
错 3 红），零编译失败。

**仍未闭环**：advisory-bus 本体（渲染 / 排序 / 去重）未移植——hook 投递的条目
**无处显示**。这是端到端可见性的最后一环。

### 第六刀（已完成）：advisory-bus 核心——投递有了落点（2026-09-19）

✅ `internal/agent/advisory_bus.go`（463 行 + 400 行测试）——**advisory 通路闭环**。
此前 hook 投递的条目无处显示（投进没有出口的箱子）。

**Scope Check**：`advisory-bus.ts` 1277 行，含四套治理子系统。移植的是**核心路径**：
submit / 去重（同 key 保高 priority，平手先出现者胜）/ 排序（priority 降序）/
类别上限（每 category 2 条）/ Top-N 预算（3 条，天权·瑶光 1 条）/ star_domain
豁免 / constitutional·immediate 豁免 / TTL 跨轮存活 / XML 渲染 / ledger。

**未移植的治理子系统**（都在代码注释与类型里显式标注，都是 provider 注入才生效）：
习惯化对抗 / efficacy 负反馈环 / lift 消费 / holdout 反抽样 / SR 通道 / status
通道 / 阶段抑制与挂起观察 / mutex 互斥对 / key 级送达冷却 / 星域措辞适配。

**oracle 对账**（`testdata/advisorybus/`）：**23 个用例 × 2 维度 = 44 个子测试**，
从真实 TS `AdvisoryBus.render()` 导出。生成器同时导出 `cases.json`（输入唯一真源）。

**oracle 抓到的真实缺陷（本轮主要收获）**：priority 格式化。
首版自写「放大 100 倍 + 远离零舍入」，并在注释里断言「Go 的 `%.2f` 用
banker's rounding 会与 JS 分歧」。**实测推翻**——分歧存在但方向相反：

| v | Go `%.2f` | 自写「远离零」 | JS `toFixed(2)` |
|---|---|---|---|
| 2.675 | 2.67 | **2.68** ✗ | 2.67 |
| 0.615 | 0.61 | **0.62** ✗ | 0.61 |
| 1.255 | 1.25 | **1.26** ✗ | 1.25 |

根因：JS 的 toFixed 与 Go 的格式化都按**浮点二进制实际值**舍入；自写的 `v*100`
引入额外精度损失，反而偏离两者共同基准。改用 `strconv.FormatFloat(p,'f',2,64)`
——实测 12/12 与 JS 一致。
**教训**：跨语言数值语义不要凭直觉断言，先写探针实测。

**变异反证 10 个：全部有判别力**（去重 `>=` 2 红 / 排序升序 18 红 / 类别上限去掉
4 红 / 星域预算失效 6 红 / CVM 预算去掉 4 红 / TTL 不减 5 红 / XML 不转义 3 红 /
priority 自写实现 3 红 / constitutional 不豁免 4 红 / ledger 不记 rendered 21 红）。
首轮 M1·M8 各红 0（**等价变异**）——补了 `dedup_same_priority` 与
`priority_edge` 两个 oracle 用例后变红，测试判别力随之提升。

**用户级验收（已执行）**：`TestAdvisoryBusClosesHookLoop`——hook 投递 → bus
渲染 → 输出里出现 `<星域-advisory>` 块含 `typecheck-reminder` 的 key 与提醒正文。
`TestAdvisoryBusAsPipelineSink` 走真实 Pipeline。

**下一步**：治理子系统按需增量移植（先做习惯化或 efficacy——它们是「提醒被忽略后
静默」的核心），或继续接 hook。

**为什么是它而不是补工具**：

- CVM（认知虚拟机）是天枢三大支柱之一——`RuntimeHookPipeline` 五阶段条件装配 60+ hook，拦截服从性漂移 / doom loop / 验证债务
- **它决定 agent 的认知行为是否与 TS 版一致**，而工具只是能力广度
- 当前 Go 版 loop 是"裸循环"——没有 hook 拦截，行为与 TS 版**根本不同**
- `internal/context`（认知层）与它耦合紧密，应一并做

**范围**（**选常驻基线，非全量 74 个**）：

- `internal/agent/hooks.go`：`Pipeline`（五阶段 + 超时 + 迟到收尾记账 + 统计）
- 首批 hook：`perception` / `signal-consumer` / `kick` / `vigor` / `theta` / `stigmergy` / `radio` / `self-verify` / `context-pressure` / `lossy-observation`
- `internal/context/`：CognitiveLedger / ClaimStore / Stigmergy / PressureMonitor

**判据**：

- `go test ./internal/agent -run TestHookEquivalence`——fixture 记录 effects 的**有序序列**，漏装某 hook 则序列不匹配
- `go test ./internal/agent -run TestHookLateFailureAccounting`——迟到收尾反证
- `go test ./internal/agent -run TestAliasCannotBypassDenyRule`——别名绕过反证

### 备选：补工具（收益较低但独立）

缺 11 个：`plan`、`job`、`ast_grep`、`diff`、`git`、`web_fetch`、`web_search`、`repo_map`、`read_section`、`request_path_access`、`ask_image`、`skill`。

其中 `git`（684 行）、`diff`（163 行）较独立；`plan` 依赖 Plan Mode 审批门禁（属 Wave 4）。

### 第三刀：Wave 3 的压缩与缓存

`internal/compact/`（边界压缩，仅 `turn==0` 重写历史）、`internal/cache/`（命中率统计 / advisor / 审计）——**目录均不存在**。

**注意**：缓存命中率是**端到端判据**，不能只靠单测。已跑通过一次真实端点冒烟（前缀缓存 93.7–95.5%），后续改动应重跑。

---

## 5. 与旧文档的关系（**避免重复劳动**）

| 文档 | 定位 | 使用方式 |
|------|------|---------|
| **本文** | 跨设备续跑入口 | 先读这个 |
| `go/HANDOFF.md` | 详细欠账与踩坑记录（71KB） | 遇到具体问题时查 |
| `.rivet/plans/go-重写天枢运行时-分波移植计划.md` | 原始分波设计 | **看设计意图，不看 checkbox** |

**⚠️ 原计划的 checkbox 已脱节**：31 个 checkbox **全部未勾选**（0 已勾选），但 Wave 0/1 实际**已完成**（含 Go/No-Go 门通过）。照 checkbox 做会重复劳动。

**建议**：接手后先花 10 分钟把原计划的 checkbox 按真实进度更新一次，或干脆在本文 §4 维护进度（单一事实来源）。

---

## 6. 排障手册（跨设备常见问题）

### 6.1 测试失败但代码没改过

**先查残留**：`find . -name ".rivet" -type d`——变异反证或测试可能在工作目录留下 `.rivet/`，污染后续运行（本项目遇到过：M5 变异在 `internal/agent/` 留下会话文件，导致 `TestLoopSessionDirUnderCwd` 假红）。

**再查并发**：本仓库常有并发 agent 会话，测试若用固定临时路径会互相干扰。

### 6.2 oracle sha256 不可复现

**原因**：生成器引入了非确定性（时间戳、随机、路径）。

**已遇到的案例**：recovery oracle 首版含 `ts` 时间戳，三次 sha256 各不相同。**修法**：输出前把时间字段替换为占位符（`scrub()`），只锁键序与格式。

**排查**：`for i in 1 2 3; do tsx gen-oracle.ts 2>&1 | grep sha256; done`——三次必须一致。

### 6.3 字节对账失败但"看起来一样"

**用 `bytes.Equal` 而非 JSON 深比较**。深比较会漏掉 `&<>` 转义、键序、数字格式（`1.0` vs `1`）等差异。

**定位技巧**：打印**首个差异的偏移量**与两侧上下文（本项目的对账测试都实现了这个）：

```
Go ...{"type":"integer","description":"最大匹配行数（默认 100）"}...
TS ...{"type":"integer","description":"最大匹配行数（默认：100）"}...
                                          ↑ 差一个全角冒号
```

### 6.4 Go 版本不匹配

`go.mod` 钉 `go 1.27`。CI 用 `go-version-file: go/go.mod` 跟随，**不要硬编码版本**（避免漂移）。

### 6.5 `golangci-lint` 跑不起来

**已知**：Go 1.27 下无法运行（工具链兼容问题）。**降级为** `go vet` + `gofmt -l`（判据：0 违规）。

---

## 7. 关键约定速查

### 7.1 序列化（缓存命中的核心）

| 场景 | 序列化器 | 语义 |
|------|---------|------|
| 请求体外层 | `JSON.stringify` | **保插入序** |
| 工具 `arguments` | `stableStringify` | **递归排序键** |
| 工具 schema 的 properties | `*wire.OrderedMap` | **TS 声明序**（`PropOrder`） |
| 会话行 | `SerializeOaiSessionMessage` | 保 `KeyOrder` |

**Go 侧陷阱**：`map[string]any` 序列化时**强制排序键**——要保序必须用 `*wire.OrderedMap`。

### 7.2 工具 schema 必须逐字节对账

工具定义进请求体的 `tools` 字段，而**工具定义变化"打的是整个前缀（system+tools 段）"**（`src/api/openai-client.ts:630`）——键序或描述文本不同会让前缀缓存**完全失效**。

**已修过的真实缺陷**：`read_file` 参数名 Go 用 `path` 而 TS 用 `file_path`（**功能性缺陷**，不只是缓存问题）；`bash`/`run_tests` 用 `timeout_ms` 而 TS 用 `timeout`。

### 7.3 安全边界（fail-closed）

- 项目级配置的敏感键在**未授信**时剥离（`internal/trust`）
- 信任文件写 `<rivetHome>/project-trust.json`，**绝不写进仓库目录**
- 按 **realpath** 键控（防符号链接绕过）
- 坏信任文件按**未授信**处理

---

## 8. 接手检查清单

- [ ] 整仓 clone（不能只 clone `go/`），切到 `go-runtime` 分支
- [ ] `npm install`（oracle 生成器需要）
- [ ] `cd go && go mod verify && GOFLAGS=-mod=readonly go build ./... && go test ./... -count=1`
- [ ] 期望 `16 包 ok / 0 FAIL`
- [ ] 读 §3 开发纪律（**尤其 §3.2 变异反证的红 0 成因清单**）
- [ ] 读 §4 确定第一刀（推荐 hook 管线）
- [ ] 更新原计划的 checkbox 或在本文维护进度

---

## 9. 交接备注（写于 2026-09-19）

**已建立的资产**（比代码更重要的是这些）：

1. **oracle 纪律**——31 个数据集，全部从真实 TS 路径导出，全部可复现
2. **变异反证习惯**——每个改动都有"改错看是否变红"的验证
3. **踩坑知识**——`HANDOFF.md` 记录了 14 次编译失败伪装、假绿事故、等价变异判据等

**当前最大的风险**：**广度不足**。地基（API 层、工具内核、prompt 静态层）很扎实，但 hook 管线、认知层、压缩、缓存、TUI、MCP 全部未动。若只在地基上继续加深（像最近几轮做的），会**偏离"可运行的 agent 运行时"这个目标**。

**建议的节奏**：先用 2-3 轮把 hook 管线立起来（它让 agent 行为真正像天枢），再考虑补工具广度。

**一个判断的记录**：`frozen 块位置`（TS 是 trailer-merge 到 user message，Go 拼在 system prompt 后）经调研后重新定性为**架构边界而非待办**——`engine.ts` 1700+ 行的 frozen 体系，移植需重构 prompt 组装架构。收益需实测缓存命中率支撑，**不硬推**。
