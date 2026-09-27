/**
 * 创始用户档位（founding tiers）——零依赖共享叶子。
 *
 * ## 这是什么
 * 官网的「创始用户」体系：按注册位次分三档，每档有自己的名字、罗马数字、
 * 主题色与冠饰符号。客户端拿到的只有 `badge_code`（`user_badges.badge_code`）
 * 或位次（`get_my_founder_rank().rank`），**文案与配色不在服务端**——所以这份
 * 映射必须由客户端持有，且两端同源。
 *
 * ## 与官网的关系
 * 单一事实源在官网仓：
 *   `../tianshu-official/apps/web/src/config/founding.ts`
 * 其阈值与 Supabase 的 `assign_founder_badge_if_eligible` 授予函数一致。
 * 跨仓无法直接断言一致，因此这里靠**不变量测试**护住结构
 * （覆盖区间无缝、code 唯一、legacy 归位、未知 code 原样回退），见
 * `src/agent/__tests__/founding-tiers.test.ts`。
 * 长期方向是把 name/numeral/accent/sigil 迁到服务端 RPC 返回值——那时本叶子
 * 退化为形状定义。今天不做：两端展示面不同，过早统一会把官网的营销表现力绑死。
 *
 * ## 命名注意
 * 本模块的 `founding` 指**创始用户体系**。`desktop/src/runtime/types.ts` 里另有
 * `founder`（星域创始者短名，来自 star-genesis），两者无关——新增消费方别混。
 *
 * ## 为什么零依赖
 * 它要经 `src/server/ui-shared.ts` re-export 给桌面端，而 `desktop/scripts/check-boundary.js`
 * 规定：闭包内每个叶子模块**只允许 type-only import**（value import 会把内核运行时
 * 拖进桌面端 bundle）。所以这里只有纯数据与纯函数，没有任何 import。
 */

/** 顶部冠饰符号——三款各一条独立路径（迸发 / 恒星环 / 双翼箭），不是同款换色。 */
export type FounderSigil = 'burst' | 'ring' | 'wing'

export interface FoundingTier {
  /** 1 最早、最尊贵 */
  tier: 1 | 2 | 3
  /** 数据库 badge_code */
  code: string
  /** 档位中文名（专有名词，与官网逐字一致） */
  name: string
  /** 罗马数字（铭牌上显示） */
  numeral: string
  /** 位次区间，含端点 */
  from: number
  /** 位次区间，含端点 */
  to: number
  /** 一句话意象——铭牌展开态的角色副标 */
  role: string
  /** 档位主题色（铭牌描边与发光同源） */
  accent: string
  /** 顶部冠饰 */
  sigil: FounderSigil
}

/** 一档（创世）名额。 */
export const FOUNDING_USER_LIMIT = 300

/** 三档合计名额（I 300 + II 300 + III 400）。 */
export const FOUNDING_TOTAL_LIMIT = 1000

export const FOUNDING_TIERS: readonly FoundingTier[] = [
  {
    tier: 1,
    code: 'FOUNDER_TIER_1',
    name: '创世星约',
    numeral: 'I',
    from: 1,
    to: 300,
    role: '星轨的起点 · 你点燃了它',
    accent: '#E7C9FF',
    sigil: 'burst',
  },
  {
    tier: 2,
    code: 'FOUNDER_TIER_2',
    name: '守望星约',
    numeral: 'II',
    from: 301,
    to: 600,
    role: '恒星不熄 · 你让它亮着',
    accent: '#9BEAF5',
    sigil: 'ring',
  },
  {
    tier: 3,
    code: 'FOUNDER_TIER_3',
    name: '同行星约',
    numeral: 'III',
    from: 601,
    to: 1000,
    role: '彗尾划过 · 你与我们同路',
    accent: '#FFB48A',
    sigil: 'wing',
  },
]

/**
 * 历史命名别名——032 迁移之前发出的徽章曾用这些 code。
 * 保留映射，让迁移前后的数据都能正确归档（否则老用户看到的是一个裸 code）。
 * 查表时先精确匹配、再按大写回退，大小写变体一并认下。
 */
const LEGACY_CODES: Record<string, string> = {
  PROMAX_ULTRA_FOUNDER: 'FOUNDER_TIER_1',
  promax_ultra: 'FOUNDER_TIER_1',
}

/** 规范化 code：历史别名 → 当前 code（未知 code 原样返回）。 */
function canonicalCode(code: string): string {
  return LEGACY_CODES[code] ?? LEGACY_CODES[code.toUpperCase()] ?? code
}

/** badge_code → 档位定义（兼容历史命名）；非创始 / 未知 code 返回 undefined。 */
export function tierOfBadgeCode(code: string): FoundingTier | undefined {
  const canonical = canonicalCode(code)
  return FOUNDING_TIERS.find((t) => t.code === canonical)
}

/** 注册位次 → 档位定义；超出合计名额返回 undefined。 */
export function tierOfRank(rank: number): FoundingTier | undefined {
  return FOUNDING_TIERS.find((t) => rank >= t.from && rank <= t.to)
}

/** 该 code 是否属于创始体系（含历史命名）——调用方据此决定要不要渲染铭牌。 */
export function isFoundingBadge(code: string): boolean {
  return tierOfBadgeCode(code) !== undefined
}

/**
 * 铭牌展示名：创始档位 → "创世星约 · I"；其余 code 原样回退（不吞信息）。
 * 与官网 `badgeDisplayName` 同口径——两处各写一份 label 表是老的坏味道。
 */
export function badgeDisplayName(code: string): string {
  const tier = tierOfBadgeCode(code)
  return tier ? `${tier.name} · ${tier.numeral}` : code
}
