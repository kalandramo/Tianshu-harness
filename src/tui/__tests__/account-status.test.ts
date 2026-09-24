/**
 * TUI 星籍段（`/status` 的第一段）。
 *
 * 锁住三件容易做错、错了也没有外显信号的事：
 *  1. **离线可见**：星籍读磁盘缓存即可显示，不发网络请求（陈旧才后台刷）。
 *  2. **不确定就说不确定**：没有缓存时显示「尚未取到」，不显示空白、也不编一个域。
 *  3. **星籍 ≠ 会话域**：这段固定带一句区分说明——两者同屏而不说明，用户必然
 *     读成「我当前在辅域」。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { accountStore, saveAccountIdentity, saveAccountToken } from '../../auth/account.js'
import {
  accountIdentityLines,
  commitWelcomeStellarIdentityLine,
  formatStellarIdentityLine,
  primeStellarIdentity,
} from '../account-status.js'

const IDENTITY = { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' }

function tokenFor(sub: string): string {
  const payload = Buffer.from(JSON.stringify({ sub, role: 'authenticated' }), 'utf8')
    .toString('base64')
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
  return `header.${payload}.sig`
}

/** 每个用例独立 RIVET_HOME；fetch 默认打成"不该被调用"。 */
async function withHome<T>(fn: (home: string) => Promise<T> | T): Promise<T> {
  const home = mkdtempSync(join(tmpdir(), 'rivet-tui-acct-'))
  const prevHome = process.env.RIVET_HOME
  const prevFetch = globalThis.fetch
  process.env.RIVET_HOME = home
  globalThis.fetch = (async () => {
    throw new Error('测试不该发网络请求')
  }) as typeof fetch
  try {
    return await fn(home)
  } finally {
    process.env.RIVET_HOME = prevHome
    globalThis.fetch = prevFetch
    rmSync(home, { recursive: true, force: true })
  }
}

test('未登录：给出可行动的提示，不假装有身份', async () => {
  await withHome(() => {
    const lines = accountIdentityLines()
    assert.equal(lines[0], '天枢账号 · 星籍')
    assert.ok(lines.some((l) => l.includes('/login account')), '未登录时必须告诉用户怎么登录')
    assert.ok(!lines.some((l) => l.includes('尚未取到')), '未登录不该显示「尚未取到」')
  })
})

test('已登录 + 有缓存：离线显示星籍（不发网络）', async () => {
  await withHome((home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })
    saveAccountIdentity(store, token, IDENTITY)

    const lines = accountIdentityLines()
    const line = lines.find((l) => l.startsWith('星籍'))
    assert.equal(line, '星籍    ⊕ 辅 · 辅-AKKV7C · 观星者')
    // 星籍与会话域必须同屏可辨（本仓最容易混淆的一对）
    assert.ok(
      lines.some((l) => l.includes('会话域由 /domain 决定') && l.includes('星籍是账号级的')),
      '缺少「星籍 ≠ 会话域」的区分说明',
    )
  })
})

test('已登录但没缓存：说「尚未取到」，不显示空白也不编一个域', async () => {
  await withHome((home) => {
    saveAccountToken(accountStore(home), { status: 'approved', accessToken: tokenFor('u-1') })
    const lines = accountIdentityLines()
    const line = lines.find((l) => l.startsWith('星籍'))
    assert.match(String(line), /尚未取到/)
  })
})

test('未知域码降级成码本身；未知称号显示原文', async () => {
  await withHome((home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })
    saveAccountIdentity(store, token, { stellarId: 'TS-ZZ-ABC123', primaryDomain: 'ZZ', title: 'future_title' })
    const line = accountIdentityLines().find((l) => l.startsWith('星籍'))
    assert.equal(line, '星籍    ZZ · TS-ZZ-ABC123 · future_title')
  })
})

test('缓存陈旧 → 后台刷新并落盘（写回前与当前 token 对账）', async () => {
  await withHome(async (home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })
    // fetchedAt=1 → 必然过期（星籍号里的码与 primary_domain 恒一致，这里一并换掉）
    store.save({
      ...token,
      identity: { stellarId: 'TS-TS-OLD001', primaryDomain: 'TS', title: 'observer', fetchedAt: 1 },
    })

    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify([
          { user_id: 'u-1', stellar_id: 'TS-FU-AKKV7C', primary_domain: 'FU', title: 'observer' },
        ]),
        { status: 200 },
      )) as typeof fetch

    const lines = accountIdentityLines()
    assert.ok(lines.some((l) => l.includes('天枢-OLD001')), '陈旧时先给手里那份旧值')

    const deadline = Date.now() + 500
    while (Date.now() < deadline && store.load()?.identity?.primaryDomain !== 'FU') {
      await new Promise((r) => setTimeout(r, 5))
    }
    assert.equal(store.load()?.identity?.primaryDomain, 'FU', '后台应刷新出新星籍')
    assert.equal(store.load()?.accessToken, token.accessToken, '刷新不得动 token')
  })
})

test('primeStellarIdentity：登录后取一次并落盘，返回可直接展示的一行', async () => {
  await withHome(async (home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })

    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify([
          { user_id: 'u-1', stellar_id: 'TS-FU-AKKV7C', primary_domain: 'FU', title: 'observer' },
        ]),
        { status: 200 },
      )) as typeof fetch

    const line = await primeStellarIdentity(token.accessToken)
    assert.equal(line, '⊕ 辅 · 辅-AKKV7C · 观星者')
    assert.equal(store.load()?.identity?.primaryDomain, 'FU')
  })
})

test('primeStellarIdentity：token 已被换掉（登录期间重新登录）→ 不写盘', async () => {
  await withHome(async (home) => {
    const store = accountStore(home)
    saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-OTHER') })
    globalThis.fetch = (async () => new Response('[]', { status: 200 })) as typeof fetch

    const line = await primeStellarIdentity(tokenFor('u-1'))
    assert.equal(line, null)
    assert.equal(store.load()?.identity, undefined)
  })
})

test('formatStellarIdentityLine：称号为空时显示占位，不留悬空分隔符', () => {
  const line = formatStellarIdentityLine({ stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: '' })
  assert.equal(line, '⊕ 辅 · 辅-AKKV7C · 未定称号')
})

// ── 首屏星籍行（Wave C）──────────────────────────────────────────────────

test('首屏：登录且有缓存 → 提交一行，带「星籍」二字（与会话域区分开）', async () => {
  await withHome((home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })
    saveAccountIdentity(store, token, IDENTITY)

    const lines: string[] = []
    const committed = commitWelcomeStellarIdentityLine((t) => lines.push(t))
    assert.equal(committed, true)
    assert.equal(lines.length, 1, '首屏只加一行')
    assert.match(lines[0]!, /星籍 辅 · 辅-AKKV7C · 观星者/)
  })
})

test('首屏：未登录 → 什么都不提交（账号可选，不做推销）', async () => {
  await withHome(() => {
    const lines: string[] = []
    assert.equal(commitWelcomeStellarIdentityLine((t) => lines.push(t)), false)
    assert.deepEqual(lines, [])
  })
})

test('首屏：登录但无缓存 → 不显示占位（不摆「尚未取到」，首屏不是报错的地方）', async () => {
  await withHome((home) => {
    saveAccountToken(accountStore(home), { status: 'approved', accessToken: tokenFor('u-1') })
    const lines: string[] = []
    assert.equal(commitWelcomeStellarIdentityLine((t) => lines.push(t)), false)
    assert.deepEqual(lines, [])
  })
})

test('首屏：commitStatic 抛错不外传（显示失败不影响启动）', async () => {
  await withHome((home) => {
    const store = accountStore(home)
    const token = saveAccountToken(store, { status: 'approved', accessToken: tokenFor('u-1') })
    saveAccountIdentity(store, token, IDENTITY)
    assert.equal(
      commitWelcomeStellarIdentityLine(() => {
        throw new Error('stdout gone')
      }),
      false,
    )
  })
})

test('首屏接线：main.ts 真的调用了首屏星籍行（主体在 account-status.ts）', () => {
  const main = readFileSync(resolve(process.cwd(), 'src/main.ts'), 'utf8')
  assert.match(main, /commitWelcomeStellarIdentityLine\(/, 'main.ts 未接首屏星籍行——登录后首屏看不到身份')
  assert.match(main, /from '\.\/tui\/account-status\.js'/, '接线应指向外置模块（main.ts 是点名巨石）')
})
