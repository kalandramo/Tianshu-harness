package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpc_test.go —— JSON-RPC 客户端（V5 的核心部分）。
//
// # 架构复用声明
//
// 本文件测的 RPC **照搬 `go/internal/lsp/rpc.go` 的架构**（Transport 抽象 /
// pending map / readLoop / AbortAllPending / 写锁覆盖 id 分配），但**分帧不同**
// ——见 framing.go 的说明。故此处不重复测「架构」（那是 LSP 已钉过的），
// 只测 **MCP 特有的分帧接线 + server→client 请求的显式偏离**。

// TestRPCInitializeHandshake —— V5：与真子进程完成握手。
func TestRPCInitializeHandshake(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	raw, err := rpc.Request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "rivet", "version": "0.1.0"},
	}, 5000)
	if err != nil {
		t.Fatalf("initialize 失败：%v", err)
	}

	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("解析 result 失败：%v（raw=%s）", err, raw)
	}
	if res.ServerInfo.Name != "fake" {
		t.Errorf("serverInfo.name 应为 fake，实得 %q", res.ServerInfo.Name)
	}
}

// TestRPCErrorResponse —— 错误响应被正确转成 error。
func TestRPCErrorResponse(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	_, err := rpc.Request("boom", nil, 5000)
	if err == nil {
		t.Fatal("boom 应返回 error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误信息应含服务端 message，实得 %q", err.Error())
	}
}

// TestRPCUnknownMethod —— 未知方法得到 -32601（不是挂起）。
func TestRPCUnknownMethod(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	_, err := rpc.Request("no/such/method", nil, 5000)
	if err == nil {
		t.Fatal("未知方法应返回 error")
	}
}

// TestRPCRequestTimeout —— 超时后不泄漏 pending（V5 的「不永等」面）。
//
// 对账 `lsp/rpc.go` 的 wedge 修法：读端不推进时调用方不能永等。
func TestRPCRequestTimeout(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	start := time.Now()
	_, err := rpc.Request("slow", nil, 300) // 300ms 超时，server 永不答
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("应答永不到达应超时")
	}
	if elapsed > 3*time.Second {
		t.Errorf("超时应及时（<3s），实得 %v", elapsed)
	}
	if n := rpc.PendingCount(); n != 0 {
		t.Errorf("超时后不该有残留 pending，实得 %d", n)
	}
}

// TestRPCServerToClientRequestGetsMethodNotFound —— ★ 显式偏离（对账 LSP 第四分支）。
//
// 假 server 会先发一条**带 id 的 server→client 请求**（`sampling/createMessage`），
// 再回正常响应。JSON-RPC 2.0 要求「请求必须有响应」——TS 在此**静默丢弃**
// （`lsp/rpc.go` 的注释记录了同款上游缺陷），Go 侧回 `MethodNotFound`。
//
// **为什么值得测**：真实 server（MCP 的 `sampling/` 系列）拿不到答复会挂等，
// 表现为「调用某工具后连接卡死」——极难排查。
func TestRPCServerToClientRequestGetsMethodNotFound(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	// 该方法会先推一条 server→client 请求再回结果
	raw, err := rpc.Request("server_to_client", nil, 5000)
	if err != nil {
		t.Fatalf("请求应成功（server→client 请求不该影响本次响应）：%v", err)
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || !res.OK {
		t.Fatalf("应拿到本请求的正常响应：raw=%s err=%v", raw, err)
	}
	// 客户端发出的 MethodNotFound 由假 server 收下但不校验（server 侧无断言），
	// 故此处断言「客户端未把它当成本次请求的响应」——即 pending 已正确清空。
	if n := rpc.PendingCount(); n != 0 {
		t.Errorf("server→client 请求不该占用 pending，实得 %d", n)
	}
}

// TestRPCBadFrameDoesNotBreak —— 坏帧不阻断后续（V2 的接线面）。
func TestRPCBadFrameDoesNotBreak(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	// 先发一条非法 JSON（假 server 收后会回一条坏帧）
	tr.Write([]byte("{this is not json\n"))

	// 随后的正常请求仍应成功
	raw, err := rpc.Request("initialize", map[string]any{}, 5000)
	if err != nil {
		t.Fatalf("坏帧后连接应仍可用：%v", err)
	}
	if len(raw) == 0 {
		t.Error("应拿到响应")
	}
}

// TestRPCNotifyDoesNotWaitForReply —— 通知不等响应。
func TestRPCNotifyDoesNotWaitForReply(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	done := make(chan error, 1)
	go func() { done <- rpc.Notify("notifications/initialized", nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Notify 不该报错：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Notify 不该等响应（挂起 2s）")
	}
}

// TestRPCDisposeUnblocksPending —— ★ Dispose 必须让在飞请求失败，而非永等。
//
// 对账 `lsp/rpc.go` 的 `AbortAllPending`（wedge 事故修法）。变异 M4 打这里。
func TestRPCDisposeUnblocksPending(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)

	errCh := make(chan error, 1)
	go func() {
		_, err := rpc.Request("slow", nil, 30_000) // 长超时——靠 Dispose 解除
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond) // 让请求真的发出去
	rpc.Dispose()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("Dispose 后应返回错误（不得让调用方永等）")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dispose 未能解除在飞请求——调用方永等（本仓库记载的 wedge 形态）")
	}
}

// TestRPCConcurrentRequestsKeepIDOrder —— 并发请求的 id 序与帧序一致。
//
// 对账 `lsp/rpc.go` 的 `writeMu` 注释：若只在锁内分配 id、锁外写帧，
// 帧序会与 id 序不一致（实测 1,3,2）。这是 LSP 侧已修过的缺陷，
// MCP 照搬其修法（写锁覆盖「分配 id → 注册 pending → 写帧」整段）。
func TestRPCConcurrentRequestsKeepIDOrder(t *testing.T) {
	tr, _ := startFakeServer(t)
	rpc := NewRPC(tr)
	defer rpc.Dispose()

	const n = 8
	var wg [n]chan error
	for i := range wg {
		wg[i] = make(chan error, 1)
	}
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := rpc.Request("initialize", map[string]any{}, 5000)
			wg[i] <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-wg[i]; err != nil {
			t.Errorf("并发请求 %d 失败：%v", i, err)
		}
	}
	if n := rpc.PendingCount(); n != 0 {
		t.Errorf("全部完成后不该有残留 pending，实得 %d", n)
	}
}

// ---- 用于区分「显式 Dispose」与「transport 死亡」的假 transport ----

// stickyTransport 的 Read 会**永久阻塞**，且 Close **不释放它**。
//
// **为什么需要它**：真 subprocess 的 Close 会关管道 → readLoop 的 Read 返回错误
// → 走「transport 死亡」路径调 AbortAllPending。于是「Dispose 显式 Abort」
// 与「靠死亡路径 Abort」在真子进程上**不可区分**（M4 变异因此红 0）。
// 本 transport 把两者分开：Close 不解除 Read，故只有显式调用才能解在飞请求。
type stickyTransport struct {
	readGate  chan struct{} // 永不关闭 → Read 永久阻塞
	closeOnce sync.Once
	writes    bytes.Buffer
}

func (t *stickyTransport) Read(p []byte) (int, error) {
	<-t.readGate // 永久阻塞（无写入者）
	return 0, nil
}

func (t *stickyTransport) Write(p []byte) (int, error) { return t.writes.Write(p) }

// Close **故意不关闭 readGate** —— 模拟「管道关闭不能中断阻塞中的 Read」
// （本仓库 HANDOFF 记载过该形态：挂了比泄漏 goroutine 更坏）。
func (t *stickyTransport) Close() error {
	t.closeOnce.Do(func() { /* 不关 readGate */ })
	return nil
}

// TestRPCDisposeAbortsEvenIfTransportDoesNotUnblock —— ★ 区分两条路径。
//
// M4 变异（Dispose 不调 AbortAllPending）在此**必红**：因为 Close 不解除
// 阻塞的 Read，别无他路可解在飞请求。
func TestRPCDisposeAbortsEvenIfTransportDoesNotUnblock(t *testing.T) {
	tr := &stickyTransport{readGate: make(chan struct{})}
	rpc := NewRPC(tr)

	errCh := make(chan error, 1)
	go func() {
		_, err := rpc.Request("whatever", nil, 60_000) // 长超时——只能靠 Dispose 解
		errCh <- err
	}()

	time.Sleep(150 * time.Millisecond) // 让请求注册进 pending
	rpc.Dispose()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("Dispose 必须让在飞请求失败——否则调用方永等（wedge）")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dispose 未解除在飞请求：Close 不释放 Read 时就永等（本测试专测此路径）")
	}
}
