/**
 * reclaim gate + compaction profile oracle 生成器。
 *
 * 生成：
 *   node_modules/.bin/tsx go/testdata/reclaim/gen-oracle.ts
 *
 * 覆盖 src/compact/compaction-profile.ts 与 src/compact/reclaim-estimate.ts：
 *   - deriveCompactionProfile —— 阈值矩阵（windowBand × billing × cache）
 *   - estimateReclaim —— token 数学（含「回收为负」与「未改动」两态）
 *   - shouldCommitReclaim —— 五条分支（unchanged / forced / no-reclaim /
 *     below-floor / above-floor）
 *
 * ## 为什么这块特别需要对账
 *
 * reclaim gate 的存在理由是**经济**：确定性重写曾无条件提交，导致只回收
 * 617–1701 token（甚至让输入变大）却击碎 200k+ token 的热前缀缓存。
 * 阈值算错不会报错——只会让压缩要么该压不压（上下文爆）、要么亏本压
 * （缓存反复重建）。没有 oracle，这些数字错了也看不出来。
 *
 * token 数学用 `estimateOaiTokens`（与压缩阶梯同一估算器）——不是消息条数、
 * 不是原始字符长度。oracle 用真实函数调用而非手抄阈值表。
 */
import { writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { createHash } from 'node:crypto'
import { deriveCompactionProfile, windowBandFor } from '../../../src/compact/compaction-profile.js'
import { estimateReclaim, shouldCommitReclaim, buildReclaimDecision } from '../../../src/compact/reclaim-estimate.js'
import type { CompactionProfileInput, CompactionProfile } from '../../../src/compact/compaction-profile.js'
import type { OaiMessage } from '../../../src/api/oai-types.js'

const here = dirname(fileURLToPath(import.meta.url))

// ── 1. windowBandFor 边界 ────────────────────────────────────────
const windowBands: Record<string, string> = {}
for (const w of [0, 1, 199_999, 200_000, 200_001, 499_999, 500_000, 1_000_000]) {
  windowBands[String(w)] = windowBandFor(w)
}

// ── 2. deriveCompactionProfile 阈值矩阵 ─────────────────────────
// 三档 × 两种 billing × 三种 cache —— 但只有 (per-token, exact-prefix)
// 与「其余」两组阈值，故取代表性组合。
const profileInputs: Array<{ name: string; input: CompactionProfileInput }> = [
  // small/medium + per-token + exact-prefix → max(8192, floor(w*0.03)), ratio 0.03
  { name: 'small_pertoken_exact', input: { contextWindow: 100_000, billing: 'per-token', cache: 'exact-prefix' } },
  { name: 'medium_pertoken_exact', input: { contextWindow: 200_000, billing: 'per-token', cache: 'exact-prefix' } },
  // 边界：floor(w*0.03) 恰好低于 8192 时取 8192
  { name: 'tiny_pertoken_exact', input: { contextWindow: 100_000, billing: 'per-token', cache: 'exact-prefix' } },
  { name: 'verytiny_pertoken_exact', input: { contextWindow: 10_000, billing: 'per-token', cache: 'exact-prefix' } },
  // large + per-token + exact-prefix → max(32768, floor(w*0.05)), ratio 0.05
  { name: 'large_pertoken_exact', input: { contextWindow: 500_000, billing: 'per-token', cache: 'exact-prefix' } },
  { name: 'large_pertoken_exact_1m', input: { contextWindow: 1_000_000, billing: 'per-token', cache: 'exact-prefix' } },
  // 其余（subscription 或 cache none/partial）→ max(4096, floor(w*0.01)), ratio 0.01
  { name: 'sub_exact', input: { contextWindow: 200_000, billing: 'subscription', cache: 'exact-prefix' } },
  { name: 'pertoken_none', input: { contextWindow: 200_000, billing: 'per-token', cache: 'none' } },
  { name: 'pertoken_partial', input: { contextWindow: 200_000, billing: 'per-token', cache: 'partial' } },
  { name: 'sub_none_large', input: { contextWindow: 1_000_000, billing: 'subscription', cache: 'none' } },
  // outputReserveTokens 透传
  { name: 'with_output_reserve', input: { contextWindow: 200_000, billing: 'subscription', cache: 'none', outputReserveTokens: 8192 } },
]

const profiles: Record<string, CompactionProfile> = {}
for (const c of profileInputs) {
  profiles[c.name] = deriveCompactionProfile(c.input)
}

// ── 3. estimateReclaim / shouldCommitReclaim 判定矩阵 ────────────
// 用真实 OaiMessage 形态构造前后列表。
const msg = (role: string, content: string): OaiMessage => ({ role, content } as OaiMessage)

/** 构造 N 条等长消息。 */
function msgs(n: number, text: string): OaiMessage[] {
  return Array.from({ length: n }, (_, i) => msg('user', `${text} ${i}`))
}

type ReclaimCase = {
  name: string
  before: OaiMessage[]
  after: OaiMessage[]
  profile: CompactionProfile
  force: boolean
}

const baseProfile = deriveCompactionProfile({
  contextWindow: 200_000,
  billing: 'per-token',
  cache: 'exact-prefix',
})

const subProfile = deriveCompactionProfile({
  contextWindow: 200_000,
  billing: 'subscription',
  cache: 'none',
})

const sameRefMsgs = msgs(10, 'x')

const reclaimCases: ReclaimCase[] = [
  // unchanged：**同一引用**（命中 messagesChanged 的 `before === after` 短路）
  { name: 'unchanged_same_ref', before: sameRefMsgs, after: sameRefMsgs, profile: baseProfile, force: false },
  // unchanged：不同引用但字节相同（走 JSON.stringify 逐条比对）
  { name: 'unchanged_same_bytes', before: msgs(10, 'hello'), after: msgs(10, 'hello'), profile: baseProfile, force: false },
  // no-reclaim：候选更大（回收为负）
  { name: 'negative_reclaim', before: msgs(5, 'hello'), after: msgs(10, 'hello'), profile: baseProfile, force: false },
  // below-floor：回收一点但不够
  { name: 'below_floor_small', before: msgs(10, 'hello'), after: msgs(9, 'hello'), profile: baseProfile, force: false },
  // above-floor：大量回收
  { name: 'above_floor_big', before: msgs(1000, 'hello world this is a long message'), after: msgs(10, 'hello world this is a long message'), profile: baseProfile, force: false },
  // force：即使 below-floor 也提交
  { name: 'force_below_floor', before: msgs(10, 'hello'), after: msgs(9, 'hello'), profile: baseProfile, force: true },
  // force + unchanged：unchanged 优先（force 不提交未改动）
  { name: 'force_unchanged', before: msgs(10, 'hello'), after: msgs(10, 'hello'), profile: baseProfile, force: true },
  // 订阅制阈值低——同一候选在订阅制下可能过闸
  { name: 'sub_profile_below', before: msgs(10, 'hello'), after: msgs(9, 'hello'), profile: subProfile, force: false },
]

const reclaim: Record<string, unknown> = {}
for (const c of reclaimCases) {
  const est = estimateReclaim(c.before, c.after)
  const verdict = shouldCommitReclaim(est, c.profile, c.force)
  const record = buildReclaimDecision('micro', est, c.profile, c.force)
  reclaim[c.name] = {
    estimate: est,
    verdict,
    record: {
      action: record.action,
      commit: record.commit,
      reason: record.reason,
      force: record.force,
      windowBand: record.windowBand,
      billing: record.billing,
      cache: record.cache,
    },
  }
}

const out = { windowBands, profiles, reclaim }

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')

const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(
  `reclaim oracle：${Object.keys(windowBands).length} 个 windowBand + ${profileInputs.length} 个 profile + ${reclaimCases.length} 个 reclaim 用例 — sha256 ${sha.slice(0, 16)}`,
)
