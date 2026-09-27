package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeserver_test.go —— 本地假 MCP server 夹具（不依赖任何外部包）。
//
// # 为什么自造而不引 npx 起的真 server
//
// ① 端到端验收不应依赖网络/registry（本仓库既有教训：`npx kill-port`
// 实测耗时 107.88s，只因真去拉包）；
// ② 假 server 能**精确控制协议行为**（延迟应答、坏帧、方法不存在），
// 真 server 反而做不到这些边界。
//
// # 形态
//
// 一个真实子进程（`go run` 不方便 → 用当前测试二进制自身做 re-exec），
// 读写**换行分隔**的 JSON-RPC——与生产形态一致。
//
// **关键**：它是真进程、真管道、真换行分帧。mock 一个 in-memory
// Transport 就测不到「管道与分帧」这层（本仓库的 wiring 纪律）。

// fakeServerMode 环境变量：非空时测试二进制以「假 server」身份运行。
const fakeServerMode = "MCP_FAKE_SERVER"

// TestMain 让本包测试二进制可被 re-exec 成假 server。
func TestMain(m *testing.M) {
	if os.Getenv(fakeServerMode) != "" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

// runFakeServer 假 MCP server 主循环：读换行分隔的请求，按方法应答。
func runFakeServer() {
	enc := json.NewEncoder(os.Stdout)
	in := bufio.NewReader(os.Stdin)

	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, "fake server read error:", err)
			}
			return
		}
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) == 0 {
			continue
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			// 故意多发一条坏帧，验证客户端容错（测试 T3 依赖）
			fmt.Fprintln(os.Stdout, "{not valid json")
			continue
		}

		// 通知（无 id）不回复
		if req.ID == nil {
			continue
		}

		switch req.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake", "version": "0.0.1"},
				},
			})
		case "tools/list":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "Echo back the input",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{"type": "string"},
								},
							},
						},
						{
							"name":        "write__thing",
							"description": "A write-capable tool (name has __)",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
				},
			})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, _ := p.Arguments["text"].(string)
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo:" + text}},
				},
			})
		case "slow":
			// 永不应答——供超时用例
			time.Sleep(10 * time.Second)
		case "boom":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"error": map[string]any{"code": -32603, "message": "boom"},
			})
		case "server_to_client":
			// 先发一条 server→client **请求**（带 id），再回正常响应。
			// 客户端应回 MethodNotFound（而非静默丢弃）。
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 999999, "method": "sampling/createMessage",
			})
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{"ok": true},
			})
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		}
	}
}

// ---- 夹具：起一个假 server 子进程并接成 Transport ----

// startFakeServer 启动假 server，返回 Transport 与清理函数。
func startFakeServer(t *testing.T) (Transport, func()) {
	t.Helper()

	tr, err := SpawnStdio(ServerConfig{
		Command: os.Args[0], // 测试二进制自身
		Args:    []string{"-test.run=TestMain"},
		Env:     map[string]string{fakeServerMode: "1"},
	}, "")
	if err != nil {
		t.Fatalf("启动假 server 失败：%v", err)
	}

	var once sync.Once
	cleanup := func() { once.Do(func() { _ = tr.Close() }) }
	t.Cleanup(cleanup)
	return tr, cleanup
}
