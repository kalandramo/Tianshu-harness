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

### 第十五刀（已完成）：AdvisoryReadback——四个治理子系统的共同前置（2026-09-19）

✅ `internal/agent/advisory_readback.go`（570 行 + 395 行测试）

**对账 `advisory-readback.ts` 核心路径**：`track`（送达跟踪）/ `observeTool`
（行为观察）/ `evaluate`（核销评估）/ 谓词求值 / 查询方法。

**三种核销语义（易错点已对账）**：

1. **正负谓词的判定时机不同**——`pattern_absent`（负向）**只在到期时判**
   （过早读文件会把「还没来得及清」误判为忽略）；其余（正向）窗口内满足即
   adopted、到期未满足才 ignored。
2. **shadow 隔离**——反事实组只进 `shadowHeld`/`shadowSatisfied` 桶，
   **不动 adopted/ignored/streak**（TS 注释：不污染副驾闸门与习惯化）。
3. **`flushAtSessionEnd` 不把未到期判 ignored**——TS 注释：advisory 在末轮送达
   时模型没走完窗口，判忽略会把「没机会响应」记成「听了不做」。这类假 ignored
   经 ignoredStreak / efficacy / 跨会话 lift 三条路径压低效力评分，最终静音掉
   本可能有效的提醒。**worker 尤其吃这一刀**（中位只跑 2 轮）。

**其他对账点**：`DEFAULT_WINDOW` 五值 / `TOOL_FAMILY` 映射 / `courseSignature`
（read/edit 族带文件面，其余只看族）/ `EVENT_RETENTION_TURNS=8` 按轮修剪（不按
条数）/ `getLift` 公式与 nil 条件 / `getAdoptionRate` 的会话+先验合并口径。

**oracle 对账**：`testdata/readback/` 24 用例，**28 个子测试**全绿。

**变异反证 8 个：全部有判别力**——adopted 不清零 streak 1 红 / shadow 不隔离
2 红 / 负向谓词不到期就判 2 红 / deadline off-by-one 3 红 / 事件不修剪 1 红 /
tool_appears 忽略 targetIncludes 1 红 / lift 在 shadowHeld=0 也返回 2 红 /
flush 不报 unresolved 1 红。

**三次用例设计修正**（红 0 的诊断，都是我的测试没覆盖到判别点）：
1. `event_trim` 原用例的窗口是 `[10,10]`，turn 0 的事件**本就不在窗口内**——
   裁不裁都不影响。改用 `withinTurns: 12` 的宽窗口让旧事件落进观察窗。
2. 首版尝试用 `course_changed` 判别，但前置窗是 `[7,10)`，turn 0 的事件同样
   不在其中——仍是等价用例。

**状态：库级交付，尚未接进 CLI**。`AdvisoryReadback` 目前无生产调用方——
下一刀接进 loop（render 后 `track`、postTool `observeTool`、postTurn `evaluate`、
postSession `flushAtSessionEnd`），再接四个治理子系统。

### 第十六刀（已完成）：readback 接进 loop——消除悬空 + 核销闭环（2026-09-19）

✅ `Loop.Readback` 字段 + **四个调用点** + CLI 装配

**消除第十五刀的悬空**：`AdvisoryReadback` 此前无生产调用方。现在四个调用点
全部接好（对账 TS 的三处触发 + 会话结束）：

| 时机 | 调用 | 对账 TS |
|---|---|---|
| render 后 | `Track(delivered, 0)` | turn-step-producer |
| postTool | `ObserveTool{Turn,Name,Target,IsError}` | tool-execution |
| postTurn | `Evaluate(turn)` | postTurn |
| 会话结束（两条路径）| `FlushAtSessionEnd` | postSession |

**`pattern_absent` 的文件读取器**在 CLI 注入 `os.ReadFile`（读失败返回空串，
对账 TS defaultReadFile 的 null 分支 → 视为满足）。

**用户级验收（已执行）**：

- `TestReadbackWiredInLoop`——loop 跑一轮，hook 投递带 `expect` 的 advisory，
  模型跑了 `run_tests` → **实测 `采纳率=1 stats={Delivered:1 Adopted:1 ...}`**
  （送达被跟踪 + 谓词被核销 + adopted 记账正确）
- `TestReadbackFlushOnNormalExit`——正常收尾路径也 flush，未到期的**不误判
  ignored**
- `TestReadbackEvaluatePerTurn`——**逐轮 Evaluate 生效**：实测
  `ignoredStreak=2`（窗口 1 轮 × 2 轮未满足）

**变异反证 4 个：全部有判别力**（render 后不 Track 1 红 / postTool 不 ObserveTool
1 红 / postTurn 不 Evaluate 1 红 / 正常收尾不 flush 1 红）。

**一次覆盖缺口修正**（M3 首轮红 0）：原用例的场景里 `run_tests` 在第 1 轮就执行，
即使不调逐轮 `Evaluate`，最后 `FlushAtSessionEnd` 也会判。补了
`TestReadbackEvaluatePerTurn`（窗口 1 轮 × 多轮不满足 → `ignoredStreak` 必须 > 1）
后才判别出「只在会话结束判一次」这个缺陷。

**已知偏差**：`Track` 的 turn 传 **0**——TS 用的是 session turn（非 run 局部
序号），Go 侧 `buildRequestMessages` 当前无 turn 参数。这会**低估多轮场景下的
时间跨度**（窗口判定偏保守）。已在代码注释标注，见 HANDOFF 的 turn 时钟统一项。

### 第十七刀（已完成）：习惯化对抗——readback 的下游消费者（2026-09-19）

✅ bus 的两级反应 + readback 接线 + 5 个 oracle 用例

**消除第十六刀遗留的 blocked 验收**：readback 提供了 `GetIgnoredStreak`，但无人
消费——「连续被忽略的提醒被静音」这个用户级行为不存在。现在接上了。

对账 TS 的 P1b 习惯化对抗段，**两级反应**：

| 条件 | 反应 | 理由（TS 注释） |
|---|---|---|
| `streak >= 2` | 升级措辞：条目前加「此提醒已连续 N 次未见执行——若你有意跳过请在回复中说明理由」 | **被忽略的事实本身是新信息**，比原文重复更能穿透注意力习惯化 |
| `streak >= 3` | 有界静音 N=4 个渲染周期 | 连续无效的提醒是纯噪音 |
| `tier == constitutional` | 永不静音 | 宪法级条目不受习惯化抑制 |

**probation 放行**（`streak > lastSilencedStreak[key]`）：静音期满放行一次，
若那次被采纳则 streak 清零恢复正常；仍被忽略（streak 增长）才再次静音。
**没有这个条件会陷入永久静音**——streak 不涨时每轮重新触发静音。

**接线**：`bus.SetHabituationPolicy(readback)`——readback 的
`GetIgnoredStreak(key) int` **精确匹配** `HabituationPolicy` 接口，无需适配器。

**用户级验收（已执行）**：`TestHabituationInRealRequests`——模型连续 8 轮不执行
被提醒的动作，观察**真实请求体**里该 advisory 的出现模式，实测：

```
present  =[false true true true false false false false true]
escalated=[false false false true false false false false true]
stats={Delivered:4 Adopted:0 Ignored:4 IgnoredStreak:4}
```

第 1-3 轮出现（第 3 轮起带升级措辞）→ **第 4-7 轮消失**（静音）→ 第 8 轮回归
（probation）。静音轮不计入 Delivered（没渲染就不该记送达）。

**变异反证 5 个：全部有判别力**（不升级措辞 / 不静音 / constitutional 不豁免 /
去掉 probation / 静音永不到期——各红 1~4）。

**一次变异反证的自纠**：H3/H4/H5 首轮红 0，我误判为「测试缺口」。实际是
`-run 'TestHabituation'` **漏了 oracle 测试**（用例名是
`TestAdvisoryBus*Parity/habituation_*`）。改用 `-run 'TestHabituation|TestAdvisoryBus'`
后各红 4。**教训：变异红 0 先查 -run 模式是否覆盖了全部相关测试**，再下「测试
有缺口」的结论——这是「变异未生效」之外的第九类红 0 成因。

**oracle**：`advisorybus` 数据集 32 → **37 用例**（+5：escalate / below_escalate /
silence / constitutional_exempt / mixed）。生成器新增 `streaks` 字段模拟
`getIgnoredStreak`。

### 第十八刀（已完成）：统一 turn 时钟——修复已知偏差（2026-09-19）

✅ `Loop.SessionTurn()` + 四个调用点统一 + flush 粒度修正 + 6 个验收测试

**修复的偏差**：第十六刀遗留的「Track 传 0」。取证后发现**比描述的更严重**——
不是「传 0」一个点，而是**两个语义不同的时钟被混用**。

**TS 的权威语义**（三处铁证）：

| 来源 | 内容 |
|---|---|
| `context.ts:360` | `turnCount = messages.filter(m => m.role === 'user').length` |
| `context.ts:202` | `turnCount++` 在 `addUserMessage` 内 |
| `loop-factory.ts:516` | `buildRuntimeSnapshot: turn: self.session.getTurnCount()` |

即 **session turn = 历史里 user 消息条数**，**在单个 Run 内恒定**，跨 user 轮才推进。

**TS 作者的原警告**（`turn-step-producer.ts:682-688`）：

> 必须与 runtime hook snapshot 使用同一 session turn 时钟；这里的 `turn` 是
> TurnOrchestrator.run 局部序号，而 postTool/postTurn 观察事件使用 session
> turn。混用会让 B2 在局部 turn=13 送达、事件却落在 session turn=2，
> **course_changed 永远无法核销**。

**移植踩了同一个坑**，且是两处：

1. `hook_snapshot.go` 的 `Track(delivered, 0)` 硬编码
2. `loop.go` 的 `ObserveTool`/`Evaluate` 用 run 局部递增 turn

**探针实证**（TS 真实代码）：session turn 恒定时 `evaluate(t)` 恒返回 0——
证实窗口靠**跨 user 轮**推进，不是 run 内模型轮。

**修复**：

- 新增 `Loop.SessionTurn()`——按 user 消息计数（对账 `context.ts:360`）
- 四个调用点统一：`Track` / `ObserveTool` / `Evaluate` / `FlushAtSessionEnd`

**第二个缺陷（测试暴露的）**：`FlushAtSessionEnd` 原先挂在 **Run 的正常收尾
路径**，而 TS 的 postSession 是**整个会话结束**。Go 的 `Run` = 一个 user 轮，
所以每个 user 轮都 flush，把未到期的 pending 清空——表现为 `Adopted:0 Ignored:0`
（既没采纳也没忽略，观测凭空消失）。

修正：移到 `FlushSession()`（CLI 在两处会话结束点调用）。并让它**返回
unresolved 列表**（TS 会写遥测，此前被丢弃——观测价值丢失）。

**用户级验收（已执行）**：

| 测试 | 实测 |
|---|---|
| `TestSessionTurnSemantics` | 3 个 Run 后 SessionTurn=3，逐个递增 |
| `TestSessionTurnConstantWithinRun` | `Run 内各轮观察到的 SessionTurn: [1 1 1]` |
| `TestClockUnifiedInProductionPath` | 跨 user 轮核销 `Adopted:1` |
| `TestTrackUsesSessionTurnNotZero` | 送达前的 run_tests **不被误判采纳** |
| `TestReadbackEvaluateAcrossUserTurns` | `ignoredStreak=2`（跨 user 轮累积） |
| `TestFlushSessionRedeemsAtSessionEnd` | 未到期作 unresolved 报出，不误判 ignored |

**变异反证 5 个：全部有判别力**（Track 退回 0 红 1 / SessionTurn 恒 0 红 3 /
FlushSession 不核销 红 1 / Evaluate 退回局部 turn 红 2 / ObserveTool 退回红 1）。

**两个既有测试按新语义重写**（它们断言的是我上一轮基于错误时钟写的期望）：
`TestReadbackWiredInLoop` 改为断言「单 Run 内窗口不闭合是**正确行为**」；
`TestReadbackEvaluatePerTurn` → `TestReadbackEvaluateAcrossUserTurns`（跨 user 轮）。

**方法论收获**：探针（`.rivet/scratch/probe_clock.ts`，已清理）在写测试前就把
「session turn 恒定 → evaluate 恒不判定」这个反直觉断言钉死了。若先写测试，
极可能按错误时钟的直觉构造出**自洽但无判别力**的用例——事实上我第一版
RED 测试正是如此（合成递增 turn，自洽通过）。

### 第十九刀（已完成）：lift 消费——负 lift 静音（2026-09-19）

✅ `GetMatureLift` + 先验字段补齐 + bus 的 lift 静音 + 6 个 oracle 用例

**与习惯化的区别**（这是本刀的核心语义）：习惯化看的是「**连续被忽略**」
（行为层信号，streak）；lift 看的是**反事实基线**——投递组采纳率 减 扣留组
自发完成率。**lift ≈ 0 意味着「没提醒模型也会做」→ 提醒是纯噪音**。

**静音时长更长**（10 vs 4 个渲染周期）：lift 基于反事实证据，结论更可靠，
不需要那么频繁地重新试探。

**三处对账点**：

| 项 | TS 来源 | 值/语义 |
|---|---|---|
| 静音周期 | `LIFT_MUTE_RENDERS` | 10 |
| 静音阈值 | `LIFT_MUTE_THRESHOLD` | 0（**≤ 0 都静音**） |
| 成熟度门 | `MATURE_LIFT_MIN_DECIDED` / `_SHADOW` | 5 / 3 |

**修复的两个既有缺口**（取证时发现的）：

1. **`EfficacyPriorCounts` 缺 3 个字段**——TS 有 5 个（`delivered`/`adopted`/
   `ignored`/`shadowHeld`/`shadowSatisfied`），Go 只有 2 个。没有 shadow 两字段
   就无法算反事实基线。
2. **`GetDeliveredCount` 漏算先验**——TS 是 `(stats?.delivered ?? 0) +
   (priors?.delivered ?? 0)`，Go 只返回会话统计。**holdout 资格判定依赖此数**，
   漏掉先验会让「送达 >=N 次才开始抽样」永不满足。

**用户级验收（已执行）**：

- `TestLiftMuteInRealRequests`——真实请求体里实测
  `present=[false ×11, true, false ×12]`：**静音 11 轮后 probation 放行**，
  前置断言确认成熟 lift = -1
- `TestMatureLiftGate`——成熟度门 5 个子用例（无样本 / decided 不足 /
  shadow 不足 / 达标正值 / 达标负值）全绿。**样本不足必须返回 nil（中性）**：
  不设门会让冷启动阶段误杀有效提醒
- `TestLiftExemptNotMuted`——三类豁免（constitutional / immediate /
  star_domain）各一个子用例

**变异反证 3 个有判别力**（豁免失效红 4 / nil 也静音红 4 / 成熟度门失效红 4）。
L1（不静音）变异导致编译失败（`lift` 变量未使用）——属「编译失败伪装」类，
不计判别力。

**一次工具缺陷的自纠**：变异脚本只备份了 `advisory_bus.go`，而 L4 变异改的是
`advisory_readback.go`——恢复时漏掉，导致残留的 `if false {` 让全量出现 3 个假
FAIL。**教训：变异脚本的备份/恢复必须覆盖该轮所有被改文件**，不能只备份主文件。

**oracle**：`advisorybus` 37 → **43 用例**（+6：mute_negative / mute_zero /
positive_no_mute / null_neutral / exempt_tiers / mixed）。

### 第二十刀（已完成）：holdout 反事实抽样——shadow 样本的产生端（2026-09-19）

✅ bus 的抽样逻辑 + 资格白名单 + CLI 装配 + **修掉一个既有接线缺陷**

**这解除了第十九刀标 blocked 的那条验收**：上一刀的 lift 静音机制虽已实现并
接线，但 **shadow 样本恒为 0**（抽样未移植 + 先验无播种）→ `GetMatureLift`
恒返回 nil → 静音永不触发。本刀补上**产生端**。

**为什么需要反事实**：采纳率度量的是**相关性**——「送达后 2 轮内出现验证」
可能只是模型本来就要做。按小概率静默扣留（不渲染但**照常核销**）得到「没提醒
也会做」的基线，`lift = 投递组采纳率 - 扣留组自发完成率` 才是**因果**增益。

**三处对账点**：

| 项 | TS 来源 | 值 |
|---|---|---|
| 抽样率 | `DEFAULT_HOLDOUT_RATE` | 0.1（`RIVET_ADVISORY_HOLDOUT` 覆盖） |
| 资格门 | `HOLDOUT_MIN_DELIVERED` | 3（冷 key 先积累投递组基数） |
| 资格白名单 | `advisory-bus.ts:1147-1151` | constitutional / immediate / star_domain / 无 expect 谓词 |

**修掉的既有缺陷（本刀最有价值的发现）**：

`hook_snapshot.go` 的 `buildRequestMessages` 在 `block == ""` 时**提前 return**，
导致 `Track` 从未执行。而 holdout 把唯一条目扣留后 block 恰为空——
**shadow 样本全部丢失**。对账 TS（`turn-step-producer.ts:690`）：
`drainDelivered()` + `track()` 在 `render()` 之后**无条件**调用。

这个缺陷**只有在 holdout 存在时才会显形**——之前没有任何条目会被扣留，
block 空时也确实没有 delivered 可 drain，所以一直没暴露。

**用户级验收（已执行）**：

- `TestHoldoutProducesShadowSamples`——真实 loop 实测
  `stats={Delivered:0 ShadowHeld:2}`（抽样率 100%，扣留 2 次且无真实送达）
- `TestHoldoutEligibilityWhitelist`——6 个子用例（普通合格 / constitutional /
  immediate / star_domain / 无谓词 / isEligible=false），Rate=1.0 隔离资格判定
- `TestHoldoutDisabledByRateZero`——rate=0 时正常送达、无 shadow
- `TestParseHoldoutRate`——9 个边界（空 / 0 / 0.5 / 1 / 越界 / 负 / 非法 / NaN / Inf）

**变异反证 5 个有判别力**（不做抽样红 7 / 无谓词也抽样红 2 / 扣留计入 dropped
红 3 / block 空时提前 return 红 1 / immediate 不豁免红 2 / star_domain 不豁免红 1）。
constitutional 那条红 0 是**等价变异**——constitutional 在更早的 tier 分流就走
`constDeduped` 独立路径，从不进入 `taken`，故该条件不可达（冗余防御）。

**oracle**：`advisorybus` 43 → **52 用例**（+9 holdout）。

### 第二十一刀（已完成）：跨会话效能信息素——先验的加载与写回（2026-09-19）

✅ `AdvisoryEfficacyStore`（JSONL + EWMA + 原子写 + 锁）+ flush 增量差分 + CLI 装配

**这补齐了治理链的最后一块**：`SeedPriors` 此前无生产调用方——readback 的
per-key 统计随会话死亡，每个新会话都要从零攒（holdout 资格需送达 >= 3，
成熟 lift 需 decided >= 5 且 shadow >= 3）。**真实会话往往没那么长**，
所以没有先验时这条链在冷启动阶段形同虚设。

**对账点**：

| 项 | TS 来源 | 值/语义 |
|---|---|---|
| 半衰期 | `HALF_LIFE_MS` | 14 天 |
| 剪枝阈值 | `PRUNE_THRESHOLD` | 0.05（各计数全低于此值则剔除） |
| 容量 | `MAX_KEYS` | 200（按 delivered+shadowHeld 降序） |
| 写回触发 | `turn-step-producer.ts:776` | `turn > 0 && turn % 20 === 0` |
| 兜底 | `loop.ts:2401` | postSession |

**增量差分**（`loop.ts:443` `flushAdvisoryEfficacy`）：`lastEfficacyFlush`
是差分基线——mergeAndSave 内部是 `base[f] += delta[f]`，传累计值会让计数翻倍。

**修掉一个真实偏差（间歇性测试暴露的）**：

`GetDeliveredCount` 原先返回 `int`（对 stats + priors 求和后截断）。TS 返回
**number（浮点）**。跨会话先验经 EWMA 衰减后是小数（如 2.9999…），`int()`
截断成 2——**永远够不到 holdout 资格门 3**。已改为 `float64`。

这个偏差不是靠读代码发现的，而是靠**连跑 8 次全量测试**暴露的间歇性失败
（约 3/8 概率）。单次跑绿会掩盖它。

**用户级验收（已执行）**：

- `TestCrossSessionPriorSeeding`——会话 A 写回 → 会话 B 启动时继承计数
- `TestEfficacyFlushIsIncremental`——重复 flush **不翻倍**（增量差分正确）
- `TestEfficacyFlushAtSessionEnd`——短会话（< 20 轮）靠 FlushSession 兜底
- `TestEfficacySeedFeedsLift`——端到端：先验 → `GetMatureLift = -1`（负值会触发静音）
- `TestEfficacyPersistenceFormat` / `TestEfficacyConcurrentWritesSerialized`
- oracle 对账 11 个子用例（5 decay + 4 prune 边界 + 3 merge）+ maxKeys + round3

**变异反证 5 个有判别力**（不衰减红 7 / load 不剪枝红 1 / 不截断 MAX_KEYS 红 1 /
基线不推进红 1 / 漏算先验红 1）。

**oracle**：新增 `efficacy` 数据集（13 用例）。**只对账纯逻辑**——文件 IO /
锁 / 原子写是平台相关的（Node 的 renameSync vs Go 的 os.Rename），逐字节对账
无意义。

**方法论收获（重要）**：**间歇性失败必须连跑多次才能暴露**。本刀的
`GetDeliveredCount` 截断偏差与两处时间敏感断言，单次跑全绿、连跑 8 次才现形。
判据：涉及时间/浮点/并发的改动，全量测试至少连跑 5 次。

### 第二十二刀（已完成）：T7 效力排序——效力跨 priority 参与预算竞争（2026-09-19）

✅ `efficacyAdjust` + `effectivePriority` + `secondaryScore` + 三个注入点 + 8 个 oracle 用例

**修掉的真实问题**（TS 注释原文，本刀取证时发现）：

> 此前只在 priority 完全相等时做 tie-break，而 hook 的 priority 高度分散——
> **实测 self-verify 采纳 77% 却因 0.58 < 0.70 恒输给采纳 12% 的 todo-missing**。

**四段逻辑**（全部对账 TS）：

| 组件 | TS 位置 | 语义 |
|---|---|---|
| `efficacyAdjust` | `advisory-bus.ts:1088` | `(score-0.5) × 2 × span × confidence`，豁免集返回 0 |
| `effectivePriority` | `:1097` | 有界调整 clamp 到 `[0.05, 0.79]` |
| `secondaryScore` | `:1079` | 平手时的次级键：成熟 lift → 采纳率 → 0.5 |
| `compareEntries` | `:1106` | 先比有效优先级，再比次级键 |

**三处边界（都是易错点）**：

1. **豁免集**（constitutional / immediate / star_domain）返回 0 → 优先级**原样透传，
   不进 clamp**。否则 `CONSTITUTIONAL_PRIORITY(0.9)` 会被压到 0.79。
2. **上限 0.79**——不得触及 0.8 的 efficacy fail-open 豁免线。
3. **下限 0.05**——保留参赛资格，**不等于静音**（与习惯化/lift 的静音是两回事）。

**置信度缩放**：`confidence = min(1, decided / EFFICACY_CONFIDENT_SAMPLES)`——
**避免单样本改写优先级**。成熟 lift 已过样本门故满置信。

**用户级验收（已执行）**：

- `TestEfficacySortingInRealRequests`——真实请求体实测
  `渲染顺序观测=[high-eff 在前 high-eff 在前]`：**0.58 的 87.5% 采纳率压过
  0.70 的 12.5%**（span=0.15 时 0.58+0.15=0.73 > 0.70-0.15=0.55）
- `TestEfficacyExemptInSorting`——三类豁免各一子用例，验证「原样透传」
- `TestEfficacyPriorityBounds`——上下限各一（0.75+0.15 → 0.79；0.10-0.15 → 0.05）
- `TestEfficacyConfidenceScaling`——confident 0.75 vs shaky 0.63（精确值）
- `TestEfficacySpanZeroDisables` / `TestParseEfficacySpan`（8 个边界）

**变异反证 5 个有判别力**（不做调整红 6 / 豁免失效红 4 / 不 clamp 红 1 /
忽略置信度红 3 / 零调整也 clamp 红 2）。

**oracle**：`advisorybus` 52 → **60 用例**（+8 efficacy）。

**一处 TS 的测试基建约束**：`efficacySpan` 是 `readonly` + 从 env 读，
**无运行时注入钩子**——生成器改用 `process.env.RIVET_ADVISORY_EFFICACY_SPAN`
逐用例设置。Go 侧我用 `SetEfficacySpan` 显式注入（更直接，且不污染进程环境）。

### 第二十三刀（已完成）：efficacy 负反馈环——负向臂 + 正向臂（2026-09-19）

✅ 三阶段阈值 + fail-open + 冷却翻倍/减半 + 排序加成回填 + 8 个 oracle 用例

**这一刀补全了 efficacy 子系统**（上一刀只做了 T7 排序）。

**三阶段**（对账 `advisory-bus.ts:869-932`，**零采纳前提**）：

| 条件 | 行为 |
|---|---|
| `delivered >= 3` | 冷却翻倍（2→4→8…），本次放行、下次进入冷却 |
| `delivered >= 6` | **会话内静默**（不再渲染） |
| `adopted >= 3` | 正向臂：冷却减半 + 排序加成 +0.05（cap 0.79） |

**与习惯化静音互补**（TS 注释原文，这是本刀存在的理由）：

> 习惯化依赖 `ignoredStreak`，而它依赖 expect 谓词——**无 expect 的 key
> （如 convergence 的多数变体）ignored 永远是 0，只有这条环能拦住它**。

**fail-open**：`constitutional` / `priority >= 0.8` 的条目**永不受负反馈约束**
——高优先级提醒不该被统计意义上的「无效」静默掉。

**口径差异（易混淆点）**：负反馈环用**会话内统计**（不含跨会话先验），
而 T7 排序含先验（`GetDeliveredCount` 合并 priors）。两者看的是不同的东西：
前者是「本次会话里说了几次没人听」，后者是「历史上这条提醒整体有效吗」。

**不 mutation `e.Priority`**（TS 注释原文）：`alive` 跨渲染周期持有同一批引用，
原地 `+= 0.05` 会复合累加，且 0.85 越过 0.8 fail-open 线导致**永久逃逸负向臂**。
加成只在 `effectivePriority` 里按需计算（本刀回填了上一刀缺的 `positiveArmKeys`）。

**用户级验收（已执行）**：

- `TestEfficacyCooldownInRealRequests`——真实请求体实测
  `present=[true false true false false false true ...]`：**冷却间隔缺席可见**
- `TestEfficacySilenceInRealRequests`——delivered=6 零采纳 → 完全静默
- `TestEfficacyFailOpenExempt`——三类豁免各一子用例（含 `priority = 0.8` 边界）
- `TestEfficacyPositiveArmInSorting`——采纳 3 次的 0.66 胜出零采纳的 0.70
- `TestEfficacyStatsExcludesPriors`——**口径验证**：播种大量先验不影响负反馈环
- `TestEfficacyNoStatsProvider`——无 provider 时不做约束（缺省行为）

**变异反证 6 个有判别力**（不做环红 10 / fail-open 失效红 10 / 不静默红 5 /
口径错红 4 / 正向臂阈值失效红 3 / 加成失效红 3）。

**oracle**：`advisorybus` 60 → **68 用例**（+8）。

### 第二十四刀（已完成）：compact 策略层 + PressureMonitor（2026-09-19）

✅ 新包 `internal/compact`——三策略分层 + 五档判定 + 压力监控（19 包）

**这一刀为「前缀缓存」支柱打地基**：`PressureMonitor` 是边界压缩的决策输入，
而压缩时机直接影响缓存命中率。

**为什么独立成包**：`PressureMonitor` 依赖压缩策略，而 `internal/agent` 不该
反向依赖它——两者是并列的消费方。

**对账范围**（`src/compact/constants.ts` + `src/context/compact-policy.ts` +
`src/context/pressure-monitor.ts` 的策略判定部分）：

| 组件 | 语义 |
|---|---|
| `StrategyForCacheType` | `exact-prefix && persistent` → cache-preserving；`none` → aggressive；其余 balanced |
| `CompactPolicyRatiosFor` | 三策略各四档比值（如 cache-preserving 是 0.72/0.86/0.92/0.95） |
| `AdaptiveCompactPolicyRatios` | 命中率 ≥0.85 各档上移、<0.3 下移；**ceiling 恒不调整** |
| `PrecisionCeilingRatio` | 大窗口 0.7 / 中窗口 0.55 / 小窗口 1（无天花板） |
| `TierForRatio` | 五档判定 |
| `PressureMonitor.Check` | 八字段 + pressureRelative + suggestion |

**三处易错点（都有 oracle 锁定）**：

1. **精度天花板必须是地板，不是回退分支**。当它（0.70）低于 cache-preserving
   的 watch（0.72）时，早先的 `return 1` 会遮蔽它，让 ladder **非单调**——
   ratio 0.71 压缩而 0.75 只 watch。oracle 的 `precision-ceiling-floor`
   用例锁定：0.71 → **tier 2**。
2. **相对压力必须取 log2**（TS 注释原文）：`min(1, ratio/p90)` 在单调增长时
   恒被钉死在 1.0——实测 901 轮里 662 轮（73.5%）pressure 恰为 0.50，该维度
   不再携带信息。改用「超出倍数」的 log2：持平基线 0，2 倍为 1.0。
3. **p90 是「排序后按下标取」而非插值分位**（`sorted[floor(len*0.9)]`）——
   用插值会让 pressureRelative 漂移。

**用户级验收（已执行）**：oracle 对账 8 组共 60+ 子用例全绿：

- `TestCompactPolicyRatiosParity`——8 种 profile 组合
- `TestAdaptiveRatiosParity`——9 个命中率（含 0.85 / 0.3 两边界）
- `TestPrecisionCeilingParity`——10 个窗口/override 组合
- `TestTierForRatioParity`——17 个 ratio（含非单调性用例）
- `TestPressureCheckParity`——4 条 token 序列 × 八字段
- `TestCvmThrottlingParity`——5% 阈值 + 8% 天花板
- `TestThrashingDetectionParity`——5 个用例（含 4 轮边界）
- `TestP90Semantics` / `TestReasonForTierParity` / `TestCvmBySourceInvariant`

**变异反证 6 个有判别力**（精度天花板失效红 3 / ceiling 档失效红 5 /
策略分层失效红 15 / 相对压力不取 log2 红 3 / 抖动检测失效红 3 / fastGrowth 失效红 4）。

**oracle**：新增 `pressure` 数据集。

**下一步**：`internal/compact` 的**压缩执行**部分（`decideCompactAction` +
熔断器状态机 + `CompactThresholds`），或 `internal/context` 其余子系统
（CognitiveLedger / Stigmergy / task-contract）。

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
