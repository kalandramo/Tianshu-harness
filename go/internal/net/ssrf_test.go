package net

import (
	"errors"
	"strings"
	"testing"
)

// ssrf_test.go —— SSRF 防护层（第八十六刀 · W3-1）。
//
// 对账 TS `src/tools/net/ssrf.ts`（115 行）。
//
// # 为什么这层必须最先做
//
// 它是 W3 网络子系统的**安全边界**：所有出站请求都要过它。没有它，
// `web_fetch` 就是一个能读云元数据服务（169.254.169.254）的 SSRF 工具。
//
// # 最易漏的一点（TS 注释里的 issue #116）
//
// IPv4-mapped（`::ffff:0:0/96`）与 IPv4-translated（`::ffff:0:0:0/96`）IPv6 字面量
// 的**低 32 位携带 IPv4 地址**，在网络上会到达同一主机。若不镜像保留段，
// `::ffff:0:169.254.169.254` 就能**绕过检查直达云元数据服务**。
//
// **不能整体封禁前缀**——那会误伤 `::ffff:8.8.8.8`（真公共地址）。
// 同理 NAT64（`64:ff9b::/96`）与 6to4（`2002::/16`）也要按段镜像，
// 否则 `64:ff9b::808:808`（NAT64 映射的 8.8.8.8）会被误判为私有。

// TestSSRFIPv4Reserved —— IPv4 保留段全拦。
//
// 对账 TS `RESERVED_IPV4` 的 14 段。
func TestSSRFIPv4Reserved(t *testing.T) {
	reserved := []struct{ ip, why string }{
		{"0.0.0.0", "0/8 本网络"},
		{"0.1.2.3", "0/8 本网络"},
		{"10.0.0.1", "10/8 私有"},
		{"10.255.255.255", "10/8 私有"},
		{"100.64.0.1", "100.64/10 CGNAT"},
		{"127.0.0.1", "127/8 环回"},
		{"127.1.2.3", "127/8 环回"},
		{"169.254.169.254", "169.254/16 链路本地（**云元数据**）"},
		{"172.16.0.1", "172.16/12 私有"},
		{"172.31.255.255", "172.16/12 私有"},
		{"192.0.0.1", "192.0.0/24 IETF"},
		{"192.0.2.1", "192.0.2/24 TEST-NET-1"},
		{"192.168.1.1", "192.168/16 私有"},
		{"198.18.0.1", "198.18/15 基准测试"},
		{"198.51.100.1", "198.51.100/24 TEST-NET-2"},
		{"203.0.113.1", "203.0.113/24 TEST-NET-3"},
		{"224.0.0.1", "224/4 组播"},
		{"240.0.0.1", "240/4 保留"},
		{"255.255.255.255", "240/4 保留"},
	}
	for _, c := range reserved {
		if !IsPrivateIP(c.ip) {
			t.Errorf("%s 应判为私有/保留（%s）", c.ip, c.why)
		}
	}
}

// TestSSRFIPv4Public —— 真公共地址**不**误拦。
//
// **为什么重要**：整体封禁前缀的做法会把公共地址误伤——这正是 TS 注释
// 强调「镜像每段而非封禁前缀」的理由。
func TestSSRFIPv4Public(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", // example.com
		"172.15.255.255",               // 172.16/12 的**下界外**
		"172.32.0.1",                   // 172.16/12 的**上界外**
		"11.0.0.1",                     // 10/8 外
		"126.255.255.255", "128.0.0.1", // 127/8 两侧
		"169.253.255.255", "169.255.0.1", // 169.254/16 两侧
	}
	for _, ip := range public {
		if IsPrivateIP(ip) {
			t.Errorf("%s 是公共地址，不应判为私有", ip)
		}
	}
}

// TestSSRFIPv6Reserved —— IPv6 保留段。
//
// 对账 TS `RESERVED_IPV6` 的 6 段。
func TestSSRFIPv6Reserved(t *testing.T) {
	reserved := []struct{ ip, why string }{
		{"::", "全零地址"},
		{"::1", "环回"},
		{"fc00::1", "fc00::/7 唯一本地"},
		{"fdff:ffff::1", "fc00::/7 上界内"},
		{"fe80::1", "fe80::/10 链路本地"},
		{"ff02::1", "ff00::/8 组播"},
		{"2001:db8::1", "2001:db8::/32 文档用"},
	}
	for _, c := range reserved {
		if !IsPrivateIP(c.ip) {
			t.Errorf("%s 应判为私有/保留（%s）", c.ip, c.why)
		}
	}
}

// TestSSRFIPv6Public —— 公共 IPv6 不误拦。
func TestSSRFIPv6Public(t *testing.T) {
	for _, ip := range []string{"2001:4860:4860::8888", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if IsPrivateIP(ip) {
			t.Errorf("%s 是公共地址，不应判为私有", ip)
		}
	}
}

// TestSSRFIPv4Mapped —— **issue #116 核心**：内嵌 IPv4 的过渡前缀。
//
// 这是本层最易漏的洞：不镜像保留段，`::ffff:0:169.254.169.254` 可直达
// 云元数据服务。
func TestSSRFIPv4Mapped(t *testing.T) {
	cases := []struct{ ip, why string }{
		// ① IPv4-mapped `::ffff:a.b.c.d`
		{"::ffff:10.0.0.1", "IPv4-mapped 私有"},
		{"::ffff:127.0.0.1", "IPv4-mapped 环回"},
		{"::ffff:169.254.169.254", "IPv4-mapped **云元数据**"},
		{"::ffff:192.168.1.1", "IPv4-mapped 私有"},
		// ② IPv4-translated `::ffff:0:a.b.c.d`
		{"::ffff:0:10.0.0.1", "IPv4-translated 私有"},
		{"::ffff:0:169.254.169.254", "IPv4-translated **云元数据**（TS 注释点名）"},
		// ③ NAT64 `64:ff9b::/96`——低 32 位是 IPv4
		{"64:ff9b::a00:1", "NAT64 映射的 10.0.0.1"},
		{"64:ff9b::a9fe:a9fe", "NAT64 映射的 169.254.169.254"},
		// ④ 6to4 `2002:WWXX:YYZZ::/48`——前 32 位是 IPv4
		{"2002:a00:1::1", "6to4 映射的 10.0.0.1"},
		{"2002:a9fe:a9fe::1", "6to4 映射的 169.254.169.254"},
	}
	for _, c := range cases {
		if !IsPrivateIP(c.ip) {
			t.Errorf("%s 应判为私有（%s）", c.ip, c.why)
		}
	}
}

// TestSSRFIPv4MappedPublicNotBlocked —— **镜像而非整体封禁**的关键验证。
//
// 对账 TS 注释：「a single `::ffff:0:0/96` subnet would also reject genuinely
// public literals such as `::ffff:8.8.8.8`」。同理 NAT64/6to4 整体封禁会让
// `64:ff9b::808:808`（NAT64 映射的 8.8.8.8）在纯 IPv6/NAT64 网络上**全部抓取失败**。
func TestSSRFIPv4MappedPublicNotBlocked(t *testing.T) {
	public := []struct{ ip, why string }{
		{"::ffff:8.8.8.8", "IPv4-mapped 公共"},
		{"::ffff:1.1.1.1", "IPv4-mapped 公共"},
		{"64:ff9b::808:808", "NAT64 映射的 8.8.8.8（TS 注释点名）"},
		{"64:ff9b::101:101", "NAT64 映射的 1.1.1.1"},
		{"2002:808:808::1", "6to4 映射的 8.8.8.8"},
	}
	for _, c := range public {
		if IsPrivateIP(c.ip) {
			t.Errorf("%s 是公共地址，不应判为私有（%s）", c.ip, c.why)
		}
	}
}

// TestSSRFNonIPInput —— 非 IP 字面量返回 false（不 panic）。
//
// 对账 TS `isIP(ip)` 返回 0 时 → false。
func TestSSRFNonIPInput(t *testing.T) {
	for _, s := range []string{"", "not-an-ip", "example.com", "999.999.999.999", "::gggg"} {
		if IsPrivateIP(s) {
			t.Errorf("%q 不是合法 IP，应返回 false", s)
		}
	}
}

// ── resolveAndAssertPublic ──────────────────────────────────────────────

// TestSSRFResolveAndAssertPublic —— 公共地址通过、私有地址报错。
func TestSSRFResolveAndAssertPublic(t *testing.T) {
	// 公共 → 返回地址
	pub := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "93.184.216.34", Family: 4}, nil
	}
	got, err := ResolveAndAssertPublic("example.com", pub)
	if err != nil {
		t.Fatalf("公共地址应通过：%v", err)
	}
	if got.Address != "93.184.216.34" {
		t.Errorf("地址不符：%+v", got)
	}

	// 私有 → SSRFError
	priv := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "169.254.169.254", Family: 4}, nil
	}
	_, err = ResolveAndAssertPublic("evil.example", priv)
	if err == nil {
		t.Fatal("私有地址应报错")
	}
	var ssrfErr *SSRFError
	if !errors.As(err, &ssrfErr) {
		t.Fatalf("应为 *SSRFError，实得 %T", err)
	}
	if ssrfErr.Hostname != "evil.example" || ssrfErr.Address != "169.254.169.254" {
		t.Errorf("SSRFError 字段不符：%+v", ssrfErr)
	}
	// 文案逐字对账 TS
	want := "Access denied: evil.example resolves to a private/reserved IP (169.254.169.254)"
	if err.Error() != want {
		t.Errorf("文案应逐字对账：\n实得 %q\n期望 %q", err.Error(), want)
	}
}

// TestSSRFBracketStripping —— **IPv6 字面量的方括号必须剥掉**。
//
// 对账 TS 注释：`URL.hostname` 对 IPv6 literal 返回带方括号的形式（`"[::1]"`）：
// `isIP` 返回 0，`dns.lookup` 也解析不了它，**校验会形同失效**。
// 四个消费点都传 `URL.hostname`，故在最靠内的一层统一剥括号。
func TestSSRFBracketStripping(t *testing.T) {
	var sawHost string
	lookup := func(host string) (ResolvedAddress, error) {
		sawHost = host
		return ResolvedAddress{Address: "10.0.0.1", Family: 4}, nil
	}
	_, err := ResolveAndAssertPublic("[::1]", lookup)
	// 传给 lookup 的应是剥掉括号的形式
	if sawHost != "::1" {
		t.Errorf("方括号应被剥掉，lookup 收到 %q", sawHost)
	}
	// 且仍应被拦（10.0.0.1 是私有）
	if err == nil {
		t.Error("应报错（解析到私有地址）")
	}
}

// TestSSRFFamilyFallback —— family 缺失时从地址推断。
func TestSSRFFamilyFallback(t *testing.T) {
	// 注入的 lookup 只返回地址（无 family）→ 应从地址推断
	noFam := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "8.8.8.8"}, nil
	}
	got, err := ResolveAndAssertPublic("dns.google", noFam)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got.Family != 4 {
		t.Errorf("应从地址推断 family=4，实得 %d", got.Family)
	}

	noFam6 := func(host string) (ResolvedAddress, error) {
		return ResolvedAddress{Address: "2606:4700:4700::1111"}, nil
	}
	got, err = ResolveAndAssertPublic("cloudflare.com", noFam6)
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if got.Family != 6 {
		t.Errorf("应从地址推断 family=6，实得 %d", got.Family)
	}
}

// TestSSRFSSRFErrorName —— 错误类型名对账（TS 的 `this.name = 'SSRFError'`）。
func TestSSRFSSRFErrorName(t *testing.T) {
	e := &SSRFError{Hostname: "h", Address: "1.2.3.4"}
	if !strings.Contains(e.Error(), "Access denied:") {
		t.Errorf("文案应含 Access denied，实得 %q", e.Error())
	}
}
