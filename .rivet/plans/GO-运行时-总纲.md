# Go 运行时移植 — 总纲（单一入口）

> **本文档是唯一入口**。此前 `.rivet/plans/` 下散落 10 份计划（3928 行），
> 状态与真实进度严重脱节——本文件收编它们，给出**一份可信的现状 + 待办**。
>
> | 项 | 值 |
> |---|---|
> | 分支 | `go-runtime` |
> | HEAD | `4f0a3e5a` |
> | 上次同步 | `origin/main` @ `6dbf1e04`，领先 0（已完全同步） |
> | 实测于 | 2026-09-27（第 108 刀后） |
>
> **进度权威**：本文 §2 的实测数字 + `.rivet/HANDOFF.md`（2028 行，每刀一个专章，**只查细节**）。
>
> ⚠️ **§2 的数字是「实测于 `<HEAD>`」的快照**——状态文档的实测值天然短时效。
> 引用前**先跑 §2 的复现命令自测**；若与文档不符，以自测为准并顺手订正本文件。

---

## 1. 一句话目标与硬约束

把 TS 版天枢运行时（`src/`，**288,211** 行）**行为等价**地移植成 Go（`go/`）。

**硬约束（不是优化项）**：前缀缓存工程是核心指标——冻结 system prompt 必须**字节稳定**。
任何进入冻结前缀的字符串差一个字节，缓存命中率就崩。故文本资产一律走
**oracle 字节对账**（生成器从真实 TS 代码路径导出 golden），绝不手抄。

**非目标**：不重写 TS、不追求「功能更多」。任何偏离 TS 语义的「优化」都是缺陷。

---

## 2. 当前真实进度（实测，非估计）

> 实测于 `4f0a3e5a`（第 108 刀后）。**数字随每刀变动**——引用前先跑下方复现命令。

| 维度 | Go | TS 对照 | 覆盖 |
|---|---|---|---|
| 生产代码行 | **65,940** | 288,211 | **23%** |
| 测试代码行 | 74,249 | — | 测试 : 生产 = **1.13 : 1** |
| 测试函数 | 2,391 | — | — |
| oracle 数据集 | 72 | — | 字节对账基线 |
| internal 包 | 28 | src 顶层 36 目录 | 缺 20 个 |
| 工具（CLI 装配） | **40**（无 LSP）/ 42（含 LSP） | full preset 目标 **51** | ~82% |
| 提交数 | 1094（`go/` 触及 299） | — | — |
| 计划文件 | 11 份（含本总纲） | — | 其中 10 份已收编，见 §9 |
| 全量测试 | **exit=0 / 30 包 ok / 0 FAIL** | — | 32 含 2 无测试包 |

**复现命令**（仓库根下直接粘贴）：
```bash
git rev-parse --short HEAD && git rev-list --count HEAD
find go -name '*.go' -not -name '*_test.go' -not -path '*/testdata/*' | xargs wc -l | tail -1
find go -name '*_test.go' | xargs wc -l | tail -1
find src -name '*.ts' -not -name '*.test.ts' -not -path '*/__tests__/*' | xargs wc -l | tail -1
grep -rh '^func Test' --include='*_test.go' go/ | wc -l
ls go/testdata/ | wc -l && ls go/internal/*/ -d | wc -l
cd go && go test ./... -count=1
```
工具数需经 `Definitions()` 实测（`grep -c 'r.Register('` 含循环注册，不准）。

**必须建立的认知**：不是「快完成了」，而是**地基已夯实、主体未动**。
已完成的那 23% 是**纵深**的（每个子系统带 oracle + 变异反证），
未动的是**整片表面层**。

---

## 3. 已完成能力全景

**内核**（`src/agent/` 对应）— Agent 主循环、turn 编排、证据门禁、交付门禁、工具调用管线
**认知虚拟机**— 五阶段 hook 管线 + 已移植的各 hook 模块、advisory bus 及其治理子系统
（去重/排序/预算/习惯化对抗/efficacy 负反馈/lift 静音/holdout 反抽样）、claims 事件溯源、
AdvisoryReadback 核销
**审批门**— 档位门、bash 写门、denylist/allowlist、pathGrant、selfKill、
request_path_access（含修的「授权形同虚设」跨模块缺陷）
**压缩**— 策略层（五档判定/压力监控）、执行层（micro/语义折叠/熔断器）、session split 闭环
**缓存**— `internal/cache` advisor + warmth
**工具**（40/42）— 文件读写 5、检索 3、勘察 5、git 家族 3、执行 2、交互/计划/撤销、
办公文档 5、网络（web_fetch/crawl/map/search 全包 + SSRF/HTML→MD/fetch 内核）
**LSP**— 导航（goto/refs）+ 诊断回流 W1–W4（含真实 gopls 端到端验收）
**undo**— filehistory 快照层 + undo 工具（含修的四写工具 ToolCallID 传错）
**plan mode**— 状态机 + 门链 + 草稿回读（第一百零七刀补完）

---

## 4. 未移植清单（按 TS 行数排序，这是真正的欠账）

| TS 目录 | 行数 | 判定 |
|---|---|---|
| `tui/` | **45,447** | **移植，排最后**（§6 已决） |
| `server/` | **26,957** | **移植，排最后**（§6 已决） |
| `repo/` | 4,295 | 中间层，可推进（需 Meridian 图 + Physarum） |
| `memory/` | 3,221 | 中间层，可推进（需存储层） |
| `mcp/` | 2,991 | 中间层，可推进（stdio/SSE 协议层，自包含） |
| `auth/` | 1,619 | 中间层，可推进（API key + Codex OAuth PKCE） |
| `plugins/` | 1,275 | 中间层，可推进（清单加载） |
| `cli/` | 910 | Go 有自己的 CLI 入口，形态不同 |
| `workers/` | 840 | 依赖 delegate 派发内核（§6 第 3 段） |
| `benchmark/` | 832 | 基准设施 |
| `constellation/` | 726 | 依赖 work-order 体系 |
| `utils/` | 656 | 工具函数 |
| `bootstrap/` | 595 | 装配层（Go 在 `cmd/tianshu` 自有一套） |
| `diagnostics/` | 417 | 诊断 |
| `model/` | 196 | profiles + 路由 |
| `system/` `commands/` `failures/` `docs/` | 共 292 | 零散 |

**工具差集**（Go 缺的，按性质分类）：
- **需先建子系统**：`delegate_task`/`delegate_batch`/`team_orchestrate`/`galaxy`/`starflow`/`plan_task`（需派发内核 ≈11.7k 行）、`semantic_search`/`repo_graph`（需索引+embedding）、`monitor`（建了是休眠）、`generate_image`（需 provider 配置）、`schedule_*`（需 cron）、`ast_grep`/`ast_edit`（需决策 cgo/纯 Go AST）、`browser`/`browser_debug`（需 Playwright 驱动层）、`ask_image`（需视觉层）
- **不可做**：`computer_use`（TS 侧开源桩 + `src/pro/` 闭源）、`sandbox_exec`（语义前提是隔离的 Node 子进程）
- **已完成但口径易漏**：`apply_patch`/`run_tests`/`related_tests`/`recall_general` 等**已注册**（用 `grep -rn "Name: \"<工具名>\"" go/internal/tools/` 核实）

---

## 5. 已判定「不做」的项（**勿重复勘探**）

| 项 | 判定 | 依据 |
|---|---|---|
| `protectionMode` | 接线后行为不变 | 破坏性 git 已在硬闸门内 |
| `canAutoApprove` | 256 组穷举 0 差异，完全冗余 | — |
| sensorium 链 | 一冗余、一无消费者 | `pressureResult` 已实现但零实例化 |
| `TaskAnchor` / `TaskContract` | 类型都不存在，需 570 行 + 16 模块消费 | 无契约生命周期维护者 |
| `update_goal` / `session_vitals` | 依赖在 Go 侧全为 0 | 造子系统 |
| `generate_image` / `schedule_*` | 缺 provider 配置体系 / cron | 造子系统 |
| delegate 族「改文案」 | 已证伪为**伪修复** | TS 那段文案是对的；删它背离 parity 且用户仍可手动调用 |

**已登记的休眠接线**（有读取方零写入方，待上游子系统）：
`OwnedFiles`（待 `ownershipLedger`）、`assessDelivery`（待 `deliveryGateV2`）、
`sessionModel` 留痕、`onPlanSubmitted`（Go 无渲染层）。
→ 这些是**显式登记**而非静默失效，读代码即可见。

---

## 6. 范围决策（**已定**，2026-09-27）

**决策：`tui/` 与 `server/` 都要移植，排在最后。**

| 项 | 行数 | 决策 | 理由 |
|---|---|---|---|
| `tui/` | 45,447 | **移植，最后做** | 与 TS 行为对齐（纯 ANSI 渲染路径），非「另起一套」 |
| `server/` | 26,957 | **移植，最后做** | 需服务桌面端 sidecar（HTTP/SSE 驱动同一 agent 内核） |

**为什么排最后**：这两个是**消费端**——它们消费内核（loop / 工具 / 认知层 / 压缩缓存）。
内核未定型就先做 TUI，会在内核演进时反复返工。且它们体量最大（共 72,404 行，
占 TS 总量 25%），放最后可让内核先行收敛。

**对中间层的影响**：`repo/`(4,295) 与 `mcp/`(2,991) 等中间层**不再被阻塞**——
它们有独立价值（`repo/` 服务检索、`mcp/` 是协议层），不依赖 TUI/server。

**建议的推进顺序**（依赖由浅到深）：
1. **内核收口**——继续按「已知的静默失效 > 新增功能」找接线缺口
2. **中间层**——`mcp/`、`auth/`、`plugins/`、`repo/`、`memory/`（各自自包含）
3. **编排层**——`delegate` 派发内核（≈11.7k 行，解锁 `workers/` + 多个工具）
4. **表面层**——`tui/` → `server/`（最后）

---

## 7. 对账基线与**显式偏离**（必须知道）

### 7.1 合并引入的基线变更

`origin/main` 的 26 个提交动了 **213 个 `src/` 文件**，而 `src/` 是 parity 对账源。

| 已核实 | 状态 |
|---|---|
| `src/tools/plan.ts` | **未动** → 第 107 刀 parity 仍成立 |
| `src/tools/types.ts` | **未动** → `ActivePlanFilePath` 对账仍成立 |
| `src/agent/evidence.ts` | **未动** → `SessionModifiedFiles` 对账仍成立 |
| `src/agent/tool-pipeline.ts` | **被改**（`1df9e6fd` sync）→ 第 106 刀依赖的 `:838` 行号需重核 |
| `src/tools/git.ts` | **被改**（`1df9e6fd` sync）→ 第 106 刀依赖的 `:163` 语义需重核 |
| `src/agent/` 累计 | 44 个文件被改 |
| `src/tools/` 累计 | 29 个文件被改 |
| `src/prompt/` 累计 | 5 个文件被改 |

**动作**：任何「对账 TS 某行」的刀，改前先 `git log -1 --format=%h -- <file>` 确认该文件
自上次对账以来未变；变了就重核行号与语义。

### 7.2 ★ Go 侧的**显式偏离**（有意不照搬 TS 的缺陷）

项目纪律：**忠实移植 ≠ 照搬缺陷**。当上游行为会让该功能**自身失效**时，
Go 侧修正并在代码注释里写明偏离理由（下表的每一条都在源码里有对应注释与测试）。

| 刀 | 位置 | 偏离内容 | 上游（TS）行为 |
|---|---|---|---|
| 第 100 | `import_resource` | 用**复制**而非符号链接放入工作区 | TS 用 symlink + `realpathSync` → 导入后 `read_file` 被路径校验拒绝（承诺是假的） |
| 第 103 | `lsp/rpc.go` 等 4 处 | 回 `MethodNotFound` / 全程 `[]byte` / 显式置 ready=false | TS 静默丢弃 server→client 请求 / 多字节置换 U+FFFD / 崩溃 server 被永久当 ready |
| **第 108** | `tools/git.go` `getScopedCommitFiles` | **过滤不存在的路径**（`os.Stat`） | TS `runGit(['add','--',...])` 不校验存在性 → 已删除路径（如回收后的 plan 草稿）会让 `git add` **整体失败**（exit=128），连正常文件也提交不了 |

**最后一条的触发链**（实测复现）：`enter_mode` 建草稿 → `submit` 成功后回收草稿
（`os.Remove`）→ 但该路径仍在 `sessionModifiedFiles`（`FileIndex.ModifiedByMe`
不因删文件而清）→ 进 `git add --` → fatal。

---

## 8. 开发纪律（本项目最重要的资产）

1. **oracle 纪律**：文本跨语言移植一律用生成器从**真实 TS 代码路径**导出 golden。
   手抄常量会引入**自洽假绿**（Go 与手抄 golden 双方同错）——本项目出过事故。
2. **变异反证**：写完测试**故意改错实现**，确认变红。红不了 = 测试无效。
   「红 0」有五种成因（等价变异 / 真覆盖缺口 / **编译失败伪装** / 输入不对抗 / 环境依赖），
   最高频的是编译失败伪装（累计 14+ 次）。
3. **装配层是最后一道缺口**：子系统全实现 + 单测全绿，但 `main.go` 没注入 → 功能永不出现。
   **用户级验收 = 走生产装配路径 + 真实二进制**。
4. **同一事实的载体要当场数**：实现 / 测试 / **模型可见描述** / 函数注释——
   改一处前先 `grep -rn "<该事实>"`，否则「改了但没改全」会以新形式复发。
5. **注释落后于实现是常态**：判缺口前先 grep 实现，别照文件头注释。

---

## 9. 已收编的计划文件（保留，不再作为待办依据）

`.rivet/plans/` 下的 10 份，**其 checkbox 已与真实进度脱节**，仅作历史留痕：

| 文件 | 真实状态 |
|---|---|
| `go-重写天枢运行时-分波移植计划.md`（471 行，31 cb 全未勾） | **已过期**——Wave 0/1 实际早已完成 |
| `go-工具移植排期-按依赖面分-w1-w4.md`（374 行，18 cb 全未勾） | **部分过期**——W1–W3 已由第 82–97 刀兑现 |
| `go-工具移植排期-第二轮-import-resource-...md`（310 行，**16 cb 全勾**） | 已完成（唯一勾满的） |
| `go-undo-快照层-...md` / `go-子系统移植排期-w1-lsp-...md` | 已完成（已标 EXECUTED） |
| `go-运行时排期-接线-sessionmodifiedfiles-...md` | 已完成（第 106 刀） |
| `go-运行时排期-订正-plan-工具的过期描述-...md` | 已完成（第 107 刀） |
| `第一百零四刀-...md` / `第一百零五刀-...md` | 已完成（审查修复刀） |
| `draft-1790487135423.md`（512 行） | 历史草稿残留，无状态标记 |

**引用建议**：这些文件只用于「当时的设计意图」，**进度一律以本文 §2 为准**。

---

## 10. 权威文档索引

| 想了解 | 读 |
|---|---|
| **现状与待办** | **本文**（唯一入口） |
| 每刀的完整技术细节 | `.rivet/HANDOFF.md`（2028 行，第一百零七刀止） |
| 架构欠账清单 | `go/PLAN.md`（其指标表会滞后，以本文 §2 为准） |
| 跨设备环境重建 | `go/PLAN.md` §2（Go 1.27 / Node 24 / `npm install` 必需） |
| 审批门状态表 | `go/internal/agent/approval_gate.go` 的 A/B/B'/C/D/E 节（**用节名定位，不用行号**） |
