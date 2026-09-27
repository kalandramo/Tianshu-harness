// Package lsp 是 LSP 子系统——按文件扩展名路由到对应语言服务器，
// 提供「跳转定义」与「查找引用」。
//
// 对账 TS `src/lsp/`（真实缺口 1111 行：tools.ts 133 / rpc.ts 195 /
// manager.ts 375 / multi-manager.ts 223 / server-registry.ts 185）。
//
// # 不移植的面（已核实，非缺口）
//
//   - `src/lsp/client.ts`（363 行）**不是** LSP 协议客户端，而是 tsc 类型检查
//     执行器（`runTypeCheck` / `runTscSubprocess`）；Go 侧等价物已在
//     `go/internal/tools/testspawn.go`（注释自述对账 `lsp/client.ts::runTscSubprocess`）。
//   - `src/lsp/typecheck-cache.ts`（556 行）是 tsc 输出的跨进程缓存门，其唯一
//     入口是 `client.ts::runTscShared`——与 goto/refs 无依赖。
//   - `src/lsp/diagnostics.ts`（34 行）解析的是 tsc 文本输出（正则匹配
//     `file(line,col): error TSxxxx`），**不解析 LSP 协议诊断**。
//   - `lsp_diagnostics` 工具：TS 侧**不存在**（`grep "name: 'lsp_" src/` 只命中
//     `lsp_goto_definition` / `lsp_find_references`）。该字面量仅在
//     `src/agent/advisory-readback.ts` 的工具名清单里出现——是幻影条目，
//     Go 侧 `advisory_readback.go` 已忠实对齐，故**不做才是 parity**。
//
// # 门链已在等（零接线自动生效）
//
// `go/internal/agent/probe_discipline.go:65-68` 注释逐字预告：
//
//	前瞻项（Go 侧尚无对应工具，保留以对齐 TS）：… lsp_goto_definition /
//	lsp_find_references … 判定集里的未知名字**行为等价于不存在**（永不匹配）
//	——保留无害且**将来移植时自动生效**。
package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

// DefaultRequestTimeoutMS 是单请求的默认超时。
//
// 对账 TS `DEFAULT_LSP_REQUEST_TIMEOUT_MS = 45_000`（`rpc.ts:39`）。
const DefaultRequestTimeoutMS = 45_000

// Transport 是 RPC 的底层字节通道（真实实现是子进程的 stdin/stdout）。
//
// **为什么是字节而非 string**（TS 侧 `decodeMessages` 返回 `rest: string`）：
// 见 `DecodeMessages` 的说明。
type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

// EncodeMessage 把 JSON 正文封成一帧。
//
// 对账 TS `encodeMessage`：
//
//	return `Content-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`
//
// 长度按**字节**（`Buffer.byteLength` 的 Go 等价物是 `len`）。
func EncodeMessage(body []byte) []byte {
	head := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	out = append(out, body...)
	return out
}

// crlfcrlf 是 LSP 帧头与正文的分隔符。
var crlfcrlf = []byte("\r\n\r\n")

// contentLengthPrefix 是头行的前缀（大小写敏感，对账 TS 的正则
// `/^Content-Length: (\d+)/m` 的字面前缀部分）。
var contentLengthPrefix = []byte("Content-Length: ")

// DecodeMessages 从累积缓冲中切出完整帧，返回（报文正文列表, 剩余缓冲）。
//
// # ★ 为什么必须按**字节**缓冲（与 TS 的显式偏离）
//
// TS 的签名是 `(input: string | Buffer) => { messages; rest: string }`——
// **rest 是 string**。它内部 `Buffer.from(rest, 'utf8')` 再拼下一块 chunk：
//
//	buffer = Buffer.from(rest, 'utf8')          // ← rest 已被切过
//
// stdin 的 chunk 边界由内核缓冲区决定，会落在**任意**字节位置，包括 UTF-8
// 多字节字符的内部。若在「多字节字符中间」切开，`toString('utf8')` 会把那个
// 残字节转成 **U+FFFD**（替换字符），再编码回 UTF-8 时字节已经**变了**——
// 后续的 `JSON.parse` 失败，被 `catch { /* Skip malformed message */ }`
// **静默丢弃**。后果不是报错，而是模型拿到空结果或等到 45s 超时。
//
// Go 侧改为**全程字节**（`[]byte` 进出，不做任何字符串转换），从根上消除
// 这个切点敏感性问题。`TestDecodeMessages_MultibyteSplitAtEveryOffset`
// 穷举所有切点把它钉住。
//
// # 语义对账（逐条）
//
//   - 找头尾 `\r\n\r\n`；找不到 → 停止（保留缓冲）
//   - 头里取 `Content-Length`；取不到 → 跳过该头（`offset = headerEnd + 4`）
//   - 正文不足 contentLength 字节 → 停止（保留缓冲）
//   - 正文 JSON 解析失败 → **跳过该帧**（对账 TS 的 catch），但不影响后续帧
func DecodeMessages(buf []byte) (messages [][]byte, rest []byte) {
	offset := 0
	for {
		headerEnd := bytes.Index(buf[offset:], crlfcrlf)
		if headerEnd == -1 {
			break
		}
		headerEnd += offset

		header := buf[offset:headerEnd]
		contentLength, ok := parseContentLength(header)
		if !ok {
			// 对账 TS：坏头跳过（推进到正文起点），不阻断后续帧
			offset = headerEnd + 4
			continue
		}

		bodyStart := headerEnd + 4
		if len(buf)-bodyStart < contentLength {
			break // 正文未收全——保留缓冲等下一块
		}

		body := buf[bodyStart : bodyStart+contentLength]
		// 对账 TS 的 `try { messages.push(JSON.parse(body)) } catch { /* skip */ }`
		if json.Valid(body) {
			messages = append(messages, append([]byte(nil), body...))
		}
		offset = bodyStart + contentLength
	}

	// 剩余缓冲按**字节**原样保留（绝不经 string 转换）
	rest = append([]byte(nil), buf[offset:]...)
	return messages, rest
}

// parseContentLength 从头块中提取 Content-Length。
//
// 对账 TS 的 `/^Content-Length: (\d+)/m`——`m` 标志下 `^` 匹配行首，
// 故我们要在**行首**位置找前缀。
func parseContentLength(header []byte) (int, bool) {
	atLineStart := true
	i := 0
	for i <= len(header)-len(contentLengthPrefix) {
		if atLineStart && bytes.HasPrefix(header[i:], contentLengthPrefix) {
			rest := header[i+len(contentLengthPrefix):]
			// 取连续数字
			n := 0
			for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
				n++
			}
			if n == 0 {
				return 0, false
			}
			v, err := strconv.Atoi(string(rest[:n]))
			if err != nil {
				return 0, false
			}
			return v, true
		}
		if header[i] == '\n' {
			atLineStart = true
		} else {
			atLineStart = false
		}
		i++
	}
	return 0, false
}

// pending 是待决请求。
type pending struct {
	onResult func(json.RawMessage)
	onError  func(error)
	timer    *time.Timer
}

// RPC 是一个 JSON-RPC 2.0 客户端（对账 TS `createRpcClient`）。
type RPC struct {
	tr Transport

	defaultTimeoutMS int

	// writeMu 串行化**出站写**，并覆盖「分配 id → 注册待决 → 写帧」整段。
	//
	// **为什么必须覆盖 id 分配**（本波测试发现的缺陷）：若只在 mu 下分配 id、
	// 写帧在锁外，两个并发请求的帧可能**乱序**落到 wire 上（实测 id 序 1,3,2）。
	// TS 是单线程，`const id = nextId++` 与 `writable.write(...)` 之间无 await，
	// 故帧序恒等于 id 序。Go 侧要复刻这个确定性，就必须把两者放进同一临界区。
	//
	// **为什么不能用 mu 兼任**：写管道在 server 不读时会阻塞。若阻塞发生在
	// 持有 mu 时，reader goroutine 的 dispatch（也要 mu）会被一起卡住 →
	// 谁都不推进（潜在死锁）。故写用独立的 writeMu，mu 只做短暂的状态读写。
	//
	// **锁序**：writeMu → mu（仅 Request 如此嵌套）；dispatch 的回写只取 writeMu。
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int
	pending  map[int]*pending
	handlers map[string][]func(json.RawMessage)

	readDone chan struct{}
	closed   bool
}

// RPCOption 配置 RPC。
type RPCOption func(*RPC)

// WithRequestTimeout 覆盖默认请求超时（对账 TS 的 `requestTimeoutMs` 选项）。
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
			msgs, buffer = DecodeMessages(buffer)
			for _, m := range msgs {
				r.dispatch(m)
			}
		}
		if err != nil {
			// 读端终止（EOF / 管道错误）——对账 TS 的 transportDead：
			// **不得让调用方永远等待**（2026-09-08 wedge 事故的修法）。
			r.AbortAllPending(fmt.Errorf("LSP transport closed: %w", err))
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
// 对账 TS 的三分支，**外加第四个分支**（见 `TestRPC_ServerToClientRequestGetsMethodNotFound`）：
//
//	TS：id+result → resolve；id+error → reject(message)；method 无 id → 通知
//	Go：以上三条 + **method 与 id 同现（server→client 请求）→ 回 MethodNotFound**
//
// 第四条是**显式偏离**：TS 静默丢弃它，而 JSON-RPC 2.0 要求请求必须有响应；
// 真实 server（typescript-language-server 的 `client/registerCapability`）
// 拿不到答复会挂等。属「移植时发现的上游缺陷」，与本仓库既有纪律一致
// （第九十七刀「移植死代码 = 搬运缺陷」、第一百刀 import_resource 的
// symlink 缺陷修正）。
func (r *RPC) dispatch(raw []byte) {
	var msg inbound
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	switch {
	case msg.ID != nil && msg.Method == nil && msg.Error == nil:
		r.settle(*msg.ID, func(p *pending) {
			p.onResult(msg.Result)
		})
	case msg.ID != nil && msg.Method == nil && msg.Error != nil:
		// 对账 TS：**只取 message**（丢弃 code / data）
		err := msg.Error
		r.settle(*msg.ID, func(p *pending) {
			p.onError(fmt.Errorf("%s", err.Message))
		})
	case msg.Method != nil && msg.ID == nil:
		r.emitNotification(*msg.Method, msg.Params)
	case msg.Method != nil && msg.ID != nil:
		// ★ 决策 B：回错误响应（TS 在此静默丢弃）
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

// methodNotFoundCode 是 JSON-RPC 2.0 的 MethodNotFound 错误码。
const methodNotFoundCode = -32601

func (r *RPC) settle(id int, fn func(*pending)) {
	r.mu.Lock()
	p, ok := r.pending[id]
	if !ok {
		r.mu.Unlock()
		return // 迟到响应 / 已超时——忽略（不改状态）
	}
	delete(r.pending, id)
	r.mu.Unlock()

	if p.timer != nil {
		p.timer.Stop()
	}
	fn(p)
}

// AbortAllPending 拒绝全部在飞请求（对账 TS `abortAllPending`）。
func (r *RPC) AbortAllPending(err error) {
	r.mu.Lock()
	all := make([]*pending, 0, len(r.pending))
	for _, p := range r.pending {
		all = append(all, p)
	}
	r.pending = make(map[int]*pending)
	r.mu.Unlock()

	for _, p := range all {
		if p.timer != nil {
			p.timer.Stop()
		}
		p.onError(err)
	}
}

// OnNotification 注册通知处理器（对账 TS `onNotification`）。
func (r *RPC) OnNotification(method string, fn func(json.RawMessage)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[method] = append(r.handlers[method], fn)
}

func (r *RPC) emitNotification(method string, params json.RawMessage) {
	r.mu.Lock()
	hs := append([]func(json.RawMessage){}, r.handlers[method]...)
	r.mu.Unlock()
	if params == nil {
		params = json.RawMessage("{}")
	}
	for _, h := range hs {
		h(params)
	}
}

// writeFrame 序列化并写出一帧（writeMu 防并发写交错）。
//
// **不取 mu**：dispatch 的错误回写路径会调本函数，若它取 mu 就会与
// 「持有 mu 等写」的路径形成互等。详见 RPC.writeMu 的说明。
func (r *RPC) writeFrame(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := EncodeMessage(body)
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, err = r.tr.Write(frame)
	return err
}

// Request 发一个请求并等待响应。
//
// 对账 TS `request`：
//
//	const effectiveTimeout = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : defaultTimeoutMs
//	return new Promise((resolve, reject) => {
//	  const id = nextId++
//	  const timer = setTimeout(() => { if (pending.delete(id)) reject(new Error(`LSP request ${method} timed out after ${effectiveTimeout / 1000}s`)) }, effectiveTimeout)
//	  pending.set(id, { resolve, reject, timer })
//	  writable.write(encodeMessage(msg))
//	})
//
// **超时先删待决条目再拒绝**——否则迟到响应会二次 settle。
func (r *RPC) Request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	// 对账 TS：`Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : defaultTimeoutMs`
	// ——非正值回落到默认（而非「立即超时」）。
	effective := time.Duration(r.defaultTimeoutMS) * time.Millisecond
	if timeout > 0 {
		effective = timeout
	}

	type outcome struct {
		raw json.RawMessage
		err error
	}
	done := make(chan outcome, 1)

	var p any = params
	if p == nil {
		p = map[string]any{}
	}
	// ★ 临界区覆盖「分配 id → 注册待决 → 写帧」——复刻 TS 的帧序确定性。
	// 详见 RPC.writeMu 的说明。
	r.writeMu.Lock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.writeMu.Unlock()
		return nil, fmt.Errorf("LSP RPC client disposed")
	}
	id := r.nextID
	r.nextID++
	r.mu.Unlock()

	timer := time.AfterFunc(effective, func() {
		r.mu.Lock()
		_, stillPending := r.pending[id]
		if stillPending {
			delete(r.pending, id) // ★ 先删（对账 TS 的 pending.delete(id) 在 reject 之前）
		}
		r.mu.Unlock()
		if stillPending {
			done <- outcome{err: fmt.Errorf(
				"LSP request %s timed out after %ss", method, formatSeconds(effective))}
		}
	})

	r.mu.Lock()
	r.pending[id] = &pending{
		onResult: func(raw json.RawMessage) { done <- outcome{raw: raw} },
		onError:  func(err error) { done <- outcome{err: err} },
		timer:    timer,
	}
	r.mu.Unlock()

	frame := encodeRequestFrame(id, method, p)
	_, writeErr := r.tr.Write(frame)
	r.writeMu.Unlock()

	if writeErr != nil {
		r.settle(id, func(pt *pending) { pt.onError(writeErr) })
	}

	select {
	case o := <-done:
		return o.raw, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// encodeRequestFrame 构造请求帧（调用方须持有 writeMu）。
//
// 单独成函数是为了让「id 已知」与「编码」分离——id 在临界区内分配，
// 编码用分配到的 id。
func encodeRequestFrame(id int, method string, params any) []byte {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		// params 来自调用方，正常都是可序列化的 map/struct；
		// 失败时退化为空 params 帧（不让一次编码失败吞掉请求）。
		body, _ = json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": method, "params": map[string]any{},
		})
	}
	return EncodeMessage(body)
}

// Notify 发一条通知（无 id、不等响应）。
func (r *RPC) Notify(method string, params any) error {
	var p any = params
	if p == nil {
		p = map[string]any{}
	}
	return r.writeFrame(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  p,
	})
}

// Dispose 释放 RPC：拒绝在飞请求并停止读循环。
func (r *RPC) Dispose() {
	r.mu.Lock()
	already := r.closed
	r.closed = true
	r.handlers = make(map[string][]func(json.RawMessage))
	r.mu.Unlock()
	if already {
		return
	}
	r.AbortAllPending(fmt.Errorf("LSP RPC client disposed"))
	_ = r.tr.Close()
	<-r.readDone
}

// formatSeconds 复刻 TS 的 `${timeoutMs / 1000}` 浮点除法输出。
//
// JS：45000/1000 → "45"；50/1000 → "0.05"；1500/1000 → "1.5"。
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// itoa 是测试与内部共用的十进制转换。
func itoa(v int) string { return strconv.Itoa(v) }
