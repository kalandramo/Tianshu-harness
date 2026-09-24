/**
 * 星籍展示口径（码表 / 称号 / 视图组装）。
 *
 * 这里最重要的不是"查表能查到"，而是**三张清单不许漂移**：
 *   1. 官网 16 个码与中文名（`config/mappings.ts` 的 `DOMAIN_NAME_MAP` / `DOMAIN_ORDER`）
 *   2. 本模块的 `STELLAR_DOMAIN_CODES` 与 `STELLAR_TITLE_LABELS`
 *   3. 内核 `STAR_DOMAINS` 的 16 个 id 及其中文名
 * 任何一侧加了/改了星域而另两侧没跟上，都必须在这里变红——否则星籍行会把用户的
 * 星域显示成裸码或空白，**且没有任何外显信号**（这正是本仓最贵的 bug 形状）。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { STAR_DOMAINS } from '../star-domain-data.js'
import {
  STELLAR_DOMAIN_CODES,
  STELLAR_TITLE_LABELS,
  buildStellarIdentityView,
  domainCodeForId,
  domainIdForCode,
  domainLookupFrom,
  formatStellarIdForDisplay,
  stellarTitleLabel,
  type StellarDomainCode,
} from '../stellar-identity.js'

const lookup = domainLookupFrom(STAR_DOMAINS)

/** 官网 `apps/web/src/config/mappings.ts` 的 DOMAIN_NAME_MAP（2026-09-18 抄录，16 项）。 */
const OFFICIAL_DOMAIN_NAMES: Record<string, string> = {
  TS: '天枢',
  PJ: '破军',
  TF: '天府',
  TL: '天梁',
  TQ: '天权',
  TJ: '天机',
  TX: '天璇',
  YG: '瑶光',
  WQ: '文曲',
  HG: '华盖',
  FU: '辅',
  KY: '开阳',
  QM: '启明',
  CG: '长庚',
  QS: '七杀',
  TY: '太一',
}

/** 官网 `TITLE_NAME_MAP`（2026-09-18 抄录，5 项）。 */
const OFFICIAL_TITLE_NAMES: Record<string, string> = {
  observer: '观星者',
  star_guide: '星导',
  star_keeper: '星守',
  domain_builder: '域建者',
  domain_guardian: '域卫',
}

// ── 码表 ─────────────────────────────────────────────────────────────────

test('码表 16 项，与官网码集合逐字相等', () => {
  assert.equal(Object.keys(STELLAR_DOMAIN_CODES).length, 16)
  assert.deepEqual(
    Object.keys(STELLAR_DOMAIN_CODES).sort(),
    Object.keys(OFFICIAL_DOMAIN_NAMES).sort(),
    '官网加了/改了星域码，这里必须同步（否则星籍显示成裸码）',
  )
})

test('码表是双射：id 不重复，且与内核 STAR_DOMAINS 严格同集合', () => {
  const ids = Object.values(STELLAR_DOMAIN_CODES)
  assert.equal(new Set(ids).size, 16, 'id 不许重复')
  assert.deepEqual(
    [...ids].sort(),
    Object.keys(STAR_DOMAINS).sort(),
    '内核加第 17 个域而码表没跟上 → 该域星籍会静默显示裸码',
  )
})

test('官网中文名与内核星域名逐字一致（两侧文案不许各自演化）', () => {
  for (const [code, officialName] of Object.entries(OFFICIAL_DOMAIN_NAMES)) {
    const info = lookup(code)
    assert.ok(info, `码 ${code} 应能解析到星域`)
    assert.equal(info?.name, officialName, `码 ${code} 的中文名与官网不一致`)
  }
})

test('domainIdForCode：大小写不敏感、容忍空白，未知码回 null', () => {
  assert.equal(domainIdForCode('FU'), 'fu')
  assert.equal(domainIdForCode('fu'), 'fu')
  assert.equal(domainIdForCode(' Fu '), 'fu')
  assert.equal(domainIdForCode('ZZ'), null)
  assert.equal(domainIdForCode(''), null)
  assert.equal(domainIdForCode(undefined), null)
  assert.equal(domainIdForCode(null), null)
})

test('domainCodeForId 与 domainIdForCode 互逆；自定义域回 null', () => {
  for (const code of Object.keys(STELLAR_DOMAIN_CODES) as StellarDomainCode[]) {
    const id = domainIdForCode(code)
    assert.equal(id, STELLAR_DOMAIN_CODES[code])
    assert.equal(domainCodeForId(id), code, '往返应回到同一个码')
  }
  assert.equal(domainCodeForId('my-custom-domain'), null)
  assert.equal(domainCodeForId(''), null)
})

// ── 称号 ─────────────────────────────────────────────────────────────────

test('称号：5 项与官网逐字一致；未知值不吞', () => {
  assert.deepEqual(STELLAR_TITLE_LABELS, OFFICIAL_TITLE_NAMES)
  assert.equal(stellarTitleLabel('observer'), '观星者')
  assert.equal(stellarTitleLabel('future_title'), 'future_title', '服务端加新称号时至少看得见原文')
  assert.equal(stellarTitleLabel(''), '')
  assert.equal(stellarTitleLabel(null), '')
})

// ── 展示 ─────────────────────────────────────────────────────────────────

test('星籍号显示名：三段式换成中文域名', () => {
  assert.equal(formatStellarIdForDisplay('TS-FU-AKKV7C', lookup), '辅-AKKV7C')
  assert.equal(formatStellarIdForDisplay('TS-TS-0137AB', lookup), '天枢-0137AB')
})

test('星籍号解析不了就原样返回——不显示空白、也不猜', () => {
  // 官网 formatStellarId 注释里的两段式历史形态
  assert.equal(formatStellarIdForDisplay('TS-0137', lookup), 'TS-0137')
  // 未知域码
  assert.equal(formatStellarIdForDisplay('TS-ZZ-AKKV7C', lookup), 'TS-ZZ-AKKV7C')
  assert.equal(formatStellarIdForDisplay('garbage', lookup), 'garbage')
  assert.equal(formatStellarIdForDisplay('', lookup), '')
  // 没注入查表时也不许猜
  assert.equal(formatStellarIdForDisplay('TS-FU-AKKV7C'), 'TS-FU-AKKV7C')
})

test('buildStellarIdentityView：完整视图（域名 / 星符 / 别名 / 称号）', () => {
  const v = buildStellarIdentityView(
    { stellarId: 'TS-FU-AKKV7C', primaryDomain: 'FU', title: 'observer' },
    lookup,
  )
  assert.equal(v.rawId, 'TS-FU-AKKV7C', '原始值是系统标识，必须原样保留')
  assert.equal(v.displayId, '辅-AKKV7C')
  assert.equal(v.domainCode, 'FU')
  assert.equal(v.domainName, '辅')
  assert.equal(v.glyph, STAR_DOMAINS.fu.uiPersona.glyph)
  assert.equal(v.domainAlias, STAR_DOMAINS.fu.alias)
  assert.equal(v.titleLabel, '观星者')
})

test('buildStellarIdentityView：未知域码降级成码本身，星符为 null（不伪造）', () => {
  const v = buildStellarIdentityView(
    { stellarId: 'TS-ZZ-ABC123', primaryDomain: 'ZZ', title: 'observer' },
    lookup,
  )
  assert.equal(v.domainName, 'ZZ', '未知域显示码本身，不吞信息')
  assert.equal(v.glyph, null, '没有星符就是 null —— 调用方不该给未知域编一个符')
  assert.equal(v.domainAlias, null)
  assert.equal(v.displayId, 'TS-ZZ-ABC123')
})

test('buildStellarIdentityView：字段缺失不炸（空串兜底）；无查表也能用', () => {
  const v = buildStellarIdentityView({ stellarId: '', primaryDomain: '', title: '' })
  assert.equal(v.rawId, '')
  assert.equal(v.displayId, '')
  assert.equal(v.domainName, '')
  assert.equal(v.glyph, null)
  assert.equal(v.titleLabel, '')
})
