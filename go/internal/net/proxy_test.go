package net

import "testing"

// proxy_test.go —— 代理解析（第九十七刀 · W1）。
//
// 对账 TS `src/tools/net/proxy-resolver.ts`（209 行）。
//
// # 覆盖策略
//
// 纯函数为主（`ParseScutilProxy` / `ParseWindowsProxyOutput` / `ShouldBypassProxy` /
// `normalizeProxyURL`）——避免 mock `exec`（Go 的 `exec.Command` 替换需改包变量，
// 成本高且脆弱）。`ResolveProxyForURL` 用 `t.Setenv` 控制环境变量。

// ── ShouldBypassProxy ───────────────────────────────────────────────────

func TestShouldBypassProxy(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		noProxy string
		want    bool
	}{
		{"空列表不绕过", "example.com", "", false},
		{"星号绕过所有", "anything.com", "*", true},
		{"精确匹配", "example.com", "example.com", true},
		{"精确匹配大小写不敏感", "Example.COM", "example.com", true},
		{"精确匹配不含子域", "api.example.com", "example.com", false},
		{"点前缀匹配子域", "api.example.com", ".example.com", true},
		{"点前缀也匹配裸域", "example.com", ".example.com", true},
		{"点前缀不含无关域", "notexample.com", ".example.com", false},
		{"逗号分隔多项", "b.com", "a.com, b.com", true},
		{"逗号分隔多项未命中", "c.com", "a.com, b.com", false},
		{"空白项被忽略", "a.com", " , ,a.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 显式传 noProxy，避开环境变量干扰
			t.Setenv("NO_PROXY", "")
			if got := ShouldBypassProxy(c.host, c.noProxy); got != c.want {
				t.Errorf("ShouldBypassProxy(%q, %q) = %v，期望 %v", c.host, c.noProxy, got, c.want)
			}
		})
	}
}

// TestShouldBypassProxyFallsBackToEnv —— noProxy 为空时读 NO_PROXY 环境变量。
func TestShouldBypassProxyFallsBackToEnv(t *testing.T) {
	t.Setenv("NO_PROXY", ".env-proxy.com")
	if !ShouldBypassProxy("api.env-proxy.com", "") {
		t.Error("noProxy 为空时应回退到 NO_PROXY 环境变量")
	}
	// 小写变体也要认（envCaseInsensitive）
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", ".lower.com")
	if !ShouldBypassProxy("x.lower.com", "") {
		t.Error("应认小写 no_proxy 环境变量")
	}
}

// ── ParseScutilProxy ────────────────────────────────────────────────────

func TestParseScutilProxy(t *testing.T) {
	t.Run("HTTPS 优先", func(t *testing.T) {
		out := `
HTTPEnable : 1
HTTPPort : 8080
HTTPProxy : 10.0.0.1
HTTPSEnable : 1
HTTPSPort : 7890
HTTPSProxy : 127.0.0.1
`
		if got := ParseScutilProxy(out); got != "http://127.0.0.1:7890" {
			t.Errorf("应取 HTTPS，实得 %q", got)
		}
	})
	t.Run("仅 HTTP 时取 HTTP", func(t *testing.T) {
		out := `
HTTPEnable : 1
HTTPPort : 8080
HTTPProxy : 10.0.0.1
HTTPSEnable : 0
`
		if got := ParseScutilProxy(out); got != "http://10.0.0.1:8080" {
			t.Errorf("应取 HTTP，实得 %q", got)
		}
	})
	t.Run("全部禁用返回空", func(t *testing.T) {
		out := "HTTPEnable : 0\nHTTPSEnable : 0\n"
		if got := ParseScutilProxy(out); got != "" {
			t.Errorf("应返回空，实得 %q", got)
		}
	})
	t.Run("PAC 不处理", func(t *testing.T) {
		out := "ProxyAutoConfigEnable : 1\nProxyAutoConfigURLString : http://pac/x\n"
		if got := ParseScutilProxy(out); got != "" {
			t.Errorf("PAC 应返回空，实得 %q", got)
		}
	})
	t.Run("启用但缺 host 或 port 返回空", func(t *testing.T) {
		out := "HTTPEnable : 1\nHTTPProxy : 10.0.0.1\n"
		if got := ParseScutilProxy(out); got != "" {
			t.Errorf("缺 port 应返回空，实得 %q", got)
		}
	})
	t.Run("空输入", func(t *testing.T) {
		if got := ParseScutilProxy(""); got != "" {
			t.Errorf("空输入应返回空，实得 %q", got)
		}
	})
}

func TestParseScutilProxyRealMacOSFormat(t *testing.T) {
	// 真实 `scutil --proxy` 输出片段（含 ExceptionsList 嵌套字典）
	out := `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
  }
  FTPPassive : 1
  HTTPEnable : 0
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
}`
	if got := ParseScutilProxy(out); got != "http://127.0.0.1:7890" {
		t.Errorf("应解析出 HTTPS 代理，实得 %q", got)
	}
}

// ── ParseWindowsProxyOutput ─────────────────────────────────────────────

func TestParseWindowsProxyOutput(t *testing.T) {
	const enableOn = "HKEY_CURRENT_USER\\...\\Internet Settings\n    ProxyEnable    REG_DWORD    0x1\n"
	const enableOff = "HKEY_CURRENT_USER\\...\\Internet Settings\n    ProxyEnable    REG_DWORD    0x0\n"

	t.Run("启用 + 裸 host:port → 补 http://", func(t *testing.T) {
		server := "    ProxyServer    REG_SZ    127.0.0.1:7890\n"
		if got := ParseWindowsProxyOutput(enableOn, server); got != "http://127.0.0.1:7890" {
			t.Errorf("实得 %q", got)
		}
	})
	t.Run("★ 禁用时即使 ProxyServer 残留也返回空", func(t *testing.T) {
		server := "    ProxyServer    REG_SZ    127.0.0.1:7890\n"
		if got := ParseWindowsProxyOutput(enableOff, server); got != "" {
			t.Errorf("禁用代理应返回空，实得 %q", got)
		}
	})
	t.Run("已带 scheme 不重复补", func(t *testing.T) {
		server := "    ProxyServer    REG_SZ    http://proxy:8080\n"
		if got := ParseWindowsProxyOutput(enableOn, server); got != "http://proxy:8080" {
			t.Errorf("实得 %q", got)
		}
	})
	t.Run("多协议格式取 https", func(t *testing.T) {
		server := "    ProxyServer    REG_SZ    http=h:1;https=h:2\n"
		if got := ParseWindowsProxyOutput(enableOn, server); got != "http://h:2" {
			t.Errorf("应取 https 段，实得 %q", got)
		}
	})
	t.Run("多协议格式无 https 时取 http", func(t *testing.T) {
		server := "    ProxyServer    REG_SZ    http=h:1\n"
		if got := ParseWindowsProxyOutput(enableOn, server); got != "http://h:1" {
			t.Errorf("应取 http 段，实得 %q", got)
		}
	})
	t.Run("启用但 ProxyServer 缺失返回空", func(t *testing.T) {
		if got := ParseWindowsProxyOutput(enableOn, ""); got != "" {
			t.Errorf("实得 %q", got)
		}
	})
}

// ── ResolveProxyForURL ──────────────────────────────────────────────────

func TestResolveProxyForURLPrefersConfig(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy:1")
	t.Setenv("NO_PROXY", "")
	opts := &ProxyResolverOptions{ProxyURL: "http://config-proxy:2"}
	if got := ResolveProxyForURL("https://example.com/", opts); got != "http://config-proxy:2" {
		t.Errorf("config 应优先于环境变量，实得 %q", got)
	}
}

func TestResolveProxyForURLEnvByScheme(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://https-proxy:1")
	t.Setenv("HTTP_PROXY", "http://http-proxy:2")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	if got := ResolveProxyForURL("https://example.com/", nil); got != "http://https-proxy:1" {
		t.Errorf("https 应取 HTTPS_PROXY，实得 %q", got)
	}
	if got := ResolveProxyForURL("http://example.com/", nil); got != "http://http-proxy:2" {
		t.Errorf("http 应取 HTTP_PROXY，实得 %q", got)
	}
}

func TestResolveProxyForURLHttpsFallbackToHTTPProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "http://http-proxy:2")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")

	if got := ResolveProxyForURL("https://example.com/", nil); got != "http://http-proxy:2" {
		t.Errorf("HTTPS_PROXY 缺失应回退 HTTP_PROXY，实得 %q", got)
	}
}

func TestResolveProxyForURLNoProxyWins(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy:1")
	t.Setenv("NO_PROXY", "")
	opts := &ProxyResolverOptions{ProxyURL: "http://config:2", NoProxy: "example.com"}
	if got := ResolveProxyForURL("https://example.com/", opts); got != "" {
		t.Errorf("NO_PROXY 命中应直连（即使 config 设了代理），实得 %q", got)
	}
}

func TestResolveProxyForURLDirectWhenNothingConfigured(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")
	if got := ResolveProxyForURL("https://example.com/", nil); got != "" {
		t.Errorf("无任何配置应直连，实得 %q", got)
	}
}

// TestResolveProxyForURLSystemProxyKillSwitch —— RIVET_NO_SYSTEM_PROXY=1 关掉 OS 回退。
//
// # 为什么要注入探测值（变异反证 M4 的教训）
//
// 首版测试只断言「返回空」——**失去区分力**：开发机 macOS 本来就没开系统代理时，
// `ReadSystemProxy()` 恒返回空，「kill switch 生效」与「kill switch 失效」产生
// **相同结果**，把 kill switch 整段删掉测试照样绿（M4 红 0）。
//
// 修法：把 OS 探测替换成**非空的固定值**。这样：
//   - kill switch 生效 → 返回 ""（本测试断言）
//   - kill switch 失效 → 返回该固定值 → **红**
//
// 这同时是真实的可测性改进：OS 探测从此可确定性验证，不依赖开发机状态。
func TestResolveProxyForURLSystemProxyKillSwitch(t *testing.T) {
	restore := systemProxyProbe
	t.Cleanup(func() { systemProxyProbe = restore })
	systemProxyProbe = func() string { return "http://os-system-proxy:9999" }

	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")

	t.Run("kill switch 开启 → 直连", func(t *testing.T) {
		t.Setenv("RIVET_NO_SYSTEM_PROXY", "1")
		if got := ResolveProxyForURL("https://example.com/", nil); got != "" {
			t.Errorf("kill switch 应禁用系统代理回退，实得 %q", got)
		}
	})

	// 反向对照：**不加 kill switch 时应返回系统代理**。
	// 没有这一半，kill switch 测试仍可能是同义反复——两半合起来才证明
	// 「系统代理确实被读到了，且 kill switch 确实能拦住它」。
	t.Run("kill switch 关闭 → 取系统代理", func(t *testing.T) {
		t.Setenv("RIVET_NO_SYSTEM_PROXY", "")
		if got := ResolveProxyForURL("https://example.com/", nil); got != "http://os-system-proxy:9999" {
			t.Errorf("无 kill switch 时应取系统代理，实得 %q", got)
		}
	})
}

func TestResolveProxyForURLInvalidURL(t *testing.T) {
	// 无法解析的 URL → 空（直连），不 panic
	if got := ResolveProxyForURL("://bad", nil); got != "" {
		t.Errorf("非法 URL 应返回空，实得 %q", got)
	}
}

// ── normalizeProxyURL（经 ParseWindowsProxyOutput 间接覆盖）──

func TestNormalizeProxyURLViaWindowsParse(t *testing.T) {
	const on = "ProxyEnable    REG_DWORD    0x1"
	cases := []struct {
		server string
		want   string
	}{
		{"ProxyServer    REG_SZ    10.0.0.1:3128", "http://10.0.0.1:3128"},
		{"ProxyServer    REG_SZ    https://10.0.0.1:3128", "https://10.0.0.1:3128"},
		{"ProxyServer    REG_SZ    HTTP=10.0.0.1:1;HTTPS=10.0.0.1:2", "http://10.0.0.1:2"},
		{"ProxyServer    REG_SZ    =bad", ""},
	}
	for _, c := range cases {
		if got := ParseWindowsProxyOutput(on, c.server); got != c.want {
			t.Errorf("ParseWindowsProxyOutput(%q) = %q，期望 %q", c.server, got, c.want)
		}
	}
}
