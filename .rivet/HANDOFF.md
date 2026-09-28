# Go 运行时移植 — 交接文档

> **写给无上下文的新会话**（agent 或人）。本文自包含，不引用任何「上文」。
> 元数据**实测于** `23a0b06f`（2026-09-28）。数字随每刀变动——引用前先跑 §6 的复现命令自测。
>
> **本文是覆盖写**：原 `.rivet/HANDOFF.md`（约 2,100 行，含第 0–12 段与第一百刀至
> 第一百一十二刀的历史专章）已被本文替换。原内容**未丢失**（在 git 历史里，
> 取回方式见「复现命令与历史归档」）。覆盖理由见「复现命令与历史归档」。

---

## 任务目标

**一句话目标（用户原话级）**：把 TypeScript 版天枢运行时（`src/`）**行为等价**地移植成 Go（`go/` 子目录），按刀推进，每刀独立验证。

**非目标**（明确不做）：
- 不重写 TS 版（`src/` 一行未动）。
- 不追求「功能更多」——只追求**与 TS 版行为等价**。任何偏离 TS 语义的「优化」都是缺陷。
- 不碰 `go/` 以外的模块。

**硬约束**：前缀缓存工程是核心指标——冻结 system prompt 必须**字节稳定**。进入冻结前缀的字符串差一个字节就会让缓存命中率崩掉。故文本资产一律走 **oracle 字节对账**（生成器从真实 TS 代码路径导出 golden），绝不手抄。

---

## 已完成

### 本轮会话（第 109–112 刀，17 提交 `8e1a7327` → `23a0b06f`）

本地领先 `origin/go-runtime` **17 个提交，尚未推送**（`0 behind / 17 ahead`）。

| 刀 | 提交 | 内容 | 验证 |
|---|---|---|---|
| 109 | `8e1a7327` `55164484` `4db5b759` `4e732489` `0dbe1589` | **MCP 子系统移植**（W1 纯函数层 / W2 RPC+stdio / W3 wrapper+manager / W4 配置+CLI 装配） | mcp 包 70 用例；全量 31 包 ok |
| 110 | `061db449` `10c3263a` `5cc04040` `eda3ac8b` `6e7abb4a` | **修 MCP 装配语义缺陷**：enabled 三态 / url 型明确拒绝 / timeoutMs 收口 / capability 接线 / ctx 透传 | mcp 包 91 用例；变异 M1–M5 全红 |
| 111 | `562da4f9` `b2a13afe` `32551ca6` `2f904bbe` | **接线模式块 + `<context-update>` 信封**：`plan-mode`/`plan-mode-exit` 块此前零消费者 | 31 包 ok；变异 M1–M4 |
| 112 | `faae78d2` `a2df3b79` `23a0b06f` | **移植 self-recognition + 接线 `<locus>` 块**：`CwdRelation` 此前恒空 | prompt 包 146 用例；变异 M1–M7 |

**关键 file:line 锚点**（当线索用，用前重新 grep）：

- **MCP 包**：`go/internal/mcp/`（`framing.go` / `types.go` / `policy.go` / `failure_classifier.go` / `rpc.go` / `stdio.go` / `wrapper.go` / `manager.go` / `config.go`）。装配点：`go/cmd/tianshu/main.go` 的 `assembleMcpTools`。
- **110 刀四处修复点**：`go/internal/mcp/types.go` 的 `Config.Enabled`（改 `*bool` + `EnabledOrDefault()`）；`config.go` 的 `validate()`；`manager.go` 的 `requestTimeoutMS` 字段；`manager.go` 的 `connectOne` 策略查表。
- **111 刀**：`go/internal/agent/dynamic_appendix.go`（`BuildDynamicAppendix` 加信封 + plan 块）；`go/internal/agent/loop.go`（`PlanExitReminderPending` 字段 + `exitPlanMode` 置位）。
- **112 刀**：`go/internal/prompt/self_recognition.go`（新增 `DetectCwdRelation`）；`go/internal/prompt/full.go`（`VolatileContext` 填 `CwdRelation`）。

### 更早的刀（本会话之前）

第 0 刀至第 108 刀已完成。**详细刀记录不在本文**——见「复现命令与历史归档」的取回方式。

---

## 当前卡点

**无硬卡点。** 全量测试绿（29 包 ok / 0 FAIL）、gofmt 零违规。工作树仅有 4 个未提交项（是计划文件，不影响代码），见「开工前必做」。

### 已知未做项（**不是**卡点，是有意收窄）

| 项 | 现状 | 缺什么 |
|---|---|---|
| MCP 的 SSE/HTTP 传输 | 未做（只做 stdio） | 需 transport-factory + OAuth + health-check |
| MCP 重连退避 / health-check / 连接级审批门 | 未做 | 审批门需 TUI/REST 消费端（属表面层） |
| `RenderAskModeBlock` | **未接线** | Go 侧**确实无** ask mode 状态载体（`grep -i 'askmode\|AskModeState' go/internal go/cmd` 零命中）。接它需先造模式机 |
| appendix 的 seq/delta 增量机制 | 未做 | 需跨轮持久化三字段（`lastEmittedAppendixParts`/`appendixSeq`/`appendixBaselineSent`）+ 与「压缩后重发 baseline」耦合 |
| 其余 6 个恒空 frozen 块 | 未做 | `project-memory`/`knowledge-manifest`/`seed-capsule`/`codebase-index`/`working-set`/`star-domain`——各需未移植子系统 |

### 已探明但未修的「恒走空路径」类缺陷（**候选下一刀**）

这类缺陷比「零调用方」更隐蔽：函数**被调用了**，但输入恒为空/恒 false，行为与未接线等价。

1. `go/internal/agent/planmode.go` 的 `PlanModeCheckContext.DelegatesWriteCapableProfile` —— 唯一生产调用点的 `PlanModeCheckContext` 字面量（在 `go/internal/agent/loop.go` 内，约 `:997`）**不含该键** → 恒 false → `CheckPlanMode` 的「写能力 profile 委派硬拦」分支恒不触发。本该由 profile registry 提供（Go 侧不存在）。
2. `go/internal/agent/dynamic_appendix.go` 的 `RenderTersenessNudge(false)` —— escalate 参数硬编码 `false`，上游 `ResolveTersenessFlags(prompt.TersenessContext{}, terseEnv)` 传空 ctx → `Enabled`/`Escalate` 指针均 nil。本该由 doom-loop 会话级状态提供（Go 侧不存在）。

**这两条由只读侦察标出，未逐条独立复核到行**——接手时先自己 grep 确认。

---

## 下一步（按优先级）

### 优先级 1 — 先完成交接动作

```bash
cd /Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness
git status --short                  # 看未提交项（见「开工前必做」）
git push origin go-runtime          # 推送 17 个提交
```

### 优先级 2 — 继续「内核收口」找接线缺口（总纲 §6 第 1 步）

**这一方向已证明高产**：本会话连续两刀（111/112）都是「实现完整但没人来取」的接线缺口。

1. 复核上节两处「恒走空路径」缺陷——**先 grep 确认**，不要采信本文转述。
2. **排查法（本会话验证有效）**：对每个被消费的可选字段，`grep '<字段名>:' go/` 看**生产装配点**是否有赋值。若只在类型定义/测试里命中，即恒空。
3. 参考：总纲 `.rivet/plans/GO-运行时-总纲.md` §5 的「已登记休眠接线」四项（`OwnedFiles` / `assessDelivery` / `sessionModel` / `onPlanSubmitted`）——**注意**：本会话只读侦察发现其中三项在源码里连字段都不存在（属「未移植」而非「有读取方零写入方」），只有 `OwnedFiles` 名副其实。引用 §5 前先自测。

### 优先级 3 — 中间层下一个子系统（总纲 §6 第 2 步）

| 候选 | TS 行数 | 已知障碍（只读侦察所得，**未逐条复核到行**） |
|---|---|---|
| `auth/` | 1,619 | **零第三方依赖**（全 `node:` 内置 + 项目内相对导入）；加密是 AES-256-GCM（无 KDF，`randomBytes(32)`，布局 `iv(12)\|tag(16)\|ct`，JSON 信封 `{v,s,b,d}`）→ Go `crypto/aes`+`cipher.NewGCM` 可逐字节兼容。**唯一非标准库能力**是 macOS 钥匙串 / Windows DPAPI 接入。消费者横跨 7 个目录约 18 处（多数属未移植的表面层） |
| `memory/` | 3,238 | 17 个源文件；`sqlite-knowledge-index.ts` 用动态 `import('node:sqlite')`（**失败即降级**到内存 BM25，权威存储仍是 JSONL）；Go 侧**零 sqlite 驱动**（`go.mod` 仅 3 依赖）。`recall`/`deep_recall` 不是独立工具，是 `src/tools/memory.ts` 单一工具的 action |
| `plugins/` | 1,275 | **结构性阻塞**：`plugin-loader.ts` 用 `pathToFileURL` + 动态 `import()` 在同进程加载插件 JS 模块并注册工具——Go 运行时**无法等价承接**。另：npm 安装链依赖 npm CLI 子进程 |
| `repo/` | 4,295 | 需 Meridian 图（TS 侧经 native-resolver 动态加载 `better-sqlite3`） |

### 优先级 4 — 表面层

`tui/`（45,447）/ `server/`（26,957）：总纲 §6 已决「都移植，排最后」——它们是消费端，须等内核收敛。

---

## 坑

### 本会话新增（第 31 条起）

31. **「消费分支 + 常量 + 字段类型」齐备 ≠ 功能可用**（type-without-producer）——第 112 刀就是这么踩的：`<locus>` 的两个消费分支与常量块一直都在，但唯一生产装配点不填字段 → 恒空 → 两分支都不可达，**且读代码时看起来已就绪**。判据：对每个被消费的可选字段 `grep '<字段名>:'`，生产装配点无赋值即恒空。
32. **变异「红 0」必须先分诊**——五种成因：①等价变异 ②真覆盖缺口 ③编译失败伪装（`declared and not used`）④测试输入不对抗 ⑤环境依赖。本会话 M3 是「我判断错了」（`append([]string{cwd}, src...)` 不写回源，无害），M6 是「假想风险结构上不可能」（单值字符串下两条件互斥）。
33. **「防重复」类断言要确认假想风险是否结构上可能**——否则测的是「不可能发生的事」，不是「实现正确」。改成可达的错误形态（如重复 append）才有判别力。
34. **端到端判据要区分「本轮新注入」与「历史累积」**——第 111 刀断言「第二轮请求体不含 exit 提示」红了，探针证明**实现是对的、判据错了**（第二轮那处来自对话历史）。正确判据是比对两轮**出现次数**。
35. **同一函数有两个版本时，先确认哪个在主路径**——按 `src/prompt/volatile.ts:868` 的 `join('\n')` 判断 Go 的分隔符是错的；核验 `src/prompt/engine.ts:1383/:1395/:1401` 三处**主路径**确认全是 `join('\n\n')`（`volatile.ts:866` 的 wrapper 主路径不用）。判据：见 `foo` 与 `fooInternal`/`fooLegacy` 并存时，先看谁被 import。
36. **`os.Exit` 会跳过 `defer`**——第 109 刀：headless 错误路径的 `os.Exit(1)` 让 `defer mcpMgr.Shutdown()` 不执行，留孤儿 MCP server 进程常驻。修法是该路径上显式回收。
37. **新加装配函数时把「谁有权取消我」显式化**——`assembleMcpTools` 初版自造 `context.Background()`，于是调用方的 signal ctx 被**类型系统**挡住，用户按 Ctrl+C 完全无效。
38. **`bool` 承载「缺席 ≠ false」是类型选择错误**——第 110 刀：TS 的 zod `.default(true)` 区分「键缺席」与「显式 false」，Go 的 `bool` 把两者塌缩为 `false` → 用户照 TS 写配置被**静默关掉整个子系统**。判据：schema 里有 `.default(X)` 且 X 非零值时，Go 侧必须用指针承载 + 访问器做唯一收口。
39. **「收了字段才能拒绝它」**——`ServerConfig` 有意不收 `url`（因不实现 HTTP 传输），但**不收就无法识别**，于是 url 型 server 静默消失。要为「不支持的能力」给诊断，得先能在配置里**看见**它。
40. **注册 waiter 与创建其 timer 必须原子**——第 109 刀 `-race` 抓到真 data race（`p.timer` 在锁外创建、`AbortAllPending` 在锁内读），**无 `-race` 时全绿**。与仓库记载的 LSP 侧缺陷同型。
41. **测序稳定性测试必须有多元**——测「`AllTools` 序稳定」时若只 1 个 server，而 `conns` 是 map，**单元素 map 遍历序当然稳定**，删掉 `sort` 后测试仍绿。
42. **变异要选「能编译的 bug 形态」**——`declared and not used` 是编译失败，不是断言失败，会被误当「红」。
43. **`go build ./cmd/tianshu` 的产物落在 `go/tianshu`**（15MB）——已补 `.gitignore` 规则，勿再误入 `git status`。
44. **one-shot 标志的清除要覆盖所有分支**——第 111 刀：planning 态下 `BuildDynamicAppendix` 走 if 分支**不读** `PlanExitReminderPending`，若只在读到它时清除，标志会挂到退出 planning 后才突然发一条早已过期的提示（时点错位）。
45. **计划文件写入后历史里显示 `[file written to …]` 指针是正常截断**（省 token），草稿内容完好，**不要重写**。

### 继承的坑（前序会话，**仍然有效**）

**A. 缺陷模式（最高频，优先看）**

- **「字段/函数存在但零调用者」是本仓库的高频缺陷模式**——`NeedsApproval`、`PermissionConfig.bash`、`TrySessionSplit` 都栽过。**落地新导出符号后必须 grep 消费方**；反之，声称「已有某能力」前也要 grep 调用点。
- **接线「状态 + 门 + 入口」三者缺一不可**——只接门链而入口不置状态 → 状态永远 `off` → **门是死代码**。验证必须走**端到端**，单测门函数绿不等于接线绿。
- **实现落地会让「未移植占位」的断言与事实相反**——改测试前先判：它是有效保护（fail-closed 语义要保留）还是过期断言（临时状态要撤销）？
- **同一事实写两处时，改一处必须 grep 另一处**（本项目两处自相矛盾都是「增量补刀只改一处」留下的疤痕）。
- **「桩函数」比「缺失函数」更危险**——存在、签名对、注释还说未来用途，但返回空；调用方不报错只静默拿空。看到「本波不实现」「暂时返回空」就当它是缺失。

**B. 反证与验证**

- **断言「不该发生 X」的测试必须验证 X 的可达性**——否则只是恒真断言。**负面用例必须带阳性对照**。
- **断言「拦截生效」必须断言命中的具体文案/标记**，不能只断言 `IsError`——空 registry 下放行也返回 `IsError=true`。
- **失效类测试不要先推送新数据**——推送会覆盖缓存，于是无论旧值是否被正确失效断言都会通过（弱代理）。应**不推送**。
- **变异反证必须核实变异真的落地且编译通过**（python 脚本替换可能静默未生效）。
- **全量失败归因别急着赖「环境污染」**——先看**耗时指纹**（超时 ≈ 超时值）与探针形态。
- **`approvalrisk/oracle.json` 含平台相关段（Windows 基准）**——macOS 上**不可重现**，不要盲目重跑。

**C. Go / TS 语义差异**

- **TS 的 `String.slice` 按 UTF-16 code unit 计数**——中文场景字节切会截半字符。Go 侧中文 bigram 必须按 `rune` 切。
- **Go `filepath.IsAbs("/etc/passwd")=false` 而 Node `path.win32.isAbsolute`=true** → 根相对路径被静默重基进工作区（fail-open 安全洞）。
- **Go 的 `json.Unmarshal` 对 `"x": []` 产出非 nil 空切片**——判空数组用 `len()==0` 且区分 nil。
- **`session.RecordVerification` 是「同 target 替换」语义**，TS 的 `verifications` 是**追加数组**——移植前先读语义，别只看签名。
- **Go 数组是值类型**（`[2]string` 赋值/传参整体拷贝，不可能被共享写脏）；**切片**会被 `append` 复用底层数组——担心包级卫生时用数组比切片强。
- **`cmd.Wait()` 在孙进程继承管道写端时会阻塞到 EOF → 超时形同虚设**。必须设 `cmd.WaitDelay`。

**D. 环境与工具**

- **`timeout` 命令在 macOS 不存在**（exit 127）；**`sh` 不支持进程替换 `<( )`**。
- **`GOFLAGS` 缓存会导致假错误**——症状诡异时先 `go clean -cache`。
- **探针必须清理**——交付前 `find . -name 'zz_probe*'` 应返回 0。
- **用 `git checkout` 恢复变异测试会丢掉本轮全部修改**——项目规则明令禁止。**改副本文件做变异**（`cp 源 副本` → 改副本 → 测 → 恢复）。
- **`npm install` 会改 `package-lock.json`** → 不要把它卷进 Go 相关提交。
- **交接文档里的行号引用天然短时效**——本项目踩过「同一刀内写下的锚点就失效」。**改用节名/函数名定位**。
- **往长文档中间插入段落时，用「标题字符串替换」会插到该标题的首次出现处**——长文档里同一标题常出现多次，必须先定位目标位置在文件中的**序位**。
- **`GOFLAGS`/批量 sed 的坑**：**批量 `sed` 替换测试文件会破坏测试意图**——改测试前逐个看注释里的意图声明。

---

## 开工前必做（执行前三查）

```bash
cd /Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness

# 1. 看工作区（本会话结束时：1 个已跟踪文件被改 + 3 个未跟踪 plan 文件）
git status --short

# 2. 看本地是否领先远程（本会话结束时：0 behind / 17 ahead）
git rev-list --left-right --count @{u}...HEAD

# 3. 跑全量基线（必须绿才开工；不绿先归因，绝不用 git 清场）
cd go && go test ./... -count=1
```

**待处理的未提交项**（本会话遗留，都是计划文件，不影响代码）：
- `M .rivet/plans/第一百一十一刀-接线模式块与-context-update-信封.md`
- `?? .rivet/plans/移植-mcp-子系统-第一百零九刀-四波-...md`
- `?? .rivet/plans/第一百一十一刀-修-mcp-装配的语义缺陷.md`
- `?? .rivet/plans/第一百一十二刀-移植-self-recognition-并接线-locus-块.md`

可一条命令收掉：`git add .rivet/plans/ && git commit -m 'docs(plans): 归档第 109–112 刀计划'`

---

## 环境事实（供下个会话核对）

| 项 | 值 |
|---|---|
| 设备 | macOS（Darwin 25.6.0，arm64） |
| 工作树 | **`go/` 子目录**（仓库根：`/Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness`） |
| 分支 | `go-runtime` |
| remote | **实测仅有 `origin`**（`git@github.com:kalandramo/Tianshu-harness.git`）。⚠️ 项目文档与旧 HANDOFF 都称「双 remote（origin + `tianshu` 公开仓库）」，但**本机 `git remote -v` 只有 origin**——以实测为准；若需 push 公开仓库，先确认 remote 是否需重新添加 |
| Go module | `github.com/kalandramo/tianshu/go`（`go/` 子目录）；Go 1.27 |
| Go 依赖 | **仅 3 个**：`klauspost/compress`、`golang.org/x/net`、`golang.org/x/text` |
| Node | 24.18.0（`package.json` engines 钉 >=24） |
| 测试框架 | Go 侧 `go test`（`node:test` 是 TS 侧的事） |
| **Go 侧验证命令** | `cd go && go test ./... -count=1` / `go vet ./...` / `gofmt -l .` |

**⚠️ 重要**：仓库根 `.rivet-config.json` 的 `verify-commands` 指向 **desktop** 的 typecheck/test——那是 TS 侧桌面应用的，**与 Go 运行时移植无关**。Go 侧验证用上表那三条。

---

## 复现命令与历史归档

### 复现命令（引用本文数字前先跑）

```bash
cd /Users/moweilong/Workspace/go/src/github.com/kalandramo/Tianshu-harness

git rev-parse --short HEAD && git rev-list --count HEAD
git rev-list --left-right --count @{u}...HEAD          # behind / ahead

find go -name '*.go' -not -name '*_test.go' -not -path '*/testdata/*' | xargs wc -l | tail -1
find go -name '*_test.go' | xargs wc -l | tail -1
find src -name '*.ts' -not -name '*.test.ts' -not -path '*/__tests__/*' | xargs wc -l | tail -1
grep -rh '^func Test' --include='*_test.go' go/ | wc -l
ls -d go/internal/*/ | wc -l && ls go/testdata/ | wc -l

cd go && go test ./... -count=1 && go vet ./... && gofmt -l .
```

### 实测快照（`23a0b06f`）

| 维度 | Go | TS 对照 | 覆盖 |
|---|---|---|---|
| 生产代码行 | **68,382** | 288,211 | **~24%** |
| 测试代码行 | 77,776 | — | 测试:生产 = **1.14:1** |
| 测试函数 | **2,513** | — | — |
| internal 包 | **29** | src 顶层 36 目录 | — |
| oracle 数据集 | **72** | — | 字节对账基线 |
| 提交数 | **1,112** | — | — |
| 全量测试 | **exit=0 / 29 包 ok / 0 FAIL** | — | 本轮实测 |
| gofmt | **零违规** | — | 本轮实测 |
| 工具（CLI 装配） | ~42（含 LSP） | full preset 目标 51 | 引自总纲 §2，**未在本轮复测** |

> **工具数的准确口径**：`grep -c 'r.Register('` 含循环注册，**不准**。准确值须经 `Definitions()` 实测。

### 历史归档（本文覆盖写的原因）

本文替换了原 `.rivet/HANDOFF.md`（约 2,100 行）。**原内容未丢失**，取回：

```bash
git log --oneline -- .rivet/HANDOFF.md | head -5
git show <该提交>:'.rivet/HANDOFF.md' | head -100
```

覆盖理由：原文件把**严重过期的元数据章节**（写于第 72–81 刀时，HEAD/提交数/包数全与实际脱节）与逐刀追加的专章混排，新会话无从判断哪份是最新。本文只保留「当前状态 + 下一步 + 坑」的自包含部分；逐刀细节交由 git 与 `.rivet/plans/` 承载。

---

## 权威文档索引

| 想了解 | 读 |
|---|---|
| **现状与待办（唯一入口）** | `.rivet/plans/GO-运行时-总纲.md`（§2 实测数字 / §4 未移植清单 / §5 已判「不做」项 / §6 范围决策 / §7 显式偏离） |
| 每刀的完整技术细节 | **git log**（提交信息里写了完整的根因分析与验证方式） |
| 具体某刀的计划 | `.rivet/plans/*.md`（每刀一份，含需求提炼/根因/分波/验证清单/瑶光反证） |
| 架构欠账清单 | `go/PLAN.md`（其指标表会滞后，以总纲 §2 为准） |
| 审批门状态表 | `go/internal/agent/approval_gate.go` 的 A/B/B'/C/D/E 节（**用节名定位，不用行号**） |
