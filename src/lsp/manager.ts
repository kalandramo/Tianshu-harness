import type { ChildProcess } from 'node:child_process'
import { readFileSync, statSync } from 'node:fs'
import { isAbsolute, resolve as resolvePath, relative as relativePath } from 'node:path'
import { pathToFileURL, fileURLToPath } from 'node:url'
import { createRpcClient, DEFAULT_LSP_REQUEST_TIMEOUT_MS, type RpcClient, type RpcClientOptions } from './rpc.js'

/** didOpen/didChange 文本载荷上限——超过此大小的文件不把内容灌进 server。 */
const MAX_LSP_DOCUMENT_BYTES = 512 * 1024

/**
 * 读磁盘内容作为 didOpen/didChange 的 text 载荷。
 *
 * 必须发真实内容：多数语言服务器（sourcekit-lsp / jdtls / metals / roslyn）
 * 按 didOpen 的 text 建立文档缓冲——发空文本等于告诉它"这个文件是空的"，
 * 之后的 definition / references 一律查不到符号。实测：sourcekit-lsp 收到
 * 空 text 时 textDocument/definition 1.4s 内返回空数组；收到真实内容才返回定义。
 * tsserver 会自行读盘，所以这个假设历史上只对 TypeScript 成立——极易漏检。
 * （getFileDiagnostics 内已单独修过同一问题，此处是同类点的收口。）
 *
 * 返回 null 表示读不到或超过上限：调用方必须**跳过通知**——didOpen 既不发也不
 * 缓存 uri（缓存了 openedDocs 就永不补发），didChange 直接 return。发空文本会把
 * server 的文档缓冲清成空，比内容略旧更坏；而「暂时读不到」一旦被当成「文档为空」
 * 发给 server，就是不可恢复的静默失效（definition/references 一律返回空）。
 */
function readDocumentText(absPath: string): string | null {
  try {
    const stat = statSync(absPath)
    if (!stat.isFile() || stat.size > MAX_LSP_DOCUMENT_BYTES) return null
    return readFileSync(absPath, 'utf-8')
  } catch {
    return null
  }
}

interface Location {
  uri: string
  range: {
    start: { line: number; character: number }
    end: { line: number; character: number }
  }
}

interface ServerCapabilities {
  definitionProvider?: boolean
  referencesProvider?: boolean
  /** LSP 3.17+: server supports textDocument/diagnostic pull model. */
  diagnosticProvider?: unknown
}

/** Simplified LSP Diagnostic for file-level error reporting. */
export interface LspDiagnostic {
  range: {
    start: { line: number; character: number }
    end: { line: number; character: number }
  }
  severity: 1 | 2 | 3 | 4  // Error / Warning / Info / Hint
  message: string
  source?: string
}

export interface LspManager {
  initialize(): Promise<void>
  isReady(): boolean
  supportsDefinition(): boolean
  supportsReferences(): boolean
  gotoDefinition(filePath: string, line: number, character: number): Promise<Location[]>
  findReferences(filePath: string, line: number, character: number): Promise<Location[]>
  /** Notify the LSP server that a file was modified on disk (e.g. by edit_file/write_file).
   *  Ensures the LSP's internal state stays in sync for subsequent goto-def / find-refs queries. */
  changeFile(filePath: string): void
  /** T4: file-level diagnostics. Uses pull model (textDocument/diagnostic) if
   *  supported, otherwise falls back to cached publishDiagnostics.
   *  Timeout ~2s; returns empty array on timeout or server unavailability. */
  getFileDiagnostics(filePath: string, timeoutMs?: number): Promise<LspDiagnostic[]>
  dispose(): void
}

type SpawnFn = () => ChildProcess

/** Absolute filesystem path for a possibly-relative file, rooted at cwd.
 *  Cross-platform: handles Windows drive-letter absolute paths correctly. */
function absFromCwd(filePath: string, cwd: string): string {
  return isAbsolute(filePath) ? filePath : resolvePath(cwd, filePath)
}

/** Build an LSP file:// URI for a path. Uses pathToFileURL so Windows yields the
 *  required `file:///C:/...` form (not the invalid `file://C:\...`). */
function fileToUri(filePath: string, cwd: string): string {
  return pathToFileURL(absFromCwd(filePath, cwd)).href
}

/** Convert an LSP file:// URI back to a cwd-relative, forward-slash path. */
function uriToRelPath(uri: string, cwd: string): string {
  let abs: string
  try {
    abs = fileURLToPath(uri)
  } catch {
    abs = uri.replace(/^file:\/\/\/?/, '')
  }
  const rel = relativePath(cwd, abs)
  const out = rel && !rel.startsWith('..') && !isAbsolute(rel) ? rel : abs
  return out.split('\\').join('/')
}

/** 单服务器调用的默认 languageId 解析（TS/JS 家族）。多语言路径由
 *  createMultiLspManager 注入 registry 的 per-def 解析器覆盖。 */
function defaultLanguageId(filePath: string): string {
  if (filePath.endsWith('.tsx')) return 'typescriptreact'
  if (filePath.endsWith('.ts') || filePath.endsWith('.mts') || filePath.endsWith('.cts')) return 'typescript'
  if (filePath.endsWith('.jsx')) return 'javascriptreact'
  return 'javascript'
}

export function createLspManager(
  spawnFn: SpawnFn,
  cwd: string,
  rpcOptions: RpcClientOptions = {},
  /**
   * 该 server 处理某文件时应发的 LSP languageId。多语言路径由
   * createMultiLspManager 按 registry 的 def 注入；省略时回落 TS/JS 家族。
   */
  languageIdFor?: (filePath: string) => string,
): LspManager {
  const resolveLanguageId = languageIdFor ?? defaultLanguageId
  let rpc: RpcClient | null = null
  let proc: ChildProcess | null = null
  let capabilities: ServerCapabilities | null = null
  let ready = false
  const openedDocs = new Set<string>()
  /** T4: diagnostic cache keyed by URI, populated from publishDiagnostics notifications. */
  const diagnosticCache = new Map<string, LspDiagnostic[]>()

  /**
   * Ensure the document is opened in the LSP server.
   * Sends the file's real content — servers that build their document buffer from
   * didOpen (sourcekit-lsp / jdtls / metals / roslyn) see nothing otherwise.
   * Caches opened URIs — only sends didOpen + waits on first access.
   */
  async function ensureDocument(filePath: string): Promise<void> {
    if (!rpc) return
    const absPath = absFromCwd(filePath, cwd)
    const uri = fileToUri(filePath, cwd)
    if (openedDocs.has(uri)) return
    // 读不到（已删除 / 超体积上限）时**不认账**：宁可这次不发，也不发空文本把
    // 「暂时读不到」固化成「永久空文档」——server 收到空 text 即认为文件为空，
    // 而 add(uri) 之后 ensureDocument 永远短路，内容再也不会补发。
    const text = readDocumentText(absPath)
    if (text === null) return
    openedDocs.add(uri)
    try {
      rpc.notify('textDocument/didOpen', {
        textDocument: {
          uri,
          languageId: resolveLanguageId(filePath),
          version: 1,
          // 真实内容（0 字节文件是 ''，属真实内容而非读不到——判据用 null 不用 falsy）
          text,
        },
      })
      // Allow server to process the notification
      await new Promise(r => setTimeout(r, 100))
    } catch {
      // Best-effort document registration
    }
  }

  return {
    async initialize() {
      // 全新服务器进程对历史一无所知：崩溃后重新 initialize 必须清掉
      // openedDocs/diagnosticCache，否则 openedDocs 短路 didOpen——新服务器
      // 永远收不到那些文档的打开通知，诊断与定义静默失真。
      openedDocs.clear()
      diagnosticCache.clear()
      try {
        proc = spawnFn()

        // stdin is writable (we send requests), stdout is readable (we receive responses).
        // A failed spawn (ENOENT on desktop's minimal PATH) yields null pipes;
        // throw immediately instead of letting the RPC request hang forever.
        const stdin = proc.stdin
        const stdout = proc.stdout
        if (!stdin || !stdout) {
          throw new Error('LSP server spawn failed: no stdio pipes (check PATH / npx)')
        }

        // Handle stderr — TypeScript language server logs diagnostics here
        if (proc.stderr) {
          proc.stderr.on('data', () => {
            // Silently consume — LSP servers may log diagnostics to stderr
          })
        }

        // 2026-09-08 wedge fix: process death must reject every in-flight
        // RPC request. Merely flipping ready=false left callers (and the
        // tool pipeline awaiting ensure()) waiting on promises that never settle.
        proc.on('error', (err) => {
          ready = false
          rpc?.abortAllPending(err instanceof Error ? err : new Error(String(err)))
        })
        proc.on('exit', (code, signal) => {
          ready = false
          rpc?.abortAllPending(new Error(`LSP server exited (code=${code ?? 'null'}, signal=${signal ?? 'null'})`))
        })

        rpc = createRpcClient(stdout, stdin, {
          requestTimeoutMs: rpcOptions.requestTimeoutMs ?? DEFAULT_LSP_REQUEST_TIMEOUT_MS,
        })

        const initResult = await rpc.request('initialize', {
          processId: process.pid,
          rootUri: pathToFileURL(cwd).href,
          capabilities: {
            textDocument: {
              definition: { linkSupport: false },
              references: {},
            },
          },
        }) as { capabilities: ServerCapabilities }

        capabilities = initResult.capabilities
        rpc.notify('initialized', {})

        // T4: listen for publishDiagnostics to populate the cache
        rpc.onNotification('textDocument/publishDiagnostics', (rawParams: Record<string, unknown>) => {
          const params = rawParams as { uri: string; diagnostics: LspDiagnostic[] }
          if (params?.uri) {
            diagnosticCache.set(params.uri, params.diagnostics ?? [])
          }
        })

        // Wait for server to fully settle after initialization
        await new Promise(r => setTimeout(r, 200))
        ready = true
      } catch {
        ready = false
        try { rpc?.dispose() } catch { /* ignore */ }
        rpc = null
        try { proc?.kill() } catch { /* ignore */ }
        proc = null
      }
    },

    isReady() {
      return ready
    },

    supportsDefinition() {
      return capabilities?.definitionProvider === true
    },

    supportsReferences() {
      return capabilities?.referencesProvider === true
    },

    async gotoDefinition(filePath, line, character) {
      if (!rpc || !ready) return []
      try {
        await ensureDocument(filePath)
        const result = await rpc.request('textDocument/definition', {
          textDocument: { uri: fileToUri(filePath, cwd) },
          position: { line: line - 1, character }, // LSP uses 0-based lines
        })

        if (!result) return []

        const locations = (Array.isArray(result) ? result : [result]) as Location[]
        return locations.map(loc => ({
          ...loc,
          uri: uriToRelPath(loc.uri, cwd),
        }))
      } catch {
        return []
      }
    },

    async findReferences(filePath, line, character) {
      if (!rpc || !ready) return []
      try {
        await ensureDocument(filePath)
        const result = await rpc.request('textDocument/references', {
          textDocument: { uri: fileToUri(filePath, cwd) },
          position: { line: line - 1, character },
          context: { includeDeclaration: false },
        })

        if (!result) return []

        const locations = (Array.isArray(result) ? result : []) as Location[]
        return locations.map(loc => ({
          ...loc,
          uri: uriToRelPath(loc.uri, cwd),
        }))
      } catch {
        return []
      }
    },

    changeFile(filePath) {
      if (!rpc || !ready) return
      const uri = fileToUri(filePath, cwd)
      if (!openedDocs.has(uri)) return // never opened, server has no cached state
      // 必须带真实内容：空文本会把 server 的文档缓冲清空，之后的查询全部失真。
      // 读不到（已删除 / 超上限）时干脆不发——保持 server 现有缓冲，比清空更安全。
      const text = readDocumentText(absFromCwd(filePath, cwd))
      if (text === null) return
      try {
        rpc.notify('textDocument/didChange', {
          textDocument: { uri, version: Date.now() },
          contentChanges: [{ text }],
        })
      } catch {
        // Best-effort: 通知失败时 server 保留旧缓冲，查询结果可能略旧但不会失空
      }
    },

    async getFileDiagnostics(filePath, timeoutMs = 2000) {
      if (!rpc || !ready) return []
      const absPath = absFromCwd(filePath, cwd)
      const uri = fileToUri(filePath, cwd)

      try {
        // Prefer pull model (LSP 3.17+ textDocument/diagnostic)
        if (capabilities?.diagnosticProvider) {
          // Pass the timeout into the RPC layer so a hung server also clears
          // its pending entry — Promise.race alone kept the request alive.
          const result = await rpc
            .request('textDocument/diagnostic', { textDocument: { uri } }, timeoutMs)
            .catch(() => null) as { items?: LspDiagnostic[] } | null
          if (result?.items) return result.items
        }

        // Fallback: trigger a didChange with real file content to refresh publishDiagnostics
        await ensureDocument(filePath)
        // Read actual file content — empty text would tell tsserver the file is empty (false green)
        let fileText = ''
        try {
          const { readFileSync } = await import('node:fs')
          fileText = readFileSync(absPath, 'utf-8')
        } catch {
          // File may not exist on disk — use empty text as last resort
        }
        // Clear stale cache BEFORE notify — avoid racing server publishDiagnostics
        diagnosticCache.delete(uri)
        rpc.notify('textDocument/didChange', {
          textDocument: { uri, version: Date.now() },
          contentChanges: [{ text: fileText }],
        })
        // Wait for publishDiagnostics to arrive (server pushes asynchronously)
        await new Promise<void>((resolve) => {
          const start = Date.now()
          const check = () => {
            if (diagnosticCache.has(uri) || Date.now() - start > timeoutMs) {
              resolve()
            } else {
              setTimeout(check, 50)
            }
          }
          check()
        })
        return diagnosticCache.get(uri) ?? []
      } catch {
        return []
      }
    },

    dispose() {
      ready = false
      try { rpc?.dispose() } catch { /* ignore */ }
      try { proc?.kill() } catch { /* ignore */ }
      proc = null
      rpc = null
    },
  }
}
