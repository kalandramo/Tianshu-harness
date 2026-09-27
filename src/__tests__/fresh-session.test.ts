/**
 * /new 的会话准备层 —— createFreshSession / startFreshSession 的确定性断言。
 *
 * 断言面刻意落在"AgentLoop 整体重建之前"：重建是重型依赖（与
 * switchAgentSession 同构），不在单测里构造。这里锁三件在磁盘上可复核的事实——
 * 新 id 的格式、meta 的内容、旧会话的零改动。重建失败时的降级语义（返回
 * ok:false 而非抛错）也在此覆盖。
 */

import { test, beforeEach, afterEach } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, existsSync, writeFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { createFreshSession, startFreshSession } from '../agent/fresh-session.js'
import type { BootstrapContext } from '../bootstrap.js'
import { isValidSessionId } from '../validation.js'
import { SessionPersist } from '../agent/session-persist.js'

let dir: string
beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'rivet-fresh-sess-'))
  process.env.RIVET_SESSION_DIR = dir
})
afterEach(() => {
  rmSync(dir, { recursive: true, force: true })
  delete process.env.RIVET_SESSION_DIR
})

test('createFreshSession：id 合法 + meta 落 cwd + 不预建转录文件', () => {
  const { sessionId, persist } = createFreshSession(dir)

  assert.ok(isValidSessionId(sessionId), `id 必须过 assertValidSessionId 的格式闸，实得 ${sessionId}`)
  assert.equal(
    persist.loadMetadata()?.cwd, dir,
    'meta.cwd 必须写：缺字段时 /resume 列表看不见该会话，且跨 cwd 守卫从「拒绝」退化为「放行」',
  )
  assert.equal(
    existsSync(join(dir, `${sessionId}.jsonl`)), false,
    '新会话不该预建空转录文件——转录由首条消息落盘时创建',
  )
})

test('createFreshSession：两次调用产出不同 id', () => {
  assert.notEqual(createFreshSession(dir).sessionId, createFreshSession(dir).sessionId)
})

test('createFreshSession：刻意为空 model —— 防 /new 踩 resume 亲和的 fail-closed', () => {
  const { persist } = createFreshSession(dir)
  assert.equal(
    persist.loadMetadata()?.model, undefined,
    'meta.model 一旦写上就会让 switchAgentSession 走 resolveProviderForModel 分支，'
    + '模型不在配置中时报「请开新会话继续」——用户正在开新会话，自相矛盾',
  )
})

test('startFreshSession：记下旧会话 id，且旧会话转录逐字节不变', () => {
  const old = new SessionPersist('old-session', dir)
  old.initMetadata({ cwd: dir })
  const oldJsonl = join(dir, 'old-session.jsonl')
  writeFileSync(oldJsonl, 'ORIGINAL-CONTENT\n')

  // 极简 ctx：AgentLoop 重建必然失败——这正是要覆盖的降级路径（返回 ok:false
  // 而非把异常抛给 slash handler）。不断言 ok：断言磁盘事实，重建容错性变化时
  // 本测试不脆断。
  const ctx = { sessionId: 'old-session', cwd: dir } as unknown as BootstrapContext
  const res = startFreshSession(ctx)

  assert.equal(res.previousSessionId, 'old-session')
  assert.ok(res.sessionId && isValidSessionId(res.sessionId), `新 id 必须合法，实得 ${res.sessionId}`)
  assert.notEqual(res.sessionId, 'old-session')
  assert.match(res.error ?? '', /切换新会话失败|无法创建新会话/)
  assert.equal(
    readFileSync(oldJsonl, 'utf-8'), 'ORIGINAL-CONTENT\n',
    '/new 不得改动旧会话转录——旧上下文是存档，可 /resume 回访',
  )
  const fresh = new SessionPersist(res.sessionId!, dir)
  assert.equal(fresh.loadMetadata()?.cwd, dir, '新会话 meta 已就位，id 可被 /resume 找回')
})
