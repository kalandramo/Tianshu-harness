/**
 * 阶段 4 全局推送通道——GET /events 路由：鉴权、hello/health 首帧、总线提示转发、
 * 心跳、断连清理；以及 RuntimeSessionManager 的失效提示触发点。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { startServer } from '../index.js'
import { ServerEventBus } from '../server-event-bus.js'
import { buildServerEventsRoute } from '../server-events-route.js'
import { RuntimeSessionManager, type ManagedAgent } from '../session-manager.js'
import type { AgentCallbacks } from '../../agent/loop-types.js'
import type { Artifact } from '../../artifact/types.js'
import type { OaiMessage } from '../../api/oai-types.js'
import type { HealthBody } from '../health-route.js'
import { RUNTIME_CAPABILITIES } from '../protocol.js'

const TOKEN = 'events-token'

const healthBody = (): HealthBody => ({
  instanceId: 'test-instance', readiness: 'ready',
  ok: true, version: '0.0.0-test', protocolVersion: 1, capabilities: RUNTIME_CAPABILITIES,
  uptimeMs: 1, sessionCount: 2, runningCount: 1, registryOk: true, configured: true,
})

interface Frame { event: string; data: Record<string, unknown> }

/** 读 SSE 帧直到收够 `want` 帧或超时；返回帧与中止句柄。 */
async function readFrames(port: number, want: number, opts: { token?: string; timeoutMs?: number } = {}): Promise<{ status: number; frames: Frame[]; abort: () => void }> {
  const ac = new AbortController()
  const res = await fetch(`http://127.0.0.1:${port}/events`, {
    headers: opts.token ? { Authorization: `Bearer ${opts.token}` } : {},
    signal: ac.signal,
  })
  if (!res.ok || !res.body) return { status: res.status, frames: [], abort: () => ac.abort() }
  const frames: Frame[] = []
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  const deadline = Date.now() + (opts.timeoutMs ?? 5000)
  while (frames.length < want && Date.now() < deadline) {
    const { done, value } = await Promise.race([
      reader.read(),
      new Promise<{ done: true; value: undefined }>((resolve) => setTimeout(() => resolve({ done: true, value: undefined }), Math.max(1, deadline - Date.now()))),
    ])
    if (done || !value) break
    buf += decoder.decode(value, { stream: true })
    let idx: number
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const raw = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      let event = ''
      const data: string[] = []
      for (const line of raw.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
      }
      if (data.length) frames.push({ event, data: JSON.parse(data.join('\n')) })
    }
  }
  return { status: res.status, frames, abort: () => { try { reader.cancel().catch(() => {}) } catch { /* ignore */ } ac.abort() } }
}

async function withServer(
  bus: ServerEventBus,
  heartbeatMs: number,
  fn: (port: number) => Promise<void>,
): Promise<void> {
  const routes = buildServerEventsRoute(bus, TOKEN, { healthSnapshot: healthBody, heartbeatMs })
  const server = await startServer(0, routes, TOKEN)
  try {
    await fn(server.port)
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()))
  }
}

test('GET /events：无 token 401；有 token 首帧 hello + health（与 /health 同体）', async () => {
  const bus = new ServerEventBus({ coalesceMs: 0 })
  await withServer(bus, 60_000, async (port) => {
    const denied = await readFrames(port, 0)
    assert.equal(denied.status, 401)
    const { status, frames, abort } = await readFrames(port, 2, { token: TOKEN })
    try {
      assert.equal(status, 200)
      assert.equal(frames[0]!.event, 'hello')
      assert.equal(frames[0]!.data.kind, 'hello')
      assert.equal(frames[0]!.data.heartbeatMs, 60_000)
      assert.equal(frames[1]!.event, 'health')
      assert.deepEqual(frames[1]!.data.data, healthBody())
      assert.equal(bus.listenerCount(), 1, '建连即订阅总线')
    } finally {
      abort()
    }
  })
  bus.close()
})

test('总线提示转发为同名帧；客户端断开后订阅被清理', async () => {
  const bus = new ServerEventBus({ coalesceMs: 0 })
  await withServer(bus, 60_000, async (port) => {
    const ac = new AbortController()
    const res = await fetch(`http://127.0.0.1:${port}/events`, { headers: { Authorization: `Bearer ${TOKEN}` }, signal: ac.signal })
    const reader = res.body!.getReader()
    const decoder = new TextDecoder()
    let buf = ''
    const readUntil = async (needle: string) => {
      const deadline = Date.now() + 5000
      while (!buf.includes(needle) && Date.now() < deadline) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
      }
      return buf.includes(needle)
    }
    assert.ok(await readUntil('event: health'))
    assert.equal(bus.listenerCount(), 1)
    bus.publish('sessions_changed', 'record')
    bus.publish('tasks_changed', 'created')
    assert.ok(await readUntil('event: tasks_changed'), '任务提示帧应到达')
    assert.match(buf, /event: sessions_changed\ndata: \{"kind":"sessions_changed","ts":\d+,"count":1,"reason":"record"\}/)
    assert.match(buf, /event: tasks_changed\ndata: \{"kind":"tasks_changed","ts":\d+,"count":1,"reason":"created"\}/)
    reader.cancel().catch(() => {})
    ac.abort()
    // 断连 → res 'close' → cleanup → 退订
    const deadline = Date.now() + 5000
    while (bus.listenerCount() > 0 && Date.now() < deadline) await new Promise((r) => setTimeout(r, 20))
    assert.equal(bus.listenerCount(), 0, '断连后总线订阅应清理')
  })
  bus.close()
})

test('心跳按 heartbeatMs 周期发 health 帧（下限 250ms）', async () => {
  const bus = new ServerEventBus({ coalesceMs: 0 })
  await withServer(bus, 100, async (port) => {
    // hello + 首帧 health + ≥2 次心跳
    const { frames, abort } = await readFrames(port, 4, { token: TOKEN, timeoutMs: 3000 })
    try {
      const healths = frames.filter((f) => f.event === 'health')
      assert.ok(healths.length >= 3, `应收到首帧 + 至少两次心跳，实际 ${healths.length}`)
      for (const h of healths) assert.deepEqual(h.data.data, healthBody())
    } finally {
      abort()
    }
  })
  bus.close()
})

// ── RuntimeSessionManager 触发点 ────────────────────────────────────

class FakeAgent implements ManagedAgent {
  run(_p: string, _cb: AgentCallbacks) { return Promise.resolve() }
  abort() {}
  listArtifacts() { return [] as Artifact[] }
  readArtifact(_id: string) { return Promise.resolve(null) }
  getMessages(): OaiMessage[] { return [] }
  replaceMessages(_msgs: OaiMessage[]) {}
  rewindToMessages(_msgs: OaiMessage[]) {}
}

test('manager：创建 / 改名 / 归档 / 删除 会话都触发 onSessionsChanged（无持久化亦然）', () => {
  const reasons: string[] = []
  const manager = new RuntimeSessionManager({
    createAgent: () => new FakeAgent(),
    defaultCwd: '/tmp/work',
    onSessionsChanged: (reason) => reasons.push(reason),
  })
  const s = manager.createSession({})
  assert.ok(reasons.includes('record'), `createSession 应触发 record，实际 ${JSON.stringify(reasons)}`)
  reasons.length = 0
  assert.equal(manager.setTitle(s.id, '新标题'), true)
  assert.ok(reasons.includes('record'), 'setTitle 应触发')
  reasons.length = 0
  assert.equal(manager.archiveSession(s.id), true)
  assert.ok(reasons.includes('record'), 'archiveSession 应触发')
  reasons.length = 0
  assert.equal(manager.deleteSession(s.id).ok, true)
  assert.ok(reasons.includes('delete'), `deleteSession 应触发 delete，实际 ${JSON.stringify(reasons)}`)
})

test('manager：回调抛错不影响会话生命周期', () => {
  const manager = new RuntimeSessionManager({
    createAgent: () => new FakeAgent(),
    defaultCwd: '/tmp/work',
    onSessionsChanged: () => { throw new Error('observer down') },
  })
  const s = manager.createSession({})
  assert.equal(manager.setTitle(s.id, 't'), true)
  assert.equal(manager.listAllSessions().length, 1)
})
