import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  serverDefForExt,
  serverDefsForExt,
  serverForFile,
  isServerAvailable,
  availableServers,
  hasServerForFile,
  LSP_SERVERS,
} from '../server-registry.js'

describe('lsp server-registry (C2 polyglot)', () => {
  it('maps extensions to the right server', () => {
    assert.equal(serverDefForExt('.py')?.id, 'pyright')
    assert.equal(serverDefForExt('.go')?.id, 'gopls')
    assert.equal(serverDefForExt('.rs')?.id, 'rust-analyzer')
    assert.equal(serverDefForExt('.cpp')?.id, 'clangd')
    assert.equal(serverDefForExt('.java')?.id, 'jdtls')
    assert.equal(serverDefForExt('.ts')?.id, 'typescript')
  })

  it('returns null for unsupported extensions', () => {
    assert.equal(serverDefForExt('.txt'), null)
    assert.equal(serverDefForExt('.md'), null)
  })

  it('typescript is always available (launched via npx)', () => {
    const ts = LSP_SERVERS.find(s => s.id === 'typescript')!
    assert.equal(isServerAvailable(ts, () => false), true)
  })

  it('non-ts servers require their binary on PATH', () => {
    const gopls = LSP_SERVERS.find(s => s.id === 'gopls')!
    assert.equal(isServerAvailable(gopls, () => false), false)
    assert.equal(isServerAvailable(gopls, (b) => b === 'gopls'), true)
  })

  it('serverForFile only returns installed servers', () => {
    // gopls not installed → no server for .go
    assert.equal(serverForFile('main.go', () => false), null)
    // gopls installed → resolves
    assert.equal(serverForFile('main.go', (b) => b === 'gopls')?.id, 'gopls')
    // ts always resolves
    assert.equal(serverForFile('app.ts', () => false)?.id, 'typescript')
  })

  it('availableServers reflects which binaries are present', () => {
    const all = availableServers((b) => b === 'pyright-langserver')
    const ids = all.map(s => s.id).sort()
    // typescript (always) + pyright (present)
    assert.deepEqual(ids, ['pyright', 'typescript'])
  })
})

describe('多语言注册表覆盖（2026-xx 语言扩展）', () => {
  const cases: Array<[string, string]> = [
    ['.cs', 'roslyn-language-server'],
    ['.kt', 'kotlin-language-server'],
    ['.kts', 'kotlin-language-server'],
    ['.swift', 'sourcekit-lsp'],
    ['.php', 'intelephense'],
    ['.rb', 'ruby-lsp'],
    ['.lua', 'lua-language-server'],
    ['.zig', 'zls'],
    ['.dart', 'dart'],
    ['.scala', 'metals'],
    ['.sh', 'bash-language-server'],
    ['.zsh', 'bash-language-server'],
    ['.tf', 'terraform-ls'],
    ['.clj', 'clojure-lsp'],
    ['.ml', 'ocamllsp'],
    ['.hs', 'haskell-language-server'],
    ['.nix', 'nil'],
    ['.vue', 'vue-language-server'],
    ['.svelte', 'svelteserver'],
  ]

  for (const [ext, id] of cases) {
    it(`${ext} → ${id}`, () => {
      assert.equal(serverDefForExt(ext)?.id, id)
    })
  }

  it('C# 有多个候选：首选微软 roslyn，csharp-ls 兜底', () => {
    const ids = serverDefsForExt('.cs').map(d => d.id)
    assert.deepEqual(ids, ['roslyn-language-server', 'csharp-ls'])
  })

  it('只装了兜底 server 时仍能解析出 C# server', () => {
    // 首选 roslyn 缺失、兜底 csharp-ls 存在 → 必须选兜底而不是返回 null
    const def = serverForFile('Program.cs', (b) => b === 'csharp-ls')
    assert.equal(def?.id, 'csharp-ls')
  })

  it('roslyn-language-server 必须显式带 --stdio（默认不走 stdio）', () => {
    const def = LSP_SERVERS.find(s => s.id === 'roslyn-language-server')!
    assert.deepEqual(def.args, ['--stdio'])
    assert.equal(def.languageId, 'csharp')
  })

  it('诊断触发面按 registry 判定：已注册语言 true，未注册 false', () => {
    assert.equal(hasServerForFile('a.py'), true)
    assert.equal(hasServerForFile('Program.cs'), true)
    assert.equal(hasServerForFile('Main.java'), true)
    assert.equal(hasServerForFile('run.sh'), true)
    // 未注册：不应为它们白跑一次 server 探测
    assert.equal(hasServerForFile('README.md'), false)
    assert.equal(hasServerForFile('package.json'), false)
    assert.equal(hasServerForFile('notes.txt'), false)
  })
})
