# 交接文档 — Go 重写天枢运行时（Windows 设备接力）

> 生成时间：2026-09-20（当日末次更新）· 本会话在 `D:\code\Tianshu-harness`（Windows 10.0.26200）
> 分支：`go-runtime` · HEAD：`516a226` · 今日共 **16 个提交**
> 本文自包含——读者无需本会话任何上下文。

---

## 任务目标

**一句话目标**：把 TypeScript 版天枢运行时**字节等价**地移植成 Go，保证在 Windows 上可编译、可运行、行为与 TS 版一致。

**背景**：这是跨设备接力。Go 重写最初在另一台设备（macOS，作者 moweilong）上推进，本会话在 Windows 接手。接力点提交是 `bff55c5`。**本仓库原本没有 `go/` 目录**——是接手时通过快进才出现的。

**权威文档（先读这两份，本文只是索引）**：
- `go/HANDOFF.md`（1906 行）—— Go 子项目的详细技术交接（每刀都有专章）
- `go/PLAN.md`（85822 字节）—— 架构欠账清单

**非目标**：
- 不追求「功能更多」——只追求**与 TS 版行为等价**。任何偏离 TS 语义的「优化」都是缺陷。
- 不重写 TS 版（`src/` 下代码不动）。
- 本会话**不碰** `go/` 以外的模块。

**硬约束**：前缀缓存工程是核心指标——冻结 system prompt 必须**字节稳定**，任何进入冻结前缀的字符串（如 `<environment os="...">` 行）差一个字节就会让缓存命中率崩掉。

---

## 已完成

今日（`bff55c5..HEAD`）共 **16 个提交**，全部在 `go-runtime` 分支。分两段：**Windows 可移植性**（1–5）与 **压缩/session split 主线**（6–16）。

### 第一段：Windows 可移植性（起点是 `go build` exit=1 的硬阻塞）

#### 1. `6ac0a49` Windows 原生可移植性（19 文件，+469/−99）

三处根因：

- **Unix-only syscall**：`syscall.Setpgid` / `syscall.Kill` / `SIGKILL` 在 Windows 不存在。修法：build tag 拆分——`internal/tools/proctree.go`（公共）、`proctree_unix.go`、`proctree_windows.go`（Windows 走 `taskkill /F /T /PID`）。
- **路径语义缺口（fail-open 安全洞）**：Go 的 `filepath.IsAbs("/etc/passwd")` 返回 **false**，而 Node 的 `path.win32.isAbsolute("/etc/passwd")` 返回 **true**——会让根相对路径被静默重基进工作区。修法在 `internal/pathsafe/pathsafe.go:179` 的 `resolveUnder`（复刻 Node 语义）。
- **trust 权限 mode 位**：Windows 上恒为 666。

**WaitDelay 缺陷（超时形同虚设）**：孙进程继承管道写端句柄，`taskkill` 杀进程后句柄未释放，`cmd.Wait()` 阻塞到 EOF。设 500ms 超时实测卡 **19–20 秒**。修法：`cmd.WaitDelay = 2s` → 实测 2.0s。

**测试夹具 4 类平台假设**：手拼 JSON 嵌 Windows 路径（`\U` 非法转义，**21 处**改 `json.Marshal`）、`.exe` 后缀、mode 位、EOL 默认。

#### 2. `5ceb39d` shell 探测移植（7 文件，+1104/−14）

**关键核实**：TS 的 `src/tools/bash.ts:514` 用 `getShellCommand()` **实际 spawn shell**，而 Go 首版硬编码 `exec.Command("bash", ...)`——在**没装 Git Bash 的 Windows 上功能完全不可用**。

新建 `internal/platform/platform.go`（对账 `src/platform.ts`）：`ResolveShellCommand`（Git Bash→pwsh→powershell→cmd.exe）+ `ResolveGitBashPath`（五级探测，**排除 WSL**，bundled PortableGit 最后）。oracle 30 用例。

**oracle 抓到的假绿**：TS 的 `deps.env` 是 `NodeJS.ProcessEnv` **对象**而非取值函数，首版传函数导致所有 env 用例**静默产出 null**。

#### 3. `85b8ade` DetectHostEnv 字节不等价（5 文件，+200/−13）

`<environment os="...">` 这一行**进冻结前缀**。实测：Node 输出 `Windows_NT 10.0.26200`；Git Bash 的 `uname` 输出 `MINGW64_NT-10.0-26200 3.6.9-...`——**不相等**。修法：`internal/prompt/hostenv_windows.go` 改用 Win32 `RtlGetVersion`（`syscall.NewLazyDLL`，零依赖）。

#### 4. `9a70e14` run_tests 的 Windows spawn（6 文件，+698/−5）

`internal/tools/testspawn.go` + `quoteCmdArg:81`。**探针实测的注入面**：Go 自动引号**只在含空白时触发**，不含空白的 `& | ( ^ %` **全裸露**（`&` 可执行注入、`%PATH%` 可展开）；且**引号挡不住 `%`**。顺带修 `run_tests.go:558` 的 `buildCmd` 硬编码 `bash -c`。

#### 5. `b4b9c69` 控制台输出乱码（7 文件，+529/−8）

本机代码页 **936（GBK）**：`cmd /c "echo 中文"` 输出 `d6 d0 ce c4...`。Go 比 TS 更小心的三处：`transform.Bytes` 每次重置状态（须持有 `Transformer`）；单块 `utf8.Valid` **不能区分「残缺」与「非法」**；GBK 转换器对不完整序列返回 `nSrc=0`（须累积重试）。新增依赖 `golang.org/x/text`。

### 第二段：压缩 / session split 主线

#### 6. `64ca6a4` reclaim gate（9 文件，+1426/−10）

`internal/compact/profile.go`（`WindowBandFor:22`、`DeriveCompactionProfile:58`）+ `reclaim.go`（`EstimateReclaim:113`、`ShouldCommitReclaim:143`）。五条分支顺序敏感；`floorMul` 用整数运算避免浮点截断。

**测试构造坑**：`BlocksUnprofitable` 首版走了**放行**分支（实测回收 10479 > 地板 8192）——根因是没算清截断目标 `ToolResultMaxTokens = floor(window × 0.3)`。

#### 7. `1af223a` 缓存顾问延迟（8 文件，+1176/−2）

新包 `internal/cache`：`warmth.go`（`SessionWarmthTracker`）+ `advisor.go`（`NewAdvisor:97`、`ShouldDelayCompact:124`，protection = hitRate × (1−pressure) ≥ 0.45）。

**三个结构性发现**：① `!decision.Force` 守卫是**死分支**（force 的 Tier 恒为 `ceiling(4)`）——M1 红 0 处是**等价变异**；② `CompactBoundary.RecentHitRate` 与 `Advisor.RecentHitRate` 是**两个独立输入**；③ `MaxTokens≥1M` 走独立 LLM 阶梯。

#### 8. `1b3a552` session split 判定层（8 文件，+940/−3）

`internal/compact/sessionsplit.go`：`ShouldSessionSplit:82`（窗口 < 500K **优先于** ratio < 0.86）+ 最小 `BuildSessionHandoff:121`。**范围有意收窄**：执行层依赖当时 Go 侧全无的 task-state / trajectory / artifact store。

**oracle 两处坑**：把非判定路径的数据混进 golden；记录时机错误（成功后历史已替换，首版记到 3098 而非判定时的 499000）。

#### 9. `222cbc4` 修复 session split 的悬空（3 文件，+177/−2）

**上一刀漏报的真实缺口**：`TrySessionSplit` 落地时**没有生产调用方**——`type-without-consumer`（悬空代码），而当时的交付报告**没有指出**，还错误声称「已通过用户级行为验收」。

**发现方式**：grep 消费方，确认 `loop.go` 只调 `MaybeCompact`——而 TS 的调用序是**先 split、再 maybeCompact**（`src/agent/compact-boundary-coordinator.ts:121,128`）。**修复**：接到 `maybeCompactAtBoundary` 内（当前 `loop.go:921`），置于 `MaybeCompact` **之前**（顺序有意义：split 的判定依据是历史占用）。

**执行层未移植下的三点诚实处理**：不替换历史、发可见事件（文案含「执行层未移植」）、不阻断常规压缩。

#### 10. `1bcd57b` task-state + trajectory + todo-deps（14 文件，+3522/−68）

四个新文件（全部对账 TS 逐字）：
- `internal/compact/trajectory.go` ← `src/agent/trajectory.ts`（`TrajectoryRecorder`）
- `internal/prompt/tododeps.go` ← `src/tools/todo-deps.ts`（依赖检测/排序）
- `internal/compact/taskstate.go` ← `src/agent/task-state.ts`（`ExtractTaskState`/`TaskStateFromTodos`）
- `internal/compact/handoff.go` ← `buildStructuredHandoff`（**9 章节完整版**）

**接线**：`loop.go:921` → `TrySessionSplit` → `compact_boundary.go:453` → `BuildSessionHandoffWithState` → `ExtractTaskState`/`TaskStateFromTodos` → `DetectDependencies`。`Loop.Trajectory` 在 `executeTool` 记录（**含失败**——失败轨迹是 handoff 错误章节的唯一来源）。

**三条关键对账**：① **UTF-16 截断**（TS `slice(0,60)` 按 code unit，中文截 60 字符 vs Go 字节切的 20 字符；变异 M1 使测试红）；② **裸数字依赖提示词**（「基于 1」算边，「还剩 1 个测试」不算；变异 M2 使测试红）；③ oracle 改为 `{input, output}` 数据驱动（首版只存 output，Go 测试需手工重建输入——重建不一致即假绿）。

#### 11. `11e0d3b` 修复 handoff 失败行（3 文件，+58/−4）

**交付门禁的 YELLOW 提示「`inputSummary` 无读取方」不是噪音**。核实后确认 TS 的错误行是
`- [Turn N] failed: <tool> <target>: <summary> (<errorClass>)`（`compaction-controller.ts:229,671`），首版漏了 `: <summary>` 段——**handoff 文本与 TS 不等价**（handoff 会进后续请求前缀）。

#### 12. `1f3ae5a` artifact store（14 文件，+2392/−2）

新包 `internal/artifact`（`types.go` / `store.go` / `threshold.go`），对账 TS `src/artifact/`。同时**消掉了 `context_collapse.go` 里「Go 侧当前无 artifact 生产端」的已记录欠账**。

**三处接线**：L1 拦截（`loop.go:613` 的 `executeTool` 调 `interceptResultForArtifact`）；`read_section` 工具（`default_registry.go:40`）；CLI 装配（`main.go:202`）。

**关键约束（TS 记录的真实事故）**：`l0WrappedTools`（read_file/read_section/grep/bash）**不得被 L1 重复包装**——否则无限嵌套（`[artifact:新ID] → read_section → ...`）＋ grep/bash 的**尾部**标记被漏检导致 double-save。Go 侧工具尚无 L0 包装，保留该集合是契约完整性。

#### 13. `8a83f82` replaceWithCheckpoint（11 文件，+1125/−46）——**验收面转 met**

新增 `internal/agent/checkpoint.go`；`session/listener.go` 的 `OnReplace` 从**「未实现占位」改为真的全量原子重写**（对账 `compactOai`）+ `persist.rewriteTranscript`（tmp + rename）。

**核心语义**：① 锚保留（前 `CacheAnchorMessages`(2) 条逐字节不动）；② **尾随未消费 user 保护**（原文保留、不进归档）；③ 摘要角色（有尾随原文时用 assistant，防 volatileBlock 双份注入）；④ reclaim gate（不提交则不碰历史）；⑤ **审计行保留**（compact_start/end/model_switch 从不进内存，不保留会静默销毁审计轨迹）。

**自 `1b3a552` 起一直 blocked 的验收面转 met**——`sessionsplit_e2e_test.go` 四条锁定。

**行为变更影响了 4 条既有测试**（断言随之反转），其中 `TestListenerReplaceUnimplemented` 在首轮全量跑时**真的 panic 了**（旧断言假设 error handler 收到错误）。

#### 14. `1e28b38` CheckpointDeps.Preflight（7 文件，+969/−1）

`internal/context/writeevidence.go` + `resumepreflight.go`（对账 `src/context/`）。

**与既有 `session.RepairOrphanToolCalls` 的区别（关键）**：后者**剔除**孤儿，本函数**拉回 + 合成**。TS 注释明确「id 存在性检查必要但不充分」——结果可能**存在**却位于中间的 user/assistant 之后，有匹配 id 但**邻接**破坏。

**本刀发现的真实 bug（非本刀引入）**：`session.NormalizeOaiMessage` 的条件是 `m.ToolCalls == nil`——**漏掉了从 JSON 读回的空数组**。探针实测：`json.Unmarshal` 对 `"tool_calls": []` 产出**非 nil 空切片**（`nil=false len=0`），而那恰恰是该函数存在的理由。已修，并让 `sameMessages` 把 nil vs 空切片差异算作「不同」。

#### 15. `f1705eb` CheckpointDeps.ArchiveDiscarded（7 文件，+1012）

`internal/context/compactarchive.go` + `recallmarker.go` + `archiveassembly.go`。

**序列化契约（必须稳定——read_section 按行定位）**：每条消息用固定 divider `--- turn:N role:ROLE ---`；**sections 按消息切分**（不是按轮）——单条 assistant 可跨几十行、单条 tool 结果可几万字符。turn 从 0 起、每条 user 递增一次。

**recall-eviction**：被召回的 compact-history 块若原样重新归档会让内容在 artifact 间**重复累积**（抵消压缩）——折叠为一行指针。

**fail-soft 四条早退**（对账 TS「compaction must never be blocked by archival」）。

#### 16. `516a226` read_section 的 compact-history 流式分支（3 文件，+313）

在 **2MB 守卫之前**插入快速路径（`readsection.go:210-242`）：只对行范围生效、走 `ReadLineRange` 流式、前置 `[recalled <id> <section>]` 标记。**顺序是关键**——长线程归档常超上限，会让归档自己的目录项无法召回（「存得下、取不回」）。

**依赖方向**：新引入 `tools → context`（无环），以 `ctxstore` 别名导入。

---

### 验证基线（当日末次真实工具输出）

| 命令 | 结果 |
|------|------|
| `go build ./...` | exit=0 |
| `go vet ./...` | exit=0 |
| `gofmt -l .` | 零违规 |
| `go test ./... -count=1`（连跑 3 次） | **22 包 ok、0 FAIL**（`go list ./...` 报 23 个包，含无测试的） |
| 探针残留检查（`find internal -name 'zz_*' -o -name '*_dbg*'`） | 零残留 |

工作区状态：干净，仅 `.rivet/skills/` 为 untracked（非本会话产物）。

**注意**：`package-lock.json` 曾因 `npm install` 变动（3.19.0→3.21.1），**未纳入任何提交**。

**变异反证总表**（每条都实测让对应测试变红）：M1（字节截断/M4 尾随user保护/M5 审计行/M6 漏检缺失结果/M7 丢弃而非拉回/M8 turn递增/M9 recall-eviction/M10 流式分支/M11 召回标记）。

---

## 当前卡点

### 卡点 1：~~session split 执行层未移植~~ → **已全部解决**

原卡点（判定层完成但无法替换历史）已由第 10–16 刀**完整闭环**：

```
判定（1b3a552）→ 前置：task-state/trajectory（1bcd57b）+ artifact store（1f3ae5a）
→ 执行：replaceWithCheckpoint（8a83f82）→ 增强：preflight（1e28b38）+ archive（f1705eb）
→ 召回：read_section 流式分支（516a226）
```

**这条主线的验收面已 met**（`sessionsplit_e2e_test.go`：历史真的变短、锚逐字节保留、handoff 内容就位、落盘生效）。

### 卡点 2：`CheckpointDeps` 最后一个增强未接

- **`TaskAnchor`** ← 需 `getActiveContract` + `renderTaskAnchor`（两个小模块，未移植）

它让压缩后的历史带一份权威任务契约（objective/constraints/success），防止摘要漂移后模型失去目标锚。**这是当前最高优先级**——`CheckpointDeps` 三个字段里唯一未接的。

### 卡点 3：其余未接线模块（详见 `go/PLAN.md` 架构欠账）

- **LLM 重写路径**（partial-llm / full-llm / checkpoint）——需 `summaryClient` 抽象（真实 API 调用）
- **`resolveCompactionEconomics` 装配层**——依赖 `classifyCostModel` + provider cache defaults
- **`Advisor.onTurnEnd`**——喂 behaviorLearner / ghostRegistry / recallMetrics
- **`summarize.go`**（`src/artifact/summarize.ts`，407 行）——按扩展名提取 sections。**当前保存时 `sections` 恒为空**（对账 TS 的实际调用 `tool-pipeline.ts:607` 也传 `[]`），只影响 read_section 的片段名提示质量
- **read_section 的 `file_path` 分支**——依赖 `getFileReadMtime`（陈旧性告警）与 `computeModelReadCap`（按窗口/提供商算读上限）。**当前 `readSectionMaxChars` 用 `ToolArtifactThreshold("read_file", ...)` 近似**——这是已知偏差
- **`promptEngine.resetAppendixBaseline`**、**`recordCompactEvent`**

---

## 下一步

按优先级排列，每条可立即执行：

1. **移植 `TaskAnchor`**（卡点 2，当前最高优先级）
   ```
   grep -n 'getActiveContract\|renderTaskAnchor' /d/code/Tianshu-harness/src/ -r
   ```
   先定位 TS 侧这两个函数的实现与契约来源（`ActiveContract` 的 objective/constraints/success 从哪来），再决定 Go 侧最小移植面。完成后接到 `main.go` 的 `loop.CheckpointDeps.TaskAnchor`。

2. **`summarize.go`**（artifact sections 提取）
   对账 `src/artifact/summarize.ts`（407 行，按 ts/py/rs 等扩展名分派）。它只影响 read_section 的片段名质量，不影响召回功能——**可独立做**。

3. **`computeModelReadCap` + `getFileReadMtime`** → 补 read_section 的 `file_path` 分支
   同时消掉 `readSectionMaxChars` 的已知近似偏差。

4. **`resolveCompactionEconomics` 装配层**
   依赖 `classifyCostModel` + provider cache defaults，先定位 TS 侧实现在 `src/compact/` 下的位置。

5. **`Advisor.onTurnEnd`**
   喂 behaviorLearner / ghostRegistry / recallMetrics——先确认这三个消费者在 Go 侧的移植状态。

6. **LLM 重写路径**（最后做）
   需 `summaryClient` 抽象 + 真实 API 调用。建议先做一个可注入的 fake client 完成结构移植，真实调用留到有 API key 的环境。

---

## 坑

**绝对不要再踩**——每条一句话说清后果：

1. **手拼 JSON 字符串嵌 Windows 路径 → `\U` 非法转义，JSON 解析失败**。21 处已改用 `json.Marshal`；新增夹具一律走它。
2. **`cmd.Wait()` 在孙进程继承管道写端时会阻塞到 EOF → 超时形同虚设**（设 500ms 实测卡 19–20s）。杀进程后必须设 `cmd.WaitDelay`。
3. **Go `filepath.IsAbs("/etc/passwd")=false` 而 Node `path.win32.isAbsolute`=true → 根相对路径被静默重基进工作区（fail-open 安全洞）**。路径校验必须复刻 Node 语义。
4. **Git Bash 的 `uname` 输出与 Node `os` 模块不一致 → 进冻结前缀的那行字节不等价 → 前缀缓存命中率崩掉**。`<environment>` 行必须走 Win32 API。
5. **Go 自动引号只在含空白时触发 → 不含空白的 `& | ( ^ %` 全裸露**；且**引号挡不住 `%`**。Windows 命令行参数必须自己 quote。
6. **本机代码页 936（GBK）→ 直读控制台输出乱码**。必须流式解码；`transform.Bytes` 每次重置状态（要持有 `Transformer`）。
7. **TS 的 `String.slice` 按 UTF-16 code unit 计数**——中文场景字节切会截半字符（60 字符 vs 20 字符）。Go 移植必须用 UTF-16 语义。
8. **Go 的 `json.Unmarshal` 对 `"x": []` 产出非 nil 空切片**（`nil=false len=0`）——判空数组必须用 `len()==0` 且区分 nil，否则漏掉从文件读回的形态（`NormalizeOaiMessage` 曾因此失效）。
9. **「红 0 处」有四种成因**，别急着宣布「等价」：①等价变异（如 `!decision.Force` 死分支）②真测试缺口 ③**编译失败伪装**（去掉 import 使用后 build failed）④用例集取值点密度不足。
10. **变异反证必须核实变异真的落地**——M11 首版用 python 脚本替换时**静默未生效**，测试没红一度被误判为「测试有漏洞」。改用 edit_file 后正常变红。
11. **断言「不该发生 X」的测试必须验证 X 的可达性**——否则只是恒真断言。
12. **落地新导出符号后必须 grep 消费方**——`TrySessionSplit` 首版是悬空代码，交付报告漏报还错误声称已验收。
13. **实现落地会让「未移植占位」的断言与事实相反**——这类测试必须随之反转或删除，否则会 panic 或假红（`TestListenerReplaceUnimplemented` 首轮全量跑时真的 panic）。
14. **交付门禁的「字段无读取方」YELLOW 提示可能是真缺陷**——`ResultSummary` 无消费方暴露了 handoff 失败行漏 summary 段。
15. **`npm install` 会改 `package-lock.json`（3.19.0→3.21.1）→ 不要把它卷进 Go 相关提交**。

---

## 环境事实（供下个会话核对）

- 平台：Windows 10.0.26200
- Shell：Git Bash（POSIX）
- Node：24.18.0（`package.json` engines 声明 >=24）
- Go：1.27
- 代码页：936（GBK）
- Go module：`github.com/kalandramo/tianshu/go`
- 仓库双 remote：`origin`（私有镜像）、`tianshu`（公开仓库，**绝不直接 push**——历史不同步会被拒）
- 分支：`go-runtime`
- 当前 HEAD：`516a226`
- `go/internal/` 包列表：agent api apierr artifact cache client compact context contract filediff pathsafe platform prompt recovery retry session syntaxcheck tools trust

---

## 权威文档索引（按需下钻）

| 想了解 | 读 |
|--------|-----|
| 每刀的完整技术细节 | `go/HANDOFF.md`（1906 行，每刀一个专章） |
| 架构欠账清单 | `go/PLAN.md` |
| session split 主线 | `go/HANDOFF.md` 的「replaceWithCheckpoint」「artifact store」「Preflight」「ArchiveDiscarded」「read_section 流式分支」五章 |
| Windows 可移植性 | `go/HANDOFF.md` 前五刀 + 本文「坑」1–6 |
