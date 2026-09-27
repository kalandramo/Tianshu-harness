package search

import (
	"time"

	"github.com/kalandramo/tianshu/go/internal/net"
)

// build.go —— 从配置构造有序后端链（第九十七刀 · W5）。
//
// 对账 TS `src/tools/web-search/build-backends.ts`（107 行）。

// BuildOptions 是链构造的依赖注入（测试与配置层用）。
type BuildOptions struct {
	// Fetch 覆盖生产抓取（测试注入桩）。为空时用 NewHTTPFetch。
	Fetch Fetch
	// Proxy 是代理解析选项（仅当 Fetch 为空时生效——对账 TS：
	// 「注入的测试 fetch 原样使用，真实 fetch 才包代理」）。
	Proxy *net.ProxyResolverOptions
	// Env 覆盖环境变量读取（测试用）。目前 Go 侧直接用 os.Getenv，
	// 故此项保留给未来扩展——留空即用真实环境。
	// （对账 TS 的 `deps.env ?? process.env`。）
}

// BuildBackends 按配置构造有序后端链。
//
// 对账 TS `buildSearchBackends`：
//
//   - 需要 key 的后端**总是构造**（可用性由调用时的 `IsAvailable()` 判定），
//     这样「列出但未配置」的后端会落到链序下一个
//   - **未知后端名跳过**（不让一个笔误废掉整条链）
//   - 若最终链为空，**加 DuckDuckGo 作零配置兜底**
func BuildBackends(cfg SearchConfig, opts BuildOptions) []Backend {
	fetch := opts.Fetch
	if fetch == nil {
		fetch = NewHTTPFetch(HTTPFetchOptions{
			Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
			Proxy:   opts.Proxy,
		})
	}

	backends := []Backend{}
	for _, name := range cfg.Backends {
		switch name {
		case "bing":
			backends = append(backends, NewBingBackend(fetch))
		case "duckduckgo":
			backends = append(backends, NewDuckDuckGoBackend(fetch))
		case "brave":
			backends = append(backends, NewBraveBackend(fetch,
				ResolveSearchKey(cfg, BackendBrave), cfg.Region))
		case "tavily":
			backends = append(backends, NewTavilyBackend(fetch,
				ResolveSearchKey(cfg, BackendTavily)))
		case "bocha":
			backends = append(backends, NewBochaBackend(fetch,
				ResolveSearchKey(cfg, BackendBocha)))
		default:
			// 未知后端名——跳过而非让整条链失败（对账 TS 注释）。
		}
	}

	if len(backends) == 0 {
		backends = append(backends, NewDuckDuckGoBackend(fetch))
	}
	return backends
}
