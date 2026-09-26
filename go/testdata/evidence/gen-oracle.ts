/**
 * EvidenceTracker 的差分 oracle。
 *
 * 生成：npx tsx go/testdata/evidence/gen-oracle.ts
 *
 * 导出 TS `src/agent/evidence.ts` 的 `EvidenceTracker` 在**受控事件序列**上的
 * 真实输出，供 Go 侧逐值对账。
 *
 * # 为什么用事件序列而非单点
 *
 * `EvidenceTracker` 是**有状态**的——它的值取决于事件顺序（如
 * `trackVerification` 会把 `editsSinceLastTest` 归零，而 `trackFileModified`
 * 会累加）。单点调用测不出状态机语义。
 *
 * # 覆盖范围（本刀最小闭合）
 *
 * 只导出 `getGateState()` 与 `getState()` 的关键字段——那是 TDD gate 与
 * sensorium 的 `evidenceState` 所需。`buildSummary`（delivery gate 投影）与
 * `deliveryStatus` 的全窗口语义**不在本刀**（需 `delivery-gate.ts` 整套）。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { EvidenceTracker } from '../../../src/agent/evidence.js'
import type { VerificationMetadata } from '../../../src/tools/types.js'

const here = dirname(fileURLToPath(import.meta.url))

/** 造一条验证元数据（只填对账关心的字段）。 */
function ver(status: string, command = 'npm test'): VerificationMetadata {
  return {
    status: status as VerificationMetadata['status'],
    command,
    scope: 'full',
  } as VerificationMetadata
}

type Step =
  | { kind: 'read'; path: string }
  | { kind: 'modify'; path: string }
  | { kind: 'verify'; status: string; command?: string }

/** 事件序列：每条是一个独立场景，Go 侧按同序重放。 */
const scenarios: Array<{ label: string; steps: Step[] }> = [
  // ── 空态 ──
  { label: 'empty', steps: [] },

  // ── 只读不写 ──
  { label: 'read-only', steps: [{ kind: 'read', path: 'src/a.ts' }] },

  // ── 代码文件编辑累加 ──
  { label: 'one-code-edit', steps: [{ kind: 'modify', path: 'src/a.ts' }] },
  { label: 'three-code-edits', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'src/b.ts' },
    { kind: 'modify', path: 'src/c.ts' },
  ] },
  // 同一文件重复编辑：filesModified 是 **Set**，去重；但 editsSinceLastTest 累加
  { label: 'same-file-three-edits', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'src/a.ts' },
  ] },

  // ── 非代码文件不计入 gate ──
  { label: 'doc-only-edits', steps: [
    { kind: 'modify', path: 'README.md' },
    { kind: 'modify', path: 'docs/x.md' },
    { kind: 'modify', path: 'config.json' },
  ] },
  // 混合：代码 1 + 文档 2 → gate 计数应为 1
  { label: 'mixed-code-and-docs', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'README.md' },
    { kind: 'modify', path: 'docs/x.md' },
  ] },

  // ── scratch 路径不计入 gate ──
  { label: 'scratch-code-edit', steps: [{ kind: 'modify', path: '.rivet/scratch/probe.ts' }] },
  { label: 'scratch-mixed', steps: [
    { kind: 'modify', path: '.rivet/scratch/probe.ts' },
    { kind: 'modify', path: 'src/a.ts' },
  ] },
  // scratch 的**非**代码文件也不计
  { label: 'scratch-doc', steps: [{ kind: 'modify', path: '.rivet/scratch/notes.md' }] },

  // ── 验证归零 editsSinceLastTest ──
  { label: 'edits-then-verify', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'src/b.ts' },
    { kind: 'verify', status: 'passed' },
  ] },
  { label: 'edits-verify-then-more-edits', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'verify', status: 'passed' },
    { kind: 'modify', path: 'src/b.ts' },
    { kind: 'modify', path: 'src/c.ts' },
  ] },
  // 失败的验证**同样**归零（gate 针对零验证编辑，不针对通过）
  { label: 'edits-then-failed-verify', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'verify', status: 'failed' },
  ] },
  // blocked 的验证也归零
  { label: 'edits-then-blocked-verify', steps: [
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'verify', status: 'blocked' },
  ] },

  // ── 重复验证 ──
  { label: 'two-verifies', steps: [
    { kind: 'verify', status: 'passed' },
    { kind: 'verify', status: 'failed' },
  ] },

  // ── hasFailedTests 的 sticky 语义（一旦失败过就恒真）──
  { label: 'failed-then-passed', steps: [
    { kind: 'verify', status: 'failed' },
    { kind: 'verify', status: 'passed' },
  ] },

  // ── hasReadTestFiles 的路径模式 ──
  { label: 'read-test-file-dot-test', steps: [{ kind: 'read', path: 'src/a.test.ts' }] },
  { label: 'read-test-file-underscore', steps: [{ kind: 'read', path: 'go/internal/x_test.go' }] },
  { label: 'read-test-dir', steps: [{ kind: 'read', path: 'src/__tests__/a.ts' }] },
  { label: 'read-spec', steps: [{ kind: 'read', path: 'src/a.spec.ts' }] },
  { label: 'read-non-test', steps: [{ kind: 'read', path: 'src/a.ts' }] },

  // ── 综合场景 ──
  { label: 'realistic-tdd-cycle', steps: [
    { kind: 'read', path: 'src/a.test.ts' },
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'verify', status: 'failed' },
    { kind: 'modify', path: 'src/a.ts' },
    { kind: 'verify', status: 'passed' },
    { kind: 'modify', path: 'src/b.ts' },
  ] },
]

const out: Record<string, unknown> = {
  scenarios: scenarios.map(({ label, steps }) => {
    const t = new EvidenceTracker()
    for (const s of steps) {
      if (s.kind === 'read') t.trackFileRead(s.path)
      else if (s.kind === 'modify') t.trackFileModified(s.path)
      else t.trackVerification(ver(s.status, s.command))
    }
    const gate = t.getGateState()
    const st = t.getState()
    return {
      label,
      steps,
      gate,
      // evidenceState 的关键投影（sensorium 所需）
      evidence: {
        filesModified: st.filesModified.size,
        verifications: st.verifications.length,
      },
      hasVerificationDebt: t.hasVerificationDebt(),
    }
  }),
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`evidence oracle：场景 ${scenarios.length} 条 — sha256 ${sha.slice(0, 16)}`)
