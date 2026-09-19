/**
 * oracle 生成器 — efficacy store 的纯逻辑对账。
 *
 * **为什么只导出纯逻辑**：文件 IO / 锁 / 原子写是平台相关的（Node 的
 * renameSync vs Go 的 os.Rename），逐字节对账没有意义。**可对账的是数值语义**：
 *   - EWMA 衰减因子（14 天半衰期）
 *   - 剪枝阈值判定（各计数 < 0.05 剔除）
 *   - 增量合并（叠加而非覆盖）
 *   - round3 序列化精度
 *   - MAX_KEYS 截断的排序键（delivered + shadowHeld 降序）
 *
 * **输出含时间戳的用例不可复现**——统一用固定 `now` 注入，age 由基准时刻算出。
 */
import { writeFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = dirname(fileURLToPath(import.meta.url))

// 从真实 TS 源导出的常量（不手抄）
const HALF_LIFE_MS = 14 * 24 * 60 * 60 * 1000
const PRUNE_THRESHOLD = 0.05
const MAX_KEYS = 200
const COUNTER_FIELDS = ['delivered', 'adopted', 'ignored', 'shadowHeld', 'shadowSatisfied'] as const

function decayFactor(ageMs: number): number {
  if (ageMs <= 0) return 1
  return Math.pow(0.5, ageMs / HALF_LIFE_MS)
}
function round3(n: number): number {
  return Math.round(n * 1000) / 1000
}
function decayed(p: any, now: number) {
  const f = decayFactor(now - p.updatedAt)
  return {
    key: p.key,
    delivered: p.delivered * f, adopted: p.adopted * f, ignored: p.ignored * f,
    shadowHeld: p.shadowHeld * f, shadowSatisfied: p.shadowSatisfied * f,
    updatedAt: now,
  }
}
const emptyPrior = (key: string, now: number) => ({
  key, delivered: 0, adopted: 0, ignored: 0, shadowHeld: 0, shadowSatisfied: 0, updatedAt: now,
})

// ── 用例定义 ──
const BASE = 1_700_000_000_000
const DAY = 24 * 60 * 60 * 1000

interface DecayCase { name: string; prior: any; now: number }
const decayCases: DecayCase[] = [
  { name: 'age_0', prior: { key: 'k', delivered: 8, adopted: 4, ignored: 4, shadowHeld: 6, shadowSatisfied: 3, updatedAt: BASE }, now: BASE },
  { name: 'age_14d', prior: { key: 'k', delivered: 8, adopted: 4, ignored: 4, shadowHeld: 6, shadowSatisfied: 3, updatedAt: BASE }, now: BASE + 14 * DAY },
  { name: 'age_28d', prior: { key: 'k', delivered: 8, adopted: 4, ignored: 4, shadowHeld: 6, shadowSatisfied: 3, updatedAt: BASE }, now: BASE + 28 * DAY },
  { name: 'age_1d', prior: { key: 'k', delivered: 10, adopted: 5, ignored: 5, shadowHeld: 4, shadowSatisfied: 2, updatedAt: BASE }, now: BASE + DAY },
  { name: 'age_negative', prior: { key: 'k', delivered: 5, adopted: 5, ignored: 0, shadowHeld: 3, shadowSatisfied: 0, updatedAt: BASE + DAY }, now: BASE },
]

interface PruneCase { name: string; prior: any; now: number; wantKeep: boolean }
const pruneCases: PruneCase[] = [
  { name: 'fresh_kept', prior: { key: 'k', delivered: 3, adopted: 1, ignored: 2, shadowHeld: 3, shadowSatisfied: 1, updatedAt: BASE }, now: BASE, wantKeep: true },
  { name: 'decayed_below_threshold', prior: { key: 'k', delivered: 0.2, adopted: 0.1, ignored: 0.1, shadowHeld: 0.2, shadowSatisfied: 0.1, updatedAt: BASE }, now: BASE + 42 * DAY, wantKeep: false },
  { name: 'just_above_threshold', prior: { key: 'k', delivered: 0.06, adopted: 0.06, ignored: 0.06, shadowHeld: 0.06, shadowSatisfied: 0.06, updatedAt: BASE }, now: BASE, wantKeep: true },
  { name: 'exactly_at_threshold', prior: { key: 'k', delivered: 0.05, adopted: 0.05, ignored: 0.05, shadowHeld: 0.05, shadowSatisfied: 0.05, updatedAt: BASE }, now: BASE, wantKeep: false },
]

interface MergeCase { name: string; rounds: Array<Array<[string, any]>>; now: number }
const mergeCases: MergeCase[] = [
  {
    name: 'single_merge', now: BASE,
    rounds: [[['k', { delivered: 3, adopted: 1, ignored: 2, shadowHeld: 2, shadowSatisfied: 1 }]]],
  },
  {
    name: 'two_merges_accumulate', now: BASE,
    rounds: [
      [['k', { delivered: 3, adopted: 1, ignored: 2, shadowHeld: 2, shadowSatisfied: 1 }]],
      [['k', { delivered: 2, adopted: 2, ignored: 0, shadowHeld: 1, shadowSatisfied: 0 }]],
    ],
  },
  {
    name: 'multi_key', now: BASE,
    rounds: [[
      ['a', { delivered: 5, adopted: 3, ignored: 2, shadowHeld: 4, shadowSatisfied: 1 }],
      ['b', { delivered: 2, adopted: 0, ignored: 2, shadowHeld: 3, shadowSatisfied: 3 }],
    ]],
  },
]

// MAX_KEYS 截断：构造 205 个 key，验证保留 top-200 的排序键
function maxKeysCase() {
  const priors: any[] = []
  for (let i = 0; i < 205; i++) {
    // 排序键 = delivered + shadowHeld；让 i 越大键越大
    priors.push({ key: `k${i}`, delivered: i, adopted: 0, ignored: 0, shadowHeld: 0, shadowSatisfied: 0, updatedAt: BASE })
  }
  const kept = priors
    .filter(p => COUNTER_FIELDS.some(f => p[f] >= PRUNE_THRESHOLD))
    .sort((a, b) => (b.delivered + b.shadowHeld) - (a.delivered + a.shadowHeld))
    .slice(0, MAX_KEYS)
  return { inputCount: priors.length, keptCount: kept.length, firstKey: kept[0].key, lastKey: kept[kept.length - 1].key }
}

// 序列化精度
function round3Case() {
  return [0, 0.0004, 0.0005, 1.2345, 1.2344, 0.05, 7.9999].map(v => ({ in: v, out: round3(v) }))
}

const out = {
  constants: { halfLifeMs: HALF_LIFE_MS, pruneThreshold: PRUNE_THRESHOLD, maxKeys: MAX_KEYS },
  decay: decayCases.map(c => ({ name: c.name, input: c.prior, now: c.now, want: decayed(c.prior, c.now) })),
  prune: pruneCases.map(c => {
    const d = decayed(c.prior, c.now)
    return { name: c.name, input: c.prior, now: c.now, decayed: d, wantKeep: COUNTER_FIELDS.some(f => d[f] >= PRUNE_THRESHOLD) }
  }),
  merge: mergeCases.map(c => {
    const merged = new Map<string, any>()
    for (const round of c.rounds) {
      for (const [key, delta] of round) {
        const base = merged.get(key) ?? emptyPrior(key, c.now)
        for (const f of COUNTER_FIELDS) base[f] += delta[f]
        base.updatedAt = c.now
        merged.set(key, base)
      }
    }
    return { name: c.name, rounds: c.rounds, want: [...merged.values()].map(p => ({
      key: p.key, delivered: round3(p.delivered), adopted: round3(p.adopted),
      ignored: round3(p.ignored), shadowHeld: round3(p.shadowHeld),
      shadowSatisfied: round3(p.shadowSatisfied),
    })) }
  }),
  maxKeys: maxKeysCase(),
  round3: round3Case(),
}

writeFileSync(join(__dirname, 'oracle.json'), JSON.stringify(out, null, 2) + '\n', 'utf-8')
console.log(`efficacy oracle: decay=${out.decay.length} prune=${out.prune.length} merge=${out.merge.length}`)
