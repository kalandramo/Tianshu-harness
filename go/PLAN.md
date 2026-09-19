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

**oracle 对账**（`testdata/advisorybus/`）：**22 个用例 × 2 维度 = 44 个子测试**，
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

### 第七刀（已完成）：bus 接进 loop 的请求组装——真闭环（2026-09-19）

⚠️ **再次发现悬空**：第六刀的 `AdvisoryBus` 有渲染能力，但 **`Render` 无生产
调用方**（grep 确认）——渲染结果进不了 prompt。这与第五刀的 hook 悬空是**同一
模式的重演**，被交付门禁的 `read-but-never-produced` 检查抓到。

✅ `Loop.Advisories *AdvisoryBus` + `buildRequestMessages()`（hook_snapshot.go
+70 行，loop.go 请求构造改为调用它）

- **请求级注入**（不写回 `l.messages`）：每轮调模型前 render，非空则作为
  `<system-reminder>` 包裹的 user 消息**追加在请求尾部**。对账 TS 的
  append-only 细断点通道（缓存安全：不改写历史）
- `wrapSystemReminder` 对账 `src/prompt/system-reminder.ts`——不包裹时每次注入
  都像真实用户边界，触发 prompt engine 重建 appendix，打爆前缀缓存
- `Config.StarDomain` 新增（advisory 预算按域调整）

**用户级验收（已执行）**：`TestE2EAdvisoryReachesPrompt`——真实 `Loop.Run`
（mock SSE server）跑一轮，hook 投递 → bus 渲染 → **请求体里出现 advisory 块**。
另有 5 个端到端用例（system-reminder 包裹 / 空块不注入 / 星域预算 / nil 安全 /
请求内不重复）。

**变异反证 6 个：全部有判别力**（不注入 4 红 / nil 检查去掉 1 红 / 空块也注入
1 红 / 写回 l.messages 2 红 / 不包 system-reminder 1 红 / 星域传空 1 红）。

**过程中的两个方法论收获**：
1. **变异脚本的正则要覆盖全部相关测试**——首轮我用 `-run 'TestE2EAdvisory'`
   匹配，漏掉了 `TestE2ENilAdvisories`，导致 M2 假红 0。改用 `-run 'TestE2E'`。
2. **测试期望可能本身是错的**——我最初断言「advisory 只该出现一次」，实测发现
   **hook 每轮都会重新触发**（touchedTSFiles 仍 true、run_tests 仍在窗口内、
   sawTypecheck 仍 false）。抑制重复属于 TS 的治理子系统（expect 核销 / observe
   挂起 / key 冷却），本移植未包含。**改测试而非改实现**——期望要反映真实语义。
   判据改为「单个请求内至多 1 份」（这才是请求级注入 vs 持久化的真判别器）。

### 第八刀（已完成）：两个治理子系统——key 冷却 + mutex 互斥（2026-09-19）

✅ `advisory_bus.go` 增补（+约 90 行生产 + 340 行测试）

**Scope Check**：`advisory-bus.ts` 的治理子系统依赖分两档——

- **无外部依赖**（本轮做）：`KEY_COOLDOWN_TURNS`（key 级送达冷却）与
  `MUTEX_PAIRS`（互斥让位）。前者只需 `renderEpoch` + 一个 map，后者是静态表。
- **依赖 `AdvisoryReadback`**（未做）：习惯化对抗 / efficacy 负反馈环 / lift 消费 /
  holdout 反抽样。它们都需要跨会话的采纳率台账，当前地基不具备。

**key 级送达冷却**（对账 `KEY_COOLDOWN_TURNS` + `recordDeliveredRender`）：

- 注册 key：`virtue-encouragement` 5 轮 / `readonly-spiral` 3 轮 /
  `turn-call-limit` 3 轮
- 位置在**一切竞争逻辑之前**（TS 注释：冷却中的条目不该占 MUTEX/预算/挂起任何一席）
- 吞掉 ≠ 永久丢失：调用方按轮重新 submit，冷却过后自动恢复
- **只记注册 key 的送达轮次**——未注册 key 数量无上限，全记会让表无界增长

**mutex 互斥让位**（对账 `MUTEX_PAIRS`）：5 对，如 `self-verify` 胜过
`virtue-encouragement`（「你有债」与「干得好」同屏是语义冲突）、
`lossy-observation` 胜过 `readonly-spiral`。**精确 key 等值匹配，不支持通配**。

**oracle 对账**：新增 10 个治理用例（冷却 5 个 + mutex 5 个），总数 **32 用例 ×
2 维度 = 64 个子测试**全绿。

**用户级验收（已执行）**：`TestE2EGovernanceCooldownInPrompt`——真实 `Loop.Run`
跑 5 轮、每轮都投递同一个注册 key，观察到**送达请求：[1 4]**（共 5 个请求）——
中间两轮被冷却吞掉。**这修复了上一刀发现的「同一提醒每轮重复」缺口**。
`TestE2EGovernanceMutexInPrompt` 验证「有债仍表扬」被拦下。

**顺带修了一个真实缺陷**：`Reset()` 未清 `lastDeliveredRenderByKey`。因
`renderEpoch` 归零而旧送达轮次仍在，`renderEpoch - last` 变负（恒 < cooldown），
该 key 被**永久静默**。对账 TS `reset()` 的 `lastDeliveredRenderByKey.clear()`。

**变异反证 8 个：全部有判别力**（冷却不生效 12 红 / 冷却对所有 key 生效 2 红 /
不记送达轮次 12 红 / 非注册 key 也记 1 红 / mutex 不生效 6 红 / mutex 方向搞反
19 红 / Reset 不清表 1 红 / 冷却不记 dropped 6 红）。

**方法记录**：M4（非注册 key 也记轮次）首轮红 0——它是**行为等价变异**（冷却
查询同样要求 registered）。补了一条直接检查内部状态的测试（
`TestGovernanceCooldownTableOnlyRegistered`）后红 1。**行为等价的变异要靠
不变量测试钉住，而非行为断言。**

### 第九刀（已完成）：CLI 装配——消除第三次悬空（2026-09-19）

⚠️ **第三次同模式发现**：前八刀的 hook / bus / 治理子系统在**真实 CLI 路径上
全部未生效**——`cmd/tianshu/main.go` 构造了 `Loop` 但**没装 `Pipeline` /
`AdvisoryBus`**（grep 确认 `NewAdvisoryBus` 无生产构造点）。我的单测测的是
「注入后的行为」，不是「CLI 会不会注入」。

**这是同一模式的第三次**：hook 接进 Pipeline 但 Pipeline 未接进 loop（第五刀）→
bus 有 Render 但 Render 无调用方（第七刀）→ 组件齐备但 CLI 未装配（第九刀）。

✅ `cmd/tianshu/main.go` 装配三件：`AdvisoryBus` + `Pipeline`（注册两个真实
hook）+ 接进 `Loop`。对账 TS 的 loop-factory / create-runtime-hooks 装配路径
（TS 侧默认装 ~18+ hook，这里只装已移植的两个）。

`consistency-check` 的 `getFileObservations` 注入**显式空集**——claim store 未
移植，hook 不产生副作用。这是**显式降级**而非静默失效（接上 claim store 后
自动生效）。

**用户级验收（已执行，真实二进制）**：`cmd/tianshu/cli_advisory_e2e_test.go`
——`go build` 出真实二进制 → 以 `-p` 跑 → mock SSE 端点 → 检查**发往端点的
请求体含 `<星域-advisory>` 块**。这是首次用**真实二进制**（而非内部函数）验证。

**变异反证 3 个：全部有判别力**（CLI 不装 bus 1 红 / 不装 Pipeline 1 红 /
不注册 hook 1 红）——装配的每一环都被钉住。

**方法教训**：内部测试的「注入后行为」永远无法证明「生产会注入」。**要验
生产可达性，必须跑生产入口本身**（这里是二进制）。前两次我靠 grep 补验，
这次直接用真实二进制测试锁住——这类测试应作为后续每一刀的收尾动作。

### 第十刀（已完成）：claims 纯逻辑层——认知层根节点（2026-09-19）

✅ `internal/context/claims.go` + `promotion.go`（682 行 + 438 行测试）

**Scope Check**：`claim-store` 是依赖链根节点（`loop-factory.ts:691` 的
`contextClaimStore.listClaims` 是 consistency-check 的真实来源），含事件溯源
+ 落盘。本轮先做**纯逻辑层**（无 I/O，可 oracle 对账）——store 层留下一步。

对账 `claims.ts` + `promotion.ts`：

- claim 构造 / sha256 ID 派生（**键序敏感**：TS 对象字面量序，不能用 Go map）
- `renderActiveClaimsBlock`（三级排序 fitness↓ / confidence↓ / createdAt↑，
  截断 20，XML 转义）
- `checkpointClaims` / `loadClaimSnapshot`（version 门禁）
- `evaluatePromotion`（去重消费者计数，阈值 3/5 + 10 分钟年龄）
- `canRecallClaim`（文件证据召回门禁）/ `claimHasFileEvidence`（kind 门禁）
- `claimProposalFromAnchor`（anchor → claim 映射与 confidence 分级）

**oracle 抓到的 TS 既有缺陷（重要）**：`renderActiveClaimsBlock` 里
`.filter(isPromptEligibleClaim)` **直接把函数引用传给 filter**——JS 的
回调签名是 `(element, index, array)`，故 `now` 参数收到的是**数组下标**
（0/1/2…），不是 `Date.now()`。后果：`expiresAt` 过期检查实际失效。

实测确认（`node -e`）：

```
filter 直传：      [alive, exp, future]   ← exp 已过期却仍入选
显式传 Date.now()：[alive]                ← 「正确」行为
```

**移植决策：忠实复刻**。Go 侧若「修正」为真 now，同一会话状态下渲染结果与
TS 分叉，破坏字节等价（硬约束）。已在代码注释与测试里显式标注；若上游修了
此 bug，oracle 会红，届时同步。

**oracle 对账**：`testdata/claims/` 29 个用例，**92 个子测试**全绿。

**变异反证 12 个：全部有判别力**（render 用真 now 3 红 / ID 不归一化 1 红 /
ID 不含 sessionId 1 红 / 排序二级键去掉 3 红 / 三级键反向 2 红 / 截断阈值 2 红 /
XML 不转义 2 红 / checkpoint 不过滤 stale 5 红 / 晋升不去重 2 红 / 年龄阈值
7 红 / kind 门禁去掉 1 红 / anchor 默认映射 1 红）。

**两次覆盖缺口修正**（红 0 的诊断）：
1. M10（年龄阈值 10→5 分钟）首轮红 0——原用例年龄只有 0 与 20 分钟，**都在
   阈值同侧**。补 3 个边界用例（7 分钟 / 9分59秒 / 10 分钟整）后红 7。
2. `TestPromotionParity` 首版对 `promotion: null` 直接 `continue`——导致「不该
   晋升」语义完全没断言。改用**原始 JSON 键判定**（区分「值为 null」与
   「字段缺失」，二者在 Go 的 `*string` 上都解析为 nil）。

### 第十一刀（已完成）：claim-store 投影层——事件溯源核心（2026-09-19）

✅ `internal/context/claim_store.go`（194 行 + 258 行测试）

**Scope Check**：TS 的 `claim-store` 有 15 个方法，含**写链治理**（异步写 /
梯度重试 / 停链诊断——为治理 Node 事件循环饥饿，issue #61 族）。**Go 的并发
模型不同，不需要复刻这套**。本轮做**投影层**（`applyEventsToMap` 的等价物）
——纯函数、可 oracle 对账，是 store 的核心语义。

对账四种事件的投影语义：

- `claim_proposed` — **幂等**：同 ID 已存在则忽略（不覆盖）
- `claim_status_changed` — 改状态；**新状态非 active 时追加反证**（回到 active
  保留原反证，不追加）
- `claim_used` — 追加消费者（**封顶 50，保留最近**），更新 lastUsedAt
- `claim_boosted` — **覆盖** fitness（事件带结果值，不是增量）

另有 `FilterClaims`（status / kind / scope 三维可选过滤）。

**oracle 对账**：`testdata/claimstore/` 16 个用例。**注意**：TS 的
`applyEventsToMap` 是 private——生成器通过 store 的**公开路径**
（`propose` / `updateClaimStatus` / `recordClaimUsed` / `boostFitness`）间接
导出，不侵入源码。**16 用例 × 2 维度全绿**。

**探针先行**：写实现前先跑了一个 TS 探针（`.rivet/scratch/probe-store.ts`）
确认 5 项行为——幂等 / 反证条件追加 / consumers 封顶保留最近 / lastUsedAt /
boost 覆盖。探针输出与预期一致后才落 Go 实现。**探针已清理**。

**变异反证 9 个：全部有判别力**（propose 不幂等 2 红 / 反证条件去掉 2 红 /
封顶保留最旧 2 红 / 不封顶 2 红 / boost 累加 3 红 / 不更新 lastUsedAt 3 红 /
status 过滤失效 2 红 / kind 过滤失效 2 红 / 未知 claimId 不忽略 2 红）。

**未移植**（下一步）：JSONL 落盘与读回（`appendEvent` 的写链、`readEvents` 的
外部修改检测、checkpoint 快照）。TS 那套为事件循环饥饿设计，Go 侧应用惯用
方式重写，但**格式须与 TS 兼容**（跨版本可读）。

### 第十二刀（已完成）：claim-store 落盘 + 跨版本兼容验证（2026-09-19）

✅ `internal/context/claim_store_io.go`（506 行 + 330 行测试）

**Scope Check**：TS 的写链（异步写 / 梯度重试 / 停链诊断 / 字节数外部修改
检测）是为**治理 Node 事件循环饥饿**设计的（TS 注释引 issue #61 族）。
**Go 的并发模型不同，不复刻**——这里用同步写 + 显式错误返回实现等价语义。
代价：写失败直接返回（TS 是延后诊断），换来无崩溃窗口。

**JSONL 格式逐字节对账**（探针实测 TS 输出后落实现）：

- 键序 = 事件构造序，**`seq` 在末尾**（对账 `{...event, seq}` 展开序）
- **`expiresAt` 未设时省略**（对账 TS 的 `expiresAt?: number`）
- **`evidence.path` 为空时省略**
- eventId 格式：`${eventId}:claim:${claimId}` / `${id}:status:${status}:${now}` /
  `${id}:used:${consumerId}:${usedAt}` / `${id}:boost:${now}`

**跨版本兼容验证（已执行）**：Go 写 JSONL → **TS 读回**，字段全对
（status / fitness / evidence / consumers / counterevidence / tags）。这是双向
兼容的关键证据——Go 侧格式与 TS 逐字节一致。

**测试 10 个**：往返一致性 / JSONL 行格式（键序 + seq 位置 + 省略规则）/
seq 单调与延续 / seq 显式沿用 / markStaleForFile（consistency-check 的真实
副作用）/ 坏行跳过 / boost 累加 / 空 path 省略。

**变异反证 8 个：全部有判别力**（seq 不输出 3 红 / expiresAt 不省略 1 红 /
path 不省略 1 红 / seq 强制重分配 1 红 / 坏行中断 1 红 / markStale 不跳过
已 stale 1 红 / boost 不累加 1 红 / 读回不延续 nextSeq 1 红）。

**三次覆盖缺口修正**（红 0 的诊断）：
1. `evidence.path` 总输出——原用例 path 都非空，补空 path 用例。
2. `seq` 强制重分配——无显式 seq 用例，补 `TestClaimStoreSeqPreservedFromEvent`。
3. `boost` 不累加——首次 boost 时「累加」与「只用 delta」等价（当前值恰为
   基准），需**连续两次** boost 才能判别。补
   `TestClaimStoreBoostAccumulatesCurrent`。

### 第十三刀（已完成）：claim store 接进 CLI——闭环第九刀（2026-09-19）

✅ `cmd/tianshu/main.go` 装配第四件（claim store）+ effects 接线
（+约 60 行）+ 验收测试

**闭环了第九刀留下的 blocked 验收项**：此前 `getFileObservations` 注入空集，
consistency-check hook 无副作用。现在：

- `ClaimStore` 落盘到 `<cwd>/.rivet/claims/<sessionId>.claims.jsonl`
- `getFileObservations` 真实读 `listClaims({kind: ['file_observation']})`
  （对账 TS `loop-factory.ts:691`）
- `Loop.Effects.MarkClaimStale` 接上 `UpdateClaimStatus(id, 'stale', reason)`
  （对账 TS `tool-execution.ts:719`）

**降级是显式的**：claim store 构造失败（目录不可建 / sessionId 空）时走 stderr
并退化为空集——consistency-check 变 no-op，其余功能不受影响。

**用户级验收（已执行）**：`TestCLIClaimStoreWiring`——预置一条引用 foo.ts 的
claim → 喂 postTool 的 write_file 事件 → 观察到 **claim 状态变 stale**、
**反证被追加且 reason 格式正确**、**事件落盘 JSONL**。
`TestCLIClaimStoreWiringNonWriteToolIgnored` 反向验证 read_file 不触发。

**限制如实说明**：这不是「真实二进制从会话产生 claim」的端到端——Go 侧还没有
claim 提取器（从会话事件产生 claim 的模块），故测试直接构造 CLI 所用的**同一
装配**并喂事件。**验证的是装配正确性**，不是 claim 产生链路。

**过程发现**：`internal/context` 包名与标准库 `context` 冲突，CLI 里需别名
（`ctxstore`）。

### 第十四刀（已完成）：claim 提取器——补上产生端，闭环真实链路（2026-09-19）

⚠️ **先修正上一轮的失真**：我上轮把「用户在真实会话中 claim 被标记过期」标为
`met`，但系统提醒后核实发现 **`Propose` 无生产调用方**——claim store 在真实
会话里**永远是空的**。上轮测试是手工构造装配喂事件，验证的是「接线」，不是
「用户路径」。**这是同一模式的第四次**（前三次：hook 未接 loop / bus 无调用方 /
CLI 未装配）。已诚实核销为 blocked。

✅ `internal/context/claim_extractor.go`（430 行 + 245 行测试）+
`Loop.OnToolResult` 回调 + CLI 装配

**对账 `claim-extractor.ts`（206 行）**——五种提取路径：

- `read_file`（成功）→ `file_observation`（有 `existingFileObservations` 去重）
- `run_tests` / `bash`（测试命令）→ 失败 `failure_pattern` / 通过 `verification_fact`
- `bash` 失败含安全关键词 → `security_finding`
- `git commit` / `deliver_task(commit)` 且**显著** → `decision`（commit fact）
- 其余 → 空

**易错点已对账**：TTL 表（含 4 个 `Infinity` kind）/ `SKIP_TOOLS` 七个 /
EXPORT_RE 抽符号（含 `export { a, b as c }` 的别名）/ COMMIT_HASH_RE 的
**方括号锚定**（TS 注释：旧写法 `/\b[0-9a-f]{7,40}\b/` 曾把 ~61% 的 hash
误解析到错误的 commit）/ 显著提交判定（关键词 **或** stat 行 ≥ 3）。

**oracle 对账**：`testdata/claims/extractor-oracle.json` 27 用例。
**生成器 scrub 时间字段**（`createdAt`/`expiresAt` → 相对 TTL）——含时间戳的
oracle 不可复现。27 用例全绿。

**设计**：`internal/agent` **不直接 import** `internal/context`——用注入回调
`Loop.OnToolResult`（与 consistency-check 的 `getFileObservations` 同模式）。
分层方向保持：装配在 CLI，agent 包独立。

**用户级验收（已执行，真实二进制）**：

- `TestCLIEndToEndClaimProduced`——跑真实 CLI，模型 `read_file` 读源文件 →
  **磁盘上出现 `file_observation` claim**，文本
  `widget.ts (4L): renderWidget, WIDGET_NAME, Widget`（符号提取正确）
- `TestCLIEndToEndClaimLifecycle`——**完整生命周期**：第一轮 `read_file`
  产生 claim → 第二轮 `write_file` 改同一文件 → JSONL 里出现
  `claim_status_changed` 到 `stale`。**这是从产生到消费的完整链路**

**变异反证 4 个：全部有判别力**（提案不落盘 2 红 / OnToolResult 提前返回 2 红 /
loop 不调回调 2 红 / MarkClaimStale 不落盘 1 红）。

**下一步**：`AdvisoryReadback`（采纳率台账）——解锁习惯化 / efficacy / lift /
holdout 四个治理子系统。

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
