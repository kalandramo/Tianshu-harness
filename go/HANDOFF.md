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

1. **工具 schema 键序未与 TS 对账**：`orderedProps` 主动对键**排序**（字母序），
   而 TS 侧 schema 由 zod 生成（**插入序**）。当前注释自称「只要每次生成
   顺序一致即可保证请求体稳定」——这保证了**确定性**，但**未保证与 TS 字节
   等价**。工具 schema 进请求体时（`tools` 字段）是缓存命中率风险。
   待办：对账 TS 的真实 schema 键序，决定是否改为保插入序。
2. **frozen 块位置**：TS 是 trailer-merge 到 user message（`engine.ts:659`），
   Go 侧拼在 system prompt 后——`full.go` 注释标了是「最小可用路径」。
   后续移植 trailer-merge 时应**替换**而非叠加。
3. **三处「最小可用路径」待替换**：`BuildFullSystemPrompt`（拼法）、
   `RenderProjectInstructionsBlock`（无 `<context>` 包裹）、
   `BuildSystemPromptWithProject`（已被 `full.go` 取代但保留，因 11 个测试锁定它）。
4. **未移植的行为差异**：TS 的信任门 `isProjectTrusted`（Go 侧无 trust store）；
   Windows 的 `resolveShellCommand`（需真实 Windows 环境验证）。
5. **`main` 分支未合并**：`go-runtime` 有 47 个提交，`main` 仍在 `69b0381`；
   分支**未 push**。

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
