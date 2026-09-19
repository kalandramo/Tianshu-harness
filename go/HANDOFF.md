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

## 环境注意

- `golangci-lint`（go1.25 构建）与 `staticcheck` **无法**在 Go 1.27 下运行
  （前者报版本低于目标，后者报导出数据版本过旧）。替代验证：`go vet` + `gofmt`
- 项目 `node_modules` 需 `npm install` 后才有；`npx tsx` 会走临时目录导致
  `zod` 等传递依赖解析失败。用 `node_modules/.bin/tsx` 跑 oracle 生成器
- 端到端冒烟可用**本地 mock 端点**完成，无需真实 API key：写一个返回构造
  SSE 的临时 HTTP 服务，用 `TIANSHU_BASE_URL` 指过去

## 剩余工作（按建议优先级）

### Wave 1 剩余
- [ ] 重试引擎已就位，但**未接入真实端点的 `cache_read_input_tokens > 0` 验证**
      （需真实 key；当前证据强度止于「与真实 TS 客户端字节一致」）

### Wave 2 剩余（工具内核）
- [x] ~~`bash`~~ 已完成（超时 / 进程组清理 / 破坏性硬闸门 / 输出截断标记）
- [ ] `run_tests`（项目测试运行器探测）——**优先级最高**，让「跑验证」不必手拼命令
- [ ] `apply_patch` / `hash_edit`（结构化编辑）
- [ ] `ast_grep`（需 tree-sitter 绑定）
- [ ] `todo` / `job` / `git` / `diff` / `repo_map`
- [ ] 工具 preset 三档（minimal / frontend / full）

### Wave 3（Prompt 引擎）
- [ ] `internal/prompt`：static(frozen) + volatile + appendixDelta 三段拼接
- [ ] 提示词资产外置（`src/prompt/static.ts` → `assets/prompt/*.txt`，TS/Go 共读）
- [ ] `internal/compact`：边界压缩（仅 `turn===0` 重写历史）
- [ ] `internal/cache`：命中率统计与 advisor
- [ ] **字节等价主判据**：同会话状态下 Go 渲染的 system prompt 与 TS 逐字节相同

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

**接 `run_tests` 工具**。理由：闭环已通，但模型要跑验证得手拼
`go test ./... 2>&1 | tail -3` 这类命令——而 `run_tests` 的价值在于
**项目测试运行器探测**（package.json scripts / pytest / go test / cargo test
自动识别）+ 结构化结果（passed/failed/blocked 与 blockedReason），
让「跑验证」变成可靠动作而非拼字符串。

之后建议补 `internal/prompt`（Wave 3）——那是字节等价的下一个主战场：
system prompt 的冻结锚 + volatile + appendixDelta 三段拼接，判据是
同会话状态下 Go 渲染结果与 TS 逐字节相同。
