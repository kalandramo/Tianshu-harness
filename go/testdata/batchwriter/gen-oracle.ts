/**
 * SessionBatchWriter oracle 生成器。
 *
 * 生成：npx tsx go/testdata/batchwriter/gen-oracle.ts
 *
 * ## 覆盖
 *
 * 对账 src/agent/session-batch-writer.ts（129 行）的核心语义：
 * - **首行同步落盘**（新会话文件立即存在）
 * - **mergePending**（未 flush 的行对进程内读者可见）
 * - **flush 屏障**（pending 排空成一个 zstd 帧）
 * - **legacy 纯文本迁移**（首次写入时备份 + 转码）
 * - 已压缩文件不重复迁移
 *
 * ## 可复现性
 *
 * 临时路径不进 golden（只记录「文件内容」与布尔事实）。
 * 帧字节因压缩器实现差异**不进 golden**——只记录解码后的文本。
 */
import { mkdtempSync, readFileSync, existsSync, writeFileSync, readdirSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { SessionBatchWriter } from '../../../src/agent/session-batch-writer.js'
import { decodeTranscriptText, isZstdFrameStream } from '../../../src/agent/session-transcript-codec.js'

const here = dirname(fileURLToPath(import.meta.url))

type Op =
  | { op: 'enqueue'; line: string }
  | { op: 'flush' }
  | { op: 'mergePending'; onDisk: string }

type Case = {
  note: string
  /** 预置的文件内容（legacy 场景用）*/
  preexisting?: string
  ops: Op[]
}

const cases: Record<string, Case> = {
  firstLineSyncFlush: {
    note: '**首行同步落盘**——新会话文件立即存在',
    ops: [{ op: 'enqueue', line: 'line1\n' }],
  },

  secondLineQueued: {
    note: '第二行排队（不 flush）——文件仍是首行内容',
    ops: [
      { op: 'enqueue', line: 'line1\n' },
      { op: 'enqueue', line: 'line2\n' },
    ],
  },

  mergePendingVisible: {
    note: '**未 flush 的行对进程内读者可见**',
    ops: [
      { op: 'enqueue', line: 'line1\n' },
      { op: 'enqueue', line: 'line2\n' },
      { op: 'mergePending', onDisk: 'ONDISK\n' },
    ],
  },

  mergePendingEmpty: {
    note: 'pending 为空时 mergePending 原样返回磁盘内容',
    ops: [{ op: 'mergePending', onDisk: 'ONDISK\n' }],
  },

  flushDrainsBatch: {
    note: '**flush 把 pending 排空成一个帧**',
    ops: [
      { op: 'enqueue', line: 'line1\n' },
      { op: 'enqueue', line: 'line2\n' },
      { op: 'flush' },
    ],
  },

  flushAfterFirstLine: {
    note: '首行已同步落盘后再 flush（不重复写）',
    ops: [
      { op: 'enqueue', line: 'line1\n' },
      { op: 'flush' },
    ],
  },

  multiBatch: {
    note: '多批次 flush（多个帧）',
    ops: [
      { op: 'enqueue', line: 'a\n' },
      { op: 'enqueue', line: 'b\n' },
      { op: 'flush' },
      { op: 'enqueue', line: 'c\n' },
      { op: 'flush' },
    ],
  },

  emptyLine: {
    note: '空行（line 为空串）——不产帧',
    ops: [{ op: 'enqueue', line: '' }],
  },

  legacyMigration: {
    note: '**legacy 纯文本首次写入时被转码为 zstd 帧 + 备份**',
    preexisting: 'legacy-line-1\nlegacy-line-2\n',
    ops: [
      { op: 'enqueue', line: 'new-line\n' },
      { op: 'flush' },
    ],
  },

  alreadyZstdNoMigration: {
    note: '已压缩文件不重复迁移（无备份产生）',
    preexisting: '', // 占位，实际由生成器先写一个帧
    ops: [{ op: 'flush' }],
  },
}

const results: Record<string, unknown> = {}

for (const [name, c] of Object.entries(cases)) {
  const dir = mkdtempSync(join(tmpdir(), 'bw-'))
  const fp = join(dir, 's.jsonl')
  const backupDir = join(dir, 'backups')

  // 预置文件
  if (c.preexisting !== undefined) {
    if (name === 'alreadyZstdNoMigration') {
      // 先写一个真实帧
      const { encodeBatch } = await import('../../../src/agent/session-transcript-codec.js')
      writeFileSync(fp, encodeBatch('already-compressed\n'))
    } else if (c.preexisting.length > 0) {
      writeFileSync(fp, c.preexisting)
    }
  }

  const w = new SessionBatchWriter(fp, () => backupDir)
  const observations: unknown[] = []

  for (const op of c.ops) {
    switch (op.op) {
      case 'enqueue':
        w.enqueueLine(op.line)
        observations.push({
          after: 'enqueue',
          fileExists: existsSync(fp),
          isZstd: existsSync(fp) ? isZstdFrameStream(readFileSync(fp)) : false,
          decoded: existsSync(fp) ? decodeTranscriptText(readFileSync(fp)) : null,
        })
        break
      case 'flush':
        await w.flush()
        observations.push({
          after: 'flush',
          fileExists: existsSync(fp),
          decoded: decodeTranscriptText(readFileSync(fp)),
        })
        break
      case 'mergePending':
        observations.push({
          after: 'mergePending',
          merged: w.mergePending(op.onDisk),
        })
        break
    }
  }

  // 最终态
  const finalDecoded = existsSync(fp) ? decodeTranscriptText(readFileSync(fp)) : null
  const backups = existsSync(backupDir) ? readdirSync(backupDir).sort() : []

  results[name] = {
    note: c.note,
    preexisting: c.preexisting ?? null,
    observations,
    finalDecoded,
    backupFiles: backups,
  }
}

const out = { cases: results }
mkdirSync(here, { recursive: true })
writeFileSync(join(here, 'oracle.json'), JSON.stringify(out, null, 2) + '\n')
const sha = createHash('sha256').update(JSON.stringify(out)).digest('hex')
console.error(`batchwriter oracle：${Object.keys(cases).length} 用例 — sha256 ${sha.slice(0, 16)}`)
