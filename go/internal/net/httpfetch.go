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
	"time"
)

// httpfetch.go —— 带防护的 HTTP 抓取（第八十七刀 · W3-2；第八十八刀修订）。
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
// # 诚实标注的能力边界
//
// **① 代理模式下 SSRF 保证弱于直连**（对账 TS 注释）：TS 明确写着 undici 的
// ProxyAgent 不读 `opts.connect`，目标主机名由代理解析，客户端拿不到隧道对端
// IP，**不存在 post-CONNECT 校验点**。Go 侧同理（`http.ProxyURL` 后由代理做
// CONNECT 与解析）。**这是能力边界而非已修复项**。
//
// **② 代理解析已补齐**（第九十七刀 W7）：`Options.ProxyURL` 为空时，
// 回退到 `ResolveProxyForURL`（proxy.go）——它读 `config.network.proxy` >
// `HTTPS_PROXY`/`HTTP_PROXY` 环境变量 > OS 系统代理（Windows 注册表 / macOS scutil），
// 并支持 `NO_PROXY` 绕过。与 TS `resolveProxyForUrl` 同序。
//
// 此项此前是第八十八刀审查点名的**待补缺口**（当时 proxy-resolver 未移植）。
// 现在逐跳解析已生效：代理按**每次请求的目标 URL** 解析（对账 TS 的逐请求语义）。
//
// **③ 多地址 DNS 记录的完整防护**：`defaultLookup` 只取第一个解析结果并预检它
// （对账 TS `dns.lookup` 的 `all:false` 语义）。理论上「一个公共 + 一个私有」的
// 记录集下，若客户端选中了另一个地址就绕过预检——但 DNS pin（直连模式）会把
// 连接钉在预检过的那个地址上，故**直连模式安全、代理模式回到边界 ①**。

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
//
// # 为什么数值字段是**指针**（第八十八刀修订）
//
// 对账 TS 的 `opts.timeoutMs ?? DEFAULT_TIMEOUT_MS`——`??` 只对 `null`/`undefined`
// 回退，**显式传入的 `0` 会被尊重**。Go 的零值无法区分「未设置」与「显式 0」，
// 故用指针：`nil` = 未设置（用默认），非 nil = 用该值（含 0）。
//
// 例：调用方设 `MaxRedirects: ptr(0)` 表示「不跟随重定向」——
// 若用值类型 + `<= 0` 判默认，会被静默变成 5。
type Options struct {
	TimeoutMs        *int
	MaxResponseBytes *int
	MaxRedirects     *int
	UserAgent        string
	// ProxyURL：**显式** HTTP 代理，优先于环境变量与 OS 系统代理。
	//
	// **见文件头的能力边界 ①**（代理模式下 SSRF 保证弱于直连）。
	// 为空时回退到 `ResolveProxyForURL`（config > env > OS 系统代理）——
	// 见文件头 ②。
	ProxyURL string
	// NoProxy：`NO_PROXY` 语义的绕过列表（逗号分隔，支持 `*` / `.` 前缀 / 精确匹配）。
	// **仅影响回退解析路径**（ProxyURL 为空时）——显式 ProxyURL 不受其约束，
	// 与既有行为保持一致。
	//
	// 留空时回退解析会读 `NO_PROXY` 环境变量。
	NoProxy string
}

// IntPtr 返回 *int（供设置 Options 的数值字段）。
func IntPtr(v int) *int { return &v }

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
	timeoutMs := defaultTimeoutMs
	if opts.TimeoutMs != nil {
		timeoutMs = *opts.TimeoutMs
	}
	maxBytes := defaultMaxBytes
	if opts.MaxResponseBytes != nil {
		maxBytes = *opts.MaxResponseBytes
	}
	maxRedirects := defaultMaxRedirects
	if opts.MaxRedirects != nil {
		maxRedirects = *opts.MaxRedirects
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

		// hopCtx 覆盖「请求 + 读 body」的完整生命周期。
		//
		// **第八十八刀修复的 P0 缺陷**：此前在 `doer(req)` 返回后立即 cancel，
		// 而终态 body 在循环外读——Go 的 http.Transport 在请求 ctx 取消后关闭
		// 连接，导致**任何 body 不是瞬间到达的抓取都读失败**（实测慢速 200KB
		// body 报 `context canceled`）。现在把 body 读取移进循环、cancel 延后。
		hopCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		req, err := http.NewRequestWithContext(hopCtx, http.MethodGet, currentURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)

		doer := deps.Doer
		if doer == nil {
			client, err := buildClient(resolved, opts)
			if err != nil {
				cancel()
				return nil, err
			}
			doer = client.Do
		}

		resp, err := doer(req)
		if err != nil {
			cancel()
			return nil, err
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			// 重定向响应体已丢弃 → 此跳的 ctx 可以安全取消。
			drainAndClose(resp.Body)
			cancel()
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

		// 终态响应：**在 ctx 存活期间**读 body，然后才 cancel。
		body, readErr := readBodyLimited(resp.Body, maxBytes)
		drainAndClose(resp.Body)
		cancel()
		if readErr != nil {
			return nil, readErr
		}

		return &Result{
			Status:      resp.StatusCode,
			FinalURL:    currentURL,
			ContentType: resp.Header.Get("Content-Type"),
			Bytes:       body,
		}, nil
	}

	return nil, fmt.Errorf("Too many redirects (>%d) for %s", maxRedirects, rawURL)
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
//
// **返回 error 而非静默降级**（第八十八刀审查发现）：此前 `url.Parse` 失败时
// 静默返回裸 client（无代理、无 pin）直连——那是**安全相关的静默降级**。
func buildClient(resolved ResolvedAddress, opts Options) (*http.Client, error) {
	// **重定向由本函数手动处理**（逐跳预检），故禁用自动跟随。
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 显式 ProxyURL 优先；为空时回退到统一代理解析（config > env > OS 系统代理）。
	// **逐跳解析**：Proxy 是函数，按每次请求的目标 URL 决定——对账 TS
	// 的逐请求 `resolveProxyForUrl`（NO_PROXY 需按 host 判定，不能一次算死）。
	if opts.ProxyURL != "" {
		proxyURL, err := url.Parse(opts.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("Invalid proxy URL %q: %w", opts.ProxyURL, err)
		}
		client.Transport = &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		return client, nil
	}

	// 无显式 ProxyURL → 回退到统一代理解析（第九十七刀 W7）。
	//
	// **逐跳**：`Proxy` 是函数，按每次请求的目标 URL 判定——`NO_PROXY`
	// 必须按 host 决定，不能在建 client 时一次算死（对账 TS 的逐请求
	// `resolveProxyForUrl`）。
	//
	// **与 DNS pin 互斥**：解析出代理时走此分支并**返回**，不再进 pin 分支——
	// 代理自己做 CONNECT 与目标解析，客户端拿不到隧道对端 IP（见文件头 ①）。
	proxyOpts := &ProxyResolverOptions{NoProxy: opts.NoProxy}
	if IsConnectionPinningEnabled() {
		client.Transport = &http.Transport{
			Proxy: func(r *http.Request) (*url.URL, error) {
				proxyURL := ResolveProxyForURL(r.URL.String(), proxyOpts)
				if proxyURL == "" {
					return nil, nil
				}
				return url.Parse(proxyURL)
			},
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		return client, nil
	}

	if !IsConnectionPinningEnabled() {
		return client, nil
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
	return client, nil
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
// **只取第一个地址**（对账 TS `dns.lookup` 的 `all:false` 语义）——
// 见文件头能力边界 ③。
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
