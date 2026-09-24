/**
 * LSP server registry — maps file extensions to language servers and detects
 * which are installed, so the agent gets go-to-definition / diagnostics for
 * many languages instead of TypeScript only.
 *
 * Pure + injectable (`which` is passed in) so selection logic is unit-testable
 * without the servers actually being installed.
 *
 * 同一个扩展名可以有多个候选 def（如 C# 的 roslyn-language-server / csharp-ls）：
 * `serverForFile` 返回第一个**已安装**的候选，用户装哪个都能用。
 */

import { execFileSync } from 'node:child_process'

export interface LspServerDef {
  id: string
  extensions: string[]
  command: string
  args: string[]
  /** 该 server 的默认 LSP languageId。 */
  languageId: string
  /**
   * 按扩展名细化的 languageId。只有同一 server 下不同扩展名用不同 id 时才需要
   * （TS 家族：.ts→typescript / .tsx→typescriptreact / .js→javascript）。
   */
  languageIdByExt?: Readonly<Record<string, string>>
  /** Binary that must exist on PATH (defaults to `command`). */
  binary?: string
  /** True when the launcher (e.g. npx) is assumed present without a PATH probe. */
  alwaysAvailable?: boolean
}

/**
 * Known servers. TypeScript is launched via `npx -y` (matching the prior
 * behavior) so it is always considered available; everything else must be
 * installed locally, otherwise the file's language simply has no LSP.
 *
 * `args` 按各 server 官方文档给出的 stdio 启动形式书写——写错会让 spawn 失败
 * 并静默降级到无 LSP（roslyn-language-server 的 `--stdio` 尤其必须显式给，
 * 它默认不走 stdio）。
 */
export const LSP_SERVERS: readonly LspServerDef[] = [
  {
    id: 'typescript',
    extensions: ['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.mts', '.cts'],
    command: 'npx',
    args: ['-y', 'typescript-language-server', '--stdio'],
    languageId: 'typescript',
    languageIdByExt: {
      '.ts': 'typescript',
      '.tsx': 'typescriptreact',
      '.js': 'javascript',
      '.jsx': 'javascriptreact',
      '.mjs': 'javascript',
      '.cjs': 'javascript',
      '.mts': 'typescript',
      '.cts': 'typescript',
    },
    alwaysAvailable: true,
  },
  { id: 'pyright', extensions: ['.py', '.pyi'], command: 'pyright-langserver', args: ['--stdio'], languageId: 'python' },
  { id: 'gopls', extensions: ['.go'], command: 'gopls', args: [], languageId: 'go' },
  { id: 'rust-analyzer', extensions: ['.rs'], command: 'rust-analyzer', args: [], languageId: 'rust' },
  {
    id: 'clangd',
    extensions: ['.c', '.h', '.cpp', '.cc', '.cxx', '.hpp', '.hh', '.hxx'],
    command: 'clangd',
    args: [],
    languageId: 'c',
    languageIdByExt: {
      '.c': 'c',
      '.h': 'c',
      '.cpp': 'cpp',
      '.cc': 'cpp',
      '.cxx': 'cpp',
      '.hpp': 'cpp',
      '.hh': 'cpp',
      '.hxx': 'cpp',
    },
  },
  // jdtls 需要 Java 21+ 运行时（JAVA_HOME 或 PATH 上），且建议用 -data 指定
  // per-project workspace——这两点由用户的 jdtls 安装/包装脚本决定，天枢不代管。
  { id: 'jdtls', extensions: ['.java'], command: 'jdtls', args: [], languageId: 'java' },
  // C#：微软官方 roslyn-language-server（VS Code C# 扩展同源）优先，csharp-ls 兜底。
  { id: 'roslyn-language-server', extensions: ['.cs'], command: 'roslyn-language-server', args: ['--stdio'], languageId: 'csharp' },
  { id: 'csharp-ls', extensions: ['.cs'], command: 'csharp-ls', args: [], languageId: 'csharp' },
  { id: 'kotlin-language-server', extensions: ['.kt', '.kts'], command: 'kotlin-language-server', args: [], languageId: 'kotlin' },
  { id: 'sourcekit-lsp', extensions: ['.swift'], command: 'sourcekit-lsp', args: [], languageId: 'swift' },
  { id: 'dart', extensions: ['.dart'], command: 'dart', args: ['language-server', '--protocol=lsp'], languageId: 'dart' },
  { id: 'metals', extensions: ['.scala', '.sbt'], command: 'metals', args: [], languageId: 'scala' },
  { id: 'intelephense', extensions: ['.php'], command: 'intelephense', args: ['--stdio'], languageId: 'php' },
  { id: 'phpactor', extensions: ['.php'], command: 'phpactor', args: ['language-server'], languageId: 'php' },
  { id: 'ruby-lsp', extensions: ['.rb', '.rake', '.gemspec'], command: 'ruby-lsp', args: [], languageId: 'ruby' },
  { id: 'solargraph', extensions: ['.rb', '.rake', '.gemspec'], command: 'solargraph', args: ['stdio'], languageId: 'ruby' },
  { id: 'lua-language-server', extensions: ['.lua'], command: 'lua-language-server', args: [], languageId: 'lua' },
  { id: 'zls', extensions: ['.zig', '.zon'], command: 'zls', args: [], languageId: 'zig' },
  { id: 'bash-language-server', extensions: ['.sh', '.bash', '.zsh'], command: 'bash-language-server', args: ['start'], languageId: 'shellscript' },
  { id: 'terraform-ls', extensions: ['.tf', '.tfvars'], command: 'terraform-ls', args: ['serve'], languageId: 'terraform' },
  { id: 'clojure-lsp', extensions: ['.clj', '.cljs', '.cljc', '.edn'], command: 'clojure-lsp', args: [], languageId: 'clojure' },
  { id: 'ocamllsp', extensions: ['.ml', '.mli'], command: 'ocamllsp', args: [], languageId: 'ocaml' },
  { id: 'haskell-language-server', extensions: ['.hs', '.lhs'], command: 'haskell-language-server-wrapper', args: ['--lsp'], languageId: 'haskell' },
  { id: 'nil', extensions: ['.nix'], command: 'nil', args: [], languageId: 'nix' },
  { id: 'vue-language-server', extensions: ['.vue'], command: 'vue-language-server', args: ['--stdio'], languageId: 'vue' },
  { id: 'svelteserver', extensions: ['.svelte'], command: 'svelteserver', args: ['--stdio'], languageId: 'svelte' },
]

export type WhichFn = (bin: string) => boolean

/**
 * PATH 探测结果的进程级缓存。注册表扩到几十个 server 后，`serverForFile` 会
 * 在**每次** LSP 调用上被触达——无缓存时每次都要同步 spawn 一次 which，
 * 启动与逐次调用都会被这层无谓的进程开销拖住。
 */
const whichCache = new Map<string, boolean>()

function probeWhich(bin: string): boolean {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin], {
      stdio: ['ignore', 'ignore', 'ignore'],
      timeout: 800,
      windowsHide: true,
    })
    return true
  } catch {
    return false
  }
}

export function defaultWhich(bin: string): boolean {
  const cached = whichCache.get(bin)
  if (cached !== undefined) return cached
  const found = probeWhich(bin)
  whichCache.set(bin, found)
  return found
}

/** 清空 PATH 探测缓存（测试隔离；或会话中途装了 server 后强制重探）。 */
export function clearWhichCache(): void {
  whichCache.clear()
}

function extOf(filePath: string): string {
  const i = filePath.lastIndexOf('.')
  return i >= 0 ? filePath.slice(i).toLowerCase() : ''
}

/** 该扩展名的所有候选 server，按优先级排列（可能为空）。 */
export function serverDefsForExt(ext: string): LspServerDef[] {
  const e = ext.startsWith('.') ? ext.toLowerCase() : `.${ext.toLowerCase()}`
  return LSP_SERVERS.filter(s => s.extensions.includes(e))
}

/** 该扩展名的首选 server（不判断是否已安装），或 null。 */
export function serverDefForExt(ext: string): LspServerDef | null {
  return serverDefsForExt(ext)[0] ?? null
}

export function isServerAvailable(def: LspServerDef, which: WhichFn = defaultWhich): boolean {
  if (def.alwaysAvailable) return true
  return which(def.binary ?? def.command)
}

/**
 * 该文件的 server：候选里第一个已安装的，或 null（语言不支持 / 未装 server）。
 * 遍历候选而非只看首选——否则"装了备选 C# server"会被首选缺失挡住。
 */
export function serverForFile(filePath: string, which: WhichFn = defaultWhich): LspServerDef | null {
  return serverDefsForExt(extOf(filePath)).find(def => isServerAvailable(def, which)) ?? null
}

/** 该文件是否有任何已注册（未必已安装）的 server——用于诊断触发判定。 */
export function hasServerForFile(filePath: string): boolean {
  return serverDefForExt(extOf(filePath)) !== null
}

/** 该文件在此 server 下的 LSP languageId（按扩展名细化，回落到 def 默认值）。 */
export function languageIdForFile(def: LspServerDef, filePath: string): string {
  return def.languageIdByExt?.[extOf(filePath)] ?? def.languageId
}

/** All servers installed on this machine (for diagnostics / readiness checks). */
export function availableServers(which: WhichFn = defaultWhich): LspServerDef[] {
  return LSP_SERVERS.filter(s => isServerAvailable(s, which))
}
