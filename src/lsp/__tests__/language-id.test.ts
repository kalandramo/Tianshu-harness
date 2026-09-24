/**
 * 多语言 LSP 的 languageId 契约测试（2026-xx 多语言扩展）。
 *
 * 背景：server-registry 的 LspServerDef.languageId 声明了每种 server 的
 * languageId，但 manager.ts 内部硬编码了 TS/JS 家族的 resolver——所有非
 * JS/TS 文件在 didOpen 时被标记为 'javascript'。这些测试把"def 声明的
 * languageId 必须真正送达 didOpen"钉死。
 */
import { describe, it, afterEach } from 'node:test'
import assert from 'node:assert/strict'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { PassThrough } from 'node:stream'
import type { ChildProcess } from 'node:child_process'
import { createMultiLspManager, type MultiLspOptions } from '../multi-manager.js'
import { languageIdForFile, serverDefForExt } from '../server-registry.js'
import { encodeMessage, decodeMessages } from '../rpc.js'

/** 捕获客户端发出的 didOpen（languageId + text），并回 initialize / definition。 */
function createDidOpenCapturingServer() {
  const stdin = new PassThrough()
  const stdout = new PassThrough()
  const openedLanguageIds: string[] = []
  const openedTexts: string[] = []

  let buf = ''
  stdin.on('data', (chunk: Buffer) => {
    buf += chunk.toString()
    const { messages, rest } = decodeMessages(buf)
    buf = rest
    for (const msg of messages) {
      if ('method' in msg && 'id' in msg) {
        const id = (msg as { id: number }).id
        const method = (msg as { method: string }).method
        if (method === 'initialize') {
          stdout.write(encodeMessage({
            jsonrpc: '2.0' as const,
            id,
            result: { capabilities: { definitionProvider: true, referencesProvider: true } },
          }))
        } else if (method === 'textDocument/definition') {
          stdout.write(encodeMessage({ jsonrpc: '2.0' as const, id, result: [] }))
        }
      } else if ('method' in msg && (msg as { method: string }).method === 'textDocument/didOpen') {
        const params = (msg as { params?: { textDocument?: { languageId?: string; text?: string } } }).params
        openedLanguageIds.push(params?.textDocument?.languageId ?? '')
        openedTexts.push(params?.textDocument?.text ?? '')
      }
    }
  })

  const proc = {
    stdin, stdout, stderr: new PassThrough(),
    kill: () => true,
    on: () => {},
  } as unknown as ChildProcess

  return { proc, openedLanguageIds, openedTexts }
}

/** 落一个占位文件：didOpen 只在读到磁盘内容时才发出——幽灵路径不会触发通知
 *  （见下方「读不到不缓存 uri」用例），故 languageId 契约测试必须让目标真实存在。 */
function touchFile(dir: string, rel: string): void {
  const full = join(dir, rel)
  mkdirSync(dirname(full), { recursive: true })
  writeFileSync(full, '')
}

describe('LSP languageId 契约：def 声明的 languageId 必须送达 didOpen', () => {
  const managers: Array<{ dispose(): void }> = []
  afterEach(() => {
    for (const m of managers) { try { m.dispose() } catch { /* ignore */ } }
    managers.length = 0
  })

  const cases: Array<{ file: string; languageId: string }> = [
    { file: 'src/app.ts', languageId: 'typescript' },
    { file: 'src/App.tsx', languageId: 'typescriptreact' },
    { file: 'src/index.js', languageId: 'javascript' },
    { file: 'src/Widget.jsx', languageId: 'javascriptreact' },
    { file: 'main.py', languageId: 'python' },
    { file: 'main.go', languageId: 'go' },
    { file: 'main.rs', languageId: 'rust' },
    { file: 'main.c', languageId: 'c' },
    { file: 'Main.java', languageId: 'java' },
    { file: 'Program.cs', languageId: 'csharp' },
    { file: 'Main.kt', languageId: 'kotlin' },
    { file: 'App.swift', languageId: 'swift' },
    { file: 'index.php', languageId: 'php' },
    { file: 'app.rb', languageId: 'ruby' },
    { file: 'init.lua', languageId: 'lua' },
    { file: 'main.zig', languageId: 'zig' },
    { file: 'main.dart', languageId: 'dart' },
    { file: 'Main.scala', languageId: 'scala' },
    { file: 'run.sh', languageId: 'shellscript' },
  ]

  for (const { file, languageId } of cases) {
    it(`${file} → languageId '${languageId}'`, async () => {
      const dir = mkdtempSync(join(tmpdir(), 'lsp-langid-'))
      touchFile(dir, file)
      const server = createDidOpenCapturingServer()
      const opts: MultiLspOptions = { which: () => true, spawnFor: () => server.proc }
      const mgr = createMultiLspManager(dir, opts)
      managers.push(mgr)

      await mgr.gotoDefinition(file, 1, 0)
      assert.deepEqual(
        server.openedLanguageIds,
        [languageId],
        `${file} 的 didOpen 必须带 def 声明的 languageId（修复前恒为 'javascript'）`,
      )
    })
  }
})

describe('languageIdForFile：同一 server 内按扩展名细化', () => {
  it('typescript server 区分 ts/tsx/js/jsx', () => {
    const ts = serverDefForExt('.ts')!
    assert.equal(languageIdForFile(ts, 'a.ts'), 'typescript')
    assert.equal(languageIdForFile(ts, 'a.tsx'), 'typescriptreact')
    assert.equal(languageIdForFile(ts, 'a.js'), 'javascript')
    assert.equal(languageIdForFile(ts, 'a.jsx'), 'javascriptreact')
    assert.equal(languageIdForFile(ts, 'a.mjs'), 'javascript')
    assert.equal(languageIdForFile(ts, 'a.cjs'), 'javascript')
  })

  it('单语言 server 回落到 def.languageId', () => {
    const py = serverDefForExt('.py')!
    assert.equal(languageIdForFile(py, 'a.py'), 'python')
    assert.equal(languageIdForFile(py, 'a.pyi'), 'python')
  })
})

describe('didOpen 载荷：必须带磁盘真实内容（多语言 server 按它建文档缓冲）', () => {
  const managers: Array<{ dispose(): void }> = []
  afterEach(() => {
    for (const m of managers) { try { m.dispose() } catch { /* ignore */ } }
    managers.length = 0
  })

  it('didOpen.text 是文件真实内容，而不是空串', async () => {
    // 实测根因（sourcekit-lsp，2026）：客户端发 text:'' 时 server 认为文档为空，
    // textDocument/definition 1.4s 内返回空——tsserver 会自己读盘所以没暴露，
    // 但 sourcekit-lsp / jdtls / metals 这类严格 server 直接失效。
    const dir = mkdtempSync(join(tmpdir(), 'lsp-didopen-'))
    const content = 'struct Greeter {\n    func greet() -> String { "hi" }\n}\nlet g = Greeter()\n'
    writeFileSync(join(dir, 'main.swift'), content)

    const server = createDidOpenCapturingServer()
    const mgr = createMultiLspManager(dir, { which: () => true, spawnFor: () => server.proc })
    managers.push(mgr)

    await mgr.gotoDefinition('main.swift', 4, 8)
    assert.deepEqual(
      server.openedTexts,
      [content],
      'didOpen 必须携带文件真实内容——空文本会让 server 找不到任何符号',
    )
  })

  it('文件读不到时不发 didOpen，也不缓存 uri——文件随后出现可补发真实内容', async () => {
    // 缺陷形态（审查复现）：ensureDocument 先 add(uri) 再读盘，读不到时发空文本兜底
    // ——server 从此认为该文档是空的，且 openedDocs 短路让后续调用永不补发，
    // 「暂时读不到」被固化成「永久空文档」（definition/references 静默返回空）。
    const dir = mkdtempSync(join(tmpdir(), 'lsp-didopen-'))
    const content = 'struct Greeter {}\nlet g = Greeter()\n'
    const server = createDidOpenCapturingServer()
    const mgr = createMultiLspManager(dir, { which: () => true, spawnFor: () => server.proc })
    managers.push(mgr)

    await mgr.gotoDefinition('later.swift', 1, 0)
    assert.deepEqual(server.openedTexts, [], '读不到时不得谎报「文档为空」——宁可不发')

    writeFileSync(join(dir, 'later.swift'), content)
    await mgr.gotoDefinition('later.swift', 2, 8)
    assert.deepEqual(
      server.openedTexts,
      [content],
      '文件出现后必须补发真实内容——uri 不能已被永久缓存',
    )
  })

  it('超过体积上限的文件不发 didOpen（不把空文本灌给 server）', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'lsp-didopen-'))
    const server = createDidOpenCapturingServer()
    const mgr = createMultiLspManager(dir, { which: () => true, spawnFor: () => server.proc })
    managers.push(mgr)

    writeFileSync(join(dir, 'big.swift'), 'x'.repeat(600 * 1024))
    await mgr.gotoDefinition('big.swift', 1, 0)
    assert.deepEqual(server.openedTexts, [])
  })

  it('空文件仍要发 didOpen——0 字节是真实内容，不是「读不到」', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'lsp-didopen-'))
    const server = createDidOpenCapturingServer()
    const mgr = createMultiLspManager(dir, { which: () => true, spawnFor: () => server.proc })
    managers.push(mgr)

    writeFileSync(join(dir, 'empty.swift'), '')
    await mgr.gotoDefinition('empty.swift', 1, 0)
    assert.deepEqual(server.openedTexts, [''])
  })
})
