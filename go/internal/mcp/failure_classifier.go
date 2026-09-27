package mcp

import (
	"regexp"
)

// failure_classifier.go —— MCP 错误分类。
//
// 对账 `src/mcp/failure-classifier.ts`（120 行）。
//
// # 为什么需要分类器（TS 注释的原始理由）
//
// MCP 的错误消息对用户**几乎没有行动价值**——同一个 `-32000: Connection closed`
// 背后可能是「PATH 缺系统目录」「包名打错」「npm 缓存损坏」三种完全不同的根因，
// 而三种的**下一步动作完全不同**。分类器的职责是把它们分开并给出可执行建议。
//
// # 两条纪律（照搬 TS）
//
//  1. **stdio 的 `-32000 Connection closed` 不是网络瞬断**——是子进程启动后
//     立刻退出。盲目重试无意义。故必须走 `classifyStdioExit` 用 stderr 细分。
//  2. **认不出来就说认不出来**——TS 注释逐字：「**不编根因**：谎报比不报更坏，
//     用户会照着错的方向修半天」。

// ErrorClass 是错误分类。
//
// 对账 `failure-classifier.ts` 的 `McpErrorClass`（8 类）。
type ErrorClass string

const (
	ClassConfig   ErrorClass = "config"
	ClassAuth     ErrorClass = "auth"
	ClassNetwork  ErrorClass = "network"
	ClassProtocol ErrorClass = "protocol"
	// ClassProcess 是 stdio 子进程启动后立即退出，且 stderr 没留下可辨特征。
	ClassProcess ErrorClass = "process"
	// ClassProcessEnv 同为「立即退出」，但 stderr 指向**环境**问题：
	// PATH 不完整，npx 连 cmd.exe 都 spawn 不起来（TS issue #149）。
	ClassProcessEnv ErrorClass = "process_env"
	// ClassProcessInstall 同为「立即退出」，但 stderr 指向**包获取失败**：
	// 包名不存在，或 npm 缓存/解包损坏（TS issue #77）。
	ClassProcessInstall ErrorClass = "process_install"
	ClassToolError      ErrorClass = "tool_error"
)

// ErrorClasses 是全部 class 的**单一真相源**。
//
// 对账 `failure-classifier.ts:24-27` 的 `MCP_ERROR_CLASSES`。
// TS 注释说明其存在理由：那份判据原先是一串字面量写在 UI 组件里，
// **新增 class 时漏改不会报错**，只会静默退化成显示英文建议（本地化悄悄失效）。
//
// Go 侧同理：消费方据此清单判断「有没有配套文案」，故必须是可枚举的显式列表。
var ErrorClasses = []ErrorClass{
	ClassConfig, ClassAuth, ClassNetwork, ClassProtocol,
	ClassProcess, ClassProcessEnv, ClassProcessInstall, ClassToolError,
}

// ErrorContext 是分类的补充上下文。
//
// 对账 `failure-classifier.ts` 的 `McpErrorContext`。
type ErrorContext struct {
	// Transport：ErrorTransportStdio = 本地子进程；ErrorTransportRemote = url 型。
	//
	// **注意类型**：是 `ErrorTransport`（二分），不是 `TransportType`（三值）。
	// 对账 `failure-classifier.ts:33` 的 `'stdio' | 'remote'`。
	Transport ErrorTransport
	// Stderr 是子进程 stderr 尾部（仅 stdio 有）。
	//
	// TS 注释逐字：分类器**必须**拿到它才算完整证据——PATH 缺失 / 包不存在 /
	// 缓存损坏三种根因在 `err.message` 上是**同一句** `-32000: Connection closed`，
	// 唯一的区别就在 stderr 里。
	Stderr string
}

// ClassifiedError 是分类结果。
//
// 对账 `failure-classifier.ts` 的 `ClassifiedMcpError`。
type ClassifiedError struct {
	Class      ErrorClass
	Retryable  bool
	Suggestion string
}

var (
	// 对账 `failure-classifier.ts` 的 `classifyStdioExit` 三条正则。
	stdioEnvRe     = regexp.MustCompile(`(?i)spawn cmd ENOENT|is not recognized as an internal or external command`)
	stdioInstallRe = regexp.MustCompile(`(?i)E404|404 Not Found|is not in this registry`)
	stdioCorruptRe = regexp.MustCompile(`(?i)ENOTEMPTY|integrity checksum failed|tarball.*corrupt`)

	// 对账 `classifyMcpError` 的各条规则。
	configRe    = regexp.MustCompile(`(?i)enoent|invalid json|bad command|spawn.*enoent|cannot find module.*config`)
	stdioExitRe = regexp.MustCompile(`(?i)connection closed|-32000`)
	authRe      = regexp.MustCompile(`(?i)401|403|permission denied|unauthorized|forbidden|scope|oauth|api key`)
	networkRe   = regexp.MustCompile(`(?i)econnrefused|etimedout|timed out|socket hang up|econnreset|fetch failed|transport.*close|disconnected|connection closed|-32000`)
	protocolRe  = regexp.MustCompile(`(?i)invalidparams|invalid params|capability mismatch|malformed|parse error|json-rpc`)
)

// classifyStdioExit 细分 stdio 子进程「启动后立即退出」。
//
// 对账 `failure-classifier.ts:52-79`。三种根因对用户是同一句报错，
// 但下一步动作完全不同（TS 注释逐字）：
//
//   - npx 要 spawn cmd.exe，PATH 里没有系统目录 → **环境/打包问题**，
//     用户改配置也修不好，应报 bug；
//   - 包名打错或拉不下来 → 改 args 里的包名 / 换 registry / 清缓存；
//   - 认不出来 → **老实说认不出来**。
func classifyStdioExit(stderr string) ClassifiedError {
	if stdioEnvRe.MatchString(stderr) {
		return ClassifiedError{
			Class:     ClassProcessEnv,
			Retryable: false,
			Suggestion: "The server failed to start because its child process could not find a system " +
				"executable (typically `cmd.exe` — PATH lacks the system directories, so npx cannot run). " +
				"This is an environment/packaging problem, not a config mistake: retrying will not help.",
		}
	}
	if stdioInstallRe.MatchString(stderr) {
		return ClassifiedError{
			Class:     ClassProcessInstall,
			Retryable: false,
			Suggestion: "The package could not be fetched — check the package name in `args` and that it " +
				"exists on the configured registry.",
		}
	}
	if stdioCorruptRe.MatchString(stderr) {
		return ClassifiedError{
			Class:     ClassProcessInstall,
			Retryable: false,
			Suggestion: "The package could not be unpacked — the npm cache looks damaged. Clear it " +
				"(`npm cache clean --force`) and retry.",
		}
	}
	return ClassifiedError{
		Class:      ClassProcess,
		Retryable:  false,
		Suggestion: "MCP server process exited right after start — check the command, its stderr log, and network/proxy settings.",
	}
}

// ClassifyMcpError 分类一个 MCP 错误。
//
// 对账 `failure-classifier.ts:81-120` 的 `classifyMcpError`。
//
// **分支顺序敏感**（照搬 TS）：config → stdio 退出细分 → auth → network
// → protocol → 兜底 tool_error。
//
// 注意 stdio 细分在 auth 之前、network 之前——因为 `-32000 Connection closed`
// 同时命中 network 的正则，若顺序颠倒就会被误判成可重试的网络错误（TS 注释
// 明写「盲目重试无意义」）。
func ClassifyMcpError(err error, ctx ErrorContext) ClassifiedError {
	msg := ""
	if err != nil {
		msg = err.Error()
	}

	// ① config
	if configRe.MatchString(msg) {
		return ClassifiedError{
			Class:      ClassConfig,
			Retryable:  false,
			Suggestion: "Check MCP server config: command path, args, and environment.",
		}
	}

	// ② stdio 子进程退出的细分（**必须在 network 之前**——否则被误判为可重试）
	if ctx.Transport == ErrorTransportStdio && stdioExitRe.MatchString(msg) {
		return classifyStdioExit(ctx.Stderr)
	}

	// ③ auth
	if authRe.MatchString(msg) {
		return ClassifiedError{
			Class:      ClassAuth,
			Retryable:  false,
			Suggestion: "Check API key or OAuth configuration for this MCP server.",
		}
	}

	// ④ network（可重试）
	if networkRe.MatchString(msg) {
		return ClassifiedError{
			Class:      ClassNetwork,
			Retryable:  true,
			Suggestion: "Transient network error. Retry may succeed.",
		}
	}

	// ⑤ protocol
	if protocolRe.MatchString(msg) {
		return ClassifiedError{
			Class:      ClassProtocol,
			Retryable:  false,
			Suggestion: "Check tool input schema against MCP server definition.",
		}
	}

	// ⑥ 兜底
	return ClassifiedError{
		Class:      ClassToolError,
		Retryable:  false,
		Suggestion: "Read the error output for details.",
	}
}
