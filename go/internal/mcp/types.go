package mcp

// types.go —— MCP 的类型定义（对账 `src/mcp/types.ts` 与 `config.ts` 的枚举）。

// TransportType 是传输类型。
//
// 对账 `src/mcp/types.ts:1` 的 `McpTransportType`。
//
// **注意上游有一处重复定义**：TS 侧 `types.ts:1` 与 `transport-factory.ts:20`
// 各定义了一份**完全相同的**联合类型，而 `manager.ts` / `presets.ts` 从
// `types.ts` 取、`transport-factory` 用自己的那份。Go 侧**只保留一份**
// （单一真相源）——这是有意的收窄，避免同一枚举两处漂移。
type TransportType string

const (
	TransportStdio          TransportType = "stdio"
	TransportStreamableHTTP TransportType = "streamableHttp"
	TransportSSELegacy      TransportType = "sse-legacy"
)

// ConnectionStatus 是连接状态。
//
// 对账 `src/mcp/types.ts` 的 `McpConnectionState.status`（7 态）。
//
// `awaiting-approval` / `denied` 属**连接级审批门**（TS issue #215，
// 在 spawn 之前拦下）。Go 侧 W1–W3 不实现该门，但**保留状态值**——
// 它们是 UI/消费端的分支判据，删掉会让将来接线时无值可用。
type ConnectionStatus string

const (
	StatusDisconnected     ConnectionStatus = "disconnected"
	StatusConnecting       ConnectionStatus = "connecting"
	StatusConnected        ConnectionStatus = "connected"
	StatusDegraded         ConnectionStatus = "degraded"
	StatusError            ConnectionStatus = "error"
	StatusAwaitingApproval ConnectionStatus = "awaiting-approval"
	StatusDenied           ConnectionStatus = "denied"
)

// ErrorTransport 是**错误归因用**的传输二分。
//
// 对账 `src/mcp/failure-classifier.ts:33` 的 `transport?: 'stdio' | 'remote'`。
//
// **★ 为什么不能复用 `TransportType`**：那是 `McpTransportType` 的
// **三值**（stdio / streamableHttp / sse-legacy，见 `src/mcp/types.ts:1`），
// 而错误分类只关心**二分**——「本地子进程」还是「远端」。两者在 TS 里是
// **两个独立枚举**，Go 侧照搬这一区分（混用会让分类器把 `sse-legacy`
// 当成非 stdio 而错归 network）。
type ErrorTransport string

const (
	// ErrorTransportStdio = 本地子进程（stdio）。
	ErrorTransportStdio ErrorTransport = "stdio"
	// ErrorTransportRemote = url 型（Streamable HTTP / SSE）。
	ErrorTransportRemote ErrorTransport = "remote"
)

// ConnectionState 是单个 server 的连接状态快照。
//
// 对账 `src/mcp/types.ts` 的 `McpConnectionState`。
type ConnectionState struct {
	ServerID    string           `json:"serverId"`
	Status      ConnectionStatus `json:"status"`
	Transport   TransportType    `json:"transport,omitempty"`
	ToolCount   int              `json:"toolCount"`
	Error       string           `json:"error,omitempty"`
	ErrorHint   string           `json:"errorHint,omitempty"`
	LastErrorAt int64            `json:"lastErrorAt,omitempty"`
}

// ServerConfig 是单个 MCP server 的配置。
//
// 对账 `src/mcp/config.ts` 的 `mcpServerConfigSchema`（字段子集——只收
// stdio 路径用得到的）。**未收**：`url` / `headers` / `transportHint` /
// `auth`（属 HTTP 传输）、`workspace`（属 subAgent 策略）、`policy`（属
// 工具级策略，W3 经 WrapOptions 传）。
//
// **零值语义**：`Disabled` 为 true 时跳过（对账 TS 的 `disabled` 字段）。
type ServerConfig struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// Config 是 MCP 子系统配置。
//
// 对账 `src/mcp/config.ts` 的 `mcpConfigSchema`。
type Config struct {
	Enabled   bool                    `json:"enabled"`
	Servers   map[string]ServerConfig `json:"servers,omitempty"`
	TimeoutMS int                     `json:"timeoutMs,omitempty"`
}

// DefaultTimeoutMS 是单次工具调用的默认超时。
//
// 对账 `src/mcp/manager.ts:48` 的 `DEFAULT_MCP_TIMEOUT_MS = 60_000`。
const DefaultTimeoutMS = 60_000

// Timeout 返回生效的超时（毫秒）——配置覆盖或默认。
//
// 对账 `manager.ts:171` 的 `config.timeoutMs ?? DEFAULT_MCP_TIMEOUT_MS`。
func (c Config) Timeout() int {
	if c.TimeoutMS > 0 {
		return c.TimeoutMS
	}
	return DefaultTimeoutMS
}
