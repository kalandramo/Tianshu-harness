package net

import (
	"net/http"
	"testing"
)

// httpfetch_proxy_test.go —— httpfetch 的代理解析接线（第九十七刀 · W7）。
//
// # 背景
//
// `httpfetch.go` 文件头 ② 此前写着「系统代理解析未移植——只支持显式
// `Options.ProxyURL`，环境变量代理不会生效。这是**待补的缺口**」
// （第八十八刀审查点名）。第九十七刀 W1 移植了 proxy-resolver，本文件
// 验证 W7 的接线确实让它生效。
//
// # 测什么
//
// `buildClient` 是未导出的内部函数——同包测试可直接调用它，
// 再检查返回的 `*http.Client.Transport` 里的 `Proxy` 函数行为。
// 这比走真实网络干净：**只验证决策逻辑，不验证传输**。

// mkResolved 构造一个可用于 buildClient 的解析结果。
func mkResolved() ResolvedAddress {
	return ResolvedAddress{Address: "93.184.216.34", Family: 4}
}

// proxyOf 取出 client 的 Proxy 函数，并对给定 URL 求代理。
func proxyOf(t *testing.T, client *http.Client, rawURL string) string {
	t.Helper()
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 应为 *http.Transport，实得 %T", client.Transport)
	}
	if tr.Proxy == nil {
		return ""
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	u, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy 函数报错：%v", err)
	}
	if u == nil {
		return ""
	}
	return u.String()
}

// TestBuildClientExplicitProxyURLStillWins —— 显式 ProxyURL **仍最优先**。
//
// 这是**回归钉子**：W7 加了环境变量回退，绝不能把既有显式语义挤掉。
func TestBuildClientExplicitProxyURLStillWins(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy:1")
	t.Setenv("HTTP_PROXY", "http://env-proxy:1")
	t.Setenv("NO_PROXY", "")

	client, err := buildClient(mkResolved(), Options{ProxyURL: "http://explicit:9"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got := proxyOf(t, client, "https://example.com/"); got != "http://explicit:9" {
		t.Errorf("显式 ProxyURL 应优先，实得 %q", got)
	}
}

// TestBuildClientEnvProxyNowWorks —— ★ **W7 的核心验收**：环境变量代理生效。
//
// 接线前：`Options.ProxyURL` 为空 → `client.Transport` 保持默认（`Proxy` 为 nil，
// 走 `ProxyFromEnvironment`）——实际**不读我们的解析链**，`NO_PROXY` 语义也与
// TS 不一致。接线后：`Proxy` 是显式函数，走上 `ResolveProxyForURL`。
func TestBuildClientEnvProxyNowWorks(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-https:8080")
	t.Setenv("HTTP_PROXY", "http://env-http:8080")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}

	if got := proxyOf(t, client, "https://example.com/"); got != "http://env-https:8080" {
		t.Errorf("https 应取 HTTPS_PROXY，实得 %q", got)
	}
	if got := proxyOf(t, client, "http://example.com/"); got != "http://env-http:8080" {
		t.Errorf("http 应取 HTTP_PROXY，实得 %q", got)
	}
}

// TestBuildClientNoProxyBypassWorks —— ★ `NO_PROXY` 按 host 逐跳绕过。
func TestBuildClientNoProxyBypassWorks(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env:8080")
	t.Setenv("HTTP_PROXY", "http://env:8080")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{NoProxy: "internal.corp"})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}

	// 命中 NO_PROXY → 直连（空代理）
	if got := proxyOf(t, client, "https://internal.corp/api"); got != "" {
		t.Errorf("命中 NO_PROXY 应直连，实得 %q", got)
	}
	// 未命中 → 走代理
	if got := proxyOf(t, client, "https://example.com/"); got != "http://env:8080" {
		t.Errorf("未命中应走代理，实得 %q", got)
	}
}

// TestBuildClientNoProxyFromEnvWhenOptionEmpty —— NoProxy 留空时读 `NO_PROXY` 环境变量。
func TestBuildClientNoProxyFromEnvWhenOptionEmpty(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env:8080")
	t.Setenv("HTTP_PROXY", "http://env:8080")
	t.Setenv("NO_PROXY", ".bypassed.com")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got := proxyOf(t, client, "https://api.bypassed.com/"); got != "" {
		t.Errorf("NoProxy 留空应回退 NO_PROXY 环境变量，实得 %q", got)
	}
}

// TestBuildClientNoProxyWhenNothingConfigured —— 无任何代理配置 → 直连。
func TestBuildClientNoProxyWhenNothingConfigured(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got := proxyOf(t, client, "https://example.com/"); got != "" {
		t.Errorf("无配置应直连，实得 %q", got)
	}
}

// TestBuildClientProxyBranchSkipsDNSPin —— **代理分支与 DNS pin 互斥**。
//
// 探针：代理模式下 `DialContext` 不应是 pin 的那个（后者会把连接死钉到预解析 IP）。
// 对账文件头 ①：代理自己做 CONNECT 与解析，客户端拿不到隧道对端 IP——
// 「不存在 post-CONNECT 校验点」是**能力边界**，不是已修复项。
func TestBuildClientProxyBranchSkipsDNSPin(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env:8080")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	tr := client.Transport.(*http.Transport)
	// 代理分支的 DialContext 应为 nil（用默认 dialer 连代理），
	// 而非 pin 分支设置的「钉死到 resolved.Address」的函数。
	if tr.DialContext != nil {
		t.Error("代理分支不应设置 DialContext——否则会把到代理的连接钉到目标 IP")
	}
}

// TestBuildClientExplicitProxyURLInvalid —— 非法 URL 报错（既有语义不变）。
func TestBuildClientExplicitProxyURLInvalid(t *testing.T) {
	if _, err := buildClient(mkResolved(), Options{ProxyURL: "://bad"}); err == nil {
		t.Error("非法代理 URL 应报错")
	}
}

// TestBuildClientHTTPTransportProxyIsFunc —— 非 nil Proxy 是函数（逐跳语义）。
//
// 逐跳是必需的：`NO_PROXY` 必须按 host 判定，若建 client 时算死成单个
// `url.URL`（`http.ProxyURL`），不同目标 host 只能用同一代理。
func TestBuildClientHTTPTransportProxyIsFunc(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env:8080")
	t.Setenv("NO_PROXY", "bypass.me")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	client, err := buildClient(mkResolved(), Options{})
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	tr := client.Transport.(*http.Transport)
	if tr.Proxy == nil {
		t.Fatal("应设置 Proxy 函数")
	}
	// 同一个 client 对两个 host 得出不同结论——证明是逐跳而非固定
	via := proxyOf(t, client, "https://go.dev/")
	direct := proxyOf(t, client, "https://bypass.me/")
	if via == direct {
		t.Errorf("逐跳解析应对不同 host 给出不同结论（via=%q direct=%q）", via, direct)
	}
}
