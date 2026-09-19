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
  - **踩坑记录（重要）**：两轮预留的区分点用了**三版用例才找到**。前两版
    变异后仍全绿——根因是用例文档不够极端，且 TS 侧"单轮"对照物自身写错
    （note 用全部标题 vs 实际略去集），产生假差异信号。最终在 **Go 侧穷举
    搜索**（3000 文档 × 全预算）定位，并以 JSON 字面量原样嵌入文档。
    教训：变异 0 红 → 先怀疑用例无效与对照物有 bug，再怀疑测试无判别力；
    连续两轮 0 红就换穷举搜索，不要继续猜。
- [ ] volatile 层剩余：`buildVolatileBlockInternal`（148 行）的平台行/sober/
  locus/working-set/session-memory/star-domain 拼接，以及
  `buildDynamicAppendixParts`（动态 appendix）。**动态部分依赖会话状态容器，
  建议先做最小 session 状态**；稳定块的其余部分依赖 runtime-env /
  verify-config / git-status-summary 等外部模块，需逐个移植
- [ ] appendixDelta / 动态 appendix 的分段与冻结边界
- [ ] `internal/compact`：边界压缩（仅 `turn===0` 重写历史）
- [ ] `internal/cache`：命中率统计与 advisor
- [ ] **字节等价主判据**：同会话状态下 Go 渲染的 system prompt 与 TS 逐字节相同
  （static 层已达成；volatile 层待办）

> **方案变更说明**：原计划把提示词外置为 `assets/prompt/*.txt` 让 TS/Go 共读。
> 实施时改为 oracle 模式（golden + 生成器），理由：外置需改 `src/prompt/static.ts`
> 生产代码，且 tsup bundle 分发时资源文件能否进 dist 有未知风险；而 oracle
> 模式与 Wave 1/2 既有架构一致、零 TS 生产代码改动、漂移可检测。

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

## 建议的第一刀

**接 `internal/prompt` 的第二层（volatile + appendixDelta）**。
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
