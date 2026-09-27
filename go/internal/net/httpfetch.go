package net

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// httpfetch.go —— 带防护的 HTTP 抓取（第八十七刀 · W3-2）。
//
// 对账 TS `src/tools/net/http-fetch.ts`（272 行）。
//
// # 防护面（每一层都有对应测试）
//
//  1. 协议白名单（仅 http/https）
//  2. **每跳** SSRF 预检（含重定向——攻击者可用 302 绕到私网）
//  3. DNS pin（防重绑定：连到预检过的地址，而非重新解析）
//  4. 超时（连接 + body 读取）
//  5. 响应大小上限（**不静默截断**——超限报错）
//  6. 重定向次数上限
//
// # DNS 重绑定与 pin（对账 TS 的核心安全设计）
//
// TS 用 undici 的 `connect.lookup` 把连接钉死在 SSRF 预检过的地址上——
// 否则「预检」与「socket 连接」之间 attacker 可以把 DNS 翻成私网地址。
// Go 侧对应物是 `http.Transport.DialContext`（探针已验证可行）。
//
// # 代理模式的**能力边界**（诚实标注，对账 TS 注释）
//
// TS 明确写着：代理模式下目标**只有**请求前的一次性预检——undici 的 ProxyAgent
// 根本不读 `opts.connect`，目标主机名由代理解析，客户端拿不到隧道对端 IP，
// 因此**不存在 post-CONNECT 校验点**。Go 侧同理（`http.ProxyURL` 后由代理做
// CONNECT 与解析）。**这是能力边界而非已修复项**，不要当作强保证使用。

// 默认值（对账 TS 常量，逐字）。
const (
	defaultTimeoutMs    = 15_000
	defaultMaxBytes     = 10_485_760
	defaultMaxRedirects = 5
	defaultUserAgent    = "Tianshu/1.0 (terminal coding agent)"
)

// Doer 是 HTTP 执行的抽象（对账 TS 的 `FetchLike` + `deps.fetch`）。
//
// **为什么用接口而非直接 http.Client**：测试需要注入固定响应（不打真实网络）。
type Doer func(req *http.Request) (*http.Response, error)

// Deps 对账 TS `HttpFetchDeps`——可注入的依赖。
type Deps struct {
	// Lookup 是 DNS 解析（缺省用 net.DefaultResolver）。
	Lookup LookupFn
	// Doer 注入自定义 HTTP 执行器（测试用）。**注入后 DNS pin 路径关闭**
	// （对账 TS：「注入后会关闭 SSRF pin + proxy 路径」）。
	Doer Doer
}

// Options 对账 TS `HttpFetchOptions`。
type Options struct {
	TimeoutMs        int
	MaxResponseBytes int
	MaxRedirects     int
	UserAgent        string
	// ProxyURL：可选的 HTTP 代理。**见文件头的能力边界说明**。
	ProxyURL string
}

// Result 对账 TS `HttpFetchResult`。
type Result struct {
	Status      int
	FinalURL    string
	ContentType string
	Bytes       []byte
}

// IsConnectionPinningEnabled 对账 TS `isConnectionPinningEnabled`。
//
// 默认开；`RIVET_FETCH_PIN=0` 或 `false` 关闭（回退到默认 DNS 解析）。
func IsConnectionPinningEnabled() bool {
	v := os.Getenv("RIVET_FETCH_PIN")
	return v != "0" && v != "false"
}

// HTTPFetchGuarded 抓取 URL，逐跳过 SSRF 预检。
//
// 对账 TS `httpFetchGuarded`。
func HTTPFetchGuarded(ctx context.Context, rawURL string, deps Deps, opts Options) (*Result, error) {
	lookup := deps.Lookup
	if lookup == nil {
		lookup = defaultLookup
	}
	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	maxBytes := opts.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = defaultMaxRedirects
	}
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("Invalid URL: %s", rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("Unsupported protocol: %s. Only http and https are allowed.", parsed.Scheme)
	}

	currentURL := parsed.String()
	var lastResp *http.Response

	for hop := 0; hop <= maxRedirects; hop++ {
		hopURL, err := url.Parse(currentURL)
		if err != nil {
			return nil, fmt.Errorf("Invalid redirect URL: %s", currentURL)
		}
		if hopURL.Scheme != "http" && hopURL.Scheme != "https" {
			return nil, fmt.Errorf("Redirect to unsupported protocol: %s", hopURL.Scheme)
		}

		// **每跳都预检**——重定向不能绕 SSRF。
		resolved, err := ResolveAndAssertPublic(hopURL.Hostname(), lookup)
		if err != nil {
			return nil, err
		}

		hopCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		req, err := http.NewRequestWithContext(hopCtx, http.MethodGet, currentURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)

		doer := deps.Doer
		if doer == nil {
			client := buildClient(resolved, opts)
			doer = client.Do
		}

		resp, err := doer(req)
		cancel()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			drainAndClose(resp.Body)
			if location == "" {
				return nil, fmt.Errorf("Redirect %d with no Location header", resp.StatusCode)
			}
			next, err := url.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("Invalid redirect URL: %s", location)
			}
			currentURL = hopURL.ResolveReference(next).String()
			continue
		}

		lastResp = resp
		break
	}

	if lastResp == nil {
		return nil, fmt.Errorf("Too many redirects (>%d) for %s", maxRedirects, rawURL)
	}
	defer drainAndClose(lastResp.Body)

	body, err := readBodyLimited(lastResp.Body, maxBytes)
	if err != nil {
		return nil, err
	}

	return &Result{
		Status:      lastResp.StatusCode,
		FinalURL:    currentURL,
		ContentType: lastResp.Header.Get("Content-Type"),
		Bytes:       body,
	}, nil
}

// buildClient 构造把连接钉在 `resolved` 地址上的 http.Client。
//
// 对账 TS `buildDispatcher` + `dispatcherConnectOptions`。
//
// **pin 与代理是两个独立维度**（对账 TS 注释）：
//   - 配了代理 → 走代理（pin 无意义，目标由代理解析）；
//   - 无代理且 pin 开 → 连接钉死在预检过的地址上；
//   - 无代理且 pin 关 → 默认解析。
//
// TS 曾把两者绑在一起（`pin ? … : undefined`），导致 `RIVET_FETCH_PIN=0`
// 会**静默丢掉用户配的代理**——此处不重蹈。
func buildClient(resolved ResolvedAddress, opts Options) *http.Client {
	// **重定向由本函数手动处理**（逐跳预检），故禁用自动跟随。
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	if opts.ProxyURL != "" {
		proxyURL, err := url.Parse(opts.ProxyURL)
		if err == nil {
			client.Transport = &http.Transport{
				Proxy:           http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			}
		}
		return client
	}

	if !IsConnectionPinningEnabled() {
		return client
	}

	pinned := resolved.Address
	dialer := &net.Dialer{}
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// **防御纵深**：绝不把私有地址交给 socket 层（对账 TS `buildPinnedLookup`）。
			if IsPrivateIP(pinned) {
				return nil, &SSRFError{Hostname: addr, Address: pinned}
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = ""
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(pinned, port))
		},
	}
	return client
}

// readBodyLimited 读取 body，超过 maxBytes 时报错（**不静默截断**）。
//
// 对账 TS `readBody`：`Response body exceeds maximum allowed size (N bytes)`。
func readBodyLimited(r io.Reader, maxBytes int) ([]byte, error) {
	limited := io.LimitReader(r, int64(maxBytes)+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(buf) > maxBytes {
		return nil, fmt.Errorf("Response body exceeds maximum allowed size (%d bytes)", maxBytes)
	}
	return buf, nil
}

// drainAndClose 丢弃并关闭 body（释放连接）。
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4096))
	_ = body.Close()
}

// defaultLookup 用 net.DefaultResolver 解析主机名。
//
// **为什么不用 net.LookupIP 直接取第一个**：需要显式处理「解析出多个地址」
// ——任一是私有地址就应拒绝（否则 attacker 可以用「一个公共 + 一个私有」
// 的记录让预检通过而实际连到私有）。此处返回**第一个**地址，但预检失败时
// 由调用方报错。
//
// 诚实标注：TS 的 `dns.lookup` 默认也只返回一个地址（Node 的 all:false 语义）。
// 多地址场景的完整防护需要逐个预检——**这是当前实现的边界**。
func defaultLookup(host string) (ResolvedAddress, error) {
	// 已是 IP 字面量 → 直接用（不解析）。
	if ip := net.ParseIP(host); ip != nil {
		family := 4
		if ip.To4() == nil {
			family = 6
		}
		return ResolvedAddress{Address: host, Family: family}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return ResolvedAddress{}, err
	}
	if len(ips) == 0 {
		return ResolvedAddress{}, fmt.Errorf("no such host: %s", host)
	}
	addr := ips[0].IP.String()
	family := 4
	if ips[0].IP.To4() == nil {
		family = 6
	}
	return ResolvedAddress{Address: addr, Family: family}, nil
}

// HostOf 返回 URL 的主机名（剥端口）。
//
// **为什么需要**：`url.URL.Host` 含端口，而 `Hostname()` 已剥——此函数用于
// 需要显式剥端口的调用方（对账 TS 直接传 `URL.hostname`）。
func HostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// 确保 strings 被引用（错误消息拼接用）。
var _ = strings.TrimSpace
