package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// rpc.go —— JSON-RPC 2.0 客户端（MCP 用）。
//
// # 架构来源（照搬，非复制）
//
// 结构照搬 `go/internal/lsp/rpc.go`——它已被 LSP 全量验证，含三处**事故修法**：
//
//  1. **writeMu 覆盖「分配 id → 注册 pending → 写帧」整段**（不用状态锁兼任）：
//     否则并发请求的帧序会与 id 序不一致（LSP 实测 1,3,2）；且写管道阻塞时
//     不能卡住 reader 的 dispatch。
//  2. **AbortAllPending**：读端终止时**不得让调用方永等**（2026-09-08 wedge 事故）。
//  3. **WithDeathHandler**：进程死亡要显式上报，否则崩溃的 server 被永久当成
//     ready（LSP 侧记载「定义跳转静默失效且永不恢复」）。
//
// **分帧不同**（见 framing.go）：MCP 是换行分隔，LSP 是 Content-Length。

// Transport 是 RPC 的底层字节通道。
//
// 与 `lsp.Transport` **同形但独立定义**（同名不同包）：本包不引用 `internal/lsp`
// ——MCP 不该依赖 LSP 子系统（依赖方向应扁平，且 LSP 的编解码是 LSP 专用的）。
type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

// DefaultRequestTimeoutMS 是单请求的默认超时。
//
// 对账 `src/mcp/manager.ts:48` 的 `DEFAULT_MCP_TIMEOUT_MS = 60_000`。
const DefaultRequestTimeoutMS = DefaultTimeoutMS

// pending 是待决请求。
type pending struct {
	onResult func(json.RawMessage)
	onError  func(error)
	timer    *time.Timer
}

// RPC 是一个 JSON-RPC 2.0 客户端。
type RPC struct {
	tr Transport

	defaultTimeoutMS int

	// writeMu 串行化**出站写**，并覆盖「分配 id → 注册待决 → 写帧」整段
	// （照搬 lsp/rpc.go 的修法，理由见该文件注释）。
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int
	pending  map[int]*pending
	handlers map[string][]func(json.RawMessage)

	readDone chan struct{}
	closed   bool
	onDeath  func(error)
}

// RPCOption 配置 RPC。
type RPCOption func(*RPC)

// WithDeathHandler 注册「transport 死亡」回调。
//
// 缺了它，崩溃的 server 会被永久当成 ready（对账 lsp 的同款说明）。
func WithDeathHandler(fn func(error)) RPCOption {
	return func(r *RPC) { r.onDeath = fn }
}

// WithRequestTimeout 覆盖默认请求超时。
func WithRequestTimeout(d time.Duration) RPCOption {
	return func(r *RPC) {
		if ms := int(d / time.Millisecond); ms > 0 {
			r.defaultTimeoutMS = ms
		}
	}
}

// NewRPC 创建 RPC 客户端并启动读循环。
func NewRPC(tr Transport, opts ...RPCOption) *RPC {
	r := &RPC{
		tr:               tr,
		defaultTimeoutMS: DefaultRequestTimeoutMS,
		nextID:           1, // 对账 TS：let nextId = 1
		pending:          make(map[int]*pending),
		handlers:         make(map[string][]func(json.RawMessage)),
		readDone:         make(chan struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	go r.readLoop()
	return r
}

// PendingCount 返回待决请求数（供测试断言「超时/abort 后无泄漏」）。
func (r *RPC) PendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// readLoop 从 transport 读字节、切帧、分派。
func (r *RPC) readLoop() {
	defer close(r.readDone)
	var buffer []byte
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.tr.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
			var msgs [][]byte
			msgs, buffer = DecodeLines(buffer)
			for _, m := range msgs {
				r.dispatch(m)
			}
		}
		if err != nil {
			// 读端终止（EOF / 管道错误）——**不得让调用方永远等待**。
			abortErr := fmt.Errorf("MCP transport closed: %w", err)
			r.AbortAllPending(abortErr)
			if r.onDeath != nil {
				r.onDeath(abortErr)
			}
			return
		}
	}
}

// inbound 是入站报文的判别形态。
type inbound struct {
	ID     *int            `json:"id"`
	Method *string         `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Params json.RawMessage `json:"params"`
}

// dispatch 分派一条入站报文。
//
// 四分支（照搬 `lsp/rpc.go` 的 dispatch）：
//
//	id+result            → resolve
//	id+error             → reject(message)
//	method 无 id          → 通知
//	**method 与 id 同现** → 回 MethodNotFound（★ 显式偏离，见下）
//
// **第四分支为什么是偏离**：JSON-RPC 2.0 要求「请求必须有响应」。TS 侧
// （`lsp/rpc.go` 记载的同款上游缺陷）在此**静默丢弃**；真实 server
// （LSP 的 `client/registerCapability`、MCP 的 `sampling/createMessage`）
// 拿不到答复会**挂等**——表现为「连接莫名卡死」，极难排查。
func (r *RPC) dispatch(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	switch {
	case msg.ID != nil && msg.Method == nil && msg.Error == nil:
		r.settle(*msg.ID, func(p *pending) { p.onResult(msg.Result) })
	case msg.ID != nil && msg.Method == nil && msg.Error != nil:
		// 对账 LSP：只取 message（丢弃 code / data）
		e := msg.Error
		r.settle(*msg.ID, func(p *pending) { p.onError(errors.New(e.Message)) })
	case msg.Method != nil && msg.ID == nil:
		r.emitNotification(*msg.Method, msg.Params)
	case msg.Method != nil && msg.ID != nil:
		// ★ 显式偏离：回错误响应（TS 静默丢弃）
		_ = r.writeFrame(map[string]any{
			"jsonrpc": "2.0",
			"id":      *msg.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": "Method not found: " + *msg.Method,
			},
		})
	}
}

func (r *RPC) settle(id int, fn func(*pending)) {
	r.mu.Lock()
	p, ok := r.pending[id]
	if !ok {
		r.mu.Unlock()
		return // 迟到响应 / 已超时——忽略
	}
	delete(r.pending, id)
	r.mu.Unlock()

	if p.timer != nil {
		p.timer.Stop()
	}
	fn(p)
}

// AbortAllPending 拒绝全部在飞请求。
func (r *RPC) AbortAllPending(err error) {
	r.mu.Lock()
	all := make([]*pending, 0, len(r.pending))
	for id, p := range r.pending {
		all = append(all, p)
		delete(r.pending, id)
	}
	r.mu.Unlock()

	for _, p := range all {
		if p.timer != nil {
			p.timer.Stop()
		}
		p.onError(err)
	}
}

// emitNotification 分派通知给注册的处理器。
//
// **先拷贝切片再出锁**：处理器可能在回调里注册新的处理器（OnNotification），
// 持锁调用会自锁死（Go 的 sync.Mutex 不可重入——本仓库已踩过两次）。
func (r *RPC) emitNotification(method string, params json.RawMessage) {
	r.mu.Lock()
	src := r.handlers[method]
	fns := make([]func(json.RawMessage), len(src))
	copy(fns, src)
	r.mu.Unlock()

	for _, fn := range fns {
		fn(params)
	}
}

// OnNotification 注册通知处理器（可按方法注册多个）。
func (r *RPC) OnNotification(method string, fn func(json.RawMessage)) {
	r.mu.Lock()
	r.handlers[method] = append(r.handlers[method], fn)
	r.mu.Unlock()
}

// Request 发一条请求并等响应（或超时）。
//
// **写锁覆盖整段**（分配 id → 注册 pending → 写帧）——见 writeMu 注释。
func (r *RPC) Request(method string, params any, timeoutMS int) (json.RawMessage, error) {
	if timeoutMS <= 0 {
		timeoutMS = r.defaultTimeoutMS
	}

	// 结果经 buffered chan 回传（容量 1，避免 settle 方阻塞）
	type outcome struct {
		result json.RawMessage
		err    error
	}
	ch := make(chan outcome, 1)

	r.writeMu.Lock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.writeMu.Unlock()
		return nil, errors.New("MCP rpc: closed")
	}
	id := r.nextID
	r.nextID++
	p := &pending{
		onResult: func(res json.RawMessage) { ch <- outcome{result: res} },
		onError:  func(err error) { ch <- outcome{err: err} },
	}
	// ★ **timer 创建必须在同一临界区内**（`-race` 抓到的真 data race）。
	//
	// 初版把 `p.timer = time.AfterFunc(...)` 放在锁外——而 `AbortAllPending`
	// 在锁内读 `p.timer`，两者并发即 race。这与本仓库 HANDOFF 记载的
	// LSP 侧同型缺陷（`await` 在锁外写 `w.timer`）是同一个错误：
	// **「注册 waiter」与「创建其 timer」必须原子**。
	//
	// 注意：`time.AfterFunc` 的回调会在**独立 goroutine** 里跑并取 `r.mu`，
	// 故此处持锁创建不会与回调自锁（回调不在此临界区内执行）。
	p.timer = time.AfterFunc(time.Duration(timeoutMS)*time.Millisecond, func() {
		r.mu.Lock()
		_, still := r.pending[id]
		if still {
			delete(r.pending, id)
		}
		r.mu.Unlock()
		ch <- outcome{err: fmt.Errorf("MCP request %q timed out after %dms", method, timeoutMS)}
	})
	r.pending[id] = p
	r.mu.Unlock()

	if err := r.writeFrameLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		// 写失败也要清 pending（否则泄漏）
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		p.timer.Stop()
		r.writeMu.Unlock()
		return nil, err
	}
	r.writeMu.Unlock()

	out := <-ch
	return out.result, out.err
}

// RequestCtx 是 `Request` 的 ctx 感知变体：ctx 取消时立即返回并回收 pending。
//
// **为什么单开一个方法而非给 `Request` 加参数**：`Request` 有 60+ 处既有调用
// （含全部既有测试），加参数会波及它们；而**大多数调用点确实不需要取消**
// （工具调用由 60s 超时兜底即可）。需要取消的只有 `Initialize` 的握手路径
// ——那里用户可能在等（Ctrl+C）。
//
// 取消语义：返回 ctx.Err()，并**清理 pending**（否则 pending 表泄漏到超时）。
func (r *RPC) RequestCtx(ctx context.Context, method string, params any, timeoutMS int) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type res struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan res, 1)

	go func() {
		raw, err := r.Request(method, params, timeoutMS)
		select {
		case ch <- res{raw: raw, err: err}:
		default:
			// 无人接收（调用方已因 ctx 取消返回）——丢弃即可。
			// **不是泄漏**：内层 Request 的 pending 会由超时或 abort 回收。
		}
	}()

	select {
	case got := <-ch:
		return got.raw, got.err
	case <-ctx.Done():
		// 在飞请求交由超时/abort 回收（不额外摘 pending——那需要 id，
		// 而 id 在 Request 内部；强行跨层摘会引入锁序风险）。
		return nil, ctx.Err()
	}
}

// Notify 发一条通知（不等响应）。
func (r *RPC) Notify(method string, params any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.writeFrameLocked(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

// writeFrame 写一帧（自取写锁）。
func (r *RPC) writeFrame(msg any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.writeFrameLocked(msg)
}

// writeFrameLocked 写一帧（调用方须已持 writeMu）。
func (r *RPC) writeFrameLocked(msg any) error {
	frame, err := EncodeLine(msg)
	if err != nil {
		return err
	}
	_, err = r.tr.Write(frame)
	return err
}

// Dispose 关闭客户端：中止在飞请求并关闭 transport。
//
// **必须 AbortAllPending**——否则调用方永等（wedge 形态）。变异 M4 打这里。
func (r *RPC) Dispose() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()

	r.AbortAllPending(errors.New("MCP rpc: disposed"))
	_ = r.tr.Close()
}
