package lsp

import (
	"encoding/json"
	"time"
)

// 本文件对账 TS `manager.ts:316-360` 的 `getFileDiagnostics` 主体。
//
// # 三件套（缺一不可）
//
//  1. **清缓存**（在触发 didChange 之前——否则与服务端推送竞态）
//  2. **触发** `textDocument/didChange`（让 server 重新分析并推送）
//  3. **等推送到达**（轮询 `diags.has(uri)`，有界超时）
//
// TS 侧还有第 0 步「pull 模型优先」（LSP 3.17+ 的 `textDocument/diagnostic`
// 请求）——Go 侧**暂不实现**，理由见下方 `getFileDiagnostics` 的注释。

const (
	// diagPollIntervalMS 是等待推送的轮询间隔。
	//
	// 对账 TS `manager.ts:358` 的 `setTimeout(check, 50)`。
	// 50ms 是「够快不空转」的折中——server 通常几十毫秒内推送。
	diagPollIntervalMS = 50
)

// getFileDiagnostics 取某文件的诊断（**已规范化的方法名**：TS 是
// `getFileDiagnostics(filePath, timeoutMs = 2000)`）。
//
// 对账 TS `manager.ts:316`。默认超时 2000ms（对账 TS 的默认值）。
//
// # 与 TS 的两处有意偏离
//
//  1. **不实现 pull 模型**（TS 的 LSP 3.17+ `textDocument/diagnostic` 优先
//     路径）。Go 侧只走 push 路径（didChange + 等 publishDiagnostics）。
//     理由：pull 模型要求 server 声明 `diagnosticProvider` 能力，而
//     Go 侧目前只对接 gopls（其 push 路径可靠）；实现 pull 会引入一条
//     当前无法验证的路径（**无 gopls 环境**）。留待需要时补。
//
//  2. **超时后返回已收到的**（而非空）。TS 的 `return diagnosticCache.get(uri) ?? []`
//     与 Go 一致——超时时若已收到过推送就返回它（哪怕只是部分）。
//     仅「从未收到」才返回 nil。
//
// # 为什么等待循环用 `has` 而非 `len > 0`
//
// server 对**无问题的文件**会推送**空数组**——那是「已检查、无问题」的
// 确切答案。若用 `len > 0` 判断，干净文件的等待会一直空转到超时（白白
// 拖慢每次编辑）。故必须区分「空列表」与「未收到」（见 `diagCache.has`）。
func (m *manager) getFileDiagnostics(filePath string, timeoutMS int) []LspDiagnostic {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	opened := m.openedDocs[absFromCwd(filePath, m.cwd)]
	m.mu.Unlock()
	if rpc == nil || !ready {
		return nil
	}

	uri := uriForPath(absFromCwd(filePath, m.cwd))

	// 文档未打开 → 先打开（didOpen 会让 server 开始分析并推送）。
	// 对账 TS `ensureDocument(filePath)`。
	if !opened {
		m.ensureDocument(filePath)
	}

	// ── ① 清缓存（**必须在触发之前**）──
	// 对账 TS `manager.ts:352` 的 `diagnosticCache.delete(uri)`，
	// 注释逐字：「Clear stale cache BEFORE notify — avoid racing server
	// publishDiagnostics」。
	m.diags.delete(uri)

	// ── ② 触发 didChange（携带**真实文件内容**）──
	//
	// 对账 TS `manager.ts:354-357` 的注释：
	// 「Read actual file content — empty text would tell tsserver the file is
	// empty (false green)」——**这是关键**：若发空内容，server 会认为文件
	// 是空的、报无诊断，于是我们拿到「假绿」。
	m.notifyDidChange(rpc, filePath, uri)

	// ── ③ 等推送到达（有界）──
	timeout := timeoutMS
	if timeout <= 0 {
		timeout = DefaultDiagnosticTimeoutMS
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		if m.diags.has(uri) {
			return m.diags.get(uri)
		}
		if !time.Now().Before(deadline) {
			// 超时：返回 nil（从未收到）——对账 TS 的 `?? []`
			return m.diags.get(uri)
		}
		time.Sleep(diagPollIntervalMS * time.Millisecond)
	}
}

// notifyDidChange 发送 didChange 通知（携带磁盘上的真实内容）。
//
// 抽成独立方法是为了让 `ChangeFile`（无等待）与 `getFileDiagnostics`
// （有等待）**共用同一段触发逻辑**——两者的 didChange 载荷必须完全一致，
// 否则 server 看到的文档版本会漂移。
//
// 对账 TS `manager.ts:354-357`。
func (m *manager) notifyDidChange(rpc *RPC, filePath, uri string) {
	text, ok := readDocumentText(absFromCwd(filePath, m.cwd))
	if !ok {
		// 文件不在磁盘上（如刚被删）→ 用空文本，让 server 清掉陈旧诊断。
		// 对账 TS：`// File may not exist on disk — use empty text as last resort`
		text = ""
	}
	params := map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": time.Now().UnixMilli()},
		"contentChanges": []any{map[string]any{"text": text}},
	}
	raw, _ := json.Marshal(params)
	if m.onDidChange != nil {
		m.onDidChange(raw)
	}
	_ = rpc.Notify("textDocument/didChange", params)
}

// DefaultDiagnosticTimeoutMS 是取诊断的默认超时（对账 TS 的 `= 2000`）。
const DefaultDiagnosticTimeoutMS = 2000
