# Go 重写天枢运行时 — 进度交接

> 更新：2026-09-19 · 本文件供后续会话（含遗忘后的自己）直接继续。
> 计划全文见 `.rivet/plans/go-重写天枢运行时-分波移植计划.md`。

## 现状一句话

**Go 版已能干活**：`tianshu -p "提示词"` → 请求构造 → 模型调用 → 工具执行 →
结果回灌 → 终答。Wave 1（模型接入层）完整收口，Wave 2（工具内核）含 bash，
Wave 4（agent 循环）最小版打通。

**四步闭环已真实验证**（本地 mock 端点）：读 calc.go（发现 `a - b` bug）→
edit_file 修复为 `a + b` → bash 跑 `go test ./...` → 文件确实改动、测试确实
从红变绿。这是 Go 版第一次真正干活。

验证基线：`go test ./...` 316 PASS / `-race` / `go vet` / `gofmt` 全绿；
干净检出（`git archive HEAD`）复验通过且 CLI 可构建。

## 目录结构

```
go/
├── go.mod                      module github.com/kalandramo/tianshu/go
├── cmd/tianshu/main.go         CLI 入口（headless + 交互式）
├── internal/
│   ├── contract/types.go       跨层中立契约（ToolResult / Definition / Usage）
│   ├── api/
│   │   ├── provider.go         Capabilities 三层合并
│   │   ├── body.go             请求体构造（BuildWireBody）
│   │   ├── wire/wire.go        OrderedMap（保插入序的 JSON 序列化）
│   │   ├── sse/sse.go          SSE 流解析
│   │   └── stablejson/         字节稳定序列化（排序键，用于工具 arguments）
│   ├── apierr/classifier.go    错误分类（31 用例对账）
│   ├── retry/retry.go          结构化重试（抖动退避 + 预算护栏）
│   ├── client/client.go        集成层（串起上面全部）
│   ├── pathsafe/pathsafe.go    路径安全（fail-closed）
│   ├── tools/                  工具内核（注册表 + 6 个工具）
│   └── agent/loop.go           agent 主循环
└── testdata/                   oracle 生成器与 golden
    ├── stablejson/ · provider/ · wire/ · sse/ · apierr/
```

## 三个必须知道的约束（踩过坑）

1. **wire 路径需要两个序列化器，不能混用**
   - `wire.OrderedMap`（保插入序）→ 请求体外层结构。真实路径是
     `JSON.stringify(effectiveBody)`（`src/api/openai-client.ts:783`）
   - `stablejson.Stringify`（排序键）→ 工具调用的 `arguments` 字段
     （`session-persist.ts:43`、`context.ts:373`）
   - **Go 的 `map[string]any` 会排序键**——用它构造请求体会直接击碎前缀缓存

2. **oracle 生成器必须调用真实代码路径**
   - 曾因手抄 wire 字段序（写成 `messages→model→stream`，真实是
     `model→messages→stream`）导致 golden 与实现**双方同错、测试照绿**——
     那不是验证，是两个错误的互相确认
   - 现全部 oracle 由真实 TS 客户端经 mock fetch 捕获，生成命令见各
     `testdata/*/gen-oracle.ts` 头部注释
   - **假绿不会被自身的绿灯暴露**——必须用变异反证（改回错误实现看是否变红）

3. **测试纪律**
   - 单元测试不得真等：retry 测试注入零延迟 sleep（`Options.Sleep`），
     套件从 42.8s 降到 0.9s
   - `httptest` handler 里 `<-r.Context().Done()` 会与 `Close()` 死锁
     （handler 等服务端 ctx，Close 等 handler）——用显式 release channel
   - handler 读请求体必须用 `io.ReadAll`——单次 `r.Body.Read` 在大 body
     （工具 schema 数十 KB）上不保证读满，会让断言随机失败
   - **杀子进程树不能靠 `exec.CommandContext`**：它只杀主进程，bash 派生的
     后台子进程成孤儿。须 `exec.Command` + `Setpgid:true` + 独立 goroutine
     在 `ctx.Done()` 时 `kill(-pid)`，与 `Wait` 并行（主进程死后 pgid 会被回收）
   - 变异反证若引入编译错误，测试报 `build failed` 而非 `FAIL`——别把
     构建失败误判为「测试未生效」
   - 新工具的 `blocked` 结果**不设 IsError**：它是中性门禁信号（门禁已重置），
     与 `failed`（测试红了）性质不同。混淆会让下游把「项目没测试」当「代码有问题」

## 环境注意

- `golangci-lint`（go1.25 构建）与 `staticcheck` **无法**在 Go 1.27 下运行
  （前者报版本低于目标，后者报导出数据版本过旧）。替代验证：`go vet` + `gofmt`
- 项目 `node_modules` 需 `npm install` 后才有；`npx tsx` 会走临时目录导致
  `zod` 等传递依赖解析失败。用 `node_modules/.bin/tsx` 跑 oracle 生成器
- 端到端冒烟可用**本地 mock 端点**完成，无需真实 API key：写一个返回构造
  SSE 的临时 HTTP 服务，用 `TIANSHU_BASE_URL` 指过去

### 真实端点验证怎么跑（2026-09-19 实测有效）

凭据在 `~/.rivet/provider-keys.json`（`keyRef` 指向 `~/.rivet/secrets.json`
的键）。**密钥不进上下文**——让 shell 直接注入环境变量：

```bash
export DEEPSEEK_API_KEY="$(python3 -c "
import json
with open('$HOME/.rivet/secrets.json') as f: s=json.load(f)
print(s['keys']['Tianyi-ds41'])")"

cd go && go build -o /tmp/tr ./cmd/tianshu
# 单轮看 usage
TIANSHU_BASE_URL=https://ai.ctaigw.cn/v1 TIANSHU_MODEL=deepseek-v4.1-flash \
  /tmp/tr -p "说：ok" --json --max-turns 1 | tail -3
# 多轮看缓存命中（连续请求，间隔久会因服务端 TTL 归零）
printf '记住42\n那个数字\n加1等于几\n再确认\n' | \
  TIANSHU_BASE_URL=https://ai.ctaigw.cn/v1 TIANSHU_MODEL=deepseek-v4.1-flash \
  /tmp/tr --json | grep '"type":"done"'
```

已配置的可用端点（`~/.rivet/config.json` 的 `provider.providers`）：
`Tianyi-ds41` → `https://ai.ctaigw.cn/v1`（deepseek-v4.1-flash，已验证）、
`deepseek` → `https://api.deepseek.com/v1`、`glm`、`kimi`、`siliconflow` 等 14 个。

**该端点的一个特性**：它把 `usage` 与 `finish_reason` 放在**同一个 SSE chunk**
（非 OpenAI 式独立尾块）。SSE 层已处理该分支。

**缓存诊断区分**（重要）：`cache_read` 归零有两种成因——① 字节不等价导致
的碎裂（**每轮都 miss**）② 服务端缓存 TTL 过期（**仅间隔久时 miss**）。
区分方法：连续快速请求多轮，若稳定命中则是 ②。

## 剩余工作（按建议优先级）

### Wave 1 剩余
- [x] ~~真实端点的 `cache_read_input_tokens > 0` 验证~~ **已完成（2026-09-19）**：
  指向 `ai.ctaigw.cn/v1` + `deepseek-v4.1-flash` 多轮实测，前缀缓存稳定命中
  93.7%–95.5%（轮1 1608→1536、轮2 1617→1536、轮3 1630→1536、轮4 1639→1536）。
  **Go/No-Go 门通过——字节等价成立**（缓存 key 与 TS 版一致）。
  真实模型下的完整闭环也已跑通：模型自主读 calc.go（识别 `a + b` 应为 `a * b`）
  → 改对 → 跑 `go test` 验证通过，该轮缓存命中 85.6%。

### Wave 2 剩余（工具内核）
- [x] ~~`bash`~~ 已完成（超时 / 进程组清理 / 破坏性硬闸门 / 输出截断标记）
- [x] ~~`run_tests`~~ 已完成（运行器探测 + blocked/failed 语义区分 + 结构化结果）；
  真实端点验证：模型自主调用 run_tests 完成「读→改→验证」闭环
- [ ] `apply_patch` / `hash_edit`（结构化编辑）
- [ ] `ast_grep`（需 tree-sitter 绑定）
- [ ] `todo` / `job` / `git` / `diff` / `repo_map`
- [ ] 工具 preset 三档（minimal / frontend / full）

### Wave 3（Prompt 引擎）
- [x] **static 层**：`internal/prompt` 的 BASE_PROMPT + MODEL_CALIBRATIONS
  - 数据来源：`internal/prompt/data/prompt.json`，由 `testdata/prompt/gen-oracle.ts`
    从真实 TS 代码路径写出（**不手抄**——Wave 1 假绿事故的教训）
  - 对账：9 个测试（含 24 子测试）全绿；31,355 字节 / sha256 `26043390ef70024e`
    与 TS 逐字节一致
  - 端到端：CLI 实际发出的 system prompt（捕获自 mock 端点）与 oracle
    逐字节一致，31,723 = 31,355 + 2 + 366
  - 变异反证 4 个全部触发红灯（分隔符 / 优先级 / 未知家族 / base 篡改）
- [x] **project-instructions 按节选取**：`internal/prompt/projinst.go`
  - 对账 `src/prompt/project-instructions.ts`（184 行纯函数）
  - 13 个 select + 6 个 split + 8 个 greedy + 8 个 escape 用例，逐字节等价
  - 变异反证 6 个全部有判别力（表格优先/`#`边界/首项分隔符/`&`转义顺序/
    围栏处理/两轮预留）
  - **缺陷修复（UTF-16 计费）**：首版实现用 `len([]rune(t))`（码点）作默认
    measure，而 TS 的 `.length` 是 **UTF-16 code unit**（emoji 计 2）。
    差异只在预算临界点显现——普通中文/ASCII 文档完全掩盖它。
    穷举搜索（20000 随机含 emoji 文档 × 全预算）定位到真实分叉点：
    文档码点 98 / UTF16 122，budget=79 时两种计费给出截然不同的选取
    （UTF16 全保住 vs 码点丢 3 节）。已引入 `UTF16Len` 并替换默认 measure
    与生产调用方（project.go），oracle 加 4 个 emoji 用例锁定。
  - **踩坑记录（重要）**：两轮预留的区分点用了**三版用例才找到**。前两版
    变异后仍全绿——根因是用例文档不够极端，且 TS 侧"单轮"对照物自身写错
    （note 用全部标题 vs 实际略去集），产生假差异信号。最终在 **Go 侧穷举
    搜索**（3000 文档 × 全预算）定位，并以 JSON 字面量原样嵌入文档。
    教训：变异 0 红 → 先怀疑用例无效与对照物有 bug，再怀疑测试无判别力；
    连续两轮 0 红就换穷举搜索，不要继续猜。
- [x] **项目指令接线**：`internal/prompt/project.go`
  - `LoadProjectInstructions` 读 cwd 下 AGENTS.md + .rivet.md（`\n\n` 拼接，
    对账 TS readRivetMd）
  - `RenderProjectInstructionsBlock(md, cap)` 对账 TS volatile.ts:1118-1124 的
    **完整路径**：选取预算先扣 `wrap`(=47) → 包裹 → 再过 `truncateBlock`
  - `BuildSystemPromptWithProject` 追加到 static 提示词尾部
  - 这是 `projinst.go` / `truncate.go` 的生产消费路径——没有它两者都是悬空代码
  - 注：仍是**最小可用路径**（不做 `<context>` 包裹）。后续移植 volatile 层时
    应**替换**本函数，而非在其上叠加（避免双写）

  **接线时的两个偏差（已修，值得记住）**：
  1. 选取预算漏了 `- wrap`（wrap=47 是包裹标签开销）
  2. 包裹后漏了再过 `truncateBlock` 这一道
  两处都是对照 TS 源码逐行核对时发现的——**接线不等于照抄签名**，
  要核对 TS 调用点的**每个参数与前后步骤**。

  **反直觉但必须复刻的行为**：`truncateBlock` 的结果**可以超出 cap**
  （实测 cap=200 → 233 字符）。它内部扣的是标签开销
  （`maxChars - tag.length*2 - 10`），而包裹标签加回来的可能更多。
  TS 就是这样，不能"顺手"让它不超。

  **端到端验证**（真实端点，9195 字符的超大 AGENTS.md）：模型确认看到
  「高危命令纪律」「参考章节0～99」「通用执行纪律」，以及
  `[本块超出前缀预算，已略去 20 节：参考章节100…119]` 标记——
  纪律类优先保住、参考类按预算丢、标记可见，正是算法意图。
### ✅ 已完成：truncateBlock / stripFirstMarkdownTable

`internal/prompt/truncate.go`。这是上轮预告的"最微妙的一处"，实际踩到的
坑比预告的更多：

1. **`truncateBlock` 的三重 UTF-16 语义全部要复刻**
   - `block.length <= maxChars` → 用 `UTF16Len`
   - `content.slice(0, N)` → **必须切断代理对**（`sliceByUTF16`，探针验证
     Go 侧可达：`"ab😀cd"` 切到 3 → `61 62 ef bf bd`）
   - 截断标记里的 `block.length` → code unit 数进最终字节

2. **JS 的负 slice 语义**：`slice(0, n)` 当 `n<0` 时从末尾倒数
   （`"abcdef".slice(0,-4)` = `"ab"`，超出则为空）。`xmlEmoji` 用例
   的 `limit = 30 - 12*2 - 10 = -4` 正是负值。

3. **单根 XML 匹配的正则无法用 Go regexp 表达**（三个叠加原因）：
   - 无反向引用（`\1`）
   - RE2 的贪婪语义与 JS 回溯不同（`(?s)(?m)` 组合会跨行吃到错误位置）
   - `m` 标志下 `$` 匹配行尾 + 贪婪 → 取**最后一个**满足行尾的同名闭合
     （实测 `<a>x</a>\n<a>y</a>` → content=`x</a>\n<a>y`）
   → 最终用手工 `matchSingleRoot` 实现，逐条对账上述语义。

4. **测试夹具自身也踩了坑**：首版 `extractBlock` 按 `\n\n` 分段找目标块，
   把 XML 开标签并入了前一段——golden 缺了开标签，我一度误判为**实现**缺陷。
   教训：oracle 生成器的提取逻辑本身也要验证（用"只含目标块"的 ctx 构造）。

**变异反证 6 个**（前 2 个首轮红 0 处，补用例后才有判别力）：
预算比较用码点（2 红）、slice 按 rune 切（2 红）、负 limit 当 0（3 红）、
闭合找第一个（2 红）、不校验闭合后行尾（2 红）、不删前置 `>` 行（4 红）。

> **补用例的方法论**：M1（预算比较）首轮 0 红的根因是唯一的码点≠UTF16
> 用例（`xmlEmoji`）**两种计费都判超预算**，判定结果相同。补一个"码点不超
> 但 UTF16 超"的用例（20 个 emoji，码点 20 < cap 30 < UTF16 40）才暴露。
> 这类"变异生效但不改变行为"的盲区，只能靠针对性的边界用例消除。

**剩余同类位点**（继续移植时逐个核对）：
- `volatile.ts:1003` `content.length > maxChars`（selectTopKBlocks）
- `engine.ts:806, 998-1001` 的 `rawChars` / 预览截断
- `prefix-budget.ts:48` `Math.ceil(text.length / 4)`

判据：TS 侧凡 `.length`、`.slice(`、`.substring(`、`.charCodeAt(` 的文本处理，
都要问"code unit 还是码点/字节"。oracle 用例必须含代理对字符（emoji）。

- [x] **frozen 稳定块**：`internal/prompt/volatile.go`
  - `BuildStableVolatileBlock(ctx, host)` 对账 TS volatile.ts:511/1055
  - 覆盖：environment / platform 相关 / sober / locus(self|world) /
    project-instructions / project-memory / knowledge-manifest / seed-capsule /
    codebase-index / working-set / session-memory / star-domain
  - 18 个 oracle 用例，含 cwd 转义与代理用对（emoji）
  - **宿主参数化**：`HostEnv{Platform, OSType, OSRelease}` 注入而非直接读 os
    ——这让 frozen 块可对账（golden 记录宿主值）。
    `DetectHostEnv()` 供生产用，用 `uname -s/-r`（实测与 Node 的
    os.type()/os.release() 逐字相同），并映射 Go 的 `windows` → Node 的 `win32`
  - 变异反证 7 个全部有判别力（块间分隔符/sober-locus 顺序/空串跳过/
    workingSet 转义/star-domain 共享纪律/blockCaps 合并/cwd 转义）
- [x] **frozen 块接线**：`internal/prompt/full.go` 的 `BuildFullSystemPrompt`
  - CLI 默认路径改用它（static + frozen 块）
  - 端到端验证：模型确认看到 `<context>` / `<environment platform="darwin"
    os="Darwin 25.6.0" />` / `<sober>`；多轮缓存命中率 0% → 79.8% → **99.0%**
  - 注：TS 侧 frozen 块是 trailer-merge 到 user message（engine.ts:659），
    此处拼在 system prompt 后是**最小可用路径**。后续移植 trailer-merge 时
    应**替换**本函数，而非叠加
- [x] **`detectRuntimeEnvBlock`**：`internal/prompt/runtimeenv.go`
  - 对账 runtime-env.ts（184 行），探测 python/node/rust/go 四种运行时
  - **可注入依赖**：`RuntimeEnvDeps{ReadFile, Probe}`——TS 侧本就如此（测试传
    fake probe），参数化后成为纯函数，21 个 oracle 用例逐字节对账
  - 踩到的语义细节：
    - rust 的 `??` 是 **null 合并**（空文件不回退 .toml），与 python 的
      truthiness 判定不同——易错点，有专项测试
    - `isDated` 的版本正则 `(\d+)\.(\d+)` **要求小数点**，故 declared "16"
      不判为 dated 而 actual "16.20.0" 会
  - 变异反证 7 个（M7 首轮红 0 处是**编译失败**而非测试无判别力——见下方教训）
- [x] **`stripFirstMarkdownTable` 接线**：projectIndexBlock 存在时剥离
  rivetMd 的首个表格（对账 volatile.ts:1113），2 个 oracle 用例
- [x] **runtime-env 接线**：块插在 environment 之后、sober 之前
  （对账 volatile.ts:1090）。**架构分歧（有意）**：TS 是内部探测
  （`detectRuntimeEnvBlock(ctx.cwd)`），Go 做成注入字段 `ctx.RuntimeEnv`
  以保纯度。故该接线点由 Go-only 测试覆盖（oracle 覆盖不了——固定假 cwd
  探测不出东西，真实 fixture 目录的临时路径不可复现）
  - **补漏（端到端验证暴露）**：首版接线只做了「插块位置」，漏了
    `BuildFullSystemPrompt` 里调 `DetectRuntimeEnvBlock`——字段恒空、
    块从不出现。Go-only 测试只测注入位置，测不出这个；是**真实端点验证**
    （问模型"有没有 runtime-env 块"）暴露的。已补测试
    `TestBuildFullSystemPromptRuntimeEnv` 锁定生产路径

> **教训（变异红 0 处的第三种高频成因：编译失败）**：本轮三次遇到「变异红 0 处」
> 实际是**编译失败**（删掉某处使用后变量/import 成为未使用，Go 编译不过，
> 测试根本没跑）。判据：看输出有无 `build failed`。规避：变异时加 `_ = x`
> 保留引用，或先单独跑一次确认能编译。
>
> **教训（`git checkout <file>` 会清掉该文件的未提交改动）**：本轮做变异反证后
> 用 `git checkout internal/tools/helpers.go` 恢复，把该文件**从未提交过的**
> 新增函数一并清掉了。变异恢复一律用 `cp` 备份/还原，
> **不要对未提交文件用 git checkout**。
>
> **教训（等价变异 ≠ 测试缺口）**：变异后红 0 处有两种成因——测试覆盖不到
> （真缺口），或**变异本身不改变行为**（等价变异）。判据：比对变异前后的
> **实际输出**，逐字节相同则是等价变异。本轮 M1（去掉 orderValue 的 map
> 递归）属后者：`orderedProps` 自身排序键、`writeSortedMap` 也排序键，
> 两条路径输出一致（已实测比对确认）。
>
> **教训（golden 复现性）**：首版 runtime-env 用例用 `mkdtempSync` 生成
> fixture 目录，**临时路径进了 golden** → 每次生成都不同 → 对账必然失败。
> 任何进入 golden 的路径/时间戳都必须可复现，否则测试是假的。本次的处置
> 是**不造这个 oracle 用例**（而非强行让它绿）——架构分歧用 Go-only 测试覆盖。

> **教训（变异红 0 处的第二种成因）**：M7 首轮红 0 处，我一度以为测试无
> 判别力；实际是**编译失败**（删掉 `hasProject` 的使用后它成为未使用变量，
> Go 编译不过 → 测试根本没跑）。变异反证必须确认测试**真的执行了**
> （看输出有无 `build failed`），而不只看 FAIL 计数。

- [x] **`renderDeclaredVerify`**：`internal/prompt/verifycmds.go`
  - **拆分设计**：`LoadDeclaredVerify`（读取，向上 20 层查找）+ 
    `RenderDeclaredVerify`（渲染，纯函数，12 个 oracle 用例逐字节对账）
  - 渲染对账点：四种 kind 的**固定顺序** test→build→typecheck→lint
    （与 config 书写顺序无关）、trim 后空项过滤、routes 追加在后、
    整体 escapeXml、无声明返回空串
  - **oracle 走真实路径**：`RIVET_TRUST_PROJECT=1` 绕过信任门 + fixture 目录
    （生成器里不手抄渲染逻辑——首版手抄过，自查后改掉）
  - 接线：块在 project-instructions 之后、project-memory 之前
    （对账 volatile.ts:1129），**不过 truncateBlock**（TS 侧无 cap）
  - 边界测试：向上查找的 20 层上限（M4 变异首轮红 0 处→补 19/20 层用例后
    双向可判别：20→1 红、20→30 红）
  - **未移植**：TS 的信任门（isProjectTrusted）——Go 侧无 trust store，
    故未授信项目也会返回声明。已知行为差异，记于此
- [x] **Windows note 三条**：`internal/prompt/winnote.go`
  - `WindowsShellNote(kind)` —— 四分支（bash/powershell/cmd 有文案，sh/其他空）
  - `renderPlatformNote(target, host)` —— 目标平台≠宿主时出现
  - `pathStyleNote` —— 目标 win32 时出现
  - **参数化**：`VolatileContext.TargetPlatform` / `.ShellKind` 注入，
    使三条 note 可测（本机 darwin 无法真实触发 win32 分支）
  - environment 行的 `host="..."` 属性（此前是死代码，现为真实逻辑）
  - 顺序对账：environment → platform-note → path-style-note → shell-note
    → runtime-env → sober
  - 变异反证 6 个（M2「不产生 host 属性」首轮红 0 处→补 environment 行
    形态断言后暴露）
- [ ] **未移植（Windows 特有，需真实环境验证）**：
  - `resolveShellCommand` 的 Windows 分支（探测 Git Bash 路径 / pwsh）。
    Go 侧 `DetectShellKind` 只做非 Windows 判定（恒返回 "sh"）；Windows
    返回空串（**保守选择**——宁不注入，也不注入可能错误的 shell 语法指引，
    后者会诱导模型反复失败重试）
- [ ] `buildDynamicAppendixParts`（动态 appendix）——**依赖会话状态容器，
  建议先做最小 session 状态**
- [ ] appendixDelta / 动态 appendix 的分段与冻结边界
- [ ] `internal/compact`：边界压缩（仅 `turn===0` 重写历史）
- [ ] `internal/cache`：命中率统计与 advisor
- [ ] **字节等价主判据**：同会话状态下 Go 渲染的 system prompt 与 TS 逐字节相同
  （static 层已达成；volatile 层待办）

> **方案变更说明**：原计划把提示词外置为 `assets/prompt/*.txt` 让 TS/Go 共读。
> 实施时改为 oracle 模式（golden + 生成器），理由：外置需改 `src/prompt/static.ts`
> 生产代码，且 tsup bundle 分发时资源文件能否进 dist 有未知风险；而 oracle
> 模式与 Wave 1/2 既有架构一致、零 TS 生产代码改动、漂移可检测。

### Wave 2 补充（工具地基）

- [x] **`hashLine` / `buildFreshAnchors`**：`internal/prompt/hashline.go`
  - 对账 src/tools/hash-edit.ts 的两个导出纯函数（572 行工具的地基）
  - **hashLine 关键语义**：先剥行尾 `\r` 再 sha256 取前 8 位（CRLF 归一化）
    ——故 `"abc"` 与 `"abc\r"` 哈希相同，跨平台锚点才一致
  - **buildFreshAnchors**：最多 4 个锚点（前文行/新区间首行/末行/后文行），
    内容去行尾空白 + 截断 80 字符加 `…`，**哈希用原始行**（未 trim）
  - 7 个变异反证全部有判别力（不剥 \r / 哈希长度 / 不去空白 / 截断阈值 /
    单行末行 / 开头前文 / 行号基数）
  - **未做**：hash_edit 工具本体（stale 锚点恢复 / 位移查找 / 语法检查）——
    属工具层，涉及文件 IO；本轮先立地基
- [x] **编辑失败计数门**（`internal/tools/editfail.go`）
  - 对账 `read-file.ts` 的 `editFailCount` Map + 三个函数
  - **语义**：按文件累计**连续**编辑失败次数；≥3 时在报错文案**前置**一句门禁
    提示（「此文件已连续 X 失败 N 次，再次编辑前必须先重新 read_file」）；
    成功编辑清零。**是提示性门禁**（文案前缀），不是硬拒绝——对账 TS 的
    `gatePrefix` 语义
  - `canonicalPathKey`：非 Windows **原样返回**（大小写敏感文件系统，
    不能 lowercase）；Windows 转 POSIX 分隔符 + lowercase
  - 接入 `write_file` / `hash_edit` / `apply_patch`（语法检查失败时递增、
    成功时清零；apply_patch 逐 target 处理）
  - 变异反证 6 个：**全部有判别力**（阈值 2→2 红 / 阈值 4→4 红 /
    reset 不清零→2 红 / increment 不递增→5 红 / hash_edit 不递增→1 红 /
    hash_edit 成功不清零→1 红）
- [x] **syntaxcheck 子系统 + 语法检查回滚**（新建 `internal/syntaxcheck/`）
  - **对账契约**：TS 的 `{ warning, fatal }`，`fatal` 非空 → 调用方回滚
  - **架构判断（有意差异，非降级）**：TS 版用 **esbuild**（+ TypeScript 编译器
    二次确认）检查 `.ts/.js`、用 tree-sitter 检查 `.py`——因为**它自己是
    TS 项目**。Go 版天枢的首要语言是 **Go**，而 Go 有**原生解析器**
    （`go/parser`），比 esbuild 更权威（TS 版自己都要做「二次确认」来过滤
    esbuild 的误报）。故按**生态重映射**：
    - `.go` → `go/parser`（原生、权威、零依赖）
    - `.json` / `.css` / `.html` → **纯算法**，与 TS **逐字节对账判定**
  - **未覆盖（已知边界）**：`.ts/.tsx/.js/.jsx`（需 esbuild，Go 无等价物）、
    `.py`（需 tree-sitter）。若将来 Go 版要服务 TS 项目，应引入 esbuild 的
    Go 绑定而非自己写解析器
  - oracle：`go/testdata/syntaxcheck/`（**只锁判定**，不锁消息文本——
    Go 与 JS 的解析器错误文本天然不同），三次 sha256 一致
  - 接入 `apply_patch`（`firstFatalSyntax`：逐文件检查，不可读则跳过，
    致命错误整补丁回滚）+ `hash_edit`（写后检查 + 从备份恢复）
  - **测试设计教训**：首版用 `t.Skipf` 处理「锚点格式不符」——结果**掩盖了
    变异**（M5 短路语法检查后测试 SKIP 而非 FAIL，显示红 0）。改为 `Fatalf`
    后 M5 立即变红。**Skipf 是变异的隐身衣**
  - 变异反证 5 个：**全部有判别力**（CSS 状态机失效 8 红 / HTML void 入栈 5 红 /
    Go 检查失效 8 红 / apply_patch 不检查 1 红 / hash_edit 不检查 1 红）
  - ✅ **`write_file` 也已接入**（原「已知边界」已消除）：
    - 回滚策略按**写入前是否存在**分流（对账 TS）：
      **新文件 → 删除**（无备份可恢复，不把语法损坏的残尸留在磁盘上）；
      **已存在文件 → 从备份恢复**
    - 变异反证 3 个：全部有判别力（不检查 2 红 / 新文件不删除 1 红 /
      `existedBefore` 恒 true 1 红）
- [x] **recovery 子系统 + apply_patch 失败回滚**（新建 `internal/recovery/`）
  - **背景**：上轮判定「数据损坏风险 > 新增工具」，`apply_patch` 失败回滚是
    与 TS 最显著的行为差异。调研发现它不是局部补丁——**依赖整个
    recovery-stack 子系统**（TS 侧被 write_file/edit_file/hash_edit/
    apply_patch **4 个写工具**共用），Go 侧完全没有。故先建地基。
  - **`internal/recovery/`**（独立包，非 session——文件操作的横切关注点，
    与「会话」概念正交）：
    - `journal.go`：恢复事件日志（`.rivet/recovery-journal.jsonl`）。
      **手写序列化器**——键序由 TS 的对象展开顺序决定
      （`file,action,linesLost,ts[,sessionId]`，探针实测），
      且 `ts` 必须是 **`toISOString()` 的固定 3 位毫秒**——Go 的
      `RFC3339Nano` 会省略尾随零（860ms → `.86`），字节不等价
    - `stack.go`：备份栈（`TrackFileChange` / `RestoreLatestBackup` /
      `EvictOldBackups`）。**核心不变量**：旧内容必须在覆写**前**捕获
  - **apply_patch 接入**：targets 快照 + 补丁前备份 + 失败时
    `rollbackTargets`（工作树内容从备份恢复 / 新建文件删除）+
    `unstagePatchTargets`（索引收回 HEAD）。文案区分「已回滚」与
    「未回滚」——后者是对结果的保证，不声称「曾发生半套用」
  - **`RIVET_APPLY_PATCH_VERIFY=0`** 退回 legacy 行为（不备份、不回滚）
  - **oracle**：`go/testdata/recovery/`，**时间戳已 scrub**（含时间的
    oracle 不可复现——首版三次 sha256 不一致，这是 oracle 纪律的漏洞）
  - **关键诊断**：索引污染的触发条件是 `--3way` **真走合并路径**。
    我最初三次构造的场景都走了「回退直接应用」（"lacks the necessary
    blob"），那条路径**本就什么都不留下**——所以 `unstagePatchTargets`
    当时看似等价变异。用**带 `index` 行的合法 patch**（`git diff` 产出）
    + 冲突内容才复现出 `UU a.txt`，M3 随即变红
  - 变异反证 7 个：**全部有判别力**（不回滚 1 / 不删新建文件 1 /
    不收回索引 1 / check_only 也备份 1 / journal 键序错 4 /
    ts 格式错 1 / 备份时序错 7）
  - **消费方接入**（write_file / edit_file / hash_edit）：
    - 备份点都在**写盘前**（核心不变量）。`dry_run` 分支提前返回故不备份；
      `append` 模式同样备份（对账 TS）
    - **发现并修复一个真实架构缺陷**：首版每个工具各持 `NewStack()` 实例
      ——而 TS 的 `latestBackups`/`memoryBackups` 是**模块级 Map**（跨工具
      共享）。后果：`write_file` 备份的文件，`apply_patch` 回滚时**读不到**。
      修法：`recovery.DefaultStack()`（`sync.Once` 单例）供生产路径，
      `NewStack()` 保留给测试构造独立实例
    - 备份路径用 `relForRecovery`（cwd 相对）——对账 TS 的
      `relative(params.cwd, filePath)`；备份目录布局与 journal 记录都用相对路径
    - 变异反证 5 个：4 个有判别力（write_file 不备份 3 红 / edit_file 不备份
      5 红 / hash_edit 不备份 1 红 / **各工具独立 Stack 2 红**）；
      M5（备份用绝对路径）因 `filepath`/`strings` 变未使用而编译失败
- [x] **工具 schema 与 TS 逐字节对账（缓存命中率防线）**
  - **背景**：Go 侧 `orderedProps` **字典序排序**属性键，TS 是**声明序**
    ——而工具定义变化「打的是整个前缀（system+tools 段）」
    （src/api/openai-client.ts:630），键序不同让前缀缓存**完全失效**
  - **更严重**：不只是键序——**参数名本身不同**（`read_file` 用 `path`
    而 TS 用 `file_path`）。这是**功能性缺陷**
  - oracle 从真实注册表导出，三次生成 sha256 一致（可复现）
  - **修掉 6 类缺陷** + 3 个消费方测试同步（详见欠账 #5）
  - 变异反证 5 个：4 个有判别力；**M2 不可构造**——类型签名阻止
    `strProp` 返回无序结构，这本身是类型的价值
- [x] **会话落盘接线（悬空消除）**：`internal/session/listener.go` + `loop.go`/`main.go`
  - **背景**：上一轮完成 `Persist` 后核查发现 `internal/session` 的组件
    **全部零生产调用**——能力已实现但从未接线。本轮消除悬空。
  - **架构决策**：落盘走**事件监听器**模式（对账
    `attachSessionPersistListener`），不是主循环内联调用——持久化是独立关注点
  - **flush 策略**（崩溃恢复的核心）：`user`/`tool` 无条件立即落盘；
    `assistant` **仅当带 `tool_calls`** 时立即落盘，纯文本走批量节拍。
    把硬杀损失窗口压到「在途记录」
  - **元数据增量**：每次 append 更新 title（仅首次，前 120 字符）/
    turnCount / toolCallCount / tokenUsage。**`<system-reminder>` 开头的
    user 消息不算真实回合**（TTSR 护栏注入，历史回放也排除）
  - `Manager.AddUsage`：token 累计。**InputTokens 是 cache-inclusive**，
    不再叠加 cacheRead/cacheCreation（否则 DeepSeek 下恰好翻倍）。
    TS 版 `addUsage` 还夹带上下文占用估算（EMA 校准），属 Wave 4 的
    `internal/context`，未移植
  - CLI 接线：`newSessionID()`（时间戳 + 随机，无 UUID 依赖）+
    `FlushSession()` 收尾排空
  - **修掉两个真实缺陷**：
    1. `applyPatch` **不处理 `turnCount`/`toolCallCount`**——这两个字段的
       0 是合法值，不能像字符串那样「非零才覆盖」，否则首次累加被丢弃。
       改用 `PresentKeys` 显式声明键存在
    2. `<system-reminder>` 判定用字节切片 `[:16]`——前缀实际 **17** 字符，
       off-by-one 导致判定恒假。改用 `strings.HasPrefix`
  - 变异反证 10 个：9 个有判别力；**M7 经查证为等价变异**——`AddUsage`
    对零值「无脑累加」与「跳过」数值等价（`+= 0`），且该函数无副作用
  - **端到端接线测试**（`internal/agent/persist_wiring_test.go`，7 个用例）：
    不碰网络，直接构造 Loop + 手动 `appendAndPersist`，验证消息真落到
    `<cwd>/.rivet/sessions/<id>.jsonl`、元数据真更新、跨 Loop 实例真能读回。
    **这补上了单元测试抓不到的接线缺口**——变异反证 5/5 全部有判别力
    （断开 Listener 构造 / 断开 append 调用 / 不累加 usage / 不排空 / Cwd 用错）
  - `session.NewID()`：会话 ID 生成移到 session 包（原在 main 包不可被测试引用）
- [x] **会话持久化编排层**：`internal/session/persist.go` + `legacy.go`
  - 对账 `SessionPersist` 类的**编排核心**（928 行里只取编排，不取压缩/清理）
  - `LoadOai` 完整链路：读 transcript（zstd 解码 + pending 合并）→
    `VerifyLines` 校验和过滤 → 逐行 `ParseSessionLine`（**跳过审计行**）→
    `IsOaiMessage` 判定 → 非 OAI 走 legacy 迁移 → `NormalizeOaiMessages` →
    `RepairOrphanToolCalls` → 有孤儿则首位插 system-reminder
  - `AppendOai`（校验和 + 入队 + 可选立即 flush）、`Flush`（transcript + 元数据
    共享节拍）、`ReadTranscriptText`（含 pending）
  - **legacy 迁移**（`legacyMessageToOaiMessages`）：user 的块数组拆成
    文本 + tool 结果；assistant 拆成文本 + thinking + tool_use；
    **tool_use 的 arguments 用 `stableStringify`（排序键）**而非 JSON.stringify
  - **反直觉行为（oracle 锁定）**：legacy 迁移产出的 tool_calls **没有对应
    tool_result** → 被判孤儿 → 触发 system-reminder。这是真实行为
  - 变异反证 7 个：6 个有判别力（审计行 2 / checksum 2 / 孤儿修复 1 /
    tool_result 拆分 2 / tool-only content 1 / thinking 迁移 1）；
    **M5 经查证为等价变异**——Go 的 `encoding/json` 对 map **一律按字典序**
    输出，与 `stableStringify` 行为一致，故替换无差异
- [x] **会话元数据存储**：`internal/session/metadata.go`
  - 对账 src/agent/session-metadata.ts（85 行）。内存缓存 + 批量落盘节拍：
    append 热路径每条消息更新元数据，每次写整个 meta.json 是读写放大热点
  - **手写缩进器 `indentJSON`**：必须逐字节匹配 `JSON.stringify(x, null, 2)`——
    12 个用例锁定（含字符串里的转义引号/反斜杠/花括号，状态机不能被干扰）
  - **重大发现：write 与 update 的键序不同**（oracle 锁定）：
    - `update`（合并构造）→ `compactEvents` **最先**
    - `write`（直接 stringify 调用方对象）→ 键序 = **调用方构造序**，
      即生产路径 `initMetadata`（session-persist.ts:519-529）的字面量序
  - **write 的输出键集精确等于调用方键集**——不是固定模板。首版无条件输出
    `turnCount`/`toolCallCount`，`writeFormat` 用例当场红。已加
    `PresentKeys []string` 显式表达键集
  - `load` 三态缓存（未加载 / 磁盘无文件 / 已加载）；`tokenUsage` 嵌套合并
  - 变异反证 7 个：5 个有判别力（缩进 1 / 尾换行 1 / sessionId 权威 1 /
    tokenUsage 合并 1 / PresentKeys 1）；**M5、M7 经查证为等价变异**——
    M5（flush 失败重设 dirty）是**冗余防御性赋值**（`Flush` 只在 dirty 时
    进写路径，失败时 dirty 本来就是 true）；M7（load 不缓存）是**性能优化**，
    不改变可观察输出
- [x] **会话消息序列化**：`internal/session/serialize.go`
  - 对账 `serializeSessionMessage` / `serializeOaiSessionMessage` /
    `capJsonValue` / `truncateString` / `serializeSessionJsonValue`
  - **三层截断**：① 原样 JSON ≤ 上限 → 返回；② 用 `max(1000, floor(上限*0.8))`
    递归 cap 后重序列化；③ 仍超长 → fallback（整条消息 JSON 截断塞进 content）
  - **截断 marker 逐字对账**：`original_chars` 是**原始值长度**、
    `kept_chars` 是**传入的 maxChars**（不是实际保留数——实际保留的是
    `maxChars - marker长度`）。这是 TS 原样行为，不要"修正"
  - **反直觉但必须复刻**：额度小于 marker 长度时 `keep` 归 0，输出**只有 marker**
    且**总长超过 maxChars**（marker 本身就有长度）
  - **重大发现：键序必须保留**。`JSON.stringify` 保插入序，而插入序由对象构造
    决定——oracle 新增 4 个键序用例（`keyOrderContentRole` /
    `keyOrderToolCallIdFirst` 等）证实「同字段集、不同书写序 → 不同输出」。
    首版 `oaiToWire` 用固定序重建，`oaiToolRole` 用例当场红。已给
    `OaiMessage` 加 `KeyOrder []string`（从 JSON 解析时记录原始序）
  - **三处测试期望是我算错的**（oracle 已证明实现正确）：① 截断后可能**超过**
    maxChars（marker 长度所致）；② 第 2 层用例的 maxChars 太小会**跳过第 2 层
    直接走 fallback**（cap 预算有 1000 下限）；③ fallback 的 content 在额度
    远小于 marker 时只剩 marker
  - 变异反证 7 个全部有判别力（预算下限 1 / 0.8 系数 2 / UTF-16 计长 2 /
    marker 扣额度 3 / 数组递归 1 / KeyOrder 1 / 归一化 2）
- [x] **孤儿工具调用修复**：`internal/session/oaimessage.go`
  - 对账 src/agent/session-persist.ts 的 `repairOrphanToolCalls`（压#7）
    + `normalizeOaiMessage` + `isOaiMessage`
  - **双向孤儿检测**：assistant 的 tool_call 无对应结果 → 剔除该 tool_call
    （全部孤儿且 content 空 → 整条丢弃）；tool 结果无对应 tool_call → 丢弃该行
  - **两条模型可见警告文案**（逐字对账）：`OrphanReminderWriteTool`（含写类
    工具被剔除 → 非破坏性：文件可能已改，先核实再重写）与
    `OrphanReminderGeneric`
  - **发现并复刻了 `loadOai` 的三层链路**：`isOaiMessage` 过滤 →
    `normalizeOaiMessages` → `repairOrphanToolCalls`。我首版只复刻了后两层，
    oracle 的 `emptyToolCallsNullContent` 用例立刻红——`{role:'assistant',
    tool_calls:[]}` 因**缺 content 键**（undefined 既非 string 也非 null）
    在第一层就被跳过，根本没进 normalize
  - **测试方法论的坑**：用 Go 结构体反序列化 oracle 无法区分「content 键缺失」
    与「显式 null」（都是 nil 指针），而 `isOaiMessage` 恰恰依赖这个区分。
    改为保留**原始 map 形态**才复刻成功
  - **`NormalizeOaiMessage` 的语义**：assistant 的 `tool_calls: []` 空数组
    必须移除（OpenAI 兼容 API 的 minItems:1 约束），content 为 null 时补空串
  - 变异反证 5 个全部有判别力（写类集合漏 hash_edit 2 / 孤儿 result 不丢
    **修正后** 2 / 全孤儿保留消息 1 / 空 tool_calls 不归一化 3 /
    isOaiMessage 不查 content 键 1）。M2 首轮**编译失败**（`i` 变未使用）
    ——本轮第 9 次遇到此成因
- [x] **write-behind 批量写入器**：`internal/session/batchwriter.go`
  - 对账 src/agent/session-batch-writer.ts（129 行）
  - 行先排队在内存，成批 flush 为**一个 zstd 帧**；200ms 定时窗口 / 显式
    flush 屏障触发
  - **新会话首行同步落盘**——文件立即存在（listSessions 靠 .jsonl 存在性
    解析会话），且崩溃在前 200ms 内不丢整个会话
  - **MergePending**：未 flush 的行对进程内读者可见（append 后立刻 load
    是合法用法）
  - **legacy 迁移**：首次写入时把纯文本 transcript 备份 + 转码为帧
  - **flush 失败把行放回队列**（ENOSPC 等不能永久丢行）
  - **反直觉发现（oracle 锁定）**：legacy 迁移的**备份目录不存在时不产生
    备份**——TS 的 `copyFileSync` 抛错被 `catch {}` 吞掉，转码照常。
    我首版 `copyFile` 会 `MkdirAll` 建目录，与 TS 行为不一致，已修
  - **两处测试期望是我算错的**（oracle 已证明实现正确）：① 首行同步落盘后
    pending 已清空，第二行入队时 `MergePending("")` 得 `"line2\n"` 而非
    两行；② 多批次是 **3 个帧**（首行同步是独立一帧）
  - 变异反证：M1 首行不同步 5 红 / M2 MergePending 忽略 pending 2 红 /
    M3 legacy 迁移（**首轮编译失败**，加 `_ = prompt.ZstdMagicLE` 后 3 红）/
    M4 flush 失败不放回（**首轮测试缺口**，补 IO 失败测试后 1 红）/
    M5 经查证是**不可达分支**（空串拼接不改 pending，不进 writeBatch）
- [x] **transcript codec（zstd 帧）**：`internal/session/transcript.go`
  - **引入首个第三方依赖** `github.com/klauspost/compress v1.20.0`
    （用户授权：允许引入成熟生态）
  - **跨版本兼容已双向验证**：Node 帧 → Go 解压 ✅、Go 帧 → Node 解压 ✅
    （`go/testdata/zstd/gen-frames.ts` 产 oracle，`verify-go-frames.ts` 验反向）
  - **判据是「互解」不是「同字节」**——实测 Node 用 singleSegment、Go 默认
    不用，descriptor 位不同（0x24 vs 0x04），但互相可解。**不要**把两者做
    逐字节对账。
  - torn tail（崩溃截断的末帧）被丢弃而非报错——`scanZstdFrames` 已实现
  - **库的默认行为陷阱**：klauspost 的 `NewWriter(nil)` **默认就开 CRC**，
    故 `WithEncoderCRC(true)` 是冗余显式声明（实测两种写法 descriptor 都是
    0x04）。保留它是为了**意图显式化**（对账 TS 的 checksumFlag: 1），
    但不要声称它有判别力——真正的判别力来自「篡改校验和后解压失败」
  - 变异反证：M1 空文本产帧 1 红 / M2 不做帧判定 1 红 / M3 torn tail 也解压
    2 红；M4（关 CRC）、M5（空 buffer 特判）经查证为**等价变异**
- [x] **会话状态容器**：`internal/session/`（新包）
  - 对账 src/agent/session-state.ts（326 行，TS 侧**零 import**——完全自包含）
  - `Manager`：状态管理（文件/决策/验证/事实/任务列表）+ `RenderForVolatile()`
  - **两个反直觉行为（oracle 锁定）**：
    1. 空状态返回**空串**而非 `<session-state></session-state>` 空壳
       ——空壳会让下游 truthiness 检查误判「状态存在」（TS 注释记录了这个 bug）
    2. 超 500 字符**先砍 decisions 段**；砍完只剩开标签则整体丢弃；仍超长才截断
  - **FileIndex 必须保插入序**（不是 map）——TS 的 `Object.entries` 保插入序，
    `Modified:` 行按插入序列出。首版用 map+排序，oracle 的 manyModifiedFiles
    用例立刻红（字典序 file0,file1,file10 vs TS 的 file0..file9）
  - **过滤阈值 `length > 3` 是 UTF-16 code unit 语义**——中文三字恰好 3 units
    被过滤。首版 oracle 用例内容太短（"新描述"），把合并路径整个掩盖了
  - `extractTaskList`：三正则（顺序敏感）+ 合并语义（保留旧 status/turnCreated）
    + 状态标记（completed/blocked/in_progress，**顺序敏感**）
  - **接线进 agent loop**：`Loop.State` + `observeToolResult`（read→TrackFileRead、
    write/edit/hash_edit/apply_patch→TrackFileModified、run_tests→RecordVerification）
  - **接线验证测试**（`TestStateWiring`）——单测 session 包全绿 ≠ 接线有效
  - 变异反证 10 个全部有判别力（阈值 3 / decisions 3 / failed 3 / 空壳 18 /
    合并 3 / 文件上限 2 / content 3 / 标记顺序 2 / verification 4 / UTF-16 1）
  - **降级项**：JSONL 落盘（session-persist，928 行）未移植——它需要
    zstd 压缩（`encodeBatch`/`decodeTranscriptText`），Go 标准库无 zstd
    且项目约束零第三方依赖。本包只做**纯状态 + 渲染**
- [x] **apply_patch 工具本体**：`internal/tools/applypatch.go`
  - 对账 src/tools/apply-patch.ts（423 行）。工具集 9 → 10
  - **核心是调 `git apply --3way`**——不自己实现 diff 解析（行为等价的
    本质是同一个 git 实现）
  - `normalizeDiffPaths`：**只**归一化 `--- `/`+++ `/`diff --git` 头部行
    （内容行里的反斜杠是数据，不能动）
  - `truncateDiffForUI`：超 600 行截断 + 提示行
  - 路径逃逸预检 + 指针守卫（仅本工具前缀）
  - **oracle 用真实 git 仓库**（`git init` + commit 基线，`--3way` 需要）
  - **对账暴露一处已声明降级**：TS 在 git apply 失败时主动回滚（备份 +
    unstage），故文案含「已回滚到补丁前状态」；Go 无回滚故**不含该前缀**
    ——不虚假声称已回滚。测试显式承认该差异
  - **M5 首轮红 0 是测试缺口**：`TestApplyPatchPathEscape` 只断言 IsError，
    但去掉预检后 git 自身也报错（两条防线都拦）——输出文本完全不同
    （预检给明确的路径错误 vs git 给「outside a repository」）。补强断言
    检查**具体错误来源**后有判别力
  - **M6 首轮红 0 是变异未生效**（shell 转义把 python 脚本搞坏，文件未改）
    ——改用 `cat > /tmp/m.py` 写脚本方式后红 1。本轮第 5 次遇到这类假红 0
  - 变异反证 7 个全部有判别力（空 diff 2 / 指针守卫 3 / --3way 2 /
    --check 2 / 路径预检 1 / normalize 1 / truncate 1）
- [x] **hash_edit 工具本体**：`internal/tools/hashedit.go` + `eol.go`
  - 对账 src/tools/hash-edit.ts（572 行）。工具集 8 → 9
  - 纯函数层：`ParseAnchor`（完整格式先试，否则 "L5:hash" 会被位置正则截成 L5）、
    `RecoverStaleAnchors`（±200 窗口 + 一致漂移量重搜，50 窗口）、
    `FormatStaleDiagnostic`（**含可重试锚点**——这是模型唯一的重试线索）
  - `eol.go`：EOL 策略（`.bat`/`.cmd` 强制 CRLF > 既有 > 目标平台默认；
    `toLF` 顺序敏感——先 `\r\n` 再裸 `\r`）
  - **oracle 走真实 `execute` 路径**：三个核心函数都未导出，手抄必假绿
  - **取证纠正实现**：JS 模板插值 `null` 得到字面量 `"null"`——我首版写成
    `<position-only>` 占位符，探针实测后修正
  - **测试断言错误**：`TestParseAnchorParity` 首版断言"TS 侧解析成功"，
    但 `badAnchorFormat`/`anchorZero` 的锚点**故意非法**（TS 同样失败）——
    是我错，不是实现错
  - 变异反证 8 个：7 个有判别力（正则顺序 1 / 窗口 1 / null 渲染 1 /
    toLF 顺序 1 / EOL 平局 1 / eof 重试 2 / 升序检查 1），
    M3（漂移非零判断）**实测确认为等价变异**（shift==0 时 50 窗口是 200
    窗口的子集，必失败，两路径输出相同）
  - **降级项（4 条，见下方欠账）**：指针回灌守卫、语法检查+回滚、
    失败计数门、dry_run 的 diff 预览
- [x] **todo 工具本体**：`internal/tools/todo.go`
  - 消费了 todofmt 的渲染（FormatTodoList / FormatTodoSummary）——
    这是本轮**唯一有生产调用方**的新增能力
  - read / write 两个 action；write 是**整体替换**；接受结构化数组与
    **原始 JSON 字符串**两种 todos 形态（模型常见形态）
  - 校验：id/content 非空、status 三值枚举
  - **接上 default_registry**（工具集 7 → 8）
  - **连带修复一个架构缺陷**：`loop.go` 的 `orderedProps` 原先只递归
    `map[string]any`，不处理 `[]any` 内的嵌套 object。数组型 schema
    （如 todo 的 todos.items）会让 `wire.writeValue` 收到裸 map 或
    非 map 类型而 panic。新增 `orderValue` 递归处理
  - 回归测试 `TestToolDefsArraySchemaNoPanic` 锁定该修复
- [x] **todo 清单渲染**：`internal/prompt/todofmt.go`
  - 对账 src/tools/todo-store.ts 的 `TodoStore.formatList` / `formatSummary`
    ——模型直接读到的清单文本
  - 三个状态 icon（✓ / ► / ○，未知状态兜底 ○）、`已更新：N/M 已完成` 计数、
    formatList 带 status 后缀而 formatSummary 不带
  - **两个真实行为差异（oracle 锁定）**：
    - `formatSummary` 空清单**不特判**（渲染 `已更新：0/0 已完成` + 空行），
      而 `formatList` 空清单返回固定文案
    - `activeForm` 字段**不参与渲染**（schema 里有但两个方法都渲染 content）
  - 空清单文案 `TODO_EMPTY_RESULT` 逐字对账（TS 注释要求「勿改成另一份字面量」）
  - 7 个变异反证全部有判别力（icon 三元组 19 红 / 去 status 后缀 11 红 /
    加 status 后缀 11 红 / 空清单特判 3 红 / 计数状态 7 红 / 文案改字 3 红 /
    渲染 activeForm 3 红）
- [x] **会话行校验和**：`internal/prompt/checksum.go`
  - 对账 src/agent/checksum.ts（108 行，4 个导出纯函数）
  - 行格式 `{json}|{checksum}`，checksum = SHA-256 前 8 字节（16 hex）
  - **legacy 兼容是核心**（三条判定）：① 无 `|` ② `|` 后非 16 位小写 hex
    ③ `|` 前非合法 JSON。这三条让"JSON 内容含 `|`"的行不被误判
  - 用 **lastIndexOf** 取最后一个 `|`（对账 TS）
  - oracle 12 checksum + 18 verify + 6 batch；7 个变异反证全部有判别力
  - **M4 首轮红 0 处是编译失败**（去掉判定后 `json` 包成为未使用 import）
    ——本轮第三次遇到这个成因，见下方教训
- [x] **zstd 帧扫描**：`internal/prompt/zstdframe.go`
  - 对账 src/agent/session-transcript-codec.ts 的 `scanZstdFrames` /
    `isZstdFrameStream` —— 会话 transcript 的跨版本兼容基础（Wave 5 判据）
  - **纯字节逻辑**（RFC 8878 帧头走查，不解压块），16 个 oracle 用例
  - **torn tail 语义**：崩溃截断的末帧应被**丢弃**（返回其起点）而非报错
    ——这是崩溃恢复的核心，错一位整条会话读不出来
  - 损坏（魔数错 / 保留位 / 保留块类型）→ 抛错，与 torn 明确区分
  - **未做**：encode/decode（需 zstd 压缩库；项目至今零依赖，Go 标准库无 zstd）
- [x] **`extractPatchTargetPaths`**：`internal/prompt/patchpath.go`
  - 对账 src/tools/apply-patch.ts:34 —— 从 unified diff 提取目标路径
  - 语义：只看 `+++ ` 行、trim、tab 截断、跳过 `/dev/null`、去成对引号、
    去 `a/`/`b/` 前缀（**只这两个**，`c/`/`d/` 不剥）、Set 去重保序
  - oracle 21 用例；7 个变异反证全部有判别力
  - **边界（oracle 锁定）**：diff 正文里以 `+++ ` 开头的行**也会**被提取
    ——TS 只看行首前缀，不区分头部与正文。看起来像 bug，但是真实语义
  - **未做**：apply_patch 工具本体（diff 应用 + 冲突检测）

### Wave 4（Agent 循环深化）
- [ ] `internal/agent/hooks.go`：五阶段 `Pipeline`（超时 / 迟到收尾记账 / 统计）
- [ ] 首批 hook 移植（常驻基线 10 个，非全量 74 个）
- [ ] `internal/context`：CognitiveLedger / ClaimStore / Stigmergy / PressureMonitor
- [ ] `internal/session`：JSONL 落盘（格式兼容 TS 版）
- [ ] 审批门禁链（plan-mode / deny 规则 / 风险评估）

### Wave 5（表面层）
- [ ] TUI（纯 ANSI）
- [ ] `internal/config`：多层配置（默认 → `~/.rivet` → 项目）
- [ ] 跨版本兼容：Go 版写的会话 JSONL 能被 TS 版读取（反之亦然）

## 本轮（自主推进）完成情况

### 已完成

**Wave 3（prompt 引擎）全部收口**：
- static 层（31,355 字节逐字节等价）+ calibration
- project-instructions 按节选取 + 块截断（UTF-16 语义完整复刻）
- frozen 稳定块（12 类块，18 个 oracle 用例）
- IO 探测块：`detectRuntimeEnvBlock`（21 用例）、`renderDeclaredVerify`（12 用例）
- Windows 三条 note（platform / path-style / shell）
- **端到端验证**：真实端点下模型确认看到 `<context>` / `<environment>` /
  `<runtime-env>` / `<verify-commands>` / `<sober>`；缓存命中率 99.0%

**Wave 2 工具地基**：
- `hashLine` / `buildFreshAnchors`（hash_edit 的地基）
- `extractPatchTargetPaths`（apply_patch 的地基）

**本轮（工具落地轮）** —— 把地基变成可用工具：
- `checksum.go`：会话行校验和（legacy 兼容三判定 + lastIndexOf）
- `todofmt.go`：todo 清单渲染（icon 三元组 / 计数 / 两处空清单差异）
- `todo.go`：**todo 工具本体**，接上 default_registry（工具集 7 → 8）
  —— 本轮唯一有生产调用方的新增能力，并连带修复了数组型 schema 的
  序列化 panic（见下方架构欠账 1 与教训）
- `zstdframe.go`：zstd 帧扫描（跨版本兼容基础）
- `hashedit.go` + `eol.go`：**hash_edit 工具本体**（工具集 8 → 9）——
  纯函数层逐字节对账（含诊断文本），工具本体走真实 oracle 路径
- `applypatch.go`：**apply_patch 工具本体**（工具集 9 → 10）——核心是调
  `git apply --3way`，oracle 用真实 git 仓库对账
- `internal/session/`：**会话状态容器**（新包）——纯状态 + volatile 渲染，
  已接进 agent loop
- `internal/session/transcript.go`：**zstd 帧编解码**——引入
  `klauspost/compress v1.20.0`，跨版本兼容双向验证通过
- `internal/session/batchwriter.go`：**write-behind 批量写入器**——首行同步
  落盘 / mergePending / legacy 迁移 / 失败放回队列
- `internal/session/oaimessage.go`：**孤儿工具调用修复 + 消息归一化**——
  双向孤儿检测 / 两条警告文案 / loadOai 三层链路
- `internal/session/serialize.go`：**消息序列化 + 三层截断**——capJsonValue
  递归截断 / truncateString marker / KeyOrder 保插入序
- `internal/session/metadata.go`：**会话元数据存储**——内存缓存 + 批量落盘节拍 /
  手写缩进器 / write 与 update 的键序差异
- `internal/session/persist.go` + `legacy.go`：**持久化编排层**——`loadOai`
  完整链路 / legacy 迁移 / 审计行跳过
- `internal/session/listener.go`：**落盘接线**——flush 策略 / 元数据增量 /
  wire 桥接（`OaiMessageFromWire`）
- `internal/tools/schema.go`：**schema 有序序列化**——`OrderedProps` /
  `OrderValue`（从 agent 包移入，schema 序列化属 tools 领域）
- `internal/tools/editfail.go`：**编辑失败计数门**——连续失败 ≥3 时前置
  read_file 提示
- `internal/syntaxcheck/`：**语法检查**——`.go`（原生解析器）+
  `.json/.css/.html`（纯算法）。判定与 TS 对账，语言按生态重映射
- `internal/recovery/`：**备份与恢复**——journal（事件日志）+ stack
  （备份栈，`DefaultStack()` 进程级共享）。**4 个写工具的共享地基**：
  write_file / edit_file / hash_edit / apply_patch 均在写盘前调
  `TrackFileChange`

**本轮核心教训**：`orderedProps` 的数组型 schema 缺陷只在**接线后**暴露
（单测工具全绿，接注册表立刻 panic）。这印证了「消费方核查」与
「端到端验证」的必要性——`type-without-consumer` 会掩盖真实缺陷。

### 验证状态

`go test ./...`（11 包全绿、0 FAIL）、`-race`（0 FAIL）、`go vet`（OK）、
`gofmt`（零违规）。分支 `go-runtime` 共 49 个提交，**未 push**。

### 未完成（后续会话的起点）

**Wave 2 剩余**（工具本体，均涉及文件 IO）：
- `hash_edit` 工具本体（stale 锚点恢复 / 位移查找 / 语法检查，约 500 行）
- `apply_patch` 工具本体（diff 应用 + 冲突检测）
- `git` / `job` / `todo` / `ast_grep` / `diff` / `repo_map`
- 工具 preset 三档（minimal/frontend/full）

**Wave 4（Agent 循环深化）** —— 未开始：
- 五阶段 `Pipeline`（超时 / 迟到收尾记账 / 统计）
- 首批 10 个常驻 hook
- `internal/context`（CognitiveLedger / ClaimStore / Stigmergy / PressureMonitor）
- `internal/session`（JSONL 落盘，格式兼容 TS 版）
- 审批门禁链（plan-mode / deny 规则）

**Wave 5（表面层）** —— 未开始：
- TUI（纯 ANSI）、多层配置、跨版本 JSONL 兼容

### 架构欠账（已知，非缺陷）

1. **`mapToOai` 的键序是启发式的**：Go 的 `map[string]any` 遍历无序，
   无法复现 TS 的 `JSON.parse` 键序。当前用 `orderKeys` 按「role → content →
   tool_calls → tool_call_id → reasoning_content」重排——**覆盖了生产路径的
   常见形态，但不是通用解**。若某会话文件的键序与此外不同，读-改-写会改变
   字节序（不影响语义，但破坏"读回再写出应与原文一致"）。**正确解法**是
   在解析层保留原始键序（用有序 map 解析 JSON）。
2. **session 剩余（zstd 依赖已解决）**：
   - ✅ **transcript codec**（zstd 帧 encode/decode + torn tail）——本轮完成
   - ✅ **write-behind 批量写入器**（`session-batch-writer.ts`）——本轮完成
   - ✅ **孤儿工具调用修复 + 消息归一化**（`repairOrphanToolCalls` /
     `normalizeOaiMessage` / `isOaiMessage`）——本轮完成
   - ✅ **消息序列化 + 三层截断**（`serializeSessionMessage` /
     `serializeOaiSessionMessage` / `capJsonValue` / `truncateString`）——本轮完成
   - ✅ **会话元数据存储**（`SessionMetadataStore`）——本轮完成
   - ✅ **编排层**（`loadOai` / `append` / `flush` + legacy 迁移）——本轮完成
   - **`SessionPersist` 剩余**：`compact` 系列（压缩重写，依赖 boundary
     coordinator）、`delete` / `evictOldSessions`（清理策略）、
     会话记忆（`appendSessionMemory`，依赖 context 层）、frozen 快照。
   - **已知偏差**：`mapToOai` 的键序用启发式重排（Go 的 map 遍历无序，
     无法完全复现 TS 的 JSON 键序）——见下方欠账。
   - **会话层地基已全部就位**：BatchWriter（写）+ transcript codec（压缩）+
     孤儿修复（完整性）+ 序列化（截断）+ 元数据（持久化）+ 编排（读写链路）
     + 状态容器（渲染）。
   - **会话恢复**（`session-recovery.ts` 140 行）、**会话注册表**
     （`session-registry.ts` 589 行）未移植。
   - **依赖策略变更**：项目已从「零第三方依赖」改为「允许成熟生态」，
     后续 `go.mod` 会有更多依赖——注意保持 `go.sum` 提交完整（他人
     拉取需能复现构建，已用 `-mod=readonly` 验证）。
3. **apply_patch 的降级**（部分已修复）：
   - ✅ **补丁前备份 + 失败回滚**——已完成
   - ✅ **应用后语法检查回滚**（firstFatalSyntax）——已完成
   - ✅ **编辑失败计数门**——已完成
   - **client-delegate（apply_edit 通道）**：未移植。
   - **跨工具指针检测**：仅做 apply_patch 自己的前缀检查。
4. **hash_edit 的降级**（部分已修复）：
   - ✅ **语法检查 + 回滚**（checkSyntax）——已完成
   - ✅ **失败计数门**（连续 3 次要求先 read_file）——已完成
   - **指针回灌守卫**（pointer-guard）：依赖 4 个未移植的 arg-processor
     常量模块（write_file / edit_file / hash_edit / apply_patch）。风险：
     模型可能把历史里的指针文本当 `new_string` 传回来并被写进文件。
   - **dry_run 的 diff 预览**（buildFileDiff / computeChangedLineRanges，
     185 行）：Go 侧只返回行数变更摘要，不含 unified diff。
5. ✅ **工具 schema 已与 TS 逐字节对账**（本轮完成）
   - oracle：`go/testdata/toolschema/`（从**真实注册表**导出，非手抄）
   - 测试：`TestToolSchemaParity`（名+声明序+required）+
     `TestToolSchemaByteParity`（**完整序列化字节**，含嵌套键序与描述文本）
   - **修掉的真实缺陷**：`read_file` 参数名 `path`→**`file_path`**（并补
     `file_paths`/`focus`/`focus_max_matches`）；`bash`/`run_tests` 的
     `timeout_ms`→**`timeout`**；`edit_file` 缺 `expected_count`/`dry_run`；
     `todo` 缺 `acceptance`；`grep` 键序错；多处描述文本与约束
     （`enum`/`minimum`）不符
   - **结构性修复**：`contract.InputSchema` 加 `PropOrder []string`；
     属性值改用 `*wire.OrderedMap`（`map[string]any` 无法表达嵌套键序）
   - **未覆盖**：嵌套 object 的键序目前用字典序（oracle 显示 TS 这几处
     恰为字典序）；`apply_patch` 不在默认注册表故未覆盖
6. **schema 描述文本的漂移风险**：工具 schema 的 `description` 现已与 TS
   逐字节对账，但 TS 侧改描述时 Go 不会自动跟随——需重跑 `gen-oracle.ts`
   并修 Go 文本。这是**有意的**（显式失败优于静默漂移）。
7. **frozen 块位置**：TS 是 trailer-merge 到 user message（`engine.ts:659`），
   Go 侧拼在 system prompt 后——`full.go` 注释标了是「最小可用路径」。
   后续移植 trailer-merge 时应**替换**而非叠加。
8. **三处「最小可用路径」待替换**：`BuildFullSystemPrompt`（拼法）、
   `RenderProjectInstructionsBlock`（无 `<context>` 包裹）、
   `BuildSystemPromptWithProject`（已被 `full.go` 取代但保留，因 11 个测试锁定它）。
9. **未移植的行为差异**：TS 的信任门 `isProjectTrusted`（Go 侧无 trust store）；
   Windows 的 `resolveShellCommand`（需真实 Windows 环境验证）。
10. **分支策略**：`go-runtime` 已 push 到 `origin`（2026-09-19）；
   `main` 仍在 `69b0381` 未动（用户明确要求不合并）。Go 实现**将来要独立
   仓库**——当前 `go/` 与 TS 源码同仓库是过渡状态。拆分可行性已核实：
   52 个提交无交叉改动（无一个同时改 `go/` 与 `src/`）、所有 `.go` 都在
   `go/` 下、`go/` 有独立 `go.mod`。**拆分时的约束**：`go/testdata/*/gen-oracle.ts`
   用相对路径 import 父仓库 TS 源码（生成 golden 用），拆出后无法重新生成
   golden——建议生成器留在 TS 仓库（它们是「对账工具」，本就该跟被对账对象
   在一起），Go 仓库只保留 `oracle.json`。

## 建议的第一刀

**接 `internal/session`（最小会话状态容器）**。

理由：它同时解锁两处——
- Wave 3 剩余的 `buildDynamicAppendixParts`（动态 appendix，依赖工具历史与
  turn 计数）
- Wave 4 的 hook 管线（多数 hook 需读会话状态）

且它的产出可独立验证（JSONL 落盘格式与 TS 版兼容）。

**若优先补工具**：`hash_edit` 工具本体（地基已就绪，直接接）。

（以下为历史记录）
static 层（BASE_PROMPT + calibration）已完成并逐字节对账通过——
Go agent 现在跑的是真正的认知资产（31,355 字节），不是占位符。

下一个主战场是 **volatile 块与动态 appendix**（`src/prompt/volatile.ts`，
1,263 行）：每轮的 git 状态快照、工具历史、待办、星域提示等。
这一层是缓存命中率的**真正杠杆**——它决定哪些内容进冻结前缀、哪些
进尾部增量。判据仍是逐字节等价，但 oracle 需构造带 volatileCtx 的用例。

注意 volatile 层依赖会话状态（工具历史、turn 计数），需要先有
会话状态容器（Wave 4 的 session 部分），两层有耦合——建议先做
最小 session 状态，再上 volatile 渲染。

其余待办见下方 Wave 2/3/4/5 清单。
