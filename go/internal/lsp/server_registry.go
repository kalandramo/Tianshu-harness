package lsp

import (
	"strings"
	"sync"
)

// LspServerDef 是语言服务器的注册表条目。
//
// 对账 TS `LspServerDef`（`server-registry.ts`）。
type LspServerDef struct {
	ID         string
	Extensions []string
	Command    string
	Args       []string
	// LanguageID 是该 server 的默认 LSP languageId。
	LanguageID string
	// LanguageIDByExt 是按扩展名细化的 languageId——只有同一 server 下不同
	// 扩展名用不同 id 时才需要（TS 家族 / C 家族）。
	LanguageIDByExt map[string]string
	// Binary 是必须在 PATH 上的可执行名（缺省用 Command）。
	Binary string
	// AlwaysAvailable 表示「启动器假定存在，不做 PATH 探测」（如 npx）。
	AlwaysAvailable bool
}

// LSP_SERVERS 是已知语言服务器表（**逐条对账 TS `LSP_SERVERS`**）。
//
// 顺序有意义：同扩展名的多候选按此序取「第一个已安装」。
//
// `Args` 按各 server 官方文档的 stdio 启动形式书写——写错会让 spawn 失败并
// 静默降级到无 LSP（`roslyn-language-server` 的 `--stdio` 尤其必须显式给，
// 它默认不走 stdio）。
var LSP_SERVERS = []LspServerDef{
	{
		ID:         "typescript",
		Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"},
		Command:    "npx",
		Args:       []string{"-y", "typescript-language-server", "--stdio"},
		LanguageID: "typescript",
		LanguageIDByExt: map[string]string{
			".ts": "typescript", ".tsx": "typescriptreact",
			".js": "javascript", ".jsx": "javascriptreact",
			".mjs": "javascript", ".cjs": "javascript",
			".mts": "typescript", ".cts": "typescript",
		},
		AlwaysAvailable: true,
	},
	{ID: "pyright", Extensions: []string{".py", ".pyi"}, Command: "pyright-langserver", Args: []string{"--stdio"}, LanguageID: "python"},
	{ID: "gopls", Extensions: []string{".go"}, Command: "gopls", LanguageID: "go"},
	{ID: "rust-analyzer", Extensions: []string{".rs"}, Command: "rust-analyzer", LanguageID: "rust"},
	{
		ID:         "clangd",
		Extensions: []string{".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"},
		Command:    "clangd",
		LanguageID: "c",
		LanguageIDByExt: map[string]string{
			".c": "c", ".h": "c",
			".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp",
			".hpp": "cpp", ".hh": "cpp", ".hxx": "cpp",
		},
	},
	// jdtls 需要 Java 21+ 运行时（JAVA_HOME 或 PATH 上），且建议用 -data 指定
	// per-project workspace——这两点由用户的安装/包装脚本决定，天枢不代管。
	{ID: "jdtls", Extensions: []string{".java"}, Command: "jdtls", LanguageID: "java"},
	// C#：官方 roslyn-language-server 优先，csharp-ls 兜底。
	{ID: "roslyn-language-server", Extensions: []string{".cs"}, Command: "roslyn-language-server", Args: []string{"--stdio"}, LanguageID: "csharp"},
	{ID: "csharp-ls", Extensions: []string{".cs"}, Command: "csharp-ls", LanguageID: "csharp"},
	{ID: "kotlin-language-server", Extensions: []string{".kt", ".kts"}, Command: "kotlin-language-server", LanguageID: "kotlin"},
	{ID: "sourcekit-lsp", Extensions: []string{".swift"}, Command: "sourcekit-lsp", LanguageID: "swift"},
	{ID: "dart", Extensions: []string{".dart"}, Command: "dart", Args: []string{"language-server", "--protocol=lsp"}, LanguageID: "dart"},
	{ID: "metals", Extensions: []string{".scala", ".sbt"}, Command: "metals", LanguageID: "scala"},
	{ID: "intelephense", Extensions: []string{".php"}, Command: "intelephense", Args: []string{"--stdio"}, LanguageID: "php"},
	{ID: "phpactor", Extensions: []string{".php"}, Command: "phpactor", Args: []string{"language-server"}, LanguageID: "php"},
	{ID: "ruby-lsp", Extensions: []string{".rb", ".rake", ".gemspec"}, Command: "ruby-lsp", LanguageID: "ruby"},
	{ID: "solargraph", Extensions: []string{".rb", ".rake", ".gemspec"}, Command: "solargraph", Args: []string{"stdio"}, LanguageID: "ruby"},
	{ID: "lua-language-server", Extensions: []string{".lua"}, Command: "lua-language-server", LanguageID: "lua"},
	{ID: "zls", Extensions: []string{".zig", ".zon"}, Command: "zls", LanguageID: "zig"},
	{ID: "bash-language-server", Extensions: []string{".sh", ".bash", ".zsh"}, Command: "bash-language-server", Args: []string{"start"}, LanguageID: "shellscript"},
	{ID: "terraform-ls", Extensions: []string{".tf", ".tfvars"}, Command: "terraform-ls", Args: []string{"serve"}, LanguageID: "terraform"},
	{ID: "clojure-lsp", Extensions: []string{".clj", ".cljs", ".cljc", ".edn"}, Command: "clojure-lsp", LanguageID: "clojure"},
	{ID: "ocamllsp", Extensions: []string{".ml", ".mli"}, Command: "ocamllsp", LanguageID: "ocaml"},
	{ID: "haskell-language-server", Extensions: []string{".hs", ".lhs"}, Command: "haskell-language-server-wrapper", Args: []string{"--lsp"}, LanguageID: "haskell"},
	{ID: "nil", Extensions: []string{".nix"}, Command: "nil", LanguageID: "nix"},
	{ID: "vue-language-server", Extensions: []string{".vue"}, Command: "vue-language-server", Args: []string{"--stdio"}, LanguageID: "vue"},
	{ID: "svelteserver", Extensions: []string{".svelte"}, Command: "svelteserver", Args: []string{"--stdio"}, LanguageID: "svelte"},
}

// WhichFunc 探测某个可执行名是否在 PATH 上。
//
// 可注入（对账 TS 的 `WhichFn`）——测试不打真实 PATH。
type WhichFunc func(bin string) bool

// whichMu 保护 whichCache（TS 是单线程，Go 侧多 goroutine 可能并发触达）。
var (
	whichMu    sync.Mutex
	whichCache = map[string]bool{}
)

// ClearWhichCache 清空探测缓存（测试隔离；或会话中途装了 server 后强制重探）。
func ClearWhichCache() {
	whichMu.Lock()
	defer whichMu.Unlock()
	whichCache = map[string]bool{}
}

// CachingWhich 用进程级缓存包装探测函数。
//
// 对账 TS 的 `defaultWhich` + 模块级 `whichCache`。理由（TS 注释原文）：
// 注册表扩到几十个 server 后 `serverForFile` 会在**每次** LSP 调用上被触达
// ——无缓存时每次都要同步 spawn 一次 which，启动与逐次调用都会被拖住。
func CachingWhich(inner WhichFunc) WhichFunc {
	return func(bin string) bool {
		whichMu.Lock()
		cached, ok := whichCache[bin]
		whichMu.Unlock()
		if ok {
			return cached
		}
		found := inner(bin)
		whichMu.Lock()
		whichCache[bin] = found
		whichMu.Unlock()
		return found
	}
}

// defaultWhich 用 `which` / `where` 真探 PATH（缓存）。
//
// 对账 TS `probeWhich`：`execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin], { stdio: 'ignore', timeout: 800, windowsHide: true })`。
func defaultWhich(bin string) bool {
	return probeWhich(bin)
}

// DefaultWhich 是供外部注入的默认探测器（带缓存）。
var DefaultWhich WhichFunc = CachingWhich(defaultWhich)

// probeTarget 返回该条目的探测目标（对账 TS `def.binary ?? def.command`）。
func probeTarget(def *LspServerDef) string {
	if def.Binary != "" {
		return def.Binary
	}
	return def.Command
}

// extOf 提取文件扩展名。
//
// 对账 TS `extOf`：
//
//	const i = filePath.lastIndexOf('.')
//	return i >= 0 ? filePath.slice(i).toLowerCase() : ''
//
// **最后一个点**而非第一个：`weird.tar.gz` → `.gz`。
// 注意 `.env` 这类隐藏文件也会得到 `.env`（TS 同样如此——没有特判）。
func extOf(filePath string) string {
	i := strings.LastIndex(filePath, ".")
	if i < 0 {
		return ""
	}
	return strings.ToLower(filePath[i:])
}

// normalizeExt 把裸扩展名（"ts"）补成带点形式（对账 TS 的 `ext.startsWith('.') ? … : '.' + …`）。
func normalizeExt(ext string) string {
	e := strings.ToLower(ext)
	if strings.HasPrefix(e, ".") {
		return e
	}
	return "." + e
}

// ServerDefsForExt 返回该扩展名的全部候选（按 LSP_SERVERS 序）。
func ServerDefsForExt(ext string) []LspServerDef {
	e := normalizeExt(ext)
	out := make([]LspServerDef, 0, 2)
	for _, s := range LSP_SERVERS {
		for _, x := range s.Extensions {
			if x == e {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// ServerDefForExt 返回该扩展名的**首选**（不判安装）。
func ServerDefForExt(ext string) *LspServerDef {
	defs := ServerDefsForExt(ext)
	if len(defs) == 0 {
		return nil
	}
	d := defs[0]
	return &d
}

// IsServerAvailable 报告该条目是否可用（对账 TS `isServerAvailable`）。
//
// `alwaysAvailable` 的条目**不触发探测**（避免对 npx 做无谓的进程 spawn）。
func IsServerAvailable(def LspServerDef, which WhichFunc) bool {
	if def.AlwaysAvailable {
		return true
	}
	return which(probeTarget(&def))
}

// ServerForFile 返回该文件的 server：候选里**第一个已安装**的，或 nil。
//
// 对账 TS `serverForFile`。遍历候选而非只看首选——否则「装了备选 C# server」
// 会被首选缺失挡住。
func ServerForFile(filePath string, which WhichFunc) *LspServerDef {
	for _, def := range ServerDefsForExt(extOf(filePath)) {
		if IsServerAvailable(def, which) {
			d := def
			return &d
		}
	}
	return nil
}

// HasServerForFile 报告该文件是否有**任何已注册**（未必已安装）的 server。
//
// 对账 TS `hasServerForFile`——用于「诊断是否该触发」，故**刻意不探安装**。
func HasServerForFile(filePath string) bool {
	return ServerDefForExt(extOf(filePath)) != nil
}

// LanguageIDForFile 返回该文件在此 server 下的 languageId
// （按扩展名细化，回落 `def.LanguageID`）。
//
// 对账 TS `languageIdForFile(def, filePath)`——**参数序是 def 在前**。
func LanguageIDForFile(def *LspServerDef, filePath string) string {
	if def == nil {
		return ""
	}
	if def.LanguageIDByExt != nil {
		if id, ok := def.LanguageIDByExt[extOf(filePath)]; ok {
			return id
		}
	}
	return def.LanguageID
}

// AvailableServers 返回本机已安装的全部 server。
func AvailableServers(which WhichFunc) []LspServerDef {
	out := make([]LspServerDef, 0, len(LSP_SERVERS))
	for _, s := range LSP_SERVERS {
		if IsServerAvailable(s, which) {
			out = append(out, s)
		}
	}
	return out
}

// serverDefByID 按 id 查条目（测试与诊断用）。
func serverDefByID(id string) *LspServerDef {
	for _, d := range LSP_SERVERS {
		if d.ID == id {
			dd := d
			return &dd
		}
	}
	return nil
}
