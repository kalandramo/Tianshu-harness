// Package net 是出站网络访问的基础设施层。
//
// 对账 TS `src/tools/net/`（ssrf.ts / http-fetch.ts / proxy-resolver.ts）。
//
// # 分层
//
//   - ssrf.go        —— SSRF 防护（**安全边界**，所有出站请求都过它）
//   - httpfetch.go   —— 带防护的 HTTP 抓取（超时/重定向/大小上限/DNS pin）
//
// # 为什么这层先做
//
// `web_fetch` 等工具若没有这层，就是「能读云元数据服务的 SSRF 工具」。
// 安全边界必须先于消费它的工具落地。
package net

import (
	"fmt"
	"net/netip"
)

// ssrf.go —— SSRF 防护（第八十六刀 · W3-1）。
//
// 对账 TS `src/tools/net/ssrf.ts`（115 行）。
//
// # 最易漏的一点（TS 注释里的 issue #116）
//
// IPv4-mapped（`::ffff:0:0/96`）与 IPv4-translated（`::ffff:0:0:0/96`）IPv6
// 字面量的**低 32 位携带 IPv4 地址**，在网络上会到达同一主机。若不镜像保留段，
// `::ffff:0:169.254.169.254` 就能**绕过检查直达云元数据服务**。
//
// **不能整体封禁前缀**——那会误伤 `::ffff:8.8.8.8`（真公共地址）。
// 同理 NAT64（`64:ff9b::/96`）与 6to4（`2002::/16`）也要按段镜像，
// 否则 `64:ff9b::808:808`（NAT64 映射的 8.8.8.8）在纯 IPv6/NAT64 网络上
// 会让**每一次抓取都失败**。

// reservedIPv4 对账 TS `RESERVED_IPV4`——绝不可通过网络工具到达的 IPv4 段。
var reservedIPv4 = []struct {
	network string
	bits    int
}{
	{"0.0.0.0", 8},
	{"10.0.0.0", 8},
	{"100.64.0.0", 10},
	{"127.0.0.0", 8},
	{"169.254.0.0", 16},
	{"172.16.0.0", 12},
	{"192.0.0.0", 24},
	{"192.0.2.0", 24},
	{"192.168.0.0", 16},
	{"198.18.0.0", 15},
	{"198.51.100.0", 24},
	{"203.0.113.0", 24},
	{"224.0.0.0", 4},
	{"240.0.0.0", 4},
}

// reservedIPv6 对账 TS `RESERVED_IPV6`——非 IPv4 内嵌的 IPv6 保留段。
var reservedIPv6 = []struct {
	network string
	bits    int
}{
	{"::", 128},
	{"::1", 128},
	{"fc00::", 7},
	{"fe80::", 10},
	{"ff00::", 8},
	{"2001:db8::", 32},
}

// reservedPrefixes 是构建后的前缀集合（含镜像）。
var reservedPrefixes = buildReservedPrefixes()

// buildReservedPrefixes 构建保留前缀集合。
//
// **镜像逻辑**（对账 TS 的两轮 for 循环）：
//
//	对每个 IPv4 保留段 (network, prefix)：
//	  ::ffff:<network>/<prefix+96>      —— IPv4-mapped
//	  ::ffff:0:<network>/<prefix+96>    —— IPv4-translated
//	  64:ff9b::<v4tail>/<prefix+96>     —— NAT64
//	  2002:<v4tail>::/<prefix+16>       —— 6to4
func buildReservedPrefixes() []netip.Prefix {
	var out []netip.Prefix
	add := func(s string) {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Masked())
		}
	}
	for _, r := range reservedIPv4 {
		add(fmt.Sprintf("%s/%d", r.network, r.bits))
	}
	for _, r := range reservedIPv6 {
		add(fmt.Sprintf("%s/%d", r.network, r.bits))
	}
	// 镜像 IPv4 保留段进「内嵌 IPv4」的过渡前缀。
	for _, r := range reservedIPv4 {
		tail := v4Tail(r.network)
		add(fmt.Sprintf("::ffff:%s/%d", r.network, r.bits+96))
		add(fmt.Sprintf("::ffff:0:%s/%d", r.network, r.bits+96))
		add(fmt.Sprintf("64:ff9b::%s/%d", tail, r.bits+96))
		add(fmt.Sprintf("2002:%s::/%d", tail, r.bits+16))
	}
	return out
}

// v4Tail 把 IPv4 点分十进制转为「两个 hextet」形式。
//
// 对账 TS `v4Tail`：`a.b.c.d` → `(a<<8|b):(c<<8|d)`（十六进制）。
// 例：`10.0.0.0` → `a00:0`；`169.254.0.0` → `a9fe:0`。
func v4Tail(v4 string) string {
	addr, err := netip.ParseAddr(v4)
	if err != nil || !addr.Is4() {
		return "0:0"
	}
	b := addr.As4()
	hi := uint16(b[0])<<8 | uint16(b[1])
	lo := uint16(b[2])<<8 | uint16(b[3])
	return fmt.Sprintf("%x:%x", hi, lo)
}

// IsPrivateIP 判定 IP 是否落在保留/私有段内。
//
// 对账 TS `isPrivateIP`：非 IP 字面量返回 false（不报错）。
//
// **为什么消费方要用它两次**：TS 在「预检」与「socket 层」各查一次
// （见 httpfetch.go 的 pinned lookup）——防御纵深。
func IsPrivateIP(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	// 归一化：IPv4-mapped 地址（`::ffff:1.2.3.4`）在 netip 里 Is4In6 为 true，
	// 其 4 字节形式可用 Unmap() 取出。**不能**只 Unmap——translated/NAT64/6to4
	// 形式不是 4in6，必须靠镜像前缀覆盖。
	check := addr
	for _, p := range reservedPrefixes {
		if p.Contains(check) {
			return true
		}
	}
	// 4in6 形式再做一次未映射判定（等价于 TS 的「mapped spelling 被折进 IPv4 表」）。
	if check.Is4In6() {
		unmapped := check.Unmap()
		for _, p := range reservedPrefixes {
			if p.Contains(unmapped) {
				return true
			}
		}
	}
	return false
}

// SSRFError 对账 TS `SSRFError`——访问被拒（解析到私有/保留地址）。
type SSRFError struct {
	Hostname string
	Address  string
}

func (e *SSRFError) Error() string {
	return fmt.Sprintf("Access denied: %s resolves to a private/reserved IP (%s)", e.Hostname, e.Address)
}

// ResolvedAddress 对账 TS `ResolvedAddress`。
type ResolvedAddress struct {
	Address string
	// Family：4 或 6；注入的 lookup 只返回地址时可为 0。
	Family int
}

// LookupFn 对账 TS `LookupFn`——可注入的 DNS 解析（便于测试）。
type LookupFn func(hostname string) (ResolvedAddress, error)

// ResolveAndAssertPublic 解析主机名并断言结果是公共地址。
//
// 对账 TS `resolveAndAssertPublic`。
//
// **方括号处理**：`URL.hostname` 对 IPv6 literal 返回带方括号的形式（`"[::1]"`）
// ——`netip.ParseAddr` 同样解析不了它，**校验会形同失效**。四个消费点都传
// `URL.hostname`，故在最靠内的一层统一剥括号。
func ResolveAndAssertPublic(hostname string, lookup LookupFn) (ResolvedAddress, error) {
	host := stripBrackets(hostname)
	resolved, err := lookup(host)
	if err != nil {
		return ResolvedAddress{}, err
	}
	if IsPrivateIP(resolved.Address) {
		return ResolvedAddress{}, &SSRFError{Hostname: hostname, Address: resolved.Address}
	}
	family := resolved.Family
	if family == 0 {
		// 从地址推断（对账 TS：`family ?? (ipFamily === 0 ? undefined : ipFamily)`）
		if addr, err := netip.ParseAddr(resolved.Address); err == nil {
			if addr.Is4() || addr.Is4In6() {
				family = 4
			} else {
				family = 6
			}
		}
	}
	return ResolvedAddress{Address: resolved.Address, Family: family}, nil
}

// stripBrackets 剥掉 IPv6 字面量的方括号（对账 TS `hostname.replace(/^\[|\]$/g, ”)`）。
func stripBrackets(host string) string {
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		return host[1 : len(host)-1]
	}
	return host
}
