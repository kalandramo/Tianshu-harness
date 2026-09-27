// Package mcp 实现 MCP（Model Context Protocol）客户端——stdio 传输。
//
// # 对账对象
//
// TS 侧 `src/mcp/`（13 文件 / 2,418 行）。本包是**最小可运行子集**：
// 连接 → 发现工具 → 调用工具。**不含**重连退避与健康检查（见 §范围收窄）。
//
// # 为什么不能复用 internal/lsp 的分帧
//
// MCP 规范（stdio）原文：
//
//	Messages are delimited by newlines, and MUST NOT contain embedded newlines.
//
// 而 LSP 用 `Content-Length: N\r\n\r\n` + body（见 `internal/lsp/rpc.go`）。
// **两者不兼容**。复用 LSP 的**架构模式**（Transport 抽象 / pending map /
// AbortAllPending）是合理的；复用它的**编解码**则会写出一套永远握不上手的实现。
//
// # 范围收窄（诚实标注）
//
//   - 只做 **stdio** 传输。`streamableHttp` / `sse-legacy` 未做——
//     Go 侧 SSE 解析器（`internal/api/sse`）是 OpenAI 专用（与 `contract.Usage`
//     耦合），非通用帧解析；另建成本高，且 HTTP 传输本机难做端到端验收。
//   - 不含**指数退避重连**与 **health-check 后台探测**（TS 侧在
//     `manager.ts` / `health-check.ts`）——属独立一刀。
//   - 不含 subAgent workspace 策略（Go 侧无 subAgent 体系）。
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// EncodeLine 把一条 JSON-RPC 消息封成一帧。
//
// 对账 MCP 规范（stdio 传输）："Messages are delimited by newlines, and
// MUST NOT contain embedded newlines." —— 故是「紧凑 JSON + \n」，**不是**
// LSP 的 `Content-Length` 帧。
//
// # ★ 为什么不是「显式压平换行」（探针推翻了初版设计）
//
// 初版我写了 `bytes.ReplaceAll(body, "\n", nil)` 显式压平。**`go run` 探针实测**
// 证明那是**死代码**——`json.Marshal` 在所有路径下都不产裸换行：
//
//	map+string      → `{"text":"l1\nl2"}`  （值内换行被转义成 \\n）裸\n=0
//	json.RawMessage → `{"a":1,"b":2}`      （**被 compact 掉了**）裸\n=0
//	嵌套 RawMessage → `{"params":{"x":1}}` 裸\n=0
//
// 故压平永不生效（M1 变异「去掉压平」→ 测试全绿 = 等价变异，非覆盖缺口）。
// 保留死代码会**掩盖**「Marshal 已保证」这一事实。改为**断言**：若未来
// Marshal 的行为变了（或有人换成不 compact 的序列化器），这里显式报错，
// 而不是静默产出会把一帧拆成两帧的坏帧。
func EncodeLine(msg any) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	// 防御性断言（非压平——见上方说明）。
	if i := bytes.IndexAny(body, "\r\n"); i >= 0 {
		return nil, fmt.Errorf("mcp: json.Marshal 产出了含裸换行的正文（offset %d）—— "+
			"MCP stdio 规范禁止 embedded newlines；这是序列化器行为变更，需显式处理", i)
	}
	out := make([]byte, 0, len(body)+1)
	out = append(out, body...)
	out = append(out, '\n')
	return out, nil
}

// DecodeLines 从累积缓冲中切出完整帧，返回（消息列表, 剩余缓冲）。
//
// 语义（对账 MCP 规范的换行分隔 + LSP 侧同款容错）：
//
//   - 按 '\n' 切分；**最后一段无换行结尾 → 留作 rest**（帧未收全）
//   - 空行跳过
//   - 非法 JSON **跳过该行但不阻断后续**（对端发坏帧不该让整条连接失效）
//
// **为什么按字节而非 string**：stdin 的 chunk 边界落在任意字节位置，
// 包括 UTF-8 多字节字符内部。LSP 侧曾因按 string 缓冲在切点处产生 U+FFFD
// 而静默丢帧（见 `internal/lsp/rpc.go` 的 `DecodeMessages` 说明）。
// 本实现全程 `[]byte`，从根上消除该问题。
func DecodeLines(buf []byte) (msgs [][]byte, rest []byte) {
	// 逐行扫描；只消费到最后一个换行为止
	start := 0
	for {
		idx := bytes.IndexByte(buf[start:], '\n')
		if idx == -1 {
			break // 余下无换行 —— 未收全，留 rest
		}
		lineEnd := start + idx
		line := buf[start:lineEnd]
		start = lineEnd + 1

		if len(bytes.TrimSpace(line)) == 0 {
			continue // 空行跳过
		}
		if !json.Valid(line) {
			continue // 坏行跳过，不阻断后续
		}
		msgs = append(msgs, append([]byte(nil), line...))
	}
	// 剩余缓冲按字节原样保留（绝不经 string 转换）
	rest = append([]byte(nil), buf[start:]...)
	return msgs, rest
}
