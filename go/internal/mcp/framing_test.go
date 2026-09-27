package mcp

import (
	"bytes"
	"strings"
	"testing"
)

// framing_test.go —— 换行分隔帧编解码（V1/V2）。
//
// # 为什么 MCP 不是 LSP 的分帧
//
// MCP 规范（stdio 传输）原文：
//
//	Messages are delimited by newlines, and MUST NOT contain embedded newlines.
//
// 而 LSP（`go/internal/lsp/rpc.go`）用 `Content-Length: N\r\n\r\n` + body。
// **两者不兼容**——故本文件是**新实现**，不是复用 LSP 的编解码。
// （复用的是**架构模式**：Transport 抽象 / pending map / AbortAllPending。）

func TestEncodeLineHasExactlyOneNewline(t *testing.T) {
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}

	got, err := EncodeLine(msg)
	if err != nil {
		t.Fatalf("EncodeLine 报错：%v", err)
	}
	if n := bytes.Count(got, []byte("\n")); n != 1 {
		t.Errorf("一帧必须恰含一个换行（MCP 规范），实得 %d：%q", n, got)
	}
	if !bytes.HasSuffix(got, []byte("\n")) {
		t.Errorf("帧应以换行结尾：%q", got)
	}
}

// TestEncodeLineSingleFrameWithNewlinesInPayload —— ★ 载荷含换行时仍是单帧。
//
// **为什么关键**：规范说 MUST NOT contain embedded newlines。若一帧被拆成两帧，
// 对端会解析出半个 JSON → 静默丢弃或协议错乱。
//
// # 实测结论（探针推翻了本测试的初版前提）
//
// 初版我以为需要「显式压平换行」，并据此写断言。`go run` 探针实测证明
// `json.Marshal` **在所有路径都不产裸换行**：
//
//	map+string      → `{"text":"l1\nl2"}`（值内换行被转义）裸\n=0
//	json.RawMessage → `{"a":1,"b":2}`（被 compact）裸\n=0
//	嵌套 RawMessage → 同上
//
// 故压平是**死代码**（M1 变异「去掉压平」→ 测试全绿 = 等价变异）。
// 现在的实现改为**断言**（若 Marshal 行为变更则显式报错），本测试守住
// 「产出单帧」这一可观察契约——它仍有判别力（守卫断言被移除时会红，见 M3'）。
func TestEncodeLineSingleFrameWithNewlinesInPayload(t *testing.T) {
	msg := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		// 正文含换行的字符串载荷
		"params": map[string]any{"text": "line1\nline2\r\nline3"},
	}

	got, err := EncodeLine(msg)
	if err != nil {
		t.Fatalf("EncodeLine 报错：%v", err)
	}
	if n := bytes.Count(got, []byte("\n")); n != 1 {
		t.Errorf("含换行载荷也必须压平成单帧，实得 %d 个换行：%q", n, got)
	}
	if bytes.Contains(got, []byte("\r")) {
		t.Errorf("帧内不该有裸回车：%q", got)
	}
	// 压平后仍是合法 JSON（内容以转义形式保留）
	msgs, rest := DecodeLines(got)
	if len(msgs) != 1 || len(rest) != 0 {
		t.Fatalf("压平后的帧应能解回一条：msgs=%d rest=%q", len(msgs), rest)
	}
}

// TestDecodeLinesSplitAtEveryOffset —— ★ 跨 chunk 边界穷举。
//
// 生产里 stdin 的 chunk 边界落在**任意**字节位置（含多字节字符内部）。
// LSP 侧曾因「按 string 缓冲」在切点处把多字节字符转成 U+FFFD 而静默丢帧
// （见 lsp/rpc.go 的说明）。本测试穷举所有切点，钉住「字节缓冲」这一前提。
func TestDecodeLinesSplitAtEveryOffset(t *testing.T) {
	// 含中文（多字节）的载荷
	body := []byte(`{"jsonrpc":"2.0","id":7,"result":{"text":"中文载荷 ✓"}}` + "\n")

	for cut := 0; cut <= len(body); cut++ {
		a := body[:cut]
		b := body[cut:]

		// 第一块：可能不足一帧
		msgs1, rest1 := DecodeLines(a)

		var all [][]byte
		all = append(all, msgs1...)

		// 第二块：把 rest 拼上
		msgs2, rest2 := DecodeLines(append(append([]byte(nil), rest1...), b...))
		all = append(all, msgs2...)

		if len(all) != 1 {
			t.Fatalf("切点 %d：应恰好解出 1 条，实得 %d（rest1=%q rest2=%q）", cut, len(all), rest1, rest2)
		}
		if len(rest2) != 0 {
			t.Errorf("切点 %d：解完后不该有残留：%q", cut, rest2)
		}
		if !strings.Contains(string(all[0]), "中文载荷") {
			t.Errorf("切点 %d：多字节内容被破坏：%q", cut, all[0])
		}
	}
}

// TestDecodeLinesKeepsPartialFrameAsRest —— 不足一帧时保留缓冲。
func TestDecodeLinesKeepsPartialFrameAsRest(t *testing.T) {
	partial := []byte(`{"jsonrpc":"2.0","id":1,"met`)

	msgs, rest := DecodeLines(partial)
	if len(msgs) != 0 {
		t.Errorf("不足一帧不该产出消息，实得 %d", len(msgs))
	}
	if !bytes.Equal(rest, partial) {
		t.Errorf("应原样保留缓冲：got %q want %q", rest, partial)
	}
}

// TestDecodeLinesSkipsMalformedButContinues —— ★ V2：坏行跳过、不阻断后续。
//
// 对账 TS `failure-classifier` 之外的另一处 catch 语义：LSP 侧
// `DecodeMessages` 对 JSON 解析失败也是「跳过该帧」（见 rpc.go 注释）。
// MCP 侧同理——一条坏帧不该让整条连接失效。
func TestDecodeLinesSkipsMalformedButContinues(t *testing.T) {
	buf := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
		`{not valid json` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n")

	msgs, rest := DecodeLines(buf)

	if len(msgs) != 2 {
		t.Fatalf("坏行应被跳过而两条好帧保留，实得 %d：%v", len(msgs), msgs)
	}
	if !strings.Contains(string(msgs[0]), `"id":1`) || !strings.Contains(string(msgs[1]), `"id":2`) {
		t.Errorf("两条好帧应按序保留：%q / %q", msgs[0], msgs[1])
	}
	if len(rest) != 0 {
		t.Errorf("不应有残留：%q", rest)
	}
}

// TestDecodeLinesSkipsEmptyLines —— 空行跳过（防御对端的多余换行）。
func TestDecodeLinesSkipsEmptyLines(t *testing.T) {
	buf := []byte("\n\n" + `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n\n")

	msgs, rest := DecodeLines(buf)
	if len(msgs) != 1 {
		t.Errorf("空行应跳过，实得 %d 条：%v", len(msgs), msgs)
	}
	if len(rest) != 0 {
		t.Errorf("不应有残留：%q", rest)
	}
}

// TestDecodeLinesMultipleFramesInOneChunk —— 一块含多帧。
func TestDecodeLinesMultipleFramesInOneChunk(t *testing.T) {
	buf := []byte(`{"id":1}` + "\n" + `{"id":2}` + "\n" + `{"id":3}` + "\n")

	msgs, rest := DecodeLines(buf)
	if len(msgs) != 3 {
		t.Fatalf("应解出 3 条，实得 %d", len(msgs))
	}
	if len(rest) != 0 {
		t.Errorf("不应有残留：%q", rest)
	}
}
