package lsp

import (
	"strings"
	"testing"
)

// fakeWhich 按给定的「已安装集合」回答探测（对账 TS 的注入式 `which`）。
func fakeWhich(installed ...string) WhichFunc {
	set := make(map[string]bool, len(installed))
	for _, b := range installed {
		set[b] = true
	}
	return func(bin string) bool { return set[bin] }
}

// TestRegistry_EntryCount —— 注册表条目数与 TS 逐条对齐。
//
// TS `LSP_SERVERS` 是 26 条（`server-registry.ts:43-105`）。
func TestRegistry_EntryCount(t *testing.T) {
	if got := len(LSP_SERVERS); got != 26 {
		t.Errorf("条目数应为 26（对账 TS LSP_SERVERS），实得 %d", got)
	}
}

// TestRegistry_TypeScriptEntryExact —— TS 条目逐字段对账（含 alwaysAvailable）。
//
// TS：
//
//	{ id:'typescript', extensions:['.ts','.tsx','.js','.jsx','.mjs','.cjs','.mts','.cts'],
//	  command:'npx', args:['-y','typescript-language-server','--stdio'],
//	  languageId:'typescript', languageIdByExt:{...}, alwaysAvailable:true }
func TestRegistry_TypeScriptEntryExact(t *testing.T) {
	def := serverDefByID("typescript")
	if def == nil {
		t.Fatal("找不到 typescript 条目")
	}
	if def.Command != "npx" {
		t.Errorf("command 应为 npx，实得 %q", def.Command)
	}
	if strings.Join(def.Args, " ") != "-y typescript-language-server --stdio" {
		t.Errorf("args 不符：%v", def.Args)
	}
	if def.LanguageID != "typescript" {
		t.Errorf("languageId 应为 typescript，实得 %q", def.LanguageID)
	}
	if !def.AlwaysAvailable {
		t.Error("typescript 应 alwaysAvailable:true（npx 兜底）")
	}
	wantExts := []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}
	if strings.Join(def.Extensions, ",") != strings.Join(wantExts, ",") {
		t.Errorf("extensions 不符\n want %v\n  got %v", wantExts, def.Extensions)
	}
	// languageIdByExt 细化
	for ext, want := range map[string]string{
		".ts": "typescript", ".tsx": "typescriptreact",
		".js": "javascript", ".jsx": "javascriptreact",
		".mjs": "javascript", ".cjs": "javascript",
		".mts": "typescript", ".cts": "typescript",
	} {
		if got := def.LanguageIDByExt[ext]; got != want {
			t.Errorf("languageIdByExt[%s] 应为 %q，实得 %q", ext, want, got)
		}
	}
}

// TestRegistry_ClangdLanguageIdByExt —— 第二个用 languageIdByExt 的条目
// （C/C++ 家族同 server 不同 id）。
func TestRegistry_ClangdLanguageIdByExt(t *testing.T) {
	def := serverDefByID("clangd")
	if def == nil {
		t.Fatal("找不到 clangd 条目")
	}
	for ext, want := range map[string]string{
		".c": "c", ".h": "c",
		".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp",
		".hpp": "cpp", ".hh": "cpp", ".hxx": "cpp",
	} {
		if got := def.LanguageIDByExt[ext]; got != want {
			t.Errorf("clangd languageIdByExt[%s] 应为 %q，实得 %q", ext, want, got)
		}
	}
}

// TestRegistry_CSharpTwoCandidatesInOrder —— 同扩展名多候选，**序**有意义。
//
// TS 注释：roslyn-language-server 优先，csharp-ls 兜底。
// 序错了会导致「装了官方 server 却用了第三方」。
func TestRegistry_CSharpTwoCandidatesInOrder(t *testing.T) {
	defs := ServerDefsForExt(".cs")
	if len(defs) != 2 {
		t.Fatalf("`.cs` 应有 2 个候选，实得 %d", len(defs))
	}
	if defs[0].ID != "roslyn-language-server" {
		t.Errorf("首个候选应为 roslyn-language-server，实得 %q", defs[0].ID)
	}
	if defs[1].ID != "csharp-ls" {
		t.Errorf("次个候选应为 csharp-ls，实得 %q", defs[1].ID)
	}
}

// TestRegistry_RubyTwoCandidates —— ruby 同样两候选（ruby-lsp 优先）。
func TestRegistry_RubyTwoCandidates(t *testing.T) {
	defs := ServerDefsForExt(".rb")
	if len(defs) != 2 {
		t.Fatalf("`.rb` 应有 2 个候选，实得 %d", len(defs))
	}
	if defs[0].ID != "ruby-lsp" || defs[1].ID != "solargraph" {
		t.Errorf("候选序不符：%s, %s", defs[0].ID, defs[1].ID)
	}
}

// TestRegistry_PhpTwoCandidates —— php 两候选（intelephense 优先）。
func TestRegistry_PhpTwoCandidates(t *testing.T) {
	defs := ServerDefsForExt(".php")
	if len(defs) != 2 {
		t.Fatalf("`.php` 应有 2 个候选，实得 %d", len(defs))
	}
	if defs[0].ID != "intelephense" || defs[1].ID != "phpactor" {
		t.Errorf("候选序不符：%s, %s", defs[0].ID, defs[1].ID)
	}
}

// TestRegistry_ServerForFilePicksFirstInstalled —— ★ 核心选择逻辑。
//
// TS：`serverDefsForExt(ext).find(def => isServerAvailable(def, which)) ?? null`
// ——遍历候选取**第一个已安装**的，不是「只看首选」。
//
// 判别力：只装**次候选**时必须返回次候选（若实现只看首选 → 返回 null → 必红）。
func TestRegistry_ServerForFilePicksFirstInstalled(t *testing.T) {
	// 只装 csharp-ls（不装 roslyn）
	w := fakeWhich("csharp-ls")
	def := ServerForFile("Program.cs", w)
	if def == nil {
		t.Fatal("装了备选 C# server 时应能选中")
	}
	if def.ID != "csharp-ls" {
		t.Errorf("应选已安装的 csharp-ls，实得 %q", def.ID)
	}

	// 两个都装 → 取首选 roslyn
	w2 := fakeWhich("csharp-ls", "roslyn-language-server")
	if got := ServerForFile("Program.cs", w2); got == nil || got.ID != "roslyn-language-server" {
		t.Errorf("两个都装时应取首选 roslyn，实得 %v", got)
	}

	// 都不装 → null
	if got := ServerForFile("Program.cs", fakeWhich()); got != nil {
		t.Errorf("都不装时应返回 nil，实得 %v", got.ID)
	}
}

// TestRegistry_AlwaysAvailableBypassesWhich —— typescript 的 alwaysAvailable
// 让它**不需探测**即为可用。
//
// 判别力：若实现忽略 alwaysAvailable 而去 which("npx")，本用例（空 PATH）会红。
func TestRegistry_AlwaysAvailableBypassesWhich(t *testing.T) {
	called := false
	w := func(bin string) bool { called = true; return false }
	def := ServerForFile("a.ts", w)
	if def == nil {
		t.Fatal("typescript 应 alwaysAvailable，即便 which 全 false 也应可选")
	}
	if def.ID != "typescript" {
		t.Errorf("应选 typescript，实得 %q", def.ID)
	}
	if called {
		t.Error("alwaysAvailable 的条目**不该**触发 which 探测（无谓的进程开销）")
	}
}

// TestRegistry_BinaryOverride —— `binary` 字段覆盖探测目标
// （TS：`which(def.binary ?? def.command)`）。
func TestRegistry_BinaryOverride(t *testing.T) {
	// haskell 的 command 是 haskell-language-server-wrapper；注册表里没有覆盖
	// binary，故探测目标 == command。
	def := serverDefByID("haskell-language-server")
	if def == nil {
		t.Fatal("找不到 haskell-language-server 条目")
	}
	if def.Command != "haskell-language-server-wrapper" {
		t.Errorf("command 应为 wrapper，实得 %q", def.Command)
	}
	if def.Args[0] != "--lsp" {
		t.Errorf("args 应为 --lsp，实得 %v", def.Args)
	}
	// 探测目标按 binary ?? command
	if got := probeTarget(def); got != "haskell-language-server-wrapper" {
		t.Errorf("探测目标应为 command，实得 %q", got)
	}
}

// TestRegistry_HasServerForFileDoesNotProbeInstall —— ★ 关键语义差异。
//
// TS：`hasServerForFile(filePath) { return serverDefForExt(extOf(filePath)) !== null }`
// ——**只看是否注册过**（`serverDefForExt` 不判安装）。
// 用于「诊断是否该触发」，故必须与「是否装了 server」解耦。
//
// 判别力：若实现里加了安装探测，未装 gopls 的环境下 `.go` 会返回 false → 红。
func TestRegistry_HasServerForFileDoesNotProbeInstall(t *testing.T) {
	if !HasServerForFile("main.go") {
		t.Error("`.go` 已注册，hasServerForFile 应为 true（不看是否安装）")
	}
	if !HasServerForFile("a.py") {
		t.Error("`.py` 已注册，应为 true")
	}
	if HasServerForFile("a.zzz") {
		t.Error("`.zzz` 未注册，应为 false")
	}
	if HasServerForFile("noext") {
		t.Error("无扩展名，应为 false")
	}
}

// TestRegistry_LanguageIDForFileFallsBackToDefault —— 细化优先、回落默认。
func TestRegistry_LanguageIDForFileFallsBackToDefault(t *testing.T) {
	ts := serverDefByID("typescript")
	if got := LanguageIDForFile(ts, "a.tsx"); got != "typescriptreact" {
		t.Errorf("`.tsx` 应细化出 typescriptreact，实得 %q", got)
	}
	// 未在 languageIdByExt 里的扩展名 → 回落 def.LanguageID
	if got := LanguageIDForFile(ts, "a.weird"); got != "typescript" {
		t.Errorf("未细化时回落 def.LanguageID，实得 %q", got)
	}
	// gopls 无 languageIdByExt → 恒回落
	gopls := serverDefByID("gopls")
	if got := LanguageIDForFile(gopls, "main.go"); got != "go" {
		t.Errorf("gopls 的 languageId 应为 go，实得 %q", got)
	}
}

// TestRegistry_ExtOfSemantics —— 扩展名提取（对账 TS `extOf`）。
//
// TS：`const i = filePath.lastIndexOf('.'); return i >= 0 ? filePath.slice(i).toLowerCase() : ”`
// ——**最后一个点**、**小写**、无点则空串。
func TestRegistry_ExtOfSemantics(t *testing.T) {
	for in, want := range map[string]string{
		"a.ts":                  ".ts",
		"dir.with.dots/a.TS":    ".ts", // 小写化
		"dir.with.dots/a.tsx":   ".tsx",
		"noext":                 "",
		"trailing.":             ".",
		"/abs/path/FILE.MJS":    ".mjs",
		"a.b/c.d/file.Js":       ".js",
		"weird.tar.gz":          ".gz", // 最后一个点
		"hidden.noext/.env":     ".env",
		"dir/file":              "",
		"a.PY":                  ".py",
		"x.cpp":                 ".cpp",
		"nested/deep/readme.md": ".md",
	} {
		if got := extOf(in); got != want {
			t.Errorf("extOf(%q) 应为 %q，实得 %q", in, want, got)
		}
	}
}

// TestRegistry_ServerDefsForExtAcceptsBareExt —— 对账 TS：
// `const e = ext.startsWith('.') ? ext.toLowerCase() : '.' + ext.toLowerCase()`
// ——传 "ts" 与 ".ts" 等价。
func TestRegistry_ServerDefsForExtAcceptsBareExt(t *testing.T) {
	a := ServerDefsForExt(".ts")
	b := ServerDefsForExt("ts")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("两种写法都应命中 1 条，实得 %d / %d", len(a), len(b))
	}
	if a[0].ID != b[0].ID {
		t.Errorf("两种写法应命中同一条：%q vs %q", a[0].ID, b[0].ID)
	}
	// 大小写不敏感
	c := ServerDefsForExt(".TS")
	if len(c) != 1 {
		t.Errorf("大写扩展名也应命中，实得 %d", len(c))
	}
}

// TestRegistry_ServerDefForExtIsFirstCandidate —— 首选（不判安装）。
func TestRegistry_ServerDefForExtIsFirstCandidate(t *testing.T) {
	if got := ServerDefForExt(".cs"); got == nil || got.ID != "roslyn-language-server" {
		t.Errorf("`.cs` 首选应为 roslyn-language-server，实得 %v", got)
	}
	if got := ServerDefForExt(".zzz"); got != nil {
		t.Errorf("未知扩展名应为 nil，实得 %v", got)
	}
}

// TestRegistry_AvailableServers —— 只列已安装的。
func TestRegistry_AvailableServers(t *testing.T) {
	// typescript 恒可用（alwaysAvailable）+ 显式装 gopls
	got := AvailableServers(fakeWhich("gopls"))
	ids := make(map[string]bool, len(got))
	for _, d := range got {
		ids[d.ID] = true
	}
	if !ids["typescript"] {
		t.Error("typescript 应恒可用（alwaysAvailable）")
	}
	if !ids["gopls"] {
		t.Error("gopls 已装，应可用")
	}
	if ids["pyright"] {
		t.Error("pyright 未装，不该出现")
	}
}

// TestRegistry_AllEntriesHaveRequiredFields —— 数据完整性守卫。
//
// 26 条里任何一条缺 id/command/languageId 或 extensions 为空，都会在运行期
// 变成「静默无 LSP」，故用测试兜住。
func TestRegistry_AllEntriesHaveRequiredFields(t *testing.T) {
	seen := make(map[string]int, len(LSP_SERVERS))
	for i, d := range LSP_SERVERS {
		if d.ID == "" {
			t.Errorf("第 %d 条缺 id", i)
		}
		if d.Command == "" {
			t.Errorf("%s 缺 command", d.ID)
		}
		if d.LanguageID == "" {
			t.Errorf("%s 缺 languageId", d.ID)
		}
		if len(d.Extensions) == 0 {
			t.Errorf("%s 的 extensions 为空", d.ID)
		}
		for _, e := range d.Extensions {
			if !strings.HasPrefix(e, ".") {
				t.Errorf("%s 的扩展名 %q 应以 '.' 开头", d.ID, e)
			}
			if e != strings.ToLower(e) {
				t.Errorf("%s 的扩展名 %q 应小写", d.ID, e)
			}
		}
		// languageIdByExt 的键必须都在 extensions 内（否则是死数据）
		for k := range d.LanguageIDByExt {
			found := false
			for _, e := range d.Extensions {
				if e == k {
					found = true
				}
			}
			if !found {
				t.Errorf("%s 的 languageIdByExt 键 %q 不在 extensions 里", d.ID, k)
			}
		}
		seen[d.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("id %q 重复出现 %d 次", id, n)
		}
	}
}

// TestRegistry_WhichCacheIsUsed —— 探测结果被缓存（对账 TS `whichCache`）。
//
// TS 的理由注释：注册表扩到几十个 server 后 `serverForFile` 会在**每次**
// LSP 调用上被触达——无缓存时每次都要同步 spawn 一次 which。
func TestRegistry_WhichCacheIsUsed(t *testing.T) {
	ClearWhichCache()
	calls := 0
	countingWhich := func(bin string) bool { calls++; return true }

	// 用 CachingWhich 包一层计数器
	cached := CachingWhich(countingWhich)
	for i := 0; i < 5; i++ {
		_ = cached("gopls")
	}
	if calls != 1 {
		t.Errorf("同一 binary 的探测应只发生 1 次（缓存），实得 %d 次", calls)
	}

	ClearWhichCache()
	_ = cached("gopls")
	if calls != 2 {
		t.Errorf("清缓存后应重新探测，实得 %d 次", calls)
	}
	ClearWhichCache()
}
