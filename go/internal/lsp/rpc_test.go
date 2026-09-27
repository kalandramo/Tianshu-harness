package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── 测试替身：可精确控制的双向传输 ─────────────────────────────────
//
// memTransport 让测试能**按任意边界**喂入字节（模拟 stdio 的任意分片），
// 并收集出站字节。Read 在无数据时阻塞（模拟活着的连接），Close 后返回 EOF。
type memTransport struct {
	mu     sync.Mutex
	cond   *sync.Cond
	inbuf  []byte
	outbuf bytes.Buffer
	closed bool
}

func newMemTransport() *memTransport {
	m := &memTransport{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *memTransport) Read(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.inbuf) == 0 && !m.closed {
		m.cond.Wait()
	}
	if len(m.inbuf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, m.inbuf)
	m.inbuf = m.inbuf[n:]
	return n, nil
}

func (m *memTransport) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.outbuf.Write(p)
}

// Feed 追加入站字节（模拟 server 写入一段数据）。
func (m *memTransport) Feed(data []byte) {
	m.mu.Lock()
	m.inbuf = append(m.inbuf, data...)
	m.mu.Unlock()
	m.cond.Broadcast()
}

// OutBytes 返回已写出的全部字节。
func (m *memTransport) OutBytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.outbuf.Bytes()...)
}

func (m *memTransport) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.cond.Broadcast()
	return nil
}

// outMessages 解析出站帧为 JSON 对象列表。
func (m *memTransport) outMessages(t *testing.T) []map[string]any {
	t.Helper()
	raw, _ := DecodeMessages(m.OutBytes())
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("出站帧不是合法 JSON：%v（原文 %s）", err, b)
		}
		out = append(out, v)
	}
	return out
}

// waitFor 轮询等待条件成立（避免固定 sleep 造成的时序脆弱）。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待超时（%v）：%s", timeout, what)
}

// ── 帧编码 ────────────────────────────────────────────────────────

// TestEncodeMessage_FrameBytes —— 帧格式逐字节对账 TS `encodeMessage`。
//
// TS：`Content-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`
// **长度按字节**（Buffer.byteLength），不是字符数。
func TestEncodeMessage_FrameBytes(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	got := EncodeMessage(body)
	want := "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + string(body)
	if string(got) != want {
		t.Errorf("帧字节不符：\n want %q\n  got %q", want, got)
	}
}

// TestEncodeMessage_LengthIsBytesNotRunes —— ★ 中文正文按**字节**计长。
//
// 这是最容易写错的一处：`len(string)` 是字节（Go 字符串底层是字节），
// 但若实现里用 `utf8.RuneCount` 或 `[]rune` 就会错。
// 该断言独立成立，不依赖其它测试。
func TestEncodeMessage_LengthIsBytesNotRunes(t *testing.T) {
	body := []byte(`{"s":"中文定义位置"}`)
	got := string(EncodeMessage(body))

	// 正文里有 6 个中文字符（18 字节）。头里的数字必须是字节数。
	runeCount := 0
	for range string(body) {
		runeCount++
	}
	if runeCount == len(body) {
		t.Fatal("用例前提不成立：正文应含多字节字符")
	}

	wantHeader := "Content-Length: " + itoa(len(body)) + "\r\n\r\n"
	if !strings.HasPrefix(got, wantHeader) {
		t.Errorf("头应为字节长度 %d：\n want 前缀 %q\n  got %q", len(body), wantHeader, got)
	}
	if strings.Contains(got, "Content-Length: "+itoa(runeCount)+"\r\n") {
		t.Errorf("头用了**字符数** %d（应为字节数 %d）", runeCount, len(body))
	}
}

// ── 帧解码 ────────────────────────────────────────────────────────

// TestDecodeMessages_TwoFramesInOneChunk —— 粘包：一次喂两帧。
func TestDecodeMessages_TwoFramesInOneChunk(t *testing.T) {
	f1 := EncodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":1}`))
	f2 := EncodeMessage([]byte(`{"jsonrpc":"2.0","id":2,"result":2}`))
	msgs, rest := DecodeMessages(append(append([]byte{}, f1...), f2...))

	if len(msgs) != 2 {
		t.Fatalf("应解出 2 条，实得 %d", len(msgs))
	}
	if len(rest) != 0 {
		t.Errorf("无剩余，实得 %q", rest)
	}
	if string(msgs[0]) != `{"jsonrpc":"2.0","id":1,"result":1}` {
		t.Errorf("第 1 条不符：%s", msgs[0])
	}
	if string(msgs[1]) != `{"jsonrpc":"2.0","id":2,"result":2}` {
		t.Errorf("第 2 条不符：%s", msgs[1])
	}
}

// TestDecodeMessages_HalfFrameKept —— 半帧：必须返回 0 条 + **完整** rest。
func TestDecodeMessages_HalfFrameKept(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":"x"}`)
	frame := EncodeMessage(body)

	// 只喂头，不喂完正文
	headOnly := frame[:len(frame)-3]
	msgs, rest := DecodeMessages(headOnly)
	if len(msgs) != 0 {
		t.Fatalf("正文不完整时不该解出消息，实得 %d 条", len(msgs))
	}
	if !bytes.Equal(rest, headOnly) {
		t.Errorf("rest 应保留全部已收字节\n want %q\n  got %q", headOnly, rest)
	}
}

// TestDecodeMessages_MultibyteSplitAtEveryOffset —— ★★ 本波最重要的断言。
//
// 穷举**所有**切点，把一帧按字节切成两段再重组，必须还原原文。
//
// # 为什么必须穷举而非挑一个切点
//
// 真实 stdio 的 chunk 边界由内核缓冲区决定，会落在**任意**字节位置——
// 包括 UTF-8 多字节字符的内部。若解码器把累积缓冲当 `string` 处理
// （TS 的 `decodeMessages` 就返回 `rest: string`），切开多字节字符那一刀会在
// 转换处产生 U+FFFD，重组后字节已损坏 → `json.Unmarshal` 失败 →
// **该响应被静默丢弃**（TS 的 catch 就是 `// Skip malformed message`）。
//
// 后果不是报错，而是工具永远拿不到结果（或等到 45s 超时）。故本测试
// 用**穷举**把这类切点全覆盖：只要有一个切点失败即红。
func TestDecodeMessages_MultibyteSplitAtEveryOffset(t *testing.T) {
	// 正文含多字节字符，且处于 JSON 值内部——被切坏就必然解析失败。
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"uri":"file:///中文目录/文件.go","定义":"跳转"}}`)
	frame := EncodeMessage(body)

	// 前提自检：正文确实含多字节字符（否则本测试测不到东西）
	if len(body) == len([]rune(string(body))) {
		t.Fatal("用例前提不成立：正文应含多字节字符")
	}

	failed := 0
	for cut := 1; cut < len(frame); cut++ {
		first, rest1 := DecodeMessages(frame[:cut])
		if len(first) != 0 {
			t.Fatalf("cut=%d：正文未收全却解出了 %d 条", cut, len(first))
		}

		// ★ 关键：rest 必须是**原始字节**，且拼上剩余后逐字节等于原帧
		combined := append(append([]byte{}, rest1...), frame[cut:]...)
		if !bytes.Equal(combined, frame) {
			t.Fatalf("cut=%d：重组后的字节与原帧不等（rest 在转换中损坏了多字节字符）", cut)
		}

		msgs, _ := DecodeMessages(combined)
		if len(msgs) != 1 {
			failed++
			t.Errorf("cut=%d：重组后应解出 1 条，实得 %d 条（多字节字符被切坏→静默丢弃）", cut, len(msgs))
			continue
		}
		if !bytes.Equal(msgs[0], body) {
			t.Errorf("cut=%d：解出的正文与原正文不等\n want %s\n  got %s", cut, body, msgs[0])
		}
	}
	if failed > 0 {
		t.Logf("共 %d 个切点失败——说明解码器不是按字节缓冲", failed)
	}
}

// TestDecodeMessages_MalformedHeaderSkipped —— 无 Content-Length 的段被跳过
// （对账 TS：`if (!lengthMatch) { offset = headerEnd + 4; continue }`）。
func TestDecodeMessages_MalformedHeaderSkipped(t *testing.T) {
	junk := []byte("X-Unknown: 1\r\n\r\n")
	good := EncodeMessage([]byte(`{"jsonrpc":"2.0","id":7,"result":"ok"}`))

	msgs, rest := DecodeMessages(append(append([]byte{}, junk...), good...))
	if len(msgs) != 1 {
		t.Fatalf("应跳过坏段后解出 1 条，实得 %d", len(msgs))
	}
	if !strings.Contains(string(msgs[0]), `"id":7`) {
		t.Errorf("解出的不是好帧：%s", msgs[0])
	}
	if len(rest) != 0 {
		t.Errorf("无剩余，实得 %q", rest)
	}
}

// TestDecodeMessages_NoHeaderAtAll —— 一个字节都没有时不该 panic。
func TestDecodeMessages_NoHeaderAtAll(t *testing.T) {
	msgs, rest := DecodeMessages([]byte(`{"partial":`))
	if len(msgs) != 0 {
		t.Fatalf("应 0 条，实得 %d", len(msgs))
	}
	if string(rest) != `{"partial":` {
		t.Errorf("rest 应原样保留，实得 %q", rest)
	}
}

// ── RPC 行为 ──────────────────────────────────────────────────────

// TestRPC_RequestIDMonotonicFromOne —— 对账 TS：`let nextId = 1`（从 1 起）。
func TestRPC_RequestIDMonotonicFromOne(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		go func() { _, _ = r.Request(ctx, "m", nil, 200*time.Millisecond) }()
	}

	waitFor(t, time.Second, "写出 3 个请求", func() bool {
		msgs := tr.outMessages(t)
		return len(msgs) >= 3
	})
	msgs := tr.outMessages(t)
	for i, m := range msgs {
		if got := m["id"]; got != float64(i+1) {
			t.Errorf("第 %d 个请求 id 应为 %d，实得 %v", i+1, i+1, got)
		}
	}
}

// TestRPC_TimeoutRemovesPendingEntry —— ★ 超时必须从待决表删除（否则泄漏）。
//
// TS 的 setTimeout 分支：`if (pending.delete(id)) reject(...)`——**先删再拒**。
// 若只 reject 不删，待决表会随超时请求无限增长；且迟到响应会去 settle
// 一个已超时的条目（二次 settle）。
func TestRPC_TimeoutRemovesPendingEntry(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	_, err := r.Request(context.Background(), "textDocument/definition", nil, 40*time.Millisecond)
	if err == nil {
		t.Fatal("超时应返回错误")
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("超时错误文案应含 'timed out after'，实得 %q", err)
	}
	if n := r.PendingCount(); n != 0 {
		t.Errorf("超时后待决表应清空，实得 %d 条（泄漏）", n)
	}
}

// TestRPC_TimeoutErrorTextMatchesTS —— 文案逐字对账。
//
// TS：`LSP request ${method} timed out after ${effectiveTimeout / 1000}s`
// 秒数用浮点除法——45000ms → "45"，50ms → "0.05"。
func TestRPC_TimeoutErrorTextMatchesTS(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	_, err := r.Request(context.Background(), "initialize", nil, 50*time.Millisecond)
	if err == nil {
		t.Fatal("超时应返回错误")
	}
	want := "LSP request initialize timed out after 0.05s"
	if err.Error() != want {
		t.Errorf("文案不符\n want %q\n  got %q", want, err.Error())
	}
}

// TestRPC_ResponseResolvesByID —— 正常响应按 id 匹配并 resolve。
func TestRPC_ResponseResolvesByID(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	type result struct {
		raw json.RawMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := r.Request(context.Background(), "initialize", nil, time.Second)
		ch <- result{raw, err}
	}()

	waitFor(t, time.Second, "请求写出", func() bool { return len(tr.outMessages(t)) >= 1 })
	tr.Feed(EncodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"definitionProvider":true}}}`)))

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("不该报错：%v", res.err)
		}
		if !strings.Contains(string(res.raw), `"definitionProvider":true`) {
			t.Errorf("result 不符：%s", res.raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待响应超时")
	}
	if n := r.PendingCount(); n != 0 {
		t.Errorf("settle 后待决表应清空，实得 %d", n)
	}
}

// TestRPC_ErrorResponseUsesMessageOnly —— 对账 TS：error 分支只取 `message`
// （丢弃 code / data）。
func TestRPC_ErrorResponseUsesMessageOnly(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	ch := make(chan error, 1)
	go func() {
		_, err := r.Request(context.Background(), "textDocument/definition", nil, time.Second)
		ch <- err
	}()

	waitFor(t, time.Second, "请求写出", func() bool { return len(tr.outMessages(t)) >= 1 })
	tr.Feed(EncodeMessage([]byte(
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"参数不合法","data":{"x":1}}}`)))

	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("应返回错误")
		}
		if err.Error() != "参数不合法" {
			t.Errorf("应只取 message（不带 code/data），实得 %q", err.Error())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待错误超时")
	}
}

// TestRPC_AbortAllPendingRejectsEveryone —— transport 断开时全量 reject。
//
// 对账 TS `abortAllPending` + `transportDead`：进程/管道死亡不得让调用方
// 永远等待（2026-09-08 wedge 事故的修法）。
func TestRPC_AbortAllPendingRejectsEveryone(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := r.Request(context.Background(), "slow", nil, 30*time.Second)
			errs <- err
		}()
	}
	waitFor(t, time.Second, "4 个请求进入待决表", func() bool { return r.PendingCount() == n })

	r.AbortAllPending(io.ErrUnexpectedEOF)

	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("被 abort 的请求必须拿到错误")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 个请求未被 reject（调用方会永远等待）", i+1)
		}
	}
	if n := r.PendingCount(); n != 0 {
		t.Errorf("abort 后待决表应清空，实得 %d", n)
	}
}

// TestRPC_TransportCloseRejectsPending —— 读端 EOF 也应触发全量 reject。
func TestRPC_TransportCloseRejectsPending(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	ch := make(chan error, 1)
	go func() {
		_, err := r.Request(context.Background(), "slow", nil, 30*time.Second)
		ch <- err
	}()
	waitFor(t, time.Second, "请求进入待决表", func() bool { return r.PendingCount() == 1 })

	_ = tr.Close()

	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("transport 关闭后应 reject")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transport 关闭未 reject 待决请求")
	}
}

// TestRPC_ServerToClientRequestGetsMethodNotFound —— ★ 决策 B（显式偏离 TS）。
//
// 对账 TS `rpc.ts` 的分派：只有三分支（id+result / id+error / method 无 id）。
// **server→client 请求（method 与 id 同现）无分支处理，被静默丢弃**——
// 而 JSON-RPC 2.0 规定请求必须得到响应。真实 server（typescript-language-server
// 启动后会发 `client/registerCapability`）拿不到答复会挂等。
//
// Go 侧改为回 `MethodNotFound`（code -32601）——协议正确，且这是**会咬人的
// 缺陷**，与本仓库「移植死代码 = 搬运缺陷」的纪律一致。
//
// 本测试的判别力：若实现退回 TS 的静默丢弃 → 出站零报文 → 必红。
func TestRPC_ServerToClientRequestGetsMethodNotFound(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	tr.Feed(EncodeMessage([]byte(
		`{"jsonrpc":"2.0","id":99,"method":"client/registerCapability","params":{}}`)))

	waitFor(t, time.Second, "应回一个错误响应", func() bool {
		for _, m := range tr.outMessages(t) {
			if m["id"] == float64(99) {
				return true
			}
		}
		return false
	})

	var got map[string]any
	for _, m := range tr.outMessages(t) {
		if m["id"] == float64(99) {
			got = m
		}
	}
	if got == nil {
		t.Fatal("未找到 id=99 的出站响应")
	}
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("出站应含 error 对象，实得 %v", got)
	}
	if errObj["code"] != float64(-32601) {
		t.Errorf("JSON-RPC MethodNotFound 的 code 应为 -32601，实得 %v", errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "client/registerCapability") {
		t.Errorf("错误消息应含方法名，实得 %q", msg)
	}
	if _, hasResult := got["result"]; hasResult {
		t.Error("错误响应不该同时带 result")
	}
}

// TestRPC_NotificationDispatchedAndNoReply —— 通知（method 无 id）：
// 交给 handler，且**不产生任何出站报文**。
func TestRPC_NotificationDispatchedAndNoReply(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	var mu sync.Mutex
	var gotParams []byte
	r.OnNotification("textDocument/publishDiagnostics", func(params json.RawMessage) {
		mu.Lock()
		gotParams = append([]byte(nil), params...)
		mu.Unlock()
	})

	tr.Feed(EncodeMessage([]byte(
		`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///a.go","diagnostics":[]}}`)))

	waitFor(t, time.Second, "通知被派发", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotParams != nil
	})

	mu.Lock()
	params := string(gotParams)
	mu.Unlock()
	if !strings.Contains(params, `"uri":"file:///a.go"`) {
		t.Errorf("handler 收到的 params 不符：%s", params)
	}

	// 通知不得产生出站报文
	time.Sleep(30 * time.Millisecond)
	if out := tr.OutBytes(); len(out) != 0 {
		t.Errorf("通知不该有出站响应，实得 %q", out)
	}
}

// TestRPC_NotifyWritesFrameWithoutID —— notify 写出的是无 id 的通知帧。
func TestRPC_NotifyWritesFrameWithoutID(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	if err := r.Notify("initialized", map[string]any{}); err != nil {
		t.Fatalf("notify 失败：%v", err)
	}
	msgs := tr.outMessages(t)
	if len(msgs) != 1 {
		t.Fatalf("应写出 1 帧，实得 %d", len(msgs))
	}
	if _, hasID := msgs[0]["id"]; hasID {
		t.Errorf("通知不该带 id，实得 %v", msgs[0])
	}
	if msgs[0]["method"] != "initialized" {
		t.Errorf("method 不符：%v", msgs[0]["method"])
	}
	if r.PendingCount() != 0 {
		t.Errorf("通知不该进待决表，实得 %d", r.PendingCount())
	}
}

// TestRPC_NonPositiveTimeoutUsesDefault —— 对账 TS：
// `Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : defaultTimeoutMs`。
//
// 传 0 / 负数必须回落到默认值，而不是「立即超时」或「永不超时」。
func TestRPC_NonPositiveTimeoutUsesDefault(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr, WithRequestTimeout(300*time.Millisecond))
	defer r.Dispose()

	done := make(chan error, 1)
	go func() {
		_, err := r.Request(context.Background(), "m", nil, 0) // 0 → 用默认
		done <- err
	}()

	waitFor(t, time.Second, "请求写出", func() bool { return len(tr.outMessages(t)) >= 1 })

	// 在默认超时（300ms）之内响应 → 必须成功
	tr.Feed(EncodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`)))
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("0 超时应回落默认值，不该立刻超时：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待超时")
	}
}

// TestRPC_NoDataRaceOnConcurrentRequests —— 并发请求 + 并发喂数据不得竞争。
//
// 配合 `go test -race` 才有意义（本仓库对 goroutine 读取的纪律）。
func TestRPC_NoDataRaceOnConcurrentRequests(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Request(context.Background(), "m", nil, 300*time.Millisecond)
		}()
	}

	waitFor(t, 2*time.Second, "8 个请求写出", func() bool { return len(tr.outMessages(t)) >= n })

	// 逐个响应（每个 id 一条）
	for i := 1; i <= n; i++ {
		tr.Feed(EncodeMessage([]byte(
			`{"jsonrpc":"2.0","id":` + itoa(i) + `,"result":"ok"}`)))
	}
	wg.Wait()

	if got := r.PendingCount(); got != 0 {
		t.Errorf("全部响应后待决表应清空，实得 %d", got)
	}
}

// TestRPC_LateResponseAfterTimeoutIsIgnored —— 超时后迟到响应不得二次 settle
// （不得 panic / 不得改状态）。
func TestRPC_LateResponseAfterTimeoutIsIgnored(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)
	defer r.Dispose()

	// 不 read 这个 error：我们只关心不 panic + 状态干净
	_, err := r.Request(context.Background(), "m", nil, 30*time.Millisecond)
	if err == nil {
		t.Fatal("应超时")
	}

	// 迟到响应（该 id 已从待决表删除）
	tr.Feed(EncodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":"late"}`)))
	time.Sleep(30 * time.Millisecond)

	if got := r.PendingCount(); got != 0 {
		t.Errorf("待决表应仍为空，实得 %d", got)
	}
}

// TestRPC_DisposeRejectsPending —— Dispose 也必须 reject 待决请求。
func TestRPC_DisposeRejectsPending(t *testing.T) {
	tr := newMemTransport()
	r := NewRPC(tr)

	ch := make(chan error, 1)
	go func() {
		_, err := r.Request(context.Background(), "m", nil, 30*time.Second)
		ch <- err
	}()
	waitFor(t, time.Second, "请求进入待决表", func() bool { return r.PendingCount() == 1 })

	r.Dispose()

	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("Dispose 后应 reject")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dispose 未 reject 待决请求")
	}
}
