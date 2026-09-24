/**
 * TUI 的星籍显示（`/status` 里的一段）。
 *
 * ## 为什么外置
 * `slash-commands.ts` 是行数棘轮点名的巨石（只降不升），星籍这段有取数、
 * 降级、格式化三件事，堆进去必然超线。这里成独立模块，slash 侧只留一行调用。
 *
 * ## 三条设计口径
 * 1. **离线可见**：星籍读的是 `<RIVET_HOME>/account.json` 里的缓存副本，不发网络
 *    请求也能显示。陈旧（>TTL）时后台刷新一次，**不阻塞** `/status`——用户敲
 *    命令要的是立刻看到东西，不是等一次往返。
 * 2. **不确定就说不确定**：没缓存时显示「尚未取到」，不显示空白、也不编一个域。
 * 3. **星籍 ≠ 会话域**（本仓最容易混淆的一对）：星籍是账号级的（官网设定，一生
 *    可改一次），会话域是当前会话由 `/domain` 定的、显示在状态栏。两者同屏而不
 *    说明，用户必然读成「我当前在辅域」。所以这段固定带一句区分说明。
 *
 * 展示口径（中文域名 / 星符 / 称号 / 星籍号显示名）与桌面端共用
 * `src/agent/stellar-identity.ts`——两个客户端不许各说各话。
 */
import {
  accountStore,
  cachedAccountIdentity,
  fetchStellarIdentity,
  isAccountIdentityStale,
  saveAccountIdentity,
} from '../auth/account.js'
import { rivetHome } from '../config/paths.js'
import { STAR_DOMAINS } from '../agent/star-domain.js'
import { buildStellarIdentityView, domainLookupFrom, type StellarIdentityLike } from '../agent/stellar-identity.js'
import { color } from './engine/ansi.js'
import { getTheme } from './theme.js'

const HEADER = ['天枢账号 · 星籍', '═══════════════════════']
const SEPARATOR = '会话域由 /domain 决定并显示在状态栏——星籍是账号级的（官网设定），两者不是一回事。'

/** 星籍 → 一行展示文案（与桌面端同一口径）。 */
export function formatStellarIdentityLine(identity: StellarIdentityLike): string {
  const v = buildStellarIdentityView(identity, domainLookupFrom(STAR_DOMAINS))
  const glyph = v.glyph ? `${v.glyph} ` : ''
  return `${glyph}${v.domainName} · ${v.displayId} · ${v.titleLabel || '未定称号'}`
}

/**
 * `/status` 的星籍段（同步返回，可直接 push 进静态行）。
 *
 * 取数走磁盘缓存；缓存陈旧时**后台**刷新（fire-and-forget），下次 `/status`
 * 自然看到新值。
 */
export function accountIdentityLines(now: number = Date.now()): string[] {
  const store = accountStore(rivetHome())
  const token = store.load()
  if (!token?.accessToken) {
    return [...HEADER, '未登录 —— /login account 可绑定星籍（账号 ≠ Pro 解锁）']
  }

  const cached = cachedAccountIdentity(token)
  if (!cached || isAccountIdentityStale(cached.fetchedAt, now)) {
    void refreshStellarIdentityInBackground(store, token.accessToken)
  }

  const lines = [...HEADER]
  lines.push(
    cached
      ? `星籍    ${formatStellarIdentityLine(cached.identity)}`
      : '星籍    （尚未取到——联网后自动补，稍后再看即可）',
  )
  lines.push('', SEPARATOR)
  return lines
}

/**
 * 后台刷新星籍缓存。
 *
 * ⚠️ 写回前重新 load 并与发起时的 accessToken 对账：期间用户可能 `/login` 换了
 * 账号（或 `/logout`），拿旧 token 写回会把新会话打回旧凭据。登录态变了就放弃。
 */
async function refreshStellarIdentityInBackground(
  store: ReturnType<typeof accountStore>,
  accessToken: string,
): Promise<void> {
  try {
    const identity = await fetchStellarIdentity(accessToken)
    if (!identity) return
    const fresh = store.load()
    if (!fresh || fresh.accessToken !== accessToken) return
    saveAccountIdentity(store, fresh, identity)
  } catch {
    // 装饰性信息：拉不到就继续用旧值，绝不让它影响别的
  }
}

/** 登录成功时取一次星籍并落盘（与 sidecar 的 `POST /account/poll` 同法）。 */
export async function primeStellarIdentity(accessToken: string): Promise<string | null> {
  const store = accountStore(rivetHome())
  const token = store.load()
  if (!token || token.accessToken !== accessToken) return null
  try {
    const identity = await fetchStellarIdentity(accessToken)
    if (!identity) return null
    const fresh = store.load()
    if (!fresh || fresh.accessToken !== accessToken) return null
    saveAccountIdentity(store, fresh, identity)
    return formatStellarIdentityLine(identity)
  } catch {
    return null
  }
}

/**
 * 首屏（欢迎页）的星籍行——登录后立刻看得见「我是谁」，这是账号体系唯一
 * 不需要用户主动敲命令就能感知到的收益。
 *
 * 三条克制：
 * 1. **只读磁盘缓存，零网络**。启动路径上加一次往返换一行装饰性文本不值当；
 *    没有缓存就不显示（`/status` 会给「尚未取到」的完整交代）。
 * 2. **未登录完全不出现**。首屏摆一句「去登录」是把可选功能做成推销——账号
 *    体系的设计前提就是「账号可选、不阻断既有路径」。
 * 3. 抛错一律吞掉：显示失败不该影响启动（同 welcome-greeting 的口径）。
 *
 * @returns 是否真的提交了一行（供测试与调用方观察）
 */
export function commitWelcomeStellarIdentityLine(commitStatic: (text: string) => void): boolean {
  try {
    const token = accountStore(rivetHome()).load()
    if (!token?.accessToken) return false
    const cached = cachedAccountIdentity(token)
    if (!cached) return false

    const view = buildStellarIdentityView(cached.identity, domainLookupFrom(STAR_DOMAINS))
    const theme = getTheme()
    const glyph = view.glyph ? `${color(view.glyph, theme.secondary)} ` : ''
    const title = view.titleLabel || '未定称号'
    // 「星籍」二字不能省：状态栏那个是**会话域**，两者同名不同物（见本模块头注释）。
    commitStatic(`${glyph}${color(`星籍 ${view.domainName} · ${view.displayId} · ${title}`, theme.muted)}`)
    return true
  } catch {
    return false
  }
}
