package net

import (
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// proxy.go —— 统一代理解析（第九十七刀 · W1）。
//
// 对账 TS `src/tools/net/proxy-resolver.ts`（209 行）。
//
// # 优先级（高 → 低）
//
//	config.network.proxy > HTTPS_PROXY/HTTP_PROXY > OS 系统代理 > 直连
//
// `NO_PROXY` 命中的域名**始终直连**，无论代理来自 config 还是环境变量。
//
// # 为什么单独成文件
//
// `httpfetch.go` 文件头 ② 明写「系统代理解析未移植，是待补的缺口」——
// 本文件正是补这个缺口。移植后 `httpfetch` 的 `Options.ProxyURL` 之外，
// 环境变量与 OS 系统代理也开始生效（见 W7 的接线）。
//
// # 与 TS 的差异（诚实披露）
//
// 1. **SOCKS 不支持**（与 TS 一致）：undici 的 `ProxyAgent` 仅支持 HTTP CONNECT
// 隧道，Go 的 `http.ProxyURL` 同理只处理 http/https scheme。SOCKS 需另引
// `golang.org/x/net/proxy`，不在本层范围。
// 2. **PAC 不处理**（与 TS 一致）：需取 PAC URL 并执行其中 JS。
// 3. **Windows 注册表读取**用 `reg query`（与 TS 同款），但 Go 侧无法在
// 非 Windows 上验证——测试用 `ParseWindowsProxyOutput` 纯函数覆盖。

// systemProxyProbe 是 OS 系统代理的探测入口。
//
// **为什么要可注入**：真实探测依赖 `runtime.GOOS` 与开发机当前状态——
// 在「开发机本来就没开系统代理」的机器上，`ReadSystemProxy` 恒返回空，
// 于是「kill switch 生效」与「kill switch 失效」**产生相同结果**，
// 测试失去区分力（第九十七刀 W1 变异反证 M4 红 0 暴露）。
//
// 注入后，测试用固定值探测即可确定性验证 kill switch。包内测试可替换。
var systemProxyProbe = ReadSystemProxy

// ReadSystemProxy 读当前 OS 的系统代理（Windows 注册表 / macOS scutil）。
//
// 非 Windows/macOS、未配置代理、或探测失败时返回空（视为直连）。
func ReadSystemProxy() string {
	if v := readWindowsSystemProxy(); v != "" {
		return v
	}
	return readMacosSystemProxy()
}

// ProxyResolverOptions 是代理解析的配置输入（对账 TS 同名接口）。
type ProxyResolverOptions struct {
	// ProxyURL 来自 `config.network.proxy`，**优先于环境变量**。
	ProxyURL string
	// NoProxy 来自 `config.network.noProxy`（逗号分隔，支持 `*` / `.` 前缀 / 精确匹配）。
	NoProxy string
}

// envCaseInsensitive 按大小写不敏感读环境变量（对账 TS `envCaseInsensitive`）。
//
// 先原样、再小写——`HTTPS_PROXY` 与 `https_proxy` 都要认。
func envCaseInsensitive(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return os.Getenv(strings.ToLower(key))
}

// ShouldBypassProxy 判定 hostname 是否命中 NO_PROXY 绕过列表。
//
// 匹配规则（与 curl/wget 语义对齐，对账 TS）：
//
//   - `*` 绕过所有
//   - 精确域名匹配（大小写不敏感）
//   - `.example.com` 后缀匹配：`api.example.com` 和 `example.com` 都命中
//
// noProxy 为空时回退到 `NO_PROXY` 环境变量。
func ShouldBypassProxy(hostname string, noProxy string) bool {
	raw := noProxy
	if raw == "" {
		raw = envCaseInsensitive("NO_PROXY")
	}
	if raw == "" {
		return false
	}
	h := strings.ToLower(hostname)
	for _, entry := range strings.Split(raw, ",") {
		p := strings.ToLower(strings.TrimSpace(entry))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if h == p {
			return true
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(h, p) || h == p[1:] {
				return true
			}
		}
	}
	return false
}

// ResolveProxyForURL 解析某个 URL 应该走哪个代理。
//
// 返回代理 URL，或空字符串表示**直连**。
//
// 优先级：
//  1. `opts.ProxyURL`（config.network.proxy）—— 设了就用，不再读环境变量
//  2. `HTTPS_PROXY` / `HTTP_PROXY` 环境变量（按 URL 协议选择，大小写不敏感）
//  3. OS 系统代理（Windows 注册表 / macOS scutil）
//  4. 直连
//
// `NO_PROXY` 命中时一律返回空，无论代理来源。
func ResolveProxyForURL(rawURL string, opts *ProxyResolverOptions) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	var noProxy string
	if opts != nil {
		noProxy = opts.NoProxy
	}
	if ShouldBypassProxy(parsed.Hostname(), noProxy) {
		return ""
	}

	// config 显式配置优先
	if opts != nil && opts.ProxyURL != "" {
		return opts.ProxyURL
	}

	// 回退到环境变量，再回退到 OS 系统代理
	systemProxy := func() string {
		// RIVET_NO_SYSTEM_PROXY=1：禁用 OS 级系统代理回退——
		// CI/沙箱/测试需要确定性直连，或用户显式绕过系统代理时使用。
		if os.Getenv("RIVET_NO_SYSTEM_PROXY") == "1" {
			return ""
		}
		return systemProxyProbe()
	}

	switch parsed.Scheme {
	case "https":
		if v := envCaseInsensitive("HTTPS_PROXY"); v != "" {
			return v
		}
		if v := envCaseInsensitive("HTTP_PROXY"); v != "" {
			return v
		}
		return systemProxy()
	case "http":
		if v := envCaseInsensitive("HTTP_PROXY"); v != "" {
			return v
		}
		if v := envCaseInsensitive("HTTPS_PROXY"); v != "" {
			return v
		}
		return systemProxy()
	default:
		// Non-HTTP protocols: try generic proxy env vars then OS fallbacks
		return systemProxy()
	}
}

// ── OS 系统代理 ─────────────────────────────────────────────────────────

// readWindowsSystemProxy 读 Windows 注册表的系统代理（HKCU Internet Settings）。
//
// 非 Windows 或无代理配置时返回空。
func readWindowsSystemProxy() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	// 先查 ProxyEnable——代理已禁用（0x0 或不存在）直接返回。
	// **必须在 ProxyServer 之前**：注册表里 ProxyServer 键即使代理禁用也常残留
	// （Windows 关代理时只翻 ProxyEnable，不清 ProxyServer），先读 ProxyServer
	// 会让禁用代理的用户被强制走残留的代理地址。
	enable, err := runRegQuery("ProxyEnable")
	if err != nil {
		return ""
	}
	server, err := runRegQuery("ProxyServer")
	if err != nil {
		return ""
	}
	return ParseWindowsProxyOutput(enable, server)
}

// runRegQuery 读一个注册表值（`reg query ... /v <name>`）。
func runRegQuery(name string) (string, error) {
	out, err := exec.Command("reg", "query",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		"/v", name).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// readMacosSystemProxy 读 macOS 的系统代理（`scutil --proxy`）。
//
// 非 macOS 或无 HTTP/HTTPS 代理启用时返回空。
func readMacosSystemProxy() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		// scutil 缺失或超时 —— 视为无系统代理，回退到环境变量/直连
		return ""
	}
	return ParseScutilProxy(string(out))
}

var scutilLine = regexp.MustCompile(`^\s*([A-Za-z]+)\s*:\s*(\S+)`)

// ParseScutilProxy 从 `scutil --proxy` 输出解析代理 URL。
//
// 抽成纯函数便于单测（避免 mock exec）。
//
// 优先级：HTTPS（启用且配置了 host+port）> HTTP。SOCKS 不处理（见文件头）。
// PAC（ProxyAutoConfigEnable=1）不处理——需取 PAC URL 并执行其中 JS。
//
// scutil 输出形如（非 JSON，嵌套字典文本）：
//
//	HTTPEnable : 1
//	HTTPPort : 7890
//	HTTPProxy : 127.0.0.1
//	HTTPSEnable : 1
//	HTTPSPort : 7890
//	HTTPSProxy : 127.0.0.1
func ParseScutilProxy(stdout string) string {
	fields := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		if m := scutilLine.FindStringSubmatch(line); m != nil {
			fields[m[1]] = m[2]
		}
	}
	// HTTPS 优先
	if fields["HTTPSEnable"] == "1" && fields["HTTPSProxy"] != "" && fields["HTTPSPort"] != "" {
		return "http://" + fields["HTTPSProxy"] + ":" + fields["HTTPSPort"]
	}
	// 回退 HTTP
	if fields["HTTPEnable"] == "1" && fields["HTTPProxy"] != "" && fields["HTTPPort"] != "" {
		return "http://" + fields["HTTPProxy"] + ":" + fields["HTTPPort"]
	}
	return ""
}

var regEnableOn = regexp.MustCompile(`0x1`)
var regProxyServer = regexp.MustCompile(`REG_SZ\s+(.+)`)

// ParseWindowsProxyOutput 从 `reg query` 的 ProxyEnable / ProxyServer 两段输出解析代理 URL。
//
// 抽成纯函数便于单测——避免 mock exec。
//
// ProxyEnable 输出形如 `    ProxyEnable    REG_DWORD    0x1`，
// ProxyServer 输出形如 `    ProxyServer    REG_SZ    host:port`。
func ParseWindowsProxyOutput(enableStdout, serverStdout string) string {
	// 代理未启用（0x0 或非 0x1）—— 即使 ProxyServer 残留也不返回
	if !regEnableOn.MatchString(enableStdout) {
		return ""
	}
	m := regProxyServer.FindStringSubmatch(serverStdout)
	if m == nil {
		return ""
	}
	return normalizeProxyURL(strings.TrimSpace(m[1]))
}

// normalizeProxyURL 保证代理 URL 带协议前缀。
//
// Windows 注册表存的是 `host:port`（无 scheme），但 `http.ProxyURL` 需要
// `http://host:port`。另处理 `http=host;https=host` 多协议格式——**优先取 https**，
// 因为绝大多数出站流量（API 调用、更新检查）走 https，http 代理端口常不监听 TLS。
func normalizeProxyURL(raw string) string {
	// Format: "http=host:port;https=host:port" — 两轮扫描，优先 https
	if strings.Contains(raw, "=") {
		parts := strings.Split(raw, ";")
		for _, want := range []string{"https", "http"} {
			for _, part := range parts {
				m := multiProtoRe.FindStringSubmatch(part)
				if m != nil && strings.ToLower(m[1]) == want {
					return normalizeProxyURL(strings.TrimSpace(m[2]))
				}
			}
		}
		return ""
	}
	// Bare "host:port" → add http:// prefix
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return raw
	}
	return "http://" + raw
}

var multiProtoRe = regexp.MustCompile(`(?i)^(https?)=(.+)`)
