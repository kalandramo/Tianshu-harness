/**
 * cache advisor 的 delay-compact 判定 oracle 生成器。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/delaycompact/gen-oracle.ts
 *
 * 覆盖 src/cache/advisor.ts 的 `shouldDelayCompact` 与 src/cache/session-warmth.ts
 * 的 `SessionWarmthTracker.predict`。
 *
 * ## 为什么这块值得对账
 *
 * delay 判定的存在理由是**缓存经济学**：热缓存时压缩会击碎已付费的前缀，
 * 而 1M 窗口的余量足以等到压力升高再压。判定错了的代价是双面的——
 * 该延迟不延迟 → 白白重建前缀；不该延迟却延迟 → 上下文爆掉。
 *
 * 三条分支（reactive-tier 短路 / protection 公式 / legacy 回退）各有阈值，
 * 手抄必错。oracle 走真实类实例 + 注入 `now` 函数（TS 本就支持注入，
 * 故 warmth 也能确定性地对账）。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { CacheAdvisor } from '../../../src/cache/advisor.js'
import { SessionWarmthTracker } from '../../../src/cache/session-warmth.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 1. SessionWarmthTracker.predict 矩阵 ────────────────────────
// 用注入 now 控制时间流逝。
const T0 = 1_000_000_000_000
const warmthCases: Array<{ name: string; elapsedMs: number; ttlMs?: number; neverCalled?: boolean }> = [
  { name: 'never_called', elapsedMs: 0, neverCalled: true },
  { name: 'just_called', elapsedMs: 0 },
  { name: 'hot_59s', elapsedMs: 59_999 },
  { name: 'boundary_60s', elapsedMs: 60_000 },
  { name: 'warm_1h_minus_1', elapsedMs: 3_599_999 },
  { name: 'boundary_ttl', elapsedMs: 3_600_000 },
  { name: 'cold_beyond', elapsedMs: 3_600_001 },
  { name: 'custom_ttl_hot', elapsedMs: 59_999, ttlMs: 120_000 },
  { name: 'custom_ttl_warm', elapsedMs: 60_000, ttlMs: 120_000 },
  { name: 'custom_ttl_cold', elapsedMs: 120_000, ttlMs: 120_000 },
]

const warmth: Record<string, { temperature: string; opportunistic: boolean }> = {}
for (const c of warmthCases) {
  let nowVal = T0
  const tracker = new SessionWarmthTracker({ now: () => nowVal, ttlMs: c.ttlMs })
  if (!c.neverCalled) {
    tracker.recordApiCall()
    nowVal = T0 + c.elapsedMs
  }
  warmth[c.name] = { temperature: tracker.predict(), opportunistic: tracker.shouldOpportunisticCompact() }
}

// ── 2. shouldDelayCompact 判定矩阵 ──────────────────────────────
// 用真实 CacheAdvisor 实例。recentHitRate 经 onTurnEnd 注入（无 API 依赖）。
/** 构造 CacheAdvisor（**不**在此注入 warmth 状态——由调用方按用例控制）。 */
function makeAdvisor(hitRate: number | null, warmthNow?: () => number): CacheAdvisor {
  const adv = new CacheAdvisor({
    providerProfile: { cacheType: 'exact-prefix', persistent: true },
    ...(warmthNow ? { now: warmthNow } : {}),
  })
  if (hitRate !== null) {
    // 注入 hitRate：cacheRead / (cacheRead + cacheCreation)。
    adv.onTurnEnd({
      turn: 1,
      cacheRead: hitRate * 1000,
      cacheCreation: (1 - hitRate) * 1000,
      prefixChanged: false,
      artifactIdsEvicted: [],
      artifactIdsAccessed: [],
    } as never)
  }
  return adv
}

type DelayCase = {
  name: string
  hitRate: number | null
  tier: number
  ctx?: { estimatedTokens: number; contextWindow: number }
  /**
   * 判定前的 warmth 状态：
   *   'cold'（默认）——从未调用过 API（lastApiCallTime = 0）
   *   number        ——曾在 T0 调用，推进该毫秒数后判定
   */
  warmth?: 'cold' | number
}

const delayCases: DelayCase[] = [
  // reactive 短路：tier>=3 永不延迟（即使热缓存 + 低压力）
  { name: 'tier3_never_delays', hitRate: 0.99, tier: 3, ctx: { estimatedTokens: 1000, contextWindow: 1_000_000 } },
  { name: 'tier4_never_delays', hitRate: 0.99, tier: 4, ctx: { estimatedTokens: 1000, contextWindow: 1_000_000 } },

  // protection 公式：hitRate × (1 − pressure)
  // 热缓存 + 低压力 → protection 高 → delay
  { name: 'hot_low_pressure_delays', hitRate: 0.95, tier: 1, ctx: { estimatedTokens: 100_000, contextWindow: 1_000_000 } },
  // 热缓存 + 高压力 → protection 低 → allow（压力压倒缓存保护）
  { name: 'hot_high_pressure_allows', hitRate: 0.95, tier: 1, ctx: { estimatedTokens: 900_000, contextWindow: 1_000_000 } },
  // protection 边界 0.45：0.9 × (1 − 0.5) = 0.45 → 恰好 >= 0.45 → delay
  { name: 'protection_boundary_exact', hitRate: 0.9, tier: 2, ctx: { estimatedTokens: 500_000, contextWindow: 1_000_000 } },
  // 刚好低于边界：0.89 × 0.5 = 0.445 → allow
  { name: 'protection_just_below', hitRate: 0.89, tier: 2, ctx: { estimatedTokens: 500_000, contextWindow: 1_000_000 } },
  // 冷缓存 + 低压力 → protection 低；warmth 也是 cold（远超 TTL）→ allow
  { name: 'cold_low_pressure_allows', hitRate: 0.1, tier: 1, ctx: { estimatedTokens: 100_000, contextWindow: 1_000_000 }, warmth: 4_000_000 },
  // warmth-hot 支：protection 不足但 warmth 热 + tier<=1 + pressure<0.5 → delay
  // 需要 hitRate 低使 protection < 0.45，但 warmth=hot（elapsed<60s）
  { name: 'warmth_hot_branch_delays', hitRate: 0.4, tier: 1, ctx: { estimatedTokens: 100_000, contextWindow: 1_000_000 }, warmth: 30_000 },
  // warmth-hot 但 tier=2 → 不满足 tier<=1 → allow
  { name: 'warmth_hot_tier2_allows', hitRate: 0.4, tier: 2, ctx: { estimatedTokens: 100_000, contextWindow: 1_000_000 }, warmth: 30_000 },
  // warmth-hot 但 pressure >= 0.5 → allow
  { name: 'warmth_hot_high_pressure_allows', hitRate: 0.4, tier: 1, ctx: { estimatedTokens: 600_000, contextWindow: 1_000_000 }, warmth: 30_000 },

  // legacy 回退（无 ctx）
  { name: 'legacy_hitrate_delays', hitRate: 0.85, tier: 1, warmth: 4_000_000 },
  { name: 'legacy_hitrate_boundary', hitRate: 0.8, tier: 1, warmth: 4_000_000 },
  // hitRate 低于 0.8 但 warmth 仍 hot（30s 内）→ legacy-warmth
  { name: 'legacy_hitrate_below_warmth_hot', hitRate: 0.79, tier: 1, warmth: 30_000 },
  // hitRate 低于 0.8 且 warmth cold → legacy-allow
  { name: 'legacy_hitrate_below_cold_allows', hitRate: 0.79, tier: 1, warmth: 4_000_000 },
  { name: 'legacy_warmth_hot_delays', hitRate: 0.5, tier: 1, warmth: 30_000 },
  { name: 'legacy_warmth_hot_tier2_allows', hitRate: 0.5, tier: 2, warmth: 30_000 },
  { name: 'legacy_allow', hitRate: 0.5, tier: 1, warmth: 200_000 },

  // hitRate 为 null（未收到任何 turn 指标）
  // **cold 与 hot 各一**——两者都该放行（命中率未知时无缓存保护依据），
  // 但走的分支不同（cold → legacy-allow；hot → legacy-warmth）。
  { name: 'null_hitrate_with_ctx_cold', hitRate: null, tier: 1, ctx: { estimatedTokens: 100_000, contextWindow: 1_000_000 }, warmth: 'cold' },
  { name: 'null_hitrate_no_ctx_cold', hitRate: null, tier: 1, warmth: 'cold' },
  { name: 'null_hitrate_no_ctx_hot', hitRate: null, tier: 1, warmth: 30_000 },
]

const delay: Record<string, { decision: boolean; reason: string }> = {}
for (const c of delayCases) {
  let nowVal = T0
  const warmthNow = () => nowVal
  const adv = makeAdvisor(c.hitRate, warmthNow)

  // warmth 状态显式控制：
  //   'cold'（或省略）→ 从未调用过 API（lastApiCallTime 保持 0）
  //   number          → 在 T0 调用一次，推进该毫秒数后判定
  //
  // ⚠️ 首版这里有 bug：hitRate 为 null 时 makeAdvisor 不调 onTurnEnd，
  // 于是 warmth 停在 cold，而用例名暗示 hot——名实不符。
  if (typeof c.warmth === 'number') {
    adv.onTurnEnd({
      turn: 2, cacheRead: 0, cacheCreation: 0, prefixChanged: false,
      artifactIdsEvicted: [], artifactIdsAccessed: [],
    } as never)
    nowVal = T0 + c.warmth
  }

  // 捕获 decision ledger 以取 reason（observe-only，不影响判定）。
  let captured: { decision: string; reason: string } | null = null
  adv.setDelayDecisionListener((d) => { captured = { decision: d.decision, reason: d.reason } })
  const decision = adv.shouldDelayCompact(c.tier, c.ctx)
  delay[c.name] = {
    decision,
    reason: captured ? (captured as { decision: string; reason: string }).reason : '',
  }
}

const out = { warmth, delay }

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `delaycompact oracle：${warmthCases.length} 个 warmth + ${delayCases.length} 个 delay 用例 — sha256 ${sha.slice(0, 16)}`,
)
