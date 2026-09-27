package lsp

import (
	"encoding/json"
	"sync"
)

// 本文件对账 TS `manager.ts` 的诊断接收链路（`client.ts`/`manager.ts:316-360`）。
//
// # 为什么需要一条独立链路
//
// LSP 的诊断是**服务端推送**（`textDocument/publishDiagnostics` 通知），
// 不是请求-响应。故取诊断 = ① 触发一次 didChange ② 等推送到达 ③ 读缓存。
// 三件缺一不可：没有 ② 就直接读会永远拿到空。
//
// TS 侧还有一个 **pull 模型**优先路径（LSP 3.17+ 的
// `textDocument/diagnostic` 请求）——Go 侧**暂不实现**（见下方说明）。

// diagCache 是 URI → 诊断列表的缓存（对账 TS `diagnosticCache`）。
//
// **并发安全**：publishDiagnostics 从 readLoop 到达，而读取来自工具执行的
// goroutine，故必须加锁（TS 是单线程事件循环，天然无此问题——这是移植
// 时最容易漏的一处）。
type diagCache struct {
	mu sync.RWMutex
	m  map[string][]LspDiagnostic
}

func newDiagCache() *diagCache {
	return &diagCache{m: map[string][]LspDiagnostic{}}
}

// set 存入某 URI 的最新诊断。
func (c *diagCache) set(uri string, diags []LspDiagnostic) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[uri] = diags
}

// get 取某 URI 的诊断（缺失 → nil，**不区分「空」与「未收到」**）。
//
// 调用方需要区分二者时用 `has`——TS 侧靠 `diagnosticCache.has(uri)` 判断
// 「推送是否已到达」，这是等待循环的退出条件。
func (c *diagCache) get(uri string) []LspDiagnostic {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.m[uri]
}

// has 报告某 URI 是否已收到过推送（含空列表推送）。
//
// ★ **与 `get` 的区别是语义关键**：LSP server 对「无诊断」会推送一个
// **空数组**——那是「已检查、无问题」的确切答案，而「未收到」是「还不知
// 道」。等待循环必须用 `has` 判断（对账 TS `diagnosticCache.has(uri)`），
// 否则干净的文件的诊断等待会一直空转到超时。
func (c *diagCache) has(uri string) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.m[uri]
	return ok
}

// delete 清除某 URI 的缓存（**在触发 didChange 之前**调）。
//
// 对账 TS `manager.ts:352` 的 `diagnosticCache.delete(uri)`——
// 注释逐字：「Clear stale cache BEFORE notify — avoid racing server
// publishDiagnostics」。
//
// 若在 notify 之后清，可能与服务端的推送**竞态**：推送先到、随后被我们
// 自己清掉 → 等待循环空转到超时。故顺序敏感。
func (c *diagCache) delete(uri string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, uri)
}

// publishDiagnosticsParams 是 `textDocument/publishDiagnostics` 的参数。
//
// 字段名对账 LSP 3.17 规范与 TS 侧的解析。
type publishDiagnosticsParams struct {
	URI         string          `json:"uri"`
	Diagnostics []LspDiagnostic `json:"diagnostics"`
}

// handlePublishDiagnostics 处理服务端推送的诊断。
//
// 对账 TS 的 `rpc.onNotification('textDocument/publishDiagnostics', ...)`。
//
// **解析失败静默丢弃**（对账 TS：诊断是 best-effort，不该因一条坏通知
// 打断整个会话）。但**空列表也要存**——见 `diagCache.has` 的说明。
func (m *manager) handlePublishDiagnostics(raw json.RawMessage) {
	var p publishDiagnosticsParams
	if err := json.Unmarshal(raw, &p); err != nil || p.URI == "" {
		return
	}
	if m.diags == nil {
		return
	}
	m.diags.set(p.URI, p.Diagnostics)
}
