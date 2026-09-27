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
	// ★ 键必须与 `ensureDocument` / `ChangeFile` 一致——**全都是 URI**。
	//
	// 曾经此处用绝对路径作键（`openedDocs[absFromCwd(...)]`），而
	// `ensureDocument` 用 URI 作键 → **永远读到 false**（Go 的 map 读缺失键
	// 返回零值且不报错）→ 每次都走「刚打开」分支 → 内容变了也不重新触发。
	//
	// # 真不变量（订正自审查 C2）
	//
	// 原文注释称两个 URI 构造函数「在 macOS 下多半产出相同字符串」——
	// **不准确**：`uriForPath(absPath)` 与 `uriForFile(fp, cwd)` 的实现
	// **逐字等价**（都是 `ToSlash` + 补前导 `/` + `url.URL{Scheme:"file"}`），
	// 传入绝对路径时**恒等**。
	//
	// 真正的不变量是：**本文件涉及的所有 map 键都必须是 URI**
	// （`openedDocs` / `diags` / `lastSentText` 三张表同键空间）。
	// 用错键**不会报错**，只会静默 miss——故这里显式写明。
	fileURI := uriForFile(filePath, m.cwd)
	m.mu.Lock()
	rpc := m.rpc
	ready := m.ready
	opened := m.openedDocs[fileURI]
	m.mu.Unlock()
	if rpc == nil || !ready {
		return nil
	}

	// ★ 必须与 `ensureDocument` / `ChangeFile` 用**同一个** URI 构造函数。
	//
	// 曾经此处用 `uriForPath(绝对路径)` 而 `ensureDocument` 用
	// `uriForFile(路径, cwd)` —— 两者虽在 macOS 下多半产出相同字符串，
	// 但只要有一处差异，`lastSentText[uri]` 与 `diags` 的键就会**对不上**：
	// `ensureDocument` 写的键读不到，于是每次都判「内容变了」→ 清缓存 →
	// 又因 `justOpened` 不发 didChange → **拿不到诊断**。
	// 统一到一个函数是唯一稳妥的做法。
	uri := uriForFile(filePath, m.cwd)

	// 先读当前文本（后续「是否变了」与「发什么」都用它）。
	text, ok := readDocumentText(absFromCwd(filePath, m.cwd))
	if !ok {
		text = "" // 文件不在磁盘上 → 空文本，让 server 清陈旧诊断
	}

	// 文档未打开 → 先打开（didOpen 会让 server 开始分析并推送）。
	// 对账 TS `ensureDocument(filePath)`。
	//
	// ★ **didOpen 之后就等它推送**，不叠 didChange：didOpen 已把内容告知
	// server（`ensureDocument` 会记录 `lastSentText`），再发同内容 didChange
	// 对 gopls 是空操作。
	//
	// ⚠️ **不要在这里 `return`**（曾经这么写，造成缺陷）：那会跳过下面的
	// 「内容变了要重新触发」判断——于是「首次 didOpen → 用户又改一次 →
	// 再取诊断」会**永远返回第一次的陈旧诊断**。
	// 正确做法是让 didOpen 只负责「打开」，是否/如何触发统一由下方决定。
	justOpened := false
	if !opened {
		m.ensureDocument(filePath)
		justOpened = true
	}

	// ── ① 判断内容是否真的变了（**真实 gopls 验证出的必需判据**）──
	//
	// ★ 与 TS 有**有意偏离**，理由是实测：gopls 对**内容未变**的
	// `didChange` **不重新分析、不推送**（探针实测：didOpen 推送后，
	// 同内容 didChange 5s 内零推送）。
	//
	// TS 的做法是「无条件 delete + didChange」，在 tsserver 下可行；
	// 在 gopls 下会导致：首次 didOpen 的推送被自己清掉、同内容 didChange
	// 又不触发 → **缓存清了却填不回来**（实测三次调用各等满 10s 全空）。
	//
	// 故：**内容未变且有缓存 → 直接用**；否则清缓存 + 重新触发。
	m.mu.Lock()
	prev, seenBefore := m.lastSentText[uri]
	m.mu.Unlock()

	unchanged := seenBefore && prev == text
	if unchanged && m.diags.has(uri) {
		return m.diags.get(uri)
	}
	// 刚 didOpen（ensureDocument 已记录 lastSentText）→ 它自己会触发推送，
	// 不必再发 didChange。
	if justOpened {
		return waitForDiagnostics(m, uri, timeoutMS)
	}

	// ── ② 清缓存（**必须在触发之前**）──
	// 对账 TS `manager.ts:352`：「Clear stale cache BEFORE notify — avoid
	// racing server publishDiagnostics」（顺序反了会自己清掉刚到的推送）。
	m.diags.delete(uri)

	// ── ③ 触发 didChange（携带**真实文件内容**）──
	//
	// 对账 TS `manager.ts:354-357`：「Read actual file content — empty text
	// would tell tsserver the file is empty (false green)」——**关键**：
	// 若发空内容，server 会认为文件是空的、报无诊断，于是拿到「假绿」。
	//
	// `lastSentText` 的同步在 `notifyDidChangeWithText` 内部完成（收口）。
	m.notifyDidChangeWithText(rpc, uri, text)

	// ── ④ 等推送到达（有界）──
	return waitForDiagnostics(m, uri, timeoutMS)
}

// waitForDiagnostics 轮询等待某 uri 的推送到达（有界）。
//
// 抽成函数是因为**两条触发路径共用它**（didOpen 路径 / didChange 路径）——
// 等待语义必须一致，否则出现「一条路径立刻返回、另一条等满超时」的偏差。
func waitForDiagnostics(m *manager, uri string, timeoutMS int) []LspDiagnostic {
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

// recordSentTextAndInvalidateDiags 记录「已发给 server 的文本」**并失效该 uri 的旧诊断**。
//
// # ★ 为什么必须成对（第一百零五刀 CRITICAL #1）
//
// `lastSentText[uri]` 与 `diags[uri]` 必须描述**同一份内容**：
//
//   - 只更新前者（W1 的 `ChangeFile` 就是这么错的）→ 后者被当成「新内容的
//     诊断」而复用 → **返回陈旧诊断**。用户可见形态：第二次编辑后诊断
//     滞后一轮，且是**确定性**的（不是竞态）。
//   - 只清后者 → 前者判「内容未变」→ 跳过必要的重触发
//
// 故凡「把新内容发给 server」的路径都必须调本函数，而非裸的 `recordSentText`。
//
// **两个状态各自有锁**（`m.mu` 与 `diagCache` 的内部锁），故分两步、不嵌套。
func (m *manager) recordSentTextAndInvalidateDiags(uri, text string) {
	m.recordSentText(uri, text)
	// 诊断在锁外清（diagCache 自带锁，避免锁嵌套）。
	m.diags.delete(uri)
}

// recordSentText 记录「已发给 server 的文档文本」（按 URI）。
//
// # ★ 核心不变量（第一百零四刀）
//
//	对任一 uri，`lastSentText[uri]` 必须恒等于「server 缓冲中该文档的内容」。
//
// **所有改变 server 文档缓冲的路径都必须经此函数**（当前有两条：
// `ensureDocument` 的 didOpen、`notifyDidChangeWithText` 的 didChange；
// 后者又被 `ChangeFile` 与 `getFileDiagnostics` 共用）。
//
// # 违反它会怎样
//
// `getFileDiagnostics` 用 `lastSentText[uri] == 当前磁盘文本` 判断
// 「是否需重新触发」：
//
//   - 表**落后**于 server（本刀修的缺陷）→ 误判「内容变了」→ 清掉刚推来的
//     有效缓存 → 而重复 didChange 对 gopls 是空操作 → **拿不到诊断**
//   - 表**超前**于 server → 误判「未变」→ 用陈旧缓存 → 返回过期诊断
//
// 两种都是「不报错但结果错」。故不变量由**结构**（单一入口）保证，
// 而非依赖每个调用点「记得写」。
//
// ⚠️ **调用方不得已持有 `m.mu`**：本函数自己加锁，而 Go 的 `sync.Mutex`
// **不可重入**——锁内调它会**自锁死**（本刀实测过一次）。
// 需要在锁内更新时，用内联赋值（见 `ensureDocument`）。
func (m *manager) recordSentText(uri, text string) {
	m.mu.Lock()
	if m.lastSentText != nil {
		m.lastSentText[uri] = text
	}
	m.mu.Unlock()
}

// notifyDidChangeWithText 发送 didChange（文本由调用方给定）并同步判据状态。
//
// ★ **这是所有「改变 server 文档缓冲」路径的唯一收口点**——它内部调
// `recordSentText`，故调用方无需记得更新 `lastSentText`。
//
// # 为什么必须收口（第一百零四刀的根因）
//
// `lastSentText` 必须恒等于「server 缓冲内容」，否则 `getFileDiagnostics`
// 的「内容是否变了」判据会误判。此前有三条路径改 server 缓冲，只有两条
// 更新该表——`ChangeFile` 漏了，于是留下残余静默失效（写工具改盘 →
// ChangeFile 发新内容 → `lastSentText` 仍旧 → 取诊断时误清刚推来的缓存）。
// 把同步放进本函数，让不变量由**结构**保证，而非靠「记得写」。
//
// `text` 作为参数传（而非内部读盘）是为了避免 TOCTOU：调用方需要先读一次
// 用于「内容是否变了」的判断，再发同一个文本——分两次读会有缝隙。
func (m *manager) notifyDidChangeWithText(rpc *RPC, uri, text string) {
	// ★ 成对更新：文本与诊断必须同步（见 recordSentTextAndInvalidateDiags）。
	m.recordSentTextAndInvalidateDiags(uri, text)
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
