/**
 * 天枢账号 device flow 客户端（CLI / TUI 侧）。
 *
 * ## 为什么另写一个而不是复用 src/auth/device-flow.ts
 * `device-flow.ts` 是按 RFC 8628 通用形状解析的——读 `raw.device_code` /
 * `raw.user_code` / `raw.verification_uri`（snake_case）。而官网 EF
 * `tui-auth-create` 返回的是**驼峰**：
 *   {"deviceCode":"…","userCode":"…","expiresIn":300,"pollInterval":5,"verifyUrl":"…"}
 * 照搬那套解析会静默拿到 undefined，用户看到「登录中」永远转圈。
 * 2026-09-14 实测线上 EF 响应确认（部署后 verifyUrl 已是 tianshuharness.com/auth/device）。
 *
 * 运行：npm exec -- tsx --test src/auth/__tests__/account.test.ts
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  parseDeviceCreate,
  parseDevicePoll,
  isTerminalPollStatus,
  accountStore,
  saveAccountToken,
  deviceAuthorizeUrl,
  requestDeviceCode,
  fetchStellarIdentity,
  parseStellarIdentity,
  saveAccountIdentity,
  cachedAccountIdentity,
  isAccountIdentityStale,
  jwtSubject,
  ACCOUNT_IDENTITY_TTL_MS,
} from '../account.js'

// ── 解析契约 ─────────────────────────────────────────────────────────────

test('解析官网 EF 的驼峰响应', () => {
  const r = parseDeviceCreate({
    deviceCode: 'dc-abc123',
    userCode: '483920',
    expiresIn: 300,
    pollInterval: 5,
    verifyUrl: 'https://tianshuharness.com/auth/device',
  })
  assert.equal(r.deviceCode, 'dc-abc123')
  assert.equal(r.userCode, '483920')
  assert.equal(r.expiresIn, 300)
  assert.equal(r.pollInterval, 5)
  assert.equal(r.verifyUrl, 'https://tianshuharness.com/auth/device')
})

test('缺 deviceCode 或 userCode 时抛错，不返回半成品', () => {
  assert.throws(() => parseDeviceCreate({ userCode: '483920', verifyUrl: 'x' }), /deviceCode/)
  assert.throws(() => parseDeviceCreate({ deviceCode: 'dc-1', verifyUrl: 'x' }), /userCode/)
  assert.throws(() => parseDeviceCreate({ deviceCode: 'dc-1', userCode: '1' }), /verifyUrl/)
})

test('轮询状态：pending 无 token、approved 带 token', () => {
  const pending = parseDevicePoll({ status: 'pending' })
  assert.equal(pending.status, 'pending')
  assert.equal(pending.accessToken, undefined)

  const ok = parseDevicePoll({
    status: 'approved',
    accessToken: 'at-xyz',
    refreshToken: 'rt-xyz',
    expiresIn: 3600,
  })
  assert.equal(ok.status, 'approved')
  assert.equal(ok.accessToken, 'at-xyz')
  assert.equal(ok.refreshToken, 'rt-xyz')
  assert.equal(ok.expiresIn, 3600)
})

test('approved 但缺 accessToken 视为异常，不写入空凭据', () => {
  assert.throws(() => parseDevicePoll({ status: 'approved' }), /accessToken/)
})

test('终态判定：denied / expired / already_consumed 都要停下来', () => {
  assert.equal(isTerminalPollStatus('denied'), true)
  assert.equal(isTerminalPollStatus('expired'), true)
  assert.equal(isTerminalPollStatus('already_consumed'), true)
  assert.equal(isTerminalPollStatus('error'), true)
  assert.equal(isTerminalPollStatus('pending'), false)
  assert.equal(isTerminalPollStatus('approved'), false)
})

// ── 落盘 ─────────────────────────────────────────────────────────────────

test('账号 token 落到 <RIVET_HOME>/account.json，权限 0600', () => {
  const home = mkdtempSync(join(tmpdir(), 'rivet-acct-'))
  try {
    const store = accountStore(home)
    const saved = saveAccountToken(store, {
      status: 'approved',
      accessToken: 'at-1',
      refreshToken: 'rt-1',
      expiresIn: 3600,
    })

    assert.equal(saved.accessToken, 'at-1')
    assert.equal(saved.refreshToken, 'rt-1')
    assert.ok(saved.expiresAt > Date.now(), 'expiresAt 应是从现在起算的未来时间')

    // 文件名必须是 account.json —— 与 provider 的 <provider>.json 区分开，
    // 否则会覆盖掉同名的 provider 凭据。
    // 内容自 v3.21.1 起是 AES-256-GCM 信封（向量⑥ 加固）：断言的是**明文不再
    // 落盘**这一安全性质，而不是具体格式——格式演进不该让安全断言失效。
    const raw = readFileSync(join(home, 'account.json'), 'utf8')
    assert.ok(!raw.includes('at-1'), 'accessToken 不允许明文落盘')
    assert.ok(!raw.includes('rt-1'), 'refreshToken 不允许明文落盘')
    const envelope = JSON.parse(raw) as { v?: number; s?: string }
    assert.equal(envelope.v, 1, '应是带版本号的密文信封')
    assert.equal(envelope.s, 'aes-256-gcm')

    const mode = statSync(join(home, 'account.json')).mode & 0o777
    assert.equal(mode, 0o600, `凭据文件权限应为 0600，实际 ${mode.toString(8)}`)

    // 能读回来
    assert.equal(store.load()?.accessToken, 'at-1')
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('saveAccountToken 拒绝没有 token 的轮询结果', () => {
  const home = mkdtempSync(join(tmpdir(), 'rivet-acct-'))
  try {
    const store = accountStore(home)
    assert.throws(() => saveAccountToken(store, { status: 'pending' }), /accessToken/)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

// ── 授权链接 ─────────────────────────────────────────────────────────────

test('设备授权链接补上 userCode——裸基址会让授权页停在「缺少授权码」', () => {
  // 页面契约（官网 DeviceAuthorizeView.vue）：从 route.query.code 读设备码，
  // 读不到就进 no-code 态，文案是「请从终端或桌面端的提示里复制完整链接，
  // 或在地址后补上 ?code= 参数」——即客户端该给出完整链接。
  // 而 EF 的 verifyUrl 是可配置的页面基址、不带 code（2026-09-14 实测线上响应：
  // {"verifyUrl":"https://tianshuharness.com/auth/device"}）。
  assert.equal(
    deviceAuthorizeUrl('https://tianshuharness.com/auth/device', '483920'),
    'https://tianshuharness.com/auth/device?code=483920',
  )
  // 基址已带 query 时用 & 续接——拼出两个 ? 会让页面读不到 code
  assert.equal(
    deviceAuthorizeUrl('https://x.dev/auth/device?lang=zh', '483920'),
    'https://x.dev/auth/device?lang=zh&code=483920',
  )
})

test('requestDeviceCode 返回的 verifyUrl 可直接打开（已含 code）', async () => {
  const created = await requestDeviceCode({
    fetchImpl: async () =>
      new Response(
        JSON.stringify({
          deviceCode: 'dc-1',
          userCode: '483920',
          expiresIn: 300,
          pollInterval: 5,
          verifyUrl: 'https://tianshuharness.com/auth/device',
        }),
        { status: 200 },
      ),
  })
  assert.equal(created.verifyUrl, 'https://tianshuharness.com/auth/device?code=483920')
  assert.equal(created.userCode, '483920')
})

// ── 星籍（stellar identity）──────────────────────────────────────────────

/** 造一个形状正确的 access token（只填 sub，够 jwtSubject 用）。 */
function tokenFor(sub: string): string {
  const payload = Buffer.from(JSON.stringify({ sub, role: 'authenticated' }), 'utf8')
    .toString('base64')
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
  return `header.${payload}.sig`
}

test('jwtSubject 解出 sub；坏 token 回 null 而不是抛', () => {
  assert.equal(jwtSubject(tokenFor('u-42')), 'u-42')
  assert.equal(jwtSubject('not-a-jwt'), null)
  assert.equal(jwtSubject('a.@@@notbase64@@@.c'), null)
  assert.equal(jwtSubject(''), null)
})

test('parseStellarIdentity：缺字段回 null，title 缺省 observer', () => {
  assert.deepEqual(
    parseStellarIdentity({ stellar_id: 'TS-FU-AKKV7C', primary_domain: 'FU', title: 'observer' }),
    { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' },
  )
  // PostgREST 返回的是数组——取第一行
  assert.deepEqual(
    parseStellarIdentity([{ stellar_id: 'TS-FU-AKKV7C', primary_domain: 'FU' }]),
    { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' },
  )
  assert.equal(parseStellarIdentity([]), null, '空结果（没有星籍）不是错误，但也没有身份')
  assert.equal(parseStellarIdentity({ primary_domain: 'FU' }), null, '缺 stellar_id')
  assert.equal(parseStellarIdentity({ stellar_id: 'TS-FU-AKKV7C' }), null, '缺 primary_domain')
  assert.equal(parseStellarIdentity(null), null)
})

test('fetchStellarIdentity：RLS 放行时取回本人星籍', async () => {
  let seen = ''
  const identity = await fetchStellarIdentity(tokenFor('u-42'), {
    fetchImpl: async (input) => {
      seen = String(input)
      return new Response(
        JSON.stringify([
          { user_id: 'u-42', stellar_id: 'TS-FU-AKKV7C', primary_domain: 'FU', title: 'observer' },
        ]),
        { status: 200 },
      )
    },
  })
  assert.deepEqual(identity, { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' })
  assert.match(seen, /\/rest\/v1\/stellar_identities/)
})

test('fetchStellarIdentity：返回行不属于本人 → 拒绝（策略改宽也不显示别人的星籍）', async () => {
  const identity = await fetchStellarIdentity(tokenFor('u-42'), {
    fetchImpl: async () =>
      new Response(JSON.stringify([{ user_id: 'someone-else', stellar_id: 'TS-TS-XXXXXX', primary_domain: 'TS' }]), {
        status: 200,
      }),
  })
  assert.equal(identity, null)
})

test('fetchStellarIdentity：401 / 空结果 / 网络异常一律回 null，不抛', async () => {
  const cases: Array<() => Promise<Response>> = [
    async () => new Response('{"message":"invalid claim"}', { status: 401 }),
    async () => new Response('[]', { status: 200 }),
    async () => {
      throw new Error('ECONNRESET')
    },
  ]
  for (const impl of cases) {
    const got = await fetchStellarIdentity(tokenFor('u-42'), { fetchImpl: impl })
    assert.equal(got, null)
  }
})

test('saveAccountIdentity 以 token 为底展开——部分写不会抹掉 accessToken', () => {
  const home = mkdtempSync(join(tmpdir(), 'rivet-acct-identity-'))
  try {
    const store = accountStore(home)
    const token = saveAccountToken(store, {
      status: 'approved',
      accessToken: 'at-1',
      refreshToken: 'rt-1',
    })

    saveAccountIdentity(store, token, { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' }, 1000)

    const back = store.load()
    assert.equal(back?.accessToken, 'at-1', 'accessToken 必须还在——save() 是全量写')
    assert.equal(back?.refreshToken, 'rt-1')
    assert.deepEqual(back?.identity, {
      stellarId: 'TS-FU-AKKV7C',
      primaryDomain: 'FU',
      title: 'observer',
      fetchedAt: 1000,
    })
    // 登出后身份不留孤儿
    store.clear()
    assert.equal(store.load(), null)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('cachedAccountIdentity 与 TTL：坏数据回 null，过期判 stale', () => {
  assert.equal(cachedAccountIdentity(null), null)
  assert.equal(cachedAccountIdentity({ accessToken: 'x', expiresAt: 0 }), null, '旧文件无 identity 字段')
  assert.equal(
    cachedAccountIdentity({ accessToken: 'x', expiresAt: 0, identity: { stellarId: '', primaryDomain: 'FU', title: 'observer', fetchedAt: 1 } }),
    null,
    'stellarId 为空串视为无缓存',
  )

  const cached = cachedAccountIdentity({
    accessToken: 'x',
    expiresAt: 0,
    identity: { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer', fetchedAt: 1000 },
  })
  assert.deepEqual(cached, {
    identity: { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' },
    fetchedAt: 1000,
  })

  assert.equal(isAccountIdentityStale(1000, 1000 + ACCOUNT_IDENTITY_TTL_MS - 1), false)
  assert.equal(isAccountIdentityStale(1000, 1000 + ACCOUNT_IDENTITY_TTL_MS + 1), true)
  assert.equal(isAccountIdentityStale(0, Date.now()), true, 'fetchedAt 缺失视为陈旧')
})

