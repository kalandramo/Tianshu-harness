/**
 * 阶段 2（恢复协议）——请求级幂等台账。
 *
 * 契约：确认（receipt）先于执行落盘；同一 (session, requestId) 重发永远返回
 * 已有回执、绝不第二次执行；requestId 换了载荷要拒绝，避免「同名不同请求」
 * 被静默吞掉。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { RunLedger } from '../run-ledger.js'

async function withLedger(fn: (ledger: RunLedger, root: string) => Promise<void>): Promise<void> {
  const root = mkdtempSync(join(tmpdir(), 'rivet-run-ledger-'))
  try {
    return await fn(new RunLedger(root), root)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
}

test('duplicate requestId returns the existing receipt and never runs twice', async () => {
  await withLedger(async (ledger) => {
    let starts = 0
    const first = await ledger.accept('s1', 'req-1', { prompt: 'hi' }, () => { starts++; return true })
    assert.equal(first.duplicate, false)
    assert.equal(first.state, 'running')
    assert.equal(starts, 1)

    const second = await ledger.accept('s1', 'req-1', { prompt: 'hi' }, () => { starts++; return true })
    assert.equal(second.duplicate, true)
    assert.equal(second.runId, first.runId)
    assert.equal(starts, 1, '重发不得创建第二次执行')
  })
})

test('same requestId with a different payload is rejected, not silently accepted', async () => {
  await withLedger(async (ledger) => {
    await ledger.accept('s1', 'req-1', { prompt: 'first' }, () => true)
    await assert.rejects(
      ledger.accept('s1', 'req-1', { prompt: 'different' }, () => true),
      /different request/,
    )
  })
})

test('a start that refuses (busy) is recorded as rejected', async () => {
  await withLedger(async (ledger) => {
    const receipt = await ledger.accept('s1', 'req-2', { prompt: 'hi' }, () => false)
    assert.equal(receipt.state, 'rejected')
    assert.equal(receipt.duplicate, false)
    const retry = await ledger.accept('s1', 'req-2', { prompt: 'hi' }, () => { throw new Error('must not run again') })
    assert.equal(retry.duplicate, true)
    assert.equal(retry.state, 'rejected')
  })
})

test('invalid requestId is refused before touching disk', async () => {
  await withLedger(async (ledger) => {
    await assert.rejects(ledger.accept('s1', '../escape', {}, () => true), /Invalid requestId/)
  })
})

test('a previous process receipt becomes needs_attention and is never restarted', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-receipt-restart-'))
  try {
    const first = new RunLedger(root)
    const receipt = await first.accept('s', 'request-1', { prompt: 'go' }, () => true)
    const second = new RunLedger(root)
    assert.equal((await second.get('s', 'request-1'))?.state, 'needs_attention')
    let starts = 0
    const duplicate = await second.accept('s', 'request-1', { prompt: 'go' }, () => { starts++; return true })
    assert.equal(duplicate.runId, receipt.runId)
    assert.equal(duplicate.state, 'needs_attention')
    assert.equal(starts, 0)
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('terminal receipt waits for acceptance and retains the original identity', async () => {
  const root = mkdtempSync(join(tmpdir(), 'rivet-receipt-terminal-'))
  try {
    const ledger = new RunLedger(root)
    const receipt = await ledger.accept('s', 'request-1', {}, () => true)
    await ledger.settle('s', 'request-1', 'aborted')
    const terminal = await ledger.get('s', 'request-1')
    assert.equal(terminal?.state, 'settled')
    assert.equal(terminal?.outcome, 'aborted')
    assert.equal(terminal?.runId, receipt.runId)
    assert.equal(terminal?.attemptId, receipt.attemptId)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
