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
// stdio 路径用得到的）。**未收**：`headers` / `transportHint` /
// `auth`（属 HTTP 传输）、`workspace`（属 subAgent 策略）。
//
// **零值语义**：`Disabled` 为 true 时跳过（对账 TS 的 `disabled` 字段）。
type ServerConfig struct {
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`

	// URL 是 url 型 server 的端点。
	//
	// **为什么「不实现它却要收它」**（第一百一十刀 finding #2）：
	// 此前 `ServerConfig` 完全不收 `url` → 用户的 url 型 server 被 JSON 解码
	// 静默丢弃 → 进 map 后 `Command` 为空 → `SpawnStdio` 报 `command is empty`
	// → 错误进 `State` 但**装配层不打印** → 用户看到「工具没出现」而不知为什么。
	//
	// 收了它，`validate` 才能识别并**明确拒绝 + 给出原因**。
	// HTTP 传输本身仍不在本刀范围（见包内 `UnsupportedServer` 的说明）。
	URL string `json:"url,omitempty"`

	// Policy 是 server 级的工具策略（对账 `src/mcp/config.ts:9-14`）。
	Policy *ServerPolicy `json:"policy,omitempty"`
}

// ServerPolicy 是 server 级策略容器。
//
// 对账 `mcpServerPolicySchema`（`src/mcp/config.ts:12-14`）。
// **键是「加 mcp__ 前缀之前」的原始 MCP 工具名**——`connectOne` 查表时
// 用 `def.Name`（原始名）而非 rivet 名。
type ServerPolicy struct {
	Tools map[string]ToolPolicy `json:"tools"`
}

// ToolPolicy 是单个 MCP 工具的策略。
//
// 对账 `mcpToolPolicySchema`（`src/mcp/config.ts:9-12`）：
//
//	capability: z.enum(['read','write','execute','network'])
//	requireApproval: z.literal(true).optional()
//
// **为什么 `Capability` 是 `Capability` 类型而非 string**：
// 直接复用 `policy.go` 的枚举，避免两处定义漂移（对账 TS 的 zod enum
// 与 `McpCapability` 是同一份）。
type ToolPolicy struct {
	Capability      Capability `json:"capability"`
	RequireApproval bool       `json:"requireApproval,omitempty"`
}

// UnsupportedServer 记录一个**被语义检查拒绝**的 server。
//
// **为什么不直接丢弃**（第一百一十刀 finding #2）：静默丢弃正是原缺陷——
// 用户配了 url 型 server，工具不出现，却没有任何线索。保留 ID + 原因
// 让装配层能打印诊断。
type UnsupportedServer struct {
	ID     string
	Reason string
}

// Config 是 MCP 子系统配置。
//
// 对账 `src/mcp/config.ts` 的 `mcpConfigSchema`。
type Config struct {
	// Enabled 缺席 = 启用（对齐 TS 的 `enabled: z.boolean().default(true)`，
	// 见 `src/mcp/config.ts:83`）。
	//
	// **为什么用指针**（第一百一十刀 finding #1）：`bool` 的零值 `false`
	// 与「用户显式写了 false」不可区分 → 缺席时整个 MCP 被静默关掉。
	// 指针让「缺席」可表达；读取一律走 `EnabledOrDefault()`（唯一收口点，
	// 不变量由结构保证，而非靠每个调用点记得判 nil）。
	Enabled *bool `json:"enabled,omitempty"`

	Servers   map[string]ServerConfig `json:"servers,omitempty"`
	TimeoutMS int                     `json:"timeoutMs,omitempty"`

	// Unsupported 是语义检查拒绝掉的 server（不参与 JSON——纯运行期诊断）。
	Unsupported []UnsupportedServer `json:"-"`
}

// EnabledOrDefault 是 `Enabled` 的**唯一**读取点。
//
// 缺席（nil）→ true，对齐 TS 的 `.default(true)`。
//
// **为什么做成方法而非在加载时就地改写**（计划「方案取舍」的 A/B）：
// `Config` 是导出类型、被测试直接构造（`manager_test.go` 多处），
// 就地改写会让「配置对象」丢失「用户是否显式写过」这一信息。
// 用指针 + 访问器让类型自己承载三态。
func (c Config) EnabledOrDefault() bool { return c.Enabled == nil || *c.Enabled }

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
