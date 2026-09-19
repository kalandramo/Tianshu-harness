// 端到端对账：真实 oracle（src/context/claims.ts + promotion.ts）vs Go 实现。
//
// **范围**：纯逻辑层——claim 构造 / ID 派生 / prompt 渲染 / 快照 / 晋升判定 /
// 召回门禁 / 状态计数。**不含** claim-store 的事件溯源与落盘（那是 I/O 层）。
//
// 运行（必须在**仓库根**）：
//   node_modules/.bin/tsx go/testdata/claims/gen-oracle.ts
// 产出：go/testdata/claims/oracle.json + cases.json
import { writeFileSync } from 'node:fs'
import {
  createClaimFromProposal,
  isPromptEligibleClaim,
  renderActiveClaimsBlock,
  checkpointClaims,
  loadClaimSnapshot,
  claimProposalFromAnchor,
  MAX_PROMPT_CLAIMS,
  type ClaimProposal,
  type ContextClaim,
  type ContextAnchor,
} from '../../../src/context/claims.js'
import {
  evaluatePromotion,
  claimHasFileEvidence,
  countClaimsByStatus,
} from '../../../src/context/promotion.js'

// 固定时间戳——含时间戳的 oracle 不可复现（生成器须 scrub 时间字段）
const T0 = 1700000000000

function baseProposal(over: Partial<ClaimProposal> = {}): ClaimProposal {
  return {
    kind: 'file_observation',
    scope: 'session',
    text: 'a.ts 里有函数 X',
    confidence: 0.7,
    fitness: 0.5,
    source: { actor: 'tool', sessionId: 'sess-1', turn: 3, eventId: 'ev-1' },
    evidence: [{ id: 'ev-1:e1', kind: 'tool_result', summary: '读到 a.ts', path: 'a.ts', createdAt: T0 }],
    createdAt: T0,
    tags: ['obs'],
    ...over,
  }
}

function baseClaim(over: Partial<ContextClaim> = {}): ContextClaim {
  return { ...createClaimFromProposal(baseProposal()), ...over }
}

// ── 用例 ──
const cases: Record<string, unknown> = {}

// 1. claim ID 派生（sha256 前 12 hex）
cases.id_derivation = {
  proposals: [
    baseProposal(),
    baseProposal({ text: 'b.ts 里有函数 Y' }),
    baseProposal({ kind: 'decision' }),
    baseProposal({ scope: 'project' }),
    // 文本归一化：首尾空白 + 连续空白 + 大小写
    baseProposal({ text: '  A.TS  里有   函数 X  ' }),
    baseProposal({ text: 'a.ts 里有函数 X' }),
    // 不同 session
    baseProposal({ source: { actor: 'tool', sessionId: 'sess-2', turn: 3, eventId: 'ev-1' } }),
  ].map(p => ({ proposal: p, id: createClaimFromProposal(p).id })),
}

// 2. renderActiveClaimsBlock —— 排序 / 截断 / 转义
cases.render_basic = {
  claims: [
    baseClaim({ id: 'c1', text: '第一条', fitness: 0.5, confidence: 0.7 }),
    baseClaim({ id: 'c2', text: '第二条', fitness: 0.9, confidence: 0.6 }),
  ],
}
cases.render_sort_by_fitness = {
  claims: [
    baseClaim({ id: 'low', text: 'L', fitness: 0.1, confidence: 0.9 }),
    baseClaim({ id: 'high', text: 'H', fitness: 0.9, confidence: 0.1 }),
    baseClaim({ id: 'mid', text: 'M', fitness: 0.5, confidence: 0.5 }),
  ],
}
cases.render_tiebreak_confidence = {
  claims: [
    baseClaim({ id: 'a', text: 'A', fitness: 0.5, confidence: 0.3, createdAt: T0 }),
    baseClaim({ id: 'b', text: 'B', fitness: 0.5, confidence: 0.8, createdAt: T0 }),
  ],
}
cases.render_tiebreak_createdAt = {
  claims: [
    baseClaim({ id: 'old', text: 'OLD', fitness: 0.5, confidence: 0.5, createdAt: T0 }),
    baseClaim({ id: 'new', text: 'NEW', fitness: 0.5, confidence: 0.5, createdAt: T0 + 1000 }),
  ],
}
cases.render_excludes_stale = {
  claims: [
    baseClaim({ id: 'active', text: 'A', status: 'active' }),
    baseClaim({ id: 'stale', text: 'S', status: 'stale' }),
    baseClaim({ id: 'quar', text: 'Q', status: 'quarantined' }),
  ],
}
cases.render_expired = {
  claims: [
    baseClaim({ id: 'alive', text: 'A' }),
    baseClaim({ id: 'exp', text: 'E', expiresAt: T0 - 1 }),
    baseClaim({ id: 'future', text: 'F', expiresAt: T0 + 1e9 }),
  ],
}
cases.render_empty = { claims: [] }
cases.render_all_ineligible = {
  claims: [baseClaim({ id: 'x', status: 'stale' })],
}
cases.render_xml_escape = {
  claims: [
    baseClaim({ id: 'a&b<c', text: '<script>"x"</script> & more' }),
  ],
}
cases.render_truncate_20 = {
  claims: Array.from({ length: 25 }, (_, i) =>
    baseClaim({ id: `c${String(i).padStart(2, '0')}`, text: `claim ${i}`, fitness: 1 - i * 0.01 })),
}
cases.render_durable_statuses = {
  claims: [
    baseClaim({ id: 'a', text: 'A', status: 'active' }),
    baseClaim({ id: 'dc', text: 'DC', status: 'durable_candidate' }),
    baseClaim({ id: 'd', text: 'D', status: 'durable' }),
  ],
}
cases.render_missing_evidence = {
  claims: [baseClaim({ id: 'noev', text: 'N', evidence: [] })],
}
cases.render_confidence_format = {
  claims: [
    baseClaim({ id: 'a', text: 'A', confidence: 0.555 }),
    baseClaim({ id: 'b', text: 'B', confidence: 0.6 }),
  ],
}

// 3. checkpoint / load
cases.checkpoint_filters = {
  claims: [
    baseClaim({ id: 'a', status: 'active' }),
    baseClaim({ id: 's', status: 'stale' }),
    baseClaim({ id: 'q', status: 'quarantined' }),
    baseClaim({ id: 'e', expiresAt: T0 - 1 }),
    baseClaim({ id: 'd', status: 'durable' }),
  ],
}
cases.load_snapshot_version_mismatch = {
  snapshot: { version: 2, createdAt: T0, claims: [baseClaim()] },
}

// 4. isPromptEligibleClaim
cases.eligible_matrix = {
  statuses: ['ephemeral', 'active', 'durable_candidate', 'durable', 'stale', 'conflicted', 'quarantined'].map(s => ({
    status: s,
    eligible: isPromptEligibleClaim(baseClaim({ status: s as never }), T0),
  })),
}

// 5. evaluatePromotion
cases.promotion_active_insufficient = {
  claim: baseClaim({ status: 'active', consumers: [{ id: 'c1', kind: 'prompt', usedAt: T0 }] }),
}
cases.promotion_active_3_consumers = {
  claim: baseClaim({
    status: 'active',
    consumers: [
      { id: 'c1', kind: 'prompt', usedAt: T0 },
      { id: 'c2', kind: 'prompt', usedAt: T0 },
      { id: 'c3', kind: 'prompt', usedAt: T0 },
    ],
  }),
}
cases.promotion_active_dup_consumers = {
  claim: baseClaim({
    status: 'active',
    consumers: [
      { id: 'c1', kind: 'prompt', usedAt: T0 },
      { id: 'c1', kind: 'prompt', usedAt: T0 + 1 },
      { id: 'c1', kind: 'prompt', usedAt: T0 + 2 },
    ],
  }),
}
cases.promotion_durable_candidate_too_young = {
  claim: baseClaim({
    status: 'durable_candidate',
    createdAt: T0,
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}
cases.promotion_durable_candidate_old_enough = {
  claim: baseClaim({
    status: 'durable_candidate',
    createdAt: T0 - 20 * 60_000,
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}
// **判别 5 vs 10 分钟阈值**：年龄落在两者之间的用例
cases.promotion_durable_candidate_7min = {
  claim: baseClaim({
    status: 'durable_candidate',
    createdAt: T0 - 7 * 60_000,
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}
cases.promotion_durable_candidate_9min59s = {
  claim: baseClaim({
    status: 'durable_candidate',
    createdAt: T0 - (10 * 60_000 - 1),
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}
cases.promotion_durable_candidate_10min = {
  claim: baseClaim({
    status: 'durable_candidate',
    createdAt: T0 - 10 * 60_000,
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}

cases.promotion_with_counterevidence = {
  claim: baseClaim({
    status: 'active',
    counterevidence: [{ id: 'ce', kind: 'tool_result', summary: '反驳', createdAt: T0 }],
    consumers: Array.from({ length: 5 }, (_, i) => ({ id: `c${i}`, kind: 'prompt' as const, usedAt: T0 })),
  }),
}

// 6. claimHasFileEvidence
cases.has_file_evidence = {
  checks: [
    { claim: baseClaim({ kind: 'file_observation' }), path: 'a.ts', result: claimHasFileEvidence(baseClaim({ kind: 'file_observation' }), 'a.ts') },
    { claim: baseClaim({ kind: 'file_observation' }), path: 'z.ts', result: claimHasFileEvidence(baseClaim({ kind: 'file_observation' }), 'z.ts') },
    { claim: baseClaim({ kind: 'decision' }), path: 'a.ts', result: claimHasFileEvidence(baseClaim({ kind: 'decision' }), 'a.ts') },
    { claim: baseClaim({ kind: 'verification_fact' }), path: 'a.ts', result: claimHasFileEvidence(baseClaim({ kind: 'verification_fact' }), 'a.ts') },
  ],
}

// 7. countClaimsByStatus
cases.status_counts = {
  claims: [
    baseClaim({ id: 'a1', status: 'active' }),
    baseClaim({ id: 'a2', status: 'active' }),
    baseClaim({ id: 's', status: 'stale' }),
    baseClaim({ id: 'c', status: 'conflicted' }),
    baseClaim({ id: 'd', status: 'durable' }),
    baseClaim({ id: 'dc', status: 'durable_candidate' }),
    baseClaim({ id: 'q', status: 'quarantined' }),
    baseClaim({ id: 'e', status: 'ephemeral' }),
  ],
}

// 8. claimProposalFromAnchor
cases.proposal_from_anchor = {
  anchors: ([
    { kind: 'user_constraint', text: '必须用 Go', sourceRoundIndex: 0, salience: 0.9 },
    { kind: 'decision', text: '选了方案 A', sourceRoundIndex: 1, salience: 0.8 },
    { kind: 'verification', text: '测试通过', sourceRoundIndex: 2, salience: 0.7 },
    { kind: 'error', text: '编译失败', sourceRoundIndex: 3, salience: 0.6 },
    { kind: 'file', text: '改了 a.ts', sourceRoundIndex: 4, salience: 0.5 },
    { kind: 'pending_task', text: '待办 X', sourceRoundIndex: 5, salience: 0.4 },
    { kind: 'user_preference', text: '喜欢简洁', sourceRoundIndex: 6, salience: 0.3 },
  ] as ContextAnchor[]).map(a => ({
    anchor: a,
    proposal: claimProposalFromAnchor(a, { actor: 'user', sessionId: 's1', turn: 1, eventId: 'e1', createdAt: T0 }),
  })),
}

// 输出：每个用例附上真实 TS 计算的结果
interface OracleEntry { [k: string]: unknown }

const out: Record<string, OracleEntry> = {}

for (const [name, c] of Object.entries(cases)) {
  const cc = c as Record<string, unknown>
  const entry: OracleEntry = {}

  if ('claims' in cc) {
    const claims = cc.claims as ContextClaim[]
    entry.render = renderActiveClaimsBlock(claims)
    entry.checkpoint = checkpointClaims(claims, T0)
    entry.checkpointWithSeq = checkpointClaims(claims, T0, 42)
    entry.statusCounts = countClaimsByStatus(claims)
  }
  if ('snapshot' in cc) {
    entry.loaded = loadClaimSnapshot(cc.snapshot as never, T0)
  }
  if ('claim' in cc) {
    entry.promotion = evaluatePromotion(cc.claim as ContextClaim, T0)
  }
  if ('statuses' in cc) {
    entry.statuses = cc.statuses
  }
  if ('proposals' in cc) {
    entry.proposals = cc.proposals
  }
  if ('checks' in cc) {
    entry.checks = cc.checks
  }
  if ('anchors' in cc) {
    entry.anchors = cc.anchors
  }

  out[name] = entry
}

// 附带常量
out.__constants = { MAX_PROMPT_CLAIMS }

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
writeFileSync(new URL('cases.json', import.meta.url), JSON.stringify(cases, null, 2) + '\n')
