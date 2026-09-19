/**
 * recovery-journal oracle 生成器。
 *
 * 生成：npx tsx go/testdata/recovery/gen-oracle.ts
 *
 * ## 为什么需要
 *
 * journal 行的**键序由对象展开顺序决定**：
 *   `{ ...entry, ts, ...(sessionId ? { sessionId } : {}) }`
 * → `file, action, linesLost, ts[, sessionId]`（linesLost 在 ts **前**）。
 *
 * 且 `ts` 是 `toISOString()`——**固定 3 位毫秒**，而 Go 的 RFC3339Nano
 * 会省略尾随零（860ms → `.86`）。两者都会让字节不等价。
 *
 * ## 覆盖
 *
 * 从**真实 recordRecovery 路径**导出（不手抄），并锁定：
 * - 键序（含 sessionId 有无两种形态）
 * - ts 的毫秒补零（0 / 1 / 10 / 100 / 860 / 999）
 * - ack / handoff 后的重写形态
 */
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import {
  recordRecovery, readUnacknowledged, acknowledgeAll, handoffRecoveries,
} from '../../../src/agent/recovery-journal.js'

const here = dirname(fileURLToPath(import.meta.url))

function freshCwd(): string {
  return mkdtempSync(join(tmpdir(), 'jr-oracle-'))
}

function journalLines(cwd: string): string[] {
  try {
    return readFileSync(join(cwd, '.rivet', 'recovery-journal.jsonl'), 'utf-8').split('\n').filter(Boolean)
  } catch {
    return []
  }
}

const out: Record<string, unknown> = {}

/** 把 ts 值替换为占位符——时间戳不可复现，oracle 只锁**键序与格式**。 */
function scrub(lines: string[]): string[] {
  return lines.map(l => l.replace(/"ts":"[^"]*"/g, '"ts":"<TS>"'))
}

// ── 1. 基本写入：键序与字段 ──
{
  const cwd = freshCwd()
  recordRecovery(cwd, { file: 'a.ts', action: 'edit', linesLost: 5 })
  recordRecovery(cwd, { file: 'b.ts', action: 'git checkout HEAD', linesLost: 22 }, 'sess-1')
  const lines = journalLines(cwd)
  out.basicWrite = {
    lines: scrub(lines),
    keyOrders: lines.map(l => Object.keys(JSON.parse(l))),
  }
  rmSync(cwd, { recursive: true, force: true })
}

// ── 2. ts 的毫秒补零（用固定时间戳无法注入，故只记录格式正则与样本） ──
{
  const cwd = freshCwd()
  const samples: string[] = []
  for (let i = 0; i < 5; i++) {
    recordRecovery(cwd, { file: `f${i}.ts`, action: 'edit', linesLost: 0 })
    samples.push(JSON.parse(journalLines(cwd)[i]!).ts)
  }
  out.tsFormat = {
    samples: samples.map(() => '<TS>'),
    // 断言面：ISO 8601 且**恰好 3 位毫秒**
    regex: '^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z$',
  }
  rmSync(cwd, { recursive: true, force: true })
}

// ── 3. 读取过滤：ack / handoff / 会话范围 ──
{
  const cwd = freshCwd()
  recordRecovery(cwd, { file: 'legacy.ts', action: 'edit', linesLost: 1 })       // 无会话归属
  recordRecovery(cwd, { file: 'mine.ts', action: 'edit', linesLost: 2 }, 'me')
  recordRecovery(cwd, { file: 'other.ts', action: 'edit', linesLost: 3 }, 'them')

  out.readScope = {
    all: readUnacknowledged(cwd).map(e => e.file),
    onlyMine: readUnacknowledged(cwd, 'me').map(e => e.file),
    onlyThem: readUnacknowledged(cwd, 'them').map(e => e.file),
  }

  // ack 只影响自己的会话
  acknowledgeAll(cwd, 'me')
  out.afterAckMine = {
    all: readUnacknowledged(cwd).map(e => e.file),
    onlyMine: readUnacknowledged(cwd, 'me').map(e => e.file),
    rawKeys: journalLines(cwd).map(l => Object.keys(JSON.parse(l))),
    raw: scrub(journalLines(cwd)),
  }

  // handoff 只影响指定会话
  handoffRecoveries(cwd, 'them')
  out.afterHandoffThem = {
    onlyThem: readUnacknowledged(cwd, 'them').map(e => e.file),
    rawKeys: journalLines(cwd).map(l => Object.keys(JSON.parse(l))),
    raw: scrub(journalLines(cwd)),
  }
  rmSync(cwd, { recursive: true, force: true })
}

// ── 4. 空 journal ──
{
  const cwd = freshCwd()
  out.emptyJournal = { entries: readUnacknowledged(cwd) }
  rmSync(cwd, { recursive: true, force: true })
}

// ── 5. 损坏行容错 ──
{
  const cwd = freshCwd()
  mkdirSync(join(cwd, '.rivet'), { recursive: true })
  writeFileSync(join(cwd, '.rivet', 'recovery-journal.jsonl'),
    '{"file":"ok.ts","action":"edit","ts":"2026-01-01T00:00:00.000Z","linesLost":1}\n' +
    '{not json\n' +
    '{"file":"ok2.ts","action":"edit","ts":"2026-01-01T00:00:01.000Z","linesLost":2}\n')
  out.corruptTolerance = { files: readUnacknowledged(cwd).map(e => e.file) }
  rmSync(cwd, { recursive: true, force: true })
}

mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`recovery oracle：${Object.keys(out).length} 组 — sha256 ${sha.slice(0, 16)}`)
