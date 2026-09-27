/**
 * 漂移守卫：`tauri.conf.json` 的 updater 公钥 ↔ 发版文档记录的 key id。
 *
 * 为什么值得一条守卫：key id 是排障时**唯一能对着用户报错核对的凭据**
 * （`The signature verification failed` 时，第一件事是确认用户端内嵌的 pubkey
 * 与线上 manifest 的签名私钥是不是一对）。2026-09-25 的密钥轮换把
 * `tauri.conf.json` 换成新 minisign 公钥（`94CFA603A032080C`），而
 * `docs/DESKTOP-RELEASE.md` 的「当前打包机配置」仍写着旧 id `198A2F01…`——
 * 配置改了、记录没改，两者都摆在「看起来权威」的位置上，下次排障就会按错的
 * id 去核对。把这条比对变成门禁，和 `license-keys-drift.test.ts` 同一思路。
 *
 * 公开仓快照里没有 `desktop/`，也没有这份私有发版文档（`docs/DESKTOP-RELEASE*`
 * 不在 sync 白名单），所以文件缺失时整组跳过，不把「本仓没有」误报成漂移红。
 */
import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const TAURI_CONF = fileURLToPath(
  new URL('../../../desktop/src-tauri/tauri.conf.json', import.meta.url)
)
const RELEASE_DOC = fileURLToPath(new URL('../../../docs/DESKTOP-RELEASE.md', import.meta.url))

const sourcesPresent = existsSync(TAURI_CONF) && existsSync(RELEASE_DOC)
const skip = sourcesPresent
  ? false
  : '本仓无 desktop/src-tauri 或 docs/DESKTOP-RELEASE.md（公开仓快照）——漂移守卫在开发仓生效'

/**
 * 轮换前的 minisign 公钥（`198A2F0156FAE921`，2026-09-25 被换掉）。
 * 仅用于契约自检：证明下面那条断言能区分新旧，而不是形同虚设。
 */
const LEGACY_UPDATER_PUBKEY =
  'dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDE5OEEyRjAxNTZGQUU5MjEKUldRaDZmcFdBUytLR2NGUVVzUmlWUE5yUHpJWUZ5QjV3SmptSmkzcjZLNEUvOHZRNDlkb1JTdlgK'

/**
 * minisign pubkey 的 base64 里嵌着 `untrusted comment: minisign public key: <16 hex>`。
 * 解不出 key id 直接判失败——pubkey 形态变了就该有人来看。
 */
function updaterKeyId(pubkeyB64: string): string {
  const decoded = Buffer.from(pubkeyB64.trim(), 'base64').toString('utf8')
  const id = decoded.match(/minisign public key:\s*([0-9A-F]{16})/)?.[1]
  assert.ok(id !== undefined, `pubkey 解不出 key id（形态已变？）：${decoded.slice(0, 120)}`)
  return id
}

/** 该公钥的 key id 是否被发版文档记录。抽成纯函数，便于用旧公钥做契约自检。 */
function keyIdRecordedInDoc(pubkeyB64: string, doc: string): boolean {
  return doc.includes(updaterKeyId(pubkeyB64))
}

function currentUpdaterPubkey(): string {
  const conf = JSON.parse(readFileSync(TAURI_CONF, 'utf8')) as {
    plugins?: { updater?: { pubkey?: string } }
  }
  const pubkey = conf.plugins?.updater?.pubkey
  assert.ok(
    typeof pubkey === 'string' && pubkey.length > 0,
    'tauri.conf.json 缺少 plugins.updater.pubkey（改名/挪位请同步本守卫）'
  )
  return pubkey
}

describe('updater 公钥漂移守卫（tauri.conf.json ⇄ 发版文档）', { skip }, () => {
  it('发版文档记录的 key id 必须是 tauri.conf.json 里那把 pubkey 的', () => {
    const keyId = updaterKeyId(currentUpdaterPubkey())
    const doc = readFileSync(RELEASE_DOC, 'utf8')
    assert.ok(
      doc.includes(keyId),
      `docs/DESKTOP-RELEASE.md 里找不到当前 updater pubkey 的 key id ${keyId}——轮换公钥后必须同步该文档（含 §1.2.1 轮换清单）`
    )
  })

  it('契约自检：旧公钥的 key id 不会被这条断言放过', () => {
    const current = updaterKeyId(currentUpdaterPubkey())
    const legacy = updaterKeyId(LEGACY_UPDATER_PUBKEY)
    assert.notEqual(legacy, current, '新公钥与旧公钥解出同一个 key id——解析逻辑错了')
    const doc = readFileSync(RELEASE_DOC, 'utf8')
    assert.equal(
      keyIdRecordedInDoc(currentUpdaterPubkey(), doc),
      true,
      '当前公钥的 id 应在文档里'
    )
    assert.equal(
      keyIdRecordedInDoc(LEGACY_UPDATER_PUBKEY, doc),
      false,
      '旧公钥的 id 仍在文档里被当成「当前」记录——配置回退到旧公钥时这条守卫就抓不住了'
    )
  })
})
