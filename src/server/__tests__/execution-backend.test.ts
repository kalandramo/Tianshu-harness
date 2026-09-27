/**
 * 阶段 2（隔离灰度）——执行后端选择缝。
 *
 * 契约：默认必须保持进程内（灰度默认关闭）；请求隔离时若闭源适配器缺席要
 * 静默回退，绝不把「灰度开关」变成「启动开关」。公开仓（无 src/pro/）同样要过。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { AgentFactory, ManagedAgent } from '../session-manager.js'
import { EXECUTION_BACKEND_ENV, inProcessBackend, resolveExecutionBackend } from '../execution-backend.js'

const makeAgent = (): ManagedAgent => ({ run: async () => {}, abort: () => {} }) as unknown as ManagedAgent

test('default backend is in-process; opt-in never throws even without the closed adapter', async () => {
  const factory: AgentFactory = () => makeAgent()
  const previous = process.env[EXECUTION_BACKEND_ENV]
  try {
    delete process.env[EXECUTION_BACKEND_ENV]
    const byDefault = await resolveExecutionBackend(factory)
    assert.equal(byDefault.kind, 'in-process')
    await byDefault.close()

    process.env[EXECUTION_BACKEND_ENV] = 'isolated'
    const byEnv = await resolveExecutionBackend(factory)
    // 本仓（闭源产物在）→ isolated；公开仓（产物缺席）→ 回退 in-process。
    // 两种都合法，关键是「不抛错、不因开关而启动失败」。
    assert.ok(byEnv.kind === 'isolated' || byEnv.kind === 'in-process')
    await byEnv.close()
  } finally {
    if (previous === undefined) delete process.env[EXECUTION_BACKEND_ENV]
    else process.env[EXECUTION_BACKEND_ENV] = previous
  }
})

test('explicit kind overrides the environment', async () => {
  const previous = process.env[EXECUTION_BACKEND_ENV]
  process.env[EXECUTION_BACKEND_ENV] = 'isolated'
  try {
    const backend = await resolveExecutionBackend(() => makeAgent(), { kind: 'in-process' })
    assert.equal(backend.kind, 'in-process')
    await backend.close()
  } finally {
    if (previous === undefined) delete process.env[EXECUTION_BACKEND_ENV]
    else process.env[EXECUTION_BACKEND_ENV] = previous
  }
})

test('in-process backend tracks created agents and shuts them down on close', async () => {
  const shut: string[] = []
  const backend = inProcessBackend(async (_cwd, sessionId) => ({
    run: async () => {},
    shutdown: () => { shut.push(sessionId ?? '?') },
  } as unknown as ManagedAgent))
  await backend.createAgent('/tmp', 's1')
  await backend.createAgent('/tmp', 's2')
  await backend.close()
  assert.deepEqual(shut.sort(), ['s1', 's2'])
})

test('released agents leave backend ownership and shutdown is shared while pending', async () => {
  let calls = 0
  let finish!: () => void
  const backend = inProcessBackend(() => ({
    shutdown: () => { calls++; return new Promise<void>(resolve => { finish = resolve }) },
  } as ManagedAgent))
  const agent = await backend.createAgent('/tmp', 'released')
  const first = agent.shutdown!()
  const second = agent.shutdown!()
  assert.equal(first, second)
  await Promise.resolve()
  assert.equal(calls, 1)
  finish()
  await first
  // If the backend still owns this agent, close will call this replacement.
  agent.shutdown = () => { throw new Error('retained released agent') }
  await backend.close()
  assert.equal(calls, 1)
})
