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
  LARGE_CONTEXT_WINDOW_TOKENS, compactThresholds,
} from '../../../src/compact/constants.js'
import {
  tierForRatio, decideCompactAction, recordCompactFailure, recordCompactSuccess,
  llmActionRatiosFor,
} from '../../../src/context/compact-policy.js'
import {
  estimateOaiMessageTokens, estimateOaiTokens, microCompactOai,
} from '../../../src/compact/micro.js'
import { KEEP_RECENT_MESSAGES, CACHE_ANCHOR_MESSAGES } from '../../../src/compact/constants.js'
import { collapseToolResult } from '../../../src/compact/context-collapse.js'
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

// ── 熔断器状态机 ──
function breakerSeq(failures: number[], successes: number[], startTurn: number) {
  let st: any = { consecutiveFailures: 0 }
  const out: any[] = []
  let turn = startTurn
  for (const f of failures) {
    for (let i = 0; i < f; i++) { st = recordCompactFailure(st, turn); turn++ }
    out.push({ ...st, turn })
  }
  for (const sc of successes) {
    for (let i = 0; i < sc; i++) { st = recordCompactSuccess(st) }
    out.push({ ...st, turn })
  }
  return out
}

const breakerCases = [
  { name: 'fail-once', failures: [1], successes: [], startTurn: 10 },
  { name: 'fail-twice', failures: [2], successes: [], startTurn: 10 },
  { name: 'fail-thrice-disables', failures: [3], successes: [], startTurn: 10 },
  { name: 'fail-four', failures: [4], successes: [], startTurn: 10 },
  { name: 'fail-then-success', failures: [3], successes: [1], startTurn: 10 },
].map(c => ({ ...c, want: breakerSeq(c.failures, c.successes, c.startTurn) }))

// ── LLM 阶梯派生 ──
const ladderCases = [
  { name: 'per-token-exact-prefix', billing: 'per-token', cache: 'exact-prefix' },
  { name: 'per-token-partial', billing: 'per-token', cache: 'partial' },
  { name: 'per-token-none', billing: 'per-token', cache: 'none' },
  { name: 'subscription-exact-prefix', billing: 'subscription', cache: 'exact-prefix' },
  { name: 'subscription-none', billing: 'subscription', cache: 'none' },
].map(c => ({ ...c, want: llmActionRatiosFor(c as any) }))

// ── decideCompactAction 六种 action ──
function decideCase(o: any) {
  const input: any = {
    estimatedTokens: o.estimatedTokens,
    maxTokens: o.maxTokens,
    turn: o.turn,
    failures: o.failures ?? { consecutiveFailures: 0 },
    profile: { billing: o.billing ?? 'subscription', cache: o.cache ?? 'none' },
    recentHitRate: o.recentHitRate ?? null,
  }
  if (o.precisionCeilingOverride !== undefined) input.precisionCeilingOverride = o.precisionCeilingOverride
  if (o.llmLadder !== undefined) input.providerProfile = { compaction: { llmLadder: o.llmLadder } }
  const d = decideCompactAction(input)
  return {
    action: d.action, reason: d.reason, force: d.force,
    precisionRisk: d.precisionRisk, tier: d.tier, shouldCompact: d.shouldCompact,
  }
}

const decideCases = [
  { name: 'small-below-watch', maxTokens: 100_000, estimatedTokens: 50_000, turn: 1 },
  { name: 'small-watch-tier', maxTokens: 100_000, estimatedTokens: 62_000, turn: 1 },
  { name: 'small-compact-tier', maxTokens: 100_000, estimatedTokens: 80_000, turn: 1 },
  { name: 'small-ceiling-forced', maxTokens: 100_000, estimatedTokens: 96_000, turn: 1 },
  { name: 'breaker-open', maxTokens: 100_000, estimatedTokens: 80_000, turn: 1, failures: { consecutiveFailures: 3, disabledUntilTurn: 5 } },
  { name: 'breaker-open-but-ceiling', maxTokens: 100_000, estimatedTokens: 96_000, turn: 1, failures: { consecutiveFailures: 3, disabledUntilTurn: 5 } },
  { name: 'large-partial-llm', maxTokens: 1_000_000, estimatedTokens: 620_000, turn: 1 },
  { name: 'large-full-llm', maxTokens: 1_000_000, estimatedTokens: 780_000, turn: 1 },
  { name: 'large-cache-preserving-below-075', maxTokens: 1_000_000, estimatedTokens: 700_000, turn: 1, billing: 'per-token', cache: 'exact-prefix' },
  { name: 'large-cache-preserving-at-075', maxTokens: 1_000_000, estimatedTokens: 760_000, turn: 1, billing: 'per-token', cache: 'exact-prefix' },
  { name: 'large-precision-band', maxTokens: 1_000_000, estimatedTokens: 710_000, turn: 1, billing: 'per-token', cache: 'exact-prefix' },
  { name: 'large-ceiling-checkpoint', maxTokens: 1_000_000, estimatedTokens: 960_000, turn: 1 },
  { name: 'large-below-all', maxTokens: 1_000_000, estimatedTokens: 100_000, turn: 1 },
  { name: 'provider-ladder-override', maxTokens: 1_000_000, estimatedTokens: 700_000, turn: 1, llmLadder: { partial: 0.85, full: 0.90 } },
].map(c => ({ ...c, want: decideCase(c) }))

// ── CompactThresholds ──
const thresholdCases = [
  { name: 'number-overload-small', input: 100_000 },
  { name: 'number-overload-large', input: 1_000_000 },
  { name: 'profile-small', input: { contextWindow: 100_000 } },
  { name: 'profile-large', input: { contextWindow: 1_000_000 } },
  { name: 'profile-cache-preserving', input: { contextWindow: 1_000_000, providerProfile: { cacheType: 'exact-prefix', persistent: true } } },
  { name: 'profile-aggressive', input: { contextWindow: 1_000_000, providerProfile: { cacheType: 'none', persistent: false } } },
  { name: 'profile-boundary-499999', input: { contextWindow: 499_999 } },
  { name: 'profile-boundary-500000', input: { contextWindow: 500_000 } },
].map(c => ({ ...c, want: compactThresholds(c.input as any) }))

// ── token 估算 ──
const tokenCases = [
  { name: 'ascii-4chars', msg: { role: 'user', content: 'abcd' } },
  { name: 'ascii-5chars', msg: { role: 'user', content: 'abcde' } },
  { name: 'cjk-1char', msg: { role: 'user', content: '中' } },
  { name: 'cjk-2chars', msg: { role: 'user', content: '中文' } },
  { name: 'mixed', msg: { role: 'user', content: 'abc中文' } },
  { name: 'assistant-content-only', msg: { role: 'assistant', content: 'hello' } },
  { name: 'assistant-with-toolcalls', msg: { role: 'assistant', content: '', tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'bash', arguments: '{"cmd":"ls"}' } }] } },
  { name: 'assistant-with-reasoning', msg: { role: 'assistant', content: 'hi', reasoning_content: 'thinking hard' } },
  { name: 'assistant-all-three', msg: { role: 'assistant', content: 'hi', reasoning_content: 'think', tool_calls: [{ id: 'c1', type: 'function', function: { name: 'f', arguments: '{}' } }] } },
  { name: 'tool-message', msg: { role: 'tool', content: 'result data', tool_call_id: 'call_1' } },
  { name: 'hiragana', msg: { role: 'user', content: 'あいう' } },
  { name: 'hangul', msg: { role: 'user', content: '한국' } },
].map(c => ({ ...c, want: estimateOaiMessageTokens(c.msg as any) }))

// ── micro-compact（截断路径）──
// 构造超长 tool 消息（超过 previewChars）
// previewChars = toolResultMaxTokens（200K 窗口 → 30000 字符档）
// 必须超过它才会触发截断
// compactThresholds(200000).toolResultMaxTokens = 60000 字符
const longTool = 'x'.repeat(70000)
function microCase(o: any) {
  const msgs = o.msgs
  const r = microCompactOai(msgs as any, o.contextWindow, o.estimatedTokens ?? estimateOaiTokens(msgs as any))
  return {
    truncated: r.truncated,
    messageCount: r.messages.length,
    // 每条消息的 role + 内容长度（用于逐条对账，避免全文）
    shapes: r.messages.map((m: any) => ({
      role: m.role,
      len: (m.content ?? '').length,
      startsWithTag: typeof m.content === 'string' && m.content.startsWith('<microcompacted'),
    })),
  }
}

const microCases = [
  {
    name: 'below-threshold-no-change',
    contextWindow: 200_000,
    estimatedTokens: 1000,
    msgs: [{ role: 'user', content: 'short' }, { role: 'assistant', content: 'ok' }],
  },
  {
    name: 'long-tool-truncated',
    contextWindow: 200_000,
    estimatedTokens: 100_000,
    msgs: [
      { role: 'user', content: 'a' },
      { role: 'assistant', content: 'b', tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'bash', arguments: '{}' } }] },
      { role: 'tool', content: longTool, tool_call_id: 'call_1' },
    ],
  },
  {
    name: 'short-tool-not-truncated',
    contextWindow: 200_000,
    estimatedTokens: 100_000,
    msgs: [
      { role: 'user', content: 'a' },
      { role: 'assistant', content: 'b', tool_calls: [{ id: 'c_1', type: 'function', function: { name: 'bash', arguments: '{}' } }] },
      { role: 'tool', content: 'short result', tool_call_id: 'c_1' },
    ],
  },
  // ── 阶段 2：轮次删除 ──
  // 构造 6 个完整轮次，中间轮次可被删。token 估算传超大值逼进阶段 2。
  {
    name: 'tier2-removes-middle-rounds',
    contextWindow: 100_000,
    estimatedTokens: 500_000,   // 远超窗口 → 触发阶段 2
    msgs: [
      // 锚（前 2 条，不可删）
      { role: 'user', content: 'anchor user' },
      { role: 'assistant', content: 'anchor asst' },
      // 中间轮次 1
      { role: 'user', content: 'mid1 user' },
      { role: 'assistant', content: 'mid1 asst' },
      // 中间轮次 2
      { role: 'user', content: 'mid2 user' },
      { role: 'assistant', content: 'mid2 asst' },
      // 近期（后 4 条，不可删）
      { role: 'user', content: 'recent user' },
      { role: 'assistant', content: 'recent asst' },
      { role: 'user', content: 'recent2 user' },
      { role: 'assistant', content: 'recent2 asst' },
    ],
  },
  // ── M7 缺口：删除「不够本」的边界（删后刚好在 70% 之上 → 不删）──
  // contextWindow=100000，70% = 70000。中间轮 token 很小时不删。
  {
    name: 'tier2-not-worth-it',
    contextWindow: 100_000,
    estimatedTokens: 200_000,   // 超窗口，但删掉小轮后仍在 70% 之上
    msgs: [
      { role: 'user', content: 'anchor user' },
      { role: 'assistant', content: 'anchor asst' },
      { role: 'user', content: 'x' },          // 极小轮 → 删了也不够本
      { role: 'assistant', content: 'y' },
      { role: 'user', content: 'recent user' },
      { role: 'assistant', content: 'recent asst' },
      { role: 'user', content: 'recent2 user' },
      { role: 'assistant', content: 'recent2 asst' },
    ],
  },
  // ── 折叠路径接线（turnAge>=4 且 tool_call_id 可提取工具名）──
  // tool_call_id = 'grep_abc123' → 提取出 'grep' → 走 grep 折叠而非截断。
  // 需要 5 轮以上历史让该 tool 消息的 turnAge >= 4。
  {
    name: 'collapse-path-grep',
    contextWindow: 200_000,
    estimatedTokens: 100_000,
    msgs: [
      { role: 'user', content: 'turn 1' },
      { role: 'assistant', content: 'a1', tool_calls: [{ id: 'grep_abc', type: 'function', function: { name: 'grep', arguments: '{}' } }] },
      // 这条 tool 结果的 turnAge 会 >= 4
      { role: 'tool', content: Array.from({ length: 40 }, (_, i) => `src/f${i % 5}.ts:${i}:hit`).join('\n') + '\n' + 'z'.repeat(200), tool_call_id: 'grep_abc' },
      { role: 'assistant', content: 'a2' },
      // 需要足够多轮次让上面那条 tool 的 turnAge 达到 4
      { role: 'user', content: 'turn 3' },
      { role: 'assistant', content: 'a3' },
      { role: 'user', content: 'turn 4' },
      { role: 'assistant', content: 'a4' },
      { role: 'user', content: 'turn 5' },
      { role: 'assistant', content: 'a5' },
      { role: 'user', content: 'turn 6' },
      { role: 'assistant', content: 'a6' },
    ],
  },
  // ── M7 缺口（真缺口）：guard 只在 currentTokens-roundTokens 落在
  //    70%~100% 窗口之间才可区分。窗口 100000 → 70%=70000、100%=100000。
  //    currentTokens=120000、中间轮 ~30000 → 删后 89999，落在区间内。
  {
    name: 'tier2-guard-band',
    contextWindow: 100_000,
    estimatedTokens: 120_000,
    msgs: [
      { role: 'user', content: 'anchor user' },
      { role: 'assistant', content: 'anchor asst' },
      { role: 'user', content: 'mid user' },
      // 大 assistant 消息（120000 字符 ≈ 30000 token）——非 tool 故不截断
      { role: 'assistant', content: 'z'.repeat(120_000) },
      { role: 'user', content: 'recent user' },
      { role: 'assistant', content: 'recent asst' },
      { role: 'user', content: 'recent2 user' },
      { role: 'assistant', content: 'recent2 asst' },
    ],
  },
  // ── M8 缺口：API 不变量为 broken 的轮次不可删 ──
  // 构造孤儿 tool 消息（有 tool 结果但缺对应的 assistant tool_calls）→ broken
  {
    name: 'tier2-skips-broken-round',
    contextWindow: 100_000,
    estimatedTokens: 500_000,
    msgs: [
      { role: 'user', content: 'anchor user' },
      { role: 'assistant', content: 'anchor asst' },
      // 孤儿 tool（无对应 assistant tool_calls）→ 该轮 invariant=broken
      { role: 'user', content: 'mid user' },
      { role: 'tool', content: 'orphan result', tool_call_id: 'no_such_call' },
      { role: 'user', content: 'recent user' },
      { role: 'assistant', content: 'recent asst' },
      { role: 'user', content: 'recent2 user' },
      { role: 'assistant', content: 'recent2 asst' },
    ],
  },
].map(c => ({ ...c, want: microCase(c) }))

// ── context-collapse：语义折叠 ──
// 构造超过 200 字符的内容（低于此不折叠）
const long = (s: string, n = 300) => s.repeat(Math.ceil(n / s.length))

const collapseCases = [
  // 前置条件：太小 / 太新 → null
  { name: 'too-small', tool: 'grep', content: 'x'.repeat(100), turnAge: 5 },
  { name: 'too-new', tool: 'grep', content: long('a'), turnAge: 1 },
  { name: 'at-boundary-200', tool: 'grep', content: 'x'.repeat(200), turnAge: 5 },
  // grep 折叠
  {
    name: 'grep-basic', tool: 'grep', turnAge: 5,
    content: ['src/a.ts:1:foo', 'src/a.ts:2:foo', 'src/b.ts:3:foo', 'src/c.ts:4:bar'].join('\n') + '\n' + 'z'.repeat(200),
  },
  {
    name: 'grep-many-files', tool: 'grep', turnAge: 5,
    content: Array.from({ length: 12 }, (_, i) => `src/f${i}.ts:${i}:hit`).join('\n') + '\n' + 'z'.repeat(200),
  },
  {
    name: 'grep-with-artifact-statline', tool: 'grep', turnAge: 5,
    content: 'grep "foo": 65 matches in 20 files\n' + 'z'.repeat(200) + '\n[artifact:abc_123]',
  },
  {
    name: 'grep-with-artifact-nostat', tool: 'grep', turnAge: 5,
    content: 'z'.repeat(250) + '\n[artifact:xyz]',
  },
  // read_file 折叠
  {
    name: 'read-file-with-funcs', tool: 'read_file', turnAge: 5,
    content: ['export function alpha() {}', 'function beta() {}', 'class Gamma {}', 'const x = 1'].join('\n') + '\n' + 'z'.repeat(200),
  },
  {
    name: 'read-file-exports-only', tool: 'read_file', turnAge: 5,
    content: ['export const a = 1', 'export const b = 2', 'export const c = 3'].join('\n') + '\n' + 'z'.repeat(200),
  },
  // bash 折叠
  {
    name: 'bash-with-exit-and-fails', tool: 'bash', turnAge: 5,
    content: ['line one', 'FAIL something broke', 'more output', 'exit code: 1', 'last line'].join('\n') + '\n' + 'z'.repeat(200),
  },
  {
    name: 'bash-success-lines-excluded', tool: 'bash', turnAge: 5,
    content: ['✓ passed test', '✗ failed test', 'output', 'exit code: 0'].join('\n') + '\n' + 'z'.repeat(200),
  },
  // ── C4 可区分用例：成功行**含** fail 关键词 ──
  // `✓ 0 errors` 会被 fail 正则命中（"error"），必须被成功前缀排除。
  // 原用例的 `✓ passed test` 不含 fail 关键词，故无法区分该 guard。
  {
    name: 'bash-success-with-error-word', tool: 'bash', turnAge: 5,
    content: ['✓ 0 errors found', '✓ all checks passed', '✗ real failure here', 'done'].join('\n') + '\n' + 'z'.repeat(200),
  },
  // write/edit
  { name: 'write-file', tool: 'write_file', content: long('w'), turnAge: 5 },
  { name: 'edit-file', tool: 'edit_file', content: long('e'), turnAge: 5 },
  // run_tests（中文口径）
  {
    name: 'run-tests-chinese', tool: 'run_tests', turnAge: 5,
    content: ['退出码：1', '3 通过，1 失败，0 跳过', '失败项：', '  ✖ my test name'].join('\n') + '\n' + 'z'.repeat(200),
  },
  // ── C6 可区分用例：`N 失败项` 形式（负向前瞻必须跳过它）──
  // `2 失败项` 会被基础正则 `(\d+)\s+失败` 命中，但 TS 的 `(?!项)` 要求
  // 跳过——所以应匹配后面的 `5 失败，`（真正的计数行）。
  {
    name: 'run-tests-negativelookahead', tool: 'run_tests', turnAge: 5,
    content: ['退出码：1', '2 失败项如下', '10 通过，5 失败，0 跳过'].join('\n') + '\n' + 'z'.repeat(200),
  },
  // delegate
  {
    name: 'delegate-task', tool: 'delegate_task', turnAge: 5,
    content: 'profile: code_scout\nsummary: found the relevant code in three files\n' + 'z'.repeat(200),
  },
  // generic
  { name: 'generic-tool', tool: 'unknown_tool', content: ['l1', 'l2', 'l3', 'l4'].join('\n') + '\n' + 'z'.repeat(200), turnAge: 5 },
  // artifact 保留
  {
    name: 'generic-with-artifact', tool: 'unknown_tool', turnAge: 5,
    content: 'z'.repeat(250) + '\n[artifact:keep_me]',
  },
].map(c => {
  const r = collapseToolResult(c.tool, c.content, c.turnAge, 200_000)
  return {
    name: c.name,
    tool: c.tool,
    content: c.content,
    turnAge: c.turnAge,
    want: r === null ? null : {
      toolName: r.toolName, summary: r.summary,
      originalTokens: r.originalTokens, collapsedTokens: r.collapsedTokens,
    },
  }
})

const out = {
  constants: { largeContextWindowTokens: LARGE_CONTEXT_WINDOW_TOKENS, keepRecentMessages: KEEP_RECENT_MESSAGES, cacheAnchorMessages: CACHE_ANCHOR_MESSAGES },
  strategies: strategyCases,
  adaptive: adaptiveCases,
  ceilings: ceilingCases,
  tiers: tierResults,
  checks: checkResults,
  cvm: cvmResults,
  thrashing: thrashCases,
  breaker: breakerCases,
  ladders: ladderCases,
  decides: decideCases,
  thresholds: thresholdCases,
  tokens: tokenCases,
  micro: microCases,
  collapse: collapseCases,
}

writeFileSync(join(__dirname, 'oracle.json'), JSON.stringify(out, null, 2) + '\n', 'utf-8')
