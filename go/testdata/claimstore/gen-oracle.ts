// 端到端对账：claim-store 的**投影层**（applyEventsToMap）vs Go 实现。
//
// **为什么用 store 实例而非直接调 applyEventsToMap**：那是 private 方法。
// 通过公开路径（appendEvent + listClaims）间接导出——等价且不侵入源码。
//
// **范围**：事件投影（4 种事件）/ 幂等 / 反证追加条件 / consumers 封顶 / 过滤。
// **不含**：写链治理（TS 特有的事件循环饥饿治理，Go 并发模型不同）。
//
// 运行（必须在**仓库根**）：
//   node_modules/.bin/tsx go/testdata/claimstore/gen-oracle.ts
import { writeFileSync } from 'node:fs'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { ContextClaimStore } from '../../../src/context/claim-store.js'
import { createClaimFromProposal, type ClaimProposal, type ContextClaim } from '../../../src/context/claims.js'

const T0 = 1700000000000

function proposal(over: Partial<ClaimProposal> = {}): ClaimProposal {
  return {
    kind: 'file_observation',
    scope: 'session',
    text: 'a.ts 里有函数 X',
    confidence: 0.7,
    fitness: 0.5,
    source: { actor: 'tool', sessionId: 'sess-1', turn: 1, eventId: 'e1' },
    evidence: [{ id: 'ev1', kind: 'tool_result', summary: 's', path: 'a.ts', createdAt: T0 }],
    createdAt: T0,
    tags: ['t'],
    ...over,
  }
}

/** 事件脚本：每个用例是一串操作 */
type Op =
  | { op: 'propose'; proposal: ClaimProposal }
  | { op: 'status'; claimId: string; status: string; reason: string }
  | { op: 'use'; claimId: string; consumerId: string; consumerKind: string; usedAt: number }
  | { op: 'boost'; claimId: string; delta: number; cap: number }

const cases: Record<string, { ops: Op[]; filter?: unknown }> = {}

// ── 基础：单次 propose ──
cases.propose_once = { ops: [{ op: 'propose', proposal: proposal() }] }

// ── 幂等：同 proposal 两次 → 只 1 条 ──
cases.propose_idempotent = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal() },
  ],
}

// ── 不同 proposal → 2 条 ──
cases.propose_two_distinct = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ text: 'b.ts 里有函数 Y' }) },
  ],
}

// ── 状态变更：非 active → 追加反证 ──
cases.status_stale_adds_counterevidence = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'status', claimId: '', status: 'stale', reason: '文件被改' },
  ],
}

// ── 状态变更：回到 active → 反证保留（不追加）──
cases.status_back_to_active = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'status', claimId: '', status: 'stale', reason: '文件被改' },
    { op: 'status', claimId: '', status: 'active', reason: '重新确认' },
  ],
}

// ── 多次非 active 状态变更 → 反证累加 ──
cases.status_multi_counterevidence = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'status', claimId: '', status: 'stale', reason: 'r1' },
    { op: 'status', claimId: '', status: 'conflicted', reason: 'r2' },
    { op: 'status', claimId: '', status: 'quarantined', reason: 'r3' },
  ],
}

// ── 消费者追加 ──
cases.use_consumers = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'use', claimId: '', consumerId: 'c1', consumerKind: 'prompt', usedAt: T0 + 1 },
    { op: 'use', claimId: '', consumerId: 'c2', consumerKind: 'tool', usedAt: T0 + 2 },
  ],
}

// ── 消费者封顶 50（保留最近）──
cases.use_consumers_cap = {
  ops: [
    { op: 'propose', proposal: proposal() },
    ...Array.from({ length: 55 }, (_, i) => ({
      op: 'use' as const, claimId: '', consumerId: `c${String(i).padStart(3, '0')}`,
      consumerKind: 'prompt', usedAt: T0 + i,
    })),
  ],
}

// ── fitness 提升 ──
cases.boost_fitness = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'boost', claimId: '', delta: 0.3, cap: 1.0 },
  ],
}

// ── fitness 提升撞上限 ──
cases.boost_fitness_cap = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'boost', claimId: '', delta: 0.9, cap: 0.6 },
  ],
}

// ── 未知 claimId 的事件被忽略 ──
cases.unknown_claim_ignored = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'status', claimId: 'nonexistent', status: 'stale', reason: 'r' },
    { op: 'use', claimId: 'nonexistent', consumerId: 'c', consumerKind: 'prompt', usedAt: T0 },
    { op: 'boost', claimId: 'nonexistent', delta: 0.5, cap: 1 },
  ],
}

// ── 过滤：kind ──
cases.filter_kind = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ kind: 'decision', text: '选了方案 A' }) },
    { op: 'propose', proposal: proposal({ kind: 'user_constraint', text: '必须用 Go' }) },
  ],
  filter: { kind: ['decision'] },
}

// ── 过滤：status ──
cases.filter_status = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ text: 'b', }) },
    { op: 'status', claimId: '', status: 'stale', reason: 'r' },
  ],
  filter: { status: ['stale'] },
}

// ── 过滤：scope ──
cases.filter_scope = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ scope: 'project', text: 'p' }) },
  ],
  filter: { scope: ['project'] },
}

// ── 过滤：多维度组合 ──
cases.filter_multi = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ kind: 'decision', text: 'd' }) },
    { op: 'propose', proposal: proposal({ kind: 'decision', scope: 'project', text: 'dp' }) },
  ],
  filter: { kind: ['decision'], scope: ['project'] },
}

// ── 空过滤 → 全部 ──
cases.filter_empty = {
  ops: [
    { op: 'propose', proposal: proposal() },
    { op: 'propose', proposal: proposal({ text: 'b' }) },
  ],
  filter: {},
}

interface OracleEntry { claims: unknown; filtered?: unknown }

const out: Record<string, OracleEntry> = {}

for (const [name, spec] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'claimstore-oracle-'))
  try {
    const store = new ContextClaimStore(dir, 'sess-1')
    let firstClaimId = ''

    for (const op of spec.ops) {
      if (op.op === 'propose') {
        const c = store.propose(op.proposal)
        if (!firstClaimId) firstClaimId = c.id
        continue
      }
      const id = op.claimId || firstClaimId
      if (op.op === 'status') {
        store.updateClaimStatus(id, op.status as never, op.reason)
      } else if (op.op === 'use') {
        store.recordClaimUsed(id, {
          consumerId: op.consumerId,
          consumerKind: op.consumerKind as never,
          usedAt: op.usedAt,
        })
      } else if (op.op === 'boost') {
        store.boostFitness(id, op.delta, op.cap)
      }
    }

    const entry: OracleEntry = { claims: store.listClaims() }
    if (spec.filter !== undefined) {
      entry.filtered = store.listClaims(spec.filter as never)
    }
    out[name] = entry
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

writeFileSync(new URL('oracle.json', import.meta.url), JSON.stringify(out, null, 2) + '\n')
writeFileSync(new URL('cases.json', import.meta.url), JSON.stringify(cases, null, 2) + '\n')
