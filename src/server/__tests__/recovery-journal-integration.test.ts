/**
 * 阶段 2（可靠续跑）——把台账接进真实 run 生命周期：
 *   beginRun / 工具事实 / settled 检查点 落账；看门狗自动续跑受闸门约束。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readdirSync, rmSync, writeFileSync, mkdirSync } from 'node:fs'
import { FileSessionPersistence } from '../session-persistence.js'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { AgentCallbacks } from '../../agent/loop-types.js'
import { RecoveryJournal, classifyToolRecovery } from '../recovery-journal.js'
import { RuntimeSessionManager, type ManagedAgent } from '../session-manager.js'

class ProbeAgent {
  getRecoverySnapshot() { return { messages: [], frozen: {} } }
  callbacks?: AgentCallbacks
  prompts: string[] = []
  /** 观测用转录水位（PLAN §4 第 1 步：只写不判）。 */
  transcriptWatermark = 3
  getTranscriptWatermark(): number { return this.transcriptWatermark }
  /** 恢复读面（PLAN §4 第 3 步）：结构化恢复注入的「结果未知」工具。 */
  historyRestore: { restored: number; error?: string; uncertainTools?: Array<{ id: string; name: string }> } = { restored: 1 }
  getHistoryRestore() { return this.historyRestore }
  private resolveRun?: () => void
  run(prompt: string, cb: AgentCallbacks): Promise<void> {
    this.prompts.push(prompt)
    this.callbacks = cb
    return new Promise<void>((res) => { this.resolveRun = res })
  }
  finish(): void { this.resolveRun?.() }
  abort(): void { this.callbacks?.onAbort(); this.resolveRun?.() }
  watchdogAbort(reason = 'watchdog:goal'): void { this.callbacks?.onAbort(reason); this.resolveRun?.() }
}

const waitUntil = async (cond: () => boolean, timeoutMs = 3000): Promise<void> => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (cond()) return
    await new Promise((r) => setTimeout(r, 5))
  }
  throw new Error(`条件在 ${timeoutMs}ms 内未成立`)
}
const settle = async (): Promise<void> => {
  await new Promise((r) => setTimeout(r, 5))
  await new Promise((r) => setImmediate(r))
  await new Promise((r) => setTimeout(r, 10))
}

/** 异步条件轮询（闸门决策走磁盘 I/O，固定 sleep 在并发负载下会 flaky）。 */
const waitFor = async (cond: () => Promise<boolean>, timeoutMs = 3000): Promise<void> => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await cond()) return
    await new Promise((r) => setTimeout(r, 10))
  }
  throw new Error(`条件在 ${timeoutMs}ms 内未成立`)
}

test('run lifecycle journals begin / tool facts / settled checkpoint', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-int-'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    defaultCwd: root,
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    await waitUntil(() => agent?.callbacks !== undefined)
    agent.callbacks!.onToolUse?.('t1', 'write', { path: 'x' })
    agent.callbacks!.onToolResult?.('t1', 'write', 'ok', false)   // 终态 → persisted
    agent.callbacks!.onToolUse?.('t2', 'read', { path: 'y' })     // 无终态 → unknown
    agent.callbacks!.onTurnComplete?.({ input_tokens: 7 }, 1, true)
    agent.finish()
    await waitUntil(() => manager.getSession(s.id)?.status !== 'running')
    await manager.waitForRunSettled(s.id)

    await waitFor(async () => (await journal.load(s.id))?.state === 'settled')
    const cp = await journal.load(s.id)
    assert.equal(cp?.state, 'settled', 'run 收尾应落 settled 检查点')
    assert.ok((cp?.watermark ?? 0) > 0, 'watermark 应为已落盘的事件 seq')
    assert.deepEqual(cp?.usage, { input_tokens: 7 }, '应记最近一次 turn_complete 用量')
    assert.equal(cp?.transcriptWatermark, 3, 'settled 检查点应记转录水位（只写不判，观测）')
    const { consumable, needsConfirmation } = classifyToolRecovery(cp!)
    assert.deepEqual(consumable.map(t => t.id), ['t1'], '已持久化工具可消费，不重跑')
    assert.deepEqual(needsConfirmation.map(t => t.id), ['t2'], '无终态工具必须转待确认')
  } finally {
    await manager.shutdownAll()
    // 台账写入是 fire-and-forget：删目录前等它落定，否则 mkdir 撞已删目录（unhandled rejection）。
    await journal.flush()
    rmSync(root, { recursive: true, force: true })
  }
})

test('工具失败（isError）算「终态已知」：不阻断自动恢复，也不留下无引用的依赖文件', async () => {
  // 回归（2026-09-27）：失败结果曾被记成 status='unknown' —— 于是
  //   ① 自动恢复闸门（needsConfirmation 看 status）被永久关掉，而主线没有任何 UI
  //      会告诉用户「因为某个工具失败，续跑不会再自动发生」；
  //   ② 上面 saveDependency 照常写下的那个依赖文件失去唯一引用，成为孤儿
  //      （journal 目录没有任何 GC，只会越积越多）。
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-tool-error-'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    defaultCwd: root,
  })
  try {
    const s = manager.createSession({ prompt: 'go' })

    await waitUntil(() => agent?.callbacks !== undefined)
    agent.callbacks!.onToolUse?.('e1', 'write', { path: 'x' })
    agent.callbacks!.onToolResult?.('e1', 'write', 'ENOENT: no such file or directory', true)   // 终态 = 失败
    agent.callbacks!.onTurnComplete?.({ input_tokens: 7 }, 1, true)
    agent.finish()
    await waitUntil(() => manager.getSession(s.id)?.status !== 'running')
    await manager.waitForRunSettled(s.id)

    const cp = await journal.load(s.id)
    const { consumable, needsConfirmation } = classifyToolRecovery(cp!)
    assert.deepEqual(needsConfirmation.map(t => t.id), [], '失败是已知终态，不是「结果未知」')
    assert.deepEqual(consumable.map(t => t.id), ['e1'], '失败终态同样已落盘 → 可消费（勿重放）')
    assert.equal(await journal.mayAutoResume(s.id), true, '一次工具失败不得永久关掉自动续跑')

    const refs = new Set<string>([cp!.snapshotRef ?? ''])
    for (const t of cp!.tools) for (const r of [t.resultRef, t.intentRef]) if (r) refs.add(r)
    const orphans = readdirSync(journal.dir(s.id))
      .filter(f => /^[a-f0-9-]{36}\.json$/.test(f) && !refs.has(f))
    assert.deepEqual(orphans, [], '依赖文件必须都有引用，不得留下孤儿')  } finally {
    await manager.shutdownAll()
    // 台账写入是 fire-and-forget：删目录前等它落定，否则 mkdir 撞已删目录（unhandled rejection）。
    await journal.flush()
    rmSync(root, { recursive: true, force: true })
  }
})



test('unwritable recovery directory prevents agent execution and reports storage failure', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-storage-'))
  const blocked = join(root, 'file')
  writeFileSync(blocked, 'not a directory')
  let builds = 0
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    defaultCwd: root, recoveryJournal: new RecoveryJournal(blocked),
    createAgent: () => { builds++; return new ProbeAgent() as unknown as ManagedAgent },
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    await waitUntil(() => manager.getSession(s.id)?.status !== 'running')
    await manager.waitForRunSettled(s.id)
    assert.equal(builds, 0)
    assert.equal(manager.getSession(s.id)?.status, 'failed')
    assert.ok(manager.getEvents(s.id, 0)!.events.some(e => e.data.code === 'recovery_storage_failed'))
  } finally { await manager.shutdownAll(); rmSync(root, { recursive: true, force: true }) }
})

for (const point of ['recordTool', 'commitSnapshot'] as const) {
  test(`journal ${point} failure stops the task without reporting completion`, async () => {
    const root = mkdtempSync(join(tmpdir(), 'rivet-rec-write-'))
    const journal = new RecoveryJournal(root)
    journal[point] = async () => { throw new Error('ENOSPC') }
    let agent!: ProbeAgent
    const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
      defaultCwd: root, recoveryJournal: journal,
      createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    })
    try {
      const s = manager.createSession({ prompt: 'go' })
      await waitUntil(() => !!agent?.callbacks)
      if (point === 'recordTool') agent.callbacks!.onToolUse('t1', 'write', {})
      else agent.finish()
      await waitUntil(() => manager.getSession(s.id)?.status !== 'running')
      await manager.waitForRunSettled(s.id)
      assert.equal(manager.getSession(s.id)?.status, 'failed')
      const events = manager.getEvents(s.id, 0)!.events
      assert.ok(events.some(e => e.data.code === 'recovery_storage_failed'))
      assert.equal(events.some(e => e.type === 'done' && e.data.status === 'completed'), false)
    } finally { await manager.shutdownAll(); rmSync(root, { recursive: true, force: true }) }
  })
}

test('watchdog cannot automatically continue without a complete model checkpoint', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-gate-'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    recoveryJournal: new RecoveryJournal(join(root, 'journal')),
    defaultCwd: root, watchdogContinueDelayMs: 0,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    await waitUntil(() => !!agent?.callbacks)
    agent.watchdogAbort()
    await waitUntil(() => manager.getEvents(s.id, 0)!.events.some(e => e.type === 'recovery_status'))
    assert.deepEqual(agent.prompts, ['go'])
  } finally { await manager.shutdownAll(); rmSync(root, { recursive: true, force: true }) }
})

for (const reason of ['unknown-tool-outcome', 'recovery-storage-failed']) {
  test(`watchdog does not resume after ${reason}`, async () => {
    const root = mkdtempSync(join(tmpdir(), 'rivet-rec-block-'))
    const journal = new RecoveryJournal(root)
    if (reason === 'recovery-storage-failed') journal.recoveryDecision = async () => { throw new Error('EACCES') }
    let agent!: ProbeAgent
    const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
      defaultCwd: root, recoveryJournal: journal, watchdogContinueDelayMs: 0,
      createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    })
    try {
      const s = manager.createSession({ prompt: 'go' })
      await waitUntil(() => !!agent?.callbacks)
      if (reason === 'unknown-tool-outcome') agent.callbacks!.onToolUse('send', 'external_send', {})
      agent.watchdogAbort()
      await waitUntil(() => manager.getEvents(s.id, 0)!.events.some(e => e.data.reason === reason))
      await settle()
      assert.deepEqual(agent.prompts, ['go'])
    } finally { await manager.shutdownAll(); rmSync(root, { recursive: true, force: true }) }
  })
}

test('cancelling during initial journal persistence never starts an agent later', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-cancel-'))
  const journal = new RecoveryJournal(root)
  const original = journal.beginRun.bind(journal)
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  journal.beginRun = async input => { await gate; return original(input) }
  let builds = 0
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    defaultCwd: root, recoveryJournal: journal,
    createAgent: () => { builds++; return new ProbeAgent() as unknown as ManagedAgent },
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    manager.abort(s.id)
    release()
    await manager.waitForRunSettled(s.id)
    assert.equal(builds, 0)
    assert.equal(manager.getSession(s.id)?.status, 'aborted')
  } finally { release(); await manager.shutdownAll(); rmSync(root, { recursive: true, force: true }) }
})

test('real event-store failure cannot publish a settled checkpoint or completed task', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-event-failure-'))
  const persistence = new FileSessionPersistence(join(root, 'events'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({ defaultCwd: root, persistence, recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
  })
  try {
    const s = manager.createSession({})
    mkdirSync(join(root, 'events', s.id, 'events.jsonl'), { recursive: true })
    manager.run(s.id, 'go')
    await waitUntil(() => !!agent?.callbacks)
    agent.callbacks!.onToolUse('write-1', 'write_file', {})
    await assert.rejects(agent.callbacks!.beforeToolExecute!('write-1', 'write_file', {}))
    await waitUntil(() => manager.getSession(s.id)?.status === 'failed')
    await manager.waitForRunSettled(s.id)
    const cp = await journal.load(s.id)
    assert.notEqual(cp?.state, 'settled')
    assert.equal(cp?.tools.some(tool => tool.status === 'persisted'), false)
    assert.equal(manager.getEvents(s.id, 0)!.events.some(e => e.type === 'done' && e.data.status === 'completed'), false)
  } finally { await manager.shutdownAll(); await persistence.flushAllAsync(); rmSync(root, { recursive: true, force: true }) }
})

test('cancelling an in-flight execution barrier never authorizes the tool', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-barrier-cancel-'))
  const persistence = new FileSessionPersistence(join(root, 'events'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  const original = persistence.flushThrough.bind(persistence)
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  persistence.flushThrough = async (...args) => { await gate; return original(...args) }
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({ defaultCwd: root, persistence, recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    await waitUntil(() => !!agent?.callbacks)
    const barrier = agent.callbacks!.beforeToolExecute!('send-1', 'send', {})
    const rejected = assert.rejects(barrier)
    manager.abort(s.id)
    release()
    await rejected
    await manager.waitForRunSettled(s.id)
    assert.equal(manager.getSession(s.id)?.status, 'aborted')
    assert.equal(await journal.mayAutoResume(s.id), false)
  } finally { release(); await manager.shutdownAll(); await persistence.flushAllAsync(); rmSync(root, { recursive: true, force: true }) }
})

test('a tool with unknown outcome blocks auto-continue and surfaces unknownTools', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-unknown-'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    defaultCwd: root,
    watchdogContinueDelayMs: 0,
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    await waitUntil(() => !!agent?.callbacks)
    agent.callbacks!.onToolUse?.('t9', 'write', { path: 'x' })   // 派发后无终态 → 结果未知
    agent.watchdogAbort('watchdog:goal')

    // 等闸门真正落事件再断言：v2 接线的 run 收尾要做持久化屏障 + commitSnapshot，
    // 固定 sleep 会跑在闸门之前（分支上 v1 收尾更轻，所以当时够用）。
    await waitUntil(() => manager.getEvents(s.id, 0)!.events.some(e => e.data.reason === 'unknown-tool-outcome'))
    await settle()  } finally {
    await manager.shutdownAll()
    // 台账写入是 fire-and-forget：删目录前等它落定，否则 mkdir 撞已删目录（unhandled rejection）。
    await journal.flush()
    rmSync(root, { recursive: true, force: true })
  }
})


test('resumeRun surfaces unknown tools (待确认) instead of blindly replaying', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-resume-'))
  const journal = new RecoveryJournal(join(root, 'journal'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    persistence: new FileSessionPersistence(join(root, 'events')),
    recoveryJournal: journal,
    createAgent: () => { agent = new ProbeAgent(); return agent as unknown as ManagedAgent },
    defaultCwd: root,
    defaultModelId: 'm',
  })
  try {
    const s = manager.createSession({})
    await journal.beginRun({ sessionId: s.id, runId: 'r', watermark: 0, model: 'm' })
    await journal.recordTool(s.id, { id: 't9', name: 'write', status: 'unknown' })

    // 只读决策面：不触发恢复即可看到分类。
    const decision = await manager.getRecoveryDecision(s.id)
    assert.deepEqual(decision?.needsConfirmation.map((t) => t.id), ['t9'])
    assert.deepEqual(decision?.consumable, [])

    const res = await manager.resumeRun(s.id)
    assert.equal(res.ok, true)
    assert.deepEqual(res.ok ? res.unknownTools : undefined, ['t9'], '显式续跑必须点名待确认工具')
    const ev = manager.getEvents(s.id, 0)!.events.find((e) => e.type === 'recovery_status')
    assert.equal(ev?.data.reason, 'unknown-tool-outcome')
    assert.deepEqual(ev?.data.unknownTools, ['t9'])

    // v2 接线：run() 先 await 台账 beginRun 再建 agent，agent 不是同步就绪的。
    await waitUntil(() => !!agent?.callbacks)
    agent.finish()
    await manager.waitForRunSettled(s.id)
  } finally {
    // 台账写入是 fire-and-forget：删目录前等它落定，否则 mkdir 撞已删目录（unhandled rejection）。
    await journal.flush()
    rmSync(root, { recursive: true, force: true })
  }
})

test('structured restore marks uncertain tools as tool_result events for the UI', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-rec-uncertain-'))
  let agent!: ProbeAgent
  const manager = new RuntimeSessionManager({
    createAgent: () => {
      agent = new ProbeAgent()
      agent.historyRestore = { restored: 1, uncertainTools: [{ id: 'c1', name: 'write_file' }] }
      return agent as unknown as ManagedAgent
    },
    defaultCwd: root,
  })
  try {
    const s = manager.createSession({ prompt: 'go' })
    const ev = manager.getEvents(s.id, 0)!.events.find((e) => e.type === 'tool_result' && e.data.uncertain === true)
    assert.ok(ev, '结构化恢复应把未知工具标成 tool_result 事件')
    assert.equal(ev?.data.id, 'c1')
    assert.equal(ev?.data.name, 'write_file')
    assert.equal(ev?.data.isError, true)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
