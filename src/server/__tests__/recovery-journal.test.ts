/**
 * 阶段 2（可靠续跑）——恢复台账：原子检查点、工具恢复事实、自动恢复闸门。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RecoveryJournal, classifyToolRecovery } from '../recovery-journal.js'

async function withJournal(fn: (journal: RecoveryJournal) => Promise<void>): Promise<void> {
  const root = mkdtempSync(join(tmpdir(), 'rivet-recovery-'))
  try { await fn(new RecoveryJournal(root)) } finally { rmSync(root, { recursive: true, force: true }) }
}

test('beginRun writes an atomic checkpoint and load round-trips it', async () => {
  await withJournal(async (journal) => {
    const started = await journal.beginRun({ sessionId: 's1', runId: 'run-1', watermark: 42, model: 'm', domain: 'auto' })
    assert.equal(started.runId, 'run-1')
    assert.equal(started.state, 'running')
    assert.equal(started.autoResumes, 0)

    const loaded = await journal.load('s1')
    assert.equal(loaded?.watermark, 42)
    assert.equal(loaded?.model, 'm')
    const files = readdirSync(journal.dir('s1'))
    assert.equal(files.filter(f => f.includes('.tmp')).length, 0, '原子写不留半成品')
    assert.ok(files.includes('checkpoint.json'))
  })
})

test('tool facts flip unknown→persisted; a persisted result bumps real progress', async () => {
  await withJournal(async (journal) => {
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 0 })
    await journal.recordTool('s1', { id: 't1', name: 'write', status: 'unknown' })
    await journal.recordTool('s1', { id: 't2', name: 'read', status: 'unknown' })
    await journal.recordTool('s1', { id: 't2', name: 'read', status: 'persisted', watermark: 7 })

    const cp = await journal.load('s1')
    assert.equal(cp?.tools.length, 2)
    assert.equal(cp?.progressWatermark, 7, '已持久化工具结果应抬高进展水位')
    const { consumable, needsConfirmation } = classifyToolRecovery(cp!)
    assert.deepEqual(consumable.map(t => t.id), ['t2'])
    assert.deepEqual(needsConfirmation.map(t => t.id), ['t1'], '无终态的工具必须转待确认，不能重放')
  })
})

test('checkpoint round-trips the observational transcript watermark', async () => {
  await withJournal(async (journal) => {
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 0 })
    assert.equal((await journal.load('s1'))?.transcriptWatermark, undefined)
    await journal.checkpoint('s1', { transcriptWatermark: 12 })
    assert.equal((await journal.load('s1'))?.transcriptWatermark, 12)
  })
})

test('checkpoint watermark never regresses', async () => {
  await withJournal(async (journal) => {
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 10 })
    await journal.checkpoint('s1', { watermark: 25, state: 'settled' })
    await journal.checkpoint('s1', { watermark: 12 })
    const cp = await journal.load('s1')
    assert.equal(cp?.watermark, 25)
    assert.equal(cp?.state, 'settled')
  })
})

test('auto-resume is allowed once, then blocked until new real progress', async () => {
  await withJournal(async (journal) => {
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 100 })
    await journal.commitSnapshot('s1', (await journal.load('s1'))!.watermark, { messages: [], frozen: {} })
    assert.equal(await journal.mayAutoResume('s1'), true, '首次自动恢复应允许')
    assert.equal(await journal.mayAutoResume('s1'), false, '无真实进展不得第二次自动恢复')
    // 普通事件（簿记/收尾）抬高 watermark，但不算进展。
    await journal.checkpoint('s1', { watermark: 200, state: 'settled' })
    assert.equal(await journal.mayAutoResume('s1'), false, '普通事件不算新进展')
    await journal.recordProgress('s1', 260)
    await journal.commitSnapshot('s1', (await journal.load('s1'))!.watermark, { messages: [], frozen: {} })
    assert.equal(await journal.mayAutoResume('s1'), true, '真实进展后允许新一次')
    assert.equal(await journal.mayAutoResume('s1'), false)
  })
})

test('a watchdog auto-resume keeps the same runId and the gate counter', async () => {
  await withJournal(async (journal) => {
    const first = await journal.beginRun({ sessionId: 's1', runId: 'run-1', watermark: 0 })
    await journal.commitSnapshot('s1', (await journal.load('s1'))!.watermark, { messages: [], frozen: {} })
    assert.equal(await journal.mayAutoResume('s1'), true)
    const second = await journal.beginRun({ sessionId: 's1', autoResume: true, watermark: 10 })
    assert.equal(second.runId, first.runId, '同一逻辑运行必须保留 runId')
    assert.equal(second.attempt, 2)
    assert.equal(await journal.mayAutoResume('s1'), false, '闸门计数必须跨自动续跑保留')
  })
})

test('load never throws on missing or corrupt checkpoints', async () => {
  await withJournal(async (journal) => {
    assert.equal(await journal.load('missing'), undefined)
    await journal.beginRun({ sessionId: 's2', runId: 'r', watermark: 1 })
    writeFileSync(join(journal.dir('s2'), 'checkpoint.json'), '{ this is not json')
    assert.equal(await journal.load('s2'), undefined)
    assert.equal(await journal.mayAutoResume('s2'), false, '损坏后闸门必须 fail-closed，而不是抛')
  })
})

test('unknown tool outcomes block recovery even when other tools made progress', async () => {
  await withJournal(async journal => {
    await journal.beginRun({ sessionId: 's', watermark: 0 })
    await journal.recordTool('s', { id: 'send', name: 'external_send', status: 'unknown' })
    await journal.recordProgress('s', 10)
    // 先落一份**完整快照**：让下面两条拒绝只能来自「工具结果未知」，而不是来自
    // 「没有快照」这条更早的闸门（否则用例标题说的东西根本没被验证）。
    await journal.commitSnapshot('s', 10, { messages: [], frozen: {} })
    assert.equal(await journal.mayAutoResume('s'), false)
    await assert.rejects(journal.beginRun({ sessionId: 's', watermark: 10, autoResume: true }))
    // `persisted` 在生产里不是直接记出来的：onToolResult 先 saveDependency 拿
    // resultRef 记 unknown，run 收尾的 commitSnapshot 再把带 resultRef 的工具提升为
    // persisted。所以这里必须走同一条路——直接写 status:'persisted' 却没有 resultRef
    // 是生产不可能出现的态（会让闸门永久 fail-closed，且没人能解释为什么）。
    const resultRef = await journal.saveDependency('s', { toolId: 'send', name: 'external_send', result: 'sent' })
    await journal.recordTool('s', { id: 'send', name: 'external_send', status: 'unknown', watermark: 11, resultRef })
    await journal.commitSnapshot('s', 10, { messages: [], frozen: {} })
    assert.equal(await journal.mayAutoResume('s'), true)
    const next = await journal.beginRun({ sessionId: 's', watermark: 11, autoResume: true })
    assert.equal(next.tools[0]?.status, 'persisted')
  })
})

test('an unknown tool outcome blocks auto-resume and lists the tool ids', async () => {
  await withJournal(async (journal) => {
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 0 })
    await journal.recordTool('s1', { id: 't1', name: 'write', status: 'unknown' })
    await journal.recordProgress('s1', 5)
    // 完整快照：让拒绝只能来自「工具结果未知」，而不是更早的有效性门。
    await journal.commitSnapshot('s1', 5, { messages: [], frozen: {} })
    const decision = await journal.recoveryDecision('s1')
    assert.equal(decision.allowed, false)
    assert.equal(decision.reason, 'unknown-tool-outcome')
    assert.deepEqual(decision.unknownTools, ['t1'], '必须点名待核实的工具')
    assert.equal(await journal.mayAutoResume('s1'), false)
    // 核实后（结果已确认并持久化）不再阻断——走产线同一条路：依赖 ref + commitSnapshot 提升。
    const resultRef = await journal.saveDependency('s1', { toolId: 't1', name: 'write', result: 'done' })
    await journal.recordTool('s1', { id: 't1', name: 'write', status: 'unknown', watermark: 6, resultRef })
    await journal.commitSnapshot('s1', 6, { messages: [], frozen: {} })
    assert.equal((await journal.recoveryDecision('s1')).allowed, true)
  })
})

test('a missing checkpoint reports no-checkpoint without throwing', async () => {
  await withJournal(async (journal) => {
    const decision = await journal.recoveryDecision('nobody')
    assert.equal(decision.allowed, false)
    assert.equal(decision.reason, 'no-checkpoint')
  })
})

test('inspect is read-only: it classifies tools without consuming the auto-resume budget', async () => {
  await withJournal(async (journal) => {
    assert.equal(await journal.inspect('nobody'), undefined)
    await journal.beginRun({ sessionId: 's1', runId: 'r', watermark: 0 })
    await journal.recordTool('s1', { id: 't1', name: 'write', status: 'unknown' })
    await journal.recordTool('s1', { id: 't2', name: 'read', status: 'persisted', watermark: 3 })

    const first = await journal.inspect('s1')
    assert.deepEqual(first?.needsConfirmation.map(t => t.id), ['t1'])
    assert.deepEqual(first?.consumable.map(t => t.id), ['t2'])

    // 读面不写盘：多次 inspect 后闸门仍按原样判定，预算未被消耗。
    await journal.inspect('s1')
    await journal.inspect('s1')
    assert.equal((await journal.load('s1'))?.autoResumes, 0)
    assert.equal(await journal.mayAutoResume('s1'), false, '未知工具仍阻断')
    assert.equal((await journal.load('s1'))?.autoResumes, 0)
  })
})
