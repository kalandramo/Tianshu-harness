# Go 重写天枢运行时 — 进度交接

> 更新：2026-09-19 · 本文件供后续会话（含遗忘后的自己）直接继续。
> 计划全文见 `.rivet/plans/go-重写天枢运行时-分波移植计划.md`。

## 现状一句话

**Go 版已能干活，且认知层已大部分落地**：`tianshu -p "提示词"` → 请求构造 →
模型调用 → 工具执行 → 结果回灌 → 终答。Wave 1/2/3 收口，Wave 4（agent 循环
+ CVM + context）推进到**压缩链路闭环**。

**当前规模**（2026-09-19 实测）：

| 指标 | 值 |
|---|---|
| internal 子包 | 16 |
| 生产代码 | 22,444 行 |
| 测试代码 | 23,949 行 |
| 测试用例 | **1,918 个 PASS** |
| oracle 数据集 | 38 个 |
| 注册工具 | 10 个 |
| `go/` 提交数 | 103（截至 `c843a14`；本文档自身的提交会使该数 +1） |
| 验证基线 | 19 包全绿 / `-race` / `go vet` / `gofmt` 零违规 |

**四步闭环已真实验证**（本地 mock 端点 + 真实端点）：读 calc.go（发现
`a - b` bug）→ edit_file 修复为 `a + b` → bash 跑 `go test ./...` → 文件确实
改动、测试确实从红变绿。真实端点（`ai.ctaigw.cn/v1` + `deepseek-v4.1-flash`）
下前缀缓存命中率 **99.0%**。

**分支状态**：`go-runtime` 已 push 到 `origin`（HEAD 随每刀推进）；
`main` 仍在 `69b0381` 未动（用户明确要求不合并）。

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
│   ├── syntaxcheck/            语法检查（.go 用原生解析器）
│   ├── filediff/               unified diff（自写 Myers，展示用）
│   ├── trust/                  项目级信任门（fail-closed）
│   ├── recovery/               备份与恢复（journal + stack）
│   ├── context/                认知上下文（rounds / pressure）
│   ├── compact/                **压缩**（策略 / 压力 / 决策 / micro / 折叠）
│   ├── session/                会话状态 + 持久化（JSONL / zstd / 元数据）
│   ├── tools/                  工具内核（注册表 + 10 个工具）
│   └── agent/                  agent 主循环 + CVM hook + 压缩接线
└── testdata/                   oracle 生成器与 golden（38 个数据集）
```

**`internal/compact` 的结构**（压缩链路，本阶段主战场）：

| 文件 | 职责 |
|---|---|
| `policy.go` | 策略阈值（watch/compact/reactive/ceiling）+ 自适应 + 精度天花板 |
| `pressure.go` | PressureMonitor（八字段 + log2 压缩 + thrashing 检测） |
| `action.go` | `DecideCompactAction`（六种 action）+ 熔断器 + 阈值计算 |
| `micro.go` | `MicroCompactOai`（截断 + 轮次删除）+ token 估算 |
| `context_collapse.go` | 语义折叠（7 个按工具名分派的折叠器） |

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

- **Windows 原生可移植性**（2026-09-20 修复，详见下节）——此前 `go build ./...`
  在 Windows 上**编译不过**（`internal/tools` 用了 Unix-only 的 `syscall.Setpgid`
  / `syscall.Kill`），连带 `internal/agent` 与 CLI 入口全红。现已修复。
- `golangci-lint`（go1.25 构建）与 `staticcheck` **无法**在 Go 1.27 下运行
  （前者报版本低于目标，后者报导出数据版本过旧）。替代验证：`go vet` + `gofmt`
- 项目 `node_modules` 需 `npm install` 后才有；`npx tsx` 会走临时目录导致
  `zod` 等传递依赖解析失败。用 `node_modules/.bin/tsx` 跑 oracle 生成器
- 端到端冒烟可用**本地 mock 端点**完成，无需真实 API key：写一个返回构造
  SSE 的临时 HTTP 服务，用 `TIANSHU_BASE_URL` 指过去

### Windows 可移植性（2026-09-20 修复）

**背景**：此前所有开发在 macOS 上完成，Windows 上 `go build ./...` exit=1——
不是测试问题，是**编译期**失败。修复涉及 4 类根因：

| # | 根因 | 修复 |
|---|---|---|
| 1 | `syscall.Setpgid` / `syscall.Kill` / `SIGKILL` 在 Windows 的 `syscall` 包中**不存在** | 按平台拆分 `internal/tools/proctree_{unix,windows}.go`（build tag）；Windows 走 `taskkill /F /T /PID` |
| 2 | `pathsafe.resolveUnder` 的绝对性判定与 Node 分叉——Go `filepath.IsAbs("/etc/passwd")`=**false**，Node `path.win32.isAbsolute`=**true** | 显式复刻 Node 语义：根相对路径重基到 base 的卷 |
| 3 | **超时在 Windows 上形同虚设**——孙进程继承管道写端句柄，`cmd.Wait()` 阻塞到 EOF | `cmd.WaitDelay`（`prepareCommand` 统一设置，两平台通用） |
| 4 | 测试夹具的平台假设（手拼 JSON 嵌 Windows 路径、`.exe` 后缀、mode 位、EOL 默认） | 见下 |

**关键实测数据**（本机 Windows，可复现）：

- **根因 3 的代价**：`bash -c "sh -c 'sleep 20' & sleep 20"` + timeout 500ms
  → `cmd.Wait()` 实测阻塞 **19–20 秒**。taskkill 确实杀掉了整棵树（杀后无残留），
  但句柄未释放，调用被吊住。加 `WaitDelay=2s` 后 → **2.0s** 返回。
  探针对照（pipe 20.3s vs DevNull **0s**）确证是管道句柄问题。
- **根因 2 的代价**：`/etc/passwd` 在 Windows 上被静默重基进工作区
  （`<tmp>\etc\passwd`）——**fail-open 的安全缺口**（TS 正确拦截，Go 放行）。

**测试夹具的平台陷阱（4 类，值得记住）**：

1. **手拼 JSON 嵌路径**：`` `{"file_path":"`+path+`"}` `` 在 Windows 上产出
   `{"file_path":"C:\Users\..."}`——`\U` 是**非法 JSON 转义**，工具报 error，
   测试以「hook 未触发」「工具结果未回灌」等**间接症状**失败，掩盖真实根因
   （夹具坏了，不是链路断了）。共 21 处，改用 `json.Marshal` 的
   `toolTurnArgs` / `sseToolCallArgs` / `sseToolCallArgsCLI2` 辅助。
   **注意**：外层 `jsonStr` 只转义嵌入，救不了本身就是非法 JSON 的输入。
2. **`.exe` 后缀**：`go build -o tianshu-test` 在 Windows 上产出的无扩展名文件，
   **bash 能跑但 Go 的 `exec.Command` 找不到**（走 `PATHEXT` 补扩展名查找）。
   CLI E2E 因此整片红且报错指向「找不到可执行文件」。改用 `buildCLIBinary`。
3. **mode 位**：Windows 上 `os.WriteFile(..., 0o600)` 的 mode **恒为 0666**
   （`os.Chmod` 只支持只读位），POSIX 语义无法表达，真实边界是 NTFS ACL。
   信任文件权限测试改为平台感知断言（`perm_{unix,windows}_test.go`）。
4. **EOL 默认**：`chooseEOL` 的平台默认随 `runtime.GOOS`，测试硬编码「非 Windows
   应为 LF」在本机误报。改为断言 `targetEOL()`。

**验证基线**（本机 Windows，2026-09-20）：`go build ./...` exit=0、
`go vet ./...` exit=0、`gofmt -l` 零违规、`go test ./... -count=1` **连跑 3 次全绿**
（20 包 ok、0 FAIL）。修复前：3 包 `[build failed]` + 2 包 FAIL。

**变异反证**（3 个，全部有判别力）：
M1 去掉 pathsafe 根相对分支 → 2 处红（原 bug 症状）；
M2 去掉 `WaitDelay` → 组杀测试红（耗时 20.2s，断言命中）；
M3 Windows 组杀改 no-op → 组杀测试红（20.9s）。
**M2 首轮红 0 处是测试缺口**（原测试只验「子进程被杀」，不验「调用及时返回」）
——补耗时断言后才有判别力，这正是「红 0 成因：真测试缺口」的又一实例。

### shell 探测移植（2026-09-20，第二刀）

补齐 HANDOFF 记录的最后一项 Windows 未移植。核实后发现**问题比记录的大**：

**它不是提示词问题**。原记录说「`DetectShellKind` 返回空串，保守选择」——
但 TS 的 `bash.ts:537` 用 `getShellCommand()` **实际 spawn shell**，而 Go 硬编码
`exec.Command("bash", "-c", ...)`。在没装 Git Bash 的 Windows 上，**bash 工具
直接启动失败**（不是提示词不精确，是功能不可用）。

**移植内容**：

| 层 | 落点 | 说明 |
|---|---|---|
| 探测（纯函数） | `internal/platform/platform.go` | `ResolveShellCommand` + `ResolveGitBashPath`，与 TS 的 `ShellProbeDeps`/`GitBashProbeDeps` 同构 |
| oracle | `testdata/shellprobe/` | 30 用例（17 git-bash + 13 shell），走 TS 真实函数调用 |
| 接线（工具） | `internal/tools/bash.go` | `exec.Command` 改用探测出的 shell |
| 接线（提示词） | `internal/prompt/full.go` | `DetectShellKind` 消除恒空占位 |

**Windows 优先级**：Git Bash → pwsh → powershell → cmd.exe。
Git Bash 路径探测五级：`RIVET_GIT_BASH_PATH` 覆盖 → `where git` 推导 →
`where bash` 兜底（**排除 WSL 的 System32\bash.exe**）→ 常见安装位/Scoop →
bundled PortableGit（**最后**，让系统 Git 优先）。

**为什么做成纯函数 + 注入依赖**：探测依赖 PATH/文件系统/环境变量，直接读进程
状态就**不可对账**（测试机与生成 golden 的机器不同）。参数化后 oracle 能记录
「给定这组 deps，TS 返回什么」。这是 TS 自己的做法。

**oracle 当场抓到一处假绿**（值得记住）：TS 的 `deps.env` 是 **`NodeJS.ProcessEnv`
对象**，不是取值函数。生成器首版传了函数 → `deps.env['RIVET_GIT_BASH_PATH']`
恒为 `undefined` → 所有 env 相关用例静默产出 null/wrong。**若照那份 golden 写
Go，两边会同错**（正是 wire 字段序事故的同一模式）。oracle 的价值就在这里：
它让这种错误在生成阶段现形，而不是在"对账通过"的假象里潜伏。

### DetectHostEnv 的 Windows 字节不等价（2026-09-20，同轮发现）

**端到端验证暴露**（又一次：单测全绿但生产路径有问题）。

`<environment os="...">` 行在 Windows 上 Go 与 TS 不等价：

| 来源 | os.type()/OSType | os.release()/OSRelease |
|---|---|---|
| Node（TS 生产） | `Windows_NT` | `10.0.26200` |
| Git Bash `uname`（**原 Go 实现**） | `MINGW64_NT-10.0-26200` | `3.6.9-b4195d69.x86_64` |
| 无 Git 时 fallback | `Windows_NT` | `""` |

该行进**冻结前缀**——不等价会让 Go/TS 的缓存 key 分叉。更糟的是 `uname` 的
结果**依赖 PATH 上有没有 Git**：同一个二进制从不同环境启动会产出不同的冻结
前缀（第三种值），这是最坏的一类不确定性。

**修法**：Windows 分支改用 Win32 `RtlGetVersion`（`hostenv_windows.go`），
实测返回 `10.0.26200` 与 Node **逐字节相同**；Unix 分支保留 `uname`（Node 在
Unix 上就用 uname(2)，实测 Darwin 一致）。用 `syscall.NewLazyDLL` 而非
`golang.org/x/sys/windows`——前者是标准库，零新增依赖。

**变异反证**（5 个，全部有判别力）：M1 反转 pwsh 优先级 → 1 处红；
M2 去掉 WSL 排除 → 2 处红；M3 bundled 提前 → 1 处红；
M4 Windows OSType 退回 uname → 报 `MINGW64_NT-10.0-26200`；
M5 OSRelease 退回 uname -r → 报 `3.6.9-b4195d69.x86_64`。
**M5 首轮是「编译失败伪装红 0」**（去掉 fmt 使用后 import 未使用）——本轮第 15 次
遇到该成因，按纪律加 `_ = fmt.Sprintf(...)` 保留引用后才有判别力。

### run_tests 的 Windows spawn 与参数注入面（2026-09-20，第三刀）

移植 `resolveTestSpawn`（`testspawn.go`）。核实后发现**Go 与 TS 的处境不同**，
需要分别判断，不能照抄：

**差异一：Go 不需要 TS 的 shell 路由**。TS（Node）拒绝在不带 `shell:true` 时
spawn `.cmd`（抛 EINVAL），故必须路由到 shell。Go **能直接**执行 `.cmd`
（实测 `exec.Command("npm", "--version")` 成功，Go 内部经 cmd.exe 解释）。
所以 `shell:true` 在 Go 侧的首要目的（让命令跑起来）已由别的机制满足。

**差异二：但安全层必须移植——这是真实缺口**。既然 Go 执行 `.cmd` 时参数仍经
cmd.exe 解析，就有注入面。实测（本机 Windows，`exec.Command(someCmdShim, args...)`）：

| 参数 | 结果 |
|---|---|
| `"a b"`（含空格） | Go **自动加引号** → 安全 |
| `"a & b"` | 同上，`&` 被引号保护 → 安全 |
| `"a&b"`（**无空格**） | **`&` 被解释执行** → 注入 |
| `"a\|b"` | **命令被拆分** → 注入 |
| `"x%PATH%"` | **变量展开** → 信息泄露 |
| `"a^b"` | **`^` 被吞** → 数据损坏 |
| `"x("` | 被解释 |

即：**Go 的自动引号只在含空白时触发**，不含空白的元字符全部裸露。TS 的
`quote`（`%`/`"` 消毒为 `_` + 条件加引号）正是堵这个面的，已移植为
`quoteCmdArg`。注意**引号挡不住 `%` 展开**（实测确认）——故 `%` 必须替换而非
仅加引号。

**顺带修掉一个真实缺陷**：`buildCmd` 的 `shell:true` 路径原先硬编码
`exec.Command("bash", "-c", ...)`。`declared` 命令（`.rivet-config.json` 的
`verify.test`，可能是复合命令 `a && b`）在 Windows 上会走错 shell——Git Bash
语义与 cmd.exe 不同，且未装 Git 时直接失败。改用 `platform.HostShellCommand()`
按平台选（Windows cmd.exe / Unix sh）。对齐 TS：TS 的 `shell:true` 交给 Node，
Node 在 Windows 用 cmd.exe。

**接线点**（对账 TS `runTestCommand` 的 spawnSpec 分支）：
`declared`（`shell:true`）**绕过** `resolveTestSpawn` 直接经平台 shell 执行；
其余（npm/npx/tsx/node/pytest）经 `ResolveTestSpawn` 规范化。

**oracle**：`testdata/testspawn/`，23 用例（TS 原 7 个 + 注入面补充）。
**又抓到一次自己的错**：我手写的期望 `"%PATH%"` → `"_PATH_"`，oracle 显示 TS
返回 `__PATH__`（`"` 本身也属危险字符，一并替换为 `_`）。按纪律以 oracle 为准
修正，未自行推断。

**变异反证**（3 个，全部有判别力）：M1 去掉 `%` 消毒 → 3 处红（`%PATH%` 裸露）；
M2 永不加引号 → 6 处红（元字符全裸露）；M3 去掉「不双重加引」短路 → 2 处红
（已引号 token 被破坏）。

### Windows 控制台输出乱码——流式编码解码器（2026-09-20，第四刀）

上轮遗留项「`WinStreamDecoder` 未移植」的处置。**核实后确认是真缺陷**，且与
上一刀的 shell 探测**直接耦合**。

**问题**：本机代码页是 **936（GBK）**。实测：

| 命令 | 原始字节 | 直读结果 |
|---|---|---|
| `cmd /c "echo 中文测试"` | `d6 d0 ce c4 b2 e2 ca d4`（GBK） | `\xd6\xd0\xceĲ...` **乱码** |
| `bash -c "echo 中文测试"` | `e4 b8 ad e6 96 87...`（UTF-8） | `中文测试` ✓ |
| `cmd /c chcp` | `bb ee b6 af...`（GBK） | `活动代码页: 936`（修复后） |

**耦合点**：shell 由探测决定——**未装 Git Bash 时回落 cmd.exe**，那时中文必乱码。
所以上一刀引入的探测能力，让这个缺陷从「理论存在」变成「可达路径」。

TS 侧的处置（`WinStreamDecoder`）是首块探测 UTF-8/GBK + 流式解码；TS 注释记录
他们**移除**了 `chcp 65001` 前缀（`nul` 重定向在沙箱环境失败），改由解码器兜底。

**Go 实现有两处必须比 TS 更小心**（都是探针实测出来的，不是推的）：

1. **`transform.Bytes` 每次调用重置状态**。逐块调用会让每个多字节字符的首字节
   单独解码成替换字符——探针实测全部产出 `ef bf bd`。必须持有
   `transform.Transformer` 反复调 `Transform`。
2. **单块 `utf8.Valid` 不是可靠的编码判据**。探针实测：单字节 `d6`（GBK 首字节）
   与 `e4`（UTF-8 首字节）的 `utf8.Valid` **都是 false**——无法区分「残缺（等更多
   字节）」与「非法（判 GBK）」。故实现 `utf8PrefixStatus` 显式区分这两种状态，
   未判定期间累积字节。
3. **GBK 转换器对不完整序列返回 `nSrc=0`**（一个字节都不消费），要求调用方累积
   重试。故 `transformChunk` 返回未消费的**残留字节**，解码器持有到下次拼接——
   丢弃残留会让半个字符永久丢失（实测症状：逐字节喂入只解出第一个字）。

**接线**：`bash` 的 `limitedWriter` 与 `run_tests` 的 `decodingWriter` 在写入时
解码（保持跨块状态）；限流仍按原始字节，但截断落在 rune 边界
（`truncateAtRuneBoundary`），避免切断 UTF-8 字符。

**新增依赖**：`golang.org/x/text`（`encoding/simplifiedchinese` + `transform`）。

**变异反证**（3 个，全部有判别力）：M1 不切 GBK → 4 处红（全乱码）；
M2 丢弃残留 → 1 处红（只解出第一个字）；M3 退回单块 `utf8.Valid` → 1 处红
（UTF-8 被误判为 GBK，产出 `涓�枃`）。
**M1 首轮是「编译失败伪装红 0」**（去掉 `simplifiedchinese` 使用后 import
未使用）——本轮第 16 次遇到该成因，加 `_ = simplifiedchinese.GBK` 保留引用后
才有判别力。

### reclaim gate —— 压缩回收量的经济闸门（2026-09-20，回主线第一刀）

Windows 系列闭环后回主线，做 HANDOFF 建议的压缩执行层第一块。

**它解决的问题**（TS 注释记录的真实事故）：确定性重写（micro / stale-round）
曾**无条件提交**候选。会话 `2c1186f5` 显示重写只回收 617–1,701 token（有时
甚至让输入变大），却每次都击碎 200k+ token 的热前缀缓存——纯亏本压缩。

**两块实现**：

| 落点 | 内容 |
|---|---|
| `internal/compact/profile.go` | `CompactionWindowBand` + `WindowBandFor` + `CompactionProfileInput` + `DeriveCompactionProfile` |
| `internal/compact/reclaim.go` | `ReclaimEstimate` + `EstimateReclaim` + `ShouldCommitReclaim` + `BuildReclaimDecision` |
| `internal/agent/compact_boundary.go` | 接线：候选先过 gate，不够本则**原消息原样保留** |

**reclaim 地板矩阵**（`DeriveCompactionProfile`，TS plan §3.2 第一版）：

| profile | minReclaimTokens | ratio |
|---|---|---|
| small/medium + per-token + exact-prefix | max(8192, floor(w×0.03)) | 0.03 |
| large + per-token + exact-prefix | max(32768, floor(w×0.05)) | 0.05 |
| subscription 或 cache none/partial | max(4096, floor(w×0.01)) | 0.01 |

**两轴的经济含义**（TS 注释）：per-token provider 为 cache-miss 重建付真金
白银，故重写必须回收够多才划算；订阅制只付延迟。而持久精确前缀缓存
（DeepSeek/GLM/MiMo）会被任何历史重写击碎——回收不够就是纯亏。

**五条判定分支**（顺序敏感）：
1. 未改动 → **永不提交**（即使 force——提交它仍会推进 appendix 基线与
   compact 标记，纯副作用）
2. force（硬天花板）→ 提交任何**已改动**候选，无视地板（替代方案是 OOM）
3. 无回收（<=0）→ 拒
4. 绝对或相对地板未达 → 拒
5. 否则提交

**实现要点**：`floorMul` 用整数运算（`n*3/100`）而非 `int(float64(n)*0.03)`
——后者的截断方向与 `Math.floor` 在边界上可能不同，且 0.03 无法精确表示。

**oracle**：`testdata/reclaim/`，8 个 windowBand 边界 + 11 个 profile + 8 个
reclaim 用例（覆盖全部五条分支，含「负回收」与「force+unchanged」两个边角）。

**变异反证**（3 个，全部有判别力）：M1 无视 gate 判定 → 2 处红（拒绝时仍改
历史）；M2 不查 `changed` → 4 处红（含 `force_unchanged` 被误提交）；M3 不查
地板 → 3 处红（含接线层「应拒却放行」）。

**测试构造的坑（值得记住）**：`TestCompactBoundary_ReclaimGateBlocksUnprofitable`
首版构造错了——我按「60K 字符 tool 结果」估回收量，实测回收 10479 > 地板 8192，
**走了放行分支**，测试「通过」但没测到想测的东西。根因是没算清截断目标：
`ToolResultMaxTokens = floor(contextWindow × 0.3)`，回收量 ≈ tool token − 该目标。
修正为「窗口 20000 → 目标 6000；tool 约 8000 token → 回收约 2000」后，实测
`reclaimed=6479 < floor=8192` 命中拒绝。**教训**：断言「不该发生 X」的测试，
必须验证 X 的可达性——否则它只是恒真断言。

**未接**：`resolveCompactionEconomics`（装配层）——它依赖 `classifyCostModel`
与 provider cache defaults，属尚未移植的 provider 模块。当前 `reclaimProfile()`
由 `Profile.Billing`/`Cache` + `ContextWindow` 直接派生（语义等价于 TS 的
缺省分支）。

### 缓存顾问延迟 —— 热缓存时推迟压缩（2026-09-20，回主线第二刀）

**它解决的问题**：压缩会击碎前缀缓存——已付费建立的前缀作废，下次请求全量
重建。但重建成本 ∝ 命中率，而压缩收益随窗口压力上升。故存在显式权衡：
**热缓存 + 低压力时推迟**（余量还够，不值得重建），压力升高时放行
（1M 下 OOM 风险 > 重建成本）。

**实现**（新包 `internal/cache`）：

| 落点 | 内容 |
|---|---|
| `internal/cache/warmth.go` | `SessionWarmthTracker`（hot/warm/cold，时间经注入） |
| `internal/cache/advisor.go` | `Advisor.ShouldDelayCompact` + 决策记录（可观测性契约） |
| `internal/agent/compact_boundary.go` | 接线：delay 检查在 reclaim gate **之前**，force 绕过 |

**判定三条分支**（顺序敏感）：

1. `tier >= 3` → **永不延迟**（响应式/ceiling 是应对压力的，延迟它等于放任爆掉）
2. 有压力上下文 + 有命中率 → `protection = hitRate × (1 − pressure)`
   - `>= 0.45` → 延迟
   - 否则若 `warmth=hot && tier<=1 && pressure<0.5` → 延迟
   - 否则放行
3. 无压力上下文 → 回退：`hitRate >= 0.8` 延迟；否则 `warmth=hot && tier<=1` 延迟

**oracle**：`testdata/delaycompact/`，10 个 warmth 边界 + 20 个 delay 用例
（覆盖三条分支 + 边界值）。

**变异反证**：M2（protection 阈值 0.45→0.99）→ 3 处红（含接线层），有判别力。
M1（去掉 `!force` 守卫）→ **红 0 处，经查证是等价变异**（见下）。

#### 三个结构性发现（值得记住）

**发现一：`!decision.Force` 守卫是死分支。** 探针确认：`force` 动作的 `Tier`
**恒为 4（ceiling）**（决策层 ceiling 分支硬编码 `Tier: TierCeiling`），而
advisor 的 `tier>=3` 短路**自己就放行**。故即便去掉 `!force` 守卫，force 场景
也不会被延迟——M1 变异红 0 处是**等价变异**而非测试缺口。保留守卫（对齐 TS
语义，且 tier 语义未来可能变），并用 `TestCompactBoundary_ForceTierIsCeilingSoAdvisorAllows`
**锁定这个事实**，避免后人误以为该分支被覆盖。这与上一轮「CompactBoundary 里
的熔断器检查是死分支」同类。

**发现二：决策层 tier 受 hitRate 影响，与 advisor 的 hitRate 是两个独立输入。**
`TierForRatio` 用 `AdaptiveCompactPolicyRatios`——`hitRate >= 0.85` 时各档
**上移**（`Watch+0.05` 等）。故 `CompactBoundary.RecentHitRate` 与
`Advisor.RecentHitRate` 需分别设置；测试里若只设后者，前者会走基准比值。

**发现三（接线约束）**：`MaxTokens >= 1_000_000` 时决策层走**独立的 LLM 阶梯
分支**——`ActionMicro` 只在 ceiling 时出现；且 `tier=3`（reactive）时决策层
直接返回 `ActionNone`。故测 delay 接线必须用**中小窗口 + 低阈值**。

#### 测试构造的坑（三条，都踩过）

1. **oracle 生成器的 warmth 状态与用例名脱节**：首版 `makeAdvisor` 在
   `hitRate=null` 时不调 `onTurnEnd`，于是 warmth 停在 cold，而用例名暗示 hot
   ——名实不符。修正为显式 `warmth: 'cold' | number` 字段。
   **教训**：oracle 的构造代码也要审——它错了会产出「看似合理」的错误 golden。
2. **恒真断言**（上轮教训重演）：`AdvisorAllowsUnderPressure` 首版用 200K 窗口
   + 75K token 历史 → pressure 仅 0.375，仍在延迟侧，断言「应放行」失败。
   pressure 必须**过半**才能压过 protection 与 warmth 两支。
3. **`Skipf` 掩盖**：`ForceBypassesDelay` 首版用 `t.Skipf` 处理「未触发 force」
   ——那会让测试在 force 未触发时静默通过。改为 `t.Fatalf` 暴露构造问题
   （项目纪律：Skipf 是变异的隐身衣）。

### session split —— 判定层（2026-09-20，回主线第三刀）

**它解决的问题**：1M 窗口下，纯缓存经济会把压缩一路推迟到精度悬崖之后。
session split 是**主动**护栏：86% 时把历史替换为结构化 handoff（task-state +
轨迹 + 近期推理），让会话以干净状态继续，而非等上下文爆掉。

**时机关键**：它在 `addUserMessage` **之前**运行（TS 的 `preUserMessageSplit`）
——若在之后，刚发送的 user 指令会被连同历史一起摘要替换，用户观感是「消息被
截断」（TS 注释记录的真实回归）。

#### 范围（有意收窄，**不是**「等价简化版」）

本刀只移植**判定层**。执行层依赖 Go 侧**全无**的三个子系统：

| TS 依赖 | Go 现状 |
|---|---|
| `replaceWithCheckpoint`（anchor 保留 / artifact 归档 / task anchor / `resetAppendixBaseline`） | 无 |
| `buildStructuredHandoff`（8 章节） | 仅最小子集（近期推理 + 文件清单） |
| `extractTaskState`（`src/agent/task-state.ts`，需 trajectory） | **无** |
| `artifact store`（`archiveDiscardedHistory`） | **无** |

故 `TrySessionSplit` **只判定 + 构造候选 handoff，不改消息列表**——由调用方
决定如何替换历史（避免半套用：判定了但替换逻辑不完整会让会话处于中间态）。
移植那三个子系统后应**替换** `BuildSessionHandoff`，而非在其上叠加。

#### 悬空缺口与修复（2026-09-20 当日补）

**首版交付漏报的问题**：`TrySessionSplit` 落地时**没有生产调用方**——只在
定义（`compact_boundary.go`）与测试里出现。这是项目纪律明确警告的
`type-without-consumer`（悬空代码），而当时的交付报告**没有指出**。

**发现方式**：交付后自检时 grep 消费方，确认 `loop.go:784` 只调
`MaybeCompact`，没有 `TrySessionSplit`——而 TS 的调用序是
**先 split、再 maybeCompact**（`compact-boundary-coordinator.ts:124`）。

**修复**：接到 `loop.maybeCompactAtBoundary`（`loop.go:799`），置于
`MaybeCompact` **之前**（顺序有意义：split 的判定依据是历史占用，若先走常规
压缩，占用已被改动）。

**执行层未移植下的诚实处理**（三点）：
1. **不替换历史**——`replaceWithCheckpoint` 未移植，故 `l.messages` 不动。
2. **发可见事件**——文案含「执行层未移植，历史未替换」，不静默、不谎称完成。
3. **不阻断常规压缩**——TS 是 split **成功**才 `userMessageConsumed = true`；
   Go 侧未执行 = 未成功，故不该 return（否则上下文压力无人处理）。

**回归测试**：`loop_split_wiring_test.go` 五条，其中
`TestMaybeCompactAtBoundary_SessionSplitHasProductionCaller` 专门锁定「有生产
调用方」——防止未来重构再次让它悬空。变异反证 M1（移除调用）→ 测试 panic
（`LastSplitDecision` 为 nil 被解引用），有判别力。

#### 判定层语义（两条门槛，顺序敏感）

1. `contextWindow < 500_000` → 不 split（**即使 ratio 极高**）
2. `ratio < 0.86` → 不 split
3. 否则 split


**顺序敏感的证据**：oracle 的 `small_window_never_splits` 用例 ratio 高达 1.562
但窗口 128K → false。且 TS 在窗口门槛处**提前 return**，根本不调
`getEstimatedTokens()`——故 oracle 用小窗口用例的 token 字段为 `null`。

**为什么 500K**（TS 语义）：中小窗口的缓存经济比值已压得够早，精度退化不是
瓶颈；split 是为大窗口设的。

---

### task-state + trajectory + todo-deps（2026-09-20，回主线第四刀）

**目标**：session split 执行层的前置——TS 的 `buildStructuredHandoff` 依赖
`extractTaskState`（需 trajectory）与 todo 依赖排序。本刀把这三个模块移植并
**接进 handoff**（不再降级）。

#### 三个新文件

| 文件 | 对账 TS | 关键导出 |
|------|---------|----------|
| `internal/compact/trajectory.go` | `src/agent/trajectory.ts` | `TrajectoryRecorder` / `TrajectoryEntry` / `TrajectorySummary` |
| `internal/prompt/tododeps.go` | `src/tools/todo-deps.ts` | `DetectDependencies` / `OrderPendingByExecutability` / `ComputeMaxDepth` / `FindExecutable` |
| `internal/compact/taskstate.go` | `src/agent/task-state.ts` | `ExtractTaskState` / `TaskStateFromTodos` / `TaskState` |
| `internal/compact/handoff.go` | `buildStructuredHandoff`（`compaction-controller.ts:199`） | `BuildSessionHandoffWithState`（**9 章节完整版**） |

#### 接线（防悬空）

- `Loop.Trajectory`（`loop.go`）—— `executeTool` 里调 `recordTrajectory`（**含失败**，
  失败轨迹是 handoff「错误与修复」章节的唯一来源）
- `Loop.Todos` / `Loop.StreamedText` —— 读取器函数（TS 是进程单例 `getTodos`，
  Go 侧 todo 工具持有实例字段，故由装配方注入）
- `TrySessionSplit(messages, state *SplitState)` —— **签名变更**（加了可选状态参数）。
  既有测试已同步传 `nil`。`SplitState` 全字段可缺省，缺省时 handoff 退化。
- `loop.go` 的 `splitState()` 汇总状态 → 传入 `TrySessionSplit`

**降级入口保留**：`BuildSessionHandoff(messages, ratio)` 仍在（向后兼容），
内部委托到 `BuildSessionHandoffWithState(..., nil, nil, "")`。新调用方应用后者。

#### 本刀踩到的坑（三条）

1. **UTF-16 vs 字节截断**：TS 的 `String.slice(0,60)` 按 **UTF-16 code unit** 计数。
   中文「接下来很很很…」截 60 → TS 得 60 个字符，Go 若按字节切只得 20 个。
   `utf16Slice` 复刻该语义；oracle 的 `truncation_boundary_cjk` 用例锁定它。
   **变异 M1（改字节截断）→ 测试红**，有判别力。
2. **裸数字 id 的依赖提示词要求**：`referencesID` 对纯数字 id 必须要求前置提示词
   （「基于 1」算边，「还剩 1 个测试」不算）。**变异 M2（去掉该要求）→
   `bare_numeric_no_cue` 用例红**。
3. **oracle 形态**：首版只存 `output`，测试要在 Go 侧**手工重建输入**——重建一旦
   与生成脚本不一致就是**假绿**。改为 `{input, output}` 数据驱动，测试零硬编码输入。
4. **测试断言的坑**：`TestHandoffTrajectoryCapped` 首版用全局 `strings.Count` 数
   `toolX`，得 20 条（第 6 章「已完成工作」也含它）——**误判成实现 bug**，实际
   第 8 章恰好 12 条。修正为只数章节内。
5. **失败行漏了 summary 段**（**交付门禁的 YELLOW 提示抓到的**）：TS 的错误行是
   `- [Turn N] failed: <tool> <target>: <summary> (<errorClass>)`，首版只输出
   `... <target> (<errorClass>)`——**handoff 文本与 TS 不等价**。
   该漏项的来源正是 `resultSummary` 字段「无读取方」的警告——**提示是对的，
   不是噪音**。修法：补 `: <summary>` 段，回退文案 `${tool} in ${target} failed`
   （对账 `compaction-controller.ts:671`）。三条新测试锁定格式。

#### 已知差异（有意）

- `TrajectoryRecorder` 的 status **只有 success / failed**——TS 还有
  `retried-success` / `retried-failed`（瞬时失败重试后的结局）。Go 侧重试在
  client 层，尚未把「是否重试过」透传到 `executeTool`。移植后应补齐。
- `NewTrajectoryRecorder(0)` 用默认上限 200（TS 显式传 0 会得 maxEntries=0，
  记录一条即清空）——**有意差异**，避免 Go 调用方零值构造时静默失效。
- `ComputeMaxDepth` 用 **-1 表示环**（TS 用 `Infinity`；Go 无 int Infinity）。
- `truncateRunes` 按符文（非 UTF-16）——仅用于轨迹摘要字段（不进 handoff 固定文本）。

#### oracle

`go/testdata/taskstate/gen-oracle.ts` + `oracle.json`（34 用例，sha256 见生成输出）。
生成：`node_modules/.bin/tsx go/testdata/taskstate/gen-oracle.ts`

#### 仍未做

- **执行层** `replaceWithCheckpoint`（历史替换）——前置（task-state / trajectory
  / **artifact store**）已全部补齐，**下一刀可做**
- `Advisor.onTurnEnd`、`resolveCompactionEconomics` 装配层、LLM 重写路径


---

### artifact store（2026-09-20，回主线第五刀）
**目标**：补齐 session split 执行层的最后一块前置，同时消掉
`context_collapse.go` 里「Go 侧当前无 artifact 生产端」的已记录欠账。

#### 新包 `internal/artifact`

| 文件 | 对账 TS | 关键导出 |
|------|---------|----------|
| `types.go` | `src/artifact/types.ts` | `Artifact` / `ArtifactSection` / `ArtifactRef` / `FormatArtifactRef` |
| `store.go` | `src/artifact/store.ts` | `Store`（`Save`/`Get`/`ReadRaw`/`ReadLines`/`ReadLineRange`/`AddFallbackSession`/`ForSession`）、`CleanupOldSessions`、`CorruptionError` |
| `threshold.go` | `constants.ts` 的 `pruneThresholds` + `artifact-threshold.ts` | `PruneThresholdsFor` / `ToolArtifactThreshold` |

#### 生产端接线（三处）

1. **L1 拦截**（`internal/agent/artifact_intercept.go`）：`executeTool` 里调
   `interceptResultForArtifact`——超阈值的大结果落盘，历史只留
   `[artifact:ID] ... Use read_section(...)`。**在 recordTrajectory 之前**，
   保证轨迹看到最终形态。
2. **read_section 工具**（`internal/tools/readsection.go`）：按行/字符范围取回。
   已注册进 `default_registry.go`。
3. **CLI 装配**（`cmd/tianshu/main.go`）：`loop.Artifacts = NewStore(join(cwd,
   ".rivet", "artifacts"), sessionId)` + 启动时 `CleanupOldSessions`。
   对账 TS `loop.ts:846` 与 `bootstrap.ts:2131`。

**端到端已验证**：大结果 → 拦截落盘 → 取 id → read_section 按区段取回原文。
`artifact_e2e_test.go` 两条测试锁定（含「落盘必须是原文，不是摘要」——变异 M3
模拟 double-save 事故时两条都红）。

#### 关键约束（L0 vs L1，TS 记录的真实事故）

**`l0WrappedTools`（read_file / read_section / grep / bash）不得被 L1 重复包装**：
1. 会造成无限嵌套 `[artifact:新ID] → read_section(新ID) → ...`（tianshu v4 pro
   2026-05-25 事故复盘）
2. grep/bash 的 L0 标记在**尾部**，早期 L1 只查 `startsWith` 漏掉它 → 把已截断
   的字符串又存一遍（double-save）

**Go 侧现状**：工具尚无 L0 包装，故该集合当前不命中——保留是**契约完整性**。

#### 本刀踩到的坑（两条，都是我的测试构造错误）

1. **cleanup 测试用了固定的过去时间戳** → 所有目录都判为超 TTL，删了 51 个而非
   预期值。TTL 是相对 `time.Now()` 的，测试必须给相对当前的时间。
2. **cleanup 期望值算错**：52 个目录排除 active 后是 51 个候选，`51-cleaned > 50`
   只删 1 个——首版写 2 是漏了「active 被排除」这一步。

#### 已知未做（记入下一刀）

- **`summarize.go`**（`src/artifact/summarize.ts`，407 行）：按文件扩展名提取
  `sections`。**当前保存时 `sections` 恒为空**（对账 TS 的实际调用
  `tool-pipeline.ts:607` 也传 `[]`）——只影响 read_section 的「片段名」提示质量，
  不影响取回功能。
- **read_section 的 `file_path` 分支**：依赖 `getFileReadMtime`（陈旧性告警）与
  `computeModelReadCap`（按窗口/提供商算读上限），两者未移植。
  **当前 `readSectionMaxChars` 用 `ToolArtifactThreshold("read_file", ...)` 近似**
  ——移植 `computeModelReadCap` 后应替换。
- **`compact-history` 快速路径**（`read_section` 对归档的流式读取 + recall 标记）：
  依赖 `recall-marker.ts`。
- **budget 感知的阈值缩放**（TS `remainingBudgetFraction > 0.5` 时阈值 ×3）：
  只接了窗口 floor（TS 注释里的「L1 层」核心）。
- **`generateArtifactSummary` 是核心分支子集**：TS 按工具分派十余 case，Go 侧
  实现了 read_file / grep / bash / run_tests / glob + 通用兜底。

#### 悬空标注（诚实披露）

`FormatArtifactRef` / `ArtifactRef` **在 TS 与 Go 两侧都无生产调用方**
（grep 确认）——TS 的实际包装文案由 `tool-pipeline.ts` 内联拼接。Go 侧保持
忠实移植并在 `types.go` 注释里明示「不要把它当成引用格式的唯一来源」。


#### oracle 生成器的两处坑（都在生成阶段被 oracle 自己暴露）

1. **把非判定路径的数据混进 golden**：首版无条件记录 `session.getEstimatedTokens()`，
   但小窗口用例根本没走到那一步。修正为「仅在窗口门槛通过后记录」。
2. **记录时机错误**：`trySessionSplit` 成功后会把历史替换成 handoff，之后再读
   session 得到的是**压缩后**的状态（首版记到 3098 而非判定时的 499000）。
   修正为**判定前**取值。

**这两条合起来的教训**：oracle 记录的是「哪个时刻、哪条路径」的值——时刻与
路径错了，值再精确也是错的。

#### 变异反证

- **M1**（窗口检查挪到比例之后）→ 2 处红（提前返回时不该算 token），有判别力。
- **M2**（比例门槛 0.86→0.5）→ **首轮红 0 处，是测试缺口**：首版用例集只有
  ratio 0.5 与 0.95 两点，无法区分门槛 0.5 与 0.86。补 `ratio 0.70 / 0.85`
  两个用例后 → 3 处红。**这是「红 0 处 = 测试缺口」的又一实例**——用例集的
  取值点没覆盖被区分区间。



与 provider cache defaults，属尚未移植的 provider 模块。当前 `reclaimProfile()`
由 `Profile.Billing`/`Cache` + `ContextWindow` 直接派生（语义等价于 TS 的
缺省分支）。

---

### replaceWithCheckpoint（2026-09-20，回主线第六刀）——**验收面转 met**

**目标**：session split 执行层的最后一块。做完后自 `1b3a552` 起一直 blocked
的验收面「1M 窗口 + 86% 占用 → 会话历史真的被切分」**转 met**。

#### 两个新文件 + 一处替换

| 文件 | 对账 TS | 内容 |
|------|---------|------|
| `internal/agent/checkpoint.go` | `replaceWithCheckpoint`（`compaction-controller.ts:1082`） | `ReplaceWithCheckpoint` + `CheckpointParams`/`CheckpointDeps`/`CheckpointOutcome` |
| `internal/session/listener.go`（替换占位） | `compactOai`（`session-persist.ts:424`） | `OnReplace` 从「未实现占位」改为**真的全量原子重写** |
| `internal/session/persist.go` | 同上 | `rewriteTranscript`（tmp + rename 原子写） |

#### 核心语义（逐条对账）

1. **锚保留**：前 `CacheAnchorMessages`（2）条**逐字节不动**——前缀缓存的前提。
2. **尾随未消费 user 保护**：末尾是 user（模型未消费）时其原文保留在末尾、
   不进归档——否则用户刚发的指令被摘要替换（观感 = 消息被截断）。
   判据 `len-1 >= anchorCount`（含 index == anchorCount 的最小边界）。
3. **摘要角色**：有尾随原文时用 **assistant**——避免中间出现第二条 user
   （promptEngine 对非 trailer 的 user 会 volatileBlock 回退注入，双份膨胀）。
4. **reclaim gate**：不提交则**不碰历史**（`force=true` 时必提交——split 的
   替代方案是超窗 API 失败）。
5. **审计行保留**（`OnReplace` 侧）：重写只从内存消息重建文件，而审计行
   （compact_start / compact_end / model_switch）**从不进内存**——不保留就会
   在第一次重写时**静默销毁审计轨迹**（TS 注释记录的回归）。

#### 接线

`loop.go` 的 `maybeCompactAtBoundary`：split 判定触发 → `ReplaceWithCheckpoint`
（force=true）→ 提交则 `replaceHistory`（重写 `l.messages` + `Listener.OnReplace`）
并 **return**（历史已是新形态，不再走常规压缩）；被 gate 拒绝则记录原因后继续
常规压缩。新增 `Loop.CheckpointDeps` 字段注入三项增强。

#### 行为变更（三条既有测试随之反转）

`replaceWithCheckpoint` 落地让「执行层未移植」时期的降级断言**与事实相反**：

| 测试 | 旧断言 | 新断言 |
|------|--------|--------|
| `_SplitDoesNotReplaceHistory` | 历史长度不变 | **已删除**——被 `_SplitReplacesHistory` 取代（历史应变短） |
| `_SplitDoesNotBlockCompact` | split 后仍走常规压缩 | 改为验证**未触发 split** 时常规压缩照常 |
| `_SessionSplitHasProductionCaller` | 事件含「执行层未移植」 | 事件含「会话切分执行」+ 替换结果 |
| `TestSplitHandoffUsesRealState` | 经 `maybeCompactAtBoundary` 取 handoff | 直接调 `TrySessionSplit`（前者现在会替换历史） |

#### 依赖分层（未移植项显式留口）

`CheckpointDeps` 三个字段，nil 时跳过（行为与 TS 的缺省分支一致）：
- `ArchiveDiscarded` ← `archiveDiscardedHistory`（需 `serializeMessagesForArchive`）
- `TaskAnchor` ← `buildTaskAnchorAppendix`（需 `getActiveContract` + `renderTaskAnchor`）
- `Preflight` ← `runResumePreflightOai`（`src/context/resume-preflight.ts`，251 行）

#### 验证

- `checkpoint_test.go` 10 条 + `listener_replace_test.go` 6 条 + `sessionsplit_e2e_test.go` 4 条
- **变异反证**：M4（去掉尾随 user 保护）→ 2 条红；M5（不保留审计行）→ 1 条红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- 上面三个 `CheckpointDeps` 增强（archive / task-anchor / preflight）
- `promptEngine.resetAppendixBaseline`（Go 侧无 promptEngine 的 appendix 机制）
- `recordCompactEvent`（Go 侧 metadata 有 `CompactEvents` 字段，但未被填充）

---

### CheckpointDeps.Preflight（2026-09-20，回主线第七刀）

**目标**：移植 `runResumePreflightOai`——历史替换后可能出现**孤儿 tool_call**
（`tool_calls` 无对应 `tool_result`），供应商会以
"insufficient tool messages following tool_calls" 拒绝下一次请求。

#### 两个新文件

| 文件 | 对账 TS | 内容 |
|------|---------|------|
| `internal/context/writeevidence.go` | `src/context/write-evidence-probe.ts` | `WriteRecoveryMarker` / `FormatWriteRecoveryContent` / `ExtractTargetPath` / `CountPriorRecoveries` / `CreateWriteEvidenceProbe` |
| `internal/context/resumepreflight.go` | `src/context/resume-preflight.ts` | `RunResumePreflightOai` / `isToolAdjacencyCleanOai` / `ResumePreflightReport` |

接线：`cmd/tianshu/main.go` 的 `loop.CheckpointDeps.Preflight`（含
`CreateWriteEvidenceProbe(cwd)`）。

#### 与既有 `session.RepairOrphanToolCalls` 的区别（**关键**）

| | 策略 |
|---|---|
| `session.RepairOrphanToolCalls` | **剔除**孤儿（丢弃 tool_call / tool_result） |
| 本函数（`RunResumePreflightOai`） | **拉回 + 合成**：从历史任意位置拉回匹配结果；仅当根本不存在时才合成占位 |

后者更接近供应商的真实要求（**邻接**，不只是 id 存在）。TS 注释明确：
「id 存在性检查（`detectOrphanToolCallsOai`）必要但不充分」——一个结果可能
**存在**却位于中间的 user/assistant 之后（迟到的 addToolResults），有匹配 id
但邻接破坏。

#### 本刀发现的**真实 bug**（非本刀引入）

`session.NormalizeOaiMessage` 的条件是 `m.ToolCalls == nil`——**漏掉了从
JSON 读回的空数组**。实测（探针验证）：`json.Unmarshal` 对 `"tool_calls": []`
产出 **非 nil 空切片**（`nil=false len=0`），而那恰恰是该函数存在的理由
（「空数组可能残留在旧会话文件里」）。

**修法**：条件改为 `len(m.ToolCalls) == 0 && m.ToolCalls != nil`（nil 时提前
返回以保持「无改动返回原消息」契约）。同时 `sameMessages` 的 nil vs 空切片
差异必须算「不同」——否则 `Repaired` 漏报。

#### 验证

- `resumepreflight_test.go` 20 条（邻接判定 6 + 修复路径 6 + 文案分支 5 + 探测 3）
- `preflight_wiring_test.go` 3 条（接线：preflight 作用于**候选**而非原文）
- **变异反证**：M6（漏检缺失结果）→ 1 条红；M7（丢弃而非拉回）→ 2 条红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- `CheckpointDeps` 另两个增强：`ArchiveDiscarded`（需 `serializeMessagesForArchive`）、
  `TaskAnchor`（需 `getActiveContract` + `renderTaskAnchor`）
- `promptEngine.resetAppendixBaseline`、`recordCompactEvent`

---

### CheckpointDeps.ArchiveDiscarded（2026-09-20，回主线第八刀）

**目标**：`replaceWithCheckpoint` 丢掉一段历史时，把它序列化存为
`compact-history` artifact，并把「召回引用块」拼到摘要末尾——模型之后可
`read_section` 逐字取回被丢的历史（而非只依赖有损摘要）。

#### 三个新文件

| 文件 | 对账 TS | 内容 |
|------|---------|------|
| `internal/context/compactarchive.go` | `src/agent/compact-archive.ts` | `SerializeMessagesForArchive` / `RenderArchiveBody` / `BuildArchiveCatalog` / `BuildRecallRefBlock` |
| `internal/context/recallmarker.go` | `src/compact/recall-marker.ts` | `BuildRecallMarker` / `ParseRecallMarker` |
| `internal/context/archiveassembly.go` | `archiveDiscardedHistory`（`compaction-controller.ts:1009`） | `BuildArchiveDiscarded`（fail-soft 装配） |

接线：`cmd/tianshu/main.go` 的 `loop.CheckpointDeps.ArchiveDiscarded`。

#### 序列化契约（**必须稳定**——read_section 按行定位）

每条消息用固定 divider 头：

```
--- turn:N role:ROLE ---
<body line 1>
<body line 2>
```

**sections 按消息切分**（不是按轮）：单条 assistant 可能携带 content +
reasoning + 多个 tool_calls 跨几十行，单条 tool 结果可能几万字符——轮→行
映射太粗。逐消息 divider 保证字节稳定边界；catalog 再聚合成 turn→行目录。

**turn 计数规则**：从 0 起，**每条 user 递增一次**（首条 user 让 `seenUser`
变 true 但不递增）。保证 assistant/tool 归属到其所属的 user 轮。

#### recall-eviction（为什么 tool 分支要折叠）

被召回的 compact-history 块会**再次**进入历史。若原样重新归档，内容会在
artifact 之间重复累积（**抵消压缩**）。故折叠为一行指针
`[recalled → <id> <section> (see original artifact)]`——原 artifact 仍持有
字节，指针保持可召回性而不复制内容。

#### fail-soft 四条早退（对账 TS 注释）

「Returns null when archiving is unavailable, the zone is empty, or the write
fails — **compaction must never be blocked by archival**」：

1. sink 为 nil（artifact store 未装配）
2. 丢弃段为空
3. 序列化后正文 trim 后为空
4. 落盘失败 / id 为空

Go 侧对应「返回空串」——调用方对空串就是「不追加」（与 TS 的
`archive ? ... : ...` 分支同形）。

#### 验证

- `compactarchive_test.go` 20 条（序列化 8 + catalog/ref 3 + 装配 6 + recall marker 3）
- `archive_wiring_test.go` 4 条（**端到端**：落盘 → 引用进摘要 → read_section 可召回）
- **变异反证**：M8（不递增 turn）→ 2 条红；M9（不做 recall-eviction）→ 1 条红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- `CheckpointDeps.TaskAnchor`（需 `getActiveContract` + `renderTaskAnchor`）
- `read_section` 的 **compact-history 快速路径**（流式读取 + recall 标记前置）
  ——归档已可写，但 read_section 尚未对 `compact-history` 走流式分支
  （当前走普通 `ReadRaw`，受 2MB 上限约束）
- `promptEngine.resetAppendixBaseline`、`recordCompactEvent`

---

### read_section 的 compact-history 流式分支（2026-09-20，回主线第九刀）

**目标**：消掉上一刀识别的缺口——归档已能写入大内容，但召回会撞 2MB 上限
（「存得下、取不回」）。

#### 改动（`internal/tools/readsection.go`）

在 **2MB 守卫之前**插入 compact-history 快速路径（对账 TS `read-section.ts`
的 "Compact-history recall fast path"）：

- **只对行范围生效**：字符范围需全文（无法流式定位），落回通用路径
- **不过 2MB 闸门**：走 `artifact.Store.ReadLineRange`（流式，不载入内存）
- **前置召回标记** `[recalled <id> <section>]`：让下一次压缩能把这块折叠回
  指针（recall-eviction，见 `context.RenderArchiveBody` 的 tool 分支）
- 起点越界 → 报总行数（**非错误**，对账 TS 的 `isError: false`）
- 超 `MaxRangeLines`(5000) → 附分页提示
- 超字符上限 → 截断

**顺序是关键**：该分支必须在 2MB 守卫**之前**——那正是它存在的理由
（长线程归档常超上限，会让归档自己的目录项无法召回）。

#### 依赖方向

`tools → context`（新引入）。**无环**（`context` 不依赖 `tools`）。
`tools` 包已用标准库 `context`，故 `internal/context` 以 `ctxstore` 别名导入。

#### 验证

- `readsection_compacthistory_test.go` 7 条，其中
  `TestReadSectionCompactHistoryStreamsBeyond2MB` 是核心（3.2MB 归档成功召回）
- **变异反证**：M10（流式分支失效）→ 测试红；M11（不加召回标记）→ 测试红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- `CheckpointDeps.TaskAnchor`（需 `getActiveContract` + `renderTaskAnchor`）
- `promptEngine.resetAppendixBaseline`、`recordCompactEvent`

---

### read_section 的 file_path 分支（2026-09-20，回主线第十刀）

**目标**：补 `read_section(file_path=...)`——从磁盘活动文件按区段读取，配合
read-ref 引用恢复本会话早前读过的文件内容。同时消掉一处**已知偏差**。

#### 三个新文件

| 文件 | 对账 TS | 内容 |
|------|---------|------|
| `internal/tools/modelreadcap.go` | `src/tools/model-read-cap.ts` | `ComputeModelReadCap`（窗口感知 + 策略系数 + 120K 硬上限） |
| `internal/tools/filestate.go` | `read-file.ts` 的 `lastKnownFileState` | `NoteFileObserved` / `GetFileReadMtime`（会话隔离 + 500 条上限） |
| `readsection.go`（改） | `read-section.ts` 的 "B3" 分支 | `readFromDisk` |

#### 分支语义（对账 TS，顺序敏感）

1. **路径校验**（fail-closed，`pathsafe.Validate`）
2. **陈旧性检查**：mtime 与上次观察不符 → 前置告警
   （`⚠ 文件自上次 read_file 后已变更（mtime 不匹配）…`）
3. **大文件守卫**：>2MB → 报错并建议 grep/head
4. 读取 + `extractSection`
5. 按 `ComputeModelReadCap` 截断

**优先级**：`file_path && !artifactId` —— 两者同时提供时走 artifactId 分支
（防 file_path 静默劫持 artifact 召回）。

#### 消掉的已知偏差（**重要**）

`readSectionMaxChars` 首版用 `artifact.ToolArtifactThreshold("read_file", ...)`
**近似** `computeModelReadCap`，注释里标注「移植后应替换」。实测偏差：

| 窗口 | 近似值（旧） | 真值（新） | 偏差 |
|------|-------------|-----------|------|
| 1M | 300000 | **120000** | 高 2.5 倍 |

真值算法：`0.05 × 1M × 4(chars/token) × 1.0(balanced)` = 200000 → 封顶
`ABSOLUTE_MAX_CHARS(120000)`。

#### 接口变更（消费方同步）

`ReadSection()` → `ReadSection(cwd, grants)`（对齐 `Grep(cwd)` /
`ReadFile(cwd, grants)`）。**消费方核对抓到一个漏网调用点**
（`internal/agent/artifact_e2e_test.go:54`）——`go vet ./...` 报出来的。

#### 验证

- `readsection_filepath_test.go` 14 条（分支 8 + 文件状态 3 + readCap 3）
- **变异反证**：M12（去掉陈旧性告警）→ 测试红；M13（file_path 劫持）→ 2 条红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- `CheckpointDeps.TaskAnchor`（经核实**不是「两个小模块」**——见下）
- `summarize.go`（artifact sections 提取，407 行）
- `resolveCompactionEconomics`、`Advisor.onTurnEnd`、LLM 重写路径

#### ⚠️ 对 `TaskAnchor` 称量的修正（2026-09-20 本刀核实）

此前 HANDOFF 把 `TaskAnchor` 排为「当前最高优先级，需 `getActiveContract` +
`renderTaskAnchor`（两个小模块）」。**该称量有误**，核实结果：

- 数据源 `TaskContract` 在 Go 侧**完全不存在**（`grep TaskContract` 零命中）
- `src/context/task-contract.ts` **566 行 / 12 个导出函数**（`classifyTurnMode`
  / `extractTaskContract` / `classifyTaskDepth` / `classifyPlanMethodology` 等，
  互相调用）
- 生产者链在 `turn-step-producer.ts:282`（Go 侧无 turn-step 等价物）
- **结论**：接上去只能恒返回空串——是**空壳**。应先把契约的**生产 + 演进**
  整条链移植进 Go，再谈锚。



**目标**：消掉上一刀识别的缺口——归档已能写入大内容，但召回会撞 2MB 上限
（「存得下、取不回」）。

#### 改动（`internal/tools/readsection.go`）

在 **2MB 守卫之前**插入 compact-history 快速路径（对账 TS `read-section.ts`
的 "Compact-history recall fast path"）：

- **只对行范围生效**：字符范围需全文（无法流式定位），落回通用路径
- **不过 2MB 闸门**：走 `artifact.Store.ReadLineRange`（流式，不载入内存）
- **前置召回标记** `[recalled <id> <section>]`：让下一次压缩能把这块折叠回
  指针（recall-eviction，见 `context.RenderArchiveBody` 的 tool 分支）
- 起点越界 → 报总行数（**非错误**，对账 TS 的 `isError: false`）
- 超 `MaxRangeLines`(5000) → 附分页提示
- 超字符上限 → 截断

**顺序是关键**：该分支必须在 2MB 守卫**之前**——那正是它存在的理由
（长线程归档常超上限，会让归档自己的目录项无法召回）。

#### 依赖方向

`tools → context`（新引入）。**无环**（`context` 不依赖 `tools`）。
`tools` 包已用标准库 `context`，故 `internal/context` 以 `ctxstore` 别名导入。

#### 验证

- `readsection_compacthistory_test.go` 7 条，其中
  `TestReadSectionCompactHistoryStreamsBeyond2MB` 是核心（3.2MB 归档成功召回）
- **变异反证**：M10（流式分支失效）→ 测试红；M11（不加召回标记）→ 测试红
- 全量 go test 连跑 3 次，22 包 0 FAIL

#### 仍未做

- `CheckpointDeps.TaskAnchor`（需 `getActiveContract` + `renderTaskAnchor`）
- `promptEngine.resetAppendixBaseline`、`recordCompactEvent`

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
- [x] ~~**未移植（Windows 特有，需真实环境验证）**：`resolveShellCommand`~~
  **已完成（2026-09-20）**——新建 `internal/platform`，见下方「Windows 可移植性」节。
  核实后发现问题比记录的大：它不只是提示词（`DetectShellKind` 返回空串），
  TS 的 `bash.ts:537` 用 `getShellCommand()` **实际 spawn shell**，而 Go 硬编码
  `exec.Command("bash", "-c", ...)`——在没装 Git Bash 的 Windows 上直接启动失败。
  已按两层移植（探测 + 接线），并顺带修掉 `DetectHostEnv` 的 Windows 字节不等价。
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
- [x] **toolDefs 缓存**（`internal/agent/loop.go`）
  - **问题**：`toolDefs()` 原本**每轮重建**全部工具的 schema 并重新序列化
    ——N 个工具 × M 轮的无谓开销
  - **为什么可以缓存**：工具集在 Loop 生命周期内不变（Registry 有
    Register/Remove，但 loop 不调用）
  - **契约**：缓存值**只读**——调用方（client.Stream）不得修改切片或其中的
    OrderedMap；`toolDefs()` 每次返回同一底层数组
  - **正确性前提**（有测试锁定）：缓存命中与重新构造必须产出**完全相同的
    序列化字节**——否则前缀缓存会失效。`TestToolDefsCachedBytesIdentical`
    用一个 Loop 走缓存、另一个强制重建，逐工具比对 `Marshal()`
  - 变异反证 2 个：**全部有判别力**（缓存不生效 1 红 / 缓存返回空 1 红）
- [x] **trust 子系统**（新建 `internal/trust/`）——项目级信任门
  - 对账 `src/config/project-trust.ts`。**SECURITY.md 的信任边界**：仓库内容
    （含项目内 `.rivet/hooks.json` 与 `.rivet-config.json`）**不能单独构成执行
    动作的授权**——未授信前项目 hooks 不执行、安全敏感键被剥离（fail-closed）
  - **安全要点（逐条有测试 + 变异反证）**：
    - 信任文件写 `<rivetHome>/project-trust.json`，**绝不写进仓库目录**
      （否则克隆一个仓库就等于授信它）——`TestTrustStoreNeverInRepo`
    - 按 **realpath** 键控——不归一化就存在「授信 A 路径、B 路径绕过」的
      符号链接漏洞——`TestRealpathKeying`
    - 权限 **0o600**（用户私有）
    - **坏信任文件按未授信处理**（fail-closed）——`TestCorruptStoreIsUntrusted`
    - **`dismiss` ≠ 授信**（关闭提示不改变剥离行为）——`TestDismissPromptDoesNotTrust`
    - `RIVET_TRUST_PROJECT` env 覆盖**优先于**信任文件（`1` 授信 / `0` 强制
      未授信 / 其他值回落文件）
  - **剥离清单**（12 个顶层键 + 5 个嵌套键）：每个都能旁路 SECURITY.md 声明的
    审批/边界/出口控制——写盘授权、bash 预授权、静默 YOLO、MCP 拉进程、
    baseUrl+key 重定向、statusline 命令执行、**MCP 子进程出口改向**、
    **web_fetch 正文抽取改向**等
  - **`agent.permissions` 是 schema 的真实位置**（顶层 `permissions` 实际不
    存在，保留在集合里仅作纵深）
  - **对账时发现并修掉一个顺序缺陷**：`findSensitiveProjectKeys` 的输出顺序
    是 TS 的**集合声明序**（`permissions, mcp, hooks, ...`），我首版用了
    `sort.Strings`——oracle 立刻暴露。改用显式 `untrustedTopLevelKeyOrder`
  - oracle：`go/testdata/trust/`（7 剥离 + 7 敏感键 + 6 赌注），三次 sha256 一致
  - 变异反证 7 个：**全部有判别力**（fail-open 1 红 / env 覆盖失效 1 红 /
    不做 realpath 1 红 / dismiss 也授信 1 红 / 不剥离顶层键 4 红 /
    不剥离 agent.permissions 6 红 / 权限 0o644 1 红）
- [x] **filediff 子系统**（新建 `internal/filediff/`）——unified diff 生成
  - 对账 `edit-diff.ts`（185 行）+ cpu-tasks 的 `diffUnifiedRaw` /
    `diffStructuredRaw`（底层 jsdiff）
  - **自写 Myers 差分算法**（Go 标准库无 diff；`git diff --no-index` 需临时
    文件且输出受 git 版本影响，不可控）
  - **用途边界**：结果只给 **uiContent** 通道（TUI/桌面工具卡片），
    **绝不进模型面向的 content**——不进对话历史、无前缀缓存成本
  - **架构差异**：TS 把 diff 放进 **worker 线程**（jsdiff 的 Myers 是同步
    CPU，8K 行约 7 秒、50K 行可能几分钟，会冻住事件循环——2026-07-08
    「write 卡住 → 丢工具返回」事故根因）。Go 侧每工具调用在独立 goroutine，
    **不需要 worker**，但保留超时保护（超时返回空串，展示用丢失只降级卡片）
  - **对账过程中探针实测出 5 个格式细节**（照 GNU diff 实现会全错）：
    1. **hunk 头总是 `start,count`**（`@@ -1,1 +1,1 @@`）——**不省略 `,1`**
    2. **空侧渲染为 `0,0`**（全新文件 `@@ -0,0 +1,2 @@`）
    3. **尾换行被剥离**（`"a
b
"` → `["a","b"]`），存在性另行记录
    4. **`\ No newline at end of file` 仅在双方都非空时输出**（全新/清空文件不加）
    5. **纯删除/纯插入的空侧行号取非空侧起始行号**（删中间行 → `new(2,0)`
       而非 `new(1,0)`）——这条直接决定 `ComputeChangedLineRanges` 的区间位置
  - oracle：`go/testdata/filediff/`（18 用例，**diff 字节 + 行范围双对账**），
    三次 sha256 一致
  - 变异反证 6 个：5 个有判别力（不剥离尾换行 23 红 / 省略 `,1` 4 红 /
    纯删除语义错 3 红 / 空侧不渲染 `0,0` 4 红 / 路径不归一化 3 红）；
    **M4 经查证为等价变异**——`hasHunkHeader` 是防御性检查（`renderUnified`
    只要有 hunk 必输出 `@@`），对应当前不可达路径
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
- `internal/trust/`：**项目级信任门**——SECURITY.md 信任边界；
  未授信时剥离敏感配置键（fail-closed）
- `internal/filediff/`：**unified diff 生成**——自写 Myers；展示用（uiContent）
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

`go test ./...`（**19 包全绿、0 FAIL、1,918 个用例 PASS**）、`-race`（0 FAIL）、
`go vet`（OK）、`gofmt`（零违规）。分支 `go-runtime` 的 `go/` 相关提交
**103 个**（截至 `c843a14`——本文档的提交会让该数继续增长，这是正常的），
**已 push 到 origin**（本段此前记录的「11 包 / 49 提交 / 未 push」已过时）。

**验证纪律（本阶段固化）**：

- **每刀三级验证**：包内测试 → 全量 `go test ./...`（连跑 3 次防间歇性失败）→
  `vet` + `gofmt`
- **变异反证是必做项**：写完测试后故意改错实现，确认变红。累计约 **130+ 个变异**
- **「红 0」四种成因**必须逐一排除：等价变异、用例不可区分、**编译失败**、
  **被测逻辑在上游已实现**（本地检查是死分支）
- **`t.Skipf` 是隐身衣**——用 `Skipf` 兜底的用例会让变异「红 0」被掩盖，
  必须用 `Fatalf`

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
   - ✅ **dry_run 的 diff 预览**（buildFileDiff）——已完成
   - **指针回灌守卫**（pointer-guard）：依赖 4 个未移植的 arg-processor
     常量模块（write_file / edit_file / hash_edit / apply_patch）。风险：
     模型可能把历史里的指针文本当 `new_string` 传回来并被写进文件。
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
7. **frozen 块位置——架构边界，非「未完成」**（**经调研后的判断变更**）：
   - **原记录**：「TS 是 trailer-merge 到 user message，Go 侧拼在 system
     prompt 后，后续应替换」——措辞把它当成待办。
   - **调研后的实情**：`engine.ts` 有 **1700+ 行**，frozen 体系是它的核心
     （`frozenUserMerged` / `frozenFetchIndex` / `frozenPendingMerged` /
     eviction clamp / 重复消息各占独立快照 / `getNextFrozen` 的索引推进）。
     移植它**不是照抄一个函数**，而是要重构 Go 的 prompt 组装架构
     （TS：messages 数组 + trailer-merge；Go：单 `SystemPrompt` 字段拼接）。
   - **当前 Go 架构的缓存特性**：system prompt 拼接同样**稳定可缓存**
     （缓存的是 system+tools 前缀）。差别在于 TS 把 volatile 内容移到
     user message 尾部，使 **system prompt 完全冻结**、历史消息尾部也可
     增量缓存；Go 的 volatile 在 system prompt 内，volatile 变化会打断
     整个前缀。
   - **判断**：这是**方向性架构改动**，收益（缓存命中率提升）需要实测数据
     支撑，成本（重构 prompt 组装 + 回归全部 prompt oracle）很高。
     **不硬推**——记录判断，等有缓存命中率实测数据再决策。
   - 相关：`full.go` 的注释应更新（当前措辞暗示「待替换」）。
8. **「三处最小可用路径」——经核实全部过时或有误**（**记录更正**）：
   - `BuildFullSystemPrompt` 的**拼法**：已在 #7 重新定性为**架构选择**
     （非「待替换」）
   - `RenderProjectInstructionsBlock` 的 **`<context>` 包裹**：**记录有误**。
     该函数渲染 `<project-instructions>` 块，**本就不该**有 `<context>` 包裹
     ——`<context>` 是 **frozen 块的外壳**，由 `BuildStableVolatileBlock`
     提供（`return "<context>\n" + ... + "\n</context>"`），已有测试
     `full_test.go` 锁定
   - `BuildSystemPromptWithProject`：**无生产消费方**（生产走
     `BuildFullSystemPrompt`）。**决定保留**并标 `Deprecated`——理由：
     `project_test.go` 的 5 个测试通过它覆盖「加载 → 按节选取 → 渲染 → 截断」
     的**集成路径**（`projinst_test.go` 只覆盖各环节单元行为），删函数会连带
     丢失这层保障；保留一个无消费方的导出函数成本极低。
   - **同时补了生产路径的端到端测试**：`BuildFullSystemPrompt` 是否真的把
     cwd 下的项目指令渲染进 `<context>`（链路：
     `LoadProjectInstructions` → `vctx.RivetMd` → volatile 层
     `RenderProjectInstructionsBlock`）。这条链路易断——某环漏了会让项目指令
     **静默消失**（模型看不到 AGENTS.md）。变异反证：把 `RivetMd` 置空 → 红
9. **未移植的行为差异**（部分已修复）：
   - ✅ **信任门**（`isProjectTrusted` + trust store）——已完成（见顶部条目）
   - **Windows 的 `resolveShellCommand`**：需真实 Windows 环境验证，未移植。
10. **分支策略**：`go-runtime` 已 push 到 `origin`（2026-09-19）；
   `main` 仍在 `69b0381` 未动（用户明确要求不合并）。Go 实现**将来要独立
   仓库**——当前 `go/` 与 TS 源码同仓库是过渡状态。拆分可行性已核实：
   52 个提交无交叉改动（无一个同时改 `go/` 与 `src/`）、所有 `.go` 都在
   `go/` 下、`go/` 有独立 `go.mod`。**拆分时的约束**：`go/testdata/*/gen-oracle.ts`
   用相对路径 import 父仓库 TS 源码（生成 golden 用），拆出后无法重新生成
   golden——建议生成器留在 TS 仓库（它们是「对账工具」，本就该跟被对账对象
   在一起），Go 仓库只保留 `oracle.json`。

## 本阶段进展（第二十至二十八刀，2026-09-19）

> 上文「本轮完成情况」记录的是更早几轮的状态。本段是最新交接点。

### 已完成

**认知层（CVM + advisory 链路）**：
- advisory bus 核心 + 接进 loop（消除悬空）
- 习惯化对抗（streak 升级措辞 / 静音 4 轮 / constitutional 豁免 / probation 放行）
- holdout 反事实抽样（率 0.1 / 资格门 3 / 白名单）
- 跨会话效能信息素（JSONL + 14 天 EWMA + 原子写 + O_EXCL 锁）
- T7 效力排序 + efficacy 负反馈环（冷却翻倍 / 静默 / 正向臂）
- AdvisoryReadback 接线（四调用点 + CLI 装配）

**压缩链路（`internal/compact`，本阶段主战场）**：

| 刀 | 提交 | 内容 |
|---|---|---|
| 二十四 | `1300067` | 策略层（三策略四阈值）+ 自适应 + 精度天花板 + PressureMonitor |
| 二十五 | `38cd386` | `DecideCompactAction`（六种 action）+ 熔断器 + 阈值计算 |
| 二十六 | `43169f8` | `MicroCompactOai`（截断 + 轮次删除）+ token 估算 + 轮龄 |
| 二十七 | `15a34bb` | 语义折叠（7 个折叠器）+ 折叠接线 |
| 二十八 | `c843a14` | **压缩接线**——判定层 → 执行层，消除悬空 |

**压缩链路的完整事实流**（后续会话的锚点）：

```
Loop.Run turn 边界 (loop.go:504)
  → maybeCompactAtBoundary(turn)              [loop.go:779]
    → orderedMapsToOai(messages)              [wire → session.OaiMessage]
      → CompactBoundary.MaybeCompact          [compact_boundary.go]
        → context.EstimateOaiMessageTokens    [rounds 口径]
        → compact.DecideCompactAction         [六种 action]
        → [none 短路] → compact.MicroCompactOai
          → CollapseToolResult                [turnAge≥4 时语义折叠]
          → 截断 / 轮次删除
      → oaiToOrderedMaps(compacted)           [KeyOrder 重建，保插入序]
    → emit(compaction 事件)
```

**三处关键设计决策**（避免后续会话重复踩坑）：

1. **压缩只在 turn 边界，不在 mid-turn**——mid-turn 改历史会让已发出的请求
   前缀失效，缓存命中率归零（对账 TS 的 `loopTurn === 0` 约束）。
2. **`CompactActionInput` 有两个不联动的字段**：`ProviderProfile`
   （`*CompactRatioProfile`）决定**策略阈值**，`Profile`（`CompactionProfile`）
   决定 **LLM 阶梯**。只设后者会让阈值停在 balanced（Watch=0.6）而非按
   cacheType 推导的 aggressive（0.5）。
3. **熔断器检查只在决策层做**——`DecideCompactAction` 内部已返回 `none` +
   reason，`CompactBoundary` 里再查是死分支（变异红 0 证明）。

### 未完成（后续会话的起点）

**压缩执行层剩余四块**（按建议优先级）：

1. ~~**reclaim gate**——`buildReclaimDecision` + `estimateReclaim`~~ **已完成
   （2026-09-20）**，见下方「reclaim gate」节。
2. ~~**缓存顾问延迟**——`cacheAdvisor.shouldDelayCompact(tier, {...})`~~ **已完成
   （2026-09-20）**，见下方「缓存顾问延迟」节。
3. **LLM 重写路径**——`partial-llm` / `full-llm` / `checkpoint`，需 `summaryClient`
   抽象（要真调模型做摘要）。这是四块里最大的。
4. ~~**session split**——86% 时主动切分会话（`preUserMessageSplit`）~~ **判定层已完成
   （2026-09-20）**；执行层依赖尚未移植的 task-state/trajectory/artifact，见下方
   「session split」节。

**压缩链路的新风险面（建议排在 gate 之后）**：
- **压缩产出的消息列表是否满足 API 格式约束**——目前无测试覆盖。特别是
  tool_calls 与 tool 结果必须配对完整（轮次删除若切在中间会产生孤儿）。
  `session.RepairOrphanToolCalls` 已存在但未接进压缩路径。

**其余未移植**（见下文「架构欠账」与「未完成」段，多数仍有效）：
- 未移植目录：`internal/cache` / `internal/config` / `internal/tui` /
  `internal/mcp` / `internal/lsp` / `internal/auth` / `internal/skills`
- 未移植工具：`plan` / `job` / `ast_grep` / `diff` / `git` / `web_fetch` /
  `web_search` / `repo_map` / `read_section` / `request_path_access` /
  `ask_image` / `skill`
- `internal/context` 剩余：CognitiveLedger / Stigmergy / task-contract
- session 剩余：会话恢复、会话注册表

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
