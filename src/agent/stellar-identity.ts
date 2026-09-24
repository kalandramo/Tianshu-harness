/**
 * 星籍（账号的星域身份）的展示口径——码表、称号、视图组装。
 *
 * ## 为什么全挤在一个文件里
 * 本模块是**零值依赖叶子**：桌面端经 `src/server/ui-shared.ts` 共享它，而
 * `desktop/scripts/check-boundary.js` 对叶子有硬约束——只允许 `import type`。
 * 于是「码 ↔ id」「称号中文化」「视图组装」必须同处一个叶子：拆成两个叶子会
 * 立刻违反"叶子之间不许 value import"。
 *
 * ## 与官网的契约
 * 官网 `stellar_identities.primary_domain` 存的是**两字母码**（`FU`），星籍号里
 * 也嵌着它（`TS-FU-AKKV7C`）；内核 16 星域用的是拼音 id（`fu`）与中文名（`辅`）。
 * 码是公开标识（写进了星籍号、官网 URL、论坛署名），属于稳定契约。
 *
 * **中文名一律不复制到这里**——由调用方注入星域表（内核侧与桌面侧都已有
 * `STAR_DOMAINS`，桌面端经 ui-shared 拿）。见 `domainLookupFrom()`。
 * 官网另有 `apps/web/src/config/mappings.ts` 的 `DOMAIN_NAME_MAP`，两侧的同步点
 * 是码与中文名，由 `__tests__/stellar-identity.test.ts` 逐字钉住。
 */
import type { StarDomainId } from './star-domain-data.js'

/** 16 个内置星域的官网码 → 内核 id。码表顺序与官网 `DOMAIN_ORDER` 一致。 */
export const STELLAR_DOMAIN_CODES = {
  TS: 'tianshu',
  PJ: 'pojun',
  TF: 'tianfu',
  TL: 'tianliang',
  TQ: 'tianquan',
  TJ: 'tianji',
  TX: 'tianxuan',
  FU: 'fu',
  WQ: 'wenqu',
  KY: 'kaiyang',
  YG: 'yaoguang',
  HG: 'huagai',
  QM: 'qiming',
  CG: 'changgeng',
  QS: 'qisha',
  TY: 'taiyi',
} as const satisfies Record<string, StarDomainId>

export type StellarDomainCode = keyof typeof STELLAR_DOMAIN_CODES

/**
 * 码 → 星域 id。大小写不敏感（库里是大写，手工粘贴/旧数据未必）；
 * 未知码回 null——调用方原样展示原始值，不猜、不吞。
 */
export function domainIdForCode(code: string | null | undefined): StarDomainId | null {
  if (typeof code !== 'string' || !code) return null
  const upper = code.trim().toUpperCase()
  return (STELLAR_DOMAIN_CODES as Record<string, StarDomainId>)[upper] ?? null
}

/** 星域 id → 码。自定义域（不在 16 个内置里）回 null。 */
export function domainCodeForId(id: string | null | undefined): StellarDomainCode | null {
  if (typeof id !== 'string' || !id) return null
  for (const [code, domainId] of Object.entries(STELLAR_DOMAIN_CODES)) {
    if (domainId === id) return code as StellarDomainCode
  }
  return null
}

// ── 称号 ─────────────────────────────────────────────────────────────────

/**
 * 进度称号 → 中文名。
 *
 * 与官网 `apps/web/src/config/mappings.ts` 的 `TITLE_NAME_MAP` 逐字一致
 * （2026-09-18 抄录，5 项）。称号是**进度**（观星 → 建域 → 守域），与星域名无关。
 * 未知值原样透出——不猜、不吞（服务端加新称号时，用户至少看得见原文）。
 */
export const STELLAR_TITLE_LABELS: Record<string, string> = {
  observer: '观星者',
  star_guide: '星导',
  star_keeper: '星守',
  domain_builder: '域建者',
  domain_guardian: '域卫',
}

/** 称号本地化；未知值回原文，空值回空串（由调用方决定不渲染）。 */
export function stellarTitleLabel(title: string | null | undefined): string {
  if (typeof title !== 'string' || !title) return ''
  return STELLAR_TITLE_LABELS[title] ?? title
}

// ── 视图组装 ─────────────────────────────────────────────────────────────

/** 星籍行渲染所需的输入（`src/auth/account.ts` 的 `StellarIdentity` 满足此形状）。 */
export interface StellarIdentityLike {
  stellarId: string
  primaryDomain: string
  title: string
}

/** 注入用的星域信息（`STAR_DOMAINS` 的条目结构上满足它）。 */
export interface StellarDomainInfo {
  name: string
  alias?: string
  uiPersona?: { glyph?: string }
}

/** 由调用方注入的域码解析器：码 → 星域信息；查不到回 null。 */
export type StellarDomainLookup = (code: string) => StellarDomainInfo | null

/**
 * 用一张星域表造解析器。
 *
 * 内核侧与桌面侧都这样调用：`domainLookupFrom(STAR_DOMAINS)`——中文名与星符
 * 永远来自那份唯一来源，本叶子不持有第二份文案。
 */
export function domainLookupFrom(table: Record<string, StellarDomainInfo | undefined>): StellarDomainLookup {
  return (code) => {
    const id = domainIdForCode(code)
    if (!id) return null
    return table[id] ?? null
  }
}

/** 星籍的展示视图：一次算清，两个客户端只做渲染。 */
export interface StellarIdentityView {
  /** 原始星籍号（系统标识：复制、链接、对账都用它）。 */
  rawId: string
  /** 展示名（`辅-AKKV7C`）；解析不了时等于 rawId。 */
  displayId: string
  /** 原始域码（`FU`）。 */
  domainCode: string
  /** 中文星域名；码未知时回退为码本身（不吞信息）。 */
  domainName: string
  /** 星符（"色 + 符"双通道里的符）。未知域回 null——不伪造。 */
  glyph: string | null
  /** 域别名（如「认知调校师」）。未知域回 null。 */
  domainAlias: string | null
  /** 称号原文（`observer`）。 */
  title: string
  /** 称号中文（未知称号为原文）。 */
  titleLabel: string
}

/**
 * 星籍号的显示名：域码换成人读的星域名。
 *   `TS-FU-AKKV7C` → `辅-AKKV7C`
 *
 * 与官网 `formatStellarId` 的差别是**有意的**：那个函数按第一个 `-` 切分
 * （注释举例 `'TS-0137' → '天枢-0137'`），对当前三段式星籍号会输出
 * `天枢-FU-AKKV7C`——地名换了、码还裸着。这里按三段式解析，
 * **解析不了就原样返回**（脏数据 / 未来新格式不至于显示成空白）。
 *
 * 注意：**仅用于展示**。复制、URL（`/u/:stellarId`）、后端查询一律用原始值。
 */
export function formatStellarIdForDisplay(stellarId: string, lookup?: StellarDomainLookup): string {
  if (typeof stellarId !== 'string' || !stellarId) return ''
  const m = /^TS-([A-Za-z]{2})-([A-Za-z0-9]{6})$/.exec(stellarId.trim())
  if (!m) return stellarId
  const [, code = '', suffix = ''] = m
  const name = lookup?.(code)?.name
  return name ? `${name}-${suffix.toUpperCase()}` : stellarId
}

/**
 * 组装展示视图。
 *
 * 不可映射的部分一律**降级而不是丢弃**：域码未知 → 域名字段回退成码本身；
 * 称号未知 → 显示原文；没注入查表 → 星符/别名为 null。星籍是用户的身份，
 * 任何一项看不懂都比看到空白好。
 */
export function buildStellarIdentityView(
  identity: StellarIdentityLike,
  lookup?: StellarDomainLookup,
): StellarIdentityView {
  const rawId = typeof identity?.stellarId === 'string' ? identity.stellarId : ''
  const domainCode = typeof identity?.primaryDomain === 'string' ? identity.primaryDomain : ''
  const title = typeof identity?.title === 'string' ? identity.title : ''
  const domain = lookup?.(domainCode) ?? null
  return {
    rawId,
    displayId: formatStellarIdForDisplay(rawId, lookup),
    domainCode,
    domainName: domain?.name ?? domainCode,
    glyph: domain?.uiPersona?.glyph ?? null,
    domainAlias: domain?.alias ?? null,
    title,
    titleLabel: stellarTitleLabel(title),
  }
}
