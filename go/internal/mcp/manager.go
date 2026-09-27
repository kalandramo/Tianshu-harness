package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/kalandramo/tianshu/go/internal/tools"
)

// jsonUnmarshal 是 `json.Unmarshal` 的薄包装（便于将来替换为稳定解析器）。
func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// manager.go —— MCP 生命周期与工具发现。
//
// 对账 `src/mcp/manager.ts`（781 行）的**最小可运行子集**：
// 连接 → initialize 握手 → tools/list → 包装成工具。
//
// # 明确未做的部分（诚实标注 → W5 另刀）
//
//   - **指数退避重连**（TS `_onTransportClosed` 的退避链）
//   - **health-check 后台探测**（TS `health-check.ts` 240 行）
//   - **连接级审批门**（TS issue #215 的 `server-approval.ts`——Go 侧
//     无 TUI/REST 消费端，做出来是「无人可批」的僵局，故不做）
//   - **reconcileFromConfig 热重载**（TS 支持配置变更时增量调和）
//
// 这些在 TS 里都是**增强**（连接失败仍可用、配置变更多数场景下一次启动即可），
// 缺它们不影响「能连、能发现、能调用」这条主干。

// serverConn 是一个已建立的 server 连接。
type serverConn struct {
	serverID string
	cfg      ServerConfig
	rpc      *RPC
	tools    []tools.Tool
}

// Manager 管理多个 MCP server 连接。
type Manager struct {
	cfg Config

	// requestTimeoutMS 是单次 MCP 请求的超时（毫秒）。
	//
	// **唯一来源是 `cfg.Timeout()`**（第一百一十刀 finding #3）。
	// 此前三处请求硬编码 `DefaultTimeoutMS` → 用户在配置里写的
	// `timeoutMs` 完全不生效（`Config.Timeout()` 甚至零生产调用方）。
	requestTimeoutMS int

	mu      sync.Mutex
	conns   map[string]*serverConn
	states  map[string]ConnectionState
	baseCwd string
}

// NewManager 创建管理器。
//
// `baseCwd` 用于 `ServerConfig.Cwd` 缺省时的回退（对账 TS 的进程 cwd）。
func NewManager(cfg Config, baseCwd string) *Manager {
	return &Manager{
		cfg:              cfg,
		requestTimeoutMS: cfg.Timeout(),
		conns:            make(map[string]*serverConn),
		states:           make(map[string]ConnectionState),
		baseCwd:          baseCwd,
	}
}

// Initialize 连接全部启用的 server（并发）。
//
// 对账 `manager.ts` 的 `initialize()`：它并发连所有 server，**单个失败不阻塞
// 其余**（TS 里失败被记录进 states 并继续）。
//
// **返回 error 的语义**：仅在**完全没有可连的 server**（配置为空）时返回 nil；
// 单个 server 的失败记入 State 而非返回——因为「一个 server 挂了」不该让
// 整个会话起不来（TS 同款语义）。
//
// # ctx 语义（★ 本刀接线时补上的真实行为）
//
// `ctx` 取消时**立即中止等待**：在飞的 `initialize` / `tools/list` 请求
// 被 abort（各自返回错误并记入 State），`Initialize` 提前返回。
//
// **为什么必须要**（不是「为了符合 Go 惯例」）：接进 CLI 后，用户在
// MCP server 启动慢（npx 首次拉包可达数十秒）时按 Ctrl+C，
// 若无 ctx 感知，`Initialize` 会一直等到**每个** server 的 60s 超时才返回
// ——用户明明取消了，进程却卡住一分钟，是明确的可用性缺陷。
func (m *Manager) Initialize(ctx context.Context) error {
	if !m.cfg.EnabledOrDefault() {
		return nil
	}

	// 按 serverID 排序——保证 Initialize 的确定性（日志/状态顺序稳定）
	ids := make([]string, 0, len(m.cfg.Servers))
	for id, sc := range m.cfg.Servers {
		if sc.Disabled {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.connectOne(ctx, id, m.cfg.Servers[id])
		}(id)
	}

	// 等全部连完，或 ctx 取消（取消时不再等，但已起的 goroutine 会
	// 在各自的请求超时/被 abort 后自行收尾——它们持有的连接若已建立
	// 会被 Shutdown 回收）。
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// connectOne 连接单个 server 并发现工具。
func (m *Manager) connectOne(ctx context.Context, serverID string, sc ServerConfig) {
	m.setState(ConnectionState{
		ServerID: serverID, Status: StatusConnecting, Transport: TransportStdio,
	})

	tr, err := SpawnStdio(sc, m.baseCwd)
	if err != nil {
		classified := ClassifyMcpError(err, ErrorContext{Transport: ErrorTransportStdio})
		m.setState(ConnectionState{
			ServerID:  serverID,
			Status:    StatusError,
			Transport: TransportStdio,
			Error:     err.Error(),
			ErrorHint: classified.Suggestion,
		})
		return
	}

	rpc := NewRPC(tr)

	// ① initialize 握手
	if _, err := rpc.RequestCtx(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "rivet", "version": "0.1.0"},
	}, m.requestTimeoutMS); err != nil {
		_ = tr.Close()
		classified := ClassifyMcpError(err, ErrorContext{Transport: ErrorTransportStdio})
		m.setState(ConnectionState{
			ServerID:  serverID,
			Status:    StatusError,
			Transport: TransportStdio,
			Error:     err.Error(),
			ErrorHint: classified.Suggestion,
		})
		return
	}
	// 按 MCP 规范发 initialized 通知（不等响应）
	_ = rpc.Notify("notifications/initialized", nil)

	// ② tools/list
	defs, err := m.listTools(ctx, rpc)
	if err != nil {
		_ = tr.Close()
		classified := ClassifyMcpError(err, ErrorContext{Transport: ErrorTransportStdio})
		m.setState(ConnectionState{
			ServerID:  serverID,
			Status:    StatusError,
			Transport: TransportStdio,
			Error:     err.Error(),
			ErrorHint: classified.Suggestion,
		})
		return
	}

	// ③ 包装成 tools.Tool
	wrapped := make([]tools.Tool, 0, len(defs))
	for _, d := range defs {
		def := d // 闭包捕获
		call := func(args map[string]any) (CallResult, error) {
			return m.callTool(rpc, serverID, def.Name, args)
		}
		wrapped = append(wrapped, WrapTool(serverID, def, call, WrapOptions{
			Transport: ErrorTransportStdio,
		}))
	}

	m.mu.Lock()
	m.conns[serverID] = &serverConn{serverID: serverID, cfg: sc, rpc: rpc, tools: wrapped}
	m.mu.Unlock()

	m.setState(ConnectionState{
		ServerID:  serverID,
		Status:    StatusConnected,
		Transport: TransportStdio,
		ToolCount: len(wrapped),
	})
}

// listTools 发 `tools/list` 并解析。
func (m *Manager) listTools(ctx context.Context, rpc *RPC) ([]ToolDef, error) {
	raw, err := rpc.RequestCtx(ctx, "tools/list", map[string]any{}, m.requestTimeoutMS)
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := jsonUnmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp: parse tools/list: %w", err)
	}
	return res.Tools, nil
}

// callTool 调 `tools/call`。
func (m *Manager) callTool(rpc *RPC, serverID, toolName string, args map[string]any) (CallResult, error) {
	m.mu.Lock()
	_, connected := m.conns[serverID]
	m.mu.Unlock()
	if !connected {
		// 对账 manager.ts 的 perToolCallFn：断连时明确报错
		// （而非静默失败——那会让模型以为工具不存在）
		return CallResult{}, fmt.Errorf("MCP server %q is disconnected", serverID)
	}

	raw, err := rpc.Request("tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	}, m.requestTimeoutMS)
	if err != nil {
		return CallResult{}, err
	}

	var res struct {
		Content []ContentPart `json:"content"`
		IsError bool          `json:"isError"`
	}
	if err := jsonUnmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("mcp: parse tools/call: %w", err)
	}
	return CallResult{Content: res.Content, IsError: res.IsError}, nil
}

// AllTools 返回全部已连接 server 的工具（供装配层注册）。
//
// **顺序稳定**（按 serverID 排序）——工具定义进请求体的 `tools` 段，
// 顺序不定会破坏前缀缓存。
func (m *Manager) AllTools() []tools.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []tools.Tool
	for _, id := range ids {
		out = append(out, m.conns[id].tools...)
	}
	return out
}

// States 返回全部连接状态（供 UI/诊断）。
func (m *Manager) States() []ConnectionState {
	m.mu.Lock()
	defer m.mu.Unlock()

	ids := make([]string, 0, len(m.states))
	for id := range m.states {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]ConnectionState, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.states[id])
	}
	return out
}

// Shutdown 关闭全部连接（释放子进程）。
//
// 对账 `bootstrap.ts:1393` 的 `killChildrenSync?.()` + `shutdown?.()`。
func (m *Manager) Shutdown() {
	m.mu.Lock()
	conns := make([]*serverConn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.conns = make(map[string]*serverConn)
	m.mu.Unlock()

	for _, c := range conns {
		c.rpc.Dispose() // Dispose 内含 tr.Close() → 杀进程树
	}
}

// KillChildrenSync 是 Shutdown 的别名。
//
// **为什么并存**：TS 侧是两个方法（`killChildrenSync` 在 `bootstrap.ts:1393`
// 的关闭路径调、`shutdown` 紧随其后）。Go 侧合并为一个（无「同步/异步」之分），
// 保留两个名字是为了让**对账 TS 的调用点**可读。
func (m *Manager) KillChildrenSync() { m.Shutdown() }

func (m *Manager) setState(s ConnectionState) {
	m.mu.Lock()
	m.states[s.ServerID] = s
	m.mu.Unlock()
}
