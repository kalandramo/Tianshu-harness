import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import {
  FOUNDING_TIERS,
  FOUNDING_USER_LIMIT,
  FOUNDING_TOTAL_LIMIT,
  badgeDisplayName,
  isFoundingBadge,
  tierOfBadgeCode,
  tierOfRank,
} from '../founding-tiers.js'

/**
 * 创始用户档位（创始铭牌）——零依赖共享叶子。
 *
 * 单一事实源在官网仓 `../tianshu-official/apps/web/src/config/founding.ts`
 * （三档阈值与 Supabase 的 `assign_founder_badge_if_eligible` 授予函数一致）。
 * 跨仓无法直接断言，所以本测试钉的是**不变量**：任何一处改档位，
 * 覆盖区间/唯一性/回退语义都必须仍然成立——而不是复述官网的常量值。
 *
 * 命名注意：本模块的 "founding" 指**创始用户体系**；
 * `runtime/types.ts` 里另有 `founder`（星域创始者短名），两者无关。
 */

describe('founding-tiers：档位结构', () => {
  it('三档连续覆盖 1..合计名额，无缝且无重叠', () => {
    const sorted = [...FOUNDING_TIERS].sort((a, b) => a.tier - b.tier)
    assert.equal(sorted[0]!.from, 1, '首档必须从第 1 位开始')
    assert.equal(sorted[sorted.length - 1]!.to, FOUNDING_TOTAL_LIMIT, '末档必须收在合计名额上')
    for (let i = 1; i < sorted.length; i++) {
      assert.equal(sorted[i]!.from, sorted[i - 1]!.to + 1, `第 ${i + 1} 档必须紧接上一档，不留缝不重叠`)
    }
  })

  it('code 唯一、罗马数字与档位序一致', () => {
    const codes = FOUNDING_TIERS.map((t) => t.code)
    assert.equal(new Set(codes).size, codes.length, 'code 不得重复')
    const numerals = ['I', 'II', 'III']
    for (const t of [...FOUNDING_TIERS].sort((a, b) => a.tier - b.tier)) {
      assert.equal(t.numeral, numerals[t.tier - 1], `第 ${t.tier} 档的罗马数字应为 ${numerals[t.tier - 1]}`)
    }
  })

  it('每档有可渲染的视觉字段：accent 是 hex、sigil 在三款之内、name/role 非空', () => {
    for (const t of FOUNDING_TIERS) {
      assert.match(t.accent, /^#[0-9A-Fa-f]{6}$/, `${t.code} 的 accent 应为 6 位 hex`)
      assert.ok(['burst', 'ring', 'wing'].includes(t.sigil), `${t.code} 的 sigil 必须是三款冠饰之一`)
      assert.ok(t.name.trim().length > 0 && t.role.trim().length > 0, `${t.code} 的 name/role 不得为空`)
    }
  })

  it('一档名额常量与一档区间一致（官网 FOUNDING_USER_LIMIT 的语义）', () => {
    const tier1 = FOUNDING_TIERS.find((t) => t.tier === 1)!
    assert.equal(tier1.from, 1)
    assert.equal(tier1.to, FOUNDING_USER_LIMIT)
  })
})

describe('founding-tiers：查表与回退', () => {
  it('tierOfRank 落在边界上取正确档位，越界返回 undefined', () => {
    assert.equal(tierOfRank(1)?.tier, 1)
    assert.equal(tierOfRank(FOUNDING_USER_LIMIT)?.tier, 1, '一档末位')
    assert.equal(tierOfRank(FOUNDING_USER_LIMIT + 1)?.tier, 2, '二档首位')
    assert.equal(tierOfRank(FOUNDING_TOTAL_LIMIT)?.tier, 3, '三档末位')
    assert.equal(tierOfRank(0), undefined, '0 不是有效位次')
    assert.equal(tierOfRank(FOUNDING_TOTAL_LIMIT + 1), undefined, '超出合计名额不是创始用户')
  })

  it('历史 code 归到对应档，且不被当成独立档', () => {
    // 032 迁移之前发出的徽章用过旧名——不映射就会显示成裸 code
    assert.equal(tierOfBadgeCode('PROMAX_ULTRA_FOUNDER')?.tier, 1)
    assert.equal(tierOfBadgeCode('promax_ultra')?.tier, 1, '大小写变体同样要认')
    assert.equal(isFoundingBadge('PROMAX_ULTRA_FOUNDER'), true)
  })

  it('未知 code 原样回退，不吞信息、不伪造档位', () => {
    assert.equal(tierOfBadgeCode('SOME_FUTURE_BADGE'), undefined)
    assert.equal(isFoundingBadge('SOME_FUTURE_BADGE'), false)
    assert.equal(badgeDisplayName('SOME_FUTURE_BADGE'), 'SOME_FUTURE_BADGE', '未知 code 必须原样返回')
  })

  it('badgeDisplayName 输出「档位名 · 罗马数字」', () => {
    const tier1 = FOUNDING_TIERS.find((t) => t.tier === 1)!
    assert.equal(badgeDisplayName(tier1.code), `${tier1.name} · ${tier1.numeral}`)
    assert.equal(badgeDisplayName('PROMAX_ULTRA_FOUNDER'), `${tier1.name} · ${tier1.numeral}`, 'legacy code 也走同一展示口径')
  })
})
