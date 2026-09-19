/**
 * oracle 生成器 — PressureMonitor + compact 阈值策略层的对账。
 *
 * **对账范围**：纯逻辑部分
 *   - `compactPolicyRatios`（三策略分层 + provider 覆盖合并）
 *   - `strategyForCacheType`（cacheType × persistent → 策略）
 *   - `adaptiveCompactPolicyRatios`（缓存命中率自适应调整）
 *   - `precisionCeilingRatio`（窗口派生的精度天花板）
 *   - `tierForRatio`（五档判定 + 精度天花板的地板语义）
 *   - `PressureMonitor.check`（八字段 + 相对压力）
 *   - `p90` / thrashing 检测
 *
 * **不对账**：CVM 记账的调用时序（那是接线问题，由 Go 侧行为测试覆盖）。
 *
 * **可复现性**：所有用例注入固定 turn/tokens 序列，不含真实时间戳。
 */
import { writeFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  compactPolicyRatios, adaptiveCompactPolicyRatios, precisionCeilingRatio,
  LARGE_CONTEXT_WINDOW_TOKENS,
} from '../../../src/compact/constants.js'
import { tierForRatio } from '../../../src/context/compact-policy.js'
import { PressureMonitor } from '../../../src/context/pressure-monitor.js'

const __dirname = dirname(fileURLToPath(import.meta.url))

// ── 策略层：三策略的基准比值 ──
const strategies = [
  { name: 'none-persistent', cacheType: 'none', persistent: true },
  { name: 'none-ephemeral', cacheType: 'none', persistent: false },
  { name: 'exact-prefix-persistent', cacheType: 'exact-prefix', persistent: true },
  { name: 'exact-prefix-ephemeral', cacheType: 'exact-prefix', persistent: false },
  { name: 'explicit-breakpoint', cacheType: 'explicit-breakpoint', persistent: false },
  { name: 'partial-prefix', cacheType: 'partial-prefix', persistent: true },
  { name: 'block-kv', cacheType: 'block-kv', persistent: false },
  { name: 'no-profile', cacheType: undefined, persistent: undefined },
] as const

const strategyCases = strategies.map(s => {
  const profile = s.cacheType === undefined ? undefined : { cacheType: s.cacheType, persistent: s.persistent }
  return { name: s.name, ratios: compactPolicyRatios(profile as any) }
})

// ── 自适应调整（缓存命中率）──
const adaptiveCases = [0, 0.2, 0.29, 0.3, 0.5, 0.84, 0.85, 0.9, 1.0].map(rate => ({
  hitRate: rate,
  balanced: adaptiveCompactPolicyRatios(undefined, rate),
  cachePreserving: adaptiveCompactPolicyRatios({ cacheType: 'exact-prefix', persistent: true } as any, rate),
}))

// ── 精度天花板（窗口派生）──
const ceilingCases = [
  { window: 1000, override: undefined },
  { window: 199_999, override: undefined },
  { window: 200_000, override: undefined },
  { window: 499_999, override: undefined },
  { window: 500_000, override: undefined },
  { window: 1_000_000, override: undefined },
  { window: 1_000_000, override: 0.42 },
  { window: 1_000_000, override: 0 },
  { window: 1_000_000, override: 1 },
  { window: 1_000_000, override: 1.5 },
].map(c => ({
  ...c,
  want: precisionCeilingRatio(c.window, c.override),
}))

// ── tierForRatio 逐值（含精度天花板的地板语义）──
// 关键用例：精度天花板（0.7）低于 watch 阈值（cache-preserving 的 0.72）时，
// 必须是**地板**而非回退分支——否则 ratio 0.71 压缩而 0.75 只 watch（非单调）。
const tierCases: Array<{ name: string; ratio: number; profile?: any; hitRate?: number | null; ceiling?: number }> = [
  { name: 'balanced-0.0', ratio: 0.0 },
  { name: 'balanced-watch-boundary', ratio: 0.6 },
  { name: 'balanced-compact-boundary', ratio: 0.78 },
  { name: 'balanced-reactive-boundary', ratio: 0.88 },
  { name: 'balanced-ceiling-boundary', ratio: 0.95 },
  { name: 'balanced-above-ceiling', ratio: 0.99 },
  { name: 'cache-preserving-0.71', ratio: 0.71, profile: { cacheType: 'exact-prefix', persistent: true } },
  { name: 'cache-preserving-0.72', ratio: 0.72, profile: { cacheType: 'exact-prefix', persistent: true } },
  { name: 'cache-preserving-0.86', ratio: 0.86, profile: { cacheType: 'exact-prefix', persistent: true } },
  { name: 'aggressive-0.5', ratio: 0.5, profile: { cacheType: 'none', persistent: false } },
  { name: 'aggressive-0.7', ratio: 0.7, profile: { cacheType: 'none', persistent: false } },
  { name: 'hot-cache-delays', ratio: 0.65, hitRate: 0.9 },
  { name: 'cold-cache-advances', ratio: 0.65, hitRate: 0.1 },
  { name: 'null-hitrate-falls-back', ratio: 0.65, hitRate: null },
  // 精度天花板：0.7 恰好落在 cache-preserving 的 watch(0.72) 之下
  { name: 'precision-ceiling-floor', ratio: 0.71, profile: { cacheType: 'exact-prefix', persistent: true }, ceiling: 0.7 },
  { name: 'precision-ceiling-above', ratio: 0.75, profile: { cacheType: 'exact-prefix', persistent: true }, ceiling: 0.7 },
  { name: 'precision-ceiling-not-hit', ratio: 0.65, profile: { cacheType: 'exact-prefix', persistent: true }, ceiling: 0.7 },
]

const tierResults = tierCases.map(c => ({
  ...c,
  want: tierForRatio(c.ratio, c.profile, c.hitRate ?? undefined, c.ceiling),
}))

// ── PressureMonitor.check 的八字段演化 ──
// 用固定 token 序列驱动（窗口 1M，走 LARGE 分支 → 精度天花板 0.7）
function runCheckSequence(window: number, seq: Array<{ tokens: number; turn: number }>) {
  const m = new PressureMonitor(window)
  return seq.map(s => {
    const r = m.check(s.tokens, s.turn)
    return {
      turn: s.turn,
      tokens: s.tokens,
      tier: r.tier,
      shouldCompact: r.shouldCompact,
      thrashing: r.thrashing,
      fastGrowth: r.fastGrowth,
      ratio: r.ratio,
      growthRate: r.growthRate,
      cvmOverheadRatio: r.cvmOverheadRatio,
      shouldThrottleCvm: r.shouldThrottleCvm,
      pressureRelative: r.pressureRelative ?? null,
      suggestion: r.suggestion ?? null,
    }
  })
}

const checkSequences = [
  {
    name: 'steady-growth',
    window: 1_000_000,
    seq: [
      { tokens: 100_000, turn: 1 },
      { tokens: 120_000, turn: 2 },
      { tokens: 140_000, turn: 3 },
      { tokens: 160_000, turn: 4 },
      { tokens: 180_000, turn: 5 },
      { tokens: 200_000, turn: 6 },
      { tokens: 220_000, turn: 7 },
    ],
  },
  {
    name: 'fast-growth-spike',
    window: 1_000_000,
    seq: [
      { tokens: 100_000, turn: 1 },
      { tokens: 300_000, turn: 2 }, // +0.20 ratio → fastGrowth
      { tokens: 320_000, turn: 3 },
    ],
  },
  {
    name: 'crossing-tiers',
    window: 1_000_000,
    seq: [
      { tokens: 100_000, turn: 1 },
      { tokens: 600_000, turn: 2 },
      { tokens: 720_000, turn: 3 },
      { tokens: 860_000, turn: 4 },
      { tokens: 920_000, turn: 5 },
      { tokens: 960_000, turn: 6 },
    ],
  },
  {
    name: 'small-window',
    window: 1000,
    seq: [
      { tokens: 100, turn: 1 },
      { tokens: 600, turn: 2 },
      { tokens: 700, turn: 3 },
    ],
  },
]

const checkResults = checkSequences.map(c => ({
  name: c.name,
  window: c.window,
  want: runCheckSequence(c.window, c.seq),
}))

// ── CVM 开销节流 ──
function cvmCase(window: number, injections: number[]) {
  const m = new PressureMonitor(window)
  const out: any[] = []
  for (const inj of injections) {
    m.recordCvmInjection(inj, 'projection')
    out.push({
      injected: inj,
      ratio: m.getCvmOverheadRatio(),
      throttling: m.isCvmThrottling(),
      ceiling: m.isCvmThrottlingCeiling(),
    })
  }
  return out
}

const cvmCases = [
  { name: 'below-threshold', window: 1_000_000, injections: [10_000, 20_000] },
  { name: 'crossing-5pct', window: 1_000_000, injections: [30_000, 30_000] },
  { name: 'crossing-8pct', window: 1_000_000, injections: [30_000, 60_000] },
]

const cvmResults = cvmCases.map(c => ({ name: c.name, window: c.window, want: cvmCase(c.window, c.injections) }))

// ── thrashing 检测（近 4 轮内压缩 ≥ 3 次）──
function thrashCase(compactions: number[], currentTurn: number) {
  const m = new PressureMonitor(1_000_000)
  for (const t of compactions) m.recordCompaction(t)
  return m.check(100_000, currentTurn).thrashing
}

const thrashCases = [
  { name: 'three-within-4', compactions: [1, 2, 3], turn: 4 },
  { name: 'three-but-spread', compactions: [1, 2, 10], turn: 11 },
  { name: 'two-only', compactions: [1, 2], turn: 3 },
  { name: 'boundary-exactly-4', compactions: [0, 1, 2], turn: 4 },
  { name: 'boundary-5-away', compactions: [0, 1, 2], turn: 5 },
].map(c => ({ ...c, want: thrashCase(c.compactions, c.turn) }))

const out = {
  constants: { largeContextWindowTokens: LARGE_CONTEXT_WINDOW_TOKENS },
  strategies: strategyCases,
  adaptive: adaptiveCases,
  ceilings: ceilingCases,
  tiers: tierResults,
  checks: checkResults,
  cvm: cvmResults,
  thrashing: thrashCases,
}

writeFileSync(join(__dirname, 'oracle.json'), JSON.stringify(out, null, 2) + '\n', 'utf-8')
